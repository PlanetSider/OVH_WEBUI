package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/ovh-webui/server/internal/types"
)

// ErrUnresolvedCheckoutAttempts 表示账户仍有关联的 checkout 结果待人工核对。
// 此时删除凭据会让后续核单更困难，也会破坏防重复下单审计链。
var ErrUnresolvedCheckoutAttempts = errors.New("账户存在结果未确认的结账记录，暂不能删除")

// ErrAccountNotFound 表示目标账户在数据库中不存在。调用方可据此将
// 并发删除/更新导致的不存在情况稳定映射为 404，而不是误报为服务器故障。
var ErrAccountNotFound = errors.New("账户不存在")

type accountRow struct {
	ID          string `db:"id"`
	Name        string `db:"name"`
	Endpoint    string `db:"endpoint"`
	Zone        string `db:"zone"`
	AppKey      string `db:"app_key"`
	AppSecret   string `db:"app_secret"`
	ConsumerKey string `db:"consumer_key"`
	IAM         string `db:"iam"`
	IsDefault   int    `db:"is_default"`
	CreatedAt   string `db:"created_at"`
}

func rowToAccount(r accountRow) types.OVHAccount {
	return types.OVHAccount{
		ID:          r.ID,
		Name:        r.Name,
		Endpoint:    r.Endpoint,
		Zone:        r.Zone,
		AppKey:      r.AppKey,
		AppSecret:   r.AppSecret,
		ConsumerKey: r.ConsumerKey,
		IAM:         r.IAM,
		IsDefault:   r.IsDefault == 1,
		CreatedAt:   r.CreatedAt,
	}
}

func accountToRow(a types.OVHAccount) accountRow {
	bi := 0
	if a.IsDefault {
		bi = 1
	}
	return accountRow{
		ID:          a.ID,
		Name:        a.Name,
		Endpoint:    a.Endpoint,
		Zone:        a.Zone,
		AppKey:      a.AppKey,
		AppSecret:   a.AppSecret,
		ConsumerKey: a.ConsumerKey,
		IAM:         a.IAM,
		IsDefault:   bi,
		CreatedAt:   a.CreatedAt,
	}
}

// ListAccounts 取全部 OVH 账户,默认账户排最前,然后按创建时间
func (db *DB) ListAccounts() ([]types.OVHAccount, error) {
	var rows []accountRow
	if err := db.Select(&rows, `SELECT * FROM ovh_accounts ORDER BY is_default DESC, created_at`); err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	out := make([]types.OVHAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToAccount(r))
	}
	return out, nil
}

// GetAccount 按 id 取单条;不存在时返回 (zero, false, nil)
func (db *DB) GetAccount(id string) (types.OVHAccount, bool, error) {
	var r accountRow
	err := db.Get(&r, `SELECT * FROM ovh_accounts WHERE id = ?`, id)
	if err == sql.ErrNoRows {
		return types.OVHAccount{}, false, nil
	}
	if err != nil {
		return types.OVHAccount{}, false, fmt.Errorf("get account %s: %w", id, err)
	}
	return rowToAccount(r), true, nil
}

// GetDefaultAccount 取当前默认账户;无默认时返回 (zero, false, nil)
func (db *DB) GetDefaultAccount() (types.OVHAccount, bool, error) {
	var r accountRow
	err := db.Get(&r, `SELECT * FROM ovh_accounts WHERE is_default = 1 LIMIT 1`)
	if err == sql.ErrNoRows {
		return types.OVHAccount{}, false, nil
	}
	if err != nil {
		return types.OVHAccount{}, false, fmt.Errorf("get default account: %w", err)
	}
	return rowToAccount(r), true, nil
}

