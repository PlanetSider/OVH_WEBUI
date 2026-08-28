package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/db"
)

const realtimeAvailabilityMaxBody = 32 << 20

var realtimeAvailabilityClient = &http.Client{Timeout: 30 * time.Second}

type availabilityComparisonCatalog struct {
	URL        string
	Subsidiary string
	Label      string
}

func realtimeAvailabilityURL(region string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", "eu":
		return "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities", true
	case "ca":
		return "https://ca.api.ovh.com/v1/dedicated/server/datacenter/availabilities", true
	default:
		return "", false
	}
}

func normalizeRealtimeRegion(region string) (string, string, bool) {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		region = "eu"
	}
	url, ok := realtimeAvailabilityURL(region)
	return region, url, ok
}

func comparisonCatalogForRegion(region string) (availabilityComparisonCatalog, bool) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "eu":
		return availabilityComparisonCatalog{
			URL:        "https://eu.api.ovh.com/v1/order/catalog/public/eco?ovhSubsidiary=IE",
			Subsidiary: "IE",
			Label:      "ovh-ie",
		}, true
	case "ca":
		return availabilityComparisonCatalog{
			URL:        "https://ca.api.ovh.com/v1/order/catalog/public/eco?ovhSubsidiary=CA",
			Subsidiary: "CA",
			Label:      "ovh-ca",
		}, true
	default:
		return availabilityComparisonCatalog{}, false
	}
}

// GetRealtimeAvailability GET /api/realtime-availability?region=eu|ca
// 页面只读取最近一次成功的后台快照，不会因为访问页面而直连 OVH。
func GetRealtimeAvailability(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		region, source, ok := normalizeRealtimeRegion(c.Query("region"))
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "region must be eu or ca"})
			return
		}

		var fetchedAt int64
		var data string
		err := state.DB.QueryRowx(`
			SELECT fetched_at, data
			FROM availability_snapshots
			WHERE region = ?
			ORDER BY fetched_at DESC
			LIMIT 1`, region).Scan(&fetchedAt, &data)
		if err == sql.ErrNoRows {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":  "该区域还没有成功的实时可用性快照",
				"region": region,
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取实时可用性快照失败: " + err.Error()})
			return
		}

		items := make([]map[string]interface{}, 0)
		if err := json.Unmarshal([]byte(data), &items); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "实时可用性快照格式无效"})
			return
		}
		fetchedAtText := time.UnixMilli(fetchedAt).UTC().Format(time.RFC3339)
		now := time.Now()
		nextRefresh := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, now.Location()).UTC().Format(time.RFC3339)
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"region":        region,
			"source":        source,
			"items":         items,
			"total":         len(items),
			"fetchedAt":     fetchedAtText,
			"fetchedAtUnix": fetchedAt,
			"lastRefreshAt": fetchedAtText,
			"nextRefreshAt": nextRefresh,
		})
	}
}

