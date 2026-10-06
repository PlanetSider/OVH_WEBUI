package monitor

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/exchange"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/price"
)

// resolvePriceAccount 自动下单订阅必须用指定账户做库存与购物车询价，
// 否则可能以默认账户的区域/价格判断后让另一个账户下单。普通订阅仍用默认账户。
func (m *Monitor) resolvePriceAccount(sub *Subscription) string {
	if sub != nil && sub.AutoOrder && sub.AutoOrderAccountID != "" {
		if _, ok := m.state.FindAccount(sub.AutoOrderAccountID); ok {
			return sub.AutoOrderAccountID
		}
	}
	acc, ok := m.state.FindAccount("")
	if ok {
		return acc.ID
	}
	return ""
}

func optionsFromConfig(configInfo map[string]interface{}) []string {
	options := []string{}
	if configInfo == nil {
		return options
	}
	if opts, ok := configInfo["options"].([]string); ok {
		return append(options, opts...)
	}
	if optsRaw, ok := configInfo["options"].([]interface{}); ok {
		for _, o := range optsRaw {
			if s, ok := o.(string); ok {
				options = append(options, s)
			}
		}
	}
	return options
}

type priceCacheKey struct {
	accountID  string
	planCode   string
	datacenter string
	optionsKey string
}

type cachedPrice struct {
	display   price.DisplayPrice
	createdAt time.Time
}

func newPriceCacheKey(accountID, planCode, datacenter string, options []string) priceCacheKey {
	normalized := make([]string, 0, len(options))
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option != "" {
			normalized = append(normalized, option)
		}
	}
	sort.Strings(normalized)
	return priceCacheKey{
		accountID:  strings.TrimSpace(accountID),
		planCode:   strings.TrimSpace(planCode),
		datacenter: strings.TrimSpace(datacenter),
		optionsKey: strings.Join(normalized, "\x00"),
	}
}

func (m *Monitor) priceCacheLookup(accountID, planCode, datacenter string, options []string) (price.DisplayPrice, bool) {
	if m == nil {
		return price.DisplayPrice{}, false
	}
	key := newPriceCacheKey(accountID, planCode, datacenter, options)
	now := time.Now()
	m.cacheLock.Lock()
	entry, ok := m.priceCache[key]
	ttl := m.priceCacheTTL
	if ttl <= 0 {
		ttl = PriceQuoteCacheTTL
	}
	if ok && entry != nil && now.Sub(entry.createdAt) < ttl {
		display := entry.display
		m.cacheLock.Unlock()
		return display, true
	}
	if ok {
		delete(m.priceCache, key)
	}
	m.cacheLock.Unlock()
	return price.DisplayPrice{}, false
}

func (m *Monitor) priceCacheStore(accountID, planCode, datacenter string, options []string, display price.DisplayPrice) {
	if m == nil {
		return
	}
	key := newPriceCacheKey(accountID, planCode, datacenter, options)
	m.cacheLock.Lock()
	if m.priceCache == nil {
		m.priceCache = map[priceCacheKey]*cachedPrice{}
	}
	m.priceCache[key] = &cachedPrice{display: display, createdAt: time.Now()}
	m.cacheLock.Unlock()
}

// invalidatePriceCache 删除 API 明确报告无货的单个报价键。
// 失败、空结果或未知状态不触碰缓存，避免临时接口问题把已确认报价删掉。
func (m *Monitor) invalidatePriceCache(accountID, planCode, datacenter string, options []string) {
	if m == nil {
		return
	}
	key := newPriceCacheKey(accountID, planCode, datacenter, options)
	m.cacheLock.Lock()
	delete(m.priceCache, key)
	m.cacheLock.Unlock()
}

