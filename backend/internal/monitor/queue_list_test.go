package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	goovh "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	ovhclient "github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/types"
)

func TestBuildQueueListMessagesFiltersAndGroupsWithoutMutation(t *testing.T) {
	state := &app.State{
		Accounts: []types.OVHAccount{
			{ID: "a", Name: "主账号", IsDefault: true},
			{ID: "b", Name: "备用账号"},
		},
		ServerPlans: []types.ServerPlan{{
			PlanCode: "plan", Name: "KS-A-1", Memory: "32 GB", Storage: "2 x 512 GB NVMe", Bandwidth: "500 Mbps",
			AvailableOptions: []types.ServerOption{{Value: "ram-64g", Label: "64 GB", Family: "memory"}},
		}},
		Queue: []types.QueueItem{
			{ID: "1", PlanCode: "plan", Datacenter: "gra", Status: "pending", AutoPay: true, Options: []string{"disk", "ram-32g"}},
			{ID: "2", AccountID: "a", PlanCode: "plan", Datacenter: "GRA", Status: "running", AutoPay: true, Options: []string{"ram-32g", "disk"}},
			{ID: "3", AccountID: "a", PlanCode: "plan", Datacenter: " rbx ", Status: "pending", AutoPay: true, Options: []string{" disk ", "ram-32g"}},
			{ID: "4", AccountID: "a", PlanCode: "plan", Datacenter: "gra", Status: "pending", Options: []string{"ram-32g", "disk"}},
			{ID: "5", AccountID: "a", PlanCode: "plan", Datacenter: "gra", Status: "running", AutoPay: true, Options: []string{"ram-64g"}},
			{ID: "6", AccountID: "b", PlanCode: "plan", Datacenter: "gra", Status: "pending", AutoPay: true, Options: []string{"ram-32g", "disk"}},
			{PlanCode: "excluded-paused", Status: "paused"},
			{PlanCode: "excluded-completed", Status: "completed"},
			{PlanCode: "excluded-failed", Status: "failed"},
			{PlanCode: "excluded-discontinued", Status: "running", Discontinued: true},
			{PlanCode: "excluded-proxy", Status: "pending", ProxyGuardPaused: true},
		},
	}
	before, _ := json.Marshal(state.Queue)
	mon := New(state)
	messages := mon.BuildQueueListMessages(context.Background())
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	got := messages[0]
	for _, want := range []string{
		"当前开启的抢购任务\n\n开启任务：4组\n抢购总数量：6台\n\n",
		"OVH 账号：主账号", "OVH 账号：备用账号", "型号：KS-A-1", "Plan Code：plan",
		"内存：32 GB", "内存：64 GB", "数据盘：2 x 512 GB NVMe", "带宽：500 Mbps",
		"数据中心：GRA，RBX", "抢购数量：GRA 2 台，RBX 1 台", "月费：暂不可用\n安装费：暂不可用\n总价：暂不可用",
		"自动付款：是", "自动付款：否",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list is missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "excluded-") || strings.Count(got, "Plan Code：plan") != 4 {
		t.Fatalf("filtering/grouping is incorrect: %s", got)
	}
	if strings.Count(got, "任务1：") != 2 || strings.Count(got, "任务3：") != 1 {
		t.Fatalf("account-local numbering is incorrect: %s", got)
	}
	after, _ := json.Marshal(state.Queue)
	if string(before) != string(after) {
		t.Fatal("listing changed queue data or option order")
	}
	if again := strings.Join(mon.BuildQueueListMessages(nil), "\n"); again != got {
		t.Fatal("list output is not deterministic")
	}
}

