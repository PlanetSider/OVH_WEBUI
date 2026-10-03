package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

type memoryStore struct{ config types.Config }

func (s *memoryStore) Get() types.Config { return s.config }
func (s *memoryStore) Set(config types.Config) error {
	s.config = config
	return nil
}

func TestRefreshFreeProviderPersistsDerivedRates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("base") != "USD" {
			t.Fatalf("expected USD base, got %q", r.URL.Query().Get("base"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"timestamp":1700000000,"base":"USD","rates":{"CNY":7.2,"EUR":0.9,"CAD":1.35}}`))
	}))
	defer server.Close()

	store := &memoryStore{config: types.Config{ExchangeProvider: ProviderFreeExchangeRateAPI}}
	service := New(store)
	service.freeURL = server.URL
	service.now = func() time.Time { return time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC) }

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got, want := store.config.ExchangeUSDCNY, 7.2; got != want {
		t.Fatalf("USD/CNY = %v, want %v", got, want)
	}
	if got, want := store.config.ExchangeEURCNY, 8.0; got != want {
		t.Fatalf("EUR/CNY = %v, want %v", got, want)
	}
	if got, want := store.config.ExchangeCADCNY, 5.333333333333333; got != want {
		t.Fatalf("CAD/CNY = %v, want %v", got, want)
	}
	if store.config.ExchangeStatus != "active" || store.config.ExchangeError != "" {
		t.Fatalf("unexpected status: %q error %q", store.config.ExchangeStatus, store.config.ExchangeError)
	}
}

func TestRefreshExchangeRateAPIUsesKeyAndConversionRates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/exchange-key/latest/USD"; got != want {
			t.Fatalf("request path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","conversion_rates":{"CNY":7.2,"EUR":0.9,"CAD":1.35}}`))
	}))
	defer server.Close()

	store := &memoryStore{config: types.Config{
		ExchangeProvider: ProviderExchangeRateAPI,
		ExchangeAPIKey:   "exchange-key",
	}}
	service := New(store)
	service.exchangeAPIURL = server.URL
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got, want := store.config.ExchangeEURCNY, 8.0; got != want {
		t.Fatalf("EUR/CNY = %v, want %v", got, want)
	}
	if store.config.ExchangeStatus != "active" {
		t.Fatalf("status = %q, want active", store.config.ExchangeStatus)
	}
}

func TestHistoricalRateUsesKeyAndCachesResult(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("X-API-Key"); got != "frank-key" {
			t.Fatalf("X-API-Key = %q", got)
		}
		if r.URL.Query().Get("date") != "2024-02-03" || r.URL.Query().Get("base") != "CAD" || r.URL.Query().Get("quotes") != "CNY" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"base":"CAD","quote":"CNY","rate":5.1,"date":"2024-02-03"}]`))
	}))
	defer server.Close()

	store := &memoryStore{config: types.Config{FrankfurterAPIKey: "frank-key"}}
	service := New(store)
	service.frankfurterURL = server.URL
	first, err := service.HistoricalRate(context.Background(), "2024-02-03T18:00:00Z", "cad", "cny")
	if err != nil || first != 5.1 {
		t.Fatalf("first HistoricalRate() = %v, %v", first, err)
	}
	second, err := service.HistoricalRate(context.Background(), "2024-02-03", "CAD", "CNY")
	if err != nil || second != first {
		t.Fatalf("cached HistoricalRate() = %v, %v", second, err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestHistoricalRateWithoutKeyIsUnavailable(t *testing.T) {
	service := New(&memoryStore{})
	_, err := service.HistoricalRate(context.Background(), "2024-02-03", "CAD", "CNY")
	if err == nil || !strings.Contains(err.Error(), "未配置 API Key") {
		t.Fatalf("error = %v, want missing-key error", err)
	}
}

func TestResolveDisplayAmountUsesConfiguredCNYRate(t *testing.T) {
	store := &memoryStore{config: types.Config{
		ExchangeProvider:    ProviderFreeExchangeRateAPI,
		ExchangeDisplayMode: DisplayCNY,
		ExchangeStatus:      "active",
		ExchangeEURCNY:      8.0,
		ExchangeUSDCNY:      7.2,
		ExchangeCADCNY:      5.3,
	}}
	service := New(store)
	got, currency, ok := service.ResolveDisplayAmount(10, "eur")
	if !ok || got != 80 || currency != DisplayCNYCode {
		t.Fatalf("ResolveDisplayAmount() = %v, %q, %v; want 80, CNY, true", got, currency, ok)
	}
}

func TestResolveDisplayAmountKeepsCNYSourceAmountWithoutProviderRefresh(t *testing.T) {
	store := &memoryStore{config: types.Config{
		ExchangeDisplayMode: DisplayCNY,
		ExchangeStatus:      "inactive",
	}}
	service := New(store)
	got, currency, ok := service.ResolveDisplayAmount(12, "cny")
	if !ok || got != 12 || currency != DisplayCNYCode {
		t.Fatalf("CNY source ResolveDisplayAmount() = %v, %q, %v; want 12, CNY, true", got, currency, ok)
	}
}

func TestResolveDisplayAmountKeepsOriginalModeAndRejectsUnavailableCNYRate(t *testing.T) {
	store := &memoryStore{config: types.Config{
		ExchangeProvider:    ProviderFreeExchangeRateAPI,
		ExchangeDisplayMode: DisplayOriginal,
		ExchangeStatus:      "inactive",
	}}
	service := New(store)
	got, currency, ok := service.ResolveDisplayAmount(10, "EUR")
	if !ok || got != 10 || currency != "EUR" {
		t.Fatalf("original ResolveDisplayAmount() = %v, %q, %v; want 10, EUR, true", got, currency, ok)
	}
	store.config.ExchangeDisplayMode = DisplayCNY
	got, currency, ok = service.ResolveDisplayAmount(10, "EUR")
	if ok || got != 0 || currency != DisplayCNYCode {
		t.Fatalf("unavailable CNY ResolveDisplayAmount() = %v, %q, %v; want 0, CNY, false", got, currency, ok)
	}
}

func TestCurrentRateRequiresActiveStatus(t *testing.T) {
	store := &memoryStore{config: types.Config{
		ExchangeProvider: ProviderFreeExchangeRateAPI,
		ExchangeStatus:   "inactive",
		ExchangeEURCNY:   7.8,
		ExchangeUSDCNY:   7.2,
		ExchangeCADCNY:   5.3,
	}}
	service := New(store)
	if _, ok := service.CurrentRate("CAD", "USD"); ok {
		t.Fatal("CurrentRate() should be unavailable while exchange status is inactive")
	}
	store.config.ExchangeStatus = "active"
	got, ok := service.CurrentRate("CAD", "USD")
	if !ok || got != store.config.ExchangeCADCNY/store.config.ExchangeUSDCNY {
		t.Fatalf("CurrentRate() = %v, %v", got, ok)
	}
}

func TestNextMidnightUsesAsiaShanghai(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 23, 59, 59, 0, location)
	got := NextMidnight(now, location)
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("NextMidnight() = %v, want %v", got, want)
	}
}
