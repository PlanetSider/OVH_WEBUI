package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/availability"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/proxyguard"
	"github.com/ovh-webui/server/internal/storage"
	"github.com/ovh-webui/server/internal/types"
)

// ServerListCache 服务器列表内存缓存
type ServerListCache struct {
	mu        sync.RWMutex
	Data      []types.ServerPlan
	Timestamp *time.Time
	TTL       time.Duration
}

// NewServerListCache 默认 2 小时 TTL。后台按整点刷新；TTL 仍用于启动补采、
// 请求降级和缓存状态展示。
func NewServerListCache() *ServerListCache {
	return &ServerListCache{TTL: 2 * time.Hour}
}

// Snapshot 返回数据、时间戳副本和是否仍在 TTL 内。
func (s *ServerListCache) Snapshot() ([]types.ServerPlan, *time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Timestamp == nil {
		return nil, nil, false
	}
	cp := make([]types.ServerPlan, len(s.Data))
	copy(cp, s.Data)
	ts := *s.Timestamp
	return cp, &ts, time.Since(ts) < s.TTL
}

// Get 返回缓存副本和是否有效。
func (s *ServerListCache) Get() ([]types.ServerPlan, bool) {
	data, _, valid := s.Snapshot()
	return data, valid
}

// Set 更新缓存，时间戳=NOW
func (s *ServerListCache) Set(data []types.ServerPlan) {
	s.SetAt(data, time.Now())
}

// SetAt 用指定时间戳更新缓存。
// 启动时从 SQLite 回灌历史数据要用这个，保留真实的 updated_at，
// 否则旧数据被当作刚拉的，过期判断会出错。
func (s *ServerListCache) SetAt(data []types.ServerPlan, ts time.Time) {
	s.mu.Lock()
	s.Data = append([]types.ServerPlan(nil), data...)
	s.Timestamp = &ts
	s.mu.Unlock()
}

// Clear 原子清空缓存和时间戳。
func (s *ServerListCache) Clear() {
	s.mu.Lock()
	s.Data = nil
	s.Timestamp = nil
	s.mu.Unlock()
}

// AttemptOutcome 是一次购买流程对队列失败预算的明确结论。
// CountFailure 只在确定性失败时为 true；Transient 仅用于尚未 checkout 的可安全重试错误。
type AttemptOutcome struct {
	CountFailure bool
	Transient    bool
}

type proxyHealthReporter struct {
	guard  *proxyguard.Guard
	logger *logger.Logger
	state  *State
}

func (r proxyHealthReporter) ReportFailure(accountID string, cause error) {
	if r.guard == nil || !ovh.IsProxyError(cause) {
		return
	}
	event := r.guard.ObserveFailure(accountID, cause)
	if event.Kind == proxyguard.EventNone {
		return
	}
	if r.state != nil {
		r.state.HandleProxyGuardEvent(event)
		return
	}
	if r.logger != nil {
		r.logger.Warn("账户 "+accountID+" 的代理状态变化", "proxyguard")
	}
}

func (r proxyHealthReporter) ReportSuccess(accountID string) {
	if r.guard == nil {
		return
	}
	event := r.guard.ObserveSuccess(accountID)
	if event.Kind == proxyguard.EventNone {
		return
	}
	if r.state != nil {
		r.state.HandleProxyGuardEvent(event)
		return
	}
	if r.logger != nil {
		r.logger.Info("账户 "+accountID+" 的代理已恢复，自动动作可继续", "proxyguard")
	}
}

// ProxyGuardAction 是代理熔断对业务状态产生的可观测变更。
// 通知层只接收脱敏字段和计数，不接触账户凭据。
type ProxyGuardAction struct {
	Event                    proxyguard.Event
	AccountID                string
	AccountName              string
	AccountZone              string
	ProxyURL                 string
	PausedQueue              int
	DisabledMonitorAutoOrder int
	DisabledVPSAutoOrder     int
	RestoredQueue            int
	RestoredMonitorAutoOrder int
	RestoredVPSAutoOrder     int
	Errors                   []string
}

type ProxyGuardMonitorMutator func(accountID string, enabled bool) (int, error)
type ProxyGuardNotificationHandler func(ProxyGuardAction)

// State 聚合所有共享运行状态。
type State struct {
	Paths       storage.Paths
	Config      *config.Store
	OVH         *ovh.Factory
	ProxyGuard  *proxyguard.Guard
	Logger      *logger.Logger
	ServerCache *ServerListCache
	DB          *db.DB // SQLite 持久化层

	proxyGuardActionMu       sync.Mutex
	proxyGuardMonitorMutator ProxyGuardMonitorMutator
	proxyGuardNotify         ProxyGuardNotificationHandler

	APIKey string
	Port   string

	// 多账户:内存里持有全部 OVH 账户副本(启动从 SQLite 加载),
	// OVH Factory 通过 FindAccount 闭包按 id 查询
	AccountsMu sync.RWMutex
	Accounts   []types.OVHAccount

	QueueMu sync.Mutex
	Queue   []types.QueueItem

	HistoryMu sync.Mutex
	History   []types.PurchaseHistoryEntry

	ServerPlansMu sync.RWMutex
	ServerPlans   []types.ServerPlan

	DeletedTaskIDsMu sync.Mutex
	DeletedTaskIDs   map[string]struct{}

	// checkoutMu 把真正 checkout 前的最终任务确认，与队列编辑、删除账户等
	// 破坏性操作串行化。purchaseTasks 覆盖从取 client 到购买流程结束的
	// 整个生命周期；checkoutTasks 只记录已经落盘防重复记录、尚未结束
	// checkout 处理的任务。
	checkoutMu    sync.Mutex
	checkoutTasks map[string]string // task ID -> account ID
	purchaseTasks map[string]string // task ID -> account ID

	VPSSubsMu        sync.Mutex
	VPSSubscriptions []types.VPSSubscription
	VPSCheckInterval int

	QueueProcessorRunning bool
	// QueueProcessorEnabled 表示启动恢复与队列数据加载是否安全完成。
	// 只要 checkout 恢复、成功订单清理或队列加载失败，就保持 false，
	// 防止在无法确认遗留下单状态时再次发起 checkout。
	QueueProcessorEnabled bool

	// FeishuConnection 由启动层注入，避免 app 包依赖具体飞书实现。
	// 保存设置后会调用 Reconfigure，使长连接配置立即生效。
	FeishuConnection FeishuConnectionController

	// Weixin 由 iLink 适配器在启动时注入。业务层只依赖最小发送能力，
	// 避免 app/monitor/purchase 与具体协议实现形成循环依赖。
	Weixin WeixinNotifier

	// 串行化全表 Replace 落盘，避免并发 SaveHistory/SaveQueue 快照互相覆盖丢数据
	accountsPersistMu sync.Mutex
	historyPersistMu  sync.Mutex
	queuePersistMu    sync.Mutex
	vpsPersistMu      sync.Mutex
	queueProcessorMu  sync.RWMutex
	queueTickMu       sync.Mutex
	queueTickRunning  bool

	// backgroundMu 管理非主循环的有限辅助任务（例如成功订单详情补全和目录预热）。
	// 任务只允许在关闭前登记，关闭时取消父 context 并等待退出。
	backgroundMu      sync.Mutex
	backgroundCtx     context.Context
	backgroundCancel  context.CancelFunc
	backgroundWG      sync.WaitGroup
	backgroundClosing bool

	// loadFailed 记录启动/重载时无法读取的持久化表。对应内存快照不可信时，
	// 所有整表覆盖写和基于该快照的 mutation 都必须拒绝，避免空快照抹掉磁盘数据。
	loadFailedMu sync.RWMutex
	loadFailed   map[string]string

	// attemptOutcomeMu 保存单个队列任务最近一轮购买流程的结果。
	// PurchaseServer 结束后由队列处理器消费，避免把瞬时网络失败混进
	// RetryCount 的终止判断；同一任务由 checkout guard 保证不会并发执行。
	attemptOutcomeMu sync.Mutex
	attemptOutcomes  map[string]AttemptOutcome // task ID -> latest result

	// notificationOutboxMu 保证整个进程内所有通知发送入口（监控轮次、抢购
	// 成功即时刷送、后台重试）不会同时发送同一条 outbox 事件。重试时间也
	// 放在 State，而不是 Monitor 实例里，避免临时 Monitor 绕过节流。
	notificationOutboxMu sync.Mutex
	// notificationOutboxRetryMu 只保护重试时间表，不能复用发送锁：这些
	// 方法会在持有 notificationOutboxMu 时被调用，复用会导致自锁。
	notificationOutboxRetryMu     sync.RWMutex
	notificationOutboxNextAttempt map[string]time.Time
}

