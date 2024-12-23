package consul

import (
	"net/url"

	"github.com/cnsync/gateway/discovery"
	"github.com/cnsync/kratos/contrib/registry/consul"
	"github.com/cnsync/kratos/registry"
	"github.com/hashicorp/consul/api"
)

func init() {
	discovery.Register("consul", New)
}

func New(dsn *url.URL) (registry.Discovery, error) {
	c := api.DefaultConfig()

	c.Address = dsn.Host
	token := dsn.Query().Get("token")
	if token != "" {
		c.Token = token
	}
	datacenter := dsn.Query().Get("datacenter")
	if datacenter != "" {
		c.Datacenter = datacenter
	}
	client, err := api.NewClient(c)
	if err != nil {
		return nil, err
	}
	return consul.New(client), nil
}
