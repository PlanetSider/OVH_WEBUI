package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

const (
	rebootStageAccount = "account"
	rebootStageLoading = "loading"
	rebootStageServer  = "server"
	rebootStageConfirm = "confirm"
	rebootStageDone    = "done"
)

type rebootAccountChoice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Zone      string `json:"zone,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

type rebootServerChoice struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	ServiceName string `json:"serviceName"`
	DisplayName string `json:"displayName"`
	Datacenter  string `json:"datacenter"`
	State       string `json:"state,omitempty"`
}

type rebootFlowPayload struct {
	Accounts    []rebootAccountChoice `json:"accounts,omitempty"`
	AccountID   string                `json:"accountId,omitempty"`
	AccountName string                `json:"accountName,omitempty"`
	Servers     []rebootServerChoice  `json:"servers,omitempty"`
	Selected    *rebootServerChoice   `json:"selected,omitempty"`
}

type rebootPreparedMenu struct {
	FlowID  string
	Stage   string
	Payload rebootFlowPayload
}

type telegramInlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func prepareRebootFlow(state *app.State, channel, actorID, chatID string) (rebootPreparedMenu, error) {
	if state == nil || state.DB == nil {
		return rebootPreparedMenu{}, fmt.Errorf("重启交互服务暂不可用")
	}
	accounts, err := listAccountChoices(state)
	if err != nil {
		return rebootPreparedMenu{}, fmt.Errorf("无法读取 OVH 账户: %w", err)
	}
	if len(accounts) == 0 {
		return rebootPreparedMenu{}, fmt.Errorf("当前没有配置 OVH 账户")
	}

	payload := rebootFlowPayload{Accounts: make([]rebootAccountChoice, 0, len(accounts))}
	for _, account := range accounts {
		payload.Accounts = append(payload.Accounts, rebootAccountChoice{
			ID: account.ID, Name: accountDisplayName(account), Zone: account.Zone, IsDefault: account.IsDefault,
		})
	}
	stage := rebootStageAccount
	if len(accounts) == 1 && accounts[0].IsDefault {
		servers, err := loadRebootServers(state, accounts[0])
		if err != nil {
			return rebootPreparedMenu{}, err
		}
		if len(servers) == 0 {
			return rebootPreparedMenu{}, fmt.Errorf("默认账户下没有可重启的独立服务器")
		}
		stage = rebootStageServer
		payload.AccountID = accounts[0].ID
		payload.AccountName = accountDisplayName(accounts[0])
		payload.Servers = servers
	}

	flowID := strings.ReplaceAll(uuid.NewString(), "-", "")
	raw, err := json.Marshal(payload)
	if err != nil {
		return rebootPreparedMenu{}, fmt.Errorf("生成重启交互失败: %w", err)
	}
	if err := state.DB.CreateBotRebootFlow(db.BotRebootFlowRow{
		ID: flowID, Channel: channel, ActorID: actorID, ChatID: chatID, Stage: stage, Payload: string(raw),
	}); err != nil {
		return rebootPreparedMenu{}, fmt.Errorf("保存重启交互失败: %w", err)
	}
	_, _ = state.DB.DeleteExpiredBotRebootFlows(float64(time.Now().Add(-24 * time.Hour).Unix()))
	return rebootPreparedMenu{FlowID: flowID, Stage: stage, Payload: payload}, nil
}

func loadRebootServers(state *app.State, account types.OVHAccount) ([]rebootServerChoice, error) {
	client, err := state.OVH.ClientFor(account.ID)
	if err != nil {
		return nil, fmt.Errorf("无法连接所选 OVH 账户: %w", err)
	}
	var serviceNames []string
	if err := client.Get("/dedicated/server", &serviceNames); err != nil {
		return nil, fmt.Errorf("读取服务器列表失败: %w", err)
	}
	aliases, err := state.DB.ListAliasesByAccount(account.ID)
	if err != nil {
		return nil, fmt.Errorf("读取服务器自定义名称失败: %w", err)
	}

	type serverDetail struct {
		info map[string]interface{}
		err  error
	}
	details := make([]serverDetail, len(serviceNames))
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for i, serviceName := range serviceNames {
		wg.Add(1)
		sem <- struct{}{}
		go func(index int, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			var info map[string]interface{}
			details[index].err = client.Get("/dedicated/server/"+name, &info)
			details[index].info = info
		}(i, serviceName)
	}
	wg.Wait()

	servers := make([]rebootServerChoice, 0, len(serviceNames))
	for i, serviceName := range serviceNames {
		info := details[i].info
		if details[i].err != nil && state.Logger != nil {
			state.Logger.Warn("读取待重启服务器详情失败: "+details[i].err.Error(), "server_control")
		}
		name := mapString(info, "name")
		displayName := visibleRebootServerName(aliases[serviceName], name, i+1)
		servers = append(servers, rebootServerChoice{
			AccountID: account.ID, AccountName: accountDisplayName(account), ServiceName: serviceName,
			DisplayName: displayName, Datacenter: mapStringOr(info, "datacenter", "N/A"),
			State: mapStringOr(info, "state", "unknown"),
		})
	}
	return servers, nil
}

func mapString(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mapStringOr(values map[string]interface{}, key, fallback string) string {
	if value := mapString(values, key); value != "" {
		return value
	}
	return fallback
}

func visibleRebootServerName(alias, ovhName string, ordinal int) string {
	for _, candidate := range []string{alias, ovhName} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !strings.HasPrefix(strings.ToLower(candidate), "ns") {
			return candidate
		}
	}
	return fmt.Sprintf("未设置自定义名称 #%d", ordinal)
}

func rebootDatacenterBadge(datacenter string) string {
	value := strings.ToLower(strings.TrimSpace(datacenter))
	code := "UNK"
	if len(value) >= 3 {
		code = strings.ToUpper(value[:3])
	}
	flags := map[string]string{
		"bhs": "🇨🇦",
		"eri": "🇬🇧", "lon": "🇬🇧",
		"fra": "🇩🇪", "lim": "🇩🇪",
		"gra": "🇫🇷", "par": "🇫🇷", "rbx": "🇫🇷", "sbg": "🇫🇷",
		"hil": "🇺🇸", "vin": "🇺🇸",
		"mum": "🇮🇳", "ynm": "🇮🇳",
		"sgp": "🇸🇬", "syd": "🇦🇺", "waw": "🇵🇱",
	}
	flag := flags[value]
	if flag == "" {
		flag = flags[strings.ToLower(code)]
	}
	if flag == "" {
		flag = "🌐"
	}
	return flag + " " + code
}

func rebootServerLabel(server rebootServerChoice) string {
	label := rebootDatacenterBadge(server.Datacenter) + " · " + server.DisplayName
	runes := []rune(label)
	if len(runes) > 60 {
		label = string(runes[:59]) + "…"
	}
	return label
}

func rebootAccountKeyboard(flowID string, accounts []rebootAccountChoice) map[string]interface{} {
	keyboard := make([][]telegramInlineButton, 0, len(accounts))
	for i, account := range accounts {
		label := account.Name
		if account.Zone != "" {
			label += " · " + account.Zone
		}
		if account.IsDefault {
			label = "✓ " + label
		}
		callback, _ := json.Marshal(map[string]interface{}{"a": "rba", "f": flowID, "i": i})
		keyboard = append(keyboard, []telegramInlineButton{{Text: label, CallbackData: string(callback)}})
	}
	return map[string]interface{}{"inline_keyboard": keyboard}
}

func rebootServerKeyboard(flowID string, servers []rebootServerChoice) map[string]interface{} {
	keyboard := make([][]telegramInlineButton, 0, len(servers))
	for i, server := range servers {
		callback, _ := json.Marshal(map[string]interface{}{"a": "rbs", "f": flowID, "i": i})
		keyboard = append(keyboard, []telegramInlineButton{{Text: rebootServerLabel(server), CallbackData: string(callback)}})
	}
	return map[string]interface{}{"inline_keyboard": keyboard}
}

func rebootConfirmKeyboard(flowID string) map[string]interface{} {
	confirm, _ := json.Marshal(map[string]interface{}{"a": "rbc", "f": flowID, "d": 1})
	cancel, _ := json.Marshal(map[string]interface{}{"a": "rbc", "f": flowID, "d": 0})
	return map[string]interface{}{"inline_keyboard": [][]telegramInlineButton{{
		{Text: "确认重启", CallbackData: string(confirm)},
		{Text: "取消", CallbackData: string(cancel)},
	}}}
}

func sendTelegramRebootMenu(state *app.State, chatID, userID interface{}, replyToMessageID int64) {
	chatKey := telegram.ChatIDString(chatID)
	actorKey := telegram.ChatIDString(userID)
	menu, err := prepareRebootFlow(state, "telegram", actorKey, chatKey)
	if err != nil {
		telegram.SendReply(state, chatID, "❌ "+err.Error(), replyToMessageID)
		return
	}
	if menu.Stage == rebootStageAccount {
		telegram.SendReplyWithMarkup(state, chatID, "请选择服务器所属的 OVH 账户（✓ 为默认账户）：", replyToMessageID,
			rebootAccountKeyboard(menu.FlowID, menu.Payload.Accounts))
		return
	}
	text := fmt.Sprintf("当前仅配置一个默认 OVH 账户：%s\n\n请选择要重启的服务器：", menu.Payload.AccountName)
	telegram.SendReplyWithMarkup(state, chatID, text, replyToMessageID, rebootServerKeyboard(menu.FlowID, menu.Payload.Servers))
}

func readRebootFlow(state *app.State, flowID, channel, actorID, chatID, stage string) (db.BotRebootFlowRow, rebootFlowPayload, error) {
	row, ok, err := state.DB.GetBotRebootFlow(flowID, channel, actorID, chatID)
	if err != nil {
		return row, rebootFlowPayload{}, err
	}
	if !ok || row.Stage != stage {
		return row, rebootFlowPayload{}, fmt.Errorf("操作已过期，请重新发送 /reboot")
	}
	var payload rebootFlowPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return row, payload, fmt.Errorf("重启交互数据无效，请重新发送 /reboot")
	}
	return row, payload, nil
}

func transitionRebootFlow(state *app.State, row db.BotRebootFlowRow, expected, next string, payload rebootFlowPayload) (bool, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	return state.DB.TransitionBotRebootFlow(row.ID, row.Channel, row.ActorID, row.ChatID, expected, next, string(raw))
}

func selectRebootAccount(state *app.State, flowID, channel, actorID, chatID string, index int) (rebootFlowPayload, error) {
	row, payload, err := readRebootFlow(state, flowID, channel, actorID, chatID, rebootStageAccount)
	if err != nil {
		return payload, err
	}
	if index < 0 || index >= len(payload.Accounts) {
		return payload, fmt.Errorf("账号选择无效，请重新发送 /reboot")
	}
	if ok, err := transitionRebootFlow(state, row, rebootStageAccount, rebootStageLoading, payload); err != nil || !ok {
		if err != nil {
			return payload, err
		}
		return payload, fmt.Errorf("账号选择已被处理，请重新发送 /reboot")
	}

	choice := payload.Accounts[index]
	account, ok := state.FindAccount(choice.ID)
	if !ok {
		_, _ = transitionRebootFlow(state, row, rebootStageLoading, rebootStageAccount, payload)
		return payload, fmt.Errorf("所选 OVH 账户已不存在")
	}
	servers, err := loadRebootServers(state, account)
	if err != nil {
		_, _ = transitionRebootFlow(state, row, rebootStageLoading, rebootStageAccount, payload)
		return payload, err
	}
	if len(servers) == 0 {
		_, _ = transitionRebootFlow(state, row, rebootStageLoading, rebootStageAccount, payload)
		return payload, fmt.Errorf("所选账户下没有可重启的独立服务器")
	}
	payload.AccountID = account.ID
	payload.AccountName = accountDisplayName(account)
	payload.Servers = servers
	if ok, err := transitionRebootFlow(state, row, rebootStageLoading, rebootStageServer, payload); err != nil || !ok {
		if err != nil {
			return payload, err
		}
		return payload, fmt.Errorf("账号选择已被新的操作替代")
	}
	return payload, nil
}

func selectRebootServer(state *app.State, flowID, channel, actorID, chatID string, index int) (rebootServerChoice, error) {
	row, payload, err := readRebootFlow(state, flowID, channel, actorID, chatID, rebootStageServer)
	if err != nil {
		return rebootServerChoice{}, err
	}
	if index < 0 || index >= len(payload.Servers) {
		return rebootServerChoice{}, fmt.Errorf("服务器选择无效，请重新发送 /reboot")
	}
	server := payload.Servers[index]
	payload.Selected = &server
	if ok, err := transitionRebootFlow(state, row, rebootStageServer, rebootStageConfirm, payload); err != nil || !ok {
		if err != nil {
			return rebootServerChoice{}, err
		}
		return rebootServerChoice{}, fmt.Errorf("服务器选择已被处理，请重新发送 /reboot")
	}
	return server, nil
}

func finishRebootFlow(state *app.State, flowID, channel, actorID, chatID string, confirm bool) (string, bool) {
	row, payload, err := readRebootFlow(state, flowID, channel, actorID, chatID, rebootStageConfirm)
	if err != nil || payload.Selected == nil {
		if err == nil {
			err = fmt.Errorf("重启确认数据无效，请重新发送 /reboot")
		}
		return "❌ " + err.Error(), false
	}
	if ok, err := transitionRebootFlow(state, row, rebootStageConfirm, rebootStageDone, payload); err != nil || !ok {
		if err != nil {
			return "❌ 确认重启失败: " + err.Error(), false
		}
		return "❌ 该确认操作已处理或已过期，请重新发送 /reboot", false
	}
	if !confirm {
		return "已取消重启操作", false
	}
	server := *payload.Selected
	client, err := state.OVH.ClientFor(server.AccountID)
	if err != nil {
		return "❌ 无法连接所选 OVH 账户: " + err.Error(), false
	}
	var result map[string]interface{}
	if err := client.Post("/dedicated/server/"+server.ServiceName+"/reboot", map[string]interface{}{}, &result); err != nil {
		if state.Logger != nil {
			state.Logger.Error("机器人重启服务器失败: "+err.Error(), "server_control")
		}
		publicError := strings.ReplaceAll(err.Error(), server.ServiceName, "所选服务器")
		return "❌ 重启命令发送失败: " + publicError, false
	}
	if state.Logger != nil {
		state.Logger.Info("机器人已发送服务器重启命令: account="+server.AccountID+" service="+server.ServiceName, "server_control")
	}
	message := "✅ 已发送服务器重启命令\n\n服务器: " + rebootServerLabel(server)
	if taskID := fmt.Sprintf("%v", result["taskId"]); taskID != "<nil>" && taskID != "" {
		message += "\n任务 ID: " + taskID
	}
	return message, true
}

func rebootConfirmationText(server rebootServerChoice) string {
	return fmt.Sprintf("确认重启以下服务器吗？\n\n账户: %s\n服务器: %s\n当前状态: %s\n\n确认后将立即向 OVH 发送重启命令。",
		server.AccountName, rebootServerLabel(server), server.State)
}

func callbackIndex(value interface{}) (int, bool) {
	switch number := value.(type) {
	case float64:
		index := int(number)
		return index, number == float64(index)
	case int:
		return number, true
	case json.Number:
		value, err := number.Int64()
		return int(value), err == nil
	default:
		return 0, false
	}
}

func handleTelegramRebootCallback(state *app.State, action string, values map[string]interface{}, cbID string, chatID, userID interface{}, messageID int64) bool {
	if action != "rba" && action != "rbs" && action != "rbc" {
		return false
	}
	flowID, _ := values["f"].(string)
	actorKey := telegram.ChatIDString(userID)
	chatKey := telegram.ChatIDString(chatID)

	switch action {
	case "rba":
		index, ok := callbackIndex(values["i"])
		if !ok {
			telegram.AnswerCallback(state, cbID, "账号选择无效", true)
			return true
		}
		telegram.AnswerCallback(state, cbID, "正在读取服务器", false)
		payload, err := selectRebootAccount(state, flowID, "telegram", actorKey, chatKey, index)
		if err != nil {
			telegram.SendReply(state, chatID, "❌ "+err.Error(), messageID)
			return true
		}
		telegram.SendReplyWithMarkup(state, chatID, "已选择账户："+payload.AccountName+"\n\n请选择要重启的服务器：", messageID,
			rebootServerKeyboard(flowID, payload.Servers))
	case "rbs":
		index, ok := callbackIndex(values["i"])
		if !ok {
			telegram.AnswerCallback(state, cbID, "服务器选择无效", true)
			return true
		}
		server, err := selectRebootServer(state, flowID, "telegram", actorKey, chatKey, index)
		if err != nil {
			telegram.AnswerCallback(state, cbID, err.Error(), true)
			return true
		}
		telegram.AnswerCallback(state, cbID, "请确认是否重启", false)
		telegram.SendReplyWithMarkup(state, chatID, rebootConfirmationText(server), messageID, rebootConfirmKeyboard(flowID))
	case "rbc":
		decision, ok := callbackIndex(values["d"])
		if !ok {
			telegram.AnswerCallback(state, cbID, "确认操作无效", true)
			return true
		}
		message, rebooted := finishRebootFlow(state, flowID, "telegram", actorKey, chatKey, decision == 1)
		if rebooted {
			telegram.AnswerCallback(state, cbID, "重启命令已发送", false)
		} else {
			telegram.AnswerCallback(state, cbID, "操作已结束", false)
		}
		telegram.SendReply(state, chatID, message, messageID)
	}
	return true
}

func feishuRebootActions(menu rebootPreparedMenu) []interface{} {
	actions := []interface{}{}
	if menu.Stage == rebootStageAccount {
		for i, account := range menu.Payload.Accounts {
			label := account.Name
			if account.Zone != "" {
				label += " · " + account.Zone
			}
			if account.IsDefault {
				label = "✓ " + label
			}
			actions = append(actions, map[string]interface{}{
				"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": label}, "type": "default",
				"value": monitor.FeishuCardAction("reboot_select_account", map[string]interface{}{"flow_id": menu.FlowID, "index": i}),
			})
		}
		return actions
	}
	for i, server := range menu.Payload.Servers {
		actions = append(actions, map[string]interface{}{
			"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": rebootServerLabel(server)}, "type": "default",
			"value": monitor.FeishuCardAction("reboot_select_server", map[string]interface{}{"flow_id": menu.FlowID, "index": i}),
		})
	}
	return actions
}

func sendFeishuRebootMenu(state *app.State, openID string) error {
	menu, err := prepareRebootFlow(state, "feishu", openID, openID)
	if err != nil {
		return monitor.FeishuSendText(state, openID, "❌ "+err.Error())
	}
	if menu.Stage == rebootStageAccount {
		return monitor.FeishuSendCard(state, openID, monitor.FeishuTextCard(
			"选择 OVH 账户", "请选择服务器所属的 OVH 账户（✓ 为默认账户）：", "blue", feishuRebootActions(menu)))
	}
	text := fmt.Sprintf("当前仅配置一个默认 OVH 账户：%s\n\n请选择要重启的服务器：", menu.Payload.AccountName)
	return monitor.FeishuSendCard(state, openID, monitor.FeishuTextCard(
		"选择要重启的服务器", text, "blue", feishuRebootActions(menu)))
}

func processFeishuRebootAction(state *app.State, openID, action string, values map[string]interface{}) (string, bool) {
	flowID, _ := values["flow_id"].(string)
	switch action {
	case "reboot_select_account":
		index, ok := callbackIndex(values["index"])
		if !ok {
			return "账号选择无效", false
		}
		payload, err := selectRebootAccount(state, flowID, "feishu", openID, openID, index)
		if err != nil {
			return err.Error(), false
		}
		menu := rebootPreparedMenu{FlowID: flowID, Stage: rebootStageServer, Payload: payload}
		if err := monitor.FeishuSendCard(state, openID, monitor.FeishuTextCard(
			"选择要重启的服务器", "已选择账户："+payload.AccountName+"\n\n请选择要重启的服务器：", "blue", feishuRebootActions(menu))); err != nil {
			return "服务器卡片发送失败: " + err.Error(), false
		}
		return "请选择服务器", false
	case "reboot_select_server":
		index, ok := callbackIndex(values["index"])
		if !ok {
			return "服务器选择无效", false
		}
		server, err := selectRebootServer(state, flowID, "feishu", openID, openID, index)
		if err != nil {
			return err.Error(), false
		}
		actions := []interface{}{
			map[string]interface{}{
				"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": "确认重启"}, "type": "danger",
				"value": monitor.FeishuCardAction("reboot_confirm", map[string]interface{}{"flow_id": flowID, "decision": "confirm"}),
			},
			map[string]interface{}{
				"tag": "button", "text": map[string]interface{}{"tag": "plain_text", "content": "取消"}, "type": "default",
				"value": monitor.FeishuCardAction("reboot_confirm", map[string]interface{}{"flow_id": flowID, "decision": "cancel"}),
			},
		}
		if err := monitor.FeishuSendCard(state, openID, monitor.FeishuTextCard(
			"确认重启服务器", rebootConfirmationText(server), "red", actions)); err != nil {
			return "确认卡片发送失败: " + err.Error(), false
		}
		return "请确认是否重启", false
	case "reboot_confirm":
		decision, _ := values["decision"].(string)
		return finishRebootFlow(state, flowID, "feishu", openID, openID, decision == "confirm")
	default:
		return "未知重启操作", false
	}
}
