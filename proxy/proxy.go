package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	config "github.com/cnsync/gateway/api/gateway/config/v1"
	"github.com/cnsync/gateway/client"
	"github.com/cnsync/gateway/middleware"
	"github.com/cnsync/gateway/router"
	"github.com/cnsync/gateway/router/mux"
	"github.com/cnsync/kratos/log"
	"github.com/cnsync/kratos/selector"
	"github.com/cnsync/kratos/transport/http/status"
	"github.com/go-kratos/aegis/circuitbreaker/sre"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// _metricRequestsTotal 是一个计数器，用于记录处理的请求总数
	_metricRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "go",
		Subsystem: "gateway",
		Name:      "requests_code_total",
		Help:      "The total number of processed requests",
	}, []string{"protocol", "method", "path", "code", "service", "basePath"})
	// _metricRequestsDuration 是一个直方图，用于记录请求的持续时间
	_metricRequestsDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "go",
		Subsystem: "gateway",
		Name:      "requests_duration_seconds",
		Help:      "Requests duration(sec).",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.250, 0.5, 1},
	}, []string{"protocol", "method", "path", "service", "basePath"})
	// _metricSentBytes 是一个计数器，用于记录发送的总字节数
	_metricSentBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "go",
		Subsystem: "gateway",
		Name:      "requests_tx_bytes",
		Help:      "Total sent connection bytes",
	}, []string{"protocol", "method", "path", "service", "basePath"})
	// _metricReceivedBytes 是一个计数器，用于记录接收的总字节数
	_metricReceivedBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "go",
		Subsystem: "gateway",
		Name:      "requests_rx_bytes",
		Help:      "Total received connection bytes",
	}, []string{"protocol", "method", "path", "service", "basePath"})
	// _metricRetryState 是一个计数器，用于记录请求重试的状态
	_metricRetryState = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "go",
		Subsystem: "gateway",
		Name:      "requests_retry_state",
		Help:      "Total request retries",
	}, []string{"protocol", "method", "path", "service", "basePath", "success"})
)

// init 函数在程序启动时自动执行，用于注册 Prometheus 指标
func init() {
	// 注册 _metricRequestsTotal 指标，用于记录处理的请求总数
	prometheus.MustRegister(_metricRequestsTotal)
	// 注册 _metricRequestsDuration 指标，用于记录请求的持续时间
	prometheus.MustRegister(_metricRequestsDuration)
	// 注册 _metricRetryState 指标，用于记录请求重试的状态
	prometheus.MustRegister(_metricRetryState)
	// 注册 _metricSentBytes 指标，用于记录发送的总字节数
	prometheus.MustRegister(_metricSentBytes)
	// 注册 _metricReceivedBytes 指标，用于记录接收的总字节数
	prometheus.MustRegister(_metricReceivedBytes)
}

// setXFFHeader 函数用于设置 HTTP 请求头中的 X-Forwarded-For 字段
func setXFFHeader(req *http.Request) {
	// 参考 https://github.com/golang/go/blob/master/src/net/http/httputil/reverseproxy.go
	// 从请求的 RemoteAddr 中提取客户端 IP 地址
	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		// 如果我们不是第一个代理，则保留之前的 X-Forwarded-For 信息，并将其作为逗号分隔的列表
		// 并将多个标头折叠为一个。
		prior, ok := req.Header["X-Forwarded-For"]
		omit := ok && prior == nil // Issue 38079: nil 现在意味着不填充标头
		if len(prior) > 0 {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		// 如果不是省略模式，则设置 X-Forwarded-For 字段
		if !omit {
			req.Header.Set("X-Forwarded-For", clientIP)
		}
	}
}

