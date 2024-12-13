package mux

import (
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/cnsync/gateway/router"
	"github.com/cnsync/kratos/log"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// EnableStrictSlash 变量用于控制是否启用严格的斜杠匹配模式
var EnableStrictSlash = parseBool(os.Getenv("ENABLE_STRICT_SLASH"), false)

// parseBool 函数将字符串解析为布尔值
func parseBool(in string, defV bool) bool {
	// 如果输入字符串为空，则返回默认值
	if in == "" {
		return defV
	}
	// 尝试将输入字符串解析为布尔值
	v, err := strconv.ParseBool(in)
	// 如果解析失败，则返回默认值
	if err != nil {
		return defV
	}
	// 解析成功，返回解析后的布尔值
	return v
}

// 引入 router 包中的 Router 接口，用于实现自定义的路由器
var _ = new(router.Router)

// muxRouter 结构体定义了一个基于 gorilla/mux 实现的路由器
type muxRouter struct {
	// Router 字段是一个指向 gorilla/mux 路由器实例的指针
	*mux.Router
	// wg 字段是一个同步等待组，用于等待所有处理中的请求完成
	wg *sync.WaitGroup
	// allCloser 字段是一个 io.Closer 接口的切片，用于存储所有需要关闭的资源
	allCloser []io.Closer
}

// ProtectedHandler 函数用于保护指定的 HTTP 处理程序
func ProtectedHandler(h http.Handler) http.Handler {
	// 返回一个新的 http.Handler 接口实现
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查请求头中是否包含 "X-Forwarded-For" 字段
		if r.Header.Get("X-Forwarded-For") != "" {
			// 如果包含，则返回 403 Forbidden 状态码和相应的状态文本
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			// 阻止请求继续处理
			return
		}
		// 调用传入的处理程序 h 的 ServeHTTP 方法，处理请求
		h.ServeHTTP(w, r)
	})
}

// NewRouter 函数用于创建一个新的路由器实例
func NewRouter(notFoundHandler, methodNotAllowedHandler http.Handler) router.Router {
	// 创建一个新的 muxRouter 实例
	r := &muxRouter{
		// 初始化 gorilla/mux 的 Router 实例，并设置是否启用严格斜杠模式
		Router: mux.NewRouter().StrictSlash(EnableStrictSlash),
		// 初始化同步等待组
		wg: &sync.WaitGroup{},
	}
	// 注册一个处理程序，用于处理 /metrics 路径的请求
	r.Router.Handle("/metrics", ProtectedHandler(promhttp.Handler()))
	// 设置 404 未找到处理程序
	r.Router.NotFoundHandler = notFoundHandler
	// 设置 405 方法不允许处理程序
	r.Router.MethodNotAllowedHandler = methodNotAllowedHandler
	// 返回创建的路由器实例
	return r
}

// cleanPath 函数用于清理和标准化 URL 路径
func cleanPath(p string) string {
	// 如果路径为空，则返回根路径 "/"
	if p == "" {
		return "/"
	}
	// 如果路径的第一个字符不是 '/', 则在路径前添加 '/'
	if p[0] != '/' {
		p = "/" + p
	}
	// 使用 path.Clean 函数去除路径中的多余斜杠和 "."、".." 等
	np := path.Clean(p)
	// path.Clean 会移除末尾的斜杠，除非路径是根路径 "/"
	// 如果原始路径以斜杠结尾，且清理后的路径不是根路径，则在清理后的路径后添加斜杠
	if p[len(p)-1] == '/' && np != "/" {
		np += "/"
	}

	return np
}

// ServeHTTP 方法实现了 http.Handler 接口，用于处理 HTTP 请求
func (r *muxRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 增加等待组计数器，表示有一个新的请求开始处理
	r.wg.Add(1)
	// 延迟执行，在请求处理结束时减少等待组计数器
	defer r.wg.Done()
	// 调用 cleanPath 函数处理请求的 URL 路径，去除多余的斜杠并确保根路径有斜杠
	req.URL.Path = cleanPath(req.URL.Path)
	// 使用 gorilla/mux 的 Router 实例处理 HTTP 请求
	r.Router.ServeHTTP(w, req)
}

