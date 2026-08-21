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
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

const feishuBindingsKV = "feishu_bindings"

var feishuToken struct {
	sync.Mutex
	value string
	expiresAt time.Time
}

func FeishuEnabled(state *app.State) bool {
	cfg := state.Config.Get()
	return cfg.FeishuEnabled && cfg.FeishuAppID != "" && cfg.FeishuAppSecret != ""
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
	req, err := http.NewRequest(http.MethodPost, "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
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
		Code int `json:"code"`
		Msg string `json:"msg"`
		Token string `json:"tenant_access_token"`
		Expire int `json:"expire"`
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

func feishuSend(state *app.State, openID, msgType string, content interface{}) error {
	token, err := feishuTenantToken(state)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(content)
	body, _ := json.Marshal(map[string]interface{}{"receive_id": openID, "msg_type": msgType, "content": string(encoded)})
	req, err := http.NewRequest(http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id", bytes.NewReader(body))
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
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("飞书接口返回 %d: %s", resp.StatusCode, string(raw))
	}
	var result struct{ Code int `json:"code"`; Msg string `json:"msg"` }
	if json.Unmarshal(raw, &result) == nil && result.Code != 0 {
		return fmt.Errorf("飞书接口错误: %s", result.Msg)
	}
	return nil
}

func FeishuSendText(state *app.State, openID, text string) error {
	return feishuSend(state, openID, "text", map[string]string{"text": text})
}

func FeishuSendCard(state *app.State, openID string, card map[string]interface{}) error {
	return feishuSend(state, openID, "interactive", card)
}

func FeishuBindings(state *app.State) map[string]types.FeishuBinding {
	bindings := map[string]types.FeishuBinding{}
	if state.DB != nil {
		_, _ = state.DB.GetKV(feishuBindingsKV, &bindings)
	}
	return bindings
}

func FeishuSaveBinding(state *app.State, binding types.FeishuBinding) error {
	bindings := FeishuBindings(state)
	bindings[binding.AccountID] = binding
	return state.DB.SetKV(feishuBindingsKV, bindings)
}

func FeishuDeleteBinding(state *app.State, accountID string) error {
	bindings := FeishuBindings(state)
	delete(bindings, accountID)
	return state.DB.SetKV(feishuBindingsKV, bindings)
}

func FeishuBindingForAccount(state *app.State, accountID string) (types.FeishuBinding, bool) {
	bindings := FeishuBindings(state)
	resolvedAccountID := strings.TrimSpace(accountID)
	if resolvedAccountID == "" || resolvedAccountID == "default" {
		if account, ok := state.FindAccount(""); ok {
			resolvedAccountID = account.ID
		}
	}
	if binding, ok := bindings[resolvedAccountID]; ok && binding.OpenID != "" {
		return binding, true
	}
	if binding, ok := bindings["default"]; ok && binding.OpenID != "" {
		return binding, true
	}
	return types.FeishuBinding{}, false
}

func NotificationConfigured(state *app.State, accountID string) (bool, string) {
	if ok, _ := telegram.VerifyConfig(state); ok {
		return true, ""
	}
	if FeishuEnabled(state) {
		if _, ok := FeishuBindingForAccount(state, accountID); ok {
			return true, ""
		}
		return false, "飞书已配置，但当前账户尚未绑定飞书用户"
	}
	return false, "Telegram 与飞书均未完成配置"
}

func FeishuCardAction(action string, values map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{"action": action}
	for key, value := range values {
		result[key] = value
	}
	return result
}

type FeishuAvailabilityGroup struct {
	Available []map[string]interface{}
	ConfigInfo map[string]interface{}
	PriceError string
	ConfigTraceID string
}

func FeishuAvailabilityCard(planCode, serverName, accountID, traceID string, groups []FeishuAvailabilityGroup) map[string]interface{} {
	elements := []interface{}{}
	summary := []string{"**型号**: " + planCode, fmt.Sprintf("**可用配置**: %d 套", len(groups))}
	if serverName != "" { summary = append(summary, "**服务器**: "+serverName) }
	elements = append(elements, map[string]interface{}{"tag": "markdown", "content": strings.Join(summary, "\n")})
	for index, group := range groups {
		configInfo := group.ConfigInfo
		lines := []string{fmt.Sprintf("**配置 %d**", index+1)}
		if display, ok := configInfo["display"].(string); ok && display != "" { lines = append(lines, display) }
		if price, ok := configInfo["cached_price"].(string); ok && price != "" { lines = append(lines, "价格: "+price) } else if group.PriceError != "" { lines = append(lines, "⚠️ "+group.PriceError) }
		dcs := []string{}
		for _, item := range group.Available { if dc, ok := item["dc"].(string); ok { dcs = append(dcs, strings.ToUpper(dc)) } }
		lines = append(lines, "机房: "+strings.Join(dcs, "、"))
		elements = append(elements, map[string]interface{}{"tag": "markdown", "content": strings.Join(lines, "\n")})
		row := []interface{}{}
		for _, item := range group.Available {
			dc, _ := item["dc"].(string)
			action := map[string]interface{}{"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": strings.ToUpper(dc)+" 入队"}, "type": "primary", "value": FeishuCardAction("add_to_queue", map[string]interface{}{"planCode": planCode, "datacenter": dc, "accountId": accountID, "options": configInfo["options"]})}
			row = append(row, action)
			if len(row) == 2 { elements = append(elements, map[string]interface{}{"tag": "action", "actions": row}); row = []interface{}{} }
		}
		if len(row) > 0 { elements = append(elements, map[string]interface{}{"tag": "action", "actions": row}) }
	}
	if traceID != "" { elements = append(elements, map[string]interface{}{"tag": "markdown", "content": "订阅 Trace: "+traceID}) }
	return map[string]interface{}{"header": map[string]interface{}{"template": "green", "title": map[string]interface{}{"tag": "plain_text", "content": "🎉 服务器配置聚合上架通知"}}, "elements": elements}
}

func FeishuTestCard() map[string]interface{} {
	return map[string]interface{}{"header": map[string]interface{}{"template": "blue", "title": map[string]interface{}{"tag": "plain_text", "content": "OVH WebUI 飞书测试"}}, "elements": []interface{}{map[string]interface{}{"tag": "markdown", "content": "飞书通知和交互卡片已连接。"}, map[string]interface{}{"tag": "action", "actions": []interface{}{map[string]interface{}{"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": "确认"}, "type": "primary", "value": FeishuCardAction("ping", map[string]interface{}{})}}}}}
}

func (m *Monitor) sendFeishuAvailabilityAggregate(planCode, serverName, accountID, traceID string, groups []FeishuAvailabilityGroup) {
	if !FeishuEnabled(m.state) {
		return
	}
	binding, ok := FeishuBindingForAccount(m.state, accountID)
	if !ok {
		return
	}
	card := FeishuAvailabilityCard(planCode, serverName, accountID, traceID, groups)
	if err := FeishuSendCard(m.state, binding.OpenID, card); err != nil {
		m.state.Logger.Warn("发送飞书可用性卡片失败: "+err.Error(), "feishu")
		return
	}
	m.state.Logger.Info(fmt.Sprintf("飞书配置聚合卡片发送成功: %s (%d 套配置)", planCode, len(groups)), "feishu")
}
