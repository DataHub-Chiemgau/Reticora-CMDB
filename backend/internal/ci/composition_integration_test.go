// Physical composition (AST-05, AST-04, DB-05; WP-273, E-123): the child
// contract on ci.parent_ci_id against PostgreSQL.
//
//   - TestCompositionChildMode: a parent without mode gives standalone (POST
//     and PATCH), no CI with a parent is left without a mode (backfill),
//     clearing the parent ("" and the ON DELETE SET NULL of a hard delete)
//     clears the mode; child_mode without a parent and an unknown mode are
//     422 on child_mode; child_mode is in ci_change.
//   - TestCompositionConfigurationAsset: a configuration child has no asset
//     of its own: POST and PATCH /cis with asset_id are 422 on asset_id,
//     configuration for a linked CI 422 on child_mode, the asset API (POST
//     /api/v1/assets/{id}/cis, primary_ci_id) 422.
//   - TestCompositionConfigurationDerivation: asset data and location of a
//     configuration child come from the nearest ancestor with an asset over
//     two levels (asset, asset_source_ci_id, site), follow an asset move and
//     a re-hang; manual location and serial writes are 422, a direct
//     location write as well; the derived location gets no override; parent
//     and configuration child have the same client (also with an org-wide
//     parent, which checkParents allows); no serial_mismatch for a
//     configuration child; the provenance names the source CI; GET /cis
//     with fields= carries the new members.
//   - TestCompositionRegression: the cycle check (422) and the rack lock
//     (409) hold for both modes.
//   - TestCompositionMergeDeleteRestore: the merge moves the victim's
//     children to the survivor and the unmerge gives them back; a merge
//     cycle and a client conflict of a configuration child are 409;
//     deleting a parent with configuration children and restoring a
//     configuration child with a deleted parent are 409.
//   - TestCompositionMemoryProfile: the same contract in the profile
//     without a database (no PostgreSQL needed).
//
// The ingest (record path and window) leaves a configuration child unlinked
// (discovery/composition_link_integration_test.go).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestComposition' ./internal/ci/
package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// compWorld is the scenario of the composition tests: the CI API and
// service of organization A, a site with two rooms of client 1.
type compWorld struct {
	f            *scopetest.Fixture
	bg, ctx      context.Context
	repo         *ci.PGRepository
	svc          *ci.Service
	api          *tagAPI
	serverType   string
	site         string
	room1, room2 string
}

func newCompWorld(t *testing.T) *compWorld {
	t.Helper()
	f := scopetest.Seed(t, "2c")
	w := &compWorld{f: f, bg: context.Background()}
	w.ctx = tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	w.repo = ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	w.svc = ci.NewService(w.repo)
	w.api = newTagAPI(t, w.ctx, w.repo)
	w.serverType = serverType(t, f)
	tree := locations.NewPGRepository(f.App)
	loc := func(req locations.CreateRequest) string {
		t.Helper()
		l, err := tree.Create(w.ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return l.ID
	}
	w.site = loc(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "comp-s"})
	building := loc(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: w.site, Name: "comp-b"})
	w.room1 = loc(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "comp-r1"})
	w.room2 = loc(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "comp-r2"})
	return w
}

