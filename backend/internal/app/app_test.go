package app

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/types"
)

func TestNotificationOutboxRetryMethodsAreSafeForConcurrentCallers(t *testing.T) {
	state := &State{}
	now := time.Now()
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				id := fmt.Sprintf("event-%d", (worker+iteration)%8)
				state.SetNotificationOutboxRetry(id, now.Add(time.Minute))
				_ = state.NotificationOutboxRetryDue(id, now)
				state.ClearNotificationOutboxRetry(id)
			}
		}()
	}
	workers.Wait()

	state.SetNotificationOutboxRetry("final", now.Add(time.Minute))
	if state.NotificationOutboxRetryDue("final", now) {
		t.Fatal("retry became due before the stored next-attempt time")
	}
	state.ClearNotificationOutboxRetry("final")
	if !state.NotificationOutboxRetryDue("final", now) {
		t.Fatal("cleared retry should be due immediately")
	}
}

func TestAvailableQueueSlotsNeverReturnsNegative(t *testing.T) {
	state := &State{Queue: make([]types.QueueItem, MaxQueueItems+3)}
	if got := state.AvailableQueueSlots(); got != 0 {
		t.Fatalf("AvailableQueueSlots() = %d, want 0", got)
	}
	state.Queue = state.Queue[:MaxQueueItems-7]
	if got := state.AvailableQueueSlots(); got != 7 {
		t.Fatalf("AvailableQueueSlots() = %d, want 7", got)
	}
}

func TestQueueProcessorStartIsSingleOwner(t *testing.T) {
	state := &State{QueueProcessorEnabled: true}
	const workers = 32
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- state.TryStartQueueProcessor()
		}()
	}
	wg.Wait()
	close(results)
	started := 0
	for ok := range results {
		if ok {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("concurrent TryStartQueueProcessor succeeded %d times, want 1", started)
	}
	if state.TryStartQueueProcessor() {
		t.Fatal("processor could be started twice without being released")
	}
	state.SetQueueProcessorRunning(false)
	if !state.TryStartQueueProcessor() {
		t.Fatal("processor could not restart after release")
	}
}

func TestQueueTickStartIsSingleOwner(t *testing.T) {
	state := &State{}
	const workers = 32
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- state.TryStartQueueTick()
		}()
	}
	wg.Wait()
	close(results)
	started := 0
	for ok := range results {
		if ok {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("concurrent TryStartQueueTick succeeded %d times, want 1", started)
	}
	if state.TryStartQueueTick() {
		t.Fatal("queue tick could be re-entered before FinishQueueTick")
	}
	state.FinishQueueTick()
	if !state.TryStartQueueTick() {
		t.Fatal("queue tick could not restart after FinishQueueTick")
	}
}

func TestAccountPurchaseGuardCoversWholePurchaseLifecycle(t *testing.T) {
	state := &State{
		checkoutTasks: make(map[string]string),
		purchaseTasks: make(map[string]string),
	}
	if err := state.BeginAccountPurchase("task-active", "account-a"); err != nil {
		t.Fatalf("begin account purchase: %v", err)
	}
	if err := state.BeginAccountPurchase("task-active", "account-a"); !errors.Is(err, ErrQueueCheckoutInProgress) {
		t.Fatalf("duplicate begin error = %v, want ErrQueueCheckoutInProgress", err)
	}

	mutatedA := false
	err := state.WithAccountCheckoutGuard("account-a", func() error {
		mutatedA = true
		return nil
	})
	if !errors.Is(err, ErrQueueCheckoutInProgress) {
		t.Fatalf("active account guard error = %v, want ErrQueueCheckoutInProgress", err)
	}
	if mutatedA {
		t.Fatal("mutation ran for account with active purchase")
	}

	mutatedB := false
	if err := state.WithAccountCheckoutGuard("account-b", func() error {
		mutatedB = true
		return nil
	}); err != nil {
		t.Fatalf("unrelated account mutation: %v", err)
	}
	if !mutatedB {
		t.Fatal("mutation did not run for unrelated account")
	}

	state.EndAccountPurchase("task-active")
	if err := state.WithAccountCheckoutGuard("account-a", func() error {
		mutatedA = true
		return nil
	}); err != nil {
		t.Fatalf("mutation after purchase ended: %v", err)
	}
	if !mutatedA {
		t.Fatal("mutation did not run after purchase ended")
	}
}