// MaxQueueItems 是所有 HTTP 和机器人入口共享的全局队列硬上限。
const MaxQueueItems = 200

// AvailableQueueSlots 返回当前内存队列还能安全接收的任务数。真正入队时
// EnqueueMonitorOrders 会在事务内重新检查数据库容量，因此该值只用于把
// 自动补货的大批待办切成可推进的小批次。
func (s *State) AvailableQueueSlots() int {
	if s == nil {
		return 0
	}
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	remaining := MaxQueueItems - len(s.Queue)
	if remaining < 0 {
		return 0
	}
	return remaining
}

var (
	// ErrQueueCheckoutInProgress 表示目标队列任务已经进入购买或 checkout 临界区。
	// 此时不能向用户误报删除、暂停、编辑或账户删除成功。
	ErrQueueCheckoutInProgress = errors.New("任务正在结账，无法取消或修改")
	// ErrQueueItemChanged 表示后台准备购物车期间，队列任务已被删除、暂停或编辑。
	ErrQueueItemChanged = errors.New("任务已不存在、暂停或发生变更")
	// ErrAccountNotFound 表示需要绑定的账户在最终提交前已不存在。
	ErrAccountNotFound = errors.New("账户不存在或已被删除")
	// ErrCheckoutAttemptExists 表示数据库已有同一 task_id 的 checkout 恢复记录。
	// 该任务不得再次自动 checkout，必须先人工核对 OVH 侧状态。
	ErrCheckoutAttemptExists = db.ErrCheckoutAttemptExists
)

// WeixinNotifier 是微信 iLink 通知适配器暴露给业务层的最小接口。
type WeixinNotifier interface {
	Configured() bool
	SendDefault(message string) bool
}

// FeishuConnectionController 是飞书长连接管理器的最小生命周期接口。
type FeishuConnectionController interface {
	Reconfigure()
	Stop()
}

// NewState 构造应用状态。DB 必须已 Open。
func NewState(paths storage.Paths, cfg *config.Store, lg *logger.Logger, sqliteDB *db.DB) *State {
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	s := &State{
		Paths:                         paths,
		Config:                        cfg,
		Logger:                        lg,
		ServerCache:                   NewServerListCache(),
		DB:                            sqliteDB,
		backgroundCtx:                 backgroundCtx,
		backgroundCancel:              backgroundCancel,
		DeletedTaskIDs:                make(map[string]struct{}),
		checkoutTasks:                 make(map[string]string),
		purchaseTasks:                 make(map[string]string),
		Accounts:                      []types.OVHAccount{},
		Queue:                         []types.QueueItem{},
		History:                       []types.PurchaseHistoryEntry{},
		ServerPlans:                   []types.ServerPlan{},
		VPSSubscriptions:              []types.VPSSubscription{},
		VPSCheckInterval:              60,
		QueueProcessorRunning:         false,
		QueueProcessorEnabled:         true,
		notificationOutboxNextAttempt: make(map[string]time.Time),
		loadFailed:                    make(map[string]string),
		attemptOutcomes:               make(map[string]AttemptOutcome),
	}
	guard := proxyguard.New(proxyguard.DefaultFailureThreshold)
	s.ProxyGuard = guard
	s.OVH = ovh.NewFactory(cfg, s.FindAccount, proxyHealthReporter{guard: guard, logger: lg, state: s})
	return s
}

// GoBackground 登记一个会在 State 关闭时被取消并等待的辅助任务。
func (s *State) GoBackground(fn func(context.Context)) bool {
	if s == nil || fn == nil {
		return false
	}
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	if s.backgroundClosing {
		return false
	}
	if s.backgroundCtx == nil {
		s.backgroundCtx, s.backgroundCancel = context.WithCancel(context.Background())
	}
	ctx := s.backgroundCtx
	s.backgroundWG.Add(1)
	go func() {
		defer s.backgroundWG.Done()
		fn(ctx)
	}()
	return true
}

// StopBackground 取消并等待所有已登记的辅助任务。
func (s *State) StopBackground(waitCtx context.Context) error {
	if s == nil {
		return nil
	}
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	s.backgroundMu.Lock()
	s.backgroundClosing = true
	if s.backgroundCancel != nil {
		s.backgroundCancel()
	}
	s.backgroundMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.backgroundWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}

// monitor 包，避免循环依赖；生产启动层传入 Monitor.MutateSubscriptions 的适配器。
func (s *State) SetProxyGuardMonitorMutator(mutator ProxyGuardMonitorMutator) {
	if s == nil {
		return
	}
	s.proxyGuardActionMu.Lock()
	s.proxyGuardMonitorMutator = mutator
	s.proxyGuardActionMu.Unlock()
}

// SetProxyGuardNotificationHandler 注入通知 outbox 适配器。核心状态提交不依赖
// 该回调成功，通知故障只能进入 action.Errors/日志。
func (s *State) SetProxyGuardNotificationHandler(handler ProxyGuardNotificationHandler) {
	if s == nil {
		return
	}
	s.proxyGuardActionMu.Lock()
	s.proxyGuardNotify = handler
	s.proxyGuardActionMu.Unlock()
}

// HandleProxyGuardEvent 把 Guard 的单次状态转换映射到持久化业务动作。
// 调用方不应直接修改队列或订阅；所有变化都经过既有 mutation owner。
func (s *State) HandleProxyGuardEvent(event proxyguard.Event) {
	if s == nil || event.Kind == proxyguard.EventNone || strings.TrimSpace(event.Status.AccountID) == "" {
		return
	}

	s.proxyGuardActionMu.Lock()
	var action ProxyGuardAction
	accountID := strings.TrimSpace(event.Status.AccountID)
	account, _ := s.FindAccount(accountID)
	action = ProxyGuardAction{
		Event:       event,
		AccountID:   accountID,
		AccountName: account.Name,
		AccountZone: account.Zone,
		ProxyURL:    ovh.ScrubProxyURL(account.ProxyURL),
		Errors:      []string{},
	}

	switch event.Kind {
	case proxyguard.EventTrip:
		if count, err := s.mutateProxyGuardQueue(accountID, false); err != nil {
			action.Errors = append(action.Errors, "抢购队列状态变更失败")
		} else {
			action.PausedQueue = count
		}
		if count, err := s.mutateProxyGuardMonitor(accountID, false); err != nil {
			action.Errors = append(action.Errors, "独服订阅状态变更失败")
		} else {
			action.DisabledMonitorAutoOrder = count
		}
		if count, err := s.mutateProxyGuardVPS(accountID, false); err != nil {
			action.Errors = append(action.Errors, "VPS 订阅状态变更失败")
		} else {
			action.DisabledVPSAutoOrder = count
		}
	case proxyguard.EventRecovery:
		if count, err := s.mutateProxyGuardQueue(accountID, true); err != nil {
			action.Errors = append(action.Errors, "恢复抢购队列失败")
		} else {
			action.RestoredQueue = count
		}
		if count, err := s.mutateProxyGuardMonitor(accountID, true); err != nil {
			action.Errors = append(action.Errors, "恢复独服订阅失败")
		} else {
			action.RestoredMonitorAutoOrder = count
		}
		if count, err := s.mutateProxyGuardVPS(accountID, true); err != nil {
			action.Errors = append(action.Errors, "恢复 VPS 订阅失败")
		} else {
			action.RestoredVPSAutoOrder = count
		}
	case proxyguard.EventReminder:
		// Reminder 只进入 outbox，不重复写业务状态。
	default:
		s.proxyGuardActionMu.Unlock()
		return
	}
	logger := s.Logger
	notify := s.proxyGuardNotify
	s.proxyGuardActionMu.Unlock()

	if logger != nil {
		switch event.Kind {
		case proxyguard.EventTrip:
			logger.Warn(fmt.Sprintf("账户 %s 代理熔断：暂停队列 %d，关闭独服自动下单 %d，关闭 VPS 自动下单 %d",
				accountID, action.PausedQueue, action.DisabledMonitorAutoOrder, action.DisabledVPSAutoOrder), "proxyguard")
		case proxyguard.EventRecovery:
			logger.Info(fmt.Sprintf("账户 %s 代理恢复：恢复队列 %d，恢复独服自动下单 %d，恢复 VPS 自动下单 %d",
				accountID, action.RestoredQueue, action.RestoredMonitorAutoOrder, action.RestoredVPSAutoOrder), "proxyguard")
		case proxyguard.EventReminder:
			logger.Warn("账户 "+accountID+" 的代理仍不可用", "proxyguard")
		}
		for range action.Errors {
			logger.Error("代理熔断状态变更未完全落库", "proxyguard")
		}
	}
	if notify != nil {
		notify(action)
	}
}

