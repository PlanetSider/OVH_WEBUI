package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

const (
	orderStatusMinInterval = 2 * time.Minute
	orderStatusMaxAge      = 30 * 24 * time.Hour
)

var ErrOrderStatusRefreshInProgress = errors.New("order status refresh already in progress")

// OrderStatusRefreshResult 描述一次后台或手动刷新，不暴露订单创建/支付操作。
type OrderStatusRefreshResult struct {
	Candidates int      `json:"candidates"`
	Selected   int      `json:"selected"`
	Updated    int      `json:"updated"`
	Skipped    int      `json:"skipped"`
	Failed     int      `json:"failed"`
	Errors     []string `json:"errors,omitempty"`
}

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
	state     *app.State
	refreshMu sync.Mutex
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

// Refresh 执行一轮后台刷新；若手动刷新正在运行则跳过本轮，避免重复查询。
func (l *OrderStatusLoop) Refresh(ctx context.Context) {
	_, _ = l.refresh(ctx, false)
}

// RefreshNow 强制刷新当前可刷新的成功订单，绕过两分钟节流，但仍跳过
// 终态和超过保留窗口的订单。后台轮询与手动入口共享同一互斥锁。
func (l *OrderStatusLoop) RefreshNow(ctx context.Context) (OrderStatusRefreshResult, error) {
	result, acquired := l.refresh(ctx, true)
	if !acquired {
		return result, ErrOrderStatusRefreshInProgress
	}
	return result, nil
}

func (l *OrderStatusLoop) refresh(ctx context.Context, force bool) (OrderStatusRefreshResult, bool) {
	result := OrderStatusRefreshResult{Errors: []string{}}
	if l == nil {
		result.Failed = 1
		result.Errors = append(result.Errors, "订单状态刷新器不可用")
		return result, true
	}
	if !l.refreshMu.TryLock() {
		return result, false
	}
	defer l.refreshMu.Unlock()
	if l.state == nil || l.state.DB == nil {
		result.Failed = 1
		result.Errors = append(result.Errors, "订单状态存储不可用")
		return result, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	candidates, err := l.state.DB.ListOrderStatusCandidates()
	if err != nil {
		result.Failed = 1
		result.Errors = append(result.Errors, scrubRefreshError("读取订单状态候选失败", err))
		if l.state.Logger != nil {
			l.state.Logger.Warn("读取订单状态候选失败", "order-status")
		}
		return result, true
	}
	result.Candidates = len(candidates)
	now := time.Now()
	for _, entry := range candidates {
		if ctx.Err() != nil {
			result.Failed++
			result.Errors = append(result.Errors, "订单状态刷新已取消")
			break
		}
		if !orderStatusRefreshEligible(entry, now, force) {
			result.Skipped++
			continue
		}
		result.Selected++
		changed, refreshErr := l.refreshOne(ctx, entry)
		if refreshErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, scrubRefreshError("订单 "+entry.OrderID+" 刷新失败", refreshErr))
			continue
		}
		if changed {
			result.Updated++
		}
	}
	return result, true
}

func orderStatusRefreshEligible(entry types.PurchaseHistoryEntry, now time.Time, force bool) bool {
	if strings.TrimSpace(entry.OrderID) == "" || isOrderStatusTerminal(entry.OrderStatus) {
		return false
	}
	purchasedAt, ok := types.ParseTS(entry.PurchaseTime)
	if !ok || now.Sub(purchasedAt) > orderStatusMaxAge {
		return false
	}
	return force || orderStatusDue(entry, now)
}

func scrubRefreshError(prefix string, _ error) string {
	return prefix
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

func (l *OrderStatusLoop) refreshOne(ctx context.Context, entry types.PurchaseHistoryEntry) (bool, error) {
	if l.state.OVH == nil {
		return false, fmt.Errorf("OVH client factory unavailable")
	}
	client, err := l.state.OVH.ClientFor(entry.AccountID)
	if err != nil {
		if l.state.Logger != nil {
			l.state.Logger.Warn(fmt.Sprintf("订单 %s 获取账户 client 失败", entry.OrderID), "order-status")
		}
		return false, err
	}
	status, err := FetchOrderStatus(ctx, client, entry.OrderID)
	if err != nil {
		if l.state.Logger != nil {
			l.state.Logger.Warn(fmt.Sprintf("订单 %s 状态查询失败", entry.OrderID), "order-status")
		}
		return false, err
	}
	statusAt := types.NowISO()
	statusChanged := strings.TrimSpace(entry.OrderStatus) != strings.TrimSpace(status)
	changed, err := l.state.UpdateHistoryOrderStatus(entry.ID, status, statusAt, func(next types.PurchaseHistoryEntry, old string) (*types.NotificationOutboxEntry, error) {
		notification, notificationErr := monitor.NewOrderStatusNotification(next, old, monitor.NotificationTargetChannels(l.state))
		if notificationErr != nil {
			if l.state.Logger != nil {
				l.state.Logger.Warn(fmt.Sprintf("订单 %s 状态已变更，但通知载荷构造失败: %s", next.OrderID, notificationErr), "order-status")
			}
			return nil, nil
		}
		return notification, nil
	})
	if err != nil {
		if l.state.Logger != nil {
			l.state.Logger.Warn(fmt.Sprintf("订单 %s 状态保存失败", entry.OrderID), "order-status")
		}
		return false, err
	}
	if changed && l.state.Logger != nil {
		l.state.Logger.Info(fmt.Sprintf("订单 %s 状态更新为 %s", entry.OrderID, status), "order-status")
	}
	return statusChanged, nil
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
