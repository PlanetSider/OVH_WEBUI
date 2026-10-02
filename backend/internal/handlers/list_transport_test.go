package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

type listTestRoundTripper func(*http.Request) (*http.Response, error)

func (f listTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func listTestResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func longQueueItems(count int) []types.QueueItem {
	items := make([]types.QueueItem, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, types.QueueItem{
			PlanCode:   strings.Repeat("模型", 700) + string(rune('A'+i)),
			Datacenter: "gra",
			Status:     "pending",
		})
	}
	return items
}

func TestHandleTelegramListRepliesStayInOriginalConversation(t *testing.T) {
	state := newRebootTestState(t)
	cfg := state.Config.Get()
	cfg.TgToken = "test-token"
	cfg.TgChatID = "chat-1"
	if err := state.Config.Set(cfg); err != nil {
		t.Fatalf("set telegram test config: %v", err)
	}
	state.Queue = longQueueItems(5)

	var payloads []map[string]interface{}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = listTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.telegram.org" || req.URL.Path != "/bottest-token/sendMessage" {
			t.Fatalf("unexpected telegram endpoint: %s", req.URL.String())
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read telegram request: %v", err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode telegram request: %v", err)
		}
		payloads = append(payloads, payload)
		return listTestResponse(req, http.StatusOK, `{"ok":true}`), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	handleTelegramText(state, nil, "/list", "chat-1", "user-1", 42)
	if len(payloads) < 2 {
		t.Fatalf("list replies = %d, want multipart replies", len(payloads))
	}
	for _, payload := range payloads {
		if payload["chat_id"] != "chat-1" {
			t.Fatalf("reply chat_id = %#v, want original chat", payload["chat_id"])
		}
		if payload["reply_to_message_id"] != float64(42) {
			t.Fatalf("reply_to_message_id = %#v, want 42", payload["reply_to_message_id"])
		}
		if !strings.Contains(payload["text"].(string), "当前开启的抢购任务") {
			t.Fatalf("reply does not contain list title: %#v", payload["text"])
		}
	}

	beforeUnauthorized := len(payloads)
	handleTelegramText(state, nil, "/list", "other-chat", "other-user", 43)
	if len(payloads) != beforeUnauthorized+1 {
		t.Fatalf("unauthorized request produced %d replies, want one denial", len(payloads)-beforeUnauthorized)
	}
	denial := payloads[len(payloads)-1]
	if denial["chat_id"] != "other-chat" || strings.Contains(denial["text"].(string), "当前开启的抢购任务") {
		t.Fatalf("unauthorized reply leaked list data: %#v", denial)
	}
}

func TestProcessFeishuListRepliesStayWithOriginalOpenID(t *testing.T) {
	state := newRebootTestState(t)
	cfg := state.Config.Get()
	cfg.FeishuEnabled = true
	cfg.FeishuAppID = "test-app"
	cfg.FeishuAppSecret = "test-secret"
	cfg.FeishuDomain = "feishu"
	if err := state.Config.Set(cfg); err != nil {
		t.Fatalf("set feishu test config: %v", err)
	}
	state.Queue = longQueueItems(5)
	t.Setenv("FEISHU_ALLOWED_OPEN_IDS", "open-1")
	monitor.FeishuResetToken()

	var messagePayloads []map[string]interface{}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = listTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return listTestResponse(req, http.StatusOK, `{"code":0,"tenant_access_token":"tenant-token","expire":3600}`), nil
		case "/open-apis/im/v1/messages":
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read feishu request: %v", err)
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode feishu request: %v", err)
			}
			messagePayloads = append(messagePayloads, payload)
			return listTestResponse(req, http.StatusOK, `{"code":0}`), nil
		default:
			return listTestResponse(req, http.StatusNotFound, `{}`), nil
		}
	})
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
		monitor.FeishuResetToken()
	})

	processFeishuMessage(state, nil, map[string]interface{}{
		"event": map[string]interface{}{
			"operator": map[string]interface{}{"open_id": "open-1"},
			"message": map[string]interface{}{
				"chat_type": "p2p",
				"content":   `{"text":"/list"}`,
			},
		},
	})
	if len(messagePayloads) < 2 {
		t.Fatalf("feishu list replies = %d, want multipart replies", len(messagePayloads))
	}
	for _, payload := range messagePayloads {
		if payload["receive_id"] != "open-1" {
			t.Fatalf("receive_id = %#v, want original open_id", payload["receive_id"])
		}
		content, ok := payload["content"].(string)
		if !ok || !strings.Contains(content, "当前开启的抢购任务") {
			t.Fatalf("feishu reply does not contain list title: %#v", payload["content"])
		}
	}
}
