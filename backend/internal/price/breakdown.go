package price

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ovhsdk "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/numconv"
)

const publicCatalogTTL = 2 * time.Hour

// DisplayPrice 是通知需要的价格拆分。
// 月费、安装费都使用含税金额；总价优先使用购物车 summary 返回的实际含税总价。
type DisplayPrice struct {
	MonthlyWithTax float64
	InstallWithTax float64
	TotalWithTax   float64
	Currency       string
	TotalKnown     bool
	BreakdownKnown bool
}

type publicCatalog struct {
	Locale struct {
		CurrencyCode string `json:"currencyCode"`
	} `json:"locale"`
	Plans  []catalogPlan `json:"plans"`
	Addons []catalogPlan `json:"addons"`
}

type catalogPlan struct {
	PlanCode string           `json:"planCode"`
	Pricings []catalogPricing `json:"pricings"`
}

type catalogPricing struct {
	Interval     float64  `json:"interval"`
	IntervalUnit string   `json:"intervalUnit"`
	Price        float64  `json:"price"`
	Tax          float64  `json:"tax"`
	Mode         string   `json:"mode"`
	Capacities   []string `json:"capacities"`
}

// GetDisplay 询价后从公开 catalog 拆出月费和一次性安装费。
// catalog 读取优先复用 SQLite 缓存，避免每次都额外直连 OVH。
func GetDisplay(state *app.State, accountID, planCode, datacenter string, options []string) (DisplayPrice, error) {
	result := GetInternal(state, accountID, planCode, datacenter, options)
	return GetDisplayFromResult(state, accountID, planCode, options, result)
}

// GetCatalogDisplay 只读取公开 catalog，计算与服务器列表相同口径的月费和
// 安装费。它不创建购物车，因此可在实时询价失败时继续为通知提供目录价格。
func GetCatalogDisplay(state *app.State, accountID, planCode string, options []string) (DisplayPrice, error) {
	return getDisplayFromCatalog(state, accountID, planCode, options, DisplayPrice{Currency: "EUR"})
}

// GetDisplayFromResult 复用已经完成的购物车询价结果，再从公开 catalog
// 拆出月费和安装费。监控流程使用此函数，避免为了生成通知再次创建购物车。
func GetDisplayFromResult(state *app.State, accountID, planCode string, options []string, result Result) (DisplayPrice, error) {
	if !result.Success {
		return DisplayPrice{}, fmt.Errorf("价格查询失败: %s", result.Error)
	}

	display := displayPriceFromSummary(result.Price)
	return getDisplayFromCatalog(state, accountID, planCode, options, display)
}

func getDisplayFromCatalog(state *app.State, accountID, planCode string, options []string, display DisplayPrice) (DisplayPrice, error) {
	client, err := state.OVH.ClientFor(accountID)
	if err != nil {
		return display, fmt.Errorf("获取 OVH 客户端失败: %w", err)
	}

	subsidiary := "IE"
	if acc, ok := state.FindAccount(accountID); ok && acc.Zone != "" {
		subsidiary = acc.Zone
	}
	catalog, err := loadPublicCatalog(state, client, subsidiary)
	if err != nil {
		return display, err
	}

	monthly, install, ok := calculateFromCatalog(catalog, planCode, options)
	if !ok {
		return display, fmt.Errorf("catalog 中未找到 %s 的有效月费价格", planCode)
	}

	currency := strings.TrimSpace(catalog.Locale.CurrencyCode)
	if currency == "" {
		currency = display.Currency
	}
	if currency == "" {
		currency = "EUR"
	}
	display.MonthlyWithTax = monthly.price + monthly.tax
	display.InstallWithTax = install.price + install.tax
	display.Currency = currency
	display.BreakdownKnown = true
	if !display.TotalKnown {
		display.TotalWithTax = display.MonthlyWithTax + display.InstallWithTax
		display.TotalKnown = true
	}
	return display, nil
}

func displayPriceFromSummary(info *PriceInfo) DisplayPrice {
	display := DisplayPrice{Currency: "EUR"}
	if info == nil || info.Prices == nil {
		return display
	}
	if currency, ok := info.Prices["currencyCode"].(string); ok && strings.TrimSpace(currency) != "" {
		display.Currency = currency
	}
	if total, ok := numconv.ToFloat64(info.Prices["withTax"]); ok {
		display.TotalWithTax = total
		display.TotalKnown = true
	}
	return display
}

