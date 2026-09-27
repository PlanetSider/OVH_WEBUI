package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ovh-webui/server/internal/qqbot"
	"github.com/ovh-webui/server/internal/types"
)

func TestHandleQQMessageEnforcesPrivateAndGroupCommandBoundaries(t *testing.T) {
	state := newRebootTestState(t)
	cfg := state.Config.Get()
	cfg.QQAppID = "app-id"
	cfg.QQAppSecret = "app-secret"
	cfg.QQUserOpenIDs = []string{"admin-1"}
	cfg.QQGroupOpenIDs = []string{"group-1"}
	if err := state.Config.Set(cfg); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_in":7200,"code":0}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := qqbot.New(state.Config.Get, server.Client(), server.URL)

	HandleQQMessage(nil, state, nil, qqbot.MessageEvent{
		ID: "ignored", Content: "/help", UserOpenID: "not-admin",
	}, client)
	HandleQQMessage(nil, state, nil, qqbot.MessageEvent{
		ID: "private", Content: "/help", UserOpenID: "admin-1",
	}, client)
	HandleQQMessage(nil, state, nil, qqbot.MessageEvent{
		ID: "group-denied", Content: "/pay 1", GroupOpenID: "group-1", UserOpenID: "member-1",
	}, client)
	HandleQQMessage(nil, state, nil, qqbot.MessageEvent{
		ID: "group-allowed", Content: "<@bot> /库存 24ska01", GroupOpenID: "group-1", UserOpenID: "member-1",
	}, client)
	HandleQQMessage(nil, state, nil, qqbot.MessageEvent{
		ID: "group-ignored", Content: "/stock 24ska01", GroupOpenID: "other-group", UserOpenID: "member-1",
	}, client)

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 3 {
		t.Fatalf("reply count = %d, paths=%#v", len(paths), paths)
	}
	if paths[0] != "/v2/users/admin-1/messages" {
		t.Fatalf("private reply path = %q", paths[0])
	}
	if paths[1] != "/v2/groups/group-1/messages" || paths[2] != "/v2/groups/group-1/messages" {
		t.Fatalf("group reply paths = %#v", paths)
	}
	if bodies[0]["msg_id"] != "private" || bodies[1]["msg_id"] != "group-denied" || bodies[2]["msg_id"] != "group-allowed" {
		t.Fatalf("reply references = %#v", bodies)
	}
	if !strings.Contains(bodies[1]["content"].(string), "仅支持 /stock、/price") {
		t.Fatalf("group denied reply = %#v", bodies[1])
	}
	if !strings.Contains(bodies[2]["content"].(string), "未配置任何 OVH 账户") {
		t.Fatalf("group allowed reply = %#v", bodies[2])
	}
}

func TestHandleQQMessageIgnoresDisabledQQBot(t *testing.T) {
	state := newRebootTestState(t)
	cfg := state.Config.Get()
	cfg.QQAppID = "app-id"
	cfg.QQAppSecret = "app-secret"
	cfg.QQUserOpenIDs = []string{"admin-1"}
	disabled := false
	cfg.QQNotificationsEnabled = &disabled
	if err := state.Config.Set(cfg); err != nil {
		t.Fatal(err)
	}
	client := qqbot.New(state.Config.Get, nil, "http://127.0.0.1")
	HandleQQMessage(nil, state, nil, qqbot.MessageEvent{ID: "disabled", Content: "/help", UserOpenID: "admin-1"}, client)
}

func TestNormalizeQQCommandText(t *testing.T) {
	cases := map[string]string{
		"/库存 24ska01":            "/库存 24ska01",
		"<@bot> /价格 24ska01 gra": "/价格 24ska01 gra",
		"@OVHBot /help":          "/help",
	}
	for input, want := range cases {
		if got := normalizeQQCommandText(input); got != want {
			t.Errorf("normalizeQQCommandText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQQAccountSwitchUsesNumberedTextFlow(t *testing.T) {
	state := newRebootTestState(t,
		types.OVHAccount{ID: "account-1", Name: "主账号", Endpoint: "ovh-eu", Zone: "IE", IsDefault: true},
		types.OVHAccount{ID: "account-2", Name: "备用账号", Endpoint: "ovh-ca", Zone: "CA"},
	)
	choices := qqAccountCommandText(state, []string{"switch"})
	if !strings.Contains(choices, "/account switch 序号") || !strings.Contains(choices, "1. ✓ 主账号") {
		t.Fatalf("choices = %q", choices)
	}
	result := qqAccountCommandText(state, []string{"switch", "2"})
	if !strings.Contains(result, "已切换") || !strings.Contains(result, "备用账号") {
		t.Fatalf("switch result = %q", result)
	}
	account, ok := state.FindAccount("")
	if !ok || account.ID != "account-2" {
		t.Fatalf("default account = %#v, ok=%v", account, ok)
	}
}
