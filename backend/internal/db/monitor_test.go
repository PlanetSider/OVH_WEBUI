package db

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func monitorRowJSON(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func legacyMonitorRow(lastStatus, confirmedStatus string) monitorSubRow {
	return monitorSubRow{
		PlanCode: "24sk10", DatacentersJSON: monitorRowJSON(`[]`),
		MemoriesJSON: monitorRowJSON(`[]`), StoragesJSON: monitorRowJSON(`[]`),
		NetworksJSON: monitorRowJSON(`[]`), LastStatusJSON: monitorRowJSON(lastStatus),
		ConfirmedStatusJSON: monitorRowJSON(confirmedStatus), PendingOrderJSON: monitorRowJSON(`{}`),
		PendingNotifyJSON: monitorRowJSON(`{}`), PendingNotifyChannelsJSON: monitorRowJSON(`{}`),
		HistoryJSON: monitorRowJSON(`[]`),
	}
}

func TestRowToMonitorSubMigratesLegacyConfirmedStatus(t *testing.T) {
	row := legacyMonitorRow(
		`{"gra|cfg":"unavailable","sbg|cfg":"available","bhs|cfg":"price_check_failed","waw|cfg":"unknown"}`,
		`{}`,
	)

	sub, err := rowToMonitorSub(row)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"gra|cfg": "unavailable", "sbg|cfg": "available"}
	if !reflect.DeepEqual(sub.ConfirmedStatus, want) {
		t.Fatalf("confirmed status = %#v, want %#v", sub.ConfirmedStatus, want)
	}
}

func TestRowToMonitorSubDoesNotOverwriteNonEmptyConfirmedStatus(t *testing.T) {
	row := legacyMonitorRow(
		`{"gra|cfg":"unavailable","sbg|cfg":"unavailable"}`,
		`{"gra|cfg":"available"}`,
	)

	sub, err := rowToMonitorSub(row)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"gra|cfg": "available"}
	if !reflect.DeepEqual(sub.ConfirmedStatus, want) {
		t.Fatalf("confirmed status = %#v, want existing value %#v", sub.ConfirmedStatus, want)
	}
}

