package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/ovh"
	"github.com/ovh-webui/server/internal/types"
)

// ErrPlanNotInCatalog 表示目录成功拉取，但当前子公司不提供该 planCode。
var ErrPlanNotInCatalog = errors.New("该子公司的目录里没有这个 planCode")

// SubsidiaryOfAccount 统一账户到公开目录子公司的推导。
func SubsidiaryOfAccount(acc types.OVHAccount) string {
	sub := strings.ToUpper(strings.TrimSpace(acc.Zone))
	if sub == "" {
		sub = ovh.DefaultSubsidiaryForEndpoint(acc.Endpoint)
	}
	return sub
}

const (
	regionCacheTTL       = 2 * time.Hour
	regionCacheFailTTL   = 30 * time.Second
	availProbeTTL        = 10 * time.Minute
	availProbeErrTTL     = 15 * time.Second
	maxAvailProbeEntries = 4096
)

type planConfig struct {
	regions       []string
	datacenters   []string
	addonFamilies map[string][]string
}

type subsidiaryCatalog struct {
	plans     map[string]planConfig
	fetchedAt time.Time
}

type catalogFailure struct {
	err error
	at  time.Time
}

type catalogCall struct {
	done chan struct{}
	cat  *subsidiaryCatalog
	err  error
}

var (
	regionCacheMu   sync.Mutex
	regionCache     = map[string]*subsidiaryCatalog{}
	regionCacheFail = map[string]catalogFailure{}
	regionCacheCall = map[string]*catalogCall{}
)

func ecoCatalogURL(subsidiary string) string {
	q := url.Values{}
	q.Set("ovhSubsidiary", subsidiary)
	return ovh.CatalogBaseURLForSubsidiary(subsidiary) + "/v1/order/catalog/public/eco?" + q.Encode()
}

// fetchEcoCatalogBody uses the shared public transport so catalog probes retain
// the configured default-account proxy and its fail-closed behavior.
const maxCatalogResponseBytes = 16 << 20

func fetchEcoCatalogBody(state *app.State, subsidiary string, parse func(io.Reader) error) error {
	return fetchEcoCatalogBodyContext(context.Background(), state, subsidiary, parse)
}

