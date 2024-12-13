package client

import (
	"net/url"
	"strconv"
	"strings"
)

// Target 是解析的目标
type Target struct {
	// Scheme 表示目标的协议方案，例如 "http"、"https" 等
	Scheme string
	// Authority 表示目标的权威部分，通常是主机名或 IP 地址，可以包含端口号
	Authority string
	// Endpoint 表示目标的端点部分，通常是路径或资源标识符，例如 "/path/to/resource"
	Endpoint string
}

// parseTarget 解析目标端点
func parseTarget(endpoint string) (*Target, error) {
	// 如果端点不包含 "://"，则添加 "direct:///" 前缀
	if !strings.Contains(endpoint, "://") {
		endpoint = "direct:///" + endpoint
	}

	// 解析端点 URL
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	// 创建 Target 结构体实例
	target := &Target{Scheme: u.Scheme, Authority: u.Host}

	// 如果 URL 路径长度大于 1，则获取路径的第一个字符之后的部分作为 Endpoint
	if len(u.Path) > 1 {
		target.Endpoint = u.Path[1:]
	}

	return target, nil
}

// parseEndpoint 解析端点 URL
func parseEndpoint(endpoints []string, scheme string, isSecure bool) (string, error) {
	// 遍历端点列表
	for _, e := range endpoints {
		// 解析端点 URL
		u, err := url.Parse(e)
		if err != nil {
			return "", err
		}
		// 如果 URL 方案与给定方案匹配
		if u.Scheme == scheme {
			// 如果 URL 的 isSecure 查询参数与给定的 isSecure 值匹配
			if IsSecure(u) == isSecure {
				// 返回 URL 的主机部分
				return u.Host, nil
			}
		}
	}
	return "", nil
}

// IsSecure 检查 URL 是否安全
func IsSecure(u *url.URL) bool {
	// 从 URL 的查询参数中获取 isSecure 值
	ok, err := strconv.ParseBool(u.Query().Get("isSecure"))
	if err != nil {
		return false
	}
	return ok
}
