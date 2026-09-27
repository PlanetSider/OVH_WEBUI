package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestQQQuickOrderReturnsHelp(t *testing.T) {
	state := newRebootTestState(t)
	status, body := performQQQuickOrder(t, QQQuickOrder(state, nil), `{"mode":"help"}`)
	if status != http.StatusOK || body["success"] != true {
		t.Fatalf("status=%d body=%#v", status, body)
	}
	if !strings.Contains(body["message"].(string), "/stock") {
		t.Fatalf("help message = %#v", body["message"])
	}
}

func TestQQQuickOrderValidatesModeAndDispatches(t *testing.T) {
	state := newRebootTestState(t)
	status, body := performQQQuickOrder(t, QQQuickOrder(state, nil), `{"mode":"unknown","planCode":"24ska01"}`)
	if status != http.StatusBadRequest || !strings.Contains(body["error"].(string), "未知 mode") {
		t.Fatalf("unknown mode status=%d body=%#v", status, body)
	}

	status, body = performQQQuickOrder(t, QQQuickOrder(state, nil), `{"mode":"stock","planCode":"24ska01"}`)
	if status != http.StatusOK || body["success"] != false {
		t.Fatalf("stock status=%d body=%#v", status, body)
	}
	if body["command"] != "/stock 24ska01" || !strings.Contains(body["message"].(string), "未配置任何 OVH 账户") {
		t.Fatalf("stock body=%#v", body)
	}
}

// Keep request construction in the test file without coupling the production API to test helpers.
func performQQQuickOrder(t *testing.T, handler gin.HandlerFunc, payload string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/api/qq/quick-order", bytes.NewBufferString(payload))
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(writer)
	context.Request = request
	handler(context)
	var response map[string]any
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, writer.Body.String())
	}
	return writer.Code, response
}