func (s *State) mutateProxyGuardQueue(accountID string, recover bool) (int, error) {
	changed := 0
	err := s.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
		for i := range queue {
			item := &queue[i]
			if item.AccountID != accountID {
				continue
			}
			if recover {
				if !item.ProxyGuardPaused {
					continue
				}
				item.ProxyGuardPaused = false
				if item.Status == "paused" {
					item.Status = "running"
					changed++
				}
				item.UpdatedAt = types.NowISO()
				continue
			}
			if item.ProxyGuardPaused || (item.Status != "running" && item.Status != "pending") {
				continue
			}
			if _, active := s.checkoutTasks[item.ID]; active {
				continue
			}
			item.Status = "paused"
			item.ProxyGuardPaused = true
			item.UpdatedAt = types.NowISO()
			changed++
		}
		return queue, nil
	})
	return changed, err
}

func (s *State) mutateProxyGuardMonitor(accountID string, enabled bool) (int, error) {
	mutator := s.proxyGuardMonitorMutator
	if mutator == nil {
		return 0, errors.New("监控订阅持久化 owner 未注入")
	}
	return mutator(accountID, enabled)
}

func (s *State) mutateProxyGuardVPS(accountID string, enabled bool) (int, error) {
	changed := 0
	err := s.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
		for i := range subscriptions {
			sub := &subscriptions[i]
			if enabled {
				if sub.ProxyGuardAutoOrderDisabled && !sub.AutoOrder && sub.AutoOrderAccountID == accountID {
					sub.AutoOrder = true
					sub.ProxyGuardAutoOrderDisabled = false
					if sub.Quantity < 1 {
						sub.Quantity = 1
					}
					changed++
				}
				continue
			}
			if sub.AutoOrder && sub.AutoOrderAccountID == accountID {
				sub.AutoOrder = false
				sub.ProxyGuardAutoOrderDisabled = true
				changed++
			}
		}
		return subscriptions, nil
	})
	return changed, err
}

// RestoreProxyGuardGates 在启动加载持久化状态后恢复代理熔断门禁；只读标记，
// 不重新发送历史告警，也不修改队列/订阅。
func (s *State) RestoreProxyGuardGates() {
	if s == nil || s.ProxyGuard == nil {
		return
	}
	seed := func(accountID, timestamp string) {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return
		}
		at := time.Now().UTC()
		if parsed, ok := types.ParseTS(timestamp); ok {
			at = parsed
		}
		s.ProxyGuard.SeedTripped(accountID, at)
	}
	s.QueueMu.Lock()
	for _, item := range s.Queue {
		if item.ProxyGuardPaused {
			seed(item.AccountID, item.UpdatedAt)
		}
	}
	s.QueueMu.Unlock()
	s.VPSSubsMu.Lock()
	for _, sub := range s.VPSSubscriptions {
		if sub.ProxyGuardAutoOrderDisabled {
			seed(sub.AutoOrderAccountID, sub.CreatedAt)
		}
	}
	s.VPSSubsMu.Unlock()
	if s.DB != nil {
		if subs, err := s.DB.ListMonitorSubscriptions(); err == nil {
			for _, sub := range subs {
				if sub.ProxyGuardAutoOrderDisabled {
					seed(sub.AutoOrderAccountID, sub.CreatedAt)
				}
			}
		} else if s.Logger != nil {
			s.Logger.Warn("恢复代理熔断门禁时读取独服订阅失败", "proxyguard")
		}
	}
}

// 多账户场景下,旧的 state.Config.HasCredentials() 不再可靠(新用户的 kv['config'] 可能为空),
// 凡是判断"系统能不能调 OVH"都应该走这个。
func (s *State) HasAnyAccount() bool {
	s.AccountsMu.RLock()
	defer s.AccountsMu.RUnlock()
	return len(s.Accounts) > 0
}

// FindAccount 多账户查找。id="" 返回默认账户(没默认 → 第一个);否则按 ID 精确匹配。
// OVH Factory 的 lookup 走这个,所有 ClientFor(accountID) 都会绕一圈到这里。
func (s *State) FindAccount(id string) (types.OVHAccount, bool) {
	s.AccountsMu.RLock()
	defer s.AccountsMu.RUnlock()
	if id == "" {
		for _, a := range s.Accounts {
			if a.IsDefault {
				return a, true
			}
		}
		if len(s.Accounts) > 0 {
			return s.Accounts[0], true
		}
		return types.OVHAccount{}, false
	}
	for _, a := range s.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return types.OVHAccount{}, false
}

// IsAccountProxyPaused 只影响绑定该账户的自动动作；不会删除队列、订阅或
// 改写停售状态。空账户 ID 解析当前默认账户。
func (s *State) IsAccountProxyPaused(accountID string) bool {
	if s == nil || s.ProxyGuard == nil {
		return false
	}
	if strings.TrimSpace(accountID) == "" {
		account, ok := s.FindAccount("")
		if !ok {
			return false
		}
		accountID = account.ID
	}
	return s.ProxyGuard.IsPaused(accountID)
}

// ResetAccountProxyGuard 供人工健康检查/恢复操作清除单账户熔断状态。
func (s *State) ResetAccountProxyGuard(accountID string) {
	if s == nil || s.ProxyGuard == nil || strings.TrimSpace(accountID) == "" {
		return
	}
	event := s.ProxyGuard.ObserveSuccess(accountID)
	if event.Kind == proxyguard.EventRecovery {
		s.HandleProxyGuardEvent(event)
	}
}

func (s *State) ProxyGuardStatus(accountID string) (proxyguard.Status, bool) {
	if s == nil || s.ProxyGuard == nil {
		return proxyguard.Status{}, false
	}
	return s.ProxyGuard.Get(accountID)
}

// ReloadAccounts 从 SQLite 重新加载账户到内存,并把整个 OVH client 缓存清掉,
// 强制下次 ClientFor() 用最新凭据重建。
// 账户 CRUD 操作完成后调一次。
func (s *State) ReloadAccounts() error {
	accs, err := s.DB.ListAccounts()
	if err != nil {
		s.MarkLoadFailed("accounts", err)
		return err
	}
	s.ClearLoadFailure("accounts")
	s.AccountsMu.Lock()
	if accs == nil {
		accs = []types.OVHAccount{}
	}
	s.Accounts = accs
	s.AccountsMu.Unlock()
	s.OVH.InvalidateAll()
	if err := s.OVH.RefreshSharedProxy(); err != nil {
		s.MarkLoadFailed("shared_proxy", err)
		return err
	}
	s.ClearLoadFailure("shared_proxy")
	return nil
}

// WithAccountMutation 串行化账户数据库变更和随后的内存重载，避免两个创建、
// 更新、删除或默认账户切换交错时，较早读取的快照最后发布并覆盖最新状态。
func (s *State) WithAccountMutation(mutate func() error) error {
	s.accountsPersistMu.Lock()
	defer s.accountsPersistMu.Unlock()
	return mutate()
}

// WithAccountMutationRollback 在账户数据库变更后刷新内存；若刷新失败，则在
// 同一账户写入临界区内执行回滚并再次刷新。适用于“必须让数据库与运行状态
// 同步成功才算完成”的账户写入，避免数据库已改但内存仍使用旧凭据/旧默认账户。
func (s *State) WithAccountMutationRollback(mutate, rollback func() error) error {
	return s.WithAccountMutation(func() error {
		if err := mutate(); err != nil {
			return err
		}
		if err := s.ReloadAccounts(); err == nil {
			return nil
		} else {
			reloadErr := err
			if rollback == nil {
				return fmt.Errorf("账户已写入，但刷新运行状态失败: %w", reloadErr)
			}
			if rollbackErr := rollback(); rollbackErr != nil {
				return fmt.Errorf("账户已写入，但刷新运行状态失败: %w；回滚也失败: %v", reloadErr, rollbackErr)
			}
			if restoreErr := s.ReloadAccounts(); restoreErr != nil {
				return fmt.Errorf("账户写入后的运行状态刷新失败: %w；数据库已回滚，但恢复运行状态也失败: %v", reloadErr, restoreErr)
			}
			return fmt.Errorf("账户写入后的运行状态刷新失败，数据库变更已回滚: %w", reloadErr)
		}
	})
}

