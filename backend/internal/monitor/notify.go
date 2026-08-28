package monitor

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/telegram"
)

var dcDisplayMapCN = map[string]string{
	"gra": "🇫🇷 法国·格拉沃利讷",
	"rbx": "🇫🇷 法国·鲁贝",
	"sbg": "🇫🇷 法国·斯特拉斯堡",
	"bhs": "🇨🇦 加拿大·博舍维尔",
	"syd": "🇦🇺 澳大利亚·悉尼",
	"sgp": "🇸🇬 新加坡",
	"ynm": "🇮🇳 印度·孟买",
	"waw": "🇵🇱 波兰·华沙",
	"fra": "🇩🇪 德国·法兰克福",
	"lon": "🇬🇧 英国·伦敦",
	"par": "🇫🇷 法国·巴黎",
	"eri": "🇮🇹 意大利·埃里切",
	"lim": "🇵🇱 波兰·利马诺瓦",
	"vin": "🇺🇸 美国·弗吉尼亚",
	"hil": "🇺🇸 美国·俄勒冈",
}

var dcDisplayShort = map[string]string{
	"gra": "🇫🇷 Gra",
	"rbx": "🇫🇷 Rbx",
	"sbg": "🇫🇷 Sbg",
	"bhs": "🇨🇦 Bhs",
	"syd": "🇦🇺 Syd",
	"sgp": "🇸🇬 Sgp",
	"ynm": "🇮🇳 Mum",
	"waw": "🇵🇱 Waw",
	"fra": "🇩🇪 Fra",
	"lon": "🇬🇧 Lon",
	"par": "🇫🇷 Par",
	"eri": "🇮🇹 Eri",
	"lim": "🇵🇱 Lim",
	"vin": "🇺🇸 Vin",
	"hil": "🇺🇸 Hil",
}

func dcDisplayCN(dc string) string {
	if v, ok := dcDisplayMapCN[strings.ToLower(dc)]; ok {
		return v
	}
	return strings.ToUpper(dc)
}

func dcDisplayShortName(dc string) string {
	if v, ok := dcDisplayShort[strings.ToLower(dc)]; ok {
		return v
	}
	return strings.ToUpper(dc)
}

func appendPriceBlock(msg *strings.Builder, priceText string) {
	priceText = strings.TrimSpace(priceText)
	if priceText == "" {
		return
	}
	msg.WriteString("\n💰 价格:\n")
	msg.WriteString(priceText)
	msg.WriteString("\n")
}

