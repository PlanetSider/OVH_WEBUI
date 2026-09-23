package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	ovhsdk "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
)

const (
	TerminationNone         = "empty"
	TerminationAtExpiration = "terminateAtExpirationDate"
	TerminationAtEngagement = "terminateAtEngagementDate"
)

var terminationPolicies = map[string]bool{
	TerminationNone:         true,
	TerminationAtExpiration: true,
	TerminationAtEngagement: true,
}

func setTerminationPolicy(client *ovhsdk.Client, serviceID int64, policy string) error {
	return client.Put(fmt.Sprintf("/services/%d", serviceID), map[string]interface{}{
		"terminationPolicy": policy,
	}, nil)
}

// UpdateTerminationPolicy PUT /api/server-control/:service_name/termination-policy
func UpdateTerminationPolicy(state *app.State) gin.HandlerFunc {
	return terminationPolicyHandler(state, serviceIDForDedicated, "server_control", "服务器")
}

// UpdateVpsTerminationPolicy PUT /api/vps-control/:service_name/termination-policy
func UpdateVpsTerminationPolicy(state *app.State) gin.HandlerFunc {
	return terminationPolicyHandler(state, serviceIDForVps, "vps_control", "VPS")
}

func terminationPolicyHandler(
	state *app.State,
	resolveID func(*ovhsdk.Client, string) (int64, error),
	logSource, label string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		svc := c.Param("service_name")
		client, err := ovhClientFor(state, c)
		if err != nil {
			noOVHResp(c)
			return
		}

		var body struct {
			Policy string `json:"policy"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "请求体格式错误: " + err.Error()})
			return
		}
		body.Policy = strings.TrimSpace(body.Policy)
		if !terminationPolicies[body.Policy] {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "policy 必须是 empty / terminateAtExpirationDate / terminateAtEngagementDate 之一",
			})
			return
		}

		serviceID, err := resolveID(client, svc)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "取 serviceId 失败: " + err.Error()})
			return
		}
		if err := setTerminationPolicy(client, serviceID, body.Policy); err != nil {
			state.Logger.Error(label+" "+svc+" 设置终止策略失败: "+err.Error(), logSource)
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": err.Error()})
			return
		}

		message := "已设为到期终止：到期日之前照常使用，到期后销毁"
		switch body.Policy {
		case TerminationNone:
			message = "已取消终止，服务恢复正常续费"
		case TerminationAtEngagement:
			message = "已设为合同期结束时终止"
		}
		state.Logger.Warn(fmt.Sprintf("%s %s 终止策略改为 %s", label, svc, body.Policy), logSource)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": message, "policy": body.Policy})
	}
}

type lifecycleTerminationResponse struct {
	Billing struct {
		Lifecycle struct {
			Current struct {
				PendingActions  []string `json:"pendingActions"`
				TerminationDate string   `json:"terminationDate"`
			} `json:"current"`
		} `json:"lifecycle"`
	} `json:"billing"`
}

func parseLifecycleTermination(info lifecycleTerminationResponse) (scheduled bool, action, date string) {
	for _, candidate := range info.Billing.Lifecycle.Current.PendingActions {
		switch candidate {
		case "terminate", TerminationAtExpiration, TerminationAtEngagement, "deleteAtExpiration":
			return true, candidate, info.Billing.Lifecycle.Current.TerminationDate
		}
	}
	return false, "", ""
}

func lifecycleTermination(client *ovhsdk.Client, serviceID int64) (scheduled bool, action, date string, err error) {
	var info lifecycleTerminationResponse
	if err := client.Get(fmt.Sprintf("/services/%d", serviceID), &info); err != nil {
		return false, "", "", err
	}
	scheduled, action, date = parseLifecycleTermination(info)
	return scheduled, action, date, nil
}

// attachTerminationState enriches serviceInfo with the lifecycle state. A failed
// lifecycle read is explicit so the UI never silently treats an unknown state as clear.
func attachTerminationState(
	state *app.State,
	client *ovhsdk.Client,
	resolveID func(*ovhsdk.Client, string) (int64, error),
	svc, logSource string,
	out map[string]interface{},
) {
	serviceID, err := resolveID(client, svc)
	if err != nil {
		state.Logger.Warn(svc+" 取 serviceId 失败，终止状态未知: "+err.Error(), logSource)
		out["terminationStateUnknown"] = true
		return
	}
	scheduled, action, date, err := lifecycleTermination(client, serviceID)
	if err != nil {
		state.Logger.Warn(svc+" 读取生命周期失败，终止状态未知: "+err.Error(), logSource)
		out["terminationStateUnknown"] = true
		return
	}
	out["terminationScheduled"] = scheduled
	if action != "" {
		out["terminationAction"] = action
	}
	if date != "" {
		out["terminationDate"] = date
	}
}
