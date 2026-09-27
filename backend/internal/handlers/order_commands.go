package handlers

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	ovhsdk "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/types"
)

const (
	maxBotOrderLimit      = 100
	maxBotOrderMessageLen = 3800
)

type botOrderQuery struct {
	limit     int
	monthOnly bool
	unpaid    bool
}

type botOrderRecord struct {
	ID     string
	Detail map[string]interface{}
	Status string
}

func parseBotOrderQuery(args []string) (botOrderQuery, string) {
	query := botOrderQuery{monthOnly: true}
	if len(args) == 0 {
		return query, ""
	}
	if len(args) > 1 {
		return botOrderQuery{}, "用法：/order [数量|unpaid]"
	}
	arg := strings.TrimSpace(args[0])
	if strings.EqualFold(arg, "unpaid") || arg == "未付款" || arg == "待付款" {
		query.unpaid = true
		return query, ""
	}
	limit, err := strconv.Atoi(arg)
	if err != nil || limit <= 0 {
		return botOrderQuery{}, "用法：/order [数量|unpaid]；数量必须是正整数"
	}
	if limit > maxBotOrderLimit {
		return botOrderQuery{}, fmt.Sprintf("订单数量最多为 %d 条", maxBotOrderLimit)
	}
	query.monthOnly = false
	query.limit = limit
	return query, ""
}

func commandOVHClient(state *app.State, accountID string) (*ovhsdk.Client, error) {
	if state == nil || state.OVH == nil {
		return nil, fmt.Errorf("OVH 客户端不可用")
	}
	if strings.TrimSpace(accountID) == "" {
		return nil, fmt.Errorf("尚未配置默认 OVH 账户")
	}
	client, err := state.OVH.ClientFor(accountID)
	if err != nil {
		return nil, fmt.Errorf("当前 OVH 账户不可用")
	}
	return client, nil
}

func cmdOrder(state *app.State, args []string, accountID string) string {
	query, errMessage := parseBotOrderQuery(args)
	if errMessage != "" {
		return errMessage
	}
	client, err := commandOVHClient(state, accountID)
	if err != nil {
		return "❌ " + err.Error()
	}
	records, err := fetchBotOrders(client, query)
	if err != nil {
		return "❌ 获取订单失败，请稍后重试"
	}
	if query.unpaid {
		records = filterUnpaidBotOrders(records)
	}
	if len(records) == 0 {
		if query.unpaid {
			return "📋 最近一个月没有未付款订单"
		}
		return "📋 最近一个月没有订单"
	}
	return formatBotOrders(records, query)
}

func cmdPay(state *app.State, args []string, accountID string) string {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "用法：/pay <订单号>"
	}
	orderID, err := parseOrderID(args[0])
	if err != nil {
		return "用法：/pay <订单号>；订单号必须是数字"
	}
	client, err := commandOVHClient(state, accountID)
	if err != nil {
		return "❌ " + err.Error()
	}
	if err := payBotOrderWithDefaultPaymentMethod(client, orderID); err != nil {
		return "❌ 订单 #" + orderID + " 支付失败：" + err.Error()
	}
	return "✅ 已提交订单 #" + orderID + " 的支付请求，请在 OVH 控制台确认支付状态。"
}

func parseOrderID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("empty order id")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return "", fmt.Errorf("invalid order id")
	}
	return strconv.FormatInt(parsed, 10), nil
}

func fetchBotOrders(client *ovhsdk.Client, query botOrderQuery) ([]botOrderRecord, error) {
	path := "/me/order"
	if query.monthOnly {
		values := url.Values{}
		now := time.Now().UTC()
		values.Set("date.from", now.AddDate(0, -1, 0).Format(time.RFC3339))
		values.Set("date.to", now.Format(time.RFC3339))
		path += "?" + values.Encode()
	}

	var ids []interface{}
	if err := client.Get(path, &ids); err != nil {
		return nil, err
	}
	sort.SliceStable(ids, func(i, j int) bool {
		return idToInt64(ids[i]) > idToInt64(ids[j])
	})
	if query.limit > 0 && len(ids) > query.limit {
		ids = ids[:query.limit]
	}

	records := make([]botOrderRecord, 0, len(ids))
	for _, rawID := range ids {
		id, err := parseOrderID(idToString(rawID))
		if err != nil {
			continue
		}
		detail := map[string]interface{}{}
		if err := client.Get("/me/order/"+id, &detail); err != nil {
			continue
		}
		status := orderMapString(detail, "status", "orderStatus")
		var statusResponse string
		if err := client.Get("/me/order/"+id+"/status", &statusResponse); err == nil && strings.TrimSpace(statusResponse) != "" {
			status = strings.TrimSpace(statusResponse)
		}
		records = append(records, botOrderRecord{ID: id, Detail: detail, Status: status})
	}
	sort.SliceStable(records, func(i, j int) bool {
		left := orderDate(records[i].Detail)
		right := orderDate(records[j].Detail)
		if !left.IsZero() && !right.IsZero() && !left.Equal(right) {
			return left.After(right)
		}
		return idToInt64(records[i].ID) > idToInt64(records[j].ID)
	})
	return records, nil
}

