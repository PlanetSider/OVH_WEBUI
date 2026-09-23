package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/purchase"
)

func TestFormatOrderStatusRefreshMessage(t *testing.T) {
	got := formatOrderStatusRefreshMessage(purchase.OrderStatusRefreshResult{
		Updated: 2,
		Failed:  1,
		Skipped: 3,
	})
	want := "订单状态刷新完成：更新 2 条，失败 1 条，跳过 3 条"
	if got != want {
		t.Fatalf("formatOrderStatusRefreshMessage() = %q, want %q", got, want)
	}
}
func TestRefreshPurchaseHistoryStatusRateLimitsRepeatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	loop := purchase.NewOrderStatusLoop(&app.State{DB: database})
	handler := RefreshPurchaseHistoryStatus(loop)
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/api/purchase-history/refresh-status", nil)
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = request
		handler(context)
		return response
	}
	first := call()
	if first.Code != 200 || !strings.Contains(first.Body.String(), "订单状态刷新完成") {
		t.Fatalf("first refresh status=%d body=%s", first.Code, first.Body.String())
	}
	second := call()
	if second.Code != 429 || !strings.Contains(second.Body.String(), "刷新太频繁") {
		t.Fatalf("second refresh status=%d body=%s", second.Code, second.Body.String())
	}
}
