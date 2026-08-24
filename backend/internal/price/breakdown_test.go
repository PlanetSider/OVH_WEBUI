package price

import (
	"math"
	"testing"
)

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
