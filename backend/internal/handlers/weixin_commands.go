package handlers

import (
	"fmt"
	"strings"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

// HandleWeixinText 复用 Telegram / 飞书已有命令语义，仅返回应答文本。
// 用户绑定和私聊鉴权在 iLink Manager 中先行完成。
func HandleWeixinText(state *app.State, mon *monitor.Monitor, senderID, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	accountID := telegram.DefaultAccountID(state)
	if command := telegram.ParseBotCommand(text); command != nil {
		if command.Name == "account" && isAccountSwitchRequest(command.Args) {
			return "微信首版暂不提供账户切换按钮，请在 WebUI 设置页切换默认 OVH 账户。"
		}
		return dispatchBotCommand(state, mon, command, accountID, "weixin")
	}
	if strings.EqualFold(text, "help") || text == "?" || text == "帮助" {
		return weixinHelpMessage()
	}
	if plans := findServerPlansByModel(state, text); len(plans) > 0 {
		return weixinServerPlanText(state, text, plans)
	}
	if looksLikeServerModelQuery(text) {
		return "❌ 服务器列表中未找到型号：" + text
	}
	if !feishuLooksLikeOrder(text) {
		return ""
	}
	order := telegram.ParseOrderMessage(text)
	if order == nil || order.PlanCode == "" {
		return ""
	}
	if strings.HasPrefix(order.PlanCode, "/") {
		return "❌ 未知命令" + string(rune(10)) + string(rune(10)) + weixinHelpMessage()
	}
	result := telegram.ProcessOrderForAccount(
		state, accountID, order.PlanCode, order.Datacenter,
		order.Quantity, order.Options, false,
	)
	if !result.Success {
		return "❌ 下单失败" + string(rune(10)) + string(rune(10)) + result.Message
	}
	datacenter := "自动选择机房"
	if order.Datacenter != "" {
		datacenter = strings.ToUpper(order.Datacenter)
	}
	options := "匹配配置"
	if len(order.Options) > 0 {
		options = strings.Join(order.Options, ", ")
	}
	return fmt.Sprintf("✅ 已加入抢购队列！%c%c型号: %s%c机房: %s%c数量: %d%c配置: %s%c%c已创建: %d/%d 个订单%c系统将自动尝试下单。",
		10, 10, order.PlanCode, 10, datacenter, 10, telegram.ClampQuantity(order.Quantity), 10, options,
		10, 10, result.CreatedOrders, result.TotalOrders, 10)
}

func weixinHelpMessage() string {
	return strings.ReplaceAll(telegram.HelpMessage(),
		"/account switch  打开账户切换菜单",
		"/account switch  请在 WebUI 切换默认账户")
}

func weixinServerPlanText(state *app.State, model string, plans []types.ServerPlan) string {
	var builder strings.Builder
	for planIndex, plan := range plans {
		if planIndex > 0 {
			builder.WriteRune(10)
			builder.WriteRune(10)
		}
		builder.WriteString(strings.TrimSpace(model) + " · " + plan.PlanCode)
		for _, section := range serverPlanSections(state, plan) {
			builder.WriteRune(10)
			builder.WriteRune(10)
			builder.WriteString(section.Title)
			builder.WriteRune(10)
			builder.WriteString(strings.Join(section.Lines, string(rune(10))))
		}
		builder.WriteRune(10)
		builder.WriteRune(10)
		builder.WriteString("下单：/buy " + plan.PlanCode + " <机房代码>")
	}
	return builder.String()
}
