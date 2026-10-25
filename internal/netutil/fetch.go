// Package netutil 提供带代理自动探测的 HTTP 能力。
//
// 探测顺序：
//  1. 环境变量 HTTPS_PROXY / HTTP_PROXY
//  2. 本机常见代理端口
//  3. 都没有 → 直连
//
// 为什么要探测而不是写死：同一个程序在「有代理的笔记本」和「直连的服务器」上都要能跑。
package netutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// UserAgent 会随每个请求发出；有些站点（如维基共享）会按 UA 拒绝默认客户端
const UserAgent = "little-robot/0.1 (+https://github.com/tiqio/little-robot)"

var proxyPorts = []int{7897, 1080, 1087}

var (
	proxyOnce sync.Once
	proxyURL  *url.URL
)

// Proxy 返回本次进程使用的代理；nil 表示直连。
// 用 sync.Once 缓存：探测一次最多 3×150ms，不能每个请求都探一遍。
func Proxy() *url.URL {
	proxyOnce.Do(func() { proxyURL = detectProxy() })
	return proxyURL
}

func detectProxy() *url.URL {
	// 1. 环境变量优先（CI / 服务器上通常这么配）
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(k); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				return u
			}
		}
	}

	// 2. 本机端口探测
	for _, port := range proxyPorts {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		conn, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err != nil {
			continue
		}
		conn.Close()
		return &url.URL{Scheme: "http", Host: addr}
	}
	return nil
}

// ProxyInfo 给人看的描述，打日志用
func ProxyInfo() string {
	if p := Proxy(); p != nil {
		return p.String()
	}
	return "direct"
}

// NewClient 返回配置好代理的 HTTP 客户端
func NewClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if p := Proxy(); p != nil {
		tr.Proxy = http.ProxyURL(p)
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// NewRequest 构造一个带标准头的请求（UA 必须带，否则维基等站点会 403）
func NewRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json, image/*;q=0.9, */*;q=0.8")
	return req, nil
}

// DoJSON 发送请求并返回响应体，失败时带上 HTTP 状态
func DoJSON(req *http.Request, maxBytes int64) ([]byte, error) {
	resp, err := NewClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败（代理：%s）：%w", ProxyInfo(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d（代理：%s）：%s", resp.StatusCode, ProxyInfo(), string(body))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("响应超过 %d 字节上限", maxBytes)
	}
	return data, nil
}

// FetchOpt 按站点定制请求头。
// 为什么要它：维基共享认"可识别的 UA"，而 nhentai 这类站在 Cloudflare 后面的
// 站点更认"浏览器 UA"，一个默认值伺候不了所有站。
type FetchOpt struct {
	// UA 覆盖默认 UserAgent；空 = 用 UserAgent
	UA string
	// Referer 可选来源页
	Referer string
	// MaxBytes 响应大小上限；0 = 默认 10MB
	MaxBytes int64
}

// Fetch 下载 URL，限制最大字节数，返回 body 和嗅探出的 Content-Type。
// 用 LimitReader 判断超限，不会先把超大响应整个读进内存。
func Fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	return FetchWith(ctx, rawURL, FetchOpt{MaxBytes: maxBytes})
}

// FetchWith 是 Fetch 的可定制版本（UA / Referer / 上限）
func FetchWith(ctx context.Context, rawURL string, opt FetchOpt) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("构造请求失败：%w", err)
	}
	// 维基共享等站点会拒绝 Go 默认的 "Go-http-client/1.1"（403），
	// 按 RFC 约定带一个可识别的 UA
	ua := opt.UA
	if ua == "" {
		ua = UserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, image/*;q=0.9, */*;q=0.8")
	if opt.Referer != "" {
		req.Header.Set("Referer", opt.Referer)
	}
	maxBytes := opt.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}

	resp, err := NewClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("请求失败（代理：%s）：%w", ProxyInfo(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d（代理：%s）", resp.StatusCode, ProxyInfo())
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("读取响应失败：%w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("响应超过 %d 字节上限", maxBytes)
	}

	// 自己嗅探，不信任服务端返回的 Content-Type（经常是错的）
	ctype := http.DetectContentType(data)
	return data, ctype, nil
}
