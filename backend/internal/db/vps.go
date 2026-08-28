package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ovh-webui/server/internal/types"
)

type vpsSubRow struct {
	ID                 string         `db:"id"`
	PlanCode           string         `db:"plan_code"`
	OvhSubsidiary      string         `db:"ovh_subsidiary"`
	DatacentersJSON    sql.NullString `db:"datacenters"`
	MonitorLinux       int            `db:"monitor_linux"`
	MonitorWindows     int            `db:"monitor_windows"`
	NotifyAvailable    int            `db:"notify_available"`
	NotifyUnavailable  int            `db:"notify_unavailable"`
	LastStatusJSON     sql.NullString `db:"last_status"`
	HistoryJSON        sql.NullString `db:"history"`
	CreatedAt          string         `db:"created_at"`
	PendingNotifyJSON  sql.NullString `db:"pending_notify"`
	PendingNotifyChannelsJSON sql.NullString `db:"pending_notify_channels"`
	AutoOrderAccountID string         `db:"auto_order_account_id"` // 旧列兼容，始终写空
}

func rowToVPSSub(r vpsSubRow) (types.VPSSubscription, error) {
	var dcs []string
	if value := strings.TrimSpace(r.DatacentersJSON.String); value != "" {
		if err := json.Unmarshal([]byte(value), &dcs); err != nil {
			return types.VPSSubscription{}, fmt.Errorf("decode datacenters for %s: %w", r.ID, err)
		}
	}
	if dcs == nil {
		dcs = []string{}
	}
	last := map[string]string{}
	if value := strings.TrimSpace(r.LastStatusJSON.String); value != "" {
		if err := json.Unmarshal([]byte(value), &last); err != nil {
			return types.VPSSubscription{}, fmt.Errorf("decode last status for %s: %w", r.ID, err)
		}
	}
	pending := map[string]string{}
	if value := strings.TrimSpace(r.PendingNotifyJSON.String); value != "" {
		if err := json.Unmarshal([]byte(value), &pending); err != nil {
			return types.VPSSubscription{}, fmt.Errorf("decode pending notifications for %s: %w", r.ID, err)
		}
	}
	pendingChannels := map[string][]string{}
	if value := strings.TrimSpace(r.PendingNotifyChannelsJSON.String); value != "" {
		if err := json.Unmarshal([]byte(value), &pendingChannels); err != nil {
			return types.VPSSubscription{}, fmt.Errorf("decode pending notification channels for %s: %w", r.ID, err)
		}
	}
	var hist []map[string]interface{}
	if value := strings.TrimSpace(r.HistoryJSON.String); value != "" {
		if err := json.Unmarshal([]byte(value), &hist); err != nil {
			return types.VPSSubscription{}, fmt.Errorf("decode history for %s: %w", r.ID, err)
		}
	}
	if hist == nil {
		hist = []map[string]interface{}{}
	}
	if pendingChannels == nil { pendingChannels = map[string][]string{} }
	return types.VPSSubscription{
		ID:                 r.ID,
		PlanCode:           r.PlanCode,
		OvhSubsidiary:      r.OvhSubsidiary,
		Datacenters:        dcs,
		MonitorLinux:       r.MonitorLinux == 1,
		MonitorWindows:     r.MonitorWindows == 1,
		NotifyAvailable:    r.NotifyAvailable == 1,
		NotifyUnavailable:  r.NotifyUnavailable == 1,
		LastStatus:         last,
		PendingNotify:      pending,
		PendingNotifyChannels: pendingChannels,
		History:            hist,
		CreatedAt:          r.CreatedAt,
	}, nil
}

func vpsSubToRow(s types.VPSSubscription) (vpsSubRow, error) {
	if s.Datacenters == nil {
		s.Datacenters = []string{}
	}
	if s.LastStatus == nil {
		s.LastStatus = map[string]string{}
	}
	if s.History == nil {
		s.History = []map[string]interface{}{}
	}
	if s.PendingNotify == nil {
		s.PendingNotify = map[string]string{}
	}
	if s.PendingNotifyChannels == nil {
		s.PendingNotifyChannels = map[string][]string{}
	}
	dcsJSON, err := json.Marshal(s.Datacenters)
	if err != nil {
		return vpsSubRow{}, fmt.Errorf("encode datacenters for %s: %w", s.ID, err)
	}
	lastJSON, err := json.Marshal(s.LastStatus)
	if err != nil {
		return vpsSubRow{}, fmt.Errorf("encode last status for %s: %w", s.ID, err)
	}
	pendingJSON, err := json.Marshal(s.PendingNotify)
	if err != nil {
		return vpsSubRow{}, fmt.Errorf("encode pending notifications for %s: %w", s.ID, err)
	}
	pendingChannelsJSON, err := json.Marshal(s.PendingNotifyChannels)
	if err != nil {
		return vpsSubRow{}, fmt.Errorf("encode pending notification channels for %s: %w", s.ID, err)
	}
	histJSON, err := json.Marshal(s.History)
	if err != nil {
		return vpsSubRow{}, fmt.Errorf("encode history for %s: %w", s.ID, err)
	}
	bi := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	return vpsSubRow{
		ID:                 s.ID,
		PlanCode:           s.PlanCode,
		OvhSubsidiary:      s.OvhSubsidiary,
		DatacentersJSON:    sql.NullString{String: string(dcsJSON), Valid: true},
		MonitorLinux:       bi(s.MonitorLinux),
		MonitorWindows:     bi(s.MonitorWindows),
		NotifyAvailable:    bi(s.NotifyAvailable),
		NotifyUnavailable:  bi(s.NotifyUnavailable),
		LastStatusJSON:     sql.NullString{String: string(lastJSON), Valid: true},
		HistoryJSON:        sql.NullString{String: string(histJSON), Valid: true},
		PendingNotifyJSON:  sql.NullString{String: string(pendingJSON), Valid: true},
		PendingNotifyChannelsJSON: sql.NullString{String: string(pendingChannelsJSON), Valid: true},
		CreatedAt:          s.CreatedAt,
		AutoOrderAccountID: "",
	}, nil
}

