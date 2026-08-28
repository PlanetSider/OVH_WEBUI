package purchase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	ovhsdk "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/types"
)

const (
	purchaseSuccessPersistAttempts = 5
	purchaseSuccessPersistDelay    = time.Second
)

// checkoutFailureIsDefinitive 只把能够确认 checkout 被 OVH 拒绝的客户端
// HTTP 错误视为可安全重试。SDK 会把所有非 2xx（包括 5xx）都包装成
// *APIError；5xx、408、409 和非标准 499 都可能出现在请求已到达 OVH、但
// 调用方无法确认最终结果的场景，必须保留 checkout attempt 并人工核对。
func checkoutFailureIsDefinitive(err error) bool {
	code, ok := checkoutAPIErrorCode(err)
	if !ok || code < 400 || code >= 500 {
		return false
	}
	switch code {
	case 408, 409, 499:
		return false
	default:
		return true
	}
}

// go-ovh 当前返回 *APIError，但 APIError 值本身也实现了 error。两种形式
// 都识别，避免上层包装或未来 SDK 调整后把明确的 4xx 错误误判为网络不确定。
func checkoutAPIErrorCode(err error) (int, bool) {
	var apiErr *ovhsdk.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.Code, true
	}
	var apiErrValue ovhsdk.APIError
	if errors.As(err, &apiErrValue) {
		return apiErrValue.Code, true
	}
	return 0, false
}

