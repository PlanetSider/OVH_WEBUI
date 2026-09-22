package ovh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// ValidateProxyURL 允许账户级 HTTP(S)/SOCKS5 代理；空值表示明确使用直连。
// 不接受其它 scheme，也不接受带 fragment 的 URL，避免把凭据或控制信息
// 混入不可预期的代理实现。
func ValidateProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid proxy URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	if u.Fragment != "" {
		return fmt.Errorf("proxy URL must not contain fragment")
	}
	return nil
}

// ValidateFingerprint 只允许可验证的用户代理指纹格式。当前不伪造 TLS
// ClientHello 指纹，避免把不可验证的浏览器指纹字符串误当作安全能力。
func ValidateFingerprint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "ua:") || strings.TrimSpace(strings.TrimPrefix(raw, "ua:")) == "" {
		return fmt.Errorf("unsupported fingerprint format")
	}
	return nil
}

func FingerprintUserAgent(raw string) string {
	if !strings.HasPrefix(strings.TrimSpace(raw), "ua:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "ua:"))
}

// ProxyHealthReporter 接收账户级代理链路健康事件；它不参与请求路径，
// 因此代理状态机故障不会改变 OVH client 的错误语义。
type ProxyHealthReporter interface {
	ReportFailure(accountID string, cause error)
	ReportSuccess(accountID string)
}

func withProxyHealthReporter(client *http.Client, accountID, proxyURL string, reporter ProxyHealthReporter) *http.Client {
	if client == nil || strings.TrimSpace(proxyURL) == "" || reporter == nil || client.Transport == nil {
		return client
	}
	client.Transport = observedRoundTripper{
		accountID: accountID,
		next:      client.Transport,
		reporter:  reporter,
	}
	return client
}

type observedRoundTripper struct {
	accountID string
	next      http.RoundTripper
	reporter  ProxyHealthReporter
}

func (t observedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.reporter.ReportFailure(t.accountID, err)
		}
		return response, err
	}
	if response != nil && response.StatusCode == http.StatusProxyAuthRequired {
		t.reporter.ReportFailure(t.accountID, fmt.Errorf("proxy authentication required"))
	} else {
		t.reporter.ReportSuccess(t.accountID)
	}
	return response, nil
}

func (t observedRoundTripper) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// BuildHTTPClient 返回账户专属 HTTP client。配置代理时只安装该代理，
// 不设置任何自动直连回退；代理连接失败由调用方报告给 proxyguard。
func BuildHTTPClient(proxyURL, fingerprint string, timeout time.Duration) (*http.Client, error) {
	if err := ValidateProxyURL(proxyURL); err != nil {
		return nil, err
	}
	if err := ValidateFingerprint(fingerprint); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	if strings.TrimSpace(proxyURL) != "" {
		u, _ := url.Parse(strings.TrimSpace(proxyURL))
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			transport.Proxy = http.ProxyURL(u)
		case "socks5", "socks5h":
			var auth *xproxy.Auth
			if u.User != nil {
				password, _ := u.User.Password()
				auth = &xproxy.Auth{User: u.User.Username(), Password: password}
			}
			dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
			if err != nil {
				return nil, fmt.Errorf("build SOCKS5 proxy: %w", err)
			}
			if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
				transport.DialContext = contextDialer.DialContext
			} else {
				transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
					return dialer.Dial(network, address)
				}
			}
		}
	}
	if fp := strings.TrimSpace(fingerprint); fp != "" && (!strings.HasPrefix(fp, "ua:") || strings.TrimSpace(strings.TrimPrefix(fp, "ua:")) == "") {
		return nil, fmt.Errorf("unsupported fingerprint format")
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
