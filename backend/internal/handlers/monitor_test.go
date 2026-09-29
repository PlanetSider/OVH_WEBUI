package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/monitor"
)

func TestMonitorSubscriptionRoutesTargetInstanceID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	state := &app.State{DB: database, Logger: logger.New(filepath.Join(t.TempDir(), "monitor.log.json"), nil)}
	mon := monitor.New(state)
	mon.LoadFromDB()
	if err := mon.AddSubscription("24sk10", []string{"gra"}, true, false, "first", nil,
		[]monitor.HistoryEntry{{Status: "available"}}, false, 1, "", []string{"32G"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mon.AddSubscription("24sk10", []string{"sbg"}, true, false, "second", nil,
		[]monitor.HistoryEntry{{Status: "unavailable"}}, false, 1, "", []string{"64G"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	subs := mon.Snapshot()
	if len(subs) != 2 || subs[0].ID == subs[1].ID {
		t.Fatalf("independent subscriptions = %#v", subs)
	}
	firstID, secondID := subs[0].ID, subs[1].ID

	call := func(method, id, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: id}}
		ctx.Request = httptest.NewRequest(method, "/api/monitor/subscriptions/"+id, strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		handler(ctx)
		return recorder
	}
	assertHistory := func(id, want string) {
		t.Helper()
		response := call(http.MethodGet, id, "", GetSubscriptionHistory(state, mon))
		if response.Code != http.StatusOK {
			t.Fatalf("GET history %s = %d: %s", id, response.Code, response.Body.String())
		}
		var entries []monitor.HistoryEntry
		if err := json.Unmarshal(response.Body.Bytes(), &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Status != want {
			t.Fatalf("history %s = %#v, want %s", id, entries, want)
		}
	}
	assertHistory(firstID, "available")
	assertHistory(secondID, "unavailable")
	if response := call(http.MethodGet, "24sk10", "", GetSubscriptionHistory(state, mon)); response.Code != http.StatusNotFound {
		t.Fatalf("legacy planCode lookup = %d, want 404", response.Code)
	}

	response := call(http.MethodPut, secondID, `{"memories":["128G"]}`, UpdateSubscription(state, mon))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT %s = %d: %s", secondID, response.Code, response.Body.String())
	}
	if got := mon.FindSubscription(firstID); len(got.Memories) != 1 || got.Memories[0] != "32G" {
		t.Fatalf("first subscription was edited: %#v", got)
	}
	if got := mon.FindSubscription(secondID); len(got.Memories) != 1 || got.Memories[0] != "128G" {
		t.Fatalf("second subscription was not edited: %#v", got)
	}
	assertHistory(firstID, "available")
	assertHistory(secondID, "unavailable")

	response = call(http.MethodDelete, firstID, "", RemoveSubscription(state, mon))
	if response.Code != http.StatusOK || mon.FindSubscription(firstID) != nil {
		t.Fatalf("DELETE %s = %d: %s", firstID, response.Code, response.Body.String())
	}
	assertHistory(secondID, "unavailable")
	persisted, err := database.ListMonitorSubscriptions()
	if err != nil || len(persisted) != 1 || persisted[0].ID != secondID || persisted[0].Memories[0] != "128G" {
		t.Fatalf("persisted subscriptions = %#v, err = %v", persisted, err)
	}
}
