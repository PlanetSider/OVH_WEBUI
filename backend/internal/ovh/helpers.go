package ovh

import "strings"

// ConvertDisplayDCToAPIDC 将前端显示的数据中心代码转换为 OVH API 代码
// 对应 Python: _convert_display_dc_to_api_dc
func ConvertDisplayDCToAPIDC(datacenter string) string {
	if datacenter == "" {
		return "gra"
	}
	dcMap := map[string]string{
		"mum": "ynm", // 孟买：前端 mum，OVH API 用 ynm
	}
	lower := strings.ToLower(datacenter)
	if v, ok := dcMap[lower]; ok {
		return v
	}
	return lower
}
