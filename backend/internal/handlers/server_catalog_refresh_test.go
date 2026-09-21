package handlers

import (
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

func TestNextHourlyRefreshUsesLocalClockBoundary(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.September, 1, 10, 37, 42, 123, location)
	want := time.Date(2026, time.September, 1, 11, 0, 0, 0, location)

	if got := nextHourlyRefresh(now); !got.Equal(want) || got.Location() != location {
		t.Fatalf("nextHourlyRefresh(%v) = %v, want %v", now, got, want)
	}
}

func TestTrackedCatalogPlansUsesPreviousNameForQueue(t *testing.T) {
	state := &app.State{Queue: []types.QueueItem{{PlanCode: "24sk502", Status: "running"}}}
	tracked := trackedCatalogPlans(state, nil, []types.ServerPlan{{PlanCode: "24sk502", Name: "KS-5"}})
	if tracked["24sk502"].ServerName != "KS-5" {
		t.Fatalf("tracked plan = %#v, want previous catalog name", tracked["24sk502"])
	}
}

func TestAdvanceDiscontinuedPlansRetainsLastCatalogNameForQueue(t *testing.T) {
	tracked := map[string]trackedCatalogPlan{
		"24sk502": {Modes: map[string]struct{}{"抢购": {}}},
	}
	current := map[string]types.ServerPlan{"24sk502": {PlanCode: "24sk502", Name: "KS-5"}}
	start := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	records, transitions := advanceDiscontinuedPlans(nil, tracked, current, start)
	if len(transitions) != 0 || records["24sk502"].ServerName != "KS-5" {
		t.Fatalf("active catalog state = %#v transitions=%#v", records, transitions)
	}
	records, transitions = advanceDiscontinuedPlans(records, tracked, nil, start.Add(time.Hour))
	if len(transitions) != 1 || transitions[0].ServerName != "KS-5" {
		t.Fatalf("missing transition = %#v, want retained model name", transitions)
	}
}

func TestAdvanceDiscontinuedPlansRequiresOneFullHourAndRecovers(t *testing.T) {
	tracked := map[string]trackedCatalogPlan{
		"24sk502": {Modes: map[string]struct{}{"抢购": {}}, ServerName: "KS-5"},
	}
	start := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	records, transitions := advanceDiscontinuedPlans(nil, tracked, nil, start)
	if len(transitions) != 0 {
		t.Fatalf("initial missing transition count = %d, want 0", len(transitions))
	}
	if records["24sk502"].Discontinued {
		t.Fatal("initial missing plan was marked discontinued")
	}

	records, transitions = advanceDiscontinuedPlans(records, tracked, nil, start.Add(59*time.Minute))
	if len(transitions) != 0 || records["24sk502"].Discontinued {
		t.Fatalf("before one hour transitions=%#v records=%#v", transitions, records)
	}

	records, transitions = advanceDiscontinuedPlans(records, tracked, nil, start.Add(time.Hour))
	if len(transitions) != 1 || transitions[0].Recovered || transitions[0].Mode != "抢购" {
		t.Fatalf("discontinued transition = %#v, want one purchase transition", transitions)
	}
	if !records["24sk502"].Discontinued {
		t.Fatal("plan was not marked discontinued after one hour")
	}

	_, transitions = advanceDiscontinuedPlans(records, tracked, nil, start.Add(2*time.Hour))
	if len(transitions) != 0 {
		t.Fatalf("repeated missing refresh emitted duplicate transitions: %#v", transitions)
	}

	current := map[string]types.ServerPlan{"24sk502": {PlanCode: "24sk502", Name: "KS-5"}}
	recovered, transitions := advanceDiscontinuedPlans(records, tracked, current, start.Add(2*time.Hour+time.Minute))
	if len(transitions) != 1 || !transitions[0].Recovered || transitions[0].ServerName != "KS-5" {
		t.Fatalf("recovery transition = %#v, want one recovery transition", transitions)
	}
	if len(recovered) != 1 || recovered["24sk502"].Discontinued || recovered["24sk502"].ServerName != "KS-5" {
		t.Fatalf("recovered tracker state = %#v, want one non-discontinued named record", recovered)
	}
}
