package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	requestTimeWindow  = 5 * time.Minute
	maxSignedBodyBytes = 2 << 20
	maxNonceLength     = 128
	maxNonceEntries    = 10000
)

// Config 认证配置（从环境变量初始化）。
// Enabled 必须为 true；保留字段是为了让调用方在升级时显式配置，
// 但不会再允许通过配置关闭 API 鉴权。
type Config struct {
	APIKey  string
	Enabled bool
	// WhitelistPaths 跳过验证的路径。仅用于健康检查和第三方 webhook，
	// 这些入口必须由各自的协议认证保护。
	WhitelistPaths map[string]struct{}
}

// DefaultWhitelist 不需要 X-API-Key 即可访问的路径。
func DefaultWhitelist() map[string]struct{} {
	return map[string]struct{}{
		"/health":                   {},
		"/api/health":               {},
		"/api/version":              {},
		"/api/version/check-update": {},
		"/api/telegram/webhook":     {},
		"/api/feishu/events":        {},
		"/api/feishu/card-action":   {},
	}
}

// ValidateAPIKeyStrength 检查 API_SECRET_KEY 是否满足生产最低强度。
// 要求至少 8 个字符，同时包含 ASCII 英文大写、小写和数字。
func ValidateAPIKeyStrength(key string) error {
	if len(key) < 8 {
		return fmt.Errorf("API_SECRET_KEY 至少需要 8 个字符")
	}
	if len(key) > 256 {
		return fmt.Errorf("API_SECRET_KEY 不能超过 256 个字符")
	}
	var upper, lower, digit bool
	for _, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	if !upper || !lower || !digit {
		return fmt.Errorf("API_SECRET_KEY 必须同时包含英文大写、英文小写和数字")
	}
	return nil
}

var usedNonces = struct {
	sync.Mutex
	items map[string]time.Time
}{items: make(map[string]time.Time)}

func consumeNonce(nonce string, now time.Time) bool {
	nonce = strings.TrimSpace(nonce)
	if nonce == "" || len(nonce) > maxNonceLength {
		return false
	}
	usedNonces.Lock()
	defer usedNonces.Unlock()
	for value, expiresAt := range usedNonces.items {
		if !expiresAt.After(now) {
			delete(usedNonces.items, value)
		}
	}
	if _, exists := usedNonces.items[nonce]; exists {
		return false
	}
	if len(usedNonces.items) >= maxNonceEntries {
		return false
	}
	usedNonces.items[nonce] = now.Add(requestTimeWindow)
	return true
}

func signedPayload(method, path, timestamp, nonce string, body []byte) []byte {
	prefix := strings.Join([]string{strings.ToUpper(method), path, timestamp, nonce}, "\n") + "\n"
	payload := make([]byte, 0, len(prefix)+len(body))
	payload = append(payload, prefix...)
	return append(payload, body...)
}

// SignRequest 生成前端使用的请求签名，便于非浏览器客户端复用相同协议。
func SignRequest(apiKey, method, path, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write(signedPayload(method, path, timestamp, nonce, body))
	return hex.EncodeToString(mac.Sum(nil))
}

func readSignedBody(c *gin.Context) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxSignedBodyBytes+1))
	if err != nil || len(body) > maxSignedBodyBytes {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": "Request body is too large",
			"code":  "REQUEST_BODY_TOO_LARGE",
		})
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

// Middleware Gin 中间件：验证 X-API-Key 以及绑定请求体的 HMAC 签名。
func Middleware(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cfg.Enabled {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "API authentication is misconfigured",
				"code":  "AUTH_MISCONFIGURED",
			})
			return
		}

		// CORS 预检不携带业务凭据，由 CORS 中间件处理。
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api/") {
			c.Next()
			return
		}

		if _, ok := cfg.WhitelistPaths[path]; ok {
			c.Next()
			return
		}

		key := c.GetHeader("X-API-Key")
		if key == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Missing API key",
				"message": "缺少API密钥，请通过官方前端访问",
				"code":    "NO_API_KEY",
			})
			return
		}
		want := []byte(cfg.APIKey)
		got := []byte(key)
		if len(got) != len(want) || subtleConstantTimeCompare(got, want) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Invalid API key",
				"message": "API密钥无效，禁止访问",
				"code":    "INVALID_API_KEY",
			})
			return
		}

		ts := c.GetHeader("X-Request-Time")
		nonce := c.GetHeader("X-Request-Nonce")
		signature := c.GetHeader("X-Request-Signature")
		if ts == "" || nonce == "" || signature == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Missing request signature",
				"message": "缺少请求签名",
				"code":    "NO_REQUEST_SIGNATURE",
			})
			return
		}
		reqMs, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Invalid request timestamp",
				"message": "请求时间戳格式无效",
				"code":    "INVALID_REQUEST_TIME",
			})
			return
		}
		diff := time.Now().UnixMilli() - reqMs
		if diff < 0 {
			diff = -diff
		}
		if diff > requestTimeWindow.Milliseconds() {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Request expired",
				"message": "请求已过期（时间戳验证失败）",
				"code":    "TIMESTAMP_EXPIRED",
			})
			return
		}
		if len(nonce) > maxNonceLength {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid request nonce",
				"code":  "INVALID_REQUEST_NONCE",
			})
			return
		}
		body, ok := readSignedBody(c)
		if !ok {
			return
		}
		target := c.Request.URL.EscapedPath()
		if target == "" {
			target = "/"
		}
		if c.Request.URL.RawQuery != "" {
			target += "?" + c.Request.URL.RawQuery
		}
		expected := SignRequest(cfg.APIKey, c.Request.Method, target, ts, nonce, body)
		provided, err := hex.DecodeString(signature)
		wantSignature, _ := hex.DecodeString(expected)
		if err != nil || len(provided) != len(wantSignature) || !hmac.Equal(provided, wantSignature) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Invalid request signature",
				"message": "请求签名无效",
				"code":    "INVALID_REQUEST_SIGNATURE",
			})
			return
		}
		if !consumeNonce(nonce, time.Now()) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "Request nonce already used",
				"message": "请求已处理或 nonce 无效",
				"code":    "REQUEST_REPLAYED",
			})
			return
		}
		c.Next()
	}
}

// subtleConstantTimeCompare 保持长度检查与比较分离，避免长度差异影响比较逻辑。
func subtleConstantTimeCompare(a, b []byte) int {
	if len(a) != len(b) {
		return 0
	}
	var result byte
	for i := range a {
		result |= a[i] ^ b[i]
	}
	if result == 0 {
		return 1
	}
	return 0
}
