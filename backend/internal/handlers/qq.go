package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
)

// TestQQ sends a configuration test to all configured QQ targets. AppSecret and
// access tokens are intentionally never included in the response.
func TestQQ(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if state == nil || state.QQ == nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "QQ Bot 尚未配置"})
			return
		}
		tester, ok := state.QQ.(interface {
			SendTest(context.Context) error
		})
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "QQ 测试能力不可用"})
			return
		}
		if err := tester.SendTest(c.Request.Context()); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "QQ 测试通知已发送"})
	}
}
