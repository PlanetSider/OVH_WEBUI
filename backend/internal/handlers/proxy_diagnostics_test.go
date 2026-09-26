package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/proxyguard"
	"github.com/ovh-webui/server/internal/types"
)

func TestProxyStatusReturnsProfilesAndDoesNotLeakProxyCredentials(t *testing.T) {
	state := &app.State{
		Accounts: []types.OVHAccount{{
			ID: "account-1", Name: "main", Zone: "IE", ProxyURL: "http://alice:secret@proxy.example:8080",
			Fingerprint: "chrome-like",
		}},
		ProxyGuard: proxyguard.New(3),
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/accounts/proxy-status", nil)
	ProxyStatus(state)(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "chrome-like") || !strings.Contains(body, "legacy-http1") {
		t.Fatalf("profile whitelist missing: %s", body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "alice") {
		t.Fatalf("proxy credentials leaked: %s", body)
	}
}

func TestProxyStatusDoesNotExposeGuardError(t *testing.T) {
	state := &app.State{
		Accounts:   []types.OVHAccount{{ID: "account-1", Name: "main"}},
		ProxyGuard: proxyguard.New(1),
	}
	state.ProxyGuard.ReportFailure("account-1", errors.New("proxyconnect http://alice:secret@proxy.example:8080 failed"))
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/accounts/account-1/proxy-status", nil)
	context.Params = gin.Params{{Key: "id", Value: "account-1"}}
	AccountProxyStatus(state)(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "代理连接失败") {
		t.Fatalf("generic proxy error missing: %s", body)
	}
	if strings.Contains(body, "proxyconnect") || strings.Contains(body, "secret") || strings.Contains(body, "alice") {
		t.Fatalf("guard error leaked: %s", body)
	}
}

func TestProxyEndpointsHandleMissingAccount(t *testing.T) {
	state := &app.State{}
	for name, handler := range map[string]gin.HandlerFunc{
		"test":  TestAccountProxy(state),
		"check": CheckAccountProxy(state),
	} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/accounts/missing/proxy-"+name, nil)
			context.Params = gin.Params{{Key: "id", Value: "missing"}}
			handler(context)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAPIHostOfStripsSchemeAndPath(t *testing.T) {
	if got := apiHostOf("https://api.example.test/v1"); got != "api.example.test" {
		t.Fatalf("host = %q", got)
	}
}