// retainPriceCachesForAvailability 仅在库存接口成功返回完整快照后调用。
// 当前有货键继续保留；同账户、同型号下不在快照中的旧配置/机房报价全部删除。
// 调用方在错误或空结果时不得调用它，以免临时接口故障清空有效缓存。
func (m *Monitor) retainPriceCachesForAvailability(accountID, planCode string, available map[priceCacheKey]struct{}) {
	if m == nil {
		return
	}
	accountID = strings.TrimSpace(accountID)
	planCode = strings.TrimSpace(planCode)
	m.cacheLock.Lock()
	for key := range m.priceCache {
		if key.accountID == accountID && key.planCode == planCode {
			if _, keep := available[key]; !keep {
				delete(m.priceCache, key)
			}
		}
	}
	m.cacheLock.Unlock()
}

// verifyPriceAvailable 完成一次购物车价格校验，并返回可直接用于通知的价格文案。
// 返回值依次为：价格文案、价格校验是否通过、失败原因。
func (m *Monitor) verifyPriceAvailable(ctx context.Context, accountID, planCode, datacenter string, configInfo map[string]interface{}) (string, bool, string) {
	if m == nil || m.state == nil {
		return "", false, "价格校验服务不可用"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	options := optionsFromConfig(configInfo)
	if err := ctx.Err(); err != nil {
		return "", false, "价格校验已取消"
	}
	if display, ok := m.priceCacheLookup(accountID, planCode, datacenter, options); ok {
		priceText := ""
		if display.TotalKnown || display.BreakdownKnown {
			priceText = m.formatNotificationPrice(display)
		}
		return priceText, true, ""
	}
	result := price.GetInternalWithContext(ctx, m.state, accountID, planCode, datacenter, options)
	if err := ctx.Err(); err != nil {
		return "", false, "价格校验已取消"
	}
	if !result.Success {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "未知错误"
		}
		if m.state.Logger != nil {
			m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		}
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, errMsg
	}
	if result.Price == nil {
		if m.state.Logger != nil {
			m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - price字段缺失", planCode, datacenter), "monitor")
		}
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, "price字段缺失"
	}
	withTax := result.Price.Prices["withTax"]
	if withTax == nil {
		errMsg := "withTax无效(<nil>)"
		if m.state.Logger != nil {
			m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		}
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, errMsg
	}
	withTaxValue, withTaxOK := numconv.ToFloat64(withTax)
	if !withTaxOK || math.IsNaN(withTaxValue) || math.IsInf(withTaxValue, 0) || withTaxValue <= 0 {
		errMsg := "withTax无效(格式)"
		if withTaxOK {
			switch {
			case withTaxValue == 0:
				errMsg = "withTax无效(0)"
			case withTaxValue < 0:
				errMsg = "withTax无效(负数)"
			default:
				errMsg = "withTax无效(非有限)"
			}
		}
		if m.state.Logger != nil {
			m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		}
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, errMsg
	}
	display, displayErr := price.GetDisplayFromResult(m.state, accountID, planCode, options, result)
	m.priceCacheStore(accountID, planCode, datacenter, options, display)
	priceText := ""
	if displayErr != nil {
		if m.state.Logger != nil {
			m.state.Logger.Warn("价格目录拆分失败", "monitor")
		}
	}
	if display.TotalKnown || display.BreakdownKnown {
		priceText = m.formatNotificationPrice(display)
	}
	if m.state.Logger != nil {
		m.state.Logger.Debug(fmt.Sprintf("价格校验通过: %s@%s - 含税价格: %v", planCode, datacenter, withTax), "monitor")
	}
	return priceText, true, ""
}

