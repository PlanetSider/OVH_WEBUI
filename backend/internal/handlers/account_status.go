package handlers

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

// AccountsStatus 返回全部账户的实时认证状态，并附带 /me 基础资料。
func AccountsStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		accounts, err := state.DB.ListAccounts()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		results := make([]gin.H, len(accounts))
		var wg sync.WaitGroup
		for i, account := range accounts {
			wg.Add(1)
			go func(index int, account types.OVHAccount) {
				defer wg.Done()
				item := gin.H{"id": account.ID, "name": account.Name, "alias": account.Name, "zone": account.Zone, "endpoint": account.Endpoint, "valid": false}
				client, clientErr := state.OVH.ClientFor(account.ID)
				if clientErr != nil {
					item["error"] = clientErr.Error()
					results[index] = item
					return
				}
				var me map[string]interface{}
				if err := client.Get("/me", &me); err != nil {
					item["error"] = err.Error()
					results[index] = item
					return
				}
				item["valid"] = true
				if email, ok := me["email"].(string); ok { item["email"] = email }
				if firstname, ok := me["firstname"].(string); ok { item["firstname"] = firstname }
				if name, ok := me["name"].(string); ok { item["ovhName"] = name }
				results[index] = item
			}(i, account)
		}
		wg.Wait()
		c.JSON(http.StatusOK, gin.H{"accounts": results, "total": len(results)})
	}
}