func fetchEcoCatalogBodyContext(ctx context.Context, state *app.State, subsidiary string, parse func(io.Reader) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state == nil || state.OVH == nil {
		return fmt.Errorf("公共目录缺少应用状态")
	}
	client, err := state.OVH.SharedHTTPClient(60 * time.Second)
	if err != nil {
		return fmt.Errorf("公共目录代理不可用: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ecoCatalogURL(subsidiary), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("拉取 %s 目录失败: %w", subsidiary, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCatalogResponseBytes))
		return fmt.Errorf("拉取 %s 目录失败(%s 站点): HTTP %d", subsidiary, ovh.SubsidiaryRegion(subsidiary), resp.StatusCode)
	}
	return parse(io.LimitReader(resp.Body, maxCatalogResponseBytes))
}

func parseEcoCatalog(r io.Reader) (map[string]planConfig, error) {
	var payload struct {
		Plans []struct {
			PlanCode       string `json:"planCode"`
			Configurations []struct {
				Name   string   `json:"name"`
				Values []string `json:"values"`
			} `json:"configurations"`
			AddonFamilies []struct {
				Name   string   `json:"name"`
				Addons []string `json:"addons"`
			} `json:"addonFamilies"`
		} `json:"plans"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Plans == nil {
		return nil, errors.New("公开 catalog 缺少 plans")
	}
	plans := make(map[string]planConfig, len(payload.Plans))
	for _, plan := range payload.Plans {
		code := strings.TrimSpace(plan.PlanCode)
		if code == "" {
			continue
		}
		parsed := planConfig{}
		for _, configuration := range plan.Configurations {
			switch configuration.Name {
			case "region":
				parsed.regions = append([]string(nil), configuration.Values...)
			case "dedicated_datacenter":
				parsed.datacenters = append([]string(nil), configuration.Values...)
			}
		}
		if len(plan.AddonFamilies) > 0 {
			parsed.addonFamilies = make(map[string][]string, len(plan.AddonFamilies))
			for _, family := range plan.AddonFamilies {
				name := strings.ToLower(strings.TrimSpace(family.Name))
				if name != "" {
					parsed.addonFamilies[name] = append([]string(nil), family.Addons...)
				}
			}
		}
		plans[code] = parsed
	}
	return plans, nil
}

// fetchSubsidiaryCatalog 可注入以测试有界缓存/单飞行为。
var fetchSubsidiaryCatalog = func(ctx context.Context, state *app.State, subsidiary string) (*subsidiaryCatalog, error) {
	var plans map[string]planConfig
	if err := fetchEcoCatalogBodyContext(ctx, state, subsidiary, func(reader io.Reader) error {
		parsed, err := parseEcoCatalog(reader)
		if err != nil {
			return fmt.Errorf("解析 %s 目录失败: %w", subsidiary, err)
		}
		plans = parsed
		return nil
	}); err != nil {
		return nil, err
	}
	parsed := &subsidiaryCatalog{plans: plans, fetchedAt: time.Now()}
	if state != nil && state.Logger != nil {
		state.Logger.Info(fmt.Sprintf("[region] 已缓存 %s 目录的区域配置(%d 个 plan)", subsidiary, len(parsed.plans)), "purchase")
	}
	return parsed, nil
}

func loadSubsidiaryCatalog(state *app.State, subsidiary string) (*subsidiaryCatalog, error) {
	return loadSubsidiaryCatalogContext(context.Background(), state, subsidiary)
}

func loadSubsidiaryCatalogContext(ctx context.Context, state *app.State, subsidiary string) (*subsidiaryCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	subsidiary = strings.ToUpper(strings.TrimSpace(subsidiary))
	if subsidiary == "" {
		return nil, fmt.Errorf("缺少 ovhSubsidiary,无法确定目录站点")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	regionCacheMu.Lock()
	if cached, ok := regionCache[subsidiary]; ok && time.Since(cached.fetchedAt) < regionCacheTTL {
		regionCacheMu.Unlock()
		return cached, nil
	}
	if failure, ok := regionCacheFail[subsidiary]; ok && time.Since(failure.at) < regionCacheFailTTL {
		stale := regionCache[subsidiary]
		regionCacheMu.Unlock()
		if stale != nil {
			return stale, nil
		}
		return nil, failure.err
	}
	if call, ok := regionCacheCall[subsidiary]; ok {
		regionCacheMu.Unlock()
		select {
		case <-call.done:
			return call.cat, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &catalogCall{done: make(chan struct{})}
	regionCacheCall[subsidiary] = call
	regionCacheMu.Unlock()

	cat, err := fetchSubsidiaryCatalog(ctx, state, subsidiary)
	regionCacheMu.Lock()
	delete(regionCacheCall, subsidiary)
	if err == nil {
		if cat == nil {
			err = fmt.Errorf("目录拉取返回空结果")
		} else {
			regionCache[subsidiary] = cat
			delete(regionCacheFail, subsidiary)
		}
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			regionCacheFail[subsidiary] = catalogFailure{err: err, at: time.Now()}
			if stale := regionCache[subsidiary]; stale != nil {
				if state != nil && state.Logger != nil {
					state.Logger.Warn(fmt.Sprintf("[region] 刷新 %s 目录失败，继续使用 %s 前缓存", subsidiary, time.Since(stale.fetchedAt).Truncate(time.Second)), "purchase")
				}
				cat, err = stale, nil
			}
		}
	}
	call.cat, call.err = cat, err
	regionCacheMu.Unlock()
	close(call.done)
	return cat, err
}

func RegionForPlan(state *app.State, accountID, planCode, apiDC string) (string, error) {
	return RegionForPlanWithContext(context.Background(), state, accountID, planCode, apiDC)
}

func RegionForPlanWithContext(ctx context.Context, state *app.State, accountID, planCode, apiDC string) (string, error) {
	if state == nil {
		return "", errors.New("区域解析缺少应用状态")
	}
	acc, _ := state.FindAccount(accountID)
	subsidiary := SubsidiaryOfAccount(acc)
	cat, err := loadSubsidiaryCatalogContext(ctx, state, subsidiary)
	if err != nil {
		return "", err
	}
	plan, ok := cat.plans[strings.TrimSpace(planCode)]
	if !ok {
		return "", fmt.Errorf("%w: %s(%s)", ErrPlanNotInCatalog, planCode, subsidiary)
	}
	return pickRegion(plan, apiDC), nil
}

func AddonFamiliesForPlan(state *app.State, accountID, planCode string) (map[string][]string, error) {
	return AddonFamiliesForPlanWithContext(context.Background(), state, accountID, planCode)
}

func AddonFamiliesForPlanWithContext(ctx context.Context, state *app.State, accountID, planCode string) (map[string][]string, error) {
	if state == nil {
		return nil, errors.New("目录解析缺少应用状态")
	}
	acc, _ := state.FindAccount(accountID)
	subsidiary := SubsidiaryOfAccount(acc)
	cat, err := loadSubsidiaryCatalogContext(ctx, state, subsidiary)
	if err != nil {
		return nil, err
	}
	plan, ok := cat.plans[strings.TrimSpace(planCode)]
	if !ok {
		return nil, fmt.Errorf("%w: %s(%s)", ErrPlanNotInCatalog, planCode, subsidiary)
	}
	return cloneStringSliceMap(plan.addonFamilies), nil
}

func DatacentersForPlan(state *app.State, accountID, planCode string) ([]string, error) {
	return DatacentersForPlanWithContext(context.Background(), state, accountID, planCode)
}

func DatacentersForPlanWithContext(ctx context.Context, state *app.State, accountID, planCode string) ([]string, error) {
	if state == nil {
		return nil, errors.New("目录解析缺少应用状态")
	}
	acc, _ := state.FindAccount(accountID)
	subsidiary := SubsidiaryOfAccount(acc)
	cat, err := loadSubsidiaryCatalogContext(ctx, state, subsidiary)
	if err != nil {
		return nil, err
	}
	plan, ok := cat.plans[strings.TrimSpace(planCode)]
	if !ok {
		return nil, fmt.Errorf("%w: %s(%s)", ErrPlanNotInCatalog, planCode, subsidiary)
	}
	return append([]string(nil), plan.datacenters...), nil
}

// PreferredDatacenterForPlan selects a catalog-listed DC matching the account bucket.
// An empty result means callers should keep their existing default-DC behavior.
func PreferredDatacenterForPlan(state *app.State, accountID, planCode string) string {
	if strings.TrimSpace(planCode) == "" {
		return ""
	}
	dcs, err := DatacentersForPlan(state, accountID, planCode)
	if err != nil || len(dcs) == 0 {
		return ""
	}
	acc, _ := state.FindAccount(accountID)
	return pickDatacenter(dcs, regionBucketForSubsidiary(SubsidiaryOfAccount(acc)))
}

var preferredDCByBucket = map[string][]string{
	"europe":        {"gra", "rbx", "sbg"},
	"canada":        {"bhs"},
	"united_states": {"vin", "hil"},
}

func pickDatacenter(dcs []string, wantBucket string) string {
	if len(dcs) == 0 {
		return ""
	}
	for _, preferred := range preferredDCByBucket[wantBucket] {
		for _, dc := range dcs {
			if normalizeDCCity(dc) == preferred {
				return dc
			}
		}
	}
	for _, dc := range dcs {
		if regionBucketForDC(dc) == wantBucket {
			return dc
		}
	}
	return dcs[0]
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	if source == nil {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(source))
	for key, values := range source {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func pickRegion(plan planConfig, apiDC string) string {
	switch len(plan.regions) {
	case 0:
		return ""
	case 1:
		return plan.regions[0]
	}
	want := regionBucketForDC(apiDC)
	for _, region := range plan.regions {
		if strings.EqualFold(region, want) {
			return region
		}
	}
	return plan.regions[0]
}

func regionBucketForDC(dc string) string {
	d := normalizeDCCity(dc)
	switch d {
	case "bhs", "tor", "yyz", "sgp", "syd", "ynm", "mum":
		return "canada"
	case "vin", "hil":
		return "united_states"
	}
	switch {
	case strings.HasPrefix(d, "ca-"), strings.HasPrefix(d, "ap-"):
		return "canada"
	case strings.HasPrefix(d, "us-"):
		return "united_states"
	default:
		return "europe"
	}
}

var dcCityMap = map[string]struct{}{
	"bhs": {}, "tor": {}, "yyz": {}, "sgp": {}, "syd": {}, "ynm": {}, "mum": {},
	"vin": {}, "hil": {}, "gra": {}, "rbx": {}, "sbg": {}, "fra": {}, "lon": {}, "waw": {},
}

func normalizeDCCity(dc string) string {
	d := strings.ToLower(strings.TrimSpace(dc))
	if d == "" {
		return ""
	}
	if _, ok := dcCityMap[d]; ok {
		return d
	}
	parts := strings.Split(d, "-")
	for index := len(parts) - 1; index >= 0; index-- {
		if _, ok := dcCityMap[parts[index]]; ok {
			return parts[index]
		}
	}
	if len(d) > 3 {
		if _, ok := dcCityMap[d[:3]]; ok {
			return d[:3]
		}
	}
	return d
}

func NormalizeDCCity(dc string) string { return normalizeDCCity(dc) }

func regionBucketForSubsidiary(subsidiary string) string {
	switch ovh.SubsidiaryRegion(subsidiary) {
	case "US":
		return "united_states"
	case "CA":
		return "canada"
	default:
		return "europe"
	}
}

func FallbackRegion(apiDC, subsidiary string) string {
	dc := normalizeDCCity(apiDC)
	if dc == "" && !strings.EqualFold(strings.TrimSpace(subsidiary), "US") {
		return ""
	}
	if region := ovh.RegionForDCInSubsidiary(dc, subsidiary); region != "" {
		return region
	}
	if bucket := regionBucketForDC(apiDC); bucket != "united_states" {
		return bucket
	}
	return ""
}

func ResolveRegion(state *app.State, accountID, planCode, apiDC string) (string, string) {
	return ResolveRegionWithContext(context.Background(), state, accountID, planCode, apiDC)
}

func ResolveRegionWithContext(ctx context.Context, state *app.State, accountID, planCode, apiDC string) (string, string) {
	if state == nil {
		return "", "unknown"
	}
	if region, err := RegionForPlanWithContext(ctx, state, accountID, planCode, apiDC); err == nil {
		return region, "catalog"
	}
	acc, _ := state.FindAccount(accountID)
	return FallbackRegion(apiDC, SubsidiaryOfAccount(acc)), "fallback"
}

// WarmRegionCache preloads public catalog data for all configured account subsidiaries.
func WarmRegionCache(ctx context.Context, state *app.State) {
	if state == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state.AccountsMu.RLock()
	subsidiaries := make(map[string]struct{})
	for _, account := range state.Accounts {
		subsidiaries[SubsidiaryOfAccount(account)] = struct{}{}
	}
	state.AccountsMu.RUnlock()
	for subsidiary := range subsidiaries {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := loadSubsidiaryCatalogContext(ctx, state, subsidiary); err != nil && state.Logger != nil {
			state.Logger.Warn(fmt.Sprintf("[region] 预热 %s 目录失败（下单时会重试）", subsidiary), "purchase")
		}
	}
}

// RegionOfPlan probes public availability endpoints and distinguishes no record from probe failure.
func RegionOfPlan(state *app.State, planCode string, candidateRegions []string) (string, error) {
	return RegionOfPlanWithContext(context.Background(), state, planCode, candidateRegions)
}

func RegionOfPlanWithContext(ctx context.Context, state *app.State, planCode string, candidateRegions []string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for _, region := range dedupRegions(candidateRegions) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		has, err := regionHasPlanWithContext(ctx, state, planCode, region)
		if err != nil {
			lastErr = err
			continue
		}
		if has {
			return region, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", lastErr
}

func dedupRegions(candidateRegions []string) []string {
	out := make([]string, 0, len(candidateRegions))
	seen := map[string]struct{}{}
	for _, region := range candidateRegions {
		region = strings.ToUpper(strings.TrimSpace(region))
		if region == "" {
			continue
		}
		if _, exists := seen[region]; exists {
			continue
		}
		seen[region] = struct{}{}
		out = append(out, region)
	}
	return out
}

type availProbeEntry struct {
	has bool
	at  time.Time
}
type availProbeFailure struct {
	err error
	at  time.Time
}
type availProbeCall struct {
	done chan struct{}
	has  bool
	err  error
}

var (
	availProbeMu    sync.Mutex
	availProbeCache = map[string]availProbeEntry{}
	availProbeFail  = map[string]availProbeFailure{}
	availProbeCalls = map[string]*availProbeCall{}
)

func pruneAvailProbeLocked(now time.Time) {
	for key, entry := range availProbeCache {
		if now.Sub(entry.at) >= availProbeTTL {
			delete(availProbeCache, key)
		}
	}
	for key, entry := range availProbeFail {
		if now.Sub(entry.at) >= availProbeErrTTL {
			delete(availProbeFail, key)
		}
	}
	for len(availProbeCache)+len(availProbeFail) > maxAvailProbeEntries {
		oldestKey := ""
		oldestAt := now
		oldestKind := 0
		for key, entry := range availProbeCache {
			if oldestKind == 0 || entry.at.Before(oldestAt) {
				oldestKey, oldestAt, oldestKind = key, entry.at, 1
			}
		}
		for key, entry := range availProbeFail {
			if oldestKind == 0 || entry.at.Before(oldestAt) {
				oldestKey, oldestAt, oldestKind = key, entry.at, 2
			}
		}
		if oldestKind == 1 {
			delete(availProbeCache, oldestKey)
		} else if oldestKind == 2 {
			delete(availProbeFail, oldestKey)
		} else {
			return
		}
	}
}

// probeRegionHasPlan 保留两参数注入点，便于无网络测试替换；生产调用通过
// probeRegionHasPlanWithState 使用共享公共 transport。
var probeRegionHasPlan = func(region, planCode string) (bool, error) {
	return probeRegionHasPlanHTTP(context.Background(), &http.Client{Timeout: 20 * time.Second}, region, planCode)
}

var probeRegionHasPlanWithState = func(ctx context.Context, state *app.State, region, planCode string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if state == nil || state.OVH == nil {
		return probeRegionHasPlan(region, planCode)
	}
	client, err := state.OVH.SharedHTTPClient(20 * time.Second)
	if err != nil {
		return false, err
	}
	return probeRegionHasPlanHTTP(ctx, client, region, planCode)
}

func probeRegionHasPlanHTTP(ctx context.Context, client *http.Client, region, planCode string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query := url.Values{}
	query.Set("planCode", planCode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ovh.APIBaseURLForRegion(region)+"/v1/dedicated/server/datacenter/availabilities?"+query.Encode(), nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var records []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCatalogResponseBytes)).Decode(&records); err != nil {
		return false, err
	}
	return len(records) > 0, nil
}

func regionHasPlan(state *app.State, planCode, region string) (bool, error) {
	return regionHasPlanWithContext(context.Background(), state, planCode, region)
}

func regionHasPlanWithContext(ctx context.Context, state *app.State, planCode, region string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key := strings.TrimSpace(planCode) + "\x00" + strings.ToUpper(strings.TrimSpace(region))
	availProbeMu.Lock()
	pruneAvailProbeLocked(time.Now())
	if cached, ok := availProbeCache[key]; ok && time.Since(cached.at) < availProbeTTL {
		availProbeMu.Unlock()
		return cached.has, nil
	}
	if failure, ok := availProbeFail[key]; ok && time.Since(failure.at) < availProbeErrTTL {
		availProbeMu.Unlock()
		return false, failure.err
	}
	if call, ok := availProbeCalls[key]; ok {
		availProbeMu.Unlock()
		select {
		case <-call.done:
			return call.has, call.err
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	call := &availProbeCall{done: make(chan struct{})}
	availProbeCalls[key] = call
	availProbeMu.Unlock()

	has, err := probeRegionHasPlanWithState(ctx, state, region, planCode)
	availProbeMu.Lock()
	delete(availProbeCalls, key)
	if err == nil {
		availProbeCache[key] = availProbeEntry{has: has, at: time.Now()}
		delete(availProbeFail, key)
	} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		availProbeFail[key] = availProbeFailure{err: err, at: time.Now()}
	}
	pruneAvailProbeLocked(time.Now())
	call.has, call.err = has, err
	availProbeMu.Unlock()
	close(call.done)
	return has, err
}
