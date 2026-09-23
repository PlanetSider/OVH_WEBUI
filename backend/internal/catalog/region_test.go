package catalog

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/ovh"
)

const catalogFixture = `{"plans":[
 {"planCode":"us-plan","configurations":[
  {"name":"dedicated_datacenter","values":["vin","hil"]},
  {"name":"region","values":["united_states"]}],
  "addonFamilies":[{"name":"memory","addons":["ram-64g-us"]}]},
 {"planCode":"eu-plan","configurations":[
  {"name":"dedicated_datacenter","values":["bhs","fra","gra"]},
  {"name":"region","values":["canada","europe"]}]}
]}`

func TestParseEcoCatalogAndPickRegion(t *testing.T) {
	plans, err := parseEcoCatalog(strings.NewReader(catalogFixture))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := pickRegion(plans["us-plan"], "gra"); got != "united_states" {
		t.Fatalf("US plan region = %q, want united_states", got)
	}
	if got := pickRegion(plans["eu-plan"], "bhs"); got != "canada" {
		t.Fatalf("Canadian DC region = %q, want canada", got)
	}
	if got := pickRegion(plans["eu-plan"], "gra"); got != "europe" {
		t.Fatalf("European DC region = %q, want europe", got)
	}
	if got := plans["us-plan"].addonFamilies["memory"][0]; got != "ram-64g-us" {
		t.Fatalf("addon family = %q, want ram-64g-us", got)
	}
}

func TestPickDatacenter(t *testing.T) {
	cases := []struct {
		name, bucket, want string
		dcs                []string
	}{
		{"欧洲主力优先", "europe", "gra", []string{"fra", "gra", "lon"}},
		{"加拿大主力优先", "canada", "bhs", []string{"fra", "bhs"}},
		{"只有亚太也要返回目录值", "europe", "sgp", []string{"sgp"}},
		{"美区主力优先", "united_states", "vin", []string{"hil", "vin"}},
		{"长机房码识别城市", "europe", "gra", []string{"eu-west-par-a", "gra"}},
		{"空目录", "europe", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickDatacenter(tc.dcs, tc.bucket); got != tc.want {
				t.Fatalf("pickDatacenter(%v, %q) = %q, want %q", tc.dcs, tc.bucket, got, tc.want)
			}
		})
	}
}

func TestRegionForDCInSubsidiaryUsesValidValues(t *testing.T) {
	for _, tc := range []struct {
		dc, subsidiary, want string
	}{
		{"gra", "US", "united_states"},
		{"vin", "US", "united_states"},
		{"gra", "IE", "europe"},
		{"sgp", "IE", "canada"},
	} {
		if got := ovh.RegionForDCInSubsidiary(tc.dc, tc.subsidiary); got != tc.want {
			t.Errorf("RegionForDCInSubsidiary(%q, %q) = %q, want %q", tc.dc, tc.subsidiary, got, tc.want)
		}
	}
}
func TestFallbackRegionDoesNotEmitLegacyLabels(t *testing.T) {
	for _, tc := range []struct {
		dc, subsidiary, want string
	}{
		{"gra", "US", "united_states"},
		{"sgp", "IE", "canada"},
		{"vin", "IE", ""},
		{"", "IE", ""},
	} {
		if got := FallbackRegion(tc.dc, tc.subsidiary); got != tc.want {
			t.Errorf("FallbackRegion(%q, %q) = %q, want %q", tc.dc, tc.subsidiary, got, tc.want)
		}
	}
}
