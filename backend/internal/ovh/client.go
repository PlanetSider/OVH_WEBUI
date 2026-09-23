package ovh

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/types"
)

// AccountLookup 由 State 提供,根据 id 找账户:
//   id == ""  → 找默认账户(或第一个);适合"未指定账户时 fallback"
//   id == "x" → 精确找 x
type AccountLookup func(id string) (types.OVHAccount, bool)

// Factory OVH client 工厂。按 accountID 缓存 client 实例;
// 同一账户多次取拿到同一个 client;Invalidate 失效特定账户的缓存。
//
// 不同账户即使 endpoint 相同(都 ovh-eu)也是独立 client(凭据不同),
// 缓存 key 是 accountID 不是 endpoint。
type Factory struct {
	lookup   AccountLookup
	fallback *config.Store // 兼容老 Client() 调用,等所有 callsite 迁完可移除

	mu          sync.Mutex
	cache       map[string]*ovh.Client // accountID → client
	proxyHealth ProxyHealthReporter

	sharedMu        sync.RWMutex
	sharedClient    *http.Client
	sharedProxy     string
	sharedAccountID string
	sharedErr       error
}

// NewFactory 构造工厂。lookup 由 State 闭包注入；reporter 可选，负责接收
// 已配置代理的 transport 健康事件。
func NewFactory(cfg *config.Store, lookup AccountLookup, reporters ...ProxyHealthReporter) *Factory {
	var reporter ProxyHealthReporter
	if len(reporters) > 0 {
		reporter = reporters[0]
	}
	sharedClient, _ := BuildHTTPClient("", "", 60*time.Second)
	return &Factory{
		lookup:       lookup,
		fallback:     cfg,
		cache:        map[string]*ovh.Client{},
		proxyHealth:  reporter,
		sharedClient: sharedClient,
	}
}

// RefreshSharedProxy 让公开、无凭据的请求跟随当前默认账户的代理出口。
// 公开请求不复用账户认证 client，但复用同一代理 transport；代理配置错误
// 会保存在 sharedErr 中，调用方随后得到明确错误而不会静默直连。
func (f *Factory) RefreshSharedProxy() error {
	if f.lookup == nil {
		return f.setSharedProxy("", "")
	}
	acc, ok := f.lookup("")
	if !ok {
		return f.setSharedProxy("", "")
	}
	return f.setSharedProxy(acc.ID, acc.ProxyURL)
}

func (f *Factory) setSharedProxy(accountID, proxyURL string) error {
	client, err := BuildHTTPClient(proxyURL, "", 60*time.Second)
	if err == nil {
		client = withProxyHealthReporter(client, accountID, proxyURL, f.proxyHealth)
	}

	f.sharedMu.Lock()
	old := f.sharedClient
	f.sharedClient = client
	f.sharedProxy = proxyURL
	f.sharedAccountID = accountID
	f.sharedErr = err
	f.sharedMu.Unlock()
	if old != nil && old != client {
		old.CloseIdleConnections()
	}
	return err
}

// SharedHTTPClient 返回公共请求共用的 transport，并只为本次调用设置超时。
func (f *Factory) SharedHTTPClient(timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	f.sharedMu.RLock()
	client, err := f.sharedClient, f.sharedErr
	f.sharedMu.RUnlock()
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("shared public HTTP client is unavailable")
	}
	return &http.Client{Transport: client.Transport, Timeout: timeout}, nil
}

// SharedProxyAccountID 返回当前公共出口所绑定的账户 ID，仅用于日志关联。
func (f *Factory) SharedProxyAccountID() string {
	f.sharedMu.RLock()
	defer f.sharedMu.RUnlock()
	return f.sharedAccountID
}

// SharedProxyURL 返回当前公共出口的原始配置，仅供内部判断；不得写入日志或响应。
func (f *Factory) SharedProxyURL() string {
	f.sharedMu.RLock()
	defer f.sharedMu.RUnlock()
	return f.sharedProxy
}

