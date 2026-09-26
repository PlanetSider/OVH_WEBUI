package vps

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/ovh"
)

// Model 是公开 VPS 目录中当前可下单的型号。
type Model struct {
	PlanCode    string   `json:"planCode"`
	Name        string   `json:"name"`
	Generation  string   `json:"generation"`
	Price       string   `json:"price,omitempty"`
	Location    string   `json:"location,omitempty"`
	Datacenters []string `json:"datacenters,omitempty"`
	OSChoices   []string `json:"osChoices,omitempty"`
}

type modelsCacheEntry struct {
	models    []Model
	fetchedAt time.Time
	err       error
}

const (
	modelsCacheTTL     = 2 * time.Hour
	modelsCacheFailTTL = 2 * time.Minute
)

var (
	modelsMu    sync.Mutex
	modelsCache = map[string]modelsCacheEntry{}
)

// NormalizeSubsidiary 统一目录查询的子公司格式。
func NormalizeSubsidiary(sub string) string { return strings.ToUpper(strings.TrimSpace(sub)) }

// DefaultSubsidiary 没有显式子公司时跟随下单账户所在站点。
func DefaultSubsidiary(state *app.State, accountID string) string {
	if state != nil {
		if acc, ok := state.FindAccount(accountID); ok {
			if zone := NormalizeSubsidiary(acc.Zone); ovh.KnownSubsidiary(zone) {
				return zone
			}
			return ovh.DefaultSubsidiaryForEndpoint(acc.Endpoint)
		}
	}
	return ovh.DefaultSubsidiaryForEndpoint("")
}

// Models 查询某子公司当前仍在售的 VPS 型号。结果按子公司缓存。
func Models(state *app.State, subsidiary string) ([]Model, error) {
	sub := NormalizeSubsidiary(subsidiary)
	if sub == "" {
		return nil, fmt.Errorf("缺少 ovhSubsidiary: VPS 目录必须按子公司查询")
	}
	if !ovh.KnownSubsidiary(sub) {
		return nil, fmt.Errorf("未知的 OVH 子公司 %q", sub)
	}

	modelsMu.Lock()
	if cached, ok := modelsCache[sub]; ok {
		fresh := cached.err == nil && time.Since(cached.fetchedAt) < modelsCacheTTL
		failFresh := cached.err != nil && time.Since(cached.fetchedAt) < modelsCacheFailTTL
		if fresh || failFresh {
			modelsMu.Unlock()
			return append([]Model(nil), cached.models...), cached.err
		}
	}
	modelsMu.Unlock()

	models, err := fetchModels(state, sub)
	modelsMu.Lock()
	if err != nil {
		if cached, ok := modelsCache[sub]; ok && cached.err == nil && len(cached.models) > 0 {
			modelsCache[sub] = modelsCacheEntry{models: cached.models, fetchedAt: time.Now()}
			stale := append([]Model(nil), cached.models...)
			modelsMu.Unlock()
			return stale, nil
		}
	}
	modelsCache[sub] = modelsCacheEntry{models: append([]Model(nil), models...), fetchedAt: time.Now(), err: err}
	modelsMu.Unlock()
	return models, err
}

func vpsCatalogURL(subsidiary string) string {
	query := url.Values{}
	query.Set("ovhSubsidiary", subsidiary)
	return ovh.CatalogBaseURLForSubsidiary(subsidiary) + "/v1/order/catalog/public/vps?" + query.Encode()
}

type catalogPlan struct {
	PlanCode    string `json:"planCode"`
	InvoiceName string `json:"invoiceName"`
	Blobs       struct {
		Tags       []string `json:"tags"`
		Commercial struct {
			Line string `json:"line"`
		} `json:"commercial"`
	} `json:"blobs"`
	Pricings []struct {
		Description    string `json:"description"`
		FormattedPrice string `json:"formattedPrice"`
		IntervalUnit   string `json:"intervalUnit"`
	} `json:"pricings"`
	Configurations []struct {
		Name   string   `json:"name"`
		Values []string `json:"values"`
	} `json:"configurations"`
}

const maxVPSCatalogResponseBytes = 16 << 20

func fetchModels(state *app.State, subsidiary string) ([]Model, error) {
	if state == nil || state.OVH == nil {
		return nil, fmt.Errorf("VPS 目录缺少应用状态")
	}
	client, err := state.OVH.SharedHTTPClient(60 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("VPS 目录公共代理不可用: %w", err)
	}
	req, err := http.NewRequest(http.MethodGet, vpsCatalogURL(subsidiary), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取 %s 的 VPS 目录失败: %w", subsidiary, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("拉取 %s 的 VPS 目录失败(%s 站点): HTTP %d", subsidiary, ovh.SubsidiaryRegion(subsidiary), resp.StatusCode)
	}
	var document struct {
		Plans []catalogPlan `json:"plans"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxVPSCatalogResponseBytes)).Decode(&document); err != nil {
		return nil, fmt.Errorf("解析 %s 的 VPS 目录失败: %w", subsidiary, err)
	}

	models := make([]Model, 0, len(document.Plans))
	for _, plan := range document.Plans {
		if !hasTag(plan.Blobs.Tags, "order-funnel:show") {
			continue
		}
		models = append(models, Model{
			PlanCode:    plan.PlanCode,
			Name:        strings.TrimSpace(plan.InvoiceName),
			Generation:  plan.Blobs.Commercial.Line,
			Price:       monthlyPrice(plan),
			Location:    locationOf(plan.PlanCode),
			Datacenters: configValues(plan, "vps_datacenter"),
			OSChoices:   configValues(plan, "vps_os"),
		})
	}
	sortModels(models)
	return models, nil
}

func configValues(plan catalogPlan, name string) []string {
	for _, config := range plan.Configurations {
		if config.Name == name {
			return append([]string(nil), config.Values...)
		}
	}
	return nil
}

func hasTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}

func monthlyPrice(plan catalogPlan) string {
	for _, pricing := range plan.Pricings {
		if pricing.IntervalUnit == "month" && pricing.Description == "Monthly fees" {
			return pricing.FormattedPrice
		}
	}
	return ""
}

func locationOf(planCode string) string {
	switch {
	case strings.HasSuffix(planCode, "-eu"):
		return "欧洲机房"
	case strings.HasSuffix(planCode, "-ca"):
		return "加拿大机房"
	default:
		return ""
	}
}

func sortModels(models []Model) {
	sort.SliceStable(models, func(i, j int) bool {
		gi, gj := generationNumber(models[i].Generation), generationNumber(models[j].Generation)
		if gi != gj {
			return gi > gj
		}
		ni, nj := modelNumber(models[i].PlanCode), modelNumber(models[j].PlanCode)
		if ni != nj {
			return ni < nj
		}
		return models[i].PlanCode < models[j].PlanCode
	})
}

func generationNumber(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func modelNumber(planCode string) int {
	index := strings.Index(planCode, "model")
	if index < 0 {
		return 1 << 30
	}
	rest := planCode[index+len("model"):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 1 << 30
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

func IsOrderable(state *app.State, subsidiary, planCode string) (bool, error) {
	models, err := Models(state, subsidiary)
	if err != nil {
		return false, err
	}
	for _, model := range models {
		if strings.EqualFold(model.PlanCode, strings.TrimSpace(planCode)) {
			return true, nil
		}
	}
	return false, nil
}
