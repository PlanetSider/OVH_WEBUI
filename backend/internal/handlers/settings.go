package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

type settingsResponse struct {
	types.Config
	AppKeyConfigured             bool `json:"appKeyConfigured"`
	AppSecretConfigured          bool `json:"appSecretConfigured"`
	ConsumerKeyConfigured        bool `json:"consumerKeyConfigured"`
	TelegramTokenConfigured      bool `json:"tgTokenConfigured"`
	TelegramChatConfigured       bool `json:"tgChatIdConfigured"`
	TelegramWebhookConfigured    bool `json:"tgWebhookSecretConfigured"`
	FeishuAppSecretConfigured    bool `json:"feishuAppSecretConfigured"`
	FeishuVerificationConfigured bool `json:"feishuVerificationTokenConfigured"`
	FeishuEncryptConfigured      bool `json:"feishuEncryptKeyConfigured"`
	QQAppSecretConfigured        bool `json:"qqAppSecretConfigured"`
}

func toSettingsResponse(cfg types.Config) settingsResponse {
	response := settingsResponse{
		Config:           cfg,
		AppKeyConfigured: cfg.AppKey != "", AppSecretConfigured: cfg.AppSecret != "",
		ConsumerKeyConfigured: cfg.ConsumerKey != "", TelegramTokenConfigured: cfg.TgToken != "",
		TelegramChatConfigured: cfg.TgChatID != "", TelegramWebhookConfigured: cfg.TgWebhookSecret != "",
		FeishuAppSecretConfigured:    cfg.FeishuAppSecret != "",
		FeishuVerificationConfigured: cfg.FeishuVerificationToken != "",
		FeishuEncryptConfigured:      cfg.FeishuEncryptKey != "",
		QQAppSecretConfigured:        cfg.QQAppSecret != "",
	}
	response.AppKey, response.AppSecret, response.ConsumerKey = "", "", ""
	response.TgToken, response.TgChatID, response.TgWebhookSecret = "", "", ""
	response.FeishuAppSecret, response.FeishuVerificationToken, response.FeishuEncryptKey = "", "", ""
	response.QQAppSecret = ""
	return response
}

func normalizeTaskBroadcastTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return "", fmt.Errorf("每日播报时间必须为 HH:mm")
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return "", fmt.Errorf("每日播报时间超出范围")
	}
	return value, nil
}

func normalizeQQIDs(values []string) []string {
	if values == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeQQChannelTargets(values []types.QQChannelTarget) []types.QQChannelTarget {
	if values == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]types.QQChannelTarget, 0, len(values))
	for _, value := range values {
		value.GuildID = strings.TrimSpace(value.GuildID)
		value.ChannelID = strings.TrimSpace(value.ChannelID)
		if value.ChannelID == "" {
			continue
		}
		if _, exists := seen[value.ChannelID]; exists {
			continue
		}
		seen[value.ChannelID] = struct{}{}
		out = append(out, value)
	}
	return out
}

func applyTaskBroadcastPatch(cfg *types.Config, patch types.Config) error {
	if cfg == nil {
		return fmt.Errorf("配置不可用")
	}
	if patch.TaskBroadcastEnabled != nil {
		cfg.TaskBroadcastEnabled = patch.TaskBroadcastEnabled
	}
	if patch.TaskBroadcastQueueEnabled != nil {
		cfg.TaskBroadcastQueueEnabled = patch.TaskBroadcastQueueEnabled
	}
	if patch.TaskBroadcastMonitorEnabled != nil {
		cfg.TaskBroadcastMonitorEnabled = patch.TaskBroadcastMonitorEnabled
	}
	if patch.TaskBroadcastVPSEnabled != nil {
		cfg.TaskBroadcastVPSEnabled = patch.TaskBroadcastVPSEnabled
	}
	if patch.TaskBroadcastReportEnabled != nil {
		cfg.TaskBroadcastReportEnabled = patch.TaskBroadcastReportEnabled
	}
	if strings.TrimSpace(patch.TaskBroadcastTime) != "" {
		timeValue, err := normalizeTaskBroadcastTime(patch.TaskBroadcastTime)
		if err != nil {
			return err
		}
		cfg.TaskBroadcastTime = timeValue
	}
	return nil
}

func GetSettings(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, toSettingsResponse(state.Config.Get()))
	}
}