// ClientFor 返回指定账户的 OVH client。accountID="" 走默认账户。
// 凭据缺失 / 账户不存在返回 error;同账户重复调用复用缓存实例。
func (f *Factory) ClientFor(accountID string) (*ovh.Client, error) {
	if f.lookup == nil {
		// State 还没把 lookup 注入(理论上不会发生)→ 退到 fallback
		return f.Client()
	}
	acc, ok := f.lookup(accountID)
	if !ok {
		if accountID == "" {
			return nil, fmt.Errorf("no default OVH account configured")
		}
		return nil, fmt.Errorf("ovh account %s not found", accountID)
	}
	if acc.AppKey == "" || acc.AppSecret == "" || acc.ConsumerKey == "" {
		return nil, fmt.Errorf("ovh account %s missing credentials", acc.ID)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if cli, ok := f.cache[acc.ID]; ok {
		return cli, nil
	}
	cli, err := f.buildClient(acc)
	if err != nil {
		return nil, err
	}
	f.cache[acc.ID] = cli
	return cli, nil
}

// NewClientForAccount 创建不进入缓存的账户 client，供保存前验证新账户或
// 显式健康检查使用。它仍使用同一套账户代理隔离与健康观察器。
func (f *Factory) NewClientForAccount(acc types.OVHAccount) (*ovh.Client, error) {
	return f.buildClient(acc)
}

func (f *Factory) buildClient(acc types.OVHAccount) (*ovh.Client, error) {
	if acc.AppKey == "" || acc.AppSecret == "" || acc.ConsumerKey == "" {
		return nil, fmt.Errorf("ovh account %s missing credentials", acc.ID)
	}
	cli, err := ovh.NewClient(acc.Endpoint, acc.AppKey, acc.AppSecret, acc.ConsumerKey)
	if err != nil {
		return nil, err
	}
	cli.Timeout = 30 * time.Second
	httpClient, err := BuildHTTPClient(acc.ProxyURL, acc.Fingerprint, cli.Timeout)
	if err != nil {
		return nil, fmt.Errorf("build account %s HTTP client: %w", acc.ID, err)
	}
	cli.Client = withProxyHealthReporter(httpClient, acc.ID, acc.ProxyURL, f.proxyHealth)
	cli.UserAgent = FingerprintUserAgent(acc.Fingerprint)
	return cli, nil
}

// Invalidate 清掉指定账户的缓存 client(更新 / 删除账户后调,避免拿到旧凭据)
func (f *Factory) Invalidate(accountID string) {
	f.mu.Lock()
	if cli, ok := f.cache[accountID]; ok && cli != nil && cli.Client != nil {
		cli.Client.CloseIdleConnections()
	}
	delete(f.cache, accountID)
	f.mu.Unlock()
}

// InvalidateAll 清全部缓存(比如重置 OVH 配置时)
func (f *Factory) InvalidateAll() {
	f.mu.Lock()
	for _, cli := range f.cache {
		if cli != nil && cli.Client != nil {
			cli.Client.CloseIdleConnections()
		}
	}
	f.cache = map[string]*ovh.Client{}
	f.mu.Unlock()
}

// Client 老接口,等价于 ClientFor("")(默认账户)。
// 未迁移到 ClientFor 的旧调用站点先用它;新代码不要用这个。
//
// Deprecated: 调用方应明确传 accountID。
func (f *Factory) Client() (*ovh.Client, error) {
	// 有账户 lookup 时，账户是唯一凭据与代理来源；不能在查找失败后
	// 静默退回旧 config，避免绕过账户级代理隔离。
	if f.lookup != nil {
		return f.ClientFor("")
	}
	// 仅在没有注入 lookup 的兼容场景使用旧 config。该路径没有账户级代理
	// 配置，生产 State 始终通过 ClientFor 进入上面的分支。
	if f.fallback == nil {
		return nil, fmt.Errorf("no default OVH account configured")
	}
	c := f.fallback.Get()
	if c.AppKey == "" || c.AppSecret == "" || c.ConsumerKey == "" {
		return nil, fmt.Errorf("missing OVH API credentials")
	}
	cli, err := ovh.NewClient(c.Endpoint, c.AppKey, c.AppSecret, c.ConsumerKey)
	if err != nil {
		return nil, err
	}
	cli.Timeout = 30 * time.Second
	return cli, nil
}
