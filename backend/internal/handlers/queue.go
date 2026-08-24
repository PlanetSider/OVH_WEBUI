package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

// AddQueueItem POST /api/queue
// 多账户:body 必须带 account_id,后端用它确定下单走哪个账户
func AddQueueItem(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			AccountID     string   `json:"account_id"`
			PlanCode      string   `json:"planCode"`
			Datacenter    string   `json:"datacenter"`
			Options       []string `json:"options"`
			RetryInterval int      `json:"retryInterval"`
		}
		_ = c.ShouldBindJSON(&body)
		body.PlanCode = strings.TrimSpace(body.PlanCode)
		body.Datacenter = strings.TrimSpace(body.Datacenter)
		if body.AccountID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "缺少 account_id"})
			return
		}
		if _, ok := state.FindAccount(body.AccountID); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "account_id 不存在"})
			return
		}
		if body.PlanCode == "" || body.Datacenter == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "缺少 planCode 或 datacenter"})
			return
		}
		if body.RetryInterval == 0 {
			body.RetryInterval = 30
		}
		item := types.QueueItem{
			ID:            uuid.NewString(),
			AccountID:     body.AccountID,
			PlanCode:      body.PlanCode,
			Datacenter:    body.Datacenter,
			Options:       body.Options,
			Status:        "running",
			CreatedAt:     types.NowISO(),
			UpdatedAt:     types.NowISO(),
			RetryInterval: body.RetryInterval,
			RetryCount:    0,
			MaxRetries:    0, // 0 = 无限抢购（与 Telegram 一致）；quick-order 路径单独设上限
			LastCheckTime: 0,
		}
		state.QueueMu.Lock()
		state.Queue = append(state.Queue, item)
		state.QueueMu.Unlock()
		_ = state.SaveQueue()
		state.Logger.Info("添加任务 "+item.ID+" ("+item.PlanCode+" 在 "+item.Datacenter+", 账户 "+body.AccountID+") 到队列并立即启动 (状态: running)", "")
		c.JSON(http.StatusOK, gin.H{"status": "success", "id": item.ID})
	}
}

// RemoveQueueItem DELETE /api/queue/:id
func RemoveQueueItem(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		state.DeletedTaskIDsMu.Lock()
		state.DeletedTaskIDs[id] = struct{}{}
		state.DeletedTaskIDsMu.Unlock()
		state.Logger.Info("标记任务 "+id+" 为删除，后台线程将立即停止处理", "system")

		state.QueueMu.Lock()
		var removed *types.QueueItem
		// 重新分配新 slice，避免 [:0] 与原 backing array 共享导致快照读到已被覆盖的元素
		kept := make([]types.QueueItem, 0, len(state.Queue))
		for i := range state.Queue {
			if state.Queue[i].ID == id {
				cp := state.Queue[i]
				removed = &cp
				continue
			}
			kept = append(kept, state.Queue[i])
		}
		state.Queue = kept
		state.QueueMu.Unlock()
		_ = state.SaveQueue()
		if removed != nil {
			state.Logger.Info("Removed "+removed.PlanCode+" from queue (ID: "+id+")", "system")
		}
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}

// ClearQueue DELETE /api/queue/clear
func ClearQueue(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		state.QueueMu.Lock()
		count := len(state.Queue)
		state.DeletedTaskIDsMu.Lock()
		for _, it := range state.Queue {
			state.DeletedTaskIDs[it.ID] = struct{}{}
		}
		state.DeletedTaskIDsMu.Unlock()
		state.Queue = []types.QueueItem{}
		state.QueueMu.Unlock()
		_ = state.SaveQueue()
		state.Logger.Info("Cleared all queue items ("+strconv.Itoa(count)+" items removed)", "")
		c.JSON(http.StatusOK, gin.H{"status": "success", "count": count})
	}
}

