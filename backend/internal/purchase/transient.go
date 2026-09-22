package purchase

import (
	"context"
	"errors"
	"net"
	"strings"

	ovhsdk "github.com/ovh/go-ovh/ovh"
)

// IsCancellation reports a caller-driven cancellation. It is intentionally
// separate from IsTransient: cancellation should not consume a retry budget.
func IsCancellation(err error) bool {
	return err != nil && errors.Is(err, context.Canceled)
}

// IsTransient 判断一次非 checkout OVH 调用是否可能在稍后成功。
// 该分类只供库存、购物车准备和配置阶段使用；checkout 仍必须由
// checkoutFailureIsDefinitive 决定是否隔离，不能套用本函数自动重试。
func IsTransient(err error) bool {
	if err == nil || IsCancellation(err) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *ovhsdk.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		switch {
		case apiErr.Code == 408 || apiErr.Code == 409 || apiErr.Code == 429 || apiErr.Code == 499:
			return true
		case apiErr.Code >= 500 && apiErr.Code <= 599:
			return true
		case apiErr.Code > 0:
			return false
		}
	}
	var apiErrValue ovhsdk.APIError
	if errors.As(err, &apiErrValue) {
		switch {
		case apiErrValue.Code == 408 || apiErrValue.Code == 409 || apiErrValue.Code == 429 || apiErrValue.Code == 499:
			return true
		case apiErrValue.Code >= 500 && apiErrValue.Code <= 599:
			return true
		case apiErrValue.Code > 0:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset", "connection refused", "no such host", "i/o timeout",
		"timeout", "eof", "broken pipe", "tls handshake", "too many requests",
		"service unavailable", "bad gateway", "gateway timeout",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
