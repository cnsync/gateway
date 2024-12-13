package client

import (
	"io"
	"net/http"
	"time"

	"github.com/cnsync/gateway/middleware"
	"github.com/cnsync/kratos/selector"
)

// client 结构体定义了一个客户端，用于发送 HTTP 请求和管理服务节点
type client struct {
	// applier 是一个节点应用程序，用于应用节点变更
	applier *nodeApplier
	// selector 是一个选择器，用于选择服务节点
	selector selector.Selector
}

// Client 接口定义了一个客户端，它继承自 http.RoundTripper 和 io.Closer 接口
type Client interface {
	http.RoundTripper
	io.Closer
}

// newClient 函数用于创建一个新的客户端实例
func newClient(applier *nodeApplier, selector selector.Selector) *client {
	return &client{
		applier:  applier,
		selector: selector,
	}
}

// Close 方法用于关闭客户端并取消节点应用程序
func (c *client) Close() error {
	// 取消节点应用程序
	c.applier.Cancel()
	return nil
}

func (c *client) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// 获取请求的上下文
	ctx := req.Context()
	// 从请求上下文中获取请求选项
	reqOpt, _ := middleware.FromRequestContext(ctx)
	// 从请求上下文中获取选择器过滤器
	filter, _ := middleware.SelectorFiltersFromContext(ctx)
	// 使用选择器选择一个节点，并获取一个完成函数和可能的错误
	n, done, err := c.selector.Select(ctx, selector.WithNodeFilter(filter...))
	// 如果发生错误，返回 nil 和错误
	if err != nil {
		return nil, err
	}
	// 将当前选择的节点设置到请求选项中
	reqOpt.CurrentNode = n

	// 获取选择的节点的地址
	addr := n.Address()
	// 将后端地址添加到请求选项的后端列表中
	reqOpt.Backends = append(reqOpt.Backends, addr)
	// 将选择的节点转换为具体的后端节点类型
	backendNode := n.(*node)
	// 设置请求的 URL 的主机和方案
	req.URL.Host = addr
	req.URL.Scheme = "http"
	// 如果后端节点启用了 TLS，则使用 HTTPS 方案
	if backendNode.tls {
		req.URL.Scheme = "https"
		req.Host = addr
	}
	// 如果节点元数据中存在 "host" 字段，则使用该字段作为请求的主机
	if nodeHost := n.Metadata()["host"]; nodeHost != "" {
		req.Host = nodeHost
	}
	// 重置请求 URI，因为它在发送请求时不需要
	req.RequestURI = ""

	// 记录请求开始时间
	startAt := time.Now()
	// 使用后端节点的客户端发送请求，并获取响应和可能的错误
	resp, err = backendNode.client.Do(req)
	// 计算并记录上游响应时间
	reqOpt.UpstreamResponseTime = append(reqOpt.UpstreamResponseTime, time.Since(startAt).Seconds())
	// 如果发生错误，调用完成函数并返回 nil 和错误
	if err != nil {
		done(ctx, selector.DoneInfo{Err: err})
		reqOpt.UpstreamStatusCode = append(reqOpt.UpstreamStatusCode, 0)
		return nil, err
	}
	// 记录上游状态码
	reqOpt.UpstreamStatusCode = append(reqOpt.UpstreamStatusCode, resp.StatusCode)
	// 将完成函数设置到请求选项中
	reqOpt.DoneFunc = done
	// 返回响应和 nil 错误
	return resp, nil
}
