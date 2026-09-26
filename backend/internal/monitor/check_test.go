package monitor

import (
	"strings"
	"testing"
)

func TestGroupAvailabilityNotificationsSeparatesDifferentPrices(t *testing.T) {
	notifications := []notification{
		{dc: "SBG", statusKey: "SBG|config"},
		{dc: "BHS", statusKey: "BHS|config"},
		{dc: "GRA", statusKey: "GRA|config"},
	}
	prices := map[string]monitorPriceCheck{
		"BHS": {text: "月费: 10/月", ok: true},
		"GRA": {text: "月费: 12/月", ok: true},
		"SBG": {text: "月费: 12/月", ok: true},
	}

	groups := groupAvailabilityNotifications(notifications, prices)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].priceText != "月费: 10/月" || len(groups[0].notifications) != 1 || groups[0].notifications[0].dc != "BHS" {
		t.Fatalf("unexpected first group: %#v", groups[0])
	}
	if groups[1].priceText != "月费: 12/月" || len(groups[1].notifications) != 2 ||
		groups[1].notifications[0].dc != "GRA" || groups[1].notifications[1].dc != "SBG" {
		t.Fatalf("unexpected second group: %#v", groups[1])
	}
}

func TestGroupAvailabilityNotificationsKeepsMissingPriceSeparate(t *testing.T) {
	notifications := []notification{{dc: "BHS"}, {dc: "GRA"}}
	prices := map[string]monitorPriceCheck{
		"BHS": {text: "月费: 10/月", ok: true},
	}

	groups := groupAvailabilityNotifications(notifications, prices)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want priced and missing-price groups", len(groups))
	}
	if groups[1].priceError == "" || len(groups[1].notifications) != 1 || groups[1].notifications[0].dc != "GRA" {
		t.Fatalf("unexpected missing-price group: %#v", groups[1])
	}
}

func TestGroupAvailabilityNotificationsSeparatesDifferentChannels(t *testing.T) {
	notifications := []notification{
		{dc: "GRA", channelsKey: "telegram"},
		{dc: "SBG", channelsKey: "feishu"},
	}
	prices := map[string]monitorPriceCheck{
		"GRA": {text: "月费: 10/月", ok: true},
		"SBG": {text: "月费: 10/月", ok: true},
	}

	groups := groupAvailabilityNotifications(notifications, prices)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want separate channel groups", len(groups))
	}
}

func TestApplyNotificationDeliveryKeepsOnlyFailedChannels(t *testing.T) {
	sub := newTransitionSubscription()
	sub.PendingNotify["GRA|config"] = "available"
	sub.PendingNotifyChannels = map[string][]string{
		"GRA|config": {NotificationChannelTelegram, NotificationChannelFeishu},
	}

	complete := applyNotificationDelivery(sub, []string{"GRA|config"}, nil, NotificationDeliveryResult{
		NotificationChannelTelegram: true,
		NotificationChannelFeishu:   false,
	})
	if complete {
		t.Fatal("partial delivery must not be complete")
	}
	remaining := sub.PendingNotifyChannels["GRA|config"]
	if len(remaining) != 1 || remaining[0] != NotificationChannelFeishu {
		t.Fatalf("remaining channels = %v, want only feishu", remaining)
	}
}

func TestApplyNotificationDeliveryClearsFullyDeliveredEvent(t *testing.T) {
	sub := newTransitionSubscription()
	sub.PendingNotify["GRA|config"] = "available"
	sub.PendingNotifyChannels = map[string][]string{
		"GRA|config": {NotificationChannelTelegram},
	}

	if !applyNotificationDelivery(sub, []string{"GRA|config"}, nil, NotificationDeliveryResult{NotificationChannelTelegram: true}) {
		t.Fatal("full delivery must be complete")
	}
	if _, exists := sub.PendingNotify["GRA|config"]; exists {
		t.Fatal("fully delivered event was not cleared")
	}
}

func newTransitionSubscription() *Subscription {
	return &Subscription{
		NotifyAvailable: true, NotifyUnavailable: true,
		AutoOrder: true, AutoOrderAccountID: "account-1", Quantity: 3,
		LastStatus: map[string]string{}, ConfirmedStatus: map[string]string{},
		PendingOrder: map[string]int{}, PendingNotify: map[string]string{},
	}
}

