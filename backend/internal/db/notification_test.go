package db

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestUpdateNotificationChannelsConcurrentCASAllowsOnlyOneWriter(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.EnqueueNotification(testNotification("event-cas", "telegram", "feishu")); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list outbox=%#v err=%v", items, err)
	}
	expected := []string{"feishu", "telegram"}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, remaining := range [][]string{{"telegram"}, {"feishu"}} {
		remaining := remaining
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, callErr := database.UpdateNotificationChannels(items[0].ID, expected, remaining)
			if callErr != nil {
				t.Errorf("CAS update error: %v", callErr)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("CAS update successes=%d, want 1", successes)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 || len(items[0].Channels) != 1 {
		t.Fatalf("final outbox=%#v err=%v, want one remaining channel", items, err)
	}
}

func TestCheckoutAttemptCannotBeOverwrittenOrCompletedWithAnotherOrder(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	item := types.QueueItem{
		ID: "task-checkout-guard", AccountID: "account-original", PlanCode: "24sk10",
		Datacenter: "gra", Options: []string{"ram-original"}, RetryCount: 2,
	}
	if err := database.RecordCheckoutAttempt(item, "cart-original"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-original", "https://example.invalid/original"); err != nil {
		t.Fatal(err)
	}

	replacement := item
	replacement.AccountID = "account-replacement"
	replacement.Datacenter = "sbg"
	replacement.Options = []string{"ram-replacement"}
	if err := database.RecordCheckoutAttempt(replacement, "cart-replacement"); !errors.Is(err, ErrCheckoutAttemptExists) {
		t.Fatalf("RecordCheckoutAttempt() error = %v, want ErrCheckoutAttemptExists", err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-other", "https://example.invalid/other"); err == nil {
		t.Fatal("CompleteCheckoutAttempt() overwrote an existing order")
	}

	var cartID, accountID, datacenter, options, orderID, orderURL string
	if err := database.QueryRowx(`
		SELECT cart_id, account_id, datacenter, options, order_id, order_url
		FROM checkout_attempts WHERE task_id = ?
	`, item.ID).Scan(&cartID, &accountID, &datacenter, &options, &orderID, &orderURL); err != nil {
		t.Fatal(err)
	}
	if cartID != "cart-original" || accountID != "account-original" || datacenter != "gra" ||
		options != `["ram-original"]` || orderID != "order-original" || orderURL != "https://example.invalid/original" {
		t.Fatalf("checkout attempt was overwritten: cart=%q account=%q dc=%q options=%q order=%q url=%q",
			cartID, accountID, datacenter, options, orderID, orderURL)
	}
}

func testNotification(eventKey string, channels ...string) types.NotificationOutboxEntry {
	return types.NotificationOutboxEntry{
		EventKey: eventKey,
		Kind:     "test",
		Payload:  "{\"ok\":true}",
		Channels: channels,
	}
}

func TestNotificationOutboxIsIdempotentAndTracksRemainingChannels(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	entry := testNotification("event-1", "telegram", "feishu", "telegram")
	if err := database.EnqueueNotification(entry); err != nil {
		t.Fatal(err)
	}
	if err := database.EnqueueNotification(entry); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("outbox len = %d, want 1", len(items))
	}
	if want := []string{"feishu", "telegram"}; !reflect.DeepEqual(items[0].Channels, want) {
		t.Fatalf("channels = %#v, want %#v", items[0].Channels, want)
	}

	ok, err := database.UpdateNotificationChannels(items[0].ID, []string{"telegram", "feishu"}, []string{"telegram"})
	if err != nil || !ok {
		t.Fatalf("partial channel update ok=%v err=%v", ok, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0].Channels, []string{"telegram"}) {
		t.Fatalf("remaining outbox = %#v", items)
	}
	ok, err = database.UpdateNotificationChannels(items[0].ID, []string{"feishu", "telegram"}, nil)
	if err != nil || ok {
		t.Fatalf("stale update ok=%v err=%v, want no match", ok, err)
	}
	ok, err = database.UpdateNotificationChannels(items[0].ID, []string{"telegram"}, nil)
	if err != nil || !ok {
		t.Fatalf("final channel delete ok=%v err=%v", ok, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 0 {
		t.Fatalf("outbox not empty: %#v err=%v", items, err)
	}
}

func TestSaveKnownServersAndNotificationsRollsBackTogether(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.SaveKnownServersAndNotifications([]string{"old-plan"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TRIGGER reject_notification_insert BEFORE INSERT ON notification_outbox BEGIN SELECT RAISE(ABORT, 'forced outbox failure'); END"); err != nil {
		t.Fatal(err)
	}
	err = database.SaveKnownServersAndNotifications([]string{"new-plan"}, []types.NotificationOutboxEntry{testNotification("new_server:new-plan", "telegram")})
	if err == nil {
		t.Fatal("expected forced transaction failure")
	}
	var known []string
	ok, getErr := database.GetKV("monitor_known_servers", &known)
	if getErr != nil || !ok || !reflect.DeepEqual(known, []string{"old-plan"}) {
		t.Fatalf("known servers = %#v, ok=%v err=%v", known, ok, getErr)
	}
	items, listErr := database.ListNotificationOutbox(10)
	if listErr != nil || len(items) != 0 {
		t.Fatalf("outbox changed after rollback: %#v err=%v", items, listErr)
	}
}

func TestCommitPurchaseSuccessWithNotificationIsAtomicAndIdempotent(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	item := types.QueueItem{ID: "task-success", AccountID: "account-1", PlanCode: "24sk10", Datacenter: "gra", Options: []string{"ram-1"}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-1"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-1", "https://example.invalid/order-1"); err != nil {
		t.Fatal(err)
	}
	history := types.PurchaseHistoryEntry{ID: "history-1", TaskID: item.ID, AccountID: item.AccountID, PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: item.Options, Status: "success", OrderID: "order-1", OrderURL: "https://example.invalid/order-1", PurchaseTime: types.NowISO()}
	notification := testNotification("purchase_success:"+item.ID, "telegram", "feishu")
	if err := database.CommitPurchaseSuccessWithNotification(history, &notification); err != nil {
		t.Fatal(err)
	}
	history.ID = "history-2"
	if err := database.CommitPurchaseSuccessWithNotification(history, &notification); err != nil {
		t.Fatal(err)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("queue = %#v err=%v", queue, err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 || histories[0].ID != "history-2" {
		t.Fatalf("history = %#v err=%v", histories, err)
	}
	outbox, err := database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox = %#v err=%v", outbox, err)
	}
	var attempts int
	if err := database.Get(&attempts, "SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?", item.ID); err != nil || attempts != 0 {
		t.Fatalf("checkout attempts = %d err=%v", attempts, err)
	}
}

func TestCommitPurchaseSuccessRejectsDifferentSuccessfulOrder(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	item := types.QueueItem{ID: "task-order-conflict-commit", AccountID: "account-1", PlanCode: "24sk10", Datacenter: "gra", Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-order-conflict-commit"); err != nil {
		t.Fatal(err)
	}
	original := types.PurchaseHistoryEntry{ID: "history-original-commit", TaskID: item.ID, AccountID: item.AccountID, PlanCode: item.PlanCode, Datacenter: item.Datacenter, Status: "success", OrderID: "order-original-commit", PurchaseTime: types.NowISO()}
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{original}); err != nil {
		t.Fatal(err)
	}
	conflicting := original
	conflicting.ID = "history-conflicting-commit"
	conflicting.OrderID = "order-conflicting-commit"
	err = database.CommitPurchaseSuccessWithNotification(conflicting, nil)
	if !errors.Is(err, ErrPurchaseOrderConflict) {
		t.Fatalf("error = %v, want ErrPurchaseOrderConflict", err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 || histories[0].OrderID != original.OrderID {
		t.Fatalf("original history was overwritten: %#v err=%v", histories, err)
	}
	var attempts int
	if err := database.Get(&attempts, "SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?", item.ID); err != nil || attempts != 1 {
		t.Fatalf("checkout attempt was removed after conflict: %d err=%v", attempts, err)
	}
}

func TestCommitPurchaseSuccessRollsBackWhenNotificationFails(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	item := types.QueueItem{ID: "task-rollback", PlanCode: "24sk10", Datacenter: "gra", Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-rollback"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TRIGGER reject_purchase_notification BEFORE INSERT ON notification_outbox BEGIN SELECT RAISE(ABORT, 'forced outbox failure'); END"); err != nil {
		t.Fatal(err)
	}
	history := types.PurchaseHistoryEntry{ID: "history-rollback", TaskID: item.ID, PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: []string{}, Status: "success", PurchaseTime: types.NowISO()}
	notification := testNotification("purchase_success:"+item.ID, "telegram")
	if err := database.CommitPurchaseSuccessWithNotification(history, &notification); err == nil {
		t.Fatal("expected forced transaction failure")
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 1 {
		t.Fatalf("queue changed after rollback: %#v err=%v", queue, err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 0 {
		t.Fatalf("history changed after rollback: %#v err=%v", histories, err)
	}
	var attempts int
	if err := database.Get(&attempts, "SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?", item.ID); err != nil || attempts != 1 {
		t.Fatalf("checkout attempt changed after rollback: %d err=%v", attempts, err)
	}
}

func TestCompletedCheckoutRecoversAfterSuccessTransactionFailure(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	item := types.QueueItem{
		ID: "task-recover-after-commit-failure", AccountID: "account-1",
		PlanCode: "24sk10", Datacenter: "gra", Options: []string{"ram-1"},
		Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-recover-after-commit-failure"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-recover-after-commit-failure", "https://example.invalid/recovered"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TRIGGER reject_success_outbox BEFORE INSERT ON notification_outbox BEGIN SELECT RAISE(ABORT, 'forced success transaction failure'); END"); err != nil {
		t.Fatal(err)
	}
	history := types.PurchaseHistoryEntry{
		ID: "history-recover-after-commit-failure", TaskID: item.ID, AccountID: item.AccountID,
		PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: item.Options,
		Status: "success", OrderID: "order-recover-after-commit-failure", PurchaseTime: types.NowISO(),
	}
	notification := testNotification("purchase_success:"+item.ID, "telegram")
	if err := database.CommitPurchaseSuccessWithNotification(history, &notification); err == nil {
		t.Fatal("expected forced success transaction failure")
	}
	var orderID string
	if err := database.Get(&orderID, "SELECT order_id FROM checkout_attempts WHERE task_id = ?", item.ID); err != nil {
		t.Fatal(err)
	}
	if orderID != "order-recover-after-commit-failure" {
		t.Fatalf("completed checkout attempt was lost: %q", orderID)
	}
	if _, err := database.Exec("DROP TRIGGER reject_success_outbox"); err != nil {
		t.Fatal(err)
	}
	recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"})
	if err != nil || recovered != 1 || quarantined != 0 {
		t.Fatalf("recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("recovered task remained runnable: %#v err=%v", queue, err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 || histories[0].OrderID != orderID || histories[0].Status != "success" {
		t.Fatalf("recovered history=%#v err=%v", histories, err)
	}
	outbox, err := database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].EventKey != "purchase_success:"+item.ID {
		t.Fatalf("recovered outbox=%#v err=%v", outbox, err)
	}
}

func TestRecoverCheckoutAttemptsUsesProvidedNotificationChannels(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	item := types.QueueItem{ID: "task-recover", AccountID: "account-1", PlanCode: "26sk10", Datacenter: "sbg", Options: []string{"disk-1"}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-recover"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-recover", "https://example.invalid/recover"); err != nil {
		t.Fatal(err)
	}
	recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"})
	if err != nil || recovered != 1 || quarantined != 0 {
		t.Fatalf("RecoverCheckoutAttempts() recovered=%d quarantined=%d err=%v", recovered, quarantined, err)
	}
	outbox, err := database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 || !reflect.DeepEqual(outbox[0].Channels, []string{"telegram"}) {
		t.Fatalf("recovered outbox = %#v err=%v", outbox, err)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("recovered queue = %#v err=%v", queue, err)
	}
}

func TestRecoverCheckoutAttemptsDefersNotificationWithoutChannels(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	item := types.QueueItem{ID: "task-recover-deferred", PlanCode: "24sk10", Datacenter: "gra", Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-recover-deferred"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-recover-deferred", ""); err != nil {
		t.Fatal(err)
	}
	if recovered, quarantined, err := database.RecoverCheckoutAttempts(nil); err != nil || recovered != 1 || quarantined != 0 {
		t.Fatalf("RecoverCheckoutAttempts() recovered=%d quarantined=%d err=%v", recovered, quarantined, err)
	}
	outbox, err := database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 || !outbox[0].AwaitingChannels || len(outbox[0].Channels) != 0 {
		t.Fatalf("deferred outbox = %#v err=%v", outbox, err)
	}
	assigned, err := database.AssignNotificationChannels(outbox[0].ID, []string{"telegram", "telegram"})
	if err != nil || !assigned {
		t.Fatalf("AssignNotificationChannels() assigned=%v err=%v", assigned, err)
	}
	outbox, err = database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].AwaitingChannels || !reflect.DeepEqual(outbox[0].Channels, []string{"telegram"}) {
		t.Fatalf("assigned outbox = %#v err=%v", outbox, err)
	}
}

func TestRecoverCheckoutAttemptsRemovesQueueBeforeDeletingCompletedAttempt(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer database.Close()
	item := types.QueueItem{ID: "task-recover-queue", AccountID: "account-1", PlanCode: "24sk10", Datacenter: "gra", Options: []string{"ram-1"}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil { t.Fatal(err) }
	if err := database.RecordCheckoutAttempt(item, "cart-recover-queue"); err != nil { t.Fatal(err) }
	if err := database.CompleteCheckoutAttempt(item.ID, "order-recover-queue", ""); err != nil { t.Fatal(err) }
	if recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"}); err != nil || recovered != 1 || quarantined != 0 { t.Fatalf("recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err) }
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 { t.Fatalf("completed checkout remained runnable: queue=%#v err=%v", queue, err) }
	var attempts int
	if err := database.Get(&attempts, `SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?`, item.ID); err != nil || attempts != 0 { t.Fatalf("checkout attempt count=%d err=%v", attempts, err) }
}

func TestRecoverCheckoutAttemptsRecordsUncertainHistoryWithoutOrderID(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer database.Close()
	item := types.QueueItem{ID: "task-recover-uncertain", AccountID: "account-1", PlanCode: "24sk10", Datacenter: "gra", Options: []string{"ram-1"}, Status: "running", RetryCount: 3, CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil { t.Fatal(err) }
	failedMessage := "old failure"
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{{ID: "history-old-failure", TaskID: item.ID, AccountID: item.AccountID, PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: item.Options, Status: "failed", ErrorMessage: &failedMessage, PurchaseTime: types.NowISO()}}); err != nil { t.Fatal(err) }
	if err := database.RecordCheckoutAttempt(item, "cart-recover-uncertain"); err != nil { t.Fatal(err) }
	if recovered, quarantined, err := database.RecoverCheckoutAttempts(nil); err != nil || recovered != 0 || quarantined != 1 {
		t.Fatalf("recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 { t.Fatalf("histories=%#v err=%v", histories, err) }
	if histories[0].Status != "uncertain" || histories[0].TaskID != item.ID || histories[0].AttemptCount != item.RetryCount || histories[0].ErrorMessage == nil {
		t.Fatalf("uncertain history = %#v", histories[0])
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 { t.Fatalf("uncertain checkout remained runnable: %#v err=%v", queue, err) }
	var attempts int
	if err := database.Get(&attempts, `SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?`, item.ID); err != nil || attempts != 1 { t.Fatalf("attempt count=%d err=%v", attempts, err) }
}

func TestRecoverCheckoutAttemptsIsIdempotentWithoutOrderID(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	item := types.QueueItem{
		ID: "task-recover-uncertain-idempotent", AccountID: "account-1",
		PlanCode: "24sk10", Datacenter: "gra", Options: []string{"ram-1"},
		Status: "running", RetryCount: 2, CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-recover-uncertain-idempotent"); err != nil {
		t.Fatal(err)
	}

	for recovery := 1; recovery <= 2; recovery++ {
		recovered, quarantined, err := database.RecoverCheckoutAttempts(nil)
		if err != nil || recovered != 0 || quarantined != 1 {
			t.Fatalf("recovery %d recovered=%d quarantined=%d err=%v", recovery, recovered, quarantined, err)
		}
		histories, err := database.ListHistory()
		if err != nil || len(histories) != 1 {
			t.Fatalf("recovery %d histories=%#v err=%v", recovery, histories, err)
		}
		if histories[0].TaskID != item.ID || histories[0].Status != "uncertain" {
			t.Fatalf("recovery %d history=%#v", recovery, histories[0])
		}
	}

	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("uncertain checkout remained runnable: %#v err=%v", queue, err)
	}
	var attempts int
	if err := database.Get(&attempts, `SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?`, item.ID); err != nil || attempts != 1 {
		t.Fatalf("attempt count=%d err=%v", attempts, err)
	}
}

func TestRecoverCheckoutAttemptsWithoutOrderIDPreservesSuccessHistory(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	item := types.QueueItem{
		ID: "task-recover-uncertain-with-success", AccountID: "account-1",
		PlanCode: "26sk10", Datacenter: "sbg", Options: []string{"disk-1"},
		Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	history := types.PurchaseHistoryEntry{
		ID: "history-existing-success", TaskID: item.ID, AccountID: item.AccountID,
		PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: item.Options,
		Status: "success", OrderID: "order-existing-success", PurchaseTime: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{history}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-recover-uncertain-with-success"); err != nil {
		t.Fatal(err)
	}

	recovered, quarantined, err := database.RecoverCheckoutAttempts(nil)
	if err != nil || recovered != 0 || quarantined != 1 {
		t.Fatalf("recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 {
		t.Fatalf("histories=%#v err=%v", histories, err)
	}
	if histories[0].ID != history.ID || histories[0].Status != "success" || histories[0].OrderID != history.OrderID {
		t.Fatalf("success history was overwritten: %#v", histories[0])
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("uncertain checkout remained runnable: %#v err=%v", queue, err)
	}
	var attempts int
	if err := database.Get(&attempts, `SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?`, item.ID); err != nil || attempts != 1 {
		t.Fatalf("attempt count=%d err=%v", attempts, err)
	}
}

func TestRecoverCheckoutAttemptsIsIdempotentForExistingSameOrder(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer database.Close()
	item := types.QueueItem{ID: "task-recover-idempotent", AccountID: "account-1", PlanCode: "24sk10", Datacenter: "sbg", Options: []string{"disk-1"}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil { t.Fatal(err) }
	history := types.PurchaseHistoryEntry{ID: "history-existing-order", TaskID: item.ID, AccountID: item.AccountID, PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: item.Options, Status: "success", OrderID: "order-same", PurchaseTime: types.NowISO()}
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{history}); err != nil { t.Fatal(err) }
	if err := database.RecordCheckoutAttempt(item, "cart-same"); err != nil { t.Fatal(err) }
	if err := database.CompleteCheckoutAttempt(item.ID, "order-same", "https://example.invalid/same"); err != nil { t.Fatal(err) }
	if recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"}); err != nil || recovered != 1 || quarantined != 0 { t.Fatalf("first recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err) }
	if recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"}); err != nil || recovered != 0 || quarantined != 0 { t.Fatalf("second recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err) }
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 || histories[0].ID != history.ID || histories[0].OrderID != "order-same" { t.Fatalf("history changed during idempotent recovery: %#v err=%v", histories, err) }
	outbox, err := database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].EventKey != "purchase_success:"+item.ID { t.Fatalf("outbox after repeated recovery: %#v err=%v", outbox, err) }
}

func TestRecoverCheckoutAttemptsKeepsDifferentSuccessfulOrderIsolated(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer database.Close()
	item := types.QueueItem{ID: "task-order-conflict", PlanCode: "24sk10", Datacenter: "gra", Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil { t.Fatal(err) }
	history := types.PurchaseHistoryEntry{ID: "history-original", TaskID: item.ID, PlanCode: item.PlanCode, Datacenter: item.Datacenter, Options: []string{}, Status: "success", OrderID: "order-original", PurchaseTime: types.NowISO()}
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{history}); err != nil { t.Fatal(err) }
	if err := database.RecordCheckoutAttempt(item, "cart-conflict"); err != nil { t.Fatal(err) }
	if err := database.CompleteCheckoutAttempt(item.ID, "order-conflict", ""); err != nil { t.Fatal(err) }
	if recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"}); err != nil || recovered != 0 || quarantined != 1 { t.Fatalf("recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err) }
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 1 || histories[0].OrderID != "order-original" { t.Fatalf("conflicting order overwrote history: %#v err=%v", histories, err) }
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 { t.Fatalf("conflicting checkout remained runnable: %#v err=%v", queue, err) }
	var attempts int
	if err := database.Get(&attempts, `SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?`, item.ID); err != nil || attempts != 1 { t.Fatalf("conflicting attempt count=%d err=%v", attempts, err) }
}

func TestRecoverCheckoutAttemptsSkipsCorruptOptionsWithoutBlockingOthers(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer database.Close()
	items := []types.QueueItem{
		{ID: "task-corrupt-options", PlanCode: "24sk10", Datacenter: "gra", Options: []string{"ram-1"}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()},
		{ID: "task-valid-options", PlanCode: "26sk10", Datacenter: "sbg", Options: []string{"disk-1"}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO()},
	}
	if err := database.ReplaceQueue(items); err != nil { t.Fatal(err) }
	for i, item := range items {
		if err := database.RecordCheckoutAttempt(item, "cart-"+item.ID); err != nil { t.Fatal(err) }
		if err := database.CompleteCheckoutAttempt(item.ID, fmt.Sprintf("order-%d", i), ""); err != nil { t.Fatal(err) }
	}
	if _, err := database.Exec(`UPDATE checkout_attempts SET options = 'not-json' WHERE task_id = ?`, items[0].ID); err != nil { t.Fatal(err) }
	if recovered, quarantined, err := database.RecoverCheckoutAttempts([]string{"telegram"}); err != nil || recovered != 2 || quarantined != 0 { t.Fatalf("recovery recovered=%d quarantined=%d err=%v", recovered, quarantined, err) }
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 2 { t.Fatalf("histories=%#v err=%v", histories, err) }
	for _, history := range histories {
		if history.TaskID == items[0].ID && len(history.Options) != 0 { t.Fatalf("corrupt options were not recovered conservatively: %#v", history.Options) }
	}
}

func TestDeleteAwaitingNotificationDoesNotDeleteAssignedEntry(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.EnqueueNotification(testNotification("event-assigned", "telegram")); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 {
		t.Fatalf("outbox = %#v err=%v", items, err)
	}
	deleted, err := database.DeleteAwaitingNotification(items[0].ID)
	if err != nil || deleted {
		t.Fatalf("DeleteAwaitingNotification() deleted=%v err=%v, want no match", deleted, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].Channels, []string{"telegram"}) {
		t.Fatalf("assigned outbox was changed: %#v err=%v", items, err)
	}
}

func TestUpdateNotificationChannelsDoesNotDeleteAwaitingEntry(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	entry := testNotification("event-awaiting")
	entry.AwaitingChannels = true
	if err := database.EnqueueNotification(entry); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 || !items[0].AwaitingChannels {
		t.Fatalf("outbox = %#v err=%v", items, err)
	}
	updated, err := database.UpdateNotificationChannels(items[0].ID, nil, nil)
	if err != nil || updated {
		t.Fatalf("UpdateNotificationChannels() updated=%v err=%v, want no match", updated, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 || !items[0].AwaitingChannels {
		t.Fatalf("awaiting outbox was changed: %#v err=%v", items, err)
	}
}

func TestAssignNotificationChannelsOnlyOnce(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	entry := testNotification("event-assign-once")
	entry.AwaitingChannels = true
	if err := database.EnqueueNotification(entry); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 {
		t.Fatalf("outbox = %#v err=%v", items, err)
	}
	assigned, err := database.AssignNotificationChannels(items[0].ID, []string{"telegram", "feishu"})
	if err != nil || !assigned {
		t.Fatalf("first assignment assigned=%v err=%v", assigned, err)
	}
	assigned, err = database.AssignNotificationChannels(items[0].ID, []string{"weixin"})
	if err != nil || assigned {
		t.Fatalf("second assignment assigned=%v err=%v, want no match", assigned, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 || items[0].AwaitingChannels ||
		!reflect.DeepEqual(items[0].Channels, []string{"feishu", "telegram"}) {
		t.Fatalf("assigned outbox = %#v err=%v", items, err)
	}
}

func TestNotificationOutboxReturnsCorruptChannelsWithoutBlockingLaterEntries(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.EnqueueNotification(testNotification("event-corrupt", "telegram")); err != nil {
		t.Fatal(err)
	}
	if err := database.EnqueueNotification(testNotification("event-healthy", "feishu")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE notification_outbox SET channels = 'not-json' WHERE event_key = 'event-corrupt'`); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v, want both entries", items, err)
	}
	if items[0].EventKey != "event-corrupt" || items[0].DecodeError == "" {
		t.Fatalf("corrupt entry = %#v, want decode error", items[0])
	}
	if items[1].EventKey != "event-healthy" || items[1].DecodeError != "" || !reflect.DeepEqual(items[1].Channels, []string{"feishu"}) {
		t.Fatalf("healthy entry = %#v", items[1])
	}
}

func TestQuarantineNotificationPreservesOriginalAndRemovesOutboxEntry(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.EnqueueNotification(testNotification("event-dead", "telegram")); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	deadID := items[0].ID
	ok, err := database.QuarantineNotification(deadID, "invalid payload")
	if err != nil || !ok {
		t.Fatalf("QuarantineNotification() ok=%v err=%v", ok, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 0 {
		t.Fatalf("outbox=%#v err=%v, want empty", items, err)
	}
	var eventKey, payload, channels, reason string
	if err := database.QueryRowx(`SELECT event_key, payload, channels, error FROM notification_dead_letters WHERE id = ?`, deadID).Scan(&eventKey, &payload, &channels, &reason); err != nil {
		t.Fatal(err)
	}
	if eventKey != "event-dead" || payload != `{"ok":true}` || channels != `["telegram"]` || reason != "invalid payload" {
		t.Fatalf("dead letter = event=%q payload=%q channels=%q reason=%q", eventKey, payload, channels, reason)
	}
}

func TestQuarantineNotificationReplacesExistingDeadLetterAndStillRemovesOutbox(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.EnqueueNotification(testNotification("event-dead-retry", "telegram")); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	deadID := items[0].ID
	if _, err := database.Exec(`
		INSERT INTO notification_dead_letters(
			id, event_key, kind, payload, channels, awaiting_channels, error, created_at, updated_at, failed_at
		) VALUES (?, 'stale-event', 'stale-kind', '{}', '[]', 0, 'old error', ?, ?, ?)
	`, deadID, types.NowISO(), types.NowISO(), types.NowISO()); err != nil {
		t.Fatal(err)
	}

	ok, err := database.QuarantineNotification(deadID, "new error")
	if err != nil || !ok {
		t.Fatalf("QuarantineNotification() ok=%v err=%v", ok, err)
	}
	items, err = database.ListNotificationOutbox(10)
	if err != nil || len(items) != 0 {
		t.Fatalf("outbox=%#v err=%v, want empty", items, err)
	}
	var eventKey, channels, reason string
	if err := database.QueryRowx(`SELECT event_key, channels, error FROM notification_dead_letters WHERE id = ?`, deadID).
		Scan(&eventKey, &channels, &reason); err != nil {
		t.Fatal(err)
	}
	if eventKey != "event-dead-retry" || channels != `["telegram"]` || reason != "new error" {
		t.Fatalf("dead letter = event=%q channels=%q reason=%q", eventKey, channels, reason)
	}
}
