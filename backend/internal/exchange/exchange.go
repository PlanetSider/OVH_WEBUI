package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

const (
	ProviderFreeExchangeRateAPI = "FreeExchangeRateApi"
	ProviderExchangeRateAPI     = "ExchangeRateApi"
	DisplayOriginal             = "original"
	DisplayCNY                  = "cny"
	freeExchangeRateAPIURL      = "https://api.exchangerate.fun/latest"
	exchangeRateAPIURL          = "https://v6.exchangerate-api.com/v6"
	frankfurterAPIURL           = "https://api.frankfurter.dev/v2/rates"
)

// ConfigStore is the small persistence contract needed by the exchange service.
// It deliberately avoids importing app.State so provider code remains reusable.
type ConfigStore interface {
	Get() types.Config
	Set(types.Config) error
}

type Rates struct {
	EURCNY float64
	USDCNY float64
	CADCNY float64
}

func (r Rates) Valid() bool {
	return r.EURCNY > 0 && r.USDCNY > 0 && r.CADCNY > 0
}

type Service struct {
	store  ConfigStore
	client *http.Client
	now    func() time.Time

	refreshMu sync.Mutex
	historyMu sync.Mutex
	history   map[string]float64

	freeURL        string
	exchangeAPIURL string
	frankfurterURL string
}

func New(store ConfigStore) *Service {
	return &Service{
		store:          store,
		client:         &http.Client{Timeout: 20 * time.Second},
		now:            time.Now,
		history:        make(map[string]float64),
		freeURL:        freeExchangeRateAPIURL,
		exchangeAPIURL: exchangeRateAPIURL,
		frankfurterURL: frankfurterAPIURL,
	}
}

func NormalizeProvider(value string) string {
	switch strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSpace(value))) {
	case "freeexchangerateapi", "freeexchangeapi", "exchangeratefun":
		return ProviderFreeExchangeRateAPI
	case "exchangerateapi", "exchangerateapi.com", "exchangerate":
		return ProviderExchangeRateAPI
	default:
		return ""
	}
}

func NormalizeDisplayMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), DisplayCNY) {
		return DisplayCNY
	}
	return DisplayOriginal
}

func ProviderConfigured(cfg types.Config) bool {
	provider := NormalizeProvider(cfg.ExchangeProvider)
	if provider == ProviderFreeExchangeRateAPI {
		return true
	}
	return provider == ProviderExchangeRateAPI && strings.TrimSpace(cfg.ExchangeAPIKey) != ""
}

func RatesFromConfig(cfg types.Config) Rates {
	return Rates{EURCNY: cfg.ExchangeEURCNY, USDCNY: cfg.ExchangeUSDCNY, CADCNY: cfg.ExchangeCADCNY}
}

// Refresh fetches the provider's latest USD-based rates and atomically publishes
// them with the config. A failed refresh preserves the last successful numbers.
func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("exchange service is not initialized")
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	cfg := s.store.Get()
	cfg.ExchangeProvider = NormalizeProvider(cfg.ExchangeProvider)
	if cfg.ExchangeProvider == "" {
		return s.markStatus(cfg, "inactive", "未选择汇率提供商")
	}
	if cfg.ExchangeProvider == ProviderExchangeRateAPI && strings.TrimSpace(cfg.ExchangeAPIKey) == "" {
		return s.markStatus(cfg, "inactive", "Exchange Rate API 未配置 API Key")
	}
	rates, err := s.fetchLatest(ctx, cfg.ExchangeProvider, strings.TrimSpace(cfg.ExchangeAPIKey))
	if err != nil {
		cfg.ExchangeStatus = "error"
		cfg.ExchangeError = err.Error()
		if setErr := s.store.Set(cfg); setErr != nil {
			return fmt.Errorf("%w; 保存汇率状态失败: %v", err, setErr)
		}
		return err
	}
	cfg.ExchangeEURCNY = rates.EURCNY
	cfg.ExchangeUSDCNY = rates.USDCNY
	cfg.ExchangeCADCNY = rates.CADCNY
	cfg.ExchangeRatesUpdatedAt = s.now().UTC().Format(time.RFC3339)
	cfg.ExchangeStatus = "active"
	cfg.ExchangeError = ""
	return s.store.Set(cfg)
}

func (s *Service) markStatus(cfg types.Config, status, message string) error {
	cfg.ExchangeStatus = status
	cfg.ExchangeError = message
	return s.store.Set(cfg)
}

