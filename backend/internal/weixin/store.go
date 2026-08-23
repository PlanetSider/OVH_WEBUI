package weixin

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/ovh-webui/server/internal/db"
)

type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) LoadCredentials() (Credentials, bool, error) {
	var credentials Credentials
	err := s.db.Get(&credentials, `SELECT account_id, bot_token, base_url, user_id, updated_at FROM weixin_credentials WHERE id = 1`)
	if err == sql.ErrNoRows {
		return Credentials{}, false, nil
	}
	if err != nil {
		return Credentials{}, false, fmt.Errorf("load weixin credentials: %w", err)
	}
	return credentials, true, nil
}

func (s *Store) SaveCredentials(credentials Credentials) error {
	if credentials.UpdatedAt == 0 {
		credentials.UpdatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(`INSERT INTO weixin_credentials(id, account_id, bot_token, base_url, user_id, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id, bot_token=excluded.bot_token,
		base_url=excluded.base_url, user_id=excluded.user_id, updated_at=excluded.updated_at`,
		credentials.AccountID, credentials.Token, credentials.BaseURL, credentials.UserID, credentials.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save weixin credentials: %w", err)
	}
	return nil
}

func (s *Store) DeleteAll() error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DELETE FROM weixin_credentials`,
		`DELETE FROM weixin_sync_state`,
		`DELETE FROM weixin_context_tokens`,
		`DELETE FROM weixin_seen_messages`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("clear weixin state: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) LoadSyncBuf(accountID string) (string, error) {
	var value string
	err := s.db.Get(&value, `SELECT sync_buf FROM weixin_sync_state WHERE account_id = ?`, accountID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load weixin sync cursor: %w", err)
	}
	return value, nil
}

func (s *Store) SaveSyncBuf(accountID, value string) error {
	_, err := s.db.Exec(`INSERT INTO weixin_sync_state(account_id, sync_buf, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET sync_buf=excluded.sync_buf, updated_at=excluded.updated_at`,
		accountID, value, time.Now().UnixMilli())
	return err
}

func (s *Store) ContextToken(accountID, userID string) (string, error) {
	var token string
	err := s.db.Get(&token, `SELECT context_token FROM weixin_context_tokens WHERE account_id = ? AND user_id = ?`, accountID, userID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return token, err
}

func (s *Store) SaveContextToken(accountID, userID, token string) error {
	_, err := s.db.Exec(`INSERT INTO weixin_context_tokens(account_id, user_id, context_token, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(account_id, user_id) DO UPDATE SET context_token=excluded.context_token, updated_at=excluded.updated_at`,
		accountID, userID, token, time.Now().UnixMilli())
	return err
}

func (s *Store) DeleteContextToken(accountID, userID string) error {
	_, err := s.db.Exec(`DELETE FROM weixin_context_tokens WHERE account_id = ? AND user_id = ?`, accountID, userID)
	return err
}

func (s *Store) MarkSeen(key string, now time.Time, ttl time.Duration) (bool, error) {
	cutoff := now.Add(-ttl).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM weixin_seen_messages WHERE seen_at < ?`, cutoff); err != nil {
		return false, err
	}
	result, err := s.db.Exec(`INSERT OR IGNORE INTO weixin_seen_messages(message_key, seen_at) VALUES(?, ?)`, key, now.UnixMilli())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 0, err
}

func (s *Store) AcquireLease(name, owner string, now time.Time, ttl time.Duration) (bool, error) {
	result, err := s.db.Exec(`INSERT INTO weixin_runtime_locks(lock_name, owner_id, expires_at) VALUES(?, ?, ?)
		ON CONFLICT(lock_name) DO UPDATE SET owner_id=excluded.owner_id, expires_at=excluded.expires_at
		WHERE weixin_runtime_locks.expires_at < ? OR weixin_runtime_locks.owner_id = ?`,
		name, owner, now.Add(ttl).UnixMilli(), now.UnixMilli(), owner)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) ReleaseLease(name, owner string) error {
	_, err := s.db.Exec(`DELETE FROM weixin_runtime_locks WHERE lock_name = ? AND owner_id = ?`, name, owner)
	return err
}
