package monitor

import (
	"reflect"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/types"
)

type successfulTestNotifier struct {
	sent int
}

func (n *successfulTestNotifier) Configured() bool { return true }
func (n *successfulTestNotifier) SendDefault(string) bool {
	n.sent++
	return true
}

func testNotificationState(t *testing.T, telegramEnabled, feishuEnabled bool) *app.State {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store := config.New(database)
	cfg := store.Get()
	cfg.TgNotificationsEnabled = &telegramEnabled
	cfg.FeishuNotificationsEnabled = &feishuEnabled
	if err := store.Set(cfg); err != nil {
		t.Fatal(err)
	}
	return &app.State{Config: store, DB: database}
}

func disableNotificationChannels(t *testing.T, store *config.Store) {
	t.Helper()
	cfg := store.Get()
	disabled := false
	cfg.TgNotificationsEnabled = &disabled
	cfg.FeishuNotificationsEnabled = &disabled
	cfg.WeixinNotificationsEnabled = &disabled
	if err := store.Set(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestPendingChannelsDistinguishMissingFromExplicitEmpty(t *testing.T) {
	sub := &Subscription{PendingNotifyChannels: map[string][]string{"empty": {}}}
	defaults := []string{NotificationChannelTelegram}
	if got := pendingChannelsForKeys(sub, []string{"missing"}, defaults); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("missing snapshot = %v, want defaults %v", got, defaults)
	}
	if got := pendingChannelsForKeys(sub, []string{"empty"}, defaults); len(got) != 0 {
		t.Fatalf("explicit empty snapshot was repopulated: %v", got)
	}
}

func TestNotificationStillCurrentRejectsDisabledGlobalChannel(t *testing.T) {
	state := testNotificationState(t, false, false)
	m := New(state)
	target := &Subscription{
		PlanCode: "plan", PendingNotify: map[string]string{"GRA|cfg": "available"},
		PendingNotifyChannels: map[string][]string{"GRA|cfg": {NotificationChannelTelegram}},
	}
	m.subscriptions = []*Subscription{target}
	working := cloneSubscription(target)
	if m.notificationStillCurrent(target, working, []string{"GRA|cfg"}, "available", []string{NotificationChannelTelegram}) {
		t.Fatal("disabled Telegram channel was still considered current")
	}
}

func TestNotificationChannelHelpersHandleMissingConfig(t *testing.T) {
	state := &app.State{}
	if channels := ConfiguredNotificationChannels(state); len(channels) != 0 {
		t.Fatalf("configured channels with nil config = %#v", channels)
	}
	if channels := PendingNotificationChannels(state); len(channels) != 0 {
		t.Fatalf("pending channels with nil config = %#v", channels)
	}
	if channels := EnabledNotificationChannels(state, []string{NotificationChannelTelegram}); len(channels) != 0 {
		t.Fatalf("enabled channels with nil config = %#v", channels)
	}
}

func TestCheckNewServersHandlesMissingDatabase(t *testing.T) {
	monitor := New(&app.State{})
	monitor.CheckNewServers([]map[string]interface{}{{"planCode": "24sk10"}})
	if len(monitor.knownServers) != 0 {
		t.Fatalf("known servers advanced without persistence: %#v", monitor.knownServers)
	}
}

func TestCheckNewServersTreatsPersistedEmptyBaselineAsInitialized(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.SaveKnownServersAndNotifications([]string{}, nil); err != nil {
		t.Fatal(err)
	}

	monitor := New(&app.State{DB: database, Logger: logger.New(t.TempDir()+"/monitor.log.json", nil)})
	monitor.LoadFromDB()
	if !monitor.knownServersInitialized {
		t.Fatal("persisted empty known-server baseline was treated as missing")
	}
	monitor.CheckNewServers([]map[string]interface{}{{"planCode": "24sk10"}})

	var known []string
	ok, err := database.GetKV("monitor_known_servers", &known)
	if err != nil || !ok || !reflect.DeepEqual(known, []string{"24sk10"}) {
		t.Fatalf("known servers = %#v, ok=%v err=%v", known, ok, err)
	}
}

func TestCheckNewServersPersistsAwaitingNotificationWithoutEnabledChannels(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.SaveKnownServersAndNotifications([]string{}, nil); err != nil {
		t.Fatal(err)
	}

	state := &app.State{
		DB: database,
		Config: config.New(database),
		Logger: logger.New(t.TempDir()+"/monitor.log.json", nil),
	}
	disableNotificationChannels(t, state.Config)
	monitor := New(state)
	monitor.LoadFromDB()
	monitor.CheckNewServers([]map[string]interface{}{{
		"planCode": "24sk10",
		"name":     "KS-1",
	}})

	entries, err := database.ListNotificationOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("outbox entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.EventKey != "new_server:24sk10" || entry.Kind != NotificationKindNewServer {
		t.Fatalf("unexpected outbox entry: %#v", entry)
	}
	if !entry.AwaitingChannels || len(entry.Channels) != 0 {
		t.Fatalf("outbox channels = %v awaiting=%v, want empty awaiting snapshot", entry.Channels, entry.AwaitingChannels)
	}

	// CheckNewServers 会在提交基线后立即触发分发。没有启用渠道时，
	// awaiting 事件必须仍在 outbox 中，供以后启用渠道时补发。
	monitor.DispatchNotificationOutbox()
	entries, err = database.ListNotificationOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].AwaitingChannels || len(entries[0].Channels) != 0 {
		t.Fatalf("awaiting event was removed without channels: %#v", entries)
	}

	var known []string
	ok, err := database.GetKV("monitor_known_servers", &known)
	if err != nil || !ok || !reflect.DeepEqual(known, []string{"24sk10"}) {
		t.Fatalf("known servers = %#v, ok=%v err=%v", known, ok, err)
	}
}

