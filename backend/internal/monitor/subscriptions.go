package monitor

import (
	"errors"
	"fmt"
	"time"

	"github.com/ovh-webui/server/internal/db"
)

// AddSubscription 对应 Python: add_subscription
// autoOrderAccountID:auto_order 触发时用哪个账户下单;空 = 只通知不下单
func (m *Monitor) AddSubscription(planCode string, datacenters []string, notifyAvailable, notifyUnavailable bool,
	serverName string, lastStatus map[string]string, history []HistoryEntry, autoOrder bool, quantity int,
	autoOrderAccountID string, memories, storages, networks []string) error {
	return m.MutateSubscriptions(func(subscriptions []*Subscription) ([]*Subscription, error) {
		for _, s := range subscriptions {
			if s.PlanCode == planCode {
				m.state.Logger.Warn(fmt.Sprintf("订阅已存在: %s，将更新配置", planCode), "monitor")
				filtersChanged := !sameStringSlice(s.Datacenters, datacenters) ||
					!sameStringSlice(s.Memories, memories) ||
					!sameStringSlice(s.Storages, storages) ||
					!sameStringSlice(s.Networks, networks)
				accountChanged := s.AutoOrderAccountID != autoOrderAccountID
				if datacenters == nil {
					datacenters = []string{}
				}
				s.Datacenters = cloneStrings(datacenters)
				s.Memories = cloneStrings(memories)
				s.Storages = cloneStrings(storages)
				s.Networks = cloneStrings(networks)
				if filtersChanged || accountChanged {
					resetTracking(s)
				}
				s.NotifyAvailable = notifyAvailable
				s.NotifyUnavailable = notifyUnavailable
				s.AutoOrder = autoOrder
				if autoOrder {
					if quantity < 1 {
						quantity = 1
					}
					s.Quantity = quantity
				} else {
					s.Quantity = 0
					s.PendingOrder = map[string]int{}
				}
				clearDisabledPendingNotify(s, notifyAvailable, notifyUnavailable)
				s.ServerName = serverName
				s.AutoOrderAccountID = autoOrderAccountID
				if s.History == nil {
					s.History = []HistoryEntry{}
				}
				return subscriptions, nil
			}
		}

		if datacenters == nil {
			datacenters = []string{}
		}
		if lastStatus == nil {
			lastStatus = map[string]string{}
		}
		if history == nil {
			history = []HistoryEntry{}
		}
		sub := &Subscription{
			PlanCode:           planCode,
			Datacenters:        datacenters,
			Memories:           cloneStrings(memories),
			Storages:           cloneStrings(storages),
			Networks:           cloneStrings(networks),
			NotifyAvailable:    notifyAvailable,
			NotifyUnavailable:  notifyUnavailable,
			LastStatus:         lastStatus,
			ConfirmedStatus:    map[string]string{},
			PendingOrder:       map[string]int{},
			PendingNotify:      map[string]string{},
			PendingNotifyChannels: map[string][]string{},
			CreatedAt:          time.Now().Format(time.RFC3339Nano),
			History:            history,
			AutoOrderAccountID: autoOrderAccountID,
		}
		if autoOrder {
			if quantity < 1 {
				quantity = 1
			}
			sub.AutoOrder = true
			sub.Quantity = quantity
		}
		if serverName != "" {
			sub.ServerName = serverName
		}
		subscriptions = append(subscriptions, sub)
		displayName := planCode
		if serverName != "" {
			displayName = planCode + " (" + serverName + ")"
		}
		dcsStr := "全部"
		if len(datacenters) > 0 {
			dcsStr = fmt.Sprintf("%v", datacenters)
		}
		m.state.Logger.Info(fmt.Sprintf("添加订阅: %s, 数据中心: %s", displayName, dcsStr), "monitor")
		return subscriptions, nil
	})
}

// resetTracking 丢弃旧筛选条件/账户对应的库存基线。筛选条件或账户改变后，
// 旧 statusKey 不能拿来和新查询结果比较，否则会把旧配置误报为下架或补货。
func resetTracking(s *Subscription) {
	s.LastStatus = map[string]string{}
	s.ConfirmedStatus = map[string]string{}
	s.PendingOrder = map[string]int{}
	s.PendingNotify = map[string]string{}
	s.PendingNotifyChannels = map[string][]string{}
}

// ResetSubscriptionTracking 供 HTTP 更新入口在筛选条件或账户变化时重建基线。
func ResetSubscriptionTracking(s *Subscription) {
	resetTracking(s)
}

// clearDisabledPendingNotify 删除已经关闭的通知类型，避免关闭开关后旧事件仍被重试。
func clearDisabledPendingNotify(s *Subscription, notifyAvailable, notifyUnavailable bool) bool {
	if s.PendingNotify == nil {
		s.PendingNotify = map[string]string{}
	}
	changed := false
	for key, status := range s.PendingNotify {
		if (!notifyAvailable && (status == "available" || status == "price_check_failed")) ||
			(!notifyUnavailable && status == "unavailable") {
			delete(s.PendingNotify, key)
			delete(s.PendingNotifyChannels, key)
			changed = true
		}
	}
	return changed
}

