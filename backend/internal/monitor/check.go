package monitor

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/ovh"
)

// notification 单次状态变化通知（内部）
type notification struct {
	dc               string
	status           string
	oldStatus        string
	hasOld           bool
	statusKey        string
	changeType       string
	priceCheckFailed bool
	priceCheckError  string
	configTraceID    string
	traceID          string
	detectedTime     string
	durationText     string
	orderCount       int
	channelsKey      string
}

func setPendingNotification(sub *Subscription, statusKey, changeType string, channels []string) {
	if sub.PendingNotify == nil {
		sub.PendingNotify = map[string]string{}
	}
	if sub.PendingNotifyChannels == nil {
		sub.PendingNotifyChannels = map[string][]string{}
	}
	channels = canonicalNotificationChannels(channels)
	if changeType == "" || len(channels) == 0 {
		delete(sub.PendingNotify, statusKey)
		delete(sub.PendingNotifyChannels, statusKey)
		return
	}
	sub.PendingNotify[statusKey] = changeType
	sub.PendingNotifyChannels[statusKey] = channels
}

func clearPendingNotification(sub *Subscription, statusKey string) {
	delete(sub.PendingNotify, statusKey)
	delete(sub.PendingNotifyChannels, statusKey)
}

func pendingChannelsForKeys(sub *Subscription, keys []string, defaults []string) []string {
	all := make([]string, 0)
	for _, key := range keys {
		channels, exists := sub.PendingNotifyChannels[key]
		if !exists {
			channels = defaults
		}
		all = append(all, channels...)
	}
	return canonicalNotificationChannels(all)
}

func applyNotificationDelivery(sub *Subscription, keys []string, attempted []string, delivered NotificationDeliveryResult) bool {
	allDelivered := true
	for _, key := range keys {
		channels, exists := sub.PendingNotifyChannels[key]
		if !exists {
			channels = attempted
		}
		remaining := remainingNotificationChannels(channels, delivered)
		if len(remaining) == 0 {
			clearPendingNotification(sub, key)
			continue
		}
		allDelivered = false
		sub.PendingNotifyChannels[key] = remaining
	}
	return allDelivered
}