// resolveNotificationDisplay applies the configured notification display currency
// without mutating the raw OVH price result.
func (m *Monitor) resolveNotificationDisplay(display price.DisplayPrice) (price.DisplayPrice, bool) {
	if m == nil || m.state == nil {
		return display, true
	}
	if m.state.Exchange == nil {
		if m.state.Config != nil && exchange.NormalizeDisplayMode(m.state.Config.Get().ExchangeDisplayMode) == exchange.DisplayCNY {
			return price.DisplayPrice{}, false
		}
		return display, true
	}
	sourceCurrency := display.Currency
	convert := func(value float64) (float64, string, bool) {
		return m.state.Exchange.ResolveDisplayAmount(value, sourceCurrency)
	}
	displayCurrency := sourceCurrency
	if display.BreakdownKnown {
		monthly, currency, ok := convert(display.MonthlyWithoutTax)
		if !ok {
			return price.DisplayPrice{}, false
		}
		install, _, ok := convert(display.InstallWithoutTax)
		if !ok {
			return price.DisplayPrice{}, false
		}
		display.MonthlyWithoutTax = monthly
		display.InstallWithoutTax = install
		displayCurrency = currency
	}
	if display.TotalWithoutTaxKnown {
		total, currency, ok := convert(display.TotalWithoutTax)
		if !ok {
			return price.DisplayPrice{}, false
		}
		display.TotalWithoutTax = total
		displayCurrency = currency
	}
	if display.BreakdownKnown || display.TotalWithoutTaxKnown {
		display.Currency = displayCurrency
	}
	return display, true
}

func (m *Monitor) formatNotificationPrice(display price.DisplayPrice) string {
	display, ok := m.resolveNotificationDisplay(display)
	if !ok {
		return unavailablePriceText()
	}
	return formatNotificationPrice(display)
}

func (m *Monitor) displayPricePartsForNotification(display price.DisplayPrice) (string, string, string) {
	display, ok := m.resolveNotificationDisplay(display)
	if !ok {
		return "暂不可用", "暂不可用", "暂不可用"
	}
	return displayPriceParts(display)
}

// 购物车失败时仍可显示月费和安装费，但不伪造首月实际总价。
func (m *Monitor) getCatalogPriceInfoText(accountID, planCode string, options []string) string {
	return m.getCatalogPriceInfoTextWithContext(context.Background(), accountID, planCode, options)
}

func (m *Monitor) getCatalogPriceInfoTextWithContext(ctx context.Context, accountID, planCode string, options []string) string {
	display, err := price.GetCatalogDisplayWithContext(ctx, m.state, accountID, planCode, options)
	if err != nil {
		if m.state.Logger != nil {
			m.state.Logger.Warn("价格目录获取失败", "monitor")
		}
		return ""
	}
	if !display.BreakdownKnown {
		return ""
	}
	display, ok := m.resolveNotificationDisplay(display)
	if !ok {
		return "月费: 暂不可用\n安装费: 暂不可用\n首月总价: 暂不可用"
	}
	installText := "无"
	if display.InstallWithoutTax > 0 {
		installText = formatCurrency(display.InstallWithoutTax, display.Currency)
	}
	return fmt.Sprintf("月费: %s/月\n安装费: %s\n首月总价: 暂不可用",
		formatCurrency(display.MonthlyWithoutTax, display.Currency), installText)
}

// GetPriceInfoText 进程内询价并格式化为通知文案
func (m *Monitor) GetPriceInfoText(accountID, planCode, datacenter string, configInfo map[string]interface{}) string {
	return m.getPriceInfoTextWithContext(context.Background(), accountID, planCode, datacenter, configInfo)
}

func (m *Monitor) getPriceInfoTextWithContext(ctx context.Context, accountID, planCode, datacenter string, configInfo map[string]interface{}) string {
	options := optionsFromConfig(configInfo)
	m.state.Logger.Debug(fmt.Sprintf("开始获取价格: plan_code=%s, datacenter=%s, options=%v account=%s",
		planCode, datacenter, options, accountID), "monitor")

	display, err := price.GetDisplayWithContext(ctx, m.state, accountID, planCode, datacenter, options)
	if err != nil {
		m.state.Logger.Warn("价格拆分失败", "monitor")
	}
	if !display.TotalKnown && !display.BreakdownKnown {
		return ""
	}
	text := m.formatNotificationPrice(display)
	if text != "" {
		m.state.Logger.Debug("价格获取成功: "+strings.ReplaceAll(text, "\n", " | "), "monitor")
	}
	return text
}

