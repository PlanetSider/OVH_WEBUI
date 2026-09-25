package price

import (
	"context"
)

type ecoCartClient interface {
	PostWithContext(context.Context, string, interface{}, interface{}) error
	GetWithContext(context.Context, string, interface{}) error
}

// PickEcoCartPricing prefers the base item's duration, then a renewable rental
// within that duration. An invalid or empty price list never invents a quote.
func PickEcoCartPricing(pricesRaw interface{}, preferDuration string) (string, string, bool) {
	list, _ := pricesRaw.([]interface{})
	type candidate struct{ duration, mode string }
	var sameRenew, sameDuration, anyRenew, first *candidate
	for _, raw := range list {
		p, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		duration, _ := p["duration"].(string)
		mode, _ := p["pricingMode"].(string)
		if duration == "" || mode == "" {
			continue
		}
		current := &candidate{duration, mode}
		pricingType, _ := p["pricingType"].(string)
		renewable := pricingType == "rental" && ecoPricingHasCapacity(p["capacities"], "renew")
		if first == nil {
			first = current
		}
		if renewable && anyRenew == nil {
			anyRenew = current
		}
		if duration == preferDuration {
			if sameDuration == nil {
				sameDuration = current
			}
			if renewable && sameRenew == nil {
				sameRenew = current
			}
		}
	}
	for _, chosen := range []*candidate{sameRenew, sameDuration, anyRenew, first} {
		if chosen != nil {
			return chosen.duration, chosen.mode, true
		}
	}
	return "", "", false
}

func ecoPricingHasCapacity(raw interface{}, capacity string) bool {
	list, _ := raw.([]interface{})
	for _, value := range list {
		if value == capacity {
			return true
		}
	}
	return false
}

// AddBaseEcoItem preserves the one-request P1M/default fast path. Only when
// OVH rejects that pricing does it consult the cart's Eco definitions and retry
// once with a different valid duration/pricingMode pair.
func AddBaseEcoItem(ctx context.Context, client ecoCartClient, cartID, planCode string) (map[string]interface{}, string, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	duration, mode := "P1M", "default"
	var item map[string]interface{}
	add := func() error {
		item = nil
		return client.PostWithContext(ctx, "/order/cart/"+cartID+"/eco", map[string]interface{}{
			"planCode": planCode, "pricingMode": mode, "duration": duration, "quantity": 1,
		}, &item)
	}
	firstErr := add()
	if firstErr == nil {
		return item, duration, mode, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}

	var definitions []map[string]interface{}
	if err := client.GetWithContext(ctx, "/order/cart/"+cartID+"/eco", &definitions); err != nil {
		if ctx.Err() != nil {
			return nil, "", "", ctx.Err()
		}
		return nil, "", "", firstErr
	}
	for _, definition := range definitions {
		if definition["planCode"] != planCode {
			continue
		}
		alternativeDuration, alternativeMode, valid := PickEcoCartPricing(definition["prices"], duration)
		if !valid || alternativeDuration == duration && alternativeMode == mode {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, "", "", err
		}
		duration, mode = alternativeDuration, alternativeMode
		if err := add(); err != nil {
			return nil, "", "", err
		}
		return item, duration, mode, nil
	}
	return nil, "", "", firstErr
}
