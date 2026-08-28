package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/telegram"
	"github.com/ovh-webui/server/internal/types"
)

// GetSubscriptions GET /api/monitor/subscriptions
func GetSubscriptions(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, mon.Snapshot())
	}
}

// AddSubscription POST /api/monitor/subscriptions
func AddSubscription(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ok, reason := monitor.NotificationConfigured(state, c.Query("account")); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "通知未配置或无效: " + reason})
			return
		}
		var body struct {
			PlanCode           string   `json:"planCode"`
			Datacenters        []string `json:"datacenters"`
			Memories           []string `json:"memories"`
			Storages           []string `json:"storages"`
			Networks           []string `json:"networks"`
			NotifyAvailable    *bool    `json:"notifyAvailable"`
			NotifyUnavailable  *bool    `json:"notifyUnavailable"`
			AutoOrder          bool     `json:"autoOrder"`
			Quantity           int      `json:"quantity"`
			AutoOrderAccountID string   `json:"autoOrderAccountId"` // 空 = 触发时只通知不下单
		}
		_ = c.ShouldBindJSON(&body)
		if body.PlanCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少planCode参数"})
			return
		}
		// 校验 auto_order_account_id 引用的账户真的存在
		if body.AutoOrderAccountID != "" {
			if _, ok := state.FindAccount(body.AutoOrderAccountID); !ok {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "autoOrderAccountId 不存在"})
				return
			}
		}
		notifyAvailable := true
		notifyUnavailable := false
		if body.NotifyAvailable != nil {
			notifyAvailable = *body.NotifyAvailable
		}
		if body.NotifyUnavailable != nil {
			notifyUnavailable = *body.NotifyUnavailable
		}
		if body.Quantity < 1 {
			body.Quantity = 1
		}

		var serverName string
		state.ServerPlansMu.RLock()
		for _, s := range state.ServerPlans {
			if s.PlanCode == body.PlanCode {
				serverName = s.Name
				state.Logger.Info("找到服务器名称: "+serverName+" ("+body.PlanCode+")", "monitor")
				break
			}
		}
		state.ServerPlansMu.RUnlock()
		if serverName == "" {
			state.Logger.Warn("未找到服务器 "+body.PlanCode+" 的名称信息", "monitor")
		}

		if err := mon.AddSubscription(body.PlanCode, body.Datacenters, notifyAvailable, notifyUnavailable,
			serverName, nil, nil, body.AutoOrder, body.Quantity, body.AutoOrderAccountID,
			body.Memories, body.Storages, body.Networks); err != nil {
			state.Logger.Error("保存服务器订阅失败: "+err.Error(), "monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "保存订阅失败"})
			return
		}

		if !mon.Running() {
			mon.Start()
			state.Logger.Info("添加订阅后自动启动监控", "")
		}
		nameDisplay := serverName
		if nameDisplay == "" {
			nameDisplay = "未知名称"
		}
		state.Logger.Info("添加服务器订阅: "+body.PlanCode+" ("+nameDisplay+")", "")
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "已订阅 " + body.PlanCode})
	}
}

