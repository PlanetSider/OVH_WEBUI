package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const availabilityRetention = 7 * 24 * time.Hour

type AvailabilitySnapshot struct {
	Region    string `db:"region"`
	FetchedAt int64  `db:"fetched_at"`
	ItemCount int    `db:"item_count"`
	Data      string `db:"data"`
}

type PreaddedServer struct {
	Region     string `json:"region" db:"region"`
	FQN        string `json:"fqn" db:"fqn"`
	PlanCode   string `json:"planCode" db:"plan_code"`
	DetectedAt int64  `json:"detectedAt" db:"detected_at"`
	Data       string `json:"-" db:"data"`
}

// SaveAvailabilitySnapshot 保存区域快照，并删除 7 天以前的快照。
func (db *DB) SaveAvailabilitySnapshot(region string, fetchedAt time.Time, items []map[string]interface{}) error {
	region = strings.ToLower(strings.TrimSpace(region))
	raw, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal availability snapshot: %w", err)
	}
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	fetchedMs := fetchedAt.UnixMilli()
	if _, err := tx.Exec(
		`INSERT INTO availability_snapshots(region, fetched_at, item_count, data)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(region, fetched_at) DO UPDATE SET item_count=excluded.item_count, data=excluded.data`,
		region, fetchedMs, len(items), string(raw),
	); err != nil {
		return fmt.Errorf("insert availability snapshot: %w", err)
	}
	cutoff := time.Now().Add(-availabilityRetention).UnixMilli()
	if _, err := tx.Exec(`DELETE FROM availability_snapshots WHERE fetched_at < ?`, cutoff); err != nil {
		return fmt.Errorf("cleanup availability snapshots: %w", err)
	}
	return tx.Commit()
}

// LatestAvailabilitySnapshot 返回区域最近一次成功保存的快照。
func (db *DB) LatestAvailabilitySnapshot(region string) (AvailabilitySnapshot, bool, error) {
	var row AvailabilitySnapshot
	err := db.Get(&row, `
		SELECT region, fetched_at, item_count, data
		FROM availability_snapshots
		WHERE region = ?
		ORDER BY fetched_at DESC
		LIMIT 1`, strings.ToLower(strings.TrimSpace(region)))
	if err != nil {
		if err == sql.ErrNoRows {
			return AvailabilitySnapshot{}, false, nil
		}
		return AvailabilitySnapshot{}, false, err
	}
	return row, true, nil
}

// ReplacePreaddedServers 按区域替换当前快照计算出的预增服务器。
func (db *DB) ReplacePreaddedServers(region string, items []map[string]interface{}, detectedAt time.Time) error {
	region = strings.ToLower(strings.TrimSpace(region))
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM preadded_servers WHERE region = ?`, region); err != nil {
		return fmt.Errorf("clear preadded servers: %w", err)
	}
	stmt, err := tx.Preparex(`
		INSERT INTO preadded_servers(region, fqn, plan_code, detected_at, data)
		VALUES(?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for index, item := range items {
		planCode, _ := item["planCode"].(string)
		fqn, _ := item["fqn"].(string)
		if strings.TrimSpace(fqn) == "" {
			fqn = fmt.Sprintf("%s#%d", planCode, index)
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal preadded server %s: %w", fqn, err)
		}
		if _, err := stmt.Exec(region, fqn, planCode, detectedAt.UnixMilli(), string(raw)); err != nil {
			return fmt.Errorf("insert preadded server %s: %w", fqn, err)
		}
	}
	return tx.Commit()
}

// ListPreaddedServers 返回预增服务器，region 为空时返回全部区域。
func (db *DB) ListPreaddedServers(region string) ([]PreaddedServer, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	var rows []PreaddedServer
	query := `SELECT region, fqn, plan_code, detected_at, data FROM preadded_servers`
	args := []interface{}{}
	if region != "" && region != "all" {
		query += ` WHERE region = ?`
		args = append(args, region)
	}
	query += ` ORDER BY detected_at DESC, region ASC, plan_code ASC, fqn ASC`
	if err := db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("list preadded servers: %w", err)
	}
	return rows, nil
}
