package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const maxJSONBodyBytes = 2 << 20

// bindJSONOrBadRequest centralizes malformed JSON handling for side-effect APIs.
func bindJSONOrBadRequest(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBodyBytes)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "请求 JSON 无效",
		})
		return false
	}
	return true
}
