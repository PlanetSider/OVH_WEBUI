package vps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

var (
	runningMu     sync.Mutex
	running       bool
	monitorCancel context.CancelFunc
	monitorDone   chan struct{}

	// 通知渠道健康检查节流。loop 每 5 分钟验证一次。
	tgCheckMu   sync.Mutex
	lastTGCheck time.Time
)

const tgRecheckInterval = 5 * time.Minute

// checkNotifications 节流后验证 Telegram / 全局飞书 / 微信。渠道临时失效时
// 监控保持运行，待通知事件会在渠道恢复后继续重试。
func checkNotifications(state *app.State) {
	tgCheckMu.Lock()
	due := time.Since(lastTGCheck) >= tgRecheckInterval
	tgCheckMu.Unlock()
	if !due {
		return
	}
	feishuOK := false
	if monitor.FeishuEnabled(state) {
		_, feishuOK = monitor.FeishuDefaultBinding(state)
	}
	tgOK, tgReason := telegram.VerifyConfig(state)
	tgCheckMu.Lock()
	lastTGCheck = time.Now()
	tgCheckMu.Unlock()
	weixinOK := state.Config.Get().IsWeixinNotificationsEnabled() && state.Weixin != nil && state.Weixin.Configured()
	if !tgOK && !feishuOK && !weixinOK {
		state.Logger.Warn("Telegram、飞书与微信通知当前均失效，VPS监控继续运行并保留待通知事件: "+tgReason, "vps_monitor")
	}
}

// vpsAPIBaseURL 把 OVH subsidiary 映射到对应区域的 base URL。
// VPS 可用性接口是 public 的,但必须连对 region 才能查到对应 subsidiary 的 VPS。
// 默认走 EU(覆盖大部分情况);CA / ASIA / SG / IN / AU 走 CA;US 走 US 独立域名。
func vpsAPIBaseURL(subsidiary string) string {
	switch strings.ToUpper(subsidiary) {
	case "US":
		return "https://api.us.ovhcloud.com"
	case "CA", "QC", "ASIA", "SG", "AU", "IN", "MA", "TN", "SN", "WS":
		return "https://ca.api.ovh.com"
	default:
		return "https://eu.api.ovh.com"
	}
}

// CheckVPSDCAvailability 对应 Python: check_vps_datacenter_availability
func checkVPSDCAvailability(ctx context.Context, state *app.State, planCode, ovhSubsidiary string) map[string]interface{} {
	// 多账户:base URL 跟着订阅的 ovhSubsidiary 走,
	// 不再读旧的 state.Config(新建账户不写 kv['config'],它永远是空)
	baseURL := vpsAPIBaseURL(ovhSubsidiary)
	u := baseURL + "/v1/vps/order/rule/datacenter"
	params := url.Values{}
	params.Set("ovhSubsidiary", ovhSubsidiary)
	params.Set("planCode", planCode)
	fullURL := u + "?" + params.Encode()

	state.Logger.Info(fmt.Sprintf("检查VPS可用性: %s (subsidiary: %s)", planCode, ovhSubsidiary), "vps_monitor")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		state.Logger.Error("创建VPS可用性请求时出错: "+err.Error(), "vps_monitor")
		return nil
	}
	req.Header.Set("accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		state.Logger.Error("检查VPS可用性时出错: "+err.Error(), "vps_monitor")
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		state.Logger.Error("读取VPS数据中心信息时出错: "+err.Error(), "vps_monitor")
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		state.Logger.Error(fmt.Sprintf("获取VPS数据中心信息失败: HTTP %d", resp.StatusCode), "vps_monitor")
		return nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		state.Logger.Error("解析VPS数据中心信息时出错: "+err.Error(), "vps_monitor")
		return nil
	}
	state.Logger.Info("VPS "+planCode+" 数据中心信息获取成功", "vps_monitor")
	return data
}

// CheckVPSDCAvailability 供手动检查接口使用。
func CheckVPSDCAvailability(state *app.State, planCode, ovhSubsidiary string) map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return checkVPSDCAvailability(ctx, state, planCode, ovhSubsidiary)
}

