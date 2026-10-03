package monitor

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

type broadcastField struct {
	Label string
	Value string
}

type taskBroadcastItem struct {
	AccountID   string
	Account     string
	Model       string
	PlanCode    string
	Memory      string
	Storage     string
	Bandwidth   string
	Datacenters string
	Monthly     string
	Install     string
	Total       string
	Extra       []broadcastField
	AutoPay     bool
}

type queueTaskGroup struct {
	AccountID        string
	PlanCode         string
	Options          []string
	AutoPay          bool
	DatacenterCounts map[string]int
}

func formatTaskBroadcastItem(item taskBroadcastItem) string {
	lines := []string{
		"型号：" + displayOrUnavailable(item.Model),
		"Plan Code：" + displayOrUnavailable(item.PlanCode),
		"内存：" + displayOrUnavailable(item.Memory),
		"数据盘：" + displayOrUnavailable(item.Storage),
		"带宽：" + displayOrUnavailable(item.Bandwidth),
		"数据中心：" + displayOrUnavailable(item.Datacenters),
		"月费：" + displayOrUnavailable(item.Monthly),
		"安装费：" + displayOrUnavailable(item.Install),
		"总价：" + displayOrUnavailable(item.Total),
	}
	for _, field := range item.Extra {
		if strings.TrimSpace(field.Label) == "" {
			continue
		}
		lines = append(lines, field.Label+"："+displayOrUnavailable(field.Value))
	}
	lines = append(lines, "自动付款："+boolLabel(item.AutoPay))
	return strings.Join(lines, "\n")
}

const taskBroadcastMessageMaxBytes = 2500
const taskBroadcastPartTitleReserveBytes = 32

func splitTaskBroadcastText(text string, maxBytes int) []string {
	if maxBytes < 1 {
		return []string{text}
	}
	chunks := make([]string, 0, len(text)/maxBytes+1)
	for start := 0; start < len(text); {
		end := start
		for end < len(text) {
			_, size := utf8.DecodeRuneInString(text[end:])
			if end > start && end-start+size > maxBytes {
				break
			}
			end += size
		}
		chunks = append(chunks, text[start:end])
		start = end
	}
	return chunks
}

func taskBroadcastCategoryHeader(title string, taskCount, part, total int) string {
	title = strings.TrimSpace(title)
	if total > 1 {
		title = fmt.Sprintf("%s（%d/%d）", title, part, total)
	}
	return fmt.Sprintf("%s\n\n正在执行任务数：%d\n\n", title, taskCount)
}

func formatTaskBroadcastCategory(title string, items []taskBroadcastItem) []string {
	return formatTaskBroadcastItems(items, func(part, total int) string {
		return taskBroadcastCategoryHeader(title, len(items), part, total)
	})
}

