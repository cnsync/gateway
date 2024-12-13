package proxy

import (
	"context"
	"net/http"
	"time"

	"github.com/go-kratos/feature"
	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/proxy/condition"
)

var (
	// retryFeature 是一个注册的功能标志，用于表示是否启用重试功能
	retryFeature = feature.MustRegister("gw:Retry", true)
)

// retryStrategy 结构体定义了一个重试策略，包括尝试次数、总超时时间、每次尝试的超时时间和重试条件
type retryStrategy struct {
	// attempts 是重试尝试的总次数
	attempts int
	// timeout 是重试策略的总超时时间
	timeout time.Duration
	// perTryTimeout 是每次重试尝试的超时时间
	perTryTimeout time.Duration
	// conditions 是重试条件的列表
	conditions []condition.Condition
}

// calcTimeout 函数用于计算给定端点的超时时间
func calcTimeout(endpoint *config.Endpoint) time.Duration {
	// 初始化超时时间为 0
	var timeout time.Duration
	// 如果端点配置中存在超时时间，则将其赋值给 timeout
	if endpoint.Timeout != nil {
		timeout = endpoint.Timeout.AsDuration()
	}
	// 如果超时时间小于等于 0，则将其设置为默认值 1 秒
	if timeout <= 0 {
		timeout = time.Second
	}
	// 返回计算得到的超时时间
	return timeout
}

// calcAttempts 函数用于计算重试策略中的尝试次数
func calcAttempts(endpoint *config.Endpoint) int {
	// 如果端点没有配置重试策略，则默认尝试次数为 1
	if endpoint.Retry == nil {
		return 1
	}
	// 如果端点的重试策略中尝试次数为 0，则默认尝试次数为 1
	if endpoint.Retry.Attempts == 0 {
		return 1
	}
	// 返回端点配置中指定的重试尝试次数
	return int(endpoint.Retry.Attempts)
}

// calcPerTryTimeout 函数用于计算重试策略中每次尝试的超时时间
func calcPerTryTimeout(endpoint *config.Endpoint) time.Duration {
	// 初始化每次尝试的超时时间为 0
	var perTryTimeout time.Duration
	// 如果端点配置中存在重试策略，并且重试策略中指定了每次尝试的超时时间，则将其赋值给 perTryTimeout
	if endpoint.Retry != nil && endpoint.Retry.PerTryTimeout != nil {
		perTryTimeout = endpoint.Retry.PerTryTimeout.AsDuration()
		// 如果端点配置中存在超时时间，则将其赋值给 perTryTimeout
	} else if endpoint.Timeout != nil {
		perTryTimeout = endpoint.Timeout.AsDuration()
	}
	// 如果每次尝试的超时时间小于等于 0，则将其设置为默认值 1 秒
	if perTryTimeout <= 0 {
		perTryTimeout = time.Second
	}
	// 返回计算得到的每次尝试的超时时间
	return perTryTimeout
}

// prepareRetryStrategy 函数用于准备一个重试策略
func prepareRetryStrategy(e *config.Endpoint) (*retryStrategy, error) {
	// 初始化一个重试策略对象
	strategy := &retryStrategy{
		// 计算重试尝试次数
		attempts: calcAttempts(e),
		// 计算总超时时间
		timeout: calcTimeout(e),
		// 计算每次尝试的超时时间
		perTryTimeout: calcPerTryTimeout(e),
	}
	// 解析重试条件
	conditions, err := parseRetryConditon(e)
	// 如果解析失败，返回错误
	if err != nil {
		return nil, err
	}
	// 设置重试条件
	strategy.conditions = conditions
	// 返回重试策略和 nil 错误，表示成功
	return strategy, nil
}

// parseRetryConditon 函数用于解析端点配置中的重试条件
func parseRetryConditon(endpoint *config.Endpoint) ([]condition.Condition, error) {
	// 如果端点没有配置重试策略，则返回一个空的条件列表和 nil 错误
	if endpoint.Retry == nil {
		return []condition.Condition{}, nil
	}
	// 调用 condition 包中的 ParseConditon 函数解析重试条件
	return condition.ParseConditon(endpoint.Retry.Conditions...)
}

// judgeRetryRequired 函数用于判断是否需要根据给定的条件和响应进行重试
func judgeRetryRequired(conditions []condition.Condition, resp *http.Response) bool {
	// 调用 condition 包中的 JudgeConditons 函数判断是否需要重试
	return condition.JudgeConditons(conditions, resp, false)
}

// defaultAttemptTimeoutContext 函数用于创建一个带有默认超时时间的上下文
func defaultAttemptTimeoutContext(ctx context.Context, _ *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	// 使用 context 包中的 WithTimeout 函数创建一个带有指定超时时间的子上下文
	return context.WithTimeout(ctx, timeout)
}