var vpsModelMap = map[string]string{
	"vps-2025-model1": "VPS-1",
	"vps-2025-model2": "VPS-2",
	"vps-2025-model3": "VPS-3",
	"vps-2025-model4": "VPS-4",
	"vps-2025-model5": "VPS-5",
	"vps-2025-model6": "VPS-6",
}

var statusMap = map[string]string{
	"available":                     "现货",
	"out-of-stock":                  "无货",
	"out-of-stock-preorder-allowed": "缺货（可预订）",
	"unavailable":                   "不可用",
	"unknown":                       "未知",
}

// SendSummaryNotification 对应 Python: send_vps_summary_notification
func SendSummaryNotification(state *app.State, planCode string, dcs []map[string]interface{}, changeType string, expectedChannels ...[]string) monitor.NotificationDeliveryResult {
	cfg := state.Config.Get()
	if len(dcs) == 0 {
		return monitor.NotificationDeliveryResult{}
	}
	planDisplay, ok := vpsModelMap[planCode]
	if !ok {
		planDisplay = planCode
	}
	var emoji, title string
	switch changeType {
	case "initial":
		emoji, title = "📊", "VPS初始状态"
	case "available":
		emoji, title = "🎉", "VPS补货通知"
	default:
		emoji, title = "📦", "VPS下架通知"
	}
	var sb strings.Builder
	sb.WriteString(emoji + " " + title + "\n\n")
	sb.WriteString("套餐: " + planDisplay + "\n")
	sb.WriteString("时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n\n")
	for idx, dc := range dcs {
		st, _ := dc["status"].(string)
		statusCN, ok := statusMap[st]
		if !ok {
			statusCN = st
		}
		name, _ := dc["name"].(string)
		code, _ := dc["code"].(string)
		sb.WriteString(fmt.Sprintf("%d. %s (%s)\n   状态: %s", idx+1, name, code, statusCN))
		if days, ok := numconv.ToInt64(dc["days"]); ok && days > 0 {
			sb.WriteString(fmt.Sprintf(" | 预计交付: %d天", days))
		}
		sb.WriteString("\n")
	}
	if changeType == "available" {
		sb.WriteString("\n💡 快去抢购吧！")
	}
	expected := monitor.ConfiguredNotificationChannels(state)
	if len(expectedChannels) > 0 { expected = expectedChannels[0] }
	result := monitor.NotificationDeliveryResult{}
	for _, channel := range expected {
		switch channel {
		case monitor.NotificationChannelTelegram:
			result[channel] = cfg.TgToken != "" && cfg.TgChatID != "" && telegram.SendMessage(state, sb.String(), nil)
		case monitor.NotificationChannelFeishu:
			result[channel] = monitor.FeishuSendDefaultNotification(state, emoji+" "+title, sb.String(), map[string]string{"available": "green", "initial": "blue"}[changeType], nil)
		case monitor.NotificationChannelWeixin:
			result[channel] = monitor.SendWeixinNotification(state, sb.String())
		}
	}
	if monitor.DeliveryCompleteForChannels(expected, result) {
		state.Logger.Info(fmt.Sprintf("✅ VPS汇总通知发送成功: %s (%d个机房)", planCode, len(dcs)), "vps_monitor")
	} else {
		state.Logger.Warn(fmt.Sprintf("⚠️ VPS汇总通知发送失败: %s", planCode), "vps_monitor")
	}
	return result
}

type dcStatus struct {
	name   string
	code   string
	status string
	days   int
}

func vpsStatusClass(status string) (available, unavailable, known bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "available":
		return true, false, true
	case "out-of-stock", "out-of-stock-preorder-allowed", "unavailable":
		return false, true, true
	default:
		return false, false, false
	}
}

func statusAvailable(status string) bool {
	available, _, known := vpsStatusClass(status)
	return known && available
}

func statusUnavailable(status string) bool {
	_, unavailable, known := vpsStatusClass(status)
	return known && unavailable
}

