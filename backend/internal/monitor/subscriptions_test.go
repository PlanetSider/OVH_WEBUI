package monitor

import (
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
)

func testButtonMonitor(t *testing.T) (*Monitor, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{DB: database, Logger: logger.New(t.TempDir()+"/monitor.log.json", nil)}
	return New(state), database
}

func TestAddSubscriptionAppendsSamePlanCodeAsSeparateInstances(t *testing.T) {
	mon, database := testButtonMonitor(t)
	defer database.Close()
	mon.LoadFromDB()

	if err := mon.AddSubscription("24sk10", []string{"gra"}, true, false, "first", nil, nil, false, 1, "", []string{"32G"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mon.AddSubscription("24sk10", []string{"sbg"}, false, true, "second", nil, nil, false, 1, "", []string{"64G"}, nil, nil); err != nil {
		t.Fatal(err)
	}

	subscriptions := mon.Snapshot()
	if len(subscriptions) != 2 {
		t.Fatalf("subscription count = %d, want 2", len(subscriptions))
	}
	if subscriptions[0].ID == "" || subscriptions[1].ID == "" || subscriptions[0].ID == subscriptions[1].ID {
		t.Fatalf("subscription IDs are not independent: %#v", subscriptions)
	}
	if subscriptions[0].PlanCode != "24sk10" || subscriptions[1].PlanCode != "24sk10" {
		t.Fatalf("plan codes changed: %#v", subscriptions)
	}
	if subscriptions[0].ServerName != "first" || subscriptions[1].ServerName != "second" {
		t.Fatalf("first subscription was overwritten: %#v", subscriptions)
	}

	persisted, err := database.ListMonitorSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 {
		t.Fatalf("persisted subscription count = %d, want 2", len(persisted))
	}
}
func TestAddMessageUUIDDoesNotPublishCacheWhenPersistenceFails(t *testing.T) {
	mon, database := testButtonMonitor(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TRIGGER reject_button_insert BEFORE INSERT ON telegram_order_buttons BEGIN SELECT RAISE(ABORT, 'forced button failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := mon.AddMessageUUID("failed-button", "24sk10", "gra", nil, map[string]interface{}{"accountId": "account-1"}); err == nil {
		t.Fatal("AddMessageUUID succeeded despite forced persistence failure")
	}
	if got := mon.MessageUUIDCacheLookup("failed-button"); got != nil {
		t.Fatalf("failed button was published to memory: %#v", got)
	}
}

func TestAddMessageUUIDFreezesNestedConfigurationAndOptions(t *testing.T) {
	mon, database := testButtonMonitor(t)
	defer database.Close()
	options := []string{"ram-original"}
	config := map[string]interface{}{
		"accountId": "account-1",
		"nested": []interface{}{
			[]interface{}{map[string]interface{}{"code": "disk-original"}},
			[]string{"network-original"},
		},
	}
	if err := mon.AddMessageUUID("snapshot-button", "24sk10", "gra", options, config); err != nil {
		t.Fatal(err)
	}
	options[0] = "ram-mutated"
	nested := config["nested"].([]interface{})
	nested[0].([]interface{})[0].(map[string]interface{})["code"] = "disk-mutated"
	nested[1].([]string)[0] = "network-mutated"

	got := mon.MessageUUIDCacheLookup("snapshot-button")
	if got == nil {
		t.Fatal("cached button is missing")
	}
	if got.Options[0] != "ram-original" {
		t.Fatalf("cached options changed: %#v", got.Options)
	}
	gotNested := got.ConfigInfo["nested"].([]interface{})
	if gotNested[0].([]interface{})[0].(map[string]interface{})["code"] != "disk-original" {
		t.Fatalf("cached nested map changed: %#v", got.ConfigInfo)
	}
	if gotNested[1].([]string)[0] != "network-original" {
		t.Fatalf("cached string slice changed: %#v", got.ConfigInfo)
	}
}
