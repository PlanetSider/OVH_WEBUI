package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/proxyguard"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

type purchaseSuccessPayload struct {
	TaskID     string   `json:"taskId"`
	AccountID  string   `json:"accountId"`
	PlanCode   string   `json:"planCode"`
	Datacenter string   `json:"datacenter"`
	Options    []string `json:"options"`
	OrderID    string   `json:"orderId"`
	OrderURL   string   `json:"orderUrl"`
}

type catalogStatusPayload struct {
	Mode       string `json:"mode"`
	PlanCode   string `json:"planCode"`
	ServerName string `json:"serverName"`
	CycleID    string `json:"cycleId"`
	Recovered  bool   `json:"recovered"`
}

type orderStatusPayload struct {
	TaskID      string `json:"taskId"`
	AccountID   string `json:"accountId"`
	PlanCode    string `json:"planCode"`
	Datacenter  string `json:"datacenter"`
	OrderID     string `json:"orderId"`
	OrderURL    string `json:"orderUrl"`
	OldStatus   string `json:"oldStatus,omitempty"`
	OrderStatus string `json:"orderStatus"`
	StatusAt    string `json:"statusAt"`
}

type proxyGuardPayload struct {
	EventKind                string   `json:"eventKind"`
	AccountID                string   `json:"accountId"`
	AccountName              string   `json:"accountName,omitempty"`
	AccountZone              string   `json:"accountZone,omitempty"`
	ProxyURL                 string   `json:"proxyUrl,omitempty"`
	LastError                string   `json:"lastError,omitempty"`
	ConsecutiveFailures      int      `json:"consecutiveFailures,omitempty"`
	PausedQueue              int      `json:"pausedQueue,omitempty"`
	DisabledMonitorAutoOrder int      `json:"disabledMonitorAutoOrder,omitempty"`
	DisabledVPSAutoOrder     int      `json:"disabledVpsAutoOrder,omitempty"`
	RestoredQueue            int      `json:"restoredQueue,omitempty"`
	RestoredMonitorAutoOrder int      `json:"restoredMonitorAutoOrder,omitempty"`
	RestoredVPSAutoOrder     int      `json:"restoredVpsAutoOrder,omitempty"`
	DurationSeconds          int64    `json:"durationSeconds,omitempty"`
	Errors                   []string `json:"errors,omitempty"`
}

func NewPurchaseSuccessNotification(item types.QueueItem, orderID, orderURL string, channels []string) (*types.NotificationOutboxEntry, error) {
	channels = canonicalNotificationChannels(channels)
	payload, err := json.Marshal(purchaseSuccessPayload{
		TaskID: item.ID, AccountID: item.AccountID, PlanCode: item.PlanCode,
		Datacenter: item.Datacenter, Options: append([]string(nil), item.Options...),
		OrderID: orderID, OrderURL: orderURL,
	})
	if err != nil {
		return nil, fmt.Errorf("encode purchase notification: %w", err)
	}
	return &types.NotificationOutboxEntry{
		EventKey: "purchase_success:" + item.ID, Kind: NotificationKindPurchaseSuccess,
		Payload: string(payload), Channels: channels, AwaitingChannels: len(channels) == 0,
	}, nil
}

func NewOrderStatusNotification(entry types.PurchaseHistoryEntry, oldStatus string, channels []string) (*types.NotificationOutboxEntry, error) {
	if strings.TrimSpace(entry.TaskID) == "" || strings.TrimSpace(entry.OrderID) == "" || strings.TrimSpace(entry.OrderStatus) == "" {
		return nil, fmt.Errorf("order status notification 缺少 taskId、orderId 或 orderStatus")
	}
	channels = canonicalNotificationChannels(channels)
	payload, err := json.Marshal(orderStatusPayload{
		TaskID: entry.TaskID, AccountID: entry.AccountID, PlanCode: entry.PlanCode,
		Datacenter: entry.Datacenter, OrderID: entry.OrderID, OrderURL: entry.OrderURL,
		OldStatus: oldStatus, OrderStatus: entry.OrderStatus, StatusAt: entry.OrderStatusAt,
	})
	if err != nil {
		return nil, fmt.Errorf("encode order status notification: %w", err)
	}
	return &types.NotificationOutboxEntry{
		EventKey: fmt.Sprintf("order_status:%s:%s:%s", entry.OrderID, entry.OrderStatus, entry.OrderStatusAt),
		Kind:     NotificationKindOrderStatus, Payload: string(payload), Channels: channels,
		AwaitingChannels: len(channels) == 0,
	}, nil
}