// SaveSettings POST /api/settings
// 合并更新：前端可不传敏感/内部字段；空值保留服务端已有配置，避免抹掉 TgWebhookSecret 等。
func SaveSettings(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		var patch types.Config
		if err := c.ShouldBindJSON(&patch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "请求格式无效"})
			return
		}

		prev := state.Config.Get()
		newCfg := prev
		if err := applyTaskBroadcastPatch(&newCfg, patch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
			return
		}
		if patch.QueueAutoPayEnabled != nil {
			newCfg.QueueAutoPayEnabled = patch.QueueAutoPayEnabled
		}
		if patch.MonitorAutoPayEnabled != nil {
			newCfg.MonitorAutoPayEnabled = patch.MonitorAutoPayEnabled
		}

		// 凭据去空白（前端粘贴时常带空格/换行，会导致 OVH 签名失败 "Invalid signature"）
		patch.AppKey = strings.TrimSpace(patch.AppKey)
		patch.AppSecret = strings.TrimSpace(patch.AppSecret)
		patch.ConsumerKey = strings.TrimSpace(patch.ConsumerKey)
		patch.TgToken = strings.TrimSpace(patch.TgToken)
		patch.TgChatID = strings.TrimSpace(patch.TgChatID)
		patch.TgWebhookSecret = strings.TrimSpace(patch.TgWebhookSecret)
		patch.FeishuAppID = strings.TrimSpace(patch.FeishuAppID)
		patch.FeishuAppSecret = strings.TrimSpace(patch.FeishuAppSecret)
		patch.FeishuDomain = strings.ToLower(strings.TrimSpace(patch.FeishuDomain))
		patch.FeishuConnectionMode = strings.ToLower(strings.TrimSpace(patch.FeishuConnectionMode))
		patch.FeishuVerificationToken = strings.TrimSpace(patch.FeishuVerificationToken)
		patch.FeishuEncryptKey = strings.TrimSpace(patch.FeishuEncryptKey)
		patch.QQAppID = strings.TrimSpace(patch.QQAppID)
		patch.QQAppSecret = strings.TrimSpace(patch.QQAppSecret)
		patch.QQUserOpenIDs = normalizeQQIDs(patch.QQUserOpenIDs)
		patch.QQGroupOpenIDs = normalizeQQIDs(patch.QQGroupOpenIDs)
		patch.QQChannelTargets = normalizeQQChannelTargets(patch.QQChannelTargets)
		patch.Endpoint = strings.TrimSpace(patch.Endpoint)
		patch.Zone = strings.TrimSpace(patch.Zone)
		patch.IAM = strings.TrimSpace(patch.IAM)

		// 非空才覆盖（合并语义）；Webhook secret 绝不用空串覆盖
		if patch.AppKey != "" {
			newCfg.AppKey = patch.AppKey
		}
		if patch.AppSecret != "" {
			newCfg.AppSecret = patch.AppSecret
		}
		if patch.ConsumerKey != "" {
			newCfg.ConsumerKey = patch.ConsumerKey
		}
		if patch.TgToken != "" {
			newCfg.TgToken = patch.TgToken
		}
		// ChatID 允许显式清空？一般不允许空覆盖已有，避免误清通知
		if patch.TgChatID != "" {
			newCfg.TgChatID = patch.TgChatID
		}
		if patch.TgWebhookSecret != "" {
			newCfg.TgWebhookSecret = patch.TgWebhookSecret
		}
		if patch.TgNotificationsEnabled != nil {
			newCfg.TgNotificationsEnabled = patch.TgNotificationsEnabled
		}
		if patch.FeishuAppID != "" {
			newCfg.FeishuAppID = patch.FeishuAppID
		}
		if patch.FeishuAppSecret != "" {
			newCfg.FeishuAppSecret = patch.FeishuAppSecret
		}
		if patch.FeishuDomain == "feishu" || patch.FeishuDomain == "lark" {
			newCfg.FeishuDomain = patch.FeishuDomain
		}
		if patch.FeishuConnectionMode == "webhook" || patch.FeishuConnectionMode == "long_connection" {
			newCfg.FeishuConnectionMode = patch.FeishuConnectionMode
		}
		if patch.FeishuVerificationToken != "" {
			newCfg.FeishuVerificationToken = patch.FeishuVerificationToken
		}
		if patch.FeishuEncryptKey != "" {
			newCfg.FeishuEncryptKey = patch.FeishuEncryptKey
		}
		if patch.FeishuNotificationsEnabled != nil {
			newCfg.FeishuNotificationsEnabled = patch.FeishuNotificationsEnabled
		}
		if patch.WeixinNotificationsEnabled != nil {
			newCfg.WeixinNotificationsEnabled = patch.WeixinNotificationsEnabled
		}
		if patch.QQAppID != "" {
			newCfg.QQAppID = patch.QQAppID
		}
		if patch.QQAppSecret != "" {
			newCfg.QQAppSecret = patch.QQAppSecret
		}
		if patch.QQNotificationsEnabled != nil {
			newCfg.QQNotificationsEnabled = patch.QQNotificationsEnabled
		}
		if patch.QQUserOpenIDs != nil {
			newCfg.QQUserOpenIDs = patch.QQUserOpenIDs
		}
		if patch.QQGroupOpenIDs != nil {
			newCfg.QQGroupOpenIDs = patch.QQGroupOpenIDs
		}
		if patch.QQChannelTargets != nil {
			newCfg.QQChannelTargets = patch.QQChannelTargets
		}
		// App ID + App Secret 即自动启用飞书发送能力；回调安全项单独校验。
		newCfg.FeishuEnabled = newCfg.FeishuAppID != "" && newCfg.FeishuAppSecret != ""
		if patch.Endpoint != "" {
			newCfg.Endpoint = patch.Endpoint
		}
		if patch.Zone != "" {
			newCfg.Zone = patch.Zone
		}
		if patch.IAM != "" {
			newCfg.IAM = patch.IAM
		}

		// 默认值兜底
		if newCfg.Endpoint == "" {
			newCfg.Endpoint = "ovh-eu"
		}
		if newCfg.Zone == "" {
			newCfg.Zone = "IE"
		}
		if newCfg.FeishuConnectionMode == "" {
			newCfg.FeishuConnectionMode = "long_connection"
		}

		if err := state.Config.Set(newCfg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "保存设置失败"})
			return
		}
		if newCfg.FeishuAppID != prev.FeishuAppID || newCfg.FeishuAppSecret != prev.FeishuAppSecret || newCfg.FeishuDomain != prev.FeishuDomain {
			monitor.FeishuResetToken()
		}
		if state.FeishuConnection != nil {
			state.FeishuConnection.Reconfigure()
		}
		state.Logger.Info("API settings updated in config.json", "system")

		// TG 配置变更 → 同步发测试消息（1:1 对应 Python save_settings 2450-2463）
		if newCfg.TgToken != "" && newCfg.TgChatID != "" {
			changed := newCfg.TgToken != prev.TgToken || newCfg.TgChatID != prev.TgChatID
			if changed || prev.TgToken == "" || prev.TgChatID == "" {
				state.Logger.Info("Telegram Token 或 Chat ID 已更新/设置，尝试发送测试消息", "telegram")
				if telegram.SendMessage(state, "OVH 控制台: Telegram 通知已成功配置 (来自 Go 后端测试)", nil) {
					state.Logger.Info("Telegram 测试消息发送成功。", "")
				} else {
					state.Logger.Warn("Telegram 测试消息发送失败。请检查 Token 和 Chat ID 以及后端日志。", "")
				}
			} else {
				state.Logger.Info("Telegram 配置未更改，跳过测试消息。", "")
			}
		} else {
			state.Logger.Info("未配置 Telegram Token 或 Chat ID，跳过测试消息。", "")
		}

		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}

// VerifyAuth POST /api/verify-auth
func VerifyAuth(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		client, err := ovhClientFor(state, c)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"valid": false})
			return
		}
		var me map[string]interface{}
		if err := client.Get("/me", &me); err != nil {
			state.Logger.Error("Authentication verification failed", "system")
			c.JSON(http.StatusOK, gin.H{"valid": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"valid": true})
	}
}

// EndpointConfig GET /api/endpoint-config
func EndpointConfig(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := state.Config.Get()
		c.JSON(http.StatusOK, gin.H{
			"endpoint": cfg.Endpoint,
			"zone":     cfg.Zone,
		})
	}
}