func (s *Service) fetchLatest(ctx context.Context, provider, apiKey string) (Rates, error) {
	switch NormalizeProvider(provider) {
	case ProviderFreeExchangeRateAPI:
		return s.fetchFree(ctx)
	case ProviderExchangeRateAPI:
		return s.fetchExchangeRateAPI(ctx, apiKey)
	default:
		return Rates{}, fmt.Errorf("不支持的汇率提供商: %s", provider)
	}
}

type latestPayload struct {
	Base            string             `json:"base"`
	Rates           map[string]float64 `json:"rates"`
	ConversionRates map[string]float64 `json:"conversion_rates"`
	Result          string             `json:"result"`
	Success         *bool              `json:"success"`
	ErrorType       string             `json:"error-type"`
}

func (s *Service) fetchFree(ctx context.Context) (Rates, error) {
	endpoint, err := url.Parse(s.freeURL)
	if err != nil {
		return Rates{}, err
	}
	query := endpoint.Query()
	query.Set("base", "USD")
	endpoint.RawQuery = query.Encode()
	var payload latestPayload
	if err := s.getJSON(ctx, endpoint.String(), "", &payload); err != nil {
		return Rates{}, fmt.Errorf("FreeExchangeRateApi 请求失败: %w", err)
	}
	return ratesFromPayload(payload)
}

func (s *Service) fetchExchangeRateAPI(ctx context.Context, apiKey string) (Rates, error) {
	endpoint := strings.TrimRight(s.exchangeAPIURL, "/") + "/" + url.PathEscape(apiKey) + "/latest/USD"
	var payload latestPayload
	if err := s.getJSON(ctx, endpoint, "", &payload); err != nil {
		return Rates{}, fmt.Errorf("Exchange Rate API 请求失败: %w", err)
	}
	if payload.Result != "" && !strings.EqualFold(payload.Result, "success") {
		if payload.ErrorType == "" {
			payload.ErrorType = payload.Result
		}
		return Rates{}, fmt.Errorf("Exchange Rate API 返回错误: %s", payload.ErrorType)
	}
	return ratesFromPayload(payload)
}

func ratesFromPayload(payload latestPayload) (Rates, error) {
	rates := payload.Rates
	if len(rates) == 0 {
		rates = payload.ConversionRates
	}
	if len(rates) == 0 {
		return Rates{}, errors.New("汇率响应缺少 rates")
	}
	usdCNY, okCNY := rateValue(rates, "CNY")
	usdEUR, okEUR := rateValue(rates, "EUR")
	usdCAD, okCAD := rateValue(rates, "CAD")
	if !okCNY || !okEUR || !okCAD || usdCNY <= 0 || usdEUR <= 0 || usdCAD <= 0 {
		return Rates{}, errors.New("汇率响应缺少 USD/CNY、USD/EUR 或 USD/CAD")
	}
	result := Rates{USDCNY: usdCNY, EURCNY: usdCNY / usdEUR, CADCNY: usdCNY / usdCAD}
	if !result.Valid() {
		return Rates{}, errors.New("汇率响应包含无效数值")
	}
	return result, nil
}

func rateValue(rates map[string]float64, code string) (float64, bool) {
	for key, value := range rates {
		if strings.EqualFold(strings.TrimSpace(key), code) {
			return value, true
		}
	}
	return 0, false
}

func (s *Service) getJSON(ctx context.Context, endpoint, apiKey string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("X-API-Key", strings.TrimSpace(apiKey))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(target); err != nil {
		return err
	}
	return nil
}

// CurrentRate returns a direct conversion rate for the configured current-day cache.
func (s *Service) CurrentRate(from, to string) (float64, bool) {
	if s == nil || s.store == nil {
		return 0, false
	}
	cfg := s.store.Get()
	if cfg.ExchangeStatus != "active" || !ProviderConfigured(cfg) {
		return 0, false
	}
	return ConvertRate(RatesFromConfig(cfg), from, to)
}

// ResolveDisplayAmount resolves one source-currency amount according to the
// configured display mode. In CNY mode it never falls back to the source
// amount when the cached exchange rate is unavailable.
func (s *Service) ResolveDisplayAmount(amount float64, currency string) (float64, string, bool) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if s == nil || s.store == nil {
		return amount, currency, true
	}
	cfg := s.store.Get()
	if NormalizeDisplayMode(cfg.ExchangeDisplayMode) != DisplayCNY {
		return amount, currency, true
	}
	if currency == DisplayCNYCode {
		return amount, DisplayCNYCode, true
	}
	rate, ok := s.CurrentRate(currency, DisplayCNYCode)
	if !ok {
		return 0, DisplayCNYCode, false
	}
	return amount * rate, DisplayCNYCode, true
}

