package weixin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientHeadersAndBaseInfo(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/ilink/bot/getupdates" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		if got := request.Header.Get("AuthorizationType"); got != "ilink_bot_token" {
			t.Fatalf("authorization type = %q", got)
		}
		if got := request.Header.Get("iLink-App-Id"); got != "bot" {
			t.Fatalf("app id = %q", got)
		}
		if got := request.Header.Get("iLink-App-ClientVersion"); got != "131584" {
			t.Fatalf("client version = %q", got)
		}
		uin, err := base64.StdEncoding.DecodeString(request.Header.Get("X-WECHAT-UIN"))
		if err != nil || strings.TrimSpace(string(uin)) == "" {
			t.Fatalf("invalid X-WECHAT-UIN: %q", request.Header.Get("X-WECHAT-UIN"))
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte(`{"ret":0,"get_updates_buf":"next","msgs":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL)
	response, err := client.GetUpdates(context.Background(), Credentials{
		AccountID: "bot", Token: "secret", BaseURL: server.URL, UserID: "user",
	}, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if response.SyncBuf != "next" {
		t.Fatalf("sync buf = %q", response.SyncBuf)
	}
	if received["get_updates_buf"] != "cursor" {
		t.Fatalf("get_updates_buf = %#v", received["get_updates_buf"])
	}
	baseInfo, ok := received["base_info"].(map[string]any)
	if !ok || baseInfo["channel_version"] != "2.2.0" {
		t.Fatalf("base_info = %#v", received["base_info"])
	}
}

func TestQRCodeUsesFullScanContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("bot_type") != "3" {
			t.Fatalf("bot_type = %q", request.URL.Query().Get("bot_type"))
		}
		_, _ = writer.Write([]byte(`{"qrcode":"raw-token","qrcode_img_content":"https://weixin.example/scan"}`))
	}))
	defer server.Close()
	client := NewClient(server.Client(), server.URL)
	response, err := client.GetBotQRCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response.ImageContent != "https://weixin.example/scan" {
		t.Fatalf("image content = %q", response.ImageContent)
	}
}

func TestAPIResultStaleAndRateLimit(t *testing.T) {
	if !(&APIResult{ErrCode: intPointer(-14)}).staleSession() {
		t.Fatal("-14 should be stale")
	}
	if !(&APIResult{Ret: intPointer(-2), ErrMsg: "unknown error"}).staleSession() {
		t.Fatal("-2 unknown error should be stale")
	}
	if !(&APIResult{Ret: intPointer(-2), ErrMsg: "freq limit"}).rateLimited() {
		t.Fatal("-2 freq limit should be rate limited")
	}
}

func TestClientAcceptsNumericAndStringMessageIDs(t *testing.T) {
	for _, payload := range []string{
		`{"ret":0,"msgs":[{"message_id":1234567890123456789}]}`,
		`{"ret":0,"msgs":[{"message_id":"1234567890123456789"}]}`,
	} {
		var response UpdatesResponse
		if err := json.Unmarshal([]byte(payload), &response); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if got := string(response.Messages[0].MessageID); got != "1234567890123456789" {
			t.Fatalf("message id = %q", got)
		}
	}
}

func intPointer(value int) *int { return &value }
