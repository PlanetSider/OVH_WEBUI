package monitor

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/types"
)

func testFeishuSecurityState(t *testing.T) (*app.State, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := config.New(database)
	cfg := types.DefaultConfig()
	cfg.FeishuEnabled = true
	cfg.FeishuAppID = "app-id"
	cfg.FeishuAppSecret = "app-secret"
	cfg.FeishuEncryptKey = "encrypt-key"
	cfg.FeishuVerificationToken = "verify-token"
	if err := store.Set(cfg); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return &app.State{Config: store}, database
}

func encryptFeishuChallenge(t *testing.T, keyText string, payload map[string]string) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(keyText))
	padding := aes.BlockSize - len(body)%aes.BlockSize
	body = append(body, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sealed := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, digest[:aes.BlockSize]).CryptBlocks(sealed, body)
	return base64.StdEncoding.EncodeToString(sealed)
}

func TestFeishuUnsignedChallengeRequiresEncryptedURLVerification(t *testing.T) {
	state, database := testFeishuSecurityState(t)
	defer database.Close()
	body := map[string]interface{}{
		"encrypt": encryptFeishuChallenge(t, "encrypt-key", map[string]string{
			"type":      "url_verification",
			"challenge": "challenge-value",
		}),
		"token":  "verify-token",
		"app_id": "app-id",
	}
	challenge, ok := FeishuUnsignedChallenge(state, body)
	if !ok || challenge != "challenge-value" {
		t.Fatalf("FeishuUnsignedChallenge() = %q, %v; want challenge-value, true", challenge, ok)
	}

	body["token"] = "wrong-token"
	if challenge, ok := FeishuUnsignedChallenge(state, body); ok || challenge != "" {
		t.Fatalf("wrong token accepted: %q, %v", challenge, ok)
	}

	body["token"] = "verify-token"
	body["encrypt"] = encryptFeishuChallenge(t, "encrypt-key", map[string]string{
		"type": "event_callback",
	})
	if challenge, ok := FeishuUnsignedChallenge(state, body); ok || challenge != "" {
		t.Fatalf("non-verification payload accepted: %q, %v", challenge, ok)
	}
}