func formatTaskBroadcastItems(items []taskBroadcastItem, header func(part, total int) string) []string {
	if len(items) == 0 {
		return nil
	}
	type accountGroup struct {
		key   string
		name  string
		items []taskBroadcastItem
	}
	groups := make([]accountGroup, 0)
	groupIndexes := make(map[string]int)
	for _, item := range items {
		accountID := strings.TrimSpace(item.AccountID)
		groupKey := "id:" + accountID
		if accountID == "" {
			groupKey = "name:" + strings.TrimSpace(item.Account)
		}
		index, ok := groupIndexes[groupKey]
		if !ok {
			index = len(groups)
			groupIndexes[groupKey] = index
			groups = append(groups, accountGroup{key: groupKey, name: item.Account})
		}
		groups[index].items = append(groups[index].items, item)
	}

	baseHeader := header(0, 0)
	maxBodyBytes := taskBroadcastMessageMaxBytes - len(baseHeader) - taskBroadcastPartTitleReserveBytes
	bodies := make([]string, 0, 1)
	currentBody := ""
	currentAccountKey := ""
	for _, group := range groups {
		accountLine := "OVH 账号：" + displayOrUnavailable(group.name)
		for taskIndex, item := range group.items {
			taskLabel := fmt.Sprintf("任务%d：", taskIndex+1)
			taskText := formatTaskBroadcastItem(item)
			task := taskLabel + "\n" + taskText
			fullTaskBlock := accountLine + "\n" + task
			block := task
			if currentBody == "" || currentAccountKey != group.key {
				block = fullTaskBlock
			}
			if len(fullTaskBlock) > maxBodyBytes {
				if currentBody != "" {
					bodies = append(bodies, currentBody)
					currentBody = ""
				}
				firstPrefix := taskLabel + "\n"
				continuationPrefix := fmt.Sprintf("任务%d（续）：\n", taskIndex+1)
				firstChunkBytes := maxBodyBytes - len(accountLine) - 1 - len(firstPrefix)
				continuationChunkBytes := maxBodyBytes - len(accountLine) - 1 - len(continuationPrefix)
				chunks := splitTaskBroadcastText(taskText, firstChunkBytes)
				if len(chunks) > 1 {
					chunks = append([]string{chunks[0]}, splitTaskBroadcastText(strings.Join(chunks[1:], ""), continuationChunkBytes)...)
				}
				for chunkIndex, chunk := range chunks {
					prefix := firstPrefix
					if chunkIndex > 0 {
						prefix = continuationPrefix
					}
					bodies = append(bodies, accountLine+"\n"+prefix+chunk)
				}
				currentAccountKey = ""
				continue
			}
			candidate := block
			if currentBody != "" {
				candidate = currentBody + "\n\n" + block
			}
			if len(candidate) > maxBodyBytes {
				bodies = append(bodies, currentBody)
				currentBody = accountLine + "\n" + task
			} else {
				currentBody = candidate
			}
			currentAccountKey = group.key
		}
	}
	if currentBody != "" {
		bodies = append(bodies, currentBody)
	}

	messages := make([]string, 0, len(bodies))
	for i, body := range bodies {
		messages = append(messages, header(i+1, len(bodies))+body)
	}
	return messages
}

type TaskBroadcastSelection struct {
	Queue   bool
	Monitor bool
	VPS     bool
	Report  bool
}

func (m *Monitor) BuildTaskBroadcastMessages(ctx context.Context, selection TaskBroadcastSelection) []string {
	if m == nil || m.state == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	messages := make([]string, 0, 8)
	if selection.Queue {
		messages = append(messages, m.buildQueueBroadcastMessages(ctx)...)
	}
	if selection.Monitor {
		messages = append(messages, m.buildMonitorBroadcastMessages(ctx)...)
	}
	if selection.VPS {
		messages = append(messages, m.buildVPSBroadcastMessages(ctx)...)
	}
	return messages
}

