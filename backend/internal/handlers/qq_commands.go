package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/qqbot"
	"github.com/ovh-webui/server/internal/telegram"
)

// HandleQQMessage 是 QQ Gateway 入站消息的业务入口。
// 私聊用户白名单拥有完整命令权限；群聊只允许只读的库存和价格命令。
func HandleQQMessage(_ context.Context, state *app.State, mon *monitor.Monitor, event qqbot.MessageEvent, client *qqbot.Client) {
	if state == nil || client == nil {
		return
	}
	cfg := state.Config.Get()
	if !cfg.IsQQNotificationsEnabled() {
		return
	}
	text := normalizeQQCommandText(event.Content)
	if text == "" {
		return
	}
	if event.GroupOpenID != "" {
		handleQQGroupCommand(state, mon, event, client, text)
		return
	}
	if !containsQQID(state.Config.Get().QQUserOpenIDs, event.UserOpenID) {
		return
	}
	cmd := telegram.ParseBotCommand(text)
	if cmd == nil {
		if isQQRebootContinuation(text) {
			reply := qqRebootCommandText(state, event, []string{text})
			sendQQCommandReply(client, event, reply)
		}
		return
	}
	if cmd.Name == "list" {
		for _, reply := range dispatchBotCommandReplies(state, mon, cmd, telegram.DefaultAccountID(state), "qq") {
			sendQQCommandReply(client, event, reply)
		}
		return
	}
	reply := dispatchQQAdminCommand(state, mon, event, cmd)
	sendQQCommandReply(client, event, reply)
}

func handleQQGroupCommand(state *app.State, mon *monitor.Monitor, event qqbot.MessageEvent, client *qqbot.Client, text string) {
	if !containsQQID(state.Config.Get().QQGroupOpenIDs, event.GroupOpenID) {
		return
	}
	cmd := telegram.ParseBotCommand(text)
	if cmd == nil {
		return
	}
	if cmd.Name != "stock" && cmd.Name != "price" {
		sendQQCommandReply(client, event, "QQ群聊仅支持 /stock、/price（或 /库存、/价格）命令。")
		return
	}
	reply := dispatchBotCommand(state, mon, cmd, telegram.DefaultAccountID(state), "qq")
	sendQQCommandReply(client, event, reply)
}

func dispatchQQAdminCommand(state *app.State, mon *monitor.Monitor, event qqbot.MessageEvent, cmd *telegram.BotCommand) string {
	switch cmd.Name {
	case "account":
		return qqAccountCommandText(state, cmd.Args)
	case "reboot":
		return qqRebootCommandText(state, event, cmd.Args)
	default:
		return dispatchBotCommand(state, mon, cmd, telegram.DefaultAccountID(state), "qq")
	}
}

func sendQQCommandReply(client *qqbot.Client, event qqbot.MessageEvent, reply string) {
	if strings.TrimSpace(reply) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if event.GroupOpenID != "" {
		_ = client.SendGroupReply(ctx, event.GroupOpenID, reply, event.ID)
		return
	}
	if event.UserOpenID != "" {
		_ = client.SendUserReply(ctx, event.UserOpenID, reply, event.ID)
	}
}

func normalizeQQCommandText(content string) string {
	text := strings.TrimSpace(content)
	for strings.HasPrefix(text, "<@") {
		end := strings.IndexByte(text, '>')
		if end < 0 {
			break
		}
		text = strings.TrimSpace(text[end+1:])
	}
	if strings.HasPrefix(text, "@") {
		parts := strings.Fields(text)
		if len(parts) > 1 && !strings.HasPrefix(parts[0], "/") {
			text = strings.Join(parts[1:], " ")
		}
	}
	return strings.TrimSpace(text)
}

func isQQRebootContinuation(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "确认" || trimmed == "取消" || strings.EqualFold(trimmed, "confirm") || strings.EqualFold(trimmed, "cancel") {
		return true
	}
	_, err := strconv.Atoi(trimmed)
	return err == nil
}

