package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestValidateAPIKeyStrength(t *testing.T) {
	hexKey := strings.Repeat("0123456789abcdef", 4)
	valid := []string{"Abcdefg1", "A1lowercase", hexKey, strings.ToUpper(hexKey), "Ab1" + strings.Repeat("x", 253)}
	for _, key := range valid {
		if err := ValidateAPIKeyStrength(key); err != nil {
			t.Errorf("ValidateAPIKeyStrength(%q) = %v", key, err)
		}
	}
	invalid := []string{
		"",
		"Abc1234",
		"abcdefg1",
		"ABCDEFG1",
		"Abcdefgh",
		"Abc1",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("g", 64),
		hexKey[:63] + "g",
		"Ab1" + strings.Repeat("x", 254),
	}
	for _, key := range invalid {
		if err := ValidateAPIKeyStrength(key); err == nil {
			t.Errorf("ValidateAPIKeyStrength(%q) unexpectedly succeeded", key)
		}
	}
}

func TestGeneratedAPIKeyAuthenticatesSignedRequests(t *testing.T) {
	var rawKey [32]byte
	if _, err := rand.Read(rawKey[:]); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(rawKey[:])
	if err := ValidateAPIKeyStrength(key); err != nil {
		t.Fatalf("generated key rejected: %v", err)
	}

	wrongKey := "0" + key[1:]
	if key[0] == '0' {
		wrongKey = "1" + key[1:]
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Middleware(Config{APIKey: key, Enabled: true, WhitelistPaths: map[string]struct{}{}}))
	router.GET("/api/stats", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, test := range []struct {
		name          string
		requestKey    string
		signed        bool
		wantStatus    int
		wantErrorCode string
	}{
		{"valid", key, true, http.StatusOK, ""},
		{"wrong-key", wrongKey, true, http.StatusUnauthorized, "INVALID_API_KEY"},
		{"missing-signature", key, false, http.StatusUnauthorized, "NO_REQUEST_SIGNATURE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
			request.Header.Set("X-API-Key", test.requestKey)
			if test.signed {
				timestamp := formatTimestamp(time.Now().UnixMilli())
				nonce := t.Name()
				request.Header.Set("X-Request-Time", timestamp)
				request.Header.Set("X-Request-Nonce", nonce)
				request.Header.Set("X-Request-Signature", SignRequest(test.requestKey, http.MethodGet, "/api/stats", timestamp, nonce, nil))
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantErrorCode != "" {
				var result struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Code != test.wantErrorCode {
					t.Fatalf("code = %s, want %s", result.Code, test.wantErrorCode)
				}
			}
		})
	}
}

func TestSignRequestAndNonceRejectReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := "Abcdefg1"
	router := gin.New()
	router.Use(Middleware(Config{APIKey: key, Enabled: true, WhitelistPaths: map[string]struct{}{}}))
	router.POST("/api/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	timestamp := time.Now().UnixMilli()
	nonce := "nonce-auth-test"
	body := []byte(`{"ok":true}`)
	signature := SignRequest(key, http.MethodPost, "/api/test?account=acct-1", formatTimestamp(timestamp), nonce, body)
	request := httptest.NewRequest(http.MethodPost, "/api/test?account=acct-1", strings.NewReader(string(body)))
	request.Header.Set("X-API-Key", key)
	request.Header.Set("X-Request-Time", formatTimestamp(timestamp))
	request.Header.Set("X-Request-Nonce", nonce)
	request.Header.Set("X-Request-Signature", signature)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("first request status = %d, want %d", response.Code, http.StatusNoContent)
	}

	replay := httptest.NewRequest(http.MethodPost, "/api/test?account=acct-1", strings.NewReader(string(body)))
	replay.Header = request.Header.Clone()
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want %d", replayResponse.Code, http.StatusUnauthorized)
	}
	var replayResult struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult.Code != "REQUEST_REPLAYED" {
		t.Fatalf("replay code = %s, want REQUEST_REPLAYED", replayResult.Code)
	}
}

func TestSignedRequestRejectsTamperedQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := "Abcdefg2"
	router := gin.New()
	router.Use(Middleware(Config{APIKey: key, Enabled: true, WhitelistPaths: map[string]struct{}{}}))
	router.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	timestamp := time.Now().UnixMilli()
	nonce := "nonce-query-tamper"
	signature := SignRequest(key, http.MethodGet, "/api/test?account=acct-1", formatTimestamp(timestamp), nonce, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/test?account=acct-2", nil)
	request.Header.Set("X-API-Key", key)
	request.Header.Set("X-Request-Time", formatTimestamp(timestamp))
	request.Header.Set("X-Request-Nonce", nonce)
	request.Header.Set("X-Request-Signature", signature)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("tampered query status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestSignedRequestRejectsExpiredTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := "Abcdefg3"
	router := gin.New()
	router.Use(Middleware(Config{APIKey: key, Enabled: true, WhitelistPaths: map[string]struct{}{}}))
	router.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	timestamp := time.Now().Add(-requestTimeWindow - time.Second).UnixMilli()
	nonce := "nonce-expired"
	signature := SignRequest(key, http.MethodGet, "/api/test", formatTimestamp(timestamp), nonce, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	request.Header.Set("X-API-Key", key)
	request.Header.Set("X-Request-Time", formatTimestamp(timestamp))
	request.Header.Set("X-Request-Nonce", nonce)
	request.Header.Set("X-Request-Signature", signature)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired timestamp status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestProtectedRequestRequiresSignatureHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := "Abcdefg4"
	router := gin.New()
	router.Use(Middleware(Config{APIKey: key, Enabled: true, WhitelistPaths: map[string]struct{}{}}))
	router.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	request.Header.Set("X-API-Key", key)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature headers status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func formatTimestamp(value int64) string {
	return strconv.FormatInt(value, 10)
}
