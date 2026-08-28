package monitor

import (
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
)

func testButtonMonitor(t *testing.T) (*Monitor, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{DB: database}
	return New(state), database
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
