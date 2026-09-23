package vps

import (
	"reflect"
	"testing"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/config"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/logger"
	"github.com/ovh-webui/server/internal/types"
)

func TestBuildVPSConfigUsesCartValuesAndOmitsInvalidOS(t *testing.T) {
	required := []requiredItem{
		{Label: "vps_datacenter", Required: true, AllowedValues: []string{"GRA", "BHS"}},
		{Label: "region", Required: true, AllowedValues: []string{"canada", "europe"}},
		{Label: "vps_os", Required: false, AllowedValues: []string{"debian12", "ubuntu24"}},
	}
	got := buildVPSConfig(required, "BHS", "windows2022")
	want := []kv{{label: "vps_datacenter", value: "BHS"}, {label: "region", value: "canada"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildVPSConfig() = %#v, want %#v", got, want)
	}

	got = buildVPSConfig(required, "GRA", "ubuntu24")
	want = []kv{{label: "vps_datacenter", value: "GRA"}, {label: "region", value: "europe"}, {label: "vps_os", value: "ubuntu24"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildVPSConfig(valid OS) = %#v, want %#v", got, want)
	}
}

func TestValidateAutoOrderAccountRejectsCrossRegion(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	state := &app.State{
		DB: database, Config: config.New(database), Logger: logger.New(t.TempDir()+"/vps-purchase.log.json", nil),
		Accounts: []types.OVHAccount{{ID: "us-account", Endpoint: "ovh-us", Zone: "US", AppKey: "a", AppSecret: "b", ConsumerKey: "c"}},
	}
	if err := ValidateAutoOrderAccount(state, "IE", "us-account"); err == nil {
		t.Fatal("cross-region auto-order account was accepted")
	}
	if err := ValidateAutoOrderAccount(state, "US", "us-account"); err != nil {
		t.Fatalf("same-region auto-order account rejected: %v", err)
	}
}

func TestVPSSubscriptionOrderFieldsRoundTrip(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	original := types.VPSSubscription{
		ID: "vps-order-fields", PlanCode: "vps-current", OvhSubsidiary: "US", Datacenters: []string{"US-EAST-VA"},
		MonitorLinux: true, NotifyAvailable: true, AutoOrder: true, Quantity: 3, AutoPay: false,
		OS: "ubuntu24", AutoOrderAccountID: "us-account", LastStatus: map[string]string{},
		PendingNotify: map[string]string{}, PendingNotifyChannels: map[string][]string{}, History: []map[string]interface{}{},
		CreatedAt: types.NowISO(),
	}
	if err := database.UpsertVPSSubscription(original); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.ListVPSSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d VPS subscriptions, want 1", len(loaded))
	}
	got := loaded[0]
	if got.AutoOrder != original.AutoOrder || got.Quantity != original.Quantity || got.AutoPay != original.AutoPay ||
		got.OS != original.OS || got.AutoOrderAccountID != original.AutoOrderAccountID {
		t.Fatalf("order fields did not round-trip: got %#v, want %#v", got, original)
	}
}
