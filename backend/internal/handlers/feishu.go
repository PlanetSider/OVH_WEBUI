package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/numconv"
	"github.com/ovh-webui/server/internal/price"
	"github.com/ovh-webui/server/internal/types"
)

func feishuJSONBody(c *gin.Context) ([]byte, map[string]interface{}, error) {
	raw, err := c.GetRawData()
	if err != nil { return nil, nil, err }
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil { return raw, nil, err }
	return raw, body, nil
}

func feishuHeaderValues(body map[string]interface{}) (string, string) {
	token, _ := body["token"].(string)
	appID, _ := body["app_id"].(string)
	header, _ := body["header"].(map[string]interface{})
	if token == "" {
		token, _ = header["token"].(string)
	}
	if appID == "" {
		appID, _ = header["app_id"].(string)
	}
	if event, ok := body["event"].(map[string]interface{}); ok {
		if eventHeader, ok := event["header"].(map[string]interface{}); ok {
			if token == "" {
				token, _ = eventHeader["token"].(string)
			}
			if appID == "" {
				appID, _ = eventHeader["app_id"].(string)
			}
		}
	}
	return token, appID
}

func FeishuEvents(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, body, err := feishuJSONBody(c)
		if err != nil { c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid json"}); return }
		token, appID := feishuHeaderValues(body)
		if !monitor.FeishuVerifyRequest(state, raw, token, appID, c.GetHeader("X-Lark-Request-Timestamp"), c.GetHeader("X-Lark-Request-Nonce"), c.GetHeader("X-Lark-Signature")) {
			c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid token"}); return
		}
		body, err = monitor.FeishuUnwrapPayload(state, body)
		if err != nil { c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()}); return }
		token, appID = feishuHeaderValues(body)
		if !monitor.FeishuVerifyIdentity(state, token, appID) { c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid identity"}); return }
		if challenge, ok := body["challenge"].(string); ok && challenge != "" { c.JSON(http.StatusOK, gin.H{"challenge": challenge}); return }
		event, _ := body["event"].(map[string]interface{})
		message, _ := event["message"].(map[string]interface{})
		contentText, _ := message["content"].(string)
		var content map[string]interface{}
		_ = json.Unmarshal([]byte(contentText), &content)
		text, _ := content["text"].(string)
		openID := feishuOpenID(body)
		if openID != "" {
			accountID := "default"
			trimmed := strings.TrimSpace(text)
			if strings.HasPrefix(trimmed, "绑定账户 ") { accountID = strings.TrimSpace(strings.TrimPrefix(trimmed, "绑定账户 ")) }
			if accountID == "default" || accountExists(state, accountID) {
				_ = monitor.FeishuSaveBinding(state, types.FeishuBinding{AccountID: accountID, OpenID: openID, Name: openID, UpdatedAt: types.NowISO()})
			}
			if trimmed == "账户状态" || strings.EqualFold(trimmed, "status") {
				_ = monitor.FeishuSendText(state, openID, feishuAccountStatusText(state))
			} else if strings.HasPrefix(trimmed, "绑定账户 ") {
				_ = monitor.FeishuSendText(state, openID, "飞书已绑定到账户："+accountID)
			}
		}
		c.JSON(http.StatusOK, gin.H{"code": 0})
	}
}

func FeishuCardAction(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, body, err := feishuJSONBody(c)
		if err != nil { c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid json"}); return }
		token, appID := feishuHeaderValues(body)
		if !monitor.FeishuVerifyRequest(state, raw, token, appID, c.GetHeader("X-Lark-Request-Timestamp"), c.GetHeader("X-Lark-Request-Nonce"), c.GetHeader("X-Lark-Signature")) {
			c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid token"}); return
		}
		body, err = monitor.FeishuUnwrapPayload(state, body)
		if err != nil { c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()}); return }
		token, appID = feishuHeaderValues(body)
		if !monitor.FeishuVerifyIdentity(state, token, appID) { c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid identity"}); return }
		if challenge, ok := body["challenge"].(string); ok && challenge != "" { c.JSON(http.StatusOK, gin.H{"challenge": challenge}); return }
		action, _ := body["action"].(map[string]interface{})
		if event, ok := body["event"].(map[string]interface{}); ok {
			if eventAction, ok := event["action"].(map[string]interface{}); ok {
				action = eventAction
			}
		}
		value := action["value"]
		if rawValue, ok := value.(string); ok { _ = json.Unmarshal([]byte(rawValue), &value) }
		values, _ := value.(map[string]interface{})
		name, _ := values["action"].(string)
		openID := feishuOpenID(body)
		message := "操作已完成"
		switch name {
		case "ping":
			message = "飞书交互卡片连接正常"
		case "add_to_queue":
			accountID, _ := values["accountId"].(string)
			binding, bound := monitor.FeishuBindingForAccount(state, accountID)
			if !bound || openID == "" || binding.OpenID != openID {
				message = "当前飞书用户未绑定该 OVH 账户"
			} else {
				message = feishuEnqueue(state, values)
			}
		default:
			message = "未知操作，请重新打开最新通知卡片"
		}
		c.JSON(http.StatusOK, gin.H{"toast": gin.H{"type": "success", "content": message}})
		if openID != "" && name == "add_to_queue" && strings.HasPrefix(message, "✅") { _ = monitor.FeishuSendText(state, openID, message) }
	}
}

