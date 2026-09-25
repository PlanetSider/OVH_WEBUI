package price

import (
	"context"
	"errors"
	"testing"
)

type fakeEcoCartClient struct {
	posts       []map[string]interface{}
	gets        int
	definitions []map[string]interface{}
	firstErr    error
	retryErr    error
	lookupErr   error
	afterPost   func()
}

func (f *fakeEcoCartClient) PostWithContext(ctx context.Context, path string, body, result interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, ok := body.(map[string]interface{})
	if !ok || path != "/order/cart/cart-id/eco" {
		return errors.New("unexpected cart POST")
	}
	f.posts = append(f.posts, payload)
	if f.afterPost != nil {
		f.afterPost()
	}
	if len(f.posts) == 1 && f.firstErr != nil {
		return f.firstErr
	}
	if len(f.posts) == 2 && f.retryErr != nil {
		return f.retryErr
	}
	*result.(*map[string]interface{}) = map[string]interface{}{"itemId": int64(42)}
	return nil
}

func (f *fakeEcoCartClient) GetWithContext(ctx context.Context, path string, result interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path != "/order/cart/cart-id/eco" {
		return errors.New("unexpected cart GET")
	}
	f.gets++
	if f.lookupErr != nil {
		return f.lookupErr
	}
	*result.(*[]map[string]interface{}) = f.definitions
	return nil
}

func TestPickEcoCartPricing(t *testing.T) {
	prices := []interface{}{
		map[string]interface{}{"duration": "P1M", "pricingMode": "default", "pricingType": "rental", "capacities": []interface{}{"renew"}},
		map[string]interface{}{"duration": "P12M", "pricingMode": "special"},
		map[string]interface{}{"duration": "P12M", "pricingMode": "renewable", "pricingType": "rental", "capacities": []interface{}{"renew"}},
	}
	for _, test := range []struct {
		name, prefer, duration, mode string
		prices                       interface{}
		valid                        bool
	}{
		{"same-duration renewal", "P12M", "P12M", "renewable", prices, true},
		{"monthly renewal", "P1M", "P1M", "default", prices, true},
		{"renewal without matching duration", "P24M", "P1M", "default", prices, true},
		{"invalid list", "P12M", "", "", []interface{}{map[string]interface{}{"duration": "P12M"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, m, ok := PickEcoCartPricing(test.prices, test.prefer)
			if d != test.duration || m != test.mode || ok != test.valid {
				t.Fatalf("PickEcoCartPricing() = %q/%q, %v; want %q/%q, %v", d, m, ok, test.duration, test.mode, test.valid)
			}
		})
	}
}

func TestAddBaseEcoItemFastPath(t *testing.T) {
	cart := &fakeEcoCartClient{}
	item, duration, mode, err := AddBaseEcoItem(context.Background(), cart, "cart-id", "server-plan")
	if err != nil || item["itemId"] != int64(42) || duration != "P1M" || mode != "default" {
		t.Fatalf("fast path = item %v, %s/%s, err %v", item, duration, mode, err)
	}
	if len(cart.posts) != 1 || cart.gets != 0 {
		t.Fatalf("fast path calls: posts=%d, gets=%d", len(cart.posts), cart.gets)
	}
}

func TestAddBaseEcoItemRetriesUsingRealCartPricing(t *testing.T) {
	cart := &fakeEcoCartClient{
		firstErr: errors.New("P1M/default rejected"),
		definitions: []map[string]interface{}{
			{"planCode": "different-plan", "prices": []interface{}{map[string]interface{}{"duration": "P1M", "pricingMode": "default"}}},
			{"planCode": "server-plan", "prices": []interface{}{map[string]interface{}{"duration": "P12M", "pricingMode": "renewable", "pricingType": "rental", "capacities": []interface{}{"renew"}}}},
		},
	}
	item, duration, mode, err := AddBaseEcoItem(context.Background(), cart, "cart-id", "server-plan")
	if err != nil || item["itemId"] != int64(42) || duration != "P12M" || mode != "renewable" {
		t.Fatalf("fallback = item %v, %s/%s, err %v", item, duration, mode, err)
	}
	if len(cart.posts) != 2 || cart.gets != 1 || cart.posts[0]["duration"] != "P1M" || cart.posts[1]["duration"] != "P12M" || cart.posts[1]["pricingMode"] != "renewable" {
		t.Fatalf("unexpected retry sequence: posts=%v, gets=%d", cart.posts, cart.gets)
	}
}

func TestAddBaseEcoItemDoesNotRetryUnknownOrUnchangedPricing(t *testing.T) {
	original := errors.New("original add error")
	for _, test := range []struct {
		name string
		defs []map[string]interface{}
		err  error
	}{
		{"missing plan", []map[string]interface{}{{"planCode": "different-plan"}}, nil},
		{"invalid prices", []map[string]interface{}{{"planCode": "server-plan", "prices": []interface{}{map[string]interface{}{"duration": "P12M"}}}}, nil},
		{"same pricing", []map[string]interface{}{{"planCode": "server-plan", "prices": []interface{}{map[string]interface{}{"duration": "P1M", "pricingMode": "default"}}}}, nil},
		{"lookup failure", nil, errors.New("catalog failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			cart := &fakeEcoCartClient{firstErr: original, definitions: test.defs, lookupErr: test.err}
			_, _, _, err := AddBaseEcoItem(context.Background(), cart, "cart-id", "server-plan")
			if err != original || len(cart.posts) != 1 || cart.gets != 1 {
				t.Fatalf("expected original error with one POST/GET; err=%v posts=%d gets=%d", err, len(cart.posts), cart.gets)
			}
		})
	}
}

func TestAddBaseEcoItemStopsAfterOneFailedRetry(t *testing.T) {
	retryErr := errors.New("fallback add rejected")
	cart := &fakeEcoCartClient{
		firstErr: errors.New("P1M rejected"), retryErr: retryErr,
		definitions: []map[string]interface{}{{"planCode": "server-plan", "prices": []interface{}{map[string]interface{}{"duration": "P12M", "pricingMode": "default"}}}},
	}
	_, _, _, err := AddBaseEcoItem(context.Background(), cart, "cart-id", "server-plan")
	if err != retryErr || len(cart.posts) != 2 || cart.gets != 1 {
		t.Fatalf("retry must stop after second POST: err=%v posts=%d gets=%d", err, len(cart.posts), cart.gets)
	}
}

func TestAddBaseEcoItemCancellationSkipsLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cart := &fakeEcoCartClient{firstErr: errors.New("P1M rejected"), afterPost: cancel}
	_, _, _, err := AddBaseEcoItem(ctx, cart, "cart-id", "server-plan")
	if !errors.Is(err, context.Canceled) || len(cart.posts) != 1 || cart.gets != 0 {
		t.Fatalf("cancellation must stop retry: err=%v posts=%d gets=%d", err, len(cart.posts), cart.gets)
	}
}