// SendAvailabilityAlertGrouped 对应 Python: send_availability_alert_grouped
func (m *Monitor) SendAvailabilityAlertGrouped(planCode string, availableDCs []map[string]interface{},
	configInfo map[string]interface{}, serverName string, priceErrorMessage string, traceID, configTraceID string, expectedChannels ...[]string) NotificationDeliveryResult {

	var msg strings.Builder
	msg.WriteString("🎉 服务器上架通知！\n\n")
	if serverName != "" {
		msg.WriteString("服务器: " + serverName + "\n")
	}
	msg.WriteString("型号: " + planCode + "\n")
	if configInfo != nil {
		display, _ := configInfo["display"].(string)
		memory, _ := configInfo["memory"].(string)
		storage, _ := configInfo["storage"].(string)
		msg.WriteString("配置: " + display + "\n")
		msg.WriteString("├─ 内存: " + memory + "\n")
		msg.WriteString("└─ 存储: " + storage + "\n")
	}

	priceText, _ := configInfo["cached_price"].(string)
	if priceText != "" {
		appendPriceBlock(&msg, priceText)
	} else {
		appendPriceBlock(&msg, unavailablePriceText())
		if priceErrorMessage != "" {
			msg.WriteString("\n⚠️ 价格提示：" + priceErrorMessage + "\n")
		}
	}

	msg.WriteString(fmt.Sprintf("\n✅ 有货的机房 (%d个):\n", len(availableDCs)))
	var detectedTimes []time.Time
	for _, dcInfo := range availableDCs {
		dc, _ := dcInfo["dc"].(string)
		msg.WriteString("  • " + dcDisplayCN(dc) + " (" + strings.ToUpper(dc) + ")")
		if dt, ok := dcInfo["duration_text"].(string); ok && dt != "" {
			msg.WriteString(" - ⏱️ 上次无货→本次有货: " + strings.TrimPrefix(dt, "历时 "))
		}
		msg.WriteString("\n")
		if dtStr, ok := dcInfo["detected_time"].(string); ok && dtStr != "" {
			if t, err := time.Parse(time.RFC3339Nano, dtStr); err == nil {
				detectedTimes = append(detectedTimes, t)
			}
		}
	}

	pushTime := m.nowBeijing()
	if traceID != "" || configTraceID != "" {
		if traceID != "" && configTraceID != "" {
			msg.WriteString("\n🆔 Trace ID:\n  订阅: " + traceID + "\n  配置: " + configTraceID)
		} else if traceID != "" {
			msg.WriteString("\n🆔 Trace ID: " + traceID)
		} else {
			msg.WriteString("\n🆔 Trace ID: " + configTraceID)
		}
	}

	if len(detectedTimes) > 0 {
		earliest := detectedTimes[0]
		for _, t := range detectedTimes[1:] {
			if t.Before(earliest) {
				earliest = t
			}
		}
		delay := pushTime.Sub(earliest)
		secs := int(delay.Seconds())
		minutes := secs / 60
		rem := secs % 60
		msg.WriteString("\n⏰ 检测时间: " + earliest.Format("2006-01-02 15:04:05"))
		msg.WriteString("\n📤 推送时间: " + pushTime.Format("2006-01-02 15:04:05"))
		switch {
		case secs > 0 && minutes > 0:
			msg.WriteString(fmt.Sprintf("\n⏱️ 推送延迟: %d分%d秒", minutes, rem))
		case secs > 0:
			msg.WriteString(fmt.Sprintf("\n⏱️ 推送延迟: %d秒", rem))
		default:
			msg.WriteString("\n⏱️ 推送延迟: <1秒")
		}
	} else {
		msg.WriteString("\n⏰ 推送时间: " + pushTime.Format("2006-01-02 15:04:05"))
	}

	// 构建按钮（每行最多 2 个）
	type btn struct {
		Text         string `json:"text"`
		CallbackData string `json:"callback_data"`
	}
	keyboard := [][]btn{}
	feishuActions := []interface{}{}
	row := []btn{}
	options := []string{}
	if configInfo != nil {
		if opts, ok := configInfo["options"].([]string); ok {
			options = opts
		} else if optsRaw, ok := configInfo["options"].([]interface{}); ok {
			for _, o := range optsRaw {
				if s, ok := o.(string); ok {
					options = append(options, s)
				}
			}
		}
	}
	expected := ConfiguredNotificationChannels(m.state)
	if len(expectedChannels) > 0 { expected = canonicalNotificationChannels(expectedChannels[0]) }
	for idx, dcInfo := range availableDCs {
		dc, _ := dcInfo["dc"].(string)
		if notificationChannelSelected(expected, NotificationChannelTelegram) {
			msgUUID := uuid.NewString()
			if err := m.AddMessageUUID(msgUUID, planCode, dc, options, configInfo); err != nil {
				m.state.Logger.Warn("持久化 Telegram 一键下单按钮失败，通知将不包含该按钮: "+err.Error(), "monitor")
			} else {
				m.state.Logger.Debug(fmt.Sprintf("生成消息UUID: %s, 配置: %s@%s, options=%v", msgUUID, planCode, dc, options), "monitor")
				cb := map[string]string{"a": "add_to_queue", "u": msgUUID}
				cbStr, _ := json.Marshal(cb)
				if len(cbStr) > 64 {
					m.state.Logger.Warn(fmt.Sprintf("UUID callback_data异常长: %d字节, UUID=%s", len(cbStr), msgUUID), "monitor")
				}
				row = append(row, btn{
					Text:         dcDisplayShortName(dc) + " 一键下单",
					CallbackData: string(cbStr),
				})
			}
		}
		// 飞书使用独立 UUID，两个渠道互不抢占同一个一次性按钮；账户已冻结在 configInfo。
		if notificationChannelSelected(expected, NotificationChannelFeishu) {
			feishuUUID := uuid.NewString()
			if err := m.AddMessageUUID(feishuUUID, planCode, dc, options, configInfo); err != nil {
				m.state.Logger.Warn("持久化飞书一键下单按钮失败，通知将不包含该按钮: "+err.Error(), "monitor")
			} else {
				feishuActions = append(feishuActions, map[string]interface{}{
					"tag":   "button",
					"text":  map[string]interface{}{"tag": "plain_text", "content": dcDisplayShortName(dc) + " 一键下单"},
					"type":  "primary",
					"value": FeishuCardAction("add_to_queue", map[string]interface{}{"uuid": feishuUUID}),
				})
			}
		}
		if len(row) >= 2 || (idx == len(availableDCs)-1 && len(row) > 0) {
			keyboard = append(keyboard, row)
			row = nil
		}
	}
	if len(keyboard) > 0 || len(feishuActions) > 0 {
		msg.WriteString("\n\n💡 点击下方按钮可直接下单对应机房！")
	}
	replyMarkup := map[string]interface{}{"inline_keyboard": keyboard}
	delivered := NotificationDeliveryResult{}
	if notificationChannelSelected(expected, NotificationChannelFeishu) {
		delivered[NotificationChannelFeishu] = FeishuSendDefaultNotification(m.state, "🎉 服务器上架通知", msg.String(), "green", feishuActions)
	}
	if notificationChannelSelected(expected, NotificationChannelWeixin) {
		delivered[NotificationChannelWeixin] = SendWeixinNotification(m.state, msg.String()+"\n\n微信下单：/buy "+planCode+" <机房代码>")
	}

	configDesc := ""
	if configInfo != nil {
		if d, ok := configInfo["display"].(string); ok {
			configDesc = " [" + d + "]"
		}
	}
	m.state.Logger.Info(fmt.Sprintf("正在发送汇总Telegram通知: %s%s - %d个机房", planCode, configDesc, len(availableDCs)), "monitor")
	tgOK := false
	if notificationChannelSelected(expected, NotificationChannelTelegram) {
		tgOK = telegram.SendMessage(m.state, msg.String(), replyMarkup)
		delivered[NotificationChannelTelegram] = tgOK
	}
	if tgOK {
		m.state.Logger.Info(fmt.Sprintf("✅ Telegram汇总通知发送成功: %s%s", planCode, configDesc), "monitor")
	} else {
		 m.state.Logger.Warn(fmt.Sprintf("⚠️ Telegram汇总通知发送失败: %s%s", planCode, configDesc), "monitor")
	}
	return delivered
}

