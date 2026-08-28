package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/types"
)

// EnqueueSingle 受控入队：账户绑定 + 去重 + 可选询价 + 队列硬顶。
// 用于按钮一键下单与 /buy 单配置路径。
func EnqueueSingle(state *app.State, accountID, planCode, datacenter string, options []string, requirePrice bool) OrderResult {
	planCode = strings.TrimSpace(planCode)
	datacenter = strings.ToLower(strings.TrimSpace(datacenter))
	if accountID == "" {
		accountID = DefaultAccountID(state)
	}
	if accountID == "" {
		return OrderResult{Success: false, Message: "未配置任何 OVH 账户"}
	}
	if planCode == "" || datacenter == "" {
		return OrderResult{Success: false, Message: "缺少 planCode 或 datacenter"}
	}
	// 无 options 时尝试从可用性补全
	if len(options) == 0 {
		availabilityResult, availabilityErr := catalog.CheckServerAvailabilityWithConfigsStrict(state, planCode, accountID)
		if availabilityErr != nil {
			return OrderResult{Success: false, Message: "无法安全获取指定配置库存：" + availabilityErr.Error()}
		}
		for _, cfg := range availabilityResult.Configs {
			if st, ok := cfg.Datacenters[datacenter]; ok && catalog.AvailabilityExplicitlyAvailable(st) && len(cfg.Options) > 0 {
				options = append([]string{}, cfg.Options...)
				break
			}
		}
	}

	if requirePrice {
		pr := price.GetInternal(state, accountID, planCode, datacenter, options)
		if !pr.Success {
			err := pr.Error
			if err == "" {
				err = "价格校验失败"
			}
			return OrderResult{Success: false, Message: "价格校验失败：" + err}
		}
	}

	item := NewTelegramQueueItem(accountID, planCode, datacenter, options)
	if err := state.MutateQueueWithHistoryForAccount(accountID, func(queue []types.QueueItem, history []types.PurchaseHistoryEntry) ([]types.QueueItem, error) {
		if recentSuccessDuplicateInHistory(history, accountID, planCode, datacenter, options, time.Now().Unix()) {
			return nil, fmt.Errorf("刚刚已成功下过同配置订单，稍后再试")
		}
		if len(queue) >= MaxQueueLen {
			return nil, fmt.Errorf("队列已满（上限 %d），请清理后再试", MaxQueueLen)
		}
		fp := OptionsFingerprint(options)
		for _, existing := range queue {
			if existing.AccountID == accountID && existing.PlanCode == planCode && existing.Datacenter == datacenter &&
				(existing.Status == "running" || existing.Status == "pending" || existing.Status == "paused") &&
				OptionsFingerprint(existing.Options) == fp {
				return nil, fmt.Errorf("已存在相同配置的购买任务，请勿重复点击")
			}
		}
		return append(queue, item), nil
	}); err != nil {
		state.Logger.Error("Telegram 入队落盘失败: "+err.Error(), "telegram")
		return OrderResult{Success: false, Message: err.Error()}
	}
	state.Logger.Info(fmt.Sprintf("Telegram 受控入队: %s@%s account=%s opts=%v",
		planCode, datacenter, accountID, options), "telegram")
	return OrderResult{
		Success:       true,
		Message:       fmt.Sprintf("已加入队列: %s @ %s", planCode, strings.ToUpper(datacenter)),
		TotalOrders:   1,
		CreatedOrders: 1,
	}
}

// ProcessOrder 重写：带数量/扇出上限、去重、账户绑定、队列硬顶。
// 未指定机房时仅取 1 个可用机房；未指定 options 时仅取 1 套配置（防笛卡尔积爆炸）。
func ProcessOrder(state *app.State, planCode, datacenter string, quantity int, options []string) OrderResult {
	return ProcessOrderForAccount(state, DefaultAccountID(state), planCode, datacenter, quantity, options, true)
}

