package vps

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	ovhsdk "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

type Outcome struct {
	Success  bool
	Fatal    bool
	Reason   string
	OrderID  string
	OrderURL string
}

var dcRegion = map[string]string{
	"BHS": "canada", "SGP": "canada", "SYD": "canada", "YNM": "canada",
	"DE": "europe", "EU-SOUTH-MIL": "europe", "EU-WEST-RBX": "europe",
	"GRA": "europe", "SBG": "europe", "UK": "europe", "WAW": "europe",
	"US-EAST-VA": "united_states", "US-WEST-OR": "united_states",
}

// PurchaseVPS 在指定数据中心下单一台或多台 VPS。
func PurchaseVPS(state *app.State, sub types.VPSSubscription, dcCode string) Outcome {
	if state == nil || state.OVH == nil {
		return Outcome{Fatal: true, Reason: "应用状态不可用"}
	}
	if strings.TrimSpace(sub.AutoOrderAccountID) == "" {
		return Outcome{Fatal: true, Reason: "没有指定下单账户"}
	}
	client, err := state.OVH.ClientFor(sub.AutoOrderAccountID)
	if err != nil {
		return Outcome{Fatal: true, Reason: fmt.Sprintf("账户 %s 不可用: %s", sub.AutoOrderAccountID, err)}
	}
	acc, ok := state.FindAccount(sub.AutoOrderAccountID)
	if !ok {
		return Outcome{Fatal: true, Reason: "下单账户不存在: " + sub.AutoOrderAccountID}
	}
	subsidiary := NormalizeSubsidiary(sub.OvhSubsidiary)
	if subsidiary == "" {
		subsidiary = DefaultSubsidiary(state, sub.AutoOrderAccountID)
	}
	if !KnownSubsidiaryForOrder(subsidiary) {
		return Outcome{Fatal: true, Reason: "未知的 OVH 子公司: " + subsidiary}
	}
	if ar, sr := ovh.EndpointRegion(acc.Endpoint), ovh.SubsidiaryRegion(subsidiary); ar != sr {
		return Outcome{Fatal: true, Reason: fmt.Sprintf("下单账户在 %s，而订阅子公司 %s 属于 %s；两个站点的购物车不互通", regionLabel(ar), subsidiary, regionLabel(sr))}
	}
	if strings.TrimSpace(dcCode) == "" {
		return Outcome{Fatal: true, Reason: "缺少目标数据中心"}
	}
	quantity := sub.Quantity
	if quantity < 1 {
		quantity = 1
	}
	if quantity > 20 {
		return Outcome{Fatal: true, Reason: "VPS 数量不能超过 20"}
	}
	// 本地产品的资金安全边界：自动付款保持关闭，即使旧客户端带入该字段也不放行。
	autoPay := false

	state.Logger.Info(fmt.Sprintf("[VPS下单] 开始: %s @ %s (子公司 %s, 账户 %s, %d 台)", sub.PlanCode, dcCode, subsidiary, acc.Name, quantity), "vps_purchase")
	var cartResult map[string]interface{}
	if err := client.Post("/order/cart", map[string]interface{}{"ovhSubsidiary": subsidiary}, &cartResult); err != nil {
		return Outcome{Reason: "创建购物车失败: " + err.Error()}
	}
	cartID, _ := cartResult["cartId"].(string)
	if cartID == "" {
		return Outcome{Reason: fmt.Sprintf("购物车响应里没有 cartId: %v", cartResult)}
	}
	checkoutStarted := false
	defer func() {
		if checkoutStarted || cartID == "" {
			return
		}
		if cleanupErr := client.Delete("/order/cart/"+cartID, nil); cleanupErr != nil {
			state.Logger.Debug("[VPS下单] 清理失败 cart "+cartID+": "+cleanupErr.Error(), "vps_purchase")
		}
	}()

	if err := client.Post("/order/cart/"+cartID+"/assign", nil, nil); err != nil {
		return Outcome{Reason: "绑定购物车失败: " + err.Error()}
	}
	duration, pricingMode := lookupVPSPricing(state, client, cartID, sub.PlanCode)
	if duration == "" {
		duration, pricingMode = "P1M", "default"
		state.Logger.Warn("[VPS下单] 拉不到计价组合，退回月付 P1M/default", "vps_purchase")
	}
	var itemResult map[string]interface{}
	if err := client.Post("/order/cart/"+cartID+"/vps", map[string]interface{}{
		"planCode": sub.PlanCode, "duration": duration, "pricingMode": pricingMode, "quantity": quantity,
	}, &itemResult); err != nil {
		message := err.Error()
		if strings.Contains(strings.ToLower(message), "not found") || strings.Contains(strings.ToLower(message), "invalid plancode") {
			return Outcome{Fatal: true, Reason: fmt.Sprintf("加购 %s 失败(%s)：型号可能已经停售", sub.PlanCode, message)}
		}
		return Outcome{Reason: fmt.Sprintf("加购 %s 失败: %s", sub.PlanCode, message)}
	}
	itemID, _ := numconv.ToInt64(itemResult["itemId"])
	if itemID == 0 {
		return Outcome{Reason: fmt.Sprintf("加购响应里没有 itemId: %v", itemResult)}
	}

	required, err := fetchRequiredConfig(client, cartID, itemID)
	if err != nil {
		return Outcome{Reason: "拉必需配置失败: " + err.Error()}
	}
	configs := buildVPSConfig(required, dcCode, sub.OS)
	hasDC := false
	for _, config := range configs {
		if config.label == "vps_datacenter" {
			hasDC = true
			break
		}
	}
	if !hasDC {
		return Outcome{Fatal: true, Reason: fmt.Sprintf("购物车未给出 vps_datacenter，拒绝在未指定机房的情况下结账（目标机房 %s）", dcCode)}
	}
	regionRequired, hasRegion := false, false
	for _, item := range required {
		if item.Label == "region" {
			regionRequired = item.Required
		}
	}
	for _, config := range configs {
		if config.label == "region" {
			hasRegion = true
			break
		}
	}
	if regionRequired && !hasRegion {
		return Outcome{Fatal: true, Reason: fmt.Sprintf("必填配置 region 无法确定（机房 %s）", dcCode)}
	}
	for _, config := range configs {
		if err := client.Post(fmt.Sprintf("/order/cart/%s/item/%d/configuration", cartID, itemID), map[string]interface{}{
			"label": config.label, "value": config.value,
		}, nil); err != nil {
			return Outcome{Fatal: true, Reason: fmt.Sprintf("设置 %s=%s 失败: %s", config.label, config.value, err.Error())}
		}
	}

	var checkoutResult map[string]interface{}
	// 一旦请求已经发出，响应可能在服务端成功后才断开；保留购物车，
	// 避免把不确定结账误判为失败并在下一轮重复下单。
	checkoutStarted = true
	if err := client.Post("/order/cart/"+cartID+"/checkout", map[string]interface{}{
		"autoPayWithPreferredPaymentMethod": autoPay,
		"waiveRetractationPeriod":           true,
	}, &checkoutResult); err != nil {
		return Outcome{Reason: "结账失败: " + err.Error()}
	}
	orderID := numconv.ToString(checkoutResult["orderId"])
	orderURL, _ := checkoutResult["url"].(string)
	if orderID == "" {
		return Outcome{Reason: "结账成功但未返回订单号"}
	}
	if strings.TrimSpace(orderURL) == "" {
		orderURL = ovh.ManagerOrderURL(acc.Endpoint, orderID)
	}
	state.Logger.Info(fmt.Sprintf("[VPS下单] 成功: %s @ %s 订单 %s", sub.PlanCode, dcCode, orderID), "vps_purchase")
	return Outcome{Success: true, OrderID: orderID, OrderURL: orderURL}
}

