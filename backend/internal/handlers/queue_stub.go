package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

// GetQueue GET /api/queue
func GetQueue(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		state.QueueMu.Lock()
		defer state.QueueMu.Unlock()
		// 返回副本，确保为空时序列化为 [] 而不是 null
		cp := make([]interface{}, 0, len(state.Queue))
		for _, it := range state.Queue {
			cp = append(cp, it)
		}
		c.JSON(http.StatusOK, cp)
	}
}

// GetPurchaseHistory GET /api/purchase-history
func GetPurchaseHistory(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		state.HistoryMu.Lock()
		defer state.HistoryMu.Unlock()
		cp := make([]interface{}, 0, len(state.History))
		for _, h := range state.History {
			cp = append(cp, publicPurchaseHistoryEntry(h))
		}
		c.JSON(http.StatusOK, cp)
	}
}

// publicPurchaseHistoryEntry protects the response boundary for legacy rows.
// New writes use stable messages, but old database rows may still contain an
// upstream response body; do not rewrite persisted history just to sanitize it.
func publicPurchaseHistoryEntry(entry types.PurchaseHistoryEntry) types.PurchaseHistoryEntry {
	if entry.ErrorMessage == nil {
		return entry
	}
	message := strings.TrimSpace(*entry.ErrorMessage)
	if isStablePurchaseHistoryMessage(message) {
		safeMessage := message
		entry.ErrorMessage = &safeMessage
		return entry
	}
	fallback := "下单失败，请稍后重试"
	if entry.Status == "uncertain" {
		fallback = "下单结果不确定，请人工核查"
	}
	entry.ErrorMessage = &fallback
	return entry
}

func isStablePurchaseHistoryMessage(message string) bool {
	switch message {
	case "下单已取消",
		"下单阶段失败，请稍后重试",
		"创建购物车成功但响应缺少 cartId",
		"无法从购物车响应中解析 itemId",
		"必需的区域配置无法确定。",
		"无法记录 checkout 防重复保护，请联系管理员",
		"结账失败，请稍后重试",
		"checkout 结果不确定，已停止自动重试并保留购物车供人工核查",
		"checkout 已返回成功但未提供订单号，结果不确定，已停止自动重试并保留购物车供人工核查",
		"应用状态不可用",
		"没有指定下单账户",
		"下单账户不可用，请检查账户配置",
		"下单账户不存在",
		"缺少目标数据中心",
		"VPS 数量不能超过 20",
		"创建购物车失败，请稍后重试",
		"绑定购物车失败，请稍后重试",
		"加购 VPS 配置失败，请稍后重试",
		"加购成功但响应缺少 itemId",
		"读取 VPS 必需配置失败，请稍后重试",
		"结账成功但未返回订单号":
		return true
	default:
		return false
	}
}
