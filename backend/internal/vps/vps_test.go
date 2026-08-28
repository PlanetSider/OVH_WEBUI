package vps

import (
	"context"
	"reflect"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

func testVPSState(t *testing.T, sub types.VPSSubscription) (*app.State, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logFile := t.TempDir() + "/vps-test.log.json"
	state := &app.State{
		DB: database,
		Config: config.New(database),
		Logger: logger.New(logFile, nil),
		VPSSubscriptions: []types.VPSSubscription{cloneVPSSubscription(sub)},
	}
	if err := database.ReplaceVPSSubscriptions(state.VPSSubscriptions); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return state, database
}

func baseVPSSubscription() types.VPSSubscription {
	return types.VPSSubscription{
		ID: "sub-lifecycle", PlanCode: "vps-test", OvhSubsidiary: "IE",
		Datacenters: []string{"GRA", "SBG"}, NotifyAvailable: true, NotifyUnavailable: true,
		LastStatus: map[string]string{"GRA": "out-of-stock", "SBG": "available"},
		PendingNotify: map[string]string{"SBG": "available"},
		PendingNotifyChannels: map[string][]string{"SBG": {monitor.NotificationChannelTelegram}},
		History: []map[string]interface{}{{"datacenterCode": "SBG", "changeType": "available"}},
		CreatedAt: types.NowISO(),
	}
}

func snapshotVPSRuntime(sub types.VPSSubscription) interface{} {
	return []interface{}{sub.LastStatus, sub.PendingNotify, sub.PendingNotifyChannels, sub.History}
}

func TestProcessSubscriptionInvalidAvailabilityDoesNotAdvanceState(t *testing.T) {
	responses := []map[string]interface{}{
		nil,
		{},
		{"datacenters": []interface{}{}},
	}
	for i, response := range responses {
		sub := baseVPSSubscription()
		state, database := testVPSState(t, sub)
		before := snapshotVPSRuntime(cloneVPSSubscription(sub))
		processed := processSubscriptionWithAvailability(context.Background(), state, &sub,
			func(context.Context, *app.State, string, string) map[string]interface{} { return response })
		if processed {
			t.Errorf("case %d processed invalid availability", i)
		}
		if got := snapshotVPSRuntime(sub); !reflect.DeepEqual(got, before) {
			t.Errorf("case %d mutated working state: got %#v want %#v", i, got, before)
		}
		persisted := state.VPSSubscriptionsSnapshot()
		if len(persisted) != 1 || !reflect.DeepEqual(snapshotVPSRuntime(persisted[0]), before) {
			t.Errorf("case %d mutated persisted state: %#v", i, persisted)
		}
		database.Close()
	}
}

func TestProcessSubscriptionPartialResponsePreservesMissingDatacenter(t *testing.T) {
	sub := baseVPSSubscription()
	// 禁用通知以避免测试产生任何外部发送；本用例只验证库存基线。
	sub.NotifyAvailable = false
	sub.NotifyUnavailable = false
	sub.PendingNotify = map[string]string{}
	sub.PendingNotifyChannels = map[string][]string{}
	state, database := testVPSState(t, sub)
	defer database.Close()
	response := map[string]interface{}{"datacenters": []interface{}{
		map[string]interface{}{"code": "GRA", "datacenter": "Gravelines", "status": "available"},
	}}
	if !processSubscriptionWithAvailability(context.Background(), state, &sub,
		func(context.Context, *app.State, string, string) map[string]interface{} { return response }) {
		t.Fatal("valid partial response was not committed")
	}
	got := state.VPSSubscriptionsSnapshot()[0].LastStatus
	if got["GRA"] != "available" || got["SBG"] != "available" {
		t.Fatalf("partial response overwrote missing datacenter: %#v", got)
	}
}

func TestProcessSubscriptionClearsPendingWhenNotificationTypeDisabled(t *testing.T) {
	sub := baseVPSSubscription()
	sub.NotifyAvailable = false
	sub.NotifyUnavailable = true
	state, database := testVPSState(t, sub)
	defer database.Close()
	response := map[string]interface{}{"datacenters": []interface{}{
		map[string]interface{}{"code": "SBG", "datacenter": "Strasbourg", "status": "available"},
	}}
	if !processSubscriptionWithAvailability(context.Background(), state, &sub,
		func(context.Context, *app.State, string, string) map[string]interface{} { return response }) {
		t.Fatal("valid response was not committed")
	}
	got := state.VPSSubscriptionsSnapshot()[0]
	if _, exists := got.PendingNotify["SBG"]; exists {
		t.Fatalf("disabled available notification remained pending: %#v", got.PendingNotify)
	}
	if _, exists := got.PendingNotifyChannels["SBG"]; exists {
		t.Fatalf("disabled pending channel snapshot remained: %#v", got.PendingNotifyChannels)
	}
}

func TestProcessSubscriptionKeepsPendingWhenEnabledChannelCredentialsMissing(t *testing.T) {
	sub := baseVPSSubscription()
	state, database := testVPSState(t, sub)
	defer database.Close()
	// 默认配置启用 Telegram，但没有 token/chat ID；事件必须留待凭据恢复。
	response := map[string]interface{}{"datacenters": []interface{}{
		map[string]interface{}{"code": "SBG", "datacenter": "Strasbourg", "status": "available"},
	}}
	if !processSubscriptionWithAvailability(context.Background(), state, &sub,
		func(context.Context, *app.State, string, string) map[string]interface{} { return response }) {
		t.Fatal("valid response was not committed")
	}
	got := state.VPSSubscriptionsSnapshot()[0]
	if got.PendingNotify["SBG"] != "available" || !reflect.DeepEqual(got.PendingNotifyChannels["SBG"], []string{monitor.NotificationChannelTelegram}) {
		t.Fatalf("temporarily unavailable channel was dropped: pending=%#v channels=%#v", got.PendingNotify, got.PendingNotifyChannels)
	}
}

func TestProcessSubscriptionPersistenceFailureDoesNotPublishMemoryAndCanRetry(t *testing.T) {
	sub := baseVPSSubscription()
	sub.NotifyAvailable = false
	sub.NotifyUnavailable = false
	sub.PendingNotify = map[string]string{}
	sub.PendingNotifyChannels = map[string][]string{}
	state, database := testVPSState(t, sub)
	defer database.Close()
	if _, err := database.Exec("CREATE TRIGGER reject_vps_insert BEFORE INSERT ON vps_subscriptions BEGIN SELECT RAISE(ABORT, 'forced vps failure'); END"); err != nil {
		t.Fatal(err)
	}
	response := map[string]interface{}{"datacenters": []interface{}{
		map[string]interface{}{"code": "GRA", "datacenter": "Gravelines", "status": "available"},
	}}
	fetch := func(context.Context, *app.State, string, string) map[string]interface{} { return response }
	working := cloneVPSSubscription(sub)
	if processSubscriptionWithAvailability(context.Background(), state, &working, fetch) {
		t.Fatal("process succeeded despite forced persistence failure")
	}
	if got := state.VPSSubscriptionsSnapshot()[0].LastStatus["GRA"]; got != "out-of-stock" {
		t.Fatalf("memory advanced after persistence failure: %q", got)
	}
	if _, err := database.Exec("DROP TRIGGER reject_vps_insert"); err != nil {
		t.Fatal(err)
	}
	working = state.VPSSubscriptionsSnapshot()[0]
	if !processSubscriptionWithAvailability(context.Background(), state, &working, fetch) {
		t.Fatal("retry after persistence recovery did not succeed")
	}
	if got := state.VPSSubscriptionsSnapshot()[0].LastStatus["GRA"]; got != "available" {
		t.Fatalf("retry did not advance memory: %q", got)
	}
}

func TestVPSPendingStateReloadsFromDatabase(t *testing.T) {
	sub := baseVPSSubscription()
	state, database := testVPSState(t, sub)
	defer database.Close()
	loaded, err := database.ListVPSSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.VPSSubscriptionsSnapshot()
	if len(loaded) != 1 || len(snapshot) != 1 || !reflect.DeepEqual(loaded[0].PendingNotify, snapshot[0].PendingNotify) ||
		!reflect.DeepEqual(loaded[0].PendingNotifyChannels, snapshot[0].PendingNotifyChannels) {
		t.Fatalf("pending state did not reload: %#v", loaded)
	}
}

func TestMergeSubscriptionStateRejectsConcurrentRuntimeChange(t *testing.T) {
	sub := baseVPSSubscription()
	state, database := testVPSState(t, sub)
	defer database.Close()
	expected := cloneVPSSubscription(sub)
	checked := cloneVPSSubscription(sub)
	checked.LastStatus["GRA"] = "available"
	if err := state.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
		subscriptions[0].History = append(subscriptions[0].History, map[string]interface{}{"concurrent": true})
		return subscriptions, nil
	}); err != nil {
		t.Fatal(err)
	}
	committed, err := mergeSubscriptionStateIfCurrent(state, checked, &expected)
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Fatal("stale checked state overwrote a concurrent runtime change")
	}
	if got := state.VPSSubscriptionsSnapshot()[0].LastStatus["GRA"]; got != "out-of-stock" {
		t.Fatalf("stale state was published: %q", got)
	}
}