func knownVPSStatus(status string) bool {
	_, _, known := vpsStatusClass(status)
	return known
}

func pendingNotificationEnabled(changeType string, notifyAvailable, notifyUnavailable bool) bool {
	switch changeType {
	case "initial", "available":
		return notifyAvailable
	case "unavailable":
		return notifyUnavailable
	default:
		return false
	}
}

func pendingNotificationMatchesStatus(changeType, status string) bool {
	switch changeType {
	case "initial", "available":
		return statusAvailable(status)
	case "unavailable":
		return statusUnavailable(status)
	default:
		return false
	}
}

func monitoredDatacenter(monitored []string, code string) bool {
	if len(monitored) == 0 {
		return true
	}
	for _, wanted := range monitored {
		if strings.EqualFold(wanted, code) {
			return true
		}
	}
	return false
}

func notificationData(statuses map[string]dcStatus, pending map[string]string, changeType string) ([]map[string]interface{}, []string) {
	dcs := make([]map[string]interface{}, 0)
	keys := make([]string, 0)
	pendingCodes := make([]string, 0, len(pending))
	for code := range pending {
		pendingCodes = append(pendingCodes, code)
	}
	sort.Strings(pendingCodes)
	for _, code := range pendingCodes {
		pendingType := pending[code]
		if pendingType != changeType {
			continue
		}
		status, ok := statuses[code]
		if !ok || !pendingNotificationMatchesStatus(changeType, status.status) {
			continue
		}
		dcs = append(dcs, map[string]interface{}{
			"name": status.name, "code": status.code, "status": status.status, "days": status.days,
		})
		keys = append(keys, code)
	}
	return dcs, keys
}

type vpsNotificationGroup struct {
	dcs      []map[string]interface{}
	keys     []string
	channels []string
}

func notificationGroups(statuses map[string]dcStatus, sub *types.VPSSubscription, changeType string, defaults []string) []vpsNotificationGroup {
	dcs, keys := notificationData(statuses, sub.PendingNotify, changeType)
	groups := make([]vpsNotificationGroup, 0)
	indices := make(map[string]int)
	for idx, code := range keys {
		channels := vpsPendingChannels(sub, []string{code}, defaults)
		channelKey := strings.Join(channels, ",")
		groupIndex, ok := indices[channelKey]
		if !ok {
			groupIndex = len(groups)
			indices[channelKey] = groupIndex
			groups = append(groups, vpsNotificationGroup{channels: channels})
		}
		groups[groupIndex].dcs = append(groups[groupIndex].dcs, dcs[idx])
		groups[groupIndex].keys = append(groups[groupIndex].keys, code)
	}
	return groups
}

func vpsPendingChannels(sub *types.VPSSubscription, keys []string, defaults []string) []string {
	combined := make([]string, 0)
	for _, key := range keys {
		channels, exists := sub.PendingNotifyChannels[key]
		if !exists { channels = defaults }
		combined = append(combined, channels...)
	}
	return canonicalVPSNotificationChannels(combined)
}

func canonicalVPSNotificationChannels(channels []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(channels))
	for _, channel := range channels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if _, ok := seen[channel]; ok { continue }
		switch channel {
		case monitor.NotificationChannelTelegram, monitor.NotificationChannelFeishu, monitor.NotificationChannelWeixin:
			seen[channel] = struct{}{}
			out = append(out, channel)
		}
	}
	sort.Strings(out)
	return out
}

func intersectVPSNotificationChannels(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, channel := range right {
		channel = strings.ToLower(strings.TrimSpace(channel))
		rightSet[channel] = struct{}{}
	}
	out := make([]string, 0, len(left))
	seen := make(map[string]struct{}, len(left))
	for _, channel := range left {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if _, allowed := rightSet[channel]; !allowed {
			continue
		}
		if _, exists := seen[channel]; exists {
			continue
		}
		seen[channel] = struct{}{}
		out = append(out, channel)
	}
	sort.Strings(out)
	return out
}

