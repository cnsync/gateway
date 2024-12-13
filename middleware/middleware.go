package middleware

import (
	"io"
	"net/http"

	configv1 "github.com/cnsync/gateway/api/gateway/config/v1"
)

// Factory 是一个中间件工厂。
type Factory func(*configv1.Middleware) (Middleware, error)

// Middleware 是处理 HTTP 请求的中间件。
type Middleware func(http.RoundTripper) http.RoundTripper

// RoundTripperFunc 是一个适配器，允许将普通函数用作 HTTP RoundTripper。
type RoundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip 调用 f(w, r)。
func (f RoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// FactoryV2 是一个中间件工厂，它接受一个配置对象并返回一个中间件实例和错误。
type FactoryV2 func(*configv1.Middleware) (MiddlewareV2, error)

// MiddlewareV2 是一个接口，定义了中间件的处理方法和关闭方法。
type MiddlewareV2 interface {
	// Process 方法接受一个 HTTP RoundTripper 并返回一个新的 HTTP RoundTripper。
	Process(http.RoundTripper) http.RoundTripper
	// Closer 方法用于关闭中间件实例。
	io.Closer
}

// wrapFactory 函数将一个 Factory 类型的中间件工厂转换为 FactoryV2 类型。
func wrapFactory(in Factory) FactoryV2 {
	return func(m *configv1.Middleware) (MiddlewareV2, error) {
		v, err := in(m)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
}

// Process 方法实现了 MiddlewareV2 接口，它将传入的 HTTP RoundTripper 进行处理并返回。
func (f Middleware) Process(in http.RoundTripper) http.RoundTripper { return f(in) }

// Close 方法实现了 MiddlewareV2 接口，它返回一个 nil 错误，表示没有需要关闭的资源。
func (f Middleware) Close() error { return nil }

// withCloser 结构体包含一个中间件处理函数和一个关闭器。
type withCloser struct {
	process Middleware
	closer  io.Closer
}

// Process 方法实现了 MiddlewareV2 接口，它将传入的 HTTP RoundTripper 进行处理并返回。
func (w *withCloser) Process(in http.RoundTripper) http.RoundTripper { return w.process(in) }

// Close 方法实现了 MiddlewareV2 接口，它调用 closer 的 Close 方法来关闭资源。
func (w *withCloser) Close() error { return w.closer.Close() }

// NewWithCloser 函数创建一个新的 withCloser 实例，它包含一个中间件处理函数和一个关闭器。
func NewWithCloser(process Middleware, closer io.Closer) MiddlewareV2 {
	return &withCloser{
		process: process,
		closer:  closer,
	}
}

// EmptyMiddleware 是一个空的中间件实例，它不做任何处理。
var EmptyMiddleware = emptyMiddleware{}

// emptyMiddleware 结构体实现了 MiddlewareV2 接口，它的 Process 方法直接返回传入的 HTTP RoundTripper。
type emptyMiddleware struct{}

// Process 方法实现了 MiddlewareV2 接口，它将传入的 HTTP RoundTripper 直接返回。
func (emptyMiddleware) Process(next http.RoundTripper) http.RoundTripper { return next }

// Close 方法实现了 MiddlewareV2 接口，它返回一个 nil 错误，表示没有需要关闭的资源。
func (emptyMiddleware) Close() error { return nil }