type kv struct{ label, value string }

type requiredItem struct {
	Label         string   `json:"label"`
	Required      bool     `json:"required"`
	AllowedValues []string `json:"allowedValues"`
}

func fetchRequiredConfig(client *ovhsdk.Client, cartID string, itemID int64) ([]requiredItem, error) {
	var required []requiredItem
	err := client.Get(fmt.Sprintf("/order/cart/%s/item/%d/requiredConfiguration", cartID, itemID), &required)
	return required, err
}

func buildVPSConfig(required []requiredItem, dcCode, os string) []kv {
	byLabel := make(map[string]requiredItem, len(required))
	for _, item := range required {
		byLabel[item.Label] = item
	}
	configs := make([]kv, 0, 3)
	if _, ok := byLabel["vps_datacenter"]; ok {
		configs = append(configs, kv{"vps_datacenter", dcCode})
	} else if len(required) == 0 {
		configs = append(configs, kv{"vps_datacenter", dcCode})
	}
	if item, ok := byLabel["region"]; ok {
		switch {
		case len(item.AllowedValues) == 1:
			configs = append(configs, kv{"region", item.AllowedValues[0]})
		case len(item.AllowedValues) > 1:
			if wanted := dcRegion[strings.ToUpper(strings.TrimSpace(dcCode))]; wanted != "" && containsFold(item.AllowedValues, wanted) {
				configs = append(configs, kv{"region", wanted})
			}
		}
	}
	os = strings.TrimSpace(os)
	if os != "" {
		if item, ok := byLabel["vps_os"]; !ok || len(item.AllowedValues) == 0 || containsFold(item.AllowedValues, os) {
			configs = append(configs, kv{"vps_os", os})
		}
	}
	return configs
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func lookupVPSPricing(state *app.State, client *ovhsdk.Client, cartID, planCode string) (string, string) {
	var definitions []struct {
		PlanCode string `json:"planCode"`
		Prices   []struct {
			Duration    string   `json:"duration"`
			PricingMode string   `json:"pricingMode"`
			Capacities  []string `json:"capacities"`
		} `json:"prices"`
	}
	if err := client.Get("/order/cart/"+cartID+"/vps", &definitions); err != nil {
		if state != nil && state.Logger != nil {
			state.Logger.Debug("[VPS下单] 拉计价失败: "+err.Error(), "vps_purchase")
		}
		return "", ""
	}
	for _, definition := range definitions {
		if !strings.EqualFold(definition.PlanCode, planCode) {
			continue
		}
		for _, price := range definition.Prices {
			if price.PricingMode == "default" && price.Duration == "P1M" {
				return price.Duration, price.PricingMode
			}
		}
		for _, price := range definition.Prices {
			for _, capacity := range price.Capacities {
				if capacity == "renew" {
					return price.Duration, price.PricingMode
				}
			}
		}
		if len(definition.Prices) > 0 {
			return definition.Prices[0].Duration, definition.Prices[0].PricingMode
		}
	}
	return "", ""
}

func recordVPSPurchase(state *app.State, sub types.VPSSubscription, dcCode string, outcome Outcome) {
	if state == nil {
		return
	}
	reason := outcome.Reason
	entry := types.PurchaseHistoryEntry{
		ID: uuid.NewString(), TaskID: "vps:" + sub.ID, AccountID: sub.AutoOrderAccountID,
		PlanCode: sub.PlanCode, Datacenter: dcCode, Options: []string{}, PurchaseTime: types.NowISO(), AttemptCount: 1,
	}
	if outcome.Success {
		entry.Status, entry.OrderID, entry.OrderURL = "success", outcome.OrderID, outcome.OrderURL
	} else {
		entry.Status, entry.ErrorMessage = "failed", &reason
	}
	if err := state.MutateHistory(func(history []types.PurchaseHistoryEntry) ([]types.PurchaseHistoryEntry, error) {
		return append(history, entry), nil
	}); err != nil {
		state.Logger.Warn("保存 VPS 自动下单历史失败: "+err.Error(), "vps_purchase")
	}
}

func autoOrderOnRestock(state *app.State, sub types.VPSSubscription, data []map[string]interface{}) {
	if !sub.AutoOrder || strings.TrimSpace(sub.AutoOrderAccountID) == "" {
		return
	}
	for _, dc := range data {
		code, _ := dc["code"].(string)
		if strings.TrimSpace(code) == "" {
			continue
		}
		outcome := PurchaseVPS(state, sub, code)
		recordVPSPurchase(state, sub, code, outcome)
		if outcome.Success {
			message := fmt.Sprintf("VPS 自动下单成功\n\n型号: %s\n机房: %s\n订单号: %s\n\n请登录 OVH 控制台确认支付状态。", sub.PlanCode, strings.ToUpper(code), outcome.OrderID)
			sendVPSOrderNotification(state, message)
			return
		}
		state.Logger.Error(fmt.Sprintf("[VPS下单] %s @ %s 失败: %s", sub.PlanCode, code, outcome.Reason), "vps_purchase")
		sendVPSOrderNotification(state, fmt.Sprintf("VPS 自动下单未成功\n\n型号: %s\n机房: %s\n原因: %s", sub.PlanCode, strings.ToUpper(code), outcome.Reason))
		if outcome.Fatal {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func sendVPSOrderNotification(state *app.State, message string) {
	if state == nil {
		return
	}
	for _, channel := range monitor.ConfiguredNotificationChannels(state) {
		switch channel {
		case monitor.NotificationChannelTelegram:
			_ = telegram.SendMessage(state, message, nil)
		case monitor.NotificationChannelFeishu:
			_ = monitor.FeishuSendDefaultNotification(state, "VPS 自动下单", message, "blue", nil)
		case monitor.NotificationChannelWeixin:
			_ = monitor.SendWeixinNotification(state, message)
		}
	}
}

func regionLabel(region string) string {
	switch region {
	case "US":
		return "US 区"
	case "CA":
		return "CA 区"
	default:
		return "EU 区"
	}
}

func KnownSubsidiaryForOrder(sub string) bool { return ovh.KnownSubsidiary(sub) }

// ValidateAutoOrderAccount checks the non-secret account/region contract before persisting a subscription.
func ValidateAutoOrderAccount(state *app.State, subsidiary, accountID string) error {
	if strings.TrimSpace(accountID) == "" {
		return fmt.Errorf("自动下单必须指定账户")
	}
	if state == nil {
		return fmt.Errorf("应用状态不可用")
	}
	account, ok := state.FindAccount(accountID)
	if !ok {
		return fmt.Errorf("下单账户不存在")
	}
	if !ovh.KnownSubsidiary(subsidiary) {
		return fmt.Errorf("未知的 OVH 子公司")
	}
	if ovh.EndpointRegion(account.Endpoint) != ovh.SubsidiaryRegion(subsidiary) {
		return fmt.Errorf("下单账户与订阅子公司不属于同一 OVH 站点")
	}
	return nil
}
