package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/proxyguard"
	"github.com/ovh-webui/server/internal/storage"
	"github.com/ovh-webui/server/internal/types"
)

func newProxyGuardTestState(t *testing.T) (*State, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := NewState(storage.Paths{}, config.New(database), logger.New(t.TempDir()+"/proxyguard.log.json", nil), database)
	state.SetProxyGuardMonitorMutator(func(accountID string, enabled bool) (int, error) {
		subs, err := database.ListMonitorSubscriptions()
		if err != nil {
			return 0, err
		}
		changed := 0
		for i := range subs {
			sub := &subs[i]
			if sub.AutoOrderAccountID != accountID {
				continue
			}
			if enabled {
				if !sub.ProxyGuardAutoOrderDisabled || sub.AutoOrder {
					continue
				}
				sub.AutoOrder = true
				sub.ProxyGuardAutoOrderDisabled = false
				changed++
				continue
			}
			if !sub.AutoOrder {
				continue
			}
			sub.AutoOrder = false
			sub.ProxyGuardAutoOrderDisabled = true
			changed++
		}
		if changed == 0 {
			return 0, nil
		}
		return changed, database.ReplaceMonitorSubscriptions(subs)
	})
	state.Accounts = []types.OVHAccount{{ID: "account-1", Name: "primary", Zone: "ie", ProxyURL: "http://user:secret@proxy.example:8080"}}
	return state, database
}

func proxyGuardTestQueue() []types.QueueItem {
	now := types.NowISO()
	return []types.QueueItem{
		{ID: "running", AccountID: "account-1", PlanCode: "plan-a", Datacenter: "gra", Status: "running", CreatedAt: now, UpdatedAt: now},
		{ID: "pending", AccountID: "account-1", PlanCode: "plan-b", Datacenter: "gra", Status: "pending", CreatedAt: now, UpdatedAt: now},
		{ID: "manual-paused", AccountID: "account-1", PlanCode: "plan-c", Datacenter: "gra", Status: "paused", CreatedAt: now, UpdatedAt: now},
		{ID: "other-account", AccountID: "account-2", PlanCode: "plan-d", Datacenter: "gra", Status: "running", CreatedAt: now, UpdatedAt: now},
	}
}

func TestProxyGuardActionPersistsTripAndRecoveryWithoutTouchingManualPause(t *testing.T) {
	state, database := newProxyGuardTestState(t)
	defer database.Close()
	state.Queue = proxyGuardTestQueue()
	if err := database.ReplaceQueue(state.Queue); err != nil {
		t.Fatal(err)
	}
	monitorSub := types.Subscription{
		PlanCode: "plan-a", Datacenters: []string{"gra"}, LastStatus: map[string]string{},
		ConfirmedStatus: map[string]string{}, PendingOrder: map[string]int{}, PendingNotify: map[string]string{},
		PendingNotifyChannels: map[string][]string{}, CreatedAt: types.NowISO(), History: []types.SubscriptionHistoryEntry{},
		AutoOrder: true, Quantity: 1, AutoOrderAccountID: "account-1",
	}
	if err := database.UpsertMonitorSubscription(monitorSub); err != nil {
		t.Fatal(err)
	}
	vpsSub := types.VPSSubscription{
		ID: "vps-1", PlanCode: "vps-a", OvhSubsidiary: "IE", Datacenters: []string{"GRA"},
		LastStatus: map[string]string{}, PendingNotify: map[string]string{}, PendingNotifyChannels: map[string][]string{},
		History: []map[string]interface{}{}, CreatedAt: types.NowISO(), AutoOrder: true,
		Quantity: 1, AutoOrderAccountID: "account-1",
	}
	state.VPSSubscriptions = []types.VPSSubscription{vpsSub}
	if err := database.ReplaceVPSSubscriptions(state.VPSSubscriptions); err != nil {
		t.Fatal(err)
	}

	var actions []ProxyGuardAction
	state.SetProxyGuardNotificationHandler(func(action ProxyGuardAction) { actions = append(actions, action) })
	trip := proxyguard.Event{Kind: proxyguard.EventTrip, Status: proxyguard.Status{AccountID: "account-1", LastError: "proxy down", ConsecutiveFailures: 3}}
	state.HandleProxyGuardEvent(trip)

	queue, err := database.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range queue {
		switch item.ID {
		case "running", "pending":
			if item.Status != "paused" || !item.ProxyGuardPaused {
				t.Fatalf("queue item %s after trip = %+v", item.ID, item)
			}
		case "manual-paused":
			if item.ProxyGuardPaused {
				t.Fatalf("manual pause was tagged by proxyguard: %+v", item)
			}
		case "other-account":
			if item.Status != "running" {
				t.Fatalf("unrelated account changed: %+v", item)
			}
		}
	}
	monitorRows, err := database.ListMonitorSubscriptions()
	if err != nil || len(monitorRows) != 1 || monitorRows[0].AutoOrder || !monitorRows[0].ProxyGuardAutoOrderDisabled {
		t.Fatalf("monitor rows after trip = %#v, err=%v", monitorRows, err)
	}
	vpsRows, err := database.ListVPSSubscriptions()
	if err != nil || len(vpsRows) != 1 || vpsRows[0].AutoOrder || !vpsRows[0].ProxyGuardAutoOrderDisabled {
		t.Fatalf("vps rows after trip = %#v, err=%v", vpsRows, err)
	}
	if len(actions) != 1 || actions[0].PausedQueue != 2 || actions[0].DisabledMonitorAutoOrder != 1 || actions[0].DisabledVPSAutoOrder != 1 {
		t.Fatalf("trip action = %#v", actions)
	}

	recovery := proxyguard.Event{Kind: proxyguard.EventRecovery, Duration: 3 * time.Second, Status: proxyguard.Status{AccountID: "account-1", LastSuccessAt: time.Now().UTC()}}
	state.HandleProxyGuardEvent(recovery)
	queue, err = database.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range queue {
		if item.ID == "running" || item.ID == "pending" {
			if item.Status != "running" || item.ProxyGuardPaused {
				t.Fatalf("queue item %s after recovery = %+v", item.ID, item)
			}
		}
		if item.ID == "manual-paused" && item.Status != "paused" {
			t.Fatalf("manual pause changed after recovery: %+v", item)
		}
	}
	monitorRows, err = database.ListMonitorSubscriptions()
	if err != nil || len(monitorRows) != 1 || !monitorRows[0].AutoOrder || monitorRows[0].ProxyGuardAutoOrderDisabled {
		t.Fatalf("monitor rows after recovery = %#v, err=%v", monitorRows, err)
	}
	vpsRows, err = database.ListVPSSubscriptions()
	if err != nil || len(vpsRows) != 1 || !vpsRows[0].AutoOrder || vpsRows[0].ProxyGuardAutoOrderDisabled {
		t.Fatalf("vps rows after recovery = %#v, err=%v", vpsRows, err)
	}
	if len(actions) != 2 || actions[1].RestoredQueue != 2 || actions[1].RestoredMonitorAutoOrder != 1 || actions[1].RestoredVPSAutoOrder != 1 {
		t.Fatalf("recovery action = %#v", actions)
	}
}

