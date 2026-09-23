package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetQueueTimingsReturnsEmptyObject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/queue/timings", GetQueueTimings())
	request := httptest.NewRequest("GET", "/queue/timings", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Body.String() == "" || response.Body.String() == `{"timings":null}` {
		t.Fatalf("empty timings response should be an object: %s", response.Body.String())
	}
}
