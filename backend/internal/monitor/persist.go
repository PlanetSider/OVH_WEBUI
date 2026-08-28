package monitor

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ovh-webui/server/internal/types"
)

// monitor 包内部用 Subscription / HistoryEntry，
// 而 SQLite 层用 types.Subscription / types.SubscriptionHistoryEntry。
// 字段一一对应，下面提供双向转换。

func toDBSub(s *Subscription) types.Subscription {
	if s == nil {
		return types.Subscription{}
	}
	hist := make([]types.SubscriptionHistoryEntry, 0, len(s.History))
	for _, h := range s.History {
		hist = append(hist, types.SubscriptionHistoryEntry{
			Timestamp:  h.Timestamp,
			Datacenter: h.Datacenter,
			Status:     h.Status,
			ChangeType: h.ChangeType,
			OldStatus:  h.OldStatus,
			Config:     h.Config,
		})
	}
	dcs := s.Datacenters
	if dcs == nil {
		dcs = []string{}
	}
	last := s.LastStatus
	if last == nil {
		last = map[string]string{}
	}
	confirmed := cloneStringMap(s.ConfirmedStatus)
	pendingOrder := cloneIntMap(s.PendingOrder)
	pendingNotify := cloneStringMap(s.PendingNotify)
	pendingNotifyChannels := cloneStringSliceMap(s.PendingNotifyChannels)
	return types.Subscription{
		PlanCode:           s.PlanCode,
		Datacenters:        dcs,
		Memories:           cloneStrings(s.Memories),
		Storages:           cloneStrings(s.Storages),
		Networks:           cloneStrings(s.Networks),
		NotifyAvailable:    s.NotifyAvailable,
		NotifyUnavailable:  s.NotifyUnavailable,
		LastStatus:         last,
		ConfirmedStatus:    confirmed,
		PendingOrder:       pendingOrder,
		PendingNotify:      pendingNotify,
		PendingNotifyChannels: pendingNotifyChannels,
		CreatedAt:          s.CreatedAt,
		History:            hist,
		ServerName:         s.ServerName,
		AutoOrder:          s.AutoOrder,
		Quantity:           s.Quantity,
		AutoOrderAccountID: s.AutoOrderAccountID,
	}
}

func fromDBSub(s types.Subscription) *Subscription {
	hist := make([]HistoryEntry, 0, len(s.History))
	for _, h := range s.History {
		hist = append(hist, HistoryEntry{
			Timestamp:  h.Timestamp,
			Datacenter: h.Datacenter,
			Status:     h.Status,
			ChangeType: h.ChangeType,
			OldStatus:  h.OldStatus,
			Config:     h.Config,
		})
	}
	dcs := s.Datacenters
	if dcs == nil {
		dcs = []string{}
	}
	last := s.LastStatus
	if last == nil {
		last = map[string]string{}
	}
	confirmed := cloneStringMap(s.ConfirmedStatus)
	if len(confirmed) == 0 {
		for key, status := range last {
			if status == "available" || status == "unavailable" {
				confirmed[key] = status
			}
		}
	}
	return &Subscription{
		PlanCode:           s.PlanCode,
		Datacenters:        dcs,
		Memories:           cloneStrings(s.Memories),
		Storages:           cloneStrings(s.Storages),
		Networks:           cloneStrings(s.Networks),
		NotifyAvailable:    s.NotifyAvailable,
		NotifyUnavailable:  s.NotifyUnavailable,
		LastStatus:         last,
		ConfirmedStatus:    confirmed,
		PendingOrder:       cloneIntMap(s.PendingOrder),
		PendingNotify:      cloneStringMap(s.PendingNotify),
		PendingNotifyChannels: cloneStringSliceMap(s.PendingNotifyChannels),
		CreatedAt:          s.CreatedAt,
		History:            hist,
		ServerName:         s.ServerName,
		AutoOrder:          s.AutoOrder,
		Quantity:           s.Quantity,
		AutoOrderAccountID: s.AutoOrderAccountID,
	}
}

// LoadFromDB 启动时从 SQLite 加载订阅 + known_servers
func (m *Monitor) LoadFromDB() {
	// 发布新快照必须和 SaveToDB/MutateSubscriptions/监控检查串行。
	// 如果账户删除时在监控检查中途直接替换 subscriptions，旧订阅可能在
	// 删除后继续自动入队；先取得 persistMu，保证数据库读取与快照发布
	// 不会穿过正在进行的检查。
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	_ = m.loadFromDBLocked()
}

// WithPersistenceGuard 把跨包的数据库变更与监控检查、订阅保存和快照重载
// 放进同一临界区。mutate 成功后立即从数据库重载订阅，避免旧订阅在账户
// 删除完成后继续自动入队。
func (m *Monitor) WithPersistenceGuard(mutate func() error) error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	if err := mutate(); err != nil {
		return err
	}
	return m.loadFromDBLocked()
}

