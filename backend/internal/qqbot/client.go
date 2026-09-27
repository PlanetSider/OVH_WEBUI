package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

const (
	DefaultBaseURL       = "https://api.bot.qq.com"
	defaultTokenLifetime = 7200 * time.Second
	tokenRefreshBefore   = 60 * time.Second
	maxResponseBodyBytes = 1 << 20
)

// Client 是 QQ Bot v2 的服务端 HTTP 客户端。AppSecret 只从配置读取，
// access_token 只保存在进程内，永不返回给前端。
type Client struct {
	getConfig  func() types.Config
	httpClient *http.Client
	baseURL    string
	now        func() time.Time

	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiresAt time.Time

	gatewayMu sync.Mutex
	gateway   *gatewaySession

	messageHandlerMu sync.RWMutex
	messageHandler   MessageHandler

	seenMessageMu sync.Mutex
	seenMessages  map[string]time.Time
}

// MessageEvent 是 QQ Gateway 投递的私聊或群聊文本消息。
// QQ 只提供 OpenID；业务层据此做管理员和群白名单校验。
type MessageEvent struct {
	ID          string
	EventType   string
	Content     string
	UserOpenID  string
	GroupOpenID string
}

// MessageHandler 处理已由 Gateway 解码的 QQ 入站消息。
type MessageHandler func(context.Context, MessageEvent)

const (
	EventC2CMessageCreate     = "C2C_MESSAGE_CREATE"
	EventGroupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE"
	EventGroupMessageCreate   = "GROUP_MESSAGE_CREATE"
)

func (c *Client) SetMessageHandler(handler MessageHandler) {
	if c == nil {
		return
	}
	c.messageHandlerMu.Lock()
	c.messageHandler = handler
	c.messageHandlerMu.Unlock()
}

func (c *Client) messageHandlerSnapshot() MessageHandler {
	c.messageHandlerMu.RLock()
	defer c.messageHandlerMu.RUnlock()
	return c.messageHandler
}

func (c *Client) claimMessageID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	now := c.now()
	cutoff := now.Add(-10 * time.Minute)
	c.seenMessageMu.Lock()
	defer c.seenMessageMu.Unlock()
	if c.seenMessages == nil {
		c.seenMessages = make(map[string]time.Time)
	}
	for seenID, seenAt := range c.seenMessages {
		if seenAt.Before(cutoff) {
			delete(c.seenMessages, seenID)
		}
	}
	if seenAt, exists := c.seenMessages[id]; exists && !seenAt.Before(cutoff) {
		return false
	}
	c.seenMessages[id] = now
	return true
}

func New(getConfig func() types.Config, httpClient *http.Client, baseURL string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		getConfig:    getConfig,
		httpClient:   httpClient,
		baseURL:      baseURL,
		now:          time.Now,
		seenMessages: make(map[string]time.Time),
	}
}

func (c *Client) Configured() bool {
	if c == nil || c.getConfig == nil {
		return false
	}
	cfg := c.getConfig()
	return cfg.IsQQNotificationsEnabled() && strings.TrimSpace(cfg.QQAppID) != "" &&
		strings.TrimSpace(cfg.QQAppSecret) != "" && len(allTargets(cfg)) > 0
}

// SendDefaultWithContext 向用户目标发送完整通知。群聊和频道目标被有意跳过；
// 若当前仅配置了这两类目标，则视为策略性跳过而不是失败。
func (c *Client) SendDefaultWithContext(ctx context.Context, message string) bool {
	ok, _ := c.send(ctx, message, false)
	return ok
}

func (c *Client) SendDefault(message string) bool {
	return c.SendDefaultWithContext(context.Background(), message)
}

// SendMonitorWithContext 向用户、群聊和频道目标发送独服/VPS 上架或下架提醒。
func (c *Client) SendMonitorWithContext(ctx context.Context, message string) bool {
	ok, _ := c.send(ctx, message, true)
	return ok
}

func (c *Client) SendMonitor(message string) bool {
	return c.SendMonitorWithContext(context.Background(), message)
}

// SendUserReply 向 QQ 私聊用户回复消息，并在 msgID 非空时引用原消息。
func (c *Client) SendUserReply(ctx context.Context, userOpenID, message, msgID string) error {
	return c.sendReply(ctx, target{kind: targetUser, id: userOpenID}, message, msgID)
}

// SendGroupReply 向 QQ 群聊回复消息，并在 msgID 非空时引用原消息。
func (c *Client) SendGroupReply(ctx context.Context, groupOpenID, message, msgID string) error {
	return c.sendReply(ctx, target{kind: targetGroup, id: groupOpenID}, message, msgID)
}

