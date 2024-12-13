package middleware

import (
	"errors"
	"strings"

	configv1 "github.com/cnsync/gateway/api/gateway/config/v1"
	"github.com/cnsync/kratos/log"
	"github.com/prometheus/client_golang/prometheus"
)

// LOG 定义一个日志记录器，用于记录中间件相关的日志信息
var LOG = log.NewHelper(log.With(log.GetLogger(), "source", "middleware"))

// 定义一个全局的中间件注册器
var globalRegistry = NewRegistry()

// 定义一个 Prometheus 计数器，用于统计创建中间件失败的次数
var _failedMiddlewareCreate = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "go",
	Subsystem: "gateway",
	Name:      "failed_middleware_create",
	Help:      "The total number of failed middleware create",
}, []string{"name", "required"})

// 在包初始化时注册 Prometheus 计数器
func init() {
	prometheus.MustRegister(_failedMiddlewareCreate)
}

// ErrNotFound 定义一个错误类型，表示未找到中间件
var ErrNotFound = errors.New("middleware has not been registered")

// Registry 是一个接口，用于调用者获取已注册的中间件
type Registry interface {
	Register(name string, factory Factory)
	RegisterV2(name string, factory FactoryV2)
	Create(cfg *configv1.Middleware) (MiddlewareV2, error)
}

// middlewareRegistry 是 Registry 接口的一个实现
type middlewareRegistry struct {
	middleware map[string]FactoryV2
}

// NewRegistry 创建一个新的中间件注册器实例
func NewRegistry() Registry {
	return &middlewareRegistry{
		middleware: map[string]FactoryV2{},
	}
}

// Register 注册单个中间件
func (p *middlewareRegistry) Register(name string, factory Factory) {
	// 将中间件名称转换为全小写，并添加前缀 "gateway.middleware."
	p.middleware[createFullName(name)] = wrapFactory(factory)
}

func (p *middlewareRegistry) RegisterV2(name string, factory FactoryV2) {
	// 将中间件名称转换为全小写，并添加前缀 "gateway.middleware."
	p.middleware[createFullName(name)] = factory
}

// Create 方法根据传入的配置对象 cfg 创建一个中间件实例
func (p *middlewareRegistry) Create(cfg *configv1.Middleware) (MiddlewareV2, error) {
	// 调用 getMiddleware 方法获取中间件工厂函数
	if method, ok := p.getMiddleware(createFullName(cfg.Name)); ok {
		// 判断该中间件是否为必需的
		if cfg.Required {
			// 如果中间件是必需的，则必须成功创建
			instance, err := method(cfg)
			if err != nil {
				// 记录创建必需中间件失败的次数
				_failedMiddlewareCreate.WithLabelValues(cfg.Name, "true").Inc()
				// 记录错误日志
				LOG.Errorw(log.DefaultMessageKey, "Failed to create required middleware", "reason", "create_required_middleware_failed", "name", cfg.Name, "error", err, "config", cfg)
				return nil, err
			}
			return instance, nil
		}
		// 尝试创建中间件实例
		instance, err := method(cfg)
		if err != nil {
			// 记录创建可选中间件失败的次数
			_failedMiddlewareCreate.WithLabelValues(cfg.Name, "false").Inc()
			// 记录错误日志
			LOG.Errorw(log.DefaultMessageKey, "Failed to create optional middleware", "reason", "create_optional_middleware_failed", "name", cfg.Name, "error", err, "config", cfg)
			return EmptyMiddleware, nil
		}
		return instance, nil
	}
	return nil, ErrNotFound
}

// getMiddleware 方法根据中间件名称获取对应的工厂函数
func (p *middlewareRegistry) getMiddleware(name string) (FactoryV2, bool) {
	// 将中间件名称转换为全小写
	nameLower := strings.ToLower(name)
	// 从 middleware 字典中获取对应的工厂函数
	middlewareFn, ok := p.middleware[nameLower]
	if ok {
		// 如果找到，返回工厂函数和 true
		return middlewareFn, true
	}
	// 如果未找到，返回 nil 和 false
	return nil, false
}

// createFullName 方法将中间件名称转换为全小写，并添加前缀 "gateway.middleware."
func createFullName(name string) string {
	return strings.ToLower("gateway.middleware." + name)
}

// Register 方法注册单个中间件
func Register(name string, factory Factory) {
	// 调用全局注册器的 Register 方法
	globalRegistry.Register(name, factory)
}

// RegisterV2 方法注册单个 v2 中间件
func RegisterV2(name string, factory FactoryV2) {
	// 调用全局注册器的 RegisterV2 方法
	globalRegistry.RegisterV2(name, factory)
}

// Create 方法根据传入的配置对象 cfg 创建一个中间件实例
func Create(cfg *configv1.Middleware) (MiddlewareV2, error) {
	// 调用全局注册器的 Create 方法
	return globalRegistry.Create(cfg)
}