const DisplayCNYCode = "CNY"

func ConvertRate(rates Rates, from, to string) (float64, bool) {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if from == "" || to == "" {
		return 0, false
	}
	if from == to {
		return 1, true
	}
	toCNY := func(code string) (float64, bool) {
		switch code {
		case "CNY":
			return 1, true
		case "EUR":
			return rates.EURCNY, rates.EURCNY > 0
		case "USD":
			return rates.USDCNY, rates.USDCNY > 0
		case "CAD":
			return rates.CADCNY, rates.CADCNY > 0
		default:
			return 0, false
		}
	}
	fromCNY, okFrom := toCNY(from)
	toCNYRate, okTo := toCNY(to)
	if !okFrom || !okTo || toCNYRate <= 0 {
		return 0, false
	}
	return fromCNY / toCNYRate, true
}

func (s *Service) HistoricalRate(ctx context.Context, date, base, quote string) (float64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("exchange service is not initialized")
	}
	normalizedDate, err := normalizeHistoricalDate(date)
	if err != nil {
		return 0, err
	}
	date = normalizedDate
	base = strings.ToUpper(strings.TrimSpace(base))
	quote = strings.ToUpper(strings.TrimSpace(quote))
	if base == "" || quote == "" {
		return 0, errors.New("历史汇率币种不能为空")
	}
	if base == quote {
		return 1, nil
	}
	key := date + ":" + base + ":" + quote
	s.historyMu.Lock()
	if rate, ok := s.history[key]; ok {
		s.historyMu.Unlock()
		return rate, nil
	}
	s.historyMu.Unlock()
	if strings.TrimSpace(s.store.Get().FrankfurterAPIKey) == "" {
		return 0, errors.New("Frankfurter 未配置 API Key")
	}
	endpoint, err := url.Parse(s.frankfurterURL)
	if err != nil {
		return 0, err
	}
	query := endpoint.Query()
	query.Set("date", date)
	query.Set("base", base)
	query.Set("quotes", quote)
	endpoint.RawQuery = query.Encode()
	var payload json.RawMessage
	if err := s.getJSON(ctx, endpoint.String(), s.store.Get().FrankfurterAPIKey, &payload); err != nil {
		return 0, fmt.Errorf("Frankfurter 历史汇率请求失败: %w", err)
	}
	rate, err := parseHistoricalRate(payload, quote)
	if err != nil {
		return 0, err
	}
	s.historyMu.Lock()
	s.history[key] = rate
	s.historyMu.Unlock()
	return rate, nil
}

func normalizeHistoricalDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("历史汇率日期无效: 日期为空")
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.Format("2006-01-02"), nil
	}
	if len(value) >= 10 {
		if parsed, err := time.Parse("2006-01-02", value[:10]); err == nil {
			return parsed.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("历史汇率日期无效: %s", value)
}

func parseHistoricalRate(payload json.RawMessage, quote string) (float64, error) {
	var rows []struct {
		Quote string  `json:"quote"`
		Rate  float64 `json:"rate"`
	}
	if err := json.Unmarshal(payload, &rows); err == nil && len(rows) > 0 {
		for _, row := range rows {
			if strings.EqualFold(row.Quote, quote) && row.Rate > 0 {
				return row.Rate, nil
			}
		}
	}
	var legacy struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(payload, &legacy); err == nil {
		if rate, ok := rateValue(legacy.Rates, quote); ok && rate > 0 {
			return rate, nil
		}
	}
	return 0, errors.New("Frankfurter 历史汇率响应缺少目标币种")
}

// NextMidnight returns the next midnight in the supplied location.
func NextMidnight(now time.Time, location *time.Location) time.Time {
	if location == nil {
		location = time.Local
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
}

// RunDaily refreshes immediately when configured and then at local midnight.
func (s *Service) RunDaily(ctx context.Context, location *time.Location) {
	if s == nil {
		return
	}
	if ProviderConfigured(s.store.Get()) {
		refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = s.Refresh(refreshCtx)
		cancel()
	}
	for {
		next := NextMidnight(s.now(), location)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_ = s.Refresh(refreshCtx)
			cancel()
		}
	}
}
