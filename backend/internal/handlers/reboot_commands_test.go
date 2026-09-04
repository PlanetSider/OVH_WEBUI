package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	goovh "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/storage"
	"github.com/ovh-webui/server/internal/types"
)

func newRebootTestState(t *testing.T, accounts ...types.OVHAccount) *app.State {
	t.Helper()
	dataDir := t.TempDir()
	database, err := db.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, account := range accounts {
		if err := database.UpsertAccount(account); err != nil {
			t.Fatal(err)
		}
	}
	paths := storage.Paths{DataDir: dataDir, CacheDir: filepath.Join(dataDir, "cache"), LogsDir: filepath.Join(dataDir, "logs")}
	state := app.NewState(paths, config.New(database), logger.New(filepath.Join(dataDir, "logs.json"), nil), database)
	if err := state.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestPrepareRebootFlowSkipsOnlyDefaultAccountAndUsesAlias(t *testing.T) {
	serviceName := "ns123456.ip-192-0-2.eu"
	var rebootCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/auth/time":
			_, _ = fmt.Fprintf(w, "%d", time.Now().Unix())
		case r.Method == http.MethodGet && r.URL.Path == "/dedicated/server":
			_ = json.NewEncoder(w).Encode([]string{serviceName})
		case r.Method == http.MethodGet && r.URL.Path == "/dedicated/server/"+serviceName:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name": serviceName, "datacenter": "gra3", "state": "ok",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/dedicated/server/"+serviceName+"/reboot":
			rebootCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"taskId": 42})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	endpointName := "reboot-flow-test"
	goovh.Endpoints[endpointName] = server.URL
	t.Cleanup(func() { delete(goovh.Endpoints, endpointName) })
	account := types.OVHAccount{
		ID: "account-default", Name: "主账号", Endpoint: endpointName, Zone: "FR",
		AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", IsDefault: true, CreatedAt: types.NowISO(),
	}
	state := newRebootTestState(t, account)
	if err := state.DB.UpsertAlias(account.ID, serviceName, "生产数据库"); err != nil {
		t.Fatal(err)
	}

	menu, err := prepareRebootFlow(state, "telegram", "user-1", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if menu.Stage != rebootStageServer {
		t.Fatalf("stage = %q, want %q", menu.Stage, rebootStageServer)
	}
	if len(menu.Payload.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(menu.Payload.Servers))
	}
	label := rebootServerLabel(menu.Payload.Servers[0])
	if label != "🇫🇷 GRA · 生产数据库" {
		t.Fatalf("server label = %q", label)
	}
	if strings.Contains(strings.ToLower(label), "ns123456") {
		t.Fatalf("technical service name leaked into label: %q", label)
	}

	selected, err := selectRebootServer(state, menu.FlowID, "telegram", "user-1", "chat-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ServiceName != serviceName {
		t.Fatalf("selected service = %q", selected.ServiceName)
	}
	message, ok := finishRebootFlow(state, menu.FlowID, "telegram", "user-1", "chat-1", true)
	if !ok || !strings.Contains(message, "生产数据库") || strings.Contains(strings.ToLower(message), "ns123456") {
		t.Fatalf("unexpected reboot result ok=%v message=%q", ok, message)
	}
	if got := rebootCalls.Load(); got != 1 {
		t.Fatalf("reboot calls = %d, want 1", got)
	}
	if _, ok := finishRebootFlow(state, menu.FlowID, "telegram", "user-1", "chat-1", true); ok {
		t.Fatal("duplicate confirmation unexpectedly succeeded")
	}
	if got := rebootCalls.Load(); got != 1 {
		t.Fatalf("duplicate confirmation sent another reboot: calls=%d", got)
	}
}

func TestPrepareRebootFlowListsAllAccountsWithoutCredentials(t *testing.T) {
	accounts := []types.OVHAccount{
		{ID: "default-id", Name: "主账号", Endpoint: "ovh-eu", Zone: "IE", AppKey: "secret-app-1", AppSecret: "secret-1", ConsumerKey: "secret-consumer-1", IsDefault: true, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "second-id", Name: "备用账号", Endpoint: "ovh-ca", Zone: "CA", AppKey: "secret-app-2", AppSecret: "secret-2", ConsumerKey: "secret-consumer-2", CreatedAt: "2026-01-02T00:00:00Z"},
	}
	state := newRebootTestState(t, accounts...)
	menu, err := prepareRebootFlow(state, "telegram", "user-1", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if menu.Stage != rebootStageAccount || len(menu.Payload.Accounts) != 2 {
		t.Fatalf("menu = %#v", menu)
	}
	row, ok, err := state.DB.GetBotRebootFlow(menu.FlowID, "telegram", "user-1", "chat-1")
	if err != nil || !ok {
		t.Fatalf("stored flow ok=%v err=%v", ok, err)
	}
	for _, secret := range []string{"secret-app-1", "secret-consumer-1", "secret-app-2", "secret-consumer-2"} {
		if strings.Contains(row.Payload, secret) {
			t.Fatalf("credential %q leaked into reboot flow", secret)
		}
	}
	keyboard := rebootAccountKeyboard(menu.FlowID, menu.Payload.Accounts)
	rows, ok := keyboard["inline_keyboard"].([][]telegramInlineButton)
	if !ok || len(rows) != 2 {
		t.Fatalf("account keyboard = %#v", keyboard)
	}
	for _, buttons := range rows {
		if len(buttons) != 1 || len([]byte(buttons[0].CallbackData)) > 64 {
			t.Fatalf("invalid Telegram account button: %#v", buttons)
		}
	}
}

func TestVisibleRebootServerNameNeverShowsNSPrefix(t *testing.T) {
	cases := []struct {
		alias, ovhName string
		want           string
	}{
		{alias: "自定义名称", ovhName: "ns1.example", want: "自定义名称"},
		{alias: "ns-local-alias", ovhName: "web-01", want: "web-01"},
		{alias: "", ovhName: "ns1.example", want: "未设置自定义名称 #3"},
	}
	for _, tc := range cases {
		if got := visibleRebootServerName(tc.alias, tc.ovhName, 3); got != tc.want {
			t.Errorf("visibleRebootServerName(%q, %q) = %q, want %q", tc.alias, tc.ovhName, got, tc.want)
		}
	}
}

func TestRebootDatacenterBadge(t *testing.T) {
	cases := map[string]string{
		"gra3": "🇫🇷 GRA",
		"bhs":  "🇨🇦 BHS",
		"waw1": "🇵🇱 WAW",
		"ynm":  "🇮🇳 YNM",
		"":     "🌐 UNK",
	}
	for input, want := range cases {
		if got := rebootDatacenterBadge(input); got != want {
			t.Errorf("rebootDatacenterBadge(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRebootConfirmationTextDoesNotExposeServiceName(t *testing.T) {
	server := rebootServerChoice{
		AccountName: "主账号", ServiceName: "ns123456.ip-192-0-2.eu", DisplayName: "生产数据库",
		Datacenter: "bhs", State: "ok",
	}
	text := rebootConfirmationText(server)
	if strings.Contains(strings.ToLower(text), "ns123456") {
		t.Fatalf("technical service name leaked into confirmation: %q", text)
	}
	if !strings.Contains(text, "🇨🇦 BHS · 生产数据库") {
		t.Fatalf("confirmation lacks formatted server label: %q", text)
	}
}
