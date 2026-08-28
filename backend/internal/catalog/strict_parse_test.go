package catalog

import "testing"

func TestFindCatalogPlan(t *testing.T) {
	catalog := map[string]interface{}{
		"plans": []interface{}{
			map[string]interface{}{"planCode": "24sk102", "invoiceName": "KS-1"},
		},
	}
	plan, err := findCatalogPlan(catalog, "24sk102")
	if err != nil {
		t.Fatalf("findCatalogPlan returned error: %v", err)
	}
	if got := getString(plan, "invoiceName", ""); got != "KS-1" {
		t.Fatalf("invoiceName = %q, want KS-1", got)
	}
}

func TestFindCatalogPlanRejectsIncompleteCatalog(t *testing.T) {
	if _, err := findCatalogPlan(map[string]interface{}{}, "24sk102"); err == nil {
		t.Fatal("missing plans should fail")
	}
	if _, err := findCatalogPlan(map[string]interface{}{"plans": []interface{}{}}, "24sk102"); err == nil {
		t.Fatal("missing planCode should fail")
	}
}

func TestNormalizeAvailability(t *testing.T) {
	tests := map[string]string{
		"  AVAILABLE  ":  "available",
		"1H-high":         "1h-high",
		"1H-low":          "1h-low",
		"24H":             "24h",
		"72H":             "72h",
		"unavailable":     "unavailable",
		"unknown":         "",
		"future-inventory": "",
		"  ":              "",
	}
	for input, want := range tests {
		if got := normalizeAvailability(input); got != want {
			t.Fatalf("normalizeAvailability(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAvailabilityExplicitlyAvailable(t *testing.T) {
	tests := map[string]bool{
		"available": true, " AVAILABLE ": true, "1H-high": true,
		"1H-low": true, "24H": true, "72H": true,
		"unavailable": false, "unknown": false, "": false, "future-status": false,
	}
	for input, want := range tests {
		if got := AvailabilityExplicitlyAvailable(input); got != want {
			t.Fatalf("AvailabilityExplicitlyAvailable(%q) = %v, want %v", input, got, want)
		}
	}
}
