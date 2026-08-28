package monitor

import "testing"

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
	applyMonitorStatus(sub, key, "price_check_failed", "unavailable", true, "unavailable", true)
	if sub.PendingOrder[key] != 0 || sub.ConfirmedStatus[key] != "unavailable" {
		t.Fatal("price failure must preserve confirmed unavailable without enqueueing")
	}
	change := applyMonitorStatus(sub, key, "available", "price_check_failed", true, "unavailable", true)
	if change != "available" || sub.PendingOrder[key] != 3 || sub.ConfirmedStatus[key] != "available" {
		t.Fatalf("recovery transition = %q pending=%d confirmed=%q", change, sub.PendingOrder[key], sub.ConfirmedStatus[key])
	}
	applyMonitorStatus(sub, key, "available", "available", true, "available", true)
	if sub.PendingOrder[key] != 3 {
		t.Fatalf("continuous availability overwrote pending count: %d", sub.PendingOrder[key])
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
		"GRA": {statusKey: "gra|config", actualStatus: "available"},
		"SBG": {statusKey: "sbg|config", actualStatus: "available"},
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
		"SBG": {statusKey: "sbg|config", actualStatus: "available"},
		"GRA": {statusKey: "gra|config", actualStatus: "available"},
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
