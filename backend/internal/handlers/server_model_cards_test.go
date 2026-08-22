package handlers

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

func testServerPlan() types.ServerPlan {
	return types.ServerPlan{
		PlanCode:   "24sk102",
		Name:       "KS-1 | Intel Xeon",
		CPU:        "Intel Xeon",
		Memory:     "32 GB",
		Storage:    "2x 480GB SSD",
		Bandwidth:  "250 Mbps",
		Datacenters: []types.Datacenter{
			{Datacenter: "gra", Availability: "1H-low"},
			{Datacenter: "rbx", Availability: "unavailable"},
			{Datacenter: "ynm", Availability: "24H"},
		},
		DefaultOptions: []types.ServerOption{
			{Value: "ram-32g-ecc"},
			{Value: "bandwidth-250"},
		},
		AvailableOptions: []types.ServerOption{
			{Label: "64 GB", Value: "ram-64g-ecc", Family: "memory"},
			{Label: "32 GB", Value: "ram-32g-ecc", Family: "memory"},
			{Label: "500 Mbps", Value: "bandwidth-500", Family: "bandwidth"},
			{Label: "250 Mbps", Value: "bandwidth-250", Family: "bandwidth"},
		},
	}
}

func TestFindServerPlansByModel(t *testing.T) {
	state := &app.State{ServerPlans: []types.ServerPlan{
		testServerPlan(),
		{PlanCode: "26sk10b-v1", Name: "KS-1 | AMD Ryzen"},
		{PlanCode: "24sk103", Name: "KS-2 | Intel Xeon"},
	}}
	plans := findServerPlansByModel(state, " ks-1 ")
	if len(plans) != 2 || plans[0].PlanCode != "24sk102" || plans[1].PlanCode != "26sk10b-v1" {
		t.Fatalf("型号匹配结果错误: %+v", plans)
	}
	if got := findServerPlansByModel(state, "KS"); len(got) != 0 {
		t.Fatalf("不应模糊匹配型号: %+v", got)
	}
}

func TestLooksLikeServerModelQuery(t *testing.T) {
	for _, value := range []string{"KS-1", "rise-2", "GAME-1-A"} {
		if !looksLikeServerModelQuery(value) {
			t.Fatalf("应识别为型号查询: %q", value)
		}
	}
	for _, value := range []string{"24sk102", "26sk10b-v1", "KS-1 gra", "hello"} {
		if looksLikeServerModelQuery(value) {
			t.Fatalf("不应识别为型号查询: %q", value)
		}
	}
}

func TestServerPlanSectionsDefaultsAndDatacenters(t *testing.T) {
	sections := serverPlanSections(testServerPlan())
	if got := strings.Join(sections[1].Lines, "\n"); !strings.HasPrefix(got, "● 32 GB（默认）") {
		t.Fatalf("默认内存应优先并标记: %q", got)
	}
	dcs := strings.Join(sections[4].Lines, "\n")
	for _, want := range []string{"🔴 GRA", "🟢 RBX", "🔴 MUM"} {
		if !strings.Contains(dcs, want) {
			t.Fatalf("数据中心状态缺少 %q: %q", want, dcs)
		}
	}
}

func TestPaginateServerSectionsPreservesLongText(t *testing.T) {
	longLine := strings.Repeat("超长配置", 80)
	pages := paginateServerSections([]serverPlanSection{{Title: "内存", Lines: []string{longLine}}}, 80)
	if len(pages) < 2 {
		t.Fatalf("超长内容应分页: %+v", pages)
	}
	var rebuilt strings.Builder
	for _, page := range pages {
		for _, section := range page {
			if serverSectionRuneCount(section) > 80 {
				t.Fatalf("分页仍超过限制: %d", serverSectionRuneCount(section))
			}
			for _, line := range section.Lines {
				rebuilt.WriteString(strings.TrimPrefix(line, "  "))
			}
		}
	}
	if utf8.RuneCountInString(rebuilt.String()) != utf8.RuneCountInString(longLine) {
		t.Fatal("分页不应丢失超长配置内容")
	}
}

func TestFeishuServerPlanCardUsesSectionsNotTable(t *testing.T) {
	card := feishuServerPlanCard("KS-1", testServerPlan(), serverPlanSections(testServerPlan()), 1, 1)
	elements, ok := card["elements"].([]interface{})
	if !ok || len(elements) != 5 {
		t.Fatalf("飞书卡片应包含五个独立配置分区: %+v", card)
	}
	for _, raw := range elements {
		element, _ := raw.(map[string]interface{})
		if element["tag"] != "markdown" {
			t.Fatalf("飞书配置分区不应使用表格元素: %+v", element)
		}
	}
}