// Handle 方法用于注册一个处理器，该处理器将处理指定模式、方法和主机的请求
func (r *muxRouter) Handle(pattern, method, host string, handler http.Handler, closer io.Closer) error {
	// 创建一个新的路由，并设置处理函数
	next := r.Router.NewRoute().Handler(handler)
	// 如果指定了主机名，则设置路由的主机限制
	if host != "" {
		next = next.Host(host)
	}
	// 如果模式以星号结尾，则设置路由为前缀匹配
	if strings.HasSuffix(pattern, "*") {
		// /api/echo/*
		next = next.PathPrefix(strings.TrimRight(pattern, "*"))
	} else {
		// /api/echo/hello
		// /api/echo/[a-z]+
		// /api/echo/{name}
		next = next.Path(pattern)
	}
	// 如果指定了方法，则设置路由的方法限制
	if method != "" && method != "*" {
		next = next.Methods(method, http.MethodOptions)
	}
	// 检查路由配置是否有错误
	if err := next.GetError(); err != nil {
		return err
	}
	// 将关闭器添加到路由器的关闭器列表中
	r.allCloser = append(r.allCloser, closer)
	// 返回 nil 表示注册成功
	return nil
}

// SyncClose 方法用于同步关闭路由器，等待所有请求处理完毕后关闭
func (r *muxRouter) SyncClose(ctx context.Context) error {
	// 检查是否超时，如果超时则记录警告信息
	if timeout := waitTimeout(ctx, r.wg); timeout {
		log.Warnf("Time out to wait all requests complete, processing force close")
	}
	// 遍历所有关闭器，执行关闭操作
	for _, closer := range r.allCloser {
		// 尝试关闭，如果失败则记录错误信息
		if err := closer.Close(); err != nil {
			log.Errorf("Failed to execute close function: %+v", err)
			continue
		}
	}
	// 返回 nil 表示关闭成功
	return nil
}

// waitTimeout 函数用于等待一个 sync.WaitGroup 完成，并在给定的 context.Context 超时后返回一个布尔值表示是否超时
func waitTimeout(ctx context.Context, wg *sync.WaitGroup) bool {
	// 创建一个无缓冲的通道，用于通知等待组完成
	c := make(chan struct{})
	go func() {
		// 延迟关闭通道，确保在等待组完成后关闭
		defer close(c)
		// 等待等待组完成
		wg.Wait()
	}()
	// 使用 select 语句等待通道关闭或上下文超时
	select {
	case <-c:
		// 等待组完成，返回 false 表示没有超时
		return false // completed normally
	case <-ctx.Done():
		// 上下文超时，返回 true 表示超时
		return true // timed out
	}
}

// RouterInspect 结构体定义了一个路由的详细信息，包括路径模板、路径正则表达式、查询参数模板、查询参数正则表达式和方法
type RouterInspect struct {
	// PathTemplate 是路由的路径模板，例如 "/api/echo/{name}"
	PathTemplate string `json:"path_template"`
	// PathRegexp 是路由的路径正则表达式，例如 "/api/echo/[a-z]+"
	PathRegexp string `json:"path_regexp"`
	// QueriesTemplates 是路由的查询参数模板列表，例如 ["name"]
	QueriesTemplates []string `json:"queries_templates"`
	// QueriesRegexps 是路由的查询参数正则表达式列表，例如 ["[a-z]+"]
	QueriesRegexps []string `json:"queries_regexps"`
	// Methods 是路由支持的 HTTP 方法列表，例如 ["GET", "POST"]
	Methods []string `json:"methods"`
}

// InspectMuxRouter 函数用于检查和收集 muxRouter 实例中的路由信息
func InspectMuxRouter(in interface{}) []*RouterInspect {
	// 将输入的接口转换为 *muxRouter 类型
	r, ok := in.(*muxRouter)
	// 如果转换失败，则返回 nil
	if !ok {
		return nil
	}
	// 初始化一个 RouterInspect 类型的切片，用于存储收集到的路由信息
	var out []*RouterInspect
	// 使用 Walk 方法遍历 muxRouter 实例中的所有路由
	_ = r.Walk(func(route *mux.Route, router *mux.Router, ancestors []*mux.Route) error {
		// 获取当前路由的路径模板
		pathTemplate, _ := route.GetPathTemplate()
		// 获取当前路由的路径正则表达式
		pathRegexp, _ := route.GetPathRegexp()
		// 获取当前路由的查询参数模板
		queriesTemplates, _ := route.GetQueriesTemplates()
		// 获取当前路由的查询参数正则表达式
		queriesRegexps, _ := route.GetQueriesRegexp()
		// 获取当前路由支持的 HTTP 方法
		methods, _ := route.GetMethods()
		// 将收集到的路由信息封装到 RouterInspect 结构体中，并添加到 out 切片中
		out = append(out, &RouterInspect{
			PathTemplate:     pathTemplate,
			PathRegexp:       pathRegexp,
			QueriesTemplates: queriesTemplates,
			QueriesRegexps:   queriesRegexps,
			Methods:          methods,
		})
		// 返回 nil，表示遍历过程中没有发生错误
		return nil
	})
	// 返回收集到的路由信息切片
	return out
}