func TestMonitorStatusUnavailablePriceFailureAvailableQueuesOnce(t *testing.T) {
	const key = "gra|config"
	sub := newTransitionSubscription()
	applyMonitorStatus(sub, key, "unavailable", "", false, "", false)
	if sub.PendingOrder[key] != 0 {
		t.Fatal("initial unavailable must not enqueue")
	}
	change := applyMonitorStatus(sub, key, "price_check_failed", "unavailable", true, "unavailable", true)
	if change != "price_check_failed" || sub.PendingOrder[key] != 3 || sub.ConfirmedStatus[key] != "available" {
		t.Fatalf("price-failed restock = %q pending=%d confirmed=%q", change, sub.PendingOrder[key], sub.ConfirmedStatus[key])
	}
	consumePendingOrders(sub.PendingOrder, []notification{{statusKey: key, orderCount: 3}})
	applyMonitorStatus(sub, key, "price_check_failed", "price_check_failed", true, "available", true)
	if sub.PendingOrder[key] != 0 {
		t.Fatalf("continuous price failure requeued %d orders", sub.PendingOrder[key])
	}
	change = applyMonitorStatus(sub, key, "available", "price_check_failed", true, "available", true)
	if change != "" || sub.PendingOrder[key] != 0 || sub.ConfirmedStatus[key] != "available" {
		t.Fatalf("price recovery requeued stock: change=%q pending=%d confirmed=%q", change, sub.PendingOrder[key], sub.ConfirmedStatus[key])
	}
	applyMonitorStatus(sub, key, "unavailable", "available", true, "available", true)
	applyMonitorStatus(sub, key, "price_check_failed", "unavailable", true, "unavailable", true)
	if sub.PendingOrder[key] != 3 {
		t.Fatalf("new restock did not enqueue: %d", sub.PendingOrder[key])
	}
}

func TestMonitorStatusContinuousAvailableDoesNotRequeue(t *testing.T) {
	const key = "gra|config"
	sub := newTransitionSubscription()
	sub.ConfirmedStatus[key] = "available"
	change := applyMonitorStatus(sub, key, "price_check_failed", "available", true, "available", true)
	if change != "price_check_failed" || sub.ConfirmedStatus[key] != "available" {
		t.Fatalf("price failure changed confirmed state: change=%q confirmed=%q", change, sub.ConfirmedStatus[key])
	}
	change = applyMonitorStatus(sub, key, "available", "price_check_failed", true, "available", true)
	if change != "" || sub.PendingOrder[key] != 0 {
		t.Fatalf("available recovery unexpectedly queued: change=%q pending=%d", change, sub.PendingOrder[key])
	}
}

func TestMonitorStatusPriceFailureDoesNotBackfillOldOrInitialStock(t *testing.T) {
	const key = "gra|config"
	initial := newTransitionSubscription()
	applyMonitorStatus(initial, key, "price_check_failed", "", false, "", false)
	if initial.PendingOrder[key] != 0 || initial.ConfirmedStatus[key] != "available" {
		t.Fatalf("initial stock triggered order: pending=%d confirmed=%q", initial.PendingOrder[key], initial.ConfirmedStatus[key])
	}
	legacy := newTransitionSubscription()
	legacy.LastStatus[key] = "price_check_failed"
	legacy.ConfirmedStatus[key] = "unavailable"
	applyMonitorStatus(legacy, key, "price_check_failed", "price_check_failed", true, "unavailable", true)
	if legacy.PendingOrder[key] != 0 || legacy.ConfirmedStatus[key] != "available" {
		t.Fatalf("historical price failure backfilled order: pending=%d confirmed=%q", legacy.PendingOrder[key], legacy.ConfirmedStatus[key])
	}
	applyMonitorStatus(legacy, key, "available", "price_check_failed", true, "available", true)
	if legacy.PendingOrder[key] != 0 {
		t.Fatalf("historical failure requeued after recovery: %d", legacy.PendingOrder[key])
	}
}

