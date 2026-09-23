package proxyguard

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGuardPausesOnlyFailingAccountAndRecovers(t *testing.T) {
	guard := New(2)
	guard.ReportFailure("a", errors.New("proxy down"))
	if guard.IsPaused("a") {
		t.Fatal("account should not pause before threshold")
	}
	guard.ReportFailure("a", errors.New("proxy down again"))
	if !guard.IsPaused("a") {
		t.Fatal("account should pause at threshold")
	}
	if guard.IsPaused("b") {
		t.Fatal("unrelated account should remain active")
	}
	status := guard.Reset("a")
	if status.Paused || status.ConsecutiveFailures != 0 {
		t.Fatalf("reset status = %+v", status)
	}
}

func TestGuardRedactsCredentialsFromFailure(t *testing.T) {
	guard := New(1)
	guard.ReportFailure("a", errors.New("proxyconnect http://user:secret@127.0.0.1:8080 failed"))
	status, ok := guard.Get("a")
	if !ok || strings.Contains(status.LastError, "secret") || strings.Contains(status.LastError, "user:") {
		t.Fatalf("status leaked proxy credentials: %+v", status)
	}
}

func TestGuardFailureWindowResetsConsecutiveCount(t *testing.T) {
	guard := New(3)
	guard.ReportFailure("a", errors.New("first"))
	guard.ReportFailure("a", errors.New("second"))

	guard.mu.Lock()
	status := guard.statuses["a"]
	status.LastFailureAt = time.Now().UTC().Add(-failureWindow - time.Second)
	guard.statuses["a"] = status
	guard.mu.Unlock()

	event := guard.ObserveFailure("a", errors.New("after window"))
	if event.Kind != EventNone || event.Status.ConsecutiveFailures != 1 || event.Status.Paused {
		t.Fatalf("event after failure window = %+v", event)
	}
}

func TestGuardReminderHonorsNotificationCooldown(t *testing.T) {
	guard := New(1)
	if event := guard.ObserveFailure("a", errors.New("trip")); event.Kind != EventTrip {
		t.Fatalf("trip event = %+v", event)
	}
	if event := guard.ObserveFailure("a", errors.New("still down")); event.Kind != EventNone {
		t.Fatalf("unexpected immediate reminder = %+v", event)
	}

	guard.mu.Lock()
	guard.lastNotify["a"] = time.Now().UTC().Add(-notificationCooldown - time.Second)
	guard.mu.Unlock()
	if event := guard.ObserveFailure("a", errors.New("still down")); event.Kind != EventReminder {
		t.Fatalf("cooldown reminder event = %+v", event)
	}
}

func TestGuardTruncatesFailure(t *testing.T) {
	guard := New(1)
	guard.ReportFailure("a", errors.New("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"))
	status, ok := guard.Get("a")
	if !ok || len(status.LastError) > 240 {
		t.Fatalf("status = %+v, ok=%v", status, ok)
	}
}

func TestGuardSeedTrippedRecoversWithDuration(t *testing.T) {
	guard := New(3)
	trippedAt := time.Now().UTC().Add(-2 * time.Second)
	guard.SeedTripped("a", trippedAt)
	status, ok := guard.Get("a")
	if !ok || !status.Paused || !status.TrippedAt.Equal(trippedAt) {
		t.Fatalf("seeded status = %+v, ok=%v", status, ok)
	}
	event := guard.ObserveSuccess("a")
	if event.Kind != EventRecovery || event.Duration < time.Second {
		t.Fatalf("seed recovery event = %+v", event)
	}
}