func TestDecodePendingOrderSupportsCurrentAndLegacyFormats(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  map[string]int
	}{
		{name: "current counts", value: `{"gra|cfg":3,"sbg|cfg":1}`, want: map[string]int{"gra|cfg": 3, "sbg|cfg": 1}},
		{name: "legacy booleans", value: `{"gra|cfg":true,"sbg|cfg":false}`, want: map[string]int{"gra|cfg": 1}},
		{name: "discard non-positive counts", value: `{"gra|cfg":0,"sbg|cfg":-2}`, want: map[string]int{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodePendingOrder(monitorRowJSON(test.value), "24sk10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("pending order = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestDecodePendingOrderRejectsInvalidEntry(t *testing.T) {
	if _, err := decodePendingOrder(monitorRowJSON(`{"gra|cfg":"yes"}`), "24sk10"); err == nil {
		t.Fatal("expected invalid pending order entry to fail")
	}
}

func monitorSubscription(planCode string) types.Subscription {
	return types.Subscription{
		PlanCode: planCode, Datacenters: []string{"gra"}, Memories: []string{},
		Storages: []string{}, Networks: []string{}, NotifyAvailable: true,
		LastStatus: map[string]string{"gra|default": "unavailable"},
		ConfirmedStatus: map[string]string{"gra|default": "unavailable"},
		PendingOrder: map[string]int{}, PendingNotify: map[string]string{},
		CreatedAt: types.NowISO(), History: []types.SubscriptionHistoryEntry{},
	}
}

func monitorQueueItem(id, planCode, datacenter string) types.QueueItem {
	now := types.NowISO()
	return types.QueueItem{
		ID: id, AccountID: "account-1", PlanCode: planCode, Datacenter: datacenter,
		Options: []string{}, Status: "running", CreatedAt: now, UpdatedAt: now,
		RetryInterval: 2, MaxRetries: 3, QuickOrder: true, Priority: 100,
	}
}

func TestEnqueueMonitorOrdersAndSaveSubscriptionCommitsTogether(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	original := monitorSubscription("24sk10")
	original.PendingOrder = map[string]int{"gra|cfg": 2, "sbg|cfg": 1}
	if err := database.UpsertMonitorSubscription(original); err != nil {
		t.Fatal(err)
	}
	saved := original
	saved.PendingOrder = map[string]int{"sbg|cfg": 1}
	items := []types.QueueItem{
		monitorQueueItem("monitor-order-1", saved.PlanCode, "gra"),
		monitorQueueItem("monitor-order-2", saved.PlanCode, "gra"),
	}

	if err := database.EnqueueMonitorOrdersAndSaveSubscription(saved, items, 200); err != nil {
		t.Fatalf("enqueue monitor orders: %v", err)
	}
	queue, err := database.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 {
		t.Fatalf("queue length = %d, want 2", len(queue))
	}
	subscriptions, err := database.ListMonitorSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 1 || !reflect.DeepEqual(subscriptions[0].PendingOrder, saved.PendingOrder) {
		t.Fatalf("saved pending orders = %#v, want %#v", subscriptions, saved.PendingOrder)
	}
}

func TestEnqueueMonitorOrdersAndSaveSubscriptionRollsBackTogether(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	original := monitorSubscription("24sk10")
	original.PendingOrder = map[string]int{"gra|cfg": 1, "sbg|cfg": 1}
	if err := database.UpsertMonitorSubscription(original); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		"CREATE TRIGGER reject_monitor_queue_insert BEFORE INSERT ON queue BEGIN SELECT RAISE(ABORT, 'forced queue failure'); END",
	); err != nil {
		t.Fatal(err)
	}
	saved := original
	saved.PendingOrder = map[string]int{"sbg|cfg": 1}

	err = database.EnqueueMonitorOrdersAndSaveSubscription(saved, []types.QueueItem{
		monitorQueueItem("monitor-order-fail", saved.PlanCode, "gra"),
	}, 200)
	if err == nil {
		t.Fatal("expected forced queue transaction failure")
	}
	queue, listErr := database.ListQueue()
	if listErr != nil || len(queue) != 0 {
		t.Fatalf("queue after rollback = %#v, err=%v", queue, listErr)
	}
	subscriptions, listErr := database.ListMonitorSubscriptions()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(subscriptions) != 1 || !reflect.DeepEqual(subscriptions[0].PendingOrder, original.PendingOrder) {
		t.Fatalf("pending orders after rollback = %#v, want %#v", subscriptions, original.PendingOrder)
	}
}

func TestEnqueueMonitorOrdersAndSaveSubscriptionCapacityFailureDoesNotChangeState(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	original := monitorSubscription("24sk10")
	original.PendingOrder = map[string]int{"gra|cfg": 1}
	if err := database.UpsertMonitorSubscription(original); err != nil {
		t.Fatal(err)
	}
	existing := monitorQueueItem("existing-order", original.PlanCode, "sbg")
	if err := database.ReplaceQueue([]types.QueueItem{existing}); err != nil {
		t.Fatal(err)
	}
	saved := original
	saved.PendingOrder = map[string]int{}

	if err := database.EnqueueMonitorOrdersAndSaveSubscription(saved, []types.QueueItem{
		monitorQueueItem("over-capacity-order", saved.PlanCode, "gra"),
	}, 1); err == nil {
		t.Fatal("expected queue capacity failure")
	}
	queue, listErr := database.ListQueue()
	if listErr != nil || len(queue) != 1 || queue[0].ID != existing.ID {
		t.Fatalf("queue after capacity failure = %#v, err=%v", queue, listErr)
	}
	subscriptions, listErr := database.ListMonitorSubscriptions()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(subscriptions) != 1 || !reflect.DeepEqual(subscriptions[0].PendingOrder, original.PendingOrder) {
		t.Fatalf("pending orders after capacity failure = %#v, want %#v", subscriptions, original.PendingOrder)
	}
}

func TestReplaceMonitorSubscriptionsAndKnownServers(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.ReplaceMonitorSubscriptionsAndKnownServers(
		[]types.Subscription{monitorSubscription("24sk10")}, []string{"24sk10", "24sk20"},
	); err != nil {
		t.Fatal(err)
	}
	assertMonitorState(t, database, []string{"24sk10"}, []string{"24sk10", "24sk20"})

	if err := database.ReplaceMonitorSubscriptionsAndKnownServers(
		[]types.Subscription{monitorSubscription("26sk10")}, []string{"26sk10"},
	); err != nil {
		t.Fatal(err)
	}
	assertMonitorState(t, database, []string{"26sk10"}, []string{"26sk10"})
}

func TestReplaceMonitorSubscriptionsAndKnownServersRollsBack(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.ReplaceMonitorSubscriptionsAndKnownServers(
		[]types.Subscription{monitorSubscription("24sk10")}, []string{"24sk10"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER reject_monitor_known_servers_update
		BEFORE UPDATE OF value ON kv
		WHEN OLD.key = 'monitor_known_servers'
		BEGIN SELECT RAISE(ABORT, 'forced kv failure'); END
	`); err != nil {
		t.Fatal(err)
	}

	err = database.ReplaceMonitorSubscriptionsAndKnownServers(
		[]types.Subscription{monitorSubscription("26sk10")}, []string{"26sk10"},
	)
	if err == nil {
		t.Fatal("expected forced transaction failure")
	}
	assertMonitorState(t, database, []string{"24sk10"}, []string{"24sk10"})
}

func assertMonitorState(t *testing.T, database *DB, wantPlans, wantKnown []string) {
	t.Helper()
	subscriptions, err := database.ListMonitorSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	plans := make([]string, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		plans = append(plans, subscription.PlanCode)
	}
	if !reflect.DeepEqual(plans, wantPlans) {
		t.Fatalf("plans = %#v, want %#v", plans, wantPlans)
	}
	var known []string
	ok, err := database.GetKV("monitor_known_servers", &known)
	if err != nil || !ok {
		t.Fatalf("GetKV() ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(known, wantKnown) {
		t.Fatalf("known servers = %#v, want %#v", known, wantKnown)
	}
}
