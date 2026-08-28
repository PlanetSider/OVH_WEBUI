package handlers

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

const (
	telegramPlanCardMaxRunes = 900
	feishuPlanCardMaxRunes  = 1800
	serverModelRefreshDelay = 5 * time.Minute
)

var (
	serverModelSpaces      = regexp.MustCompile(`\s+`)
	serverModelDashSpaces  = regexp.MustCompile(`\s*-\s*`)
	serverModelSpaceJoin   = regexp.MustCompile(`(?i)^([a-z][a-z0-9]*)\s+(\d[a-z0-9-]*)$`)
	serverMemoryOption     = regexp.MustCompile(`(?i)ram-(\d+)g`)
	serverMemoryFrequency  = regexp.MustCompile(`(?i)(?:^|[-_])((?:no)?ecc-\d+)(?:[-_]|$)`)
	serverBandwidthOption  = regexp.MustCompile(`(?i)bandwidth-(\d+)`)
	serverTrafficBandwidth = regexp.MustCompile(`(?i)traffic-(\d+)(tb|gb|mb)-(\d+)`)
	serverTrafficOption    = regexp.MustCompile(`(?i)traffic-(\d+)(tb|gb|mb)`)
	serverModelQuery       = regexp.MustCompile(`(?i)^[a-z][a-z0-9]*-\d[a-z0-9-]*$`)
	serverModelIdentifier  = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]*\s*-\s*\d[a-z0-9-]*\b`)
	serverModelDashes      = strings.NewReplacer("‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "﹘", "-", "﹣", "-", "－", "-")
	serverModelPlanAliases = map[string]map[string]struct{}{
		"ks-1": {"24sk102": {}, "26sk10b-v1": {}},
	}
	serverModelRefreshMu   sync.Mutex
	serverModelLastRefresh time.Time
)

type serverPlanSection struct {
	Title string
	Lines []string
}

func normalizeServerModel(value string) string {
	value = strings.ToLower(strings.TrimSpace(serverModelDashes.Replace(value)))
	value = serverModelDashSpaces.ReplaceAllString(value, "-")
	value = serverModelSpaces.ReplaceAllString(value, " ")
	if match := serverModelSpaceJoin.FindStringSubmatch(value); match != nil {
		value = match[1] + "-" + match[2]
	}
	return value
}

func looksLikeServerModelQuery(value string) bool {
	return serverModelQuery.MatchString(normalizeServerModel(value))
}

func serverModelCandidates(plan types.ServerPlan) []string {
	candidates := []string{plan.Name}
	if index := strings.Index(plan.Name, "|"); index >= 0 {
		candidates = append(candidates, plan.Name[:index])
	}
	if index := strings.Index(plan.Description, "|"); index >= 0 {
		candidates = append(candidates, plan.Description[:index])
	}
	// 部分 OVH 目录会在型号前加系列名，例如 "Kimsufi Essential | KS-1 | ..."。
	// 从完整名称和描述中提取型号标识，避免漏匹配后被普通文本路径误当成下单请求。
	for _, value := range []string{plan.Name, plan.Description} {
		value = serverModelDashes.Replace(value)
		candidates = append(candidates, serverModelIdentifier.FindAllString(value, -1)...)
	}
	return candidates
}

func appendServerPlansByPlanCode(target []types.ServerPlan, seen map[string]struct{}, plans []types.ServerPlan) []types.ServerPlan {
	for _, plan := range plans {
		key := strings.ToLower(strings.TrimSpace(plan.PlanCode))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		target = append(target, plan)
	}
	return target
}

func serverPlanCatalogSnapshot(state *app.State) []types.ServerPlan {
	if state == nil {
		return nil
	}
	plans := make([]types.ServerPlan, 0)
	seen := make(map[string]struct{})
	state.ServerPlansMu.RLock()
	plans = appendServerPlansByPlanCode(plans, seen, state.ServerPlans)
	state.ServerPlansMu.RUnlock()
	if state.ServerCache != nil {
		if cachedPlans, _ := state.ServerCache.Get(); len(cachedPlans) > 0 {
			plans = appendServerPlansByPlanCode(plans, seen, cachedPlans)
		}
	}
	if state.DB != nil {
		if storedPlans, err := state.DB.ListServers(); err == nil {
			plans = appendServerPlansByPlanCode(plans, seen, storedPlans)
		}
	}
	return plans
}

func matchServerPlansByModel(plans []types.ServerPlan, model string) []types.ServerPlan {
	want := normalizeServerModel(model)
	if want == "" {
		return nil
	}
	aliases := serverModelPlanAliases[want]
	result := make([]types.ServerPlan, 0)
	seen := make(map[string]struct{})
	for _, plan := range plans {
		key := strings.ToLower(strings.TrimSpace(plan.PlanCode))
		_, matched := aliases[key]
		for _, candidate := range serverModelCandidates(plan) {
			if normalizeServerModel(candidate) == want {
				matched = true
				break
			}
		}
		if !matched || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, plan)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].PlanCode) < strings.ToLower(result[j].PlanCode)
	})
	return result
}

func refreshServerPlansForModelQuery(state *app.State) {
	if state == nil || state.OVH == nil || state.Logger == nil || !state.HasAnyAccount() {
		return
	}
	serverModelRefreshMu.Lock()
	defer serverModelRefreshMu.Unlock()
	if !serverModelLastRefresh.IsZero() && time.Since(serverModelLastRefresh) < serverModelRefreshDelay {
		return
	}
	serverModelLastRefresh = time.Now()
	refreshed := catalog.LoadServerList(state)
	if len(refreshed) == 0 {
		return
	}
	state.ServerPlansMu.Lock()
	state.ServerPlans = refreshed
	state.ServerPlansMu.Unlock()
	if state.ServerCache != nil {
		state.ServerCache.Set(refreshed)
	}
	if state.DB != nil {
		if err := state.SaveServers(); err != nil {
			state.Logger.Warn("保存型号查询刷新后的服务器目录失败: "+err.Error(), "")
		}
	}
}

// findServerPlansByModel 按服务器列表中的型号精确匹配，忽略大小写和横线格式差异。
// 本地目录未命中时按需刷新一次；不用模糊包含匹配，避免把普通文本误当成型号查询。
func findServerPlansByModel(state *app.State, model string) []types.ServerPlan {
	if !looksLikeServerModelQuery(model) {
		return nil
	}
	plans := matchServerPlansByModel(serverPlanCatalogSnapshot(state), model)
	expected := len(serverModelPlanAliases[normalizeServerModel(model)])
	if len(plans) > 0 && (expected == 0 || len(plans) >= expected) {
		return plans
	}
	refreshServerPlansForModelQuery(state)
	return matchServerPlansByModel(serverPlanCatalogSnapshot(state), model)
}

func serverOptionGroup(option types.ServerOption) string {
	family := strings.ToLower(strings.TrimSpace(option.Family))
	value := strings.ToLower(option.Value)
	label := strings.ToLower(option.Label)
	switch {
	case strings.Contains(family, "system-storage"):
		return "storage"
	case strings.Contains(family, "memory") || strings.Contains(family, "ram") || strings.HasPrefix(value, "ram-"):
		return "memory"
	case strings.Contains(family, "storage") || strings.Contains(family, "disk") || strings.Contains(family, "drive") ||
		strings.Contains(value, "raid") || strings.Contains(value, "nvme") || strings.Contains(value, "ssd") || strings.Contains(value, "hdd"):
		return "storage"
	case strings.Contains(family, "bandwidth") || strings.Contains(family, "traffic") || strings.Contains(family, "network") ||
		strings.HasPrefix(value, "bandwidth-") || strings.HasPrefix(value, "traffic-"):
		return "bandwidth"
	case strings.Contains(family, "cpu") || strings.Contains(family, "processor") ||
		strings.Contains(label, "cpu") || strings.Contains(label, "processor") || strings.Contains(label, "intel") ||
		strings.Contains(label, "amd") || strings.Contains(label, "xeon") || strings.Contains(label, "epyc") || strings.Contains(label, "ryzen"):
		return "cpu"
	default:
		return ""
	}
}

func formatServerOption(option types.ServerOption, group string) string {
	value := strings.TrimSpace(option.Value)
	label := strings.TrimSpace(option.Label)
	if label == "" {
		label = value
	}
	switch group {
	case "memory":
		if match := serverMemoryOption.FindStringSubmatch(value); match != nil {
			memory := match[1] + " GB"
			if frequency := serverMemoryFrequency.FindStringSubmatch(value); len(frequency) > 1 {
				memory += " · " + strings.ToUpper(frequency[1])
			}
			return memory
		}
	case "storage":
		if formatted := catalog.FormatStorageDisplay(value); formatted != "" && formatted != "默认存储" {
			return formatted
		}
	case "bandwidth":
		lower := strings.ToLower(value)
		if strings.Contains(lower, "unlimited") {
			return "无限流量"
		}
		if match := serverTrafficBandwidth.FindStringSubmatch(value); match != nil {
			return fmt.Sprintf("%s Mbps · %s %s 流量", match[3], match[1], strings.ToUpper(match[2]))
		}
		if match := serverTrafficOption.FindStringSubmatch(value); match != nil {
			return fmt.Sprintf("%s %s 流量", match[1], strings.ToUpper(match[2]))
		}
		if match := serverBandwidthOption.FindStringSubmatch(value); match != nil {
			if speed, err := strconv.Atoi(match[1]); err == nil && speed >= 1000 {
				return strconv.FormatFloat(float64(speed)/1000, 'f', -1, 64) + " Gbps"
			}
			return match[1] + " Mbps"
		}
	}
	return label
}

func cleanServerSpec(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "N/A") {
		return "暂无数据"
	}
	return value
}

func serverOptionLines(plan types.ServerPlan, group, fallback string) []string {
	defaults := make(map[string]struct{}, len(plan.DefaultOptions))
	for _, option := range plan.DefaultOptions {
		defaults[strings.ToLower(strings.TrimSpace(option.Value))] = struct{}{}
	}
	type displayOption struct {
		Label     string
		IsDefault bool
		Order     int
	}
	options := make([]displayOption, 0)
	seen := make(map[string]int)
	for _, option := range plan.AvailableOptions {
		if serverOptionGroup(option) != group {
			continue
		}
		label := cleanServerSpec(formatServerOption(option, group))
		key := strings.ToLower(label)
		_, isDefault := defaults[strings.ToLower(strings.TrimSpace(option.Value))]
		isDefault = isDefault || option.IsDefault
		if index, exists := seen[key]; exists {
			options[index].IsDefault = options[index].IsDefault || isDefault
			continue
		}
		seen[key] = len(options)
		options = append(options, displayOption{Label: label, IsDefault: isDefault, Order: len(options)})
	}
	if len(options) == 0 {
		fallback = cleanServerSpec(fallback)
		if group == "memory" {
			for _, option := range plan.DefaultOptions {
				if serverOptionGroup(option) != "memory" {
					continue
				}
				if formatted := strings.TrimSpace(formatServerOption(option, group)); formatted != "" {
					fallback = formatted
					break
				}
			}
		}
		if group == "memory" || group == "storage" || group == "bandwidth" {
			return []string{"● " + fallback + "（默认）"}
		}
		return []string{fallback}
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].IsDefault != options[j].IsDefault {
			return options[i].IsDefault
		}
		return options[i].Order < options[j].Order
	})
	lines := make([]string, 0, len(options))
	for _, option := range options {
		if option.IsDefault {
			lines = append(lines, "● "+option.Label+"（默认）")
		} else {
			lines = append(lines, "  "+option.Label)
		}
	}
	return lines
}

var standardServerDatacenters = []string{
	"gra", "sbg", "rbx", "bhs", "mum", "waw", "fra", "lon", "hil", "vin", "sgp", "syd",
}

var standardServerDatacenterLocations = map[string]string{
	"gra": "法国 · 格拉夫尼茨",
	"sbg": "法国 · 斯特拉斯堡",
	"rbx": "法国 · 鲁贝",
	"bhs": "加拿大 · 博阿尔诺",
	"mum": "印度 · 孟买",
	"waw": "波兰 · 华沙",
	"fra": "德国 · 法兰克福",
	"lon": "英国 · 伦敦",
	"hil": "美国西部 · 俄勒冈",
	"vin": "美国东部 · 弗吉尼亚",
	"sgp": "新加坡 · 新加坡",
	"syd": "澳大利亚 · 悉尼",
}

func canonicalServerDatacenter(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "ynm" {
		return "mum"
	}
	for _, code := range standardServerDatacenters {
		if value == code || strings.HasPrefix(value, code+"-") {
			return code
		}
	}
	return value
}

func serverDatacenterLocations(plan types.ServerPlan) map[string]string {
	locations := make(map[string]string, len(standardServerDatacenterLocations))
	for code, location := range standardServerDatacenterLocations {
		locations[code] = location
	}
	for _, datacenter := range plan.Datacenters {
		code := canonicalServerDatacenter(datacenter.Datacenter)
		if code == "" {
			continue
		}
		region := strings.TrimSpace(datacenter.Region)
		name := strings.TrimSpace(datacenter.DCName)
		if region == "" || strings.EqualFold(region, "unknown") || region == "未知" {
			continue
		}
		if name == "" || strings.EqualFold(name, "unknown") || name == "未知" {
			continue
		}
		locations[code] = region + " · " + name
	}
	return locations
}

func serverDatacenterLines(plan types.ServerPlan) []string {
	available := make(map[string]bool)
	known := make(map[string]struct{})
	for _, datacenter := range plan.Datacenters {
		code := canonicalServerDatacenter(datacenter.Datacenter)
		if code != "" {
			known[code] = struct{}{}
		}
		if code != "" && catalog.AvailabilityExplicitlyAvailable(datacenter.Availability) {
			available[code] = true
		}
	}
	extra := make([]string, 0)
	for code := range known {
		found := false
		for _, standard := range standardServerDatacenters {
			if code == standard {
				found = true
				break
			}
		}
		if !found {
			extra = append(extra, code)
		}
	}
	sort.Strings(extra)
	codes := append(append([]string{}, standardServerDatacenters...), extra...)
	locations := serverDatacenterLocations(plan)
	lines := make([]string, 0, len(codes))
	for _, code := range codes {
		point := "🔴"
		if available[code] {
			point = "🟢"
		}
		line := point + " " + strings.ToUpper(code)
		if location := strings.TrimSpace(locations[code]); location != "" {
			line += "    " + location
		}
		lines = append(lines, line)
	}
	return lines
}

func serverDatacenterAvailabilitySummary(plan types.ServerPlan) string {
	available := make(map[string]bool)
	known := make(map[string]struct{})
	for _, datacenter := range plan.Datacenters {
		code := canonicalServerDatacenter(datacenter.Datacenter)
		if code == "" {
			continue
		}
		known[code] = struct{}{}
		if catalog.AvailabilityExplicitlyAvailable(datacenter.Availability) {
			available[code] = true
		}
	}
	availableCount := 0
	for code := range available {
		if _, ok := known[code]; ok {
			availableCount++
		}
	}
	total := len(standardServerDatacenters)
	for code := range known {
		found := false
		for _, standard := range standardServerDatacenters {
			if code == standard {
				found = true
				break
			}
		}
		if !found {
			total++
		}
	}
	return fmt.Sprintf("%d/%d 可用", availableCount, total)
}

func serverPriceSection(state *app.State, plan types.ServerPlan) serverPlanSection {
	section := serverPlanSection{Title: "💰 价格:"}
	if state == nil {
		section.Lines = []string{"月费: 暂不可用", "安装费: 无", "总价: 暂不可用"}
		return section
	}
	options := make([]string, 0, len(plan.DefaultOptions))
	for _, option := range plan.DefaultOptions {
		if value := strings.TrimSpace(option.Value); value != "" {
			options = append(options, value)
		}
	}
	// 型号查询与服务器列表使用同一份公开 catalog 价格。不要先创建购物车：
	// 购物车询价会受指定机房库存影响，导致明明列表有价格、消息卡片却显示不可用。
	display, err := price.GetCatalogDisplay(state, "", plan.PlanCode, options)
	if err != nil || !display.BreakdownKnown {
		section.Lines = []string{"月费: 暂不可用", "安装费: 无", "总价: 暂不可用"}
		return section
	}
	formatted := monitor.FormatDisplayPrice(display)
	if strings.TrimSpace(formatted) == "" {
		section.Lines = []string{"月费: 暂不可用", "安装费: 无", "总价: 暂不可用"}
		return section
	}
	section.Lines = strings.Split(formatted, "\n")
	return section
}

func serverPlanSections(state *app.State, plan types.ServerPlan) []serverPlanSection {
	return []serverPlanSection{
		{Title: "CPU", Lines: serverOptionLines(plan, "cpu", plan.CPU)},
		{Title: "内存 / 频率", Lines: serverOptionLines(plan, "memory", plan.Memory)},
		{Title: "存储 / 数据盘", Lines: serverOptionLines(plan, "storage", plan.Storage)},
		{Title: "带宽 / 网络", Lines: serverOptionLines(plan, "bandwidth", plan.Bandwidth)},
		serverPriceSection(state, plan),
		{Title: "数据中心 " + serverDatacenterAvailabilitySummary(plan), Lines: serverDatacenterLines(plan)},
	}
}

func serverSectionRuneCount(section serverPlanSection) int {
	count := utf8.RuneCountInString(section.Title) + 2
	for _, line := range section.Lines {
		count += utf8.RuneCountInString(line) + 1
	}
	return count
}

func splitServerLine(line string, maxRunes int) []string {
	runes := []rune(line)
	if maxRunes < 1 || len(runes) <= maxRunes {
		return []string{line}
	}
	parts := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		part := string(runes[start:end])
		if start > 0 {
			part = "  " + part
		}
		parts = append(parts, part)
	}
	return parts
}

func splitLargeServerSection(section serverPlanSection, maxRunes int) []serverPlanSection {
	// 单个异常长的 OVH 配置名也要主动换段，不能交给飞书静默截断。
	lineLimit := maxRunes - utf8.RuneCountInString(section.Title) - 16
	if lineLimit < 32 {
		lineLimit = 32
	}
	expandedLines := make([]string, 0, len(section.Lines))
	for _, line := range section.Lines {
		expandedLines = append(expandedLines, splitServerLine(line, lineLimit)...)
	}
	section.Lines = expandedLines
	if serverSectionRuneCount(section) <= maxRunes || len(section.Lines) <= 1 {
		return []serverPlanSection{section}
	}
	parts := make([]serverPlanSection, 0)
	current := serverPlanSection{Title: section.Title}
	for _, line := range section.Lines {
		candidate := current
		candidate.Lines = append(append([]string{}, current.Lines...), line)
		if len(current.Lines) > 0 && serverSectionRuneCount(candidate) > maxRunes {
			parts = append(parts, current)
			current = serverPlanSection{Title: section.Title + "（续）", Lines: []string{line}}
		} else {
			current = candidate
		}
	}
	if len(current.Lines) > 0 {
		parts = append(parts, current)
	}
	return parts
}

func paginateServerSections(sections []serverPlanSection, maxRunes int) [][]serverPlanSection {
	expanded := make([]serverPlanSection, 0, len(sections))
	for _, section := range sections {
		expanded = append(expanded, splitLargeServerSection(section, maxRunes)...)
	}
	pages := make([][]serverPlanSection, 0)
	current := make([]serverPlanSection, 0)
	currentRunes := 0
	for _, section := range expanded {
		sectionRunes := serverSectionRuneCount(section)
		if len(current) > 0 && currentRunes+sectionRunes > maxRunes {
			pages = append(pages, current)
			current = nil
			currentRunes = 0
		}
		current = append(current, section)
		currentRunes += sectionRunes
	}
	if len(current) > 0 {
		pages = append(pages, current)
	}
	return pages
}

func telegramServerPlanCard(model string, plan types.ServerPlan, sections []serverPlanSection, page, total int) string {
	title := strings.TrimSpace(model) + " · " + plan.PlanCode
	if total > 1 {
		title += fmt.Sprintf(" · %d/%d", page, total)
	}
	var builder strings.Builder
	builder.WriteString("╭─ " + title + "\n")
	for _, section := range sections {
		builder.WriteString("│\n│ " + section.Title + "\n")
		for _, line := range section.Lines {
			builder.WriteString("│ " + line + "\n")
		}
	}
	builder.WriteString("╰────────────────────────")
	return builder.String()
}

func sendTelegramServerPlanCards(state *app.State, chatID interface{}, replyToMessageID int64, model string, plans []types.ServerPlan) {
	first := true
	for _, plan := range plans {
		pages := paginateServerSections(serverPlanSections(state, plan), telegramPlanCardMaxRunes)
		for index, page := range pages {
			replyTo := int64(0)
			if first {
				replyTo = replyToMessageID
				first = false
			}
			telegram.SendReply(state, chatID, telegramServerPlanCard(model, plan, page, index+1, len(pages)), replyTo)
		}
	}
}

func feishuServerPlanCard(model string, plan types.ServerPlan, sections []serverPlanSection, page, total int) map[string]interface{} {
	title := strings.TrimSpace(model) + " · " + plan.PlanCode
	if total > 1 {
		title += fmt.Sprintf(" · %d/%d", page, total)
	}
	elements := make([]interface{}, 0, len(sections))
	for _, section := range sections {
		content := "**" + section.Title + "**\n" + strings.Join(section.Lines, "\n")
		elements = append(elements, map[string]interface{}{"tag": "markdown", "content": content})
	}
	return map[string]interface{}{
		"header": map[string]interface{}{
			"template": "blue",
			"title":    map[string]interface{}{"tag": "plain_text", "content": title},
		},
		"elements": elements,
	}
}

func sendFeishuServerPlanCards(state *app.State, openID, model string, plans []types.ServerPlan) error {
	for _, plan := range plans {
		pages := paginateServerSections(serverPlanSections(state, plan), feishuPlanCardMaxRunes)
		for index, page := range pages {
			card := feishuServerPlanCard(model, plan, page, index+1, len(pages))
			if err := monitor.FeishuSendCard(state, openID, card); err != nil {
				return err
			}
		}
	}
	return nil
}