func publicProxyGuardError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "代理探测失败"
}

func publicProxyGuardIssue(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "代理状态变更失败"
}

func NewProxyGuardNotification(action app.ProxyGuardAction, channels []string) (*types.NotificationOutboxEntry, error) {
	accountID := strings.TrimSpace(action.AccountID)
	if accountID == "" || action.Event.Kind == "" {
		return nil, fmt.Errorf("proxy guard notification 缺少 accountId 或 eventKind")
	}
	if action.Event.Kind != proxyguard.EventTrip && action.Event.Kind != proxyguard.EventReminder && action.Event.Kind != proxyguard.EventRecovery {
		return nil, fmt.Errorf("proxy guard notification eventKind 无效: %q", action.Event.Kind)
	}
	channels = canonicalNotificationChannels(channels)
	sanitizedErrors := make([]string, len(action.Errors))
	for i, issue := range action.Errors {
		sanitizedErrors[i] = publicProxyGuardIssue(issue)
	}
	payload := proxyGuardPayload{
		EventKind: string(action.Event.Kind), AccountID: accountID,
		AccountName: action.AccountName, AccountZone: action.AccountZone,
		ProxyURL: ovh.ScrubProxyURL(action.ProxyURL), LastError: publicProxyGuardError(action.Event.Status.LastError),
		ConsecutiveFailures: action.Event.Status.ConsecutiveFailures,
		PausedQueue:         action.PausedQueue, DisabledMonitorAutoOrder: action.DisabledMonitorAutoOrder,
		DisabledVPSAutoOrder: action.DisabledVPSAutoOrder, RestoredQueue: action.RestoredQueue,
		RestoredMonitorAutoOrder: action.RestoredMonitorAutoOrder,
		RestoredVPSAutoOrder:     action.RestoredVPSAutoOrder,
		DurationSeconds:          int64(action.Event.Duration / time.Second),
		Errors:                   sanitizedErrors,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode proxy guard notification: %w", err)
	}
	stamp := action.Event.Status.LastFailureAt
	if action.Event.Kind == "recovery" {
		stamp = action.Event.Status.LastSuccessAt
	}
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	return &types.NotificationOutboxEntry{
		EventKey: fmt.Sprintf("proxy_guard:%s:%s:%d", action.Event.Kind, accountID, stamp.UnixNano()),
		Kind:     NotificationKindProxyGuard, Payload: string(payloadBytes), Channels: channels,
		AwaitingChannels: len(channels) == 0,
	}, nil
}

func NewCatalogStatusNotification(mode, planCode, serverName, cycleID string, recovered bool, channels []string) (*types.NotificationOutboxEntry, error) {
	mode = strings.TrimSpace(mode)
	planCode = strings.TrimSpace(planCode)
	cycleID = strings.TrimSpace(cycleID)
	channels = canonicalNotificationChannels(channels)
	if mode != "抢购" && mode != "监控" {
		return nil, fmt.Errorf("catalog status notification mode 无效: %q", mode)
	}
	if planCode == "" || cycleID == "" {
		return nil, fmt.Errorf("catalog status notification 缺少 planCode 或 cycleID")
	}
	payload, err := json.Marshal(catalogStatusPayload{
		Mode: mode, PlanCode: planCode, ServerName: strings.TrimSpace(serverName),
		CycleID: cycleID, Recovered: recovered,
	})
	if err != nil {
		return nil, fmt.Errorf("encode catalog status notification: %w", err)
	}
	state := "discontinued"
	if recovered {
		state = "recovered"
	}
	return &types.NotificationOutboxEntry{
		EventKey: fmt.Sprintf("catalog_status:%s:%s:%s:%s", state, mode, planCode, cycleID),
		Kind:     NotificationKindCatalogStatus, Payload: string(payload),
		Channels: channels, AwaitingChannels: len(channels) == 0,
	}, nil
}

