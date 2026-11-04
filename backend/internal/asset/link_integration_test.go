package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Retest (WP-272, AST-04, AST-03, AST-01):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssetCILink' ./internal/asset/

// linkWorld serves the asset routes with the links through the CI service
// (audit and outbox like production).
type linkWorld struct {
	f   *scopetest.Fixture
	mux *chi.Mux
	svc *ci.Service
}

func newLinkWorld(t *testing.T) *linkWorld {
	t.Helper()
	f := scopetest.Seed(t, "2b")
	svc := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()))
	mux := chi.NewRouter()
	asset.NewHandler(asset.NewPGRepository(f.App)).WithCILinks(svc).RegisterRoutes(mux)
	return &linkWorld{f: f, mux: mux, svc: svc}
}

// allPerms are the permissions of a full writer.
var allPerms = []identity.Permission{identity.PermAssetRead, identity.PermAssetWrite, identity.PermCIRead, identity.PermCIWrite}

// linkCall is one request of a principal with perms in the tenant scope of
// scope (organization A org-wide without one), from the API.
type linkCall struct {
	scope        context.Context
	perms        []identity.Permission
	method, path string
	body         string
	want         int
	wantField    string
}

func (w *linkWorld) do(t *testing.T, c *linkCall) *httptest.ResponseRecorder {
	t.Helper()
	ctx := c.scope
	if ctx == nil {
		ctx = w.f.OrgCtx(w.f.OrgA)
	}
	perms := c.perms
	if perms == nil {
		perms = allPerms
	}
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: w.f.User, OrganizationID: w.f.OrgA, Permissions: perms})
	ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(perms))
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp272", Source: audit.SourceAPI})
	req := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(c.body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	if rec.Code != c.want {
		t.Fatalf("%s %s %s: got %d %s, want %d", c.method, c.path, c.body, rec.Code, rec.Body.String(), c.want)
	}
	if c.wantField != "" {
		var p api.FieldProblem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Field != c.wantField {
			t.Fatalf("%s %s %s: problem %s, want field %q", c.method, c.path, c.body, rec.Body.String(), c.wantField)
		}
	}
	return rec
}

// link posts ci to the asset and returns the link.
func (w *linkWorld) link(t *testing.T, assetID, body string) asset.CILink {
	t.Helper()
	rec := w.do(t, &linkCall{method: http.MethodPost, path: assetsPath + "/" + assetID + "/cis", body: body, want: http.StatusCreated})
	var l asset.CILink
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatalf("decode link %s: %v", rec.Body.String(), err)
	}
	return l
}