// asset inserts an asset of organization A and returns its id.
func (w *compWorld) asset(t *testing.T, tag, serial, clientID, location string) string {
	t.Helper()
	var id string
	err := database.WithTenant(w.bg, w.f.App, scopePtr(w.f.OrgA, w.f.User), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO asset (organization_id, client_id, asset_tag, name, serial_number, location_id, lifecycle_state)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $3, $4, NULLIF($5, '')::uuid, 'in_use') RETURNING id::text`,
			w.f.OrgA, clientID, tag, serial, location).Scan(&id)
	})
	if err != nil {
		t.Fatalf("asset %s: %v", tag, err)
	}
	return id
}

// create creates a CI through POST /cis and returns it; status other than
// 201 returns the zero item.
func (w *compWorld) create(t *testing.T, body map[string]any, status int) (ci.Item, string) {
	t.Helper()
	if _, ok := body["ci_type_id"]; !ok {
		body["ci_type_id"] = w.serverType
	}
	raw, _ := json.Marshal(body)
	rec := w.api.call(http.MethodPost, "/api/v1/cis", string(raw))
	if rec.Code != status {
		t.Fatalf("POST /cis %s: %d %s, want %d", raw, rec.Code, rec.Body.String(), status)
	}
	var item ci.Item
	if status == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
	}
	return item, rec.Body.String()
}

// get reads a CI through the repository.
func (w *compWorld) get(t *testing.T, id string) *ci.Item {
	t.Helper()
	item, err := w.repo.GetByID(w.ctx, w.f.OrgA, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return item
}

func (w *compWorld) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := w.f.Admin.QueryRow(w.bg, query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

func (w *compWorld) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := w.f.Admin.Exec(w.bg, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestCompositionChildMode(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	parent, _ := w.create(t, map[string]any{"name": "comp-parent", "client_id": f.Client1}, http.StatusCreated)
	child, _ := w.create(t, map[string]any{"name": "comp-child", "client_id": f.Client1, "parent_ci_id": parent.ID}, http.StatusCreated)
	if child.ChildMode != ci.ChildModeStandalone {
		t.Errorf("child created without a mode: %q, want standalone", child.ChildMode)
	}
	// Backfill and trigger: no CI with a parent lacks a mode.
	if n := countRows(t, f, `SELECT count(*) FROM ci WHERE parent_ci_id IS NOT NULL AND child_mode IS NULL`); n != 0 {
		t.Errorf("%d CIs with a parent and without a mode", n)
	}

	// Clearing the parent clears the mode; setting it again gives standalone.
	code, body := patchCI(w.api, child.ID, `{"parent_ci_id":""}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH parent_ci_id \"\": %d %s", code, body)
	}
	if got := w.get(t, child.ID); got.ParentCIID != "" || got.ChildMode != "" {
		t.Errorf("after clearing the parent: parent %q mode %q", got.ParentCIID, got.ChildMode)
	}
	if code, body = patchCI(w.api, child.ID, `{"parent_ci_id":"`+parent.ID+`","child_mode":"configuration"}`); code != http.StatusOK {
		t.Fatalf("PATCH configuration: %d %s", code, body)
	}
	if got := w.get(t, child.ID); got.ChildMode != ci.ChildModeConfiguration {
		t.Errorf("mode %q, want configuration", got.ChildMode)
	}
	if n := countRows(t, f, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'child_mode'
		AND new_value #>> '{}' = 'configuration'`, child.ID); n != 1 {
		t.Errorf("%d ci_change rows child_mode → configuration, want 1", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'child_mode'`, child.ID); n != 0 {
		t.Errorf("child_mode got %d overrides", n)
	}
	// null resets to the default.
	if code, body = patchCI(w.api, child.ID, `{"child_mode":null}`); code != http.StatusOK {
		t.Fatalf("PATCH child_mode null: %d %s", code, body)
	}
	if got := w.get(t, child.ID); got.ChildMode != ci.ChildModeStandalone {
		t.Errorf("mode after null: %q, want standalone", got.ChildMode)
	}

	// A mode without a parent and an unknown mode: 422 on child_mode.
	_, body = w.create(t, map[string]any{"name": "comp-orphan", "child_mode": "configuration"}, http.StatusUnprocessableEntity)
	if !violationOn(t, http.StatusUnprocessableEntity, body, "child_mode") {
		t.Errorf("POST child_mode without a parent: %s", body)
	}
	_, body = w.create(t, map[string]any{"name": "comp-unknown", "parent_ci_id": parent.ID, "client_id": f.Client1,
		"child_mode": "glued"}, http.StatusUnprocessableEntity)
	if !violationOn(t, http.StatusUnprocessableEntity, body, "child_mode") {
		t.Errorf("POST unknown child_mode: %s", body)
	}
	if code, body = patchCI(w.api, child.ID, `{"parent_ci_id":"","child_mode":"standalone"}`); !violationOn(t, code, body, "child_mode") {
		t.Errorf("PATCH child_mode with the parent cleared: %d %s", code, body)
	}

	// The hard delete of the parent (ON DELETE SET NULL (parent_ci_id))
	// clears the mode.
	hardParent := f.CI(t, f.OrgA, f.Client1, "comp-hard-parent")
	hardChild := f.CI(t, f.OrgA, f.Client1, "comp-hard-child")
	w.exec(t, `UPDATE ci SET parent_ci_id = $1, child_mode = 'configuration' WHERE id = $2`, hardParent, hardChild)
	w.exec(t, `DELETE FROM ci WHERE id = $1`, hardParent)
	if got := w.scalar(t, `SELECT COALESCE(parent_ci_id::text, '') || '|' || COALESCE(child_mode, '') FROM ci WHERE id = $1`, hardChild); got != "|" {
		t.Errorf("child after the hard delete of its parent: %q, want no parent and no mode", got)
	}
}

func TestCompositionConfigurationAsset(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	parent, _ := w.create(t, map[string]any{"name": "comp-a-parent", "client_id": f.Client1, "location_id": w.room1}, http.StatusCreated)
	free := w.asset(t, "COMP-FREE", "SN-COMP-FREE", f.Client1, w.room1)

	_, body := w.create(t, map[string]any{"name": "comp-a-refused", "client_id": f.Client1, "parent_ci_id": parent.ID,
		"child_mode": "configuration", "asset_id": free}, http.StatusUnprocessableEntity)
	if !violationOn(t, http.StatusUnprocessableEntity, body, "asset_id") {
		t.Errorf("POST configuration with asset_id: %s", body)
	}
	cfg, _ := w.create(t, map[string]any{"name": "comp-a-cfg", "client_id": f.Client1, "parent_ci_id": parent.ID,
		"child_mode": "configuration"}, http.StatusCreated)
	if code, b := patchCI(w.api, cfg.ID, `{"asset_id":"`+free+`"}`); !violationOn(t, code, b, "asset_id") {
		t.Errorf("PATCH asset_id on a configuration child: %d %s", code, b)
	}
	linkedAsset := w.asset(t, "COMP-LINKED", "SN-COMP-LINKED", f.Client1, w.room1)
	linked, _ := w.create(t, map[string]any{"name": "comp-a-linked", "client_id": f.Client1, "parent_ci_id": parent.ID,
		"asset_id": linkedAsset}, http.StatusCreated)
	if code, b := patchCI(w.api, linked.ID, `{"child_mode":"configuration"}`); !violationOn(t, code, b, "child_mode") {
		t.Errorf("PATCH configuration on a linked CI: %d %s", code, b)
	}
	// The database refuses it as well (ci_configuration_child_asset).
	if _, err := w.repo.Update(w.ctx, f.OrgA, cfg.ID, ci.UpdateRequest{AssetID: &free}); !isValidationOn(err, "asset_id") {
		t.Errorf("repository link of a configuration child: %v", err)
	}

	// The asset API of WP-272.
	mux := chi.NewRouter()
	asset.NewHandler(asset.NewPGRepository(f.App)).WithCILinks(w.svc).RegisterRoutes(mux)
	perms := []identity.Permission{identity.PermAssetRead, identity.PermAssetWrite, identity.PermCIRead, identity.PermCIWrite}
	actx := identity.WithPrincipal(w.ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Permissions: perms})
	actx = permission.WithPolicy(actx, permission.UnrestrictedPolicy(perms))
	actx = audit.WithRequestInfo(actx, audit.RequestInfo{RequestID: "wp273", Source: audit.SourceAPI})
	assetAPI := &tagAPI{t: t, mux: mux, ctx: actx}
	for _, c := range []struct{ method, path, body, field string }{
		{http.MethodPost, "/api/v1/assets/" + free + "/cis", `{"ci_id":"` + cfg.ID + `"}`, "asset_id"},
		{http.MethodPatch, "/api/v1/assets/" + free, `{"primary_ci_id":"` + cfg.ID + `"}`, "primary_ci_id"},
	} {
		rec := assetAPI.call(c.method, c.path, c.body)
		if !violationOn(t, rec.Code, rec.Body.String(), c.field) {
			t.Errorf("%s %s %s: %d %s, want 422 on %s", c.method, c.path, c.body, rec.Code, rec.Body.String(), c.field)
		}
	}
	if got := w.get(t, cfg.ID); got.AssetID != "" {
		t.Errorf("configuration child linked to %s", got.AssetID)
	}
}

func TestCompositionConfigurationDerivation(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	assetRoot := w.asset(t, "COMP-ROOT", "SN-ROOT", f.Client1, w.room1)
	root, _ := w.create(t, map[string]any{"name": "comp-root", "client_id": f.Client1, "asset_id": assetRoot}, http.StatusCreated)
	c1, _ := w.create(t, map[string]any{"name": "comp-c1", "client_id": f.Client1, "parent_ci_id": root.ID,
		"child_mode": "configuration", "serial_number": "SN-OBSERVED-C1"}, http.StatusCreated)
	c2, _ := w.create(t, map[string]any{"name": "comp-c2", "client_id": f.Client1, "parent_ci_id": c1.ID,
		"child_mode": "configuration"}, http.StatusCreated)

	expect := func(step, id, assetID, source, location string) {
		t.Helper()
		got := w.get(t, id)
		if got.LocationID != location || got.AssetSourceCIID != source || got.Asset == nil || got.Asset.ID != assetID ||
			got.Asset.LocationID != location || (location != "" && got.SiteID != w.site) {
			t.Errorf("%s: CI %s location %q site %q source %q asset %+v; want location %q, source %s, asset %s",
				step, id, got.LocationID, got.SiteID, got.AssetSourceCIID, got.Asset, location, source, assetID)
		}
	}
	expect("level 1", c1.ID, assetRoot, root.ID, w.room1)
	expect("level 2", c2.ID, assetRoot, root.ID, w.room1)
	if got := w.get(t, c1.ID); got.SerialNumber != "SN-OBSERVED-C1" || got.Asset.SerialNumber != "SN-ROOT" ||
		got.Asset.LifecycleState != "in_use" {
		t.Errorf("serial and lifecycle of c1: observed %q, asset %+v", got.SerialNumber, got.Asset)
	}
	// One read of a page loads the facts of both levels (GetByIDs).
	items, err := w.repo.GetByIDs(w.ctx, f.OrgA, []string{c1.ID, c2.ID, root.ID})
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if items[i].Asset == nil || items[i].Asset.ID != assetRoot {
			t.Errorf("GetByIDs: %s without the root asset", items[i].ID)
		}
	}

	// A move of the asset reaches both levels.
	err = database.WithTenant(w.bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
		_, uerr := tx.Exec(ctx, `UPDATE asset SET location_id = $1 WHERE id = $2`, w.room2, assetRoot)
		return uerr
	})
	if err != nil {
		t.Fatal(err)
	}
	expect("after the asset move", c1.ID, assetRoot, root.ID, w.room2)
	expect("after the asset move", c2.ID, assetRoot, root.ID, w.room2)

	// A standalone child with its own asset is the source of its
	// configuration children.
	assetMid := w.asset(t, "COMP-MID", "SN-MID", f.Client1, w.room1)
	mid, _ := w.create(t, map[string]any{"name": "comp-mid", "client_id": f.Client1, "parent_ci_id": root.ID, "asset_id": assetMid},
		http.StatusCreated)
	if mid.ChildMode != ci.ChildModeStandalone {
		t.Errorf("mid mode %q", mid.ChildMode)
	}
	// Re-hang c1 (with c2) under it: both take its asset and location, no
	// override for the derived location.
	if code, body := patchCI(w.api, c1.ID, `{"parent_ci_id":"`+mid.ID+`"}`); code != http.StatusOK {
		t.Fatalf("re-hang: %d %s", code, body)
	}
	expect("after the re-hang", c1.ID, assetMid, mid.ID, w.room1)
	expect("after the re-hang", c2.ID, assetMid, mid.ID, w.room1)
	if n := countRows(t, f, `SELECT count(*) FROM ci_field_value WHERE ci_id = ANY($1::uuid[]) AND field_name = 'location_id'`,
		[]string{c1.ID, c2.ID}); n != 0 {
		t.Errorf("%d overrides of the derived location", n)
	}

	// Manual location and serial writes are refused, a direct location
	// write as well.
	for _, c := range []struct{ body, field string }{
		{`{"location_id":"` + w.room2 + `"}`, "location_id"},
		{`{"serial_number":"SN-MANUAL"}`, "serial_number"},
	} {
		if code, body := patchCI(w.api, c1.ID, c.body); !violationOn(t, code, body, c.field) {
			t.Errorf("PATCH %s: %d %s, want 422 on %s", c.body, code, body, c.field)
		}
	}
	_, body := w.create(t, map[string]any{"name": "comp-located", "client_id": f.Client1, "parent_ci_id": mid.ID,
		"child_mode": "configuration", "location_id": w.room2}, http.StatusUnprocessableEntity)
	if !violationOn(t, http.StatusUnprocessableEntity, body, "location_id") {
		t.Errorf("POST configuration with location_id: %s", body)
	}
	if _, err = w.repo.Update(w.ctx, f.OrgA, c2.ID, ci.UpdateRequest{LocationID: &w.room2}); !isValidationOn(err, "location_id") {
		t.Errorf("direct location write on a configuration child: %v", err)
	}
	// The observed serial number stays writable for discovery; no
	// serial_mismatch for a configuration child.
	observed := "SN-OBSERVED-OTHER"
	if _, err = w.repo.Update(w.ctx, f.OrgA, c1.ID, ci.UpdateRequest{SerialNumber: &observed}); err != nil {
		t.Fatalf("observed serial write: %v", err)
	}
	if n := countRows(t, f, `SELECT count(*) FROM review_item WHERE kind = 'serial_mismatch' AND payload ->> 'ci_id' = ANY($1)`,
		[]string{c1.ID, c2.ID}); n != 0 {
		t.Errorf("%d serial_mismatch reviews for configuration children", n)
	}

	// The provenance names the source CI.
	got := w.get(t, c1.ID)
	entries := ci.BuildProvenance(got, nil)
	for _, field := range []string{"serial_number", "location_id"} {
		i := slices.IndexFunc(entries, func(e ci.FieldProvenance) bool { return e.Field == field })
		if i < 0 || entries[i].Reason != ci.ReasonAsset || entries[i].SourceCIID != mid.ID {
			t.Errorf("provenance of %s: %+v, want reason asset from %s", field, entries, mid.ID)
		}
	}

	// GET /cis with fields= carries the new members.
	rec := w.api.call(http.MethodGet, "/api/v1/cis?fields=child_mode,asset_source_ci_id,asset&filter[parent_ci_id][exact]="+c1.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /cis fields=: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data []struct {
			ChildMode       string         `json:"child_mode"`
			AssetSourceCIID string         `json:"asset_source_ci_id"`
			Asset           *ci.AssetFacts `json:"asset"`
		} `json:"data"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range list.Data {
		if d.AssetSourceCIID == mid.ID {
			found = d.ChildMode == ci.ChildModeConfiguration && d.Asset != nil && d.Asset.ID == assetMid
		}
	}
	if !found {
		t.Errorf("GET /cis fields=: %s", rec.Body.String())
	}

	// Parent and configuration child have the same client: an org-wide
	// parent (allowed by checkParents for a standalone child) is refused.
	orgWide, _ := w.create(t, map[string]any{"name": "comp-orgwide"}, http.StatusCreated)
	w.create(t, map[string]any{"name": "comp-ow-standalone", "client_id": f.Client1, "parent_ci_id": orgWide.ID}, http.StatusCreated)
	_, body = w.create(t, map[string]any{"name": "comp-ow-cfg", "client_id": f.Client1, "parent_ci_id": orgWide.ID,
		"child_mode": "configuration"}, http.StatusUnprocessableEntity)
	if !violationOn(t, http.StatusUnprocessableEntity, body, "parent_ci_id") {
		t.Errorf("configuration child of an org-wide parent: %s", body)
	}
}

func TestCompositionRegression(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	root, _ := w.create(t, map[string]any{"name": "comp-reg-root", "client_id": f.Client1, "location_id": w.room1}, http.StatusCreated)
	racks := rack.NewPGRepository(f.App)
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: w.room1, Name: "comp-rack", HeightUnits: 42}
	if err := racks.CreateRack(w.ctx, rk); err != nil {
		t.Fatal(err)
	}
	for i, mode := range []string{ci.ChildModeConfiguration, ci.ChildModeStandalone} {
		child, _ := w.create(t, map[string]any{"name": "comp-reg-" + mode, "client_id": f.Client1, "parent_ci_id": root.ID,
			"child_mode": mode}, http.StatusCreated)
		// Cycle: the root cannot become a child of its child (422).
		if code, body := patchCI(w.api, root.ID, `{"parent_ci_id":"`+child.ID+`"}`); !violationOn(t, code, body, "parent_ci_id") {
			t.Errorf("%s: cycle %d %s, want 422 on parent_ci_id", mode, code, body)
		}
		// Rack lock: a child is not mounted (409).
		err := racks.CreateMount(w.ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: child.ID,
			PositionU: 1 + 2*i, HeightU: 1, Face: "front"})
		if !errors.Is(err, rack.ErrConflict) {
			t.Errorf("%s: mount of a child %v, want conflict", mode, err)
		}
	}
}

