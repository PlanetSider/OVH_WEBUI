package handlers

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

const serverCatalogDiscontinuedKey = "server_catalog_discontinued"

type discontinuedPlanState struct {
	ServerName   string  `json:"serverName,omitempty"`
	MissingSince float64 `json:"missingSince,omitempty"`
	CycleID      string  `json:"cycleId,omitempty"`
	Discontinued bool    `json:"discontinued,omitempty"`
}

type trackedCatalogPlan struct {
	Modes      map[string]struct{}
	ServerName string
}

type catalogStatusTransition struct {
	Mode       string
	PlanCode   string
	ServerName string
	CycleID    string
	Recovered  bool
}

func trackedCatalogPlans(state *app.State, mon *monitor.Monitor, previousPlans []types.ServerPlan) map[string]trackedCatalogPlan {
	tracked := make(map[string]trackedCatalogPlan)
	previousByCode := catalogPlansByCode(previousPlans)
	add := func(planCode, mode, serverName string) {
		planCode = strings.TrimSpace(planCode)
		if planCode == "" {
			return
		}
		item := tracked[planCode]
		if item.Modes == nil {
			item.Modes = make(map[string]struct{})
		}
		item.Modes[mode] = struct{}{}
		if serverName = strings.TrimSpace(serverName); serverName != "" {
			item.ServerName = serverName
		}
		tracked[planCode] = item
	}

	if state != nil {
		state.QueueMu.Lock()
		queue := append([]types.QueueItem(nil), state.Queue...)
		state.QueueMu.Unlock()
		for _, item := range queue {
			if item.Status == "completed" || item.Status == "failed" {
				continue
			}
			serverName := ""
			if previous, ok := previousByCode[strings.TrimSpace(item.PlanCode)]; ok {
				serverName = previous.Name
			}
			add(item.PlanCode, "抢购", serverName)
		}
	}
	if mon != nil {
		for _, sub := range mon.Snapshot() {
			if sub == nil {
				continue
			}
			add(sub.PlanCode, "监控", sub.ServerName)
		}
	}
	return tracked
}

func catalogPlansByCode(plans []types.ServerPlan) map[string]types.ServerPlan {
	current := make(map[string]types.ServerPlan, len(plans))
	for _, plan := range plans {
		code := strings.TrimSpace(plan.PlanCode)
		if code != "" {
			current[code] = plan
		}
	}
	return current
}

func catalogDiscontinuedCycleID(missingSince float64) string {
	return strconv.FormatInt(int64(missingSince), 10)
}

func advanceDiscontinuedPlans(
	records map[string]discontinuedPlanState,
	tracked map[string]trackedCatalogPlan,
	current map[string]types.ServerPlan,
	now time.Time,
) (map[string]discontinuedPlanState, []catalogStatusTransition) {
	next := make(map[string]discontinuedPlanState, len(tracked))
	transitions := make([]catalogStatusTransition, 0)
	codes := make([]string, 0, len(tracked))
	for code := range tracked {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, code := range codes {
		trackedPlan := tracked[code]
		record, hadPriorRecord := records[code]
		if plan, ok := current[code]; ok {
			if name := strings.TrimSpace(plan.Name); name != "" {
				record.ServerName = name
			}
			if record.ServerName == "" {
				record.ServerName = trackedPlan.ServerName
			}
			if record.Discontinued {
				if record.CycleID == "" {
					record.CycleID = catalogDiscontinuedCycleID(float64(now.Unix()))
				}
				serverName := record.ServerName
				for _, mode := range []string{"抢购", "监控"} {
					if _, exists := trackedPlan.Modes[mode]; exists {
						transitions = append(transitions, catalogStatusTransition{
							Mode: mode, PlanCode: code, ServerName: serverName,
							CycleID: record.CycleID, Recovered: true,
						})
					}
				}
			}
			record.MissingSince = 0
			record.CycleID = ""
			record.Discontinued = false
			if record.ServerName != "" {
				next[code] = record
			}
			continue
		}

		if record.ServerName == "" {
			record.ServerName = trackedPlan.ServerName
		}
		if record.MissingSince <= 0 {
			record.MissingSince = float64(now.Unix())
			// 目录只在启动补采和整点刷新时更新。已有 active tracker
			// 记录意味着该型号在上一轮仍存在，因此本轮缺失已经覆盖
			// 一个完整刷新间隔；首次观察到的缺失则仍从当前时刻计时。
			if hadPriorRecord && strings.TrimSpace(record.ServerName) != "" && !record.Discontinued {
				record.MissingSince = float64(now.Unix() - int64(types.DiscontinuedCheckIntervalSeconds))
			}
		}
		if record.CycleID == "" {
			record.CycleID = catalogDiscontinuedCycleID(record.MissingSince)
		}
		if !record.Discontinued && now.Unix()-int64(record.MissingSince) >= int64(types.DiscontinuedCheckIntervalSeconds) {
			record.Discontinued = true
			for _, mode := range []string{"抢购", "监控"} {
				if _, exists := trackedPlan.Modes[mode]; exists {
					transitions = append(transitions, catalogStatusTransition{
						Mode: mode, PlanCode: code, ServerName: record.ServerName,
						CycleID: record.CycleID,
					})
				}
			}
		}
		next[code] = record
	}
	return next, transitions
}

func applyDiscontinuedQueueState(state *app.State, desired map[string]bool) error {
	if state == nil || len(desired) == 0 {
		return nil
	}
	return state.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
		for i := range queue {
			if queue[i].Status == "completed" || queue[i].Status == "failed" {
				continue
			}
			if discontinued, ok := desired[queue[i].PlanCode]; ok {
				queue[i].Discontinued = discontinued
			}
		}
		return queue, nil
	})
}

