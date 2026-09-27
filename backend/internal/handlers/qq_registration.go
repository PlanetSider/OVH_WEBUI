package handlers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
)

const (
	qqRegistrationPortalURL      = "https://q.qq.com"
	qqRegistrationCreatePath     = "/lite/create_bind_task"
	qqRegistrationPollPath       = "/lite/poll_bind_result"
	qqRegistrationQRCodeTemplate = "https://q.qq.com/qqbot/openclaw/connect.html?task_id=%s&_wv=2&source=hermes"
	qqRegistrationExpires        = 10 * time.Minute
	qqRegistrationPollInterval   = 2 * time.Second
	qqRegistrationHTTPTimeout    = 10 * time.Second
	qqRegistrationMaxBodyBytes   = 1 << 20
)

type qqRegistrationSession struct {
	TaskID     string
	BindKey    string
	ExpiresAt  time.Time
	NextPollAt time.Time
}

var qqRegistrationSessions = struct {
	sync.Mutex
	items map[string]*qqRegistrationSession
}{items: map[string]*qqRegistrationSession{}}

type qqRegistrationBindData struct {
	TaskID           string          `json:"task_id"`
	Status           json.RawMessage `json:"status"`
	AppID            string          `json:"app_id"`
	BotAppID         string          `json:"bot_appid"`
	ClientSecret     string          `json:"client_secret"`
	BotEncryptSecret string          `json:"bot_encrypt_secret"`
	UserOpenID       string          `json:"user_openid"`
	OpenID           string          `json:"openid"`
}

type qqRegistrationBindResponse struct {
	RetCode int                    `json:"retcode"`
	Message string                 `json:"msg"`
	Data    qqRegistrationBindData `json:"data"`
}

// qqRegistrationCall is replaceable in tests so the handler tests never call q.qq.com.
var qqRegistrationCall = callQQRegistration

func qqRegistrationNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
}

func generateQQBindKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("生成 QQ 绑定密钥失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func decryptQQBotSecret(encryptedBase64, keyBase64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyBase64))
	if err != nil || len(key) != 32 {
		return "", errors.New("QQ 绑定密钥无效")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedBase64))
	if err != nil {
		return "", errors.New("QQ Bot 密钥密文无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.New("初始化 QQ Bot 密钥解密失败")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("初始化 QQ Bot 密钥解密失败")
	}
	if len(ciphertext) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("QQ Bot 密钥密文长度无效")
	}
	plaintext, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("QQ Bot 密钥解密失败")
	}
	secret := strings.TrimSpace(string(plaintext))
	if secret == "" {
		return "", errors.New("QQ Bot 密钥为空")
	}
	return secret, nil
}

func callQQRegistration(ctx context.Context, path string, payload any) (qqRegistrationBindResponse, error) {
	if path != qqRegistrationCreatePath && path != qqRegistrationPollPath {
		return qqRegistrationBindResponse{}, errors.New("不受信任的 QQ 注册端点")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return qqRegistrationBindResponse{}, fmt.Errorf("编码 QQ 注册请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qqRegistrationPortalURL+path, strings.NewReader(string(body)))
	if err != nil {
		return qqRegistrationBindResponse{}, fmt.Errorf("创建 QQ 注册请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OVH-WebUI/QQBot-Onboard")
	resp, err := (&http.Client{Timeout: qqRegistrationHTTPTimeout}).Do(req)
	if err != nil {
		return qqRegistrationBindResponse{}, fmt.Errorf("QQ 注册服务请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, qqRegistrationMaxBodyBytes+1))
	if err != nil {
		return qqRegistrationBindResponse{}, errors.New("读取 QQ 注册服务响应失败")
	}
	if len(raw) > qqRegistrationMaxBodyBytes {
		return qqRegistrationBindResponse{}, errors.New("QQ 注册服务响应过大")
	}
	var result qqRegistrationBindResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return qqRegistrationBindResponse{}, errors.New("QQ 注册服务返回了无效数据")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return result, fmt.Errorf("QQ 注册服务失败：HTTP %d", resp.StatusCode)
	}
	if result.RetCode != 0 {
		return result, errors.New("QQ 注册服务返回错误")
	}
	return result, nil
}

func qqRegistrationStatus(raw json.RawMessage) int {
	var status int
	if json.Unmarshal(raw, &status) == nil {
		return status
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "pending", "waiting", "wait":
			return 1
		case "complete", "completed", "success":
			return 2
		case "expired", "expire":
			return 3
		}
	}
	return 0
}

func qqRegistrationValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// StartQQRegistration POST /api/qq/registration/start
func StartQQRegistration(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		qqRegistrationNoStore(c)
		bindKey, err := generateQQBindKey()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "无法生成 QQ 扫码会话"})
			return
		}
		result, err := qqRegistrationCall(c.Request.Context(), qqRegistrationCreatePath, gin.H{"key": bindKey})
		if err != nil || strings.TrimSpace(result.Data.TaskID) == "" {
			if state != nil && state.Logger != nil {
				state.Logger.Warn("创建 QQ Bot 扫码绑定任务失败", "qq")
			}
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "QQ 注册服务暂不可用"})
			return
		}
		now := time.Now()
		sessionID := uuid.NewString()
		qqRegistrationSessions.Lock()
		for id, session := range qqRegistrationSessions.items {
			if now.After(session.ExpiresAt) {
				delete(qqRegistrationSessions.items, id)
			}
		}
		qqRegistrationSessions.items[sessionID] = &qqRegistrationSession{
			TaskID: result.Data.TaskID, BindKey: bindKey,
			ExpiresAt: now.Add(qqRegistrationExpires), NextPollAt: now,
		}
		qqRegistrationSessions.Unlock()
		c.JSON(http.StatusOK, gin.H{
			"success": true, "sessionId": sessionID,
			"verificationUriComplete": fmt.Sprintf(qqRegistrationQRCodeTemplate, result.Data.TaskID),
			"expiresIn":               int(qqRegistrationExpires / time.Second),
			"interval":                int(qqRegistrationPollInterval / time.Second),
		})
	}
}

