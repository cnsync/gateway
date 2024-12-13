package transcoder

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"strings"

	config "github.com/cnsync/gateway/api/gateway/config/v1"
	"github.com/cnsync/gateway/middleware"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// decodeBinHeader 解码 base64 编码的二进制数据
func decodeBinHeader(v string) ([]byte, error) {
	// 如果输入字符串的长度是 4 的倍数，则直接使用标准 base64 解码
	if len(v)%4 == 0 {
		return base64.StdEncoding.DecodeString(v)
	}
	// 如果输入字符串的长度不是 4 的倍数，则使用 raw 标准 base64 解码
	return base64.RawStdEncoding.DecodeString(v)
}

// newResponse 创建一个新的 HTTP 响应
func newResponse(statusCode int, header http.Header, data []byte) (*http.Response, error) {
	// 创建一个新的 HTTP 响应对象
	return &http.Response{
		// 设置响应头
		Header: header,
		// 设置状态码
		StatusCode: statusCode,
		// 设置内容长度
		ContentLength: int64(len(data)),
		// 设置响应体
		Body: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

// 包初始化时注册 transcoder 中间件
func init() {
	// 使用 middleware 包的 Register 函数注册 transcoder 中间件
	middleware.Register("transcoder", Middleware)
}

// Middleware 函数根据传入的配置对象 c 创建一个中间件实例
func Middleware(c *config.Middleware) (middleware.Middleware, error) {
	// 返回一个函数，该函数接受一个 http.RoundTripper 并返回一个新的 http.RoundTripper
	return func(next http.RoundTripper) http.RoundTripper {
		// 返回一个 RoundTripperFunc，它是 http.RoundTripper 的一个实现
		return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// 获取请求的上下文
			ctx := req.Context()
			// 获取请求的 Content-Type 头
			contentType := req.Header.Get("Content-Type")
			// 从上下文中获取端点信息
			endpoint, _ := middleware.EndpointFromContext(ctx)
			// 如果端点协议不是 gRPC 或者 Content-Type 不是以 application/grpc 开头，则直接返回
			if endpoint.Protocol != config.Protocol_GRPC || strings.HasPrefix(contentType, "application/grpc") {
				return next.RoundTrip(req)
			}
			// 读取请求体
			b, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			// 创建一个新的字节数组，长度为请求体长度加 5
			bb := make([]byte, len(b)+5)
			// 将请求体长度转换为大端字节序并写入新数组的第 2 到第 5 个字节
			binary.BigEndian.PutUint32(bb[1:], uint32(len(b)))
			// 将请求体数据复制到新数组的第 6 个字节开始的位置
			copy(bb[5:], b)
			// 设置请求的 Content-Type 为 application/grpc+json 或 application/grpc+proto
			req.Header.Set("Content-Type", "application/grpc+"+strings.TrimLeft(contentType, "application/"))
			// 删除请求的 Content-Length 头
			req.Header.Del("Content-Length")
			// 设置请求的 ContentLength 为新数组的长度
			req.ContentLength = int64(len(bb))
			// 将请求体替换为新的字节数组
			req.Body = io.NopCloser(bytes.NewReader(bb))
			// 调用下一个中间件或最终的处理器
			resp, err := next.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			// 读取响应体
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			// 将 HTTP/2 响应转换为 HTTP/1.1
			// 因为 trailers 是在数据帧中发送的，所以不要宣布 trailers，否则下游代理可能会感到困惑
			for trailerName, values := range resp.Trailer {
				resp.Header[trailerName] = values
			}
			resp.Trailer = nil
			// 恢复原始的 Content-Type
			resp.Header.Set("Content-Type", contentType)
			// 检查 grpc-status 头，如果不是 0，则表示有错误
			if grpcStatus := resp.Header.Get("grpc-status"); grpcStatus != "0" {
				// 将 grpc-status 转换为整数
				code, err := strconv.ParseInt(grpcStatus, 10, 64)
				if err != nil {
					return nil, err
				}
				// 创建一个新的 status 对象
				st := &spb.Status{
					Code:    int32(code),
					Message: resp.Header.Get("grpc-message"),
				}
				// 如果有 grpc-status-details-bin 头，则解码它
				if grpcDetails := resp.Header.Get("grpc-status-details-bin"); grpcDetails != "" {
					// 解码二进制头
					details, err := decodeBinHeader(grpcDetails)
					if err != nil {
						return nil, err
					}
					// 将解码后的细节合并到 status 对象中
					if err = proto.Unmarshal(details, st); err != nil {
						return nil, err
					}
				}
				// 将 status 对象序列化为 JSON
				data, err := protojson.Marshal(st)
				if err != nil {
					return nil, err
				}
				// 创建一个新的响应，状态码为 200，包含 JSON 数据
				return newResponse(200, resp.Header, data)
			}
			// 从响应数据中移除前 5 个字节
			resp.Body = io.NopCloser(bytes.NewReader(data[5:]))
			// 设置响应的 ContentLength 为移除前 5 个字节后的数据长度
			resp.ContentLength = int64(len(data) - 5)
			// 删除 Content-Length 头，因为 trailers 可能会影响长度
			resp.Header.Del("Content-Length")
			// 返回修改后的响应
			return resp, nil
		})
	}, nil
}
