package monitor

import (
	"testing"

	"github.com/ovh-webui/server/internal/price"
)

func TestFormatNotificationPriceUsesFirstMonthTotal(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		MonthlyWithoutTax:    12.4,
		InstallWithoutTax:    3.1,
		TotalWithoutTax:      15.5,
		TotalWithoutTaxKnown: true,
		Currency:             "EUR",
		TotalKnown:           true,
		BreakdownKnown:       true,
	})
	want := "月费: €12.40/月\n安装费: €3.10\n首月总价: €15.50"
	if got != want {
		t.Fatalf("formatNotificationPrice() = %q, want %q", got, want)
	}
}

func TestFormatNotificationPriceFallsBackToCartTotal(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		TotalWithoutTax:      20,
		Currency:             "USD",
		TotalWithoutTaxKnown: true,
	})
	if got != "首月总价: $20.00" {
		t.Fatalf("formatNotificationPrice() = %q", got)
	}
}

func TestFormatNotificationPriceShowsNonMonthlyDuration(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		TotalWithoutTax:      240,
		Currency:             "USD",
		Duration:             "P12M",
		TotalWithoutTaxKnown: true,
	})
	if got != "购物车总价（P12M）: $240.00" {
		t.Fatalf("formatNotificationPrice() = %q", got)
	}
}

func TestFormatNotificationPriceMarksUnknownCurrency(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		MonthlyWithoutTax:    12.4,
		TotalWithoutTax:      12.4,
		Currency:             "",
		TotalWithoutTaxKnown: true,
		BreakdownKnown:       true,
	})
	want := "月费: 币种未知 12.40/月\n安装费: 无\n首月总价: 币种未知 12.40"
	if got != want {
		t.Fatalf("formatNotificationPrice() = %q, want %q", got, want)
	}
}
func TestUnavailablePriceTextKeepsThreeFields(t *testing.T) {
	if got := unavailablePriceText(); got != "月费: 暂不可用\n安装费: 暂不可用\n首月总价: 暂不可用" {
		t.Fatalf("unavailablePriceText() = %q", got)
	}
}

func TestFormatDisplayPriceUsesPlainLabels(t *testing.T) {
	got := FormatDisplayPrice(price.DisplayPrice{
		MonthlyWithoutTax:    12.4,
		InstallWithoutTax:    3.1,
		TotalWithoutTax:      15.5,
		TotalWithoutTaxKnown: true,
		Currency:             "EUR",
		BreakdownKnown:       true,
	})
	want := "月费: €12.40/月\n安装费: €3.10\n总价: €15.50"
	if got != want {
		t.Fatalf("FormatDisplayPrice() = %q, want %q", got, want)
	}
}