func TestVPSStatusClass(t *testing.T) {
	tests := []struct {
		status      string
		available   bool
		unavailable bool
		known       bool
	}{
		{status: "available", available: true, known: true},
		{status: " AVAILABLE ", available: true, known: true},
		{status: "out-of-stock", unavailable: true, known: true},
		{status: "out-of-stock-preorder-allowed", unavailable: true, known: true},
		{status: "unavailable", unavailable: true, known: true},
		{status: "unknown"},
		{status: ""},
		{status: "future-status"},
	}

	for _, test := range tests {
		available, unavailable, known := vpsStatusClass(test.status)
		if available != test.available || unavailable != test.unavailable || known != test.known {
			t.Fatalf("vpsStatusClass(%q) = (%v, %v, %v), want (%v, %v, %v)",
				test.status, available, unavailable, known, test.available, test.unavailable, test.known)
		}
	}
}

func TestNotificationDataRequiresMatchingExplicitStatus(t *testing.T) {
	statuses := map[string]dcStatus{
		"BHS": {name: "Beauharnois", code: "BHS", status: "available"},
		"GRA": {name: "Gravelines", code: "GRA", status: "unavailable"},
	}
	pending := map[string]string{
		"BHS":     "available",
		"GRA":     "available",
		"MISSING": "available",
	}

	dcs, keys := notificationData(statuses, pending, "available")
	if len(dcs) != 1 || len(keys) != 1 || keys[0] != "BHS" {
		t.Fatalf("notificationData returned keys %v and %d entries, want only BHS", keys, len(dcs))
	}
}

