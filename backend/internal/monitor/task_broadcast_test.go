package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

func TestFormatTaskBroadcastItem(t *testing.T) {
	item := taskBroadcastItem{
		Title:       "🛒 抢购任务",
		Account:     "主账号",
		Model:       "KS-1",
		PlanCode:    "24sk10",
		Memory:      "64GB",
		Storage:     "2x 480GB SSD",
		Bandwidth:   "1Gbps",
		Datacenters: "GRA",
		Monthly:     "€12.40/月",
		Install:     "€3.10",
		Total:       "€15.50",
		Extra: []broadcastField{
			{Label: "抢购数量", Value: "GRA 2 台"},
		},
		AutoPay: true,
	}
	got := formatTaskBroadcastItem(item)
	want := strings.Join([]string{
		"🛒 抢购任务",
		"",
		"OVH 账号：主账号",
		"型号：KS-1",
		"Plan Code：24sk10",
		"内存：64GB",
		"数据盘：2x 480GB SSD",
		"带宽：1Gbps",
		"数据中心：GRA",
		"月费：€12.40/月",
		"安装费：€3.10",
		"总价：€15.50",
		"抢购数量：GRA 2 台",
		"自动付款：是",
	}, "\n")
	if got != want {
		t.Fatalf("formatted task = %q, want %q", got, want)
	}
	if strings.Contains(got, "数据中心：GRA\n\n") || strings.Contains(got, "月费：€12.40/月\n\n") {
		t.Fatal("task fields contain an unexpected internal blank line")
	}
}

func TestFormatTaskBroadcastCategoryCombinesTasks(t *testing.T) {
	item := taskBroadcastItem{Account: "账号", Model: "型号", PlanCode: "plan", Datacenters: "GRA", Monthly: "€1", Install: "无", Total: "€1"}
	got := formatTaskBroadcastCategory("🛒 抢购任务", []taskBroadcastItem{item, item})
	if strings.Count(got, "🛒 抢购任务") != 1 {
		t.Fatalf("category title count = %d, want 1", strings.Count(got, "🛒 抢购任务"))
	}
	if !strings.HasPrefix(got, "🛒 抢购任务\n\n") {
		t.Fatalf("category does not have exactly one title blank line: %q", got)
	}
	if strings.Count(got, "OVH 账号：账号") != 2 {
		t.Fatalf("task count = %d, want 2", strings.Count(got, "OVH 账号：账号"))
	}
	if strings.Contains(got, "🛒 抢购任务\n\n\n") {
		t.Fatal("category has more than one blank line after title")
	}
}

func TestAggregateQueueTaskCounts(t *testing.T) {
	items := []types.QueueItem{
		{ID: "1", AccountID: "a", PlanCode: "p", Datacenter: "gra", Options: []string{"ram-64g"}, Status: "running", AutoPay: true},
		{ID: "2", AccountID: "a", PlanCode: "p", Datacenter: "GRA", Options: []string{"ram-64g"}, Status: "pending", AutoPay: true},
		{ID: "3", AccountID: "a", PlanCode: "p", Datacenter: "rbx", Options: []string{"ram-64g"}, Status: "paused", AutoPay: true},
		{ID: "4", AccountID: "a", PlanCode: "p", Datacenter: "bhs", Options: []string{"ram-64g"}, Status: "completed", AutoPay: true},
	}
	groups := aggregateQueueTasks(items)
	if len(groups) != 1 {
		t.Fatalf("groups = %#v, want one active group", groups)
	}
	if got := groups[0].DatacenterCounts["GRA"]; got != 2 {
		t.Fatalf("GRA count = %d, want 2", got)
	}
	if got := groups[0].DatacenterCounts["RBX"]; got != 1 {
		t.Fatalf("RBX count = %d, want 1", got)
	}
	if _, ok := groups[0].DatacenterCounts["BHS"]; ok {
		t.Fatal("completed queue item was included")
	}
}

func TestNextDailyBroadcastAt(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	before := time.Date(2026, 9, 28, 8, 59, 0, 0, loc)
	if got := nextDailyBroadcastAt(before, "09:00", loc); !got.Equal(time.Date(2026, 9, 28, 9, 0, 0, 0, loc)) {
		t.Fatalf("before trigger = %s", got)
	}
	after := time.Date(2026, 9, 28, 9, 0, 1, 0, loc)
	if got := nextDailyBroadcastAt(after, "09:00", loc); !got.Equal(time.Date(2026, 9, 29, 9, 0, 0, 0, loc)) {
		t.Fatalf("after trigger = %s", got)
	}
}

func TestTaskBroadcastSelectionsAreIndependent(t *testing.T) {
	falseValue := false
	trueValue := true
	selection := scheduledTaskBroadcastSelection(types.Config{
		TaskBroadcastEnabled:        &trueValue,
		TaskBroadcastQueueEnabled:   &trueValue,
		TaskBroadcastMonitorEnabled: &falseValue,
		TaskBroadcastVPSEnabled:     &trueValue,
		TaskBroadcastReportEnabled:  &falseValue,
	})
	if !selection.Queue || selection.Monitor || !selection.VPS || selection.Report {
		t.Fatalf("scheduled selection = %+v", selection)
	}
	startup := startupTaskBroadcastSelection()
	if !startup.Queue || !startup.Monitor || !startup.VPS || startup.Report {
		t.Fatalf("startup selection = %+v", startup)
	}
}

func TestFormatBattleReportGroupsByAccount(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	priceOne, priceTwo := 12.5, 30.0
	history := []types.PurchaseHistoryEntry{
		{AccountID: "a", Status: "success", PurchaseTime: now.Add(-2 * time.Hour).Format(time.RFC3339), OrderID: "o1", OrderStatus: "notPaid", Price: &types.PriceInfo{WithTax: &priceOne, CurrencyCode: "EUR"}},
		{AccountID: "a", Status: "success", PurchaseTime: now.Add(-3 * time.Hour).Format(time.RFC3339), OrderID: "o2", OrderStatus: "delivered", Price: &types.PriceInfo{WithTax: &priceTwo, CurrencyCode: "EUR"}},
		{AccountID: "b", Status: "success", PurchaseTime: now.Add(-25 * time.Hour).Format(time.RFC3339), OrderID: "o3", OrderStatus: "delivered", Price: &types.PriceInfo{WithTax: &priceTwo, CurrencyCode: "EUR"}},
	}
	got := formatBattleReport(history, now, map[string]string{"a": "主账号", "b": "备用账号"})
	for _, want := range []string{
		"🏆 抢购战报",
		"OVH 账号：主账号",
		"过去 24 小时成功下单：2",
		"未支付订单：1 单",
		"未支付金额：€12.50",
		"已支付订单：1 单",
		"已支付金额：€30.00",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "备用账号") {
		t.Fatal("out-of-window account should not be included")
	}
}