func FeishuBinding(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.Query("account")
		if accountID == "" { accountID = "default" }
		binding, ok := monitor.FeishuBindingForAccount(state, accountID)
		c.JSON(http.StatusOK, gin.H{"success": true, "accountId": accountID, "bound": ok, "binding": binding})
	}
}

func ClearFeishuBinding(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.Query("account")
		if accountID == "" { accountID = "default" }
		if err := monitor.FeishuDeleteBinding(state, accountID); err != nil { c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}); return }
		c.JSON(http.StatusOK, gin.H{"success": true, "cleared": true})
	}
}

func FeishuTestCard(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.Query("account")
		binding, ok := monitor.FeishuBindingForAccount(state, accountID)
		if !ok { c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "当前账户未绑定飞书用户"}); return }
		if !monitor.FeishuEnabled(state) { c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "飞书应用未启用或配置不完整"}); return }
		if err := monitor.FeishuSendCard(state, binding.OpenID, monitor.FeishuTestCard()); err != nil { c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}); return }
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "测试交互卡片已发送"})
	}
}

func feishuOpenID(body map[string]interface{}) string {
	if id, ok := body["open_id"].(string); ok && id != "" {
		return id
	}
	if operator, ok := body["operator"].(map[string]interface{}); ok {
		if id, ok := operator["open_id"].(string); ok && id != "" {
			return id
		}
		if operatorID, ok := operator["operator_id"].(map[string]interface{}); ok {
			if id, ok := operatorID["open_id"].(string); ok && id != "" {
				return id
			}
		}
	}
	event, _ := body["event"].(map[string]interface{})
	operator, _ := event["operator"].(map[string]interface{})
	if id, ok := operator["open_id"].(string); ok && id != "" {
		return id
	}
	if operatorID, ok := operator["operator_id"].(map[string]interface{}); ok {
		if id, ok := operatorID["open_id"].(string); ok && id != "" {
			return id
		}
	}
	sender, _ := event["sender"].(map[string]interface{})
	ids, _ := sender["sender_id"].(map[string]interface{})
	id, _ := ids["open_id"].(string)
	return id
}

func accountExists(state *app.State, accountID string) bool { _, ok := state.FindAccount(accountID); return ok }

func feishuAccountStatusText(state *app.State) string {
	accounts, _ := state.DB.ListAccounts()
	if len(accounts) == 0 { return "当前没有配置 OVH 账户" }
	lines := []string{"OVH 账户状态"}
	for _, acc := range accounts { lines = append(lines, "• "+acc.Name+" ("+acc.Zone+")：已配置") }
	return strings.Join(lines, "\n")
}

func feishuEnqueue(state *app.State, values map[string]interface{}) string {
	planCode, _ := values["planCode"].(string)
	datacenter, _ := values["datacenter"].(string)
	accountID, _ := values["accountId"].(string)
	if planCode == "" || datacenter == "" { return "缺少型号或机房" }
	if _, ok := state.FindAccount(accountID); !ok { return "账户不存在，请重新绑定账户" }
	options := []string{}
	if raw, ok := values["options"].([]interface{}); ok { for _, item := range raw { if text, ok := item.(string); ok { options = append(options, text) } } }
	if len(options) == 0 {
		for _, config := range catalog.CheckServerAvailabilityWithConfigs(state, planCode, accountID) {
			if status := config.Datacenters[datacenter]; status != "unavailable" && status != "unknown" && len(config.Options) > 0 { options = append(options, config.Options...); break }
		}
	}
	result := price.GetInternal(state, accountID, planCode, datacenter, options)
	if !result.Success { return "价格校验失败："+result.Error }
	if result.Price == nil { return "价格校验失败：未返回价格" }
	if raw := result.Price.Prices["withTax"]; raw == nil { return "价格校验失败：该组合暂无有效价格" } else if amount, ok := numconv.ToFloat64(raw); ok && amount == 0 { return "价格校验失败：该组合暂无有效价格" }
	if err := enqueueQuickOrder(state, accountID, planCode, datacenter, options, false, false); err != nil {
		return err.Error()
	}
	return fmt.Sprintf("✅ %s (%s) 已加入购买队列", planCode, strings.ToUpper(datacenter))
}
