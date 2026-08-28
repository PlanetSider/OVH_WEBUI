package monitor

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

// tgRecheckInterval loop 内通知渠道健康检查节流间隔。
const tgRecheckInterval = 5 * time.Minute

// checkNotifications 节流验证通知渠道。渠道临时失效只记录警告，监控保持运行，
// 待发送状态会在渠道恢复后自动重试。
func (m *Monitor) checkNotifications() {
	m.tgCheckMu.Lock()
	due := time.Since(m.lastTGCheck) >= tgRecheckInterval
	m.tgCheckMu.Unlock()
	if !due {
		return
	}
	feishuOK := false
	if FeishuEnabled(m.state) {
		_, feishuOK = FeishuDefaultBinding(m.state)
	}
	tgOK, tgReason := telegram.VerifyConfig(m.state)
	m.tgCheckMu.Lock()
	m.lastTGCheck = time.Now()
	m.tgCheckMu.Unlock()
	weixinOK := m.state.Config.Get().IsWeixinNotificationsEnabled() && m.state.Weixin != nil && m.state.Weixin.Configured()
	if !tgOK && !feishuOK && !weixinOK {
		m.state.Logger.Warn("Telegram、飞书与微信通知当前均失效，监控继续运行并保留待通知事件: "+tgReason, "monitor")
	}
}

// CheckNewServers 对应 Python: check_new_servers
func (m *Monitor) CheckNewServers(currentServerList []map[string]interface{}) {
	// 正常启动路径总会注入数据库；这里仍做防御，避免异常构造的 Monitor
	// 在后台刷新入口解引用空数据库。没有持久化能力时不能推进基线，
	// 否则数据库恢复后会漏掉这段时间的新服务器。
	if m == nil || m.state == nil || m.state.DB == nil {
		return
	}
	current := map[string]struct{}{}
	for _, s := range currentServerList {
		if pc, ok := s["planCode"].(string); ok && pc != "" {
			current[pc] = struct{}{}
		}
	}
	// 与 SaveToDB / MutateSubscriptions 保持统一锁顺序：persistMu → subsMu。
	// 快照写入必须与订阅全表替换串行，避免旧 known_servers 覆盖新状态。
	m.persistMu.Lock()
	m.subsMu.Lock()
	if !m.knownServersInitialized {
		m.subsMu.Unlock()
		if err := m.state.DB.SaveKnownServersAndNotifications(sortedKnownServers(current), nil); err != nil {
			m.state.Logger.Warn("初始化已知服务器列表失败，下次继续重试: "+err.Error(), "monitor")
			m.persistMu.Unlock()
			return
		}
		m.subsMu.Lock()
		m.knownServers = current
		m.knownServersInitialized = true
		m.subsMu.Unlock()
		m.state.Logger.Info(fmt.Sprintf("初始化已知服务器列表: %d 台", len(current)), "monitor")
		m.persistMu.Unlock()
		return
	}
	newServers := []string{}
	for k := range current {
		if _, ok := m.knownServers[k]; !ok {
			newServers = append(newServers, k)
		}
	}
	newServerDetails := make([]map[string]interface{}, 0, len(newServers))
	if len(newServers) > 0 {
		for _, code := range newServers {
			for _, s := range currentServerList {
				if pc, _ := s["planCode"].(string); pc == code {
					newServerDetails = append(newServerDetails, s)
				}
			}
		}
	}
	m.subsMu.Unlock()

	if len(newServers) == 0 {
		m.persistMu.Unlock()
		m.DispatchNotificationOutbox()
		return
	}
	channels := NotificationTargetChannels(m.state)
	entries := make([]types.NotificationOutboxEntry, 0, len(newServerDetails))
	for _, server := range newServerDetails {
		planCode, _ := server["planCode"].(string)
		payload, err := json.Marshal(server)
		if err != nil {
			m.state.Logger.Warn("序列化新服务器通知失败: "+err.Error(), "monitor")
			m.persistMu.Unlock()
			return
		}
		// 即使当前没有已启用渠道，也要先把事件写入 awaiting outbox。
		// 如果只保存 known_servers 而跳过事件，之后用户重新启用通知时
		// 该服务器已经被视为“已知”，新服务器提醒会永久漏掉。
		entries = append(entries, types.NotificationOutboxEntry{
			EventKey: "new_server:" + planCode, Kind: NotificationKindNewServer,
			Payload: string(payload), Channels: append([]string(nil), channels...),
			AwaitingChannels: len(channels) == 0,
		})
	}
	if err := m.state.DB.SaveKnownServersAndNotifications(sortedKnownServers(current), entries); err != nil {
		m.state.Logger.Warn("保存新服务器基线与通知失败，下次继续重试: "+err.Error(), "monitor")
		m.persistMu.Unlock()
		return
	}
	m.subsMu.Lock()
	m.knownServers = current
	m.knownServersInitialized = true
	m.subsMu.Unlock()
	m.state.Logger.Info(fmt.Sprintf("检测到 %d 台新服务器上架", len(newServers)), "monitor")
	m.persistMu.Unlock()

	// 通知发送完全放在持久化屏障之外。网络阻塞不能暂停订阅检查、
	// 订阅修改或账户删除。
	m.DispatchNotificationOutbox()
}

