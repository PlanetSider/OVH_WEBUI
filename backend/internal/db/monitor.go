package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ovh-webui/server/internal/types"
)

type monitorSubRow struct {
	ID                          string         `db:"id"`
	PlanCode                    string         `db:"plan_code"`
	DatacentersJSON             sql.NullString `db:"datacenters"`
	MemoriesJSON                sql.NullString `db:"memories"`
	StoragesJSON                sql.NullString `db:"storages"`
	NetworksJSON                sql.NullString `db:"networks"`
	NotifyAvailable             int            `db:"notify_available"`
	NotifyUnavailable           int            `db:"notify_unavailable"`
	LastStatusJSON              sql.NullString `db:"last_status"`
	ConfirmedStatusJSON         sql.NullString `db:"confirmed_status"`
	PendingOrderJSON            sql.NullString `db:"pending_order"`
	PendingNotifyJSON           sql.NullString `db:"pending_notify"`
	PendingNotifyChannelsJSON   sql.NullString `db:"pending_notify_channels"`
	CreatedAt                   string         `db:"created_at"`
	HistoryJSON                 sql.NullString `db:"history"`
	ServerName                  string         `db:"server_name"`
	AutoOrder                   int            `db:"auto_order"`
	AutoPay                     int            `db:"auto_pay"`
	Quantity                    int            `db:"quantity"`
	AutoOrderAccountID          string         `db:"auto_order_account_id"`
	Discontinued                int            `db:"discontinued"`
	DiscontinuedNextCheckAt     float64        `db:"discontinued_next_check_at"`
	ProxyGuardAutoOrderDisabled int            `db:"proxy_guard_auto_order_disabled"`
}

