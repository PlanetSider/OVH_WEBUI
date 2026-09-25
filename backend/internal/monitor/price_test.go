package monitor

import (
	"testing"

	"github.com/ovh-webui/server/internal/price"
)

func TestFormatNotificationPriceUsesFirstMonthTotal(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		MonthlyWithTax: 12.4,
		InstallWithTax: 3.1,
		TotalWithTax:   15.5,
		Currency:       "EUR",
		TotalKnown:     true,
		BreakdownKnown: true,
	})
	want := "月费: €12.40/月\n安装费: €3.10\n首月总价: €15.50"
	if got != want {
		t.Fatalf("formatNotificationPrice() = %q, want %q", got, want)
	}
}

func TestFormatNotificationPriceFallsBackToCartTotal(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		TotalWithTax: 20,
		Currency:     "USD",
		TotalKnown:   true,
	})
	if got != "首月总价: $20.00" {
		t.Fatalf("formatNotificationPrice() = %q", got)
	}
}

func TestFormatNotificationPriceShowsNonMonthlyDuration(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		TotalWithTax: 240,
		Currency:     "USD",
		Duration:     "P12M",
		TotalKnown:   true,
	})
	if got != "购物车含税总价（P12M）: $240.00" {
		t.Fatalf("formatNotificationPrice() = %q", got)
	}
}

func TestFormatNotificationPriceMarksUnknownCurrency(t *testing.T) {
	got := formatNotificationPrice(price.DisplayPrice{
		MonthlyWithTax: 12.4,
		TotalWithTax:   12.4,
		Currency:       "",
		TotalKnown:     true,
		BreakdownKnown: true,
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
