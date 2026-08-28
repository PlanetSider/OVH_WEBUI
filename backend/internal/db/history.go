package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ovh-webui/server/internal/types"
)

// ErrCheckoutAttemptExists 表示该队列任务已经拥有不可覆盖的 checkout 恢复记录。
// 调用方必须停止自动 checkout 并人工核对，而不是复用 task_id 创建新订单。
var ErrCheckoutAttemptExists = errors.New("checkout attempt already exists")

// ErrPurchaseOrderConflict 表示同一队列任务已经关联了另一个成功订单。
// 这类冲突不能通过覆盖历史或重新入队解决，必须保留 checkout attempt 供人工核对。
var ErrPurchaseOrderConflict = errors.New("purchase task already has a different successful order")

type historyRow struct {
	ID             string         `db:"id"`
	AccountID      string         `db:"account_id"`
	TaskID         string         `db:"task_id"`
	PlanCode       string         `db:"plan_code"`
	Datacenter     string         `db:"datacenter"`
	OptionsJSON    string         `db:"options"`
	Status         string         `db:"status"`
	OrderID        string         `db:"order_id"`
	OrderURL       string         `db:"order_url"`
	ErrorMessage   sql.NullString `db:"error_message"`
	PurchaseTime   string         `db:"purchase_time"`
	AttemptCount   int            `db:"attempt_count"`
	ExpirationTime string         `db:"expiration_time"`
	PriceJSON      sql.NullString `db:"price"`
}

func rowToHistory(r historyRow) types.PurchaseHistoryEntry {
	var opts []string
	if r.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(r.OptionsJSON), &opts)
	}
	if opts == nil {
		opts = []string{}
	}
	var price *types.PriceInfo
	if r.PriceJSON.Valid && r.PriceJSON.String != "" {
		var p types.PriceInfo
		if err := json.Unmarshal([]byte(r.PriceJSON.String), &p); err == nil {
			price = &p
		}
	}
	var errMsg *string
	if r.ErrorMessage.Valid {
		s := r.ErrorMessage.String
		errMsg = &s
	}
	return types.PurchaseHistoryEntry{
		ID:             r.ID,
		AccountID:      r.AccountID,
		TaskID:         r.TaskID,
		PlanCode:       r.PlanCode,
		Datacenter:     r.Datacenter,
		Options:        opts,
		Status:         r.Status,
		OrderID:        r.OrderID,
		OrderURL:       r.OrderURL,
		ErrorMessage:   errMsg,
		PurchaseTime:   r.PurchaseTime,
		AttemptCount:   r.AttemptCount,
		ExpirationTime: r.ExpirationTime,
		Price:          price,
	}
}

func historyToRow(h types.PurchaseHistoryEntry) (historyRow, error) {
	if h.Options == nil {
		h.Options = []string{}
	}
	optsJSON, err := json.Marshal(h.Options)
	if err != nil {
		return historyRow{}, err
	}
	row := historyRow{
		ID:             h.ID,
		AccountID:      h.AccountID,
		TaskID:         h.TaskID,
		PlanCode:       h.PlanCode,
		Datacenter:     h.Datacenter,
		OptionsJSON:    string(optsJSON),
		Status:         h.Status,
		OrderID:        h.OrderID,
		OrderURL:       h.OrderURL,
		PurchaseTime:   h.PurchaseTime,
		AttemptCount:   h.AttemptCount,
		ExpirationTime: h.ExpirationTime,
	}
	if h.ErrorMessage != nil {
		row.ErrorMessage = sql.NullString{String: *h.ErrorMessage, Valid: true}
	}
	if h.Price != nil {
		priceJSON, err := json.Marshal(h.Price)
		if err != nil {
			return row, err
		}
		row.PriceJSON = sql.NullString{String: string(priceJSON), Valid: true}
	}
	return row, nil
}

// ListHistory 取全部抢购历史，按时间倒序
func (db *DB) ListHistory() ([]types.PurchaseHistoryEntry, error) {
	var rows []historyRow
	if err := db.Select(&rows, `SELECT * FROM history ORDER BY purchase_time DESC`); err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	out := make([]types.PurchaseHistoryEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToHistory(r))
	}
	return out, nil
}