func TestMonitorStatusPriceFailureHonorsAutoOrderAndAccount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoOrder bool
		accountID string
		want      int
	}{
		{name: "auto order disabled", autoOrder: false, accountID: "account-1"},
		{name: "no account", autoOrder: true},
		{name: "notifications disabled", autoOrder: true, accountID: "account-1", want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := newTransitionSubscription()
			sub.NotifyAvailable = false
			sub.AutoOrder = tc.autoOrder
			sub.AutoOrderAccountID = tc.accountID
			const key = "gra|config"
			sub.LastStatus[key] = "unavailable"
			sub.ConfirmedStatus[key] = "unavailable"
			applyMonitorStatus(sub, key, "price_check_failed", "unavailable", true, "unavailable", true)
			if sub.PendingOrder[key] != tc.want {
				t.Fatalf("pending = %d, want %d", sub.PendingOrder[key], tc.want)
			}
		})
	}
}

func TestMonitorStatusUnavailableClearsPendingOrder(t *testing.T) {
	const key = "gra|config"
	sub := newTransitionSubscription()
	sub.PendingOrder[key] = 2
	applyMonitorStatus(sub, key, "unavailable", "available", true, "available", true)
	if _, exists := sub.PendingOrder[key]; exists {
		t.Fatal("explicit unavailable must clear pending order")
	}
}

func TestPendingOrderTargetsCapsBatchWithoutMutatingPending(t *testing.T) {
	sub := newTransitionSubscription()
	sub.PendingOrder = map[string]int{"gra|config": 250, "sbg|config": 2}
	statuses := map[string]dcStatusSnapshot{
		"GRA": {statusKey: "gra|config", actualStatus: "available", inventoryAvailable: true},
		"SBG": {statusKey: "sbg|config", actualStatus: "available", inventoryAvailable: true},
	}
	targets := pendingOrderTargets(sub, statuses, 200)
	if len(targets) != 1 || targets[0].dc != "GRA" || targets[0].orderCount != 200 {
		t.Fatalf("targets = %#v, want one 200-item GRA batch", targets)
	}
	if sub.PendingOrder["gra|config"] != 250 || sub.PendingOrder["sbg|config"] != 2 {
		t.Fatalf("target planning mutated pending orders: %#v", sub.PendingOrder)
	}
}

func TestPendingOrderTargetsSkipsUnconfirmedAvailability(t *testing.T) {
	sub := newTransitionSubscription()
	sub.PendingOrder = map[string]int{"gra|config": 2}
	targets := pendingOrderTargets(sub, map[string]dcStatusSnapshot{
		"GRA": {statusKey: "gra|config", actualStatus: "price_check_failed"},
	}, 200)
	if len(targets) != 0 {
		t.Fatalf("unconfirmed availability produced order targets: %#v", targets)
	}
}

func TestPendingOrderTargetsPriceFailureRequiresExplicitStockAndPreservesRemainder(t *testing.T) {
	const key = "gra|config"
	sub := newTransitionSubscription()
	sub.PendingOrder[key] = 3
	statuses := map[string]dcStatusSnapshot{
		"GRA": {statusKey: key, actualStatus: "price_check_failed", inventoryAvailable: true},
	}
	first := pendingOrderTargets(sub, statuses, 2)
	if len(first) != 1 || first[0].orderCount != 2 {
		t.Fatalf("first batch = %#v, want two price-failed stock orders", first)
	}
	consumePendingOrders(sub.PendingOrder, first)
	if sub.PendingOrder[key] != 1 {
		t.Fatalf("remaining orders = %d, want one", sub.PendingOrder[key])
	}
	second := pendingOrderTargets(sub, statuses, 1)
	if len(second) != 1 || second[0].orderCount != 1 {
		t.Fatalf("second batch = %#v, want remaining order", second)
	}
	for _, status := range []dcStatusSnapshot{
		{statusKey: key, actualStatus: "available", inventoryAvailable: false},
		{statusKey: key, actualStatus: "price_check_failed", inventoryAvailable: false},
		{statusKey: key, actualStatus: "unavailable", inventoryAvailable: true},
		{statusKey: key, actualStatus: "unknown", inventoryAvailable: true},
	} {
		if targets := pendingOrderTargets(sub, map[string]dcStatusSnapshot{"GRA": status}, 200); len(targets) != 0 {
			t.Fatalf("invalid stock produced targets: status=%#v targets=%#v", status, targets)
		}
	}
}

