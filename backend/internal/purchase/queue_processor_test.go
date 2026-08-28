package purchase

import (
	"context"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

func TestEffectiveRetryInterval(t *testing.T) {
	for _, value := range []int{0, -1, -60} {
		if got := effectiveRetryInterval(types.QueueItem{RetryInterval: value}); got != 60 {
			t.Fatalf("effectiveRetryInterval(%d) = %d, want 60", value, got)
		}
	}
	for _, value := range []int{2, 30, 120} {
		if got := effectiveRetryInterval(types.QueueItem{RetryInterval: value}); got != value {
			t.Fatalf("effectiveRetryInterval(%d) = %d, want %d", value, got, value)
		}
	}
}

func TestProcessQueueTickDoesNotClearIsolationWhenQueueIsEmpty(t *testing.T) {
	state := &app.State{
		QueueProcessorEnabled: true,
		DeletedTaskIDs:       map[string]struct{}{"uncertain-checkout": {}},
	}
	processQueueTick(context.Background(), state)

	state.DeletedTaskIDsMu.Lock()
	_, isolated := state.DeletedTaskIDs["uncertain-checkout"]
	state.DeletedTaskIDsMu.Unlock()
	if !isolated {
		t.Fatal("empty queue cleared an unresolved checkout isolation mark")
	}
}
