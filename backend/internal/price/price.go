package price

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/ovh"
)

// Result 对应 Python: _get_server_price_internal 返回结构
type Result struct {
	Success    bool                   `json:"success"`
	PlanCode   string                 `json:"planCode,omitempty"`
	Datacenter string                 `json:"datacenter,omitempty"`
	Options    []string               `json:"options,omitempty"`
	Price      *PriceInfo             `json:"price"`
	Error      string                 `json:"error,omitempty"`
	Raw        map[string]interface{} `json:"-"`
}

type PriceInfo struct {
	PricingMode string                   `json:"pricingMode"`
	Prices      map[string]interface{}   `json:"prices"`
	Items       []map[string]interface{} `json:"items"`
}

// GetInternal 询价。accountID 决定用哪个账户调 OVH(空 = 默认账户),
// 以及购物车走哪个 subsidiary(账户的 zone)。多账户必须区分。
func GetInternal(state *app.State, accountID, planCode, datacenter string, options []string) Result {
	return GetInternalWithContext(context.Background(), state, accountID, planCode, datacenter, options)
}

// GetInternalWithContext 执行可取消的购物车询价。旧调用方继续通过
// GetInternal 使用后台 context；监控超时链路传入带 deadline 的 context，
// 避免调用方返回后仍继续创建、配置或绑定购物车。
func GetInternalWithContext(ctx context.Context, state *app.State, accountID, planCode, datacenter string, options []string) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	planCode = strings.TrimSpace(planCode)
	datacenter = strings.TrimSpace(datacenter)
	if planCode == "" {
		return Result{Success: false, Error: "缺少 planCode"}
	}
	if datacenter == "" {
		return Result{Success: false, Error: "缺少 datacenter"}
	}
	if err := ctx.Err(); err != nil {
		return Result{Success: false, Error: err.Error()}
	}
	// 与 catalog 拆价保持一致：忽略空白 option，并去掉来自消息/HTTP
	// 参数的首尾空格，避免合法 addon 被误判为“不在可用选项中”。
	options = normalizeOptionCodes(options)
	if options == nil {
		options = []string{}
	}
	apiDC := ovh.ConvertDisplayDCToAPIDC(datacenter)

	client, err := state.OVH.ClientFor(accountID)
	if err != nil {
		return Result{Success: false, Error: "未配置OVH API密钥: " + err.Error()}
	}
	acc, _ := state.FindAccount(accountID)
	subsidiary := acc.Zone
	if subsidiary == "" {
		subsidiary = "IE"
	}

	state.Logger.Info(fmt.Sprintf("查询 %s 的配置价格，数据中心: %s (原始: %s), 选项: %v",
		planCode, apiDC, datacenter, options), "price")

	cartID := ""

	cleanup := func() {
		if cartID == "" {
			return
		}
		// 主询价 context 可能已经超时；清理使用独立短超时，确保取消
		// 询价不会把已创建的临时购物车永久遗留在账户中。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.DeleteWithContext(cleanupCtx, "/order/cart/"+cartID, nil)
	}
	// 防止中间步骤 panic（map 断言 / 空指针等）导致 cart 泄漏永不清理；
	// Python `app.py:3961-3989` 在两个 except 块都 best-effort delete
	defer cleanup()

	// 1. 创建购物车
	var cartResult map[string]interface{}
	if err := client.PostWithContext(ctx, "/order/cart", map[string]interface{}{
		"ovhSubsidiary": subsidiary,
	}, &cartResult); err != nil {
		return Result{Success: false, Error: err.Error()}
	}
	cartID, _ = cartResult["cartId"].(string)
	if strings.TrimSpace(cartID) == "" {
		return Result{Success: false, Error: fmt.Sprintf("创建购物车成功但响应缺少 cartId（响应: %v）", cartResult)}
	}
	state.Logger.Debug("购物车创建成功，ID: "+cartID, "price")

	// 2. 添加基础商品
	itemPayload := map[string]interface{}{
		"planCode":    planCode,
		"pricingMode": "default",
		"duration":    "P1M",
		"quantity":    1,
	}
	var itemResult map[string]interface{}
	if err := client.PostWithContext(ctx, "/order/cart/"+cartID+"/eco", itemPayload, &itemResult); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "is not available in") {
			state.Logger.Warn("配置在指定数据中心不可用: "+msg, "price")
			return Result{Success: false, Error: "该配置在指定数据中心不可用"}
		}
		return Result{Success: false, Error: msg}
	}
	itemID, _ := numconv.ToInt64(itemResult["itemId"])
	if itemID == 0 {
		return Result{Success: false, Error: fmt.Sprintf("无法从购物车响应中解析 itemId（响应: %v）", itemResult)}
	}
	state.Logger.Debug(fmt.Sprintf("基础商品添加成功，项目 ID: %d", itemID), "price")

	// 3. 设置必需配置
	// 1:1 对应 Python app.py:3756-3761：dict 在 Py3.7+ 保持插入序：datacenter → os → region。
	// Go map 遍历顺序随机，若 region 先于 dedicated_datacenter 设置，OVH 可能返回 400
	region := ovh.RegionForDC(apiDC)
	type kv struct{ label, value string }
	configurations := []kv{
		{"dedicated_datacenter", apiDC},
		{"dedicated_os", "none_64.en"},
	}
	if region != "" {
		configurations = append(configurations, kv{"region", region})
	}
	for _, cfg := range configurations {
		body := map[string]interface{}{"label": cfg.label, "value": cfg.value}
		if err := client.PostWithContext(ctx, fmt.Sprintf("/order/cart/%s/item/%d/configuration", cartID, itemID), body, nil); err != nil {
			return Result{Success: false, Error: fmt.Sprintf("设置必需配置 %s 失败: %s", cfg.label, err.Error())}
		} else {
			state.Logger.Debug(fmt.Sprintf("设置配置: %s = %s", cfg.label, cfg.value), "price")
		}
	}

	// 4. 添加用户 addons
	if len(options) > 0 {
		var availableOpts []map[string]interface{}
		q := url.Values{}
		q.Set("planCode", planCode)
		if err := client.GetWithContext(ctx, fmt.Sprintf("/order/cart/%s/eco/options?%s", cartID, q.Encode()), &availableOpts); err != nil {
			return Result{Success: false, Error: fmt.Sprintf("获取可用 Eco 选项失败: %s", err.Error())}
		}
		state.Logger.Debug(fmt.Sprintf("找到 %d 个可用选项", len(availableOpts)), "price")
		added := []string{}
		for _, wanted := range options {
			matched := false
			for _, avail := range availableOpts {
				if availPlanCode, _ := avail["planCode"].(string); strings.TrimSpace(availPlanCode) == wanted {
					duration := "P1M"
					if d, ok := avail["duration"].(string); ok && d != "" {
						duration = d
					}
					pricingMode := "default"
					if pm, ok := avail["pricingMode"].(string); ok && pm != "" {
						pricingMode = pm
					}
					optPayload := map[string]interface{}{
						"itemId":      itemID,
						"planCode":    wanted,
						"duration":    duration,
						"pricingMode": pricingMode,
						"quantity":    1,
					}
					if err := client.PostWithContext(ctx, fmt.Sprintf("/order/cart/%s/eco/options", cartID), optPayload, nil); err != nil {
						return Result{Success: false, Error: fmt.Sprintf("添加选项 %s 失败: %s", wanted, err.Error())}
					}
					added = append(added, wanted)
					matched = true
					state.Logger.Debug("成功添加选项: "+wanted, "price")
					break
				}
			}
			if !matched {
				return Result{Success: false, Error: fmt.Sprintf("请求的选项 %s 不在 OVH 可用选项中", wanted)}
			}
		}
		state.Logger.Info(fmt.Sprintf("共添加 %d 个选项: %v", len(added), added), "price")
	}

	// 5. 绑定购物车
	if err := client.PostWithContext(ctx, "/order/cart/"+cartID+"/assign", map[string]interface{}{}, nil); err != nil {
		return Result{Success: false, Error: "绑定购物车失败: " + err.Error()}
	}

	// 6. 获取详情 + summary
	// 1:1 对应 Python app.py:3812-3813：OVH 错误直接抛进外层 except 返回 success:false。
	// 之前 Go 静默忽略会导致瞬断时 success:true 但价格全 nil，前端误以为有效价格 0
	var cartInfo map[string]interface{}
	if err := client.GetWithContext(ctx, "/order/cart/"+cartID, &cartInfo); err != nil {
		return Result{Success: false, Error: err.Error()}
	}
	var cartSummary map[string]interface{}
	if err := client.GetWithContext(ctx, "/order/cart/"+cartID+"/summary", &cartSummary); err != nil {
		return Result{Success: false, Error: err.Error()}
	}

	priceInfo := &PriceInfo{
		PricingMode: "default",
		Prices: map[string]interface{}{
			"withTax":      nil,
			"withoutTax":   nil,
			"tax":          nil,
			"currencyCode": nil,
		},
		Items: []map[string]interface{}{},
	}

	// 从 summary 提取价格
	if cartSummary != nil {
		if pricesField, ok := cartSummary["prices"].(map[string]interface{}); ok {
			withTaxVal, withTaxCurrency := extractPriceField(pricesField["withTax"])
			withoutTaxVal, _ := extractPriceField(pricesField["withoutTax"])
			taxVal, _ := extractPriceField(pricesField["tax"])

			currency := withTaxCurrency
			if currency == "" {
				if c, ok := pricesField["currencyCode"].(string); ok {
					currency = c
				}
			}
			if currency == "" {
				currency = "EUR"
			}

			priceInfo.Prices["withTax"] = withTaxVal
			priceInfo.Prices["withoutTax"] = withoutTaxVal
			priceInfo.Prices["tax"] = taxVal
			priceInfo.Prices["currencyCode"] = currency
		}
	}
	if !validPriceValue(priceInfo.Prices["withTax"]) {
		return Result{Success: false, Error: "购物车 summary 缺少有效含税价格"}
	}

	// 每个商品的价格
	if cartInfo != nil {
		if itemsRaw, ok := cartInfo["items"].([]interface{}); ok {
			for _, itemRaw := range itemsRaw {
				it, ok := itemRaw.(map[string]interface{})
				if !ok {
					continue
				}
				itemPrices, _ := it["prices"].(map[string]interface{})
				if itemPrices == nil {
					continue
				}
				withTaxVal, currency := extractPriceField(itemPrices["withTax"])
				withoutTaxVal, _ := extractPriceField(itemPrices["withoutTax"])
				taxVal, _ := extractPriceField(itemPrices["tax"])
				if currency == "" {
					if c, ok := itemPrices["currencyCode"].(string); ok {
						currency = c
					}
				}
				if currency == "" {
					currency = "EUR"
				}
				priceInfo.Items = append(priceInfo.Items, map[string]interface{}{
					"itemId":      it["itemId"],
					"planCode":    it["planCode"],
					"description": it["description"],
					"prices": map[string]interface{}{
						"withTax":      withTaxVal,
						"withoutTax":   withoutTaxVal,
						"tax":          taxVal,
						"currencyCode": currency,
					},
				})
			}
		}
	}

	withTaxStr := fmt.Sprintf("%v", priceInfo.Prices["withTax"])
	currencyStr := fmt.Sprintf("%v", priceInfo.Prices["currencyCode"])
	state.Logger.Info(fmt.Sprintf("价格查询成功: 总价含税=%s %s", withTaxStr, currencyStr), "price")

	return Result{
		Success:    true,
		PlanCode:   planCode,
		Datacenter: datacenter,
		Options:    options,
		Price:      priceInfo,
	}
}

// validPriceValue 防止 OVH 返回 NaN/Inf 或无法解析的价格被当成成功结果。
// 0 仍视为合法数值（例如极少数促销商品），库存监控会单独拒绝 0 价配置。
func validPriceValue(v interface{}) bool {
	f, ok := numconv.ToFloat64(v)
	return ok && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0
}

// extractPriceField 兼容字典形式（{value, currencyCode}）与直接值，统一转 float64
// OVH SDK 用 UseNumber，数字是 json.Number，必须经过 numconv 才能拿到正确值
func extractPriceField(v interface{}) (interface{}, string) {
	if v == nil {
		return nil, ""
	}
	if m, ok := v.(map[string]interface{}); ok {
		currency, _ := m["currencyCode"].(string)
		if f, ok := numconv.ToFloat64(m["value"]); ok {
			return f, currency
		}
		return nil, currency
	}
	if f, ok := numconv.ToFloat64(v); ok {
		return f, ""
	}
	return nil, ""
}
