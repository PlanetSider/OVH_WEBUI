package db

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/ovh-webui/server/internal/types"
)

type notificationOutboxRow struct {
	ID               string `db:"id"`
	EventKey         string `db:"event_key"`
	Kind             string `db:"kind"`
	Payload          string `db:"payload"`
	ChannelsJSON     string `db:"channels"`
	AwaitingChannels int    `db:"awaiting_channels"`
	CreatedAt        string `db:"created_at"`
	UpdatedAt        string `db:"updated_at"`
}

func normalizeOutboxEntry(entry types.NotificationOutboxEntry) (types.NotificationOutboxEntry, error) {
	entry.EventKey = strings.TrimSpace(entry.EventKey)
	entry.Kind = strings.TrimSpace(entry.Kind)
	if entry.EventKey == "" || entry.Kind == "" {
		return entry, fmt.Errorf("notification outbox requires event key and kind")
	}
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Channels == nil {
		entry.Channels = []string{}
	}
	// 统一排序，保证条件更新不会因 JSON 数组顺序不同而误判并发冲突。
	seen := make(map[string]struct{}, len(entry.Channels))
	channels := make([]string, 0, len(entry.Channels))
	for _, channel := range entry.Channels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if channel == "" {
			continue
		}
		if _, ok := seen[channel]; ok {
			continue
		}
		seen[channel] = struct{}{}
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	entry.Channels = channels
	// AwaitingChannels 只对空目标快照有意义；一旦渠道已分配，清除该状态，
	// 避免调用方忘记同步布尔值导致事件被重复分配。
	if len(channels) > 0 {
		entry.AwaitingChannels = false
	}
	now := types.NowISO()
	if entry.CreatedAt == "" {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	return entry, nil
}

func outboxToRow(entry types.NotificationOutboxEntry) (notificationOutboxRow, error) {
	entry, err := normalizeOutboxEntry(entry)
	if err != nil {
		return notificationOutboxRow{}, err
	}
	channels, err := json.Marshal(entry.Channels)
	if err != nil {
		return notificationOutboxRow{}, fmt.Errorf("encode notification channels: %w", err)
	}
	return notificationOutboxRow{
		ID: entry.ID, EventKey: entry.EventKey, Kind: entry.Kind, Payload: entry.Payload,
		ChannelsJSON: string(channels), AwaitingChannels: boolInt(entry.AwaitingChannels),
		CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
	}, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rowToOutbox(row notificationOutboxRow) (types.NotificationOutboxEntry, error) {
	channels := []string{}
	decodeError := ""
	if strings.TrimSpace(row.ChannelsJSON) != "" {
		if err := json.Unmarshal([]byte(row.ChannelsJSON), &channels); err != nil {
			decodeError = fmt.Sprintf("decode notification channels for %s: %v", row.EventKey, err)
			channels = []string{}
		}
	}
	if channels == nil {
		channels = []string{}
	}
	return types.NotificationOutboxEntry{
		ID: row.ID, EventKey: row.EventKey, Kind: row.Kind, Payload: row.Payload,
		Channels: channels, AwaitingChannels: row.AwaitingChannels == 1,
		DecodeError: decodeError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func insertNotificationOutboxTx(tx *sqlx.Tx, entry types.NotificationOutboxEntry) error {
	row, err := outboxToRow(entry)
	if err != nil {
		return err
	}
	_, err = tx.NamedExec(`
		INSERT INTO notification_outbox(id, event_key, kind, payload, channels, awaiting_channels, created_at, updated_at)
		VALUES(:id, :event_key, :kind, :payload, :channels, :awaiting_channels, :created_at, :updated_at)
		ON CONFLICT(event_key) DO NOTHING
	`, row)
	if err != nil {
		return fmt.Errorf("insert notification outbox %s: %w", entry.EventKey, err)
	}
	return nil
}

func (db *DB) EnqueueNotification(entry types.NotificationOutboxEntry) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertNotificationOutboxTx(tx, entry); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) ListNotificationOutbox(limit int) ([]types.NotificationOutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []notificationOutboxRow
	if err := db.Select(&rows, `
		SELECT id, event_key, kind, payload, channels, awaiting_channels, created_at, updated_at
		FROM notification_outbox ORDER BY created_at, id LIMIT ?
	`, limit); err != nil {
		return nil, fmt.Errorf("list notification outbox: %w", err)
	}
	out := make([]types.NotificationOutboxEntry, 0, len(rows))
	for _, row := range rows {
		entry, _ := rowToOutbox(row)
		out = append(out, entry)
	}
	return out, nil
}

// QuarantineNotification 把确认无法投递的事件原子移入死信表。原始字段
// 完整保留，避免静默丢失；按 id 条件删除也不会误伤并发更新后的其它事件。
func (db *DB) QuarantineNotification(id, reason string) (bool, error) {
	id = strings.TrimSpace(id)
	reason = strings.TrimSpace(reason)
	if id == "" || reason == "" {
		return false, fmt.Errorf("quarantine notification requires id and reason")
	}
	tx, err := db.Beginx()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := types.NowISO()
	res, err := tx.Exec(`
		INSERT INTO notification_dead_letters(
			id, event_key, kind, payload, channels, awaiting_channels, error, created_at, updated_at, failed_at
		)
		SELECT id, event_key, kind, payload, channels, awaiting_channels, ?, created_at, updated_at, ?
		FROM notification_outbox WHERE id = ?
		ON CONFLICT(id) DO UPDATE SET
			event_key = excluded.event_key,
			kind = excluded.kind,
			payload = excluded.payload,
			channels = excluded.channels,
			awaiting_channels = excluded.awaiting_channels,
			error = excluded.error,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			failed_at = excluded.failed_at
	`, reason, now, id)
	if err != nil {
		return false, fmt.Errorf("quarantine notification %s: %w", id, err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check quarantined notification %s: %w", id, err)
	}
	if inserted < 1 {
		return false, nil
	}
	if _, err := tx.Exec(`DELETE FROM notification_outbox WHERE id = ?`, id); err != nil {
		return false, fmt.Errorf("remove quarantined notification %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit quarantined notification %s: %w", id, err)
	}
	return true, nil
}

// AssignNotificationChannels 把尚未确定接收端的事件原子转换为普通待发送事件。
// 进程在抢购成功恢复后可能还没有完成飞书/微信初始化，因此必须先持久化
// 事件，等至少一个接收端真正可用时再冻结目标渠道。
func (db *DB) AssignNotificationChannels(id string, channels []string) (bool, error) {
	normalized, err := normalizeOutboxEntry(types.NotificationOutboxEntry{EventKey: "assign", Kind: "assign", Channels: channels})
	if err != nil {
		return false, err
	}
	if len(normalized.Channels) == 0 {
		return false, nil
	}
	channelsJSON, err := json.Marshal(normalized.Channels)
	if err != nil {
		return false, err
	}
	res, err := db.Exec(`
		UPDATE notification_outbox
		SET channels = ?, awaiting_channels = 0, updated_at = ?
		WHERE id = ? AND awaiting_channels = 1
	`, string(channelsJSON), types.NowISO(), id)
	if err != nil {
		return false, fmt.Errorf("assign notification channels %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteAwaitingNotification 删除一个仍未冻结目标渠道的事件。正常分发流程会
// 保留 awaiting 事件，直到将来至少一个渠道可用；此方法仅供显式管理或迁移
// 使用，避免空 channels 事件被普通进度更新误删。
func (db *DB) DeleteAwaitingNotification(id string) (bool, error) {
	res, err := db.Exec(`DELETE FROM notification_outbox WHERE id = ? AND awaiting_channels = 1`, id)
	if err != nil {
		return false, fmt.Errorf("delete awaiting notification %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UpdateNotificationChannels 只在渠道快照仍与调用方读取的一致时更新，避免并发发送者覆盖彼此。
func (db *DB) UpdateNotificationChannels(id string, expected, remaining []string) (bool, error) {
	normalizedExpected, err := normalizeOutboxEntry(types.NotificationOutboxEntry{EventKey: "update", Kind: "update", Channels: expected})
	if err != nil {
		return false, err
	}
	normalizedRemaining, err := normalizeOutboxEntry(types.NotificationOutboxEntry{EventKey: "update", Kind: "update", Channels: remaining})
	if err != nil {
		return false, err
	}
	expectedJSON, err := json.Marshal(normalizedExpected.Channels)
	if err != nil {
		return false, err
	}
	if len(normalizedRemaining.Channels) == 0 {
		res, err := db.Exec(`DELETE FROM notification_outbox WHERE id = ? AND channels = ? AND awaiting_channels = 0`, id, string(expectedJSON))
		if err != nil {
			return false, fmt.Errorf("delete notification outbox %s: %w", id, err)
		}
		n, _ := res.RowsAffected()
		return n == 1, nil
	}
	remainingJSON, err := json.Marshal(normalizedRemaining.Channels)
	if err != nil {
		return false, err
	}
	res, err := db.Exec(`
		UPDATE notification_outbox SET channels = ?, updated_at = ? WHERE id = ? AND channels = ? AND awaiting_channels = 0
	`, string(remainingJSON), types.NowISO(), id, string(expectedJSON))
	if err != nil {
		return false, fmt.Errorf("update notification outbox %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