// formatNotificationPrice 统一生成监控通知中的价格块。
// 月费与安装费来自 catalog 的未税价格；首月总价优先使用购物车 summary 返回的实际未税总价。
func formatNotificationPrice(display price.DisplayPrice) string {
	if display.Duration != "" && display.Duration != "P1M" {
		return formatPriceWithTotalLabel(display, "购物车总价（"+display.Duration+"）")
	}
	return formatPriceWithTotalLabel(display, "首月总价")
}

func formatPriceWithTotalLabel(display price.DisplayPrice, totalLabel string) string {
	if !display.BreakdownKnown {
		if !display.TotalWithoutTaxKnown {
			return fmt.Sprintf("%s: 暂不可用", totalLabel)
		}
		return fmt.Sprintf("%s: %s", totalLabel, formatCurrency(display.TotalWithoutTax, display.Currency))
	}

	installText := "无"
	if display.InstallWithoutTax > 0 {
		installText = formatCurrency(display.InstallWithoutTax, display.Currency)
	}
	total := display.MonthlyWithoutTax + display.InstallWithoutTax
	if display.TotalWithoutTaxKnown {
		total = display.TotalWithoutTax
	}
	return fmt.Sprintf("月费: %s/月\n安装费: %s\n%s: %s",
		formatCurrency(display.MonthlyWithoutTax, display.Currency),
		installText,
		totalLabel,
		formatCurrency(total, display.Currency))
}

// FormatDisplayPrice 保持服务器型号卡片的既有字段结构，但金额统一显示未税值。
// 监控通知请使用内部 formatNotificationPrice，避免改变卡片兼容性。
func FormatDisplayPrice(display price.DisplayPrice) string {
	if display.Duration != "" && display.Duration != "P1M" {
		return formatPriceWithTotalLabel(display, "购物车总价（"+display.Duration+"）")
	}
	return formatPriceWithTotalLabel(display, "总价")
}

func formatCurrency(value float64, currency string) string {
	sym := currencySymbol(currency)
	return fmt.Sprintf("%s%.2f", sym, value)
}

func currencySymbol(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "EUR":
		return "€"
	case "USD":
		return "$"
	case "CAD":
		return "CA$"
	case "GBP":
		return "£"
	case "AUD":
		return "A$"
	case "SGD":
		return "S$"
	case "INR":
		return "₹"
	case "PLN":
		return "zł"
	case "JPY", "CNY":
		return "¥"
	case "KRW":
		return "₩"
	case "HKD":
		return "HK$"
	default:
		code := strings.ToUpper(strings.TrimSpace(currency))
		if code == "" {
			return "币种未知 "
		}
		return code + " "
	}
}

// getPriceWithTimeout 带超时的询价
func (m *Monitor) getPriceWithTimeout(accountID, planCode, datacenter string, configInfo map[string]interface{}, timeout time.Duration) (string, string) {
	return m.getPriceWithTimeoutContext(context.Background(), accountID, planCode, datacenter, configInfo, timeout)
}

func (m *Monitor) getPriceWithTimeoutContext(parent context.Context, accountID, planCode, datacenter string, configInfo map[string]interface{}, timeout time.Duration) (string, string) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type result struct {
		text string
	}
	resultCh := make(chan result, 1)
	start := time.Now()
	go func() {
		resultCh <- result{text: m.getPriceInfoTextWithContext(ctx, accountID, planCode, datacenter, configInfo)}
	}()
	select {
	case result := <-resultCh:
		if result.text == "" {
			elapsed := time.Since(start).Seconds()
			if ctx.Err() != nil {
				if parent.Err() != nil {
					return "", "价格接口已取消"
				}
				return "", fmt.Sprintf("价格接口超时（等待%.1f秒）", elapsed)
			}
			return "", fmt.Sprintf("价格接口未返回结果（耗时%.1f秒）", elapsed)
		}
		return result.text, ""
	case <-ctx.Done():
		elapsed := time.Since(start).Seconds()
		if parent.Err() != nil {
			return "", "价格接口已取消"
		}
		m.state.Logger.Warn("价格获取超时，已请求取消后台请求，发送不带价格的通知。", "monitor")
		return "", fmt.Sprintf("价格接口超时（等待%.1f秒）", elapsed)
	}
}
