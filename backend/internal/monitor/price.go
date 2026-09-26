package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// verifyPriceAvailable 完成一次购物车价格校验，并返回可直接用于通知的价格文案。
// 返回值依次为：价格文案、价格校验是否通过、失败原因。
func (m *Monitor) verifyPriceAvailable(ctx context.Context, accountID, planCode, datacenter string, configInfo map[string]interface{}) (string, bool, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	options := optionsFromConfig(configInfo)
	result := price.GetInternalWithContext(ctx, m.state, accountID, planCode, datacenter, options)
	if err := ctx.Err(); err != nil {
		return "", false, "价格校验已取消"
	}
	if !result.Success {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "未知错误"
		}
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, errMsg
	}
	if result.Price == nil {
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - price字段缺失", planCode, datacenter), "monitor")
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, "price字段缺失"
	}
	withTax := result.Price.Prices["withTax"]
	if withTax == nil {
		errMsg := "withTax无效(<nil>)"
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, errMsg
	}
	if v, ok := numconv.ToFloat64(withTax); ok && v == 0 {
		errMsg := "withTax无效(0)"
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		return m.getCatalogPriceInfoTextWithContext(ctx, accountID, planCode, options), false, errMsg
	}
	display, displayErr := price.GetDisplayFromResult(m.state, accountID, planCode, options, result)
	priceText := ""
	if displayErr != nil {
		m.state.Logger.Warn("价格目录拆分失败", "monitor")
	}
	if display.TotalKnown || display.BreakdownKnown {
		priceText = formatNotificationPrice(display)
	}
	m.state.Logger.Debug(fmt.Sprintf("价格校验通过: %s@%s - 含税价格: %v", planCode, datacenter, withTax), "monitor")
	return priceText, true, ""
}

// getCatalogPriceInfoText 与服务器列表使用相同的公开 catalog 价格口径。
// 购物车失败时仍可显示月费和安装费，但不伪造首月实际总价。
func (m *Monitor) getCatalogPriceInfoText(accountID, planCode string, options []string) string {
	return m.getCatalogPriceInfoTextWithContext(context.Background(), accountID, planCode, options)
}

func (m *Monitor) getCatalogPriceInfoTextWithContext(ctx context.Context, accountID, planCode string, options []string) string {
	display, err := price.GetCatalogDisplayWithContext(ctx, m.state, accountID, planCode, options)
	if err != nil {
		m.state.Logger.Warn("价格目录获取失败", "monitor")
		return ""
	}
	if !display.BreakdownKnown {
		return ""
	}
	installText := "无"
	if display.InstallWithTax > 0 {
		installText = formatCurrency(display.InstallWithTax, display.Currency)
	}
	return fmt.Sprintf("月费: %s/月\n安装费: %s\n首月总价: 暂不可用",
		formatCurrency(display.MonthlyWithTax, display.Currency), installText)
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
	text := formatNotificationPrice(display)
	if text != "" {
		m.state.Logger.Debug("价格获取成功: "+strings.ReplaceAll(text, "\n", " | "), "monitor")
	}
	return text
}

// formatNotificationPrice 统一生成监控通知中的价格块。
// 月费与安装费来自 catalog 的含税价格；首月总价优先使用购物车 summary 的含税总价。
func formatNotificationPrice(display price.DisplayPrice) string {
	if display.Duration != "" && display.Duration != "P1M" {
		return formatPriceWithTotalLabel(display, "购物车含税总价（"+display.Duration+"）")
	}
	return formatPriceWithTotalLabel(display, "首月总价")
}

func formatPriceWithTotalLabel(display price.DisplayPrice, totalLabel string) string {
	if !display.BreakdownKnown {
		if !display.TotalKnown {
			return ""
		}
		return fmt.Sprintf("%s: %s", totalLabel, formatCurrency(display.TotalWithTax, display.Currency))
	}

	installText := "无"
	if display.InstallWithTax > 0 {
		installText = formatCurrency(display.InstallWithTax, display.Currency)
	}
	total := display.TotalWithTax
	if !display.TotalKnown {
		total = display.MonthlyWithTax + display.InstallWithTax
	}
	return fmt.Sprintf("月费: %s/月\n安装费: %s\n%s: %s",
		formatCurrency(display.MonthlyWithTax, display.Currency),
		installText,
		totalLabel,
		formatCurrency(total, display.Currency))
}

// FormatDisplayPrice 保持服务器型号卡片的既有“总价”字段格式。
// 监控通知请使用内部 formatNotificationPrice，避免改变卡片兼容性。
func FormatDisplayPrice(display price.DisplayPrice) string {
	if display.Duration != "" && display.Duration != "P1M" {
		return formatPriceWithTotalLabel(display, "购物车含税总价（"+display.Duration+"）")
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
