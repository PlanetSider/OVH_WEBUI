package catalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

func resetCatalogTestCaches() {
	regionCacheMu.Lock()
	regionCache = map[string]*subsidiaryCatalog{}
	regionCacheFail = map[string]catalogFailure{}
	regionCacheCall = map[string]*catalogCall{}
	regionCacheMu.Unlock()
	availProbeMu.Lock()
	availProbeCache = map[string]availProbeEntry{}
	availProbeFail = map[string]availProbeFailure{}
	availProbeCalls = map[string]*availProbeCall{}
	availProbeMu.Unlock()
}

func TestLoadSubsidiaryCatalogSingleflightAndNormalization(t *testing.T) {
	resetCatalogTestCaches()
	defer resetCatalogTestCaches()
	original := fetchSubsidiaryCatalog
	defer func() { fetchSubsidiaryCatalog = original }()
	var calls int32
	release := make(chan struct{})
	fetchSubsidiaryCatalog = func(_ context.Context, _ *app.State, subsidiary string) (*subsidiaryCatalog, error) {
		if subsidiary != "IE" {
			t.Errorf("subsidiary = %q, want IE", subsidiary)
		}
		atomic.AddInt32(&calls, 1)
		<-release
		return &subsidiaryCatalog{plans: map[string]planConfig{"plan": {regions: []string{"europe"}}}, fetchedAt: time.Now()}, nil
	}
	const n = 12
	results := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, results[index] = loadSubsidiaryCatalog(&app.State{}, "ie")
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	for index, err := range results {
		if err != nil {
			t.Fatalf("call %d failed: %v", index, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
	regionCacheMu.Lock()
	_, ok := regionCache["IE"]
	regionCacheMu.Unlock()
	if !ok {
		t.Fatal("normalized cache key IE not found")
	}
}

func TestLoadSubsidiaryCatalogWaiterHonorsContextCancellation(t *testing.T) {
	resetCatalogTestCaches()
	defer resetCatalogTestCaches()
	original := fetchSubsidiaryCatalog
	defer func() { fetchSubsidiaryCatalog = original }()
	started := make(chan struct{})
	release := make(chan struct{})
	fetchSubsidiaryCatalog = func(_ context.Context, _ *app.State, _ string) (*subsidiaryCatalog, error) {
		close(started)
		<-release
		return &subsidiaryCatalog{plans: map[string]planConfig{}, fetchedAt: time.Now()}, nil
	}
	firstDone := make(chan struct{})
	go func() {
		_, _ = loadSubsidiaryCatalogContext(context.Background(), &app.State{}, "IE")
		close(firstDone)
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadSubsidiaryCatalogContext(ctx, &app.State{}, "IE"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("singleflight owner did not finish")
	}
}

func TestLoadSubsidiaryCatalogNegativeCacheAndStaleFallback(t *testing.T) {
	resetCatalogTestCaches()
	defer resetCatalogTestCaches()
	original := fetchSubsidiaryCatalog
	defer func() { fetchSubsidiaryCatalog = original }()
	wantErr := errors.New("upstream unavailable")
	var calls int32
	fetchSubsidiaryCatalog = func(_ context.Context, _ *app.State, _ string) (*subsidiaryCatalog, error) {
		atomic.AddInt32(&calls, 1)
		return nil, wantErr
	}
	stale := &subsidiaryCatalog{plans: map[string]planConfig{"plan": {}}, fetchedAt: time.Now().Add(-regionCacheTTL - time.Minute)}
	regionCacheMu.Lock()
	regionCache["US"] = stale
	regionCacheMu.Unlock()
	for i := 0; i < 4; i++ {
		got, err := loadSubsidiaryCatalog(&app.State{}, "US")
		if err != nil || got != stale {
			t.Fatalf("call %d got catalog=%p err=%v, want stale catalog and nil error", i, got, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fetch calls = %d, want 1 during failure TTL", got)
	}
}

func TestRegionProbeSingleflightByPlanAndRegion(t *testing.T) {
	resetCatalogTestCaches()
	defer resetCatalogTestCaches()
	original := probeRegionHasPlan
	defer func() { probeRegionHasPlan = original }()
	var calls int32
	release := make(chan struct{})
	probeRegionHasPlan = func(region, plan string) (bool, error) {
		if region != "US" || plan != "plan" {
			t.Errorf("probe args = %s/%s", region, plan)
		}
		atomic.AddInt32(&calls, 1)
		<-release
		return true, nil
	}
	const n = 10
	results := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], _ = regionHasPlan(&app.State{}, "plan", "US")
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	for _, result := range results {
		if !result {
			t.Fatal("probe result was false")
		}
	}
}

func TestAvailProbeCachePrunesExpiredAndCaps(t *testing.T) {
	resetCatalogTestCaches()
	defer resetCatalogTestCaches()
	now := time.Now()
	availProbeMu.Lock()
	availProbeCache["expired"] = availProbeEntry{has: true, at: now.Add(-availProbeTTL - time.Second)}
	for i := 0; i < maxAvailProbeEntries+10; i++ {
		availProbeCache[fmt.Sprintf("key-%d", i)] = availProbeEntry{has: i%2 == 0, at: now.Add(-time.Duration(i+1) * time.Millisecond)}
	}
	pruneAvailProbeLocked(now)
	got := len(availProbeCache) + len(availProbeFail)
	_, expiredPresent := availProbeCache["expired"]
	availProbeMu.Unlock()
	if expiredPresent {
		t.Fatal("expired availability entry was not pruned")
	}
	if got > maxAvailProbeEntries {
		t.Fatalf("availability cache entries = %d, want <= %d", got, maxAvailProbeEntries)
	}
}

func TestSubsidiaryOfAccountUsesEndpointFallback(t *testing.T) {
	if got := SubsidiaryOfAccount(types.OVHAccount{Endpoint: "ovh-us"}); got != "US" {
		t.Fatalf("got %q, want US", got)
	}
	if got := SubsidiaryOfAccount(types.OVHAccount{Zone: " ca "}); got != "CA" {
		t.Fatalf("got %q, want CA", got)
	}
}
