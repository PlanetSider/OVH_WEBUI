package db

import (
	"encoding/json"
	"fmt"

	"github.com/ovh-webui/server/internal/types"
)

// queueRow 是表结构的一一对应（snake_case 列名 + JSON 列做 string）
type queueRow struct {
	ID                 string  `db:"id"`
	AccountID          string  `db:"account_id"`
	PlanCode           string  `db:"plan_code"`
	Datacenter         string  `db:"datacenter"`
	OptionsJSON        string  `db:"options"`
	Status             string  `db:"status"`
	CreatedAt          string  `db:"created_at"`
	UpdatedAt          string  `db:"updated_at"`
	RetryInterval      int     `db:"retry_interval"`
	RetryCount         int     `db:"retry_count"`
	MaxRetries         int     `db:"max_retries"`
	LastCheckTime      float64 `db:"last_check_time"`
	QuickOrder         int     `db:"quick_order"`
	Priority           int     `db:"priority"`
	FromTelegram       int     `db:"from_telegram"`
	ConfigSniperTaskID string  `db:"config_sniper_task_id"`
}

func rowToQueueItem(r queueRow) types.QueueItem {
	var opts []string
	if r.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(r.OptionsJSON), &opts)
	}
	if opts == nil {
		opts = []string{}
	}
	return types.QueueItem{
		ID:                 r.ID,
		AccountID:          r.AccountID,
		PlanCode:           r.PlanCode,
		Datacenter:         r.Datacenter,
		Options:            opts,
		Status:             r.Status,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
		RetryInterval:      r.RetryInterval,
		RetryCount:         r.RetryCount,
		MaxRetries:         r.MaxRetries,
		LastCheckTime:      r.LastCheckTime,
		QuickOrder:         r.QuickOrder == 1,
		Priority:           r.Priority,
		FromTelegram:       r.FromTelegram == 1,
		ConfigSniperTaskID: r.ConfigSniperTaskID,
	}
}

func queueItemToRow(q types.QueueItem) (queueRow, error) {
	if q.Options == nil {
		q.Options = []string{}
	}
	optsJSON, err := json.Marshal(q.Options)
	if err != nil {
		return queueRow{}, err
	}
	bi := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	return queueRow{
		ID:                 q.ID,
		AccountID:          q.AccountID,
		PlanCode:           q.PlanCode,
		Datacenter:         q.Datacenter,
		OptionsJSON:        string(optsJSON),
		Status:             q.Status,
		CreatedAt:          q.CreatedAt,
		UpdatedAt:          q.UpdatedAt,
		RetryInterval:      q.RetryInterval,
		RetryCount:         q.RetryCount,
		MaxRetries:         q.MaxRetries,
		LastCheckTime:      q.LastCheckTime,
		QuickOrder:         bi(q.QuickOrder),
		Priority:           q.Priority,
		FromTelegram:       bi(q.FromTelegram),
		ConfigSniperTaskID: q.ConfigSniperTaskID,
	}, nil
}

// ListQueue 取全部队列任务
func (db *DB) ListQueue() ([]types.QueueItem, error) {
	var rows []queueRow
	if err := db.Select(&rows, `SELECT * FROM queue ORDER BY created_at`); err != nil {
		return nil, fmt.Errorf("list queue: %w", err)
	}
	out := make([]types.QueueItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToQueueItem(r))
	}
	return out, nil
}

