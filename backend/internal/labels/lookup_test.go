package labels_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// lookupResult is one answer of GET /api/v1/labels/lookup.
type lookupResult struct {
	status  int
	object  labels.Object
	problem api.ProblemDetail
}

// entitled returns an entitlement service under which org holds the
// features.
func entitled(t *testing.T, org string, features ...string) *entitlement.Service {
	t.Helper()
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	for _, f := range features {
		if _, err := svc.Grant(context.Background(), &entitlement.Entitlement{OrganizationID: org, FeatureKey: f,
			Plan: entitlement.PlanStandard, Enabled: true}); err != nil {
			t.Fatalf("grant %s: %v", f, err)
		}
	}
	return svc
}

// lookupRoute builds the lookup route behind the entitlement middleware
// of the service, as the server mounts it.
func lookupRoute(repo labels.Repository, svc *entitlement.Service) http.Handler {
	mux := chi.NewRouter()
	labels.NewHandler(repo).WithFeatures(svc).RegisterRoutes(mux)
	return svc.Middleware(mux)
}

// caller is the user a lookup is sent as: the request context (with the
// tenant scope in the PostgreSQL tests), the organization and the
// permissions.
type caller struct {
	scope func() context.Context
	org   string
	perms []identity.Permission
}

// lookup sends the code as the caller; a code of "-" sends no code
// parameter.
func lookup(t *testing.T, h http.Handler, c caller, code string) lookupResult {
	t.Helper()
	target := "/api/v1/labels/lookup"
	if code != "-" {
		target += "?code=" + url.QueryEscape(code)
	}
	ctx := tenant.WithTenant(c.scope(), tenant.TenantInfo{OrganizationID: c.org})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: c.org, Permissions: c.perms,
		Type: identity.PrincipalTypeUser})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, http.NoBody).WithContext(ctx))
	res := lookupResult{status: w.Code}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &res.object); err != nil {
			t.Fatalf("lookup %q: %v", code, err)
		}
	} else if err := json.Unmarshal(w.Body.Bytes(), &res.problem); err != nil {
		t.Fatalf("lookup %q: problem %q: %v", code, w.Body.String(), err)
	}
	return res
}

var allReads = []identity.Permission{identity.PermAssetRead, identity.PermStockRead, identity.PermLocationRead}

// TestParseURN covers the object URN of E-52.
func TestParseURN(t *testing.T) {
	const id = "0b4f7c2e-1d3a-4e5f-8a9b-0c1d2e3f4a5b"
	cases := []struct {
		code        string
		isURN, ok   bool
		kind, refID string
	}{
		{"urn:reticora:asset:" + id, true, true, labels.KindAsset, id},
		{"URN:Reticora:Consumable:" + strings.ToUpper(id), true, true, labels.KindConsumable, id},
		{"urn:reticora:location:" + id, true, true, labels.KindLocation, id},
		{"urn:reticora:ticket:" + id, true, false, "", ""},
		{"urn:reticora:asset:not-a-uuid", true, false, "", ""},
		{"urn:reticora:asset", true, false, "", ""},
		{"urn:reticora:", true, false, "", ""},
		{"INV-0001", false, false, "", ""},
		{"urn:other:asset:" + id, false, false, "", ""},
	}
	for _, c := range cases {
		ref, isURN, ok := labels.ParseURN(c.code)
		if isURN != c.isURN || ok != c.ok || ref.Kind != c.kind || ref.ID != c.refID {
			t.Errorf("ParseURN(%q) = %+v, %v, %v; want %s %s, %v, %v", c.code, ref, isURN, ok, c.kind, c.refID, c.isURN, c.ok)
		}
	}
}