// PurchaseServer 对应 Python: purchase_server
// 返回是否成功。多账户:用 item.AccountID 取对应 OVH client 和 subsidiary。
func PurchaseServer(ctx context.Context, state *app.State, item *types.QueueItem) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if item == nil || strings.TrimSpace(item.ID) == "" {
		state.Logger.Error("PurchaseServer: 队列任务为空或缺少 ID", "purchase")
		return false
	}
	// 先把空 account_id 解析为当前默认账户，再登记整个购买生命周期。
	// 后续显式使用 resolvedAccountID 取 client，避免默认账户在购物车准备
	// 期间被切换后，流程中途改用另一套凭据。
	resolvedAccountID := strings.TrimSpace(item.AccountID)
	if resolvedAccountID == "" {
		if account, ok := state.FindAccount(""); ok {
			resolvedAccountID = account.ID
		}
	}
	if err := state.BeginAccountPurchase(item.ID, resolvedAccountID); err != nil {
		if errors.Is(err, app.ErrQueueCheckoutInProgress) {
			state.Logger.Info("任务 "+item.ID+" 已有进行中的购买流程，跳过重复启动", "purchase")
		} else {
			state.Logger.Warn("任务 "+item.ID+" 缺少有效账户或身份已失效，安全跳过购买: "+err.Error(), "purchase")
		}
		return false
	}
	defer state.EndAccountPurchase(item.ID)
	// 后续 checkout attempt、购买历史和恢复记录都应持久化本次已经固定的
	// 账户 ID。不要直接修改调用方持有的队列对象，避免与队列读写产生数据竞争。
	if item.AccountID != resolvedAccountID {
		itemCopy := *item
		itemCopy.AccountID = resolvedAccountID
		item = &itemCopy
	}

	client, err := state.OVH.ClientFor(resolvedAccountID)
	if err != nil {
		state.Logger.Error("PurchaseServer: 取 OVH client 失败 ("+resolvedAccountID+"): "+err.Error(), "purchase")
		return false
	}
	// SDK 的 NewRequest 在签名阶段可能先读取 OVH 时间，单纯依赖
	// http.Request context 仍会在取消后发起该准备请求。统一在每个购买
	// 请求前检查 context，避免停机/批次取消后继续创建或修改购物车。
	get := func(path string, result interface{}) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return client.GetWithContext(ctx, path, result)
	}
	post := func(path string, body, result interface{}) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return client.PostWithContext(ctx, path, body, result)
	}
	cartID := ""
	var itemID int64

	state.Logger.Info(fmt.Sprintf("开始为 %s 在 %s 的购买流程，选项: %v",
		item.PlanCode, item.Datacenter, item.Options), "purchase")

	// 检查可用性
	var availabilities []map[string]interface{}
	q := url.Values{}
	q.Set("planCode", item.PlanCode)
	if err := get("/dedicated/server/datacenter/availabilities?"+q.Encode(), &availabilities); err != nil {
		errMsg := err.Error()
		state.Logger.Error(fmt.Sprintf("购买 %s 时发生 OVH API 错误: %s", item.PlanCode, errMsg), "purchase")
		recordFailure(state, item, errMsg)
		return false
	}

	apiDC := ovh.ConvertDisplayDCToAPIDC(item.Datacenter)
	foundAvailable := false
	// 记下"实际可用的那条 FQN"。FQN 格式：<planCode>.<addon1>.<addon2>...
	// 用户没显式指定 options 时，会从这个 FQN 推断 addon，让订单走"有货的那套配置"，
	// 不再退化到 OVH 默认 addon（多半是 HDD / 最小内存）。
	var availableFQN string
	wantedOptions := canonicalOptions(item.Options)
	for _, av := range availabilities {
		fqn, _ := av["fqn"].(string)
		if len(wantedOptions) > 0 && !fqnContainsOptions(fqn, wantedOptions) {
			continue
		}
		if dcsRaw, ok := av["datacenters"].([]interface{}); ok {
			for _, dcRaw := range dcsRaw {
				dc, ok := dcRaw.(map[string]interface{})
				if !ok {
					continue
				}
				dcName, _ := dc["datacenter"].(string)
				availStr, _ := dc["availability"].(string)
				availStr = strings.ToLower(strings.TrimSpace(availStr))
				if strings.EqualFold(strings.TrimSpace(dcName), strings.TrimSpace(apiDC)) &&
					catalog.AvailabilityExplicitlyAvailable(availStr) {
					foundAvailable = true
					availableFQN = fqn
					break
				}
			}
		}
		if foundAvailable {
			break
		}
	}
	if !foundAvailable {
		state.Logger.Info(fmt.Sprintf("服务器 %s 在数据中心 %s 当前无货", item.PlanCode, item.Datacenter), "purchase")
		return false
	}

	// 决定本次下单使用的硬件 options：
	// - 用户显式指定了 options → 直接用（fail-fast 由后面的 eco/options 处理）
	// - 用户没指定 → 从可用 FQN 推断 addon planCode，确保订单走"实际有货的那套配置"
	effectiveOptions := append([]string{}, item.Options...)
	if len(effectiveOptions) == 0 && availableFQN != "" {
		parts := strings.Split(availableFQN, ".")
		if len(parts) > 1 {
			effectiveOptions = parts[1:] // 第一段是 base planCode，其余是 addon planCodes
			state.Logger.Info(fmt.Sprintf("用户未指定硬件选项，从可用 FQN %s 推断 addon: %v",
				availableFQN, effectiveOptions), "purchase")
		}
	}

	// 多账户:购物车 subsidiary 跟着账户走,不再读全局 cfg
	acc, _ := state.FindAccount(resolvedAccountID)
	subsidiary := acc.Zone
	if subsidiary == "" {
		subsidiary = "IE"
	}

	// 创建购物车
	state.Logger.Info(fmt.Sprintf("为区域 %s 创建购物车 (账户 %s)", subsidiary, acc.Name), "purchase")
	var cartResult map[string]interface{}
	if err := post("/order/cart", map[string]interface{}{
		"ovhSubsidiary": subsidiary,
	}, &cartResult); err != nil {
		state.Logger.Error(fmt.Sprintf("购买 %s 时发生 OVH API 错误: %s", item.PlanCode, err.Error()), "purchase")
		recordFailure(state, item, err.Error())
		return false
	}
	cartID, _ = cartResult["cartId"].(string)
	if strings.TrimSpace(cartID) == "" {
		errMsg := fmt.Sprintf("创建购物车成功但响应缺少 cartId（响应: %v）", cartResult)
		state.Logger.Error(errMsg, "purchase")
		recordFailure(state, item, errMsg)
		return false
	}
	state.Logger.Info("购物车创建成功，ID: "+cartID, "purchase")

	// 抢购失败时清理 OVH 购物车,避免 OVH 侧堆积僵尸 cart(高频抢购累计能上千个,
	// 进而触发 OVH 限流)。checkout 成功时 cart 自动转 order,Delete 会 404,
	// 所以只在 !success 时尝试,且失败不影响主流程。
	success := false
	preserveCart := false
	defer func() {
		if success || preserveCart || cartID == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.DeleteWithContext(cleanupCtx, "/order/cart/"+cartID, nil); err != nil {
			state.Logger.Debug(fmt.Sprintf("清理失败 cart %s: %s", cartID, err.Error()), "purchase")
		} else {
			state.Logger.Debug("已清理失败 cart "+cartID, "purchase")
		}
	}()

	// 立即绑定购物车到账户 —— 对齐 OVH 官方 PHP / Python 示例的推荐顺序：
	// cart → assign → eco → configuration → options → summary → checkout。
	// 在 add item 之前 assign，OVH 后端不会出现"cart 未绑定就 checkout"的边界错误。
	state.Logger.Info("绑定购物车 "+cartID, "purchase")
	if err := post("/order/cart/"+cartID+"/assign", map[string]interface{}{}, nil); err != nil {
		errMsg := err.Error()
		state.Logger.Error(fmt.Sprintf("购买 %s 时发生 OVH API 错误: %s", item.PlanCode, errMsg), "purchase")
		state.Logger.Error("错误发生时的购物车ID: "+cartID, "purchase")
		recordFailure(state, item, errMsg)
		return false
	}
	state.Logger.Info("购物车绑定成功", "purchase")

	// 添加基础商品 /eco
	state.Logger.Info(fmt.Sprintf("添加基础商品 %s 到购物车 (使用 /eco)", item.PlanCode), "purchase")
	var itemResult map[string]interface{}
	if err := post("/order/cart/"+cartID+"/eco", map[string]interface{}{
		"planCode":    item.PlanCode,
		"pricingMode": "default",
		"duration":    "P1M",
		"quantity":    1,
	}, &itemResult); err != nil {
		state.Logger.Error(fmt.Sprintf("购买 %s 时发生 OVH API 错误: %s", item.PlanCode, err.Error()), "purchase")
		state.Logger.Error(fmt.Sprintf("错误发生时的购物车ID: %s", cartID), "purchase")
		recordFailure(state, item, err.Error())
		return false
	}
	if n, ok := numconv.ToInt64(itemResult["itemId"]); ok {
		itemID = n
	}
	if itemID == 0 {
		errMsg := fmt.Sprintf("无法从购物车响应中解析 itemId（响应: %v）", itemResult)
		state.Logger.Error(fmt.Sprintf("购买 %s 时发生未知错误: %s", item.PlanCode, errMsg), "purchase")
		state.Logger.Error("错误发生时的购物车ID: "+cartID, "purchase")
		recordFailure(state, item, errMsg)
		return false
	}
	state.Logger.Info(fmt.Sprintf("基础商品添加成功，项目 ID: %d", itemID), "purchase")

	// 设置必需配置
	state.Logger.Info(fmt.Sprintf("为项目 %d 设置必需配置", itemID), "purchase")
	region := ovh.RegionForDC(apiDC)

	// 与 Python 一致的顺序：dedicated_datacenter → dedicated_os → (region)
	type kv struct{ label, value string }
	configurations := []kv{
		{"dedicated_datacenter", apiDC},
		{"dedicated_os", "none_64.en"},
	}
	if region != "" {
		configurations = append(configurations, kv{"region", region})
	} else {
		state.Logger.Warn(fmt.Sprintf("无法为数据中心 %s 推断区域，可能导致配置失败", strings.ToLower(apiDC)), "purchase")
		// 对应 Python: 查 requiredConfiguration 看 region 是否必填
		var required []map[string]interface{}
		if err := get(fmt.Sprintf("/order/cart/%s/item/%d/requiredConfiguration", cartID, itemID), &required); err != nil {
			state.Logger.Warn(fmt.Sprintf("获取必需配置失败或区域为必需但未确定: %s", err.Error()), "purchase")
		} else {
			for _, conf := range required {
				if label, _ := conf["label"].(string); label == "region" {
					if req, _ := conf["required"].(bool); req {
						errMsg := "必需的区域配置无法确定。"
						state.Logger.Error(fmt.Sprintf("购买 %s 时发生未知错误: %s", item.PlanCode, errMsg), "purchase")
						recordFailure(state, item, errMsg)
						return false
					}
				}
			}
		}
	}
	// configuration 写入必须按顺序完成。购物车是共享可变状态，
	// 并发 POST 可能乱序或相互覆盖，导致最终配置与日志不一致。
	postConfig := func(label, value string) error {
		state.Logger.Info(fmt.Sprintf("配置项目 %d: 设置必需项 %s = %s", itemID, label, value), "purchase")
		if err := post(fmt.Sprintf("/order/cart/%s/item/%d/configuration", cartID, itemID),
			map[string]interface{}{"label": label, "value": value}, nil); err != nil {
			return err
		}
		state.Logger.Info(fmt.Sprintf("成功设置必需项: %s = %s", label, value), "purchase")
		return nil
	}
	for _, c := range configurations {
		if err := postConfig(c.label, c.value); err != nil {
			errMsg := err.Error()
			state.Logger.Error(fmt.Sprintf("购买 %s 时发生 OVH API 错误(%s): %s", item.PlanCode, c.label, errMsg), "purchase")
		state.Logger.Error(fmt.Sprintf("错误发生时的购物车ID: %s", cartID), "purchase")
		state.Logger.Error(fmt.Sprintf("错误发生时的基础商品ID: %d", itemID), "purchase")
		recordFailure(state, item, errMsg)
		return false
		}
	}

	// 硬件选项处理。effectiveOptions 已经包含了：
	//   - 用户显式 options（如果有），或
	//   - 从可用 FQN 推断的 addon planCode（用户没指定时）
	if len(effectiveOptions) > 0 {
		state.Logger.Info(fmt.Sprintf("📦 处理硬件选项（%d个）: %v", len(effectiveOptions), effectiveOptions), "purchase")
		filtered := []string{}
		for _, opt := range effectiveOptions {
			if opt == "" {
				continue
			}
			lc := strings.ToLower(opt)
			skip := false
			// 过滤掉非硬件 / 许可证类（注意 "panel" 不在过滤词里：FQN 推断的 addon
			// 不会撞这词，删了避免误伤；旧版有 "panel" 是因为前端可能塞 cpanel 选项过来）
			for _, term := range []string{"windows-server", "sql-server", "cpanel-license", "plesk-",
				"-license-", "os-", "control-panel", "license", "security"} {
				if strings.Contains(lc, term) {
					skip = true
					break
				}
			}
			if skip {
				state.Logger.Info("跳过非硬件/许可证选项: "+opt, "purchase")
				continue
			}
			filtered = append(filtered, opt)
		}
		if len(filtered) > 0 {
			state.Logger.Info(fmt.Sprintf("过滤后的硬件选项计划代码: %v", filtered), "purchase")
			var availableEcoOpts []map[string]interface{}
			q := url.Values{}
			q.Set("planCode", item.PlanCode)
			if err := get(fmt.Sprintf("/order/cart/%s/eco/options?%s", cartID, q.Encode()), &availableEcoOpts); err != nil {
				// 拉 eco/options 失败 → 中止订单。否则会用基础 plan 默认存储（多半是 HDD）下到错误配置
				errMsg := fmt.Sprintf("获取 Eco 硬件选项列表失败: %s（用户指定了 %d 个选项，无法验证，已取消下单避免下到错误配置）", err.Error(), len(filtered))
				state.Logger.Error(errMsg, "purchase")
				recordFailure(state, item, errMsg)
				return false
			}
			state.Logger.Info(fmt.Sprintf("找到 %d 个可用的 Eco 硬件选项。", len(availableEcoOpts)), "purchase")

			// 先全部匹配,失败直接 fail-fast(避免任何 POST 都发出去之前先卡 missing)
			type addonPayload struct {
				planCode string
				body     map[string]interface{}
			}
			var todo []addonPayload
			var missing []string
			for _, wanted := range filtered {
				matched := false
				for _, avail := range availableEcoOpts {
					availPC, _ := avail["planCode"].(string)
					if availPC != wanted {
						continue
					}
					duration := "P1M"
					if d, ok := avail["duration"].(string); ok && d != "" {
						duration = d
					}
					pricingMode := "default"
					if pm, ok := avail["pricingMode"].(string); ok && pm != "" {
						pricingMode = pm
					}
					todo = append(todo, addonPayload{
						planCode: availPC,
						body: map[string]interface{}{
							"itemId":      itemID,
							"planCode":    availPC,
							"duration":    duration,
							"pricingMode": pricingMode,
							"quantity":    1,
						},
					})
					matched = true
					break
				}
				if !matched {
					missing = append(missing, wanted)
				}
			}
			if len(missing) > 0 {
				errMsg := fmt.Sprintf("用户请求的硬件选项 %v 未在 OVH 可用 Eco 选项中找到（已取消下单避免下到错误配置）", missing)
				state.Logger.Error(errMsg, "purchase")
				recordFailure(state, item, errMsg)
				return false
			}

			// addon 同样按匹配顺序串行添加。每次 POST 都会修改同一购物车，
			// 任一步失败立即停止，绝不带着不完整硬件配置 checkout。
			state.Logger.Info(fmt.Sprintf("串行添加 %d 个 Eco 选项: %v", len(todo), filtered), "purchase")
			for _, t := range todo {
				if err := post(fmt.Sprintf("/order/cart/%s/eco/options", cartID), t.body, nil); err != nil {
					state.Logger.Error(fmt.Sprintf("添加 Eco 选项 %s 失败: %s", t.planCode, err.Error()), "purchase")
					errMsg := fmt.Sprintf("添加 Eco 选项 %s 失败: %s（已取消下单避免下到错误配置）", t.planCode, err.Error())
					recordFailure(state, item, errMsg)
					return false
				}
				state.Logger.Info(fmt.Sprintf("成功添加 Eco 选项: %s", t.planCode), "purchase")
			}
			state.Logger.Info(fmt.Sprintf("共成功添加 %d 个硬件选项。", len(filtered)), "purchase")
		}
	} else {
		state.Logger.Info("⚠️ 用户未提供任何硬件选项，将使用默认配置下单", "purchase")
	}

	// 结账前最后确认请求未取消，且任务仍存在并处于 running。
	// 购物车准备期间用户可能删除、清空或暂停任务，这些操作都必须阻止 checkout。
	if ctx.Err() != nil {
		state.Logger.Info("任务 "+item.ID+" 已在结账前取消，取消 checkout", "purchase")
		return false
	}
	if !state.IsQueueItemRunning(item.ID) {
		state.Logger.Info("任务 "+item.ID+" 已不存在或不再运行，取消 checkout", "purchase")
		return false
	}

	// 直接结账 —— 跳过 /summary(它只是日志用的价格,2 秒开销),
	// 价格 + 过期时间下面 checkout 成功后用 /me/order 异步补,不阻塞主流程。
	state.Logger.Info("对购物车 "+cartID+" 执行结账", "purchase")
	var checkoutResult map[string]interface{}
	checkoutPayload := map[string]interface{}{
		"autoPayWithPreferredPaymentMethod": false,
		"waiveRetractationPeriod":           true,
	}
	// 在真正 checkout 前，以当前队列快照原子登记防重复记录并发布 checkout
	// 闸门。闸门不持有互斥锁跨越网络请求，而是通过 checkoutTasks 让普通队列
	// 变更返回冲突；这样既封闭最终检查后的竞态，也允许异常结果安全隔离任务。
	if err := state.BeginCheckoutAttempt(*item, cartID); err != nil {
		if errors.Is(err, app.ErrQueueItemChanged) || errors.Is(err, app.ErrQueueCheckoutInProgress) {
			state.Logger.Info("任务 "+item.ID+" 在最终结账检查时已取消或发生变更，取消 checkout", "purchase")
			return false
		}
		if errors.Is(err, app.ErrCheckoutAttemptExists) {
			// 数据库已有同一任务的恢复记录，说明此前 checkout 的结果可能已被
			// OVH 接收。即使当前队列仍残留，也只能隔离并提示人工核对，不能
			// 继续创建新购物车或重复下单。
			state.Logger.Warn("任务已有 checkout 恢复记录，停止自动重试并隔离等待人工核对: "+item.ID, "purchase")
			if quarantineErr := state.QuarantineQueueItem(item.ID); quarantineErr != nil {
				state.Logger.Error("隔离已有 checkout 恢复记录的任务失败: "+quarantineErr.Error(), "purchase")
			}
			return false
		}
		errMsg := fmt.Sprintf("无法记录 checkout 防重复保护: %s", err)
		state.Logger.Error(errMsg, "purchase")
		recordFailure(state, item, errMsg)
		return false
	}
	defer state.EndCheckoutAttempt(item.ID)
	// checkout 闸门已登记后若上下文被取消，checkout 尚未发出，可安全清理 attempt。
	if ctx.Err() != nil {
		if removeErr := state.CancelCheckoutAttemptBeforeRequest(item.ID); removeErr != nil {
			state.Logger.Warn("取消 checkout 时清理防重复记录失败: "+removeErr.Error(), "purchase")
		}
		state.Logger.Info("任务 "+item.ID+" 在最终结账检查时已取消，取消 checkout", "purchase")
		return false
	}
	if err := client.PostWithContext(ctx, "/order/cart/"+cartID+"/checkout", checkoutPayload, &checkoutResult); err != nil {
		errMsg := err.Error()
		state.Logger.Error(fmt.Sprintf("购买 %s 时发生 OVH API 错误: %s", item.PlanCode, errMsg), "purchase")
		// 只有 OVH 明确拒绝 checkout 的客户端错误，才能删除 attempt 并允许
		// 重试。5xx/超时/冲突/连接中断都可能发生在请求已被接收之后，结果
		// 不确定时宁可隔离等待人工核查，也不能冒险重复创建订单。
		definitiveFailure := checkoutFailureIsDefinitive(err)
		if definitiveFailure {
			if removeErr := state.FinishCheckoutHTTPError(item.ID); removeErr != nil {
				state.Logger.Warn("checkout 明确失败，但清理防重复记录失败: "+removeErr.Error(), "purchase")
			}
		} else {
			preserveCart = true
			if quarantineErr := state.QuarantineQueueItemDuringCheckout(item.ID); quarantineErr != nil {
				state.Logger.Error("checkout 结果不确定，隔离队列任务落盘失败（本进程仍已阻止重试）: "+quarantineErr.Error(), "purchase")
			}
			errMsg = "checkout 结果不确定，已停止自动重试并保留购物车供人工核查: " + errMsg
			state.Logger.Warn(errMsg+"（任务 "+item.ID+"，购物车 "+cartID+"）", "purchase")
		}
		if definitiveFailure {
			recordFailure(state, item, errMsg)
		} else {
			recordUncertain(state, item, errMsg)
		}
		return false
	}

	orderID := numconv.ToString(checkoutResult["orderId"])
	orderURL, _ := checkoutResult["url"].(string)
	if strings.TrimSpace(orderID) == "" {
		// 2xx 并不等价于已拿到可核验订单。没有 orderId 时不能删除
		// checkout_attempt，也不能让任务自动重试，否则可能重复下单。
		preserveCart = true
		if quarantineErr := state.QuarantineQueueItemDuringCheckout(item.ID); quarantineErr != nil {
			state.Logger.Error("checkout 未返回订单号，隔离队列任务失败（本进程仍会保留 attempt）: "+quarantineErr.Error(), "purchase")
		}
		errMsg := "checkout 已返回成功但未提供订单号，结果不确定，已停止自动重试并保留购物车供人工核查"
		state.Logger.Warn(errMsg+"（任务 "+item.ID+"，购物车 "+cartID+"）", "purchase")
		recordUncertain(state, item, errMsg)
		return false
	}

	// checkout 已返回订单 ID,cart 已成功转 order,标记成功阻止 defer 删除
	success = true
	if err := state.DB.CompleteCheckoutAttempt(item.ID, orderID, orderURL); err != nil {
		state.Logger.Warn("checkout 成功但无法更新订单恢复记录，尝试安全补写: "+err.Error(), "purchase")
		if ensureErr := state.DB.EnsureCheckoutAttemptCompleted(*item, cartID, orderID, orderURL); ensureErr != nil {
			// 后续成功事务仍会尝试原子写历史并删除队列。若它也失败，
			// 进程内隔离标记会阻止自动重试；这里输出最高优先级日志，
			// 提醒人工按订单号核对恢复记录异常。
			state.Logger.Error("checkout 成功但无法补写订单恢复记录: "+ensureErr.Error()+"（订单 "+orderID+"）", "purchase")
		}
	}

	// checkout 已发生，尝试以事务同时写成功历史、删队列并创建成功通知 outbox。首次调用即会
	// 从内存队列移除任务，后续有限重试只补 SQLite；持续失败时依赖
	// checkout_attempts 在重启后恢复，不能无限阻塞整个队列批次。
	persisted, persistErr := recordSuccess(ctx, state, item, orderID, orderURL, "", nil)
	if errors.Is(persistErr, db.ErrPurchaseOrderConflict) {
		// 同一 task_id 已经有另一个成功订单：不能覆盖原历史，也不能
		// 把本次 checkout 当作普通失败重新入队。recordSuccess 已经尝试
		// 将任务从数据库队列隔离；checkout attempt 则必须保留供人工核对。
		state.Logger.Warn("检测到同一任务对应多个成功订单，已隔离任务并保留 checkout 记录，需人工核对: "+item.ID, "purchase")
		return false
	}

	// 异步补:从 /me/order/{orderID} 读 expirationDate + 价格,写回 history
	if orderID != "" {
		go backfillOrderDetail(state, client, item.ID, orderID)
	}

	state.Logger.Info(fmt.Sprintf("成功购买 %s 在 %s (订单ID: %s, URL: %s)",
		item.PlanCode, item.Datacenter, orderID, orderURL), "purchase")

	if !persisted {
		state.Logger.Warn("订单已由 OVH 创建，但本地成功状态尚未落盘；系统已阻止该任务自动重试，重启后将从 checkout 记录恢复。", "purchase")
	}
	monitor.FlushNotificationOutbox(state)
	return true
}

