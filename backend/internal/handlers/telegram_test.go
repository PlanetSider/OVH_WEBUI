package handlers

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
)

func TestRollbackTelegramButtonLogsPersistenceFailure(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.UpsertTelegramButton("rollback-log-button", "24sk10", "gra", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := database.ClaimTelegramButton("rollback-log-button"); err != nil || !claimed {
		t.Fatalf("button claim claimed=%v err=%v", claimed, err)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER reject_button_unclaim
		BEFORE UPDATE OF used_at ON telegram_order_buttons
		WHEN NEW.used_at = 0
		BEGIN
			SELECT RAISE(ABORT, 'forced rollback failure');
		END
	`); err != nil {
		t.Fatal(err)
	}

	log := logger.New(filepath.Join(t.TempDir(), "logs.json"), nil)
	state := &app.State{DB: database, Logger: log}
	if rollbackTelegramButton(state, "rollback-log-button", "test rollback") {
		t.Fatal("rollback unexpectedly succeeded")
	}

	entries := log.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Level != "ERROR" || entry.Source != "button_security" {
		t.Fatalf("unexpected rollback log metadata: %#v", entry)
	}
	if !strings.Contains(entry.Message, "回滚一键下单按钮失败") ||
		!strings.Contains(entry.Message, "rollback-log-button") ||
		!strings.Contains(entry.Message, "test rollback") {
		t.Fatalf("rollback log lacks context: %q", entry.Message)
	}
}
