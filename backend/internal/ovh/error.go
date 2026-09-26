package ovh

import (
	"context"
	"errors"
	"fmt"
	"net"

	ovhsdk "github.com/ovh/go-ovh/ovh"
)

// ErrorSummary returns a bounded diagnostic category for an OVH request error.
// SDK API errors may include the complete upstream response body in Error();
// callers that log or expose a provider failure must use this summary instead.
func ErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "请求已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}
	var apiErr *ovhsdk.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return fmt.Sprintf("OVH API 请求失败（HTTP %d）", apiErr.Code)
	}
	var apiErrValue ovhsdk.APIError
	if errors.As(err, &apiErrValue) {
		return fmt.Sprintf("OVH API 请求失败（HTTP %d）", apiErrValue.Code)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "OVH 请求超时"
	}
	return "OVH 请求失败"
}
