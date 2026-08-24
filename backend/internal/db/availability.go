package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

const availabilityRetention = 7 * 24 * time.Hour

type AvailabilitySnapshot struct {
	Region    string `db:"region"`
	FetchedAt int64  `db:"fetched_at"`
	ItemCount int    `db:"item_count"`
	Data      string `db:"data"`
}

type ServerPlanSnapshot struct {
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

type PreaddedServerResult struct {
	Region     string `db:"region"`
	PlanCode   string `db:"plan_code"`
	ComparedAt int64  `db:"compared_at"`
	Data       string `db:"data"`
}

type PreaddedServerComparison struct {
	Region     string `db:"region"`
	ComparedAt int64  `db:"compared_at"`
	ItemCount  int    `db:"item_count"`
}

type PreaddedServerDatacenter struct {
	Datacenter        string `json:"datacenter"`
	Availability      string `json:"availability"`
	AvailableVariants int    `json:"availableVariants"`
	ReportedVariants  int    `json:"reportedVariants"`
}

type PreaddedServerPageItem struct {
	PlanCode       string                      `json:"planCode"`
	Server         string                      `json:"server"`
	Regions        []string                    `json:"regions"`
	VariantCount   int                         `json:"variantCount"`
	Memories       []string                    `json:"memories"`
	Storages       []string                    `json:"storages"`
	SystemStorages []string                    `json:"systemStorages"`
	Datacenters    []PreaddedServerDatacenter `json:"datacenters"`
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

// LatestServerPlanSnapshot 返回区域最近一次成功保存的服务器目录 planCode 快照。
func (db *DB) LatestServerPlanSnapshot(region string) (ServerPlanSnapshot, bool, error) {
	var row ServerPlanSnapshot
	err := db.Get(&row, `
		SELECT region, fetched_at, item_count, data
		FROM server_plan_snapshots
		WHERE region = ?
		ORDER BY fetched_at DESC
		LIMIT 1`, strings.ToLower(strings.TrimSpace(region)))
	if err != nil {
		if err == sql.ErrNoRows {
			return ServerPlanSnapshot{}, false, nil
		}
		return ServerPlanSnapshot{}, false, err
	}
	return row, true, nil
}

// SaveRealtimeAvailabilityBatch 原子保存同一整点批次的实时可用性、服务器目录 planCode
// 快照和预增服务器结果。任一写入失败时，旧批次和旧比对结果保持不变。
func (db *DB) SaveRealtimeAvailabilityBatch(
	region string,
	fetchedAt time.Time,
	availabilityItems []map[string]interface{},
	serverPlanCodes []string,
	preadded []PreaddedServerPageItem,
) error {
	region = strings.ToLower(strings.TrimSpace(region))
	availabilityRaw, err := json.Marshal(availabilityItems)
	if err != nil {
		return fmt.Errorf("marshal availability batch: %w", err)
	}
	normalizedPlanCodes := normalizePlanCodes(serverPlanCodes)
	serverPlanRaw, err := json.Marshal(normalizedPlanCodes)
	if err != nil {
		return fmt.Errorf("marshal server plan snapshot: %w", err)
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
		region, fetchedMs, len(availabilityItems), string(availabilityRaw)); err != nil {
		return fmt.Errorf("insert availability batch: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO server_plan_snapshots(region, fetched_at, item_count, data)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(region, fetched_at) DO UPDATE SET item_count=excluded.item_count, data=excluded.data`,
		region, fetchedMs, len(normalizedPlanCodes), string(serverPlanRaw)); err != nil {
		return fmt.Errorf("insert server plan snapshot: %w", err)
	}
	if err := replacePreaddedServerResultsTx(tx, region, preadded, fetchedMs); err != nil {
		return err
	}

	cutoff := time.Now().Add(-availabilityRetention).UnixMilli()
	if _, err := tx.Exec(`DELETE FROM availability_snapshots WHERE fetched_at < ?`, cutoff); err != nil {
		return fmt.Errorf("cleanup availability snapshots: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM server_plan_snapshots WHERE fetched_at < ?`, cutoff); err != nil {
		return fmt.Errorf("cleanup server plan snapshots: %w", err)
	}
	return tx.Commit()
}

func normalizePlanCodes(planCodes []string) []string {
	seen := make(map[string]struct{}, len(planCodes))
	for _, planCode := range planCodes {
		planCode = strings.ToLower(strings.TrimSpace(planCode))
		if planCode != "" {
			seen[planCode] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for planCode := range seen {
		result = append(result, planCode)
	}
	sort.Strings(result)
	return result
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

// ReplacePreaddedServerResults 原子替换某区域按 planCode 聚合后的页面结果，
// 并记录该区域最后一次成功比对时间。失败时旧结果和旧时间保持不变。
func (db *DB) ReplacePreaddedServerResults(region string, items []PreaddedServerPageItem, comparedAt time.Time) error {
	region = strings.ToLower(strings.TrimSpace(region))
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replacePreaddedServerResultsTx(tx, region, items, comparedAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func replacePreaddedServerResultsTx(tx *sqlx.Tx, region string, items []PreaddedServerPageItem, comparedAtMs int64) error {
	if _, err := tx.Exec(`DELETE FROM preadded_server_results WHERE region = ?`, region); err != nil {
		return fmt.Errorf("clear preadded server results: %w", err)
	}
	stmt, err := tx.Preparex(`
		INSERT INTO preadded_server_results(region, plan_code, compared_at, search_text, data)
		VALUES(?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range items {
		planCode := strings.TrimSpace(item.PlanCode)
		if planCode == "" {
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal preadded server result %s: %w", planCode, err)
		}
		searchText := strings.ToLower(string(raw))
		if _, err := stmt.Exec(region, strings.ToLower(planCode), comparedAtMs, searchText, string(raw)); err != nil {
			return fmt.Errorf("insert preadded server result %s: %w", planCode, err)
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO preadded_server_comparisons(region, compared_at, item_count)
		VALUES(?, ?, ?)
		ON CONFLICT(region) DO UPDATE SET
			compared_at = excluded.compared_at,
			item_count = excluded.item_count`, region, comparedAtMs, len(items)); err != nil {
		return fmt.Errorf("save preadded comparison status: %w", err)
	}
	return nil
}

// ListPreaddedServerResults 读取已经在后台聚合好的页面结果，搜索先在 SQLite 内过滤。
func (db *DB) ListPreaddedServerResults(region, search string) ([]PreaddedServerResult, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	search = strings.ToLower(strings.TrimSpace(search))
	rows := make([]PreaddedServerResult, 0)
	query := `SELECT region, plan_code, compared_at, data FROM preadded_server_results`
	args := []interface{}{}
	conditions := make([]string, 0, 2)
	if region != "" && region != "all" {
		conditions = append(conditions, `region = ?`)
		args = append(args, region)
	}
	if search != "" {
		conditions = append(conditions, `instr(search_text, ?) > 0`)
		args = append(args, search)
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY plan_code ASC, region ASC`
	if err := db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("list preadded server results: %w", err)
	}
	return rows, nil
}

// ListPreaddedServerComparisons 返回所选区域最后一次成功比对信息。
func (db *DB) ListPreaddedServerComparisons(region string) ([]PreaddedServerComparison, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	rows := make([]PreaddedServerComparison, 0, 2)
	query := `SELECT region, compared_at, item_count FROM preadded_server_comparisons`
	args := []interface{}{}
	if region != "" && region != "all" {
		query += ` WHERE region = ?`
		args = append(args, region)
	}
	query += ` ORDER BY region ASC`
	if err := db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("list preadded comparison status: %w", err)
	}
	return rows, nil
}

func (db *DB) HasPreaddedServerComparison(region string) (bool, error) {
	var count int
	if err := db.Get(&count, `SELECT COUNT(*) FROM preadded_server_comparisons WHERE region = ?`, strings.ToLower(strings.TrimSpace(region))); err != nil {
		return false, err
	}
	return count > 0, nil
}
