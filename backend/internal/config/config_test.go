package config

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/secret"
	"github.com/ovh-webui/server/internal/types"
)

func TestNewWithCipherMigratesLegacyConfigWithoutExposingPlaintext(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	legacy := types.Config{AppKey: "app", AppSecret: "secret", ConsumerKey: "consumer", TgToken: "telegram"}
	if err := database.SetKV(kvConfigKey, legacy); err != nil {
		t.Fatal(err)
	}
	cipher, _ := secret.New([]byte(strings.Repeat("k", 32)))
	store, err := NewWithCipher(database, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.AppKey != legacy.AppKey || got.TgToken != legacy.TgToken {
		t.Fatalf("config = %+v", got)
	}
	var raw map[string]interface{}
	if ok, err := database.GetKV(kvConfigKey, &raw); err != nil || !ok {
		t.Fatalf("raw config read failed: ok=%v err=%v", ok, err)
	} else if value, _ := raw["appKey"].(string); value == legacy.AppKey || !secret.IsEncrypted(value) {
		t.Fatalf("appKey was not migrated: %q", value)
	}
}

func TestNewWithCipherRejectsWrongKey(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first, _ := secret.New([]byte(strings.Repeat("a", 32)))
	encoded, _ := first.EncryptConfig(types.Config{AppKey: "app"})
	if err := database.SetKV(kvConfigKey, encoded); err != nil {
		t.Fatal(err)
	}
	second, _ := secret.New([]byte(strings.Repeat("b", 32)))
	if _, err := NewWithCipher(database, second); err == nil {
		t.Fatal("wrong key unexpectedly succeeded")
	}
}
