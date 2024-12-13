package client

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/selector"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http2"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/middleware"
)

// 定义一个空的 node 结构体实例，用于实现 selector.Node 接口
var _ selector.Node = &node{}

// 定义一个全局的 HTTP 客户端实例，使用默认配置
var _globalClient = defaultClient()

// 定义一个全局的 HTTP/2 客户端实例，允许 HTTP 升级到 HTTP/2
var _globalH2CClient = defaultH2CClient()

// 定义一个全局的 HTTPS 客户端实例，使用默认的 TLS 配置
var _globalHTTPSClient = createHTTPSClient(nil)

// 定义一个全局的拨号超时时间，默认值为 200 毫秒
var _dialTimeout = 200 * time.Millisecond

// 定义一个全局变量，指示是否跟随重定向，默认值为 false
var followRedirect = false

// 初始化函数，在程序启动时自动调用
func init() {
	var err error
	// 尝试从环境变量中获取拨号超时时间，如果获取成功则更新全局变量
	if v := os.Getenv("PROXY_DIAL_TIMEOUT"); v != "" {
		if _dialTimeout, err = time.ParseDuration(v); err != nil {
			// 如果解析失败，抛出 panic
			panic(err)
		}
	}
	// 尝试从环境变量中获取是否跟随重定向的配置，如果获取成功则更新全局变量
	if val := os.Getenv("PROXY_FOLLOW_REDIRECT"); val != "" {
		followRedirect = true
	}
	// 注册一个 Prometheus 计数器，用于统计客户端重定向的总数
	prometheus.MustRegister(_metricClientRedirect)
}

// 定义一个 Prometheus 计数器，用于统计客户端重定向的总数
var _metricClientRedirect = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "go",
	Subsystem: "gateway",
	Name:      "client_redirect_total",
	Help:      "The total number of client redirect",
}, []string{"protocol", "method", "path", "service", "basePath"})

