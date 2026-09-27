package handlers

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestSettingsResponseIncludesTaskBroadcastSettings(t *testing.T) {
	enabled, queueEnabled, vpsEnabled, reportEnabled := true, true, true, true
	response := toSettingsResponse(types.Config{
		TaskBroadcastEnabled:        &enabled,
		TaskBroadcastTime:           "18:30",
		TaskBroadcastQueueEnabled:   &queueEnabled,
		TaskBroadcastMonitorEnabled: &enabled,
		TaskBroadcastVPSEnabled:     &vpsEnabled,
		TaskBroadcastReportEnabled:  &reportEnabled,
	})
	if response.TaskBroadcastEnabled == nil || !*response.TaskBroadcastEnabled || response.TaskBroadcastTime != "18:30" || response.TaskBroadcastQueueEnabled == nil || !*response.TaskBroadcastQueueEnabled || response.TaskBroadcastMonitorEnabled == nil || !*response.TaskBroadcastMonitorEnabled || response.TaskBroadcastVPSEnabled == nil || !*response.TaskBroadcastVPSEnabled || response.TaskBroadcastReportEnabled == nil || !*response.TaskBroadcastReportEnabled {
		t.Fatalf("task broadcast settings were not returned: %+v", response)
	}
}

func TestNormalizeTaskBroadcastTime(t *testing.T) {
	for _, value := range []string{"00:00", "09:05", "23:59"} {
		if got, err := normalizeTaskBroadcastTime(value); err != nil || got != value {
			t.Fatalf("normalizeTaskBroadcastTime(%q) = %q, %v", value, got, err)
		}
	}
	if got, err := normalizeTaskBroadcastTime(" 08:30 "); err != nil || got != "08:30" {
		t.Fatalf("trimmed time = %q, %v", got, err)
	}
	if got, err := normalizeTaskBroadcastTime(""); err != nil || got != "" {
		t.Fatalf("empty time = %q, %v", got, err)
	}
	for _, value := range []string{"24:00", "12:60", "9:00", "noon"} {
		if _, err := normalizeTaskBroadcastTime(value); err == nil {
			t.Fatalf("normalizeTaskBroadcastTime(%q) unexpectedly succeeded", value)
		}
	}
}

func TestApplyTaskBroadcastPatch(t *testing.T) {
	oldEnabled, oldQueue, oldMonitor, oldVPS, oldReport := true, true, true, true, true
	newQueue, newReport := false, true
	cfg := types.Config{
		TaskBroadcastEnabled:        &oldEnabled,
		TaskBroadcastTime:           "09:00",
		TaskBroadcastQueueEnabled:   &oldQueue,
		TaskBroadcastMonitorEnabled: &oldMonitor,
		TaskBroadcastVPSEnabled:     &oldVPS,
		TaskBroadcastReportEnabled:  &oldReport,
	}
	patch := types.Config{TaskBroadcastQueueEnabled: &newQueue, TaskBroadcastReportEnabled: &newReport, TaskBroadcastTime: " 22:15 "}
	if err := applyTaskBroadcastPatch(&cfg, patch); err != nil {
		t.Fatal(err)
	}
	if cfg.TaskBroadcastEnabled == nil || !*cfg.TaskBroadcastEnabled || cfg.TaskBroadcastQueueEnabled == nil || *cfg.TaskBroadcastQueueEnabled || cfg.TaskBroadcastMonitorEnabled == nil || !*cfg.TaskBroadcastMonitorEnabled || cfg.TaskBroadcastVPSEnabled == nil || !*cfg.TaskBroadcastVPSEnabled || cfg.TaskBroadcastReportEnabled == nil || !*cfg.TaskBroadcastReportEnabled || cfg.TaskBroadcastTime != "22:15" {
		t.Fatalf("unexpected merged settings: %+v", cfg)
	}
}

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
