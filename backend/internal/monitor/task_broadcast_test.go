package monitor

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

func TestFormatTaskBroadcastItem(t *testing.T) {
	item := taskBroadcastItem{
		Account:     "主账号",
		Model:       "KS-1",
		PlanCode:    "24sk10",
		Memory:      "64GB",
		Storage:     "2x 480GB SSD",
		Bandwidth:   "1Gbps",
		Datacenters: "GRA",
		Monthly:     "€10.00/月",
		Install:     "€2.50",
		Total:       "€12.50",
		Extra: []broadcastField{
			{Label: "抢购数量", Value: "GRA 2 台"},
		},
		AutoPay: true,
	}
	got := formatTaskBroadcastItem(item)
	want := strings.Join([]string{
		"型号：KS-1",
		"Plan Code：24sk10",
		"内存：64GB",
		"数据盘：2x 480GB SSD",
		"带宽：1Gbps",
		"数据中心：GRA",
		"月费：€10.00/月",
		"安装费：€2.50",
		"总价：€12.50",
		"抢购数量：GRA 2 台",
		"自动付款：是",
	}, "\n")
	if got != want {
		t.Fatalf("formatted task = %q, want %q", got, want)
	}
}

func TestFormatTaskBroadcastCategoryGroupsTasksByAccount(t *testing.T) {
	newItem := func(accountID, account, planCode string) taskBroadcastItem {
		return taskBroadcastItem{
			AccountID: accountID, Account: account, Model: "型号", PlanCode: planCode,
			Memory: "64GB", Storage: "2x SSD", Bandwidth: "1Gbps", Datacenters: "GRA",
			Monthly: "€1/月", Install: "无", Total: "€1", AutoPay: false,
		}
	}
	items := []taskBroadcastItem{
		newItem("acct-1", "主账号", "plan-1"),
		newItem("acct-2", "备用账号", "plan-2"),
		newItem("acct-1", "主账号", "plan-3"),
	}
	messages := formatTaskBroadcastCategory("🛒 抢购任务", items)
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want one short category message", len(messages))
	}
	got := messages[0]
	if !strings.HasPrefix(got, "🛒 抢购任务\n\n正在执行任务数：3\n\n") {
		t.Fatalf("category heading/count formatting is incorrect: %q", got)
	}
	wantOrder := []string{
		"OVH 账号：主账号", "任务1：", "Plan Code：plan-1",
		"任务2：", "Plan Code：plan-3",
		"OVH 账号：备用账号", "任务1：", "Plan Code：plan-2",
	}
	position := 0
	for _, want := range wantOrder {
		next := strings.Index(got[position:], want)
		if next < 0 {
			t.Fatalf("category %q missing or misordered %q", got, want)
		}
		position += next + len(want)
	}
	if strings.Count(got, "任务1：") != 2 || strings.Count(got, "任务2：") != 1 {
		t.Fatalf("account task numbers are incorrect: %q", got)
	}
	if !strings.Contains(got, "自动付款：否\n\n任务2：") {
		t.Fatalf("tasks for one account are not separated by one blank line: %q", got)
	}
	if !strings.Contains(got, "自动付款：否\n\nOVH 账号：备用账号") {
		t.Fatalf("account groups are not separated by one blank line: %q", got)
	}
}

