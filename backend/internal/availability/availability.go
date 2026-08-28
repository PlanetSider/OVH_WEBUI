// Package availability contains OVH stock-state classification shared by
// catalog parsing, UI statistics and purchase safety checks. It deliberately
// has no dependency on higher-level application packages.
package availability

import "strings"

// ExplicitlyAvailable accepts only OVH stock states that are known to mean
// an order can be attempted. Unknown future states must fail closed.
func ExplicitlyAvailable(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "available", "1h-high", "1h-low", "24h", "72h":
		return true
	default:
		return false
	}
}
