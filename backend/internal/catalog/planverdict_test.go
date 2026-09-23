package catalog

import (
	"errors"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

func TestClassifyPlanDistinguishesCatalogAndProbeVerdicts(t *testing.T) {
	resetCatalogTestCaches()
	defer resetCatalogTestCaches()
	oldFetch := fetchSubsidiaryCatalog
	oldProbe := probeRegionHasPlan
	defer func() {
		fetchSubsidiaryCatalog = oldFetch
		probeRegionHasPlan = oldProbe
	}()
	state := &app.State{Accounts: []types.OVHAccount{{ID: "a", Name: "US account", Endpoint: "ovh-us", Zone: "US"}}}
	fetchSubsidiaryCatalog = func(_ *app.State, _ string) (*subsidiaryCatalog, error) {
		return &subsidiaryCatalog{plans: map[string]planConfig{"eco-plan": {addonFamilies: map[string][]string{"memory": {"ram-64g"}}}}, fetchedAt: time.Now()}, nil
	}
	probeRegionHasPlan = func(region, plan string) (bool, error) {
		switch plan {
		case "cross-plan":
			return region == "EU", nil
		case "missing-plan":
			return false, nil
		case "unknown-plan":
			return false, errors.New("probe timeout")
		default:
			return false, nil
		}
	}
	if verdict, _ := ClassifyPlan(state, "a", "eco-plan", "test"); verdict != PlanVerdictOK {
		t.Fatalf("eco verdict = %s, want OK", verdict)
	}
	if verdict, _ := ClassifyPlan(state, "a", "cross-plan", "test"); verdict != PlanVerdictCrossRegion {
		t.Fatalf("cross verdict = %s, want CrossRegion", verdict)
	}
	if verdict, _ := ClassifyPlan(state, "a", "missing-plan", "test"); verdict != PlanVerdictNoSuchPlan {
		t.Fatalf("missing verdict = %s, want NoSuchPlan", verdict)
	}
	if verdict, _ := ClassifyPlan(state, "a", "unknown-plan", "test"); verdict != PlanVerdictUnknown {
		t.Fatalf("unknown verdict = %s, want Unknown", verdict)
	}
}