// vpsNotificationStillCurrent 在网络发送前复核订阅配置和待办事件。
// 发送请求期间用户可能删除/修改订阅，旧快照不能继续向渠道发送。
func vpsNotificationStillCurrent(state *app.State, working *types.VPSSubscription, keys []string, changeType string, channels []string) bool {
	if state == nil || working == nil || working.ID == "" || len(keys) == 0 || len(channels) == 0 {
		return false
	}
	for _, current := range state.VPSSubscriptionsSnapshot() {
		if current.ID != working.ID {
			continue
		}
		if current.PlanCode != working.PlanCode ||
			current.OvhSubsidiary != working.OvhSubsidiary ||
			!sameStrings(current.Datacenters, working.Datacenters) ||
			current.MonitorLinux != working.MonitorLinux ||
			current.MonitorWindows != working.MonitorWindows ||
			current.NotifyAvailable != working.NotifyAvailable ||
			current.NotifyUnavailable != working.NotifyUnavailable {
			return false
		}
		for _, key := range keys {
			if current.PendingNotify[key] != changeType {
				return false
			}
			pendingChannels, exists := current.PendingNotifyChannels[key]
			if !exists {
				pendingChannels = channels
			}
			if len(pendingChannels) == 0 {
				return false
			}
			for _, channel := range channels {
				found := false
				for _, pending := range pendingChannels {
					if strings.EqualFold(strings.TrimSpace(pending), channel) {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			}
		}
		if !sameStrings(monitor.EnabledNotificationChannels(state, channels), channels) {
			return false
		}
		return true
	}
	return false
}

func setVPSPendingNotification(sub *types.VPSSubscription, code, changeType string, channels []string) {
	channels = canonicalVPSNotificationChannels(channels)
	if len(channels) == 0 {
		delete(sub.PendingNotify, code)
		delete(sub.PendingNotifyChannels, code)
		return
	}
	if sub.PendingNotify == nil {
		sub.PendingNotify = map[string]string{}
	}
	if sub.PendingNotifyChannels == nil {
		sub.PendingNotifyChannels = map[string][]string{}
	}
	sub.PendingNotify[code] = changeType
	sub.PendingNotifyChannels[code] = channels
}

func clearVPSPendingNotification(sub *types.VPSSubscription, code string) {
	delete(sub.PendingNotify, code)
	delete(sub.PendingNotifyChannels, code)
}

func appendHistory(sub *types.VPSSubscription, statuses map[string]dcStatus, keys []string, changeType string, oldStatus map[string]string) {
	now := time.Now().Format(time.RFC3339Nano)
	for _, code := range keys {
		status := statuses[code]
		historyType := changeType
		if changeType == "initial" {
			if !statusAvailable(status.status) {
				continue
			}
			historyType = "available"
		}
		var old interface{}
		if value, ok := oldStatus[code]; ok {
			old = value
		}
		sub.History = append(sub.History, map[string]interface{}{
			"timestamp": now, "datacenter": status.name, "datacenterCode": code,
			"status": status.status, "changeType": historyType, "oldStatus": old,
		})
	}
}

type vpsAvailabilityFetcher func(context.Context, *app.State, string, string) map[string]interface{}

func processSubscription(ctx context.Context, state *app.State, sub *types.VPSSubscription) bool {
	return processSubscriptionWithAvailability(ctx, state, sub, checkVPSDCAvailability)
}

// processSubscriptionWithAvailability 把公开库存查询作为显式依赖传入，便于在
// 生命周期测试中覆盖异常、缺字段和部分机房响应，而不访问真实 OVH API。
func processSubscriptionWithAvailability(ctx context.Context, state *app.State, sub *types.VPSSubscription, fetch vpsAvailabilityFetcher) bool {
	ovhSub := strings.TrimSpace(sub.OvhSubsidiary)
	if ovhSub == "" {
		ovhSub = "IE"
		if account, ok := state.FindAccount(""); ok && account.Zone != "" {
			ovhSub = account.Zone
		}
	}
	if fetch == nil {
		return false
	}
	currentData := fetch(ctx, state, sub.PlanCode, ovhSub)
	if currentData == nil {
		if ctx.Err() == nil {
			state.Logger.Warn("无法获取VPS "+sub.PlanCode+" 的数据中心信息", "vps_monitor")
		}
		return false
	}

	if sub.LastStatus == nil {
		sub.LastStatus = map[string]string{}
	}
	if sub.PendingNotify == nil {
		sub.PendingNotify = map[string]string{}
	}
	if sub.PendingNotifyChannels == nil {
		sub.PendingNotifyChannels = map[string][]string{}
	}
	if sub.History == nil {
		sub.History = []map[string]interface{}{}
	}
	oldStatus := make(map[string]string, len(sub.LastStatus))
	for code, status := range sub.LastStatus {
		oldStatus[code] = status
	}
	statuses := make(map[string]dcStatus)
	notificationChannels := monitor.PendingNotificationChannels(state)
	// 兼容旧数据库中没有 pending_notify_channels 的事件；同时移除
	// 用户后来明确关闭的渠道，避免旧配置继续发送。
	for code := range sub.PendingNotify {
		channels, exists := sub.PendingNotifyChannels[code]
		if !exists {
			channels = notificationChannels
		}
		channels = monitor.EnabledNotificationChannels(state, channels)
		if len(channels) == 0 {
			clearVPSPendingNotification(sub, code)
			continue
		}
		sub.PendingNotifyChannels[code] = channels
	}
	dcsValue, exists := currentData["datacenters"]
	dcsRaw, valid := dcsValue.([]interface{})
	if !exists || !valid {
		state.Logger.Warn("VPS "+sub.PlanCode+" 响应缺少有效的 datacenters 字段，本轮不更新库存基线", "vps_monitor")
		return false
	}
	if len(dcsRaw) == 0 {
		state.Logger.Warn("VPS "+sub.PlanCode+" 响应的 datacenters 为空，本轮不更新库存基线", "vps_monitor")
		return false
	}
	for _, dcRaw := range dcsRaw {
		dc, ok := dcRaw.(map[string]interface{})
		if !ok {
			state.Logger.Warn("VPS "+sub.PlanCode+" 响应包含无效的数据中心项，已跳过", "vps_monitor")
			continue
		}
		code, _ := dc["code"].(string)
		code = strings.TrimSpace(code)
		if code == "" || !monitoredDatacenter(sub.Datacenters, code) {
			continue
		}
		name, _ := dc["datacenter"].(string)
		if name == "" {
			name = code
		}
		status, _ := dc["status"].(string)
		status = strings.ToLower(strings.TrimSpace(status))
		if _, _, known := vpsStatusClass(status); !known {
			state.Logger.Warn(fmt.Sprintf("VPS %s 数据中心 %s 返回未知库存状态 %q，本轮保留旧状态", sub.PlanCode, code, status), "vps_monitor")
			continue
		}
		daysI64, _ := numconv.ToInt64(dc["daysBeforeDelivery"])
		statuses[code] = dcStatus{name: name, code: code, status: status, days: int(daysI64)}
	}
	if len(statuses) == 0 {
		state.Logger.Warn("VPS "+sub.PlanCode+" 响应中没有可用的已知库存状态，本轮不更新库存基线", "vps_monitor")
		return false
	}

	// API 可能返回部分机房结果。缺失机房保留旧状态，只有明确状态才更新基线。
	for code, current := range statuses {
		old, existed := oldStatus[code]
		sub.LastStatus[code] = current.status
		if !existed || !knownVPSStatus(old) {
			if sub.NotifyAvailable && statusAvailable(current.status) {
				setVPSPendingNotification(sub, code, "initial", notificationChannels)
			} else {
				clearVPSPendingNotification(sub, code)
			}
			continue
		}
		wasUnavailable := statusUnavailable(old)
		isUnavailable := statusUnavailable(current.status)
		switch {
		case wasUnavailable && !isUnavailable && sub.NotifyAvailable:
			setVPSPendingNotification(sub, code, "available", notificationChannels)
		case !wasUnavailable && isUnavailable && sub.NotifyUnavailable:
			setVPSPendingNotification(sub, code, "unavailable", notificationChannels)
		case wasUnavailable != isUnavailable:
			// 状态在旧通知送达前再次变化，而新状态没有开启通知时，旧事件已经过期。
			clearVPSPendingNotification(sub, code)
		}
	}
	for code, pending := range sub.PendingNotify {
		if !pendingNotificationEnabled(pending, sub.NotifyAvailable, sub.NotifyUnavailable) {
			clearVPSPendingNotification(sub, code)
			continue
		}
		current, ok := statuses[code]
		if ok && !pendingNotificationMatchesStatus(pending, current.status) {
			clearVPSPendingNotification(sub, code)
		}
	}
	// 先把本轮状态与待通知事件落盘，再开始网络发送。这样发送前复核
	// 可以识别当前事件；若进程此时退出，重启后仍会从 pending 状态重试。
	committed, err := mergeSubscriptionState(state, *sub)
	if err != nil {
		state.Logger.Warn("保存VPS订阅状态失败，本轮跳过通知发送: "+err.Error(), "vps_monitor")
		return false
	}
	if !committed {
		state.Logger.Info("VPS订阅在检查期间已删除或修改，丢弃旧检查结果", "vps_monitor")
		return false
	}
	persistedState := cloneVPSSubscription(*sub)

	for _, changeType := range []string{"initial", "available", "unavailable"} {
		for _, group := range notificationGroups(statuses, sub, changeType, notificationChannels) {
			if len(group.dcs) == 0 || len(group.channels) == 0 {
				continue
			}
			// 预提交与真正发送之间，用户可能关闭某个全局渠道。只保留
			// 此刻仍启用且仍在各事件快照中的渠道，并先把清理结果 CAS
			// 落盘，防止旧快照继续发送到刚关闭的渠道。
			enabled := monitor.EnabledNotificationChannels(state, group.channels)
			preparedChannels := make([]string, 0, len(enabled))
			channelsChanged := false
			for _, code := range group.keys {
				pending, exists := sub.PendingNotifyChannels[code]
				if !exists {
					pending = group.channels
				}
				filtered := intersectVPSNotificationChannels(pending, enabled)
				if len(filtered) == 0 {
					clearVPSPendingNotification(sub, code)
					channelsChanged = true
					continue
				}
				if !exists || !sameStrings(vpsPendingChannels(sub, []string{code}, nil), filtered) {
					sub.PendingNotifyChannels[code] = filtered
					channelsChanged = true
				}
				preparedChannels = append(preparedChannels, filtered...)
			}
			group.channels = vpsPendingChannels(&types.VPSSubscription{
				PendingNotifyChannels: map[string][]string{"group": preparedChannels},
			}, []string{"group"}, nil)
			if channelsChanged {
				committed, err = mergeSubscriptionStateIfCurrent(state, *sub, &persistedState)
				if err != nil {
					state.Logger.Warn("保存VPS待通知渠道清理结果失败: "+err.Error(), "vps_monitor")
					return false
				}
				if !committed {
					state.Logger.Info("VPS订阅在通知渠道清理期间已删除或修改，丢弃旧通知", "vps_monitor")
					return false
				}
				persistedState = cloneVPSSubscription(*sub)
			}
			if len(group.channels) == 0 {
				state.Logger.Info(fmt.Sprintf("VPS %s %s 通知的目标渠道均已关闭，已清理待办", sub.PlanCode, changeType), "vps_monitor")
				continue
			}
			if !vpsNotificationStillCurrent(state, sub, group.keys, changeType, group.channels) {
				state.Logger.Info(fmt.Sprintf("VPS %s %s 通知在发送前已失效，跳过旧通知", sub.PlanCode, changeType), "vps_monitor")
				return false
			}
			delivered := SendSummaryNotification(state, sub.PlanCode, group.dcs, changeType, group.channels)
			for _, code := range group.keys {
				channels, exists := sub.PendingNotifyChannels[code]
				if !exists {
					// 旧数据库没有渠道快照时，必须使用发送前解析出的目标
					// 集合计算 remaining，否则失败渠道会被错误清除。
					channels = group.channels
					sub.PendingNotifyChannels[code] = append([]string(nil), group.channels...)
				}
				remaining := monitor.RemainingNotificationChannels(channels, delivered)
				if len(remaining) > 0 {
					sub.PendingNotifyChannels[code] = remaining
					continue
				}
				appendHistory(sub, statuses, []string{code}, changeType, oldStatus)
				clearVPSPendingNotification(sub, code)
			}
			if len(sub.History) > 100 {
				sub.History = sub.History[len(sub.History)-100:]
			}
			// 每组渠道发送后立即条件落盘。这样已经成功的渠道不会因为后续
			// 发送阻塞而长期保留；若用户在网络请求期间编辑了订阅，CAS
			// 校验会拒绝旧工作副本覆盖新状态。
			committed, err = mergeSubscriptionStateIfCurrent(state, *sub, &persistedState)
			if err != nil {
				state.Logger.Warn("保存VPS通知结果失败，下轮将继续重试: "+err.Error(), "vps_monitor")
				return false
			}
			if !committed {
				state.Logger.Info("VPS订阅在发送通知期间已删除或修改，丢弃旧通知结果", "vps_monitor")
				return false
			}
			persistedState = cloneVPSSubscription(*sub)
		}
	}
	return true
}

func mergeSubscriptionState(state *app.State, checked types.VPSSubscription) (bool, error) {
	return mergeSubscriptionStateIfCurrent(state, checked, nil)
}

// mergeSubscriptionStateIfCurrent 以 expected 作为上次已持久化状态的 CAS
// 条件。expected 为空时用于网络查询后的首次提交，只校验用户配置；非空时
// 还要求运行状态完全一致，防止通知发送期间发生的编辑或其他提交被旧副本覆盖。
func mergeSubscriptionStateIfCurrent(state *app.State, checked types.VPSSubscription, expected *types.VPSSubscription) (bool, error) {
	committed := false
	err := state.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
		for i := range subscriptions {
			if subscriptions[i].ID == checked.ID {
				current := &subscriptions[i]
				if current.PlanCode != checked.PlanCode ||
					current.OvhSubsidiary != checked.OvhSubsidiary ||
					!sameStrings(current.Datacenters, checked.Datacenters) ||
					current.MonitorLinux != checked.MonitorLinux ||
					current.MonitorWindows != checked.MonitorWindows ||
					current.NotifyAvailable != checked.NotifyAvailable ||
					current.NotifyUnavailable != checked.NotifyUnavailable {
					// 用户在本轮网络请求期间更新了订阅，丢弃基于旧配置的检查结果。
					return subscriptions, nil
				}
				if expected != nil && !sameVPSRuntimeState(*current, *expected) {
					return subscriptions, nil
				}
				// 不能直接复用 checked 的 map/slice。调用方在第一次合并后
				// 还会更新工作副本；共享引用会绕过锁修改 State 中的快照，
				// 甚至让用户编辑期间的旧结果重新写回数据库。
				current.LastStatus = cloneStringMap(checked.LastStatus)
				current.PendingNotify = cloneStringMap(checked.PendingNotify)
				current.PendingNotifyChannels = cloneStringSliceMap(checked.PendingNotifyChannels)
				current.History = cloneHistory(checked.History)
				committed = true
				break
			}
		}
		return subscriptions, nil
	})
	return committed, err
}