// notificationStillCurrent closes the gap between the pre-send state commit
// and the actual network call. It rejects a stale working copy when the user
// deleted/edited the subscription or the pending event changed meanwhile.
func (m *Monitor) notificationStillCurrent(target, working *Subscription, keys []string, changeType string, channels []string) bool {
	if target == nil || working == nil || len(keys) == 0 || len(channels) == 0 {
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
	for _, key := range keys {
		if target.PendingNotify[key] != changeType {
			return false
		}
		pendingChannels, exists := target.PendingNotifyChannels[key]
		if !exists {
			pendingChannels = channels
		}
		if len(pendingChannels) == 0 {
			return false
		}
		for _, channel := range channels {
			if !notificationChannelSelected(pendingChannels, channel) {
				return false
			}
		}
	}
	if !sameStringSlice(EnabledNotificationChannels(m.state, channels), canonicalNotificationChannels(channels)) {
		return false
	}
	return true
}

// prepareNotificationChannelsForSend 在真正发网络请求前再次过滤全局渠道开关。
// 用户可能在预提交后关闭某个渠道；显式空快照表示该事件已无目标，不能再
// 用默认渠道回填。返回值为空时调用方应清理/跳过该事件。
func (m *Monitor) prepareNotificationChannelsForSend(target, working *Subscription, keys []string, expected []string) ([]string, bool) {
	if m == nil || working == nil || len(keys) == 0 {
		return nil, false
	}
	enabled := EnabledNotificationChannels(m.state, expected)
	changed := false
	all := make([]string, 0, len(enabled))
	for _, key := range keys {
		pending, exists := working.PendingNotifyChannels[key]
		if !exists {
			pending = expected // 兼容旧库中缺失渠道快照的事件
		}
		filtered := intersectNotificationChannels(pending, enabled)
		if len(filtered) == 0 {
			if working.PendingNotify[key] != "" {
				clearPendingNotification(working, key)
				changed = true
			}
			continue
		}
		if !exists || !sameStringSlice(canonicalNotificationChannels(pending), filtered) {
			if working.PendingNotifyChannels == nil {
				working.PendingNotifyChannels = map[string][]string{}
			}
			working.PendingNotifyChannels[key] = filtered
			changed = true
		}
		all = append(all, filtered...)
	}
	channels := canonicalNotificationChannels(all)
	if changed && !m.commitWorkingSubscription(target, working) {
		return nil, false
	}
	return channels, true
}

func intersectNotificationChannels(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, channel := range canonicalNotificationChannels(right) {
		rightSet[channel] = struct{}{}
	}
	out := make([]string, 0, len(left))
	for _, channel := range canonicalNotificationChannels(left) {
		if _, ok := rightSet[channel]; ok {
			out = append(out, channel)
		}
	}
	return canonicalNotificationChannels(out)
}

type monitorPriceCheck struct {
	text string
	ok   bool
	err  string
}

type availabilityNotificationGroup struct {
	priceText     string
	priceError    string
	notifications []notification
}

// applyMonitorStatus 只处理单个配置/机房的状态迁移，不执行网络或数据库操作。
// ConfirmedStatus 只记录明确有货/无货；price_check_failed 不覆盖确认状态，
// 从而保留 unavailable → 校价失败 → available 的补货边沿。
func applyMonitorStatus(sub *Subscription, statusKey, actualStatus, oldStatus string, hasOld bool,
	confirmedOld string, hasConfirmed bool, channels ...[]string) string {
	changeType := ""
	switch actualStatus {
	case "available":
		if sub.AutoOrder && sub.AutoOrderAccountID != "" && hasConfirmed && confirmedOld == "unavailable" {
			quantity := sub.Quantity
			if quantity < 1 {
				quantity = 1
			}
			if sub.PendingOrder[statusKey] < 1 {
				sub.PendingOrder[statusKey] = quantity
			}
		}
		if sub.NotifyAvailable && (!hasConfirmed || confirmedOld != "available") {
			changeType = "available"
		}
		sub.ConfirmedStatus[statusKey] = "available"
	case "unavailable":
		delete(sub.PendingOrder, statusKey)
		if sub.NotifyUnavailable && (!hasConfirmed || confirmedOld != "unavailable") {
			changeType = "unavailable"
		}
		sub.ConfirmedStatus[statusKey] = "unavailable"
	case "price_check_failed":
		if sub.NotifyAvailable && (!hasOld || oldStatus != "price_check_failed") {
			changeType = "price_check_failed"
		}
	}
	if pending := sub.PendingNotify[statusKey]; pending != "" && pending == actualStatus {
		changeType = pending
	}
	if changeType != "" {
		targetChannels := []string{}
		if len(channels) > 0 {
			targetChannels = channels[0]
		}
		if pending := sub.PendingNotify[statusKey]; pending == changeType {
			if pendingChannels, exists := sub.PendingNotifyChannels[statusKey]; exists {
				targetChannels = pendingChannels
			}
		}
		setPendingNotification(sub, statusKey, changeType, targetChannels)
	} else if pending := sub.PendingNotify[statusKey]; pending != "" && pending != actualStatus {
		clearPendingNotification(sub, statusKey)
	}
	sub.LastStatus[statusKey] = actualStatus
	return changeType
}

func groupAvailabilityNotifications(notifications []notification, priceChecks map[string]monitorPriceCheck) []availabilityNotificationGroup {
	sort.Slice(notifications, func(i, j int) bool { return notifications[i].dc < notifications[j].dc })
	groups := make([]availabilityNotificationGroup, 0)
	groupIndex := make(map[string]int)
	for _, n := range notifications {
		checked, ok := priceChecks[n.dc]
		priceText := ""
		priceError := ""
		if ok {
			priceText = checked.text
			priceError = checked.err
		} else {
			priceError = "价格校验结果不存在"
		}
		if priceText == "" && priceError == "" {
			priceError = "价格接口未返回结果"
		}
		// 目标渠道不同的事件不能合并，否则会把消息发给不应接收的渠道，
		// 也会把某个渠道的成功错误应用到另一事件。
		key := priceText + "\x00" + priceError + "\x00" + n.channelsKey
		idx, exists := groupIndex[key]
		if !exists {
			idx = len(groups)
			groupIndex[key] = idx
			groups = append(groups, availabilityNotificationGroup{
				priceText:  priceText,
				priceError: priceError,
			})
		}
		groups[idx].notifications = append(groups[idx].notifications, n)
	}
	return groups
}

func groupNotificationsByChannels(notifications []notification) [][]notification {
	groups := make([][]notification, 0)
	indices := make(map[string]int)
	for _, n := range notifications {
		idx, ok := indices[n.channelsKey]
		if !ok {
			idx = len(groups)
			indices[n.channelsKey] = idx
			groups = append(groups, []notification{})
		}
		groups[idx] = append(groups[idx], n)
	}
	return groups
}

// pendingOrderTargets 返回当前配置仍然明确有货的自动下单待办，并限制
// 本批任务数不超过队列剩余容量。容量不足时未纳入本批的数量继续留在
// PendingOrder，下一轮再入队，避免一次异常大的 quantity 永久饿死。
func pendingOrderTargets(sub *Subscription, statuses map[string]dcStatusSnapshot, maxOrders int) []notification {
	if sub == nil || maxOrders <= 0 {
		return nil
	}
	dcs := make([]string, 0, len(statuses))
	for dc := range statuses {
		dcs = append(dcs, dc)
	}
	sort.Strings(dcs)
	targets := make([]notification, 0)
	for _, dc := range dcs {
		status := statuses[dc]
		remaining := sub.PendingOrder[status.statusKey]
		if remaining <= 0 || status.actualStatus != "available" {
			continue
		}
		if remaining > maxOrders {
			remaining = maxOrders
		}
		targets = append(targets, notification{dc: dc, statusKey: status.statusKey, orderCount: remaining})
		maxOrders -= remaining
		if maxOrders == 0 {
			break
		}
	}
	return targets
}

type dcStatusSnapshot struct {
	statusKey    string
	actualStatus string
}

func (n notification) oldStatusJSON() interface{} {
	if !n.hasOld {
		return nil
	}
	return n.oldStatus
}

// CheckAvailabilityChange 对应 Python: check_availability_change。
// 只在复制订阅状态时短暂持有 sub.mu；库存、价格和通知请求均在锁外执行，
// 避免通知接口阻塞时卡住订阅编辑、账户删除和全量持久化。
func (m *Monitor) CheckAvailabilityChange(target *Subscription, traceID string) {
	if target == nil {
		return
	}
	// 同一订阅不能并发执行两轮检查；否则两个旧工作副本可能交错发送
	// 重复通知或覆盖彼此的 PendingNotify。不同订阅仍可并发。
	target.checkMu.Lock()
	defer target.checkMu.Unlock()
	target.mu.Lock()
	working := cloneSubscriptionUnlocked(target)
	target.mu.Unlock()
	m.checkAvailabilityChange(target, working, traceID)
}

// checkAvailabilityChange 在 working 副本上执行一次完整检查。target 只用于
// 提交状态前确认该订阅仍属于当前快照；如果用户在网络请求期间修改/删除了
// 订阅，旧结果会被丢弃，不会覆盖新配置或继续自动下单。
func (m *Monitor) checkAvailabilityChange(target, sub *Subscription, traceID string) {

	// 更新入口会清理已关闭渠道的待通知，但旧数据库记录或其它调用入口
	// 仍可能留下事件。检查开始时再次清理，防止开关关闭后继续发送旧通知。
	if clearDisabledPendingNotify(sub, sub.NotifyAvailable, sub.NotifyUnavailable) {
		if !m.commitWorkingSubscription(target, sub) {
			m.state.Logger.Warn("清理已关闭通知渠道的待发送事件落盘失败或订阅已变更", "monitor")
			return
		}
	}

	planCode := sub.PlanCode
	// 自动下单订阅必须使用指定账户的区域和价格判断库存；普通通知订阅
	// 使用当前默认账户。
	notificationAccountID := m.resolvePriceAccount(sub)
	availabilityResult, err := catalog.CheckServerAvailabilityWithConfigsStrict(m.state, planCode, notificationAccountID)
	if err != nil || len(availabilityResult.Configs) == 0 {
		if err != nil {
			m.state.Logger.Warn(fmt.Sprintf("无法安全获取 %s 的可用性信息: %s", planCode, err.Error()), "monitor")
		} else {
			m.state.Logger.Warn(fmt.Sprintf("无法获取 %s 的可用性信息", planCode), "monitor")
		}
		return
	}
	currentAvailability := availabilityResult.Configs

	if sub.LastStatus == nil {
		sub.LastStatus = map[string]string{}
	}
	if sub.ConfirmedStatus == nil {
		sub.ConfirmedStatus = map[string]string{}
	}
	if sub.PendingOrder == nil {
		sub.PendingOrder = map[string]int{}
	}
	if sub.PendingNotify == nil {
		sub.PendingNotify = map[string]string{}
	}
	if sub.PendingNotifyChannels == nil {
		sub.PendingNotifyChannels = map[string][]string{}
	}
	// 记录所有当前启用的渠道，即使凭据暂时无效也保留待办，待渠道恢复后重试。
	notificationChannels := PendingNotificationChannels(m.state)
	// 兼容旧数据库中没有渠道快照的事件，并清理用户后来明确关闭的渠道。
	pendingNotifyChanged := false
	for key := range sub.PendingNotify {
		channels, exists := sub.PendingNotifyChannels[key]
		if !exists {
			channels = notificationChannels
		}
		channels = EnabledNotificationChannels(m.state, channels)
		if len(channels) == 0 {
			clearPendingNotification(sub, key)
			pendingNotifyChanged = true
			continue
		}
		if !sameStringSlice(sub.PendingNotifyChannels[key], channels) {
			sub.PendingNotifyChannels[key] = channels
			pendingNotifyChanged = true
		}
	}
	// 若本轮可用性查询成功但所有配置都被筛选掉，后续配置循环没有提交点。
	// 旧事件的渠道兼容/关闭清理仍必须立即落盘，否则重启后会再次投递。
	if pendingNotifyChanged && !m.commitWorkingSubscription(target, sub) {
		m.state.Logger.Warn("保存待通知渠道清理结果失败或订阅已变更", "monitor")
		return
	}
	lastStatus := sub.LastStatus
	confirmedStatus := sub.ConfirmedStatus
	monitoredDCs := sub.Datacenters
	serverNetwork := m.serverNetwork(planCode)

	m.state.Logger.Info(fmt.Sprintf("订阅 %s - 监控数据中心: %v", planCode, monitoredDCs), "monitor")
	m.state.Logger.Info(fmt.Sprintf("订阅 %s - 当前发现 %d 个配置组合", planCode, len(currentAvailability)), "monitor")

	for configKey, configData := range currentAvailability {
		memory := configData.Memory
		storage := configData.Storage
		if !matchesMonitorFilters(sub, memory, storage, serverNetwork, configData.Options) {
			continue
		}
		configDisplay := memory + " + " + storage

		configTraceID := uuid.NewString()
		m.state.Logger.Info(fmt.Sprintf("检查配置: %s [config-trace:%s]", configDisplay, configTraceID), "monitor")

		configInfo := map[string]interface{}{
			"memory":    memory,
			"storage":   storage,
			"display":   configDisplay,
			"options":   configData.Options,
			"accountId": notificationAccountID,
		}

		type dcStatus struct {
			status        string
			statusKey     string
			oldStatus     string
			hasOld        bool
			confirmedOld string
			hasConfirmed bool
		}
		dcStatusMap := map[string]dcStatus{}
		priceCheckTasks := []string{}
		for dc, status := range configData.Datacenters {
			if len(monitoredDCs) > 0 && !monitorDatacenterMatches(monitoredDCs, dc) {
				continue
			}
			statusKey := dc + "|" + configKey
			old, hasOld := lastStatus[statusKey]
			confirmedOld, hasConfirmed := confirmedStatus[statusKey]
			dcStatusMap[dc] = dcStatus{status: status, statusKey: statusKey, oldStatus: old, hasOld: hasOld, confirmedOld: confirmedOld, hasConfirmed: hasConfirmed}
			// unknown/空状态已在 Strict catalog 解析阶段拒绝；只有 OVH 明确
			// 返回的非 unavailable 状态才允许进入购物车校价。
			if catalog.AvailabilityExplicitlyAvailable(status) {
				priceCheckTasks = append(priceCheckTasks, dc)
			}
		}

		// 并发价格校验
		priceCheckResults := map[string]monitorPriceCheck{}
		if len(priceCheckTasks) > 0 {
			var pcMu sync.Mutex
			var wg sync.WaitGroup
			workers := len(priceCheckTasks)
			if workers > 10 {
				workers = 10
			}
			sem := make(chan struct{}, workers)
			for _, dc := range priceCheckTasks {
				wg.Add(1)
				sem <- struct{}{}
				go func(dc string) {
					defer wg.Done()
					defer func() { <-sem }()
					priceText, ok, errMsg := m.verifyPriceAvailable(notificationAccountID, planCode, dc, configInfo)
					pcMu.Lock()
					priceCheckResults[dc] = monitorPriceCheck{text: priceText, ok: ok, err: errMsg}
					pcMu.Unlock()
					if ok {
						m.state.Logger.Info(fmt.Sprintf("%s@%s [%s] 价格校验通过 [config-trace:%s]",
							planCode, dc, configDisplay, configTraceID), "monitor")
					} else {
						m.state.Logger.Info(fmt.Sprintf("%s@%s [%s] 价格校验失败，原因: %s [config-trace:%s]",
							planCode, dc, configDisplay, errMsg, configTraceID), "monitor")
					}
				}(dc)
			}
			wg.Wait()
		}

		notifications := []notification{}
		actualStatuses := map[string]string{}

		for dc, ds := range dcStatusMap {
			actualStatus := ds.status
			priceCheckFailed := false
			priceCheckError := ""

			if catalog.AvailabilityExplicitlyAvailable(ds.status) {
				if v, ok := priceCheckResults[dc]; ok {
					okBool := v.ok
					errStr := v.err
					if !okBool {
						actualStatus = "price_check_failed"
						priceCheckFailed = true
						priceCheckError = errStr
						m.state.Logger.Info(fmt.Sprintf("%s@%s [%s] 可用性显示有货但价格校验失败，原因: %s，标记为price_check_failed",
							planCode, dc, configDisplay, errStr), "monitor")
					} else {
						actualStatus = "available"
						m.state.Logger.Info(fmt.Sprintf("%s@%s [%s] 可用性有货且价格校验通过，确认有货",
							planCode, dc, configDisplay), "monitor")
					}
				} else {
					actualStatus = "price_check_failed"
					priceCheckFailed = true
					priceCheckError = "价格校验未执行"
				}
			}

			changeType := applyMonitorStatus(sub, ds.statusKey, actualStatus, ds.oldStatus, ds.hasOld,
				ds.confirmedOld, ds.hasConfirmed, notificationChannels)

			if changeType != "" {
				detectedTime := m.nowBeijing().Format(time.RFC3339Nano)
				n := notification{
					dc:               dc,
					status:           actualStatus,
					oldStatus:        ds.confirmedOld,
					hasOld:           ds.hasConfirmed,
					statusKey:        ds.statusKey,
					changeType:       changeType,
					priceCheckFailed: priceCheckFailed,
					priceCheckError:  priceCheckError,
					configTraceID:    configTraceID,
					traceID:          traceID,
					detectedTime:     detectedTime,
				}
				n.channelsKey = notificationChannelKey(sub.PendingNotifyChannels[ds.statusKey])
				if changeType == "available" && ds.confirmedOld == "unavailable" {
					n.durationText = m.calcDuration(sub, dc, configDisplay, []string{"unavailable", "price_check_failed"})
				}
				notifications = append(notifications, n)
			}

			actualStatuses[ds.statusKey] = actualStatus
		}

		// 分类
		var availables, unavailables, priceFailed []notification
		for _, n := range notifications {
			switch n.changeType {
			case "available":
				availables = append(availables, n)
			case "unavailable":
				unavailables = append(unavailables, n)
			case "price_check_failed":
				priceFailed = append(priceFailed, n)
			}
		}

		// 自动下单待办只在确认的 unavailable -> available 边沿创建。
		// 入队失败会保留剩余数量，下一轮继续补齐。
		orderStatuses := make(map[string]dcStatusSnapshot, len(dcStatusMap))
		for dc, ds := range dcStatusMap {
			orderStatuses[dc] = dcStatusSnapshot{statusKey: ds.statusKey, actualStatus: actualStatuses[ds.statusKey]}
		}
		orderTargets := pendingOrderTargets(sub, orderStatuses, m.state.AvailableQueueSlots())
		// 触发条件:订阅勾了 AutoOrder + 指定了某个账户。
		// 没账户(AutoOrderAccountID="") → 只发可用通知,不下单(用户明确决定)。
		if len(orderTargets) > 0 && sub.AutoOrder {
			if sub.AutoOrderAccountID == "" {
				m.state.Logger.Info(fmt.Sprintf("[monitor] %s 触发 auto_order 但未指定账户,只通知不下单", planCode), "monitor")
			} else {
				m.batchOrder(target, sub, configInfo, orderTargets, sub.AutoOrderAccountID)
			}
		}

		// 在任何网络通知前先保存本轮状态和 PendingNotify。发送成功后会在
		// 各分支再次保存清理结果；若进程此时退出，重启仍会重试未确认事件。
		if !m.commitWorkingSubscription(target, sub) {
			m.state.Logger.Warn(fmt.Sprintf("订阅 %s 状态预落盘失败或订阅已变更，本轮不发送通知", planCode), "monitor")
			return
		}

		// 发送有货通知
		if len(availables) > 0 {
			// 首月总价由逐机房购物车询价得到。只有价格文本相同的机房才聚合，
			// 防止把一个机房的价格误显示到另一个机房。
			for _, group := range groupAvailabilityNotifications(availables, priceCheckResults) {
				m.state.Logger.Info(fmt.Sprintf("准备发送汇总提醒: %s [%s] - %d个机房有货",
					planCode, configDisplay, len(group.notifications)), "monitor")
				configInfoWithPrice := copyMap(configInfo)
				if group.priceText != "" {
					configInfoWithPrice["cached_price"] = group.priceText
					m.state.Logger.Debug(fmt.Sprintf("配置 %s 复用价格校验结果: %s", configDisplay, group.priceText), "monitor")
				} else {
					m.state.Logger.Warn(fmt.Sprintf("配置 %s 价格结果为空，通知中将显示价格不可用", configDisplay), "monitor")
				}
				availDCs := make([]map[string]interface{}, 0, len(group.notifications))
				for _, n := range group.notifications {
					dcInfo := map[string]interface{}{"dc": n.dc, "status": n.status}
					if n.durationText != "" {
						dcInfo["duration_text"] = n.durationText
					}
					if n.detectedTime != "" {
						dcInfo["detected_time"] = n.detectedTime
					}
					availDCs = append(availDCs, dcInfo)
				}
				configTraceForNotif := group.notifications[0].configTraceID
				groupKeys := make([]string, 0, len(group.notifications))
				for _, n := range group.notifications { groupKeys = append(groupKeys, n.statusKey) }
				expectedChannels := pendingChannelsForKeys(sub, groupKeys, notificationChannels)
				var prepared bool
				expectedChannels, prepared = m.prepareNotificationChannelsForSend(target, sub, groupKeys, expectedChannels)
				if !prepared {
					m.state.Logger.Info(fmt.Sprintf("订阅 %s 有货通知渠道更新时订阅已变更，跳过旧通知", planCode), "monitor")
					return
				}
				if len(expectedChannels) == 0 {
					m.state.Logger.Info(fmt.Sprintf("订阅 %s 有货通知的目标渠道均已关闭，已清理待办", planCode), "monitor")
					continue
				}
				if !m.notificationStillCurrent(target, sub, groupKeys, "available", expectedChannels) {
					m.state.Logger.Info(fmt.Sprintf("订阅 %s 有货通知在发送前已失效，跳过旧通知", planCode), "monitor")
					return
				}
				delivered := m.SendAvailabilityAlertGrouped(planCode, availDCs, configInfoWithPrice, sub.ServerName,
					group.priceError, traceID, configTraceForNotif, expectedChannels)
				applyNotificationDelivery(sub, groupKeys, expectedChannels, delivered)
				for _, n := range group.notifications {
					if _, pending := sub.PendingNotify[n.statusKey]; pending {
						continue
					}
					entry := HistoryEntry{
						Timestamp:  m.nowBeijing().Format(time.RFC3339Nano),
						Datacenter: n.dc,
						Status:     n.status,
						ChangeType: n.changeType,
						OldStatus:  n.oldStatusJSON(),
						Config:     configInfo,
					}
					sub.History = append(sub.History, entry)
				}
				if !m.commitWorkingSubscription(target, sub) {
					m.state.Logger.Warn(fmt.Sprintf("订阅 %s 有货通知状态落盘失败或订阅已变更", planCode), "monitor")
					return
				}
			}
		}

		// 价格校验失败通知
		for _, n := range priceFailed {
			m.state.Logger.Info(fmt.Sprintf("准备发送价格校验失败提醒: %s@%s [%s] - 可用性有货但价格校验失败",
				planCode, n.dc, configDisplay), "monitor")
			priceTextFailed := ""
			if checked, ok := priceCheckResults[n.dc]; ok {
				priceTextFailed = checked.text
			}
			configInfoFailed := copyMap(configInfo)
			if priceTextFailed != "" {
				configInfoFailed["cached_price"] = priceTextFailed
				configInfoFailed["price_check_error"] = n.priceCheckError
			}
			expectedChannels := pendingChannelsForKeys(sub, []string{n.statusKey}, notificationChannels)
			var prepared bool
			expectedChannels, prepared = m.prepareNotificationChannelsForSend(target, sub, []string{n.statusKey}, expectedChannels)
			if !prepared {
				m.state.Logger.Info(fmt.Sprintf("订阅 %s 价格失败通知渠道更新时订阅已变更，跳过旧通知", planCode), "monitor")
				return
			}
			if len(expectedChannels) == 0 {
				m.state.Logger.Info(fmt.Sprintf("订阅 %s 价格失败通知的目标渠道均已关闭，已清理待办", planCode), "monitor")
				continue
			}
			if !m.notificationStillCurrent(target, sub, []string{n.statusKey}, "price_check_failed", expectedChannels) {
				m.state.Logger.Info(fmt.Sprintf("订阅 %s 价格失败通知在发送前已失效，跳过旧通知", planCode), "monitor")
				return
			}
			delivered := m.SendAvailabilityAlert(planCode, n.dc, "unavailable", "price_check_failed",
				configInfoFailed, sub.ServerName, "", n.priceCheckError, traceID, n.configTraceID, n.detectedTime, expectedChannels)
			applyNotificationDelivery(sub, []string{n.statusKey}, expectedChannels, delivered)
			if _, pending := sub.PendingNotify[n.statusKey]; pending {
				if !m.commitWorkingSubscription(target, sub) {
					m.state.Logger.Warn(fmt.Sprintf("订阅 %s 价格失败通知分渠道状态落盘失败或订阅已变更", planCode), "monitor")
					return
				}
				continue
			}
			entry := HistoryEntry{
				Timestamp:  m.nowBeijing().Format(time.RFC3339Nano),
				Datacenter: n.dc,
				Status:     "price_check_failed",
				ChangeType: "price_check_failed",
				OldStatus:  n.oldStatusJSON(),
				Config:     configInfo,
			}
			sub.History = append(sub.History, entry)
			if !m.commitWorkingSubscription(target, sub) {
				m.state.Logger.Warn(fmt.Sprintf("订阅 %s 价格失败通知状态落盘失败或订阅已变更", planCode), "monitor")
				return
			}
		}

		// 下架聚合通知
		if len(unavailables) > 0 {
			for _, unavailableGroup := range groupNotificationsByChannels(unavailables) {
				m.state.Logger.Info(fmt.Sprintf("准备发送聚合下架提醒: %s [%s] - %d个机房",
					planCode, configDisplay, len(unavailableGroup)), "monitor")
				unavailDCs := make([]map[string]interface{}, 0, len(unavailableGroup))
				for _, n := range unavailableGroup {
					dcInfo := map[string]interface{}{"dc": n.dc, "status": n.status}
					isBecame := n.changeType == "unavailable" && n.hasOld && n.oldStatus != "unavailable"
					if isBecame {
						if d := m.calcDuration(sub, n.dc, configDisplay, []string{"available"}); d != "" {
							dcInfo["duration_text"] = d
						}
					}
					unavailDCs = append(unavailDCs, dcInfo)
				}
				configTraceForNotif := ""
				if len(unavailableGroup) > 0 {
					configTraceForNotif = unavailableGroup[0].configTraceID
				}
				groupKeys := make([]string, 0, len(unavailableGroup))
				for _, n := range unavailableGroup {
					groupKeys = append(groupKeys, n.statusKey)
				}
				expectedChannels := pendingChannelsForKeys(sub, groupKeys, notificationChannels)
				var prepared bool
				expectedChannels, prepared = m.prepareNotificationChannelsForSend(target, sub, groupKeys, expectedChannels)
				if !prepared {
					m.state.Logger.Info(fmt.Sprintf("订阅 %s 下架通知渠道更新时订阅已变更，跳过旧通知", planCode), "monitor")
					return
				}
				if len(expectedChannels) == 0 {
					m.state.Logger.Info(fmt.Sprintf("订阅 %s 下架通知的目标渠道均已关闭，已清理待办", planCode), "monitor")
					continue
				}
				if !m.notificationStillCurrent(target, sub, groupKeys, "unavailable", expectedChannels) {
					m.state.Logger.Info(fmt.Sprintf("订阅 %s 下架通知在发送前已失效，跳过旧通知", planCode), "monitor")
					return
				}
				delivered := m.SendUnavailableAlertGrouped(planCode, unavailDCs, configInfo, sub.ServerName,
					traceID, configTraceForNotif, expectedChannels)
				applyNotificationDelivery(sub, groupKeys, expectedChannels, delivered)
				for _, n := range unavailableGroup {
					if _, pending := sub.PendingNotify[n.statusKey]; pending {
						continue
					}
					entry := HistoryEntry{
						Timestamp:  m.nowBeijing().Format(time.RFC3339Nano),
						Datacenter: n.dc,
						Status:     n.status,
						ChangeType: n.changeType,
						OldStatus:  n.oldStatusJSON(),
						Config:     configInfo,
					}
					sub.History = append(sub.History, entry)
				}
				if !m.commitWorkingSubscription(target, sub) {
					m.state.Logger.Warn(fmt.Sprintf("订阅 %s 下架通知状态落盘失败或订阅已变更", planCode), "monitor")
					return
				}
			}
		}

		m.limitHistorySize(sub, 100)
	}
	// 只根据本轮响应中明确出现的状态更新库存。API 暂时省略某个配置或
	// 机房时保留上轮状态，避免不完整响应制造“下架”边沿并清掉自动下单待办；
	// 真正无货应由 OVH 明确返回 availability=unavailable。
	m.limitHistorySize(sub, 100)
	sub.LastStatus = lastStatus
	sub.ConfirmedStatus = confirmedStatus
	// 通常每个匹配配置已在网络通知前后提交；此处仍做最终提交，覆盖“响应
	// 有效但无配置命中筛选”以及未来新增的无通知状态路径，避免工作副本中
	// 已完成的兼容清理只停留在内存。
	if !m.commitWorkingSubscription(target, sub) {
		m.state.Logger.Warn(fmt.Sprintf("订阅 %s 最终状态落盘失败或订阅已变更", planCode), "monitor")
	}
}

// serverNetwork 返回目录中的基础网络规格。配置组合中的网络 addon 仍通过 options 匹配。
func (m *Monitor) serverNetwork(planCode string) string {
	m.state.ServerPlansMu.RLock()
	defer m.state.ServerPlansMu.RUnlock()
	for _, plan := range m.state.ServerPlans {
		if plan.PlanCode == planCode {
			return plan.Bandwidth
		}
	}
	return ""
}

func normalizeMonitorValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

func monitorValueMatches(selected, actual string) bool {
	s, a := normalizeMonitorValue(selected), normalizeMonitorValue(actual)
	if s == "" || a == "" {
		return false
	}
	return s == a || strings.Contains(a, s) || strings.Contains(s, a)
}

func monitorFilterMatches(values []string, actual string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if monitorValueMatches(value, actual) {
			return true
		}
	}
	return false
}