// writeError 函数用于将错误信息写入 HTTP 响应
func writeError(w http.ResponseWriter, r *http.Request, err error, labels middleware.MetricsLabels) {
	// 根据错误类型设置状态码
	var statusCode int
	switch {
	case errors.Is(err, context.Canceled),
		err.Error() == "client disconnected":
		// 客户端取消请求或断开连接
		statusCode = 499
	case errors.Is(err, context.DeadlineExceeded):
		// 请求超时
		statusCode = 504
	default:
		// 其他错误
		log.Errorf("Failed to handle request: %s: %+v", r.URL.String(), err)
		statusCode = 502
	}
	// 记录请求总数指标
	requestsTotalIncr(r, labels, statusCode)
	// 如果是 gRPC 协议，则设置相应的响应头
	if labels.Protocol() == config.Protocol_GRPC.String() {
		// see https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto
		// 将状态码转换为 gRPC 错误码
		code := strconv.Itoa(int(status.ToGRPCCode(statusCode)))
		// 设置响应头
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Grpc-Status", code)
		w.Header().Set("Grpc-Message", err.Error())
		// gRPC 状态码为 200
		statusCode = 200
	}
	// 写入状态码
	w.WriteHeader(statusCode)
}

// notFoundHandler 函数用于处理 HTTP 请求中的 404 错误
func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	// 设置 HTTP 状态码为 404，表示页面未找到
	code := http.StatusNotFound
	// 设置错误信息
	message := "404 page not found"
	// 使用 http.Error 函数向客户端发送 404 错误
	http.Error(w, message, code)
	// 使用 log 包记录错误信息
	log.Context(r.Context()).Errorw(
		// 记录错误来源为 accesslog
		"source", "accesslog",
		// 记录请求的主机名
		"host", r.Host,
		// 记录请求的方法
		"method", r.Method,
		// 记录请求的路径
		"path", r.URL.Path,
		// 记录请求的查询字符串
		"query", r.URL.RawQuery,
		// 记录请求的用户代理
		"user_agent", r.Header.Get("User-Agent"),
		// 记录错误状态码
		"code", code,
		// 记录错误信息
		"error", message,
	)
	// 使用 Prometheus 指标记录 404 错误的数量
	_metricRequestsTotal.WithLabelValues("HTTP", r.Method, "/404", strconv.Itoa(code), "", "").Inc()
}

// methodNotAllowedHandler 函数用于处理 HTTP 请求中的 405 错误
func methodNotAllowedHandler(w http.ResponseWriter, r *http.Request) {
	// 设置 HTTP 状态码为 405，表示方法不被允许
	code := http.StatusMethodNotAllowed
	// 设置错误信息
	message := http.StatusText(code)
	// 使用 http.Error 函数向客户端发送 405 错误
	http.Error(w, message, code)
	// 使用 log 包记录错误信息
	log.Context(r.Context()).Errorw(
		// 记录错误来源为 accesslog
		"source", "accesslog",
		// 记录请求的主机名
		"host", r.Host,
		// 记录请求的方法
		"method", r.Method,
		// 记录请求的路径
		"path", r.URL.Path,
		// 记录请求的查询字符串
		"query", r.URL.RawQuery,
		// 记录请求的用户代理
		"user_agent", r.Header.Get("User-Agent"),
		// 记录错误状态码
		"code", code,
		// 记录错误信息
		"error", message,
	)
	// 使用 Prometheus 指标记录 405 错误的数量
	_metricRequestsTotal.WithLabelValues("HTTP", r.Method, "/405", strconv.Itoa(code), "", "").Inc()
}

// interceptors 结构体定义了一个拦截器，用于在请求处理过程中进行拦截和处理
type interceptors struct {
	// prepareAttemptTimeoutContext 函数用于准备尝试超时上下文
	prepareAttemptTimeoutContext func(ctx context.Context, req *http.Request, timeout time.Duration) (context.Context, context.CancelFunc)
}

// SetPrepareAttemptTimeoutContext 方法用于设置一个函数，该函数用于准备尝试超时的上下文。
func (i *interceptors) SetPrepareAttemptTimeoutContext(f func(ctx context.Context, req *http.Request, timeout time.Duration) (context.Context, context.CancelFunc)) {
	// 如果传入的函数不为 nil，则将其设置为 interceptors 结构体的 prepareAttemptTimeoutContext 字段。
	if f != nil {
		i.prepareAttemptTimeoutContext = f
	}
}

// Proxy 是一个网关代理。
type Proxy struct {
	// router 是一个原子值，用于存储路由器。
	router atomic.Value
	// clientFactory 是一个客户端工厂，用于创建客户端。
	clientFactory client.Factory
	// Interceptors 是一个拦截器，用于在请求处理过程中进行拦截和处理。
	Interceptors interceptors
	// middlewareFactory 是一个中间件工厂，用于创建中间件。
	middlewareFactory middleware.FactoryV2
}