// SendUnavailableAlertGrouped 对应 Python: send_unavailable_alert_grouped
func (m *Monitor) SendUnavailableAlertGrouped(planCode string, unavailableDCs []map[string]interface{},
	configInfo map[string]interface{}, serverName, traceID, configTraceID string, expectedChannels ...[]string) NotificationDeliveryResult {

	var msg strings.Builder
	msg.WriteString("📦 服务器下架通知\n\n")
	if serverName != "" {
		msg.WriteString("服务器: " + serverName + "\n")
	}
	msg.WriteString("型号: " + planCode + "\n")
	if configInfo != nil {
		display, _ := configInfo["display"].(string)
		memory, _ := configInfo["memory"].(string)
		storage, _ := configInfo["storage"].(string)
		msg.WriteString("配置: " + display + "\n")
		msg.WriteString("├─ 内存: " + memory + "\n")
		msg.WriteString("└─ 存储: " + storage + "\n")
	}
	msg.WriteString(fmt.Sprintf("\n已下架机房 (%d 个):\n", len(unavailableDCs)))
	for _, dcInfo := range unavailableDCs {
		dc, _ := dcInfo["dc"].(string)
		msg.WriteString("  • " + dcDisplayCN(dc) + " (" + strings.ToUpper(dc) + ")")
		if dt, ok := dcInfo["duration_text"].(string); ok && dt != "" {
			msg.WriteString(" - ⏱️ 本次上架持续: " + strings.TrimPrefix(dt, "历时 "))
		}
		msg.WriteString("\n")
	}
	if traceID != "" || configTraceID != "" {
		if traceID != "" && configTraceID != "" {
			msg.WriteString("\n🆔 Trace ID:\n  订阅: " + traceID + "\n  配置: " + configTraceID)
		} else if traceID != "" {
			msg.WriteString("\n🆔 Trace ID: " + traceID)
		} else {
			msg.WriteString("\n🆔 Trace ID: " + configTraceID)
		}
	}
	msg.WriteString("\n⏰ 时间: " + m.nowBeijing().Format("2006-01-02 15:04:05"))
	expected := ConfiguredNotificationChannels(m.state)
	if len(expectedChannels) > 0 { expected = canonicalNotificationChannels(expectedChannels[0]) }
	delivered := NotificationDeliveryResult{}
	if notificationChannelSelected(expected, NotificationChannelFeishu) {
		delivered[NotificationChannelFeishu] = FeishuSendDefaultNotification(m.state, "📦 服务器下架通知", msg.String(), "grey", nil)
	}
	if notificationChannelSelected(expected, NotificationChannelWeixin) {
		delivered[NotificationChannelWeixin] = SendWeixinNotification(m.state, msg.String())
	}

	configDesc := ""
	if configInfo != nil {
		if d, ok := configInfo["display"].(string); ok {
			configDesc = " [" + d + "]"
		}
	}
	m.state.Logger.Info(fmt.Sprintf("正在发送聚合下架Telegram通知: %s%s - %d个机房", planCode, configDesc, len(unavailableDCs)), "monitor")
	tgOK := false
	if notificationChannelSelected(expected, NotificationChannelTelegram) {
		tgOK = telegram.SendMessage(m.state, msg.String(), nil)
		delivered[NotificationChannelTelegram] = tgOK
	}
	if tgOK {
		m.state.Logger.Info(fmt.Sprintf("✅ Telegram聚合下架通知发送成功: %s%s", planCode, configDesc), "monitor")
	} else {
		m.state.Logger.Warn(fmt.Sprintf("⚠️ Telegram聚合下架通知发送失败: %s%s", planCode, configDesc), "monitor")
	}
	return delivered
}

