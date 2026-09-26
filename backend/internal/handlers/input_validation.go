package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	maxAccountIDLength  = 128
	maxPlanCodeLength   = 128
	maxDatacenterLength = 128
	maxOptions          = 64
	maxOptionLength     = 128
	maxDatacenters      = 32
)

func boundedInput(value, field string, max int) error {
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s 超过长度限制", field)
	}
	for _, r := range value {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("%s 包含非法字符", field)
		}
	}
	return nil
}

func validateQueueFields(accountID, planCode, datacenter string, options []string) error {
	for _, item := range []struct {
		value string
		name  string
		max   int
	}{{accountID, "account_id", maxAccountIDLength}, {planCode, "planCode", maxPlanCodeLength}, {datacenter, "datacenter", maxDatacenterLength}} {
		if err := boundedInput(item.value, item.name, item.max); err != nil {
			return err
		}
	}
	if len(options) > maxOptions {
		return fmt.Errorf("options 数量超过限制")
	}
	for _, option := range options {
		if err := boundedInput(strings.TrimSpace(option), "options", maxOptionLength); err != nil {
			return err
		}
	}
	return nil
}

func validateDatacenters(datacenters []string) error {
	if len(datacenters) > maxDatacenters {
		return fmt.Errorf("datacenters 数量超过限制")
	}
	for _, datacenter := range datacenters {
		if err := boundedInput(strings.TrimSpace(datacenter), "datacenter", maxDatacenterLength); err != nil {
			return err
		}
	}
	return nil
}

func validatePathParam(name, value string) error {
	if utf8.RuneCountInString(value) > 256 {
		return fmt.Errorf("路径参数 %s 超过长度限制", name)
	}
	if strings.ContainsAny(value, "\x00\r\n\\") {
		return fmt.Errorf("路径参数 %s 包含非法字符", name)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("路径参数 %s 包含目录穿越片段", name)
		}
	}
	if !strings.HasPrefix(name, "*") && strings.Contains(value, "/") {
		return fmt.Errorf("路径参数 %s 不得包含路径分隔符", name)
	}
	return nil
}

// ValidatePathParams 在路由处理器之前统一约束 URL 参数。
// `*planCode` 等通配参数允许内部斜杠，但不允许目录穿越或反斜杠。
func ValidatePathParams(c *gin.Context) {
	for _, param := range c.Params {
		if err := validatePathParam(param.Key, param.Value); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   err.Error(),
				"code":    "INVALID_PATH_PARAMETER",
			})
			return
		}
	}
	c.Next()
}
