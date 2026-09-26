package handlers

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

func TestRollbackQueueItemDeletedPreservesExistingIsolation(t *testing.T) {
	state := &app.State{DeletedTaskIDs: map[string]struct{}{"task-1": {}}}
	alreadyMarked := markQueueItemDeleted(state, "task-1")
	rollbackQueueItemDeleted(state, "task-1", alreadyMarked)

	state.DeletedTaskIDsMu.Lock()
	_, isolated := state.DeletedTaskIDs["task-1"]
	state.DeletedTaskIDsMu.Unlock()
	if !isolated {
		t.Fatal("failed removal rolled back an isolation mark created by another flow")
	}
}

func TestRollbackQueueItemDeletedRemovesOnlyNewMark(t *testing.T) {
	state := &app.State{DeletedTaskIDs: map[string]struct{}{}}
	alreadyMarked := markQueueItemDeleted(state, "task-1")
	rollbackQueueItemDeleted(state, "task-1", alreadyMarked)

	state.DeletedTaskIDsMu.Lock()
	_, isolated := state.DeletedTaskIDs["task-1"]
	state.DeletedTaskIDsMu.Unlock()
	if isolated {
		t.Fatal("failed removal kept the isolation mark created only by that request")
	}
}

func TestPublicPurchaseHistoryEntryScrubsLegacyError(t *testing.T) {
	legacy := `HTTP 500: {"message":"api secret"}`
	failed := publicPurchaseHistoryEntry(types.PurchaseHistoryEntry{
		Status:       "failed",
		ErrorMessage: &legacy,
	})
	if failed.ErrorMessage == nil || *failed.ErrorMessage != "下单失败，请稍后重试" {
		t.Fatalf("failed history error = %v", failed.ErrorMessage)
	}
	if strings.Contains(*failed.ErrorMessage, "api secret") {
		t.Fatalf("legacy provider text leaked: %q", *failed.ErrorMessage)
	}

	uncertainMessage := `{"body":"provider response"}`
	uncertain := publicPurchaseHistoryEntry(types.PurchaseHistoryEntry{
		Status:       "uncertain",
		ErrorMessage: &uncertainMessage,
	})
	if uncertain.ErrorMessage == nil || *uncertain.ErrorMessage != "下单结果不确定，请人工核查" {
		t.Fatalf("uncertain history error = %v", uncertain.ErrorMessage)
	}

	stableMessage := "结账失败，请稍后重试"
	stable := publicPurchaseHistoryEntry(types.PurchaseHistoryEntry{
		Status:       "failed",
		ErrorMessage: &stableMessage,
	})
	if stable.ErrorMessage == nil || *stable.ErrorMessage != stableMessage {
		t.Fatalf("stable history error = %v", stable.ErrorMessage)
	}
}
