package middleware

import (
	"context"

	config "github.com/cnsync/gateway/api/gateway/config/v1"
	"github.com/cnsync/kratos/selector"
)

type contextKey struct{}

// RequestOptions 是一个请求选项。
type RequestOptions struct {
	// Endpoint 是一个配置端点。
	Endpoint *config.Endpoint
	// Filters 是一个节点过滤器列表。
	Filters []selector.NodeFilter
	// Backends 是一个后端节点列表。
	Backends []string
	// Metadata 是一个元数据映射。
	Metadata map[string]string
	// UpstreamStatusCode 是一个上游状态码列表。
	UpstreamStatusCode []int
	// UpstreamResponseTime 是一个上游响应时间列表。
	UpstreamResponseTime []float64
	// CurrentNode 是当前选择的节点。
	CurrentNode selector.Node
	// DoneFunc 是一个完成函数。
	DoneFunc selector.DoneFunc
	// LastAttempt 表示是否是最后一次尝试。
	LastAttempt bool
	// Values 是一个请求值映射。
	Values RequestValues
}

type RequestValues interface {
	// Get 获取指定键的值。
	Get(key any) (any, bool)
	// Set 设置指定键的值。
	Set(key, val any)
}

// requestValues 是一个键值对映射，用于存储请求的相关值。
type requestValues map[any]any

// Get 方法用于获取指定键的值。
func (v requestValues) Get(key any) (any, bool) {
	// 尝试从映射中获取键对应的值，并返回值和是否存在的布尔值。
	val, ok := v[key]
	return val, ok
}

// Set 方法用于设置指定键的值。
func (v requestValues) Set(key, val any) {
	// 将给定的值设置到映射中对应键的位置。
	v[key] = val
}

// MetricsLabels 接口定义了一组方法，用于获取与度量相关的标签信息。
type MetricsLabels interface {
	// Protocol 方法返回协议名称。
	Protocol() string
	// Method 方法返回请求方法。
	Method() string
	// Path 方法返回请求路径。
	Path() string
	// Service 方法返回服务名称。
	Service() string
	// BasePath 方法返回基础路径。
	BasePath() string
}

// metricsLabels 结构体实现了 MetricsLabels 接口，用于存储和提供度量标签信息。
type metricsLabels struct {
	// endpoint 字段存储了与度量标签相关的端点配置。
	endpoint *config.Endpoint
}

// Protocol 方法返回端点配置中的协议名称。
func (m *metricsLabels) Protocol() string { return m.endpoint.Protocol.String() }

// Method 方法返回端点配置中的请求方法。
func (m *metricsLabels) Method() string { return m.endpoint.Method }

// Path 方法返回端点配置中的请求路径。
func (m *metricsLabels) Path() string { return m.endpoint.Path }

// Service 方法返回端点配置中的服务名称。
func (m *metricsLabels) Service() string { return m.endpoint.Metadata["service"] }

// BasePath 方法返回端点配置中的基础路径。
func (m *metricsLabels) BasePath() string { return m.endpoint.Metadata["basePath"] }

// AllLabels 方法返回一个包含所有度量标签的映射。
func (m *metricsLabels) AllLabels() map[string]string {
	// 创建一个新的映射来存储所有标签。
	return map[string]string{
		// 遍历每个标签方法，将其返回值作为键值对添加到映射中。
		"protocol": m.Protocol(),
		"method":   m.Method(),
		"path":     m.Path(),
		"service":  m.Service(),
		"basePath": m.BasePath(),
	}
}