// UpdateQueueStatus PUT /api/queue/:id/status
func UpdateQueueStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var body struct {
			Status string `json:"status"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Status == "" {
			body.Status = "pending"
		}
		state.QueueMu.Lock()
		for i := range state.Queue {
			if state.Queue[i].ID == id {
				state.Queue[i].Status = body.Status
				state.Queue[i].UpdatedAt = types.NowISO()
				state.Logger.Info("Updated "+state.Queue[i].PlanCode+" status to "+body.Status, "")
				break
			}
		}
		state.QueueMu.Unlock()
		_ = state.SaveQueue()
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}

// UpdateQueueItem PUT /api/queue/:id
// 编辑已有任务：配置、数据中心、账户、数量和重试间隔均可调整。
// 一个队列项代表一个“单机房单台”任务；数量大于 1 或选择多个机房时，保留当前任务作为第一项并复制其余任务。
func UpdateQueueItem(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var body struct {
			AccountID     *string   `json:"account_id"`
			PlanCode      *string   `json:"planCode"`
			Datacenter    *string   `json:"datacenter"`
			Datacenters   *[]string `json:"datacenters"`
			Options       *[]string `json:"options"`
			RetryInterval *int      `json:"retryInterval"`
			Quantity      *int      `json:"quantity"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
			return
		}

		state.QueueMu.Lock()
		index := -1
		for i := range state.Queue {
			if state.Queue[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			state.QueueMu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "任务不存在"})
			return
		}
		item := state.Queue[index]
		state.QueueMu.Unlock()

		accountID := item.AccountID
		if body.AccountID != nil {
			accountID = strings.TrimSpace(*body.AccountID)
		}
		if accountID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "缺少 account_id"})
			return
		}
		if _, ok := state.FindAccount(accountID); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "account_id 不存在"})
			return
		}

		planCode := item.PlanCode
		if body.PlanCode != nil {
			planCode = strings.TrimSpace(*body.PlanCode)
		}
		if planCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "缺少 planCode"})
			return
		}

		dcs := []string{item.Datacenter}
		if body.Datacenters != nil {
			dcs = make([]string, 0, len(*body.Datacenters))
			for _, dc := range *body.Datacenters {
				dc = strings.TrimSpace(dc)
				if dc != "" {
					dcs = append(dcs, dc)
				}
			}
		} else if body.Datacenter != nil {
			dcs = []string{strings.TrimSpace(*body.Datacenter)}
		}
		if len(dcs) == 0 || dcs[0] == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "至少选择一个数据中心"})
			return
		}
		qty := 1
		if body.Quantity != nil && *body.Quantity > 0 {
			qty = *body.Quantity
		}
		retryInterval := item.RetryInterval
		if body.RetryInterval != nil {
			retryInterval = *body.RetryInterval
		}
		if retryInterval < 1 {
			retryInterval = 60
		}
		options := item.Options
		if body.Options != nil {
			options = append([]string{}, (*body.Options)...)
		}

		now := types.NowISO()
		state.QueueMu.Lock()
		// 任务可能在上面的校验期间被删除，重新确认索引。
		index = -1
		for i := range state.Queue {
			if state.Queue[i].ID == id { index = i; break }
		}
		if index < 0 {
			state.QueueMu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "任务不存在"})
			return
		}
		state.Queue[index].AccountID = accountID
		state.Queue[index].PlanCode = planCode
		state.Queue[index].Datacenter = dcs[0]
		state.Queue[index].Options = append([]string{}, options...)
		state.Queue[index].RetryInterval = retryInterval
		state.Queue[index].RetryCount = 0
		state.Queue[index].LastCheckTime = 0
		state.Queue[index].Status = "running"
		state.Queue[index].UpdatedAt = now
		created := 0
		for _, dc := range dcs {
			for n := 0; n < qty; n++ {
				if dc == dcs[0] && n == 0 { continue }
				state.Queue = append(state.Queue, types.QueueItem{
					ID: uuid.NewString(), AccountID: accountID, PlanCode: planCode, Datacenter: dc,
					Options: append([]string{}, options...), Status: "running", CreatedAt: now, UpdatedAt: now,
					RetryInterval: retryInterval, MaxRetries: item.MaxRetries, Priority: item.Priority,
					QuickOrder: item.QuickOrder, FromTelegram: item.FromTelegram,
					ConfigSniperTaskID: item.ConfigSniperTaskID,
				})
				created++
			}
		}
		state.QueueMu.Unlock()
		if err := state.SaveQueue(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
		state.Logger.Info("更新抢购任务 "+id+"，新增 "+strconv.Itoa(created)+" 个任务", "queue")
		c.JSON(http.StatusOK, gin.H{"status": "success", "created": created, "id": id})
	}
}

// ClearPurchaseHistory DELETE /api/purchase-history
func ClearPurchaseHistory(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		state.HistoryMu.Lock()
		state.History = state.History[:0]
		state.HistoryMu.Unlock()
		_ = state.SaveHistory()
		state.Logger.Info("Purchase history cleared", "")
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}
