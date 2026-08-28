package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
	"github.com/ovh-webui/server/internal/vps"
)

// GetVPSSubscriptions GET /api/vps-monitor/subscriptions
func GetVPSSubscriptions(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, state.VPSSubscriptionsSnapshot())
	}
}

// AddVPSSubscription POST /api/vps-monitor/subscriptions
func AddVPSSubscription(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		// VPS 监控与独服监控一致：Telegram、飞书或微信任一可用即可。
		if ok, reason := monitor.NotificationConfigured(state, ""); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": reason})
			return
		}
		var body struct {
			PlanCode          string   `json:"planCode"`
			OvhSubsidiary     string   `json:"ovhSubsidiary"`
			Datacenters       []string `json:"datacenters"`
			MonitorLinux      *bool    `json:"monitorLinux"`
			MonitorWindows    *bool    `json:"monitorWindows"`
			NotifyAvailable   *bool    `json:"notifyAvailable"`
			NotifyUnavailable *bool    `json:"notifyUnavailable"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
			return
		}
		if body.PlanCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少planCode参数"})
			return
		}
		if body.OvhSubsidiary == "" {
			body.OvhSubsidiary = "IE"
			if account, ok := state.FindAccount(""); ok && account.Zone != "" {
				body.OvhSubsidiary = account.Zone
			}
		}
		monitorLinux := true
		if body.MonitorLinux != nil {
			monitorLinux = *body.MonitorLinux
		}
		monitorWindows := false
		if body.MonitorWindows != nil {
			monitorWindows = *body.MonitorWindows
		}
		notifyAvailable := true
		if body.NotifyAvailable != nil {
			notifyAvailable = *body.NotifyAvailable
		}
		notifyUnavailable := false
		if body.NotifyUnavailable != nil {
			notifyUnavailable = *body.NotifyUnavailable
		}

		sub := types.VPSSubscription{
			ID: uuid.NewString(), PlanCode: body.PlanCode, OvhSubsidiary: body.OvhSubsidiary,
			Datacenters: body.Datacenters, MonitorLinux: monitorLinux, MonitorWindows: monitorWindows,
			NotifyAvailable: notifyAvailable, NotifyUnavailable: notifyUnavailable,
			LastStatus: map[string]string{}, PendingNotify: map[string]string{}, PendingNotifyChannels: map[string][]string{},
			History: []map[string]interface{}{}, CreatedAt: types.NowISO(),
		}
		duplicate := errors.New("该VPS套餐已订阅")
		if err := state.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
			for _, existing := range subscriptions {
				if existing.PlanCode == sub.PlanCode && existing.OvhSubsidiary == sub.OvhSubsidiary {
					return nil, duplicate
				}
			}
			return append(subscriptions, sub), nil
		}); err != nil {
			if errors.Is(err, duplicate) {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
			} else {
				state.Logger.Error("保存VPS订阅失败: "+err.Error(), "vps_monitor")
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "保存订阅失败"})
			}
			return
		}
		state.Logger.Info("添加VPS订阅: "+body.PlanCode+" (subsidiary: "+body.OvhSubsidiary+")", "vps_monitor")

		if !vps.Running() {
			vps.Start(state)
			state.Logger.Info("自动启动VPS监控", "vps_monitor")
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "已订阅 " + body.PlanCode, "subscription": sub})
	}
}

// UpdateVPSSubscription PUT /api/vps-monitor/subscriptions/:subscription_id
func UpdateVPSSubscription(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("subscription_id")
		var body struct {
			Datacenters       *[]string `json:"datacenters"`
			MonitorLinux      *bool     `json:"monitorLinux"`
			MonitorWindows    *bool     `json:"monitorWindows"`
			NotifyAvailable   *bool     `json:"notifyAvailable"`
			NotifyUnavailable *bool     `json:"notifyUnavailable"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
			return
		}
		notFound := errors.New("订阅不存在")
		var updated types.VPSSubscription
		if err := state.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
			for i := range subscriptions {
				if subscriptions[i].ID != id {
					continue
				}
				found := &subscriptions[i]
				if body.Datacenters != nil {
					found.Datacenters = *body.Datacenters
					found.LastStatus = map[string]string{}
					found.PendingNotify = map[string]string{}
					found.PendingNotifyChannels = map[string][]string{}
				}
				if body.MonitorLinux != nil {
					if found.MonitorLinux != *body.MonitorLinux {
						found.LastStatus = map[string]string{}
						found.PendingNotify = map[string]string{}
						found.PendingNotifyChannels = map[string][]string{}
					}
					found.MonitorLinux = *body.MonitorLinux
				}
				if body.MonitorWindows != nil {
					if found.MonitorWindows != *body.MonitorWindows {
						found.LastStatus = map[string]string{}
						found.PendingNotify = map[string]string{}
						found.PendingNotifyChannels = map[string][]string{}
					}
					found.MonitorWindows = *body.MonitorWindows
				}
				if body.NotifyAvailable != nil {
					found.NotifyAvailable = *body.NotifyAvailable
					if !*body.NotifyAvailable {
						for key, changeType := range found.PendingNotify {
							if changeType == "initial" || changeType == "available" {
								delete(found.PendingNotify, key)
								delete(found.PendingNotifyChannels, key)
							}
						}
					}
				}
				if body.NotifyUnavailable != nil {
					found.NotifyUnavailable = *body.NotifyUnavailable
					if !*body.NotifyUnavailable {
						for key, changeType := range found.PendingNotify {
							if changeType == "unavailable" {
								delete(found.PendingNotify, key)
								delete(found.PendingNotifyChannels, key)
							}
						}
					}
				}
				updated = *found
				return subscriptions, nil
			}
			return nil, notFound
		}); err != nil {
			if errors.Is(err, notFound) {
				c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": err.Error()})
			} else {
				state.Logger.Error("保存VPS订阅更新失败: "+err.Error(), "vps_monitor")
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "保存订阅失败"})
			}
			return
		}
		state.Logger.Info("更新VPS订阅: "+id, "vps_monitor")
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "订阅已更新", "subscription": updated})
	}
}

