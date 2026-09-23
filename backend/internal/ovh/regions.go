package ovh

import "strings"

// subsidiaryRegions 是公开目录与库存站点的唯一归属表。
var subsidiaryRegions = map[string]string{
	"CZ": "EU", "DE": "EU", "ES": "EU", "EU": "EU", "FI": "EU", "FR": "EU",
	"GB": "EU", "IE": "EU", "IT": "EU", "LT": "EU", "MA": "EU", "NL": "EU",
	"PL": "EU", "PT": "EU", "SN": "EU", "TN": "EU",
	"ASIA": "CA", "AU": "CA", "CA": "CA", "IN": "CA", "QC": "CA",
	"SG": "CA", "WE": "CA", "WS": "CA",
	"US": "US",
}

func SubsidiaryRegion(sub string) string {
	if region, ok := subsidiaryRegions[strings.ToUpper(strings.TrimSpace(sub))]; ok {
		return region
	}
	return "EU"
}

func KnownSubsidiary(sub string) bool {
	_, ok := subsidiaryRegions[strings.ToUpper(strings.TrimSpace(sub))]
	return ok
}

func APIBaseURLForRegion(region string) string {
	switch strings.ToUpper(strings.TrimSpace(region)) {
	case "US":
		return "https://api.us.ovhcloud.com"
	case "CA":
		return "https://ca.api.ovh.com"
	default:
		return "https://eu.api.ovh.com"
	}
}

func CatalogBaseURLForSubsidiary(sub string) string {
	return APIBaseURLForRegion(SubsidiaryRegion(sub))
}

func DefaultSubsidiaryForEndpoint(endpoint string) string {
	switch strings.ToLower(strings.TrimSpace(endpoint)) {
	case "ovh-us":
		return "US"
	case "ovh-ca", "kimsufi-ca", "soyoustart-ca":
		return "CA"
	default:
		return "IE"
	}
}

func EndpointRegion(endpoint string) string {
	e := strings.ToLower(strings.TrimSpace(endpoint))
	switch {
	case e == "ovh-us":
		return "US"
	case strings.HasSuffix(e, "-ca"):
		return "CA"
	default:
		return "EU"
	}
}

func ManagerOrderURL(endpoint, orderID string) string {
	if strings.TrimSpace(orderID) == "" {
		return ""
	}
	host := "manager.eu.ovhcloud.com"
	switch EndpointRegion(endpoint) {
	case "US":
		host = "manager.us.ovhcloud.com"
	case "CA":
		host = "manager.ca.ovhcloud.com"
	}
	return "https://" + host + "/dedicated/#/billing/order?orderId=" + orderID
}

// RegionForDCInSubsidiary returns a catalog-valid region value for a short DC code.
// It is only a static fallback; catalog data remains authoritative when available.
func RegionForDCInSubsidiary(datacenter, subsidiary string) string {
	if strings.EqualFold(strings.TrimSpace(subsidiary), "US") {
		return "united_states"
	}
	dc := strings.ToLower(strings.TrimSpace(datacenter))
	for _, prefix := range []string{"bhs", "yyz", "sgp", "syd", "ynm", "mum"} {
		if strings.HasPrefix(dc, prefix) {
			return "canada"
		}
	}
	for _, prefix := range []string{"gra", "rbx", "sbg", "eri", "lim", "waw", "par", "fra", "lon", "eu-west", "eu-central", "eu-south"} {
		if strings.HasPrefix(dc, prefix) {
			return "europe"
		}
	}
	// 美国机房与非 US 子公司不构成可订购组合，返回空让调用方查询
	// requiredConfiguration，而不是提交错误的 region。
	if strings.HasPrefix(dc, "vin") || strings.HasPrefix(dc, "hil") {
		return ""
	}
	return ""
}
