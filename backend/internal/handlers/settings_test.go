package handlers

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestSettingsResponseOmitsSensitiveValues(t *testing.T) {
	response := toSettingsResponse(types.Config{
		AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", TgToken: "telegram",
		TgChatID: "chat", TgWebhookSecret: "webhook", FeishuAppID: "app-id",
		FeishuAppSecret: "feishu-secret", FeishuVerificationToken: "verify", FeishuEncryptKey: "encrypt",
	})
	if response.AppKey != "" || response.AppSecret != "" || response.ConsumerKey != "" || response.TgToken != "" || response.TgChatID != "" || response.TgWebhookSecret != "" || response.FeishuAppSecret != "" || response.FeishuVerificationToken != "" || response.FeishuEncryptKey != "" {
		t.Fatalf("sensitive values leaked: %+v", response)
	}
	if !response.AppKeyConfigured || !response.TelegramTokenConfigured || !response.FeishuAppSecretConfigured {
		t.Fatalf("configured flags missing: %+v", response)
	}
	if response.FeishuAppID != "app-id" {
		t.Fatalf("non-secret app id was removed: %q", response.FeishuAppID)
	}
	if strings.Contains(response.FeishuAppID, "secret") {
		t.Fatal("unexpected secret value in app id")
	}
}