// ReplaceQueue 用给定列表覆盖整张表（事务内 DELETE + 批量 INSERT）。
// 与原 storage.WriteJSON 语义对齐。
func (db *DB) ReplaceQueue(items []types.QueueItem) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM queue`); err != nil {
		return fmt.Errorf("clear queue: %w", err)
	}
	for _, q := range items {
		r, err := queueItemToRow(q)
		if err != nil {
			return err
		}
		_, err = tx.NamedExec(`
			INSERT INTO queue
			(id, account_id, plan_code, datacenter, options, status, created_at, updated_at,
			 retry_interval, retry_count, max_retries, last_check_time,
			 quick_order, priority, from_telegram, config_sniper_task_id)
			VALUES
			(:id, :account_id, :plan_code, :datacenter, :options, :status, :created_at, :updated_at,
			 :retry_interval, :retry_count, :max_retries, :last_check_time,
			 :quick_order, :priority, :from_telegram, :config_sniper_task_id)
		`, r)
		if err != nil {
			return fmt.Errorf("insert queue %s: %w", q.ID, err)
		}
	}
	return tx.Commit()
}

// DeleteQueueItem 按 id 删除单条
func (db *DB) DeleteQueueItem(id string) error {
	_, err := db.Exec(`DELETE FROM queue WHERE id = ?`, id)
	return err
}

// ClearQueue 清空队列，返回删了多少条
func (db *DB) ClearQueue() (int64, error) {
	res, err := db.Exec(`DELETE FROM queue`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// EnqueueMonitorOrdersAndSaveSubscription 在同一 SQLite 事务内保存监控订阅
// 的最新库存/待下单状态，并插入由本次补货边沿生成的队列任务。这样无论
// 进程在事务前、事务中还是事务后退出，都不会出现队列已写入但订阅仍会
// 再次触发同一批任务的窗口。
func (db *DB) EnqueueMonitorOrdersAndSaveSubscription(sub types.Subscription, items []types.QueueItem, maxQueueItems int) error {
	subRow, err := monitorSubToRow(sub)
	if err != nil {
		return err
	}
	queueRows := make([]queueRow, 0, len(items))
	for _, item := range items {
		row, err := queueItemToRow(item)
		if err != nil {
			return err
		}
		queueRows = append(queueRows, row)
	}

	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentCount int
	if err := tx.Get(&currentCount, `SELECT COUNT(*) FROM queue`); err != nil {
		return fmt.Errorf("count queue before monitor enqueue: %w", err)
	}
	if maxQueueItems > 0 && currentCount+len(queueRows) > maxQueueItems {
		return fmt.Errorf("队列容量不足（当前 %d，本次 %d，上限 %d）", currentCount, len(queueRows), maxQueueItems)
	}

	if _, err := tx.NamedExec(`
		INSERT INTO monitor_subscriptions
		(plan_code, datacenters, memories, storages, networks, notify_available, notify_unavailable, last_status, confirmed_status, pending_order, pending_notify, pending_notify_channels,
		 created_at, history, server_name, auto_order, quantity, auto_order_account_id)
		VALUES
		(:plan_code, :datacenters, :memories, :storages, :networks, :notify_available, :notify_unavailable, :last_status, :confirmed_status, :pending_order, :pending_notify, :pending_notify_channels,
		 :created_at, :history, :server_name, :auto_order, :quantity, :auto_order_account_id)
		ON CONFLICT(plan_code) DO UPDATE SET
		  datacenters = excluded.datacenters, memories = excluded.memories, storages = excluded.storages,
		  networks = excluded.networks, notify_available = excluded.notify_available,
		  notify_unavailable = excluded.notify_unavailable, last_status = excluded.last_status,
		  confirmed_status = excluded.confirmed_status, pending_order = excluded.pending_order,
		  pending_notify = excluded.pending_notify, history = excluded.history, server_name = excluded.server_name,
		  pending_notify_channels = excluded.pending_notify_channels,
		  auto_order = excluded.auto_order, quantity = excluded.quantity,
		  auto_order_account_id = excluded.auto_order_account_id
	`, subRow); err != nil {
		return fmt.Errorf("save monitor subscription %s with orders: %w", sub.PlanCode, err)
	}

	for _, row := range queueRows {
		if _, err := tx.NamedExec(`
			INSERT INTO queue
			(id, account_id, plan_code, datacenter, options, status, created_at, updated_at,
			 retry_interval, retry_count, max_retries, last_check_time,
			 quick_order, priority, from_telegram, config_sniper_task_id)
			VALUES
			(:id, :account_id, :plan_code, :datacenter, :options, :status, :created_at, :updated_at,
			 :retry_interval, :retry_count, :max_retries, :last_check_time,
			 :quick_order, :priority, :from_telegram, :config_sniper_task_id)
		`, row); err != nil {
			return fmt.Errorf("insert monitor queue item %s: %w", row.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit monitor orders for %s: %w", sub.PlanCode, err)
	}
	return nil
}
