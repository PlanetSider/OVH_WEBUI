package handlers

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/price"
)

func TestPublicPriceResultScrubsInternalError(t *testing.T) {
	result := publicPriceResult(price.Result{
		Success: false,
		Error:   `选项 provider-returned-code 失败：HTTP 500 body secret`,
	})
	if result.Error != "询价失败，请稍后重试" {
		t.Fatalf("public price error = %q", result.Error)
	}
	if strings.Contains(result.Error, "provider-returned-code") || strings.Contains(result.Error, "secret") {
		t.Fatalf("internal price error leaked: %q", result.Error)
	}
}

func TestPublicPriceResultPreservesSuccessfulPrice(t *testing.T) {
	result := price.Result{Success: true, PlanCode: "example", Price: &price.PriceInfo{PricingMode: "monthly"}}
	got := publicPriceResult(result)
	if got.Error != "" || got.Price != result.Price || got.PlanCode != result.PlanCode {
		t.Fatalf("successful price result changed: %#v", got)
	}
}