// NewRequestOptions 函数用于创建一个新的请求选项对象，并带有重试过滤器。
func NewRequestOptions(c *config.Endpoint) *RequestOptions {
	// 创建一个新的 RequestOptions 对象 o
	o := &RequestOptions{
		// 配置端点
		Endpoint: c,
		// 初始化后端节点列表，容量为 1
		Backends: make([]string, 0, 1),
		// 初始化元数据映射
		Metadata: make(map[string]string),
		// 完成函数，目前为空函数
		DoneFunc: func(ctx context.Context, di selector.DoneInfo) {},
		// 初始化请求值映射，容量为 5
		Values: make(requestValues, 5),
	}

	// 初始化过滤器列表，目前只有一个重试过滤器
	o.Filters = []selector.NodeFilter{func(ctx context.Context, nodes []selector.Node) []selector.Node {
		// 如果后端节点列表为空，则直接返回所有节点
		if len(o.Backends) == 0 {
			return nodes
		}

		// 创建一个 map 用于存储选中的后端节点
		selected := make(map[string]struct{}, len(o.Backends))
		// 遍历后端节点列表，将每个节点的地址加入选中 map
		for _, b := range o.Backends {
			selected[b] = struct{}{}
		}

		// 创建一个新的节点列表，用于存储过滤后的节点
		newNodes := nodes[:0]
		// 遍历所有节点，将未被选中的节点加入新节点列表
		for _, node := range nodes {
			if _, ok := selected[node.Address()]; !ok {
				newNodes = append(newNodes, node)
			}
		}

		// 如果新节点列表为空，则返回原始节点列表
		if len(newNodes) == 0 {
			return nodes
		}

		// 返回过滤后的节点列表
		return newNodes
	}}

	// 返回创建的 RequestOptions 对象
	return o
}

// NewRequestContext 返回一个新的 Context，其中携带了值。
func NewRequestContext(ctx context.Context, o *RequestOptions) context.Context {
	// 使用 context.WithValue 函数将 RequestOptions 存储在 Context 中
	return context.WithValue(ctx, contextKey{}, o)
}

// FromRequestContext 从 Context 中提取 RequestOptions。
func FromRequestContext(ctx context.Context) (*RequestOptions, bool) {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，返回 RequestOptions 和 true
		return o, true
	}
	// 如果获取失败，返回 nil 和 false
	return nil, false
}

// EndpointFromContext 从 Context 中提取 Endpoint 配置。
func EndpointFromContext(ctx context.Context) (*config.Endpoint, bool) {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，返回 Endpoint 配置和 true
		return o.Endpoint, true
	}
	// 如果获取失败，返回 nil 和 false
	return nil, false
}

// RequestBackendsFromContext 从 Context 中提取后端节点列表。
func RequestBackendsFromContext(ctx context.Context) ([]string, bool) {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，返回后端节点列表和 true
		return o.Backends, true
	}
	// 如果获取失败，返回 nil 和 false
	return nil, false
}

// WithRequestBackends 将后端节点列表添加到 Context 中。
func WithRequestBackends(ctx context.Context, backend ...string) context.Context {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，将后端节点列表添加到 RequestOptions 中
		o.Backends = append(o.Backends, backend...)
	}
	// 返回更新后的 Context
	return ctx
}

// SelectorFiltersFromContext 从 Context 中提取选择器过滤器列表。
func SelectorFiltersFromContext(ctx context.Context) ([]selector.NodeFilter, bool) {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，返回选择器过滤器列表和 true
		return o.Filters, true
	}
	// 如果获取失败，返回 nil 和 false
	return nil, false
}

// WithSelectorFitler 将选择器过滤器添加到 Context 中。
func WithSelectorFitler(ctx context.Context, fn selector.NodeFilter) context.Context {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，将选择器过滤器添加到 RequestOptions 中
		o.Filters = append(o.Filters, fn)
	}
	// 返回更新后的 Context
	return ctx
}

// MetricsLabelsFromContext 从 Context 中提取度量标签。
func MetricsLabelsFromContext(ctx context.Context) (MetricsLabels, bool) {
	// 尝试从 Context 中获取 RequestOptions
	o, ok := ctx.Value(contextKey{}).(*RequestOptions)
	if ok {
		// 如果获取成功，创建并返回度量标签和 true
		return NewMetricsLabels(o.Endpoint), true
	}
	// 如果获取失败，返回 nil 和 false
	return nil, false
}

// NewMetricsLabels 根据 Endpoint 配置创建新的度量标签。
func NewMetricsLabels(ep *config.Endpoint) MetricsLabels {
	// 创建并返回一个新的 metricsLabels 实例
	return &metricsLabels{endpoint: ep}
}