// SendAvailabilityAlert 对应 Python: send_availability_alert
func (m *Monitor) SendAvailabilityAlert(planCode, datacenter, status, changeType string,
	configInfo map[string]interface{}, serverName, durationText, priceCheckError, traceID, configTraceID, detectedTime string, expectedChannels ...[]string) NotificationDeliveryResult {

	var msg strings.Builder
	pushTime := m.nowBeijing()

	switch changeType {
	case "available":
		msg.WriteString("🎉 服务器上架通知！\n\n")
		if serverName != "" {
			msg.WriteString("服务器: " + serverName + "\n")
		}
		msg.WriteString("型号: " + planCode + "\n")
		msg.WriteString("数据中心: " + datacenter + "\n")
		if configInfo != nil {
			display, _ := configInfo["display"].(string)
			memory, _ := configInfo["memory"].(string)
			storage, _ := configInfo["storage"].(string)
			msg.WriteString("配置: " + display + "\n")
			msg.WriteString("├─ 内存: " + memory + "\n")
			msg.WriteString("└─ 存储: " + storage + "\n")
		}
		priceText, _ := configInfo["cached_price"].(string)
		if priceText == "" {
			accountID, _ := configInfo["accountId"].(string)
			// 1:1 对应 Python server_monitor.py:1331-1392：用 30 秒超时保护，
			// 否则在 OVH 价格 API 卡死时整个通知会阻塞
			priceText, _ = m.getPriceWithTimeout(accountID, planCode, datacenter, configInfo, 30*time.Second)
		}
		if priceText != "" {
			appendPriceBlock(&msg, priceText)
		} else {
			appendPriceBlock(&msg, unavailablePriceText())
		}
		msg.WriteString("状态: " + status + "\n")
		if durationText != "" {
			msg.WriteString("⏱️ 上次无货→本次有货: " + strings.TrimPrefix(durationText, "历时 ") + "\n")
		}
		if detectedTime != "" {
			if t, err := time.Parse(time.RFC3339Nano, detectedTime); err == nil {
				delay := pushTime.Sub(t)
				secs := int(delay.Seconds())
				minutes := secs / 60
				rem := secs % 60
				msg.WriteString("⏰ 检测时间: " + t.Format("2006-01-02 15:04:05") + "\n")
				msg.WriteString("📤 推送时间: " + pushTime.Format("2006-01-02 15:04:05") + "\n")
				switch {
				case secs > 0 && minutes > 0:
					msg.WriteString(fmt.Sprintf("⏱️ 推送延迟: %d分%d秒\n", minutes, rem))
				case secs > 0:
					msg.WriteString(fmt.Sprintf("⏱️ 推送延迟: %d秒\n", rem))
				default:
					msg.WriteString("⏱️ 推送延迟: <1秒\n")
				}
			}
		} else {
			msg.WriteString("⏰ 推送时间: " + pushTime.Format("2006-01-02 15:04:05") + "\n")
		}
		if traceID != "" || configTraceID != "" {
			if traceID != "" && configTraceID != "" {
				msg.WriteString("\n🆔 Trace ID:\n  订阅: " + traceID + "\n  配置: " + configTraceID)
			} else if traceID != "" {
				msg.WriteString("\n🆔 Trace ID: " + traceID)
			} else {
				msg.WriteString("\n🆔 Trace ID: " + configTraceID)
			}
		}
		msg.WriteString("\n\n💡 快去抢购吧！")
	case "price_check_failed":
		msg.WriteString("📦 服务器可用性通知\n\n")
		if serverName != "" {
			msg.WriteString("服务器: " + serverName + "\n")
		}
		msg.WriteString("型号: " + planCode + "\n")
		msg.WriteString("数据中心: " + datacenter + "\n")
		if configInfo != nil {
			display, _ := configInfo["display"].(string)
			memory, _ := configInfo["memory"].(string)
			storage, _ := configInfo["storage"].(string)
			msg.WriteString("配置: " + display + "\n")
			msg.WriteString("├─ 内存: " + memory + "\n")
			msg.WriteString("└─ 存储: " + storage + "\n")
		}
		if priceText, ok := configInfo["cached_price"].(string); ok && priceText != "" {
			appendPriceBlock(&msg, priceText)
		} else {
			appendPriceBlock(&msg, unavailablePriceText())
		}
		msg.WriteString("\n状态: 可用性显示有货\n")
		msg.WriteString("时间: " + pushTime.Format("2006-01-02 15:04:05") + "\n")
		if traceID != "" || configTraceID != "" {
			if traceID != "" && configTraceID != "" {
				msg.WriteString("🆔 Trace ID:\n  订阅: " + traceID + "\n  配置: " + configTraceID + "\n")
			} else if traceID != "" {
				msg.WriteString("🆔 Trace ID: " + traceID + "\n")
			} else {
				msg.WriteString("🆔 Trace ID: " + configTraceID + "\n")
			}
		}
		msg.WriteString("\n")
		msg.WriteString("⚠️ 特别说明：\n")
		if priceCheckError != "" {
			msg.WriteString(fmt.Sprintf("（价格校验未通过: %s，已跳过自动下单）", priceCheckError))
		} else {
			msg.WriteString("（价格校验未通过，已跳过自动下单）")
		}
	default:
		msg.WriteString("📦 服务器下架通知\n\n")
		if serverName != "" {
			msg.WriteString("服务器: " + serverName + "\n")
		}
		msg.WriteString("型号: " + planCode + "\n")
		if configInfo != nil {
			display, _ := configInfo["display"].(string)
			memory, _ := configInfo["memory"].(string)
			storage, _ := configInfo["storage"].(string)
			msg.WriteString("配置: " + display + "\n")
			msg.WriteString("├─ 内存: " + memory + "\n")
			msg.WriteString("└─ 存储: " + storage + "\n")
		}
		msg.WriteString("\n数据中心: " + datacenter + "\n")
		msg.WriteString("状态: 已无货\n")
		msg.WriteString("⏰ 时间: " + pushTime.Format("2006-01-02 15:04:05"))
		if traceID != "" || configTraceID != "" {
			if traceID != "" && configTraceID != "" {
				msg.WriteString("\n🆔 Trace ID:\n  订阅: " + traceID + "\n  配置: " + configTraceID)
			} else if traceID != "" {
				msg.WriteString("\n🆔 Trace ID: " + traceID)
			} else {
				msg.WriteString("\n🆔 Trace ID: " + configTraceID)
			}
		}
		if durationText != "" {
			msg.WriteString("\n⏱️ 本次上架持续: " + strings.TrimPrefix(durationText, "历时 "))
		}
	}

	configDesc := ""
	if configInfo != nil {
		if d, ok := configInfo["display"].(string); ok {
			configDesc = " [" + d + "]"
		}
	}
	m.state.Logger.Info(fmt.Sprintf("正在发送Telegram通知: %s@%s%s", planCode, datacenter, configDesc), "monitor")
	template := "grey"
	title := "📦 服务器下架通知"
	if changeType == "available" {
		template, title = "green", "🎉 服务器上架通知"
	} else if changeType == "price_check_failed" {
		template, title = "orange", "⚠️ 价格校验失败通知"
	}
	expected := ConfiguredNotificationChannels(m.state)
	if len(expectedChannels) > 0 { expected = canonicalNotificationChannels(expectedChannels[0]) }
	delivered := NotificationDeliveryResult{}
	if notificationChannelSelected(expected, NotificationChannelFeishu) {
		delivered[NotificationChannelFeishu] = FeishuSendDefaultNotification(m.state, title, msg.String(), template, nil)
	}
	if notificationChannelSelected(expected, NotificationChannelWeixin) {
		delivered[NotificationChannelWeixin] = SendWeixinNotification(m.state, msg.String())
	}
	tgOK := false
	if notificationChannelSelected(expected, NotificationChannelTelegram) {
		tgOK = telegram.SendMessage(m.state, msg.String(), nil)
		delivered[NotificationChannelTelegram] = tgOK
	}
	if tgOK {
		m.state.Logger.Info(fmt.Sprintf("✅ Telegram通知发送成功: %s@%s%s - %s", planCode, datacenter, configDesc, changeType), "monitor")
	} else {
		m.state.Logger.Warn(fmt.Sprintf("⚠️ Telegram通知发送失败: %s@%s%s", planCode, datacenter, configDesc), "monitor")
	}
	return delivered
}