func sortedKnownServers(known map[string]struct{}) []string {
	codes := make([]string, 0, len(known))
	for code := range known {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// runSubscriptionCheck 对应 Python: _run_subscription_check
func (m *Monitor) runSubscriptionCheck(sub *Subscription, traceID string) {
	planCode := sub.PlanCode
	m.state.Logger.Info("开始处理订阅: "+planCode, "monitor")
	// CheckAvailabilityChange 使用工作副本执行网络请求，并在短暂的状态提交
	// 临界区内重新确认旧指针仍属于当前快照。不要在整个网络检查期间持有
	// persistMu，否则通知接口阻塞会拖住订阅编辑、账户删除和全量持久化。
	if !m.stillInSubscriptions(sub) {
		m.state.Logger.Debug(fmt.Sprintf("[trace:%s] 订阅 %s 已在等待期间被删除或替换，跳过旧快照", traceID, planCode), "monitor")
		return
	}
	m.CheckAvailabilityChange(sub, traceID)
	m.state.Logger.Info("完成处理订阅: "+planCode, "monitor")
}

// monitorLoop 对应 Python: monitor_loop
func (m *Monitor) monitorLoop(stop <-chan struct{}, done *sync.WaitGroup) {
	defer done.Done()
	m.state.Logger.Info("监控循环已启动", "monitor")
	for {
		// 使用本次启动专属的 stop channel，避免 Stop 等待期间新的 Start
		// 把 running 重置为 true 后，旧 goroutine 被误唤醒继续运行。
		select {
		case <-stop:
			m.state.Logger.Info("监控循环收到停止信号", "monitor")
			return
		default:
		}
		m.subsMu.Lock()
		running := m.running
		m.subsMu.Unlock()
		if !running {
			break
		}

		m.checkNotifications()
		m.DispatchNotificationOutbox()

		m.cleanupExpiredCaches()

		m.subsMu.Lock()
		count := len(m.subscriptions)
		subsCopy := make([]*Subscription, count)
		copy(subsCopy, m.subscriptions)
		interval := m.checkInterval
		m.subsMu.Unlock()

		if count > 0 {
			m.state.Logger.Info(fmt.Sprintf("开始检查 %d 个订阅...", count), "monitor")
			workers := m.maxWorkers
			if count < workers {
				workers = count
			}
			if workers < 1 {
				workers = 1
			}
			sem := make(chan struct{}, workers)
			var wg sync.WaitGroup
			for _, sub := range subsCopy {
				m.subsMu.Lock()
				running := m.running
				m.subsMu.Unlock()
				if !running {
					break
				}
				if !m.stillInSubscriptions(sub) {
					m.state.Logger.Debug(fmt.Sprintf("订阅 %s 在检查期间被删除，跳过", sub.PlanCode), "monitor")
					continue
				}
				traceID := uuid.NewString()
				wg.Add(1)
				sem <- struct{}{}
				go func(s *Subscription, tid string) {
					defer wg.Done()
					defer func() { <-sem }()
					defer func() {
						if r := recover(); r != nil {
							m.state.Logger.Error(fmt.Sprintf("[trace:%s] 并发检查订阅 %s 时异常: %v",
								tid, s.PlanCode, r), "monitor")
						}
					}()
					m.runSubscriptionCheck(s, tid)
				}(sub, traceID)
			}
			wg.Wait()
			// 持久化 LastStatus / History，避免重启后空基线触发误下单
			if err := m.SaveToDB(); err != nil {
				m.state.Logger.Warn("监控状态本轮未持久化，下轮将继续重试: "+err.Error(), "monitor")
			}
		} else {
			m.state.Logger.Info("当前无订阅，跳过检查", "monitor")
		}

		// 等下次（可中断 sleep）
		m.subsMu.Lock()
		running = m.running
		m.subsMu.Unlock()
		if running {
			m.state.Logger.Info(fmt.Sprintf("等待 %d 秒后进行下次检查...", interval), "monitor")
			timer := time.NewTimer(time.Duration(interval) * time.Second)
			select {
			case <-timer.C:
			case <-stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
		}
	}
	m.state.Logger.Info("监控循环已停止", "monitor")
}

func (m *Monitor) stillInSubscriptions(sub *Subscription) bool {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for _, s := range m.subscriptions {
		if s == sub {
			return true
		}
	}
	return false
}

// Start 对应 Python: start
func (m *Monitor) Start() bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.subsMu.Lock()
	if m.running {
		m.subsMu.Unlock()
		m.state.Logger.Warn("监控已在运行中", "monitor")
		return false
	}
	if !m.loaded {
		m.subsMu.Unlock()
		m.state.Logger.Warn("监控数据尚未安全加载，拒绝启动", "monitor")
		return false
	}
	m.running = true
	m.stopCh = make(chan struct{})
	interval := m.checkInterval
	done := &sync.WaitGroup{}
	done.Add(1)
	m.thread = done
	stop := m.stopCh
	m.subsMu.Unlock()
	// 重置 TG 检查时间戳,保证启动后第一轮一定 verify
	m.tgCheckMu.Lock()
	m.lastTGCheck = time.Time{}
	m.tgCheckMu.Unlock()
	go m.monitorLoop(stop, done)
	m.state.Logger.Info(fmt.Sprintf("服务器监控已启动 (检查间隔: %d秒)", interval), "monitor")
	return true
}

// Stop 对应 Python: stop
func (m *Monitor) Stop() bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.subsMu.Lock()
	if !m.running {
		m.subsMu.Unlock()
		m.state.Logger.Warn("监控未运行", "monitor")
		return false
	}
	m.running = false
	stop := m.stopCh
	done := m.thread
	m.stopCh = nil
	m.subsMu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		done.Wait()
	}
	m.state.Logger.Info("正在停止服务器监控...", "monitor")
	return true
}

