package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

func feishuJSONBody(c *gin.Context) ([]byte, map[string]interface{}, error) {
	raw, err := c.GetRawData()
	if err != nil { return nil, nil, err }
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil { return raw, nil, err }
	return raw, body, nil
}

func feishuHeaderValues(body map[string]interface{}) (string, string) {
	token, _ := body["token"].(string)
	appID, _ := body["app_id"].(string)
	header, _ := body["header"].(map[string]interface{})
	if token == "" {
		token, _ = header["token"].(string)
	}
	if appID == "" {
		appID, _ = header["app_id"].(string)
	}
	if event, ok := body["event"].(map[string]interface{}); ok {
		if eventHeader, ok := event["header"].(map[string]interface{}); ok {
			if token == "" {
				token, _ = eventHeader["token"].(string)
			}
			if appID == "" {
				appID, _ = eventHeader["app_id"].(string)
			}
		}
	}
	return token, appID
}

func FeishuEvents(state *app.State) gin.HandlerFunc {
	return FeishuEventsWithMonitor(state, nil)
}

const feishuEventRetentionDays = 7

// FeishuEventsWithMonitor 同时处理全局接收人绑定与飞书私聊命令。
func FeishuEventsWithMonitor(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, body, err := feishuJSONBody(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid json"})
			return
		}
		token, appID := feishuHeaderValues(body)
		if !monitor.FeishuVerifyRequest(state, raw, token, appID, c.GetHeader("X-Lark-Request-Timestamp"), c.GetHeader("X-Lark-Request-Nonce"), c.GetHeader("X-Lark-Signature")) {
			c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid token"})
			return
		}
		body, err = monitor.FeishuUnwrapPayload(state, body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()})
			return
		}
		token, appID = feishuHeaderValues(body)
		if !monitor.FeishuVerifyIdentity(state, token, appID) {
			c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid identity"})
			return
		}
		if challenge, ok := body["challenge"].(string); ok && challenge != "" {
			c.JSON(http.StatusOK, gin.H{"challenge": challenge})
			return
		}
		if !claimFeishuEvent(state, body) {
			c.JSON(http.StatusOK, gin.H{"code": 0, "duplicate": true})
			return
		}
		event, _ := body["event"].(map[string]interface{})
		message, _ := event["message"].(map[string]interface{})
		chatType, _ := message["chat_type"].(string)
		contentText, _ := message["content"].(string)
		var content map[string]interface{}
		_ = json.Unmarshal([]byte(contentText), &content)
		text, _ := content["text"].(string)
		openID := feishuOpenID(body)
		if openID != "" && chatType == "p2p" {
			// 合法私聊事件的发送人即全局飞书接收人；业务命令统一使用当时的默认 OVH 账户。
			if binding, ok := monitor.FeishuDefaultBinding(state); !ok || binding.OpenID != openID {
				_ = monitor.FeishuSaveDefaultBinding(state, types.FeishuBinding{AccountID: "default", OpenID: openID, Name: openID, UpdatedAt: types.NowISO()})
			}
			trimmed := strings.TrimSpace(text)
			if trimmed != "" && !telegram.AllowRate("feishu:"+openID) {
				_ = monitor.FeishuSendText(state, openID, "⚠️ 操作过于频繁，请稍后再试")
				c.JSON(http.StatusOK, gin.H{"code": 0})
				return
			}
			accountID := telegram.DefaultAccountID(state)
			_, bound := monitor.FeishuDefaultBinding(state)
			if strings.HasPrefix(trimmed, "绑定账户 ") {
				_ = monitor.FeishuSendText(state, openID, "飞书接收人已全局绑定；命令和通知始终使用当前默认 OVH 账户，无需再指定账户。")
				c.JSON(http.StatusOK, gin.H{"code": 0})
				return
			}
			if trimmed == "账户状态" || strings.EqualFold(trimmed, "status") {
				_ = monitor.FeishuSendText(state, openID, feishuAccountStatusText(state))
			} else if strings.EqualFold(trimmed, "help") || trimmed == "帮助" || trimmed == "?" {
				_ = monitor.FeishuSendText(state, openID, telegram.HelpMessage()+"\n当前账户：系统设置中的默认 OVH 账户")
			} else if cmd := telegram.ParseBotCommand(trimmed); cmd != nil {
				if cmd.Name == "start" || cmd.Name == "help" {
					_ = monitor.FeishuSendText(state, openID, telegram.HelpMessage()+"\n当前账户：系统设置中的默认 OVH 账户")
				} else if cmd.Name == "account" && isAccountSwitchRequest(cmd.Args) {
					_ = sendFeishuAccountMenu(state, openID)
				} else if cmd.Name == "account" {
					_ = monitor.FeishuSendText(state, openID, accountCommandText(state, cmd.Args, "feishu"))
				} else if cmd.Name == "reboot" {
					if len(cmd.Args) > 0 {
						_ = monitor.FeishuSendText(state, openID, "用法：/reboot")
					} else {
						_ = sendFeishuRebootMenu(state, openID)
					}
				} else if !bound {
					_ = monitor.FeishuSendText(state, openID, "请先在飞书中私聊机器人完成全局接收人绑定")
				} else if accountID == "" {
					_ = monitor.FeishuSendText(state, openID, "请先在系统设置中配置默认 OVH 账户")
				} else {
					_ = monitor.FeishuSendText(state, openID, dispatchBotCommand(state, mon, cmd, accountID, "feishu"))
				}
			} else if plans := findServerPlansByModel(state, trimmed); len(plans) > 0 {
				// 每个 PlanCode 独立发送非表格卡片；配置过长时在完整分区之间自动分页。
				if err := sendFeishuServerPlanCards(state, openID, trimmed, plans); err != nil {
					state.Logger.Warn("发送飞书服务器型号卡片失败: "+err.Error(), "feishu")
					_ = monitor.FeishuSendText(state, openID, "❌ 服务器配置卡片发送失败，请稍后重试")
				}
			} else if looksLikeServerModelQuery(trimmed) {
				_ = monitor.FeishuSendText(state, openID, "❌ 服务器列表中未找到型号："+trimmed)
			} else if feishuLooksLikeOrder(trimmed) {
				order := telegram.ParseOrderMessage(trimmed)
				if !bound {
					_ = monitor.FeishuSendText(state, openID, "请先在飞书中私聊机器人完成全局接收人绑定")
				} else if accountID == "" {
					_ = monitor.FeishuSendText(state, openID, "请先在系统设置中配置默认 OVH 账户")
				} else if order != nil && order.PlanCode != "" {
					result := telegram.ProcessOrderForAccount(state, accountID, order.PlanCode, order.Datacenter, order.Quantity, order.Options, false)
					if result.Success {
						_ = monitor.FeishuSendText(state, openID, fmt.Sprintf("✅ 已加入抢购队列\n\n型号: %s\n机房: %s\n数量: %d\n已创建: %d 个任务", order.PlanCode, feishuDCText(order.Datacenter), telegram.ClampQuantity(order.Quantity), result.CreatedOrders))
					} else {
						_ = monitor.FeishuSendText(state, openID, "❌ 下单失败\n\n"+result.Message)
					}
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"code": 0})
	}
}