// ClearDisabledPendingNotifications 供更新入口清理已关闭渠道对应的待通知事件。
func ClearDisabledPendingNotifications(s *Subscription, notifyAvailable, notifyUnavailable bool) {
	clearDisabledPendingNotify(s, notifyAvailable, notifyUnavailable)
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
}

// RemoveSubscription 对应 Python: remove_subscription
func (m *Monitor) RemoveSubscription(planCode string) error {
	return m.MutateSubscriptions(func(subscriptions []*Subscription) ([]*Subscription, error) {
		kept := make([]*Subscription, 0, len(subscriptions))
		for _, s := range subscriptions {
			if s.PlanCode != planCode {
				kept = append(kept, s)
			}
		}
		if len(kept) < len(subscriptions) {
			m.state.Logger.Info("删除订阅: "+planCode, "monitor")
			return kept, nil
		}
		return nil, errors.New("订阅不存在")
	})
}

// ClearSubscriptions 对应 Python: clear_subscriptions
func (m *Monitor) ClearSubscriptions() (int, error) {
	count := 0
	err := m.MutateSubscriptions(func(subscriptions []*Subscription) ([]*Subscription, error) {
		count = len(subscriptions)
		m.state.Logger.Info(fmt.Sprintf("清空所有订阅 (%d 项)", count), "monitor")
		return []*Subscription{}, nil
	})
	return count, err
}

// FindSubscription 按 planCode 查找
func (m *Monitor) FindSubscription(planCode string) *Subscription {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for _, s := range m.subscriptions {
		if s.PlanCode == planCode {
			return cloneSubscription(s)
		}
	}
	return nil
}

// SetKnownServers 用于从持久化恢复
func (m *Monitor) SetKnownServers(set map[string]struct{}) {
	m.subsMu.Lock()
	if set == nil {
		set = map[string]struct{}{}
	}
	m.knownServers = set
	m.knownServersInitialized = true
	m.subsMu.Unlock()
}

// KnownServers 返回当前已知服务器集合（用于持久化）
func (m *Monitor) KnownServers() []string {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	out := make([]string, 0, len(m.knownServers))
	for k := range m.knownServers {
		out = append(out, k)
	}
	return out
}

// MessageUUIDCacheLookup 用于 webhook 回调时取回完整配置。
// 先查内存，再查 SQLite（进程重启后按钮仍可用）。
func (m *Monitor) MessageUUIDCacheLookup(id string) *CachedMessage {
	ttlSec := int64(m.messageUUIDCacheTTL.Seconds())
	now := time.Now().Unix()

	// 注意：lookup 允许读取未消费配置；是否已 used 由 webhook 层单独判断。
	// 不要在这里因 used_at 返回 nil，否则「先 consume 再 lookup」或并发路径会丢配置。

	m.cacheLock.Lock()
	if cm, ok := m.messageUUIDCache[id]; ok {
		if now-int64(cm.Timestamp) < ttlSec {
			m.cacheLock.Unlock()
			return cm
		}
		delete(m.messageUUIDCache, id)
		m.cacheLock.Unlock()
		m.state.Logger.Warn("UUID缓存已过期: "+id, "telegram")
		if m.state.DB != nil {
			_ = m.state.DB.DeleteTelegramButton(id)
		}
		return nil
	}
	m.cacheLock.Unlock()

	// 内存未命中 → SQLite（部署/重启后恢复）
	if m.state.DB == nil {
		return nil
	}
	row, ok, err := m.state.DB.GetTelegramButton(id)
	if err != nil {
		m.state.Logger.Warn("读取 UUID 持久化缓存失败: "+err.Error(), "telegram")
		return nil
	}
	if !ok {
		return nil
	}
	if now-int64(row.CreatedAt) >= ttlSec {
		m.state.Logger.Warn("UUID持久化缓存已过期: "+id, "telegram")
		_ = m.state.DB.DeleteTelegramButton(id)
		return nil
	}
	cm := &CachedMessage{
		PlanCode:   row.PlanCode,
		Datacenter: row.Datacenter,
		Options:    db.ParseTelegramButtonOptions(row.Options),
		ConfigInfo: db.ParseTelegramButtonConfigInfo(row.ConfigInfo),
		Timestamp:  row.CreatedAt,
	}
	// 回灌内存，避免每次点按钮都查库
	m.cacheLock.Lock()
	m.messageUUIDCache[id] = cm
	m.cacheLock.Unlock()
	m.state.Logger.Info("✅ 从 SQLite 恢复 UUID 按钮配置: "+id+" → "+cm.PlanCode+"@"+cm.Datacenter, "telegram")
	return cm
}

// InvalidateMessageUUID 入队成功后从内存删除按钮（DB used_at 由 TryConsume 负责）
func (m *Monitor) InvalidateMessageUUID(id string) {
	if id == "" {
		return
	}
	m.cacheLock.Lock()
	delete(m.messageUUIDCache, id)
	m.cacheLock.Unlock()
}