func catalogStatusMessage(payload catalogStatusPayload) (string, string, string) {
	name := strings.TrimSpace(payload.ServerName)
	if name == "" {
		name = payload.PlanCode
	}
	if payload.Recovered {
		return "✅ 型号重新上架通知", fmt.Sprintf("正在%s的%s（%s）已重新上架！\n\n已恢复原%s频率。", payload.Mode, name, payload.PlanCode, payload.Mode), "green"
	}
	return "⚠️ 型号停售通知", fmt.Sprintf("正在%s的%s（%s）已停售！\n\n该型号已连续 1 小时未出现在服务器目录中，已将%s检查频率降为每小时一次。", payload.Mode, name, payload.PlanCode, payload.Mode), "red"
}

func purchaseSuccessMessage(payload purchaseSuccessPayload) string {
	msg := fmt.Sprintf("🎉 OVH 服务器抢购成功！🎉\n\n服务器型号 (Plan Code): %s\n数据中心: %s\n订单 ID: %s\n订单链接: %s\n",
		payload.PlanCode, payload.Datacenter, payload.OrderID, payload.OrderURL)
	if len(payload.Options) > 0 {
		msg += "自定义配置: " + strings.Join(payload.Options, ", ") + "\n"
	}
	return msg + "\n抢购任务ID: " + payload.TaskID
}

func orderStatusMessage(payload orderStatusPayload) string {
	return fmt.Sprintf("📦 OVH 订单状态更新\n\n型号: %s\n数据中心: %s\n订单 ID: %s\n状态: %s\n订单链接: %s\n任务ID: %s",
		payload.PlanCode, payload.Datacenter, payload.OrderID, payload.OrderStatus, payload.OrderURL, payload.TaskID)
}

func proxyGuardMessage(payload proxyGuardPayload) (string, string, string) {
	label := payload.AccountName
	if label == "" {
		label = payload.AccountID
	}
	if payload.AccountZone != "" {
		label += "（" + strings.ToUpper(payload.AccountZone) + "）"
	}
	issues := ""
	if len(payload.Errors) > 0 {
		issues = "\n\n状态落库告警:\n- " + strings.Join(payload.Errors, "\n- ")
	}
	switch payload.EventKind {
	case "trip":
		return "🚨 账户代理已熔断", fmt.Sprintf("账户 %s 的出站代理连续失败，已暂停 %d 个抢购任务，关闭独服自动下单 %d 条、VPS 自动下单 %d 条。\n\n代理: %s\n原因: %s\n\n代理恢复后将自动恢复本次暂停的任务和自动下单。%s",
			label, payload.PausedQueue, payload.DisabledMonitorAutoOrder, payload.DisabledVPSAutoOrder, payload.ProxyURL, payload.LastError, issues), "red"
	case "recovery":
		return "✅ 账户代理已恢复", fmt.Sprintf("账户 %s 的出站代理已恢复，已恢复 %d 个抢购任务、独服自动下单 %d 条、VPS 自动下单 %d 条。\n\n中断约 %d 秒。\n代理: %s%s",
			label, payload.RestoredQueue, payload.RestoredMonitorAutoOrder, payload.RestoredVPSAutoOrder, payload.DurationSeconds, payload.ProxyURL, issues), "green"
	default:
		return "⚠️ 账户代理仍不可用", fmt.Sprintf("账户 %s 的出站代理仍不可用。\n\n连续失败: %d\n代理: %s\n原因: %s%s",
			label, payload.ConsecutiveFailures, payload.ProxyURL, payload.LastError, issues), "orange"
	}
}

