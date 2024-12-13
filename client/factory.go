package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/selector"
	"github.com/go-kratos/kratos/v2/selector/p2c"
)

// BuildContext 结构体定义了构建客户端所需的上下文信息
type BuildContext struct {
	// TLSConfigs 是一个映射，包含了多个 TLS 配置
	TLSConfigs map[string]*tls.Config
	// TLSClientStore 是一个 HTTPS 客户端存储
	TLSClientStore *HTTPSClientStore
}

// Factory 是一个函数类型，它接受 BuildContext 和 Endpoint 作为参数，并返回一个 Client 和一个 error
type Factory func(*BuildContext, *config.Endpoint) (Client, error)

// Option 是一个函数类型，它接受 options 结构体指针作为参数，可以用来设置一些选项
type Option func(*options)

// options 结构体定义了一些可选参数
type options struct {
	// pickerBuilder 是一个选择器构建器
	pickerBuilder selector.Builder
}

// WithPickerBuilder 函数返回一个 Option 函数，用于设置 pickerBuilder 选项
func WithPickerBuilder(in selector.Builder) Option {
	return func(o *options) {
		// 设置 pickerBuilder 选项
		o.pickerBuilder = in
	}
}

// EmptyBuildContext 函数返回一个空的 BuildContext 实例
func EmptyBuildContext() *BuildContext {
	return &BuildContext{}
}

// NewBuildContext 函数根据传入的网关配置创建一个构建上下文对象
func NewBuildContext(cfg *config.Gateway) *BuildContext {
	// 创建一个 map 用于存储 TLS 配置
	tlsConfigs := make(map[string]*tls.Config, len(cfg.TlsStore))
	// 遍历网关配置中的 TLS 存储
	for k, v := range cfg.TlsStore {
		// 创建一个新的 TLS 配置对象
		cfg := &tls.Config{
			// 设置是否跳过证书验证
			InsecureSkipVerify: v.Insecure,
			// 设置服务器名称
			ServerName: v.ServerName,
		}
		// 将证书和密钥转换为 TLS 证书对象
		cert, err := tls.X509KeyPair([]byte(v.Cert), []byte(v.Key))
		// 如果转换失败，记录错误并继续
		if err != nil {
			LOG.Warnf("failed to load tls cert: %q: %v", k, err)
			continue
		}
		// 将证书添加到 TLS 配置中
		cfg.Certificates = []tls.Certificate{cert}
		// 如果存在 CA 证书，将其添加到 TLS 配置中
		if v.Cacert != "" {
			// 创建一个新的证书池
			roots := x509.NewCertPool()
			// 将 CA 证书添加到证书池中
			if ok := roots.AppendCertsFromPEM([]byte(v.Cacert)); !ok {
				// 如果添加失败，记录错误并继续
				LOG.Warnf("failed to load tls cacert: %q", k)
				continue
			}
			// 将证书池设置为 TLS 配置的根证书
			cfg.RootCAs = roots
		}
		// 将 TLS 配置添加到 map 中
		tlsConfigs[k] = cfg
	}
	// 返回一个新的构建上下文对象
	return &BuildContext{
		// 设置 TLS 配置
		TLSConfigs: tlsConfigs,
		// 设置 HTTPS 客户端存储
		TLSClientStore: NewHTTPSClientStore(tlsConfigs),
	}
}

// NewFactory 函数创建一个客户端工厂，该工厂使用给定的注册发现服务和选项来构建客户端
func NewFactory(r registry.Discovery, opts ...Option) Factory {
	// 初始化选项结构体
	o := &options{
		// 默认使用 p2c 构建器来创建选择器
		pickerBuilder: p2c.NewBuilder(),
	}
	// 应用传入的选项函数，以修改默认选项
	for _, opt := range opts {
		opt(o)
	}
	// 返回一个工厂函数，该函数接受构建上下文和端点配置，并返回一个客户端实例和错误
	return func(builderCtx *BuildContext, endpoint *config.Endpoint) (Client, error) {
		// 使用选项中的构建器来创建选择器实例
		picker := o.pickerBuilder.Build()
		// 创建一个带有取消功能的上下文
		ctx, cancel := context.WithCancel(context.Background())
		// 创建一个节点应用程序实例，用于管理服务实例的选择和应用
		applier := &nodeApplier{
			// 初始化取消状态为未取消
			canceled: 0,
			// 存储构建上下文
			buildContext: builderCtx,
			// 存储取消函数，用于取消节点应用程序
			cancel: cancel,
			// 存储端点配置
			endpoint: endpoint,
			// 存储注册发现服务
			registry: r,
			// 存储选择器实例
			picker: picker,
		}
		// 应用节点变更，即注册服务实例并开始选择
		if err := applier.apply(ctx); err != nil {
			// 如果应用节点变更失败，返回错误
			return nil, err
		}
		// 创建一个新的客户端实例，使用节点应用程序和选择器
		client := newClient(applier, picker)
		// 返回客户端实例和 nil 错误
		return client, nil
	}
}

