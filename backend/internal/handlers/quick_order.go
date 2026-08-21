package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/types"
)

// quickOrderMu 串行化 quick-order 入队的逻辑,避免并发同 plan@dc 重复入队
var quickOrderMu sync.Mutex

func enqueueQuickOrder(state *app.State, accountID, planCode, datacenter string, options []string, fromMonitor, skipDuplicateCheck bool) error {
	quickOrderMu.Lock()
	defer quickOrderMu.Unlock()
	if !(fromMonitor && skipDuplicateCheck) {
		fp := fingerprint(options)
		state.QueueMu.Lock()
		for _, item := range state.Queue {
			if item.PlanCode == planCode && item.Datacenter == datacenter &&
				(item.Status == "running" || item.Status == "pending" || item.Status == "paused") &&
				fingerprint(item.Options) == fp {
				state.QueueMu.Unlock()
				return fmt.Errorf("已存在相同配置的购买任务，稍后再试")
			}
		}
		state.QueueMu.Unlock()

		nowTS := time.Now().Unix()
		state.HistoryMu.Lock()
		for index := len(state.History) - 1; index >= 0; index-- {
			history := state.History[index]
			if history.PlanCode == planCode && history.Datacenter == datacenter && history.Status == "success" && fingerprint(history.Options) == fp {
				if timestamp, err := time.Parse(time.RFC3339Nano, history.PurchaseTime); err == nil && nowTS-timestamp.Unix() < 120 {
					state.HistoryMu.Unlock()
					return fmt.Errorf("刚刚已成功下过同配置订单，稍后再试")
				}
			}
		}
		state.HistoryMu.Unlock()
	}

	now := types.NowISO()
	item := types.QueueItem{
		ID: uuid.NewString(), AccountID: accountID, PlanCode: planCode, Datacenter: datacenter,
		Options: options, Status: "running", RetryCount: 0, MaxRetries: 3, RetryInterval: 2,
		CreatedAt: now, UpdatedAt: now, LastCheckTime: 0, QuickOrder: true, Priority: 100,
	}
	state.QueueMu.Lock()
	state.Queue = append([]types.QueueItem{item}, state.Queue...)
	state.QueueMu.Unlock()
	if err := state.SaveQueue(); err != nil {
		return err
	}
	return nil
}

// QuickOrder POST /api/queue/quick-order
// 监控触发的"立即下单"或者外部主动调用:验证账户 + 拉一次价格 → 直接塞队列头(高优先级 + 2 秒重试)。
// 这条端点是 monitor.batchOrder() auto-order 触发时走的 HTTP 路径。
func QuickOrder(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			AccountID          string   `json:"account_id"` // 必填,哪个账户下单
			PlanCode           string   `json:"planCode"`
			Datacenter         string   `json:"datacenter"`
			Options            []string `json:"options"`
			FromMonitor        bool     `json:"fromMonitor"`
			SkipDuplicateCheck bool     `json:"skipDuplicateCheck"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.PlanCode == "" || body.Datacenter == "" {
			c.JSON(http.StatusOK, gin.H{"success": false, "error": "缺少 planCode 或 datacenter"})
			return
		}
		if body.AccountID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 account_id"})
			return
		}
		if _, ok := state.FindAccount(body.AccountID); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "account_id 不存在"})
			return
		}
		options := body.Options
		if len(options) == 0 {
			availByConfig := catalog.CheckServerAvailabilityWithConfigs(state, body.PlanCode, body.AccountID)
			for _, cfg := range availByConfig {
				if dcStatus, ok := cfg.Datacenters[body.Datacenter]; ok &&
					dcStatus != "unavailable" && dcStatus != "unknown" && len(cfg.Options) > 0 {
					options = append(options, cfg.Options...)
					break
				}
			}
			if len(options) == 0 {
				err := "指定机房无可定价配置（" + body.PlanCode + "@" + body.Datacenter + "）"
				state.Logger.Warn("[quick_order] "+err, "quick_order")
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err})
				return
			}
		}

		priceResult := price.GetInternal(state, body.AccountID, body.PlanCode, body.Datacenter, options)
		if !priceResult.Success {
			err := priceResult.Error
			if err == "" {
				err = "价格查询失败"
			}
			state.Logger.Warn("快速下单前价格校验失败: "+body.PlanCode+"@"+body.Datacenter+" - "+err, "quick_order")
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "价格校验失败：" + err})
			return
		}
		if priceResult.Price == nil {
			state.Logger.Warn("快速下单前价格校验失败: price字段缺失", "quick_order")
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "价格查询返回数据格式异常：缺少price字段"})
			return
		}
		withTaxRaw, _ := priceResult.Price.Prices["withTax"]
		if withTaxRaw == nil {
			state.Logger.Warn("快速下单前价格缺失或无效: "+body.PlanCode+"@"+body.Datacenter, "quick_order")
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "该组合暂无有效价格，暂不支持下单"})
			return
		}
		if f, ok := numconv.ToFloat64(withTaxRaw); ok && f == 0 {
			state.Logger.Warn("快速下单前价格缺失或无效: "+body.PlanCode+"@"+body.Datacenter, "quick_order")
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "该组合暂无有效价格，暂不支持下单"})
			return
		}

		if err := enqueueQuickOrder(state, body.AccountID, body.PlanCode, body.Datacenter, options, body.FromMonitor, body.SkipDuplicateCheck); err != nil {
			state.Logger.Info(err.Error(), "quick_order")
			c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "error": err.Error()})
			return
		}

		state.Logger.Info("快速下单: "+body.PlanCode+" ("+body.Datacenter+") 已加入队列", "quick_order")

		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "✅ " + body.PlanCode + " (" + body.Datacenter + ") 已加入购买队列",
			"price":   priceResult.Price,
			"options": options,
		})
	}
}

// fingerprint 排序后用 "|" 连接的 options 指纹,用于队列去重
func fingerprint(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	uniq := map[string]struct{}{}
	for _, o := range opts {
		s := strings.TrimSpace(o)
		if s != "" {
			uniq[s] = struct{}{}
		}
	}
	list := make([]string, 0, len(uniq))
	for s := range uniq {
		list = append(list, s)
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j-1] > list[j]; j-- {
			list[j-1], list[j] = list[j], list[j-1]
		}
	}
	return strings.Join(list, "|")
}
