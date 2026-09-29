package handlers

import (
	"strings"
	"testing"
)

func TestFormatPriceQueryResponseUsesWithoutTaxOnly(t *testing.T) {
	got := formatPriceQueryResponse("24ska01", "gra", "ram-32g", "EUR", 10.5)
	if !strings.Contains(got, "价格: 10.5 EUR") {
		t.Fatalf("formatPriceQueryResponse() = %q, missing without-tax amount", got)
	}
	for _, unwanted := range []string{"12.6", "含税", "未税"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("formatPriceQueryResponse() = %q, contains unwanted text %q", got, unwanted)
		}
	}
}

func TestFormatPriceQueryResponseMarksMissingPriceUnavailable(t *testing.T) {
	got := formatPriceQueryResponse("24ska01", "gra", "默认/匹配配置", "EUR", nil)
	if !strings.Contains(got, "价格: 暂不可用") {
		t.Fatalf("formatPriceQueryResponse() = %q, missing unavailable price", got)
	}
}