// batchOrder 把本次补货边沿产生的全部任务和订阅状态放进同一事务。
// 返回 true 表示整个批次已原子入队；失败时一个也不会插入，PendingOrder
// 保持不变，下一轮可安全重试而不会重复下单。
func (m *Monitor) batchOrder(target, sub *Subscription, configInfo map[string]interface{}, targets []notification, accountID string) bool {
	if accountID == "" {
		m.state.Logger.Warn("[monitor->order] 跳过自动下单: 订阅未指定 auto_order 账户", "monitor")
		return false
	}
	if _, ok := m.state.FindAccount(accountID); !ok {
		m.state.Logger.Warn("[monitor->order] 跳过自动下单: 账户不存在", "monitor")
		return false
	}
	if target == nil || sub == nil {
		return false
	}
	planCode := sub.PlanCode
	totalOrders := 0
	for _, target := range targets {
		if target.orderCount > 0 {
			totalOrders += target.orderCount
		}
	}
	if totalOrders == 0 {
		return true
	}
	m.state.Logger.Info(fmt.Sprintf("[monitor->order] 开始批量下单: %s, 配置数=1, 数据中心数=%d, 总订单数=%d",
		planCode, len(targets), totalOrders), "monitor")
	m.state.Logger.Info("[monitor->order] 下单条件：仅对从无货变有货的情况下单（过滤掉持续有货的情况）", "monitor")

	options := []string{}
	if configInfo != nil {
		if opts, ok := configInfo["options"].([]string); ok {
			options = opts
		} else if optsRaw, ok := configInfo["options"].([]interface{}); ok {
			for _, o := range optsRaw {
				if s, ok := o.(string); ok {
					options = append(options, s)
				}
			}
		}
	}

	now := types.NowISO()
	items := make([]types.QueueItem, 0, totalOrders)
	for _, n := range targets {
		for i := 0; i < n.orderCount; i++ {
			items = append(items, types.QueueItem{
				ID: uuid.NewString(), AccountID: accountID, PlanCode: planCode, Datacenter: n.dc,
				Options: append([]string(nil), options...), Status: "running", RetryInterval: 2, MaxRetries: 3,
				CreatedAt: now, UpdatedAt: now, QuickOrder: true, Priority: 100,
			})
		}
	}

	// 事务提交的订阅快照中清除本批待办；若事务失败，内存状态不会修改。
	// sub 是单轮检查的私有工作副本，不与其它 goroutine 共享，因此直接
	// 使用无锁副本；target 的真实状态仍在下方持锁后发布。
	persisted := cloneSubscriptionUnlocked(sub)
	consumePendingOrders(persisted.PendingOrder, targets)
	// 自动入队必须和订阅替换、账户删除及全量保存串行。worker 可能在
	// 查询库存/价格期间被用户删除或替换；旧指针不再属于当前快照时
	// 直接放弃，绝不能根据过时配置继续创建抢购任务。
	m.persistMu.RLock()
	defer m.persistMu.RUnlock()
	if !m.stillInSubscriptions(target) {
		m.state.Logger.Info("[monitor->order] 订阅已在检查期间删除或修改，放弃旧结果入队", "monitor")
		return false
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if !sameSubscriptionSettings(target, sub) {
		m.state.Logger.Info("[monitor->order] 订阅配置已在检查期间修改，放弃旧结果入队", "monitor")
		return false
	}
	if err := m.state.EnqueueMonitorOrders(toDBSub(persisted), items); err != nil {
		m.state.Logger.Warn(fmt.Sprintf("[monitor->order] 批量入队失败，保留全部 %d 个待办下轮重试: %s", totalOrders, err), "monitor")
		return false
	}
	sub.PendingOrder = cloneIntMap(persisted.PendingOrder)
	copySubscriptionState(target, persisted)
	m.state.Logger.Info(fmt.Sprintf("[monitor->order] 批量下单任务已原子入队: 成功=%d, 失败=0, 总计=%d", totalOrders, totalOrders), "monitor")
	return true
}

func consumePendingOrders(pending map[string]int, targets []notification) {
	for _, target := range targets {
		remaining := pending[target.statusKey] - target.orderCount
		if remaining > 0 {
			pending[target.statusKey] = remaining
		} else {
			delete(pending, target.statusKey)
		}
	}
}

// commitWorkingSubscription 在网络请求前后将工作副本提交回真实订阅。
// 只有 target 仍在当前快照且其关键用户配置未被修改时才写入，避免旧检查
// 结果覆盖编辑后的订阅。持锁时间仅包含内存复制和单条数据库 upsert。
func (m *Monitor) commitWorkingSubscription(target, working *Subscription) bool {
	if target == nil || working == nil {
		return false
	}
	m.persistMu.RLock()
	defer m.persistMu.RUnlock()
	if !m.stillInSubscriptions(target) {
		return false
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if !sameSubscriptionSettings(target, working) {
		return false
	}
	if m.state.DB != nil {
		if err := m.state.DB.UpsertMonitorSubscription(toDBSub(working)); err != nil {
			return false
		}
	}
	copySubscriptionState(target, working)
	return true
}

func sameSubscriptionSettings(left, right *Subscription) bool {
	return left != nil && right != nil &&
		left.PlanCode == right.PlanCode &&
		left.ServerName == right.ServerName &&
		sameStringSlice(left.Datacenters, right.Datacenters) &&
		sameStringSlice(left.Memories, right.Memories) &&
		sameStringSlice(left.Storages, right.Storages) &&
		sameStringSlice(left.Networks, right.Networks) &&
		left.NotifyAvailable == right.NotifyAvailable &&
		left.NotifyUnavailable == right.NotifyUnavailable &&
		left.AutoOrder == right.AutoOrder &&
		left.Quantity == right.Quantity &&
		left.AutoOrderAccountID == right.AutoOrderAccountID
}

func copySubscriptionState(dst, src *Subscription) {
	dst.LastStatus = cloneStringMap(src.LastStatus)
	dst.ConfirmedStatus = cloneStringMap(src.ConfirmedStatus)
	dst.PendingOrder = cloneIntMap(src.PendingOrder)
	dst.PendingNotify = cloneStringMap(src.PendingNotify)
	dst.PendingNotifyChannels = cloneStringSliceMap(src.PendingNotifyChannels)
	dst.History = make([]HistoryEntry, len(src.History))
	for i, entry := range src.History {
		dst.History[i] = entry
		dst.History[i].Config = copyMap(entry.Config)
	}
}
