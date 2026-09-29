package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	goovh "github.com/ovh/go-ovh/ovh"
)

func newOrderCommandClient(t *testing.T, handler http.HandlerFunc) *goovh.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint := "order-command-test-" + fmt.Sprint(time.Now().UnixNano())
	goovh.Endpoints[endpoint] = server.URL
	t.Cleanup(func() { delete(goovh.Endpoints, endpoint) })
	client, err := goovh.NewClient(endpoint, "app", "secret", "consumer")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestParseBotOrderQuery(t *testing.T) {
	cases := []struct {
		args      []string
		limit     int
		monthOnly bool
		unpaid    bool
		wantError bool
	}{
		{args: nil, monthOnly: true},
		{args: []string{"12"}, limit: 12},
		{args: []string{"unpaid"}, monthOnly: true, unpaid: true},
		{args: []string{"未付款"}, monthOnly: true, unpaid: true},
		{args: []string{"0"}, wantError: true},
		{args: []string{"101"}, wantError: true},
		{args: []string{"abc"}, wantError: true},
		{args: []string{"1", "2"}, wantError: true},
	}
	for _, tc := range cases {
		got, message := parseBotOrderQuery(tc.args)
		if tc.wantError {
			if message == "" {
				t.Errorf("parseBotOrderQuery(%v) expected error", tc.args)
			}
			continue
		}
		if message != "" {
			t.Errorf("parseBotOrderQuery(%v) error = %q", tc.args, message)
			continue
		}
		if got.limit != tc.limit || got.monthOnly != tc.monthOnly || got.unpaid != tc.unpaid {
			t.Errorf("parseBotOrderQuery(%v) = %#v", tc.args, got)
		}
	}
}

func TestFetchBotOrdersMonthFilterStatusAndUnpaid(t *testing.T) {
	var mu sync.Mutex
	var listPath string
	client := newOrderCommandClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/time" {
			_, _ = fmt.Fprint(w, time.Now().Unix())
			return
		}
		mu.Lock()
		if r.URL.Path == "/me/order" {
			listPath = r.URL.RequestURI()
		}
		mu.Unlock()
		switch r.URL.Path {
		case "/me/order":
			_ = json.NewEncoder(w).Encode([]int64{1001, 1002})
		case "/me/order/1001":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"date": "2026-09-10T10:00:00+00:00", "priceWithTax": map[string]interface{}{"text": "12.00 €"}, "priceWithoutTax": map[string]interface{}{"text": "10.00 €"},
			})
		case "/me/order/1002":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"date": "2026-09-11T10:00:00+00:00", "priceWithTax": map[string]interface{}{"text": "24.00 €"}, "priceWithoutTax": map[string]interface{}{"text": "20.00 €"},
			})
		case "/me/order/1001/status":
			_ = json.NewEncoder(w).Encode("paid")
		case "/me/order/1002/status":
			_ = json.NewEncoder(w).Encode("notPaid")
		default:
			http.NotFound(w, r)
		}
	})

	records, err := fetchBotOrders(client, botOrderQuery{monthOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != "1002" || records[0].Status != "notPaid" {
		t.Fatalf("records = %#v", records)
	}
	if !strings.Contains(listPath, "date.from=") || !strings.Contains(listPath, "date.to=") {
		t.Fatalf("month query missing date bounds: %q", listPath)
	}
	unpaid := filterUnpaidBotOrders(records)
	if len(unpaid) != 1 || unpaid[0].ID != "1002" {
		t.Fatalf("unpaid = %#v", unpaid)
	}
	formatted := formatBotOrders(unpaid, botOrderQuery{monthOnly: true, unpaid: true})
	for _, want := range []string{"#1002", "时间:", "状态: notPaid", "价格: 20.00 €"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted orders missing %q: %s", want, formatted)
		}
	}
}

func TestPayBotOrderUsesDefaultPaymentMethod(t *testing.T) {
	var gotMethod string
	var gotBody map[string]interface{}
	client := newOrderCommandClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/time" {
			_, _ = fmt.Fprint(w, time.Now().Unix())
			return
		}
		switch r.URL.Path {
		case "/me/payment/method":
			gotMethod = r.URL.RawQuery
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"paymentMethodId": 42, "default": true}})
		case "/me/order/1002/pay":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	if err := payBotOrderWithDefaultPaymentMethod(client, "1002"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "default=true" {
		t.Fatalf("payment method query = %q", gotMethod)
	}
	if gotBody["id"] != float64(42) {
		t.Fatalf("payment body = %#v", gotBody)
	}
}

func TestPayBotOrderRejectsMissingDefaultPaymentMethod(t *testing.T) {
	client := newOrderCommandClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/time" {
			_, _ = fmt.Fprint(w, time.Now().Unix())
			return
		}
		if r.URL.Path == "/me/payment/method" {
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
			return
		}
		http.NotFound(w, r)
	})
	if err := payBotOrderWithDefaultPaymentMethod(client, "1002"); err == nil || !strings.Contains(err.Error(), "默认支付方式") {
		t.Fatalf("error = %v", err)
	}
}

func TestFormatOrderPriceDoesNotFallbackToWithTax(t *testing.T) {
	if got := formatOrderPrice(map[string]interface{}{
		"priceWithTax": map[string]interface{}{"text": "24.00 €"},
	}); got != "价格暂不可用" {
		t.Fatalf("formatOrderPrice() = %q, want unavailable without-tax price", got)
	}
}
