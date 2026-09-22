package proxyguard

import (
	"errors"
	"strings"
	"testing"
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

func TestGuardTruncatesFailure(t *testing.T) {
	guard := New(1)
	guard.ReportFailure("a", errors.New("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"))
	status, ok := guard.Get("a")
	if !ok || len(status.LastError) > 240 {
		t.Fatalf("status = %+v, ok=%v", status, ok)
	}
}
