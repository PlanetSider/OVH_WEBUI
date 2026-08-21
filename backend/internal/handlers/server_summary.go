package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
)

// GetServerSummary 聚合服务器详情、服务状态、硬件和 IP 数量。
func GetServerSummary(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		serviceName := c.Param("service_name")
		client, err := ovhClientFor(state, c)
		if err != nil {
			noOVHResp(c)
			return
		}
		var info, serviceInfo, hardware map[string]interface{}
		if err := client.Get("/dedicated/server/"+serviceName, &info); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
			return
		}
		_ = client.Get("/dedicated/server/"+serviceName+"/serviceInfos", &serviceInfo)
		_ = client.Get("/dedicated/server/"+serviceName+"/specifications/hardware", &hardware)
		ips := []interface{}{}
		_ = client.Get("/dedicated/server/"+serviceName+"/ips", &ips)
		c.JSON(http.StatusOK, gin.H{"success": true, "serviceName": serviceName, "server": info, "serviceInfo": serviceInfo, "hardware": hardware, "ipCount": len(ips)})
	}
}
