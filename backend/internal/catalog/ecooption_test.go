package catalog

import "testing"

func ecoOptions(codes ...string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(codes))
	for _, code := range codes {
		out = append(out, map[string]interface{}{"planCode": code, "duration": "P1M"})
	}
	return out
}

func TestMatchEcoOptionPrefersShortestPrefixIndependentOfOrder(t *testing.T) {
	options := ecoOptions(
		"softraid-2x960nvme-2x6000sa-26sk50a-v1",
		"softraid-2x960nvme-26sk50a-v1",
		"softraid-2x6000sa-26sk50a-v1",
	)
	for _, input := range [][]map[string]interface{}{options, {options[2], options[1], options[0]}} {
		_, code, tier := MatchEcoOption(input, "softraid-2x960nvme")
		if code != "softraid-2x960nvme-26sk50a-v1" || tier != "原始码前缀" {
			t.Fatalf("matched %q at %q, want shortest raw-prefix candidate", code, tier)
		}
	}
}

func TestMatchEcoOptionRejectsCapacityMismatch(t *testing.T) {
	if _, code, _ := MatchEcoOption(ecoOptions("ram-64g-ecc-3200-24rise06-v1"), "ram-32g-ecc-3200"); code != "" {
		t.Fatalf("capacity mismatch matched %q", code)
	}
}

func TestMatchEcoOptionExactBeatsPrefix(t *testing.T) {
	_, code, tier := MatchEcoOption(ecoOptions("ram-64g-ecc-2133-24sk20-us", "ram-64g-ecc-2133"), "ram-64g-ecc-2133")
	if code != "ram-64g-ecc-2133" || tier != "原样相等" {
		t.Fatalf("matched %q at %q, want exact candidate", code, tier)
	}
}
