package price

import (
	"context"
	"errors"
	"strings"
	"testing"

	ovhsdk "github.com/ovh/go-ovh/ovh"
)

func TestPriceFailureIncludesSafeStageAndHTTPStatus(t *testing.T) {
	err := errors.New("provider body contains api-secret")
	// Keep the assertion focused on the helper contract; APIError is wrapped so
	// this also exercises errors.As through the same path as the OVH client.
	err = &wrappedAPIError{err: &ovhsdk.APIError{Code: 429}, message: err.Error()}

	got := priceFailure("询价失败，请稍后重试", "读取购物车 summary", err)
	if got != "询价失败，请稍后重试（读取购物车 summary：OVH API 请求失败（HTTP 429））" {
		t.Fatalf("priceFailure() = %q", got)
	}
	if strings.Contains(got, "api-secret") {
		t.Fatalf("priceFailure() leaked provider text: %q", got)
	}
}

func TestPriceFailureClassifiesCancellationAndTimeout(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "请求已取消"},
		{"deadline", context.DeadlineExceeded, "请求超时"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := priceFailure("询价失败，请稍后重试", "创建购物车", test.err)
			want := "询价失败，请稍后重试（创建购物车：" + test.want + "）"
			if got != want {
				t.Fatalf("priceFailure() = %q, want %q", got, want)
			}
		})
	}
}

func TestPriceFailureClassifiesUnknownErrorWithoutMessage(t *testing.T) {
	got := priceFailure("询价失败，请稍后重试", "添加配置", errors.New("secret response body"))
	want := "询价失败，请稍后重试（添加配置：OVH 请求失败）"
	if got != want {
		t.Fatalf("priceFailure() = %q, want %q", got, want)
	}
	if strings.Contains(got, "secret response body") {
		t.Fatalf("priceFailure() leaked unknown error text: %q", got)
	}
}

type wrappedAPIError struct {
	err     error
	message string
}

func (e *wrappedAPIError) Error() string { return e.message }
func (e *wrappedAPIError) Unwrap() error { return e.err }