// ReplaceHistory 全表覆盖（保留 ReplaceX API 一致）
func (db *DB) ReplaceHistory(items []types.PurchaseHistoryEntry) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM history`); err != nil {
		return fmt.Errorf("clear history: %w", err)
	}
	for _, h := range items {
		r, err := historyToRow(h)
		if err != nil {
			return err
		}
		_, err = tx.NamedExec(`
			INSERT INTO history
			(id, account_id, task_id, plan_code, datacenter, options, status, order_id, order_url,
			 error_message, purchase_time, attempt_count, expiration_time, price)
			VALUES
			(:id, :account_id, :task_id, :plan_code, :datacenter, :options, :status, :order_id, :order_url,
			 :error_message, :purchase_time, :attempt_count, :expiration_time, :price)
		`, r)
		if err != nil {
			return fmt.Errorf("insert history %s: %w", h.ID, err)
		}
	}
	return tx.Commit()
}

// ClearHistory 清空历史
func (db *DB) ClearHistory() (int64, error) {
	res, err := db.Exec(`DELETE FROM history`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CommitPurchaseSuccess 在同一事务内写入成功历史并删除队列任务。按 task_id
// 先删后插使该操作可安全重试，也兼容旧库里已经存在的失败尝试记录。
func (db *DB) CommitPurchaseSuccess(entry types.PurchaseHistoryEntry) error {
	return db.CommitPurchaseSuccessWithNotification(entry, nil)
}

// CommitPurchaseSuccessWithNotification 在同一事务中提交成功历史、删除队列/checkout
// 记录并创建通知 outbox。通知网络发送在事务外完成。
func (db *DB) CommitPurchaseSuccessWithNotification(entry types.PurchaseHistoryEntry, notification *types.NotificationOutboxEntry) error {
	r, err := historyToRow(entry)
	if err != nil {
		return err
	}
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 成功历史按 task_id 逻辑上只能对应一个订单。先做冲突检查，再执行
	// 兼容旧库的先删后插；否则进程恢复或重复回调可能把原订单覆盖掉。
	var existing struct {
		Status  string `db:"status"`
		OrderID string `db:"order_id"`
	}
	err = tx.Get(&existing, `
		SELECT status, order_id FROM history
		WHERE task_id = ? AND status = 'success'
		ORDER BY purchase_time DESC LIMIT 1
	`, entry.TaskID)
	if err == nil {
		if strings.TrimSpace(existing.OrderID) != "" && existing.OrderID != entry.OrderID {
			return fmt.Errorf("%w: task %s already has order %s, received %s", ErrPurchaseOrderConflict, entry.TaskID, existing.OrderID, entry.OrderID)
		}
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("check existing successful history for task %s: %w", entry.TaskID, err)
	}
	if _, err := tx.Exec(`DELETE FROM history WHERE task_id = ?`, entry.TaskID); err != nil {
		return fmt.Errorf("delete previous history for task %s: %w", entry.TaskID, err)
	}
	if _, err := tx.NamedExec(`
		INSERT INTO history
		(id, account_id, task_id, plan_code, datacenter, options, status, order_id, order_url,
		 error_message, purchase_time, attempt_count, expiration_time, price)
		VALUES
		(:id, :account_id, :task_id, :plan_code, :datacenter, :options, :status, :order_id, :order_url,
		 :error_message, :purchase_time, :attempt_count, :expiration_time, :price)
	`, r); err != nil {
		return fmt.Errorf("insert successful history for task %s: %w", entry.TaskID, err)
	}
	if _, err := tx.Exec(`DELETE FROM queue WHERE id = ?`, entry.TaskID); err != nil {
		return fmt.Errorf("delete successful queue task %s: %w", entry.TaskID, err)
	}
	if _, err := tx.Exec(`DELETE FROM checkout_attempts WHERE task_id = ?`, entry.TaskID); err != nil {
		return fmt.Errorf("delete checkout attempt %s: %w", entry.TaskID, err)
	}
	// 即使当前没有已配置渠道，也要保留 AwaitingChannels 事件；启动恢复
	// 可能发生在飞书/微信初始化之前，后续配置完成后由 outbox 再分配渠道。
	if notification != nil {
		if err := insertNotificationOutboxTx(tx, *notification); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordCheckoutAttempt 必须在真正调用 OVH checkout 前完成。重启恢复时，
// checkout 是否已被 OVH 接收可能无法可靠判断，因此宁可保留人工核对记录也不能重复下单。
func (db *DB) RecordCheckoutAttempt(item types.QueueItem, cartID string) error {
	opts, err := json.Marshal(item.Options)
	if err != nil {
		return fmt.Errorf("encode checkout options for %s: %w", item.ID, err)
	}
	result, err := db.Exec(`
		INSERT INTO checkout_attempts
		(task_id, cart_id, account_id, plan_code, datacenter, options, attempt_count, started_at, order_id, order_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '')
		ON CONFLICT(task_id) DO NOTHING
	`, item.ID, cartID, item.AccountID, item.PlanCode, item.Datacenter, string(opts), item.RetryCount, types.NowISO())
	if err != nil {
		return fmt.Errorf("record checkout attempt %s: %w", item.ID, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check checkout attempt %s: %w", item.ID, err)
	}
	if inserted != 1 {
		return fmt.Errorf("%w: task %s", ErrCheckoutAttemptExists, item.ID)
	}
	return nil
}

func (db *DB) CompleteCheckoutAttempt(taskID, orderID, orderURL string) error {
	result, err := db.Exec(`
		UPDATE checkout_attempts SET order_id = ?, order_url = ?
		WHERE task_id = ? AND (order_id = '' OR order_id = ?)
	`, orderID, orderURL, taskID, orderID)
	if err != nil {
		return fmt.Errorf("complete checkout attempt %s: %w", taskID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check completed checkout attempt %s: %w", taskID, err)
	}
	if updated != 1 {
		return fmt.Errorf("complete checkout attempt %s: record missing or contains another order", taskID)
	}
	return nil
}

// EnsureCheckoutAttemptCompleted 在 checkout 已经返回订单号、但普通 UPDATE
// 没有找到防重复记录时补写恢复记录。它不会覆盖已经记录的其它订单号；
// 这样即使进程正好在成功 checkout 与本地事务之间退出，启动恢复仍能把
// 该订单安全恢复为成功历史，而不会再次下单。
func (db *DB) EnsureCheckoutAttemptCompleted(item types.QueueItem, cartID, orderID, orderURL string) error {
	if item.ID == "" || cartID == "" || orderID == "" {
		return fmt.Errorf("ensure checkout attempt requires task, cart and order IDs")
	}
	opts, err := json.Marshal(item.Options)
	if err != nil {
		return fmt.Errorf("encode checkout options for %s: %w", item.ID, err)
	}
	res, err := db.Exec(`
		INSERT INTO checkout_attempts
		(task_id, cart_id, account_id, plan_code, datacenter, options, attempt_count, started_at, order_id, order_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
		  order_id = excluded.order_id, order_url = excluded.order_url
		WHERE checkout_attempts.order_id = '' OR checkout_attempts.order_id = excluded.order_id
	`, item.ID, cartID, item.AccountID, item.PlanCode, item.Datacenter, string(opts), item.RetryCount, types.NowISO(), orderID, orderURL)
	if err != nil {
		return fmt.Errorf("ensure completed checkout attempt %s: %w", item.ID, err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check ensured checkout attempt %s: %w", item.ID, err)
	}
	if updated != 1 {
		return fmt.Errorf("checkout attempt %s already contains a different order", item.ID)
	}
	return nil
}

// RemoveCheckoutAttempt 仅用于已明确收到 OVH HTTP 错误的 checkout。
// 网络超时、连接中断等不确定结果不能调用它，否则重启后可能重复下单。
func (db *DB) RemoveCheckoutAttempt(taskID string) error {
	if _, err := db.Exec(`DELETE FROM checkout_attempts WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("remove checkout attempt %s: %w", taskID, err)
	}
	return nil
}

