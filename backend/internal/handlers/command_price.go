package handlers

import (
	"fmt"
	"strings"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/exchange"
	"github.com/ovh-webui/server/internal/numconv"
)

func commandUsesCNY(state *app.State) bool {
	if state == nil || state.Config == nil {
		return false
	}
	return exchange.NormalizeDisplayMode(state.Config.Get().ExchangeDisplayMode) == exchange.DisplayCNY
}

func resolveCommandDisplayAmount(state *app.State, amount float64, currency string) (float64, string, bool) {
	if state == nil || state.Exchange == nil {
		return 0, exchange.DisplayCNYCode, false
	}
	return state.Exchange.ResolveDisplayAmount(amount, currency)
}

// formatCommandPriceValue preserves the existing raw text in original mode,
// while CNY mode requires a numeric amount and an active exchange rate.
func formatCommandPriceValue(state *app.State, raw interface{}, currency string) (string, bool) {
	if raw == nil {
		return "", false
	}
	if !commandUsesCNY(state) {
		return fmt.Sprintf("%v %s", raw, currency), true
	}
	amount, ok := numconv.ToFloat64(raw)
	if !ok {
		return "", false
	}
	amount, displayCurrency, ok := resolveCommandDisplayAmount(state, amount, currency)
	if !ok {
		return "", false
	}
	return formatCommandDisplayAmount(amount, displayCurrency), true
}

func formatCommandDisplayAmount(amount float64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	symbol := ""
	switch currency {
	case "EUR":
		symbol = "€"
	case "USD":
		symbol = "$"
	case "CAD":
		symbol = "CA$"
	case "CNY":
		symbol = "¥"
	case "GBP":
		symbol = "£"
	case "AUD":
		symbol = "A$"
	case "SGD":
		symbol = "S$"
	case "INR":
		symbol = "₹"
	case "PLN":
		symbol = "zł"
	case "JPY":
		symbol = "¥"
	case "KRW":
		symbol = "₩"
	case "HKD":
		symbol = "HK$"
	}
	if symbol != "" {
		return fmt.Sprintf("%s%.2f", symbol, amount)
	}
	if currency == "" {
		return fmt.Sprintf("%.2f", amount)
	}
	return fmt.Sprintf("%.2f %s", amount, currency)
}