// nodeApplier 结构体定义了一个节点应用程序，用于管理和应用服务实例节点
type nodeApplier struct {
	// canceled 是一个原子整数，用于表示节点应用程序是否已被取消
	canceled int64
	// buildContext 是一个构建上下文对象，包含了构建客户端所需的信息
	buildContext *BuildContext
	// cancel 是一个上下文取消函数，用于取消节点应用程序
	cancel context.CancelFunc
	// endpoint 是一个端点配置对象，包含了服务端点的信息
	endpoint *config.Endpoint
	// registry 是一个注册发现服务对象，用于发现和注册服务实例
	registry registry.Discovery
	// picker 是一个选择器对象，用于选择服务实例节点
	picker selector.Selector
}

// apply 方法用于应用服务实例节点，它接受一个上下文对象作为参数，并返回一个错误
func (na *nodeApplier) apply(ctx context.Context) error {
	// 初始化一个节点列表
	var nodes []selector.Node
	// 遍历端点配置中的后端列表
	for _, backend := range na.endpoint.Backends {
		// 解析后端目标字符串，得到目标对象
		target, err := parseTarget(backend.Target)
		// 如果解析失败，返回错误
		if err != nil {
			return err
		}
		// 根据目标对象的方案类型，进行不同的处理
		switch target.Scheme {
		case "direct":
			// 对于直接方案，获取后端的权重值
			weighted := backend.Weight // weight is only valid for direct scheme
			// 创建一个新的节点对象，包含构建上下文、目标地址、协议、权重、元数据等信息
			node := newNode(na.buildContext, backend.Target, na.endpoint.Protocol, weighted, backend.Metadata, "", "", WithTLS(backend.Tls), WithTLSConfigName(backend.TlsConfigName))
			// 将新节点添加到节点列表中
			nodes = append(nodes, node)
			// 将节点列表应用到选择器中
			na.picker.Apply(nodes)
		case "discovery":
			// 对于发现方案，添加一个观察器，用于监视目标端点的服务实例变化
			existed := AddWatch(ctx, na.registry, target.Endpoint, na)
			// 如果观察器已经存在，记录一条信息
			if existed {
				log.Infof("watch target %+v already existed", target)
			}
		default:
			// 如果遇到未知的方案类型，返回一个错误
			return fmt.Errorf("unknown scheme: %s", target.Scheme)
		}
	}
	// 返回 nil，表示应用节点成功
	return nil
}

// _defaultWeight 定义了默认的权重值，当从服务实例的元数据中获取的权重值不存在或小于等于 0 时，将使用该默认值
var _defaultWeight = int64(10)

// nodeWeight 函数从服务实例的元数据中获取权重值，如果权重值不存在或小于等于 0，则返回默认权重值
func nodeWeight(n *registry.ServiceInstance) *int64 {
	w, ok := n.Metadata["weight"]
	if ok {
		val, _ := strconv.ParseInt(w, 10, 64)
		if val <= 0 {
			return &_defaultWeight
		}
		return &val
	}
	return &_defaultWeight
}

// Callback 方法是一个回调函数，当注册发现服务发现新的服务实例时，会调用这个方法
func (na *nodeApplier) Callback(services []*registry.ServiceInstance) error {
	// 检查节点应用程序是否已被取消
	if atomic.LoadInt64(&na.canceled) == 1 {
		return ErrCancelWatch
	}
	// 如果没有服务实例，则直接返回
	if len(services) == 0 {
		return nil
	}
	// 获取端点配置的协议，并转换为小写
	scheme := strings.ToLower(na.endpoint.Protocol.String())
	// 初始化一个节点列表
	nodes := make([]selector.Node, 0, len(services))
	// 遍历服务实例列表
	for _, ser := range services {
		// 解析服务实例的端点，获取地址
		addr, err := parseEndpoint(ser.Endpoints, scheme, false)
		// 如果解析失败或地址为空，则记录错误并继续
		if err != nil || addr == "" {
			log.Errorf("failed to parse endpoint: %v/%s: %v", ser.Endpoints, scheme, err)
			continue
		}
		// 创建一个新的节点对象，包含构建上下文、地址、协议、权重、元数据、版本和名称等信息
		node := newNode(na.buildContext, addr, na.endpoint.Protocol, nodeWeight(ser), ser.Metadata, ser.Version, ser.Name, WithTLS(false))
		// 将新节点添加到节点列表中
		nodes = append(nodes, node)
	}
	// 将节点列表应用到选择器中
	na.picker.Apply(nodes)
	// 返回 nil，表示回调成功
	return nil
}

// Cancel 方法用于取消节点应用程序，它会设置取消状态，并调用上下文的取消函数
func (na *nodeApplier) Cancel() {
	log.Infof("Closing node applier for endpoint: %+v", na.endpoint)
	atomic.StoreInt64(&na.canceled, 1)
	na.cancel()
}

// Canceled 方法用于检查节点应用程序是否已被取消
func (na *nodeApplier) Canceled() bool {
	return atomic.LoadInt64(&na.canceled) == 1
}
