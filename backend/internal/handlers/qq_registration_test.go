package handlers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
)

func TestDecryptQQBotSecret(t *testing.T) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte("client-secret"), nil)
	secret, err := decryptQQBotSecret(
		base64.StdEncoding.EncodeToString(sealed),
		base64.StdEncoding.EncodeToString(key),
	)
	if err != nil || secret != "client-secret" {
		t.Fatalf("decryptQQBotSecret() = %q, %v", secret, err)
	}
	if _, err := decryptQQBotSecret("not-base64", base64.StdEncoding.EncodeToString(key)); err == nil {
		t.Fatal("invalid ciphertext should fail")
	}
}

func TestPollQQRegistrationSavesCredentialsWithoutReturningSecret(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	state := &app.State{Config: config.New(database)}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte("client-secret"), nil)
	keyBase64 := base64.StdEncoding.EncodeToString(key)
	sessionID := "qq-registration-success"
	qqRegistrationSessions.Lock()
	qqRegistrationSessions.items[sessionID] = &qqRegistrationSession{
		TaskID: "task-1", BindKey: keyBase64,
		ExpiresAt: time.Now().Add(time.Minute), NextPollAt: time.Now().Add(-time.Second),
	}
	qqRegistrationSessions.Unlock()
	defer func() {
		qqRegistrationSessions.Lock()
		delete(qqRegistrationSessions.items, sessionID)
		qqRegistrationSessions.Unlock()
	}()

	originalCall := qqRegistrationCall
	qqRegistrationCall = func(_ context.Context, path string, _ any) (qqRegistrationBindResponse, error) {
		if path != qqRegistrationPollPath {
			t.Fatalf("unexpected QQ registration path: %s", path)
		}
		return qqRegistrationBindResponse{Data: qqRegistrationBindData{
			Status:           json.RawMessage("2"),
			AppID:            "app-id",
			BotEncryptSecret: base64.StdEncoding.EncodeToString(sealed),
			UserOpenID:       "user-openid",
		}}, nil
	}
	defer func() { qqRegistrationCall = originalCall }()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/qq/registration/:sessionId", PollQQRegistration(state))
	req := httptest.NewRequest(http.MethodGet, "/api/qq/registration/"+sessionID, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "client-secret") {
		t.Fatal("QQ AppSecret leaked in poll response")
	}
	cfg := state.Config.Get()
	if cfg.QQAppID != "app-id" || cfg.QQAppSecret != "client-secret" || !cfg.IsQQNotificationsEnabled() {
		t.Fatalf("credentials not saved correctly: %+v", cfg)
	}
	if len(cfg.QQUserOpenIDs) != 1 || cfg.QQUserOpenIDs[0] != "user-openid" {
		t.Fatalf("user OpenID not bound: %#v", cfg.QQUserOpenIDs)
	}
}

func TestStartQQRegistrationCreatesSession(t *testing.T) {
	originalCall := qqRegistrationCall
	qqRegistrationCall = func(_ context.Context, path string, payload any) (qqRegistrationBindResponse, error) {
		if path != qqRegistrationCreatePath {
			t.Fatalf("unexpected QQ registration path: %s", path)
		}
		request, ok := payload.(gin.H)
		if !ok || strings.TrimSpace(request["key"].(string)) == "" {
			t.Fatal("QQ bind key was not sent")
		}
		return qqRegistrationBindResponse{Data: qqRegistrationBindData{TaskID: "task-start-1"}}, nil
	}
	defer func() { qqRegistrationCall = originalCall }()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/qq/registration/start", StartQQRegistration(&app.State{}))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/qq/registration/start", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var body struct {
		SessionID string `json:"sessionId"`
		URL       string `json:"verificationUriComplete"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID == "" || !strings.Contains(body.URL, "q.qq.com/qqbot/openclaw/connect.html?task_id=task-start-1") {
		t.Fatalf("unexpected QQ registration response: %+v", body)
	}
	qqRegistrationSessions.Lock()
	_, exists := qqRegistrationSessions.items[body.SessionID]
	delete(qqRegistrationSessions.items, body.SessionID)
	qqRegistrationSessions.Unlock()
	if !exists {
		t.Fatal("QQ registration session was not saved")
	}
}
func TestPollQQRegistrationExpiredSession(t *testing.T) {
	sessionID := "qq-registration-expired"
	qqRegistrationSessions.Lock()
	qqRegistrationSessions.items[sessionID] = &qqRegistrationSession{ExpiresAt: time.Now().Add(-time.Second)}
	qqRegistrationSessions.Unlock()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/qq/registration/:sessionId", PollQQRegistration(&app.State{}))
	req := httptest.NewRequest(http.MethodGet, "/api/qq/registration/"+sessionID, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusGone)
	}
	qqRegistrationSessions.Lock()
	_, exists := qqRegistrationSessions.items[sessionID]
	qqRegistrationSessions.Unlock()
	if exists {
		t.Fatal("expired QQ registration session was not removed")
	}
}