// GetPreaddedServers GET /api/preadded-servers?region=all|eu|ca&page=1&pageSize=20&search=
// 只读取后台已经按 planCode 聚合并保存的结果；不会访问 OVH，也不会返回全量 FQN 原始配置。
func GetPreaddedServers(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		region := strings.ToLower(strings.TrimSpace(c.DefaultQuery("region", "all")))
		if region != "all" && region != "eu" && region != "ca" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "region must be all, eu or ca"})
			return
		}
		page := positiveQueryInt(c.Query("page"), 1, 1, 1_000_000)
		pageSize := positiveQueryInt(c.Query("pageSize"), 20, 1, 100)
		search := strings.ToLower(strings.TrimSpace(c.Query("search")))

		// 区域单选时可以让 SQLite 先搜索过滤；“全部”必须先合并 EU/CA，
		// 再搜索合并后的字段，避免搜索词分别存在于两个区域时漏掉同一型号。
		dbSearch := search
		if region == "all" {
			dbSearch = ""
		}
		rows, err := state.DB.ListPreaddedServerResults(region, dbSearch)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取预增服务器失败: " + err.Error()})
			return
		}
		items := make([]db.PreaddedServerPageItem, 0, len(rows))
		for _, row := range rows {
			var item db.PreaddedServerPageItem
			if err := json.Unmarshal([]byte(row.Data), &item); err != nil {
				continue
			}
			if len(item.Regions) == 0 {
				item.Regions = []string{row.Region}
			}
			items = append(items, item)
		}
		items = mergePreaddedServerPageItems(items)
		if region == "all" && search != "" {
			filtered := make([]db.PreaddedServerPageItem, 0, len(items))
			for _, item := range items {
				raw, _ := json.Marshal(item)
				if strings.Contains(strings.ToLower(string(raw)), search) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		sort.Slice(items, func(i, j int) bool {
			return strings.ToLower(items[i].PlanCode) < strings.ToLower(items[j].PlanCode)
		})

		total := len(items)
		totalPages := 1
		if total > 0 {
			totalPages = (total + pageSize - 1) / pageSize
		}
		if page > totalPages {
			page = totalPages
		}
		start := (page - 1) * pageSize
		end := start + pageSize
		if end > total {
			end = total
		}
		pageItems := make([]db.PreaddedServerPageItem, 0)
		if start < total {
			pageItems = items[start:end]
		}

		comparisons, err := state.DB.ListPreaddedServerComparisons(region)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取预增服务器比对时间失败: " + err.Error()})
			return
		}
		comparisonTimes := gin.H{}
		var lastComparedAt int64
		for _, comparison := range comparisons {
			comparisonTimes[comparison.Region] = time.UnixMilli(comparison.ComparedAt).UTC().Format(time.RFC3339)
			if lastComparedAt == 0 || comparison.ComparedAt < lastComparedAt {
				// “全部”使用较早的成功时间，避免某一区域失败时误显为全部已经更新。
				lastComparedAt = comparison.ComparedAt
			}
		}
		lastComparedAtText := ""
		comparisonComplete := (region == "all" && len(comparisons) == 2) || (region != "all" && len(comparisons) == 1)
		if comparisonComplete && lastComparedAt > 0 {
			lastComparedAtText = time.UnixMilli(lastComparedAt).UTC().Format(time.RFC3339)
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"region":          region,
			"items":           pageItems,
			"total":           total,
			"page":            page,
			"pageSize":        pageSize,
			"totalPages":      totalPages,
			"lastComparedAt":  lastComparedAtText,
			"comparisonTimes": comparisonTimes,
		})
	}
}

