package weixin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractTextAndSplit(t *testing.T) {
	text := extractText([]MessageItem{
		{Type: itemTypeText, TextItem: &TextItem{Text: "hello"}},
		{Type: 2},
		{Type: itemTypeText, TextItem: &TextItem{Text: "world"}},
	})
	want := strings.Join([]string{"hello", "world"}, string(rune(10)))
	if text != want {
		t.Fatalf("text = %q", text)
	}
	chunks := splitText(strings.Repeat("中", 19), 8)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %#v", chunks)
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 8 {
			t.Fatalf("chunk too long: %q", chunk)
		}
	}
}

func TestPollLoginScrubsUnknownProviderStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"provider-secret-status"}`))
	}))
	defer server.Close()

	manager := &Manager{
		client: NewClient(server.Client(), server.URL),
		loginSessions: map[string]*loginSession{
			"session": {
				ID:        "session",
				QRCode:    "qr",
				BaseURL:   server.URL,
				ExpiresAt: time.Now().Add(time.Minute),
			},
		},
	}
	result, err := manager.PollLogin(context.Background(), "session")
	if err != nil {
		t.Fatalf("PollLogin error = %v", err)
	}
	if result.Error != "扫码状态异常，请重新尝试" {
		t.Fatalf("unknown status error = %q", result.Error)
	}
	if strings.Contains(result.Error, "provider-secret-status") {
		t.Fatalf("provider status leaked: %q", result.Error)
	}
}

func TestStatusNeverSerializesBotToken(t *testing.T) {
	data, err := json.Marshal(Status{
		Configured: true,
		AccountID:  "bot-id",
		UserID:     "user-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") {
		t.Fatalf("status leaked token field: %s", data)
	}
}
