package config

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/secret"
	"github.com/ovh-webui/server/internal/types"
)

// Store 配置存取（线程安全，对应 Python 全局 config dict）
type Store struct {
	mu      sync.RWMutex
	cfg     types.Config
	db      *db.DB
	cipher  *secret.Cipher
	loadErr error
}

const kvConfigKey = "config"

// New 从 SQLite kv 表加载配置；不存在则使用默认值。保留旧签名供测试和
// 无密钥兼容调用使用；主程序应使用 NewWithCipher，才能明确处理密钥错误。
func New(database *db.DB) *Store {
	store, err := NewWithCipher(database, nil)
	if err != nil {
		if store == nil {
			store = &Store{db: database, cfg: types.DefaultConfig()}
		}
		store.loadErr = err
		store.cfg = types.DefaultConfig()
	}
	return store
}

func NewWithCipher(database *db.DB, cipher *secret.Cipher) (*Store, error) {
	s := &Store{cfg: types.DefaultConfig(), db: database, cipher: cipher}
	var loaded types.Config
	ok, err := database.GetKV(kvConfigKey, &loaded)
	if err != nil {
		return s, err
	}
	if ok {
		if cipher == nil {
			if secret.ContainsEncryptedConfig(loaded) {
				return s, fmt.Errorf("encrypted config requires a database key")
			}
			s.cfg = loaded
		} else {
			decrypted, _, decryptErr := cipher.DecryptConfig(loaded)
			if decryptErr != nil {
				return s, fmt.Errorf("decrypt config: %w", decryptErr)
			}
			s.cfg = decrypted
			encoded, encodeErr := cipher.EncryptConfig(decrypted)
			if encodeErr != nil {
				return s, fmt.Errorf("encrypt config migration: %w", encodeErr)
			}
			if secret.ConfigNeedsEncryption(loaded) {
				if err := database.SetKV(kvConfigKey, encoded); err != nil {
					return s, fmt.Errorf("persist encrypted config migration: %w", err)
				}
			}
		}
	}
	s.applyDefaults()
	return s, nil
}

func (s *Store) applyDefaults() {
	if s.cfg.Endpoint == "" {
		s.cfg.Endpoint = "ovh-eu"
	}
	if s.cfg.Zone == "" {
		s.cfg.Zone = "IE"
	}
	if s.cfg.IAM == "" {
		s.cfg.IAM = "go-ovh-" + strings.ToLower(s.cfg.Zone)
	}
	if s.cfg.FeishuConnectionMode == "" {
		s.cfg.FeishuConnectionMode = "long_connection"
	}
	// 旧版本配置没有通知开关字段，缺失/为 null 时默认开启；只有显式 false 才关闭。
	if s.cfg.TgNotificationsEnabled == nil {
		v := true
		s.cfg.TgNotificationsEnabled = &v
	}
	if s.cfg.FeishuNotificationsEnabled == nil {
		v := true
		s.cfg.FeishuNotificationsEnabled = &v
	}
	if s.cfg.WeixinNotificationsEnabled == nil {
		v := true
		s.cfg.WeixinNotificationsEnabled = &v
	}
}

func (s *Store) LoadError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

// Get 返回配置的副本（调用方不能修改后影响存储）
func (s *Store) Get() types.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Set 覆盖整个配置并落盘。先加密并提交数据库，再发布内存快照，避免
// 数据库写失败时运行状态与持久化状态分叉。
func (s *Store) Set(c types.Config) error {
	s.mu.RLock()
	loadErr := s.loadErr
	cipher := s.cipher
	s.mu.RUnlock()
	if loadErr != nil {
		return fmt.Errorf("config load is unsafe: %w", loadErr)
	}
	if c.IAM == "" {
		c.IAM = "go-ovh-" + strings.ToLower(c.Zone)
	}
	if c.FeishuConnectionMode == "" {
		c.FeishuConnectionMode = "long_connection"
	}
	encoded := c
	var err error
	if cipher != nil {
		encoded, err = cipher.EncryptConfig(c)
		if err != nil {
			return fmt.Errorf("encrypt config: %w", err)
		}
	}
	if err := s.db.SetKV(kvConfigKey, encoded); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.loadErr = nil
	s.mu.Unlock()
	return nil
}

// HasCredentials 判断是否已配置 OVH 凭据
func (s *Store) HasCredentials() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.AppKey != "" && s.cfg.AppSecret != "" && s.cfg.ConsumerKey != ""
}

// APIBaseURL 根据 endpoint 返回 OVH REST API base URL
func (s *Store) APIBaseURL() string {
	s.mu.RLock()
	ep := s.cfg.Endpoint
	s.mu.RUnlock()
	switch ep {
	case "ovh-us":
		return "https://api.us.ovhcloud.com"
	case "ovh-ca":
		return "https://ca.api.ovh.com"
	default:
		return "https://eu.api.ovh.com"
	}
}
