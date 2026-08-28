package db

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramButtonOlderThanTTLIsRejected(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	createdAt := float64(time.Now().Add(-TelegramButtonTTL - time.Minute).Unix())
	if err := database.UpsertTelegramButton("expired-button", "24sk10", "gra", nil, nil, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := database.ClaimTelegramButton("expired-button"); err != nil || claimed {
		t.Fatalf("ClaimTelegramButton(expired) claimed=%v err=%v, want false, nil", claimed, err)
	}
	used, exists, err := database.IsTelegramButtonUsed("expired-button")
	if err != nil || !exists || used {
		t.Fatalf("expired button state used=%v exists=%v err=%v", used, exists, err)
	}
}

func TestTelegramButtonConcurrentClaimOnlySucceedsOnce(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.UpsertTelegramButton("concurrent-button", "24sk10", "gra", nil, nil, 0); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	start := make(chan struct{})
	var successes atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, err := database.ClaimTelegramButton("concurrent-button")
			if err != nil {
				errs <- err
				return
			}
			if claimed {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent claim error: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
}

func TestUnclaimTelegramButtonAllowsRetry(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.UpsertTelegramButton("retry-button", "24sk10", "gra", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := database.ClaimTelegramButton("retry-button"); err != nil || !claimed {
		t.Fatalf("first claim claimed=%v err=%v", claimed, err)
	}
	if err := database.UnclaimTelegramButton("retry-button"); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := database.ClaimTelegramButton("retry-button"); err != nil || !claimed {
		t.Fatalf("claim after rollback claimed=%v err=%v", claimed, err)
	}
}

func TestFeishuEventClaimIsSharedAcrossTransports(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	first, err := database.TryClaimFeishuEvent("event-shared-transport")
	if err != nil || !first {
		t.Fatalf("first event claim=%v err=%v", first, err)
	}
	second, err := database.TryClaimFeishuEvent("event-shared-transport")
	if err != nil || second {
		t.Fatalf("duplicate event claim=%v err=%v, want false, nil", second, err)
	}
}
