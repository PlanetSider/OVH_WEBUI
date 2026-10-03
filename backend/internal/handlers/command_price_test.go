package handlers

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/exchange"
)

func newCommandCNYState(t *testing.T, status string) *app.State {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store := config.New(database)
	cfg := store.Get()
	cfg.ExchangeProvider = exchange.ProviderFreeExchangeRateAPI
	cfg.ExchangeDisplayMode = exchange.DisplayCNY
	cfg.ExchangeStatus = status
	cfg.ExchangeEURCNY = 8
	cfg.ExchangeUSDCNY = 7.2
	cfg.ExchangeCADCNY = 5.3
	if err := store.Set(cfg); err != nil {
		t.Fatal(err)
	}
	return &app.State{Config: store, Exchange: exchange.New(store)}
}

func TestFormatPriceQueryResponseConvertsCNYForConfiguredState(t *testing.T) {
	state := newCommandCNYState(t, "active")
	got := formatPriceQueryResponseForState(state, "24ska01", "gra", "ram-32g", "EUR", 10.5)
	if !containsAll(got, "价格: ¥84.00") {
		t.Fatalf("formatted price = %q, want converted amount", got)
	}
	if containsAny(got, []string{"10.5 EUR", "€10.50"}) {
		t.Fatalf("formatted price still contains source amount: %q", got)
	}
}

func TestFormatPriceQueryResponseMarksUnavailableCNYConversion(t *testing.T) {
	state := newCommandCNYState(t, "inactive")
	got := formatPriceQueryResponseForState(state, "24ska01", "gra", "默认/匹配配置", "EUR", 10.5)
	if !containsAll(got, "价格: 暂不可用") {
		t.Fatalf("formatted price = %q, want unavailable", got)
	}
}

func TestFormatOrderPriceConvertsNumericWithoutTaxValueToCNY(t *testing.T) {
	state := newCommandCNYState(t, "active")
	got := formatOrderPriceForState(state, map[string]interface{}{
		"priceWithoutTax": map[string]interface{}{
			"text":         "10.50 €",
			"value":        "10.5",
			"currencyCode": "EUR",
		},
	})
	if got != "¥84.00" {
		t.Fatalf("formatOrderPriceForState() = %q, want ¥84.00", got)
	}
}

func TestFormatOrderPriceMarksUnavailableCNYConversion(t *testing.T) {
	state := newCommandCNYState(t, "inactive")
	got := formatOrderPriceForState(state, map[string]interface{}{
		"priceWithoutTax": map[string]interface{}{
			"text":         "10.50 €",
			"value":        "10.5",
			"currencyCode": "EUR",
		},
	})
	if got != "价格暂不可用" {
		t.Fatalf("formatOrderPriceForState() = %q, want unavailable", got)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}