func extract(v interface{}) *float64 {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]interface{}); ok {
		if f, ok := numconv.ToFloat64(m["value"]); ok {
			return &f
		}
		return nil
	}
	if f, ok := numconv.ToFloat64(v); ok {
		return &f
	}
	return nil
}

func canonicalOptions(options []string) []string {
	seen := make(map[string]struct{}, len(options))
	out := make([]string, 0, len(options))
	for _, option := range options {
		option = strings.ToLower(strings.TrimSpace(option))
		if option == "" {
			continue
		}
		if _, ok := seen[option]; ok {
			continue
		}
		seen[option] = struct{}{}
		out = append(out, option)
	}
	return out
}

// fqnContainsOptions 按 FQN 的点分段匹配用户指定的 addon，避免只按机房
// 找到另一套库存配置后继续下单。
func fqnContainsOptions(fqn string, options []string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(fqn)), ".")
	if len(parts) < 2 {
		return false
	}
	segments := make(map[string]struct{}, len(parts)-1)
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part != "" {
			segments[part] = struct{}{}
		}
	}
	for _, option := range options {
		if _, ok := segments[option]; !ok {
			return false
		}
	}
	return true
}

func recordSuccess(ctx context.Context, state *app.State, item *types.QueueItem, orderID, orderURL, expirationTime string, priceInfo *types.PriceInfo) (bool, error) {
	now := types.NowISO()
	entry := types.PurchaseHistoryEntry{
		ID:           uuid.NewString(),
		TaskID:       item.ID,
		AccountID:    item.AccountID,
		PlanCode:     item.PlanCode,
		Datacenter:   item.Datacenter,
		Options:      item.Options,
		Status:       "success",
		OrderID:      orderID,
		OrderURL:     orderURL,
		PurchaseTime: now,
		AttemptCount: item.RetryCount,
	}
	if expirationTime != "" {
		entry.ExpirationTime = expirationTime
	}
	if priceInfo != nil {
		entry.Price = priceInfo
	}
	notification, notificationErr := monitor.NewPurchaseSuccessNotification(*item, orderID, orderURL, monitor.NotificationTargetChannels(state))
	if notificationErr != nil {
		// 订单成功状态的原子落盘优先级高于通知。通知载荷构造失败时记录
		// 明确错误，但不能因此让已成功的 checkout 留在可重试队列。
		state.Logger.Error("构造抢购成功通知失败，将只保存订单成功状态: "+notificationErr.Error(), "purchase")
		notification = nil
	}
	for attempt := 1; attempt <= purchaseSuccessPersistAttempts; attempt++ {
		if err := state.CommitPurchaseSuccessDuringCheckoutWithNotification(entry, notification); err == nil {
			state.Logger.Info("已原子保存成功历史并移除队列任务: "+item.ID, "purchase")
			return true, nil
		} else {
			if errors.Is(err, db.ErrPurchaseOrderConflict) {
				// 冲突不是可重试的数据库瞬时错误。原成功历史必须保留，
				// 新 checkout attempt 也不能删除；仅把残留队列任务隔离。
				if quarantineErr := state.QuarantineQueueItemDuringCheckout(item.ID); quarantineErr != nil {
					state.Logger.Error("订单冲突后隔离队列任务失败（checkout 记录仍保留）: "+quarantineErr.Error(), "purchase")
				}
				return false, err
			}
			state.Logger.Error(fmt.Sprintf("订单已创建，但持久化成功状态失败（%d/%d）: %s",
				attempt, purchaseSuccessPersistAttempts, err), "purchase")
			if attempt == purchaseSuccessPersistAttempts {
				break
			}
			select {
			case <-ctx.Done():
				state.Logger.Warn("停机期间停止重试成功订单事务，将由 checkout 启动恢复处理: "+item.ID, "purchase")
				return false, err
			case <-time.After(purchaseSuccessPersistDelay):
			}
		}
	}
	state.Logger.Error("订单已创建，但本地成功状态在有限重试后仍未落盘；任务已从内存队列隔离，将由 checkout 启动恢复处理: "+item.ID, "purchase")
	return false, fmt.Errorf("保存成功订单状态失败: task %s", item.ID)
}

