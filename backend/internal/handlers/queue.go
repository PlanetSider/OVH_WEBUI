package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

var errQueueItemNotFound = errors.New("任务不存在")

func markQueueItemDeleted(state *app.State, id string) bool {
	state.DeletedTaskIDsMu.Lock()
	defer state.DeletedTaskIDsMu.Unlock()
	if state.DeletedTaskIDs == nil {
		state.DeletedTaskIDs = make(map[string]struct{})
	}
	_, alreadyMarked := state.DeletedTaskIDs[id]
	state.DeletedTaskIDs[id] = struct{}{}
	return alreadyMarked
}

func rollbackQueueItemDeleted(state *app.State, id string, alreadyMarked bool) {
	if alreadyMarked {
		return
	}
	state.DeletedTaskIDsMu.Lock()
	delete(state.DeletedTaskIDs, id)
	state.DeletedTaskIDsMu.Unlock()
}

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
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
			return
		}
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
		if body.RetryInterval < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "retryInterval 必须大于 0"})
			return
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
		if err := state.MutateQueueForAccount(body.AccountID, func(queue []types.QueueItem) ([]types.QueueItem, error) {
			if len(queue) >= app.MaxQueueItems {
				return nil, fmt.Errorf("队列已满（上限 %d）", app.MaxQueueItems)
			}
			return append(queue, item), nil
		}); err != nil {
			state.Logger.Error("添加队列任务失败: "+err.Error(), "queue")
			status := http.StatusInternalServerError
			if errors.Is(err, app.ErrAccountNotFound) {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"status": "error", "error": err.Error()})
			return
		}
		state.Logger.Info("添加任务 "+item.ID+" ("+item.PlanCode+" 在 "+item.Datacenter+", 账户 "+body.AccountID+") 到队列并立即启动 (状态: running)", "")
		c.JSON(http.StatusOK, gin.H{"status": "success", "id": item.ID})
	}
}

// RemoveQueueItem DELETE /api/queue/:id
func RemoveQueueItem(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		alreadyMarked := markQueueItemDeleted(state, id)
		state.Logger.Info("标记任务 "+id+" 为删除，后台线程将立即停止处理", "system")

		var removed *types.QueueItem
		err := state.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
			kept := make([]types.QueueItem, 0, len(queue))
			for i := range queue {
				if queue[i].ID == id {
					cp := queue[i]
					removed = &cp
					continue
				}
				kept = append(kept, queue[i])
			}
			if removed == nil {
				return nil, errQueueItemNotFound
			}
			return kept, nil
		})
		if err != nil {
			// 仅回滚本次请求新增的标记。若此前已有 checkout 不确定
			// 或其它并发流程设置的隔离标记，删除失败不能把它撤销。
			rollbackQueueItemDeleted(state, id, alreadyMarked)
			if errors.Is(err, app.ErrQueueCheckoutInProgress) {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": err.Error()})
			} else if errors.Is(err, errQueueItemNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			}
			return
		}
		if removed != nil {
			state.Logger.Info("Removed "+removed.PlanCode+" from queue (ID: "+id+")", "system")
		}
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}

// ClearQueue DELETE /api/queue/clear
func ClearQueue(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		count := 0
		marked := []string{}
		err := state.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
			count = len(queue)
			state.DeletedTaskIDsMu.Lock()
			if state.DeletedTaskIDs == nil {
				state.DeletedTaskIDs = make(map[string]struct{})
			}
			for _, it := range queue {
				if _, existed := state.DeletedTaskIDs[it.ID]; !existed {
					marked = append(marked, it.ID)
				}
				state.DeletedTaskIDs[it.ID] = struct{}{}
			}
			state.DeletedTaskIDsMu.Unlock()
			return []types.QueueItem{}, nil
		})
		if err != nil {
			state.DeletedTaskIDsMu.Lock()
			for _, id := range marked {
				delete(state.DeletedTaskIDs, id)
			}
			state.DeletedTaskIDsMu.Unlock()
			if errors.Is(err, app.ErrQueueCheckoutInProgress) {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			}
			return
		}
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
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
			return
		}
		body.Status = strings.TrimSpace(body.Status)
		if body.Status != "running" && body.Status != "paused" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "status 仅支持 running 或 paused"})
			return
		}
		planCode := ""
		err := state.MutateQueue(func(queue []types.QueueItem) ([]types.QueueItem, error) {
			for i := range queue {
				if queue[i].ID == id {
					queue[i].Status = body.Status
					queue[i].UpdatedAt = types.NowISO()
					planCode = queue[i].PlanCode
					return queue, nil
				}
			}
			return nil, errQueueItemNotFound
		})
		if err != nil {
			if errors.Is(err, app.ErrQueueCheckoutInProgress) {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": err.Error()})
			} else if errors.Is(err, errQueueItemNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			}
			return
		}
		state.Logger.Info("Updated "+planCode+" status to "+body.Status, "queue")
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
		created := 0
		err := state.MutateQueueForAccount(accountID, func(queue []types.QueueItem) ([]types.QueueItem, error) {
			index = -1
			for i := range queue {
				if queue[i].ID == id {
					index = i
					break
				}
			}
			if index < 0 {
				return nil, errQueueItemNotFound
			}
			created = len(dcs)*qty - 1
			if len(queue)+created > app.MaxQueueItems {
				return nil, fmt.Errorf("队列空间不足（上限 %d）", app.MaxQueueItems)
			}
			queue[index].AccountID = accountID
			queue[index].PlanCode = planCode
			queue[index].Datacenter = dcs[0]
			queue[index].Options = append([]string{}, options...)
			queue[index].RetryInterval = retryInterval
			queue[index].RetryCount = 0
			queue[index].LastCheckTime = 0
			queue[index].Status = "running"
			queue[index].UpdatedAt = now
			for _, dc := range dcs {
				for n := 0; n < qty; n++ {
					if dc == dcs[0] && n == 0 {
						continue
					}
					queue = append(queue, types.QueueItem{
						ID: uuid.NewString(), AccountID: accountID, PlanCode: planCode, Datacenter: dc,
						Options: append([]string{}, options...), Status: "running", CreatedAt: now, UpdatedAt: now,
						RetryInterval: retryInterval, MaxRetries: item.MaxRetries, Priority: item.Priority,
						QuickOrder: item.QuickOrder, FromTelegram: item.FromTelegram,
						ConfigSniperTaskID: item.ConfigSniperTaskID,
					})
				}
			}
			return queue, nil
		})
		if err != nil {
			if errors.Is(err, app.ErrQueueCheckoutInProgress) {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": err.Error()})
				return
			}
			if errors.Is(err, app.ErrAccountNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
				return
			}
			if errors.Is(err, errQueueItemNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": err.Error()})
				return
			}
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
		if err := state.MutateHistory(func([]types.PurchaseHistoryEntry) ([]types.PurchaseHistoryEntry, error) {
			return []types.PurchaseHistoryEntry{}, nil
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
		state.Logger.Info("Purchase history cleared", "")
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}
