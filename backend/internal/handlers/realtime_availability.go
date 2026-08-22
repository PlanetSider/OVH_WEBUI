package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const realtimeAvailabilityMaxBody = 32 << 20

var realtimeAvailabilityClient = &http.Client{Timeout: 30 * time.Second}

func realtimeAvailabilityURL(region string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", "eu":
		return "https://eu.api.ovh.com/v1/dedicated/server/datacenter/availabilities", true
	case "ca":
		return "https://ca.api.ovh.com/v1/dedicated/server/datacenter/availabilities", true
	default:
		return "", false
	}
}

// GetRealtimeAvailability GET /api/realtime-availability?region=eu|ca
//
// OVH 的可用性端点是公开接口。由后端使用固定白名单代理，避免浏览器跨域，
// 同时不接受任意上游 URL，防止该接口被用作通用代理。
func GetRealtimeAvailability() gin.HandlerFunc {
	return func(c *gin.Context) {
		region := strings.ToLower(strings.TrimSpace(c.DefaultQuery("region", "eu")))
		upstreamURL, ok := realtimeAvailabilityURL(region)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "region must be eu or ca"})
			return
		}
		if region == "" {
			region = "eu"
		}

		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, upstreamURL, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upstream request"})
			return
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "OVH-WebUI-Realtime-Availability")

		resp, err := realtimeAvailabilityClient.Do(req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "OVH availability request failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			detail := strings.TrimSpace(string(body))
			if detail == "" {
				detail = http.StatusText(resp.StatusCode)
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"error": fmt.Sprintf("OVH availability API returned HTTP %d: %s", resp.StatusCode, detail),
			})
			return
		}

		var items []map[string]interface{}
		decoder := json.NewDecoder(io.LimitReader(resp.Body, realtimeAvailabilityMaxBody))
		if err := decoder.Decode(&items); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from OVH availability API"})
			return
		}
		if items == nil {
			items = []map[string]interface{}{}
		}

		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"region":    region,
			"source":    upstreamURL,
			"items":     items,
			"total":     len(items),
			"fetchedAt": time.Now().UTC().Format(time.RFC3339),
		})
	}
}
