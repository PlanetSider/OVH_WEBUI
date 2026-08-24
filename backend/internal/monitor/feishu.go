package monitor

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

const (
	feishuBindingsKV        = "feishu_bindings"
	feishuDefaultBindingKey = "default"
)

var feishuToken struct {
	sync.Mutex
	value     string
	expiresAt time.Time
}

func feishuOpenAPIBase(state *app.State) string {
	if strings.EqualFold(state.Config.Get().FeishuDomain, "lark") {
		return "https://open.larksuite.com"
	}
	return "https://open.feishu.cn"
}

// FeishuResetToken 在 App ID / App Secret 变更后清空旧租户令牌。
func FeishuResetToken() {
	feishuToken.Lock()
	feishuToken.value = ""
	feishuToken.expiresAt = time.Time{}
	feishuToken.Unlock()
}

func FeishuEnabled(state *app.State) bool {
	cfg := state.Config.Get()
	return cfg.FeishuEnabled && cfg.IsFeishuNotificationsEnabled() && cfg.FeishuAppID != "" && cfg.FeishuAppSecret != ""
}

func FeishuVerifyRequest(state *app.State, body []byte, token, appID, timestamp, nonce, signature string) bool {
	cfg := state.Config.Get()
	if !cfg.FeishuEnabled || cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		return false
	}
	if cfg.FeishuVerificationToken == "" && cfg.FeishuEncryptKey == "" {
		return false
	}
	if cfg.FeishuEncryptKey != "" {
		if signature == "" || timestamp == "" || nonce == "" {
			return false
		}
		sum := sha256.Sum256([]byte(timestamp + nonce + cfg.FeishuEncryptKey + string(body)))
		if !hmac.Equal([]byte(base64.StdEncoding.EncodeToString(sum[:])), []byte(signature)) {
			return false
		}
	}
	if cfg.FeishuVerificationToken != "" && (token == "" || !hmac.Equal([]byte(cfg.FeishuVerificationToken), []byte(token))) {
		return false
	}
	if appID == "" {
		return cfg.FeishuEncryptKey != ""
	}
	return hmac.Equal([]byte(cfg.FeishuAppID), []byte(appID))
}

func FeishuVerifyIdentity(state *app.State, token, appID string) bool {
	cfg := state.Config.Get()
	if !cfg.FeishuEnabled || cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		return false
	}
	if cfg.FeishuVerificationToken == "" && cfg.FeishuEncryptKey == "" {
		return false
	}
	if cfg.FeishuVerificationToken != "" && (token == "" || !hmac.Equal([]byte(cfg.FeishuVerificationToken), []byte(token))) {
		return false
	}
	return appID != "" && hmac.Equal([]byte(cfg.FeishuAppID), []byte(appID))
}

func FeishuUnwrapPayload(state *app.State, body map[string]interface{}) (map[string]interface{}, error) {
	value, ok := body["encrypt"].(string)
	if !ok || value == "" {
		return body, nil
	}
	return decryptFeishuPayload(state.Config.Get().FeishuEncryptKey, value)
}

func decryptFeishuPayload(keyText, encrypted string) (map[string]interface{}, error) {
	if keyText == "" {
		return nil, fmt.Errorf("缺少飞书 Encrypt Key")
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(keyText))
	keys := [][]byte{digest[:], make([]byte, 32)}
	copy(keys[1], []byte(keyText))
	for _, key := range keys {
		attempts := []struct{ iv, payload []byte }{}
		if len(raw) >= aes.BlockSize {
			attempts = append(attempts, struct{ iv, payload []byte }{key[:aes.BlockSize], raw})
		}
		if len(raw) > aes.BlockSize {
			attempts = append(attempts, struct{ iv, payload []byte }{raw[:aes.BlockSize], raw[aes.BlockSize:]})
		}
		for _, attempt := range attempts {
			block, err := aes.NewCipher(key)
			if err != nil || len(attempt.payload)%block.BlockSize() != 0 {
				continue
			}
			plain := make([]byte, len(attempt.payload))
			func() {
				defer func() { _ = recover() }()
				cipher.NewCBCDecrypter(block, attempt.iv).CryptBlocks(plain, attempt.payload)
			}()
			plain = bytes.TrimRight(plain, "\x00")
			if len(plain) > 0 {
				padding := int(plain[len(plain)-1])
				if padding > 0 && padding <= aes.BlockSize && padding <= len(plain) {
					plain = plain[:len(plain)-padding]
				}
			}
			var result map[string]interface{}
			if json.Unmarshal(plain, &result) == nil {
				return result, nil
			}
			start, end := bytes.IndexByte(plain, '{'), bytes.LastIndexByte(plain, '}')
			if start >= 0 && end > start && json.Unmarshal(plain[start:end+1], &result) == nil {
				return result, nil
			}
		}
	}
	return nil, fmt.Errorf("无法解密飞书事件")
}

