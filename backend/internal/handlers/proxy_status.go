package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
)

// AccountProxyStatus 返回账户代理的脱敏配置和账户级熔断状态，不返回
// 代理认证信息或 OVH 凭据。
func AccountProxyStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		account, ok := state.FindAccount(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
			return
		}
		status, hasStatus := state.ProxyGuardStatus(id)
		c.JSON(http.StatusOK, gin.H{
			"accountId":       account.ID,
			"proxyConfigured": account.ProxyURL != "",
			"proxyUrl":        redactProxyURL(account.ProxyURL),
			"fingerprint":     account.Fingerprint,
			"guard":           status,
			"hasGuardState":   hasStatus,
		})
	}
}

// ResetAccountProxyGuard 清除单账户代理熔断状态；不修改账户、队列或监控
// 订阅。调用方随后可用 verify 接口进行一次真实健康检查。
func ResetAccountProxyGuard(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if _, ok := state.FindAccount(id); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
			return
		}
		state.ResetAccountProxyGuard(id)
		c.JSON(http.StatusOK, gin.H{"status": "reset"})
	}
}
