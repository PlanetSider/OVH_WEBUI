package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ovh-webui/server/internal/types"
)

func testConfig() types.Config {
	enabled := true
	return types.Config{
		QQAppID:                "app-id",
		QQAppSecret:            "app-secret",
		QQNotificationsEnabled: &enabled,
		QQUserOpenIDs:          []string{"user-1"},
		QQGroupOpenIDs:         []string{"group-1"},
		QQChannelTargets:       []types.QQChannelTarget{{GuildID: "guild-1", ChannelID: "channel-1"}},
	}
}

func TestClientRoutesDefaultAndMonitorMessages(t *testing.T) {
	cfg := testConfig()
	var mu sync.Mutex
	var tokenRequests int
	var messagePaths []string
	var messageBodies []map[string]any

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway: %v", err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}}); err != nil {
			t.Errorf("write hello: %v", err)
			return
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read identify: %v", err)
			return
		}
		var identify gatewayPayload
		if err := json.Unmarshal(raw, &identify); err != nil {
			t.Errorf("decode identify: %v", err)
			return
		}
		if identify.Op != 2 {
			t.Errorf("gateway op = %d, want identify (2)", identify.Op)
		}
		var identifyData gatewayIdentify
		if err := json.Unmarshal(identify.D, &identifyData); err != nil {
			t.Errorf("decode identify data: %v", err)
		}
		if identifyData.Token != "QQBot token-1" {
			t.Errorf("identify token = %q, want QQBot prefix", identifyData.Token)
		}
		if err := conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{}}); err != nil {
			t.Errorf("write ready: %v", err)
			return
		}
		for {
			var payload gatewayPayload
			if err := conn.ReadJSON(&payload); err != nil {
				return
			}
			if payload.Op == 1 {
				_ = conn.WriteJSON(map[string]any{"op": 11, "d": nil})
			}
		}
	}))
	defer wsServer.Close()
	gatewayURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			mu.Lock()
			tokenRequests++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_in":7200,"code":0}`))
			return
		}
		if r.URL.Path == "/gateway" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"` + gatewayURL + `"}`))
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "QQBot token-1" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		mu.Lock()
		messagePaths = append(messagePaths, r.URL.Path)
		messageBodies = append(messageBodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"message-1"}`))
	}))
	defer server.Close()

	client := New(func() types.Config { return cfg }, server.Client(), server.URL)
	defer client.Close()
	if !client.SendDefaultWithContext(context.Background(), "full notification") {
		t.Fatal("default notification should send to user")
	}
	mu.Lock()
	if got := messagePaths; len(got) != 1 || got[0] != "/v2/users/user-1/messages" {
		t.Fatalf("default paths = %#v", got)
	}
	mu.Unlock()
	if !client.SendMonitor("monitor notification") {
		t.Fatal("monitor notification should send to all targets")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/v2/users/user-1/messages", "/v2/groups/group-1/messages", "/channels/channel-1/messages"}
	if len(messagePaths) != 4 {
		t.Fatalf("message paths = %#v", messagePaths)
	}
	for i, path := range want {
		if messagePaths[i+1] != path {
			t.Errorf("monitor path[%d] = %q, want %q", i, messagePaths[i+1], path)
		}
	}
	if tokenRequests != 1 {
		t.Errorf("token requests = %d, want one cached token", tokenRequests)
	}
	for i, body := range messageBodies {
		if body["content"] == "" {
			t.Errorf("message body[%d] missing content: %#v", i, body)
		}
		if i != 3 && body["msg_type"] != float64(0) {
			t.Errorf("message body[%d] missing msg_type=0: %#v", i, body)
		}
		if i == 3 {
			if _, exists := body["msg_type"]; exists {
				t.Errorf("channel body unexpectedly contains msg_type: %#v", body)
			}
		}
	}
}

