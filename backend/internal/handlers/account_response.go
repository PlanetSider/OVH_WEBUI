package handlers

import (
	"net/url"
	"strings"

	"github.com/ovh-webui/server/internal/types"
)

// accountResponseDTO deliberately omits credential values. The account API is
// used by the browser, so encrypted-at-rest values must never be decrypted into
// a JSON response. Empty credential fields preserve the legacy shape while the
// configured flags let the UI distinguish omission from an unconfigured account.
type accountResponseDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Zone        string `json:"zone"`
	AppKey      string `json:"appKey,omitempty"`
	AppSecret   string `json:"appSecret,omitempty"`
	ConsumerKey string `json:"consumerKey,omitempty"`
	IAM         string `json:"iam"`
	ProxyURL    string `json:"proxyUrl,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	IsDefault   bool   `json:"isDefault"`
	CreatedAt   string `json:"createdAt"`
	AppKeyConfigured      bool `json:"appKeyConfigured"`
	AppSecretConfigured   bool `json:"appSecretConfigured"`
	ConsumerKeyConfigured bool `json:"consumerKeyConfigured"`
	ProxyConfigured       bool `json:"proxyConfigured"`
}

func redactProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[configured]"
	}
	u.User = nil
	u.RawQuery = ""
	return u.String()
}

func toAccountResponse(account types.OVHAccount) accountResponseDTO {
	return accountResponseDTO{
		ID: account.ID, Name: account.Name, Endpoint: account.Endpoint, Zone: account.Zone,
		IAM: account.IAM, ProxyURL: redactProxyURL(account.ProxyURL), Fingerprint: account.Fingerprint,
		IsDefault: account.IsDefault, CreatedAt: account.CreatedAt,
		AppKeyConfigured: account.AppKey != "", AppSecretConfigured: account.AppSecret != "",
		ConsumerKeyConfigured: account.ConsumerKey != "", ProxyConfigured: account.ProxyURL != "",
	}
}

func accountsResponse(accounts []types.OVHAccount) []accountResponseDTO {
	out := make([]accountResponseDTO, len(accounts))
	for i, account := range accounts {
		out[i] = toAccountResponse(account)
	}
	return out
}