// BatchAddAll POST /api/monitor/subscriptions/batch-add-all
func BatchAddAll(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ok, reason := monitor.NotificationConfigured(state, c.Query("account")); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "通知未配置或无效: " + reason})
			return
		}
		state.ServerPlansMu.RLock()
		hasServers := len(state.ServerPlans) > 0
		state.ServerPlansMu.RUnlock()
		if !hasServers {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "服务器列表为空，请先刷新服务器列表"})
			return
		}

		var body struct {
			NotifyAvailable    *bool  `json:"notifyAvailable"`
			NotifyUnavailable  *bool  `json:"notifyUnavailable"`
			Memories           []string `json:"memories"`
			Storages           []string `json:"storages"`
			Networks           []string `json:"networks"`
			AutoOrder          bool   `json:"autoOrder"`
			AutoOrderAccountID string `json:"autoOrderAccountId"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.AutoOrderAccountID != "" {
			if _, ok := state.FindAccount(body.AutoOrderAccountID); !ok {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "autoOrderAccountId 不存在"})
				return
			}
		}
		notifyAvailable := true
		notifyUnavailable := false
		if body.NotifyAvailable != nil {
			notifyAvailable = *body.NotifyAvailable
		}
		if body.NotifyUnavailable != nil {
			notifyUnavailable = *body.NotifyUnavailable
		}

		added := 0
		skipped := 0
		state.ServerPlansMu.RLock()
		plansCopy := make([]types.ServerPlan, len(state.ServerPlans))
		copy(plansCopy, state.ServerPlans)
		state.ServerPlansMu.RUnlock()

		if err := mon.MutateSubscriptions(func(subscriptions []*monitor.Subscription) ([]*monitor.Subscription, error) {
			existing := make(map[string]struct{}, len(subscriptions))
			for _, sub := range subscriptions {
				existing[sub.PlanCode] = struct{}{}
			}
			for _, server := range plansCopy {
				pc := server.PlanCode
				if pc == "" {
					continue
				}
				if _, ok := existing[pc]; ok {
					skipped++
					continue
				}
				quantity := 0
				if body.AutoOrder {
					quantity = 1
				}
				subscriptions = append(subscriptions, &monitor.Subscription{
					PlanCode: pc, Datacenters: []string{}, Memories: append([]string{}, body.Memories...),
					Storages: append([]string{}, body.Storages...), Networks: append([]string{}, body.Networks...),
					NotifyAvailable: notifyAvailable, NotifyUnavailable: notifyUnavailable,
					LastStatus: map[string]string{}, ConfirmedStatus: map[string]string{},
					PendingOrder: map[string]int{}, PendingNotify: map[string]string{}, PendingNotifyChannels: map[string][]string{},
					CreatedAt: types.NowISO(), History: []monitor.HistoryEntry{}, ServerName: server.Name,
					AutoOrder: body.AutoOrder, Quantity: quantity, AutoOrderAccountID: body.AutoOrderAccountID,
				})
				existing[pc] = struct{}{}
				added++
			}
			return subscriptions, nil
		}); err != nil {
			state.Logger.Error("批量保存服务器订阅失败: "+err.Error(), "monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "批量保存订阅失败"})
			return
		}
		if !mon.Running() {
			mon.Start()
			state.Logger.Info("批量添加订阅后自动启动监控", "monitor")
		}

		message := "已添加 " + strconv.Itoa(added) + " 个服务器到监控（全机房监控）"
		if skipped > 0 {
			message += "，跳过 " + strconv.Itoa(skipped) + " 个已订阅的服务器"
		}
		state.Logger.Info("批量添加订阅完成: "+message, "monitor")
		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"added":   added,
			"skipped": skipped,
			"errors":  []string{},
			"message": message,
		})
	}
}

// RemoveSubscription DELETE /api/monitor/subscriptions/:planCode
func RemoveSubscription(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		planCode := c.Param("planCode")
		if err := mon.RemoveSubscription(planCode); err == nil {
			state.Logger.Info("删除服务器订阅: "+planCode, "")
			c.JSON(http.StatusOK, gin.H{"status": "success", "message": "已取消订阅 " + planCode})
			return
		} else if err.Error() != "订阅不存在" {
			state.Logger.Error("删除服务器订阅失败: "+err.Error(), "monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "删除订阅失败"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "订阅不存在"})
	}
}

// ClearSubscriptions DELETE /api/monitor/subscriptions/clear
func ClearSubscriptions(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		count, err := mon.ClearSubscriptions()
		if err != nil {
			state.Logger.Error("清空服务器订阅失败: "+err.Error(), "monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "清空订阅失败"})
			return
		}
		state.Logger.Info("清空所有订阅 ("+strconv.Itoa(count)+" 项)", "")
		c.JSON(http.StatusOK, gin.H{"status": "success", "count": count, "message": "已清空 " + strconv.Itoa(count) + " 个订阅"})
	}
}

// UpdateSubscription PUT /api/monitor/subscriptions/:planCode
// 原地更新已有订阅（通知开关 / 机房 / 自动下单 / 数量 / 账户），不重置 lastStatus 与 history。
func UpdateSubscription(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		planCode := c.Param("planCode")
		if planCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少 planCode"})
			return
		}
		var body struct {
			Datacenters        *[]string `json:"datacenters"`
			Memories           *[]string `json:"memories"`
			Storages           *[]string `json:"storages"`
			Networks           *[]string `json:"networks"`
			NotifyAvailable    *bool     `json:"notifyAvailable"`
			NotifyUnavailable  *bool     `json:"notifyUnavailable"`
			AutoOrder          *bool     `json:"autoOrder"`
			Quantity           *int      `json:"quantity"`
			AutoOrderAccountID *string   `json:"autoOrderAccountId"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
			return
		}
		if body.AutoOrderAccountID != nil && *body.AutoOrderAccountID != "" {
			if _, ok := state.FindAccount(*body.AutoOrderAccountID); !ok {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "autoOrderAccountId 不存在"})
				return
			}
		}

		found := false
		if err := mon.MutateSubscriptions(func(subscriptions []*monitor.Subscription) ([]*monitor.Subscription, error) {
			for _, sub := range subscriptions {
				if sub.PlanCode != planCode {
					continue
				}
				found = true
				filtersChanged := (body.Datacenters != nil && !sameStringSlice(sub.Datacenters, *body.Datacenters)) ||
					(body.Memories != nil && !sameStringSlice(sub.Memories, *body.Memories)) ||
					(body.Storages != nil && !sameStringSlice(sub.Storages, *body.Storages)) ||
					(body.Networks != nil && !sameStringSlice(sub.Networks, *body.Networks))
				accountChanged := body.AutoOrderAccountID != nil && sub.AutoOrderAccountID != *body.AutoOrderAccountID
				if body.Datacenters != nil { sub.Datacenters = append([]string{}, (*body.Datacenters)...) }
				if body.Memories != nil { sub.Memories = append([]string{}, (*body.Memories)...) }
				if body.Storages != nil { sub.Storages = append([]string{}, (*body.Storages)...) }
				if body.Networks != nil { sub.Networks = append([]string{}, (*body.Networks)...) }
				if filtersChanged || accountChanged { monitor.ResetSubscriptionTracking(sub) }
				if body.NotifyAvailable != nil { sub.NotifyAvailable = *body.NotifyAvailable }
				if body.NotifyUnavailable != nil { sub.NotifyUnavailable = *body.NotifyUnavailable }
				if body.AutoOrder != nil { sub.AutoOrder = *body.AutoOrder }
				if body.Quantity != nil { sub.Quantity = *body.Quantity }
				if body.AutoOrderAccountID != nil { sub.AutoOrderAccountID = *body.AutoOrderAccountID }
				if sub.AutoOrder && sub.Quantity < 1 { sub.Quantity = 1 }
				if !sub.AutoOrder { sub.Quantity = 0; sub.PendingOrder = map[string]int{} }
				monitor.ClearDisabledPendingNotifications(sub, sub.NotifyAvailable, sub.NotifyUnavailable)
				return subscriptions, nil
			}
			return subscriptions, nil
		}); err != nil {
			state.Logger.Error("更新服务器订阅失败: "+err.Error(), "monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "更新订阅失败"})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "订阅不存在"})
			return
		}
		state.Logger.Info("更新服务器订阅: "+planCode, "monitor")
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "订阅已更新", "planCode": planCode})
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GetSubscriptionHistory GET /api/monitor/subscriptions/:planCode/history
// 返回该订阅的历史记录数组（倒序，最新在前）。
func GetSubscriptionHistory(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		planCode := c.Param("planCode")
		sub := mon.FindSubscription(planCode)
		if sub == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "订阅不存在"})
			return
		}
		history := sub.History
		if history == nil {
			history = []monitor.HistoryEntry{}
		}
		reversed := make([]monitor.HistoryEntry, len(history))
		for i, e := range history {
			reversed[len(history)-1-i] = e
		}
		c.JSON(http.StatusOK, reversed)
	}
}

