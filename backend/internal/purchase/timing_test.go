package purchase

import (
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

func TestTimelineRecordsOrderedDeltas(t *testing.T) {
	timeline := newTimeline()
	time.Sleep(25 * time.Millisecond)
	timeline.mark("查库存")
	timeline.mark("创建购物车")

	phases := timeline.entries()
	if len(phases) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(phases))
	}
	if phases[0].Name != "查库存" || phases[1].Name != "创建购物车" {
		t.Fatalf("unexpected phase order: %#v", phases)
	}
	if phases[0].Ms < 15 {
		t.Fatalf("first phase should contain elapsed time, got %dms", phases[0].Ms)
	}
	if phases[1].Ms > 20 {
		t.Fatalf("second phase should be a delta, got %dms", phases[1].Ms)
	}
}

func TestTimelineStringAndNilSafety(t *testing.T) {
	tl := newTimeline()
	tl.mark("结账")
	if value := tl.String(); !strings.Contains(value, "总 ") || !strings.Contains(value, "结账") {
		t.Fatalf("unexpected timeline summary: %q", value)
	}
	var nilTimeline *timeline
	if nilTimeline.String() != "" || nilTimeline.total() != 0 || nilTimeline.entries() != nil {
		t.Fatal("nil timeline should return zero values")
	}
}

func TestRecordTimingKeepsBoundedIndependentSnapshots(t *testing.T) {
	lastTimingMu.Lock()
	lastTimings = map[string]types.PurchaseTiming{}
	lastTimingMu.Unlock()

	for i := 0; i < 520; i++ {
		timeline := newTimeline()
		timeline.mark("查库存")
		recordTiming(TimingKey("plan", string(rune(i))), timeline, "unavailable")
	}
	if got := len(LastTimings()); got > 500 {
		t.Fatalf("timing snapshots exceeded cap: %d", got)
	}

	copy := LastTimings()
	for key, value := range copy {
		if len(value.Phases) > 0 {
			value.Phases[0].Name = "mutated"
			copy[key] = value
			break
		}
	}
	for _, value := range LastTimings() {
		if len(value.Phases) > 0 && value.Phases[0].Name == "mutated" {
			t.Fatal("LastTimings returned a shared phase slice")
		}
	}
}
