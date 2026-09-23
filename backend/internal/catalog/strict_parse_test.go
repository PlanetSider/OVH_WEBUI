package catalog

import (
	"strings"
	"testing"
)

func TestParseEcoCatalogRejectsMalformedPayload(t *testing.T) {
	if _, err := parseEcoCatalog(strings.NewReader(`{"plans":`)); err == nil {
		t.Fatal("malformed catalog should fail")
	}
}

func TestParseEcoCatalogSkipsPlansWithoutCode(t *testing.T) {
	plans, err := parseEcoCatalog(strings.NewReader(`{"plans":[{"invoiceName":"missing-code"},{"planCode":"valid"}]}`))
	if err != nil {
		t.Fatalf("parseEcoCatalog returned error: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("parsed plans = %d, want 1", len(plans))
	}
	if _, ok := plans["valid"]; !ok {
		t.Fatal("valid plan was not retained")
	}
}

func TestNormalizeAvailability(t *testing.T) {
	tests := map[string]string{
		"  AVAILABLE  ":    "available",
		"1H-high":          "1h-high",
		"1H-low":           "1h-low",
		"24H":              "24h",
		"72H":              "72h",
		"unavailable":      "unavailable",
		"unknown":          "",
		"future-inventory": "",
		"  ":               "",
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