func TestRestoreProxyGuardGatesSeedsPersistedMarkers(t *testing.T) {
	state, database := newProxyGuardTestState(t)
	defer database.Close()
	now := types.NowISO()
	state.Queue = []types.QueueItem{{ID: "paused", AccountID: "account-1", Status: "paused", ProxyGuardPaused: true, UpdatedAt: now}}
	state.VPSSubscriptions = []types.VPSSubscription{{ID: "vps", AutoOrderAccountID: "account-1", ProxyGuardAutoOrderDisabled: true, CreatedAt: now}}
	if err := database.ReplaceQueue(state.Queue); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceVPSSubscriptions(state.VPSSubscriptions); err != nil {
		t.Fatal(err)
	}
	state.RestoreProxyGuardGates()
	if !state.IsAccountProxyPaused("account-1") {
		t.Fatal("persisted proxyguard marker did not restore the account gate")
	}
}

func TestProxyHealthReporterIgnoresNonProxyErrors(t *testing.T) {
	guard := proxyguard.New(1)
	reporter := proxyHealthReporter{guard: guard}
	reporter.ReportFailure("account-1", errors.New("OVH returned HTTP 500"))
	if guard.IsPaused("account-1") {
		t.Fatal("ordinary OVH error tripped proxyguard")
	}
	reporter.ReportFailure("account-1", &ovh.ProxyError{Proxy: "http://proxy.example:8080", Err: errors.New("connection refused")})
	if !guard.IsPaused("account-1") {
		t.Fatal("proxy error did not trip proxyguard")
	}
}

func TestProxyGuardGuardEmitsOneTripAndOneRecoveryUnderConcurrency(t *testing.T) {
	guard := proxyguard.New(3)
	var wg sync.WaitGroup
	trips := make(chan proxyguard.Event, 32)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := guard.ObserveFailure("account-1", errors.New("proxy down"))
			if event.Kind != proxyguard.EventNone {
				trips <- event
			}
		}()
	}
	wg.Wait()
	close(trips)
	tripCount := 0
	for event := range trips {
		if event.Kind == proxyguard.EventTrip {
			tripCount++
		}
	}
	if tripCount != 1 {
		t.Fatalf("trip event count = %d, want 1", tripCount)
	}
	if event := guard.ObserveSuccess("account-1"); event.Kind != proxyguard.EventRecovery {
		t.Fatalf("recovery event = %+v", event)
	}
}
