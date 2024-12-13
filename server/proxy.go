package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/cnsync/kratos/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var (
	// 定义变量 readHeaderTimeout，设置读取请求头的超时时间为 10 秒
	readHeaderTimeout = time.Second * 10
	// 定义变量 readTimeout，设置读取请求体的超时时间为 15 秒
	readTimeout = time.Second * 15
	// 定义变量 writeTimeout，设置发送响应的超时时间为 15 秒
	writeTimeout = time.Second * 15
	// 定义变量 idleTimeout，设置连接空闲超时时间为 120 秒
	idleTimeout = time.Second * 120
)

// 初始化函数，从环境变量中读取配置
func init() {
	// 尝试从环境变量中读取 PROXY_READ_HEADER_TIMEOUT 的值
	var err error
	if v := os.Getenv("PROXY_READ_HEADER_TIMEOUT"); v != "" {
		// 如果读取成功，则尝试将其解析为 time.Duration 类型
		if readHeaderTimeout, err = time.ParseDuration(v); err != nil {
			// 如果解析失败，则抛出异常
			panic(err)
		}
	}
	// 尝试从环境变量中读取 PROXY_READ_TIMEOUT 的值
	if v := os.Getenv("PROXY_READ_TIMEOUT"); v != "" {
		// 如果读取成功，则尝试将其解析为 time.Duration 类型
		if readTimeout, err = time.ParseDuration(v); err != nil {
			// 如果解析失败，则抛出异常
			panic(err)
		}
	}
	// 尝试从环境变量中读取 PROXY_WRITE_TIMEOUT 的值
	if v := os.Getenv("PROXY_WRITE_TIMEOUT"); v != "" {
		// 如果读取成功，则尝试将其解析为 time.Duration 类型
		if writeTimeout, err = time.ParseDuration(v); err != nil {
			// 如果解析失败，则抛出异常
			panic(err)
		}
	}
	// 尝试从环境变量中读取 PROXY_IDLE_TIMEOUT 的值
	if v := os.Getenv("PROXY_IDLE_TIMEOUT"); v != "" {
		// 如果读取成功，则尝试将其解析为 time.Duration 类型
		if idleTimeout, err = time.ParseDuration(v); err != nil {
			// 如果解析失败，则抛出异常
			panic(err)
		}
	}
}

// ProxyServer 代理服务器
type ProxyServer struct {
	// 嵌入 http.Server 类型，以便使用其方法和字段
	*http.Server
}

// NewProxy 函数用于创建一个新的代理服务器实例
func NewProxy(handler http.Handler, addr string) *ProxyServer {
	return &ProxyServer{
		// 创建一个新的 http.Server 实例
		Server: &http.Server{
			// 设置服务器监听的地址
			Addr: addr,
			// 使用 h2c.NewHandler 包装处理程序，支持 HTTP/2 协议
			Handler: h2c.NewHandler(handler, &http2.Server{
				// 设置空闲超时时间
				IdleTimeout: idleTimeout,
				// 设置最大并发流数
				MaxConcurrentStreams: math.MaxUint32,
			}),
			// 设置读取超时时间
			ReadTimeout: readTimeout,
			// 设置读取头超时时间
			ReadHeaderTimeout: readHeaderTimeout,
			// 设置写入超时时间
			WriteTimeout: writeTimeout,
			// 设置空闲超时时间
			IdleTimeout: idleTimeout,
		},
	}
}

// Start 方法用于启动代理服务
func (s *ProxyServer) Start(ctx context.Context) error {
	// 记录日志，显示代理服务器正在监听的地址
	log.Infof("proxy listening on %s", s.Addr)
	// 调用 http.Server 的 ListenAndServe 方法，开始监听并处理请求
	err := s.ListenAndServe()
	// 如果发生错误，并且错误类型是 http.ErrServerClosed
	if errors.Is(err, http.ErrServerClosed) {
		// 这表示服务器已经被关闭，返回 nil 表示没有错误
		return nil
	}
	// 如果发生其他错误，返回该错误
	return err
}

// Stop 方法用于停止代理服务器的运行
func (s *ProxyServer) Stop(ctx context.Context) error {
	// 记录日志，显示代理服务器正在停止
	log.Info("proxy stopping")
	// 调用 http.Server 的 Shutdown 方法，停止服务器的运行
	return s.Shutdown(ctx)
}