// OptionsCacheLookup 兼容旧机制
func (m *Monitor) OptionsCacheLookup(key string) []string {
	m.cacheLock.Lock()
	defer m.cacheLock.Unlock()
	if c, ok := m.optionsCache[key]; ok {
		if time.Now().Unix()-int64(c.Timestamp) < int64(m.optionsCacheTTL.Seconds()) {
			return c.Options
		}
		delete(m.optionsCache, key)
		m.state.Logger.Warn("options缓存已过期: "+key, "telegram")
	}
	return nil
}

// cleanupExpiredCaches 对应 Python: _cleanup_expired_caches
func (m *Monitor) cleanupExpiredCaches() {
	now := time.Now().Unix()
	ttlUUID := int64(m.messageUUIDCacheTTL.Seconds())
	ttlOpts := int64(m.optionsCacheTTL.Seconds())
	m.cacheLock.Lock()
	expUUIDs := []string{}
	for k, v := range m.messageUUIDCache {
		if now-int64(v.Timestamp) >= ttlUUID {
			expUUIDs = append(expUUIDs, k)
		}
	}
	for _, k := range expUUIDs {
		delete(m.messageUUIDCache, k)
	}
	expOpts := []string{}
	for k, v := range m.optionsCache {
		if now-int64(v.Timestamp) >= ttlOpts {
			expOpts = append(expOpts, k)
		}
	}
	for _, k := range expOpts {
		delete(m.optionsCache, k)
	}
	m.cacheLock.Unlock()

	// 同步清理 SQLite 过期按钮
	if m.state.DB != nil {
		if n, err := m.state.DB.DeleteExpiredTelegramButtons(float64(now - ttlUUID)); err != nil {
			m.state.Logger.Warn("清理过期 TG 按钮失败: "+err.Error(), "monitor")
		} else if n > 0 {
			m.state.Logger.Debug(fmt.Sprintf("清理过期 TG 按钮: %d 条", n), "monitor")
		}
	}
	if len(expUUIDs) > 0 || len(expOpts) > 0 {
		m.state.Logger.Debug(fmt.Sprintf("清理过期缓存: UUID=%d个, Options=%d个", len(expUUIDs), len(expOpts)), "monitor")
	}
}

// AddMessageUUID 持久化并缓存按钮对应的配置。SQLite 是一次性消费和
// 重启恢复的事实来源，因此必须先成功写库，再发布内存缓存；写库失败时
// 调用方不得把这个 UUID 放进已经发送给用户的按钮。
func (m *Monitor) AddMessageUUID(id, planCode, datacenter string, options []string, configInfo map[string]interface{}) error {
	ts := float64(time.Now().Unix())
	if options == nil {
		options = []string{}
	}
	if m == nil || m.state == nil || m.state.DB == nil {
		return fmt.Errorf("按钮持久化服务不可用")
	}
	if err := m.state.DB.UpsertTelegramButton(id, planCode, datacenter, options, configInfo, ts); err != nil {
		return err
	}
	cachedConfig := cloneConfigInfo(configInfo)
	m.cacheLock.Lock()
	m.messageUUIDCache[id] = &CachedMessage{
		PlanCode:   planCode,
		Datacenter: datacenter,
		Options:    append([]string{}, options...),
		ConfigInfo: cachedConfig,
		Timestamp:  ts,
	}
	m.cacheLock.Unlock()
	return nil
}

// cloneConfigInfo 隔离按钮缓存中的配置快照。通知构建结束后，调用方仍可能
// 复用或修改 configInfo；按钮必须保留发送当时冻结的账户和选项。
func cloneConfigInfo(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(source))
	for key, value := range source {
		out[key] = cloneConfigValue(value)
	}
	return out
}

func cloneConfigValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneConfigInfo(typed)
	case []interface{}:
		items := make([]interface{}, len(typed))
		for i, item := range typed {
			items[i] = cloneConfigValue(item)
		}
		return items
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

// LoadMessageUUIDCacheFromDB 启动时回灌近 TTL 内的按钮配置
func (m *Monitor) LoadMessageUUIDCacheFromDB() {
	if m.state.DB == nil {
		return
	}
	since := float64(time.Now().Add(-m.messageUUIDCacheTTL).Unix())
	rows, err := m.state.DB.ListTelegramButtonsSince(since)
	if err != nil {
		m.state.Logger.Warn("加载 TG 一键下单按钮缓存失败: "+err.Error(), "monitor")
		return
	}
	m.cacheLock.Lock()
	for _, row := range rows {
		m.messageUUIDCache[row.ID] = &CachedMessage{
			PlanCode:   row.PlanCode,
			Datacenter: row.Datacenter,
			Options:    db.ParseTelegramButtonOptions(row.Options),
			ConfigInfo: db.ParseTelegramButtonConfigInfo(row.ConfigInfo),
			Timestamp:  row.CreatedAt,
		}
	}
	n := len(rows)
	m.cacheLock.Unlock()
	if n > 0 {
		m.state.Logger.Info(fmt.Sprintf("已从 SQLite 回灌 %d 个 TG 一键下单按钮", n), "monitor")
	}
}