// New 函数用于创建一个新的 Proxy 实例。
func New(clientFactory client.Factory, middlewareFactory middleware.FactoryV2) (*Proxy, error) {
	// 创建一个新的 Proxy 实例。
	p := &Proxy{
		// 设置客户端工厂。
		clientFactory: clientFactory,
		// 设置中间件工厂。
		middlewareFactory: middlewareFactory,
		// 初始化拦截器。
		Interceptors: interceptors{
			// 设置默认的尝试超时上下文函数。
			prepareAttemptTimeoutContext: defaultAttemptTimeoutContext,
		},
	}
	// 初始化路由器。
	p.router.Store(mux.NewRouter(http.HandlerFunc(notFoundHandler), http.HandlerFunc(methodNotAllowedHandler)))
	// 返回新创建的 Proxy 实例和 nil 错误。
	return p, nil
}

// buildMiddleware 方法用于构建一个中间件链，其中每个中间件都会处理下一个中间件的请求。
func (p *Proxy) buildMiddleware(ms []*config.Middleware, next http.RoundTripper) (http.RoundTripper, error) {
	// 遍历中间件列表，从后往前遍历。
	for i := len(ms) - 1; i >= 0; i-- {
		// 从中间件工厂中获取中间件实例。
		m, err := p.middlewareFactory(ms[i])
		// 如果获取中间件实例时发生错误。
		if err != nil {
			// 如果错误是因为中间件不存在。
			if errors.Is(err, middleware.ErrNotFound) {
				// 记录错误信息，跳过这个中间件。
				log.Errorf("Skip does not exist middleware: %s", ms[i].Name)
				// 继续处理下一个中间件。
				continue
			}
			// 如果错误不是因为中间件不存在，返回错误。
			return nil, err
		}
		// 将当前中间件添加到中间件链中，处理下一个中间件的请求。
		next = m.Process(next)
	}
	// 返回构建好的中间件链和 nil 错误。
	return next, nil
}

// splitRetryMetricsHandler 函数用于拆分重试指标处理程序
func splitRetryMetricsHandler(e *config.Endpoint) (func(*http.Request, int), func(*http.Request, int, error)) {
	// 根据端点配置创建指标标签
	labels := middleware.NewMetricsLabels(e)
	// 定义成功重试处理函数
	success := func(req *http.Request, i int) {
		// 如果重试次数小于等于 0，则不进行任何操作
		if i <= 0 {
			return
		}
		// 增加成功重试次数
		retryStateIncr(req, labels, true)
	}
	// 定义失败重试处理函数
	failed := func(req *http.Request, i int, err error) {
		// 如果重试次数小于等于 0，则不进行任何操作
		if i <= 0 {
			return
		}
		// 如果错误是由上下文取消引起的，则不进行任何操作
		if errors.Is(err, context.Canceled) {
			return
		}
		// 增加失败重试次数
		retryStateIncr(req, labels, false)
	}
	// 返回成功和失败重试处理函数
	return success, failed
}