func (c *Client) sendReply(ctx context.Context, destination target, message, msgID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.getConfig == nil {
		return errors.New("QQ Bot 客户端不可用")
	}
	cfg := c.getConfig()
	if !cfg.IsQQNotificationsEnabled() {
		return errors.New("QQ 机器人已关闭")
	}
	if strings.TrimSpace(destination.id) == "" {
		return errors.New("QQ 回复目标为空")
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.getAccessToken(ctx)
		if err != nil {
			return err
		}
		err = c.postMessageWithToken(ctx, token, destination, message, msgID)
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusUnauthorized {
			c.invalidateToken(token)
			continue
		}
		return err
	}
	return errors.New("QQ access token 刷新失败")
}

// RunMessageGateway 启动入站命令所需的 Gateway 生命周期。
// 通道通知仍按需建立 Gateway；命令入口则在配置了用户或群白名单时主动保持连接。
func (c *Client) RunMessageGateway(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	refresh := time.NewTicker(30 * time.Second)
	defer refresh.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if !c.messageGatewayConfigured() {
			select {
			case <-ctx.Done():
				return
			case <-refresh.C:
			}
			continue
		}
		token, err := c.getAccessToken(ctx)
		if err == nil {
			_ = c.ensureGateway(ctx, token)
		}
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
		}
	}
}

func (c *Client) messageGatewayConfigured() bool {
	if c == nil || c.getConfig == nil {
		return false
	}
	cfg := c.getConfig()
	return cfg.IsQQNotificationsEnabled() && strings.TrimSpace(cfg.QQAppID) != "" &&
		strings.TrimSpace(cfg.QQAppSecret) != "" &&
		(len(cfg.QQUserOpenIDs) > 0 || len(cfg.QQGroupOpenIDs) > 0)
}

// SendTest 向所有目标发送测试消息，便于验证三类目标的权限和 ID。
func (c *Client) SendTest(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := c.send(ctx, "🔔 OVH QQ 机器人测试通知\n\n✅ QQ Bot v2 配置已生效。", true)
	return err
}

func (c *Client) send(ctx context.Context, message string, monitorOnly bool) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c == nil || c.getConfig == nil {
		return false, errors.New("QQ Bot 客户端不可用")
	}
	cfg := c.getConfig()
	if !cfg.IsQQNotificationsEnabled() {
		return false, errors.New("QQ 通知已关闭")
	}
	if strings.TrimSpace(cfg.QQAppID) == "" || strings.TrimSpace(cfg.QQAppSecret) == "" {
		return false, errors.New("QQ AppID 或 AppSecret 未配置")
	}
	selected := targetsForMessage(cfg, monitorOnly)
	if len(selected) == 0 {
		if !monitorOnly && len(allTargets(cfg)) > 0 {
			return true, nil
		}
		return false, errors.New("QQ 未配置可用通知目标")
	}

	var failures []string
	for _, target := range selected {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := c.postMessage(ctx, target, message); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return false, fmt.Errorf("QQ 通知发送失败：%s", strings.Join(failures, "; "))
	}
	return true, nil
}

type targetKind string

const (
	targetUser    targetKind = "user"
	targetGroup   targetKind = "group"
	targetChannel targetKind = "channel"
)

type target struct {
	kind    targetKind
	id      string
	guildID string
}

func allTargets(cfg types.Config) []target {
	out := make([]target, 0, len(cfg.QQUserOpenIDs)+len(cfg.QQGroupOpenIDs)+len(cfg.QQChannelTargets))
	seen := make(map[string]struct{})
	appendTarget := func(candidate target) {
		candidate.id = strings.TrimSpace(candidate.id)
		candidate.guildID = strings.TrimSpace(candidate.guildID)
		if candidate.id == "" {
			return
		}
		key := string(candidate.kind) + ":" + candidate.id
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	for _, id := range cfg.QQUserOpenIDs {
		appendTarget(target{kind: targetUser, id: id})
	}
	for _, id := range cfg.QQGroupOpenIDs {
		appendTarget(target{kind: targetGroup, id: id})
	}
	for _, channel := range cfg.QQChannelTargets {
		appendTarget(target{kind: targetChannel, id: channel.ChannelID, guildID: channel.GuildID})
	}
	return out
}

func targetsForMessage(cfg types.Config, monitorOnly bool) []target {
	all := allTargets(cfg)
	if monitorOnly {
		return all
	}
	users := make([]target, 0, len(all))
	for _, candidate := range all {
		if candidate.kind == targetUser {
			users = append(users, candidate)
		}
	}
	return users
}

func (c *Client) postMessage(ctx context.Context, target target, message string) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.getAccessToken(ctx)
		if err != nil {
			return err
		}
		err = c.postMessageWithToken(ctx, token, target, message, "")
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusUnauthorized {
			c.invalidateToken(token)
			continue
		}
		return err
	}
	return errors.New("QQ access token 刷新失败")
}

