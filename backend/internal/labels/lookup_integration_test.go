package labels_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLabelLookup covers WP-762 (INV-03) against PostgreSQL: the URN, the
// barcode and the inventory number resolve per kind over barcode_registry
// (WP-289); the object of another client or another site is 404, like an
// unknown code and a malformed URN; a principal without stock:read finds
// no consumable code but asset codes; an organization without stock finds
// no consumable; an organization without barcode is refused with 403
// feature-not-entitled.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLabelLookup(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// warehouse creates a site of the client with a warehouse carrying the
	// barcode and returns site and warehouse.
	warehouse := func(clientID, name, code string) (site, wh string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, clientID, name).Scan(&site); err != nil {
			t.Fatalf("seed site %s: %v", name, err)
		}
		if err := f.Admin.QueryRow(bg, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, 'warehouse', $2, $3) RETURNING id::text`,
			f.OrgA, site, name+"-wh").Scan(&wh); err != nil {
			t.Fatalf("seed warehouse %s: %v", name, err)
		}
		exec(`UPDATE location SET barcode = $2 WHERE id = $1`, wh, code)
		return site, wh
	}
	siteA, whA := warehouse(f.Client1, "lk-site-a", "LK-LOC-A")
	_, whB := warehouse(f.Client1, "lk-site-b", "LK-LOC-B")
	_, whC := warehouse(f.Client2, "lk-site-c", "LK-LOC-C")

	// asset1 at site A, asset3 at site B (client 1), asset2 of client 2.
	asset1 := f.Asset(t, f.OrgA, f.Client1, "LK-INV-1")
	exec(`UPDATE asset SET barcode = 'LK-BC-1', location_id = $2 WHERE id = $1`, asset1, whA)
	asset2 := f.Asset(t, f.OrgA, f.Client2, "LK-INV-2")
	asset3 := f.Asset(t, f.OrgA, f.Client1, "LK-INV-3")
	exec(`UPDATE asset SET location_id = $2 WHERE id = $1`, asset3, whB)
	toner := f.ID()
	exec(`INSERT INTO consumable (id, organization_id, name, barcode) VALUES ($1, $2, 'lk-toner', 'LK-CONS')`, toner, f.OrgA)

	repo := labels.NewPGRepository(f.App)
	full := lookupRoute(repo, entitled(t, f.OrgA, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic, entitlement.FeatureStock))
	scoped := func(scope func() context.Context) func(perms ...identity.Permission) caller {
		return func(perms ...identity.Permission) caller { return caller{scope: scope, org: f.OrgA, perms: perms} }
	}
	orgWide := scoped(func() context.Context { return f.OrgCtx(f.OrgA) })
	client1 := scoped(func() context.Context { return f.ClientCtx(f.Client1) })
	onSiteA := scoped(func() context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(siteA)
		return database.ContextWithTenantScope(context.Background(), &scope)
	})

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

	asset1Obj := labels.Object{Kind: labels.KindAsset, ID: asset1, Name: "LK-INV-1", LocationID: whA}
	asset2Obj := labels.Object{Kind: labels.KindAsset, ID: asset2, Name: "LK-INV-2"}
	tonerObj := labels.Object{Kind: labels.KindConsumable, ID: toner, Name: "lk-toner"}
	whAObj := labels.Object{Kind: labels.KindLocation, ID: whA, Name: "lk-site-a-wh"}

	// Every kind over URN and register, for an org-wide caller.
	want(t, "asset URN", lookup(t, full, orgWide(allReads...), "urn:reticora:asset:"+asset1), asset1Obj)
	want(t, "asset barcode", lookup(t, full, orgWide(allReads...), "LK-BC-1"), asset1Obj)
	want(t, "asset inventory number", lookup(t, full, orgWide(allReads...), "LK-INV-1"), asset1Obj)
	want(t, "asset of client 2", lookup(t, full, orgWide(allReads...), "LK-INV-2"), asset2Obj)
	want(t, "consumable URN", lookup(t, full, orgWide(allReads...), "urn:reticora:consumable:"+toner), tonerObj)
	want(t, "consumable barcode", lookup(t, full, orgWide(allReads...), "LK-CONS"), tonerObj)
	want(t, "location URN", lookup(t, full, orgWide(allReads...), "urn:reticora:location:"+whA), whAObj)
	want(t, "location barcode", lookup(t, full, orgWide(allReads...), "LK-LOC-A"), whAObj)

	// Client scope: the objects of client 2 are not found, over the
	// register and over the URN.
	want(t, "client 1: own asset", lookup(t, full, client1(allReads...), "LK-BC-1"), asset1Obj)
	notFound(t, "client 1: asset of client 2", lookup(t, full, client1(allReads...), "LK-INV-2"))
	notFound(t, "client 1: asset URN of client 2", lookup(t, full, client1(allReads...), "urn:reticora:asset:"+asset2))
	notFound(t, "client 1: location of client 2", lookup(t, full, client1(allReads...), "LK-LOC-C"))
	notFound(t, "client 1: location URN of client 2", lookup(t, full, client1(allReads...), "urn:reticora:location:"+whC))

	// Site scope: the objects of site B are not found.
	want(t, "site A: own asset", lookup(t, full, onSiteA(allReads...), "LK-INV-1"), asset1Obj)
	want(t, "site A: own location", lookup(t, full, onSiteA(allReads...), "LK-LOC-A"), whAObj)
	notFound(t, "site A: asset at site B", lookup(t, full, onSiteA(allReads...), "LK-INV-3"))
	notFound(t, "site A: asset URN at site B", lookup(t, full, onSiteA(allReads...), "urn:reticora:asset:"+asset3))
	notFound(t, "site A: location of site B", lookup(t, full, onSiteA(allReads...), "LK-LOC-B"))
	notFound(t, "site A: location URN of site B", lookup(t, full, onSiteA(allReads...), "urn:reticora:location:"+whB))

	// Without stock:read no consumable, asset codes still resolve.
	readNoStock := []identity.Permission{identity.PermAssetRead, identity.PermLocationRead}
	notFound(t, "consumable without stock:read", lookup(t, full, orgWide(readNoStock...), "LK-CONS"))
	notFound(t, "consumable URN without stock:read", lookup(t, full, orgWide(readNoStock...), "urn:reticora:consumable:"+toner))
	want(t, "asset without stock:read", lookup(t, full, orgWide(readNoStock...), "LK-BC-1"), asset1Obj)

	// An organization without stock finds no consumable.
	noStock := lookupRoute(repo, entitled(t, f.OrgA, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic))
	notFound(t, "consumable without stock", lookup(t, noStock, orgWide(allReads...), "LK-CONS"))
	want(t, "asset without stock", lookup(t, noStock, orgWide(allReads...), "LK-INV-1"), asset1Obj)

	// An organization without barcode: 403 feature-not-entitled.
	noBarcode := lookupRoute(repo, entitled(t, f.OrgA, entitlement.FeatureAssetsBasic, entitlement.FeatureStock))
	if got := lookup(t, noBarcode, orgWide(allReads...), "LK-INV-1"); got.status != http.StatusForbidden ||
		got.problem.Type != api.ProblemFeatureNotEntitled {
		t.Errorf("without barcode: %d %+v, want 403 feature-not-entitled", got.status, got.problem)
	}

	// Unknown codes, malformed URNs, a URN of the wrong kind and the codes
	// of organization A for a caller of organization B.
	for _, c := range []string{
		"LK-NONE", "lk-bc-1", "urn:reticora:asset:not-a-uuid", "urn:reticora:ticket:" + asset1,
		"urn:reticora:consumable:" + asset1, "urn:reticora:asset:" + f.ID(), "LK\x00INV",
	} {
		notFound(t, "code "+c, lookup(t, full, orgWide(allReads...), c))
	}
	otherOrg := caller{scope: func() context.Context { return f.OrgCtx(f.OrgB) }, org: f.OrgB, perms: allReads}
	otherFull := lookupRoute(repo, entitled(t, f.OrgB, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic, entitlement.FeatureStock))
	notFound(t, "organization B: code of A", lookup(t, otherFull, otherOrg, "LK-INV-1"))
	notFound(t, "organization B: URN of A", lookup(t, otherFull, otherOrg, "urn:reticora:asset:"+asset1))
}

// TestLabelLookupUniformNotFound covers the enumeration case of WP-829
// (SEC-11) against PostgreSQL: for a caller of client 1, the code of an
// object of another organization, of another client and a code that does
// not exist answer identically, status, Content-Type and body, over the
// register (barcode, inventory number) and over the URN; the answer does
// not reveal whether a code exists.
func TestLabelLookupUniformNotFound(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	own := f.Asset(t, f.OrgA, f.Client1, "LK829-INV-OWN")
	exec(`UPDATE asset SET barcode = 'LK829-BC-OWN' WHERE id = $1`, own)
	otherClient := f.Asset(t, f.OrgA, f.Client2, "LK829-INV-C2")
	exec(`UPDATE asset SET barcode = 'LK829-BC-C2' WHERE id = $1`, otherClient)
	otherOrg := f.Asset(t, f.OrgB, "", "LK829-INV-B")
	exec(`UPDATE asset SET barcode = 'LK829-BC-B' WHERE id = $1`, otherOrg)

	route := lookupRoute(labels.NewPGRepository(f.App),
		entitled(t, f.OrgA, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic, entitlement.FeatureStock))
	// raw sends the code as a client-1 user of organization A with a fixed
	// trace id, so bodies compare verbatim.
	raw := func(code string) *httptest.ResponseRecorder {
		t.Helper()
		ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: f.Client1})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: f.OrgA, Permissions: allReads,
			Type: identity.PrincipalTypeUser})
		w := httptest.NewRecorder()
		w.Header().Set(api.RequestIDHeader, "wp829")
		route.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/labels/lookup?code="+url.QueryEscape(code), http.NoBody).WithContext(ctx))
		return w
	}

	if w := raw("LK829-BC-OWN"); w.Code != http.StatusOK {
		t.Fatalf("own asset: %d %s, want 200", w.Code, w.Body.String())
	}
	missing := raw("LK829-NONE")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown code: %d %s, want 404", missing.Code, missing.Body.String())
	}
	for name, code := range map[string]string{
		"barcode of another organization":          "LK829-BC-B",
		"inventory number of another organization": "LK829-INV-B",
		"URN of another organization":              "urn:reticora:asset:" + otherOrg,
		"barcode of another client":                "LK829-BC-C2",
		"inventory number of another client":       "LK829-INV-C2",
		"URN of another client":                    "urn:reticora:asset:" + otherClient,
		"URN that does not exist":                  "urn:reticora:asset:" + f.ID(),
	} {
		w := raw(code)
		if w.Code != missing.Code || w.Body.String() != missing.Body.String() ||
			w.Header().Get("Content-Type") != missing.Header().Get("Content-Type") {
			t.Errorf("%s: %d %q %s, want %d %q %s as for an unknown code", name, w.Code, w.Header().Get("Content-Type"),
				w.Body.String(), missing.Code, missing.Header().Get("Content-Type"), missing.Body.String())
		}
	}
}