func TestBeginAccountPurchaseRejectsMissingIdentity(t *testing.T) {
	state := &State{}
	for _, test := range []struct {
		taskID    string
		accountID string
	}{
		{taskID: "", accountID: "account-a"},
		{taskID: "task-a", accountID: ""},
	} {
		if err := state.BeginAccountPurchase(test.taskID, test.accountID); !errors.Is(err, ErrQueueItemChanged) {
			t.Fatalf("BeginAccountPurchase(%q, %q) error = %v, want ErrQueueItemChanged", test.taskID, test.accountID, err)
		}
	}
}

func TestBeginCheckoutAttemptPersistsResolvedDefaultAccountForLegacyQueueItem(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	legacy := types.QueueItem{
		ID: "task-legacy-account", PlanCode: "24sk10", Datacenter: "gra",
		Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{legacy}); err != nil {
		t.Fatal(err)
	}
	state := &State{
		DB: database, Queue: []types.QueueItem{legacy},
		checkoutTasks: make(map[string]string), purchaseTasks: make(map[string]string),
	}
	resolved := legacy
	resolved.AccountID = "account-default"
	if err := state.BeginCheckoutAttempt(resolved, "cart-legacy-account"); err != nil {
		t.Fatalf("begin checkout with resolved account: %v", err)
	}
	var accountID string
	if err := database.Get(&accountID, "SELECT account_id FROM checkout_attempts WHERE task_id = ?", resolved.ID); err != nil {
		t.Fatal(err)
	}
	if accountID != resolved.AccountID {
		t.Fatalf("checkout attempt account_id = %q, want %q", accountID, resolved.AccountID)
	}
	mutated := false
	if err := state.WithAccountCheckoutGuard(resolved.AccountID, func() error {
		mutated = true
		return nil
	}); !errors.Is(err, ErrQueueCheckoutInProgress) {
		t.Fatalf("resolved account guard error = %v, want ErrQueueCheckoutInProgress", err)
	}
	if mutated {
		t.Fatal("resolved account mutation ran while checkout was active")
	}
	state.EndCheckoutAttempt(resolved.ID)
}

func TestBeginCheckoutAttemptRejectsChangingExplicitAccount(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	item := types.QueueItem{
		ID: "task-explicit-account", AccountID: "account-a", PlanCode: "24sk10", Datacenter: "gra",
		Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	state := &State{DB: database, Queue: []types.QueueItem{item}, checkoutTasks: make(map[string]string)}
	changed := item
	changed.AccountID = "account-b"
	if err := state.BeginCheckoutAttempt(changed, "cart-explicit-account"); !errors.Is(err, ErrQueueItemChanged) {
		t.Fatalf("changed explicit account error = %v, want ErrQueueItemChanged", err)
	}
}

func TestEnqueueMonitorOrdersPublishesMemoryOnlyAfterTransaction(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account := types.OVHAccount{
		ID: "account-1", Name: "account-1", Endpoint: "ovh-eu", Zone: "IE",
		AppKey: "test-app-key", AppSecret: "test-app-secret", ConsumerKey: "test-consumer-key",
		IAM: "go-ovh-ie", IsDefault: true, CreatedAt: types.NowISO(),
	}
	if err := database.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}

	original := types.Subscription{
		PlanCode: "24sk10", Datacenters: []string{"gra"}, Memories: []string{},
		Storages: []string{}, Networks: []string{}, NotifyAvailable: true,
		LastStatus: map[string]string{"gra|cfg": "available"},
		ConfirmedStatus: map[string]string{"gra|cfg": "available"},
		PendingOrder: map[string]int{"gra|cfg": 1}, PendingNotify: map[string]string{},
		PendingNotifyChannels: map[string][]string{}, CreatedAt: types.NowISO(),
		History: []types.SubscriptionHistoryEntry{},
	}
	if err := database.UpsertMonitorSubscription(original); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		"CREATE TRIGGER reject_app_monitor_queue_insert BEFORE INSERT ON queue BEGIN SELECT RAISE(ABORT, 'forced queue failure'); END",
	); err != nil {
		t.Fatal(err)
	}
	saved := original
	saved.PendingOrder = map[string]int{}
	now := types.NowISO()
	item := types.QueueItem{
		ID: "app-monitor-order", AccountID: "account-1", PlanCode: saved.PlanCode,
		Datacenter: "gra", Options: []string{}, Status: "running", CreatedAt: now,
		UpdatedAt: now, RetryInterval: 2, MaxRetries: 3,
	}
	state := &State{
		DB: database, Queue: []types.QueueItem{}, checkoutTasks: make(map[string]string),
		purchaseTasks: make(map[string]string),
	}

	if err := state.EnqueueMonitorOrders(saved, []types.QueueItem{item}); err == nil {
		t.Fatal("expected forced monitor enqueue failure")
	}
	if len(state.Queue) != 0 {
		t.Fatalf("memory queue changed before transaction commit: %#v", state.Queue)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("database queue after rollback: %#v err=%v", queue, err)
	}
	subscriptions, err := database.ListMonitorSubscriptions()
	if err != nil || len(subscriptions) != 1 || subscriptions[0].PendingOrder["gra|cfg"] != 1 {
		t.Fatalf("subscription after rollback: %#v err=%v", subscriptions, err)
	}

	if _, err := database.Exec("DROP TRIGGER reject_app_monitor_queue_insert"); err != nil {
		t.Fatal(err)
	}
	if err := state.EnqueueMonitorOrders(saved, []types.QueueItem{item}); err != nil {
		t.Fatalf("retry monitor enqueue: %v", err)
	}
	if len(state.Queue) != 1 || state.Queue[0].ID != item.ID {
		t.Fatalf("memory queue after commit: %#v", state.Queue)
	}
	queue, err = database.ListQueue()
	if err != nil || len(queue) != 1 || queue[0].ID != item.ID {
		t.Fatalf("database queue after commit: %#v err=%v", queue, err)
	}
}

