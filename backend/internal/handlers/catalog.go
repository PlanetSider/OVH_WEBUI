package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/ovh"
)

const (
	catalogTTL          = 2 * time.Hour
	maxCatalogBodyBytes = 16 << 20
)

// GetCatalog GET /api/catalog?subsidiary=IE[&forceRefresh=true]
// 返回 OVH 公开 eco catalog 的原始 JSON。优先走 SQLite 缓存（2 小时 TTL），
// 缓存过期或带 forceRefresh=true 时才直连 OVH。
func GetCatalog(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		sub := strings.ToUpper(strings.TrimSpace(c.Query("subsidiary")))
		if sub == "" {
			// 多账户:落到默认账户的 zone,不读 state.Config
			acc, _ := state.FindAccount("")
			sub = strings.ToUpper(acc.Zone)
			if sub == "" {
				sub = "IE"
			}
		}
		force := strings.EqualFold(c.Query("forceRefresh"), "true")

		// 1. 命中 SQLite 缓存且未过期 → 直接返回
		if !force {
			raw, ts, ok, err := state.DB.GetCatalog(sub)
			if err == nil && ok {
				age := time.Since(time.UnixMilli(ts))
				if age < catalogTTL {
					c.Header("X-Cache-Age-Seconds", strconv.FormatInt(int64(age.Seconds()), 10))
					c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(raw))
					return
				}
			}
		}

		// 2. 直连 OVH 拉新数据
		baseURL := ovh.CatalogBaseURLForSubsidiary(sub)
		url := fmt.Sprintf("%s/v1/order/catalog/public/eco?ovhSubsidiary=%s", baseURL, sub)
		client, err := state.OVH.SharedHTTPClient(30 * time.Second)
		if err != nil {
			if raw, _, ok, _ := state.DB.GetCatalog(sub); ok {
				c.Header("X-Cache-Warning", "stale (shared public proxy unavailable)")
				c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(raw))
				return
			}
			state.Logger.Error("catalog 公共代理不可用 "+sub, "catalog")
			c.JSON(http.StatusBadGateway, gin.H{"error": "目录服务暂不可用"})
			return
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			// 拉失败时回退到 stale 缓存（如果有），比直接给 500 强
			if raw, _, ok, _ := state.DB.GetCatalog(sub); ok {
				c.Header("X-Cache-Warning", "stale (upstream fetch failed)")
				c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(raw))
				return
			}
			state.Logger.Error("catalog 拉取失败 "+sub, "catalog")
			c.JSON(http.StatusBadGateway, gin.H{"error": "目录服务暂不可用"})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBodyBytes+1))
		if err != nil {
			state.Logger.Error("catalog 读取响应失败 "+sub, "catalog")
			c.JSON(http.StatusBadGateway, gin.H{"error": "目录服务响应不可用"})
			return
		}
		if len(body) > maxCatalogBodyBytes {
			state.Logger.Error("catalog 响应超过大小限制 "+sub, "catalog")
			c.JSON(http.StatusBadGateway, gin.H{"error": "目录服务响应过大"})
			return
		}
		if resp.StatusCode != http.StatusOK {
			state.Logger.Error(fmt.Sprintf("catalog 上游 HTTP %d: %s", resp.StatusCode, sub), "catalog")
			c.JSON(resp.StatusCode, gin.H{"error": fmt.Sprintf("upstream returned %d", resp.StatusCode)})
			return
		}

		// 3. 落 SQLite + 回写响应
		if err := state.DB.UpsertCatalog(sub, string(body)); err != nil {
			state.Logger.Warn("catalog 写库失败 "+sub, "catalog")
		} else {
			state.Logger.Info(fmt.Sprintf("catalog %s 已缓存 (%d KB)", sub, len(body)/1024), "catalog")
		}
		c.Header("X-Cache-Age-Seconds", "0")
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
	}
}
