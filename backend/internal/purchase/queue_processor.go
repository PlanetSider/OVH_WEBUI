package purchase

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

const concurrentBatchSize = 10

func effectiveRetryInterval(item types.QueueItem) int {
	if item.Discontinued {
		return types.DiscontinuedCheckIntervalSeconds
	}
	if item.RetryInterval < 1 {
		return 60
	}
	return item.RetryInterval
}

// ProcessQueueLoop 处理抢购队列，ctx 取消后不再开始新任务，并等待已开始的批次结束。
func ProcessQueueLoop(ctx context.Context, state *app.State) {
	if state == nil {
		return
	}
	// 启动闸门必须由处理器自身取得，不能依赖 main.go 的布尔字段。
	// 这样测试、重载或未来其它启动路径重复调用时也不会出现两个循环
	// 同时抢同一批任务。
	if !state.TryStartQueueProcessor() {
		state.Logger.Warn("抢购队列处理器已在运行，忽略重复启动", "queue")
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer state.SetQueueProcessorRunning(false)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		processQueueTick(ctx, state)
	}
}

func processQueueTick(ctx context.Context, state *app.State) {
	if ctx == nil || ctx.Err() != nil || state == nil {
		return
	}
	if !state.TryStartQueueTick() {
		return
	}
	defer state.FinishQueueTick()
	if !state.IsQueueProcessorEnabled() {
		return
	}
	state.QueueMu.Lock()
	sorted := append([]types.QueueItem(nil), state.Queue...)
	state.QueueMu.Unlock()
	if len(sorted) == 0 {
		// DeletedTaskIDs 不仅表示用户删除的任务，也表示 checkout 结果
		// 不确定或本地成功事务尚未完成的任务。队列为空并不能证明这些
		// 隔离标记已经安全解除；清空它们会让旧 worker 或恢复流程再次
		// 处理同一 task。标记只能在明确完成/重新建立任务的路径按 ID 清理。
		return
	}

	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.QuickOrder != b.QuickOrder {
			return a.QuickOrder
		}
		at, aOK := types.ParseTS(a.CreatedAt)
		bt, bOK := types.ParseTS(b.CreatedAt)
		if aOK != bOK {
			return aOK
		}
		if !aOK {
			return a.CreatedAt > b.CreatedAt
		}
		return at.After(bt)
	})

	now := time.Now().Unix()
	ready := make([]types.QueueItem, 0, len(sorted))
	state.DeletedTaskIDsMu.Lock()
	deleted := make(map[string]struct{}, len(state.DeletedTaskIDs))
	for id := range state.DeletedTaskIDs {
		deleted[id] = struct{}{}
	}
	state.DeletedTaskIDsMu.Unlock()
	for _, item := range sorted {
		if _, removed := deleted[item.ID]; removed || item.Status != "running" {
			continue
		}
		if state.IsAccountProxyPaused(item.AccountID) {
			continue
		}
		retryInterval := effectiveRetryInterval(item)
		if item.LastCheckTime == 0 || float64(now)-item.LastCheckTime >= float64(retryInterval) {
			ready = append(ready, item)
		}
	}

	for start := 0; start < len(ready); start += concurrentBatchSize {
		if ctx.Err() != nil {
			return
		}
		end := start + concurrentBatchSize
		if end > len(ready) {
			end = len(ready)
		}
		var wg sync.WaitGroup
		for _, item := range ready[start:end] {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			go func(candidate types.QueueItem) {
				defer wg.Done()
				processQueueItem(ctx, state, candidate)
			}(item)
		}
		wg.Wait()
	}
}

func processQueueItem(ctx context.Context, state *app.State, candidate types.QueueItem) {
	state.DeletedTaskIDsMu.Lock()
	_, deleted := state.DeletedTaskIDs[candidate.ID]
	state.DeletedTaskIDsMu.Unlock()
	if deleted {
		return
	}

	var snapshot types.QueueItem
	err := state.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
		for i := range queue {
			if queue[i].ID != candidate.ID || queue[i].Status != "running" {
				continue
			}
			queue[i].LastCheckTime = float64(time.Now().Unix())
			queue[i].RetryCount++
			queue[i].UpdatedAt = types.NowISO()
			snapshot = queue[i]
			return queue, nil
		}
		return nil, fmt.Errorf("任务已不在运行队列中")
	})
	if err != nil {
		if snapshot.ID != "" {
			state.Logger.Error("保存队列重试状态失败: "+err.Error(), "queue")
		}
		return
	}

	if snapshot.RetryCount == 1 {
		state.Logger.Info("首次尝试任务 "+snapshot.ID+": "+snapshot.PlanCode+" 在 "+snapshot.Datacenter, "queue")
	} else {
		state.Logger.Info("重试检查任务 "+snapshot.ID+": "+snapshot.PlanCode+" 在 "+snapshot.Datacenter, "queue")
	}

	if PurchaseServer(ctx, state, &snapshot) {
		state.ClearAttemptOutcome(snapshot.ID)
		state.Logger.Info("购买成功并已从队列原子移除: "+snapshot.PlanCode+" ("+snapshot.ID+")", "queue")
		return
	}
	// PurchaseServer 可能因为 checkout 结果不确定而隔离任务，或用户在
	// 请求过程中删除/暂停任务。此时不能再进入 MaxRetries 终止分支，
	// 以免覆盖隔离语义并输出误导日志。
	if !state.IsQueueItemRunning(snapshot.ID) {
		state.ClearAttemptOutcome(snapshot.ID)
		return
	}
	outcome, hasOutcome := state.TakeAttemptOutcome(snapshot.ID)
	if !hasOutcome {
		// 没有阶段结果通常表示无货、取消或调用在进入业务阶段前失败；
		// 保守地不消耗失败预算，仍由 retry interval 控制下一次尝试。
		outcome = app.AttemptOutcome{}
	}
	if outcome.Transient {
		state.Logger.Warn("任务因临时错误结束，本轮不消耗失败预算: "+snapshot.ID, "queue")
		return
	}
	// PurchaseServer 执行期间用户可能编辑任务。是否达到上限必须使用当前
	// 队列值原子判断，不能使用请求开始前的 snapshot，否则旧 MaxRetries
	// 可能误删刚刚调高重试上限的任务。
	terminated := false
	err = state.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
		for i := range queue {
			if queue[i].ID != snapshot.ID || queue[i].Status != "running" {
				continue
			}
			if outcome.CountFailure {
				queue[i].FailureCount++
			}
			if queue[i].MaxRetries > 0 && queue[i].FailureCount >= queue[i].MaxRetries {
				terminated = true
			}
		}
		if terminated {
			kept := make([]types.QueueItem, 0, len(queue))
			for _, item := range queue {
				if item.ID == snapshot.ID && item.Status == "running" {
					continue
				}
				kept = append(kept, item)
			}
			return kept, nil
		}
		return queue, nil
	})
	if err != nil {
		state.Logger.Error("更新失败预算或移除达到上限任务失败: "+err.Error(), "queue")
		return
	}
	if terminated {
		state.Logger.Info("任务达到 MaxRetries 失败预算上限已终止: "+snapshot.PlanCode+" ("+snapshot.ID+")", "queue")
	}
}
