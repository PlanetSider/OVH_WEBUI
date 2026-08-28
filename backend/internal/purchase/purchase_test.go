package purchase

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	ovhsdk "github.com/ovh/go-ovh/ovh"
	"github.com/ovh-webui/server/internal/catalog"
)

func TestCheckoutFailureIsDefinitive(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "business rejection", err: &ovhsdk.APIError{Code: 400}, want: true},
		{name: "unauthorized", err: &ovhsdk.APIError{Code: 401}, want: true},
		{name: "rate limited", err: &ovhsdk.APIError{Code: 429}, want: true},
		{name: "wrapped rejection", err: fmt.Errorf("checkout: %w", &ovhsdk.APIError{Code: 422}), want: true},
		{name: "value api error", err: ovhsdk.APIError{Code: 403}, want: true},
		{name: "request timeout", err: &ovhsdk.APIError{Code: 408}, want: false},
		{name: "conflict", err: &ovhsdk.APIError{Code: 409}, want: false},
		{name: "client closed request", err: &ovhsdk.APIError{Code: 499}, want: false},
		{name: "server error", err: &ovhsdk.APIError{Code: 500}, want: false},
		{name: "gateway timeout", err: &ovhsdk.APIError{Code: 504}, want: false},
		{name: "unexpected redirect", err: &ovhsdk.APIError{Code: 307}, want: false},
		{name: "network error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkoutFailureIsDefinitive(tt.err); got != tt.want {
				t.Fatalf("checkoutFailureIsDefinitive(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCanonicalOptions(t *testing.T) {
	got := canonicalOptions([]string{" RAM-64G ", "", "ram-64g", "SOFTRAID-2X960NVME"})
	want := []string{"ram-64g", "softraid-2x960nvme"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalOptions() = %#v, want %#v", got, want)
	}
}

func TestFQNContainsOptions(t *testing.T) {
	tests := []struct {
		name    string
		fqn     string
		options []string
		want    bool
	}{
		{name: "exact segments", fqn: "24sk10.gra.ram-64g.softraid-2x960nvme", options: []string{"ram-64g", "softraid-2x960nvme"}, want: true},
		{name: "case insensitive", fqn: "24SK10.GRA.RAM-64G", options: []string{"ram-64g"}, want: true},
		{name: "substring is rejected", fqn: "24sk10.gra.ram-64g-extra", options: []string{"ram-64g"}, want: false},
		{name: "missing option", fqn: "24sk10.gra.ram-64g", options: []string{"ram-64g", "softraid-2x960nvme"}, want: false},
		{name: "invalid fqn", fqn: "24sk10", options: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fqnContainsOptions(tt.fqn, tt.options); got != tt.want {
				t.Fatalf("fqnContainsOptions(%q, %#v) = %v, want %v", tt.fqn, tt.options, got, tt.want)
			}
		})
	}
}

func TestAvailabilityExplicitlyAvailable(t *testing.T) {
	tests := map[string]bool{
		"available":     true,
		" AVAILABLE ":   true,
		"1H-high":       true,
		"1H-low":        true,
		"24H":           true,
		"72H":           true,
		"unavailable":   false,
		"unknown":       false,
		"":              false,
		"future-status": false,
	}
	for status, want := range tests {
		t.Run(status, func(t *testing.T) {
			if got := catalog.AvailabilityExplicitlyAvailable(status); got != want {
				t.Fatalf("catalog.AvailabilityExplicitlyAvailable(%q) = %v, want %v", status, got, want)
			}
		})
	}
}