func TestMutateQueueForAccountRechecksDatabaseAccount(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account := types.OVHAccount{ID: "account-race", Name: "race", Endpoint: "ovh-eu", Zone: "IE", AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", IAM: "iam", IsDefault: true, CreatedAt: types.NowISO()}
	if err := database.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	state := &State{DB: database, Queue: []types.QueueItem{}, checkoutTasks: make(map[string]string), purchaseTasks: make(map[string]string)}
	if err := database.DeleteAccount(account.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	err = state.MutateQueueForAccount(account.ID, func(queue []types.QueueItem) ([]types.QueueItem, error) {
		called = true
		return append(queue, types.QueueItem{ID: "should-not-publish"}), nil
	})
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("MutateQueueForAccount error = %v, want ErrAccountNotFound", err)
	}
	if called {
		t.Fatal("mutator ran after account was deleted")
	}
	if len(state.Queue) != 0 {
		t.Fatalf("memory queue changed: %#v", state.Queue)
	}
	items, err := database.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("database queue changed: %#v", items)
	}
}

func TestMutateQueueWithHistoryForAccountRechecksDatabaseAccount(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account := types.OVHAccount{ID: "account-history-race", Name: "race", Endpoint: "ovh-eu", Zone: "IE", AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", IAM: "iam", IsDefault: true, CreatedAt: types.NowISO()}
	if err := database.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	state := &State{DB: database, Queue: []types.QueueItem{}, History: []types.PurchaseHistoryEntry{}, checkoutTasks: make(map[string]string), purchaseTasks: make(map[string]string)}
	if err := database.DeleteAccount(account.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	err = state.MutateQueueWithHistoryForAccount(account.ID, func(queue []types.QueueItem, history []types.PurchaseHistoryEntry) ([]types.QueueItem, error) {
		called = true
		return queue, nil
	})
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("MutateQueueWithHistoryForAccount error = %v, want ErrAccountNotFound", err)
	}
	if called {
		t.Fatal("mutator ran after account was deleted")
	}
}

func TestCheckoutAttemptCleanupFailureKeepsPersistentDuplicateGuard(t *testing.T) {
	cases := []struct {
		name    string
		cleanup func(*State, string) error
	}{
		{name: "cancel before request", cleanup: func(state *State, taskID string) error {
			return state.CancelCheckoutAttemptBeforeRequest(taskID)
		}},
		{name: "definitive HTTP error", cleanup: func(state *State, taskID string) error {
			return state.FinishCheckoutHTTPError(taskID)
		}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			database, err := db.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()

			item := types.QueueItem{
				ID: "task-cleanup-failure-" + tt.name, AccountID: "account-1",
				PlanCode: "24sk10", Datacenter: "gra", Options: []string{}, Status: "running",
				CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(), RetryCount: 1,
			}
			if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("CREATE TRIGGER reject_checkout_attempt_delete BEFORE DELETE ON checkout_attempts BEGIN SELECT RAISE(ABORT, 'forced checkout attempt cleanup failure'); END"); err != nil {
				t.Fatal(err)
			}

			state := &State{
				DB: database, Queue: []types.QueueItem{item},
				checkoutTasks: make(map[string]string), purchaseTasks: make(map[string]string),
			}
			if err := state.BeginCheckoutAttempt(item, "cart-cleanup-failure"); err != nil {
				t.Fatalf("begin checkout attempt: %v", err)
			}
			if err := tt.cleanup(state, item.ID); err == nil {
				t.Fatal("cleanup unexpectedly succeeded")
			}

			state.EndCheckoutAttempt(item.ID)
			if err := state.BeginCheckoutAttempt(item, "cart-cleanup-retry"); !errors.Is(err, ErrCheckoutAttemptExists) {
				t.Fatalf("retry begin error = %v, want ErrCheckoutAttemptExists", err)
			}
		})
	}
}

