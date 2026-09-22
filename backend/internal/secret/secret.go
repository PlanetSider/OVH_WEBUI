package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ovh-webui/server/internal/types"
)

const (
	CiphertextPrefix     = "v1:"
	legacyKeyFileName    = ".dbkey"
	protectedKeyFileName = "config.key"
)

type KeySource string

const (
	KeySourceEnvironment KeySource = "environment"
	KeySourceDBKey       KeySource = ".dbkey"
	KeySourceConfigFile  KeySource = "config-file"
)

type KeyInfo struct {
	Source KeySource
	Path   string
}

type Cipher struct {
	key [32]byte
}

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	cipher := &Cipher{}
	copy(cipher.key[:], key)
	return cipher, nil
}

// LoadKey applies the documented precedence: OVH_DB_KEY, existing legacy
// .dbkey, existing protected config.key, then a newly generated protected
// data-dir config.key.
func LoadKey(dataDir string) (*Cipher, KeyInfo, error) {
	if raw := strings.TrimSpace(os.Getenv("OVH_DB_KEY")); raw != "" {
		key, err := ParseKey(raw)
		if err != nil {
			return nil, KeyInfo{Source: KeySourceEnvironment}, fmt.Errorf("parse OVH_DB_KEY: %w", err)
		}
		cipher, err := New(key)
		return cipher, KeyInfo{Source: KeySourceEnvironment}, err
	}

	candidates := []struct {
		path   string
		source KeySource
	}{
		{filepath.Join(dataDir, legacyKeyFileName), KeySourceDBKey},
		{legacyKeyFileName, KeySourceDBKey},
		{filepath.Join(dataDir, protectedKeyFileName), KeySourceConfigFile},
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		path, err := filepath.Abs(candidate.path)
		if err != nil {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, KeyInfo{Source: candidate.source, Path: path}, fmt.Errorf("read secret key %s: %w", path, err)
		}
		key, err := ParseKeyFile(data)
		if err != nil {
			return nil, KeyInfo{Source: candidate.source, Path: path}, fmt.Errorf("parse secret key %s: %w", path, err)
		}
		cipher, err := New(key)
		return cipher, KeyInfo{Source: candidate.source, Path: path}, err
	}

	if strings.TrimSpace(dataDir) == "" {
		return nil, KeyInfo{}, fmt.Errorf("data directory is empty; cannot create secret key")
	}
	path := filepath.Join(dataDir, protectedKeyFileName)
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, KeyInfo{Source: KeySourceConfigFile, Path: path}, fmt.Errorf("generate secret key: %w", err)
	}
	if err := writeProtectedFile(path, []byte(hex.EncodeToString(key))); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, KeyInfo{Source: KeySourceConfigFile, Path: path}, err
		}
		// Another process may have initialized the data directory concurrently.
		// Re-read its key instead of overwriting ciphertext with a new key.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, KeyInfo{Source: KeySourceConfigFile, Path: path}, fmt.Errorf("read concurrently-created secret key %s: %w", path, readErr)
		}
		key, parseErr := ParseKeyFile(data)
		if parseErr != nil {
			return nil, KeyInfo{Source: KeySourceConfigFile, Path: path}, fmt.Errorf("parse concurrently-created secret key %s: %w", path, parseErr)
		}
		cipher, newErr := New(key)
		return cipher, KeyInfo{Source: KeySourceConfigFile, Path: path}, newErr
	}
	cipher, err := New(key)
	return cipher, KeyInfo{Source: KeySourceConfigFile, Path: path}, err
}

// ParseKey accepts a 64-character hex key, a base64 key, a raw 32-byte key,
// or derives a stable AES-256 key from a passphrase with SHA-256.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty key")
	}
	if strings.HasPrefix(raw, "hex:") {
		decoded, err := hex.DecodeString(strings.TrimPrefix(raw, "hex:"))
		if err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("hex key must decode to 32 bytes")
		}
		return decoded, nil
	}
	if strings.HasPrefix(raw, "base64:") {
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(raw, "base64:"))
		if err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("base64 key must decode to 32 bytes")
		}
		return decoded, nil
	}
	if len(raw) == 64 {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len([]byte(raw)) == 32 {
		return []byte(raw), nil
	}
	digest := sha256.Sum256([]byte(raw))
	return digest[:], nil
}

func ParseKeyFile(data []byte) ([]byte, error) {
	if len(data) == 32 {
		key := make([]byte, 32)
		copy(key, data)
		return key, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("empty key file")
	}
	var envelope struct {
		Key string `json:"key"`
	}
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || strings.TrimSpace(envelope.Key) == "" {
			return nil, fmt.Errorf("invalid key file envelope")
		}
		trimmed = envelope.Key
	}
	return ParseKey(trimmed)
}

func (c *Cipher) Encrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return CiphertextPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(value string) (plain string, encrypted bool, err error) {
	if !strings.HasPrefix(value, CiphertextPrefix) {
		return value, false, nil
	}
	encoded := strings.TrimPrefix(value, CiphertextPrefix)
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", true, fmt.Errorf("decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", true, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", true, err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", true, fmt.Errorf("ciphertext is shorter than nonce")
	}
	nonce, payload := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plainBytes, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		return "", true, fmt.Errorf("decrypt ciphertext: %w", err)
	}
	return string(plainBytes), true, nil
}

func IsEncrypted(value string) bool { return strings.HasPrefix(value, CiphertextPrefix) }

func writeProtectedFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create secret key directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write secret key: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write secret key: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect secret key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close secret key: %w", err)
	}
	cleanup = false
	return nil
}

func (c *Cipher) EncryptConfig(input types.Config) (types.Config, error) {
	output := input
	fields := configSecretFields(&output)
	for _, field := range fields {
		if *field == "" || IsEncrypted(*field) {
			continue
		}
		value, err := c.Encrypt(*field)
		if err != nil {
			return types.Config{}, err
		}
		*field = value
	}
	return output, nil
}

func (c *Cipher) DecryptConfig(input types.Config) (types.Config, bool, error) {
	output := input
	changed := false
	fields := configSecretFields(&output)
	for _, field := range fields {
		if *field == "" || !IsEncrypted(*field) {
			continue
		}
		value, _, err := c.Decrypt(*field)
		if err != nil {
			return types.Config{}, false, err
		}
		*field = value
		changed = true
	}
	return output, changed, nil
}

func ContainsEncryptedConfig(input types.Config) bool {
	for _, field := range configSecretFields(&input) {
		if IsEncrypted(*field) {
			return true
		}
	}
	return false
}

func ConfigNeedsEncryption(input types.Config) bool {
	for _, field := range configSecretFields(&input) {
		if *field != "" && !IsEncrypted(*field) {
			return true
		}
	}
	return false
}

func configSecretFields(input *types.Config) []*string {
	return []*string{
		&input.AppKey,
		&input.AppSecret,
		&input.ConsumerKey,
		&input.TgToken,
		&input.TgChatID,
		&input.TgWebhookSecret,
		&input.FeishuAppID,
		&input.FeishuAppSecret,
		&input.FeishuVerificationToken,
		&input.FeishuEncryptKey,
	}
}