func TestCompositionMergeDeleteRestore(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	victim, _ := w.create(t, map[string]any{"name": "comp-victim", "client_id": f.Client1, "location_id": w.room1}, http.StatusCreated)
	survivor, _ := w.create(t, map[string]any{"name": "comp-survivor", "client_id": f.Client1, "location_id": w.room2}, http.StatusCreated)
	cfg, _ := w.create(t, map[string]any{"name": "comp-m-cfg", "client_id": f.Client1, "parent_ci_id": victim.ID,
		"child_mode": "configuration"}, http.StatusCreated)
	alone, _ := w.create(t, map[string]any{"name": "comp-m-alone", "client_id": f.Client1, "parent_ci_id": victim.ID}, http.StatusCreated)

	// Delete of a parent with a configuration child: 409; restore of a
	// configuration child with a deleted parent: 409.
	rec := w.api.call(http.MethodDelete, "/api/v1/cis/"+victim.ID, "")
	if rec.Code != http.StatusConflict {
		t.Errorf("DELETE parent with a configuration child: %d %s", rec.Code, rec.Body.String())
	}
	cfgDeleted, _ := w.create(t, map[string]any{"name": "comp-m-cfg2", "client_id": f.Client1, "parent_ci_id": alone.ID,
		"child_mode": "configuration"}, http.StatusCreated)
	if _, err := w.svc.Delete(w.ctx, f.OrgA, cfgDeleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Delete(w.ctx, f.OrgA, alone.ID); err != nil {
		t.Fatalf("delete a parent whose configuration child is deleted: %v", err)
	}
	if _, err := w.svc.Restore(w.ctx, f.OrgA, cfgDeleted.ID); !errors.Is(err, ci.ErrConfigurationParentDeleted) {
		t.Errorf("restore a configuration child with a deleted parent: %v", err)
	}
	if _, err := w.svc.Restore(w.ctx, f.OrgA, alone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Restore(w.ctx, f.OrgA, cfgDeleted.ID); err != nil {
		t.Errorf("restore after the parent: %v", err)
	}
	// A parent with a standalone child only is deleted; the child keeps
	// parent_ci_id (API-01).
	other, _ := w.create(t, map[string]any{"name": "comp-m-other", "client_id": f.Client1}, http.StatusCreated)
	keeps, _ := w.create(t, map[string]any{"name": "comp-m-keeps", "client_id": f.Client1, "parent_ci_id": other.ID}, http.StatusCreated)
	if rec = w.api.call(http.MethodDelete, "/api/v1/cis/"+other.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE parent with a standalone child: %d %s", rec.Code, rec.Body.String())
	}
	if got := w.get(t, keeps.ID); got.ParentCIID != other.ID || got.ChildMode != ci.ChildModeStandalone {
		t.Errorf("standalone child of a deleted parent: parent %q mode %q", got.ParentCIID, got.ChildMode)
	}

	// The merge moves the children to the survivor; the configuration
	// child takes its location.
	res, err := w.svc.Merge(w.ctx, f.OrgA, survivor.ID, victim.ID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.Moved["children"] != 2 {
		t.Errorf("moved %+v, want 2 children", res.Moved)
	}
	for _, id := range []string{cfg.ID, alone.ID} {
		if got := w.get(t, id); got.ParentCIID != survivor.ID {
			t.Errorf("child %s after the merge: parent %s", id, got.ParentCIID)
		}
	}
	if got := w.get(t, cfg.ID); got.LocationID != w.room2 || got.ChildMode != ci.ChildModeConfiguration {
		t.Errorf("configuration child after the merge: location %s mode %s", got.LocationID, got.ChildMode)
	}
	if _, err = w.svc.Unmerge(w.ctx, f.OrgA, victim.ID, true); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	for _, id := range []string{cfg.ID, alone.ID} {
		if got := w.get(t, id); got.ParentCIID != victim.ID {
			t.Errorf("child %s after the unmerge: parent %s", id, got.ParentCIID)
		}
	}
	if got := w.get(t, cfg.ID); got.LocationID != w.room1 || got.ChildMode != ci.ChildModeConfiguration {
		t.Errorf("configuration child after the unmerge: location %s mode %s", got.LocationID, got.ChildMode)
	}

	// A survivor that is a child of the victim: cycle, 409.
	if _, err = w.svc.Merge(w.ctx, f.OrgA, alone.ID, victim.ID); !errors.Is(err, ci.ErrMergeConflict) {
		t.Errorf("merge into a child of the victim: %v, want a merge conflict", err)
	}
	// A configuration child of client 1 cannot hang on an org-wide
	// survivor: 409.
	orgWide, _ := w.create(t, map[string]any{"name": "comp-m-orgwide"}, http.StatusCreated)
	if _, err = w.svc.Merge(w.ctx, f.OrgA, orgWide.ID, victim.ID); !errors.Is(err, ci.ErrMergeConflict) {
		t.Errorf("merge with a client conflict of a configuration child: %v, want a merge conflict", err)
	}
	if got := w.get(t, cfg.ID); got.ParentCIID != victim.ID {
		t.Errorf("configuration child after the refused merges: parent %s", got.ParentCIID)
	}
}

// TestCompositionMemoryProfile checks the child contract in the profile
// without a database (memory repository): default standalone, reset with
// the parent, 422 for a mode without a parent and for an asset of a
// configuration child, 409 for deleting a parent with a configuration
// child and restoring that child while the parent is deleted.
func TestCompositionMemoryProfile(t *testing.T) {
	ctx := context.Background()
	svc := ci.NewService(ci.NewMemoryRepository())
	const org = "00000000-0000-4000-8000-0000000002c0"
	const typeID = "00000000-0000-4000-8000-0000000002c1"
	newCI := func(name, parent, mode string) *ci.Item {
		t.Helper()
		item := &ci.Item{OrganizationID: org, CITypeID: typeID, Name: name, Status: "active", ParentCIID: parent, ChildMode: mode,
			Attributes: map[string]any{}}
		if err := svc.Create(ctx, item); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return item
	}
	parent := newCI("mem-parent", "", "")
	child := newCI("mem-child", parent.ID, "")
	if child.ChildMode != ci.ChildModeStandalone {
		t.Errorf("default mode %q", child.ChildMode)
	}
	empty, cfgMode, assetID := "", ci.ChildModeConfiguration, "00000000-0000-4000-8000-0000000002c2"
	got, err := svc.Update(ctx, org, child.ID, ci.UpdateRequest{ParentCIID: &empty})
	if err != nil || got.ChildMode != "" {
		t.Errorf("clear the parent: %v, %+v", err, got)
	}
	if _, err = svc.Update(ctx, org, child.ID, ci.UpdateRequest{ChildMode: &cfgMode}); !isValidationOn(err, "child_mode") {
		t.Errorf("mode without a parent: %v", err)
	}
	if err = svc.Create(ctx, &ci.Item{OrganizationID: org, CITypeID: typeID, Name: "mem-refused", Status: "active",
		ParentCIID: parent.ID, ChildMode: cfgMode, AssetID: assetID, Attributes: map[string]any{}}); !isValidationOn(err, "asset_id") {
		t.Errorf("configuration child with an asset: %v", err)
	}
	cfg := newCI("mem-cfg", parent.ID, cfgMode)
	if _, err = svc.Update(ctx, org, cfg.ID, ci.UpdateRequest{AssetID: &assetID}); !isValidationOn(err, "asset_id") {
		t.Errorf("link a configuration child: %v", err)
	}
	if _, err = svc.Delete(ctx, org, parent.ID); !errors.Is(err, ci.ErrConfigurationChildren) {
		t.Errorf("delete a parent with a configuration child: %v", err)
	}
	if _, err = svc.Delete(ctx, org, cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Delete(ctx, org, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Restore(ctx, org, cfg.ID); !errors.Is(err, ci.ErrConfigurationParentDeleted) {
		t.Errorf("restore a configuration child with a deleted parent: %v", err)
	}
}

func isValidationOn(err error, field string) bool {
	var v *ci.ValidationError
	return errors.As(err, &v) && len(v.Violations) > 0 && v.Violations[0].Field == field
}
