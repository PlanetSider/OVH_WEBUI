package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/types"
)

func testExistingAccount() types.OVHAccount {
	return types.OVHAccount{
		ID: "account-1", Name: "old name", Endpoint: "ovh-eu", Zone: "IE",
		AppKey: "old-app-key", AppSecret: "old-app-secret", ConsumerKey: "old-consumer-key",
		IAM: "go-ovh-ie", IsDefault: true, CreatedAt: "2026-08-28T00:00:00Z",
	}
}

func TestMergeAccountUpdatePreservesOmittedFields(t *testing.T) {
	existing := testExistingAccount()
	in := accountInput{Name: "  renamed  "}
	in.normalizeUpdate()
	got := mergeAccountUpdate(existing, in)

	if got.Name != "renamed" {
		t.Fatalf("name = %q, want renamed", got.Name)
	}
	if got.Zone != existing.Zone || got.Endpoint != existing.Endpoint || got.IAM != existing.IAM {
		t.Fatalf("location fields changed: got zone=%q endpoint=%q iam=%q", got.Zone, got.Endpoint, got.IAM)
	}
	if got.AppKey != existing.AppKey || got.AppSecret != existing.AppSecret || got.ConsumerKey != existing.ConsumerKey {
		t.Fatal("credentials changed when omitted")
	}
	if !got.IsDefault {
		t.Fatal("omitting setDefault cleared the existing default account")
	}
}

func TestMergeAccountUpdateAllowsEndpointOnlyChange(t *testing.T) {
	existing := testExistingAccount()
	in := accountInput{Endpoint: "  ovh-us  "}
	in.normalizeUpdate()
	got := mergeAccountUpdate(existing, in)

	if got.Endpoint != "ovh-us" {
		t.Fatalf("endpoint = %q, want ovh-us", got.Endpoint)
	}
	if got.Zone != existing.Zone || got.IAM != existing.IAM {
		t.Fatalf("endpoint-only update changed zone or iam: zone=%q iam=%q", got.Zone, got.IAM)
	}
}

func TestMergeAccountUpdateDerivesEndpointAndIAMFromZone(t *testing.T) {
	existing := testExistingAccount()
	in := accountInput{Zone: " ca "}
	in.normalizeUpdate()
	got := mergeAccountUpdate(existing, in)

	if got.Zone != "CA" || got.Endpoint != "ovh-ca" || got.IAM != "go-ovh-ca" {
		t.Fatalf("derived fields = zone=%q endpoint=%q iam=%q", got.Zone, got.Endpoint, got.IAM)
	}
}

func TestMergeAccountUpdateCanPromoteButDoesNotDemoteDefault(t *testing.T) {
	nonDefault := testExistingAccount()
	nonDefault.IsDefault = false
	if got := mergeAccountUpdate(nonDefault, accountInput{SetDefault: true}); !got.IsDefault {
		t.Fatal("setDefault=true did not promote account")
	}

	existingDefault := testExistingAccount()
	if got := mergeAccountUpdate(existingDefault, accountInput{SetDefault: false}); !got.IsDefault {
		t.Fatal("setDefault=false demoted an existing default account")
	}
}

func TestAccountClientConfigChanged(t *testing.T) {
	base := testExistingAccount()

	metadataOnly := base
	metadataOnly.Name = "renamed"
	metadataOnly.IAM = "another-iam"
	metadataOnly.IsDefault = !base.IsDefault
	if accountClientConfigChanged(base, metadataOnly) {
		t.Fatal("metadata-only update should not require credential verification")
	}

	changed := base
	changed.Endpoint = "ovh-ca"
	if !accountClientConfigChanged(base, changed) {
		t.Fatal("endpoint update should require credential verification")
	}
	changed = base
	changed.ConsumerKey = "new-consumer-key"
	if !accountClientConfigChanged(base, changed) {
		t.Fatal("credential update should require credential verification")
	}
}

func TestSameAccountSnapshotDetectsConcurrentChanges(t *testing.T) {
	base := testExistingAccount()
	if !sameAccountSnapshot(base, base) {
		t.Fatal("identical account snapshots should match")
	}

	changed := base
	changed.Name = "concurrent rename"
	if sameAccountSnapshot(base, changed) {
		t.Fatal("concurrently changed account snapshots should not match")
	}
}

func TestSetDefaultAccountByIDReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	state := &app.State{DB: database}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/accounts/missing/set-default", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "missing"}}

	SetDefaultAccountByID(state)(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
}

func TestDeleteAccountByIDReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	state := &app.State{DB: database}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/accounts/missing", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "missing"}}

	DeleteAccountByID(state, nil)(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
}
