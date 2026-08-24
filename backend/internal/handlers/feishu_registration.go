package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

const (
	feishuRegistrationURL = "https://accounts.feishu.cn/oauth/v1/app/registration"
	larkRegistrationURL   = "https://accounts.larksuite.com/oauth/v1/app/registration"
)

type feishuRegistrationSession struct {
	DeviceCode string
	Endpoint   string
	Interval   time.Duration
	ExpiresAt  time.Time
	NextPollAt time.Time
}

var feishuRegistrationSessions = struct {
	sync.Mutex
	items map[string]*feishuRegistrationSession
}{items: map[string]*feishuRegistrationSession{}}

type feishuRegistrationResponse struct {
	SupportedAuthMethods []string `json:"supported_auth_methods"`
	DeviceCode           string   `json:"device_code"`
	VerificationURI      string   `json:"verification_uri"`
	VerificationComplete string   `json:"verification_uri_complete"`
	ExpiresIn            int      `json:"expires_in"`
	ExpireIn             int      `json:"expire_in"`
	Interval             int      `json:"interval"`
	ClientID             string   `json:"client_id"`
	ClientSecret         string   `json:"client_secret"`
	Error                string   `json:"error"`
	ErrorDescription     string   `json:"error_description"`
	UserInfo             struct {
		OpenID      string `json:"open_id"`
		TenantBrand string `json:"tenant_brand"`
	} `json:"user_info"`
}

func callFeishuRegistration(endpoint string, values url.Values) (feishuRegistrationResponse, error) {
	if endpoint != feishuRegistrationURL && endpoint != larkRegistrationURL {
		return feishuRegistrationResponse{}, fmt.Errorf("不受信任的飞书注册端点")
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return feishuRegistrationResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return feishuRegistrationResponse{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	var result feishuRegistrationResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("飞书注册接口返回了无效数据")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// poll 会使用 4xx 返回 authorization_pending / slow_down 等设备授权状态。
		// 官方工具同样会把这类结构化响应交给上层状态机处理，不能当成网关错误。
		if result.Error != "" {
			return result, nil
		}
		message := result.ErrorDescription
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return result, fmt.Errorf("飞书注册接口失败：%s", message)
	}
	return result, nil
}

func feishuRegistrationNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
}

func trustedFeishuVerificationURL(raw string) (*url.URL, bool) {
	verificationURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || verificationURL.Scheme != "https" || verificationURL.User != nil {
		return nil, false
	}
	switch strings.ToLower(verificationURL.Hostname()) {
	case "accounts.feishu.cn", "accounts.larksuite.com", "open.feishu.cn", "open.larksuite.com":
		if verificationURL.Port() == "" || verificationURL.Port() == "443" {
			return verificationURL, true
		}
	}
	return nil, false
}

// StartFeishuRegistration POST /api/feishu/registration/start
// 使用飞书官方 PersonalAgent 注册协议创建扫码会话。
func StartFeishuRegistration(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		feishuRegistrationNoStore(c)
		initResult, err := callFeishuRegistration(feishuRegistrationURL, url.Values{"action": {"init"}})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}
		if initResult.Error != "" {
			message := strings.TrimSpace(initResult.ErrorDescription)
			if message == "" {
				message = initResult.Error
			}
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "初始化飞书扫码注册失败：" + message})
			return
		}
		supported := false
		for _, method := range initResult.SupportedAuthMethods {
			if method == "client_secret" {
				supported = true
				break
			}
		}
		if !supported {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "当前飞书环境不支持扫码返回机器人凭据"})
			return
		}
		beginResult, err := callFeishuRegistration(feishuRegistrationURL, url.Values{
			"action":            {"begin"},
			"archetype":         {"PersonalAgent"},
			"auth_method":       {"client_secret"},
			"request_user_info": {"open_id"},
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}
		if beginResult.Error != "" {
			message := strings.TrimSpace(beginResult.ErrorDescription)
			if message == "" {
				message = beginResult.Error
			}
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "创建飞书扫码会话失败：" + message})
			return
		}
		if beginResult.DeviceCode == "" || beginResult.VerificationComplete == "" {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "飞书注册会话缺少二维码或设备码"})
			return
		}
		expires := beginResult.ExpiresIn
		if expires <= 0 {
			expires = beginResult.ExpireIn
		}
		if expires <= 0 || expires > 1800 {
			expires = 600
		}
		interval := beginResult.Interval
		if interval <= 0 {
			interval = 5
		}
		if interval > 60 {
			interval = 60
		}
		verificationURL, trusted := trustedFeishuVerificationURL(beginResult.VerificationComplete)
		if !trusted {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "飞书返回了无效的二维码地址"})
			return
		}
		query := verificationURL.Query()
		query.Set("from", "onboard")
		verificationURL.RawQuery = query.Encode()

		now := time.Now()
		sessionID := uuid.NewString()
		feishuRegistrationSessions.Lock()
		for id, item := range feishuRegistrationSessions.items {
			if now.After(item.ExpiresAt) {
				delete(feishuRegistrationSessions.items, id)
			}
		}
		feishuRegistrationSessions.items[sessionID] = &feishuRegistrationSession{
			DeviceCode: beginResult.DeviceCode,
			Endpoint:   feishuRegistrationURL,
			Interval:   time.Duration(interval) * time.Second,
			ExpiresAt:  now.Add(time.Duration(expires) * time.Second),
		}
		feishuRegistrationSessions.Unlock()
		state.Logger.Info("已创建飞书机器人扫码配置会话", "feishu")
		c.JSON(http.StatusOK, gin.H{
			"success": true, "sessionId": sessionID,
			"verificationUriComplete": verificationURL.String(),
			"expiresIn":               expires, "interval": interval,
		})
	}
}