func containsQQID(values []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == wanted {
			return true
		}
	}
	return false
}

func qqAccountCommandText(state *app.State, args []string) string {
	if len(args) == 0 {
		return accountCommandText(state, nil, "qq")
	}
	if len(args) == 1 && isAccountSwitchRequest(args) {
		return qqAccountChoices(state)
	}
	if len(args) == 2 && strings.EqualFold(strings.TrimSpace(args[0]), "switch") {
		accounts, err := listAccountChoices(state)
		if err != nil {
			return "❌ 无法读取 OVH 账户，请稍后重试"
		}
		index, err := parseQQChoiceIndex(args[1], len(accounts))
		if err != nil {
			return err.Error()
		}
		account, err := switchDefaultAccount(state, accounts[index].ID)
		if err != nil {
			return "❌ 切换账户失败：" + err.Error()
		}
		return fmt.Sprintf("✅ 已切换当前 OVH 账户：%s（%s）", accountDisplayName(account), account.Endpoint)
	}
	return "用法：/account 或 /account switch；QQ 中切换账户请发送 /account switch 后按序号选择"
}

func qqAccountChoices(state *app.State) string {
	accounts, err := listAccountChoices(state)
	if err != nil {
		return "❌ 无法读取 OVH 账户，请稍后重试"
	}
	if len(accounts) == 0 {
		return "❌ 当前没有配置 OVH 账户"
	}
	var builder strings.Builder
	builder.WriteString("请选择要切换到的 OVH 账户（发送 /account switch 序号）：\n")
	for index, account := range accounts {
		label := accountDisplayName(account)
		if account.Zone != "" {
			label += " · " + account.Zone
		}
		if account.IsDefault {
			label = "✓ " + label
		}
		builder.WriteString(fmt.Sprintf("%d. %s\n", index+1, label))
	}
	return strings.TrimSpace(builder.String())
}

func qqRebootCommandText(state *app.State, event qqbot.MessageEvent, args []string) string {
	if state == nil || state.DB == nil {
		return "❌ 重启交互服务暂不可用"
	}
	actorID := strings.TrimSpace(event.UserOpenID)
	chatID := actorID
	if actorID == "" {
		return "❌ 无法识别 QQ 管理员身份"
	}
	if len(args) == 0 {
		menu, err := prepareRebootFlow(state, "qq", actorID, chatID)
		if err != nil {
			return "❌ " + err.Error()
		}
		return formatQQRebootMenu(menu)
	}

	command := strings.ToLower(strings.TrimSpace(args[0]))
	switch command {
	case "confirm", "确认":
		return finishQQRebootFlow(state, actorID, chatID, true)
	case "cancel", "取消":
		return finishQQRebootFlow(state, actorID, chatID, false)
	case "account", "账户":
		if len(args) != 2 {
			return "用法：/reboot account 序号"
		}
		return selectQQRebootAccount(state, actorID, chatID, args[1])
	case "server", "服务器":
		if len(args) != 2 {
			return "用法：/reboot server 序号"
		}
		return selectQQRebootServer(state, actorID, chatID, args[1])
	default:
		if len(args) == 1 {
			return selectQQRebootNumber(state, actorID, chatID, args[0])
		}
		return "用法：/reboot；收到列表后发送序号，确认时发送 /reboot confirm 或 /reboot cancel"
	}
}

func selectQQRebootNumber(state *app.State, actorID, chatID, rawIndex string) string {
	row, ok, err := state.DB.GetLatestBotRebootFlow("qq", actorID, chatID)
	if err != nil || !ok {
		return "❌ 重启选择已过期，请重新发送 /reboot"
	}
	switch row.Stage {
	case rebootStageAccount:
		return selectQQRebootAccount(state, actorID, chatID, rawIndex)
	case rebootStageServer:
		return selectQQRebootServer(state, actorID, chatID, rawIndex)
	case rebootStageConfirm:
		return "请发送 /reboot confirm 确认，或 /reboot cancel 取消。"
	default:
		return "❌ 重启选择已过期，请重新发送 /reboot"
	}
}