// claimFeishuEvent 在 Webhook 和长连接之间共用事件幂等记录。
func claimFeishuEvent(state *app.State, body map[string]interface{}) bool {
	eventID := feishuEventID(body)
	if state.DB == nil || eventID == "" {
		return true
	}
	claimed, err := state.DB.TryClaimFeishuEvent(eventID)
	if err != nil {
		state.Logger.Warn("飞书 event_id 幂等写入失败: "+err.Error(), "feishu")
		return true
	}
	if !claimed {
		return false
	}
	if len(eventID)%20 == 0 {
		_, _ = state.DB.CleanupFeishuEvents(float64(time.Now().Add(-feishuEventRetentionDays * 24 * time.Hour).Unix()))
	}
	return true
}

// processFeishuMessage 是长连接消息适配器使用的业务处理入口。
// body 采用飞书事件 v2 的通用 JSON 结构，与 Webhook 解析保持一致。
func processFeishuMessage(state *app.State, mon *monitor.Monitor, body map[string]interface{}) {
	event, _ := body["event"].(map[string]interface{})
	message, _ := event["message"].(map[string]interface{})
	chatType, _ := message["chat_type"].(string)
	contentText, _ := message["content"].(string)
	var content map[string]interface{}
	_ = json.Unmarshal([]byte(contentText), &content)
	text, _ := content["text"].(string)
	openID := feishuOpenID(body)
	if openID == "" || chatType != "p2p" {
		return
	}
	if binding, ok := monitor.FeishuDefaultBinding(state); !ok || binding.OpenID != openID {
		_ = monitor.FeishuSaveDefaultBinding(state, types.FeishuBinding{AccountID: "default", OpenID: openID, Name: openID, UpdatedAt: types.NowISO()})
	}
	trimmed := strings.TrimSpace(text)
	if trimmed != "" && !telegram.AllowRate("feishu:"+openID) {
		_ = monitor.FeishuSendText(state, openID, "⚠️ 操作过于频繁，请稍后再试")
		return
	}
	accountID := telegram.DefaultAccountID(state)
	_, bound := monitor.FeishuDefaultBinding(state)
	if strings.HasPrefix(trimmed, "绑定账户 ") {
		_ = monitor.FeishuSendText(state, openID, "飞书接收人已全局绑定；命令和通知始终使用当前默认 OVH 账户，无需再指定账户")
		return
	}
	if trimmed == "账户状态" || strings.EqualFold(trimmed, "status") {
		_ = monitor.FeishuSendText(state, openID, feishuAccountStatusText(state))
	} else if strings.EqualFold(trimmed, "help") || trimmed == "帮助" || trimmed == "?" {
		_ = monitor.FeishuSendText(state, openID, telegram.HelpMessage()+"\n当前账户：系统设置中的默认 OVH 账户")
	} else if cmd := telegram.ParseBotCommand(trimmed); cmd != nil {
		if cmd.Name == "start" || cmd.Name == "help" {
			_ = monitor.FeishuSendText(state, openID, telegram.HelpMessage()+"\n当前账户：系统设置中的默认 OVH 账户")
		} else if cmd.Name == "account" && isAccountSwitchRequest(cmd.Args) {
			_ = sendFeishuAccountMenu(state, openID)
		} else if cmd.Name == "account" {
			_ = monitor.FeishuSendText(state, openID, accountCommandText(state, cmd.Args, "feishu"))
		} else if cmd.Name == "reboot" {
			if len(cmd.Args) > 0 {
				_ = monitor.FeishuSendText(state, openID, "用法：/reboot")
			} else {
				_ = sendFeishuRebootMenu(state, openID)
			}
		} else if !bound {
			_ = monitor.FeishuSendText(state, openID, "请先在飞书中私聊机器人完成全局接收人绑定")
		} else if accountID == "" {
			_ = monitor.FeishuSendText(state, openID, "请先在系统设置中配置默认 OVH 账户")
		} else {
			_ = monitor.FeishuSendText(state, openID, dispatchBotCommand(state, mon, cmd, accountID, "feishu"))
		}
	} else if plans := findServerPlansByModel(state, trimmed); len(plans) > 0 {
		if err := sendFeishuServerPlanCards(state, openID, trimmed, plans); err != nil {
			state.Logger.Warn("发送飞书服务器型号卡片失败: "+err.Error(), "feishu")
			_ = monitor.FeishuSendText(state, openID, "⚠️ 服务器配置卡片发送失败，请稍后重试")
		}
	} else if looksLikeServerModelQuery(trimmed) {
		_ = monitor.FeishuSendText(state, openID, "⚠️ 服务器列表中未找到型号："+trimmed)
	} else if feishuLooksLikeOrder(trimmed) {
		order := telegram.ParseOrderMessage(trimmed)
		if !bound {
			_ = monitor.FeishuSendText(state, openID, "请先在飞书中私聊机器人完成全局接收人绑定")
		} else if accountID == "" {
			_ = monitor.FeishuSendText(state, openID, "请先在系统设置中配置默认 OVH 账户")
		} else if order != nil && order.PlanCode != "" {
			result := telegram.ProcessOrderForAccount(state, accountID, order.PlanCode, order.Datacenter, order.Quantity, order.Options, false)
			if result.Success {
				_ = monitor.FeishuSendText(state, openID, fmt.Sprintf("✅ 已加入抢购队列\n\n型号: %s\n机房: %s\n数量: %d\n已创建: %d 个任务", order.PlanCode, feishuDCText(order.Datacenter), telegram.ClampQuantity(order.Quantity), result.CreatedOrders))
			} else {
				_ = monitor.FeishuSendText(state, openID, "❌ 下单失败\n\n"+result.Message)
			}
		}
	}
}

