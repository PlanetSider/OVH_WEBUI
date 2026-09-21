package db

import (
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestDiscontinuedStateRoundTripsThroughSQLite(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	queueItem := types.QueueItem{
		ID: "queue-discontinued", PlanCode: "24sk502", Datacenter: "gra", Options: []string{},
		Status: "running", CreatedAt: types.NowISO(), UpdatedAt: types.NowISO(),
		RetryInterval: 30, Discontinued: true,
	}
	if err := database.ReplaceQueue([]types.QueueItem{queueItem}); err != nil {
		t.Fatal(err)
	}
	queue, err := database.ListQueue()
	if err != nil || len(queue) != 1 || !queue[0].Discontinued {
		t.Fatalf("queue = %#v err=%v", queue, err)
	}

	subscription := types.Subscription{
		PlanCode: "24sk502", Datacenters: []string{}, Memories: []string{}, Storages: []string{}, Networks: []string{},
		LastStatus: map[string]string{}, ConfirmedStatus: map[string]string{}, PendingOrder: map[string]int{},
		PendingNotify: map[string]string{}, PendingNotifyChannels: map[string][]string{}, History: []types.SubscriptionHistoryEntry{},
		CreatedAt: types.NowISO(), Discontinued: true, DiscontinuedNextCheckAt: 1700003600,
	}
	if err := database.UpsertMonitorSubscription(subscription); err != nil {
		t.Fatal(err)
	}
	subs, err := database.ListMonitorSubscriptions()
	if err != nil || len(subs) != 1 || !subs[0].Discontinued || subs[0].DiscontinuedNextCheckAt != subscription.DiscontinuedNextCheckAt {
		t.Fatalf("subscriptions = %#v err=%v", subs, err)
	}
}

func TestDiscontinuedTrackerKVKeepsMissingFieldsAtSafeDefaults(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.SetKV("server_catalog_discontinued", map[string]interface{}{
		"24sk502": map[string]interface{}{"missingSince": 1700000000, "cycleId": "1700000000"},
	}); err != nil {
		t.Fatal(err)
	}
	var records map[string]struct {
		MissingSince float64 `json:"missingSince"`
		CycleID      string  `json:"cycleId"`
		Discontinued bool    `json:"discontinued"`
	}
	ok, err := database.GetKV("server_catalog_discontinued", &records)
	if err != nil || !ok {
		t.Fatalf("tracker read ok=%v err=%v", ok, err)
	}
	if records["24sk502"].Discontinued {
		t.Fatal("legacy tracker record unexpectedly became discontinued")
	}
}