// CountAccounts 当前有多少账户
func (db *DB) CountAccounts() (int, error) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM ovh_accounts`); err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return n, nil
}

// UpsertAccount 插入或更新账户;若 is_default=1 则会把其它账户的 is_default 清 0(只有一个默认)
func (db *DB) UpsertAccount(a types.OVHAccount) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if a.IsDefault {
		if _, err := tx.Exec(`UPDATE ovh_accounts SET is_default = 0 WHERE id != ?`, a.ID); err != nil {
			return fmt.Errorf("clear other defaults: %w", err)
		}
	}
	r := accountToRow(a)
	_, err = tx.NamedExec(`
		INSERT INTO ovh_accounts
		(id, name, endpoint, zone, app_key, app_secret, consumer_key, iam, is_default, created_at)
		VALUES
		(:id, :name, :endpoint, :zone, :app_key, :app_secret, :consumer_key, :iam, :is_default, :created_at)
		ON CONFLICT(id) DO UPDATE SET
		  name         = excluded.name,
		  endpoint     = excluded.endpoint,
		  zone         = excluded.zone,
		  app_key      = excluded.app_key,
		  app_secret   = excluded.app_secret,
		  consumer_key = excluded.consumer_key,
		  iam          = excluded.iam,
		  is_default   = excluded.is_default
	`, r)
	if err != nil {
		return fmt.Errorf("upsert account %s: %w", a.ID, err)
	}
	return tx.Commit()
}

// SetDefaultAccount 把指定账户标为默认,其他清 0
func (db *DB) SetDefaultAccount(id string) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE ovh_accounts SET is_default = 0`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE ovh_accounts SET is_default = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrAccountNotFound, id)
	}
	return tx.Commit()
}

// ClearDefaultAccount 清除默认账户标记。正常业务始终应保留一个默认账户；
// 该方法仅用于账户变更发布失败时，把数据库准确回滚到“变更前没有默认账户”
// 的异常旧状态，避免为了回滚而擅自选择另一账户。
func (db *DB) ClearDefaultAccount() error {
	if _, err := db.Exec(`UPDATE ovh_accounts SET is_default = 0`); err != nil {
		return fmt.Errorf("clear default account: %w", err)
	}
	return nil
}

// DeleteAccount 级联删除:queue / history / config_sniper_tasks 全部清掉关联记录,
// monitor_subscriptions / vps_subscriptions 的 auto_order_account_id 清空。
// 删默认账户后若还有其他账户,选 created_at 最早的那个补为默认。
func (db *DB) DeleteAccount(id string) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 是不是默认账户?删完要补一个新默认
	var wasDefault int
	if err := tx.Get(&wasDefault, `SELECT is_default FROM ovh_accounts WHERE id = ?`, id); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: %s", ErrAccountNotFound, id)
		}
		return err
	}

	var unresolvedAttempts int
	if err := tx.Get(&unresolvedAttempts, `SELECT COUNT(*) FROM checkout_attempts WHERE account_id = ? AND order_id = ''`, id); err != nil {
		return fmt.Errorf("count unresolved checkout attempts: %w", err)
	}
	if unresolvedAttempts > 0 {
		return fmt.Errorf("%w（%d 条）；请先在 OVH 后台核对订单和购物车", ErrUnresolvedCheckoutAttempts, unresolvedAttempts)
	}

	if _, err := tx.Exec(`DELETE FROM history WHERE account_id = ?`, id); err != nil {
		return fmt.Errorf("cascade delete history: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM queue WHERE account_id = ?`, id); err != nil {
		return fmt.Errorf("cascade delete queue: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE monitor_subscriptions SET auto_order_account_id = '' WHERE auto_order_account_id = ?`, id,
	); err != nil {
		return fmt.Errorf("clear monitor auto_order_account_id: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE vps_subscriptions SET auto_order_account_id = '' WHERE auto_order_account_id = ?`, id,
	); err != nil {
		return fmt.Errorf("clear vps auto_order_account_id: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM server_aliases WHERE account_id = ?`, id); err != nil {
		return fmt.Errorf("cascade delete server_aliases: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM ovh_accounts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	// 删的是默认账户 + 还有其他账户存活 → 选最早一个补默认
	if wasDefault == 1 {
		var newDefaultID sql.NullString
		err := tx.Get(&newDefaultID,
			`SELECT id FROM ovh_accounts ORDER BY created_at LIMIT 1`)
		if err == nil && newDefaultID.Valid {
			if _, err := tx.Exec(
				`UPDATE ovh_accounts SET is_default = 1 WHERE id = ?`, newDefaultID.String,
			); err != nil {
				return err
			}
		}
		// sql.ErrNoRows 说明已经没账户了,这是正常的;什么也不做
	}

	return tx.Commit()
}
