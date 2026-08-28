package db

import (
	"errors"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func testAccount(id, createdAt string, isDefault bool) types.OVHAccount {
	return types.OVHAccount{
		ID: id, Name: id, Endpoint: "ovh-eu", Zone: "IE", AppKey: "app-key",
		AppSecret: "app-secret", ConsumerKey: "consumer-key", IAM: "go-ovh-ie",
		IsDefault: isDefault, CreatedAt: createdAt,
	}
}

func TestDeleteAccountRejectsUnresolvedCheckoutAndRollsBack(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	account := testAccount("account-a", "2026-01-01T00:00:00Z", true)
	if err := database.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	queue := []types.QueueItem{{
		ID: "task-a", AccountID: account.ID, PlanCode: "24sk10", Datacenter: "gra",
		Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}}
	if err := database.ReplaceQueue(queue); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{{
		ID: "history-a", TaskID: "old-task", AccountID: account.ID, PlanCode: "24sk10",
		Datacenter: "gra", Options: []string{}, Status: "failed", PurchaseTime: types.NowISO(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertMonitorSubscription(types.Subscription{
		PlanCode: "24sk10", Datacenters: []string{"gra"}, LastStatus: map[string]string{},
		History: []types.SubscriptionHistoryEntry{}, CreatedAt: types.NowISO(), AutoOrder: true,
		AutoOrderAccountID: account.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordCheckoutAttempt(queue[0], "cart-a"); err != nil {
		t.Fatal(err)
	}

	err = database.DeleteAccount(account.ID)
	if !errors.Is(err, ErrUnresolvedCheckoutAttempts) {
		t.Fatalf("DeleteAccount() error = %v, want ErrUnresolvedCheckoutAttempts", err)
	}
	if _, ok, err := database.GetAccount(account.ID); err != nil || !ok {
		t.Fatalf("account was partially deleted: ok=%v err=%v", ok, err)
	}
	if items, err := database.ListQueue(); err != nil || len(items) != 1 {
		t.Fatalf("queue changed after rollback: len=%d err=%v", len(items), err)
	}
	if items, err := database.ListHistory(); err != nil || len(items) != 1 {
		t.Fatalf("history changed after rollback: len=%d err=%v", len(items), err)
	}
	subs, err := database.ListMonitorSubscriptions()
	if err != nil || len(subs) != 1 || subs[0].AutoOrderAccountID != account.ID {
		t.Fatalf("monitor account reference changed after rollback: %#v err=%v", subs, err)
	}
}

func TestDeleteDefaultAccountCascadesAndPromotesOldest(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	first := testAccount("account-first", "2026-01-01T00:00:00Z", false)
	deleted := testAccount("account-default", "2026-01-02T00:00:00Z", true)
	last := testAccount("account-last", "2026-01-03T00:00:00Z", false)
	for _, account := range []types.OVHAccount{first, deleted, last} {
		if err := database.UpsertAccount(account); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.ReplaceQueue([]types.QueueItem{{
		ID: "task-delete", AccountID: deleted.ID, PlanCode: "24sk10", Datacenter: "gra",
		Options: []string{}, Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{{
		ID: "history-delete", TaskID: "old-task", AccountID: deleted.ID, PlanCode: "24sk10",
		Datacenter: "gra", Options: []string{}, Status: "failed", PurchaseTime: types.NowISO(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertMonitorSubscription(types.Subscription{
		PlanCode: "24sk10", Datacenters: []string{"gra"}, LastStatus: map[string]string{},
		History: []types.SubscriptionHistoryEntry{}, CreatedAt: types.NowISO(), AutoOrder: true,
		AutoOrderAccountID: deleted.ID,
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteAccount(deleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := database.GetAccount(deleted.ID); err != nil || ok {
		t.Fatalf("deleted account still exists: ok=%v err=%v", ok, err)
	}
	if items, err := database.ListQueue(); err != nil || len(items) != 0 {
		t.Fatalf("account queue not removed: len=%d err=%v", len(items), err)
	}
	if items, err := database.ListHistory(); err != nil || len(items) != 0 {
		t.Fatalf("account history not removed: len=%d err=%v", len(items), err)
	}
	subscriptions, err := database.ListMonitorSubscriptions()
	if err != nil || len(subscriptions) != 1 || subscriptions[0].AutoOrderAccountID != "" {
		t.Fatalf("monitor account reference not cleared: %#v err=%v", subscriptions, err)
	}
	defaultAccount, ok, err := database.GetDefaultAccount()
	if err != nil || !ok || defaultAccount.ID != first.ID {
		t.Fatalf("default account = %#v ok=%v err=%v, want %s", defaultAccount, ok, err, first.ID)
	}
}

func TestClearDefaultAccountPreservesAccounts(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	account := testAccount("account-default", "2026-01-01T00:00:00Z", true)
	if err := database.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	if err := database.ClearDefaultAccount(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := database.GetDefaultAccount(); err != nil || ok {
		t.Fatalf("default account still exists: ok=%v err=%v", ok, err)
	}
	stored, ok, err := database.GetAccount(account.ID)
	if err != nil || !ok || stored.IsDefault {
		t.Fatalf("account was removed or remained default: %#v ok=%v err=%v", stored, ok, err)
	}
}

func TestDeleteAccountMissingReturnsSentinel(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.DeleteAccount("missing"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("DeleteAccount() error = %v, want ErrAccountNotFound", err)
	}
}