// LoadAll 启动时从 SQLite 加载全部持久化数据到内存。
// 列表字段保证非 nil（JSON 序列化为 [] 而非 null）。
func (s *State) LoadAll() {
	// 在所有关键启动恢复步骤成功前，抢购处理器保持禁用。
	s.SetQueueProcessorEnabled(false)
	queueSafe := true
	// accounts: 必须最先加载,因为别的数据/loop 都按 account_id 索引
	s.migrateLegacyConfigToAccount() // 老用户从 kv['config'] 自动建默认账户
	if accs, err := s.DB.ListAccounts(); err == nil {
		s.ClearLoadFailure("accounts")
		if accs == nil {
			accs = []types.OVHAccount{}
		}
		s.AccountsMu.Lock()
		s.Accounts = accs
		s.AccountsMu.Unlock()
		s.Logger.Info("已加载 OVH 账户: "+intStr(len(accs))+" 个", "system")
		if err := s.OVH.RefreshSharedProxy(); err != nil {
			s.MarkLoadFailed("shared_proxy", err)
			queueSafe = false
		} else {
			s.ClearLoadFailure("shared_proxy")
		}
	} else {
		s.MarkLoadFailed("accounts", err)
		queueSafe = false
	}

	// 恢复上次进程在 checkout 请求期间退出的任务。已收到订单 ID 的记录
	// 恢复为成功历史；结果不确定的任务从队列隔离并保留数据库记录，
	// 防止启动后再次 checkout 造成重复下单。
	if recovered, quarantined, err := s.DB.RecoverCheckoutAttempts(s.recoveryNotificationChannels()); err != nil {
		s.Logger.Error("recover checkout attempts 失败", "system")
		queueSafe = false
	} else {
		if recovered > 0 {
			s.Logger.Warn(fmt.Sprintf("启动时恢复了 %d 个 checkout 成功订单", recovered), "system")
		}
		if quarantined > 0 {
			s.Logger.Warn(fmt.Sprintf("启动时隔离了 %d 个结果不确定的 checkout 任务；请人工核对 OVH 购物车/订单后再决定是否重建任务", quarantined), "system")
		}
	}

	// checkout 成功后，成功历史和队列删除会在同一事务内提交。旧版本可能
	// 在两次写入之间退出，因此启动时先清理已有成功历史对应的残留任务。
	if removed, err := s.DB.RemoveSuccessfullyPurchasedQueueItems(); err != nil {
		s.Logger.Error("cleanup purchased queue items 失败", "system")
		queueSafe = false
	} else if removed > 0 {
		s.Logger.Warn(fmt.Sprintf("启动时清理了 %d 个已有成功订单的残留队列任务", removed), "system")
	}

	// queue
	if items, err := s.DB.ListQueue(); err == nil {
		s.ClearLoadFailure("queue")
		s.QueueMu.Lock()
		s.Queue = items
		s.QueueMu.Unlock()
	} else {
		s.MarkLoadFailed("queue", err)
		queueSafe = false
	}
	if s.Queue == nil {
		s.Queue = []types.QueueItem{}
	}

	// history
	if items, err := s.DB.ListHistory(); err == nil {
		s.ClearLoadFailure("history")
		s.HistoryMu.Lock()
		s.History = items
		s.HistoryMu.Unlock()
	} else {
		s.MarkLoadFailed("history", err)
		queueSafe = false
	}
	if s.History == nil {
		s.History = []types.PurchaseHistoryEntry{}
	}

	// servers
	if plans, err := s.DB.ListServers(); err == nil {
		s.ClearLoadFailure("servers")
		if plans == nil {
			plans = []types.ServerPlan{}
		}
		s.ServerPlansMu.Lock()
		s.ServerPlans = plans
		s.ServerPlansMu.Unlock()
		// 空目录是合法的已加载状态，但不能把它当成可用缓存；下一次启动补采仍会刷新。
		if len(plans) > 0 {
			// 用 SQLite 里真实的 updated_at 重建缓存时间戳，
			// 这样过期的旧数据下次访问能正确触发刷新；NOW 会导致旧数据被当作"刚刷的"。
			if tsMs, err := s.DB.ServersUpdatedAt(); err == nil && tsMs > 0 {
				s.ServerCache.SetAt(plans, time.UnixMilli(tsMs))
			} else {
				s.ServerCache.Set(plans)
			}
			s.Logger.Info("已从 SQLite 加载服务器目录并同步到缓存", "system")
		} else {
			s.ServerCache.Clear()
		}
	} else {
		s.MarkLoadFailed("servers", err)
	}

	// vps subscriptions
	if subs, err := s.DB.ListVPSSubscriptions(); err == nil {
		s.ClearLoadFailure("vps_subscriptions")
		if subs == nil {
			subs = []types.VPSSubscription{}
		}
		s.VPSSubsMu.Lock()
		s.VPSSubscriptions = subs
		s.VPSSubsMu.Unlock()
	} else {
		s.MarkLoadFailed("vps_subscriptions", err)
	}
	// vps check interval 存 kv
	var ci int
	if ok, _ := s.DB.GetKV("vps_check_interval", &ci); ok && ci > 0 {
		s.VPSCheckInterval = ci
	}
	s.SetQueueProcessorEnabled(queueSafe)
	if !queueSafe {
		s.Logger.Error("抢购队列处理器已禁用：启动恢复或队列加载未安全完成，请先检查数据库与 checkout 记录", "system")
	}
}

// recoveryNotificationChannels 返回启动恢复时已经具备明确接收端的通知渠道。
// app 包不能依赖 monitor 包（会形成循环依赖），因此这里只做启动阶段所需的
// 最小凭据检查；发送时仍由 monitor 再次校验渠道是否已被用户关闭或暂时失效。
func (s *State) recoveryNotificationChannels() []string {
	if s == nil || s.Config == nil {
		return nil
	}
	cfg := s.Config.Get()
	channels := make([]string, 0, 3)
	if cfg.IsTelegramNotificationsEnabled() && strings.TrimSpace(cfg.TgToken) != "" && strings.TrimSpace(cfg.TgChatID) != "" {
		channels = append(channels, "telegram")
	}
	if cfg.IsFeishuNotificationsEnabled() && cfg.FeishuEnabled && strings.TrimSpace(cfg.FeishuAppID) != "" && strings.TrimSpace(cfg.FeishuAppSecret) != "" {
		var bindings map[string]struct {
			OpenID string `json:"openId"`
		}
		if ok, err := s.DB.GetKV("feishu_bindings", &bindings); err == nil && ok {
			for _, binding := range bindings {
				if strings.TrimSpace(binding.OpenID) != "" {
					channels = append(channels, "feishu")
					break
				}
			}
		}
	}
	if cfg.IsWeixinNotificationsEnabled() {
		var count int
		if err := s.DB.Get(&count, `SELECT COUNT(1) FROM weixin_credentials WHERE id = 1 AND TRIM(account_id) <> '' AND TRIM(bot_token) <> '' AND TRIM(base_url) <> '' AND TRIM(user_id) <> ''`); err == nil && count > 0 {
			channels = append(channels, "weixin")
		}
	}
	return channels
}

// LockNotificationOutbox / UnlockNotificationOutbox 供 monitor 包共享发送互斥锁。
func (s *State) LockNotificationOutbox() {
	if s != nil {
		s.notificationOutboxMu.Lock()
	}
}

// LockNotificationOutboxContext 获取通知发送锁，并在调用方取消时返回。
func (s *State) LockNotificationOutboxContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.notificationOutboxMu.TryLock() {
			if err := ctx.Err(); err != nil {
				s.notificationOutboxMu.Unlock()
				return err
			}
			return nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *State) UnlockNotificationOutbox() {
	if s != nil {
		s.notificationOutboxMu.Unlock()
	}
}

// NotificationOutboxRetryDue 返回某事件是否已经过了本进程内的短暂重试节流期。
func (s *State) NotificationOutboxRetryDue(id string, now time.Time) bool {
	if s == nil {
		return false
	}
	s.notificationOutboxRetryMu.RLock()
	defer s.notificationOutboxRetryMu.RUnlock()
	if next, ok := s.notificationOutboxNextAttempt[id]; ok && now.Before(next) {
		return false
	}
	return true
}

func (s *State) SetNotificationOutboxRetry(id string, next time.Time) {
	if s != nil {
		s.notificationOutboxRetryMu.Lock()
		defer s.notificationOutboxRetryMu.Unlock()
		if s.notificationOutboxNextAttempt == nil {
			s.notificationOutboxNextAttempt = make(map[string]time.Time)
		}
		s.notificationOutboxNextAttempt[id] = next
	}
}

func (s *State) ClearNotificationOutboxRetry(id string) {
	if s != nil {
		s.notificationOutboxRetryMu.Lock()
		defer s.notificationOutboxRetryMu.Unlock()
		delete(s.notificationOutboxNextAttempt, id)
	}
}

// MarkLoadFailed 记录启动/重载时未能读取的持久化表。
// 对应内存快照不可信时，任何整表覆盖写都必须被拒绝。
func (s *State) MarkLoadFailed(table string, err error) {
	if s == nil || strings.TrimSpace(table) == "" || err == nil {
		return
	}
	s.loadFailedMu.Lock()
	if s.loadFailed == nil {
		s.loadFailed = make(map[string]string)
	}
	s.loadFailed[table] = "读取持久化表失败"
	s.loadFailedMu.Unlock()
	if s.Logger != nil {
		s.Logger.Error("load "+table+" 失败，已禁止本次运行覆盖写该表", "system")
	}
}