func TestGatewayReconnectsForSubsequentChannelMessage(t *testing.T) {
	cfg := testConfig()
	cfg.QQUserOpenIDs = nil
	cfg.QQGroupOpenIDs = nil
	var connections int32
	var messages int32
	firstClosed := make(chan struct{})

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection := atomic.AddInt32(&connections, 1)
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway: %v", err)
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		var identify gatewayPayload
		if err := conn.ReadJSON(&identify); err != nil {
			t.Errorf("read identify: %v", err)
			return
		}
		if identify.Op != 2 {
			t.Errorf("gateway op = %d, want identify (2)", identify.Op)
		}
		var identifyData gatewayIdentify
		if err := json.Unmarshal(identify.D, &identifyData); err != nil {
			t.Errorf("decode identify data: %v", err)
		}
		if identifyData.Token != "QQBot token-1" {
			t.Errorf("identify token = %q, want QQBot prefix", identifyData.Token)
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{}})
		if connection == 1 {
			time.Sleep(25 * time.Millisecond)
			close(firstClosed)
			return
		}
		for {
			var payload gatewayPayload
			if err := conn.ReadJSON(&payload); err != nil {
				return
			}
			if payload.Op == 1 {
				_ = conn.WriteJSON(map[string]any{"op": 11, "d": nil})
			}
		}
	}))
	defer wsServer.Close()
	gatewayURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_in":7200,"code":0}`))
		case "/gateway":
			_, _ = w.Write([]byte(`{"url":"` + gatewayURL + `"}`))
		default:
			atomic.AddInt32(&messages, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"message-1"}`))
		}
	}))
	defer server.Close()

	client := New(func() types.Config { return cfg }, server.Client(), server.URL)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ok := client.SendMonitorWithContext(ctx, "first"); !ok {
		t.Fatal("first channel notification failed")
	}
	select {
	case <-firstClosed:
	case <-ctx.Done():
		t.Fatal("first Gateway connection did not close")
	}
	waitForGatewayOffline(t, client, ctx)
	if ok := client.SendMonitorWithContext(ctx, "second"); !ok {
		t.Fatal("second channel notification did not recover after reconnect")
	}
	if got := atomic.LoadInt32(&connections); got < 2 {
		t.Fatalf("gateway connections = %d, want at least 2", got)
	}
	if got := atomic.LoadInt32(&messages); got != 2 {
		t.Fatalf("channel messages = %d, want 2", got)
	}
}
func TestDefaultWithOnlyGroupAndChannelTargetsIsSuppressed(t *testing.T) {
	cfg := testConfig()
	cfg.QQUserOpenIDs = nil
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Errorf("unexpected QQ request: %s", r.URL.Path)
	}))
	defer server.Close()
	client := New(func() types.Config { return cfg }, server.Client(), server.URL)
	if !client.SendDefault("ordinary notification") {
		t.Fatal("group/channel-only default notification should be intentionally suppressed")
	}
	if called {
		t.Fatal("suppressed default notification made an HTTP request")
	}
}

func waitForGatewayOffline(t *testing.T, client *Client, ctx context.Context) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		client.gatewayMu.Lock()
		session := client.gateway
		online := session != nil && session.isOnline()
		client.gatewayMu.Unlock()
		if !online {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Gateway offline: %v", ctx.Err())
		case <-deadline.C:
			t.Fatal("client did not observe Gateway disconnect")
		case <-poll.C:
		}
	}
}

func TestTokenResponseCodeErrorOnHTTP200(t *testing.T) {
	cfg := testConfig()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1001,"message":"invalid credentials"}`))
	}))
	defer server.Close()
	client := New(func() types.Config { return cfg }, server.Client(), server.URL)
	if err := client.SendTest(context.Background()); err == nil || !strings.Contains(err.Error(), "code=1001") {
		t.Fatalf("error = %v, want token code", err)
	}
}

func TestOpenAPIErrorUsesErrCode(t *testing.T) {
	cfg := testConfig()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_in":7200,"code":0}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"err_code":40013,"message":"permission denied","trace_id":"trace-1"}`))
	}))
	defer server.Close()
	client := New(func() types.Config { return cfg }, server.Client(), server.URL)
	if ok := client.SendDefault("message"); ok {
		t.Fatal("forbidden OpenAPI request should fail")
	}
	if _, err := client.send(context.Background(), "message", false); err == nil || !strings.Contains(err.Error(), "code=40013") || !strings.Contains(err.Error(), "trace-1") {
		t.Fatalf("error = %v, want err_code and trace id", err)
	}
}

func TestTokenRefreshBeforeExpiry(t *testing.T) {
	cfg := testConfig()
	var mu sync.Mutex
	tokens := []string{"token-1", "token-2"}
	tokenRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			mu.Lock()
			index := tokenRequests
			tokenRequests++
			mu.Unlock()
			if index >= len(tokens) {
				index = len(tokens) - 1
			}
			_, _ = w.Write([]byte(`{"access_token":"` + tokens[index] + `","expires_in":70,"code":0}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New(func() types.Config { return cfg }, server.Client(), server.URL)
	current := time.Unix(1000, 0)
	client.now = func() time.Time { return current }
	if !client.SendDefault("first") {
		t.Fatal("first send failed")
	}
	current = current.Add(1 * time.Second)
	if !client.SendDefault("cached") {
		t.Fatal("cached send failed")
	}
	current = current.Add(10 * time.Second)
	if !client.SendDefault("refresh") {
		t.Fatal("refreshed send failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenRequests != 2 {
		t.Fatalf("token requests = %d, want refresh at 60-second safety window", tokenRequests)
	}
}