// PollFeishuRegistration GET /api/feishu/registration/:sessionId
func PollFeishuRegistration(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		feishuRegistrationNoStore(c)
		sessionID := strings.TrimSpace(c.Param("sessionId"))
		feishuRegistrationSessions.Lock()
		session := feishuRegistrationSessions.items[sessionID]
		if session == nil {
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"success": false, "status": "expired", "error": "扫码会话不存在或已结束"})
			return
		}
		now := time.Now()
		if now.After(session.ExpiresAt) {
			delete(feishuRegistrationSessions.items, sessionID)
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusGone, gin.H{"success": false, "status": "expired", "error": "二维码已过期，请重新生成"})
			return
		}
		if now.Before(session.NextPollAt) {
			retryAfter := int(time.Until(session.NextPollAt).Seconds()) + 1
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusOK, gin.H{"success": true, "status": "pending", "retryAfter": retryAfter})
			return
		}
		session.NextPollAt = now.Add(session.Interval)
		endpoint, deviceCode := session.Endpoint, session.DeviceCode
		feishuRegistrationSessions.Unlock()

		result, err := callFeishuRegistration(endpoint, url.Values{"action": {"poll"}, "device_code": {deviceCode}})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "status": "error", "error": err.Error()})
			return
		}
		if result.UserInfo.TenantBrand == "lark" && endpoint != larkRegistrationURL && result.ClientID == "" {
			feishuRegistrationSessions.Lock()
			if current := feishuRegistrationSessions.items[sessionID]; current != nil {
				current.Endpoint = larkRegistrationURL
				current.NextPollAt = time.Time{}
			}
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusOK, gin.H{"success": true, "status": "pending", "retryAfter": 1})
			return
		}
		if result.ClientID != "" && result.ClientSecret != "" {
			cfg := state.Config.Get()
			cfg.FeishuAppID = strings.TrimSpace(result.ClientID)
			cfg.FeishuAppSecret = strings.TrimSpace(result.ClientSecret)
			if endpoint == larkRegistrationURL || result.UserInfo.TenantBrand == "lark" {
				cfg.FeishuDomain = "lark"
			} else {
				cfg.FeishuDomain = "feishu"
			}
			cfg.FeishuEnabled = true
			if err := state.Config.Set(cfg); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "status": "error", "error": "保存飞书机器人凭据失败"})
				return
			}
			if state.FeishuConnection != nil {
				state.FeishuConnection.Reconfigure()
			}
			monitor.FeishuResetToken()
			if result.UserInfo.OpenID != "" {
				_ = monitor.FeishuSaveDefaultBinding(state, types.FeishuBinding{
					AccountID: "default", OpenID: result.UserInfo.OpenID,
					Name: result.UserInfo.OpenID, UpdatedAt: types.NowISO(),
				})
			}
			feishuRegistrationSessions.Lock()
			delete(feishuRegistrationSessions.items, sessionID)
			feishuRegistrationSessions.Unlock()
			state.Logger.Info("飞书机器人扫码配置成功，凭据已安全保存", "feishu")
			c.JSON(http.StatusOK, gin.H{
				"success": true, "status": "complete", "appId": cfg.FeishuAppID,
				"appSecret": cfg.FeishuAppSecret, "domain": cfg.FeishuDomain,
				"bound": result.UserInfo.OpenID != "",
			})
			return
		}
		switch result.Error {
		case "", "authorization_pending":
			c.JSON(http.StatusOK, gin.H{"success": true, "status": "pending"})
		case "slow_down":
			feishuRegistrationSessions.Lock()
			if current := feishuRegistrationSessions.items[sessionID]; current != nil {
				current.Interval += 5 * time.Second
				if current.Interval > 60*time.Second {
					current.Interval = 60 * time.Second
				}
			}
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusOK, gin.H{"success": true, "status": "pending"})
		case "access_denied":
			feishuRegistrationSessions.Lock()
			delete(feishuRegistrationSessions.items, sessionID)
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusForbidden, gin.H{"success": false, "status": "denied", "error": "你已取消创建飞书机器人"})
		case "expired_token":
			feishuRegistrationSessions.Lock()
			delete(feishuRegistrationSessions.items, sessionID)
			feishuRegistrationSessions.Unlock()
			c.JSON(http.StatusGone, gin.H{"success": false, "status": "expired", "error": "二维码已过期，请重新生成"})
		default:
			message := strings.TrimSpace(result.ErrorDescription)
			if message == "" {
				message = result.Error
			}
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "status": "error", "error": message})
		}
	}
}
