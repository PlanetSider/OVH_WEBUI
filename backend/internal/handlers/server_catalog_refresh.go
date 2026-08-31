package handlers

import (
	"fmt"
	"sync"
	"time"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/catalog"
	"github.com/ovh-webui/server/internal/monitor"
	"github.com/ovh-webui/server/internal/types"
)

type serverCatalogRefreshCall struct {
	done  chan struct{}
	plans []types.ServerPlan
	err   error
}

var (
	serverCatalogRefreshMu       sync.Mutex
	serverCatalogRefreshInFlight *serverCatalogRefreshCall
)

// refreshServerCatalog 把所有完整目录刷新入口合并为同一个进行中的请求。
// 数据完整获取并持久化成功后才发布到内存，失败时保留上一份目录。
func refreshServerCatalog(state *app.State) ([]types.ServerPlan, error) {
	if state == nil || state.OVH == nil || state.DB == nil || state.ServerCache == nil {
		return nil, fmt.Errorf("服务器目录刷新依赖尚未初始化")
	}
	if !state.HasAnyAccount() {
		return nil, fmt.Errorf("未配置可用的 OVH 账户")
	}

	serverCatalogRefreshMu.Lock()
	if current := serverCatalogRefreshInFlight; current != nil {
		serverCatalogRefreshMu.Unlock()
		<-current.done
		return append([]types.ServerPlan(nil), current.plans...), current.err
	}
	call := &serverCatalogRefreshCall{done: make(chan struct{})}
	serverCatalogRefreshInFlight = call
	serverCatalogRefreshMu.Unlock()

	defer func() {
		serverCatalogRefreshMu.Lock()
		serverCatalogRefreshInFlight = nil
		close(call.done)
		serverCatalogRefreshMu.Unlock()
	}()

	plans := catalog.LoadServerList(state)
	if len(plans) == 0 {
		call.err = fmt.Errorf("OVH API 返回空服务器目录")
		return nil, call.err
	}
	if err := state.DB.ReplaceServers(plans); err != nil {
		call.err = fmt.Errorf("持久化服务器目录失败: %w", err)
		return nil, call.err
	}

	state.ServerPlansMu.Lock()
	state.ServerPlans = append([]types.ServerPlan(nil), plans...)
	state.ServerPlansMu.Unlock()
	state.ServerCache.Set(plans)
	call.plans = append([]types.ServerPlan(nil), plans...)
	return append([]types.ServerPlan(nil), plans...), nil
}

func notifyNewServers(mon *monitor.Monitor, plans []types.ServerPlan) {
	if mon == nil || len(plans) == 0 {
		return
	}
	snapshot := make([]map[string]interface{}, 0, len(plans))
	for _, server := range plans {
		snapshot = append(snapshot, map[string]interface{}{
			"planCode":  server.PlanCode,
			"name":      server.Name,
			"cpu":       server.CPU,
			"memory":    server.Memory,
			"storage":   server.Storage,
			"bandwidth": server.Bandwidth,
		})
	}
	mon.CheckNewServers(snapshot)
}

func refreshServerCatalogAndNotify(state *app.State, mon *monitor.Monitor, source string) error {
	plans, err := refreshServerCatalog(state)
	if err != nil {
		return err
	}
	notifyNewServers(mon, plans)
	if source == "" {
		source = "主动"
	}
	state.Logger.Info(fmt.Sprintf("%s刷新服务器目录成功，共 %d 个型号", source, len(plans)), "server_catalog")
	return nil
}

func nextHourlyRefresh(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, now.Location())
}

// runHourlyDataRefresh 同时启动预增批次与完整服务器目录刷新。预增批次仍只使用
// 自己当批获取的实时可用性和区域 catalog；完整目录不会参与或覆盖其比对数据。
func runHourlyDataRefresh(state *app.State, mon *monitor.Monitor) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		RefreshRealtimeAvailabilityOnce(state)
	}()
	go func() {
		defer wg.Done()
		if err := refreshServerCatalogAndNotify(state, mon, "整点"); err != nil {
			state.Logger.Warn("整点刷新服务器目录失败: "+err.Error(), "server_catalog")
		}
	}()
	wg.Wait()
}

// StartHourlyDataRefresh 启动统一整点调度。启动时只补齐缺失或过期的数据；
// 后续每个本机时区整点并行刷新预增批次和完整服务器目录。
func StartHourlyDataRefresh(state *app.State, mon *monitor.Monitor) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(done)
		var startupWG sync.WaitGroup
		startupWG.Add(2)
		go func() {
			defer startupWG.Done()
			if err := ensureRealtimeAvailabilitySnapshots(state); err != nil {
				state.Logger.Warn("实时可用性启动补采失败: "+err.Error(), "availability")
			}
		}()
		go func() {
			defer startupWG.Done()
			_, _, valid := state.ServerCache.Snapshot()
			if !valid {
				if err := refreshServerCatalogAndNotify(state, mon, "启动补采"); err != nil {
					state.Logger.Warn("服务器目录启动补采失败: "+err.Error(), "server_catalog")
				}
			}
		}()
		startupWG.Wait()

		for {
			timer := time.NewTimer(time.Until(nextHourlyRefresh(time.Now())))
			select {
			case <-stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
				runHourlyDataRefresh(state, mon)
			}
		}
	}()
	return func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}
}
