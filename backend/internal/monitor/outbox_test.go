package monitor

import (
	"strings"
	"testing"

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