type feishuCardActionResult struct {
	Type      string
	Content   string
	SendText  bool
	Action    string
	OpenID    string
}

// processFeishuCardActionBody 处理 WebSocket 卡片事件，并返回 SDK 所需的 toast 内容。
func processFeishuCardActionBody(state *app.State, body map[string]interface{}) feishuCardActionResult {
	action, _ := body["action"].(map[string]interface{})
	if event, ok := body["event"].(map[string]interface{}); ok {
		if eventAction, ok := event["action"].(map[string]interface{}); ok {
			action = eventAction
		}
	}
	value := action["value"]
	if rawValue, ok := value.(string); ok {
		_ = json.Unmarshal([]byte(rawValue), &value)
	}
	values, _ := value.(map[string]interface{})
	name, _ := values["action"].(string)
	openID := feishuOpenID(body)
	result := feishuCardActionResult{Type: "success", Content: "操作已完成", Action: name, OpenID: openID}
	if openID != "" && !telegram.AllowRate("feishu-card:"+openID) {
		result.Type = "warning"
		result.Content = "操作过于频繁，请稍后再试"
		return result
	}
	switch name {
	case "ping":
		result.Content = "飞书交互卡片连接正常"
	case "switch_account":
		binding, bound := monitor.FeishuDefaultBinding(state)
		if !bound || openID == "" || binding.OpenID != openID {
			result.Content = "当前飞书用户不是全局通知接收人"
			break
		}
		accountID, _ := values["account_id"].(string)
		account, err := switchDefaultAccount(state, accountID)
		if err != nil {
			state.Logger.Warn("飞书切换默认账户失败: "+err.Error(), "feishu")
			result.Content = "切换失败：" + err.Error()
		} else {
			result.Content = "✅ 默认 OVH 账户已切换为：" + accountDisplayName(account)
			result.SendText = true
			state.Logger.Info("飞书切换默认 OVH 账户: account="+account.ID+" open_id="+openID, "feishu")
		}
	case "add_to_queue":
		binding, bound := monitor.FeishuDefaultBinding(state)
		if !bound || openID == "" || binding.OpenID != openID {
			result.Content = "当前飞书用户不是全局通知接收人"
		} else {
			result.Content = feishuEnqueue(state, values)
			result.SendText = true
		}
	case "reboot_select_account", "reboot_select_server", "reboot_confirm":
		binding, bound := monitor.FeishuDefaultBinding(state)
		if !bound || openID == "" || binding.OpenID != openID {
			result.Content = "当前飞书用户不是全局通知接收人"
		} else {
			result.Content, _ = processFeishuRebootAction(state, openID, name, values)
			if strings.HasPrefix(result.Content, "❌") || strings.Contains(result.Content, "失败") || strings.Contains(result.Content, "已过期") {
				result.Type = "error"
			}
			result.SendText = name == "reboot_confirm"
		}
	default:
		result.Content = "未知操作，请重新打开最新通知卡片"
	}
	return result
}