func feishuTenantToken(state *app.State) (string, error) {
	feishuToken.Lock()
	defer feishuToken.Unlock()
	if feishuToken.value != "" && time.Now().Before(feishuToken.expiresAt) {
		return feishuToken.value, nil
	}
	cfg := state.Config.Get()
	body, _ := json.Marshal(map[string]string{"app_id": cfg.FeishuAppID, "app_secret": cfg.FeishuAppSecret})
	req, err := http.NewRequest(http.MethodPost, feishuOpenAPIBase(state)+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if result.Code != 0 || result.Token == "" {
		return "", fmt.Errorf("获取飞书 tenant_access_token 失败: %s", result.Msg)
	}
	feishuToken.value = result.Token
	feishuToken.expiresAt = time.Now().Add(time.Duration(result.Expire-120) * time.Second)
	return result.Token, nil
}

func feishuAPIRequest(state *app.State, method, endpoint string, payload interface{}, result interface{}) error {
	token, err := feishuTenantToken(state)
	if err != nil {
		return err
	}
	var reader io.Reader
	if payload != nil {
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, feishuOpenAPIBase(state)+"/open-apis"+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		message := strings.TrimSpace(envelope.Msg)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("飞书接口返回 HTTP %d / code %d: %s", resp.StatusCode, envelope.Code, message)
	}
	if result != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("飞书接口返回了无效数据: %w", err)
		}
	}
	return nil
}

func feishuSend(state *app.State, openID, msgType string, content interface{}) error {
	encoded, _ := json.Marshal(content)
	return feishuAPIRequest(state, http.MethodPost, "/im/v1/messages?receive_id_type=open_id", map[string]interface{}{
		"receive_id": openID,
		"msg_type":   msgType,
		"content":    string(encoded),
	}, nil)
}

func FeishuSendText(state *app.State, openID, text string) error {
	return feishuSend(state, openID, "text", map[string]string{"text": text})
}

func FeishuSendCard(state *app.State, openID string, card map[string]interface{}) error {
	return feishuSend(state, openID, "interactive", card)
}

const feishuStreamElementID = "ovh_stream_md"

func feishuStreamingText(card map[string]interface{}) string {
	elements, _ := card["elements"].([]interface{})
	for _, raw := range elements {
		element, _ := raw.(map[string]interface{})
		if element["tag"] == "markdown" {
			if content, ok := element["content"].(string); ok {
				return content
			}
		}
	}
	return "OVH WebUI 通知"
}

func feishuCardKitCard(card map[string]interface{}, content string, streaming bool) map[string]interface{} {
	config := map[string]interface{}{
		"streaming_mode": streaming,
		"summary":        map[string]interface{}{"content": "OVH WebUI 通知"},
	}
	if streaming {
		config["streaming_config"] = map[string]interface{}{
			"print_frequency_ms": map[string]interface{}{"default": 35},
			"print_step":         map[string]interface{}{"default": 3},
			"print_strategy":     "fast",
		}
	}
	bodyElements := []interface{}{
		map[string]interface{}{
			"tag":        "markdown",
			"element_id": feishuStreamElementID,
			"content":    content,
		},
	}
	if actions, ok := card["elements"].([]interface{}); ok {
		for _, raw := range actions {
			element, _ := raw.(map[string]interface{})
			if element["tag"] == "action" {
				bodyElements = append(bodyElements, element)
			}
		}
	}
	result := map[string]interface{}{
		"schema": "2.0",
		"config": config,
		"body":   map[string]interface{}{"elements": bodyElements},
	}
	if header, ok := card["header"].(map[string]interface{}); ok {
		result["header"] = header
	}
	return result
}

func feishuCardKitCreate(state *app.State, card map[string]interface{}) (string, error) {
	data, _ := json.Marshal(card)
	var result struct {
		Data struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}
	if err := feishuAPIRequest(state, http.MethodPost, "/cardkit/v1/cards", map[string]interface{}{
		"type": "card_json",
		"data": string(data),
	}, &result); err != nil {
		return "", err
	}
	if result.Data.CardID == "" {
		return "", fmt.Errorf("飞书 CardKit 未返回 card_id")
	}
	return result.Data.CardID, nil
}

func feishuCardKitUpdateContent(state *app.State, cardID, content string, sequence int) error {
	endpoint := fmt.Sprintf("/cardkit/v1/cards/%s/elements/%s/content", url.PathEscape(cardID), feishuStreamElementID)
	return feishuAPIRequest(state, http.MethodPut, endpoint, map[string]interface{}{
		"content":  content,
		"sequence": sequence,
		"uuid":     fmt.Sprintf("c_%s_%d", cardID, sequence),
	}, nil)
}