func filterUnpaidBotOrders(records []botOrderRecord) []botOrderRecord {
	filtered := make([]botOrderRecord, 0, len(records))
	for _, record := range records {
		if isUnpaidOrderStatus(record.Status) {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func isUnpaidOrderStatus(status string) bool {
	compact := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.TrimSpace(status)))
	switch compact {
	case "notpaid", "unpaid", "paymentpending", "pendingpayment", "waitingpayment", "未付款", "待付款":
		return true
	default:
		return false
	}
}

func formatBotOrders(records []botOrderRecord, query botOrderQuery) string {
	title := "📋 最近一个月订单"
	if query.limit > 0 {
		title = fmt.Sprintf("📋 最近 %d 条订单", query.limit)
	}
	if query.unpaid {
		title = "📋 最近一个月未付款订单"
	}
	var builder strings.Builder
	builder.WriteString(title)
	builder.WriteString(fmt.Sprintf("（共 %d 条）\n\n", len(records)))
	shown := 0
	for _, record := range records {
		block := fmt.Sprintf("订单号: #%s\n时间: %s\n状态: %s\n价格: %s\n",
			record.ID,
			formatOrderDate(record.Detail),
			fallbackText(record.Status, "未知"),
			formatOrderPrice(record.Detail),
		)
		if expiration := formatOrderFieldDate(record.Detail, "expirationDate"); expiration != "" {
			block += "到期时间: " + expiration + "\n"
		}
		if orderURL := orderMapString(record.Detail, "url"); orderURL != "" {
			block += "订单链接: " + orderURL + "\n"
		}
		if pdfURL := orderMapString(record.Detail, "pdfUrl"); pdfURL != "" {
			block += "PDF: " + pdfURL + "\n"
		}
		block += "\n"
		if builder.Len()+len(block) > maxBotOrderMessageLen {
			break
		}
		builder.WriteString(block)
		shown++
	}
	if shown < len(records) {
		builder.WriteString(fmt.Sprintf("……消息过长，仅显示前 %d 条，请使用 /order 数量分批查看。\n", shown))
	}
	return strings.TrimSpace(builder.String())
}

func formatOrderDate(detail map[string]interface{}) string {
	when := orderDate(detail)
	if when.IsZero() {
		return "未知"
	}
	return when.Local().Format("2006-01-02 15:04:05")
}

func formatOrderFieldDate(detail map[string]interface{}, key string) string {
	value := orderMapString(detail, key)
	if value == "" {
		return ""
	}
	parsed, ok := types.ParseTS(value)
	if !ok {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04:05")
}

func orderDate(detail map[string]interface{}) time.Time {
	for _, key := range []string{"date", "creationDate", "createdAt"} {
		if value := orderMapString(detail, key); value != "" {
			if parsed, ok := types.ParseTS(value); ok {
				return parsed
			}
		}
	}
	return time.Time{}
}

func formatOrderPrice(detail map[string]interface{}) string {
	for _, key := range []string{"priceWithTax", "priceWithoutTax"} {
		value, ok := detail[key].(map[string]interface{})
		if !ok {
			continue
		}
		if text := orderMapString(value, "text"); text != "" {
			return text
		}
		amount := orderMapString(value, "value")
		currency := orderMapString(value, "currencyCode")
		if amount != "" {
			return strings.TrimSpace(amount + " " + currency)
		}
	}
	return "未知"
}

func orderMapString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func payBotOrderWithDefaultPaymentMethod(client *ovhsdk.Client, orderID string) error {
	var methods []map[string]interface{}
	if err := client.Get("/me/payment/method?default=true", &methods); err != nil {
		return fmt.Errorf("获取默认支付方式失败")
	}
	if len(methods) == 0 {
		return fmt.Errorf("当前账户没有可用的默认支付方式")
	}

	method := methods[0]
	for _, candidate := range methods {
		if value, ok := candidate["default"].(bool); ok && value {
			method = candidate
			break
		}
	}
	paymentMethodID := paymentMethodIDValue(method)
	if paymentMethodID <= 0 {
		return fmt.Errorf("默认支付方式缺少 paymentMethodId")
	}
	payload := map[string]interface{}{"id": paymentMethodID}
	if err := client.Post("/me/order/"+orderID+"/pay", payload, nil); err != nil {
		return fmt.Errorf("OVH 支付接口拒绝请求")
	}
	return nil
}

func paymentMethodIDValue(method map[string]interface{}) int64 {
	for _, key := range []string{"paymentMethodId", "id"} {
		value, ok := method[key]
		if !ok || value == nil {
			continue
		}
		if number, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64); err == nil && number > 0 {
			return number
		}
	}
	return 0
}
