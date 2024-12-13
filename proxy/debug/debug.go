package debug

import (
	"net/http"
	"net/http/pprof"
	"path"
	"strings"

	rmux "github.com/cnsync/gateway/router/mux"
	"github.com/cnsync/kratos/log"
	"github.com/gorilla/mux"
)

// _debugPrefix 定义了调试服务的前缀路径
const (
	_debugPrefix = "/debug"
)

// globalService 是一个全局的 debugService 实例，用于处理调试请求
var globalService = &debugService{
	// handlers 是一个映射，包含了处理特定调试请求的函数
	handlers: map[string]http.HandlerFunc{
		// 处理 /debug/ping 请求的函数，目前为空实现
		"/debug/ping": func(rw http.ResponseWriter, r *http.Request) {},
		// 处理 /debug/pprof/ 请求的函数，调用 pprof.Index 函数
		"/debug/pprof/": pprof.Index,
		// 处理 /debug/pprof/cmdline 请求的函数，调用 pprof.Cmdline 函数
		"/debug/pprof/cmdline": pprof.Cmdline,
		// 处理 /debug/pprof/profile 请求的函数，调用 pprof.Profile 函数
		"/debug/pprof/profile": pprof.Profile,
		// 处理 /debug/pprof/symbol 请求的函数，调用 pprof.Symbol 函数
		"/debug/pprof/symbol": pprof.Symbol,
		// 处理 /debug/pprof/trace 请求的函数，调用 pprof.Trace 函数
		"/debug/pprof/trace": pprof.Trace,
		// 处理 /debug/pprof/allocs 请求的函数，调用 pprof.Handler("allocs") 函数
		"/debug/pprof/allocs": pprof.Handler("allocs").ServeHTTP,
		// 处理 /debug/pprof/block 请求的函数，调用 pprof.Handler("block") 函数
		"/debug/pprof/block": pprof.Handler("block").ServeHTTP,
		// 处理 /debug/pprof/goroutine 请求的函数，调用 pprof.Handler("goroutine") 函数
		"/debug/pprof/goroutine": pprof.Handler("goroutine").ServeHTTP,
		// 处理 /debug/pprof/heap 请求的函数，调用 pprof.Handler("heap") 函数
		"/debug/pprof/heap": pprof.Handler("heap").ServeHTTP,
		// 处理 /debug/pprof/mutex 请求的函数，调用 pprof.Handler("mutex") 函数
		"/debug/pprof/mutex": pprof.Handler("mutex").ServeHTTP,
		// 处理 /debug/pprof/threadcreate 请求的函数，调用 pprof.Handler("threadcreate") 函数
		"/debug/pprof/threadcreate": pprof.Handler("threadcreate").ServeHTTP,
	},
	// mux 是一个路由器，用于处理调试请求的路由
	mux: mux.NewRouter(),
}

// Register 函数用于向全局的 debugService 实例注册一个可调试的服务
func Register(name string, debuggable Debuggable) {
	// 调用全局的 debugService 实例的 Register 方法，传入服务名称和可调试的服务实例
	globalService.Register(name, debuggable)
}

// MashupWithDebugHandler 函数将调试处理程序与原始处理程序合并
func MashupWithDebugHandler(origin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// 检查请求的 URL 路径是否以 _debugPrefix 开头
		if strings.HasPrefix(req.URL.Path, _debugPrefix) {
			// 如果是，则使用受保护的处理程序来处理请求
			rmux.ProtectedHandler(globalService).ServeHTTP(w, req)
			return
		}
		// 如果不是，则使用原始处理程序来处理请求
		origin.ServeHTTP(w, req)
	})
}

// Debuggable 接口定义了一个方法 DebugHandler，用于返回一个 http.Handler 接口的实现
type Debuggable interface {
	DebugHandler() http.Handler
}

// debugService 结构体定义了一个调试服务，包含了处理特定调试请求的函数和一个路由器
type debugService struct {
	// handlers 是一个映射，包含了处理特定调试请求的函数
	handlers map[string]http.HandlerFunc
	// mux 是一个路由器，用于处理调试请求的路由
	mux *mux.Router
}

// ServeHTTP 方法实现了 http.Handler 接口，用于处理 HTTP 请求
func (d *debugService) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 遍历 handlers 映射，查找与请求路径匹配的处理函数
	for path, handler := range d.handlers {
		// 如果找到匹配的路径，则调用相应的处理函数
		if path == req.URL.Path {
			handler(w, req)
			// 处理完成后返回，不再继续查找
			return
		}
	}
	// 如果没有找到匹配的处理函数，则使用默认的路由器处理请求
	d.mux.ServeHTTP(w, req)
}

// Register 方法用于注册一个可调试的服务
func (d *debugService) Register(name string, debuggable Debuggable) {
	// 使用 path 包的 Join 函数将 _debugPrefix 和 name 拼接成一个路径
	path := path.Join(_debugPrefix, name)
	// 使用 mux 路由器的 PathPrefix 方法注册一个路径前缀处理器
	d.mux.PathPrefix(path).Handler(debuggable.DebugHandler())
	// 使用 log 包的 Infof 函数记录一条信息，表明已经注册了一个调试服务
	log.Infof("register debug: %s", path)
}
