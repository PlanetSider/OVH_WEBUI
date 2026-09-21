package monitor

import (
	"testing"
	"time"
)

func TestSubscriptionCheckDueThrottlesDiscontinuedSubscription(t *testing.T) {
	mon := &Monitor{}
	now := time.Unix(1700000000, 0)
	sub := &Subscription{}
	if !mon.subscriptionCheckDue(sub, now) {
		t.Fatal("normal subscription should be due")
	}

	sub.Discontinued = true
	sub.DiscontinuedNextCheckAt = float64(now.Unix() + 60)
	if mon.subscriptionCheckDue(sub, now) {
		t.Fatal("discontinued subscription was checked before its hourly deadline")
	}
	if !mon.subscriptionCheckDue(sub, now.Add(time.Minute)) {
		t.Fatal("discontinued subscription was not checked at its deadline")
	}
	want := float64(now.Unix() + 60 + 3600)
	if sub.DiscontinuedNextCheckAt != want {
		t.Fatalf("next check = %v, want %v", sub.DiscontinuedNextCheckAt, want)
	}
}