// ClearLoadFailure 清除指定表的读取失败标记。只有一次成功读取后才能调用。
func (s *State) ClearLoadFailure(table string) {
	if s == nil {
		return
	}
	s.loadFailedMu.Lock()
	delete(s.loadFailed, table)
	s.loadFailedMu.Unlock()
}

// SaveBlocked 返回启动读取失败表的写入闸门错误。
func (s *State) SaveBlocked(table string) error {
	if s == nil {
		return fmt.Errorf("拒绝写 %s: 状态未初始化", table)
	}
	s.loadFailedMu.RLock()
	_, blocked := s.loadFailed[table]
	s.loadFailedMu.RUnlock()
	if !blocked {
		return nil
	}
	return fmt.Errorf("拒绝写 %s: 启动时读取失败；继续写入可能用不完整内存快照覆盖磁盘数据，请修复后重启", table)
}

// LoadFailures 返回当前记录的持久化读取失败副本，供健康检查和诊断使用。
func (s *State) LoadFailures() map[string]string {
	if s == nil {
		return map[string]string{}
	}
	s.loadFailedMu.RLock()
	defer s.loadFailedMu.RUnlock()
	out := make(map[string]string, len(s.loadFailed))
	for key, value := range s.loadFailed {
		out[key] = value
	}
	return out
}

// SetAttemptOutcome 记录最近一轮任务的失败预算结论。
func (s *State) SetAttemptOutcome(taskID string, outcome AttemptOutcome) {
	if s == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	s.attemptOutcomeMu.Lock()
	if s.attemptOutcomes == nil {
		s.attemptOutcomes = make(map[string]AttemptOutcome)
	}
	s.attemptOutcomes[taskID] = outcome
	s.attemptOutcomeMu.Unlock()
}

// ClearAttemptOutcome 丢弃任务上一轮残留的临时错误结果。
func (s *State) ClearAttemptOutcome(taskID string) {
	if s == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	s.attemptOutcomeMu.Lock()
	delete(s.attemptOutcomes, taskID)
	s.attemptOutcomeMu.Unlock()
}

// TakeAttemptOutcome 读取并清除最近一轮任务结果。
func (s *State) TakeAttemptOutcome(taskID string) (AttemptOutcome, bool) {
	if s == nil || strings.TrimSpace(taskID) == "" {
		return AttemptOutcome{}, false
	}
	s.attemptOutcomeMu.Lock()
	outcome, ok := s.attemptOutcomes[taskID]
	delete(s.attemptOutcomes, taskID)
	s.attemptOutcomeMu.Unlock()
	return outcome, ok
}

// SetQueueProcessorEnabled 更新启动安全闸门。
func (s *State) SetQueueProcessorEnabled(enabled bool) {
	s.queueProcessorMu.Lock()
	s.QueueProcessorEnabled = enabled
	s.queueProcessorMu.Unlock()
}

// IsQueueProcessorEnabled 返回抢购队列是否允许启动。
func (s *State) IsQueueProcessorEnabled() bool {
	s.queueProcessorMu.RLock()
	defer s.queueProcessorMu.RUnlock()
	return s.QueueProcessorEnabled
}

// SetQueueProcessorRunning 发布队列处理器的实际运行状态。
// 该状态会被 HTTP 状态接口并发读取，必须与启动安全闸门使用同一把锁。
func (s *State) SetQueueProcessorRunning(running bool) {
	s.queueProcessorMu.Lock()
	s.QueueProcessorRunning = running
	s.queueProcessorMu.Unlock()
}

// TryStartQueueProcessor 以 CAS 语义取得队列处理器运行权。
// QueueProcessorEnabled 是启动安全状态；Running 则是实际 goroutine 闸门，
// 两者都必须满足才允许启动。
func (s *State) TryStartQueueProcessor() bool {
	if s == nil {
		return false
	}
	s.queueProcessorMu.Lock()
	defer s.queueProcessorMu.Unlock()
	if !s.QueueProcessorEnabled || s.QueueProcessorRunning {
		return false
	}
	s.QueueProcessorRunning = true
	return true
}

// TryStartQueueTick 防止单次处理超出 ticker 周期时下一轮重入。
func (s *State) TryStartQueueTick() bool {
	if s == nil {
		return false
	}
	s.queueTickMu.Lock()
	defer s.queueTickMu.Unlock()
	if s.queueTickRunning {
		return false
	}
	s.queueTickRunning = true
	return true
}

func (s *State) FinishQueueTick() {
	if s == nil {
		return
	}
	s.queueTickMu.Lock()
	s.queueTickRunning = false
	s.queueTickMu.Unlock()
}

func (s *State) IsQueueProcessorRunning() bool {
	s.queueProcessorMu.RLock()
	defer s.queueProcessorMu.RUnlock()
	return s.QueueProcessorRunning
}

// CountActiveQueues 统计未完成的队列项
func (s *State) CountActiveQueues() int {
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	cnt := 0
	for _, it := range s.Queue {
		if it.Status == "running" || it.Status == "pending" || it.Status == "paused" {
			cnt++
		}
	}
	return cnt
}

// CountAvailableServers 统计有库存的型号
func (s *State) CountAvailableServers() int {
	s.ServerPlansMu.RLock()
	defer s.ServerPlansMu.RUnlock()
	cnt := 0
	for _, p := range s.ServerPlans {
		for _, dc := range p.Datacenters {
			if availability.ExplicitlyAvailable(dc.Availability) {
				cnt++
				break
			}
		}
	}
	return cnt
}

// CountPurchase 统计成功/失败订单数
func (s *State) CountPurchase() (success, failed int) {
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()
	for _, h := range s.History {
		switch h.Status {
		case "success":
			success++
		case "failed":
			failed++
		}
	}
	return
}

// SaveQueue 把内存中 Queue 整表覆盖写入 SQLite（串行化，取最新快照）
func (s *State) SaveQueue() error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.QueueMu.Lock()
	cp := make([]types.QueueItem, len(s.Queue))
	copy(cp, s.Queue)
	s.QueueMu.Unlock()
	return s.DB.ReplaceQueue(cp)
}

// MutateQueue 串行完成队列内存变更和 SQLite 持久化。mutate 收到独立副本；
// 只有落盘成功才会发布到内存，因此调用方无需自行实现容易误删并发任务的回滚。
func (s *State) MutateQueue(mutate func([]types.QueueItem) ([]types.QueueItem, error)) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	return s.mutateQueue(false, mutate)
}

// MutateQueueForAccount 在队列变更提交前再次确认账户仍存在。调用方通常会
// 在请求开始时先做一次账户校验，但账户可能在校验与落盘之间被删除；此方法
// 把最终账户确认与队列写入放进同一临界区，避免产生引用已删除账户的任务。
func (s *State) MutateQueueForAccount(accountID string, mutate func([]types.QueueItem) ([]types.QueueItem, error)) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	return s.mutateQueueForAccount(false, accountID, mutate)
}

// mutateQueue 修改队列并持久化。普通调用不允许改变正在 checkout 的任务；
// allowCheckout 仅供 checkout 结果已确定/不确定后的内部清理使用。
func (s *State) mutateQueue(allowCheckout bool, mutate func([]types.QueueItem) ([]types.QueueItem, error)) error {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()

	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	current := append([]types.QueueItem(nil), s.Queue...)
	next, err := mutate(current)
	if err != nil {
		return err
	}
	if next == nil {
		next = []types.QueueItem{}
	}
	if !allowCheckout {
		if err := s.ensureCheckoutItemsUnchanged(current, next); err != nil {
			return err
		}
	}
	if err := s.DB.ReplaceQueue(next); err != nil {
		return err
	}
	s.Queue = next
	return nil
}

// mutateQueueForAccount 是 MutateQueueForAccount 的内部实现。锁顺序保持为
// checkoutMu → queuePersistMu → accountsPersistMu，与账户删除路径一致；
// 因此账户删除不能穿过本次队列提交。
func (s *State) mutateQueueForAccount(allowCheckout bool, accountID string, mutate func([]types.QueueItem) ([]types.QueueItem, error)) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("缺少账户 ID")
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.accountsPersistMu.Lock()
	defer s.accountsPersistMu.Unlock()
	if s.DB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	if _, ok, err := s.DB.GetAccount(accountID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
	}

	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	current := append([]types.QueueItem(nil), s.Queue...)
	next, err := mutate(current)
	if err != nil {
		return err
	}
	if next == nil {
		next = []types.QueueItem{}
	}
	if !allowCheckout {
		if err := s.ensureCheckoutItemsUnchanged(current, next); err != nil {
			return err
		}
	}
	if err := s.DB.ReplaceQueue(next); err != nil {
		return err
	}
	s.Queue = next
	return nil
}

