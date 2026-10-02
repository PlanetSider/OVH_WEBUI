package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/types"
)

const queueListPriceTimeout = 5 * time.Second

// BuildQueueListMessages reads enabled queue tasks across all accounts without
// changing tasks or sending notifications. Prices are per-server catalog prices.
func (m *Monitor) BuildQueueListMessages(ctx context.Context) []string {
	if m == nil || m.state == nil {
		return formatQueueListMessages(nil, 0)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, queueListPriceTimeout)
	defer cancel()

	defaultID := defaultAccountID(m.state)
	m.state.QueueMu.Lock()
	items := make([]types.QueueItem, 0, len(m.state.Queue))
	for _, item := range m.state.Queue {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if (status != "pending" && status != "running") || item.Discontinued || item.ProxyGuardPaused {
			continue
		}
		item.AccountID = strings.TrimSpace(item.AccountID)
		if item.AccountID == "" {
			item.AccountID = defaultID
		}
		item.PlanCode = strings.TrimSpace(item.PlanCode)
		item.Options = append([]string(nil), item.Options...)
		for i := range item.Options {
			item.Options[i] = strings.TrimSpace(item.Options[i])
		}
		sort.Strings(item.Options)
		items = append(items, item)
	}
	m.state.QueueMu.Unlock()

	groups := aggregateQueueTasks(items)
	entries := make([]taskBroadcastItem, 0, len(groups))
	prices := make(map[string][3]string)
	for _, group := range groups {
		plan := m.serverPlan(group.PlanCode)
		memory, storage, bandwidth := planSpecifications(plan, group.Options)
		priceKey := queueTaskKey(group.AccountID, group.PlanCode, group.Options, false)
		parts, ok := prices[priceKey]
		if !ok {
			parts = [3]string{"暂不可用", "暂不可用", "暂不可用"}
			if m.state.OVH != nil && ctx.Err() == nil {
				if _, exists := m.state.FindAccount(group.AccountID); exists {
					if display, err := price.GetCatalogDisplayWithContext(ctx, m.state, group.AccountID, group.PlanCode, group.Options); err == nil {
						parts[0], parts[1], parts[2] = displayPriceParts(display)
					}
				}
			}
			prices[priceKey] = parts
		}
		entries = append(entries, taskBroadcastItem{
			AccountID: group.AccountID, Account: m.accountName(group.AccountID),
			Model: planName(plan, group.PlanCode), PlanCode: group.PlanCode,
			Memory: memory, Storage: storage, Bandwidth: bandwidth,
			Datacenters: strings.Join(sortedDatacenterCounts(group.DatacenterCounts), "，"),
			Monthly:     parts[0], Install: parts[1], Total: parts[2],
			Extra:   []broadcastField{{Label: "抢购数量", Value: formatDatacenterCounts(group.DatacenterCounts)}},
			AutoPay: group.AutoPay,
		})
	}
	return formatQueueListMessages(entries, len(items))
}

func formatQueueListMessages(items []taskBroadcastItem, quantity int) []string {
	if len(items) == 0 {
		return []string{"当前没有开启的抢购任务。"}
	}
	return formatTaskBroadcastItems(items, func(part, total int) string {
		title := "当前开启的抢购任务"
		if total > 1 {
			title = fmt.Sprintf("%s（%d/%d）", title, part, total)
		}
		return fmt.Sprintf("%s\n\n开启任务：%d组\n抢购总数量：%d台\n\n", title, len(items), quantity)
	})
}