func matchesMonitorFilters(sub *Subscription, memory, storage, network string, options []string) bool {
	if !monitorFilterMatches(sub.Memories, memory) || !monitorFilterMatches(sub.Storages, storage) {
		return false
	}
	if len(sub.Networks) == 0 {
		return true
	}
	for _, selected := range sub.Networks {
		if monitorValueMatches(selected, network) {
			return true
		}
		for _, option := range options {
			if monitorValueMatches(selected, option) {
				return true
			}
		}
	}
	return false
}

func monitorDatacenterMatches(list []string, actual string) bool {
	actualAPI := ovh.ConvertDisplayDCToAPIDC(actual)
	for _, selected := range list {
		if ovh.ConvertDisplayDCToAPIDC(selected) == actualAPI {
			return true
		}
	}
	return false
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// calcDuration 计算最近一次相反状态到现在的历时
func (m *Monitor) calcDuration(sub *Subscription, dc, configDisplay string, targetChangeTypes []string) string {
	var lastTS string
	for i := len(sub.History) - 1; i >= 0; i-- {
		entry := sub.History[i]
		if entry.Datacenter != dc {
			continue
		}
		matched := false
		for _, t := range targetChangeTypes {
			if entry.ChangeType == t {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if configDisplay != "" && entry.Config != nil {
			if d, ok := entry.Config["display"].(string); ok && d != configDisplay {
				continue
			}
		}
		lastTS = entry.Timestamp
		if lastTS != "" {
			break
		}
	}
	if lastTS == "" {
		return ""
	}
	startDT, err := time.Parse(time.RFC3339Nano, lastTS)
	if err != nil {
		startDT, err = time.Parse(time.RFC3339, lastTS)
		if err != nil {
			return ""
		}
	}
	delta := m.nowBeijing().Sub(startDT)
	totalSec := int(delta.Seconds())
	if totalSec < 0 {
		totalSec = 0
	}
	days := totalSec / 86400
	rem := totalSec % 86400
	hours := rem / 3600
	minutes := (rem % 3600) / 60
	seconds := rem % 60
	if days > 0 {
		return fmt.Sprintf("历时 %d天%d小时%d分%d秒", days, hours, minutes, seconds)
	}
	if hours > 0 {
		return fmt.Sprintf("历时 %d小时%d分%d秒", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("历时 %d分%d秒", minutes, seconds)
	}
	return fmt.Sprintf("历时 %d秒", seconds)
}