func TestPendingNotificationPolicy(t *testing.T) {
	if pendingNotificationEnabled("available", false, true) {
		t.Fatal("available notification must be disabled with NotifyAvailable=false")
	}
	if !pendingNotificationEnabled("unavailable", false, true) {
		t.Fatal("unavailable notification must be enabled with NotifyUnavailable=true")
	}
	if pendingNotificationMatchesStatus("available", "unknown") {
		t.Fatal("unknown status must not match an available notification")
	}
	if pendingNotificationMatchesStatus("unavailable", "unknown") {
		t.Fatal("unknown status must not match an unavailable notification")
	}
}

func TestVPSOldPendingChannelsUseDefaults(t *testing.T) {
	sub := &types.VPSSubscription{PendingNotifyChannels: map[string][]string{}}
	got := vpsPendingChannels(sub, []string{"GRA"}, []string{monitor.NotificationChannelTelegram, monitor.NotificationChannelFeishu})
	want := []string{monitor.NotificationChannelFeishu, monitor.NotificationChannelTelegram}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vpsPendingChannels() = %v, want %v", got, want)
	}
}

func TestVPSExplicitEmptyPendingChannelsDoNotUseDefaults(t *testing.T) {
	sub := &types.VPSSubscription{PendingNotifyChannels: map[string][]string{"GRA": {}}}
	got := vpsPendingChannels(sub, []string{"GRA"}, []string{monitor.NotificationChannelTelegram})
	if len(got) != 0 {
		t.Fatalf("explicit empty pending channels were repopulated: %v", got)
	}
}

