package ovh

import (
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

func TestSharedHTTPClientFollowsDefaultAccountProxy(t *testing.T) {
	acc := types.OVHAccount{
		ID: "default", Name: "默认", Endpoint: "ovh-eu", Zone: "IE",
		AppKey: "k", AppSecret: "s", ConsumerKey: "c",
		ProxyURL: "http://127.0.0.1:8888",
	}
	f := NewFactory(nil, func(id string) (types.OVHAccount, bool) {
		if id == "" || id == acc.ID {
			return acc, true
		}
		return types.OVHAccount{}, false
	})
	if err := f.RefreshSharedProxy(); err != nil {
		t.Fatalf("RefreshSharedProxy: %v", err)
	}
	client, err := f.SharedHTTPClient(5 * time.Second)
	if err != nil {
		t.Fatalf("SharedHTTPClient: %v", err)
	}
	if client.Transport == nil {
		t.Fatal("shared client has no transport")
	}
	if f.SharedProxyAccountID() != acc.ID {
		t.Fatalf("shared proxy account = %q, want %q", f.SharedProxyAccountID(), acc.ID)
	}

	acc.ProxyURL = "socks5://127.0.0.1:1080"
	f.lookup = func(id string) (types.OVHAccount, bool) {
		if id == "" || id == acc.ID {
			return acc, true
		}
		return types.OVHAccount{}, false
	}
	if err := f.RefreshSharedProxy(); err != nil {
		t.Fatalf("RefreshSharedProxy after change: %v", err)
	}
	changed, err := f.SharedHTTPClient(5 * time.Second)
	if err != nil {
		t.Fatalf("SharedHTTPClient after change: %v", err)
	}
	if changed.Transport == client.Transport {
		t.Fatal("shared transport was not replaced after proxy change")
	}
}

func TestSharedHTTPClientDoesNotFallbackAfterInvalidProxy(t *testing.T) {
	acc := types.OVHAccount{ID: "default", ProxyURL: "ftp://127.0.0.1:21"}
	f := NewFactory(nil, func(string) (types.OVHAccount, bool) { return acc, true })
	if err := f.RefreshSharedProxy(); err == nil {
		t.Fatal("invalid shared proxy should fail")
	}
	if _, err := f.SharedHTTPClient(time.Second); err == nil {
		t.Fatal("invalid shared proxy must not silently use direct connection")
	}
}
