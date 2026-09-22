package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/types"
)

func TestCipherRoundTripAndPlaintextCompatibility(t *testing.T) {
	cipher, err := New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := cipher.Encrypt("secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(encoded) || encoded == "secret-value" {
		t.Fatalf("encoded = %q", encoded)
	}
	decoded, encrypted, err := cipher.Decrypt(encoded)
	if err != nil || !encrypted || decoded != "secret-value" {
		t.Fatalf("decoded=%q encrypted=%v err=%v", decoded, encrypted, err)
	}
	plain, encrypted, err := cipher.Decrypt("legacy")
	if err != nil || encrypted || plain != "legacy" {
		t.Fatalf("plaintext decode=%q encrypted=%v err=%v", plain, encrypted, err)
	}
}

func TestCipherWrongKeyFails(t *testing.T) {
	first, _ := New([]byte(strings.Repeat("a", 32)))
	second, _ := New([]byte(strings.Repeat("b", 32)))
	encoded, err := first.Encrypt("secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Decrypt(encoded); err == nil {
		t.Fatal("wrong key unexpectedly decrypted ciphertext")
	}
}

func TestLoadKeyEnvironmentPrecedesFiles(t *testing.T) {
	t.Setenv("OVH_DB_KEY", "environment-key")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, legacyKeyFileName), []byte("file-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, info, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != KeySourceEnvironment {
		t.Fatalf("source = %q", info.Source)
	}
}

func TestLoadKeyUsesProtectedConfigFileAfterLegacyCandidates(t *testing.T) {
	t.Setenv("OVH_DB_KEY", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, protectedKeyFileName), []byte("config-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, info, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != KeySourceConfigFile || info.Path != filepath.Join(dir, protectedKeyFileName) {
		t.Fatalf("info = %+v", info)
	}
}

func TestLoadKeyGeneratesProtectedFile(t *testing.T) {
	t.Setenv("OVH_DB_KEY", "")
	dir := t.TempDir()
	_, info, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != KeySourceConfigFile || info.Path != filepath.Join(dir, protectedKeyFileName) {
		t.Fatalf("info = %+v", info)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatal(err)
	}
}

func TestLoadKeyGeneratesProtectedFileAndCanBeReloaded(t *testing.T) {
	t.Setenv("OVH_DB_KEY", "")
	dir := t.TempDir()
	first, info, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != KeySourceConfigFile || info.Path != filepath.Join(dir, protectedKeyFileName) {
		t.Fatalf("info = %+v", info)
	}
	second, secondInfo, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if secondInfo.Source != KeySourceConfigFile || secondInfo.Path != info.Path {
		t.Fatalf("reload info = %+v", secondInfo)
	}
	encoded, err := first.Encrypt("restart-safe")
	if err != nil {
		t.Fatal(err)
	}
	decoded, encrypted, err := second.Decrypt(encoded)
	if err != nil || !encrypted || decoded != "restart-safe" {
		t.Fatalf("reloaded key did not decrypt: value=%q encrypted=%v err=%v", decoded, encrypted, err)
	}
}

func TestParseKeyFilePreservesLegacyBinaryKey(t *testing.T) {
	legacy := make([]byte, 32)
	legacy[0] = ' '
	legacy[31] = '\n'
	for i := 1; i < len(legacy)-1; i++ {
		legacy[i] = byte(i)
	}
	parsed, err := ParseKeyFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed) != string(legacy) {
		t.Fatalf("binary key changed during parse: %v != %v", parsed, legacy)
	}
}

func TestConfigMigrationIsIdempotentByRepresentation(t *testing.T) {
	cipher, _ := New([]byte(strings.Repeat("z", 32)))
	input := types.Config{AppKey: "app", TgToken: "tg", FeishuEncryptKey: "feishu"}
	encoded, err := cipher.EncryptConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if ConfigNeedsEncryption(encoded) || !ContainsEncryptedConfig(encoded) {
		t.Fatalf("encoded config = %+v", encoded)
	}
	decoded, changed, err := cipher.DecryptConfig(encoded)
	if err != nil || !changed || decoded.AppKey != input.AppKey || decoded.TgToken != input.TgToken {
		t.Fatalf("decoded=%+v changed=%v err=%v", decoded, changed, err)
	}
}