func (m *Monitor) dispatchOutboxEntry(entry types.NotificationOutboxEntry) (NotificationDeliveryResult, error) {
	return m.dispatchOutboxEntryWithContext(context.Background(), entry)
}

func (m *Monitor) dispatchOutboxEntryWithContext(ctx context.Context, entry types.NotificationOutboxEntry) (NotificationDeliveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return NotificationDeliveryResult{}, err
	}
	result := NotificationDeliveryResult{}
	switch entry.Kind {
	case NotificationKindNewServer:
		var server map[string]interface{}
		if err := json.Unmarshal([]byte(entry.Payload), &server); err != nil {
			return result, fmt.Errorf("解析新服务器通知失败: %w", err)
		}
		if len(server) == 0 {
			return result, fmt.Errorf("解析新服务器通知失败: payload 为空对象")
		}
		return m.SendNewServerAlertWithContext(ctx, server, entry.Channels), nil
	case NotificationKindOrderStatus:
		var payload orderStatusPayload
		if err := json.Unmarshal([]byte(entry.Payload), &payload); err != nil {
			return result, fmt.Errorf("解析订单状态通知失败: %w", err)
		}
		if strings.TrimSpace(payload.TaskID) == "" || strings.TrimSpace(payload.OrderID) == "" || strings.TrimSpace(payload.OrderStatus) == "" {
			return result, fmt.Errorf("解析订单状态通知失败: 缺少 taskId、orderId 或 orderStatus")
		}
		msg := orderStatusMessage(payload)
		if notificationChannelSelected(entry.Channels, NotificationChannelTelegram) {
			result[NotificationChannelTelegram] = telegram.SendMessageWithContext(ctx, m.state, msg, nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelFeishu) {
			result[NotificationChannelFeishu] = FeishuSendDefaultNotificationWithContext(ctx, m.state, "📦 OVH 订单状态更新", msg, "blue", nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelWeixin) {
			result[NotificationChannelWeixin] = SendWeixinNotificationWithContext(ctx, m.state, msg)
		}
		return result, nil
	case NotificationKindPurchaseSuccess:
		var payload purchaseSuccessPayload
		if err := json.Unmarshal([]byte(entry.Payload), &payload); err != nil {
			return result, fmt.Errorf("解析抢购成功通知失败: %w", err)
		}
		if strings.TrimSpace(payload.TaskID) == "" || strings.TrimSpace(payload.PlanCode) == "" || strings.TrimSpace(payload.OrderID) == "" {
			return result, fmt.Errorf("解析抢购成功通知失败: 缺少 taskId、planCode 或 orderId")
		}
		msg := purchaseSuccessMessage(payload)
		if notificationChannelSelected(entry.Channels, NotificationChannelTelegram) {
			result[NotificationChannelTelegram] = telegram.SendMessageWithContext(ctx, m.state, msg, nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelFeishu) {
			result[NotificationChannelFeishu] = FeishuSendDefaultNotificationWithContext(ctx, m.state, "🎉 OVH 服务器抢购成功", msg, "green", nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelWeixin) {
			result[NotificationChannelWeixin] = SendWeixinNotificationWithContext(ctx, m.state, msg)
		}
		return result, nil
	case NotificationKindCatalogStatus:
		var payload catalogStatusPayload
		if err := json.Unmarshal([]byte(entry.Payload), &payload); err != nil {
			return result, fmt.Errorf("解析型号状态通知失败: %w", err)
		}
		if (payload.Mode != "抢购" && payload.Mode != "监控") || strings.TrimSpace(payload.PlanCode) == "" || strings.TrimSpace(payload.CycleID) == "" {
			return result, fmt.Errorf("解析型号状态通知失败: mode、planCode 或 cycleId 无效")
		}
		title, msg, template := catalogStatusMessage(payload)
		if notificationChannelSelected(entry.Channels, NotificationChannelTelegram) {
			result[NotificationChannelTelegram] = telegram.SendMessageWithContext(ctx, m.state, msg, nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelFeishu) {
			result[NotificationChannelFeishu] = FeishuSendDefaultNotificationWithContext(ctx, m.state, title, msg, template, nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelWeixin) {
			result[NotificationChannelWeixin] = SendWeixinNotificationWithContext(ctx, m.state, msg)
		}
		return result, nil
	case NotificationKindProxyGuard:
		var payload proxyGuardPayload
		if err := json.Unmarshal([]byte(entry.Payload), &payload); err != nil {
			return result, fmt.Errorf("解析代理熔断通知失败: %w", err)
		}
		if strings.TrimSpace(payload.AccountID) == "" || payload.EventKind == "" {
			return result, fmt.Errorf("解析代理熔断通知失败: 缺少 accountId 或 eventKind")
		}
		if payload.EventKind != string(proxyguard.EventTrip) && payload.EventKind != string(proxyguard.EventReminder) && payload.EventKind != string(proxyguard.EventRecovery) {
			return result, fmt.Errorf("解析代理熔断通知失败: eventKind 无效")
		}
		payload.ProxyURL = ovh.ScrubProxyURL(payload.ProxyURL)
		payload.LastError = publicProxyGuardError(payload.LastError)
		for i := range payload.Errors {
			payload.Errors[i] = publicProxyGuardIssue(payload.Errors[i])
		}
		title, msg, template := proxyGuardMessage(payload)
		if notificationChannelSelected(entry.Channels, NotificationChannelTelegram) {
			result[NotificationChannelTelegram] = telegram.SendMessageWithContext(ctx, m.state, msg, nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelFeishu) {
			result[NotificationChannelFeishu] = FeishuSendDefaultNotificationWithContext(ctx, m.state, title, msg, template, nil)
		}
		if notificationChannelSelected(entry.Channels, NotificationChannelWeixin) {
			result[NotificationChannelWeixin] = SendWeixinNotificationWithContext(ctx, m.state, msg)
		}
		return result, nil
	default:
		return result, fmt.Errorf("未知通知类型 %q", entry.Kind)
	}
}

func (m *Monitor) quarantineOutboxEntry(entry types.NotificationOutboxEntry, reason string) {
	ok, err := m.state.DB.QuarantineNotification(entry.ID, reason)
	if err != nil {
		m.state.Logger.Error("隔离损坏通知失败", "monitor")
		m.state.SetNotificationOutboxRetry(entry.ID, time.Now().Add(15*time.Second))
		return
	}
	if ok {
		m.state.Logger.Error(fmt.Sprintf("通知事件已移入死信表，不再阻塞后续通知: event=%s, reason=%s", entry.EventKey, reason), "monitor")
	}
	m.state.ClearNotificationOutboxRetry(entry.ID)
}

// DispatchNotificationOutbox 串行重试待通知事件。调用方可在监控轮次和抢购成功后调用；
// 网络发送期间不持有订阅、队列或数据库事务锁。
func (m *Monitor) DispatchNotificationOutbox() {
	m.DispatchNotificationOutboxWithContext(context.Background())
}

func (m *Monitor) DispatchNotificationOutboxWithContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || m.state == nil || m.state.DB == nil {
		return
	}
	// 锁和节流状态属于共享 app.State：主监控、独立后台循环以及抢购成功
	// 后创建的临时 Monitor 都必须经过同一个发送临界区，避免重复发送。
	if err := m.state.LockNotificationOutboxContext(ctx); err != nil {
		return
	}
	defer m.state.UnlockNotificationOutbox()
	if ctx.Err() != nil {
		return
	}
	entries, err := m.state.DB.ListNotificationOutbox(100)
	if err != nil {
		m.state.Logger.Warn("读取通知 outbox 失败", "monitor")
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if !m.state.NotificationOutboxRetryDue(entry.ID, time.Now()) {
			continue
		}
		if entry.DecodeError != "" {
			m.quarantineOutboxEntry(entry, "通知渠道数据损坏")
			continue
		}
		if entry.AwaitingChannels {
			// 事件产生时没有任何启用渠道，也必须继续保留。新服务器基线和
			// 抢购成功结果已经提交，若此处删除，用户之后启用通知渠道也无法
			// 补发。awaiting 事件只有在至少一个渠道真正可用后才分配目标。
			pending := PendingNotificationChannels(m.state)
			if len(pending) == 0 {
				m.state.SetNotificationOutboxRetry(entry.ID, time.Now().Add(15*time.Second))
				continue
			}
			configured := ConfiguredNotificationChannels(m.state)
			if len(configured) == 0 {
				m.state.SetNotificationOutboxRetry(entry.ID, time.Now().Add(15*time.Second))
				continue
			}
			// 至少一个渠道已经具备发送条件后，把事件分配给所有当前仍启用
			// 的渠道，而不只是此刻已配置好的渠道。其余渠道可能只是凭据
			// 暂时失效；若只冻结 configured，首个渠道成功后事件会被删除，
			// 暂时失效的渠道将永久漏收。
			assigned, err := m.state.DB.AssignNotificationChannels(entry.ID, pending)
			if err != nil {
				m.state.Logger.Warn("分配通知接收渠道失败", "monitor")
				m.state.SetNotificationOutboxRetry(entry.ID, time.Now().Add(15*time.Second))
				continue
			}
			if !assigned {
				continue
			}
			entry.Channels = pending
			entry.AwaitingChannels = false
		}
		expected := canonicalNotificationChannels(entry.Channels)
		remaining := EnabledNotificationChannels(m.state, expected)
		if len(remaining) == 0 {
			if ok, err := m.state.DB.UpdateNotificationChannels(entry.ID, expected, nil); err != nil || !ok {
				m.state.Logger.Warn("清理已关闭通知事件失败: "+entry.EventKey, "monitor")
			} else {
				m.state.ClearNotificationOutboxRetry(entry.ID)
			}
			continue
		}
		entry.Channels = remaining
		delivered, dispatchErr := m.dispatchOutboxEntryWithContext(ctx, entry)
		if dispatchErr != nil {
			if ctx.Err() != nil {
				return
			}
			m.quarantineOutboxEntry(entry, "通知发送失败")
			continue
		}
		remaining = remainingNotificationChannels(remaining, delivered)
		if len(remaining) > 0 {
			m.state.SetNotificationOutboxRetry(entry.ID, time.Now().Add(15*time.Second))
		} else {
			m.state.ClearNotificationOutboxRetry(entry.ID)
		}
		if ok, err := m.state.DB.UpdateNotificationChannels(entry.ID, expected, remaining); err != nil {
			m.state.Logger.Warn("保存通知 outbox 进度失败", "monitor")
		} else if !ok {
			m.state.Logger.Debug("通知 outbox 已被其它发送流程更新: "+entry.EventKey, "monitor")
		}
	}
}

// RunNotificationOutboxLoop 独立重试通知事件，不依赖独服监控是否存在订阅。
// 调用方负责提供可取消的进程生命周期 context。
func (m *Monitor) RunNotificationOutboxLoop(ctx context.Context) {
	if m == nil || m.state == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.DispatchNotificationOutboxWithContext(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.DispatchNotificationOutboxWithContext(ctx)
		}
	}
}

// FlushNotificationOutbox 用于没有长期运行 Monitor 实例的流程（例如抢购成功）
// 立即尝试发送；未成功渠道仍会留在 SQLite 中等待后续监控轮次/重启重试。
func FlushNotificationOutbox(state *app.State) {
	if state == nil {
		return
	}
	New(state).DispatchNotificationOutbox()
}