func (m *Monitor) buildQueueBroadcastMessages(ctx context.Context) []string {
	m.state.QueueMu.Lock()
	items := append([]types.QueueItem(nil), m.state.Queue...)
	m.state.QueueMu.Unlock()
	groups := aggregateQueueTasks(items)
	entries := make([]taskBroadcastItem, 0, len(groups))
	for _, group := range groups {
		if ctx.Err() != nil {
			break
		}
		plan := m.serverPlan(group.PlanCode)
		memory, storage, bandwidth := planSpecifications(plan, group.Options)
		dcs := sortedDatacenterCounts(group.DatacenterCounts)
		monthly, install, total := m.priceParts(ctx, group.AccountID, group.PlanCode, group.Options, dcs)
		entries = append(entries, taskBroadcastItem{
			AccountID: group.AccountID, Account: m.accountName(group.AccountID),
			Model: planName(plan, group.PlanCode), PlanCode: group.PlanCode,
			Memory: memory, Storage: storage, Bandwidth: bandwidth,
			Datacenters: strings.Join(dcs, "，"), Monthly: monthly, Install: install, Total: total,
			Extra: []broadcastField{{Label: "抢购数量", Value: formatDatacenterCounts(group.DatacenterCounts)}}, AutoPay: group.AutoPay,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return formatTaskBroadcastCategory("🛒 抢购任务", entries)
}

func (m *Monitor) buildMonitorBroadcastMessages(ctx context.Context) []string {
	entries := make([]taskBroadcastItem, 0)
	for _, sub := range m.Snapshot() {
		if sub == nil || ctx.Err() != nil {
			break
		}
		plan := m.serverPlan(sub.PlanCode)
		memory := strings.Join(sub.Memories, "，")
		storage := strings.Join(sub.Storages, "，")
		network := strings.Join(sub.Networks, "，")
		if memory == "" {
			memory = planValue(plan, func(p types.ServerPlan) string { return p.Memory })
		}
		if storage == "" {
			storage = planValue(plan, func(p types.ServerPlan) string { return p.Storage })
		}
		if network == "" {
			network = planValue(plan, func(p types.ServerPlan) string { return p.Bandwidth })
		}
		dcs := normalizedDatacenters(sub.Datacenters)
		accountID := m.resolvePriceAccount(sub)
		monthly, install, total := m.priceParts(ctx, accountID, sub.PlanCode, nil, dcs)
		entries = append(entries, taskBroadcastItem{
			AccountID: accountID, Account: m.accountName(accountID),
			Model: firstNonEmpty(sub.ServerName, planName(plan, sub.PlanCode)), PlanCode: sub.PlanCode,
			Memory: memory, Storage: storage, Bandwidth: network,
			Datacenters: displayDatacenters(dcs), Monthly: monthly, Install: install, Total: total,
			Extra: []broadcastField{
				{Label: "有货提醒", Value: boolLabel(sub.NotifyAvailable)},
				{Label: "无货提醒", Value: boolLabel(sub.NotifyUnavailable)},
				{Label: "有货自动下单", Value: boolLabel(sub.AutoOrder)},
			},
			AutoPay: sub.AutoPay,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return formatTaskBroadcastCategory("🖥️ 独服监控", entries)
}

func (m *Monitor) buildVPSBroadcastMessages(ctx context.Context) []string {
	entries := make([]taskBroadcastItem, 0)
	for _, sub := range m.state.VPSSubscriptionsSnapshot() {
		if ctx.Err() != nil {
			break
		}
		accountID := sub.AutoOrderAccountID
		if accountID == "" {
			accountID = defaultAccountID(m.state)
		}
		model, err := m.resolveVPSBroadcastModel(ctx, accountID, sub.OvhSubsidiary, sub.PlanCode)
		if err != nil {
			model = VPSBroadcastModel{}
		}
		dcs := normalizedDatacenters(sub.Datacenters)
		quantity := sub.Quantity
		if quantity < 1 {
			quantity = 1
		}
		entries = append(entries, taskBroadcastItem{
			AccountID: accountID, Account: m.accountName(accountID),
			Model: firstNonEmpty(model.Name, sub.PlanCode), PlanCode: sub.PlanCode,
			Memory: model.Memory, Storage: model.Storage, Bandwidth: model.Bandwidth,
			Datacenters: displayDatacenters(dcs), Monthly: model.Monthly, Install: model.Install, Total: model.Total,
			Extra: []broadcastField{
				{Label: "有货提醒", Value: boolLabel(sub.NotifyAvailable)},
				{Label: "无货提醒", Value: boolLabel(sub.NotifyUnavailable)},
				{Label: "Linux 系统", Value: boolLabel(sub.MonitorLinux)},
				{Label: "Windows 系统", Value: boolLabel(sub.MonitorWindows)},
				{Label: "有货自动下单", Value: boolLabel(sub.AutoOrder)},
				{Label: "数量", Value: strconv.Itoa(quantity)},
			},
			AutoPay: sub.AutoPay,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return formatTaskBroadcastCategory("🖥️ VPS 监控", entries)
}

func (m *Monitor) BuildBattleReport() string {
	if m == nil || m.state == nil {
		return ""
	}
	m.state.HistoryMu.Lock()
	history := append([]types.PurchaseHistoryEntry(nil), m.state.History...)
	m.state.HistoryMu.Unlock()
	m.state.AccountsMu.RLock()
	accounts := make(map[string]string, len(m.state.Accounts))
	for _, account := range m.state.Accounts {
		accounts[account.ID] = firstNonEmpty(account.Name, account.ID)
	}
	m.state.AccountsMu.RUnlock()
	return m.formatBattleReport(history, time.Now(), accounts)
}

func (m *Monitor) formatBattleReport(history []types.PurchaseHistoryEntry, now time.Time, accountNames map[string]string) string {
	var resolve func(float64, string) (float64, string, bool)
	if m != nil && m.state != nil && m.state.Exchange != nil {
		resolve = m.state.Exchange.ResolveDisplayAmount
	}
	return formatBattleReportWithResolver(history, now, accountNames, resolve)
}

func (m *Monitor) priceParts(ctx context.Context, accountID, planCode string, options, dcs []string) (string, string, string) {
	if len(dcs) == 0 {
		dcs = []string{""}
	}
	monthly, install, total := map[string]string{}, map[string]string{}, map[string]string{}
	for _, dc := range dcs {
		if ctx.Err() != nil {
			break
		}
		var display price.DisplayPrice
		var err error
		if dc != "" {
			display, err = price.GetDisplayWithContext(ctx, m.state, accountID, planCode, dc, options)
		} else {
			display, err = price.GetCatalogDisplayWithContext(ctx, m.state, accountID, planCode, options)
		}
		if err != nil {
			display, err = price.GetCatalogDisplayWithContext(ctx, m.state, accountID, planCode, options)
		}
		if err != nil {
			continue
		}
		month, setup, first := m.displayPricePartsForNotification(display)
		monthly[dc], install[dc], total[dc] = month, setup, first
	}
	return joinPriceValues(monthly), joinPriceValues(install), joinPriceValues(total)
}

func displayPriceParts(display price.DisplayPrice) (string, string, string) {
	monthly, install, total := "暂不可用", "暂不可用", "暂不可用"
	if display.BreakdownKnown {
		monthly = formatCurrency(display.MonthlyWithoutTax, display.Currency) + "/月"
		install = "无"
		if display.InstallWithoutTax > 0 {
			install = formatCurrency(display.InstallWithoutTax, display.Currency)
		}
		if display.TotalWithoutTaxKnown {
			total = formatCurrency(display.TotalWithoutTax, display.Currency)
		} else {
			total = formatCurrency(display.MonthlyWithoutTax+display.InstallWithoutTax, display.Currency)
		}
	} else if display.TotalWithoutTaxKnown {
		total = formatCurrency(display.TotalWithoutTax, display.Currency)
	}
	return monthly, install, total
}

func joinPriceValues(values map[string]string) string {
	if len(values) == 0 {
		return "暂不可用"
	}
	keys := make([]string, 0, len(values))
	unique := map[string]struct{}{}
	for key, value := range values {
		keys = append(keys, key)
		unique[value] = struct{}{}
	}
	sort.Strings(keys)
	if len(unique) == 1 {
		for value := range unique {
			return value
		}
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		if key == "" {
			parts = append(parts, value)
		} else {
			parts = append(parts, key+"："+value)
		}
	}
	return strings.Join(parts, "；")
}

func (m *Monitor) SendTaskBroadcastWithContext(ctx context.Context, selection TaskBroadcastSelection) int {
	if m == nil || m.state == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	messages := m.BuildTaskBroadcastMessages(ctx, selection)
	if selection.Report {
		if report := m.BuildBattleReport(); report != "" {
			messages = append(messages, report)
		}
	}
	if len(messages) == 0 {
		return 0
	}
	channels := ConfiguredNotificationChannels(m.state)
	if len(channels) == 0 {
		if m.state.Logger != nil {
			m.state.Logger.Warn("任务播报没有可用通知渠道", "task_broadcast")
		}
		return 0
	}
	if err := m.state.LockNotificationOutboxContext(ctx); err != nil {
		return 0
	}
	defer m.state.UnlockNotificationOutbox()
	sent := 0
	for _, message := range messages {
		if ctx.Err() != nil {
			break
		}
		lines := strings.SplitN(message, "\n", 2)
		title := lines[0]
		for _, channel := range channels {
			var ok bool
			switch channel {
			case NotificationChannelTelegram:
				ok = telegram.SendMessageWithContext(ctx, m.state, message, nil)
			case NotificationChannelFeishu:
				ok = FeishuSendDefaultNotificationWithContext(ctx, m.state, title, message, "blue", nil)
			case NotificationChannelQQ:
				ok = SendQQMonitorNotificationWithContext(ctx, m.state, message)
			}
			if ok {
				sent++
			}
		}
	}
	return sent
}

func (m *Monitor) SendStartupTaskBroadcast(ctx context.Context) int {
	return m.SendTaskBroadcastWithContext(ctx, startupTaskBroadcastSelection())
}

func (m *Monitor) RunTaskBroadcastLoop(ctx context.Context) {
	if m == nil || m.state == nil || m.state.Config == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loc := beijingLocation()
	for {
		cfg := m.state.Config.Get()
		if !taskBroadcastFlag(cfg.TaskBroadcastEnabled) {
			if !waitBroadcast(ctx, time.Minute) {
				return
			}
			continue
		}
		timeText := cfg.TaskBroadcastTime
		if _, err := normalizeBroadcastTime(timeText); err != nil {
			timeText = "09:00"
		}
		next := nextDailyBroadcastAt(time.Now(), timeText, loc)
		wait := time.Until(next)
		if wait > time.Minute {
			wait = time.Minute
		}
		if !waitBroadcast(ctx, wait) {
			return
		}
		if time.Now().In(loc).Before(next) {
			continue
		}
		current := m.state.Config.Get()
		if !taskBroadcastFlag(current.TaskBroadcastEnabled) || current.TaskBroadcastTime != cfg.TaskBroadcastTime {
			continue
		}
		selection := scheduledTaskBroadcastSelection(current)
		m.SendTaskBroadcastWithContext(ctx, selection)
	}
}

func scheduledTaskBroadcastSelection(cfg types.Config) TaskBroadcastSelection {
	return TaskBroadcastSelection{
		Queue:   taskBroadcastFlag(cfg.TaskBroadcastQueueEnabled),
		Monitor: taskBroadcastFlag(cfg.TaskBroadcastMonitorEnabled),
		VPS:     taskBroadcastFlag(cfg.TaskBroadcastVPSEnabled),
		Report:  taskBroadcastFlag(cfg.TaskBroadcastReportEnabled),
	}
}

func startupTaskBroadcastSelection() TaskBroadcastSelection {
	return TaskBroadcastSelection{Queue: true, Monitor: true, VPS: true}
}

func taskBroadcastFlag(value *bool) bool { return value != nil && *value }

func waitBroadcast(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func beijingLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}

func normalizeBroadcastTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 5 || value[2] != ':' {
		return "", fmt.Errorf("invalid daily broadcast time")
	}
	hour, hourErr := strconv.Atoi(value[:2])
	minute, minuteErr := strconv.Atoi(value[3:])
	if hourErr != nil || minuteErr != nil || hour > 23 || minute > 59 {
		return "", fmt.Errorf("invalid daily broadcast time")
	}
	return value, nil
}

func (m *Monitor) serverPlan(planCode string) *types.ServerPlan {
	if m == nil || m.state == nil {
		return nil
	}
	m.state.ServerPlansMu.RLock()
	defer m.state.ServerPlansMu.RUnlock()
	for _, plan := range m.state.ServerPlans {
		if strings.EqualFold(plan.PlanCode, planCode) {
			copyPlan := plan
			return &copyPlan
		}
	}
	return nil
}

func planName(plan *types.ServerPlan, fallback string) string {
	if plan != nil && strings.TrimSpace(plan.Name) != "" {
		return plan.Name
	}
	return fallback
}

func planValue(plan *types.ServerPlan, getter func(types.ServerPlan) string) string {
	if plan == nil {
		return ""
	}
	return getter(*plan)
}

func planSpecifications(plan *types.ServerPlan, options []string) (string, string, string) {
	memory, storage, bandwidth := planValue(plan, func(p types.ServerPlan) string { return p.Memory }), planValue(plan, func(p types.ServerPlan) string { return p.Storage }), planValue(plan, func(p types.ServerPlan) string { return p.Bandwidth })
	if plan == nil {
		return memory, storage, bandwidth
	}
	for _, selected := range options {
		for _, option := range plan.AvailableOptions {
			if !strings.EqualFold(strings.TrimSpace(selected), strings.TrimSpace(option.Value)) && !strings.EqualFold(strings.TrimSpace(selected), strings.TrimSpace(option.Label)) {
				continue
			}
			family := strings.ToLower(option.Family)
			label := option.Label
			switch {
			case strings.Contains(family, "memory") || strings.Contains(family, "ram"):
				memory = firstNonEmpty(catalog.FormatMemoryDisplay(label), label)
			case strings.Contains(family, "storage") || strings.Contains(family, "disk") || strings.Contains(family, "drive"):
				storage = firstNonEmpty(catalog.FormatStorageDisplay(label), label)
			case strings.Contains(family, "bandwidth") || strings.Contains(family, "network") || strings.Contains(family, "traffic"):
				bandwidth = label
			}
		}
	}
	return memory, storage, bandwidth
}

func (m *Monitor) accountName(accountID string) string {
	if m == nil || m.state == nil {
		return accountID
	}
	if account, ok := m.state.FindAccount(accountID); ok {
		return firstNonEmpty(account.Name, account.ID)
	}
	return firstNonEmpty(accountID, "未指定账户")
}

func defaultAccountID(state interface {
	FindAccount(string) (types.OVHAccount, bool)
}) string {
	if account, ok := state.FindAccount(""); ok {
		return account.ID
	}
	return ""
}

func normalizedDatacenters(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedDatacenterCounts(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatDatacenterCounts(values map[string]int) string {
	keys := sortedDatacenterCounts(values)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d 台", key, values[key]))
	}
	return strings.Join(parts, "，")
}

func displayDatacenters(values []string) string {
	if len(values) == 0 {
		return "全部"
	}
	return strings.Join(values, "，")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func displayOrUnavailable(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "暂不可用"
}

func boolLabel(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func aggregateQueueTasks(items []types.QueueItem) []queueTaskGroup {
	groups := make(map[string]*queueTaskGroup)
	for _, item := range items {
		if !isActiveQueueStatus(item.Status) {
			continue
		}
		options := append([]string(nil), item.Options...)
		key := queueTaskKey(item.AccountID, item.PlanCode, options, item.AutoPay)
		group := groups[key]
		if group == nil {
			group = &queueTaskGroup{
				AccountID: item.AccountID, PlanCode: item.PlanCode, Options: options,
				AutoPay: item.AutoPay, DatacenterCounts: map[string]int{},
			}
			groups[key] = group
		}
		dc := strings.ToUpper(strings.TrimSpace(item.Datacenter))
		if dc == "" {
			dc = "未知"
		}
		group.DatacenterCounts[dc]++
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]queueTaskGroup, 0, len(keys))
	for _, key := range keys {
		group := *groups[key]
		group.Options = append([]string(nil), group.Options...)
		out = append(out, group)
	}
	return out
}

func queueTaskKey(accountID, planCode string, options []string, autoPay bool) string {
	return strings.Join([]string{strings.TrimSpace(accountID), strings.TrimSpace(planCode), strings.Join(options, "\x00"), strconv.FormatBool(autoPay)}, "\x01")
}

func isActiveQueueStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "pending", "paused":
		return true
	default:
		return false
	}
}

func nextDailyBroadcastAt(now time.Time, hhmm string, loc *time.Location) time.Time {
	if loc == nil {
		loc = now.Location()
	}
	parts := strings.Split(strings.TrimSpace(hhmm), ":")
	hour, minute := 0, 0
	if len(parts) == 2 {
		hour, _ = strconv.Atoi(parts[0])
		minute, _ = strconv.Atoi(parts[1])
	}
	localNow := now.In(loc)
	next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
	if !next.After(localNow) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

type battleReportAccount struct {
	Name           string
	Successful     int
	Unpaid         int
	Paid           int
	UnpaidAmount   float64
	PaidAmount     float64
	UnpaidCurrency string
	PaidCurrency   string
	UnpaidKnown    bool
	PaidKnown      bool
}

func formatBattleReport(history []types.PurchaseHistoryEntry, now time.Time, accountNames map[string]string) string {
	return formatBattleReportWithResolver(history, now, accountNames, nil)
}

func formatBattleReportWithResolver(history []types.PurchaseHistoryEntry, now time.Time, accountNames map[string]string, resolve func(float64, string) (float64, string, bool)) string {
	accounts := make(map[string]*battleReportAccount)
	cutoff := now.Add(-24 * time.Hour)
	for _, entry := range history {
		if strings.ToLower(strings.TrimSpace(entry.Status)) != "success" {
			continue
		}
		when, ok := types.ParseTS(entry.PurchaseTime)
		if !ok || when.Before(cutoff) || when.After(now) {
			continue
		}
		id := strings.TrimSpace(entry.AccountID)
		name := strings.TrimSpace(accountNames[id])
		if name == "" {
			name = id
		}
		if name == "" {
			name = "未指定账户"
		}
		account := accounts[id]
		if account == nil {
			account = &battleReportAccount{Name: name, UnpaidKnown: true, PaidKnown: true}
			accounts[id] = account
		}
		account.Successful++
		if isUnpaidBattleStatus(entry.OrderStatus) {
			account.Unpaid++
			if entry.Price == nil || entry.Price.WithoutTax == nil {
				account.UnpaidKnown = false
			} else if amount, currency, ok := resolveBattleAmount(*entry.Price.WithoutTax, entry.Price.CurrencyCode, resolve); !ok {
				account.UnpaidKnown = false
			} else {
				account.UnpaidAmount += amount
				account.UnpaidCurrency = mergeCurrency(account.UnpaidCurrency, currency)
			}
		} else {
			account.Paid++
			if entry.Price == nil || entry.Price.WithoutTax == nil {
				account.PaidKnown = false
			} else if amount, currency, ok := resolveBattleAmount(*entry.Price.WithoutTax, entry.Price.CurrencyCode, resolve); !ok {
				account.PaidKnown = false
			} else {
				account.PaidAmount += amount
				account.PaidCurrency = mergeCurrency(account.PaidCurrency, currency)
			}
		}
	}
	keys := make([]string, 0, len(accounts))
	for id := range accounts {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := accounts[keys[i]], accounts[keys[j]]
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return keys[i] < keys[j]
	})
	lines := []string{"🏆 抢购战报"}
	if len(keys) == 0 {
		lines = append(lines, "", "OVH 账号：暂无",
			"过去 24 小时成功下单：0",
			"未支付订单：0 单",
			"未支付金额：0",
			"已支付订单：0 单",
			"已支付金额：0")
	}
	for _, id := range keys {
		account := accounts[id]
		lines = append(lines, "", "OVH 账号："+account.Name,
			fmt.Sprintf("过去 24 小时成功下单：%d", account.Successful),
			fmt.Sprintf("未支付订单：%d 单", account.Unpaid),
			"未支付金额："+battleAmount(account.Unpaid, account.UnpaidKnown, account.UnpaidAmount, account.UnpaidCurrency),
			fmt.Sprintf("已支付订单：%d 单", account.Paid),
			"已支付金额："+battleAmount(account.Paid, account.PaidKnown, account.PaidAmount, account.PaidCurrency))
	}
	return strings.Join(lines, "\n")
}

func isUnpaidBattleStatus(status string) bool {
	normalized := strings.ToLower(strings.TrimSpace(status))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	switch normalized {
	case "", "notpaid", "unpaid", "paymentpending", "pendingpayment", "waitingpayment", "pending", "created", "new":
		return true
	default:
		return false
	}
}

func mergeCurrency(current, next string) string {
	next = strings.ToUpper(strings.TrimSpace(next))
	if current == "" {
		return next
	}
	if next == "" || current != next {
		return ""
	}
	return current
}

func resolveBattleAmount(amount float64, currency string, resolve func(float64, string) (float64, string, bool)) (float64, string, bool) {
	if resolve == nil {
		return amount, currency, true
	}
	return resolve(amount, currency)
}

func battleAmount(count int, known bool, amount float64, currency string) string {
	if count == 0 {
		return "0"
	}
	if !known || currency == "" {
		return "暂不可用"
	}
	return formatCurrency(amount, currency)
}