func applyDiscontinuedMonitorState(mon *monitor.Monitor, desired map[string]bool, now time.Time) error {
	if mon == nil || len(desired) == 0 {
		return nil
	}
	return mon.MutateSubscriptions(func(subscriptions []*monitor.Subscription) ([]*monitor.Subscription, error) {
		for _, sub := range subscriptions {
			if sub == nil {
				continue
			}
			discontinued, ok := desired[sub.PlanCode]
			if !ok {
				continue
			}
			if discontinued {
				if !sub.Discontinued {
					sub.Discontinued = true
					sub.DiscontinuedNextCheckAt = float64(now.Unix() + int64(types.DiscontinuedCheckIntervalSeconds))
				} else if sub.DiscontinuedNextCheckAt <= 0 {
					sub.DiscontinuedNextCheckAt = float64(now.Unix() + int64(types.DiscontinuedCheckIntervalSeconds))
				}
				continue
			}
			sub.Discontinued = false
			sub.DiscontinuedNextCheckAt = 0
		}
		return subscriptions, nil
	})
}

func updateDiscontinuedCatalogState(state *app.State, mon *monitor.Monitor, plans, previousPlans []types.ServerPlan) error {
	if state == nil || state.DB == nil {
		return fmt.Errorf("停售状态刷新依赖数据库")
	}
	tracked := trackedCatalogPlans(state, mon, previousPlans)
	records := map[string]discontinuedPlanState{}
	if ok, err := state.DB.GetKV(serverCatalogDiscontinuedKey, &records); err != nil {
		return err
	} else if !ok || records == nil {
		records = map[string]discontinuedPlanState{}
	}
	now := time.Now()
	next, transitions := advanceDiscontinuedPlans(records, tracked, catalogPlansByCode(plans), now)
	desired := make(map[string]bool, len(tracked))
	for code := range tracked {
		desired[code] = false
	}
	for code, record := range next {
		if _, ok := tracked[code]; ok {
			desired[code] = record.Discontinued
		}
	}
	if err := applyDiscontinuedQueueState(state, desired); err != nil {
		return fmt.Errorf("更新抢购停售状态失败: %w", err)
	}
	if mon != nil {
		if !mon.LoadReady() {
			return fmt.Errorf("监控订阅尚未加载，无法更新停售状态")
		}
		if err := applyDiscontinuedMonitorState(mon, desired, now); err != nil {
			return fmt.Errorf("更新监控停售状态失败: %w", err)
		}
	}
	channels := monitor.NotificationTargetChannels(state)
	entries := make([]types.NotificationOutboxEntry, 0, len(transitions))
	for _, transition := range transitions {
		entry, err := monitor.NewCatalogStatusNotification(
			transition.Mode, transition.PlanCode, transition.ServerName,
			transition.CycleID, transition.Recovered, channels,
		)
		if err != nil {
			return err
		}
		entries = append(entries, *entry)
	}
	// 先写幂等 outbox，再推进 tracker。若进程在两步之间退出，下一轮会
	// 重试相同 event key；反过来会有“状态已推进但通知永远没有事件”的窗口。
	for _, entry := range entries {
		if err := state.DB.EnqueueNotification(entry); err != nil {
			return fmt.Errorf("保存型号状态通知失败: %w", err)
		}
	}
	if err := state.DB.SetKV(serverCatalogDiscontinuedKey, next); err != nil {
		return fmt.Errorf("保存停售追踪状态失败: %w", err)
	}
	if len(entries) > 0 && mon != nil {
		mon.DispatchNotificationOutbox()
	}
	return nil
}
