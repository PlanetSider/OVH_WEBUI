package monitor

import (
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/price"
)

func TestPriceCacheCanonicalizesOptionsAndIsolatesKeys(t *testing.T) {
	m := &Monitor{priceCacheTTL: PriceQuoteCacheTTL}
	display := price.DisplayPrice{TotalKnown: true, TotalWithoutTax: 42, Currency: "EUR"}
	m.priceCacheStore("account-1", "plan-1", "BHS", []string{"option-b", " option-a "}, display)

	got, ok := m.priceCacheLookup("account-1", "plan-1", "BHS", []string{"option-a", "option-b"})
	if !ok || got.TotalWithoutTax != 42 {
		t.Fatalf("canonical option lookup = %#v, %v; want cached display", got, ok)
	}

	for name, key := range map[string]struct {
		accountID  string
		planCode   string
		datacenter string
		options    []string
	}{
		"different account": {accountID: "account-2", planCode: "plan-1", datacenter: "BHS", options: []string{"option-a", "option-b"}},
		"different plan":    {accountID: "account-1", planCode: "plan-2", datacenter: "BHS", options: []string{"option-a", "option-b"}},
		"different dc":      {accountID: "account-1", planCode: "plan-1", datacenter: "GRA", options: []string{"option-a", "option-b"}},
		"different options": {accountID: "account-1", planCode: "plan-1", datacenter: "BHS", options: []string{"option-a", "option-c"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := m.priceCacheLookup(key.accountID, key.planCode, key.datacenter, key.options); ok {
				t.Fatal("lookup unexpectedly reused a different cache key")
			}
		})
	}
}

func TestPriceCacheExpiresAfterTwentyFourHours(t *testing.T) {
	m := &Monitor{priceCacheTTL: PriceQuoteCacheTTL}
	options := []string{"option-a"}
	m.priceCacheStore("account-1", "plan-1", "BHS", options, price.DisplayPrice{TotalKnown: true})
	key := newPriceCacheKey("account-1", "plan-1", "BHS", options)

	m.cacheLock.Lock()
	m.priceCache[key].createdAt = time.Now().Add(-PriceQuoteCacheTTL - time.Second)
	m.cacheLock.Unlock()

	if _, ok := m.priceCacheLookup("account-1", "plan-1", "BHS", options); ok {
		t.Fatal("expired quote was returned")
	}
	m.cacheLock.Lock()
	defer m.cacheLock.Unlock()
	if _, exists := m.priceCache[key]; exists {
		t.Fatal("expired quote was not removed during lookup")
	}
}

func TestPriceCacheCleanupRemovesExpiredQuotes(t *testing.T) {
	m := &Monitor{
		priceCache:    map[priceCacheKey]*cachedPrice{},
		priceCacheTTL: time.Hour,
	}
	options := []string{"option-a"}
	m.priceCacheStore("account-1", "plan-1", "BHS", options, price.DisplayPrice{TotalKnown: true})
	key := newPriceCacheKey("account-1", "plan-1", "BHS", options)
	m.cacheLock.Lock()
	m.priceCache[key].createdAt = time.Now().Add(-time.Hour - time.Second)
	m.cacheLock.Unlock()

	m.cleanupExpiredCaches()

	m.cacheLock.Lock()
	defer m.cacheLock.Unlock()
	if len(m.priceCache) != 0 {
		t.Fatalf("cleanup left %d expired quotes", len(m.priceCache))
	}
}

func TestPriceCacheInvalidatesOnlyUnavailableKey(t *testing.T) {
	m := &Monitor{priceCacheTTL: PriceQuoteCacheTTL}
	optionsA := []string{"option-a"}
	optionsB := []string{"option-b"}
	display := price.DisplayPrice{TotalKnown: true}
	m.priceCacheStore("account-1", "plan-1", "BHS", optionsA, display)
	m.priceCacheStore("account-1", "plan-1", "BHS", optionsB, display)
	m.priceCacheStore("account-1", "plan-1", "GRA", optionsA, display)

	m.invalidatePriceCache("account-1", "plan-1", "BHS", optionsA)

	if _, ok := m.priceCacheLookup("account-1", "plan-1", "BHS", optionsA); ok {
		t.Fatal("unavailable key still returned a cached quote")
	}
	for name, key := range map[string]struct {
		datacenter string
		options    []string
	}{
		"same dc different options": {datacenter: "BHS", options: optionsB},
		"different dc":              {datacenter: "GRA", options: optionsA},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := m.priceCacheLookup("account-1", "plan-1", key.datacenter, key.options); !ok {
				t.Fatal("invalidation removed an unrelated quote")
			}
		})
	}
}

func TestPriceCacheRetainsOnlyCurrentAvailableSnapshot(t *testing.T) {
	m := &Monitor{
		priceCache:    map[priceCacheKey]*cachedPrice{},
		priceCacheTTL: PriceQuoteCacheTTL,
	}
	display := price.DisplayPrice{TotalKnown: true}
	m.priceCacheStore("account-1", "plan-1", "BHS", []string{"option-a"}, display)
	m.priceCacheStore("account-1", "plan-1", "GRA", []string{"option-b"}, display)
	m.priceCacheStore("account-1", "plan-1", "BHS", []string{"old-option"}, display)
	m.priceCacheStore("account-2", "plan-1", "BHS", []string{"old-option"}, display)
	m.priceCacheStore("account-1", "plan-2", "BHS", []string{"old-option"}, display)

	available := map[priceCacheKey]struct{}{
		newPriceCacheKey("account-1", "plan-1", "BHS", []string{"option-a"}): {},
	}
	m.retainPriceCachesForAvailability(" account-1 ", " plan-1 ", available)

	if _, ok := m.priceCacheLookup("account-1", "plan-1", "BHS", []string{"option-a"}); !ok {
		t.Fatal("current available quote was removed")
	}
	for name, key := range map[string]struct {
		accountID  string
		planCode   string
		datacenter string
		options    []string
	}{
		"old datacenter": {accountID: "account-1", planCode: "plan-1", datacenter: "GRA", options: []string{"option-b"}},
		"old options":    {accountID: "account-1", planCode: "plan-1", datacenter: "BHS", options: []string{"old-option"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := m.priceCacheLookup(key.accountID, key.planCode, key.datacenter, key.options); ok {
				t.Fatal("stale quote from successful availability snapshot was retained")
			}
		})
	}
	for name, key := range map[string]struct {
		accountID string
		planCode  string
	}{
		"different account": {accountID: "account-2", planCode: "plan-1"},
		"different plan":    {accountID: "account-1", planCode: "plan-2"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := m.priceCacheLookup(key.accountID, key.planCode, "BHS", []string{"old-option"}); !ok {
				t.Fatal("snapshot cleanup crossed an account or plan boundary")
			}
		})
	}
}

func TestPriceCacheStoreKeepsSuccessfulUnknownDisplay(t *testing.T) {
	m := &Monitor{priceCacheTTL: PriceQuoteCacheTTL}
	m.priceCacheStore("account-1", "plan-1", "BHS", nil, price.DisplayPrice{})
	if len(m.priceCache) != 1 {
		t.Fatal("successful unknown display price was not cached")
	}
}
