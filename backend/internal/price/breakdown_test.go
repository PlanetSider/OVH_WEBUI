package price

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func TestGetInternalWithContextHonorsCancellationBeforeRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := GetInternalWithContext(ctx, nil, "account", "server-plan", "gra", nil)
	if result.Success {
		t.Fatal("GetInternalWithContext() returned success for canceled context")
	}
	if result.Error == "" {
		t.Fatal("GetInternalWithContext() returned empty cancellation error")
	}
}

func TestGetDisplayWithContextHonorsCancellationBeforeCatalog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	display, err := GetDisplayWithContext(ctx, nil, "account", "server-plan", "gra", nil)
	if err == nil {
		t.Fatal("GetDisplayWithContext() returned nil error for canceled context")
	}
	if display.TotalKnown || display.BreakdownKnown {
		t.Fatalf("canceled GetDisplayWithContext() returned price: %+v", display)
	}
}

func TestCalculateFromCatalogSeparatesMonthlyAndInstallation(t *testing.T) {
	catalog := &publicCatalog{
		Plans: []catalogPlan{{
			PlanCode: "server-plan",
			Pricings: []catalogPricing{
				{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 10e8, Tax: 2e8},
				{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 3e8, Tax: 0.6e8, Capacities: []string{"installation"}},
			},
		}},
		Addons: []catalogPlan{{
			PlanCode: "addon-ram",
			Pricings: []catalogPricing{
				{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 2e8, Tax: 0.4e8},
				{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 1e8, Tax: 0.2e8, Capacities: []string{"installation"}},
			},
		}},
	}

	monthly, install, ok := calculateFromCatalog(catalog, "server-plan", []string{"addon-ram"})
	if !ok {
		t.Fatal("calculateFromCatalog() returned ok=false")
	}
	if math.Abs(monthly.price-12) > 1e-9 || math.Abs(monthly.tax-2.4) > 1e-9 {
		t.Fatalf("monthly = %+v, want price=12 tax=2.4", monthly)
	}
	if math.Abs(install.price-4) > 1e-9 || math.Abs(install.tax-0.8) > 1e-9 {
		t.Fatalf("install = %+v, want price=4 tax=0.8", install)
	}
}

func TestCalculateFromCatalogRequiresMonthlyPrice(t *testing.T) {
	catalog := &publicCatalog{Plans: []catalogPlan{{
		PlanCode: "server-plan",
		Pricings: []catalogPricing{{
			Interval: 1, IntervalUnit: "month", Mode: "default", Price: 3e8,
			Capacities: []string{"installation"},
		}},
	}}}

	if _, _, ok := calculateFromCatalog(catalog, "server-plan", nil); ok {
		t.Fatal("calculateFromCatalog() returned ok=true for installation-only pricing")
	}
}

func TestCalculateFromCatalogRejectsMissingAddon(t *testing.T) {
	catalog := &publicCatalog{Plans: []catalogPlan{{
		PlanCode: "server-plan",
		Pricings: []catalogPricing{{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 3e8}},
	}}}

	if _, _, ok := calculateFromCatalog(catalog, "server-plan", []string{"missing-addon"}); ok {
		t.Fatal("calculateFromCatalog() returned ok=true for missing addon")
	}
}

func TestCalculateFromCatalogRejectsAddonWithoutMonthlyPrice(t *testing.T) {
	catalog := &publicCatalog{
		Plans: []catalogPlan{{
			PlanCode: "server-plan",
			Pricings: []catalogPricing{{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 3e8}},
		}},
		Addons: []catalogPlan{{
			PlanCode: "addon-install-only",
			Pricings: []catalogPricing{{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 1e8, Capacities: []string{"installation"}}},
		}},
	}

	if _, _, ok := calculateFromCatalog(catalog, "server-plan", []string{"addon-install-only"}); ok {
		t.Fatal("calculateFromCatalog() returned ok=true for addon without monthly price")
	}
}

func TestCalculateFromCatalogIgnoresBlankOptions(t *testing.T) {
	catalog := &publicCatalog{Plans: []catalogPlan{{
		PlanCode: "server-plan",
		Pricings: []catalogPricing{{Interval: 1, IntervalUnit: "month", Mode: "default", Price: 3e8}},
	}}}

	monthly, install, ok := calculateFromCatalog(catalog, "server-plan", []string{" ", "\t"})
	if !ok {
		t.Fatal("calculateFromCatalog() returned ok=false for blank options")
	}
	if monthly.price != 3 || monthly.tax != 0 || install.price != 0 || install.tax != 0 {
		t.Fatalf("unexpected amounts: monthly=%+v install=%+v", monthly, install)
	}
}

func TestNormalizeOptionCodesTrimsAndSkipsBlank(t *testing.T) {
	got := normalizeOptionCodes([]string{" addon-a ", "", "\taddon-b\n", " addon-a ", "  "})
	want := []string{"addon-a", "addon-b", "addon-a"}
	if len(got) != len(want) {
		t.Fatalf("normalizeOptionCodes() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeOptionCodes() = %#v, want %#v", got, want)
		}
	}
}

func TestDisplayPriceFromSummaryParsesTaxInclusiveTotal(t *testing.T) {
	display := displayPriceFromSummary(&PriceInfo{Prices: map[string]interface{}{
		"withTax":      "12.50",
		"currencyCode": "EUR",
	}})
	if !display.TotalKnown || math.Abs(display.TotalWithTax-12.5) > 1e-9 || display.Currency != "EUR" {
		t.Fatalf("displayPriceFromSummary() = %+v", display)
	}
}

func TestDisplayPriceFromSummaryRejectsMissingTotal(t *testing.T) {
	display := displayPriceFromSummary(&PriceInfo{Prices: map[string]interface{}{
		"withTax":      "not-a-number",
		"currencyCode": "EUR",
	}})
	if display.TotalKnown {
		t.Fatalf("displayPriceFromSummary() marked invalid total as known: %+v", display)
	}
}

func TestExtractPriceFieldSupportsOVHAmountObject(t *testing.T) {
	value, currency := extractPriceField(map[string]interface{}{
		"value":        json.Number("123.45"),
		"currencyCode": "EUR",
	})
	f, ok := value.(float64)
	if !ok || math.Abs(f-123.45) > 1e-9 || currency != "EUR" {
		t.Fatalf("extractPriceField() = (%#v, %q)", value, currency)
	}
}

func TestValidPriceValueRejectsMalformedValues(t *testing.T) {
	for _, value := range []interface{}{nil, "", "NaN", "-1", math.NaN(), math.Inf(1), struct{}{}} {
		if validPriceValue(value) {
			t.Fatalf("validPriceValue(%#v) = true, want false", value)
		}
	}
	for _, value := range []interface{}{0, 12.5, "12.5", json.Number("3")} {
		if !validPriceValue(value) {
			t.Fatalf("validPriceValue(%#v) = false, want true", value)
		}
	}
}