func feishuCardKitUpdate(state *app.State, cardID string, card map[string]interface{}, sequence int) error {
	data, _ := json.Marshal(card)
	return feishuAPIRequest(state, http.MethodPut, "/cardkit/v1/cards/"+url.PathEscape(cardID), map[string]interface{}{
		"card": map[string]interface{}{
			"type": "card_json",
			"data": string(data),
		},
		"sequence": sequence,
		"uuid":     fmt.Sprintf("u_%s_%d", cardID, sequence),
	}, nil)
}

func feishuCardKitFinish(state *app.State, cardID, summary string, sequence int) error {
	settings, _ := json.Marshal(map[string]interface{}{
		"config": map[string]interface{}{
			"streaming_mode": false,
			"summary":        map[string]interface{}{"content": summary},
		},
	})
	return feishuAPIRequest(state, http.MethodPatch, "/cardkit/v1/cards/"+url.PathEscape(cardID)+"/settings", map[string]interface{}{
		"settings": string(settings),
		"sequence": sequence,
		"uuid":     fmt.Sprintf("s_%s_%d", cardID, sequence),
	}, nil)
}

func feishuStreamChunks(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{"…"}
	}
	steps := 5
	if len(runes) < steps {
		steps = len(runes)
	}
	result := make([]string, 0, steps)
	for i := 1; i <= steps; i++ {
		end := (len(runes)*i + steps - 1) / steps
		if len(result) == 0 || result[len(result)-1] != string(runes[:end]) {
			result = append(result, string(runes[:end]))
		}
	}
	return result
}

// FeishuSendStreamingCard 使用 CardKit 真流式生命周期发送通知。
// CardKit 不可用或权限不足时自动退回普通 interactive 卡片，保证通知不丢失。
func FeishuSendStreamingCard(state *app.State, openID string, card map[string]interface{}) error {
	fullText := feishuStreamingText(card)
	streamCard := feishuCardKitCard(card, "正在生成通知…", true)
	cardID, err := feishuCardKitCreate(state, streamCard)
	if err != nil {
		state.Logger.Warn("CardKit 创建流式卡片失败，降级为普通卡片: "+err.Error(), "feishu")
		return FeishuSendCard(state, openID, card)
	}
	if err := feishuSend(state, openID, "interactive", map[string]interface{}{
		"type": "card",
		"data": map[string]interface{}{"card_id": cardID},
	}); err != nil {
		state.Logger.Warn("CardKit 卡片实例发送失败，降级为普通卡片: "+err.Error(), "feishu")
		return FeishuSendCard(state, openID, card)
	}
	sequence := 0
	for _, content := range feishuStreamChunks(fullText) {
		sequence++
		if err := feishuCardKitUpdateContent(state, cardID, content, sequence); err != nil {
			state.Logger.Warn("CardKit 流式内容更新失败，将直接完成当前卡片: "+err.Error(), "feishu")
			break
		}
	}
	sequence++
	if err := feishuCardKitUpdate(state, cardID, feishuCardKitCard(card, fullText, true), sequence); err != nil {
		state.Logger.Warn("CardKit 最终整卡更新失败: "+err.Error(), "feishu")
	}
	sequence++
	summary := strings.ReplaceAll(strings.TrimSpace(fullText), "\n", " ")
	if len([]rune(summary)) > 50 {
		summary = string([]rune(summary)[:49]) + "…"
	}
	if err := feishuCardKitFinish(state, cardID, summary, sequence); err != nil {
		state.Logger.Warn("CardKit 关闭流式模式失败: "+err.Error(), "feishu")
	}
	return nil
}

// FeishuTextCard 用同一份 Telegram 文案渲染飞书卡片，不再次查询或筛选业务数据。
func FeishuTextCard(title, text, template string, actions []interface{}) map[string]interface{} {
	if strings.TrimSpace(title) == "" {
		title = "OVH WebUI 通知"
	}
	if template == "" {
		template = "blue"
	}
	elements := []interface{}{map[string]interface{}{"tag": "markdown", "content": text}}
	// 与 Telegram 每行两个按钮的布局一致，也避免单个 action 元素超过飞书按钮数量限制。
	for start := 0; start < len(actions); start += 2 {
		end := start + 2
		if end > len(actions) {
			end = len(actions)
		}
		elements = append(elements, map[string]interface{}{"tag": "action", "actions": actions[start:end]})
	}
	return map[string]interface{}{
		"header":   map[string]interface{}{"template": template, "title": map[string]interface{}{"tag": "plain_text", "content": title}},
		"elements": elements,
	}
}