func (w *linkWorld) text(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := w.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (w *linkWorld) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// ciAsset is ci.asset_id ("" without), primary asset.ci_id.
func (w *linkWorld) ciAsset(t *testing.T, ciID string) string {
	return w.text(t, `SELECT COALESCE(asset_id::text, '') FROM ci WHERE id = $1`, ciID)
}

func (w *linkWorld) primary(t *testing.T, assetID string) string {
	return w.text(t, `SELECT COALESCE(ci_id::text, '') FROM asset WHERE id = $1`, assetID)
}

func (w *linkWorld) version(t *testing.T, ciID string) int {
	return w.count(t, `SELECT version FROM ci WHERE id = $1`, ciID)
}

// changes counts the ci_change rows asset_id and the events ci.changed of
// the CI.
func (w *linkWorld) changes(t *testing.T, ciID string) (rows, changed int) {
	return w.count(t, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'asset_id'`, ciID),
		w.count(t, `SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'`, ciID)
}

func (w *linkWorld) manualLink(ctx context.Context, ciID string, req ci.UpdateRequest) error { //nolint:gocritic // test helper
	req.Manual = &ci.ManualChange{Author: w.f.User}
	_, err := w.svc.Update(ctx, w.f.OrgA, ciID, req)
	return err
}

func isViolation(err error, field string) bool {
	var v *ci.ValidationError
	return errors.As(err, &v) && len(v.Violations) > 0 && v.Violations[0].Field == field
}

// TestAssetCILink covers the asset↔CI link (WP-272): linking and unlinking
// through POST/DELETE /assets/{id}/cis, n CIs per asset, a consistent
// primary CI in both directions (also through PATCH /cis/{id}), the client
// check on every path, 404 for an invisible asset or CI, 403 for a scoped
// principal and an org-wide asset, the CI-side effects of a primary CI
// set by PATCH /assets/{id}, the audit entries and the permissions.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetCILink(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	org := f.OrgCtx(f.OrgA)
	cisOf := func(id string) string { return assetsPath + "/" + id + "/cis" }

	assetA := f.Asset(t, f.OrgA, f.Client1, "link-a")
	assetB := f.Asset(t, f.OrgA, f.Client1, "link-b")
	ci1 := f.CI(t, f.OrgA, f.Client1, "link-ci1")
	ci2 := f.CI(t, f.OrgA, "", "link-ci2") // org-wide: compatible with every client

	// Link: two CIs on one asset, the first one primary; each link is one
	// manual CI change.
	v1 := w.version(t, ci1)
	if l := w.link(t, assetA, `{"ci_id":"`+ci1+`","primary":true}`); l.AssetID != assetA || l.CIID != ci1 || !l.Primary {
		t.Fatalf("link ci1: %+v", l)
	}
	if l := w.link(t, assetA, `{"ci_id":"`+ci2+`"}`); l.Primary {
		t.Fatalf("link ci2: %+v", l)
	}
	if w.ciAsset(t, ci1) != assetA || w.ciAsset(t, ci2) != assetA || w.primary(t, assetA) != ci1 {
		t.Fatalf("links: ci1→%s ci2→%s primary %s", w.ciAsset(t, ci1), w.ciAsset(t, ci2), w.primary(t, assetA))
	}
	if rows, changed := w.changes(t, ci1); w.version(t, ci1) != v1+1 || rows != 1 || changed != 1 {
		t.Errorf("ci1 after the link: version %d (was %d), %d ci_change rows asset_id, %d ci.changed, want +1, 1, 1",
			w.version(t, ci1), v1, rows, changed)
	}
	// Linking again changes nothing on the CI.
	w.link(t, assetA, `{"ci_id":"`+ci1+`"}`)
	if w.version(t, ci1) != v1+1 {
		t.Errorf("relinking raised the version of ci1 to %d", w.version(t, ci1))
	}

	// A new primary CI keeps the previous one linked.
	w.link(t, assetA, `{"ci_id":"`+ci2+`","primary":true}`)
	if w.primary(t, assetA) != ci2 || w.ciAsset(t, ci1) != assetA {
		t.Fatalf("primary ci2: primary %s, ci1→%s", w.primary(t, assetA), w.ciAsset(t, ci1))
	}
	// primary false clears it, the CI stays linked.
	w.link(t, assetA, `{"ci_id":"`+ci2+`","primary":false}`)
	if w.primary(t, assetA) != "" || w.ciAsset(t, ci2) != assetA {
		t.Fatalf("primary false: primary %q, ci2→%s", w.primary(t, assetA), w.ciAsset(t, ci2))
	}
	w.link(t, assetA, `{"ci_id":"`+ci2+`","primary":true}`)

	// Moving the primary CI to another asset clears the old asset's
	// primary CI: through the link API ...
	w.link(t, assetB, `{"ci_id":"`+ci2+`"}`)
	if w.ciAsset(t, ci2) != assetB || w.primary(t, assetA) != "" {
		t.Fatalf("moved ci2: ci2→%s, primary of A %q", w.ciAsset(t, ci2), w.primary(t, assetA))
	}

	// ... and through PATCH /cis/{id}. The primary CI of PATCH /assets/{id}
	// is linked as one manual CI change.
	ci3 := f.CI(t, f.OrgA, f.Client1, "link-ci3")
	v3 := w.version(t, ci3)
	got := decodeAsset(t, w.do(t, &linkCall{method: http.MethodPatch, path: assetsPath + "/" + assetA,
		body: `{"primary_ci_id":"` + ci3 + `"}`, want: http.StatusOK}))
	if got.CIID != ci3 || w.ciAsset(t, ci3) != assetA || w.ciAsset(t, ci1) != assetA {
		t.Fatalf("PATCH primary_ci_id: %+v, ci3→%s, ci1→%s", got, w.ciAsset(t, ci3), w.ciAsset(t, ci1))
	}
	if rows, changed := w.changes(t, ci3); w.version(t, ci3) != v3+1 || rows != 1 || changed != 1 {
		t.Errorf("ci3 after PATCH /assets: version %d (was %d), %d ci_change rows asset_id, %d ci.changed, want +1, 1, 1",
			w.version(t, ci3), v3, rows, changed)
	}
	if err := w.manualLink(org, ci3, ci.UpdateRequest{AssetID: &assetB}); err != nil {
		t.Fatalf("PATCH /cis ci3 to B: %v", err)
	}
	if w.ciAsset(t, ci3) != assetB || w.primary(t, assetA) != "" {
		t.Fatalf("ci3 moved by PATCH /cis: ci3→%s, primary of A %q", w.ciAsset(t, ci3), w.primary(t, assetA))
	}
	empty := ""
	w.link(t, assetA, `{"ci_id":"`+ci1+`","primary":true}`)
	if err := w.manualLink(org, ci1, ci.UpdateRequest{AssetID: &empty}); err != nil || w.primary(t, assetA) != "" {
		t.Fatalf("ci1 unlinked by PATCH /cis: %v, primary of A %q", err, w.primary(t, assetA))
	}

	// POST /assets with primary_ci_id links in one call (1:1).
	ci4 := f.CI(t, f.OrgA, f.Client1, "link-ci4")
	created := decodeAsset(t, w.do(t, &linkCall{method: http.MethodPost, path: assetsPath,
		body: `{"inventory_no":"LINK-C","name":"one call","client_id":"` + f.Client1 + `","primary_ci_id":"` + ci4 + `"}`,
		want: http.StatusCreated}))
	if created.CIID != ci4 || w.ciAsset(t, ci4) != created.ID {
		t.Fatalf("POST /assets with primary_ci_id: %+v, ci4→%s", created, w.ciAsset(t, ci4))
	}
	if rows, changed := w.changes(t, ci4); rows != 1 || changed != 1 {
		t.Errorf("ci4 after POST /assets: %d ci_change rows asset_id, %d ci.changed, want 1, 1", rows, changed)
	}

	// Unlink: DELETE; a primary CI is cleared with it; a CI that is not
	// linked to the asset is 404.
	w.link(t, assetB, `{"ci_id":"`+ci2+`","primary":true}`)
	w.do(t, &linkCall{method: http.MethodDelete, path: cisOf(assetB) + "/" + ci2, want: http.StatusNoContent})
	if w.ciAsset(t, ci2) != "" || w.primary(t, assetB) != "" || w.ciAsset(t, ci3) != assetB {
		t.Fatalf("unlinked ci2: ci2→%q, primary of B %q, ci3→%s", w.ciAsset(t, ci2), w.primary(t, assetB), w.ciAsset(t, ci3))
	}
	w.do(t, &linkCall{method: http.MethodDelete, path: cisOf(assetB) + "/" + ci2, want: http.StatusNotFound})
	w.do(t, &linkCall{method: http.MethodDelete, path: cisOf(assetA) + "/" + ci3, want: http.StatusNotFound})

	// Audit: asset.linked and asset.unlinked with the CI, the primary flag
	// and the source of the request.
	if n := w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'asset.linked' AND resource_type = 'asset'
		AND resource_id = $1 AND changes ->> 'ci_id' = $2 AND changes ->> 'primary' = 'true' AND source = 'api'`, assetA, ci1); n == 0 {
		t.Error("no audit entry asset.linked of ci1 (primary, source api)")
	}
	if n := w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'asset.unlinked' AND resource_type = 'asset'
		AND resource_id = $1 AND changes ->> 'ci_id' = $2 AND changes ->> 'primary' = 'true' AND source = 'api'`, assetB, ci2); n != 1 {
		t.Errorf("%d audit entries asset.unlinked of ci2, want 1", n)
	}

	// Client check on every path: 422 on asset_id (primary_ci_id on
	// POST/PATCH /assets); NULL on either side is compatible.
	asset2 := f.Asset(t, f.OrgA, f.Client2, "link-c2")
	ciC1 := f.CI(t, f.OrgA, f.Client1, "link-c1")
	w.do(t, &linkCall{method: http.MethodPost, path: cisOf(asset2), body: `{"ci_id":"` + ciC1 + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "asset_id"})
	if err := w.manualLink(org, ciC1, ci.UpdateRequest{AssetID: &asset2}); !isViolation(err, "asset_id") {
		t.Errorf("PATCH /cis asset_id of another client: %v, want 422 on asset_id", err)
	}
	w.do(t, &linkCall{method: http.MethodPatch, path: assetsPath + "/" + asset2, body: `{"primary_ci_id":"` + ciC1 + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "primary_ci_id"})
	w.do(t, &linkCall{method: http.MethodPost, path: assetsPath,
		body: `{"inventory_no":"LINK-X","name":"x","client_id":"` + f.Client2 + `","primary_ci_id":"` + ciC1 + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "primary_ci_id"})
	if w.ciAsset(t, ciC1) != "" || w.count(t, `SELECT count(*) FROM asset WHERE organization_id = $1 AND inventory_no = 'LINK-X'`, f.OrgA) != 0 {
		t.Fatal("a refused client conflict changed something")
	}
	w.link(t, asset2, `{"ci_id":"`+ci2+`"}`) // org-wide CI, client-2 asset
	if err := w.manualLink(org, ci2, ci.UpdateRequest{ClientID: &f.Client1}); !isViolation(err, "asset_id") {
		t.Errorf("PATCH /cis client_id of another client than the asset: %v, want 422 on asset_id", err)
	}
	err := database.WithTenant(context.Background(), f.App, scopePtr(f, f.OrgA), func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `UPDATE asset SET client_id = $2 WHERE id = $1`, assetB, f.Client2)
		return execErr
	})
	if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.ConstraintName != "ci_asset_client" {
		t.Errorf("asset client against its linked CIs: %v, want ci_asset_client", err)
	}

	// Visibility: an asset or a CI outside the caller's scope is 404.
	client1 := f.ClientCtx(f.Client1)
	ciOther := f.CI(t, f.OrgA, f.Client2, "link-other")
	w.do(t, &linkCall{scope: client1, method: http.MethodPost, path: cisOf(asset2), body: `{"ci_id":"` + ciC1 + `"}`, want: http.StatusNotFound})
	w.do(t, &linkCall{scope: client1, method: http.MethodPost, path: cisOf(assetA), body: `{"ci_id":"` + ciOther + `"}`, want: http.StatusNotFound})
	w.do(t, &linkCall{scope: client1, method: http.MethodDelete, path: cisOf(asset2) + "/" + ci2, want: http.StatusNotFound})
	w.do(t, &linkCall{method: http.MethodPost, path: cisOf(f.ID()), body: `{"ci_id":"` + ciC1 + `"}`, want: http.StatusNotFound})
	w.do(t, &linkCall{method: http.MethodPost, path: cisOf(assetA), body: `{"ci_id":"` + f.ID() + `"}`, want: http.StatusNotFound})
	w.do(t, &linkCall{method: http.MethodPost, path: cisOf(assetA), body: `{"ci_id":"no-uuid"}`, want: http.StatusNotFound})
	w.do(t, &linkCall{method: http.MethodPost, path: cisOf(assetA), body: `{}`, want: http.StatusBadRequest, wantField: "ci_id"})

	// A client-scoped principal may not change an org-wide asset (TEN-05):
	// unlinking its primary CI and making a CI its primary CI are 403, and
	// nothing changes.
	orgWide := f.Asset(t, f.OrgA, "", "link-ow")
	ciP := f.CI(t, f.OrgA, f.Client1, "link-p")
	w.link(t, orgWide, `{"ci_id":"`+ciP+`","primary":true}`)
	vP := w.version(t, ciP)
	w.do(t, &linkCall{scope: client1, method: http.MethodDelete, path: cisOf(orgWide) + "/" + ciP, want: http.StatusForbidden})
	if w.ciAsset(t, ciP) != orgWide || w.primary(t, orgWide) != ciP || w.version(t, ciP) != vP {
		t.Fatalf("refused unlink changed: ciP→%q, primary %q, version %d (was %d)", w.ciAsset(t, ciP), w.primary(t, orgWide),
			w.version(t, ciP), vP)
	}
	ciQ := f.CI(t, f.OrgA, f.Client1, "link-q")
	w.do(t, &linkCall{scope: client1, method: http.MethodPost, path: cisOf(orgWide), body: `{"ci_id":"` + ciQ + `","primary":true}`,
		want: http.StatusForbidden})
	if w.ciAsset(t, ciQ) != "" || w.primary(t, orgWide) != ciP {
		t.Fatalf("refused primary link changed: ciQ→%q, primary %q", w.ciAsset(t, ciQ), w.primary(t, orgWide))
	}

	// Permissions: asset:write for the link routes (the route needs
	// ci:write), ci:write for a primary CI of POST/PATCH /assets.
	noAssetWrite := []identity.Permission{identity.PermAssetRead, identity.PermCIWrite}
	noCIWrite := []identity.Permission{identity.PermAssetRead, identity.PermAssetWrite}
	w.do(t, &linkCall{perms: noAssetWrite, method: http.MethodPost, path: cisOf(assetA), body: `{"ci_id":"` + ciQ + `"}`, want: http.StatusForbidden})
	w.do(t, &linkCall{perms: noAssetWrite, method: http.MethodDelete, path: cisOf(orgWide) + "/" + ciP, want: http.StatusForbidden})
	w.do(t, &linkCall{perms: noCIWrite, method: http.MethodPost, path: cisOf(assetA), body: `{"ci_id":"` + ciQ + `"}`, want: http.StatusForbidden})
	w.do(t, &linkCall{perms: noCIWrite, method: http.MethodPatch, path: assetsPath + "/" + assetA, body: `{"primary_ci_id":"` + ciQ + `"}`,
		want: http.StatusForbidden})
	w.do(t, &linkCall{perms: noCIWrite, method: http.MethodPost, path: assetsPath,
		body: `{"inventory_no":"LINK-Y","name":"y","primary_ci_id":"` + ciQ + `"}`, want: http.StatusForbidden})
	if w.ciAsset(t, ciQ) != "" || w.ciAsset(t, ciP) != orgWide {
		t.Fatal("a refused request changed a link")
	}
	// Clearing the primary CI writes no CI and needs no ci:write.
	if got = decodeAsset(t, w.do(t, &linkCall{perms: noCIWrite, method: http.MethodPatch, path: assetsPath + "/" + orgWide,
		body: `{"primary_ci_id":""}`, want: http.StatusOK})); got.CIID != "" || w.ciAsset(t, ciP) != orgWide {
		t.Fatalf("cleared primary: %+v, ciP→%q", got, w.ciAsset(t, ciP))
	}
}