// 默认的重定向检查函数，用于在客户端发起请求时检查是否需要重定向
func defaultCheckRedirect(req *http.Request, via []*http.Request) error {
	// 从请求上下文中获取指标标签，如果获取成功则更新计数器
	labels, ok := middleware.MetricsLabelsFromContext(req.Context())
	if ok {
		_metricClientRedirect.WithLabelValues(labels.Protocol(), labels.Method(), labels.Path(), labels.Service(), labels.BasePath()).Inc()
	}
	// 如果全局变量 followRedirect 为 true，则跟随重定向
	if followRedirect {
		if len(via) >= 10 {
			// 如果重定向次数超过 10 次，则返回错误
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	// 如果不跟随重定向，则返回错误
	return http.ErrUseLastResponse
}

// defaultClient 函数创建一个默认的 HTTP 客户端实例
func defaultClient() *http.Client {
	return &http.Client{
		// 设置重定向检查函数
		CheckRedirect: defaultCheckRedirect,
		// 创建一个 HTTP 传输实例
		Transport: &http.Transport{
			// 设置代理，从环境变量中获取
			Proxy: http.ProxyFromEnvironment,
			// 设置拨号上下文，使用自定义的拨号器
			DialContext: (&net.Dialer{
				// 设置拨号超时时间
				Timeout: _dialTimeout,
				// 设置保持活动的超时时间
				KeepAlive: 30 * time.Second,
			}).DialContext,
			// 设置最大空闲连接数
			MaxIdleConns: 10000,
			// 设置每个主机的最大空闲连接数
			MaxIdleConnsPerHost: 1000,
			// 设置每个主机的最大连接数
			MaxConnsPerHost: 1000,
			// 禁用压缩
			DisableCompression: true,
			// 设置空闲连接超时时间
			IdleConnTimeout: 90 * time.Second,
			// 设置 TLS 握手超时时间
			TLSHandshakeTimeout: 10 * time.Second,
			// 设置预期继续超时时间
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// defaultH2CClient 函数创建一个默认的 HTTP/2 客户端实例，该实例允许 HTTP 升级到 HTTP/2
func defaultH2CClient() *http.Client {
	return &http.Client{
		// 设置重定向检查函数
		CheckRedirect: defaultCheckRedirect,
		// 创建一个 HTTP/2 传输实例
		Transport: &http2.Transport{
			// 允许 HTTP 协议
			AllowHTTP: true,
			// 禁用压缩
			DisableCompression: true,
			// 自定义的 DialTLS 函数，用于处理非 TLS 连接
			DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
				// 忽略传入的 TLS 配置，直接使用网络和地址进行拨号
				return net.DialTimeout(network, addr, _dialTimeout)
			},
		},
	}
}

// createHTTPSClient 函数根据传入的 TLS 配置创建一个新的 HTTP 客户端实例
func createHTTPSClient(tlsConfig *tls.Config) *http.Client {
	// 创建一个 HTTP 传输实例
	tr := &http.Transport{
		// 设置 TLS 客户端配置
		TLSClientConfig: tlsConfig,
		// 设置代理，从环境变量中获取
		Proxy: http.ProxyFromEnvironment,
		// 设置拨号上下文，使用自定义的拨号器
		DialContext: (&net.Dialer{
			// 设置拨号超时时间
			Timeout: _dialTimeout,
			// 设置保持活动的超时时间
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// 设置最大空闲连接数
		MaxIdleConns: 10000,
		// 设置每个主机的最大空闲连接数
		MaxIdleConnsPerHost: 1000,
		// 设置每个主机的最大连接数
		MaxConnsPerHost: 1000,
		// 禁用压缩
		DisableCompression: true,
		// 设置空闲连接超时时间
		IdleConnTimeout: 90 * time.Second,
		// 设置 TLS 握手超时时间
		TLSHandshakeTimeout: 10 * time.Second,
		// 设置预期继续超时时间
		ExpectContinueTimeout: 1 * time.Second,
	}
	// 配置 HTTP/2 传输
	_ = http2.ConfigureTransport(tr)
	// 创建一个 HTTP 客户端实例
	return &http.Client{
		// 设置重定向检查函数
		CheckRedirect: defaultCheckRedirect,
		// 设置传输实例
		Transport: tr,
	}
}

// HTTPSClientStore 结构体定义了一个存储 HTTPS 客户端的仓库
type HTTPSClientStore struct {
	// 存储客户端配置的映射，键为配置名称，值为 TLS 配置
	clientConfigs map[string]*tls.Config
	// 存储客户端实例的映射，键为客户端名称，值为 HTTP 客户端实例
	clients map[string]*http.Client
}

// NewHTTPSClientStore 函数创建一个新的 HTTPSClientStore 实例
func NewHTTPSClientStore(clientConfigs map[string]*tls.Config) *HTTPSClientStore {
	return &HTTPSClientStore{
		// 初始化客户端配置映射
		clientConfigs: clientConfigs,
		// 初始化客户端实例映射
		clients: make(map[string]*http.Client),
	}
}

// GetClient 方法根据名称获取一个 HTTP 客户端实例
func (s *HTTPSClientStore) GetClient(name string) *http.Client {
	// 如果名称为空，则返回默认的全局客户端
	if name == "" {
		return _globalClient
	}
	// 尝试从客户端实例映射中获取客户端
	client, ok := s.clients[name]
	if ok {
		return client
	}
	// 尝试从客户端配置映射中获取 TLS 配置
	tlsConfig, ok := s.clientConfigs[name]
	if !ok {
		// 如果未找到配置，则记录警告并返回默认的全局 HTTPS 客户端
		LOG.Warnf("tls config not found for %s, using default instead", name)
		return _globalHTTPSClient
	}
	// 根据 TLS 配置创建一个新的 HTTP 客户端实例
	client = createHTTPSClient(tlsConfig)
	// 将新创建的客户端实例存储在客户端实例映射中
	s.clients[name] = client
	// 返回获取到的客户端实例
	return client
}

// NodeOptions 结构体定义了节点的 TLS 配置选项
type NodeOptions struct {
	// TLS 字段表示是否启用 TLS 加密
	TLS bool
	// TLSConfigName 字段表示 TLS 配置的名称
	TLSConfigName string
}

// NewNodeOption 是一个函数类型，它接受一个 NodeOptions 类型的指针参数，并返回一个 NodeOptions 类型的指针
type NewNodeOption func(*NodeOptions)

// WithTLS 函数返回一个 NewNodeOption 类型的函数，该函数设置 NodeOptions 结构体的 TLS 字段为传入的布尔值
func WithTLS(in bool) NewNodeOption {
	return func(o *NodeOptions) {
		o.TLS = in
	}
}

// WithTLSConfigName 函数返回一个 NewNodeOption 类型的函数，该函数设置 NodeOptions 结构体的 TLSConfigName 字段为传入的字符串
func WithTLSConfigName(in string) NewNodeOption {
	return func(o *NodeOptions) {
		o.TLSConfigName = in
	}
}

// newNode 函数根据传入的参数创建一个新的 node 结构体实例
func newNode(ctx *BuildContext, addr string, protocol config.Protocol, weight *int64, md map[string]string, version string, name string, opts ...NewNodeOption) *node {
	// 创建一个新的 node 结构体实例
	node := &node{
		// 设置协议
		protocol: protocol,
		// 设置地址
		address: addr,
		// 设置权重
		weight: weight,
		// 设置元数据
		metadata: md,
		// 设置版本
		version: version,
		// 设置名称
		name: name,
	}
	// 根据协议类型设置默认的 HTTP 客户端
	node.client = _globalClient
	if protocol == config.Protocol_GRPC {
		node.client = _globalH2CClient
	}
	// 初始化 NodeOptions 结构体
	opt := &NodeOptions{}
	// 遍历传入的选项函数，应用每个选项
	for _, o := range opts {
		o(opt)
	}
	// 如果启用了 TLS，则设置 TLS 相关属性
	if opt.TLS {
		node.tls = true
		node.client = _globalHTTPSClient
		// 如果指定了 TLS 配置名称，则从 BuildContext 中获取对应的客户端
		if opt.TLSConfigName != "" {
			node.client = ctx.TLSClientStore.GetClient(opt.TLSConfigName)
		}
	}
	// 返回新创建的 node 结构体实例
	return node
}

// node 结构体定义了一个服务实例的节点信息
type node struct {
	// 节点的地址，通常是一个 URL 或 IP 地址
	address string
	// 服务的名称
	name string
	// 节点的权重，用于负载均衡
	weight *int64
	// 服务的版本号
	version string
	// 节点的元数据，包含一些额外的信息
	metadata map[string]string

	// 用于与该节点通信的 HTTP 客户端
	client *http.Client
	// 节点的协议类型，如 HTTP 或 HTTPS
	protocol config.Protocol
	// 是否启用 TLS 加密
	tls bool
}

// Scheme 方法返回节点的协议方案，将协议字符串转换为小写形式
func (n *node) Scheme() string {
	return strings.ToLower(n.protocol.String())
}

// Address 方法返回节点的地址
func (n *node) Address() string {
	return n.address
}

// ServiceName 方法返回服务的名称
func (n *node) ServiceName() string {
	return n.name
}

// InitialWeight 方法返回节点的初始权重，如果未设置则返回 nil
func (n *node) InitialWeight() *int64 {
	return n.weight
}

// Version 方法返回节点的版本信息
func (n *node) Version() string {
	return n.version
}

// Metadata 方法返回节点的元数据，这是一个键值对映射，包含了与服务实例相关的额外信息
func (n *node) Metadata() map[string]string {
	return n.metadata
}