func TestSetVPSPendingNotificationReplacesPreviousChannelSnapshot(t *testing.T) {
	sub := &types.VPSSubscription{
		PendingNotify: map[string]string{"GRA": "unavailable"},
		PendingNotifyChannels: map[string][]string{
			"GRA": {monitor.NotificationChannelTelegram},
		},
	}

	setVPSPendingNotification(sub, "GRA", "available", []string{
		monitor.NotificationChannelTelegram,
		monitor.NotificationChannelFeishu,
	})

	if sub.PendingNotify["GRA"] != "available" {
		t.Fatalf("pending change type = %q, want available", sub.PendingNotify["GRA"])
	}
	want := []string{monitor.NotificationChannelFeishu, monitor.NotificationChannelTelegram}
	if got := sub.PendingNotifyChannels["GRA"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("new event retained stale channels: got %v, want %v", got, want)
	}
}

func TestVPSNotificationStillCurrentRejectsDisabledGlobalChannel(t *testing.T) {
	state := &app.State{}
	// nil Config means no global channel is enabled; this should never send
	// an event whose snapshot still names Telegram.
	sub := &types.VPSSubscription{
		ID: "sub-1", PlanCode: "vps-1",
		PendingNotify: map[string]string{"GRA": "available"},
		PendingNotifyChannels: map[string][]string{"GRA": {monitor.NotificationChannelTelegram}},
	}
	state.VPSSubsMu.Lock()
	state.VPSSubscriptions = []types.VPSSubscription{cloneVPSSubscription(*sub)}
	state.VPSSubsMu.Unlock()
	if vpsNotificationStillCurrent(state, sub, []string{"GRA"}, "available", []string{monitor.NotificationChannelTelegram}) {
		t.Fatal("disabled Telegram channel was still considered current for VPS")
	}
}

func TestVPSNotificationGroupsSeparateChannels(t *testing.T) {
	sub := &types.VPSSubscription{
		PendingNotify: map[string]string{"GRA": "available", "SBG": "available"},
		PendingNotifyChannels: map[string][]string{
			"GRA": {monitor.NotificationChannelTelegram},
			"SBG": {monitor.NotificationChannelFeishu},
		},
	}
	statuses := map[string]dcStatus{
		"GRA": {name: "Gravelines", code: "GRA", status: "available"},
		"SBG": {name: "Strasbourg", code: "SBG", status: "available"},
	}
	groups := notificationGroups(statuses, sub, "available", nil)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want separate channel groups", len(groups))
	}
}

func TestCloneHistoryRecursivelyDetachesNestedValues(t *testing.T) {
	source := []map[string]interface{}{
		{"config": map[string]interface{}{"options": []interface{}{"ram-1", map[string]interface{}{"code": "disk-1"}}}},
	}
	cloned := cloneHistory(source)
	clonedConfig := cloned[0]["config"].(map[string]interface{})
	clonedOptions := clonedConfig["options"].([]interface{})
	clonedOptions[0] = "ram-2"
	clonedOptions[1].(map[string]interface{})["code"] = "disk-2"
	originalOptions := source[0]["config"].(map[string]interface{})["options"].([]interface{})
	if originalOptions[0] != "ram-1" || originalOptions[1].(map[string]interface{})["code"] != "disk-1" {
		t.Fatalf("nested history still shares references: %#v", source)
	}
}

func TestCloneVPSSubscriptionDetachesDatacenters(t *testing.T) {
	source := baseVPSSubscription()

	cloned := cloneVPSSubscription(source)
	cloned.Datacenters[0] = "BHS"

	if source.Datacenters[0] != "GRA" {
		t.Fatalf("cloned VPS subscription shares datacenter slice: %#v", source)
	}
}
