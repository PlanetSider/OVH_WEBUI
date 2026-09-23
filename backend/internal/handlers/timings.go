package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/purchase"
)

// GetQueueTimings GET /api/queue/timings
// 返回每条 plan@datacenter 链路最近一轮已经完成的阶段耗时。
func GetQueueTimings() gin.HandlerFunc {
	return func(c *gin.Context) {
		timings := purchase.LastTimings()
		c.JSON(http.StatusOK, gin.H{"timings": timings})
	}
}
