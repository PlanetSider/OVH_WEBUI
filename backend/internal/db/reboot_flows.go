package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// BotRebootFlowTTL 是账号选择、服务器选择和重启确认卡片的有效期。
const BotRebootFlowTTL = 10 * time.Minute

// BotRebootFlowRow 保存机器人重启流程。Payload 由上层以 JSON 管理。
type BotRebootFlowRow struct {
	ID        string  `db:"id"`
	Channel   string  `db:"channel"`
	ActorID   string  `db:"actor_id"`
	ChatID    string  `db:"chat_id"`
	Stage     string  `db:"stage"`
	Payload   string  `db:"payload"`
	CreatedAt float64 `db:"created_at"`
	UpdatedAt float64 `db:"updated_at"`
}

func (db *DB) CreateBotRebootFlow(row BotRebootFlowRow) error {
	row.ID = strings.TrimSpace(row.ID)
	row.Channel = strings.TrimSpace(row.Channel)
	row.ActorID = strings.TrimSpace(row.ActorID)
	row.ChatID = strings.TrimSpace(row.ChatID)
	row.Stage = strings.TrimSpace(row.Stage)
	if row.ID == "" || row.Channel == "" || row.ActorID == "" || row.ChatID == "" || row.Stage == "" {
		return fmt.Errorf("invalid bot reboot flow")
	}
	if row.Payload == "" {
		row.Payload = "{}"
	}
	now := float64(time.Now().Unix())
	if row.CreatedAt <= 0 {
		row.CreatedAt = now
	}
	if row.UpdatedAt <= 0 {
		row.UpdatedAt = row.CreatedAt
	}
	_, err := db.Exec(
		`INSERT INTO bot_reboot_flows (id, channel, actor_id, chat_id, stage, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Channel, row.ActorID, row.ChatID, row.Stage, row.Payload, row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create bot reboot flow: %w", err)
	}
	return nil
}

// GetBotRebootFlow 只返回仍在有效期内、且属于当前渠道/操作者/会话的流程。
func (db *DB) GetBotRebootFlow(id, channel, actorID, chatID string) (BotRebootFlowRow, bool, error) {
	var row BotRebootFlowRow
	notBefore := float64(time.Now().Add(-BotRebootFlowTTL).Unix())
	err := db.Get(&row,
		`SELECT id, channel, actor_id, chat_id, stage, payload, created_at, updated_at
		 FROM bot_reboot_flows
		 WHERE id = ? AND channel = ? AND actor_id = ? AND chat_id = ? AND created_at >= ?`,
		id, channel, actorID, chatID, notBefore,
	)
	if err == sql.ErrNoRows {
		return row, false, nil
	}
	if err != nil {
		return row, false, fmt.Errorf("get bot reboot flow: %w", err)
	}
	return row, true, nil
}

// TransitionBotRebootFlow 原子推进流程阶段。同一确认卡片被并发或重复点击时，
// 只有第一次能从 confirm 推进到 done。
func (db *DB) TransitionBotRebootFlow(id, channel, actorID, chatID, expectedStage, nextStage, payload string) (bool, error) {
	if payload == "" {
		payload = "{}"
	}
	now := float64(time.Now().Unix())
	notBefore := float64(time.Now().Add(-BotRebootFlowTTL).Unix())
	res, err := db.Exec(
		`UPDATE bot_reboot_flows
		 SET stage = ?, payload = ?, updated_at = ?
		 WHERE id = ? AND channel = ? AND actor_id = ? AND chat_id = ?
		   AND stage = ? AND created_at >= ?`,
		nextStage, payload, now, id, channel, actorID, chatID, expectedStage, notBefore,
	)
	if err != nil {
		return false, fmt.Errorf("transition bot reboot flow: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (db *DB) DeleteExpiredBotRebootFlows(beforeUnix float64) (int64, error) {
	res, err := db.Exec(`DELETE FROM bot_reboot_flows WHERE created_at < ?`, beforeUnix)
	if err != nil {
		return 0, fmt.Errorf("delete expired bot reboot flows: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