// ensureCheckoutItemsUnchanged 确保普通队列变更没有删除、暂停或修改正在 checkout 的任务。
// 调用方必须已经持有 checkoutMu。
func (s *State) ensureCheckoutItemsUnchanged(current, next []types.QueueItem) error {
	for taskID := range s.checkoutTasks {
		var currentItem, nextItem *types.QueueItem
		for i := range current {
			if current[i].ID == taskID {
				cp := current[i]
				currentItem = &cp
				break
			}
		}
		for i := range next {
			if next[i].ID == taskID {
				cp := next[i]
				nextItem = &cp
				break
			}
		}
		if currentItem == nil || nextItem == nil || !reflect.DeepEqual(*currentItem, *nextItem) {
			return ErrQueueCheckoutInProgress
		}
	}
	return nil
}

// BeginCheckoutAttempt 在真正发送 checkout 前，以当前队列快照登记防重复记录，
// 并发布 checkout 闸门。闸门持续到 EndCheckoutAttempt，期间普通队列变更会失败。
func (s *State) BeginCheckoutAttempt(item types.QueueItem, cartID string) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(cartID) == "" {
		return ErrQueueItemChanged
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	if s.checkoutTasks == nil {
		s.checkoutTasks = make(map[string]string)
	}
	s.DeletedTaskIDsMu.Lock()
	_, deleted := s.DeletedTaskIDs[item.ID]
	s.DeletedTaskIDsMu.Unlock()
	if deleted {
		return ErrQueueItemChanged
	}
	if _, active := s.checkoutTasks[item.ID]; active {
		return ErrQueueCheckoutInProgress
	}
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	var current *types.QueueItem
	for i := range s.Queue {
		if s.Queue[i].ID == item.ID {
			cp := s.Queue[i]
			current = &cp
			break
		}
	}
	if current == nil || current.Status != "running" || !queueItemMatchesResolvedAccount(*current, item) {
		return ErrQueueItemChanged
	}
	if err := s.DB.RecordCheckoutAttempt(item, cartID); err != nil {
		if errors.Is(err, db.ErrCheckoutAttemptExists) {
			return fmt.Errorf("%w: %v", ErrCheckoutAttemptExists, err)
		}
		return err
	}
	s.checkoutTasks[item.ID] = item.AccountID
	return nil
}

// queueItemMatchesResolvedAccount 允许旧队列任务在 account_id 为空时，把本次
// 已解析并固定的默认账户写入 checkout attempt。除此之外的任何字段变化，或
// 已显式指定账户的任务被改用另一账户，仍会被视为任务已变更。
func queueItemMatchesResolvedAccount(current, resolved types.QueueItem) bool {
	if reflect.DeepEqual(current, resolved) {
		return true
	}
	if strings.TrimSpace(current.AccountID) != "" || strings.TrimSpace(resolved.AccountID) == "" {
		return false
	}
	current.AccountID = resolved.AccountID
	return reflect.DeepEqual(current, resolved)
}

// CancelCheckoutAttemptBeforeRequest 清理尚未发送的 checkout attempt。
// checkoutTasks 登记由 PurchaseServer 的 defer EndCheckoutAttempt 统一释放，
// 避免清理完成到函数返回之间出现账户删除或队列编辑窗口。
func (s *State) CancelCheckoutAttemptBeforeRequest(taskID string) error {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	if s.checkoutTasks == nil {
		return nil
	}
	if _, active := s.checkoutTasks[taskID]; !active {
		return nil
	}
	if err := s.DB.RemoveCheckoutAttempt(taskID); err != nil {
		return err
	}
	return nil
}

// FinishCheckoutHTTPError 清理 OVH 明确 HTTP 拒绝的 checkout attempt。
// 网络错误和响应不确定路径不得调用。
func (s *State) FinishCheckoutHTTPError(taskID string) error {
	return s.CancelCheckoutAttemptBeforeRequest(taskID)
}

// EndCheckoutAttempt 释放 checkout 闸门。
func (s *State) EndCheckoutAttempt(taskID string) {
	if taskID == "" {
		return
	}
	s.checkoutMu.Lock()
	if s.checkoutTasks == nil {
		s.checkoutMu.Unlock()
		return
	}
	delete(s.checkoutTasks, taskID)
	s.checkoutMu.Unlock()
}

// BeginAccountPurchase 在购买流程第一次取 OVH client 之前登记账户活动。
// 账户更新/删除会通过 WithAccountCheckoutGuard 检查这个登记，从而覆盖
// 创建购物车、配置商品等尚未落盘 checkout attempt 的长窗口。调用方必须
// 在整个 PurchaseServer 生命周期结束时调用 EndAccountPurchase。
func (s *State) BeginAccountPurchase(taskID, accountID string) error {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(accountID) == "" {
		return ErrQueueItemChanged
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	if s.purchaseTasks == nil {
		s.purchaseTasks = make(map[string]string)
	}
	if _, active := s.purchaseTasks[taskID]; active {
		return ErrQueueCheckoutInProgress
	}
	if s.checkoutTasks != nil {
		if _, active := s.checkoutTasks[taskID]; active {
			return ErrQueueCheckoutInProgress
		}
	}
	s.purchaseTasks[taskID] = accountID
	return nil
}

// EndAccountPurchase 释放 PurchaseServer 的账户级活动登记。
func (s *State) EndAccountPurchase(taskID string) {
	if strings.TrimSpace(taskID) == "" {
		return
	}
	s.checkoutMu.Lock()
	if s.purchaseTasks != nil {
		delete(s.purchaseTasks, taskID)
	}
	s.checkoutMu.Unlock()
}

// WithAccountCheckoutGuard 在账户级联删除或其他账户级破坏性变更期间，
// 阻止该账户已有购买流程或 checkout 进入/继续发送阶段。
func (s *State) WithAccountCheckoutGuard(accountID string, mutate func() error) error {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	for _, activeAccountID := range s.purchaseTasks {
		if activeAccountID == accountID {
			return ErrQueueCheckoutInProgress
		}
	}
	for _, activeAccountID := range s.checkoutTasks {
		if activeAccountID == accountID {
			return ErrQueueCheckoutInProgress
		}
	}
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()
	s.vpsPersistMu.Lock()
	defer s.vpsPersistMu.Unlock()
	return s.WithAccountMutation(mutate)
}

// PruneDeletedTaskIDs 回收已经不可能影响后台处理的任务隔离标记。
// 数据库查询失败时保留标记，避免把不确定 checkout 重新放回可处理路径。
func (s *State) PruneDeletedTaskIDs() {
	if s == nil || s.DB == nil {
		return
	}

	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()

	queued := make(map[string]struct{}, len(s.Queue))
	for _, item := range s.Queue {
		queued[item.ID] = struct{}{}
	}
	s.DeletedTaskIDsMu.Lock()
	ids := make([]string, 0, len(s.DeletedTaskIDs))
	for id := range s.DeletedTaskIDs {
		ids = append(ids, id)
	}
	s.DeletedTaskIDsMu.Unlock()

	for _, id := range ids {
		if _, active := s.purchaseTasks[id]; active {
			continue
		}
		if _, active := s.checkoutTasks[id]; active {
			continue
		}
		if _, exists := queued[id]; exists {
			continue
		}
		hasAttempt, err := s.DB.HasCheckoutAttempt(id)
		if err != nil || hasAttempt {
			continue
		}
		s.DeletedTaskIDsMu.Lock()
		delete(s.DeletedTaskIDs, id)
		s.DeletedTaskIDsMu.Unlock()
	}
}

// IsQueueItemRunning 在 checkout 前确认任务仍存在且仍处于运行状态。
// 用户删除、清空或暂停任务后，即使后台已经创建好购物车，也不能继续结账。
func (s *State) IsQueueItemRunning(id string) bool {
	if id == "" {
		return false
	}
	s.DeletedTaskIDsMu.Lock()
	_, deleted := s.DeletedTaskIDs[id]
	s.DeletedTaskIDsMu.Unlock()
	if deleted {
		return false
	}
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	for _, item := range s.Queue {
		if item.ID == id {
			return item.Status == "running"
		}
	}
	return false
}

// QuarantineQueueItem 立即阻止结果不确定的 checkout 在本进程和重启后重试。
// DeletedTaskIDs 先落内存；即使 SQLite 暂时不可写，队列处理器也会跳过该任务，
// 而 checkout_attempts 会在下次启动时再次执行持久化隔离。
func (s *State) QuarantineQueueItem(id string) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("缺少队列任务 ID")
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	return s.quarantineQueueItemLocked(id)
}

