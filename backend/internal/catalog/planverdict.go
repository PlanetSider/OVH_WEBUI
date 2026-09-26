package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/ovh"
)

// PlanVerdict 是 planCode 在账户站点上的统一归属判定。
type PlanVerdict int

const (
	PlanVerdictOK PlanVerdict = iota
	PlanVerdictUnknown
	PlanVerdictCrossRegion
	PlanVerdictNotEco
	PlanVerdictNoSuchPlan
)

func (v PlanVerdict) String() string {
	switch v {
	case PlanVerdictOK:
		return "OK"
	case PlanVerdictUnknown:
		return "Unknown"
	case PlanVerdictCrossRegion:
		return "CrossRegion"
	case PlanVerdictNotEco:
		return "NotEco"
	case PlanVerdictNoSuchPlan:
		return "NoSuchPlan"
	default:
		return "Unknown"
	}
}

// ClassifyPlan 统一判断本地 Eco 下单链路是否能处理某个 planCode。
// 目录故障返回 Unknown，绝不把一次瞬断固化成永久失败。
func ClassifyPlan(state *app.State, accountID, planCode, logSource string) (PlanVerdict, string) {
	return ClassifyPlanWithContext(context.Background(), state, accountID, planCode, logSource)
}

func ClassifyPlanWithContext(ctx context.Context, state *app.State, accountID, planCode, logSource string) (PlanVerdict, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PlanVerdictUnknown, ""
	}
	if state == nil {
		return PlanVerdictUnknown, ""
	}
	account, _ := state.FindAccount(accountID)
	accountRegion := ovh.EndpointRegion(account.Endpoint)
	subsidiary := SubsidiaryOfAccount(account)

	_, catalogErr := AddonFamiliesForPlanWithContext(ctx, state, accountID, planCode)
	if catalogErr == nil {
		return PlanVerdictOK, ""
	}
	if !errors.Is(catalogErr, ErrPlanNotInCatalog) {
		if state.Logger != nil {
			state.Logger.Warn(fmt.Sprintf("判定 %s 归属时目录失败(%s): %s", planCode, subsidiary, ovh.ErrorSummary(catalogErr)), logSource)
		}
		return PlanVerdictUnknown, ""
	}

	region, probeErr := RegionOfPlanWithContext(ctx, state, planCode, []string{accountRegion, "EU", "US", "CA"})
	if probeErr != nil {
		if state.Logger != nil {
			state.Logger.Warn(fmt.Sprintf("探测 %s 归属失败: %s", planCode, ovh.ErrorSummary(probeErr)), logSource)
		}
		return PlanVerdictUnknown, ""
	}
	if region == "" {
		return PlanVerdictNoSuchPlan, fmt.Sprintf("机型 %s 在 EU、US、CA 三个站点都没有库存记录，planCode 可能拼错或已下架。", planCode)
	}
	if region != accountRegion {
		return PlanVerdictCrossRegion, fmt.Sprintf("机型 %s 属于 %s 区，账户 %s 在 %s 区；请更换本区 planCode 或账户。", planCode, region, account.Name, accountRegion)
	}
	return PlanVerdictNotEco, fmt.Sprintf("机型 %s 在 %s 区有库存记录，但不在 %s 子公司的 Eco 目录中，本工具无法通过 Eco 购物车下单。", planCode, accountRegion, subsidiary)
}
