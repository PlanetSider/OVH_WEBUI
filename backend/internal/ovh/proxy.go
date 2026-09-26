package ovh

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// FingerprintProfile 描述一个受控的出站请求 profile。它只承诺标准库可以
// 实际控制的 TLS 版本、ALPN、TLS 1.2 套件集合和请求头，不伪造完整 JA3。
type FingerprintProfile struct {
	Name           string
	UserAgent      string
	AcceptLanguage string
	HTTP2          bool
	MinTLS         uint16
	MaxTLS         uint16
	CipherSuites   []uint16
	Curves         []tls.CurveID
}

var fingerprintProfiles = map[string]FingerprintProfile{
	"default": {
		Name:  "default",
		HTTP2: true,
	},
	"chrome-like": {
		Name:           "chrome-like",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		AcceptLanguage: "en-US,en;q=0.9",
		HTTP2:          true,
		MinTLS:         tls.VersionTLS12,
		MaxTLS:         tls.VersionTLS13,
	},
	"firefox-like": {
		Name:           "firefox-like",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
		AcceptLanguage: "en-US,en;q=0.5",
		HTTP2:          true,
		MinTLS:         tls.VersionTLS12,
		MaxTLS:         tls.VersionTLS13,
		Curves:         []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384, tls.CurveP521},
	},
	"legacy-http1": {
		Name:      "legacy-http1",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
		HTTP2:     false,
		MinTLS:    tls.VersionTLS12,
		MaxTLS:    tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
	},
}

// FingerprintProfileNames 返回稳定顺序的白名单，供 API/UI 使用。
func FingerprintProfileNames() []string {
	return []string{"default", "chrome-like", "firefox-like", "legacy-http1"}
}

// ResolveFingerprintProfile 将保存值解析为实际使用的 profile。
// 旧版 ua:... 值保留为兼容输入，但不再作为新 UI 的可选项。
func ResolveFingerprintProfile(raw string) (FingerprintProfile, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fingerprintProfiles["default"], ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "ua:") {
		ua := strings.TrimSpace(raw[3:])
		return FingerprintProfile{Name: "legacy-ua", UserAgent: ua, HTTP2: true}, "兼容旧版 ua: 指纹；建议迁移到命名 profile"
	}
	if profile, ok := fingerprintProfiles[strings.ToLower(raw)]; ok {
		return profile, ""
	}
	return fingerprintProfiles["default"], fmt.Sprintf("不认识的指纹配置 %q，已按 default 处理", raw)
}

func ValidateFingerprint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "ua:") {
		if strings.TrimSpace(raw[3:]) == "" {
			return fmt.Errorf("unsupported fingerprint format")
		}
		return nil
	}
	if _, ok := fingerprintProfiles[strings.ToLower(raw)]; !ok {
		return fmt.Errorf("unsupported fingerprint profile %q", raw)
	}
	return nil
}

func FingerprintUserAgent(raw string) string {
	profile, _ := ResolveFingerprintProfile(raw)
	return profile.UserAgent
}

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

// ProxyHealthReporter 接收账户级代理链路健康事件；它不参与请求路径，
// 因此代理状态机故障不会改变 OVH client 的错误语义。
type ProxyHealthReporter interface {
	ReportFailure(accountID string, cause error)
	ReportSuccess(accountID string)
}

// ScrubProxyURL 返回可写入日志/通知的代理地址，不保留认证信息、查询参数或片段。
func ScrubProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[configured]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

var proxyCredentialPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)

// ScrubProxyText 清理错误文本中的代理 URL 认证信息，并限制可外发长度。
func ScrubProxyText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = proxyCredentialPattern.ReplaceAllString(raw, `${1}[redacted]@`)
	if len(raw) > 240 {
		raw = raw[:240]
	}
	return raw
}

type ProxyError struct {
	Proxy string
	Err   error
}

func (e *ProxyError) Error() string {
	if e == nil {
		return "proxy request failed"
	}
	detail := ""
	if e.Err != nil {
		detail = ScrubProxyText(e.Err.Error())
	}
	if e.Proxy == "" {
		if detail == "" {
			return "proxy request failed"
		}
		return "proxy request failed: " + detail
	}
	if detail == "" {
		return fmt.Sprintf("proxy request failed (%s)", ScrubProxyURL(e.Proxy))
	}
	return fmt.Sprintf("proxy request failed (%s): %s", ScrubProxyURL(e.Proxy), detail)
}