// loadFromDBLocked 的调用方必须已经持有 persistMu。
func (m *Monitor) loadFromDBLocked() error {
	subs, err := m.state.DB.ListMonitorSubscriptions()
	if err != nil {
		m.state.Logger.Error("加载监控订阅失败（不会写回空列表，也不会启动监控）: "+err.Error(), "monitor")
		m.subsMu.Lock()
		m.subscriptions = []*Subscription{}
		m.knownServers = map[string]struct{}{}
		m.knownServersInitialized = false
		m.loaded = false
		m.subsMu.Unlock()
		return err
	}
	known := []string{}
	knownInitialized, err := m.state.DB.GetKV("monitor_known_servers", &known)
	if err != nil {
		m.state.Logger.Error("加载已知服务器失败（不会启动监控）: "+err.Error(), "monitor")
		m.subsMu.Lock()
		m.subscriptions = []*Subscription{}
		m.knownServers = map[string]struct{}{}
		m.knownServersInitialized = false
		m.loaded = false
		m.subsMu.Unlock()
		return err
	}

	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	m.subscriptions = make([]*Subscription, 0, len(subs))
	for _, s := range subs {
		m.subscriptions = append(m.subscriptions, fromDBSub(s))
	}
	knownSet := map[string]struct{}{}
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	m.knownServers = knownSet
	m.knownServersInitialized = knownInitialized
	m.loaded = true
	// 全局强制 5 秒
	m.checkInterval = 5
	m.state.Logger.Info("检查间隔已强制设置为: 5秒（全局固定值）", "monitor")
	m.state.Logger.Info(fmt.Sprintf("已加载订阅: %d 条", len(m.subscriptions)), "monitor")
	// TG 一键下单 UUID 在 LoadFromDB 返回后由调用方 LoadMessageUUIDCacheFromDB()
	return nil
}

// SaveToDB 把订阅 + known_servers 写回 SQLite
func (m *Monitor) SaveToDB() error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.subsMu.Lock()
	if !m.loaded {
		m.subsMu.Unlock()
		return fmt.Errorf("监控订阅尚未安全加载，拒绝写回数据库")
	}
	subs := make([]types.Subscription, 0, len(m.subscriptions))
	for _, s := range m.subscriptions {
		subs = append(subs, toDBSub(cloneSubscription(s)))
	}
	known := make([]string, 0, len(m.knownServers))
	for k := range m.knownServers {
		known = append(known, k)
	}
	sort.Strings(known)
	knownInitialized := m.knownServersInitialized
	m.checkInterval = 5
	n := len(subs)
	m.subsMu.Unlock()

	// Replace 会先清空表再写入；允许空列表（用户主动 clear），但打醒目日志便于排查
	if n == 0 {
		m.state.Logger.Warn("保存监控订阅: 当前内存列表为空，将清空 SQLite 订阅表", "monitor")
	}
	// 首次 CheckNewServers 尚未成功落盘时，known 只是空的内存基线。
	// 此时 SaveToDB 只能保存订阅，不能用空列表覆盖数据库中的基线，
	// 更不能把 knownServersInitialized 推进为 true，否则下轮会把全部
	// 现有服务器误报为新服务器。
	var err error
	if knownInitialized {
		err = m.state.DB.ReplaceMonitorSubscriptionsAndKnownServers(subs, known)
	} else {
		err = m.state.DB.ReplaceMonitorSubscriptions(subs)
	}
	if err != nil {
		m.state.Logger.Error("原子保存监控订阅失败: "+err.Error(), "monitor")
		return err
	}
	m.state.Logger.Info(fmt.Sprintf("订阅数据已保存: %d 条（检查间隔固定为5秒）", n), "monitor")
	return nil
}

// persistSubscriptionLocked 立即把单条订阅写入 SQLite。调用方必须已经持有
// sub.mu；通常也会持有 persistMu 的读锁。这样通知发送前的 PendingNotify
// 以及发送成功后的清理都能尽快落盘，避免进程在整轮 SaveToDB 之前退出时
// 丢失事件或重复发送。
func (m *Monitor) persistSubscriptionLocked(sub *Subscription) error {
	if sub == nil || m.state.DB == nil {
		return nil
	}
	return m.state.DB.UpsertMonitorSubscription(toDBSub(cloneSubscriptionUnlocked(sub)))
}

// MutateSubscriptions 串行修改订阅，并且只在 SQLite 全表替换成功后发布
// 新内存快照。mutate 收到的是深副本，可以安全修改。
func (m *Monitor) MutateSubscriptions(mutate func([]*Subscription) ([]*Subscription, error)) error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	if !m.loaded {
		return fmt.Errorf("监控订阅尚未安全加载，拒绝修改")
	}

	current := make([]*Subscription, 0, len(m.subscriptions))
	for _, sub := range m.subscriptions {
		current = append(current, cloneSubscription(sub))
	}
	next, err := mutate(current)
	if err != nil {
		return err
	}
	if next == nil {
		next = []*Subscription{}
	}
	dbSubs := make([]types.Subscription, 0, len(next))
	for _, sub := range next {
		dbSubs = append(dbSubs, toDBSub(sub))
	}
	if err := m.state.DB.ReplaceMonitorSubscriptions(dbSubs); err != nil {
		return err
	}
	m.subscriptions = next
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneIntMap(source map[string]int) map[string]int {
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	out := make(map[string][]string, len(source))
	for key, value := range source {
		out[key] = cloneStrings(value)
	}
	return out
}

// SubscriptionAsJSON 帮助 handler 返回订阅
func (m *Monitor) SubscriptionAsJSON(planCode string) ([]byte, bool) {
	sub := m.FindSubscription(planCode)
	if sub == nil {
		return nil, false
	}
	b, _ := json.Marshal(sub)
	return b, true
}
