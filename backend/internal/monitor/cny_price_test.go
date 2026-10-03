package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/exchange"
	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/types"
)

type monitorExchangeStore struct {
	config types.Config
}

func (s *monitorExchangeStore) Get() types.Config { return s.config }
func (s *monitorExchangeStore) Set(config types.Config) error {
	s.config = config
	return nil
}

func newCNYMonitor(status string) *Monitor {
	store := &monitorExchangeStore{config: types.Config{
		ExchangeProvider:    exchange.ProviderFreeExchangeRateAPI,
		ExchangeDisplayMode: exchange.DisplayCNY,
		ExchangeStatus:      status,
		ExchangeEURCNY:      8,
		ExchangeUSDCNY:      7.2,
		ExchangeCADCNY:      5.3,
	}}
	return New(&app.State{Exchange: exchange.New(store)})
}

func TestMonitorNotificationPriceConvertsEveryComponentToCNY(t *testing.T) {
	mon := newCNYMonitor("active")
	display := price.DisplayPrice{
		MonthlyWithoutTax:    10,
		InstallWithoutTax:    2,
		TotalWithoutTax:      12,
		TotalWithoutTaxKnown: true,
		Currency:             "EUR",
		TotalKnown:           true,
		BreakdownKnown:       true,
	}
	got := mon.formatNotificationPrice(display)
	want := "月费: ¥80.00/月\n安装费: ¥16.00\n首月总价: ¥96.00"
	if got != want {
		t.Fatalf("formatNotificationPrice() = %q, want %q", got, want)
	}
	monthly, install, total := mon.displayPricePartsForNotification(display)
	if monthly != "¥80.00/月" || install != "¥16.00" || total != "¥96.00" {
		t.Fatalf("displayPricePartsForNotification() = %q, %q, %q", monthly, install, total)
	}
}

func TestMonitorNotificationPriceDoesNotRelabelWhenCNYRateUnavailable(t *testing.T) {
	mon := newCNYMonitor("inactive")
	got := mon.formatNotificationPrice(price.DisplayPrice{
		MonthlyWithoutTax:    10,
		InstallWithoutTax:    2,
		TotalWithoutTax:      12,
		TotalWithoutTaxKnown: true,
		Currency:             "EUR",
		TotalKnown:           true,
		BreakdownKnown:       true,
	})
	want := "月费: 暂不可用\n安装费: 暂不可用\n首月总价: 暂不可用"
	if got != want {
		t.Fatalf("formatNotificationPrice() = %q, want %q", got, want)
	}
}

func TestMonitorBattleReportConvertsMixedSourceCurrenciesBeforeAggregation(t *testing.T) {
	mon := newCNYMonitor("active")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	eurAmount, usdAmount := 10.0, 10.0
	history := []types.PurchaseHistoryEntry{
		{AccountID: "a", Status: "success", PurchaseTime: now.Add(-time.Hour).Format(time.RFC3339), OrderID: "eur", OrderStatus: "notPaid", Price: &types.PriceInfo{WithoutTax: &eurAmount, CurrencyCode: "EUR"}},
		{AccountID: "a", Status: "success", PurchaseTime: now.Add(-2 * time.Hour).Format(time.RFC3339), OrderID: "usd", OrderStatus: "notPaid", Price: &types.PriceInfo{WithoutTax: &usdAmount, CurrencyCode: "USD"}},
	}
	got := mon.formatBattleReport(history, now, map[string]string{"a": "主账号"})
	if !strings.Contains(got, "未支付金额：¥152.00") {
		t.Fatalf("battle report = %q, want per-entry converted total", got)
	}
}

func TestMonitorNotificationPriceKeepsCNYSourceAmount(t *testing.T) {
	mon := newCNYMonitor("inactive")
	got := mon.formatNotificationPrice(price.DisplayPrice{
		TotalWithoutTax:      12,
		TotalWithoutTaxKnown: true,
		Currency:             "CNY",
	})
	if got != "首月总价: ¥12.00" {
		t.Fatalf("formatNotificationPrice() = %q, want direct CNY amount", got)
	}
}
