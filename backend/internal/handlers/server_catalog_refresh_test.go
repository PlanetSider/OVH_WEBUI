package handlers

import (
	"testing"
	"time"
)

func TestNextHourlyRefreshUsesLocalClockBoundary(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.September, 1, 10, 37, 42, 123, location)
	want := time.Date(2026, time.September, 1, 11, 0, 0, 0, location)

	if got := nextHourlyRefresh(now); !got.Equal(want) || got.Location() != location {
		t.Fatalf("nextHourlyRefresh(%v) = %v, want %v", now, got, want)
	}
}

func TestNextHourlyRefreshAdvancesAtExactHour(t *testing.T) {
	now := time.Date(2026, time.September, 1, 23, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)

	if got := nextHourlyRefresh(now); !got.Equal(want) {
		t.Fatalf("nextHourlyRefresh(%v) = %v, want %v", now, got, want)
	}
}