func (c *Client) postMessageWithToken(ctx context.Context, token string, target target, message, replyToMessageID string) error {
	path := ""
	switch target.kind {
	case targetUser:
		path = "/v2/users/" + url.PathEscape(target.id) + "/messages"
	case targetGroup:
		path = "/v2/groups/" + url.PathEscape(target.id) + "/messages"
	case targetChannel:
		path = "/channels/" + url.PathEscape(target.id) + "/messages"
		if err := c.ensureGateway(ctx, token); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知 QQ 目标类型 %q", target.kind)
	}
	payload := map[string]interface{}{"content": message}
	if strings.TrimSpace(replyToMessageID) != "" {
		payload["msg_id"] = strings.TrimSpace(replyToMessageID)
	}
	if target.kind != targetChannel {
		payload["msg_type"] = 0
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码 QQ 消息失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("创建 QQ 消息请求失败: %w", err)
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 QQ 消息接口失败: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return fmt.Errorf("读取 QQ 消息响应失败: %w", err)
	}
	if len(responseBody) > maxResponseBodyBytes {
		return errors.New("QQ 消息响应过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, responseBody)
	}
	var result struct {
		ErrCode int64  `json:"err_code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if len(strings.TrimSpace(string(responseBody))) > 0 && json.Unmarshal(responseBody, &result) == nil && result.ErrCode != 0 {
		return &APIError{HTTPStatus: resp.StatusCode, Code: result.ErrCode, Message: result.Message, TraceID: result.TraceID}
	}
	return nil
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := c.now()
	if c.accessToken != "" && now.Before(c.tokenExpiresAt.Add(-tokenRefreshBefore)) {
		return c.accessToken, nil
	}
	cfg := c.getConfig()
	payload, err := json.Marshal(map[string]string{"appId": strings.TrimSpace(cfg.QQAppID), "clientSecret": strings.TrimSpace(cfg.QQAppSecret)})
	if err != nil {
		return "", fmt.Errorf("编码 QQ token 请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/app/getAppAccessToken", strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("创建 QQ token 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 QQ token 接口失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 QQ token 响应失败: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return "", errors.New("QQ token 响应过大")
	}
	var result struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		Code        int64           `json:"code"`
		Message     string          `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析 QQ token 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || result.Code != 0 || strings.TrimSpace(result.AccessToken) == "" {
		return "", &APIError{HTTPStatus: resp.StatusCode, Code: result.Code, Message: result.Message}
	}
	expiresIn := parseExpiresIn(result.ExpiresIn)
	if expiresIn <= 0 {
		expiresIn = defaultTokenLifetime
	}
	c.accessToken = strings.TrimSpace(result.AccessToken)
	c.tokenExpiresAt = now.Add(expiresIn)
	return c.accessToken, nil
}

func parseExpiresIn(raw json.RawMessage) time.Duration {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0
	}
	if strings.HasPrefix(value, `"`) {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			value = strings.TrimSpace(text)
		}
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (c *Client) invalidateToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken == token {
		c.accessToken = ""
		c.tokenExpiresAt = time.Time{}
	}
}

// APIError 是 QQ token/OpenAPI 的结构化业务错误；不会包含 AppSecret 或 access_token。
type APIError struct {
	HTTPStatus int
	Code       int64
	Message    string
	TraceID    string
}

func (e *APIError) Error() string {
	if e == nil {
		return "QQ API error"
	}
	parts := []string{"QQ API 错误"}
	if e.Code != 0 {
		parts = append(parts, "code="+strconv.FormatInt(e.Code, 10))
	}
	if e.HTTPStatus != 0 {
		parts = append(parts, "http="+strconv.Itoa(e.HTTPStatus))
	}
	if strings.TrimSpace(e.Message) != "" {
		parts = append(parts, strings.TrimSpace(e.Message))
	}
	if strings.TrimSpace(e.TraceID) != "" {
		parts = append(parts, "trace_id="+strings.TrimSpace(e.TraceID))
	}
	return strings.Join(parts, ": ")
}

func parseAPIError(status int, body []byte) error {
	var payload struct {
		Code    int64  `json:"code"`
		ErrCode int64  `json:"err_code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	_ = json.Unmarshal(body, &payload)
	code := payload.ErrCode
	if code == 0 {
		code = payload.Code
	}
	return &APIError{HTTPStatus: status, Code: code, Message: payload.Message, TraceID: payload.TraceID}
}