// StartMonitor POST /api/monitor/start
func StartMonitor(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !mon.LoadReady() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status":  "error",
				"message": "监控数据尚未安全加载，暂时无法启动",
			})
			return
		}
		if ok, reason := monitor.NotificationConfigured(state, c.Query("account")); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "通知未配置或无效，无法启动监控: " + reason})
			return
		}
		if mon.Start() {
			state.Logger.Info("用户启动服务器监控", "")
			c.JSON(http.StatusOK, gin.H{"status": "success", "message": "监控已启动"})
		} else {
			c.JSON(http.StatusOK, gin.H{"status": "info", "message": "监控已在运行中"})
		}
	}
}

// VerifyTelegram GET /api/telegram/verify
// 前端添加订阅对话框打开时调一次,决定是否允许提交。
// 后端不缓存,每次请求都真去 telegram API 探一下,前端 React Query 控频率(5min staleTime)。
func VerifyTelegram(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, reason := telegram.VerifyConfig(state)
		c.JSON(http.StatusOK, gin.H{"ok": ok, "reason": reason})
	}
}

// StopMonitor POST /api/monitor/stop
func StopMonitor(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		if mon.Stop() {
			state.Logger.Info("用户停止服务器监控", "")
			c.JSON(http.StatusOK, gin.H{"status": "success", "message": "监控已停止"})
		} else {
			c.JSON(http.StatusOK, gin.H{"status": "info", "message": "监控未运行"})
		}
	}
}

// GetMonitorStatus GET /api/monitor/status
func GetMonitorStatus(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, mon.Status())
	}
}

// SetMonitorInterval PUT /api/monitor/interval
func SetMonitorInterval(state *app.State, mon *monitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "info", "message": "检查间隔已全局固定为5秒，无法修改"})
	}
}

// TestNotification POST /api/monitor/test-notification
func TestNotification(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		msg := "🔔 服务器监控测试通知\n\n时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n\n✅ 通知配置正常！"
		tgOK := telegram.SendMessage(state, msg, nil)
		feishuOK := monitor.FeishuSendDefaultNotification(state, "🔔 服务器监控测试通知", msg, "blue", nil)
		weixinOK := monitor.SendWeixinNotification(state, msg)
		if tgOK || feishuOK || weixinOK {
			state.Logger.Info("机器人测试通知发送成功", "monitor")
			c.JSON(http.StatusOK, gin.H{"status": "success", "message": "测试通知已发送", "telegram": tgOK, "feishu": feishuOK, "weixin": weixinOK})
			return
		}
		state.Logger.Warn("机器人测试通知发送失败", "monitor")
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "发送失败，请检查 Telegram/飞书/微信配置和日志"})
	}
}
