package router

import (
	"context"
	"io"
	"net/http"
)

// Router 网关路由器接口
type Router interface {
	// Handler http.Handler 是一个接口，用于处理 HTTP 请求。
	http.Handler
	// Handle 方法用于注册一个处理器，该处理器将处理指定模式、方法和主机的请求。
	Handle(pattern, method, host string, handler http.Handler, closer io.Closer) error
	// SyncClose 方法用于同步关闭路由器，等待所有请求处理完毕后关闭。
	SyncClose(ctx context.Context) error
}
