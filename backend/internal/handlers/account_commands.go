package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

func isAccountSwitchRequest(args []string) bool {
	return len(args) == 1 && strings.EqualFold(strings.TrimSpace(args[0]), "switch")
}

func accountChannelName(channel string) string {
	if channel == "feishu" {
		return "飞书"
	}
	if channel == "weixin" {
		return "微信"
	}
	return "Telegram"
}

func accountDisplayName(account types.OVHAccount) string {
	name := strings.TrimSpace(account.Name)
	if name == "" {
		name = account.ID
	}
	return name
}

func accountCommandText(state *app.State, args []string, channel string) string {
	if len(args) > 0 {
		if isAccountSwitchRequest(args) {
			return "请选择要切换到的 OVH 账户："
		}
		return "用法：/account 或 /account switch"
	}
	account, ok := state.FindAccount("")
	if !ok {
		return "❌ 当前没有配置 OVH 账户"
	}
	return fmt.Sprintf("👤 当前%s使用的 OVH 账户\n\n名称：%s\n区域：%s\n接口：%s\n\n切换账户：/account switch",
		accountChannelName(channel), accountDisplayName(account), account.Zone, account.Endpoint)
}

func listAccountChoices(state *app.State) ([]types.OVHAccount, error) {
	if state.DB == nil {
		return nil, fmt.Errorf("账户数据库不可用")
	}
	return state.DB.ListAccounts()
}

func switchDefaultAccount(state *app.State, accountID string) (types.OVHAccount, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || state.DB == nil {
		return types.OVHAccount{}, fmt.Errorf("账户参数无效")
	}
	var account types.OVHAccount
	var previousDefault types.OVHAccount
	var hadPreviousDefault bool
	err := state.WithAccountMutationRollback(func() error {
		var ok bool
		var err error
		account, ok, err = state.DB.GetAccount(accountID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("所选账户不存在或已被删除")
		}
		previousDefault, hadPreviousDefault, err = state.DB.GetDefaultAccount()
		if err != nil {
			return err
		}
		if err := state.DB.SetDefaultAccount(accountID); err != nil {
			return err
		}
		return nil
	}, func() error {
		return restoreDefaultAccount(state, previousDefault, hadPreviousDefault)
	})
	if err != nil {
		return types.OVHAccount{}, err
	}
	return account, nil
}

func sendTelegramAccountMenu(state *app.State, chatID interface{}, replyToMessageID int64) {
	accounts, err := listAccountChoices(state)
	if err != nil {
		telegram.SendReply(state, chatID, "❌ 无法读取 OVH 账户："+err.Error(), replyToMessageID)
		return
	}
	if len(accounts) == 0 {
		telegram.SendReply(state, chatID, "❌ 当前没有配置 OVH 账户", replyToMessageID)
		return
	}
	type button struct {
		Text         string `json:"text"`
		CallbackData string `json:"callback_data"`
	}
	keyboard := make([][]button, 0, len(accounts))
	for _, account := range accounts {
		label := accountDisplayName(account)
		if account.Zone != "" {
			label += " · " + account.Zone
		}
		if account.IsDefault {
			label = "✓ " + label
		}
		callback, _ := json.Marshal(map[string]string{"a": "as", "i": account.ID})
		keyboard = append(keyboard, []button{{Text: label, CallbackData: string(callback)}})
	}
	telegram.SendReplyWithMarkup(state, chatID, "请选择要切换到的 OVH 账户（✓ 为当前账户）：", replyToMessageID,
		map[string]interface{}{"inline_keyboard": keyboard})
}

func sendFeishuAccountMenu(state *app.State, openID string) error {
	accounts, err := listAccountChoices(state)
	if err != nil {
		return monitor.FeishuSendText(state, openID, "❌ 无法读取 OVH 账户："+err.Error())
	}
	if len(accounts) == 0 {
		return monitor.FeishuSendText(state, openID, "❌ 当前没有配置 OVH 账户")
	}
	actions := make([]interface{}, 0, len(accounts))
	for _, account := range accounts {
		label := accountDisplayName(account)
		if account.Zone != "" {
			label += " · " + account.Zone
		}
		buttonType := "default"
		if account.IsDefault {
			label = "✓ " + label
			buttonType = "primary"
		}
		actions = append(actions, map[string]interface{}{
			"tag":   "button",
			"text":  map[string]interface{}{"tag": "plain_text", "content": label},
			"type":  buttonType,
			"value": monitor.FeishuCardAction("switch_account", map[string]interface{}{"account_id": account.ID}),
		})
	}
	card := monitor.FeishuTextCard("切换 OVH 账户", "请选择要切换到的账户（✓ 为当前账户）：", "blue", actions)
	return monitor.FeishuSendCard(state, openID, card)
}
