package app

import (
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

func TestServerListCacheSnapshotCopiesDataAndTimestamp(t *testing.T) {
	cache := NewServerListCache()
	refreshedAt := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	cache.SetAt([]types.ServerPlan{{PlanCode: "plan-1"}}, refreshedAt)

	plans, timestamp, valid := cache.Snapshot()
	if !valid || timestamp == nil || !timestamp.Equal(refreshedAt) {
		t.Fatalf("Snapshot() timestamp=%v valid=%v, want %v true", timestamp, valid, refreshedAt)
	}
	plans[0].PlanCode = "changed"
	*timestamp = time.Time{}

	again, againTimestamp, againValid := cache.Snapshot()
	if !againValid || againTimestamp == nil || !againTimestamp.Equal(refreshedAt) {
		t.Fatalf("second Snapshot() timestamp=%v valid=%v, want %v true", againTimestamp, againValid, refreshedAt)
	}
	if len(again) != 1 || again[0].PlanCode != "plan-1" {
		t.Fatalf("Snapshot() exposed mutable cache data: %#v", again)
	}
}

func TestServerListCacheClearRemovesDataAndTimestamp(t *testing.T) {
	cache := NewServerListCache()
	cache.Set([]types.ServerPlan{{PlanCode: "plan-1"}})
	cache.Clear()

	plans, timestamp, valid := cache.Snapshot()
	if valid || timestamp != nil || len(plans) != 0 {
		t.Fatalf("Snapshot() after Clear() = plans=%v timestamp=%v valid=%v", plans, timestamp, valid)
	}
}

func TestServerListCacheExpiredSnapshotKeepsFallbackData(t *testing.T) {
	cache := NewServerListCache()
	cache.SetAt([]types.ServerPlan{{PlanCode: "plan-1"}}, time.Now().Add(-cache.TTL-time.Minute))

	plans, timestamp, valid := cache.Snapshot()
	if valid || timestamp == nil || len(plans) != 1 || plans[0].PlanCode != "plan-1" {
		t.Fatalf("expired Snapshot() = plans=%v timestamp=%v valid=%v", plans, timestamp, valid)
	}
}
