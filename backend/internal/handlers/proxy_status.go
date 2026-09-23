package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/types"
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

// ResetAccountProxyGuard 清除单账户代理熔断状态，并恢复本次熔断标记过的队列/自动下单订阅。
// 未被 proxyguard 标记的用户暂停或关闭状态不会被覆盖。
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

// ProxyStatus 返回所有账户的代理健康状态和当前支持的 fingerprint 白名单。
// 它只读取状态，不执行网络请求，也不返回任何代理认证信息。
func ProxyStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		state.AccountsMu.RLock()
		accounts := append([]types.OVHAccount(nil), state.Accounts...)
		state.AccountsMu.RUnlock()
		items := make([]gin.H, 0, len(accounts))
		for _, account := range accounts {
			status, ok := state.ProxyGuardStatus(account.ID)
			item := gin.H{
				"id":          account.ID,
				"name":        account.Name,
				"zone":        strings.ToUpper(account.Zone),
				"usingProxy":  strings.TrimSpace(account.ProxyURL) != "",
				"proxy":       ovh.ScrubProxyURL(account.ProxyURL),
				"fingerprint": account.Fingerprint,
				"tripped":     false,
				"fails":       0,
			}
			if ok {
				item["tripped"] = status.Paused
				item["fails"] = status.ConsecutiveFailures
				if !status.TrippedAt.IsZero() {
					item["trippedAt"] = status.TrippedAt
				}
				if !status.LastFailureAt.IsZero() {
					item["lastFailAt"] = status.LastFailureAt
				}
			}
			items = append(items, item)
		}
		c.JSON(http.StatusOK, gin.H{
			"success":  true,
			"accounts": items,
			"profiles": ovh.FingerprintProfileNames(),
		})
	}
}

// TestAccountProxy 查询账户当前保存配置对应的真实出口 IP。该 client 不挂
// proxyguard reporter，因此诊断失败不会暂停队列或关闭自动下单。
func TestAccountProxy(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		account, ok := state.FindAccount(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "账户不存在"})
			return
		}
		profile, warning := ovh.ResolveFingerprintProfile(account.Fingerprint)
		ip, err := ovh.EgressIP(account.ProxyURL, account.Fingerprint, 15*time.Second)
		if err != nil {
			via := "直连"
			if strings.TrimSpace(account.ProxyURL) != "" {
				via = "代理 " + ovh.ScrubProxyURL(account.ProxyURL)
			}
			c.JSON(http.StatusOK, gin.H{
				"success":     false,
				"error":       ovh.ScrubProxyText(err.Error()),
				"via":         via,
				"usingProxy":  strings.TrimSpace(account.ProxyURL) != "",
				"fingerprint": profile.Name,
			})
			return
		}
		result := gin.H{
			"success":     true,
			"egressIP":    ip,
			"usingProxy":  strings.TrimSpace(account.ProxyURL) != "",
			"proxy":       ovh.ScrubProxyURL(account.ProxyURL),
			"fingerprint": profile.Name,
		}
		if warning != "" {
			result["warning"] = warning
		}
		c.JSON(http.StatusOK, result)
	}
}

// CheckAccountProxy 检查账户到其 OVH endpoint 的出口、连通性和延迟。
// 诊断使用独立 client，不触发 proxyguard，也不执行任何业务写入。
func CheckAccountProxy(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		account, ok := state.FindAccount(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "账户不存在"})
			return
		}
		profile, warning := ovh.ResolveFingerprintProfile(account.Fingerprint)
		region := ovh.EndpointRegion(account.Endpoint)
		base := ovh.APIBaseURLForRegion(region)
		targets := []ovh.ProxyProbeSpec{
			{Name: apiHostOf(base) + "（下单/控制台）", URL: base + "/1.0/auth/time"},
			{Name: apiHostOf(base) + "（库存查询）", URL: base + "/1.0/dedicated/server/datacenter/availabilities?planCode=24sk602"},
		}
		probes := ovh.ProbeProxyTargets(account.ProxyURL, account.Fingerprint, 10*time.Second, targets)
		egress, egressErr := ovh.EgressIP(account.ProxyURL, account.Fingerprint, 15*time.Second)
		result := gin.H{
			"success":     true,
			"accountId":   account.ID,
			"accountName": account.Name,
			"region":      region,
			"usingProxy":  strings.TrimSpace(account.ProxyURL) != "",
			"proxy":       ovh.ScrubProxyURL(account.ProxyURL),
			"fingerprint": profile.Name,
			"targets":     probes,
			"checkedAt":   time.Now().UTC().Format(time.RFC3339),
		}
		if egressErr != nil {
			result["egressError"] = ovh.ScrubProxyText(egressErr.Error())
		} else {
			result["egressIP"] = egress
		}
		if warning != "" {
			result["warning"] = warning
		}
		c.JSON(http.StatusOK, result)
	}
}

func apiHostOf(base string) string {
	value := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if index := strings.IndexByte(value, '/'); index >= 0 {
		value = value[:index]
	}
	return value
}