// QuarantineQueueItemDuringCheckout 供已经登记 checkoutTasks 的下单流程使用。
// BeginCheckoutAttempt 不会跨网络请求持有互斥锁，因此这里仍需取得 checkoutMu。
func (s *State) QuarantineQueueItemDuringCheckout(id string) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("缺少队列任务 ID")
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	return s.quarantineQueueItemLocked(id)
}

func (s *State) quarantineQueueItemLocked(id string) error {
	s.DeletedTaskIDsMu.Lock()
	if s.DeletedTaskIDs == nil {
		s.DeletedTaskIDs = make(map[string]struct{})
	}
	s.DeletedTaskIDs[id] = struct{}{}
	s.DeletedTaskIDsMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	current := append([]types.QueueItem(nil), s.Queue...)
	kept := make([]types.QueueItem, 0, len(current))
	for _, item := range current {
		if item.ID != id {
			kept = append(kept, item)
		}
	}
	if err := s.DB.ReplaceQueue(kept); err != nil {
		return err
	}
	s.Queue = kept
	return nil
}

// MutateQueueWithHistory 在同一临界区读取购买历史并修改队列。它用于需要同时
// 根据近期成功记录和当前队列做去重的入口，锁顺序与 CommitPurchaseSuccess 一致。
func (s *State) MutateQueueWithHistory(mutate func([]types.QueueItem, []types.PurchaseHistoryEntry) ([]types.QueueItem, error)) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	if err := s.SaveBlocked("history"); err != nil {
		return err
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()

	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()

	currentQueue := append([]types.QueueItem(nil), s.Queue...)
	history := append([]types.PurchaseHistoryEntry(nil), s.History...)
	next, err := mutate(currentQueue, history)
	if err != nil {
		return err
	}
	if next == nil {
		next = []types.QueueItem{}
	}
	if err := s.ensureCheckoutItemsUnchanged(currentQueue, next); err != nil {
		return err
	}
	if err := s.DB.ReplaceQueue(next); err != nil {
		return err
	}
	s.Queue = next
	return nil
}

// MutateQueueWithHistoryForAccount 同时执行账户最终确认、队列变更和历史快照
// 读取，供需要按历史去重且明确绑定账户的入队入口使用。
func (s *State) MutateQueueWithHistoryForAccount(accountID string, mutate func([]types.QueueItem, []types.PurchaseHistoryEntry) ([]types.QueueItem, error)) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	if err := s.SaveBlocked("history"); err != nil {
		return err
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("缺少账户 ID")
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()
	s.accountsPersistMu.Lock()
	defer s.accountsPersistMu.Unlock()
	if s.DB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	if _, ok, err := s.DB.GetAccount(accountID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
	}

	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()
	currentQueue := append([]types.QueueItem(nil), s.Queue...)
	history := append([]types.PurchaseHistoryEntry(nil), s.History...)
	next, err := mutate(currentQueue, history)
	if err != nil {
		return err
	}
	if next == nil {
		next = []types.QueueItem{}
	}
	if err := s.ensureCheckoutItemsUnchanged(currentQueue, next); err != nil {
		return err
	}
	if err := s.DB.ReplaceQueue(next); err != nil {
		return err
	}
	s.Queue = next
	return nil
}

// EnqueueMonitorOrders 将监控订阅状态和本次补货产生的队列任务原子落盘，
// 数据库提交成功后才发布新的内存队列。
func (s *State) EnqueueMonitorOrders(sub types.Subscription, items []types.QueueItem) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	if err := s.SaveBlocked("monitor_subscriptions"); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	// 账户删除按 checkoutMu → queuePersistMu → ... → accountsPersistMu
	// 取得锁。自动入队沿用同一顺序，并在账户锁内重新确认目标账户，
	// 防止检查阶段验证通过后账户恰好被删除，仍插入悬空账户任务。
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.accountsPersistMu.Lock()
	defer s.accountsPersistMu.Unlock()
	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	if len(s.Queue)+len(items) > MaxQueueItems {
		return fmt.Errorf("队列容量不足（当前 %d，本次 %d，上限 %d）", len(s.Queue), len(items), MaxQueueItems)
	}
	for _, item := range items {
		accountID := strings.TrimSpace(item.AccountID)
		if accountID == "" {
			return fmt.Errorf("监控任务 %s 缺少账户 ID", item.ID)
		}
		if _, ok, err := s.DB.GetAccount(accountID); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("监控任务账户 %s 不存在或已被删除", accountID)
		}
	}
	if err := s.DB.EnqueueMonitorOrdersAndSaveSubscription(sub, items, MaxQueueItems); err != nil {
		return err
	}
	next := make([]types.QueueItem, 0, len(items)+len(s.Queue))
	next = append(next, items...)
	next = append(next, s.Queue...)
	s.Queue = next
	return nil
}

// SaveHistory 把内存中 History 整表覆盖写入 SQLite（串行化，取最新快照）
func (s *State) SaveHistory() error {
	if err := s.SaveBlocked("history"); err != nil {
		return err
	}
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()
	s.HistoryMu.Lock()
	cp := make([]types.PurchaseHistoryEntry, len(s.History))
	copy(cp, s.History)
	s.HistoryMu.Unlock()
	return s.DB.ReplaceHistory(cp)
}