// SendNewServerAlert 对应 Python: send_new_server_alert
func (m *Monitor) SendNewServerAlert(server map[string]interface{}, expectedChannels ...[]string) NotificationDeliveryResult {
	planCode, _ := server["planCode"].(string)
	priceText := ""
	if planCode != "" {
		priceText = m.getCatalogPriceInfoText("", planCode, nil)
	}
	msg := fmt.Sprintf("🆕 新服务器上架通知！\n\n型号: %v\n名称: %v\nCPU: %v\n内存: %v\n存储: %v\n带宽: %v\n",
		server["planCode"], server["name"], server["cpu"], server["memory"], server["storage"], server["bandwidth"],
	)
	if priceText == "" {
		priceText = unavailablePriceText()
	}
	msg += "\n💰 价格:\n" + priceText + "\n\n⏰ 发现时间: " + m.nowBeijing().Format("2006-01-02 15:04:05") + "\n\n💡 快去查看详情！"
	expected := ConfiguredNotificationChannels(m.state)
	if len(expectedChannels) > 0 {
		expected = canonicalNotificationChannels(expectedChannels[0])
	}
	delivered := NotificationDeliveryResult{}
	if notificationChannelSelected(expected, NotificationChannelFeishu) {
		delivered[NotificationChannelFeishu] = FeishuSendDefaultNotification(m.state, "🆕 新服务器上架通知", msg, "green", nil)
	}
	if notificationChannelSelected(expected, NotificationChannelTelegram) {
		delivered[NotificationChannelTelegram] = telegram.SendMessage(m.state, msg, nil)
	}
	if notificationChannelSelected(expected, NotificationChannelWeixin) {
		delivered[NotificationChannelWeixin] = SendWeixinNotification(m.state, msg)
	}
	m.state.Logger.Info(fmt.Sprintf("发送新服务器提醒: %v", server["planCode"]), "monitor")
	return delivered
}

func unavailablePriceText() string {
	return "月费: 暂不可用\n安装费: 暂不可用\n首月总价: 暂不可用"
}
