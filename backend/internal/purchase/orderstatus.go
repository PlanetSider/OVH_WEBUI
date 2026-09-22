package purchase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

const (
	orderStatusMinInterval = 2 * time.Minute
	orderStatusMaxAge      = 30 * 24 * time.Hour
)

var orderStatusTerminal = map[string]struct{}{
	"delivered":                  {},
	"cancelled":                  {},
	"cancelledbycustomer":        {},
	"cancelledbycustomerrequest": {},
}

// OrderStatusLoop 轮询已成功创建订单的状态；它只读 OVH 订单状态，
// 状态持久化和通知仍由 history/outbox owner 完成。
// StatusFetcher 是订单状态读取的最小接口，便于测试响应解析而不绑定
// concrete OVH client。
type StatusFetcher interface {
	GetWithContext(context.Context, string, interface{}) error
}

// FetchOrderStatus 读取 /me/order/{id}/status。OVH 当前返回裸 JSON 字符串，
// 同时容忍少数代理/兼容层返回的 status/state/orderStatus 对象；空或未知结构
// 一律返回错误，不覆盖历史中的旧状态。
func FetchOrderStatus(ctx context.Context, fetcher StatusFetcher, orderID string) (string, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return "", fmt.Errorf("order id is empty")
	}
	if fetcher == nil {
		return "", fmt.Errorf("order status fetcher is nil")
	}
	var response interface{}
	if err := fetcher.GetWithContext(ctx, "/me/order/"+orderID+"/status", &response); err != nil {
		return "", err
	}
	status := parseOrderStatus(response)
	if status == "" {
		return "", fmt.Errorf("order status response is empty or unsupported")
	}
	return status, nil
}

type OrderStatusLoop struct {
	state *app.State
}

func NewOrderStatusLoop(state *app.State) *OrderStatusLoop {
	return &OrderStatusLoop{state: state}
}

func (l *OrderStatusLoop) Run(ctx context.Context) {
	if l == nil || l.state == nil {
		return
	}
	l.Refresh(ctx)
	ticker := time.NewTicker(orderStatusMinInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Refresh(ctx)
		}
	}
}

func (l *OrderStatusLoop) Refresh(ctx context.Context) {
	if l == nil || l.state == nil || l.state.DB == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	candidates, err := l.state.DB.ListOrderStatusCandidates()
	if err != nil {
		l.state.Logger.Warn("读取订单状态候选失败: "+err.Error(), "order-status")
		return
	}
	now := time.Now()
	for _, entry := range candidates {
		if ctx.Err() != nil {
			return
		}
		if !orderStatusDue(entry, now) {
			continue
		}
		l.refreshOne(ctx, entry)
	}
}

func orderStatusDue(entry types.PurchaseHistoryEntry, now time.Time) bool {
	if strings.TrimSpace(entry.OrderID) == "" || isOrderStatusTerminal(entry.OrderStatus) {
		return false
	}
	purchasedAt, ok := types.ParseTS(entry.PurchaseTime)
	if !ok || now.Sub(purchasedAt) > orderStatusMaxAge {
		return false
	}
	if entry.OrderStatusAt == "" {
		return true
	}
	last, ok := types.ParseTS(entry.OrderStatusAt)
	return !ok || now.Sub(last) >= orderStatusMinInterval
}

func isOrderStatusTerminal(status string) bool {
	_, ok := orderStatusTerminal[strings.ToLower(strings.TrimSpace(status))]
	return ok
}

func (l *OrderStatusLoop) refreshOne(ctx context.Context, entry types.PurchaseHistoryEntry) {
	client, err := l.state.OVH.ClientFor(entry.AccountID)
	if err != nil {
		l.state.Logger.Warn(fmt.Sprintf("订单 %s 获取账户 client 失败: %s", entry.OrderID, err), "order-status")
		return
	}
	status, err := FetchOrderStatus(ctx, client, entry.OrderID)
	if err != nil {
		l.state.Logger.Warn(fmt.Sprintf("查询订单 %s 状态失败: %s", entry.OrderID, err), "order-status")
		return
	}
	statusAt := types.NowISO()
	changed, err := l.state.UpdateHistoryOrderStatus(entry.ID, status, statusAt, func(next types.PurchaseHistoryEntry, old string) (*types.NotificationOutboxEntry, error) {
		notification, notificationErr := monitor.NewOrderStatusNotification(next, old, monitor.NotificationTargetChannels(l.state))
		if notificationErr != nil {
			l.state.Logger.Warn(fmt.Sprintf("订单 %s 状态已变更，但通知载荷构造失败: %s", next.OrderID, notificationErr), "order-status")
			return nil, nil
		}
		return notification, nil
	})
	if err != nil {
		l.state.Logger.Warn(fmt.Sprintf("保存订单 %s 状态失败: %s", entry.OrderID, err), "order-status")
		return
	}
	if changed {
		l.state.Logger.Info(fmt.Sprintf("订单 %s 状态更新为 %s", entry.OrderID, status), "order-status")
	}
}

func parseOrderStatus(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]interface{}:
		for _, key := range []string{"status", "state", "orderStatus"} {
			if status, ok := v[key].(string); ok && strings.TrimSpace(status) != "" {
				return strings.TrimSpace(status)
			}
		}
	}
	return ""
}
