package ovh

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/types"
)

type testProxyReporter struct {
	failures int
	success  int
}

func (r *testProxyReporter) ReportFailure(string, error) { r.failures++ }
func (r *testProxyReporter) ReportSuccess(string)        { r.success++ }

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

func TestFingerprintProfilesAreWhitelistedAndLegacyIsBounded(t *testing.T) {
	for _, name := range FingerprintProfileNames() {
		if err := ValidateFingerprint(name); err != nil {
			t.Errorf("profile %q rejected: %v", name, err)
		}
		profile, warning := ResolveFingerprintProfile(name)
		if profile.Name != name || warning != "" {
			t.Errorf("profile %q resolved to %#v warning=%q", name, profile, warning)
		}
	}
	if err := ValidateFingerprint("ua:legacy-client"); err != nil {
		t.Fatalf("legacy ua value should remain readable: %v", err)
	}
	if err := ValidateFingerprint("random-browser"); err == nil {
		t.Fatal("unknown fingerprint profile should be rejected")
	}
	profile, warning := ResolveFingerprintProfile("ua:legacy-client")
	if profile.Name != "legacy-ua" || profile.UserAgent != "legacy-client" || warning == "" {
		t.Fatalf("legacy profile resolution = %#v warning=%q", profile, warning)
	}
}

func TestNamedFingerprintAppliesHeadersAndHTTPVersion(t *testing.T) {
	client, err := BuildHTTPClient("", "legacy-http1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := client.Transport.(*fingerprintRoundTripper)
	if !ok {
		t.Fatalf("named profile transport = %T", client.Transport)
	}
	transport, ok := wrapped.next.(*http.Transport)
	if !ok {
		t.Fatalf("wrapped transport = %T", wrapped.next)
	}
	if transport.ForceAttemptHTTP2 || transport.TLSClientConfig.MaxVersion != tls.VersionTLS12 {
		t.Fatalf("legacy profile transport = http2=%v maxTLS=%x", transport.ForceAttemptHTTP2, transport.TLSClientConfig.MaxVersion)
	}
	if got := transport.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("legacy ALPN = %v", got)
	}
}

func TestProbeProxyTargetTreatsAnyHTTPResponseAsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	result := ProbeProxyTarget("", "default", time.Second, ProxyProbeSpec{Name: "local", URL: srv.URL})
	if !result.OK || result.Status != http.StatusNotFound || result.MinMS < 0 || result.AvgMS < 0 {
		t.Fatalf("probe result = %#v", result)
	}
}

func TestProbeProxyTargetsPreservesOrderAndDeadProxyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	targets := []ProxyProbeSpec{{Name: "first", URL: srv.URL}, {Name: "second", URL: srv.URL}}
	results := ProbeProxyTargets("http://127.0.0.1:1", "default", 100*time.Millisecond, targets)
	for i, result := range results {
		if result.Name != targets[i].Name || result.OK {
			t.Fatalf("dead proxy result[%d] = %#v", i, result)
		}
		if strings.Contains(result.Error, "127.0.0.1:1") && strings.Contains(result.Error, "@") {
			t.Fatalf("unexpected credential-bearing probe error: %q", result.Error)
		}
	}
}