func (p *Proxy) buildEndpoint(buildCtx *client.BuildContext, e *config.Endpoint, ms []*config.Middleware) (_ http.Handler, _ io.Closer, retError error) {
	// 使用客户端工厂创建一个新的客户端实例
	client, err := p.clientFactory(buildCtx, e)
	// 如果发生错误，返回 nil, nil, err
	if err != nil {
		return nil, nil, err
	}
	// 将客户端转换为 http.RoundTripper 接口类型
	tripper := http.RoundTripper(client)
	// 将客户端转换为 io.Closer 接口类型
	closer := io.Closer(client)
	// 延迟调用 closeOnError 函数，确保在函数返回时关闭资源
	defer closeOnError(closer, &retError)

	// 使用中间件工厂构建中间件链
	tripper, err = p.buildMiddleware(e.Middlewares, tripper)
	// 如果发生错误，返回 nil, nil, err
	if err != nil {
		return nil, nil, err
	}
	// 使用中间件工厂构建中间件链
	tripper, err = p.buildMiddleware(ms, tripper)
	// 如果发生错误，返回 nil, nil, err
	if err != nil {
		return nil, nil, err
	}
	// 准备重试策略
	retryStrategy, err := prepareRetryStrategy(e)
	// 如果发生错误，返回 nil, nil, err
	if err != nil {
		return nil, nil, err
	}
	// 创建指标标签
	labels := middleware.NewMetricsLabels(e)
	// 拆分重试指标处理程序
	markSuccessStat, markFailedStat := splitRetryMetricsHandler(e)
	// 创建重试断路器
	retryBreaker := sre.NewBreaker(sre.WithSuccess(0.8))
	// 定义标记成功的函数
	markSuccess := func(req *http.Request, i int) {
		// 标记成功状态
		markSuccessStat(req, i)
		// 如果重试次数大于 0，则标记断路器为成功
		if i > 0 {
			retryBreaker.MarkSuccess()
		}
	}
	// 定义标记失败的函数
	markFailed := func(req *http.Request, i int, err error) {
		// 标记失败状态
		markFailedStat(req, i, err)
		// 如果重试次数大于 0，则标记断路器为失败
		if i > 0 {
			retryBreaker.MarkFailed()
		}
	}
	// 返回一个 http.Handler 接口类型的函数
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// 记录请求开始时间
		startTime := time.Now()
		// 设置 X-Forwarded-For 头部
		setXFFHeader(req)

		// 创建请求选项
		reqOpts := middleware.NewRequestOptions(e)
		// 创建请求上下文
		ctx := middleware.NewRequestContext(req.Context(), reqOpts)
		// 设置请求超时时间
		ctx, cancel := context.WithTimeout(ctx, retryStrategy.timeout)
		// 延迟调用 cancel 函数，确保在函数结束时取消上下文
		defer cancel()
		// 延迟调用函数，记录请求持续时间
		defer func() {
			// 观察请求持续时间指标
			requestsDurationObserve(req, labels, time.Since(startTime).Seconds())
		}()

		// 读取请求体
		body, err := io.ReadAll(req.Body)
		// 如果发生错误，写入错误信息并返回
		if err != nil {
			writeError(w, req, err, labels)
			return
		}
		// 增加接收到的字节数指标
		receivedBytesAdd(req, labels, int64(len(body)))
		// 设置请求体的读取函数
		req.GetBody = func() (io.ReadCloser, error) {
			// 创建一个新的字节读取器
			reader := bytes.NewReader(body)
			// 返回一个 io.NopCloser 包装的读取器
			return io.NopCloser(reader), nil
		}

		// 初始化响应对象
		var resp *http.Response
		// 循环重试策略的尝试次数
		for i := 0; i < retryStrategy.attempts; i++ {
			// 如果不是第一次尝试
			if i > 0 {
				// 如果重试功能未启用，则跳出循环
				if !retryFeature.Enabled() {
					break
				}
				// 如果断路器不允许重试，则标记失败并跳出循环
				if err := retryBreaker.Allow(); err != nil {
					markFailed(req, i, err)
					break
				}
			}

			// 如果是最后一次尝试
			if (i + 1) >= retryStrategy.attempts {
				reqOpts.LastAttempt = true
			}
			// 如果上下文已取消或超时
			if err = ctx.Err(); err != nil {
				markFailed(req, i, err)
				break
			}
			// 准备尝试超时上下文
			tryCtx, cancel := p.Interceptors.prepareAttemptTimeoutContext(ctx, req, retryStrategy.perTryTimeout)
			// 延迟调用 cancel 函数，确保在函数结束时取消上下文
			defer cancel()
			// 创建一个新的字节读取器
			reader := bytes.NewReader(body)
			// 将请求体设置为新的读取器
			req.Body = io.NopCloser(reader)
			// 发送请求并获取响应
			resp, err = tripper.RoundTrip(req.Clone(tryCtx))
			// 如果发生错误，标记失败并记录日志
			if err != nil {
				markFailed(req, i, err)
				log.Errorf("Attempt at [%d/%d], failed to handle request: %s: %+v", i+1, retryStrategy.attempts, req.URL.String(), err)
				continue
			}
			// 如果不需要重试
			if !judgeRetryRequired(retryStrategy.conditions, resp) {
				reqOpts.LastAttempt = true
				// 标记成功
				markSuccess(req, i)
				break
			}
			// 标记失败
			markFailed(req, i, errors.New("assertion failed"))
			// 继续重试循环
		}
		// 如果发生错误，写入错误信息并返回
		if err != nil {
			writeError(w, req, err, labels)
			return
		}

		// 将响应头复制到响应写入器
		headers := w.Header()
		for k, v := range resp.Header {
			headers[k] = v
		}
		// 设置响应状态码
		w.WriteHeader(resp.StatusCode)

		// 定义一个函数，用于复制响应体
		doCopyBody := func() bool {
			// 如果响应体为空，返回 true
			if resp.Body == nil {
				return true
			}
			// 延迟关闭响应体
			defer resp.Body.Close()
			// 复制响应体到响应写入器
			sent, err := io.Copy(w, resp.Body)
			// 如果发生错误，记录错误信息并增加发送字节数指标
			if err != nil {
				reqOpts.DoneFunc(ctx, selector.DoneInfo{Err: err})
				sentBytesAdd(req, labels, sent)
				log.Errorf("Failed to copy backend response body to client: [%s] %s %s %d %+v\n", e.Protocol, e.Method, e.Path, sent, err)
				return false
			}
			// 增加发送字节数指标
			sentBytesAdd(req, labels, sent)
			// 调用完成函数，传递响应元数据
			reqOpts.DoneFunc(ctx, selector.DoneInfo{ReplyMD: getReplyMD(e, resp)})
			// 处理响应的 Trailer
			for k, v := range resp.Trailer {
				headers[http.TrailerPrefix+k] = v
			}
			return true
		}
		// 调用复制响应体的函数
		doCopyBody()
		// 增加请求总数指标
		requestsTotalIncr(req, labels, resp.StatusCode)
	}), closer, nil
}