// UpdateHistoryOrderStatus 原子更新单条历史的订单状态，并可在同一事务中
// 创建通知 outbox。callback 在 history 持久化锁内执行，只应构造纯数据，不得
// 发起网络请求或再次修改 State。
func (s *State) UpdateHistoryOrderStatus(id, status, statusAt string, callback func(types.PurchaseHistoryEntry, string) (*types.NotificationOutboxEntry, error)) (bool, error) {
	if err := s.SaveBlocked("history"); err != nil {
		return false, err
	}
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()
	index := -1
	for i := range s.History {
		if s.History[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return false, fmt.Errorf("history %s not found", id)
	}
	current := s.History[index]
	if current.OrderStatus == status && current.OrderStatusAt == statusAt {
		return false, nil
	}
	next := current
	next.OrderStatus = status
	next.OrderStatusAt = statusAt
	var notification *types.NotificationOutboxEntry
	if current.OrderStatus != status && callback != nil {
		var err error
		notification, err = callback(next, current.OrderStatus)
		if err != nil {
			return false, err
		}
	}
	if err := s.DB.UpdateHistoryOrderStatusWithNotification(id, status, statusAt, notification); err != nil {
		return false, err
	}
	s.History[index] = next
	return true, nil
}

// MutateHistory 与 MutateQueue 相同，确保失败历史和异步补全不会只修改内存。
func (s *State) MutateHistory(mutate func([]types.PurchaseHistoryEntry) ([]types.PurchaseHistoryEntry, error)) error {
	if err := s.SaveBlocked("history"); err != nil {
		return err
	}
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()

	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()
	current := append([]types.PurchaseHistoryEntry(nil), s.History...)
	next, err := mutate(current)
	if err != nil {
		return err
	}
	if next == nil {
		next = []types.PurchaseHistoryEntry{}
	}
	if err := s.DB.ReplaceHistory(next); err != nil {
		return err
	}
	s.History = next
	return nil
}

// MutateVPSSubscriptions 串行修改 VPS 订阅；数据库写入成功后才发布内存快照。
// mutate 收到独立副本，写库失败时内存保持不变。
func (s *State) MutateVPSSubscriptions(mutate func([]types.VPSSubscription) ([]types.VPSSubscription, error)) error {
	if err := s.SaveBlocked("vps_subscriptions"); err != nil {
		return err
	}
	s.vpsPersistMu.Lock()
	defer s.vpsPersistMu.Unlock()

	s.VPSSubsMu.Lock()
	defer s.VPSSubsMu.Unlock()
	current := cloneVPSSubscriptions(s.VPSSubscriptions)
	next, err := mutate(current)
	if err != nil {
		return err
	}
	if next == nil {
		next = []types.VPSSubscription{}
	}
	if err := s.DB.ReplaceVPSSubscriptions(next); err != nil {
		return err
	}
	s.VPSSubscriptions = next
	return nil
}

// VPSSubscriptionsSnapshot 返回可安全交给 HTTP 响应或监控循环使用的深副本。
func (s *State) VPSSubscriptionsSnapshot() []types.VPSSubscription {
	s.VPSSubsMu.Lock()
	defer s.VPSSubsMu.Unlock()
	return cloneVPSSubscriptions(s.VPSSubscriptions)
}

// SetVPSCheckInterval 仅在 SQLite 写入成功后发布新的检查间隔。
func (s *State) SetVPSCheckInterval(interval int) error {
	if err := s.SaveBlocked("vps_subscriptions"); err != nil {
		return err
	}
	s.vpsPersistMu.Lock()
	defer s.vpsPersistMu.Unlock()
	if err := s.DB.SetKV("vps_check_interval", interval); err != nil {
		return err
	}
	s.VPSSubsMu.Lock()
	s.VPSCheckInterval = interval
	s.VPSSubsMu.Unlock()
	return nil
}

// VPSCheckIntervalSnapshot 返回当前 VPS 检查间隔，供监控循环和日志并发读取。
func (s *State) VPSCheckIntervalSnapshot() int {
	s.VPSSubsMu.Lock()
	defer s.VPSSubsMu.Unlock()
	return s.VPSCheckInterval
}

func cloneVPSSubscriptions(source []types.VPSSubscription) []types.VPSSubscription {
	out := make([]types.VPSSubscription, 0, len(source))
	for _, sub := range source {
		copySub := sub
		copySub.Datacenters = append([]string{}, sub.Datacenters...)
		copySub.LastStatus = make(map[string]string, len(sub.LastStatus))
		for key, value := range sub.LastStatus {
			copySub.LastStatus[key] = value
		}
		copySub.PendingNotify = make(map[string]string, len(sub.PendingNotify))
		for key, value := range sub.PendingNotify {
			copySub.PendingNotify[key] = value
		}
		copySub.PendingNotifyChannels = make(map[string][]string, len(sub.PendingNotifyChannels))
		for key, value := range sub.PendingNotifyChannels {
			copySub.PendingNotifyChannels[key] = append([]string(nil), value...)
		}
		copySub.History = make([]map[string]interface{}, 0, len(sub.History))
		for _, entry := range sub.History {
			copySub.History = append(copySub.History, cloneInterfaceMap(entry))
		}
		out = append(out, copySub)
	}
	return out
}

func cloneInterfaceMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(source))
	for key, value := range source {
		out[key] = cloneInterfaceValue(value)
	}
	return out
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneInterfaceMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneInterfaceValue(item)
		}
		return out
	case []map[string]interface{}:
		out := make([]map[string]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneInterfaceMap(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

// CommitPurchaseSuccess 原子持久化成功历史并移除对应队列任务。checkout 已经
// 发生时如果 SQLite 暂时失败，会先在内存中隔离任务以阻止再次下单；只有事务
// 成功后才发布新的队列和历史快照。
func (s *State) CommitPurchaseSuccess(entry types.PurchaseHistoryEntry) error {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	return s.commitPurchaseSuccessLocked(entry)
}

// CommitPurchaseSuccessDuringCheckout 供已经登记 checkoutTasks 的下单流程使用。
// checkoutMu 只保护登记表和状态提交，不跨网络请求持有。
func (s *State) CommitPurchaseSuccessDuringCheckout(entry types.PurchaseHistoryEntry) error {
	return s.CommitPurchaseSuccessDuringCheckoutWithNotification(entry, nil)
}

// CommitPurchaseSuccessDuringCheckoutWithNotification 在成功订单事务中同时创建通知 outbox。
func (s *State) CommitPurchaseSuccessDuringCheckoutWithNotification(entry types.PurchaseHistoryEntry, notification *types.NotificationOutboxEntry) error {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	return s.commitPurchaseSuccessLockedWithNotification(entry, notification)
}

func (s *State) commitPurchaseSuccessLocked(entry types.PurchaseHistoryEntry) error {
	return s.commitPurchaseSuccessLockedWithNotification(entry, nil)
}

func (s *State) commitPurchaseSuccessLockedWithNotification(entry types.PurchaseHistoryEntry, notification *types.NotificationOutboxEntry) error {
	if err := s.SaveBlocked("queue"); err != nil {
		return err
	}
	if err := s.SaveBlocked("history"); err != nil {
		return err
	}
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.historyPersistMu.Lock()
	defer s.historyPersistMu.Unlock()

	s.QueueMu.Lock()
	defer s.QueueMu.Unlock()
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()

	nextHistory := make([]types.PurchaseHistoryEntry, 0, len(s.History)+1)
	for _, existing := range s.History {
		if existing.TaskID != entry.TaskID {
			nextHistory = append(nextHistory, existing)
		}
	}
	nextHistory = append(nextHistory, entry)
	nextQueue := make([]types.QueueItem, 0, len(s.Queue))
	for _, item := range s.Queue {
		if item.ID != entry.TaskID {
			nextQueue = append(nextQueue, item)
		}
	}

	if err := s.DB.CommitPurchaseSuccessWithNotification(entry, notification); err != nil {
		// checkout 已经成功，不能因为本地事务失败而再次下单。这里仅设置
		// 进程内隔离标记，保留原队列和历史快照，避免并发 SaveQueue/
		// SaveHistory 把尚未原子提交的半成品状态写回数据库。调用方会在
		// 当前进程有限重试，持续失败则由 checkout_attempts 启动恢复。
		s.DeletedTaskIDsMu.Lock()
		if s.DeletedTaskIDs == nil {
			s.DeletedTaskIDs = make(map[string]struct{})
		}
		s.DeletedTaskIDs[entry.TaskID] = struct{}{}
		s.DeletedTaskIDsMu.Unlock()
		return err
	}

	s.History = nextHistory
	s.Queue = nextQueue
	s.DeletedTaskIDsMu.Lock()
	delete(s.DeletedTaskIDs, entry.TaskID)
	s.DeletedTaskIDsMu.Unlock()
	return nil
}

// SaveServers 把内存中 ServerPlans 整表覆盖写入 SQLite
func (s *State) SaveServers() error {
	if err := s.SaveBlocked("servers"); err != nil {
		return err
	}
	s.ServerPlansMu.RLock()
	cp := make([]types.ServerPlan, len(s.ServerPlans))
	copy(cp, s.ServerPlans)
	s.ServerPlansMu.RUnlock()
	return s.DB.ReplaceServers(cp)
}

// migrateLegacyConfigToAccount 老用户升级:如果 SQLite 里没账户但 kv['config'] 有
// 完整 OVH 凭据,自动把它建成一个名为"默认账户"的 OVHAccount,设默认,
// 并把现有所有 queue/history/sniper_task 的 account_id 列回填指向它。
// 已经有账户的话什么都不做,幂等。
func (s *State) migrateLegacyConfigToAccount() {
	n, err := s.DB.CountAccounts()
	if err != nil {
		s.Logger.Error("count accounts 失败", "system")
		return
	}
	if n > 0 {
		return // 已经有账户,跳过
	}
	cfg := s.Config.Get()
	if cfg.AppKey == "" || cfg.AppSecret == "" || cfg.ConsumerKey == "" {
		// 没老凭据,首次安装,等用户在 OvhCredsGate 创建第一个账户
		return
	}
	zone := cfg.Zone
	if zone == "" {
		zone = "IE"
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "ovh-eu"
	}
	iam := cfg.IAM
	if iam == "" {
		iam = "go-ovh-" + strings.ToLower(zone)
	}
	acc := types.OVHAccount{
		ID:          uuid.NewString(),
		Name:        "默认账户",
		Endpoint:    endpoint,
		Zone:        zone,
		AppKey:      cfg.AppKey,
		AppSecret:   cfg.AppSecret,
		ConsumerKey: cfg.ConsumerKey,
		IAM:         iam,
		IsDefault:   true,
		CreatedAt:   types.NowISO(),
	}
	if err := s.DB.UpsertAccount(acc); err != nil {
		s.Logger.Error("migrate legacy config to account 失败", "system")
		return
	}
	// 回填现有数据的 account_id 列(从空值 → 新账户 ID)
	for _, stmt := range []string{
		`UPDATE queue SET account_id = ? WHERE account_id = '' OR account_id IS NULL`,
		`UPDATE history SET account_id = ? WHERE account_id = '' OR account_id IS NULL`,
		`UPDATE config_sniper_tasks SET account_id = ? WHERE account_id = '' OR account_id IS NULL`,
	} {
		if _, err := s.DB.Exec(stmt, acc.ID); err != nil {
			s.Logger.Warn("backfill account_id 失败", "system")
		}
	}
	s.Logger.Info("已把旧 kv['config'] 迁移成默认账户: "+acc.Name+" ("+acc.Zone+")", "system")
}

// intStr 小工具,避免在 LoadAll 里临时引 strconv
func intStr(n int) string {
	// 简化版,只处理 0..9999
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// SaveAll 一次性保存所有数据
func (s *State) SaveAll() {
	// 成功历史必须先于队列落盘。若进程在两步之间退出，启动清理会根据成功
	// 历史移除残留任务；反过来则可能丢失订单记录。
	if err := s.SaveHistory(); err != nil {
		s.Logger.Error("save history 失败", "system")
	}
	if err := s.SaveQueue(); err != nil {
		s.Logger.Error("save queue 失败", "system")
	}
	if err := s.SaveServers(); err != nil {
		s.Logger.Error("save servers 失败", "system")
	}
}