func FeishuBindings(state *app.State) map[string]types.FeishuBinding {
	bindings := map[string]types.FeishuBinding{}
	if state.DB != nil {
		_, _ = state.DB.GetKV(feishuBindingsKV, &bindings)
	}
	return bindings
}

// FeishuDefaultBinding 返回唯一的全局飞书接收人，并自动迁移旧版“按 OVH 账户绑定”的数据。
func FeishuDefaultBinding(state *app.State) (types.FeishuBinding, bool) {
	bindings := FeishuBindings(state)

	var selected types.FeishuBinding
	// 迁移优先级：当前默认账户旧绑定 > 旧 default 键 > 最近更新的有效绑定。
	// 这样从“按账户绑定”升级时，接收人与当前默认账户的行为保持一致。
	if account, ok := state.FindAccount(""); ok {
		selected = bindings[account.ID]
	}
	if selected.OpenID == "" {
		selected = bindings[feishuDefaultBindingKey]
	}
	if selected.OpenID == "" {
		for _, binding := range bindings {
			if binding.OpenID != "" && (selected.OpenID == "" || binding.UpdatedAt > selected.UpdatedAt) {
				selected = binding
			}
		}
	}
	if selected.OpenID == "" {
		return types.FeishuBinding{}, false
	}
	selected.AccountID = feishuDefaultBindingKey
	if state.DB != nil {
		// 完成一次性迁移后只保留全局键，避免旧账户键再次覆盖后续扫码绑定。
		_ = state.DB.SetKV(feishuBindingsKV, map[string]types.FeishuBinding{feishuDefaultBindingKey: selected})
	}
	return selected, true
}

func FeishuSaveDefaultBinding(state *app.State, binding types.FeishuBinding) error {
	if state.DB == nil {
		return fmt.Errorf("数据库不可用")
	}
	binding.AccountID = feishuDefaultBindingKey
	return state.DB.SetKV(feishuBindingsKV, map[string]types.FeishuBinding{feishuDefaultBindingKey: binding})
}

func FeishuDeleteDefaultBinding(state *app.State) error {
	if state.DB == nil {
		return fmt.Errorf("数据库不可用")
	}
	// 清空旧键，防止删除全局绑定后又被迁移回来。
	return state.DB.SetKV(feishuBindingsKV, map[string]types.FeishuBinding{})
}

func NotificationConfigured(state *app.State, accountID string) (bool, string) {
	if ok, _ := telegram.VerifyConfig(state); ok {
		return true, ""
	}
	if FeishuEnabled(state) {
		if _, ok := FeishuDefaultBinding(state); ok {
			return true, ""
		}
	}
	if state.Config.Get().IsWeixinNotificationsEnabled() && state.Weixin != nil && state.Weixin.Configured() {
		return true, ""
	}
	if FeishuEnabled(state) {
		return false, "飞书已配置，但尚未绑定全局飞书接收人"
	}
	return false, "Telegram、飞书与微信均未完成配置"
}

// SendWeixinNotification 将同一份通知文案发送到全局绑定的微信 iLink 用户。
func SendWeixinNotification(state *app.State, message string) bool {
	return state.Config.Get().IsWeixinNotificationsEnabled() && state.Weixin != nil && state.Weixin.SendDefault(message)
}

// FeishuSendDefaultNotification 向全局飞书接收人发送通知。
func FeishuSendDefaultNotification(state *app.State, title, text, template string, actions []interface{}) bool {
	if !FeishuEnabled(state) {
		return false
	}
	binding, ok := FeishuDefaultBinding(state)
	if !ok {
		return false
	}
	if err := FeishuSendStreamingCard(state, binding.OpenID, FeishuTextCard(title, text, template, actions)); err != nil {
		state.Logger.Warn("发送飞书通知失败: "+err.Error(), "feishu")
		return false
	}
	return true
}

func FeishuCardAction(action string, values map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{"action": action}
	for key, value := range values {
		result[key] = value
	}
	return result
}

func FeishuTestCard() map[string]interface{} {
	return map[string]interface{}{"header": map[string]interface{}{"template": "blue", "title": map[string]interface{}{"tag": "plain_text", "content": "OVH WebUI 飞书测试"}}, "elements": []interface{}{map[string]interface{}{"tag": "markdown", "content": "飞书通知和交互卡片已连接。"}, map[string]interface{}{"tag": "action", "actions": []interface{}{map[string]interface{}{"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": "确认"}, "type": "primary", "value": FeishuCardAction("ping", map[string]interface{}{})}}}}}
}
