package purchase

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

// timeline 按顺序记录一次采购尝试已经完成的阶段。它只记录墙钟事实，
// 不参与重试、支付或订单状态判断。
type timeline struct {
	mu    sync.Mutex
	start time.Time
	last  time.Time
	items []phase
}

type phase struct {
	Name string
	Dur  time.Duration
}

func newTimeline() *timeline {
	now := time.Now()
	return &timeline{start: now, last: now}
}

// mark 结束当前阶段；调用点就是阶段的结束点。
func (t *timeline) mark(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.last.IsZero() {
		t.last = now
	}
	if t.start.IsZero() {
		t.start = now
	}
	t.items = append(t.items, phase{Name: name, Dur: now.Sub(t.last)})
	t.last = now
}

func (t *timeline) total() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.start.IsZero() {
		return 0
	}
	return time.Since(t.start)
}

func (t *timeline) entries() []types.PhaseTiming {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.items) == 0 {
		return nil
	}
	out := make([]types.PhaseTiming, 0, len(t.items))
	for _, item := range t.items {
		out = append(out, types.PhaseTiming{Name: item.Name, Ms: item.Dur.Milliseconds()})
	}
	return out
}

func (t *timeline) String() string {
	if t == nil {
		return ""
	}
	entries := t.entries()
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, 0, len(entries))
	for _, item := range entries {
		parts = append(parts, fmt.Sprintf("%s %dms", item.Name, item.Ms))
	}
	return fmt.Sprintf("总 %dms = %s", t.total().Milliseconds(), strings.Join(parts, " + "))
}

var (
	lastTimingMu sync.RWMutex
	lastTimings  = map[string]types.PurchaseTiming{}
)

// TimingKey 用机型和机房共同标识一条独立抢购链路。
func TimingKey(planCode, datacenter string) string {
	return planCode + "@" + datacenter
}

// recordTiming 保存最近一轮快照，供队列汇总接口和前端诊断使用。
func recordTiming(key string, t *timeline, outcome string) {
	if t == nil || len(t.entries()) == 0 {
		return
	}
	lastTimingMu.Lock()
	defer lastTimingMu.Unlock()
	if len(lastTimings) >= 500 {
		lastTimings = map[string]types.PurchaseTiming{}
	}
	lastTimings[key] = types.PurchaseTiming{
		At:      types.NowISO(),
		TotalMs: t.total().Milliseconds(),
		Phases:  t.entries(),
		Outcome: outcome,
	}
}

// LastTimings 返回独立副本，避免 HTTP 序列化或调用方修改共享快照。
func LastTimings() map[string]types.PurchaseTiming {
	lastTimingMu.RLock()
	defer lastTimingMu.RUnlock()
	out := make(map[string]types.PurchaseTiming, len(lastTimings))
	for key, timing := range lastTimings {
		phases := append([]types.PhaseTiming(nil), timing.Phases...)
		timing.Phases = phases
		out[key] = timing
	}
	return out
}

// recordTimingToHistory 把已完成阶段写入对应历史。它使用 State 的统一历史
// owner，确保旧记录兼容且数据库失败时不会只改内存。
func recordTimingToHistory(state *app.State, taskID string, t *timeline) {
	if state == nil || strings.TrimSpace(taskID) == "" || t == nil {
		return
	}
	phases := t.entries()
	if len(phases) == 0 {
		return
	}
	totalMs := t.total().Milliseconds()
	if err := state.MutateHistory(func(history []types.PurchaseHistoryEntry) ([]types.PurchaseHistoryEntry, error) {
		for i := range history {
			if history[i].TaskID == taskID {
				history[i].Timing = phases
				history[i].TotalMs = totalMs
				break
			}
		}
		return history, nil
	}); err != nil && state.Logger != nil {
		state.Logger.Warn(fmt.Sprintf("保存任务 %s 的采购耗时失败", taskID), "purchase")
	}
}