// TestAssetCILinkLocationAndFacts covers E-81 on the link API and the
// regression of WP-077: an asset without location takes the location of
// the CI it is linked to, without the right to change the asset that is
// 422 on asset_id; the linked CI shows the asset's data read-only and a
// manual serial number write on it stays 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetCILinkLocationAndFacts(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	org := f.OrgCtx(f.OrgA)
	site, err := locations.NewPGRepository(f.App).Create(org, f.OrgA, locations.CreateRequest{
		Kind: locations.KindSite, ClientID: f.Client1, Name: "wp272-site"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	located := func(name string) string {
		id := f.CI(t, f.OrgA, f.Client1, name)
		if _, execErr := f.Admin.Exec(context.Background(), `UPDATE ci SET location_id = $2 WHERE id = $1`, id, site.ID); execErr != nil {
			t.Fatal(execErr)
		}
		return id
	}

	unlocated := f.Asset(t, f.OrgA, f.Client1, "link-unlocated")
	if _, err = f.Admin.Exec(context.Background(), `UPDATE asset SET serial_number = 'SN-272' WHERE id = $1`, unlocated); err != nil {
		t.Fatal(err)
	}
	ciL := located("link-located")
	w.link(t, unlocated, `{"ci_id":"`+ciL+`"}`)
	if loc := w.text(t, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, unlocated); loc != site.ID {
		t.Fatalf("asset location after the link: %q, want the CI's %s", loc, site.ID)
	}
	if loc := w.text(t, `SELECT COALESCE(location_id::text, '') FROM ci WHERE id = $1`, ciL); loc != site.ID {
		t.Fatalf("CI location after the link: %q, want %s", loc, site.ID)
	}

	// A client-scoped principal may not let an org-wide asset take the
	// location: 422 on asset_id, nothing linked.
	orgWide := f.Asset(t, f.OrgA, "", "link-ow-unlocated")
	ciM := located("link-located-2")
	w.do(t, &linkCall{scope: f.ClientCtx(f.Client1), method: http.MethodPost, path: assetsPath + "/" + orgWide + "/cis",
		body: `{"ci_id":"` + ciM + `"}`, want: http.StatusUnprocessableEntity, wantField: "asset_id"})
	if w.ciAsset(t, ciM) != "" {
		t.Fatal("the refused link was written")
	}

	// Regression (WP-077): asset data read-only in the CI, manual serial
	// number write 422.
	item, err := w.svc.GetByID(org, f.OrgA, ciL)
	if err != nil {
		t.Fatal(err)
	}
	if item.Asset == nil || item.Asset.SerialNumber != "SN-272" || item.Asset.LocationID != site.ID {
		t.Fatalf("asset facts of the linked CI: %+v", item.Asset)
	}
	serial := "SN-OTHER"
	if err = w.manualLink(org, ciL, ci.UpdateRequest{SerialNumber: &serial}); !isViolation(err, "serial_number") {
		t.Errorf("manual serial number on a linked CI: %v, want 422 on serial_number", err)
	}
}

func scopePtr(f *scopetest.Fixture, orgID string) *database.TenantScope {
	s := database.OrgWideScope(orgID, f.User)
	return &s
}