// RecoverCheckoutAttempts 把遗留的不可判定 checkout 任务从运行队列中移除。
// 已拿到 order_id 的记录同时恢复为成功历史；未拿到的记录保留在表中供日志提示和人工核对。
func (db *DB) RecoverCheckoutAttempts(notificationChannels []string) (recoveredSuccess, quarantined int64, err error) {
	tx, err := db.Beginx()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	type attemptRow struct {
		TaskID       string `db:"task_id"`
		AccountID    string `db:"account_id"`
		PlanCode     string `db:"plan_code"`
		Datacenter   string `db:"datacenter"`
		OptionsJSON  string `db:"options"`
		AttemptCount int    `db:"attempt_count"`
		StartedAt    string `db:"started_at"`
		OrderID      string `db:"order_id"`
		OrderURL     string `db:"order_url"`
	}
	var attempts []attemptRow
	if err := tx.Select(&attempts, `SELECT task_id, account_id, plan_code, datacenter, options, attempt_count, started_at, order_id, order_url FROM checkout_attempts`); err != nil {
		return 0, 0, fmt.Errorf("list checkout attempts: %w", err)
	}
	for _, attempt := range attempts {
		// 只要存在 checkout attempt，就说明该任务已经越过了真正 checkout
		// 前的防重复闸门。无论是否拿到订单号、恢复数据是否损坏，都必须
		// 在同一事务中先从运行队列隔离。不能等循环末尾再通过
		// checkout_attempts 子查询清理：成功恢复会先删除 attempt，那样对应
		// 的队列任务就会漏删，并可能在重启后再次下单。
		if _, err := tx.Exec(`DELETE FROM queue WHERE id = ?`, attempt.TaskID); err != nil {
			return 0, 0, fmt.Errorf("quarantine checkout queue task %s: %w", attempt.TaskID, err)
		}
		if attempt.OrderID == "" {
			// 进程可能在 checkout 请求发出后、recordUncertain 写历史前退出。
			// 启动恢复必须补出“待核实”记录，避免界面只看到任务消失，
			// 也不能沿用旧的 failed 状态误导用户安全重试。
			var options []string
			if attempt.OptionsJSON != "" {
				if err := json.Unmarshal([]byte(attempt.OptionsJSON), &options); err != nil {
					options = []string{}
				}
			}
			if options == nil {
				options = []string{}
			}
			message := "checkout 结果不确定，已停止自动重试；请人工核对 OVH 购物车和订单后再决定是否重建任务"
			entry := types.PurchaseHistoryEntry{
				ID: uuid.NewString(), TaskID: attempt.TaskID, AccountID: attempt.AccountID,
				PlanCode: attempt.PlanCode, Datacenter: attempt.Datacenter, Options: options,
				Status: "uncertain", ErrorMessage: &message, PurchaseTime: attempt.StartedAt,
				AttemptCount: attempt.AttemptCount,
			}
			r, err := historyToRow(entry)
			if err != nil {
				return 0, 0, fmt.Errorf("encode uncertain checkout history %s: %w", attempt.TaskID, err)
			}
			if _, err := tx.Exec(`DELETE FROM history WHERE task_id = ? AND status != 'success'`, attempt.TaskID); err != nil {
				return 0, 0, fmt.Errorf("delete old uncertain history %s: %w", attempt.TaskID, err)
			}
			var successfulHistory int
			if err := tx.Get(&successfulHistory, `SELECT COUNT(1) FROM history WHERE task_id = ? AND status = 'success'`, attempt.TaskID); err != nil {
				return 0, 0, fmt.Errorf("check successful history for uncertain checkout %s: %w", attempt.TaskID, err)
			}
			if successfulHistory == 0 {
				if _, err := tx.NamedExec(`
					INSERT INTO history
					(id, account_id, task_id, plan_code, datacenter, options, status, order_id, order_url,
					 error_message, purchase_time, attempt_count, expiration_time, price)
					VALUES
					(:id, :account_id, :task_id, :plan_code, :datacenter, :options, :status, :order_id, :order_url,
					 :error_message, :purchase_time, :attempt_count, :expiration_time, :price)
				`, r); err != nil {
					return 0, 0, fmt.Errorf("insert uncertain checkout history %s: %w", attempt.TaskID, err)
				}
			}
			quarantined++
			continue
		}
		// 旧版本或一次人工修复可能已经写入成功历史、但 checkout attempt
		// 仍然残留。相同订单视为幂等恢复；不同订单绝不能互相覆盖，保留
		// attempt 并隔离队列，交给人工核对是否发生过重复下单。
		var existing struct {
			Status  string `db:"status"`
			OrderID string `db:"order_id"`
		}
		existingFound := false
		if err := tx.Get(&existing, `
			SELECT status, order_id FROM history
			WHERE task_id = ? ORDER BY purchase_time DESC LIMIT 1
		`, attempt.TaskID); err == nil {
			existingFound = true
		} else if err != sql.ErrNoRows {
			return 0, 0, fmt.Errorf("read existing checkout history %s: %w", attempt.TaskID, err)
		}
		if existingFound && existing.Status == "success" && existing.OrderID != "" && existing.OrderID != attempt.OrderID {
			quarantined++
			continue
		}
		var options []string
		if attempt.OptionsJSON != "" {
			if err := json.Unmarshal([]byte(attempt.OptionsJSON), &options); err != nil {
				// options 只用于历史展示和通知，不影响订单身份。损坏的一行不能
				// 阻塞其它 checkout 的恢复，更不能因此让整个抢购队列失去启动
				// 保护；保守地恢复为空选项并继续保留订单号。
				options = []string{}
			}
		}
		if options == nil {
			options = []string{}
		}
		if !(existingFound && existing.Status == "success" && existing.OrderID == attempt.OrderID) {
			if _, err := tx.Exec(`DELETE FROM history WHERE task_id = ?`, attempt.TaskID); err != nil {
				return 0, 0, fmt.Errorf("delete old history %s: %w", attempt.TaskID, err)
			}
			entry := types.PurchaseHistoryEntry{
				ID: uuid.NewString(), TaskID: attempt.TaskID, AccountID: attempt.AccountID,
				PlanCode: attempt.PlanCode, Datacenter: attempt.Datacenter, Options: options,
				Status: "success", OrderID: attempt.OrderID, OrderURL: attempt.OrderURL,
				PurchaseTime: attempt.StartedAt, AttemptCount: attempt.AttemptCount,
			}
			r, err := historyToRow(entry)
			if err != nil {
				return 0, 0, fmt.Errorf("encode recovered history %s: %w", attempt.TaskID, err)
			}
			if _, err := tx.NamedExec(`
				INSERT INTO history
				(id, account_id, task_id, plan_code, datacenter, options, status, order_id, order_url,
				 error_message, purchase_time, attempt_count, expiration_time, price)
				VALUES
				(:id, :account_id, :task_id, :plan_code, :datacenter, :options, :status, :order_id, :order_url,
				 :error_message, :purchase_time, :attempt_count, :expiration_time, :price)
			`, r); err != nil {
				return 0, 0, fmt.Errorf("insert recovered history %s: %w", attempt.TaskID, err)
			}
		}
		payload, err := json.Marshal(map[string]interface{}{
			"taskId": attempt.TaskID, "accountId": attempt.AccountID,
			"planCode": attempt.PlanCode, "datacenter": attempt.Datacenter,
			"options": options, "orderId": attempt.OrderID, "orderUrl": attempt.OrderURL,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("encode recovered notification %s: %w", attempt.TaskID, err)
		}
		if err := insertNotificationOutboxTx(tx, types.NotificationOutboxEntry{
			EventKey: "purchase_success:" + attempt.TaskID, Kind: "purchase_success",
			Payload: string(payload), Channels: append([]string(nil), notificationChannels...),
			AwaitingChannels: len(notificationChannels) == 0,
		}); err != nil {
			return 0, 0, fmt.Errorf("enqueue recovered notification %s: %w", attempt.TaskID, err)
		}
		if _, err := tx.Exec(`DELETE FROM checkout_attempts WHERE task_id = ? AND order_id = ?`, attempt.TaskID, attempt.OrderID); err != nil {
			return 0, 0, fmt.Errorf("remove recovered checkout attempt %s: %w", attempt.TaskID, err)
		}
		recoveredSuccess++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return recoveredSuccess, quarantined, nil
}

// RemoveSuccessfullyPurchasedQueueItems 清理旧版本在成功历史落盘后、队列删除前
// 退出所留下的任务，避免升级后的首次启动再次 checkout。
func (db *DB) RemoveSuccessfullyPurchasedQueueItems() (int64, error) {
	res, err := db.Exec(`
		DELETE FROM queue
		WHERE EXISTS (
			SELECT 1 FROM history
			WHERE history.task_id = queue.id AND history.status = 'success'
		)
	`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