func TestEnqueueMonitorOrdersRejectsDeletedAccountWithoutPublishingState(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	original := types.Subscription{
		PlanCode: "24sk10", Datacenters: []string{"gra"}, Memories: []string{},
		Storages: []string{}, Networks: []string{}, NotifyAvailable: true,
		LastStatus: map[string]string{"gra|cfg": "available"},
		ConfirmedStatus: map[string]string{"gra|cfg": "available"},
		PendingOrder: map[string]int{"gra|cfg": 1}, PendingNotify: map[string]string{},
		PendingNotifyChannels: map[string][]string{}, CreatedAt: types.NowISO(),
		History: []types.SubscriptionHistoryEntry{},
	}
	if err := database.UpsertMonitorSubscription(original); err != nil {
		t.Fatal(err)
	}

	saved := original
	saved.PendingOrder = map[string]int{}
	now := types.NowISO()
	item := types.QueueItem{
		ID: "app-monitor-deleted-account", AccountID: "deleted-account", PlanCode: saved.PlanCode,
		Datacenter: "gra", Options: []string{}, Status: "running", CreatedAt: now,
		UpdatedAt: now, RetryInterval: 2, MaxRetries: 3,
	}
	state := &State{
		DB: database, Queue: []types.QueueItem{}, checkoutTasks: make(map[string]string),
		purchaseTasks: make(map[string]string),
	}

	if err := state.EnqueueMonitorOrders(saved, []types.QueueItem{item}); err == nil {
		t.Fatal("expected missing monitor account to be rejected")
	}
	if len(state.Queue) != 0 {
		t.Fatalf("memory queue changed after account rejection: %#v", state.Queue)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("database queue changed after account rejection: %#v err=%v", queue, err)
	}
	subscriptions, err := database.ListMonitorSubscriptions()
	if err != nil || len(subscriptions) != 1 || subscriptions[0].PendingOrder["gra|cfg"] != 1 {
		t.Fatalf("subscription changed after account rejection: %#v err=%v", subscriptions, err)
	}
}