func (db *DB) migrateMonitorSubscriptionIDs() error {
	var columns []struct {
		CID  int    `db:"cid"`
		Name string `db:"name"`
		PK   int    `db:"pk"`
	}
	if err := db.Unsafe().Select(&columns, `PRAGMA table_info(monitor_subscriptions)`); err != nil {
		return fmt.Errorf("inspect monitor subscription schema: %w", err)
	}
	hasID, idIsPrimary, planIsPrimary := false, false, false
	for _, column := range columns {
		switch column.Name {
		case "id":
			hasID = true
			idIsPrimary = column.PK == 1
		case "plan_code":
			planIsPrimary = column.PK == 1
		}
	}
	if hasID && idIsPrimary && !planIsPrimary {
		_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_monitor_plan_code ON monitor_subscriptions(plan_code)`)
		return err
	}

	var rows []monitorSubRow
	selectColumns := `plan_code, datacenters, memories, storages, networks,
		notify_available, notify_unavailable, last_status, confirmed_status,
		pending_order, pending_notify, pending_notify_channels, created_at, history, server_name,
		auto_order, auto_pay, quantity, auto_order_account_id, discontinued, discontinued_next_check_at,
		proxy_guard_auto_order_disabled`
	if hasID {
		selectColumns = `id, ` + selectColumns
	}
	if err := db.Select(&rows, `SELECT `+selectColumns+` FROM monitor_subscriptions ORDER BY created_at`); err != nil {
		return fmt.Errorf("read monitor subscriptions for migration: %w", err)
	}

	tx, err := db.Beginx()
	if err != nil {
		return fmt.Errorf("begin monitor subscription migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS monitor_subscriptions_new`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE monitor_subscriptions_new (
		id TEXT PRIMARY KEY,
		plan_code TEXT NOT NULL,
		datacenters TEXT NOT NULL DEFAULT '[]',
		memories TEXT NOT NULL DEFAULT '[]',
		storages TEXT NOT NULL DEFAULT '[]',
		networks TEXT NOT NULL DEFAULT '[]',
		notify_available INTEGER NOT NULL DEFAULT 1,
		notify_unavailable INTEGER NOT NULL DEFAULT 0,
		last_status TEXT NOT NULL DEFAULT '{}',
		confirmed_status TEXT NOT NULL DEFAULT '{}',
		pending_order TEXT NOT NULL DEFAULT '{}',
		pending_notify TEXT NOT NULL DEFAULT '{}',
		pending_notify_channels TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		history TEXT NOT NULL DEFAULT '[]',
		server_name TEXT NOT NULL DEFAULT '',
		auto_order INTEGER NOT NULL DEFAULT 0,
		quantity INTEGER NOT NULL DEFAULT 1,
		auto_order_account_id TEXT NOT NULL DEFAULT '',
		auto_pay INTEGER NOT NULL DEFAULT 0,
		discontinued INTEGER NOT NULL DEFAULT 0,
		discontinued_next_check_at REAL NOT NULL DEFAULT 0,
		proxy_guard_auto_order_disabled INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return fmt.Errorf("create monitor subscription migration table: %w", err)
	}
	insertSQL := `INSERT INTO monitor_subscriptions_new
		(id, plan_code, datacenters, memories, storages, networks, notify_available, notify_unavailable, last_status, confirmed_status, pending_order, pending_notify, pending_notify_channels,
		 created_at, history, server_name, auto_order, auto_pay, quantity, auto_order_account_id, discontinued, discontinued_next_check_at,
		 proxy_guard_auto_order_disabled)
		VALUES
		(:id, :plan_code, :datacenters, :memories, :storages, :networks, :notify_available, :notify_unavailable, :last_status, :confirmed_status, :pending_order, :pending_notify, :pending_notify_channels,
		 :created_at, :history, :server_name, :auto_order, :auto_pay, :quantity, :auto_order_account_id, :discontinued, :discontinued_next_check_at,
		 :proxy_guard_auto_order_disabled)`
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" {
			row.ID = uuid.NewString()
		}
		if _, err := tx.NamedExec(insertSQL, row); err != nil {
			return fmt.Errorf("copy monitor subscription %s: %w", row.PlanCode, err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE monitor_subscriptions`); err != nil {
		return fmt.Errorf("drop old monitor subscriptions: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE monitor_subscriptions_new RENAME TO monitor_subscriptions`); err != nil {
		return fmt.Errorf("rename monitor subscriptions: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_monitor_plan_code ON monitor_subscriptions(plan_code)`); err != nil {
		return fmt.Errorf("index monitor plan code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit monitor subscription migration: %w", err)
	}
	return nil
}

func decodeMonitorJSON(value sql.NullString, target interface{}, field, planCode string) error {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(value.String), target); err != nil {
		return fmt.Errorf("decode %s for %s: %w", field, planCode, err)
	}
	return nil
}

func rowToMonitorSub(r monitorSubRow) (types.Subscription, error) {
	dcs := []string{}
	if err := decodeMonitorJSON(r.DatacentersJSON, &dcs, "datacenters", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	memories := []string{}
	if err := decodeMonitorJSON(r.MemoriesJSON, &memories, "memories", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	storages := []string{}
	if err := decodeMonitorJSON(r.StoragesJSON, &storages, "storages", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	networks := []string{}
	if err := decodeMonitorJSON(r.NetworksJSON, &networks, "networks", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	last := map[string]string{}
	if err := decodeMonitorJSON(r.LastStatusJSON, &last, "last status", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	confirmed := map[string]string{}
	if err := decodeMonitorJSON(r.ConfirmedStatusJSON, &confirmed, "confirmed status", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	// confirmed_status 是新版本增加的列。旧数据库升级后该列默认为空，
	// 但 last_status 里可能已经保存了可靠的有货/无货基线。如果不迁移，
	// 升级后的第一次 unavailable -> available 会漏掉自动补货边沿。只在
	// confirmed_status 整体为空时兼容迁移，避免覆盖新版本保存的部分状态；
	// price_check_failed 等非确认状态不能成为自动下单基线。
	if len(confirmed) == 0 {
		for key, status := range last {
			if status == "available" || status == "unavailable" {
				confirmed[key] = status
			}
		}
	}
	pendingOrder, err := decodePendingOrder(r.PendingOrderJSON, r.PlanCode)
	if err != nil {
		return types.Subscription{}, err
	}
	pendingNotify := map[string]string{}
	if err := decodeMonitorJSON(r.PendingNotifyJSON, &pendingNotify, "pending notifications", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	pendingNotifyChannels := map[string][]string{}
	if err := decodeMonitorJSON(r.PendingNotifyChannelsJSON, &pendingNotifyChannels, "pending notification channels", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	hist := []types.SubscriptionHistoryEntry{}
	if err := decodeMonitorJSON(r.HistoryJSON, &hist, "history", r.PlanCode); err != nil {
		return types.Subscription{}, err
	}
	if dcs == nil {
		dcs = []string{}
	}
	if memories == nil {
		memories = []string{}
	}
	if storages == nil {
		storages = []string{}
	}
	if networks == nil {
		networks = []string{}
	}
	if last == nil {
		last = map[string]string{}
	}
	if confirmed == nil {
		confirmed = map[string]string{}
	}
	if pendingNotify == nil {
		pendingNotify = map[string]string{}
	}
	if pendingNotifyChannels == nil {
		pendingNotifyChannels = map[string][]string{}
	}
	if hist == nil {
		hist = []types.SubscriptionHistoryEntry{}
	}
	return types.Subscription{
		ID:                          r.ID,
		PlanCode:                    r.PlanCode,
		Datacenters:                 dcs,
		Memories:                    memories,
		Storages:                    storages,
		Networks:                    networks,
		NotifyAvailable:             r.NotifyAvailable == 1,
		NotifyUnavailable:           r.NotifyUnavailable == 1,
		LastStatus:                  last,
		ConfirmedStatus:             confirmed,
		PendingOrder:                pendingOrder,
		PendingNotify:               pendingNotify,
		PendingNotifyChannels:       pendingNotifyChannels,
		CreatedAt:                   r.CreatedAt,
		History:                     hist,
		ServerName:                  r.ServerName,
		AutoOrder:                   r.AutoOrder == 1,
		AutoPay:                     r.AutoPay == 1,
		Quantity:                    r.Quantity,
		AutoOrderAccountID:          r.AutoOrderAccountID,
		Discontinued:                r.Discontinued == 1,
		DiscontinuedNextCheckAt:     r.DiscontinuedNextCheckAt,
		ProxyGuardAutoOrderDisabled: r.ProxyGuardAutoOrderDisabled == 1,
	}, nil
}

func monitorSubToRow(s types.Subscription) (monitorSubRow, error) {
	if strings.TrimSpace(s.ID) == "" {
		s.ID = uuid.NewString()
	}
	if s.Datacenters == nil {
		s.Datacenters = []string{}
	}
	if s.Memories == nil {
		s.Memories = []string{}
	}
	if s.Storages == nil {
		s.Storages = []string{}
	}
	if s.Networks == nil {
		s.Networks = []string{}
	}
	if s.LastStatus == nil {
		s.LastStatus = map[string]string{}
	}
	if s.History == nil {
		s.History = []types.SubscriptionHistoryEntry{}
	}
	dcsJSON, err := json.Marshal(s.Datacenters)
	if err != nil {
		return monitorSubRow{}, err
	}
	if s.ConfirmedStatus == nil {
		s.ConfirmedStatus = map[string]string{}
	}
	if s.PendingOrder == nil {
		s.PendingOrder = map[string]int{}
	}
	if s.PendingNotify == nil {
		s.PendingNotify = map[string]string{}
	}
	if s.PendingNotifyChannels == nil {
		s.PendingNotifyChannels = map[string][]string{}
	}
	memoriesJSON, err := json.Marshal(s.Memories)
	if err != nil {
		return monitorSubRow{}, err
	}
	storagesJSON, err := json.Marshal(s.Storages)
	if err != nil {
		return monitorSubRow{}, err
	}
	networksJSON, err := json.Marshal(s.Networks)
	if err != nil {
		return monitorSubRow{}, err
	}
	lastJSON, err := json.Marshal(s.LastStatus)
	if err != nil {
		return monitorSubRow{}, err
	}
	confirmedJSON, err := json.Marshal(s.ConfirmedStatus)
	if err != nil {
		return monitorSubRow{}, err
	}
	pendingOrderJSON, err := json.Marshal(s.PendingOrder)
	if err != nil {
		return monitorSubRow{}, err
	}
	pendingNotifyJSON, err := json.Marshal(s.PendingNotify)
	if err != nil {
		return monitorSubRow{}, err
	}
	pendingNotifyChannelsJSON, err := json.Marshal(s.PendingNotifyChannels)
	if err != nil {
		return monitorSubRow{}, err
	}
	histJSON, err := json.Marshal(s.History)
	if err != nil {
		return monitorSubRow{}, err
	}
	bi := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	return monitorSubRow{
		ID:                          s.ID,
		PlanCode:                    s.PlanCode,
		DatacentersJSON:             sql.NullString{String: string(dcsJSON), Valid: true},
		MemoriesJSON:                sql.NullString{String: string(memoriesJSON), Valid: true},
		StoragesJSON:                sql.NullString{String: string(storagesJSON), Valid: true},
		NetworksJSON:                sql.NullString{String: string(networksJSON), Valid: true},
		NotifyAvailable:             bi(s.NotifyAvailable),
		NotifyUnavailable:           bi(s.NotifyUnavailable),
		LastStatusJSON:              sql.NullString{String: string(lastJSON), Valid: true},
		ConfirmedStatusJSON:         sql.NullString{String: string(confirmedJSON), Valid: true},
		PendingOrderJSON:            sql.NullString{String: string(pendingOrderJSON), Valid: true},
		PendingNotifyJSON:           sql.NullString{String: string(pendingNotifyJSON), Valid: true},
		PendingNotifyChannelsJSON:   sql.NullString{String: string(pendingNotifyChannelsJSON), Valid: true},
		CreatedAt:                   s.CreatedAt,
		HistoryJSON:                 sql.NullString{String: string(histJSON), Valid: true},
		ServerName:                  s.ServerName,
		AutoOrder:                   bi(s.AutoOrder),
		AutoPay:                     bi(s.AutoPay),
		Quantity:                    s.Quantity,
		AutoOrderAccountID:          s.AutoOrderAccountID,
		Discontinued:                bi(s.Discontinued),
		DiscontinuedNextCheckAt:     s.DiscontinuedNextCheckAt,
		ProxyGuardAutoOrderDisabled: bi(s.ProxyGuardAutoOrderDisabled),
	}, nil
}

// decodePendingOrder 兼容旧版本持久化的 map[string]bool。true 表示还有
// 一次入队待办；新版本使用整数精确记录批量入队失败后的剩余数量。
func decodePendingOrder(value sql.NullString, planCode string) (map[string]int, error) {
	out := map[string]int{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return out, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return nil, fmt.Errorf("decode pending order for %s: %w", planCode, err)
	}
	if raw == nil {
		return out, nil
	}
	for key, item := range raw {
		var count int
		if err := json.Unmarshal(item, &count); err == nil {
			if count > 0 {
				out[key] = count
			}
			continue
		}
		var pending bool
		if err := json.Unmarshal(item, &pending); err == nil {
			if pending {
				out[key] = 1
			}
			continue
		}
		return nil, fmt.Errorf("decode pending order entry %q for %s: invalid count", key, planCode)
	}
	return out, nil
}

// ListMonitorSubscriptions 取全部服务器监控订阅
func (db *DB) ListMonitorSubscriptions() ([]types.Subscription, error) {
	var rows []monitorSubRow
	if err := db.Select(&rows, `
		SELECT id, plan_code, datacenters, memories, storages, networks,
		       notify_available, notify_unavailable, last_status, confirmed_status,
		       pending_order, pending_notify, pending_notify_channels, created_at, history, server_name,
		       auto_order, auto_pay, quantity, auto_order_account_id, discontinued, discontinued_next_check_at,
		       proxy_guard_auto_order_disabled
		FROM monitor_subscriptions ORDER BY created_at
	`); err != nil {
		return nil, fmt.Errorf("list monitor subs: %w", err)
	}
	out := make([]types.Subscription, 0, len(rows))
	for _, r := range rows {
		sub, err := rowToMonitorSub(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, nil
}

// UpsertMonitorSubscription 按订阅实例 ID upsert；plan_code 仅用于业务筛选。
func (db *DB) UpsertMonitorSubscription(s types.Subscription) error {
	r, err := monitorSubToRow(s)
	if err != nil {
		return err
	}
	_, err = db.NamedExec(`
		INSERT INTO monitor_subscriptions
		(id, plan_code, datacenters, memories, storages, networks, notify_available, notify_unavailable, last_status, confirmed_status, pending_order, pending_notify, pending_notify_channels,
		 created_at, history, server_name, auto_order, auto_pay, quantity, auto_order_account_id, discontinued, discontinued_next_check_at,
		       proxy_guard_auto_order_disabled)
		VALUES
		(:id, :plan_code, :datacenters, :memories, :storages, :networks, :notify_available, :notify_unavailable, :last_status, :confirmed_status, :pending_order, :pending_notify, :pending_notify_channels,
		 :created_at, :history, :server_name, :auto_order, :auto_pay, :quantity, :auto_order_account_id, :discontinued, :discontinued_next_check_at,
			 :proxy_guard_auto_order_disabled)
		ON CONFLICT(id) DO UPDATE SET
		  datacenters        = excluded.datacenters,
		  memories           = excluded.memories,
		  storages           = excluded.storages,
		  networks           = excluded.networks,
		  notify_available   = excluded.notify_available,
		  notify_unavailable = excluded.notify_unavailable,
		  last_status        = excluded.last_status,
		  confirmed_status   = excluded.confirmed_status,
		  pending_order      = excluded.pending_order,
		  pending_notify     = excluded.pending_notify,
		  pending_notify_channels = excluded.pending_notify_channels,
		  history            = excluded.history,
		  server_name        = excluded.server_name,
		  auto_order             = excluded.auto_order,
		  auto_pay               = excluded.auto_pay,
		  quantity               = excluded.quantity,
		  auto_order_account_id  = excluded.auto_order_account_id,
		  discontinued            = excluded.discontinued,
		  discontinued_next_check_at = excluded.discontinued_next_check_at,
		  proxy_guard_auto_order_disabled = excluded.proxy_guard_auto_order_disabled
	`, r)
	if err != nil {
		return fmt.Errorf("upsert monitor sub %s: %w", s.PlanCode, err)
	}
	return nil
}

// ReplaceMonitorSubscriptions 全表覆盖
func (db *DB) ReplaceMonitorSubscriptions(subs []types.Subscription) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM monitor_subscriptions`); err != nil {
		return err
	}
	for _, s := range subs {
		r, err := monitorSubToRow(s)
		if err != nil {
			return err
		}
		_, err = tx.NamedExec(`
			INSERT INTO monitor_subscriptions
			(id, plan_code, datacenters, memories, storages, networks, notify_available, notify_unavailable, last_status, confirmed_status, pending_order, pending_notify, pending_notify_channels,
			 created_at, history, server_name, auto_order, auto_pay, quantity, auto_order_account_id, discontinued, discontinued_next_check_at,
		       proxy_guard_auto_order_disabled)
			VALUES
			(:id, :plan_code, :datacenters, :memories, :storages, :networks, :notify_available, :notify_unavailable, :last_status, :confirmed_status, :pending_order, :pending_notify, :pending_notify_channels,
			 :created_at, :history, :server_name, :auto_order, :auto_pay, :quantity, :auto_order_account_id, :discontinued, :discontinued_next_check_at,
			 :proxy_guard_auto_order_disabled)
		`, r)
		if err != nil {
			return fmt.Errorf("insert monitor sub %s: %w", s.PlanCode, err)
		}
	}
	return tx.Commit()
}

// ReplaceMonitorSubscriptionsAndKnownServers 在同一事务内覆盖监控订阅并更新
// known_servers 基线，避免进程在两次独立提交之间退出而留下跨表不一致状态。
func (db *DB) ReplaceMonitorSubscriptionsAndKnownServers(subs []types.Subscription, knownServers []string) error {
	rows := make([]monitorSubRow, 0, len(subs))
	for _, sub := range subs {
		row, err := monitorSubToRow(sub)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	knownJSON, err := json.Marshal(knownServers)
	if err != nil {
		return fmt.Errorf("encode monitor known servers: %w", err)
	}

	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM monitor_subscriptions`); err != nil {
		return fmt.Errorf("clear monitor subscriptions: %w", err)
	}
	for _, row := range rows {
		if _, err := tx.NamedExec(`
			INSERT INTO monitor_subscriptions
			(id, plan_code, datacenters, memories, storages, networks, notify_available, notify_unavailable, last_status, confirmed_status, pending_order, pending_notify, pending_notify_channels,
			 created_at, history, server_name, auto_order, auto_pay, quantity, auto_order_account_id, discontinued, discontinued_next_check_at,
		       proxy_guard_auto_order_disabled)
			VALUES
			(:id, :plan_code, :datacenters, :memories, :storages, :networks, :notify_available, :notify_unavailable, :last_status, :confirmed_status, :pending_order, :pending_notify, :pending_notify_channels,
			 :created_at, :history, :server_name, :auto_order, :auto_pay, :quantity, :auto_order_account_id, :discontinued, :discontinued_next_check_at,
			 :proxy_guard_auto_order_disabled)
		`, row); err != nil {
			return fmt.Errorf("insert monitor sub %s: %w", row.PlanCode, err)
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO kv(key, value) VALUES('monitor_known_servers', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, string(knownJSON)); err != nil {
		return fmt.Errorf("save monitor known servers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit monitor state: %w", err)
	}
	return nil
}

// SaveKnownServersAndNotifications 将新服务器基线与对应通知事件原子提交。
// 这样进程不会在“已经记住服务器”和“尚未保存通知”之间退出而永久漏报。
func (db *DB) SaveKnownServersAndNotifications(knownServers []string, entries []types.NotificationOutboxEntry) error {
	knownJSON, err := json.Marshal(knownServers)
	if err != nil {
		return fmt.Errorf("encode monitor known servers: %w", err)
	}
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO kv(key, value) VALUES('monitor_known_servers', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, string(knownJSON)); err != nil {
		return fmt.Errorf("save monitor known servers: %w", err)
	}
	for _, entry := range entries {
		if err := insertNotificationOutboxTx(tx, entry); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit known servers and notifications: %w", err)
	}
	return nil
}

// DeleteMonitorSubscription 按实例 ID 删除
func (db *DB) DeleteMonitorSubscription(id string) error {
	_, err := db.Exec(`DELETE FROM monitor_subscriptions WHERE id = ?`, id)
	return err
}

// ClearMonitorSubscriptions 清空
func (db *DB) ClearMonitorSubscriptions() (int64, error) {
	res, err := db.Exec(`DELETE FROM monitor_subscriptions`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