func TestAwaitingNotificationIsDeliveredAfterChannelEnabled(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := config.New(database)
	state := &app.State{
		DB: database, Config: store,
		Logger: logger.New(t.TempDir()+"/monitor.log.json", nil),
	}
	disableNotificationChannels(t, store)
	entry, err := NewPurchaseSuccessNotification(types.QueueItem{
		ID: "task-late-channel", PlanCode: "24sk102", Datacenter: "gra",
	}, "order-late-channel", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.EnqueueNotification(*entry); err != nil {
		t.Fatal(err)
	}

	m := New(state)
	m.DispatchNotificationOutbox()
	items, err := database.ListNotificationOutbox(10)
	if err != nil || len(items) != 1 || !items[0].AwaitingChannels {
		t.Fatalf("disabled channel changed awaiting outbox: %#v err=%v", items, err)
	}

	enabled := true
	cfg := store.Get()
	cfg.WeixinNotificationsEnabled = &enabled
	if err := store.Set(cfg); err != nil {
		t.Fatal(err)
	}
	notifier := &successfulTestNotifier{}
	state.Weixin = notifier
	state.ClearNotificationOutboxRetry(items[0].ID)
	m.DispatchNotificationOutbox()

	items, err = database.ListNotificationOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || notifier.sent != 1 {
		t.Fatalf("outbox=%#v sends=%d, want delivered once", items, notifier.sent)
	}
}

func TestSaveToDBDoesNotInitializeMissingKnownServerBaseline(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	monitor := New(&app.State{DB: database, Logger: logger.New(t.TempDir()+"/monitor.log.json", nil)})
	monitor.loaded = true
	if monitor.knownServersInitialized {
		t.Fatal("new monitor unexpectedly has an initialized known-server baseline")
	}
	if err := monitor.SaveToDB(); err != nil {
		t.Fatal(err)
	}
	if monitor.knownServersInitialized {
		t.Fatal("subscription save incorrectly initialized the missing known-server baseline")
	}

	var known []string
	ok, err := database.GetKV("monitor_known_servers", &known)
	if err != nil || ok {
		t.Fatalf("known servers = %#v, ok=%v err=%v", known, ok, err)
	}
}

func TestSaveToDBPreservesExistingKnownServerBaselineUntilInitialized(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.SaveKnownServersAndNotifications([]string{"old-plan"}, nil); err != nil {
		t.Fatal(err)
	}

	monitor := New(&app.State{DB: database, Logger: logger.New(t.TempDir()+"/monitor.log.json", nil)})
	monitor.loaded = true
	monitor.knownServers = map[string]struct{}{}
	monitor.knownServersInitialized = false
	if err := monitor.SaveToDB(); err != nil {
		t.Fatal(err)
	}

	var known []string
	ok, err := database.GetKV("monitor_known_servers", &known)
	if err != nil || !ok || !reflect.DeepEqual(known, []string{"old-plan"}) {
		t.Fatalf("known servers = %#v, ok=%v err=%v; existing baseline was overwritten", known, ok, err)
	}
}
