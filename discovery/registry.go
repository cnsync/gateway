package discovery

import (
	"fmt"
	"net/url"

	"github.com/go-kratos/kratos/v2/registry"
)

// globalRegistry 是一个全局的注册中心实例
var globalRegistry = NewRegistry()

// Factory 是一个工厂函数，用于创建发现服务实例
type Factory func(dsn *url.URL) (registry.Discovery, error)

// Registry 是一个接口，用于管理和创建发现服务
type Registry interface {
	Register(name string, factory Factory)
	Create(discoveryDSN string) (registry.Discovery, error)
}

// discoveryRegistry 是 Registry 接口的一个实现
type discoveryRegistry struct {
	discovery map[string]Factory
}

// NewRegistry 创建一个新的发现服务注册中心
func NewRegistry() Registry {
	return &discoveryRegistry{
		discovery: map[string]Factory{},
	}
}

// Register 注册一个发现服务工厂
func (d *discoveryRegistry) Register(name string, factory Factory) {
	d.discovery[name] = factory
}

// Create 根据给定的 DSN 创建一个发现服务实例
func (d *discoveryRegistry) Create(discoveryDSN string) (registry.Discovery, error) {
	if discoveryDSN == "" {
		return nil, fmt.Errorf("discoveryDSN is empty")
	}

	dsn, err := url.Parse(discoveryDSN)
	if err != nil {
		return nil, fmt.Errorf("parse discoveryDSN error: %s", err)
	}

	factory, ok := d.discovery[dsn.Scheme]
	if !ok {
		return nil, fmt.Errorf("discovery %s has not been registered", dsn.Scheme)
	}

	impl, err := factory(dsn)
	if err != nil {
		return nil, fmt.Errorf("create discovery error: %s", err)
	}
	return impl, nil
}

// Register 注册一个发现服务
func Register(name string, factory Factory) {
	globalRegistry.Register(name, factory)
}

// Create 根据给定的 DSN 创建一个发现服务实例
func Create(discoveryDSN string) (registry.Discovery, error) {
	return globalRegistry.Create(discoveryDSN)
}
