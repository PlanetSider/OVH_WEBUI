package monitor

import (
	"sort"
	"strings"

	"github.com/ovh-webui/server/internal/app"
)

const (
	NotificationChannelTelegram = "telegram"
	NotificationChannelFeishu   = "feishu"
	NotificationChannelWeixin   = "weixin"
)

// NotificationDeliveryResult 按渠道记录一次通知尝试的结果。只有目标渠道
// 会出现在结果中；失败渠道必须保留在待发送状态，不能被其它渠道的成功覆盖。
type NotificationDeliveryResult map[string]bool

func canonicalNotificationChannels(channels []string) []string {
	seen := make(map[string]struct{}, len(channels))
	out := make([]string, 0, len(channels))
	for _, channel := range channels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		switch channel {
		case NotificationChannelTelegram, NotificationChannelFeishu, NotificationChannelWeixin:
		default:
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

func notificationChannelSelected(channels []string, wanted string) bool {
	for _, channel := range channels {
		if channel == wanted {
			return true
		}
	}
	return false
}

// ConfiguredNotificationChannels 返回当前确实具备接收端的渠道。它只用于
// 立即发送时的可用渠道判断，不应用来创建待通知快照。
func ConfiguredNotificationChannels(state *app.State) []string {
	if state == nil || state.Config == nil {
		return nil
	}
	cfg := state.Config.Get()
	channels := make([]string, 0, 3)
	if cfg.IsTelegramNotificationsEnabled() && strings.TrimSpace(cfg.TgToken) != "" && strings.TrimSpace(cfg.TgChatID) != "" {
		channels = append(channels, NotificationChannelTelegram)
	}
	if FeishuEnabled(state) {
		if _, ok := FeishuDefaultBinding(state); ok {
			channels = append(channels, NotificationChannelFeishu)
		}
	}
	if cfg.IsWeixinNotificationsEnabled() && state.Weixin != nil && state.Weixin.Configured() {
		channels = append(channels, NotificationChannelWeixin)
	}
	return canonicalNotificationChannels(channels)
}

// NotificationTargetChannels 给新 outbox 事件建立目标快照。只要用户启用了
// 渠道就把它纳入快照，不要求凭据此刻可用；这样凭据临时失效或尚未配置
// 完成时，事件仍会保留到渠道恢复或用户明确关闭。
func NotificationTargetChannels(state *app.State) []string {
	return PendingNotificationChannels(state)
}

// PendingNotificationChannels 返回当前启用的通知渠道，不要求凭据已经
// 配置完整。通知事件使用它建立目标快照，渠道暂时不可用时仍会保留，
// 待凭据恢复后由下一轮监控重试；只有用户明确关闭渠道才会被移除。
func PendingNotificationChannels(state *app.State) []string {
	if state == nil || state.Config == nil {
		return nil
	}
	cfg := state.Config.Get()
	channels := make([]string, 0, 3)
	if cfg.IsTelegramNotificationsEnabled() {
		channels = append(channels, NotificationChannelTelegram)
	}
	if cfg.IsFeishuNotificationsEnabled() {
		channels = append(channels, NotificationChannelFeishu)
	}
	if cfg.IsWeixinNotificationsEnabled() {
		channels = append(channels, NotificationChannelWeixin)
	}
	return canonicalNotificationChannels(channels)
}

// NotificationDeliveryComplete 仅要求当前已启用的渠道全部成功。这样一个渠道
// 失败时不会因另一个渠道成功而清掉待通知事件；已成功渠道会从持久化快照移除，
// 后续只重试仍失败的渠道。
func NotificationDeliveryComplete(state *app.State, results NotificationDeliveryResult) bool {
	return notificationDeliveryCompleteForChannels(state, ConfiguredNotificationChannels(state), results)
}

func notificationDeliveryCompleteForChannels(state *app.State, expected []string, results NotificationDeliveryResult) bool {
	if state == nil {
		return false
	}
	wanted := canonicalNotificationChannels(expected)
	if len(wanted) == 0 {
		return false
	}
	for _, channel := range wanted {
		if !results[channel] {
			return false
		}
	}
	return true
}

// EnabledNotificationChannels 只剔除被用户明确关闭的渠道。凭据暂时失效不等于
// 用户关闭：这类渠道仍要保留，待配置恢复后重试。
func EnabledNotificationChannels(state *app.State, channels []string) []string {
	if state == nil || state.Config == nil {
		return nil
	}
	cfg := state.Config.Get()
	out := make([]string, 0, len(channels))
	for _, channel := range canonicalNotificationChannels(channels) {
		switch channel {
		case NotificationChannelTelegram:
			if cfg.IsTelegramNotificationsEnabled() {
				out = append(out, channel)
			}
		case NotificationChannelFeishu:
			if cfg.IsFeishuNotificationsEnabled() {
				out = append(out, channel)
			}
		case NotificationChannelWeixin:
			if cfg.IsWeixinNotificationsEnabled() {
				out = append(out, channel)
			}
		}
	}
	return out
}

func remainingNotificationChannels(channels []string, delivered NotificationDeliveryResult) []string {
	remaining := make([]string, 0, len(channels))
	for _, channel := range canonicalNotificationChannels(channels) {
		if !delivered[channel] {
			remaining = append(remaining, channel)
		}
	}
	return remaining
}

// DeliveryCompleteForChannels reports whether every snapshotted target channel
// completed. It is exported for the VPS monitor, which shares delivery policy
// without importing dedicated-server internals.
func DeliveryCompleteForChannels(channels []string, delivered NotificationDeliveryResult) bool {
	wanted := canonicalNotificationChannels(channels)
	if len(wanted) == 0 {
		return false
	}
	for _, channel := range wanted {
		if !delivered[channel] {
			return false
		}
	}
	return true
}

// RemainingNotificationChannels removes successfully delivered targets.
func RemainingNotificationChannels(channels []string, delivered NotificationDeliveryResult) []string {
	return remainingNotificationChannels(channels, delivered)
}

func notificationChannelKey(channels []string) string {
	return strings.Join(canonicalNotificationChannels(channels), ",")
}