func recordFailure(state *app.State, item *types.QueueItem, errMsg string) {
	recordHistoryStatus(state, item, "failed", errMsg)
}

// recordUncertain 记录 checkout 已经发出、但无法确认 OVH 是否创建订单的结果。
// 这类记录不能计入普通失败，否则用户可能误以为可以安全重试并造成重复下单。
func recordUncertain(state *app.State, item *types.QueueItem, errMsg string) {
	recordHistoryStatus(state, item, "uncertain", errMsg)
}

func recordHistoryStatus(state *app.State, item *types.QueueItem, status, errMsg string) {
	now := types.NowISO()
	em := errMsg
	entry := types.PurchaseHistoryEntry{
		ID:           uuid.NewString(),
		TaskID:       item.ID,
		AccountID:    item.AccountID,
		PlanCode:     item.PlanCode,
		Datacenter:   item.Datacenter,
		Options:      item.Options,
		Status:       status,
		ErrorMessage: &em,
		PurchaseTime: now,
		AttemptCount: item.RetryCount,
	}
	if err := state.MutateHistory(func(history []types.PurchaseHistoryEntry) ([]types.PurchaseHistoryEntry, error) {
		for i := range history {
			if history[i].TaskID == item.ID {
				entry.ID = history[i].ID
				history[i] = entry
				return history, nil
			}
		}
		return append(history, entry), nil
	}); err != nil {
		state.Logger.Error("保存抢购历史状态失败: "+err.Error(), "purchase")
	}
}