func TestCommitPurchaseSuccessPublishesMemoryOnlyAfterTransaction(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	item := types.QueueItem{
		ID: "task-atomic-memory", PlanCode: "24sk10", Datacenter: "gra",
		Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(item, "cart-atomic-memory"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCheckoutAttempt(item.ID, "order-atomic-memory", "https://example.invalid/order"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TRIGGER reject_app_purchase_notification BEFORE INSERT ON notification_outbox BEGIN SELECT RAISE(ABORT, 'forced outbox failure'); END"); err != nil {
		t.Fatal(err)
	}

	state := &State{
		DB: database, Queue: []types.QueueItem{item}, History: []types.PurchaseHistoryEntry{},
		checkoutTasks: make(map[string]string),
	}
	history := types.PurchaseHistoryEntry{
		ID: "history-atomic-memory", TaskID: item.ID, PlanCode: item.PlanCode,
		Datacenter: item.Datacenter, Options: []string{}, Status: "success",
		OrderID: "order-atomic-memory", PurchaseTime: types.NowISO(),
	}
	now := types.NowISO()
	notification := &types.NotificationOutboxEntry{
		ID: "notification-atomic-memory", EventKey: "purchase_success:" + item.ID,
		Kind: "purchase_success", Payload: "{}", Channels: []string{"telegram"},
		CreatedAt: now, UpdatedAt: now,
	}

	if err := state.CommitPurchaseSuccessDuringCheckoutWithNotification(history, notification); err == nil {
		t.Fatal("expected forced transaction failure")
	}
	if len(state.Queue) != 1 || state.Queue[0].ID != item.ID {
		t.Fatalf("memory queue changed before commit: %#v", state.Queue)
	}
	if len(state.History) != 0 {
		t.Fatalf("memory history changed before commit: %#v", state.History)
	}
	if _, isolated := state.DeletedTaskIDs[item.ID]; !isolated {
		t.Fatal("checked-out task was not isolated after transaction failure")
	}
	if state.IsQueueItemRunning(item.ID) {
		t.Fatal("isolated task must not be considered runnable")
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 1 || queue[0].ID != item.ID {
		t.Fatalf("database queue changed after rollback: %#v err=%v", queue, err)
	}
	histories, err := database.ListHistory()
	if err != nil || len(histories) != 0 {
		t.Fatalf("database history changed after rollback: %#v err=%v", histories, err)
	}

	if _, err := database.Exec("DROP TRIGGER reject_app_purchase_notification"); err != nil {
		t.Fatal(err)
	}
	if err := state.CommitPurchaseSuccessDuringCheckoutWithNotification(history, notification); err != nil {
		t.Fatalf("retry commit: %v", err)
	}
	if len(state.Queue) != 0 || len(state.History) != 1 || state.History[0].TaskID != item.ID {
		t.Fatalf("memory was not published after commit: queue=%#v history=%#v", state.Queue, state.History)
	}
	if _, isolated := state.DeletedTaskIDs[item.ID]; isolated {
		t.Fatal("successful retry did not clear isolation marker")
	}
	queue, err = database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("database queue after retry: %#v err=%v", queue, err)
	}
	histories, err = database.ListHistory()
	if err != nil || len(histories) != 1 || histories[0].TaskID != item.ID {
		t.Fatalf("database history after retry: %#v err=%v", histories, err)
	}
	outbox, err := database.ListNotificationOutbox(10)
	if err != nil || len(outbox) != 1 || outbox[0].EventKey != notification.EventKey {
		t.Fatalf("database outbox after retry: %#v err=%v", outbox, err)
	}
	var attempts int
	if err := database.Get(&attempts, "SELECT COUNT(1) FROM checkout_attempts WHERE task_id = ?", item.ID); err != nil || attempts != 0 {
		t.Fatalf("checkout attempts after retry: %d err=%v", attempts, err)
	}
}

func TestQuarantineQueueItemInitializesIsolationMap(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	item := types.QueueItem{
		ID: "task-quarantine-nil-map", PlanCode: "24sk10", Datacenter: "gra",
		Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}
	if err := database.ReplaceQueue([]types.QueueItem{item}); err != nil {
		t.Fatal(err)
	}
	state := &State{DB: database, Queue: []types.QueueItem{item}}

	if err := state.QuarantineQueueItem(item.ID); err != nil {
		t.Fatalf("quarantine queue item: %v", err)
	}
	if _, isolated := state.DeletedTaskIDs[item.ID]; !isolated {
		t.Fatal("quarantined task was not added to an initialized isolation map")
	}
	if len(state.Queue) != 0 {
		t.Fatalf("memory queue after quarantine: %#v", state.Queue)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 0 {
		t.Fatalf("database queue after quarantine: %#v err=%v", queue, err)
	}
}

func TestCountPurchaseExcludesUncertainHistory(t *testing.T) {
	state := &State{
		History: []types.PurchaseHistoryEntry{
			{ID: "history-success", Status: "success"},
			{ID: "history-failed", Status: "failed"},
			{ID: "history-uncertain", Status: "uncertain"},
		},
	}

	success, failed := state.CountPurchase()
	if success != 1 || failed != 1 {
		t.Fatalf("CountPurchase() success=%d failed=%d, want 1 and 1", success, failed)
	}
}
