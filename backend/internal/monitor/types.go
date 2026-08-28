package monitor

import (
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
)

// Monitor 对应 Python: ServerMonitor 类
type Monitor struct {
	state *app.State

	lifecycleMu   sync.Mutex
	subsMu        sync.Mutex
	// persistMu 作为监控状态的读写屏障：单个订阅检查持有读锁，允许不同
	// 订阅并发查询；订阅替换、账户级联删除和全量持久化持有写锁。
	persistMu     sync.RWMutex
	subscriptions []*Subscription
	knownServers  map[string]struct{}
	// knownServersInitialized 区分“从未建立过基线”和“基线已经持久化但
	// 当前恰好为空”。不能用 len(knownServers)==0 代替，否则服务器目录从
	// 空列表恢复为非空时会被错误地当成首次初始化，永久漏掉新服务器通知。
	knownServersInitialized bool

	running       bool
	loaded        bool // SQLite 订阅/已知服务器已安全加载
	checkInterval int // 全局固定 5 秒
	thread        *sync.WaitGroup
	stopCh        chan struct{}
	maxWorkers    int

	// Options 缓存（旧机制，兼容性保留）
	optionsCache    map[string]*CachedOptions
	optionsCacheTTL time.Duration

	// UUID 消息缓存
	messageUUIDCache    map[string]*CachedMessage
	messageUUIDCacheTTL time.Duration

	cacheLock sync.Mutex

	// 通知渠道健康检查时间戳：loop 每 5 分钟验证一次。临时失效只记警告，
	// 不停止监控；待发送事件保留到渠道恢复。
	// 不放 subsMu 下，使用单独的锁。
	tgCheckMu   sync.Mutex
	lastTGCheck time.Time
}

const (
	NotificationKindNewServer       = "new_server"
	NotificationKindPurchaseSuccess = "purchase_success"
	// MessageButtonTTL 是 Telegram / 飞书一键下单按钮的统一有效期。
	// 两个渠道共用同一张 SQLite 表，必须使用同一边界，避免飞书按钮
	// 绕过 Telegram 缓存层后永久有效。
	MessageButtonTTL = 24 * time.Hour
)

type CachedOptions struct {
	Options   []string `json:"options"`
	Timestamp float64  `json:"timestamp"`
}

type CachedMessage struct {
	PlanCode   string                 `json:"planCode"`
	Datacenter string                 `json:"datacenter"`
	Options    []string               `json:"options"`
	ConfigInfo map[string]interface{} `json:"configInfo"`
	Timestamp  float64                `json:"timestamp"`
}

// Subscription 订阅条目(monitor 包内部用,落 SQLite 时转 types.Subscription)
type Subscription struct {
	mu                 sync.Mutex             `json:"-"`
	checkMu            sync.Mutex             `json:"-"`
	PlanCode           string                 `json:"planCode"`
	Datacenters        []string               `json:"datacenters"`
	Memories           []string               `json:"memories,omitempty"`
	Storages           []string               `json:"storages,omitempty"`
	Networks           []string               `json:"networks,omitempty"`
	NotifyAvailable    bool                   `json:"notifyAvailable"`
	NotifyUnavailable  bool                   `json:"notifyUnavailable"`
	LastStatus         map[string]string      `json:"lastStatus"`
	ConfirmedStatus    map[string]string      `json:"confirmedStatus,omitempty"`
	PendingOrder       map[string]int         `json:"pendingOrder,omitempty"`
	PendingNotify      map[string]string      `json:"pendingNotify,omitempty"`
	PendingNotifyChannels map[string][]string `json:"pendingNotifyChannels,omitempty"`
	CreatedAt          string                 `json:"createdAt"`
	History            []HistoryEntry         `json:"history"`
	ServerName         string                 `json:"serverName,omitempty"`
	AutoOrder          bool                   `json:"autoOrder,omitempty"`
	Quantity           int                    `json:"quantity,omitempty"`
	AutoOrderAccountID string                 `json:"autoOrderAccountId,omitempty"` // 空 = 触发时只通知不下单
}

