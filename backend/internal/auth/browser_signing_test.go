package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Opt in with fixtures from scripts/test-http-signing.mjs --fixtures.
// Ordinary Go tests do not require Node or installed frontend dependencies.
func TestBrowserSigningFixtures(t *testing.T) {
	raw := os.Getenv("OVH_BROWSER_SIGNING_FIXTURES")
	if raw == "" {
		t.Skip("OVH_BROWSER_SIGNING_FIXTURES is not set")
	}
	var fixtures struct {
		APIKey   string `json:"apiKey"`
		Requests []struct {
			Name    string            `json:"name"`
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Body    []byte            `json:"body"`
			Headers map[string]string `json:"headers"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(raw), &fixtures); err != nil {
		t.Fatal(err)
	}
	if fixtures.APIKey == "" || len(fixtures.Requests) != 15 {
		t.Fatal("expected a fixture key and all 15 browser requests")
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Middleware(Config{APIKey: fixtures.APIKey, Enabled: true, WhitelistPaths: map[string]struct{}{}}))
	router.Any("/api/*path", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, fixture := range fixtures.Requests {
		t.Run(fixture.Name, func(t *testing.T) {
			request := func() *http.Request {
				r := httptest.NewRequest(fixture.Method, fixture.Path, bytes.NewReader(fixture.Body))
				for name, value := range fixture.Headers {
					r.Header.Set(name, value)
				}
				return r
			}
			check := func(r *http.Request, wantStatus int, wantCode string) {
				t.Helper()
				response := httptest.NewRecorder()
				router.ServeHTTP(response, r)
				if response.Code != wantStatus {
					t.Fatalf("status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
				}
				if wantCode != "" {
					var result struct {
						Code string `json:"code"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.Code != wantCode {
						t.Fatalf("code = %s, want %s", result.Code, wantCode)
					}
				}
			}

			check(request(), http.StatusOK, "")
			check(request(), http.StatusUnauthorized, "REQUEST_REPLAYED")

			wrongKey := request()
			wrongKey.Header.Set("X-API-Key", "WrongKey12")
			check(wrongKey, http.StatusUnauthorized, "INVALID_API_KEY")

			tamperedQuery := request()
			if tamperedQuery.URL.RawQuery != "" {
				tamperedQuery.URL.RawQuery += "&"
			}
			tamperedQuery.URL.RawQuery += "tampered=1"
			check(tamperedQuery, http.StatusUnauthorized, "INVALID_REQUEST_SIGNATURE")

			tamperedBody := request()
			tamperedBody.Body = http.NoBody
			if len(fixture.Body) == 0 {
				tamperedBody = httptest.NewRequest(fixture.Method, fixture.Path, strings.NewReader("tampered"))
				for name, value := range fixture.Headers {
					tamperedBody.Header.Set(name, value)
				}
			}
			check(tamperedBody, http.StatusUnauthorized, "INVALID_REQUEST_SIGNATURE")

			expired := request()
			expired.Header.Set("X-Request-Time", strconv.FormatInt(time.Now().Add(-requestTimeWindow-time.Second).UnixMilli(), 10))
			check(expired, http.StatusUnauthorized, "TIMESTAMP_EXPIRED")

			unsigned := request()
			unsigned.Header.Del("X-Request-Signature")
			check(unsigned, http.StatusUnauthorized, "NO_REQUEST_SIGNATURE")
		})
	}
}