// ProcessOrderForAccount 执行指定机器人通道、指定 OVH 账户的受控批量入队。
// TG/飞书命令传入当前默认账户；通知按钮传入生成通知时冻结的账户。
func ProcessOrderForAccount(state *app.State, accountID, planCode, datacenter string, quantity int, options []string, fromTelegram bool) OrderResult {
	quantity = ClampQuantity(quantity)
	planCode = strings.TrimSpace(planCode)
	datacenter = strings.ToLower(strings.TrimSpace(datacenter))
	if planCode == "" {
		return OrderResult{Success: false, Message: "缺少 planCode"}
	}
	if accountID == "" {
		return OrderResult{Success: false, Message: "未配置任何 OVH 账户"}
	}
	if _, ok := state.FindAccount(accountID); !ok {
		return OrderResult{Success: false, Message: "指定的 OVH 账户不存在"}
	}

	// 生产建议：强制指定机房，避免「全机房 × 全配置」扇出
	// 若未指定：仅允许最多 MaxDCsWhenNoDC 个机房
	availabilityResult, availabilityErr := catalog.CheckServerAvailabilityWithConfigsStrict(state, planCode, accountID)
	if availabilityErr != nil || len(availabilityResult.Configs) == 0 {
		if availabilityErr != nil {
			return OrderResult{Success: false, Message: "无法安全获取 " + planCode + " 的可用性信息：" + availabilityErr.Error()}
		}
		return OrderResult{Success: false, Message: "无法获取 " + planCode + " 的可用性信息"}
	}
	availByConfig := availabilityResult.Configs

	type configEntry struct {
		key  string
		data *catalog.ConfigAvailability
	}
	configsToOrder := []configEntry{}
	if len(options) > 0 {
		for k, d := range availByConfig {
			if subset(options, d.Options) {
				configsToOrder = append(configsToOrder, configEntry{key: k, data: d})
			}
		}
	} else {
		for k, d := range availByConfig {
			configsToOrder = append(configsToOrder, configEntry{key: k, data: d})
		}
		// 限制配置扇出
		if len(configsToOrder) > MaxConfigsWhenNoOpts {
			configsToOrder = configsToOrder[:MaxConfigsWhenNoOpts]
		}
	}
	if len(configsToOrder) == 0 {
		return OrderResult{Success: false, Message: fmt.Sprintf("未找到匹配的配置（指定选项: %v）", options)}
	}

	availableDCs := map[string]struct{}{}
	for _, e := range configsToOrder {
		for dc, status := range e.data.Datacenters {
			if catalog.AvailabilityExplicitlyAvailable(status) {
				availableDCs[dc] = struct{}{}
			}
		}
	}
	if len(availableDCs) == 0 {
		return OrderResult{Success: false, Message: "所有配置在所有机房都无货"}
	}

	dcsToOrder := []string{}
	if datacenter != "" {
		if _, ok := availableDCs[datacenter]; !ok {
			return OrderResult{Success: false, Message: "指定机房 " + datacenter + " 无货"}
		}
		dcsToOrder = append(dcsToOrder, datacenter)
	} else {
		for dc := range availableDCs {
			dcsToOrder = append(dcsToOrder, dc)
			if len(dcsToOrder) >= MaxDCsWhenNoDC {
				break
			}
		}
		state.Logger.Info(fmt.Sprintf("[机器人下单] 未指定机房，限制为 %d 个: %v", len(dcsToOrder), dcsToOrder), "bot")
	}

	// 预估并硬顶
	planned := len(configsToOrder) * len(dcsToOrder) * quantity
	if planned > MaxOrdersPerRequest {
		return OrderResult{Success: false, Message: fmt.Sprintf(
			"本次将创建 %d 个任务，超过单次上限 %d。请指定机房/配置或减小数量（≤%d）",
			planned, MaxOrdersPerRequest, MaxQuantityPerOrder)}
	}
	ordersToCreate := []types.QueueItem{}
	skippedDup := 0
	err := state.MutateQueueWithHistoryForAccount(accountID, func(queue []types.QueueItem, history []types.PurchaseHistoryEntry) ([]types.QueueItem, error) {
		ordersToCreate = ordersToCreate[:0]
		skippedDup = 0
		nowTS := time.Now().Unix()
		for _, ce := range configsToOrder {
			configOptions := append([]string{}, ce.data.Options...)
			for _, dc := range dcsToOrder {
				status, ok := ce.data.Datacenters[dc]
				if !ok || !catalog.AvailabilityExplicitlyAvailable(status) {
					continue
				}
				if recentSuccessDuplicateInHistory(history, accountID, planCode, dc, configOptions, nowTS) {
					skippedDup += quantity
					continue
				}
				duplicate := false
				fp := OptionsFingerprint(configOptions)
				for _, existing := range queue {
					if existing.AccountID == accountID && existing.PlanCode == planCode && existing.Datacenter == dc &&
						(existing.Status == "running" || existing.Status == "pending" || existing.Status == "paused") &&
						OptionsFingerprint(existing.Options) == fp {
						duplicate = true
						break
					}
				}
				if duplicate {
					skippedDup += quantity
					continue
				}
				for i := 0; i < quantity; i++ {
					item := NewTelegramQueueItem(accountID, planCode, dc, configOptions)
					item.FromTelegram = fromTelegram
					ordersToCreate = append(ordersToCreate, item)
				}
			}
		}
		if len(ordersToCreate) == 0 {
			return nil, fmt.Errorf("近期已成功下单或已存在相同配置的活跃任务，跳过重复入队")
		}
		if len(queue)+len(ordersToCreate) > MaxQueueLen {
			return nil, fmt.Errorf("队列空间不足（当前上限 %d）", MaxQueueLen)
		}
		return append(queue, ordersToCreate...), nil
	})
	if err != nil {
		state.Logger.Error("机器人批量入队落盘失败: "+err.Error(), "bot")
		return OrderResult{Success: false, Message: err.Error()}
	}
	created := len(ordersToCreate)
	state.Logger.Info(fmt.Sprintf("机器人受控批量入队: %d 个 (skip_dup=%d) account=%s", created, skippedDup, accountID), "bot")
	return OrderResult{
		Success:       true,
		Message:       fmt.Sprintf("已创建 %d 个订单", created),
		TotalOrders:   created,
		CreatedOrders: created,
	}
}