// HistoryEntry 历史记录条目
type HistoryEntry struct {
	Timestamp  string                 `json:"timestamp"`
	Datacenter string                 `json:"datacenter"`
	Status     string                 `json:"status"`
	ChangeType string                 `json:"changeType"`
	OldStatus  interface{}            `json:"oldStatus"`
	Config     map[string]interface{} `json:"config,omitempty"`
}

// New 创建监控器
func New(state *app.State) *Monitor {
	return &Monitor{
		state:               state,
		subscriptions:       []*Subscription{},
		knownServers:        map[string]struct{}{},
		checkInterval:       5,
		maxWorkers:          4,
		optionsCache:        map[string]*CachedOptions{},
		optionsCacheTTL:     24 * time.Hour,
		messageUUIDCache:    map[string]*CachedMessage{},
		messageUUIDCacheTTL: MessageButtonTTL,
	}
}

// Snapshot 返回订阅列表副本（JSON 用），永不返回 nil
func (m *Monitor) Snapshot() []*Subscription {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	cp := make([]*Subscription, 0, len(m.subscriptions))
	for _, s := range m.subscriptions {
		cp = append(cp, cloneSubscription(s))
	}
	return cp
}

// Status 对应 Python: get_status
func (m *Monitor) Status() map[string]interface{} {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	subs := make([]*Subscription, len(m.subscriptions))
	for i, s := range m.subscriptions {
		subs[i] = cloneSubscription(s)
	}
	return map[string]interface{}{
		"running":             m.running,
		"subscriptions_count": len(m.subscriptions),
		"known_servers_count": len(m.knownServers),
		"check_interval":      m.checkInterval,
		"subscriptions":       subs,
	}
}

func cloneSubscription(source *Subscription) *Subscription {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return cloneSubscriptionUnlocked(source)
}

// cloneSubscriptionUnlocked 仅供已经持有 source.mu 的代码调用。
func cloneSubscriptionUnlocked(source *Subscription) *Subscription {
	if source == nil {
		return nil
	}
	out := &Subscription{
		PlanCode: source.PlanCode, Datacenters: cloneStrings(source.Datacenters),
		Memories: cloneStrings(source.Memories), Storages: cloneStrings(source.Storages),
		Networks: cloneStrings(source.Networks), NotifyAvailable: source.NotifyAvailable,
		NotifyUnavailable: source.NotifyUnavailable, LastStatus: cloneStringMap(source.LastStatus),
		ConfirmedStatus: cloneStringMap(source.ConfirmedStatus), PendingOrder: cloneIntMap(source.PendingOrder),
		PendingNotify: cloneStringMap(source.PendingNotify), CreatedAt: source.CreatedAt, ServerName: source.ServerName,
		PendingNotifyChannels: cloneStringSliceMap(source.PendingNotifyChannels),
		AutoOrder: source.AutoOrder, Quantity: source.Quantity, AutoOrderAccountID: source.AutoOrderAccountID,
	}
	out.History = make([]HistoryEntry, 0, len(source.History))
	for _, entry := range source.History {
		copyEntry := entry
		copyEntry.Config = copyMap(entry.Config)
		out.History = append(out.History, copyEntry)
	}
	return out
}

// Running 监控是否在运行
func (m *Monitor) Running() bool {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	return m.running
}

// LoadReady 返回监控是否已经安全加载完成。
func (m *Monitor) LoadReady() bool {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	return m.loaded
}

// SetCheckInterval 已禁用，全局固定 5
func (m *Monitor) SetCheckInterval(_ int) {
	m.subsMu.Lock()
	m.checkInterval = 5
	m.subsMu.Unlock()
	m.state.Logger.Info("检查间隔已全局固定为5秒，无法修改", "monitor")
}

// nowBeijing 返回北京时间
func (m *Monitor) nowBeijing() time.Time {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Now().UTC().Add(8 * time.Hour)
	}
	return time.Now().In(loc)
}

func (m *Monitor) limitHistorySize(sub *Subscription, maxSize int) {
	if len(sub.History) > maxSize {
		sub.History = sub.History[len(sub.History)-maxSize:]
	}
}