// getReplyMD 根据协议类型获取响应的元数据。
func getReplyMD(ep *config.Endpoint, resp *http.Response) selector.ReplyMD {
	// 如果协议是 gRPC，则返回响应的 Trailer
	if ep.Protocol == config.Protocol_GRPC {
		return resp.Trailer
	}
	// 否则返回响应的 Header
	return resp.Header
}

// receivedBytesAdd 增加接收到的字节数指标。
func receivedBytesAdd(req *http.Request, labels middleware.MetricsLabels, received int64) {
	// 使用标签值更新接收到的字节数指标
	_metricReceivedBytes.WithLabelValues(labels.Protocol(), req.Method, labels.Path(), labels.Service(), labels.BasePath()).Add(float64(received))
}

// sentBytesAdd 增加发送的字节数指标。
func sentBytesAdd(req *http.Request, labels middleware.MetricsLabels, sent int64) {
	// 使用标签值更新发送的字节数指标
	_metricSentBytes.WithLabelValues(labels.Protocol(), req.Method, labels.Path(), labels.Service(), labels.BasePath()).Add(float64(sent))
}

// requestsTotalIncr 增加请求总数指标。
func requestsTotalIncr(req *http.Request, labels middleware.MetricsLabels, statusCode int) {
	// 使用标签值更新请求总数指标
	_metricRequestsTotal.WithLabelValues(labels.Protocol(), req.Method, labels.Path(), strconv.Itoa(statusCode), labels.Service(), labels.BasePath()).Inc()
}

// requestsDurationObserve 观察请求持续时间指标。
func requestsDurationObserve(req *http.Request, labels middleware.MetricsLabels, seconds float64) {
	// 使用标签值更新请求持续时间指标
	_metricRequestsDuration.WithLabelValues(labels.Protocol(), req.Method, labels.Path(), labels.Service(), labels.BasePath()).Observe(seconds)
}

// retryStateIncr 增加重试状态指标。
func retryStateIncr(req *http.Request, labels middleware.MetricsLabels, success bool) {
	// 如果重试成功，则增加成功重试的指标
	if success {
		_metricRetryState.WithLabelValues(labels.Protocol(), req.Method, labels.Path(), labels.Service(), labels.BasePath(), "true").Inc()
		return
	}
	// 否则增加失败重试的指标
	_metricRetryState.WithLabelValues(labels.Protocol(), req.Method, labels.Path(), labels.Service(), labels.BasePath(), "false").Inc()
}