// backfillOrderDetail 下单成功后异步补 history 行的 expirationTime + price。
// 不阻塞 PurchaseServer 主流程,即便这一步失败 history 也已经标 success(只是少了价格 / 过期时间)。
// 在独立 goroutine 跑,持有 OVH client 引用,只 read /me/order/{orderID}。
func backfillOrderDetail(state *app.State, client *ovhsdk.Client, taskID, orderID string) {
	// 订单详情是辅助信息，不应让进程停机时的后台 goroutine 无限等待。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var orderInfo map[string]interface{}
	if err := client.GetWithContext(ctx, "/me/order/"+orderID, &orderInfo); err != nil {
		state.Logger.Warn(fmt.Sprintf("异步查询订单 %s 详情失败: %s", orderID, err.Error()), "purchase")
		return
	}

	expirationTime := ""
	if ret, ok := orderInfo["retractionDate"].(string); ok && ret != "" {
		expirationTime = ret
	} else if exp, ok := orderInfo["expirationDate"].(string); ok {
		expirationTime = exp
	}

	// /me/order 返回的价格字段:priceWithTax / priceWithoutTax / tax,
	// 每个典型形式 { value: <number-or-json.Number>, currencyCode: string, text: string }。
	// 复用 extract(),它能容 float64 / json.Number / string / int 各种来法。
	pickCurrency := func(field interface{}) string {
		m, ok := field.(map[string]interface{})
		if !ok {
			return ""
		}
		c, _ := m["currencyCode"].(string)
		return c
	}

	var priceInfo *types.PriceInfo
	withTax := extract(orderInfo["priceWithTax"])
	withoutTax := extract(orderInfo["priceWithoutTax"])
	tax := extract(orderInfo["tax"])
	currency := pickCurrency(orderInfo["priceWithTax"])
	if currency == "" {
		currency = pickCurrency(orderInfo["priceWithoutTax"])
	}
	if currency == "" {
		currency = "EUR"
	}
	if withTax != nil || withoutTax != nil {
		priceInfo = &types.PriceInfo{
			WithTax:      withTax,
			WithoutTax:   withoutTax,
			Tax:          tax,
			CurrencyCode: currency,
		}
	}

	if expirationTime == "" && priceInfo == nil {
		return
	}

	changed := false
	err := state.MutateHistory(func(history []types.PurchaseHistoryEntry) ([]types.PurchaseHistoryEntry, error) {
		for i := range history {
			if history[i].TaskID != taskID {
				continue
			}
			if expirationTime != "" && history[i].ExpirationTime != expirationTime {
				history[i].ExpirationTime = expirationTime
				changed = true
			}
			if priceInfo != nil && history[i].Price == nil {
				history[i].Price = priceInfo
				changed = true
			}
			return history, nil
		}
		return history, nil
	})
	if err != nil {
		state.Logger.Warn("补全订单 "+orderID+" 详情落盘失败: "+err.Error(), "purchase")
	} else if changed {
		state.Logger.Info(fmt.Sprintf("补全订单 %s 详情: 过期时间=%q 价格=%v",
			orderID, expirationTime, priceInfo != nil), "purchase")
	}
}
