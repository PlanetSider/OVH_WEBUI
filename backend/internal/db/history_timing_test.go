package db

import (
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestHistoryTimingRoundTripsAndLegacyRowsRemainEmpty(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.ReplaceHistory([]types.PurchaseHistoryEntry{
		{ID: "legacy", PlanCode: "plan-legacy", Datacenter: "gra", Status: "failed", PurchaseTime: types.NowISO()},
		{ID: "timed", PlanCode: "plan-timed", Datacenter: "rbx", Status: "success", PurchaseTime: types.NowISO(), Timing: []types.PhaseTiming{{Name: "查库存", Ms: 42}}, TotalMs: 84},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("history count = %d, want 2", len(items))
	}
	for _, item := range items {
		switch item.ID {
		case "legacy":
			if item.TotalMs != 0 || len(item.Timing) != 0 {
				t.Fatalf("legacy timing should remain empty: %#v", item)
			}
		case "timed":
			if item.TotalMs != 84 || len(item.Timing) != 1 || item.Timing[0].Name != "查库存" || item.Timing[0].Ms != 42 {
				t.Fatalf("timing did not round trip: %#v", item)
			}
		}
	}
}