// PollQQRegistration GET /api/qq/registration/:sessionId
func PollQQRegistration(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		qqRegistrationNoStore(c)
		sessionID := strings.TrimSpace(c.Param("sessionId"))
		qqRegistrationSessions.Lock()
		session := qqRegistrationSessions.items[sessionID]
		if session == nil {
			qqRegistrationSessions.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"success": false, "status": "expired", "error": "扫码会话不存在或已结束"})
			return
		}
		now := time.Now()
		if now.After(session.ExpiresAt) {
			delete(qqRegistrationSessions.items, sessionID)
			qqRegistrationSessions.Unlock()
			c.JSON(http.StatusGone, gin.H{"success": false, "status": "expired", "error": "二维码已过期，请重新生成"})
			return
		}
		if now.Before(session.NextPollAt) {
			retryAfter := int(time.Until(session.NextPollAt).Seconds()) + 1
			qqRegistrationSessions.Unlock()
			c.JSON(http.StatusOK, gin.H{"success": true, "status": "pending", "retryAfter": retryAfter})
			return
		}
		session.NextPollAt = now.Add(qqRegistrationPollInterval)
		taskID, bindKey := session.TaskID, session.BindKey
		qqRegistrationSessions.Unlock()

		result, err := qqRegistrationCall(c.Request.Context(), qqRegistrationPollPath, gin.H{"task_id": taskID})
		if err != nil {
			if state != nil && state.Logger != nil {
				state.Logger.Warn("轮询 QQ Bot 扫码绑定任务失败", "qq")
			}
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "status": "error", "error": "QQ 注册服务暂不可用"})
			return
		}
		data := result.Data
		status := qqRegistrationStatus(data.Status)
		appID := qqRegistrationValue(data.BotAppID, data.AppID)
		encryptedSecret := qqRegistrationValue(data.BotEncryptSecret, data.ClientSecret)
		openID := qqRegistrationValue(data.UserOpenID, data.OpenID)
		if status == 3 {
			qqRegistrationSessions.Lock()
			delete(qqRegistrationSessions.items, sessionID)
			qqRegistrationSessions.Unlock()
			c.JSON(http.StatusGone, gin.H{"success": false, "status": "expired", "error": "二维码已过期，请重新生成"})
			return
		}
		if (status == 2 || (appID != "" && encryptedSecret != "")) && appID != "" && encryptedSecret != "" {
			secret, decryptErr := decryptQQBotSecret(encryptedSecret, bindKey)
			if decryptErr != nil {
				qqRegistrationSessions.Lock()
				delete(qqRegistrationSessions.items, sessionID)
				qqRegistrationSessions.Unlock()
				c.JSON(http.StatusBadGateway, gin.H{"success": false, "status": "error", "error": "QQ Bot 密钥解密失败，请重新扫码"})
				return
			}
			cfg := state.Config.Get()
			cfg.QQAppID = strings.TrimSpace(appID)
			cfg.QQAppSecret = secret
			enabled := true
			cfg.QQNotificationsEnabled = &enabled
			if openID != "" {
				found := false
				for _, existing := range cfg.QQUserOpenIDs {
					if strings.TrimSpace(existing) == openID {
						found = true
						break
					}
				}
				if !found {
					cfg.QQUserOpenIDs = append(cfg.QQUserOpenIDs, openID)
				}
			}
			if err := state.Config.Set(cfg); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "status": "error", "error": "保存 QQ Bot 凭据失败"})
				return
			}
			if reconfigurer, ok := state.QQ.(interface{ Reconfigure() }); ok {
				reconfigurer.Reconfigure()
			}
			qqRegistrationSessions.Lock()
			delete(qqRegistrationSessions.items, sessionID)
			qqRegistrationSessions.Unlock()
			if state.Logger != nil {
				state.Logger.Info("QQ Bot 扫码配置成功，凭据已安全保存", "qq")
			}
			c.JSON(http.StatusOK, gin.H{
				"success": true, "status": "complete", "appId": cfg.QQAppID,
				"appSecretConfigured": true, "userOpenId": openID, "bound": openID != "",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "status": "pending", "retryAfter": int(qqRegistrationPollInterval / time.Second)})
	}
}
