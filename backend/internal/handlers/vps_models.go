package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/vps"
)

// GetVPSModels GET /api/vps-monitor/models?subsidiary=IE&accountId=...
func GetVPSModels(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		subsidiary := strings.TrimSpace(c.Query("subsidiary"))
		if subsidiary == "" {
			subsidiary = vps.DefaultSubsidiary(state, c.Query("accountId"))
		}
		models, err := vps.Models(state, subsidiary)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"status":  "error",
				"message": "获取 VPS 型号失败",
				"models":  []vps.Model{},
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"subsidiary": vps.NormalizeSubsidiary(subsidiary), "models": models})
	}
}
