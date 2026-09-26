package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestValidateAPIKeyStrength(t *testing.T) {
	valid := []string{"Abcdefg1", "A1lowercase"}
	for _, key := range valid {
		if err := ValidateAPIKeyStrength(key); err != nil {
			t.Errorf("ValidateAPIKeyStrength(%q) = %v", key, err)
		}
	}
	invalid := []string{"", "Abc1234", "abcdefg1", "ABCDEFG1", "Abcdefgh", "Abc1"}
	for _, key := range invalid {
		if err := ValidateAPIKeyStrength(key); err == nil {
			t.Errorf("ValidateAPIKeyStrength(%q) unexpectedly succeeded", key)
		}
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

	replay := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(string(body)))
	replay.Header = request.Header.Clone()
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want %d", replayResponse.Code, http.StatusUnauthorized)
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
