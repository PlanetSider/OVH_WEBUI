package handlers

import (
	"testing"

	"github.com/ovh-webui/server/internal/app"
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
