package handlers

import "testing"

func TestRealtimeAvailabilityURL(t *testing.T) {
	tests := []struct {
		region string
		want   string
		ok     bool
	}{
		{region: "", want: "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities", ok: true},
		{region: "EU", want: "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities", ok: true},
		{region: "ca", want: "https://ca.api.ovh.com/v1/dedicated/server/datacenter/availabilities", ok: true},
		{region: "us", want: "", ok: false},
		{region: "https://example.com", want: "", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.region, func(t *testing.T) {
			got, ok := realtimeAvailabilityURL(tc.region)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("realtimeAvailabilityURL(%q) = (%q, %v), want (%q, %v)", tc.region, got, ok, tc.want, tc.ok)
			}
		})
	}
}