func feishuEventID(body map[string]interface{}) string {
	header, _ := body["header"].(map[string]interface{})
	if eventID, ok := header["event_id"].(string); ok && eventID != "" {
		return eventID
	}
	event, _ := body["event"].(map[string]interface{})
	eventHeader, _ := event["header"].(map[string]interface{})
	eventID, _ := eventHeader["event_id"].(string)
	return eventID
}

func feishuLooksLikeOrder(text string) bool {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 || len(parts) > 4 {
		return false
	}
	planCode := parts[0]
	if strings.HasPrefix(planCode, "/") || strings.ContainsAny(planCode, "：:，,。！？!?“”\"") {
		return false
	}
	hasDigit := false
	for _, r := range planCode {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	return hasDigit
}

func feishuDCText(datacenter string) string {
	if strings.TrimSpace(datacenter) == "" {
		return "自动选择机房"
	}
	return strings.ToUpper(datacenter)
}

func feishuAccountForOpenID(state *app.State, openID string) (string, bool) {
	binding, ok := monitor.FeishuDefaultBinding(state)
	if !ok || binding.OpenID != openID {
		return "", false
	}
	accountID := telegram.DefaultAccountID(state)
	return accountID, accountID != ""
}

func FeishuCardAction(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, body, err := feishuJSONBody(c)
		if err != nil { c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid json"}); return }
		token, appID := feishuHeaderValues(body)
		if !monitor.FeishuVerifyRequest(state, raw, token, appID, c.GetHeader("X-Lark-Request-Timestamp"), c.GetHeader("X-Lark-Request-Nonce"), c.GetHeader("X-Lark-Signature")) {
			c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid token"}); return
		}
		body, err = monitor.FeishuUnwrapPayload(state, body)
		if err != nil { c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()}); return }
		token, appID = feishuHeaderValues(body)
		if !monitor.FeishuVerifyIdentity(state, token, appID) { c.JSON(http.StatusForbidden, gin.H{"code": 1, "msg": "invalid identity"}); return }
		if challenge, ok := body["challenge"].(string); ok && challenge != "" { c.JSON(http.StatusOK, gin.H{"challenge": challenge}); return }
		if !claimFeishuEvent(state, body) {
			c.JSON(http.StatusOK, gin.H{"toast": gin.H{"type": "success", "content": "操作已处理"}})
			return
		}
		result := processFeishuCardActionBody(state, body)
		c.JSON(http.StatusOK, gin.H{"toast": gin.H{"type": result.Type, "content": result.Content}})
		if result.SendText && result.OpenID != "" {
			_ = monitor.FeishuSendText(state, result.OpenID, result.Content)
		}
	}
}

