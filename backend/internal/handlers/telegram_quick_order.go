package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
)

// TelegramQuickOrder POST /api/telegram/quick-order
// 网页「Telegram 下单」页执行与 Bot 相同的命令语义（不经过 Telegram 网络）。
// body: { mode, planCode, datacenter?, quantity?, options? }
func TelegramQuickOrder(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Mode       string   `json:"mode"`
			PlanCode   string   `json:"planCode"`
			Datacenter string   `json:"datacenter"`
			Quantity   int      `json:"quantity"`
			Options    []string `json:"options"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "无效的请求体"})
			return
		}
		mode := strings.ToLower(strings.TrimSpace(body.Mode))
		planCode := strings.TrimSpace(body.PlanCode)
		dc := strings.ToLower(strings.TrimSpace(body.Datacenter))

		if mode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 mode"})
			return
		}
		if mode == "help" || mode == "start" {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": telegram.HelpMessage()})
			return
		}
		if planCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 planCode"})
			return
		}

		args, errMsg := buildTelegramCommandArgs(mode, planCode, dc, body.Quantity, body.Options)
		if errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
			return
		}

		cmd := &telegram.BotCommand{
			Name: mode,
			Args: args,
			Raw:  "/" + mode + " " + strings.Join(args, " "),
		}
		reply := dispatchTelegramCommand(state, mon, cmd)
		success := !strings.HasPrefix(strings.TrimSpace(reply), "❌")
		errField := ""
		if !success {
			errField = reply
		}
		c.JSON(http.StatusOK, gin.H{
			"success": success,
			"message": reply,
			"error":   errField,
			"mode":    mode,
			"command": cmd.Raw,
		})
	}
}

// FeishuQuickOrder POST /api/feishu/quick-order
// 网页「飞书下单」页按当前飞书绑定账户执行与机器人相同的命令语义。
func FeishuQuickOrder(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Mode       string   `json:"mode"`
			PlanCode   string   `json:"planCode"`
			Datacenter string   `json:"datacenter"`
			Quantity   int      `json:"quantity"`
			Options    []string `json:"options"`
			AccountID  string   `json:"accountId"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "无效的请求体"})
			return
		}

		mode := strings.ToLower(strings.TrimSpace(body.Mode))
		planCode := strings.TrimSpace(body.PlanCode)
		dc := strings.ToLower(strings.TrimSpace(body.Datacenter))
		accountID := strings.TrimSpace(body.AccountID)
		if !monitor.FeishuEnabled(state) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "飞书应用未启用或配置不完整"})
			return
		}
		if accountID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 accountId"})
			return
		}
		if _, ok := state.FindAccount(accountID); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "指定的 OVH 账户不存在"})
			return
		}
		binding, ok := monitor.FeishuBindingForAccount(state, accountID)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "当前 OVH 账户尚未绑定飞书用户"})
			return
		}
		if mode == "help" || mode == "start" {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": telegram.HelpMessage()})
			return
		}
		if mode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 mode"})
			return
		}
		if planCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "缺少 planCode"})
			return
		}

		args, errMsg := buildTelegramCommandArgs(mode, planCode, dc, body.Quantity, body.Options)
		if errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
			return
		}
		cmd := &telegram.BotCommand{Name: mode, Args: args, Raw: "/" + mode + " " + strings.Join(args, " ")}
		reply := dispatchBotCommand(state, mon, cmd, accountID, "feishu")
		success := !strings.HasPrefix(strings.TrimSpace(reply), "❌")
		if err := monitor.FeishuSendText(state, binding.OpenID, reply); err != nil {
			state.Logger.Warn("飞书网页下单结果回传失败: "+err.Error(), "feishu")
		}
		errField := ""
		if !success {
			errField = reply
		}
		c.JSON(http.StatusOK, gin.H{"success": success, "message": reply, "error": errField, "mode": mode, "command": cmd.Raw})
	}
}

func buildTelegramCommandArgs(mode, planCode, dc string, quantity int, options []string) ([]string, string) {
	switch mode {
	case "stock":
		return []string{planCode}, ""
	case "queue", "buy":
		args := []string{planCode}
		if dc != "" {
			args = append(args, dc)
		}
		if quantity > 1 {
			args = append(args, strconv.Itoa(quantity))
		}
		if len(options) > 0 {
			args = append(args, strings.Join(options, ","))
		}
		return args, ""
	case "monitor":
		args := []string{planCode}
		if dc != "" {
			for _, part := range strings.FieldsFunc(dc, func(r rune) bool {
				return r == ',' || r == ' ' || r == ';'
			}) {
				part = strings.TrimSpace(part)
				if part != "" {
					args = append(args, part)
				}
			}
		}
		return args, ""
	case "price":
		if dc == "" {
			return nil, "price 模式需要 datacenter"
		}
		return []string{planCode, dc}, ""
	default:
		return nil, "未知 mode: " + mode + "（支持 stock/queue/buy/monitor/price）"
	}
}

// RegisterTelegramCommands POST /api/telegram/register-commands
// 单独注册 Bot 命令菜单（不改 webhook）。
func RegisterTelegramCommands(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if errMsg := telegram.SetMyCommands(state); errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Bot 命令菜单已注册"})
	}
}
