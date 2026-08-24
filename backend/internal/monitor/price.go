package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/price"
)

// resolvePriceAccount 询价账户始终使用当前默认账户，与 TG / 飞书命令的数据源一致。
// AutoOrderAccountID 只决定自动下单账户，不再隐式改变目录、库存和通知数据源。
func (m *Monitor) resolvePriceAccount(sub *Subscription) string {
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

// verifyPriceAvailable 进程内询价（不再 HTTP 自调 /api/internal/monitor/price）。
// accountID 为空时用默认账户。
func (m *Monitor) verifyPriceAvailable(accountID, planCode, datacenter string, configInfo map[string]interface{}) (bool, string) {
	options := optionsFromConfig(configInfo)
	result := price.GetInternal(m.state, accountID, planCode, datacenter, options)
	if !result.Success {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "未知错误"
		}
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		return false, errMsg
	}
	if result.Price == nil {
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - price字段缺失", planCode, datacenter), "monitor")
		return false, "price字段缺失"
	}
	withTax := result.Price.Prices["withTax"]
	if withTax == nil {
		errMsg := "withTax无效(<nil>)"
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - %s", planCode, datacenter, errMsg), "monitor")
		return false, errMsg
	}
	if v, ok := numconv.ToFloat64(withTax); ok && v == 0 {
		m.state.Logger.Debug(fmt.Sprintf("价格校验失败: %s@%s - withTax无效(0)", planCode, datacenter), "monitor")
		return false, "withTax无效(0)"
	}
	m.state.Logger.Debug(fmt.Sprintf("价格校验通过: %s@%s - 含税价格: %v", planCode, datacenter, withTax), "monitor")
	return true, ""
}

// GetPriceInfoText 进程内询价并格式化为通知文案
func (m *Monitor) GetPriceInfoText(accountID, planCode, datacenter string, configInfo map[string]interface{}) string {
	options := optionsFromConfig(configInfo)
	m.state.Logger.Debug(fmt.Sprintf("开始获取价格: plan_code=%s, datacenter=%s, options=%v account=%s",
		planCode, datacenter, options, accountID), "monitor")

	display, err := price.GetDisplay(m.state, accountID, planCode, datacenter, options)
	if err != nil {
		m.state.Logger.Warn("价格拆分失败: "+err.Error(), "monitor")
	}
	if !display.TotalKnown && !display.BreakdownKnown {
		return ""
	}
	text := formatDisplayPrice(display)
	if text != "" {
		m.state.Logger.Debug("价格获取成功: "+strings.ReplaceAll(text, "\n", " | "), "monitor")
	}
	return text
}

// formatDisplayPrice 统一生成上架通知中的价格块。
// 月费与安装费来自 catalog 的含税价格；首月总价优先使用购物车 summary 的含税总价。
func formatDisplayPrice(display price.DisplayPrice) string {
	if !display.BreakdownKnown {
		if !display.TotalKnown {
			return ""
		}
		return fmt.Sprintf("总价: %s", formatCurrency(display.TotalWithTax, display.Currency))
	}

	installText := "无"
	if display.InstallWithTax > 0 {
		installText = formatCurrency(display.InstallWithTax, display.Currency)
	}
	total := display.TotalWithTax
	if !display.TotalKnown {
		total = display.MonthlyWithTax + display.InstallWithTax
	}
	return fmt.Sprintf("月费: %s/月\n安装费: %s\n总价: %s",
		formatCurrency(display.MonthlyWithTax, display.Currency),
		installText,
		formatCurrency(total, display.Currency))
}

// FormatDisplayPrice 对外复用通知价格格式，保证 Telegram、飞书与监控通知口径一致。
func FormatDisplayPrice(display price.DisplayPrice) string {
	return formatDisplayPrice(display)
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
		if currency == "" {
			return "EUR "
		}
		return strings.TrimSpace(currency) + " "
	}
}

// getPriceWithTimeout 带超时的询价
func (m *Monitor) getPriceWithTimeout(accountID, planCode, datacenter string, configInfo map[string]interface{}, timeout time.Duration) (string, string) {
	type res struct {
		text string
	}
	ch := make(chan res, 1)
	start := time.Now()
	go func() {
		text := m.GetPriceInfoText(accountID, planCode, datacenter, configInfo)
		ch <- res{text: text}
	}()
	select {
	case r := <-ch:
		if r.text == "" {
			elapsed := time.Since(start).Seconds()
			return "", fmt.Sprintf("价格接口未返回结果（耗时%.1f秒）", elapsed)
		}
		return r.text, ""
	case <-time.After(timeout):
		elapsed := time.Since(start).Seconds()
		errMsg := fmt.Sprintf("价格接口超时（等待%.1f秒）", elapsed)
		m.state.Logger.Warn("价格获取超时，发送不带价格的通知。后台请求将继续运行直到完成。", "monitor")
		return "", errMsg
	}
}
