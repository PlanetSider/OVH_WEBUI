package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/proxyguard"
	"github.com/ovh-webui/server/internal/types"
)

func TestNewPurchaseSuccessNotificationCanonicalizesChannels(t *testing.T) {
	item := types.QueueItem{ID: "task-1", AccountID: "account-1", PlanCode: "24sk102", Datacenter: "gra", Options: []string{"ram-1", "disk-1"}}
	entry, err := NewPurchaseSuccessNotification(item, "order-1", "https://example.invalid/order-1", []string{"telegram", "feishu", "telegram", "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("notification is nil")
	}
	if entry.EventKey != "purchase_success:task-1" || entry.Kind != NotificationKindPurchaseSuccess {
		t.Fatalf("notification identity = %#v", entry)
	}
	if got := strings.Join(entry.Channels, ","); got != "feishu,telegram" {
		t.Fatalf("channels = %q, want feishu,telegram", got)
	}
	if !strings.Contains(entry.Payload, "\"orderId\":\"order-1\"") || !strings.Contains(entry.Payload, "\"options\":[\"ram-1\",\"disk-1\"]") {
		t.Fatalf("payload = %s", entry.Payload)
	}
}

func TestNewPurchaseSuccessNotificationWithoutTargetsWaitsForChannels(t *testing.T) {
	entry, err := NewPurchaseSuccessNotification(types.QueueItem{ID: "task-1"}, "order-1", "", nil)
	if err != nil || entry == nil || !entry.AwaitingChannels || len(entry.Channels) != 0 {
		t.Fatalf("entry=%#v err=%v, want deferred notification", entry, err)
	}
}

func TestPurchaseSuccessMessageIncludesOrderAndConfiguration(t *testing.T) {
	message := purchaseSuccessMessage(purchaseSuccessPayload{TaskID: "task-1", PlanCode: "26sk10b-v1", Datacenter: "SBG", Options: []string{"ram-1", "disk-1"}, OrderID: "order-1", OrderURL: "https://example.invalid/order-1"})
	for _, expected := range []string{"26sk10b-v1", "SBG", "order-1", "ram-1, disk-1", "task-1"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message missing %q: %s", expected, message)
		}
	}
}

func TestDispatchOutboxEntryRejectsInvalidOrUnknownPayload(t *testing.T) {
	m := &Monitor{}
	if _, err := m.dispatchOutboxEntry(types.NotificationOutboxEntry{Kind: NotificationKindPurchaseSuccess, Payload: `{"taskId":"task-1"}`}); err == nil {
		t.Fatal("missing purchase fields should be rejected")
	}
	if _, err := m.dispatchOutboxEntry(types.NotificationOutboxEntry{Kind: "future_kind", Payload: `{}`}); err == nil {
		t.Fatal("unknown notification kind should be rejected")
	}
}

func TestNewOrderStatusNotificationUsesStableIdentity(t *testing.T) {
	entry, err := NewOrderStatusNotification(types.PurchaseHistoryEntry{
		TaskID: "task-1", AccountID: "account-1", PlanCode: "24sk10", Datacenter: "gra",
		OrderID: "order-1", OrderURL: "https://example.invalid/order-1",
		OrderStatus: "delivered", OrderStatusAt: "2026-09-22T13:44:26.123456",
	}, "pending", []string{"telegram", "telegram", "feishu"})
	if err != nil || entry == nil {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if entry.EventKey != "order_status:order-1:delivered:2026-09-22T13:44:26.123456" || entry.Kind != NotificationKindOrderStatus {
		t.Fatalf("entry identity = %#v", entry)
	}
	if !strings.Contains(entry.Payload, `"oldStatus":"pending"`) || !strings.Contains(entry.Payload, `"orderStatus":"delivered"`) {
		t.Fatalf("payload = %s", entry.Payload)
	}
}

func TestNewCatalogStatusNotificationUsesStableTransitionIdentity(t *testing.T) {
	entry, err := NewCatalogStatusNotification("抢购", "24sk502", "KS-5", "1700000000", false, []string{"telegram", "telegram", "feishu"})
	if err != nil || entry == nil {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if entry.EventKey != "catalog_status:discontinued:抢购:24sk502:1700000000" {
		t.Fatalf("event key = %q", entry.EventKey)
	}
	if entry.Kind != NotificationKindCatalogStatus || strings.Join(entry.Channels, ",") != "feishu,telegram" {
		t.Fatalf("entry identity = %#v", entry)
	}
	if !strings.Contains(entry.Payload, `"serverName":"KS-5"`) || !strings.Contains(entry.Payload, `"recovered":false`) {
		t.Fatalf("payload = %s", entry.Payload)
	}
}

func TestNewProxyGuardNotificationScrubsProxyCredentials(t *testing.T) {
	entry, err := NewProxyGuardNotification(app.ProxyGuardAction{
		AccountID: "account-1", AccountName: "primary", AccountZone: "ie",
		ProxyURL: "http://user:secret@proxy.example:8080",
		Event: proxyguard.Event{Kind: proxyguard.EventTrip, Status: proxyguard.Status{
			AccountID: "account-1", ConsecutiveFailures: 3, LastError: "proxyconnect http://user:secret@proxy.example:8080 failed",
			LastFailureAt: time.Now().UTC(),
		}},
	}, []string{"telegram", "telegram"})
	if err != nil || entry == nil {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if entry.Kind != NotificationKindProxyGuard || !strings.HasPrefix(entry.EventKey, "proxy_guard:trip:account-1:") {
		t.Fatalf("entry identity = %#v", entry)
	}
	if strings.Contains(entry.Payload, "secret") || strings.Contains(entry.Payload, "user:") || strings.Contains(entry.Payload, "proxyconnect") {
		t.Fatalf("proxy credentials or provider error leaked in payload: %s", entry.Payload)
	}
	if !strings.Contains(entry.Payload, "代理探测失败") {
		t.Fatalf("stable proxy error missing from payload: %s", entry.Payload)
	}
	if !strings.Contains(entry.Payload, "proxy.example") {
		t.Fatalf("scrubbed proxy missing from payload: %s", entry.Payload)
	}
}

func TestDispatchProxyGuardNotificationAcceptsValidPayloadWithoutChannels(t *testing.T) {
	m := &Monitor{}
	entry, err := NewProxyGuardNotification(app.ProxyGuardAction{
		AccountID: "account-1",
		ProxyURL:  "http://proxy.example:8080",
		Event:     proxyguard.Event{Kind: proxyguard.EventRecovery, Status: proxyguard.Status{AccountID: "account-1"}},
	}, nil)
	if err != nil || entry == nil {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if _, err := m.dispatchOutboxEntry(*entry); err != nil {
		t.Fatalf("dispatch without selected channels: %v", err)
	}
}

func TestCatalogStatusMessageIncludesModeAndFrequency(t *testing.T) {
	messageTitle, message, template := catalogStatusMessage(catalogStatusPayload{
		Mode: "监控", PlanCode: "24sk502", ServerName: "KS-5", Recovered: false,
	})
	for _, expected := range []string{"正在监控的KS-5（24sk502）已停售", "每小时一次"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message missing %q: %s", expected, message)
		}
	}
	if messageTitle == "" || template != "red" {
		t.Fatalf("title/template = %q/%q", messageTitle, template)
	}
}
