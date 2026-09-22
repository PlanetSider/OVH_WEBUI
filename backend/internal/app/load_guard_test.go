package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestSaveBlockedTracksAndClearsTableFailure(t *testing.T) {
	state := &State{}
	loadErr := errors.New("database is temporarily unavailable")
	state.MarkLoadFailed("queue", loadErr)

	if err := state.SaveBlocked("queue"); err == nil || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("SaveBlocked(queue) = %v, want a queue guard error", err)
	}
	failures := state.LoadFailures()
	if failures["queue"] != loadErr.Error() {
		t.Fatalf("LoadFailures()[queue] = %q, want %q", failures["queue"], loadErr.Error())
	}

	state.ClearLoadFailure("queue")
	if err := state.SaveBlocked("queue"); err != nil {
		t.Fatalf("SaveBlocked(queue) after clear = %v, want nil", err)
	}
}

func TestEmptyLoadedSnapshotsAreNotBlocked(t *testing.T) {
	state := &State{
		Queue:            []types.QueueItem{},
		History:          []types.PurchaseHistoryEntry{},
		ServerPlans:      []types.ServerPlan{},
		VPSSubscriptions: []types.VPSSubscription{},
	}
	for _, table := range []string{"queue", "history", "servers", "vps_subscriptions", "monitor_subscriptions"} {
		if err := state.SaveBlocked(table); err != nil {
			t.Fatalf("empty successfully loaded table %q is blocked: %v", table, err)
		}
	}
}

func TestDestructiveQueueAndHistoryPathsRespectLoadGuards(t *testing.T) {
	state := &State{}
	state.MarkLoadFailed("queue", errors.New("queue read failed"))
	if err := state.QuarantineQueueItem("task"); err == nil {
		t.Fatal("queue quarantine bypassed the queue load guard")
	}
	state.ClearLoadFailure("queue")
	state.MarkLoadFailed("history", errors.New("history read failed"))
	if err := state.CommitPurchaseSuccess(types.PurchaseHistoryEntry{TaskID: "task"}); err == nil {
		t.Fatal("purchase success bypassed the history load guard")
	}
	state.ClearLoadFailure("history")
	state.MarkLoadFailed("monitor_subscriptions", errors.New("monitor read failed"))
	if err := state.EnqueueMonitorOrders(types.Subscription{}, []types.QueueItem{{ID: "task"}}); err == nil {
		t.Fatal("monitor order enqueue bypassed the monitor load guard")
	}
}
