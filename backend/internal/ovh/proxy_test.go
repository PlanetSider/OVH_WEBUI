package ovh

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

type testProxyReporter struct {
	failures int
	success  int
}

func (r *testProxyReporter) ReportFailure(string, error) { r.failures++ }
func (r *testProxyReporter) ReportSuccess(string)          { r.success++ }

func TestValidateProxyURL(t *testing.T) {
	valid := []string{"", "http://127.0.0.1:8080", "https://proxy.example:443", "socks5://u:p@127.0.0.1:1080", "socks5h://127.0.0.1:1080"}
	for _, value := range valid {
		if err := ValidateProxyURL(value); err != nil {
			t.Errorf("ValidateProxyURL(%q) = %v", value, err)
		}
	}
	invalid := []string{"ftp://127.0.0.1:21", "http://", "http://proxy/#fragment", "://bad"}
	for _, value := range invalid {
		if err := ValidateProxyURL(value); err == nil {
			t.Errorf("ValidateProxyURL(%q) unexpectedly succeeded", value)
		}
	}
}

func TestBuildHTTPClientDoesNotUseDirectFallback(t *testing.T) {
	client, err := BuildHTTPClient("http://127.0.0.1:1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("configured HTTP proxy was not installed")
	}
}

func TestFactoryClientDoesNotFallbackWhenLookupConfigured(t *testing.T) {
	factory := NewFactory(nil, func(string) (types.OVHAccount, bool) {
		return types.OVHAccount{}, false
	})
	if _, err := factory.Client(); err == nil {
		t.Fatal("Client unexpectedly fell back to legacy config without an account")
	}
}

func TestObservedRoundTripperReportsProxyErrors(t *testing.T) {
	reporter := &testProxyReporter{}
	transport := observedRoundTripper{
		accountID: "a",
		next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial proxy")
		}),
		reporter: reporter,
	}
	_, err := transport.RoundTrip(&http.Request{})
	if err == nil || reporter.failures != 1 || reporter.success != 0 {
		t.Fatalf("err=%v failures=%d success=%d", err, reporter.failures, reporter.success)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
