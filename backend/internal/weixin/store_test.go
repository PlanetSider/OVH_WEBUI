package weixin

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/secret"
)

func TestStoreMigratesLegacyTokensAndReadsThemBack(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	legacy := Credentials{AccountID: "bot", Token: "bot-token", BaseURL: DefaultBaseURL, UserID: "user"}
	if _, err := database.Exec(`INSERT INTO weixin_credentials(id, account_id, bot_token, base_url, user_id, updated_at) VALUES(1, ?, ?, ?, ?, ?)`, legacy.AccountID, legacy.Token, legacy.BaseURL, legacy.UserID, legacy.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO weixin_context_tokens(account_id, user_id, context_token, updated_at) VALUES(?, ?, ?, ?)`, "bot", "user", "context-token", 1); err != nil {
		t.Fatal(err)
	}
	cipher, _ := secret.New([]byte(strings.Repeat("w", 32)))
	database.SetSecretCipher(cipher)
	if err := store.MigrateSecrets(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := database.Get(&raw, `SELECT bot_token FROM weixin_credentials WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(raw) {
		t.Fatalf("bot token was not encrypted: %q", raw)
	}
	contextToken, err := store.ContextToken("bot", "user")
	if err != nil || contextToken != "context-token" {
		t.Fatalf("context token = %q err=%v", contextToken, err)
	}
	got, ok, err := store.LoadCredentials()
	if err != nil || !ok || got.Token != legacy.Token {
		t.Fatalf("credentials = %#v ok=%v err=%v", got, ok, err)
	}
}

func TestStoreWrongKeyDoesNotMigrateLegacyOrEncryptedToken(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first, _ := secret.New([]byte(strings.Repeat("a", 32)))
	encoded, _ := first.Encrypt("bot-token")
	if _, err := database.Exec(`INSERT INTO weixin_credentials(id, account_id, bot_token, base_url, user_id, updated_at) VALUES(1, ?, ?, ?, ?, ?)`, "bot", encoded, DefaultBaseURL, "user", 1); err != nil {
		t.Fatal(err)
	}
	second, _ := secret.New([]byte(strings.Repeat("b", 32)))
	database.SetSecretCipher(second)
	if err := NewStore(database).MigrateSecrets(); err == nil {
		t.Fatal("wrong key unexpectedly migrated token")
	}
	var raw string
	if err := database.Get(&raw, `SELECT bot_token FROM weixin_credentials WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if raw != encoded {
		t.Fatalf("wrong-key migration modified token: %q", raw)
	}
}