// RemoveVPSSubscription DELETE /api/vps-monitor/subscriptions/:subscription_id
func RemoveVPSSubscription(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("subscription_id")
		notFound := errors.New("订阅不存在")
		empty := false
		if err := state.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
			kept := make([]types.VPSSubscription, 0, len(subscriptions))
			removed := false
			for _, sub := range subscriptions {
				if sub.ID == id {
					removed = true
					continue
				}
				kept = append(kept, sub)
			}
			if !removed {
				return nil, notFound
			}
			empty = len(kept) == 0
			return kept, nil
		}); err != nil {
			if errors.Is(err, notFound) {
				c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": err.Error()})
			} else {
				state.Logger.Error("删除VPS订阅失败: "+err.Error(), "vps_monitor")
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "删除订阅失败"})
			}
			return
		}
		state.Logger.Info("删除VPS订阅: "+id, "vps_monitor")
		if empty && vps.Running() {
			vps.Stop(state)
			state.Logger.Info("所有订阅已删除，自动停止VPS监控", "vps_monitor")
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "订阅已删除"})
	}
}

// ClearVPSSubscriptions DELETE /api/vps-monitor/subscriptions/clear
func ClearVPSSubscriptions(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		count := 0
		if err := state.MutateVPSSubscriptions(func(subscriptions []types.VPSSubscription) ([]types.VPSSubscription, error) {
			count = len(subscriptions)
			return []types.VPSSubscription{}, nil
		}); err != nil {
			state.Logger.Error("清空VPS订阅失败: "+err.Error(), "vps_monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "清空订阅失败"})
			return
		}
		state.Logger.Info("清空所有VPS订阅 ("+strconv.Itoa(count)+" 项)", "vps_monitor")
		if vps.Running() {
			vps.Stop(state)
			state.Logger.Info("所有订阅已清空，自动停止VPS监控", "vps_monitor")
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "count": count, "message": "已清空 " + strconv.Itoa(count) + " 个订阅"})
	}
}

// GetVPSSubscriptionHistory GET /api/vps-monitor/subscriptions/:subscription_id/history
func GetVPSSubscriptionHistory(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("subscription_id")
		var history []map[string]interface{}
		for _, sub := range state.VPSSubscriptionsSnapshot() {
			if sub.ID == id {
				history = sub.History
				break
			}
		}
		if history == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "订阅不存在"})
			return
		}
		reversed := make([]map[string]interface{}, len(history))
		for i, e := range history {
			reversed[len(history)-1-i] = e
		}
		c.JSON(http.StatusOK, reversed)
	}
}

// StartVPSMonitor POST /api/vps-monitor/start
func StartVPSMonitor(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if vps.Running() {
			c.JSON(http.StatusOK, gin.H{"status": "info", "message": "VPS监控已在运行中"})
			return
		}
		if ok, reason := monitor.NotificationConfigured(state, ""); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "无法启动 VPS 监控：" + reason})
			return
		}
		vps.Start(state)
		state.Logger.Info("VPS监控已启动", "vps_monitor")
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "VPS监控已启动"})
	}
}

// StopVPSMonitor POST /api/vps-monitor/stop
func StopVPSMonitor(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !vps.Running() {
			c.JSON(http.StatusOK, gin.H{"status": "info", "message": "VPS监控未运行"})
			return
		}
		vps.Stop(state)
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "VPS监控已停止"})
	}
}

// GetVPSMonitorStatus GET /api/vps-monitor/status
func GetVPSMonitorStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		state.VPSSubsMu.Lock()
		count := len(state.VPSSubscriptions)
		interval := state.VPSCheckInterval
		state.VPSSubsMu.Unlock()
		c.JSON(http.StatusOK, gin.H{
			"running":              vps.Running(),
			"subscriptions_count":  count,
			"check_interval":       interval,
		})
	}
}

// SetVPSMonitorInterval PUT /api/vps-monitor/interval
func SetVPSMonitorInterval(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Interval int `json:"interval"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
			return
		}
		if body.Interval < 60 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "间隔不能小于60秒"})
			return
		}
		if err := state.SetVPSCheckInterval(body.Interval); err != nil {
			state.Logger.Error("保存VPS检查间隔失败: "+err.Error(), "vps_monitor")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "保存检查间隔失败"})
			return
		}
		state.Logger.Info("VPS检查间隔已设置为 "+strconv.Itoa(body.Interval)+" 秒", "vps_monitor")
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": "检查间隔已设置为 " + strconv.Itoa(body.Interval) + " 秒"})
	}
}

// ManualCheckVPS POST /api/vps-monitor/check/:plan_code
func ManualCheckVPS(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		planCode := c.Param("plan_code")
		var body struct {
			OvhSubsidiary string `json:"ovhSubsidiary"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.OvhSubsidiary == "" {
			body.OvhSubsidiary = "IE"
		}
		result := vps.CheckVPSDCAvailability(state, planCode, body.OvhSubsidiary)
		if result == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "获取VPS数据中心信息失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "data": result})
	}
}