func TestConsumePendingOrdersPreservesUnqueuedRemainder(t *testing.T) {
	pending := map[string]int{"gra|config": 250, "sbg|config": 2}
	consumePendingOrders(pending, []notification{{statusKey: "gra|config", orderCount: 200}})
	if pending["gra|config"] != 50 || pending["sbg|config"] != 2 {
		t.Fatalf("pending after partial batch = %#v", pending)
	}
	consumePendingOrders(pending, []notification{{statusKey: "gra|config", orderCount: 50}})
	if _, exists := pending["gra|config"]; exists {
		t.Fatalf("fully consumed pending key remains: %#v", pending)
	}
}

func TestPendingOrderTargetsUsesCapacityInStableDatacenterOrder(t *testing.T) {
	sub := newTransitionSubscription()
	sub.PendingOrder = map[string]int{"gra|config": 1, "sbg|config": 1}
	statuses := map[string]dcStatusSnapshot{
		"SBG": {statusKey: "sbg|config", actualStatus: "available", inventoryAvailable: true},
		"GRA": {statusKey: "gra|config", actualStatus: "available", inventoryAvailable: true},
	}

	first := pendingOrderTargets(sub, statuses, 1)
	if len(first) != 1 || first[0].dc != "GRA" || first[0].orderCount != 1 {
		t.Fatalf("first batch = %#v, want one GRA order", first)
	}
	consumePendingOrders(sub.PendingOrder, first)
	if _, exists := sub.PendingOrder["gra|config"]; exists || sub.PendingOrder["sbg|config"] != 1 {
		t.Fatalf("pending after first batch = %#v, want only SBG", sub.PendingOrder)
	}

	second := pendingOrderTargets(sub, statuses, 1)
	if len(second) != 1 || second[0].dc != "SBG" || second[0].orderCount != 1 {
		t.Fatalf("second batch = %#v, want remaining SBG order", second)
	}
}

func TestPendingOrderRemainderIsClearedWhenDatacenterBecomesUnavailable(t *testing.T) {
	const key = "sbg|config"
	sub := newTransitionSubscription()
	sub.PendingOrder[key] = 2
	sub.ConfirmedStatus[key] = "available"
	sub.LastStatus[key] = "available"

	applyMonitorStatus(sub, key, "unavailable", "available", true, "available", true)
	if _, exists := sub.PendingOrder[key]; exists {
		t.Fatalf("unavailable datacenter retained pending remainder: %#v", sub.PendingOrder)
	}
}

func TestClearDisabledPendingNotify(t *testing.T) {
	sub := newTransitionSubscription()
	sub.PendingNotify = map[string]string{
		"a": "available", "b": "price_check_failed", "c": "unavailable",
	}
	if !clearDisabledPendingNotify(sub, false, true) {
		t.Fatal("expected disabled available notifications to be cleared")
	}
	if _, ok := sub.PendingNotify["a"]; ok {
		t.Fatal("available pending notification was not cleared")
	}
	if _, ok := sub.PendingNotify["b"]; ok {
		t.Fatal("price-check pending notification was not cleared")
	}
	if sub.PendingNotify["c"] != "unavailable" {
		t.Fatal("enabled unavailable notification must be preserved")
	}
}

func TestPriceCheckFailureExplanationDoesNotSayOrderSkipped(t *testing.T) {
	for _, reason := range []string{"", "OVHcloud API 404"} {
		message := priceCheckFailureExplanation(reason)
		for _, text := range []string{"价格校验未通过", "首月总价尚未确认", "仍会尝试下单", "实际金额"} {
			if !strings.Contains(message, text) {
				t.Fatalf("message %q missing %q", message, text)
			}
		}
		if strings.Contains(message, "已跳过自动下单") {
			t.Fatalf("incorrect skip notice: %q", message)
		}
		if reason != "" && !strings.Contains(message, reason) {
			t.Fatalf("message %q missing reason %q", message, reason)
		}
	}
}

func TestSameSubscriptionSettingsIncludesServerName(t *testing.T) {
	left := newTransitionSubscription()
	left.PlanCode = "24sk10"
	left.ServerName = "旧名称"
	right := cloneSubscription(left)
	right.ServerName = "新名称"
	if sameSubscriptionSettings(left, right) {
		t.Fatal("server name edit must invalidate an in-flight monitor snapshot")
	}
}
