package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/weixin"
)

func StartWeixinLogin(manager *weixin.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := manager.StartLogin(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func PollWeixinLogin(manager *weixin.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := manager.PollLogin(c.Request.Context(), c.Param("sessionId"))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func GetWeixinStatus(manager *weixin.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, manager.Status())
	}
}

func TestWeixin(manager *weixin.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := manager.SendTest(c.Request.Context()); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "微信测试通知已发送"})
	}
}

func DisconnectWeixin(manager *weixin.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := manager.Disconnect(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "微信 iLink Bot 已解除绑定"})
	}
}
