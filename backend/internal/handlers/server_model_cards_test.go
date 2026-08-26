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
			{Value: "ram-32g-ecc-2133"},
			{Value: "bandwidth-250"},
		},
		AvailableOptions: []types.ServerOption{
			{Label: "64 GB", Value: "ram-64g-ecc-2400", Family: "memory"},
			{Label: "32 GB", Value: "ram-32g-ecc-2133", Family: "memory"},
			{Label: "500 Mbps", Value: "bandwidth-500", Family: "bandwidth"},
			{Label: "250 Mbps", Value: "bandwidth-250", Family: "bandwidth"},
		},
	}
}

func TestFindServerPlansByModel(t *testing.T) {
	state := &app.State{ServerPlans: []types.ServerPlan{
		testServerPlan(),
		{PlanCode: "26sk10b-v1", Name: "Kimsufi Essential"},
		{PlanCode: "24sk103", Name: "KS-2 | Intel Xeon"},
	}}
	plans := findServerPlansByModel(state, " ks-1 ")
	if len(plans) != 2 || plans[0].PlanCode != "24sk102" || plans[1].PlanCode != "26sk10b-v1" {
		t.Fatalf("型号匹配结果错误: %+v", plans)
	}
	if got := findServerPlansByModel(state, "KS"); len(got) != 0 {
		t.Fatalf("不应模糊匹配型号: %+v", got)
	}
	for _, value := range []string{"KS-1", "ks - 1", "KS 1", "KS‑1"} {
		got := findServerPlansByModel(state, value)
		if len(got) != 2 || got[0].PlanCode != "24sk102" || got[1].PlanCode != "26sk10b-v1" {
			t.Fatalf("型号输入格式 %q 的匹配结果错误: %+v", value, got)
		}
	}
	namePlan := types.ServerPlan{PlanCode: "24sk103", Name: "Kimsufi Essential | KS - 2 | Intel Xeon"}
	if got := matchServerPlansByModel([]types.ServerPlan{namePlan}, "KS-2"); len(got) != 1 || got[0].PlanCode != "24sk103" {
		t.Fatalf("应从带系列前缀的目录名称中提取型号: %+v", got)
	}
}

func TestFindServerPlansByModelMergesCache(t *testing.T) {
	state := &app.State{
		ServerPlans: []types.ServerPlan{{PlanCode: "24sk102", Name: "Kimsufi Essential"}},
		ServerCache: app.NewServerListCache(),
	}
	state.ServerCache.Set([]types.ServerPlan{{PlanCode: "26sk10b-v1", Name: "Kimsufi Essential"}})

	plans := findServerPlansByModel(state, "KS-1")
	if len(plans) != 2 || plans[0].PlanCode != "24sk102" || plans[1].PlanCode != "26sk10b-v1" {
		t.Fatalf("应合并内存目录和服务器缓存: %+v", plans)
	}
}

func TestFindServerPlansByModelWithoutRuntimeDoesNotRefresh(t *testing.T) {
	if plans := findServerPlansByModel(&app.State{}, "KS-1"); len(plans) != 0 {
		t.Fatalf("空运行态不应生成不存在的服务器配置: %+v", plans)
	}
}

func TestLooksLikeServerModelQuery(t *testing.T) {
	for _, value := range []string{"KS-1", "ks - 1", "KS 1", "KS‑1", "rise-2", "GAME-1-A"} {
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

func TestNormalizeServerModel(t *testing.T) {
	for _, value := range []string{"KS-1", "ks - 1", "KS 1", "KS‐1", "KS‑1", "KS－1"} {
		if got := normalizeServerModel(value); got != "ks-1" {
			t.Fatalf("型号归一化错误: %q => %q", value, got)
		}
	}
}

func TestServerPlanSectionsDefaultsAndDatacenters(t *testing.T) {
	sections := serverPlanSections(nil, testServerPlan())
	if sections[1].Title != "内存 / 频率" {
		t.Fatalf("内存分区标题错误: %q", sections[1].Title)
	}
	if got := strings.Join(sections[1].Lines, "\n"); !strings.HasPrefix(got, "● 32 GB · ECC-2133（默认）") {
		t.Fatalf("默认内存应优先并标记: %q", got)
	}
	if sections[2].Title != "存储 / 数据盘" || sections[3].Title != "带宽 / 网络" {
		t.Fatalf("存储/带宽分区标题错误: %q, %q", sections[2].Title, sections[3].Title)
	}
	if got := strings.Join(sections[4].Lines, "\n"); got != "月费: 暂不可用\n安装费: 无\n总价: 暂不可用" {
		t.Fatalf("价格不可用时应保持三行结构: %q", got)
	}
	dcs := strings.Join(sections[5].Lines, "\n")
	if sections[5].Title != "数据中心 2/12 可用" {
		t.Fatalf("数据中心可用比例错误: %q", sections[5].Title)
	}
	for _, want := range []string{
		"🟢 GRA    法国 · 格拉夫尼茨",
		"🔴 RBX    法国 · 鲁贝",
		"🟢 MUM    印度 · 孟买",
	} {
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
	card := feishuServerPlanCard("KS-1", testServerPlan(), serverPlanSections(nil, testServerPlan()), 1, 1)
	elements, ok := card["elements"].([]interface{})
	if !ok || len(elements) != 6 {
		t.Fatalf("飞书卡片应包含六个独立配置分区: %+v", card)
	}
	for _, raw := range elements {
		element, _ := raw.(map[string]interface{})
		if element["tag"] != "markdown" {
			t.Fatalf("飞书配置分区不应使用表格元素: %+v", element)
		}
	}
}
