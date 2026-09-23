package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

type testStatusFetcher struct {
	value interface{}
	path  string
}

func (f *testStatusFetcher) GetWithContext(_ context.Context, path string, target interface{}) error {
	f.path = path
	*target.(*interface{}) = f.value
	return nil
}

func TestFetchOrderStatus(t *testing.T) {
	fetcher := &testStatusFetcher{value: " pending "}
	status, err := FetchOrderStatus(context.Background(), fetcher, "order-1")
	if err != nil || status != "pending" || fetcher.path != "/me/order/order-1/status" {
		t.Fatalf("status=%q err=%v path=%q", status, err, fetcher.path)
	}
	fetcher.value = map[string]interface{}{"state": "delivered"}
	status, err = FetchOrderStatus(context.Background(), fetcher, "order-2")
	if err != nil || status != "delivered" {
		t.Fatalf("object status=%q err=%v", status, err)
	}
	if _, err := FetchOrderStatus(context.Background(), fetcher, ""); err == nil {
		t.Fatal("empty order id should fail")
	}
	fetcher.value = map[string]interface{}{"message": "ok"}
	if _, err := FetchOrderStatus(context.Background(), fetcher, "order-3"); err == nil {
		t.Fatal("unsupported response should fail")
	}
}

func TestParseOrderStatus(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "string", value: " delivered ", want: "delivered"},
		{name: "status field", value: map[string]interface{}{"status": "pending"}, want: "pending"},
		{name: "state field", value: map[string]interface{}{"state": "cancelled"}, want: "cancelled"},
		{name: "empty", value: map[string]interface{}{"message": "ok"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseOrderStatus(tc.value); got != tc.want {
				t.Fatalf("parseOrderStatus(%#v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestOrderStatusDue(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour).Format(types.NowISOLayout)
	old := now.Add(-31 * 24 * time.Hour).Format(types.NowISOLayout)
	if !orderStatusDue(types.PurchaseHistoryEntry{OrderID: "o1", PurchaseTime: fresh}, now) {
		t.Fatal("fresh order should be due")
	}
	if orderStatusDue(types.PurchaseHistoryEntry{OrderID: "o2", PurchaseTime: fresh, OrderStatus: "delivered"}, now) {
		t.Fatal("delivered order should not be due")
	}
	if orderStatusDue(types.PurchaseHistoryEntry{OrderID: "o3", PurchaseTime: old}, now) {
		t.Fatal("old order should not be due")
	}
	if orderStatusDue(types.PurchaseHistoryEntry{OrderID: "o4", PurchaseTime: fresh, OrderStatusAt: now.Add(-time.Minute).Format(types.NowISOLayout)}, now) {
		t.Fatal("recently checked order should be throttled")
	}
}

func TestOrderStatusRefreshEligibleForceBypassesThrottleOnly(t *testing.T) {
	now := time.Now()
	recentlyChecked := types.PurchaseHistoryEntry{
		OrderID: "o1", PurchaseTime: now.Add(-time.Hour).Format(types.NowISOLayout),
		OrderStatusAt: now.Add(-time.Minute).Format(types.NowISOLayout),
	}
	if orderStatusRefreshEligible(recentlyChecked, now, false) {
		t.Fatal("background refresh should respect the throttle")
	}
	if !orderStatusRefreshEligible(recentlyChecked, now, true) {
		t.Fatal("manual refresh should bypass the throttle")
	}
	terminal := recentlyChecked
	terminal.OrderStatus = "delivered"
	if orderStatusRefreshEligible(terminal, now, true) {
		t.Fatal("manual refresh should not query terminal orders")
	}
	old := recentlyChecked
	old.PurchaseTime = now.Add(-31 * 24 * time.Hour).Format(types.NowISOLayout)
	if orderStatusRefreshEligible(old, now, true) {
		t.Fatal("manual refresh should respect the maximum age")
	}
}

func TestOrderStatusRefreshNowRejectsConcurrentRun(t *testing.T) {
	loop := &OrderStatusLoop{}
	loop.refreshMu.Lock()
	defer loop.refreshMu.Unlock()

	_, err := loop.RefreshNow(context.Background())
	if !errors.Is(err, ErrOrderStatusRefreshInProgress) {
		t.Fatalf("RefreshNow() error = %v, want ErrOrderStatusRefreshInProgress", err)
	}
}