func TestFormatTaskBroadcastCategorySplitsLongMessagesWithoutLosingTasks(t *testing.T) {
	items := make([]taskBroadcastItem, 0, 9)
	for accountIndex := 1; accountIndex <= 3; accountIndex++ {
		for taskIndex := 1; taskIndex <= 3; taskIndex++ {
			items = append(items, taskBroadcastItem{
				AccountID: fmt.Sprintf("account-%d", accountIndex),
				Account:   fmt.Sprintf("账号%d", accountIndex),
				Model:     strings.Repeat("M", 900),
				PlanCode:  fmt.Sprintf("plan-%d-%d", accountIndex, taskIndex),
				Memory:    "64GB", Storage: "2x SSD", Bandwidth: "1Gbps", Datacenters: "GRA",
				Monthly: "€1/月", Install: "无", Total: "€1", AutoPay: false,
			})
		}
	}

	messages := formatTaskBroadcastCategory("🛒 抢购任务", items)
	if len(messages) < 2 {
		t.Fatalf("message count = %d, want long category to be split", len(messages))
	}
	all := strings.Join(messages, "\n")
	for i, message := range messages {
		if len(message) > taskBroadcastMessageMaxBytes {
			t.Fatalf("message %d is %d bytes, over limit %d", i+1, len(message), taskBroadcastMessageMaxBytes)
		}
		wantHeader := fmt.Sprintf("🛒 抢购任务（%d/%d）\n\n正在执行任务数：9\n\nOVH 账号：", i+1, len(messages))
		if !strings.HasPrefix(message, wantHeader) {
			t.Fatalf("message %d has an incorrect continuation header: %q", i+1, message)
		}
	}
	for accountIndex := 1; accountIndex <= 3; accountIndex++ {
		for taskIndex := 1; taskIndex <= 3; taskIndex++ {
			planCode := fmt.Sprintf("Plan Code：plan-%d-%d", accountIndex, taskIndex)
			if strings.Count(all, planCode) != 1 {
				t.Fatalf("%q appears %d times; want once", planCode, strings.Count(all, planCode))
			}
		}
	}
	if strings.Count(all, "任务1：") != 3 || strings.Count(all, "任务2：") != 3 || strings.Count(all, "任务3：") != 3 {
		t.Fatalf("task numbering was not preserved across chunks: %q", all)
	}
}

func TestFormatTaskBroadcastCategorySplitsOversizedTaskOnUTF8Boundaries(t *testing.T) {
	longModel := strings.Repeat("界", 4000)
	messages := formatTaskBroadcastCategory("🛒 抢购任务", []taskBroadcastItem{{
		AccountID: "acct-1", Account: "主账号", Model: longModel, PlanCode: "plan-1",
		Memory: "64GB", Storage: "2x SSD", Bandwidth: "1Gbps", Datacenters: "GRA",
		Monthly: "€1/月", Install: "无", Total: "€1", AutoPay: false,
	}})
	if len(messages) < 2 {
		t.Fatalf("message count = %d, want oversized task to continue across messages", len(messages))
	}
	all := strings.Join(messages, "\n")
	if got := strings.Count(all, "界"); got != len([]rune(longModel)) {
		t.Fatalf("long model rune count = %d, want %d", got, len([]rune(longModel)))
	}
	for i, message := range messages {
		if len(message) > taskBroadcastMessageMaxBytes {
			t.Fatalf("message %d is %d bytes, over limit %d", i+1, len(message), taskBroadcastMessageMaxBytes)
		}
		if !strings.Contains(message, "OVH 账号：主账号") {
			t.Fatalf("message %d is missing the account label", i+1)
		}
	}
	if !strings.Contains(messages[0], "任务1：") {
		t.Fatalf("first message is missing the task label: %q", messages[0][:min(len(messages[0]), 100)])
	}
	for i, message := range messages[1:] {
		if !strings.Contains(message, "任务1（续）：") {
			t.Fatalf("continuation message %d is missing its continuation label", i+2)
		}
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
	priceOne, priceTwo := 10.0, 20.0
	withTaxOne, withTaxTwo := 12.5, 25.0
	history := []types.PurchaseHistoryEntry{
		{AccountID: "a", Status: "success", PurchaseTime: now.Add(-2 * time.Hour).Format(time.RFC3339), OrderID: "o1", OrderStatus: "notPaid", Price: &types.PriceInfo{WithTax: &withTaxOne, WithoutTax: &priceOne, CurrencyCode: "EUR"}},
		{AccountID: "a", Status: "success", PurchaseTime: now.Add(-3 * time.Hour).Format(time.RFC3339), OrderID: "o2", OrderStatus: "delivered", Price: &types.PriceInfo{WithTax: &withTaxTwo, WithoutTax: &priceTwo, CurrencyCode: "EUR"}},
		{AccountID: "b", Status: "success", PurchaseTime: now.Add(-25 * time.Hour).Format(time.RFC3339), OrderID: "o3", OrderStatus: "delivered", Price: &types.PriceInfo{WithTax: &priceTwo, CurrencyCode: "EUR"}},
	}
	got := formatBattleReport(history, now, map[string]string{"a": "主账号", "b": "备用账号"})
	for _, want := range []string{
		"🏆 抢购战报",
		"OVH 账号：主账号",
		"过去 24 小时成功下单：2",
		"未支付订单：1 单",
		"未支付金额：€10.00",
		"已支付订单：1 单",
		"已支付金额：€20.00",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "备用账号") {
		t.Fatal("out-of-window account should not be included")
	}
}