// closeOnError 在发生错误时关闭资源。
func closeOnError(closer io.Closer, err *error) {
	// 如果没有错误，则不执行任何操作
	if *err == nil {
		return
	}
	// 关闭资源
	closer.Close()
}

// Update 更新服务端点。
func (p *Proxy) Update(buildContext *client.BuildContext, c *config.Gateway) (retError error) {
	// 创建一个新的路由器，使用 notFoundHandler 和 methodNotAllowedHandler 作为默认处理器
	router := mux.NewRouter(http.HandlerFunc(notFoundHandler), http.HandlerFunc(methodNotAllowedHandler))

	// 遍历配置中的所有端点
	for _, e := range c.Endpoints {
		// 为每个端点构建处理程序和关闭器
		handler, closer, err := p.buildEndpoint(buildContext, e, c.Middlewares)
		// 如果发生错误，返回错误
		if err != nil {
			return err
		}
		// 延迟调用 closeOnError 函数，确保在函数返回时关闭资源
		defer closeOnError(closer, &retError)

		// 将处理程序注册到路由器中
		if err = router.Handle(e.Path, e.Method, e.Host, handler, closer); err != nil {
			// 如果注册过程中发生错误，返回错误
			return err
		}
		// 记录日志，表示成功构建了端点
		log.Infof("build endpoint: [%s] %s %s", e.Protocol, e.Method, e.Path)
	}

	// 替换旧的路由器
	old := p.router.Swap(router)
	// 尝试关闭旧的路由器
	tryCloseRouter(old)

	// 返回 nil，表示更新成功
	return nil
}

// tryCloseRouter 尝试关闭传入的路由器。
func tryCloseRouter(in interface{}) {
	// 如果传入的对象为 nil，则直接返回
	if in == nil {
		return
	}
	// 尝试将传入的对象转换为 router.Router 接口类型
	r, ok := in.(router.Router)
	// 如果转换失败，则直接返回
	if !ok {
		return
	}
	// 启动一个新的 goroutine 来关闭路由器
	go func() {
		// 创建一个带有 120 秒超时的上下文
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		// 延迟调用 cancel 函数，确保在函数结束时取消上下文
		defer cancel()
		// 调用路由器的 SyncClose 方法来关闭路由器
		r.SyncClose(ctx)
	}()
}

// ServeHTTP 实现了 http.Handler 接口，用于处理 HTTP 请求。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 延迟调用 recover 函数，用于捕获可能发生的 panic
	defer func() {
		// 如果发生了 panic，获取 panic 的值
		if err := recover(); err != nil {
			// 向客户端发送 502 Bad Gateway 状态码
			w.WriteHeader(http.StatusBadGateway)
			// 创建一个 64KB 的缓冲区
			buf := make([]byte, 64<<10) //nolint:gomnd
			// 获取堆栈跟踪信息
			n := runtime.Stack(buf, false)
			// 记录错误日志，包含 panic 的值和堆栈跟踪信息
			log.Errorf("panic recovered: %+v\n%s", err, buf[:n])
			// 将错误信息输出到标准错误输出
			fmt.Fprintf(os.Stderr, "panic recovered: %+v\n%s\n", err, buf[:n])
		}
	}()
	// 加载当前的路由器，并将其转换为 router.Router 接口类型
	p.router.Load().(router.Router).ServeHTTP(w, req)
}

// DebugHandler 实现了一个调试处理器。
func (p *Proxy) DebugHandler() http.Handler {
	// 创建一个新的 ServeMux 用于调试
	debugMux := http.NewServeMux()
	// 注册一个处理函数，用于检查路由器的状态
	debugMux.HandleFunc("/debug/proxy/router/inspect", func(rw http.ResponseWriter, r *http.Request) {
		// 加载当前的路由器，并将其转换为 router.Router 接口类型
		router, ok := p.router.Load().(router.Router)
		// 如果转换失败，直接返回
		if !ok {
			return
		}
		// 获取路由器的检查信息
		inspect := mux.InspectMuxRouter(router)
		// 设置响应头，指定内容类型为 application/json
		rw.Header().Set("Content-Type", "application/json")
		// 将检查信息编码为 JSON 并写入响应
		json.NewEncoder(rw).Encode(inspect)
	})
	// 返回调试处理器
	return debugMux
}