func TestBuildQueueListMessagesEmptyAndCanceled(t *testing.T) {
	for _, state := range []*app.State{nil, {}, {Queue: []types.QueueItem{{Status: "paused"}, {Status: "completed"}, {Status: "failed"}, {Status: "pending", Discontinued: true}, {Status: "running", ProxyGuardPaused: true}}}} {
		got := New(state).BuildQueueListMessages(nil)
		if len(got) != 1 || got[0] != "当前没有开启的抢购任务。" {
			t.Fatalf("empty reply = %#v", got)
		}
	}
	state := &app.State{Queue: []types.QueueItem{
		{AccountID: "missing", PlanCode: "one", Status: "pending"},
		{AccountID: "missing", PlanCode: "two", Status: "running"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := strings.Join(New(state).BuildQueueListMessages(ctx), "\n")
	for _, want := range []string{"开启任务：2组", "抢购总数量：2台", "Plan Code：one", "Plan Code：two", "OVH 账号：missing", "月费：暂不可用"} {
		if !strings.Contains(got, want) {
			t.Fatalf("canceled lookup omitted task information %q: %s", want, got)
		}
	}
}

func TestFormatQueueListMessagesSplitsWithoutLosingUTF8(t *testing.T) {
	items := make([]taskBroadcastItem, 0, 9)
	for i := 0; i < 9; i++ {
		items = append(items, taskBroadcastItem{
			AccountID: fmt.Sprintf("account-%d", i/3), Account: fmt.Sprintf("账号%d", i/3),
			Model: strings.Repeat("界", 900), PlanCode: fmt.Sprintf("plan-%d", i), AutoPay: true,
		})
	}
	messages := formatQueueListMessages(items, 18)
	if len(messages) < 2 {
		t.Fatal("long list was not split")
	}
	for i, message := range messages {
		if !utf8.ValidString(message) || len(message) > taskBroadcastMessageMaxBytes {
			t.Fatalf("part %d is invalid UTF-8 or too long: %d bytes", i+1, len(message))
		}
		want := fmt.Sprintf("当前开启的抢购任务（%d/%d）\n\n开启任务：9组\n抢购总数量：18台\n\nOVH 账号：", i+1, len(messages))
		if !strings.HasPrefix(message, want) {
			t.Fatalf("part %d has incorrect heading", i+1)
		}
	}
	all := strings.Join(messages, "\n")
	if strings.Count(all, "界") != 8100 {
		t.Fatal("split lost model characters")
	}
	for i := range items {
		if strings.Count(all, fmt.Sprintf("Plan Code：plan-%d", i)) != 1 {
			t.Fatalf("plan-%d is missing or duplicated", i)
		}
	}
}

func TestBuildQueueListMessagesUsesReadOnlyCatalog(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutating request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "mutation forbidden", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Ovh-Signature") != "" || r.Header.Get("X-Ovh-Consumer") != "" {
			t.Error("public catalog request must not require a signed account request")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/order/catalog/public/eco":
			reads.Add(1)
			_, _ = w.Write([]byte(`{"locale":{"currencyCode":"EUR"},"plans":[{"planCode":"plan","pricings":[{"interval":1,"intervalUnit":"month","mode":"default","price":1000000000,"tax":200000000},{"mode":"default","capacities":["installation"],"price":250000000}]}],"addons":[{"planCode":"ram","pricings":[{"interval":1,"intervalUnit":"month","mode":"default","price":300000000}]}]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpoint := "queue-list-test"
	goovh.Endpoints[endpoint] = server.URL
	t.Cleanup(func() { delete(goovh.Endpoints, endpoint) })
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	state := &app.State{
		DB:       database,
		Accounts: []types.OVHAccount{{ID: "a", Name: "测试账号", Endpoint: endpoint, Zone: "IE", AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", IsDefault: true}},
		Queue: []types.QueueItem{
			{AccountID: "a", PlanCode: "plan", Datacenter: "gra", Status: "pending", Options: []string{"ram"}},
			{AccountID: "a", PlanCode: "plan", Datacenter: "rbx", Status: "running", Options: []string{"ram"}},
			{AccountID: "a", PlanCode: "plan", Datacenter: "gra", Status: "pending", AutoPay: true, Options: []string{"ram"}},
		},
	}
	state.OVH = ovhclient.NewFactory(nil, state.FindAccount)
	got := strings.Join(New(state).BuildQueueListMessages(context.Background()), "\n")
	for _, want := range []string{"开启任务：2组", "抢购总数量：3台", "月费：€13.00/月", "安装费：€2.50", "总价：€15.50"} {
		if !strings.Contains(got, want) {
			t.Fatalf("catalog list missing %q: %s", want, got)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("catalog reads = %d, want cached read once", reads.Load())
	}
}

func TestBuildQueueListMessagesHonorsCatalogContext(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/order/catalog/public/eco" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	endpoint := "queue-list-context"
	goovh.Endpoints[endpoint] = server.URL
	t.Cleanup(func() { delete(goovh.Endpoints, endpoint) })

	state := &app.State{
		Accounts: []types.OVHAccount{{ID: "a", Name: "测试账号", Endpoint: endpoint, Zone: "IE", AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", IsDefault: true}},
		Queue:    []types.QueueItem{{AccountID: "a", PlanCode: "plan", Datacenter: "gra", Status: "pending"}},
	}
	state.OVH = ovhclient.NewFactory(nil, state.FindAccount)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	messages := New(state).BuildQueueListMessages(ctx)
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("list remained blocked after context cancellation: %s", elapsed)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("catalog request was not started")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("catalog request did not observe context cancellation")
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "抢购总数量：1台") || !strings.Contains(messages[0], "月费：暂不可用") {
		t.Fatalf("context cancellation lost task information: %#v", messages)
	}
}
