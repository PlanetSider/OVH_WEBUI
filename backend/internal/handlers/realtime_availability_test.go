package handlers

import (
	"testing"

	"github.com/ovh-webui/server/internal/db"
)

func TestRealtimeAvailabilityURL(t *testing.T) {
	tests := []struct {
		region string
		want   string
		ok     bool
	}{
		{region: "", want: "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities", ok: true},
		{region: "EU", want: "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities", ok: true},
		{region: "ca", want: "https://ca.api.ovh.com/v1/dedicated/server/datacenter/availabilities", ok: true},
		{region: "us", want: "", ok: false},
		{region: "https://example.com", want: "", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.region, func(t *testing.T) {
			got, ok := realtimeAvailabilityURL(tc.region)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("realtimeAvailabilityURL(%q) = (%q, %v), want (%q, %v)", tc.region, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestComparisonCatalogForRegion(t *testing.T) {
	tests := []struct {
		region         string
		wantURL        string
		wantSubsidiary string
		wantLabel      string
		ok             bool
	}{
		{
			region:         "EU",
			wantURL:        "https://eu.api.ovh.com/v1/order/catalog/public/eco?ovhSubsidiary=IE",
			wantSubsidiary: "IE",
			wantLabel:      "ovh-ie",
			ok:             true,
		},
		{
			region:         " ca ",
			wantURL:        "https://ca.api.ovh.com/v1/order/catalog/public/eco?ovhSubsidiary=CA",
			wantSubsidiary: "CA",
			wantLabel:      "ovh-ca",
			ok:             true,
		},
		{region: "", ok: false},
		{region: "us", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.region, func(t *testing.T) {
			got, ok := comparisonCatalogForRegion(tc.region)
			if ok != tc.ok {
				t.Fatalf("comparisonCatalogForRegion(%q) ok = %v, want %v", tc.region, ok, tc.ok)
			}
			if got.URL != tc.wantURL || got.Subsidiary != tc.wantSubsidiary || got.Label != tc.wantLabel {
				t.Fatalf(
					"comparisonCatalogForRegion(%q) = %#v, want URL=%q Subsidiary=%q Label=%q",
					tc.region, got, tc.wantURL, tc.wantSubsidiary, tc.wantLabel,
				)
			}
		})
	}
}

func TestParseComparisonPlanCodes(t *testing.T) {
	planCodes, err := parseComparisonPlanCodes([]byte(`{
		"plans": [
			{"planCode": "  RISE-1  "},
			{"planCode": "rise-2"},
			{"planCode": "RISE-1"},
			{"planCode": ""}
		]
	}`))
	if err != nil {
		t.Fatalf("parseComparisonPlanCodes() error = %v", err)
	}
	if len(planCodes) != 2 {
		t.Fatalf("parseComparisonPlanCodes() returned %d plan codes, want 2", len(planCodes))
	}
	for _, code := range []string{"rise-1", "rise-2"} {
		if _, ok := planCodes[code]; !ok {
			t.Errorf("parseComparisonPlanCodes() missing %q", code)
		}
	}
}

func TestParseComparisonPlanCodesRejectsInvalidOrEmptyCatalog(t *testing.T) {
	for _, raw := range []string{`not-json`, `{"plans": []}`, `{"plans": [{"planCode": " "}]}`} {
		if _, err := parseComparisonPlanCodes([]byte(raw)); err == nil {
			t.Errorf("parseComparisonPlanCodes(%q) returned nil error", raw)
		}
	}
}

func TestAggregatePreaddedServerItemsGroupsByPlanCode(t *testing.T) {
	items := []map[string]interface{}{
		{
			"planCode": " RISE-1 ",
			"server":   "Rise 1",
			"fqn":      "rise-1.ram-64.storage-a",
			"memory":   "64 GB",
			"storage":  "2x SSD",
			"datacenters": []interface{}{
				map[string]interface{}{"datacenter": "GRA", "availability": "1H-low"},
				map[string]interface{}{"datacenter": "BHS", "availability": "unavailable"},
			},
		},
		{
			"planCode": "rise-1",
			"server":   "Rise 1",
			"fqn":      "rise-1.ram-128.storage-b",
			"memory":   "128 GB",
			"storage":  "4x NVMe",
			"datacenters": []interface{}{
				map[string]interface{}{"datacenter": "gra", "availability": "unavailable"},
				map[string]interface{}{"datacenter": "bhs", "availability": "72H"},
			},
		},
		// 相同 FQN 不应重复计入配置数。
		{
			"planCode": "RISE-1",
			"fqn":      "rise-1.ram-128.storage-b",
			"memory":   "128 GB",
			"storage":  "4x NVMe",
		},
	}

	results := aggregatePreaddedServerItems("eu", items)
	if len(results) != 1 {
		t.Fatalf("aggregatePreaddedServerItems() returned %d items, want 1", len(results))
	}
	got := results[0]
	if got.PlanCode != "RISE-1" || got.VariantCount != 2 {
		t.Fatalf("aggregated item = %#v, want planCode RISE-1 and 2 variants", got)
	}
	if len(got.Memories) != 2 || len(got.Storages) != 2 || len(got.Datacenters) != 2 {
		t.Fatalf("aggregated options/datacenters incomplete: %#v", got)
	}
	if len(got.Regions) != 1 || got.Regions[0] != "eu" {
		t.Fatalf("aggregated regions = %v, want [eu]", got.Regions)
	}
	for _, datacenter := range got.Datacenters {
		if datacenter.AvailableVariants != 1 || datacenter.ReportedVariants != 2 {
			t.Errorf("datacenter %#v, want 1/2 available variants", datacenter)
		}
	}
}

func TestMergePreaddedServerPageItemsAcrossRegions(t *testing.T) {
	items := []db.PreaddedServerPageItem{
		{
			PlanCode:     "RISE-1",
			Server:       "Rise 1",
			Regions:      []string{"eu"},
			VariantCount: 2,
			Memories:     []string{"64 GB"},
			Datacenters: []db.PreaddedServerDatacenter{
				{Datacenter: "gra", Availability: "1H-low", AvailableVariants: 1, ReportedVariants: 2},
			},
		},
		{
			PlanCode:     "rise-1",
			Regions:      []string{"ca"},
			VariantCount: 3,
			Memories:     []string{"128 GB"},
			Datacenters: []db.PreaddedServerDatacenter{
				{Datacenter: "gra", Availability: "unavailable", AvailableVariants: 0, ReportedVariants: 3},
			},
		},
	}

	results := mergePreaddedServerPageItems(items)
	if len(results) != 1 {
		t.Fatalf("mergePreaddedServerPageItems() returned %d items, want 1", len(results))
	}
	got := results[0]
	if got.VariantCount != 5 || len(got.Regions) != 2 || len(got.Memories) != 2 {
		t.Fatalf("merged item = %#v", got)
	}
	if len(got.Datacenters) != 1 || got.Datacenters[0].AvailableVariants != 1 || got.Datacenters[0].ReportedVariants != 5 {
		t.Fatalf("merged datacenters = %#v", got.Datacenters)
	}
}

func TestMergePreaddedAvailabilityStatus(t *testing.T) {
	tests := []struct {
		current   string
		candidate string
		want      string
	}{
		{current: "", candidate: "", want: "unknown"},
		{current: "unknown", candidate: "unavailable", want: "unavailable"},
		{current: "unavailable", candidate: "unknown", want: "unavailable"},
		{current: "unavailable", candidate: "1H-low", want: "1H-low"},
		{current: "72H", candidate: "1H-high", want: "72H"},
	}
	for _, tc := range tests {
		if got := mergePreaddedAvailabilityStatus(tc.current, tc.candidate); got != tc.want {
			t.Errorf("mergePreaddedAvailabilityStatus(%q, %q) = %q, want %q", tc.current, tc.candidate, got, tc.want)
		}
	}
}