func sameVPSRuntimeState(left, right types.VPSSubscription) bool {
	return reflect.DeepEqual(left.LastStatus, right.LastStatus) &&
		reflect.DeepEqual(left.PendingNotify, right.PendingNotify) &&
		reflect.DeepEqual(left.PendingNotifyChannels, right.PendingNotifyChannels) &&
		reflect.DeepEqual(left.History, right.History)
}

func cloneVPSSubscription(source types.VPSSubscription) types.VPSSubscription {
	out := source
	out.Datacenters = append([]string(nil), source.Datacenters...)
	out.LastStatus = cloneStringMap(source.LastStatus)
	out.PendingNotify = cloneStringMap(source.PendingNotify)
	out.PendingNotifyChannels = cloneStringSliceMap(source.PendingNotifyChannels)
	out.History = cloneHistory(source.History)
	return out
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	if source == nil {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(source))
	for key, value := range source {
		out[key] = append([]string(nil), value...)
	}
	return out
}

func cloneHistory(source []map[string]interface{}) []map[string]interface{} {
	if source == nil {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(source))
	for _, entry := range source {
		out = append(out, cloneInterfaceMap(entry))
	}
	return out
}

// cloneInterfaceMap 递归复制历史中可能出现的嵌套 map/slice。仅复制最外层
// map 会让工作副本和 State 共享内部引用，网络请求期间对旧副本的修改可能
// 绕过 VPS 的 CAS 与持久化锁，重新污染用户刚编辑后的订阅状态。
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

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// monitorLoop 对应 Python: vps_monitor_loop。
func monitorLoop(ctx context.Context, state *app.State, done chan struct{}) {
	defer close(done)
	defer func() {
		if recovered := recover(); recovered != nil {
			state.Logger.Error(fmt.Sprintf("VPS监控循环异常退出: %v", recovered), "vps_monitor")
		}
		runningMu.Lock()
		running = false
		monitorCancel = nil
		monitorDone = nil
		runningMu.Unlock()
	}()
	state.Logger.Info("VPS监控循环已启动", "vps_monitor")
	for {
		select {
		case <-ctx.Done():
			state.Logger.Info("VPS监控循环已停止", "vps_monitor")
			return
		default:
		}
		checkNotifications(state)
		subs := state.VPSSubscriptionsSnapshot()
		interval := state.VPSCheckIntervalSnapshot()
		if len(subs) == 0 {
			state.Logger.Info("当前无VPS订阅，跳过检查", "vps_monitor")
		} else {
			state.Logger.Info(fmt.Sprintf("开始检查 %d 个VPS订阅...", len(subs)), "vps_monitor")
			for idx := range subs {
				select {
				case <-ctx.Done():
					state.Logger.Info("VPS监控循环已停止", "vps_monitor")
					return
				default:
				}
				processSubscription(ctx, state, &subs[idx])
				timer := time.NewTimer(time.Second)
				select {
				case <-timer.C:
				case <-ctx.Done():
					if !timer.Stop() {
						select { case <-timer.C: default: }
					}
					state.Logger.Info("VPS监控循环已停止", "vps_monitor")
					return
				}
			}
		}

		state.Logger.Info(fmt.Sprintf("等待 %d 秒后进行下次VPS检查...", interval), "vps_monitor")
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			state.Logger.Info("VPS监控循环已停止", "vps_monitor")
			return
		}
	}
}

// Start 启动监控
func Start(state *app.State) bool {
	runningMu.Lock()
	if running {
		runningMu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	running = true
	monitorCancel = cancel
	monitorDone = done
	runningMu.Unlock()
	// 重置 TG 检查时间戳,保证启动后第一轮一定 verify
	tgCheckMu.Lock()
	lastTGCheck = time.Time{}
	tgCheckMu.Unlock()
	go monitorLoop(ctx, state, done)
	state.Logger.Info(fmt.Sprintf("VPS监控已启动 (检查间隔: %d秒)", state.VPSCheckIntervalSnapshot()), "vps_monitor")
	return true
}

// Stop 停止监控
func Stop(state *app.State) bool {
	runningMu.Lock()
	if !running {
		runningMu.Unlock()
		return false
	}
	cancel := monitorCancel
	done := monitorDone
	runningMu.Unlock()
	state.Logger.Info("正在停止VPS监控...", "vps_monitor")
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return true
}

// Running 返回是否在运行
func Running() bool {
	runningMu.Lock()
	defer runningMu.Unlock()
	return running
}
