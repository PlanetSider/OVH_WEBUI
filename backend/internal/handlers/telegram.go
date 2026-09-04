package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
)

// SetTelegramWebhook POST /api/telegram/set-webhook
func SetTelegramWebhook(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			WebhookURL string `json:"webhook_url"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.WebhookURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 webhook_url 参数"})
			return
		}
		ok, msg, info := telegram.SetWebhook(state, body.WebhookURL)
		if ok {
			c.JSON(http.StatusOK, gin.H{
				"success":      true,
				"message":      "Webhook 设置成功（已启用 secret_token + 命令菜单）",
				"webhook_url":  msg,
				"webhook_info": info,
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "设置失败: " + msg})
	}
}

// GetTelegramWebhookInfo GET /api/telegram/get-webhook-info
func GetTelegramWebhookInfo(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, info, errMsg := telegram.GetWebhookInfo(state)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "webhook_info": info})
	}
}

// TelegramWebhook POST /api/telegram/webhook
// 安全链：secret_token → body 大小 → update_id 幂等 → Chat/User 白名单 → 业务
func TelegramWebhook(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1) secret_token：伪造来源直接 401（Telegram 不会伪造错误 secret）
		secretHdr := c.GetHeader(telegram.SecretTokenHeader)
		if !telegram.ValidateWebhookSecret(state, secretHdr) {
			state.Logger.Warn("拒绝无效 secret_token 的 webhook 请求 from="+c.ClientIP(), "telegram")
			c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "invalid_secret_token"})
			return
		}

		// 2) 限制 body 大小
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, telegram.MaxTelegramBodyBytes)
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "error": "body_too_large_or_read_error"})
			return
		}
		var data map[string]interface{}
		if err := json.Unmarshal(raw, &data); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}

		// 3) update_id 幂等（重放直接 200 吞掉）
		var updateID int64
		switch v := data["update_id"].(type) {
		case float64:
			updateID = int64(v)
		case json.Number:
			n, _ := v.Int64()
			updateID = n
		}
		if state.DB != nil && updateID > 0 {
			claimed, err := state.DB.TryClaimTelegramUpdate(updateID)
			if err != nil {
				state.Logger.Warn("update_id 幂等写入失败: "+err.Error(), "telegram")
			} else if !claimed {
				state.Logger.Info(fmt.Sprintf("忽略重复 update_id=%d", updateID), "telegram")
				c.JSON(http.StatusOK, gin.H{"ok": true, "duplicate": true})
				return
			}
			// 偶发清理 7 天前记录
			if updateID%50 == 0 {
				before := float64(time.Now().Add(-time.Duration(telegram.UpdateIDRetentionDays) * 24 * time.Hour).Unix())
				_, _ = state.DB.CleanupTelegramUpdates(before)
			}
		}

		// 处理 callback_query
		if cb, ok := data["callback_query"].(map[string]interface{}); ok {
			handleCallbackQuery(state, mon, cb, c)
			return
		}

		// 处理普通消息
		if msg, ok := data["message"].(map[string]interface{}); ok {
			text, _ := msg["text"].(string)
			text = strings.TrimSpace(text)
			chatID := getNested(msg, "chat", "id")
			messageID, _ := getNumOrFloat(msg["message_id"])
			fromUser, _ := msg["from"].(map[string]interface{})
			userID, _ := getNumOrFloat(fromUser["id"])
			username, _ := fromUser["username"].(string)
			if username == "" {
				username = "未知用户"
			}
			state.Logger.Info(fmt.Sprintf("收到Telegram普通消息: user_id=%v, username=%s, text=%s",
				userID, username, truncate(text, 100)), "telegram")

			// 频率限制
			rateKey := telegram.ChatIDString(chatID)
			if rateKey == "" {
				rateKey = fmt.Sprintf("%v", userID)
			}
			if !telegram.AllowRate(rateKey) {
				telegram.SendReply(state, chatID, "⚠️ 操作过于频繁，请稍后再试", int64(messageID))
				c.JSON(http.StatusOK, gin.H{"ok": true, "error": "rate_limited"})
				return
			}

			handleTelegramText(state, mon, text, chatID, userID, messageID)
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func handleCallbackQuery(state *app.State, mon *monitor.Monitor, cb map[string]interface{}, c *gin.Context) {
	cbData, _ := cb["data"].(string)
	message, _ := cb["message"].(map[string]interface{})
	chatID := getNested(message, "chat", "id")
	messageID, _ := getNumOrFloat(message["message_id"])
	fromUser, _ := cb["from"].(map[string]interface{})
	userID, _ := getNumOrFloat(fromUser["id"])
	state.Logger.Info(fmt.Sprintf("收到Telegram回调: user_id=%v, callback_data=%s...", userID, truncate(cbData, 50)), "telegram")

	cbID := fmt.Sprintf("%v", cb["id"])

	// 频率限制
	if !telegram.AllowRate(fmt.Sprintf("cb:%v", userID)) {
		telegram.AnswerCallback(state, cbID, "操作过于频繁", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "rate_limited"})
		return
	}

	// Chat / User 白名单
	if !telegram.IsAuthorizedActor(state, chatID, userID) {
		state.Logger.Warn(fmt.Sprintf("拒绝未授权一键下单: chat=%v user=%v", chatID, userID), "telegram")
		telegram.AnswerCallback(state, cbID, "未授权的会话", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "unauthorized"})
		return
	}

	var callbackObj map[string]interface{}
	if strings.HasPrefix(cbData, "b64:") {
		base64Part := cbData[4:]
		if missing := len(base64Part) % 4; missing != 0 {
			base64Part += strings.Repeat("=", 4-missing)
		}
		decoded, err := base64.StdEncoding.DecodeString(base64Part)
		if err != nil {
			telegram.AnswerCallback(state, cbID, "按钮数据无效", true)
			c.JSON(http.StatusOK, gin.H{"ok": true, "error": "callback_decode_failed"})
			return
		}
		if err := json.Unmarshal(decoded, &callbackObj); err != nil {
			telegram.AnswerCallback(state, cbID, "按钮数据无效", true)
			c.JSON(http.StatusOK, gin.H{"ok": true, "error": "invalid_callback_json"})
			return
		}
	} else {
		if err := json.Unmarshal([]byte(cbData), &callbackObj); err != nil {
			telegram.AnswerCallback(state, cbID, "按钮数据无效", true)
			c.JSON(http.StatusOK, gin.H{"ok": true, "error": "invalid_callback_json"})
			return
		}
	}

	action := ""
	if v, ok := callbackObj["a"].(string); ok {
		action = v
	} else if v, ok := callbackObj["action"].(string); ok {
		action = v
	}
	if handleTelegramRebootCallback(state, action, callbackObj, cbID, chatID, userID, int64(messageID)) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if action == "as" {
		accountID, _ := callbackObj["i"].(string)
		account, err := switchDefaultAccount(state, accountID)
		if err != nil {
			state.Logger.Warn("Telegram 切换默认账户失败: "+err.Error(), "telegram")
			telegram.AnswerCallback(state, cbID, "切换失败："+err.Error(), true)
			c.JSON(http.StatusOK, gin.H{"ok": true, "error": "account_switch_failed"})
			return
		}
		message := fmt.Sprintf("✅ 默认 OVH 账户已切换为：%s", accountDisplayName(account))
		telegram.AnswerCallback(state, cbID, "账户已切换", false)
		telegram.SendReply(state, chatID, message, int64(messageID))
		state.Logger.Info(fmt.Sprintf("Telegram 切换默认 OVH 账户: account=%s user=%v", account.ID, userID), "telegram")
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if action != "add_to_queue" {
		state.Logger.Warn("未知的action: "+action, "telegram")
		telegram.AnswerCallback(state, cbID, "未知操作", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "unknown_action"})
		return
	}

	messageUUID := ""
	if v, ok := callbackObj["u"].(string); ok {
		messageUUID = v
	} else if v, ok := callbackObj["uuid"].(string); ok {
		messageUUID = v
	}

	// 生产：必须带 UUID 一次性 nonce；拒绝旧版 p/d/o 裸参数回调（可伪造/可重放）
	if messageUUID == "" {
		state.Logger.Warn("拒绝无 UUID 的一键下单回调", "telegram")
		telegram.AnswerCallback(state, cbID, "按钮协议已升级，请等待新通知", true)
		telegram.SendReply(state, chatID,
			"❌ 该按钮协议过旧或数据无效。\n请等待新的上架通知后使用「一键下单」。",
			int64(messageID))
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "uuid_required"})
		return
	}

	// 一次性按钮必须先通过 SQLite 原子认领，再执行任何业务操作。
	// 内存缓存只负责 TTL 校验，不能作为并发消费依据。
	if state.DB == nil {
		telegram.AnswerCallback(state, cbID, "按钮服务暂不可用", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "button_store_unavailable"})
		return
	}
	if mon.MessageUUIDCacheLookup(messageUUID) == nil {
		telegram.AnswerCallback(state, cbID, "按钮已失效", true)
		telegram.SendReply(state, chatID,
			"❌ 一键下单失败：该通知按钮已过期或无效。\n\n请等待新的上架通知后重试。",
			int64(messageID))
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "button_expired"})
		return
	}

	row, claimed, err := state.DB.ClaimTelegramButton(messageUUID)
	if err != nil {
		state.Logger.Warn("原子认领 TG 按钮失败: "+err.Error(), "telegram")
		telegram.AnswerCallback(state, cbID, "按钮服务暂不可用", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "button_claim_failed"})
		return
	}
	if !claimed {
		telegram.AnswerCallback(state, cbID, "该按钮已使用过", true)
		telegram.SendReply(state, chatID, "⚠️ 该一键下单按钮已使用，请等待新的上架通知。", int64(messageID))
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "button_already_used"})
		return
	}

	planCode := row.PlanCode
	dc := row.Datacenter
	options := dbParseOptions(row.Options)
	configInfo := dbParseConfigInfo(row.ConfigInfo)
	if planCode == "" || dc == "" {
		rollbackTelegramButton(state, messageUUID, "Telegram 按钮数据无效")
		telegram.AnswerCallback(state, cbID, "按钮数据无效", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "invalid_button_data"})
		return
	}
	accountID, _ := configInfo["accountId"].(string)
	if accountID == "" {
		rollbackTelegramButton(state, messageUUID, "Telegram 通知未冻结账户")
		telegram.AnswerCallback(state, cbID, "通知未冻结账户", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "account_not_frozen"})
		return
	}
	if _, ok := state.FindAccount(accountID); !ok {
		rollbackTelegramButton(state, messageUUID, "Telegram 通知账户不存在")
		telegram.AnswerCallback(state, cbID, "通知对应的账户已不存在", true)
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "account_not_found"})
		return
	}

	result := telegram.EnqueueSingle(state, accountID, planCode, dc, options, true)
	if !result.Success {
		// 入队失败：回滚已认领的按钮，允许重试。
		rollbackTelegramButton(state, messageUUID, "Telegram 入队失败")
		telegram.AnswerCallback(state, cbID, "入队失败", true)
		telegram.SendReply(state, chatID, "❌ "+result.Message, int64(messageID))
		c.JSON(http.StatusOK, gin.H{"ok": true, "error": "enqueue_failed"})
		return
	}

	mon.InvalidateMessageUUID(messageUUID)

	optsStr := strings.Join(options, ", ")
	if optsStr == "" {
		optsStr = "无（默认配置）"
	}
	confirmMsg := fmt.Sprintf("✅ 已添加到抢购队列！\n\n型号: %s\n机房: %s\n配置: %s\n\n系统将自动尝试下单。",
		planCode, strings.ToUpper(dc), optsStr)
	telegram.AnswerCallback(state, cbID, "已添加到队列！", false)
	telegram.SendReply(state, chatID, confirmMsg, int64(messageID))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// rollbackTelegramButton 将已经认领、但未能入队的通知按钮恢复为可重试。
// 回滚失败不能被静默忽略，否则用户看到的是可重试错误，按钮却会永久停留
// 在已使用状态。调用方仍返回原始业务错误，日志负责保留持久化故障证据。
func rollbackTelegramButton(state *app.State, messageUUID, reason string) bool {
	if state == nil || state.DB == nil || messageUUID == "" {
		return false
	}
	if err := state.DB.UnclaimTelegramButton(messageUUID); err != nil {
		if state.Logger != nil {
			state.Logger.Error(fmt.Sprintf("回滚一键下单按钮失败: uuid=%s, reason=%s, error=%v", messageUUID, reason, err), "button_security")
		}
		return false
	}
	return true
}

func dbParseOptions(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var opts []string
	if err := json.Unmarshal([]byte(raw), &opts); err != nil || opts == nil {
		return []string{}
	}
	return opts
}

func dbParseConfigInfo(raw string) map[string]interface{} {
	var info map[string]interface{}
	if raw == "" || json.Unmarshal([]byte(raw), &info) != nil || info == nil {
		return map[string]interface{}{}
	}
	return info
}

func getNested(m map[string]interface{}, keys ...string) interface{} {
	var cur interface{} = m
	for _, k := range keys {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func getNumOrFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func strOr(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func toStringSlice(v interface{}) []string {
	out := []string{}
	switch x := v.(type) {
	case []interface{}:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, x...)
	}
	return out
}