func selectQQRebootAccount(state *app.State, actorID, chatID, rawIndex string) string {
	row, ok, err := state.DB.GetLatestBotRebootFlow("qq", actorID, chatID)
	if err != nil || !ok || row.Stage != rebootStageAccount {
		return "❌ 账户选择已过期，请重新发送 /reboot"
	}
	var payload rebootFlowPayload
	if err := decodeRebootPayload(row.Payload, &payload); err != nil {
		return "❌ 重启交互数据无效，请重新发送 /reboot"
	}
	index, err := parseQQChoiceIndex(rawIndex, len(payload.Accounts))
	if err != nil {
		return err.Error()
	}
	selected, err := selectRebootAccount(state, row.ID, "qq", actorID, chatID, index)
	if err != nil {
		return "❌ " + err.Error()
	}
	return formatQQRebootServers(selected, "账户已选择")
}

func selectQQRebootServer(state *app.State, actorID, chatID, rawIndex string) string {
	row, ok, err := state.DB.GetLatestBotRebootFlow("qq", actorID, chatID)
	if err != nil || !ok || row.Stage != rebootStageServer {
		return "❌ 服务器选择已过期，请重新发送 /reboot"
	}
	var payload rebootFlowPayload
	if err := decodeRebootPayload(row.Payload, &payload); err != nil {
		return "❌ 重启交互数据无效，请重新发送 /reboot"
	}
	index, err := parseQQChoiceIndex(rawIndex, len(payload.Servers))
	if err != nil {
		return err.Error()
	}
	server, err := selectRebootServer(state, row.ID, "qq", actorID, chatID, index)
	if err != nil {
		return "❌ " + err.Error()
	}
	return fmt.Sprintf("即将重启：%s\n\n发送 /reboot confirm 确认，或 /reboot cancel 取消。", rebootServerLabel(server))
}

func finishQQRebootFlow(state *app.State, actorID, chatID string, confirm bool) string {
	message, _ := finishRebootFlow(state, latestQQRebootFlowID(state, actorID, chatID), "qq", actorID, chatID, confirm)
	return message
}

func latestQQRebootFlowID(state *app.State, actorID, chatID string) string {
	row, ok, err := state.DB.GetLatestBotRebootFlow("qq", actorID, chatID)
	if err != nil || !ok {
		return ""
	}
	return row.ID
}

func formatQQRebootMenu(menu rebootPreparedMenu) string {
	if menu.Stage == rebootStageAccount {
		var builder strings.Builder
		builder.WriteString("请选择服务器所属的 OVH 账户（发送 /reboot 序号）：\n")
		for index, account := range menu.Payload.Accounts {
			label := account.Name
			if account.Zone != "" {
				label += " · " + account.Zone
			}
			if account.IsDefault {
				label = "✓ " + label
			}
			builder.WriteString(fmt.Sprintf("%d. %s\n", index+1, label))
		}
		return strings.TrimSpace(builder.String())
	}
	return formatQQRebootServers(menu.Payload, "当前账户已确定")
}

func formatQQRebootServers(payload rebootFlowPayload, prefix string) string {
	var builder strings.Builder
	builder.WriteString(prefix + "，请选择要重启的服务器（发送 /reboot 序号）：\n")
	for index, server := range payload.Servers {
		builder.WriteString(fmt.Sprintf("%d. %s\n", index+1, rebootServerLabel(server)))
	}
	return strings.TrimSpace(builder.String())
}

func decodeRebootPayload(raw string, target interface{}) error {
	return json.Unmarshal([]byte(raw), target)
}

func parseQQChoiceIndex(raw string, length int) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > length {
		return 0, fmt.Errorf("选择无效，请发送 1-%d 之间的序号", length)
	}
	return value - 1, nil
}
