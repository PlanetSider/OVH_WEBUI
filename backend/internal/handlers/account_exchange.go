package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
)

// enrichBillExchange adds historical-CNY metadata without mutating OVH's raw amounts.
func enrichBillExchange(ctx context.Context, state *app.State, bill map[string]interface{}) {
	if state == nil || state.Exchange == nil || state.Config == nil {
		return
	}
	cfg := state.Config.Get()
	if cfg.ExchangeDisplayMode != "cny" {
		return
	}
	date := firstBillString(bill, "date", "billDate", "creationDate", "createdAt")
	base := billCurrency(bill)
	result := map[string]interface{}{
		"available": false,
		"date":      date,
		"base":      base,
		"quote":     "CNY",
	}
	if date == "" || base == "" {
		result["error"] = "账单缺少日期或原始币种"
		bill["exchangeDisplay"] = result
		return
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		if len(date) >= 10 {
			date = date[:10]
		}
		if _, err = time.Parse("2006-01-02", date); err != nil {
			result["error"] = "账单日期无效"
			bill["exchangeDisplay"] = result
			return
		}
	}
	result["date"] = date
	rate, err := state.Exchange.HistoricalRate(ctx, date, base, "CNY")
	if err != nil {
		result["error"] = err.Error()
		bill["exchangeDisplay"] = result
		return
	}
	result["available"] = true
	result["rate"] = rate
	if value, ok := billAmount(bill, "priceWithoutTax"); ok {
		result["withoutTax"] = value * rate
	}
	if value, ok := billAmount(bill, "priceWithTax"); ok {
		result["withTax"] = value * rate
	}
	bill["exchangeDisplay"] = result
}

func firstBillString(bill map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := bill[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func billCurrency(bill map[string]interface{}) string {
	for _, key := range []string{"currencyCode", "currency"} {
		if value, ok := bill[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.ToUpper(strings.TrimSpace(value))
		}
	}
	for _, key := range []string{"priceWithoutTax", "priceWithTax", "total"} {
		if nested, ok := bill[key].(map[string]interface{}); ok {
			if value, ok := nested["currencyCode"].(string); ok && strings.TrimSpace(value) != "" {
				return strings.ToUpper(strings.TrimSpace(value))
			}
		}
	}
	return ""
}

func billAmount(bill map[string]interface{}, key string) (float64, bool) {
	value, ok := bill[key]
	if !ok {
		return 0, false
	}
	if nested, ok := value.(map[string]interface{}); ok {
		value = nested["value"]
	}
	switch number := value.(type) {
	case float64:
		return number, number == number
	case float32:
		return float64(number), number == number
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