func FeishuBinding(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		binding, ok := monitor.FeishuDefaultBinding(state)
		accountID := telegram.DefaultAccountID(state)
		c.JSON(http.StatusOK, gin.H{"success": true, "accountId": accountID, "bound": ok, "binding": binding})
	}
}

func ClearFeishuBinding(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := monitor.FeishuDeleteDefaultBinding(state); err != nil { c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}); return }
		c.JSON(http.StatusOK, gin.H{"success": true, "cleared": true})
	}
}

func FeishuTestCard(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		binding, ok := monitor.FeishuDefaultBinding(state)
		if !ok { c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "尚未绑定全局飞书接收人"}); return }
		if !monitor.FeishuEnabled(state) { c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "飞书应用未启用或配置不完整"}); return }
		if err := monitor.FeishuSendStreamingCard(state, binding.OpenID, monitor.FeishuTestCard()); err != nil { c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}); return }
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "测试交互卡片已发送"})
	}
}

func feishuOpenID(body map[string]interface{}) string {
	if id, ok := body["open_id"].(string); ok && id != "" {
		return id
	}
	if operator, ok := body["operator"].(map[string]interface{}); ok {
		if id, ok := operator["open_id"].(string); ok && id != "" {
			return id
		}
		if operatorID, ok := operator["operator_id"].(map[string]interface{}); ok {
			if id, ok := operatorID["open_id"].(string); ok && id != "" {
				return id
			}
		}
	}
	event, _ := body["event"].(map[string]interface{})
	operator, _ := event["operator"].(map[string]interface{})
	if id, ok := operator["open_id"].(string); ok && id != "" {
		return id
	}
	if operatorID, ok := operator["operator_id"].(map[string]interface{}); ok {
		if id, ok := operatorID["open_id"].(string); ok && id != "" {
			return id
		}
	}
	sender, _ := event["sender"].(map[string]interface{})
	ids, _ := sender["sender_id"].(map[string]interface{})
	id, _ := ids["open_id"].(string)
	return id
}

func feishuAccountStatusText(state *app.State) string {
	accounts, _ := state.DB.ListAccounts()
	if len(accounts) == 0 { return "当前没有配置 OVH 账户" }
	lines := []string{"OVH 账户状态"}
	for _, acc := range accounts { lines = append(lines, "• "+acc.Name+" ("+acc.Zone+")：已配置") }
	return strings.Join(lines, "\n")
}

func feishuEnqueue(state *app.State, values map[string]interface{}) string {
	uuid, _ := values["uuid"].(string)
	if uuid == "" || state.DB == nil { return "按钮协议已升级，请等待新的通知卡片" }
	row, ok, err := state.DB.ClaimTelegramButton(uuid)
	if err != nil { return "按钮处理失败："+err.Error() }
	if !ok { return "按钮已使用、已过期或不存在" }
	configInfo := dbParseConfigInfo(row.ConfigInfo)
	accountID, _ := configInfo["accountId"].(string)
	if accountID == "" { rollbackTelegramButton(state, uuid, "飞书通知未冻结账户"); return "通知未冻结账户，请等待新的通知卡片" }
	if _, ok := state.FindAccount(accountID); !ok { rollbackTelegramButton(state, uuid, "飞书通知账户不存在"); return "通知对应的 OVH 账户已不存在" }
	options := dbParseOptions(row.Options)
	result := telegram.EnqueueSingle(state, accountID, row.PlanCode, row.Datacenter, options, true)
	if !result.Success { rollbackTelegramButton(state, uuid, "飞书入队失败"); return "入队失败："+result.Message }
	return fmt.Sprintf("✅ %s (%s) 已加入购买队列", row.PlanCode, strings.ToUpper(row.Datacenter))
}