func loadPublicCatalog(state *app.State, client *ovhsdk.Client, subsidiary string) (*publicCatalog, error) {
	subsidiary = strings.ToUpper(strings.TrimSpace(subsidiary))
	if subsidiary == "" {
		subsidiary = "IE"
	}

	var staleRaw string
	if state.DB != nil {
		if raw, updatedAt, ok, err := state.DB.GetCatalog(subsidiary); err == nil && ok {
			staleRaw = raw
			if time.Since(time.UnixMilli(updatedAt)) < publicCatalogTTL {
				if catalog, parseErr := parsePublicCatalog([]byte(raw)); parseErr == nil {
					return catalog, nil
				}
			}
		}
	}

	var raw map[string]interface{}
	if err := client.Get("/order/catalog/public/eco?ovhSubsidiary="+subsidiary, &raw); err != nil {
		if staleRaw != "" {
			if catalog, parseErr := parsePublicCatalog([]byte(staleRaw)); parseErr == nil {
				return catalog, nil
			}
		}
		return nil, fmt.Errorf("获取公开 catalog 失败: %w", err)
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("编码公开 catalog 失败: %w", err)
	}
	if state.DB != nil {
		if err := state.DB.UpsertCatalog(subsidiary, string(body)); err != nil {
			if state.Logger != nil {
				state.Logger.Warn("写入价格 catalog 缓存失败: "+err.Error(), "price")
			}
		}
	}
	catalog, err := parsePublicCatalog(body)
	if err != nil {
		return nil, fmt.Errorf("解析公开 catalog 失败: %w", err)
	}
	return catalog, nil
}

func parsePublicCatalog(raw []byte) (*publicCatalog, error) {
	var catalog publicCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	return &catalog, nil
}

type catalogAmount struct {
	price float64
	tax   float64
}

func calculateFromCatalog(catalog *publicCatalog, planCode string, options []string) (catalogAmount, catalogAmount, bool) {
	if catalog == nil {
		return catalogAmount{}, catalogAmount{}, false
	}
	plans := make(map[string]catalogPlan, len(catalog.Plans))
	for _, plan := range catalog.Plans {
		plans[plan.PlanCode] = plan
	}
	addons := make(map[string]catalogPlan, len(catalog.Addons))
	for _, addon := range catalog.Addons {
		addons[addon.PlanCode] = addon
	}

	plan, ok := plans[planCode]
	if !ok {
		return catalogAmount{}, catalogAmount{}, false
	}
	monthly, monthlyOK := monthlyPricing(plan.Pricings)
	if !monthlyOK {
		return catalogAmount{}, catalogAmount{}, false
	}
	install := installationPricing(plan.Pricings)

	for _, optionCode := range options {
		optionCode = strings.TrimSpace(optionCode)
		if optionCode == "" {
			continue
		}
		addon, exists := addons[optionCode]
		if !exists {
			continue
		}
		if addonMonthly, ok := monthlyPricing(addon.Pricings); ok {
			monthly.price += addonMonthly.price
			monthly.tax += addonMonthly.tax
		}
		addonInstall := installationPricing(addon.Pricings)
		install.price += addonInstall.price
		install.tax += addonInstall.tax
	}
	return monthly, install, true
}

func monthlyPricing(pricings []catalogPricing) (catalogAmount, bool) {
	for _, pricing := range pricings {
		if hasCapacity(pricing.Capacities, "installation") {
			continue
		}
		if pricing.IntervalUnit == "month" && pricing.Interval == 1 && pricing.Mode == "default" {
			return catalogAmount{price: pricing.Price / 1e8, tax: pricing.Tax / 1e8}, true
		}
	}
	return catalogAmount{}, false
}

func installationPricing(pricings []catalogPricing) catalogAmount {
	for _, pricing := range pricings {
		if pricing.Mode == "default" && hasCapacity(pricing.Capacities, "installation") {
			return catalogAmount{price: pricing.Price / 1e8, tax: pricing.Tax / 1e8}
		}
	}
	return catalogAmount{}
}

func hasCapacity(capacities []string, wanted string) bool {
	for _, capacity := range capacities {
		if capacity == wanted {
			return true
		}
	}
	return false
}
