package handlers

import "testing"

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
