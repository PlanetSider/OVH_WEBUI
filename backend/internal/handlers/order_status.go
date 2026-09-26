package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/purchase"
)

const (
	manualOrderStatusRefreshTimeout = 45 * time.Second
	manualOrderStatusMinInterval    = 15 * time.Second
)

// RefreshPurchaseHistoryStatus POST /api/purchase-history/refresh-status
// 只触发已有成功订单的状态读取；状态与通知仍由 OrderStatusLoop 的原子 owner 写入。
func RefreshPurchaseHistoryStatus(loop *purchase.OrderStatusLoop) gin.HandlerFunc {
	var rateMu sync.Mutex
	var lastCall time.Time
	return func(c *gin.Context) {
		if loop == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"success": false,
				"status":  "error",
				"error":   "订单状态刷新器不可用",
			})
			return
		}
		rateMu.Lock()
		if wait := manualOrderStatusMinInterval - time.Since(lastCall); !lastCall.IsZero() && wait > 0 {
			rateMu.Unlock()
			seconds := int(wait.Seconds()) + 1
			c.JSON(http.StatusTooManyRequests, gin.H{
				"success":           false,
				"status":            "rate_limited",
				"error":             fmt.Sprintf("刷新太频繁，请等待 %d 秒；每条订单都会单独查询 OVH", seconds),
				"retryAfterSeconds": seconds,
			})
			return
		}
		lastCall = time.Now()
		rateMu.Unlock()
		ctx, cancel := context.WithTimeout(c.Request.Context(), manualOrderStatusRefreshTimeout)
		defer cancel()
		result, err := loop.RefreshNow(ctx)
		if errors.Is(err, purchase.ErrOrderStatusRefreshInProgress) {
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"status":  "busy",
				"error":   "订单状态刷新正在进行中，请稍后重试",
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"status":  "error",
				"error":   "订单状态刷新失败",
			})
			return
		}
		status := "success"
		if result.Failed > 0 {
			status = "partial"
		}
		response := gin.H{
			"success":    result.Failed == 0,
			"status":     status,
			"message":    formatOrderStatusRefreshMessage(result),
			"candidates": result.Candidates,
			"selected":   result.Selected,
			"updated":    result.Updated,
			"skipped":    result.Skipped,
			"failed":     result.Failed,
		}
		if len(result.Errors) > 0 {
			response["errors"] = result.Errors
		}
		c.JSON(http.StatusOK, response)
	}
}

func formatOrderStatusRefreshMessage(result purchase.OrderStatusRefreshResult) string {
	return fmt.Sprintf("订单状态刷新完成：更新 %d 条，失败 %d 条，跳过 %d 条", result.Updated, result.Failed, result.Skipped)
}