// TestLookupMemoryProfile covers WP-762 (INV-03, API-01) in the memory
// profile: URN, barcode and inventory number resolve per kind; a missing
// read permission or feature of the kind, an unknown code, a malformed URN
// and an object of another organization answer 404 alike; without barcode
// the path is 403 feature-not-entitled; without code 422.
func TestLookupMemoryProfile(t *testing.T) {
	const org, other = "org-wp762", "org-wp762-other"
	ctx := context.Background()
	repos := server.MemoryRepositories()

	site, err := repos.LocationTree.Create(ctx, org, locations.CreateRequest{Kind: locations.KindSite, ClientID: "client-1", Name: "lk-site"})
	if err != nil {
		t.Fatal(err)
	}
	wh, err := repos.LocationTree.Create(ctx, org, locations.CreateRequest{Kind: locations.KindWarehouse, ParentID: site.ID, Name: "lk-wh"})
	if err != nil {
		t.Fatal(err)
	}
	code := "LK-LOC"
	if _, err = repos.LocationTree.Update(ctx, org, wh.ID, locations.UpdateRequest{Barcode: &code}); err != nil {
		t.Fatal(err)
	}
	laptop := &asset.Asset{OrganizationID: org, AssetTag: "LK-INV-1", Name: "lk-laptop", Barcode: "LK-BC-1", LocationID: wh.ID}
	foreign := &asset.Asset{OrganizationID: other, AssetTag: "LK-INV-9", Name: "lk-foreign"}
	for _, a := range []*asset.Asset{laptop, foreign} {
		if err = repos.Asset.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	toner := &consumable.Consumable{OrganizationID: org, Name: "lk-toner", Barcode: "LK-CONS"}
	if err = repos.Consumable.Create(ctx, toner); err != nil {
		t.Fatal(err)
	}

	as := func(perms ...identity.Permission) caller {
		return caller{scope: context.Background, org: org, perms: perms}
	}
	full := lookupRoute(repos.Labels, entitled(t, org, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic, entitlement.FeatureStock))
	want := func(t *testing.T, name string, got lookupResult, obj labels.Object) {
		t.Helper()
		if got.status != http.StatusOK || got.object != obj {
			t.Errorf("%s: %d %+v %+v, want 200 %+v", name, got.status, got.object, got.problem, obj)
		}
	}
	notFound := func(t *testing.T, name string, got lookupResult) {
		t.Helper()
		if got.status != http.StatusNotFound || got.problem.Detail != "no object found for the code" {
			t.Errorf("%s: %d %+v, want 404 without detail on the object", name, got.status, got.problem)
		}
	}

	laptopObj := labels.Object{Kind: labels.KindAsset, ID: laptop.ID, Name: "lk-laptop", LocationID: wh.ID}
	tonerObj := labels.Object{Kind: labels.KindConsumable, ID: toner.ID, Name: "lk-toner"}
	whObj := labels.Object{Kind: labels.KindLocation, ID: wh.ID, Name: "lk-wh"}
	want(t, "asset URN", lookup(t, full, as(allReads...), "urn:reticora:asset:"+laptop.ID), laptopObj)
	want(t, "asset barcode", lookup(t, full, as(allReads...), "LK-BC-1"), laptopObj)
	want(t, "asset inventory number", lookup(t, full, as(allReads...), " LK-INV-1 "), laptopObj)
	want(t, "consumable barcode", lookup(t, full, as(allReads...), "LK-CONS"), tonerObj)
	want(t, "location URN", lookup(t, full, as(allReads...), "urn:reticora:location:"+wh.ID), whObj)
	want(t, "location barcode", lookup(t, full, as(allReads...), "LK-LOC"), whObj)

	// The read permission of the kind: without stock:read the consumable
	// is not found, the asset is; without location:read no location.
	notFound(t, "consumable without stock:read", lookup(t, full, as(identity.PermAssetRead, identity.PermLocationRead), "LK-CONS"))
	want(t, "asset without stock:read", lookup(t, full, as(identity.PermAssetRead), "LK-BC-1"), laptopObj)
	notFound(t, "location without location:read", lookup(t, full, as(identity.PermAssetRead, identity.PermStockRead), "LK-LOC"))
	notFound(t, "asset without asset:read", lookup(t, full, as(identity.PermLocationRead), "urn:reticora:asset:"+laptop.ID))

	// The feature of the kind: without stock no consumable, without
	// assets_basic no asset; locations are core.
	noStock := lookupRoute(repos.Labels, entitled(t, org, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic))
	notFound(t, "consumable without stock", lookup(t, noStock, as(allReads...), "LK-CONS"))
	want(t, "asset without stock", lookup(t, noStock, as(allReads...), "LK-INV-1"), laptopObj)
	barcodeOnly := lookupRoute(repos.Labels, entitled(t, org, entitlement.FeatureBarcode))
	notFound(t, "asset without assets_basic", lookup(t, barcodeOnly, as(allReads...), "LK-INV-1"))
	want(t, "location with barcode only", lookup(t, barcodeOnly, as(allReads...), "LK-LOC"), whObj)

	// Without barcode the path is refused by the entitlement middleware.
	noBarcode := lookupRoute(repos.Labels, entitled(t, org, entitlement.FeatureAssetsBasic, entitlement.FeatureStock))
	if got := lookup(t, noBarcode, as(allReads...), "LK-INV-1"); got.status != http.StatusForbidden || got.problem.Type != api.ProblemFeatureNotEntitled {
		t.Errorf("without barcode: %d %+v, want 403 feature-not-entitled", got.status, got.problem)
	}

	// Unknown codes, malformed URNs and objects of another organization.
	for _, c := range []string{
		"LK-NONE", "lk-inv-1", "urn:reticora:asset:not-a-uuid", "urn:reticora:ticket:" + laptop.ID,
		"urn:reticora:asset:" + foreign.ID, "LK-INV-9", "urn:reticora:location:" + laptop.ID, "LK\x00INV",
	} {
		notFound(t, "code "+c, lookup(t, full, as(allReads...), c))
	}

	// Without code 422 on the field.
	for _, c := range []string{"-", "", "   "} {
		if got := lookup(t, full, as(allReads...), c); got.status != http.StatusUnprocessableEntity {
			t.Errorf("code %q: %d %+v, want 422", c, got.status, got.problem)
		}
	}

	// Without a resolver the route answers 503.
	unwired := lookupRoute(nil, entitled(t, org, entitlement.FeatureBarcode))
	if got := lookup(t, unwired, as(allReads...), "LK-INV-1"); got.status != http.StatusServiceUnavailable {
		t.Errorf("without resolver: %d, want 503", got.status)
	}
}