func (e *ProxyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsProxyError 供 proxyguard 只统计代理层错误。
func IsProxyError(err error) bool {
	var proxyErr *ProxyError
	return errors.As(err, &proxyErr)
}

func withProxyHealthReporter(client *http.Client, accountID, proxyURL string, reporter ProxyHealthReporter) *http.Client {
	if client == nil || strings.TrimSpace(proxyURL) == "" || reporter == nil || client.Transport == nil {
		return client
	}
	client.Transport = observedRoundTripper{
		accountID: accountID,
		proxyURL:  ScrubProxyURL(proxyURL),
		next:      client.Transport,
		reporter:  reporter,
	}
	return client
}

type observedRoundTripper struct {
	accountID string
	proxyURL  string
	next      http.RoundTripper
	reporter  ProxyHealthReporter
}

func (t observedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.reporter.ReportFailure(t.accountID, &ProxyError{Proxy: t.proxyURL, Err: err})
		}
		return response, err
	}
	if response != nil && response.StatusCode == http.StatusProxyAuthRequired {
		t.reporter.ReportFailure(t.accountID, &ProxyError{Proxy: t.proxyURL, Err: fmt.Errorf("proxy authentication required")})
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
	profile, _ := ResolveFingerprintProfile(fingerprint)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tlsConfig := &tls.Config{
		MinVersion:       profile.MinTLS,
		MaxVersion:       profile.MaxTLS,
		CipherSuites:     append([]uint16(nil), profile.CipherSuites...),
		CurvePreferences: append([]tls.CurveID(nil), profile.Curves...),
	}
	if tlsConfig.MinVersion == 0 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	if !profile.HTTP2 {
		tlsConfig.NextProtos = []string{"http/1.1"}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     profile.HTTP2,
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
	var roundTripper http.RoundTripper = transport
	if profile.UserAgent != "" || profile.AcceptLanguage != "" {
		roundTripper = &fingerprintRoundTripper{next: roundTripper, profile: profile}
	}
	return &http.Client{Transport: roundTripper, Timeout: timeout}, nil
}

type fingerprintRoundTripper struct {
	next    http.RoundTripper
	profile FingerprintProfile
}

func (t *fingerprintRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if t.profile.UserAgent != "" && cloned.Header.Get("User-Agent") == "" {
		cloned.Header.Set("User-Agent", t.profile.UserAgent)
	}
	if t.profile.AcceptLanguage != "" && cloned.Header.Get("Accept-Language") == "" {
		cloned.Header.Set("Accept-Language", t.profile.AcceptLanguage)
	}
	return t.next.RoundTrip(cloned)
}

func (t *fingerprintRoundTripper) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// EgressIP 通过给定账户配置查询真实出口 IP。它创建独立 client，
// 不触发 proxyguard，也不改变任何业务状态。
func EgressIP(proxyURL, fingerprint string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client, err := BuildHTTPClient(proxyURL, fingerprint, timeout)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodGet, "https://api.ipify.org?format=text", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("通过该代理访问外网失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询出口 IP 返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		return "", fmt.Errorf("读取出口 IP 失败: %w", err)
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("返回的不是合法 IP: %q", ip)
	}
	return ip, nil
}

// ProxyProbe 描述一个账户代理诊断目标的多次采样结果。
type ProxyProbe struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	MinMS  int64  `json:"minMs,omitempty"`
	AvgMS  int64  `json:"avgMs,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

type ProxyProbeSpec struct {
	Name string
	URL  string
}

const proxyProbeSamples = 3

// ProbeProxyTarget 任何 HTTP 响应都算链路可达，避免把 401/404 误判为代理故障。
func ProbeProxyTarget(proxyURL, fingerprint string, timeout time.Duration, spec ProxyProbeSpec) ProxyProbe {
	result := ProxyProbe{Name: spec.Name, URL: spec.URL}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client, err := BuildHTTPClient(proxyURL, fingerprint, timeout)
	if err != nil {
		result.Error = "代理客户端创建失败"
		return result
	}
	var total time.Duration
	var minimum time.Duration
	successes := 0
	for i := 0; i < proxyProbeSamples; i++ {
		request, requestErr := http.NewRequest(http.MethodGet, spec.URL, nil)
		if requestErr != nil {
			result.Error = "代理探测请求构造失败"
			return result
		}
		started := time.Now()
		response, requestErr := client.Do(request)
		elapsed := time.Since(started)
		if requestErr != nil {
			if result.Error == "" {
				result.Error = "代理探测请求失败"
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		result.Status = response.StatusCode
		successes++
		total += elapsed
		if minimum == 0 || elapsed < minimum {
			minimum = elapsed
		}
	}
	if successes == 0 {
		return result
	}
	result.OK = true
	result.MinMS = minimum.Milliseconds()
	result.AvgMS = (total / time.Duration(successes)).Milliseconds()
	if successes == proxyProbeSamples {
		result.Error = ""
	}
	return result
}

// ProbeProxyTargets 并发探测并保持输入顺序，避免 UI 将名称与延迟错配。
func ProbeProxyTargets(proxyURL, fingerprint string, timeout time.Duration, targets []ProxyProbeSpec) []ProxyProbe {
	results := make([]ProxyProbe, len(targets))
	var waitGroup sync.WaitGroup
	for index, target := range targets {
		waitGroup.Add(1)
		go func(index int, target ProxyProbeSpec) {
			defer waitGroup.Done()
			results[index] = ProbeProxyTarget(proxyURL, fingerprint, timeout, target)
		}(index, target)
	}
	waitGroup.Wait()
	return results
}
