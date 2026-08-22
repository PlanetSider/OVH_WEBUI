package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
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

// GetPreaddedServers GET /api/preadded-servers?region=all|eu|ca
func GetPreaddedServers(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		region := strings.ToLower(strings.TrimSpace(c.DefaultQuery("region", "all")))
		if region != "all" && region != "eu" && region != "ca" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "region must be all, eu or ca"})
			return
		}
		rows, err := state.DB.ListPreaddedServers(region)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取预增服务器失败: " + err.Error()})
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, row := range rows {
			item := map[string]interface{}{}
			if err := json.Unmarshal([]byte(row.Data), &item); err != nil {
				continue
			}
			item["region"] = row.Region
			item["fqn"] = row.FQN
			item["planCode"] = row.PlanCode
			item["detectedAt"] = time.UnixMilli(row.DetectedAt).UTC().Format(time.RFC3339)
			if comparison, ok := comparisonCatalogForRegion(row.Region); ok {
				item["comparisonRegion"] = comparison.Label
			}
			items = append(items, item)
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"region": region, "items": items, "total": len(items)})
	}
}

// RefreshRealtimeAvailabilityOnce 拉取并保存 EU/CA 两个区域，保存完成后立即计算预增服务器。
func RefreshRealtimeAvailabilityOnce(state *app.State) {
	for _, region := range []string{"eu", "ca"} {
		if err := refreshRealtimeAvailabilityRegion(context.Background(), state, region); err != nil {
			state.Logger.Warn("实时可用性整点刷新失败 "+region+": "+err.Error(), "availability")
		}
	}
}

func refreshRealtimeAvailabilityRegion(ctx context.Context, state *app.State, region string) error {
	region, source, ok := normalizeRealtimeRegion(region)
	if !ok {
		return fmt.Errorf("unsupported region %s", region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OVH-WebUI-Realtime-Availability")
	resp, err := realtimeAvailabilityClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("OVH availability API returned HTTP %d: %s", resp.StatusCode, detail)
	}
	var items []map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, realtimeAvailabilityMaxBody)).Decode(&items); err != nil {
		return fmt.Errorf("invalid response from OVH availability API: %w", err)
	}
	if items == nil {
		items = []map[string]interface{}{}
	}
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	if err := state.DB.SaveAvailabilitySnapshot(region, fetchedAt, items); err != nil {
		return err
	}
	knownPlanCodes, comparisonRegion, err := loadComparisonPlanCodes(ctx, state, region)
	if err != nil {
		return fmt.Errorf("load %s comparison catalog: %w", region, err)
	}
	if err := updatePreaddedServers(state, region, items, knownPlanCodes, fetchedAt); err != nil {
		return err
	}
	state.Logger.Info(fmt.Sprintf("实时可用性已保存 %s: %d 条，已严格对比 %s 服务器列表", region, len(items), comparisonRegion), "availability")
	return nil
}

func loadComparisonPlanCodes(ctx context.Context, state *app.State, region string) (map[string]struct{}, string, error) {
	comparison, ok := comparisonCatalogForRegion(region)
	if !ok {
		return nil, "", fmt.Errorf("unsupported region %s", region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, comparison.URL, nil)
	if err == nil {
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "OVH-WebUI-Preadded-Comparison")
		if resp, requestErr := realtimeAvailabilityClient.Do(req); requestErr == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				raw, readErr := io.ReadAll(io.LimitReader(resp.Body, realtimeAvailabilityMaxBody))
				if readErr == nil {
					if planCodes, parseErr := parseComparisonPlanCodes(raw); parseErr == nil {
						if saveErr := state.DB.UpsertCatalog(comparison.Subsidiary, string(raw)); saveErr != nil {
							state.Logger.Warn("保存 "+comparison.Label+" 对比目录失败: "+saveErr.Error(), "availability")
						}
						return planCodes, comparison.Label, nil
					}
				}
			}
		}
	}

	// 对比目录请求失败时只允许回退到同一 subsidiary 的历史缓存，绝不跨区域取目录。
	raw, _, cached, cacheErr := state.DB.GetCatalog(comparison.Subsidiary)
	if cacheErr != nil {
		return nil, comparison.Label, cacheErr
	}
	if !cached {
		return nil, comparison.Label, fmt.Errorf("%s catalog unavailable and no same-region cache exists", comparison.Label)
	}
	planCodes, parseErr := parseComparisonPlanCodes([]byte(raw))
	if parseErr != nil {
		return nil, comparison.Label, parseErr
	}
	state.Logger.Warn("使用缓存的 "+comparison.Label+" 目录完成预增服务器比对", "availability")
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

func updatePreaddedServers(state *app.State, region string, items []map[string]interface{}, known map[string]struct{}, detectedAt time.Time) error {
	if len(known) == 0 {
		return fmt.Errorf("comparison catalog for %s is empty", region)
	}
	preadded := make([]map[string]interface{}, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		planCode, _ := item["planCode"].(string)
		planCode = strings.TrimSpace(planCode)
		if planCode == "" {
			continue
		}
		if _, exists := known[strings.ToLower(planCode)]; exists {
			continue
		}
		fqn, _ := item["fqn"].(string)
		key := fqn
		if key == "" {
			key = planCode
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		preadded = append(preadded, item)
	}
	return state.DB.ReplacePreaddedServers(region, preadded, detectedAt)
}

// RebuildPreaddedServersFromSnapshots 使用各区域固定对应的服务器目录重新比对最近快照。
// EU 只允许使用 ovh-ie，CA 只允许使用 ovh-ca，不依赖默认账户的服务器列表。
func RebuildPreaddedServersFromSnapshots(state *app.State) {
	for _, region := range []string{"eu", "ca"} {
		snapshot, ok, err := state.DB.LatestAvailabilitySnapshot(region)
		if err != nil || !ok {
			continue
		}
		items := make([]map[string]interface{}, 0)
		if err := json.Unmarshal([]byte(snapshot.Data), &items); err != nil {
			continue
		}
		knownPlanCodes, _, err := loadComparisonPlanCodes(context.Background(), state, region)
		if err != nil {
			state.Logger.Warn("读取区域服务器列表失败 "+region+": "+err.Error(), "availability")
			continue
		}
		if err := updatePreaddedServers(state, region, items, knownPlanCodes, time.UnixMilli(snapshot.FetchedAt)); err != nil {
			state.Logger.Warn("重建预增服务器失败 "+region+": "+err.Error(), "availability")
		}
	}
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
	if _, err := state.DB.Exec(`DELETE FROM availability_snapshots WHERE fetched_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err := state.DB.Exec(`DELETE FROM preadded_servers WHERE detected_at < ?`, cutoff); err != nil {
		return err
	}
	for _, region := range []string{"eu", "ca"} {
		var fetchedAt int64
		err := state.DB.Get(&fetchedAt, `SELECT COALESCE(MAX(fetched_at), 0) FROM availability_snapshots WHERE region = ?`, region)
		if err != nil {
			return err
		}
		if fetchedAt == 0 || fetchedAt < cutoff {
			needsRefresh = true
		}
	}
	if needsRefresh {
		RefreshRealtimeAvailabilityOnce(state)
		return nil
	}
	RebuildPreaddedServersFromSnapshots(state)
	return nil
}