// ListVPSSubscriptions 取全部 VPS 订阅
func (db *DB) ListVPSSubscriptions() ([]types.VPSSubscription, error) {
	var rows []vpsSubRow
	if err := db.Select(&rows, `
		SELECT id, plan_code, ovh_subsidiary, datacenters, monitor_linux, monitor_windows,
		       notify_available, notify_unavailable, last_status, history, created_at,
		       pending_notify, pending_notify_channels, auto_order_account_id
		FROM vps_subscriptions
		ORDER BY created_at
	`); err != nil {
		return nil, fmt.Errorf("list vps subs: %w", err)
	}
	out := make([]types.VPSSubscription, 0, len(rows))
	for _, r := range rows {
		sub, err := rowToVPSSub(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, nil
}

// UpsertVPSSubscription 按 id upsert
func (db *DB) UpsertVPSSubscription(s types.VPSSubscription) error {
	r, err := vpsSubToRow(s)
	if err != nil {
		return err
	}
	_, err = db.NamedExec(`
		INSERT INTO vps_subscriptions
		(id, plan_code, ovh_subsidiary, datacenters, monitor_linux, monitor_windows,
		 notify_available, notify_unavailable, last_status, pending_notify, pending_notify_channels, history, created_at, auto_order_account_id)
		VALUES
		(:id, :plan_code, :ovh_subsidiary, :datacenters, :monitor_linux, :monitor_windows,
		 :notify_available, :notify_unavailable, :last_status, :pending_notify, :pending_notify_channels, :history, :created_at, :auto_order_account_id)
		ON CONFLICT(id) DO UPDATE SET
		  plan_code          = excluded.plan_code,
		  ovh_subsidiary     = excluded.ovh_subsidiary,
		  datacenters        = excluded.datacenters,
		  monitor_linux      = excluded.monitor_linux,
		  monitor_windows    = excluded.monitor_windows,
		  notify_available   = excluded.notify_available,
		  notify_unavailable = excluded.notify_unavailable,
		  last_status            = excluded.last_status,
		  pending_notify         = excluded.pending_notify,
		  pending_notify_channels = excluded.pending_notify_channels,
		  history                = excluded.history,
		  auto_order_account_id  = excluded.auto_order_account_id
	`, r)
	if err != nil {
		return fmt.Errorf("upsert vps sub %s: %w", s.ID, err)
	}
	return nil
}

// ReplaceVPSSubscriptions 全表覆盖
func (db *DB) ReplaceVPSSubscriptions(subs []types.VPSSubscription) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM vps_subscriptions`); err != nil {
		return err
	}
	for _, s := range subs {
		r, err := vpsSubToRow(s)
		if err != nil {
			return err
		}
		_, err = tx.NamedExec(`
			INSERT INTO vps_subscriptions
			(id, plan_code, ovh_subsidiary, datacenters, monitor_linux, monitor_windows,
			 notify_available, notify_unavailable, last_status, pending_notify, pending_notify_channels, history, created_at, auto_order_account_id)
			VALUES
			(:id, :plan_code, :ovh_subsidiary, :datacenters, :monitor_linux, :monitor_windows,
			 :notify_available, :notify_unavailable, :last_status, :pending_notify, :pending_notify_channels, :history, :created_at, :auto_order_account_id)
		`, r)
		if err != nil {
			return fmt.Errorf("insert vps sub %s: %w", s.ID, err)
		}
	}
	return tx.Commit()
}

// DeleteVPSSubscription 按 id 删
func (db *DB) DeleteVPSSubscription(id string) error {
	_, err := db.Exec(`DELETE FROM vps_subscriptions WHERE id = ?`, id)
	return err
}

// ClearVPSSubscriptions 清空
func (db *DB) ClearVPSSubscriptions() (int64, error) {
	res, err := db.Exec(`DELETE FROM vps_subscriptions`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
