package tracing

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	config "github.com/cnsync/gateway/api/gateway/config/v1"
	v1 "github.com/cnsync/gateway/api/gateway/middleware/tracing/v1"
	"github.com/cnsync/gateway/middleware"
	"github.com/cnsync/kratos"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// defaultTimeout 定义了默认的超时时间，这里设置为 10 秒
const defaultTimeout = time.Duration(10 * time.Second)

// defaultServiceName 定义了默认的服务名，这里设置为 "gateway"
const defaultServiceName = "gateway"

// defaultTracerName 定义了默认的跟踪器名，这里设置为 "gateway"
const defaultTracerName = "gateway"

// globaltp 是一个结构体，包含一个 trace.TracerProvider 类型的 provider 字段和一个 sync.Once 类型的 initOnce 字段
var globaltp = &struct {
	provider trace.TracerProvider
	initOnce sync.Once
}{}

// 包初始化时注册 tracing 中间件
func init() {
	middleware.Register("tracing", Middleware)
}

// Middleware 函数根据传入的配置对象 c 创建一个中间件实例
func Middleware(c *config.Middleware) (middleware.Middleware, error) {
	// 初始化一个 v1.Tracing 类型的指针 options，用于存储中间件的配置选项
	options := &v1.Tracing{}
	// 检查配置对象 c 的 Options 字段是否不为 nil
	if c.Options != nil {
		// 将配置对象 c 的 Options 字段解析到 options 中
		if err := anypb.UnmarshalTo(c.Options, options, proto.UnmarshalOptions{Merge: true}); err != nil {
			// 如果解析失败，返回 nil 和错误信息
			return nil, err
		}
	}
	// 检查全局 tracerProvider 是否为 nil
	if globaltp.provider == nil {
		// 使用 sync.Once 保证 tracerProvider 只初始化一次
		globaltp.initOnce.Do(func() {
			// 调用 newTracerProvider 函数创建一个 tracerProvider 实例
			globaltp.provider = newTracerProvider(context.Background(), options)
			// 创建一个 CompositeTextMapPropagator 实例，用于在 HTTP 请求头中传播跟踪信息
			propagator := propagation.NewCompositeTextMapPropagator(propagation.Baggage{}, propagation.TraceContext{})
			// 设置全局的 TracerProvider
			otel.SetTracerProvider(globaltp.provider)
			// 设置全局的 TextMapPropagator
			otel.SetTextMapPropagator(propagator)
		})
	}
	// 获取一个默认的 tracer 实例
	tracer := otel.Tracer(defaultTracerName)
	// 返回一个函数，该函数接受一个 http.RoundTripper 并返回一个新的 http.RoundTripper
	return func(next http.RoundTripper) http.RoundTripper {
		// 返回一个 RoundTripperFunc，它是 http.RoundTripper 的一个实现
		return middleware.RoundTripperFunc(func(req *http.Request) (reply *http.Response, err error) {
			// 从请求中获取上下文，并创建一个新的 span
			ctx, span := tracer.Start(
				req.Context(),
				fmt.Sprintf("%s %s", req.Method, req.URL.Path),
				trace.WithSpanKind(trace.SpanKindClient),
			)
			// 设置 span 的属性，包括 HTTP 方法、目标 URL 和客户端 IP
			span.SetAttributes(
				semconv.HTTPMethodKey.String(req.Method),
				semconv.HTTPTargetKey.String(req.URL.Path),
				semconv.NetPeerIPKey.String(req.RemoteAddr),
			)
			// 创建一个 HeaderCarrier，用于在请求头中传播跟踪信息
			car := propagation.HeaderCarrier(req.Header)
			// 注入跟踪信息到请求头中
			otel.GetTextMapPropagator().Inject(ctx, car)
			// 使用 defer 确保在函数返回时执行以下操作
			defer func() {
				// 如果发生错误，记录错误并设置 span 的状态为错误
				if err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, err.Error())
				} else {
					// 如果没有错误，设置 span 的状态为成功
					span.SetStatus(codes.Ok, "OK")
				}
				// 如果有响应，设置响应的 HTTP 状态码为 span 的属性
				if reply != nil {
					span.SetAttributes(semconv.HTTPStatusCodeKey.Int(reply.StatusCode))
				}
				// 结束 span
				span.End()
			}()
			// 调用下一个中间件或最终的处理器
			return next.RoundTrip(req.WithContext(ctx))
		})
	}, nil
}

// newTracerProvider 函数根据传入的配置对象 options 创建一个 tracerProvider 实例
func newTracerProvider(ctx context.Context, options *v1.Tracing) trace.TracerProvider {
	// 初始化超时时间为默认值 10 秒
	var timeout = defaultTimeout
	// 初始化服务名为默认值 gateway
	var serviceName = defaultServiceName

	// 从上下文中获取应用信息，如果存在则设置服务名为应用名
	if appInfo, ok := kratos.FromContext(ctx); ok {
		serviceName = appInfo.Name()
	}

	// 如果配置对象中存在超时时间，则覆盖默认值
	if options.Timeout != nil {
		timeout = options.Timeout.AsDuration()
	}

	// 根据配置对象中的采样率设置采样器
	var sampler sdktrace.Sampler
	if options.SampleRatio == nil {
		// 如果未设置采样率，则默认总是采样
		sampler = sdktrace.AlwaysSample()
	} else {
		// 如果设置了采样率，则根据采样率进行采样
		sampler = sdktrace.TraceIDRatioBased(float64(*options.SampleRatio))
	}

	// 创建一个 OTLP HTTP 客户端选项列表
	otlpoptions := []otlptracehttp.Option{
		// 设置 OTLP 端点为配置对象中的 HTTP 端点
		otlptracehttp.WithEndpoint(options.HttpEndpoint),
		// 设置超时时间为配置对象中的超时时间
		otlptracehttp.WithTimeout(timeout),
	}
	// 如果配置对象中设置了不启用 TLS，则添加不安全选项
	if options.Insecure != nil && *options.Insecure {
		otlpoptions = append(otlpoptions, otlptracehttp.WithInsecure())
	}

	// 创建一个 OTLP HTTP 客户端
	client := otlptracehttp.NewClient(
		otlpoptions...,
	)

	// 创建一个 OTLP 跟踪导出器
	exporter, err := otlptrace.New(ctx, client)
	if err != nil {
		// 如果创建导出器失败，则记录错误并退出程序
		log.Fatalf("creating OTLP trace exporter: %v", err)
	}

	// 创建一个资源对象，包含服务名等属性
	resources := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
	)

	// 返回一个新的 tracerProvider 实例，包含采样器、导出器和资源
	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resources),
	)
}
