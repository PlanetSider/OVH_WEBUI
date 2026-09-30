package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/exchange"
)

func GetExchangeStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if state == nil || state.Config == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "汇率服务不可用"})
			return
		}
		cfg := state.Config.Get()
		c.JSON(http.StatusOK, gin.H{
			"status":               "success",
			"provider":             exchange.NormalizeProvider(cfg.ExchangeProvider),
			"displayMode":          exchange.NormalizeDisplayMode(cfg.ExchangeDisplayMode),
			"active":               exchange.ProviderConfigured(cfg) && exchange.RatesFromConfig(cfg).Valid() && cfg.ExchangeStatus == "active",
			"rates":                gin.H{"eurCny": cfg.ExchangeEURCNY, "usdCny": cfg.ExchangeUSDCNY, "cadCny": cfg.ExchangeCADCNY},
			"updatedAt":            cfg.ExchangeRatesUpdatedAt,
			"error":                cfg.ExchangeError,
			"historicalConfigured": strings.TrimSpace(cfg.FrankfurterAPIKey) != "",
		})
	}
}

func GetExchangeRate(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		from := strings.TrimSpace(c.Query("from"))
		to := strings.TrimSpace(c.Query("to"))
		if from == "" || to == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "from 和 to 不能为空"})
			return
		}
		rate, ok := state.Exchange.CurrentRate(from, to)
		if !ok {
			c.JSON(http.StatusOK, gin.H{"status": "success", "available": false, "from": from, "to": to})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "available": true, "from": strings.ToUpper(from), "to": strings.ToUpper(to), "rate": rate})
	}
}

func GetHistoricalExchangeRate(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		date := strings.TrimSpace(c.Query("date"))
		base := strings.TrimSpace(c.Query("base"))
		quote := strings.TrimSpace(c.Query("quote"))
		if date == "" || base == "" || quote == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "date、base 和 quote 不能为空"})
			return
		}
		rate, err := state.Exchange.HistoricalRate(c.Request.Context(), date, base, quote)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"status": "success", "available": false, "date": date, "base": strings.ToUpper(base), "quote": strings.ToUpper(quote), "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "available": true, "date": date, "base": strings.ToUpper(base), "quote": strings.ToUpper(quote), "rate": rate})
	}
}