func positiveQueryInt(raw string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < min {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

// RefreshRealtimeAvailabilityOnce 按区域获取实时可用性和服务器目录，成功后以同一批次
// 原子保存快照并计算预增服务器；任一上游失败时保留上一次成功结果。
func RefreshRealtimeAvailabilityOnce(state *app.State) {
	for _, region := range []string{"eu", "ca"} {
		if err := refreshRealtimeAvailabilityRegion(context.Background(), state, region); err != nil {
			state.Logger.Warn("实时可用性整点刷新失败 "+region+": "+err.Error(), "availability")
		}
	}
}

func refreshRealtimeAvailabilityRegion(ctx context.Context, state *app.State, region string) error {
	region, _, ok := normalizeRealtimeRegion(region)
	if !ok {
		return fmt.Errorf("unsupported region %s", region)
	}
	items, err := fetchRealtimeAvailabilityItems(ctx, region)
	if err != nil {
		return err
	}
	if items == nil {
		items = []map[string]interface{}{}
	}
	knownPlanCodes, comparisonRegion, err := loadComparisonPlanCodes(ctx, state, region)
	if err != nil {
		return fmt.Errorf("load %s comparison catalog: %w", region, err)
	}
	preadded := filterPreaddedServerItems(items, knownPlanCodes)
	aggregated := aggregatePreaddedServerItems(region, preadded)
	serverPlanCodes := sortedKnownPlanCodes(knownPlanCodes)
	batchAt := time.Now().UTC().Truncate(time.Second)
	if err := state.DB.SaveRealtimeAvailabilityBatch(region, batchAt, items, serverPlanCodes, aggregated); err != nil {
		return err
	}
	state.Logger.Info(fmt.Sprintf("实时可用性和服务器目录快照已保存 %s: %d 条，已按同一批次严格对比 %s 服务器列表，预增 %d 个型号", region, len(items), comparisonRegion, len(aggregated)), "availability")
	return nil
}

func fetchRealtimeAvailabilityItems(ctx context.Context, region string) ([]map[string]interface{}, error) {
	_, source, ok := normalizeRealtimeRegion(region)
	if !ok {
		return nil, fmt.Errorf("unsupported region %s", region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OVH-WebUI-Realtime-Availability")
	resp, err := realtimeAvailabilityClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("OVH availability API returned HTTP %d: %s", resp.StatusCode, detail)
	}
	var items []map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, realtimeAvailabilityMaxBody)).Decode(&items); err != nil {
		return nil, fmt.Errorf("invalid response from OVH availability API: %w", err)
	}
	return items, nil
}

// loadComparisonPlanCodes 只读取本批次在线获取的对应区域目录，不回退到历史目录缓存。
func loadComparisonPlanCodes(ctx context.Context, state *app.State, region string) (map[string]struct{}, string, error) {
	comparison, ok := comparisonCatalogForRegion(region)
	if !ok {
		return nil, "", fmt.Errorf("unsupported region %s", region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, comparison.URL, nil)
	if err != nil {
		return nil, comparison.Label, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OVH-WebUI-Preadded-Comparison")
	resp, err := realtimeAvailabilityClient.Do(req)
	if err != nil {
		return nil, comparison.Label, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return nil, comparison.Label, fmt.Errorf("OVH catalog API returned HTTP %d: %s", resp.StatusCode, detail)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, realtimeAvailabilityMaxBody))
	if err != nil {
		return nil, comparison.Label, fmt.Errorf("read %s catalog: %w", comparison.Label, err)
	}
	planCodes, err := parseComparisonPlanCodes(raw)
	if err != nil {
		return nil, comparison.Label, err
	}
	if saveErr := state.DB.UpsertCatalog(comparison.Subsidiary, string(raw)); saveErr != nil {
		state.Logger.Warn("保存 "+comparison.Label+" 对比目录失败: "+saveErr.Error(), "availability")
	}
	return planCodes, comparison.Label, nil
}

func parseComparisonPlanCodes(raw []byte) (map[string]struct{}, error) {
	var catalogData struct {
		Plans []struct {
			PlanCode string `json:"planCode"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(raw, &catalogData); err != nil {
		return nil, fmt.Errorf("invalid comparison catalog: %w", err)
	}
	known := make(map[string]struct{}, len(catalogData.Plans))
	for _, plan := range catalogData.Plans {
		planCode := strings.ToLower(strings.TrimSpace(plan.PlanCode))
		if planCode != "" {
			known[planCode] = struct{}{}
		}
	}
	if len(known) == 0 {
		return nil, fmt.Errorf("comparison catalog contains no plans")
	}
	return known, nil
}

func filterPreaddedServerItems(items []map[string]interface{}, known map[string]struct{}) []map[string]interface{} {
	if len(known) == 0 {
		return []map[string]interface{}{}
	}
	preadded := make([]map[string]interface{}, 0)
	for _, item := range items {
		planCode, _ := item["planCode"].(string)
		planCode = strings.TrimSpace(planCode)
		if planCode == "" {
			continue
		}
		if _, exists := known[strings.ToLower(planCode)]; exists {
			continue
		}
		preadded = append(preadded, item)
	}
	return preadded
}

func sortedKnownPlanCodes(known map[string]struct{}) []string {
	planCodes := make([]string, 0, len(known))
	for planCode := range known {
		planCodes = append(planCodes, planCode)
	}
	sort.Strings(planCodes)
	return planCodes
}

func aggregatePreaddedServerItems(region string, items []map[string]interface{}) []db.PreaddedServerPageItem {
	type groupState struct {
		item           db.PreaddedServerPageItem
		memories       map[string]struct{}
		storages       map[string]struct{}
		systemStorages map[string]struct{}
		datacenters    map[string]*db.PreaddedServerDatacenter
		seenVariants   map[string]struct{}
	}
	groups := make(map[string]*groupState)
	for _, rawItem := range items {
		planCode, _ := rawItem["planCode"].(string)
		planCode = strings.TrimSpace(planCode)
		key := strings.ToLower(planCode)
		if key == "" {
			continue
		}
		group := groups[key]
		if group == nil {
			server, _ := rawItem["server"].(string)
			group = &groupState{
				item: db.PreaddedServerPageItem{
					PlanCode: planCode,
					Server:   server,
					Regions:  []string{region},
				},
				memories:       make(map[string]struct{}),
				storages:       make(map[string]struct{}),
				systemStorages: make(map[string]struct{}),
				datacenters:    make(map[string]*db.PreaddedServerDatacenter),
				seenVariants:   make(map[string]struct{}),
			}
			groups[key] = group
		} else if group.item.Server == "" {
			server, _ := rawItem["server"].(string)
			group.item.Server = server
		}
		fqn, _ := rawItem["fqn"].(string)
		memory, _ := rawItem["memory"].(string)
		storage, _ := rawItem["storage"].(string)
		systemStorage, _ := rawItem["systemStorage"].(string)
		variantKey := strings.ToLower(strings.TrimSpace(fqn))
		if variantKey == "" {
			variantKey = strings.ToLower(strings.Join([]string{planCode, memory, storage, systemStorage}, "|"))
		}
		if _, exists := group.seenVariants[variantKey]; exists {
			continue
		}
		group.seenVariants[variantKey] = struct{}{}
		group.item.VariantCount++
		addTrimmedValue(group.memories, memory)
		addTrimmedValue(group.storages, storage)
		addTrimmedValue(group.systemStorages, systemStorage)

		seenDatacenters := make(map[string]struct{})
		datacenters, _ := rawItem["datacenters"].([]interface{})
		for _, rawDatacenter := range datacenters {
			datacenter, ok := rawDatacenter.(map[string]interface{})
			if !ok {
				continue
			}
			code, _ := datacenter["datacenter"].(string)
			code = strings.ToLower(strings.TrimSpace(code))
			if code == "" {
				continue
			}
			if _, exists := seenDatacenters[code]; exists {
				continue
			}
			seenDatacenters[code] = struct{}{}
			availability, _ := datacenter["availability"].(string)
			dc := group.datacenters[code]
			if dc == nil {
				dc = &db.PreaddedServerDatacenter{Datacenter: code, Availability: availability}
				group.datacenters[code] = dc
			}
			dc.ReportedVariants++
			if preaddedAvailabilityIsAvailable(availability) {
				dc.AvailableVariants++
			}
			dc.Availability = mergePreaddedAvailabilityStatus(dc.Availability, availability)
		}
	}

	results := make([]db.PreaddedServerPageItem, 0, len(groups))
	for _, group := range groups {
		group.item.Memories = sortedMapKeys(group.memories)
		group.item.Storages = sortedMapKeys(group.storages)
		group.item.SystemStorages = sortedMapKeys(group.systemStorages)
		group.item.Datacenters = make([]db.PreaddedServerDatacenter, 0, len(group.datacenters))
		for _, datacenter := range group.datacenters {
			group.item.Datacenters = append(group.item.Datacenters, *datacenter)
		}
		sort.Slice(group.item.Datacenters, func(i, j int) bool {
			return group.item.Datacenters[i].Datacenter < group.item.Datacenters[j].Datacenter
		})
		results = append(results, group.item)
	}
	sort.Slice(results, func(i, j int) bool {
		return strings.ToLower(results[i].PlanCode) < strings.ToLower(results[j].PlanCode)
	})
	return results
}

func mergePreaddedServerPageItems(items []db.PreaddedServerPageItem) []db.PreaddedServerPageItem {
	groups := make(map[string]*db.PreaddedServerPageItem)
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.PlanCode))
		if key == "" {
			continue
		}
		existing := groups[key]
		if existing == nil {
			copyItem := item
			copyItem.Regions = append([]string{}, item.Regions...)
			copyItem.Memories = append([]string{}, item.Memories...)
			copyItem.Storages = append([]string{}, item.Storages...)
			copyItem.SystemStorages = append([]string{}, item.SystemStorages...)
			copyItem.Datacenters = append([]db.PreaddedServerDatacenter{}, item.Datacenters...)
			groups[key] = &copyItem
			continue
		}
		if existing.Server == "" {
			existing.Server = item.Server
		}
		existing.VariantCount += item.VariantCount
		existing.Regions = mergeUniqueStrings(existing.Regions, item.Regions)
		existing.Memories = mergeUniqueStrings(existing.Memories, item.Memories)
		existing.Storages = mergeUniqueStrings(existing.Storages, item.Storages)
		existing.SystemStorages = mergeUniqueStrings(existing.SystemStorages, item.SystemStorages)
		dcIndex := make(map[string]int, len(existing.Datacenters))
		for index, datacenter := range existing.Datacenters {
			dcIndex[strings.ToLower(datacenter.Datacenter)] = index
		}
		for _, datacenter := range item.Datacenters {
			index, exists := dcIndex[strings.ToLower(datacenter.Datacenter)]
			if !exists {
				existing.Datacenters = append(existing.Datacenters, datacenter)
				dcIndex[strings.ToLower(datacenter.Datacenter)] = len(existing.Datacenters) - 1
				continue
			}
			target := &existing.Datacenters[index]
			target.AvailableVariants += datacenter.AvailableVariants
			target.ReportedVariants += datacenter.ReportedVariants
			target.Availability = mergePreaddedAvailabilityStatus(target.Availability, datacenter.Availability)
		}
		sort.Slice(existing.Datacenters, func(i, j int) bool {
			return existing.Datacenters[i].Datacenter < existing.Datacenters[j].Datacenter
		})
	}
	results := make([]db.PreaddedServerPageItem, 0, len(groups))
	for _, item := range groups {
		results = append(results, *item)
	}
	return results
}

func preaddedAvailabilityIsAvailable(status string) bool {
	return catalog.AvailabilityExplicitlyAvailable(status)
}

func mergePreaddedAvailabilityStatus(current, candidate string) string {
	if preaddedAvailabilityIsAvailable(current) {
		return current
	}
	if preaddedAvailabilityIsAvailable(candidate) {
		return candidate
	}
	if current == "unavailable" || candidate == "unavailable" {
		return "unavailable"
	}
	if current != "" {
		return current
	}
	if candidate != "" {
		return candidate
	}
	return "unknown"
}

func addTrimmedValue(values map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		values[value] = struct{}{}
	}
}

func sortedMapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func mergeUniqueStrings(left, right []string) []string {
	values := make(map[string]struct{}, len(left)+len(right))
	for _, value := range append(append([]string{}, left...), right...) {
		addTrimmedValue(values, value)
	}
	return sortedMapKeys(values)
}

// migrateLegacyPreaddedResults 把升级前按 FQN 保存的结果纯本地聚合到新表。
// 只在新表没有对应区域记录时运行，不访问 OVH，也不改变旧比对时间。
func migrateLegacyPreaddedResults(state *app.State) error {
	for _, region := range []string{"eu", "ca"} {
		exists, err := state.DB.HasPreaddedServerComparison(region)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		rows, err := state.DB.ListPreaddedServers(region)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			// 旧表无法证明“实时可用性”和“服务器目录”属于同一批次，
			// 不再使用历史目录缓存补建结果，等待新的整点批次重新比对。
			continue
		}
		items := make([]map[string]interface{}, 0, len(rows))
		var comparedAt int64
		for _, row := range rows {
			item := map[string]interface{}{}
			if err := json.Unmarshal([]byte(row.Data), &item); err != nil {
				continue
			}
			item["planCode"] = row.PlanCode
			item["fqn"] = row.FQN
			items = append(items, item)
			if row.DetectedAt > comparedAt {
				comparedAt = row.DetectedAt
			}
		}
		if comparedAt == 0 {
			continue
		}
		aggregated := aggregatePreaddedServerItems(region, items)
		if err := state.DB.ReplacePreaddedServerResults(region, aggregated, time.UnixMilli(comparedAt)); err != nil {
			return err
		}
		state.Logger.Info(fmt.Sprintf("已迁移 %s 预增服务器聚合结果: %d 个型号", region, len(aggregated)), "availability")
	}
	return nil
}

// StartRealtimeAvailabilityRefresh 启动后台整点刷新。首次启动没有快照时立即补采，
// 后续每次都等待本机时区下一个整点，避免页面访问触发上游请求。
func StartRealtimeAvailabilityRefresh(state *app.State) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := ensureRealtimeAvailabilitySnapshots(state); err != nil {
			state.Logger.Warn("实时可用性启动补采失败: "+err.Error(), "availability")
		}
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, now.Location())
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
				RefreshRealtimeAvailabilityOnce(state)
			}
		}
	}()
	return cancel
}

func ensureRealtimeAvailabilitySnapshots(state *app.State) error {
	needsRefresh := false
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	if err := migrateLegacyPreaddedResults(state); err != nil {
		return err
	}
	if _, err := state.DB.Exec(`DELETE FROM availability_snapshots WHERE fetched_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err := state.DB.Exec(`DELETE FROM server_plan_snapshots WHERE fetched_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err := state.DB.Exec(`DELETE FROM preadded_servers WHERE detected_at < ?`, cutoff); err != nil {
		return err
	}
	for _, region := range []string{"eu", "ca"} {
		availabilitySnapshot, availabilityOK, err := state.DB.LatestAvailabilitySnapshot(region)
		if err != nil {
			return err
		}
		serverPlanSnapshot, serverPlanOK, err := state.DB.LatestServerPlanSnapshot(region)
		if err != nil {
			return err
		}
		if !availabilityOK || !serverPlanOK ||
			availabilitySnapshot.FetchedAt < cutoff || serverPlanSnapshot.FetchedAt < cutoff ||
			availabilitySnapshot.FetchedAt != serverPlanSnapshot.FetchedAt {
			needsRefresh = true
		}
	}
	if needsRefresh {
		RefreshRealtimeAvailabilityOnce(state)
	}
	return nil
}
