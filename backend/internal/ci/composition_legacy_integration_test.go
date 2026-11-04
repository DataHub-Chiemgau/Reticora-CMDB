// Legacy composition on parent_ci_id (AST-05, AST-04; WP-742, E-26) against
// PostgreSQL.
//
//   - TestCompositionLegacyTakeover: migration 000248 in a rolled-back
//     transaction. A child_ci_id row becomes a configuration child (flags,
//     no own asset, same client; the location follows the parent) or a
//     standalone child (an independently_* flag, an own asset, an org-wide
//     parent of another client); a child_asset_id row makes the CIs of the
//     child asset standalone children. Quarantined: a parent asset with
//     several CIs and no primary CI, one without CI, a child asset without
//     CI, a child with another parent, a cycle (self, which
//     trg_ci_parent_cycle refuses before ci_parent_not_self, and through
//     the parents), a parent of another client that is not org-wide and a
//     mounted child. The before images name exactly the changed CIs; the
//     down migration restores them and removes the rows of 000248.
//     composition stays unchanged.
//   - TestCompositionLegacyParentPath: GET /api/v1/cis/{id}/parent in the
//     former format for a configuration child, a standalone child and a
//     configuration grandchild (derived asset), 404 without a parent and
//     for a parent without asset, Deprecation/Sunset/Link from
//     api/openapi.yaml.
//   - TestCompositionLegacyAssetChildren: GET /api/v1/assets/{id}/children
//     lists the children of every CI of the asset with child_mode, over two
//     cursor pages.
//   - TestCompositionLegacyChildAssetWritable: without the field lock over
//     composition (WP-841) PATCH /api/v1/assets/{id} on a former child
//     asset writes a formerly parent-owned field (200 instead of 409).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestCompositionLegacy' ./internal/ci/
package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
)

func readMigration(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCompositionLegacyTakeover(t *testing.T) {
	w := newCompWorld(t)
	f, bg := w.f, w.bg
	up := readMigration(t, "000248_composition_to_parent_ci.up.sql")
	down := readMigration(t, "000248_composition_to_parent_ci.down.sql")

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, query, args...); execErr != nil {
			t.Fatalf("%s: %v", query, execErr)
		}
	}
	newCI := func(name, client, location string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, location_id)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, 'active', NULLIF($6, '')::uuid)`,
			id, f.OrgA, client, w.serverType, name, location)
		return id
	}
	newAsset := func(tag, client string, location ...string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO asset (id, organization_id, client_id, asset_tag, name, location_id)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $4, NULLIF($5, '')::uuid)`, id, f.OrgA, client, tag, append(location, "")[0])
		return id
	}
	link := func(ciID, assetID string, primary bool) {
		t.Helper()
		exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, ciID, assetID)
		if primary {
			exec(`UPDATE asset SET ci_id = $2 WHERE id = $1`, assetID, ciID)
		}
	}
	type flags struct{ configurationOnly, serialized bool }
	compose := func(parentAsset, childCI, childAsset string, fl flags) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO composition (id, organization_id, parent_asset_id, child_ci_id, child_asset_id,
				configuration_only, independently_serialized)
			VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7)`,
			id, f.OrgA, parentAsset, childCI, childAsset, fl.configurationOnly, fl.serialized)
		return id
	}
	cfgFlags := flags{configurationOnly: true}

	// The parent: asset PA1 in room 1 with its primary CI P1, whose
	// location mirrors the asset's.
	pa1 := newAsset("leg-pa1", f.Client1, w.room1)
	p1 := newCI("leg-p1", f.Client1, w.room1)
	link(p1, pa1, true)

	cfg := newCI("leg-cfg", f.Client1, w.room2)
	compCfg := compose(pa1, cfg, "", cfgFlags)
	flagged := newCI("leg-flagged", f.Client1, "")
	compose(pa1, flagged, "", flags{configurationOnly: true, serialized: true})
	ownAsset := newCI("leg-own-asset", f.Client1, "")
	link(ownAsset, newAsset("leg-own", f.Client1), false)
	compose(pa1, ownAsset, "", cfgFlags)
	// The child asset AK with two CIs.
	ak := newAsset("leg-ak", f.Client1)
	k1, k2 := newCI("leg-k1", f.Client1, ""), newCI("leg-k2", f.Client1, "")
	link(k1, ak, false)
	link(k2, ak, false)
	compose(pa1, "", ak, cfgFlags)

	// An org-wide parent (no primary CI, its only CI) and a child of client 1.
	pa2 := newAsset("leg-pa2", "")
	p2 := newCI("leg-p2", "", "")
	link(p2, pa2, false)
	otherClient := newCI("leg-other-client", f.Client1, "")
	compose(pa2, otherClient, "", cfgFlags)

	// Quarantine cases.
	pa3 := newAsset("leg-pa3", f.Client1)
	link(newCI("leg-m1", f.Client1, ""), pa3, false)
	link(newCI("leg-m2", f.Client1, ""), pa3, false)
	several := newCI("leg-several", f.Client1, "")
	compSeveral := compose(pa3, several, "", cfgFlags)
	compNoCI := compose(newAsset("leg-pa4", f.Client1), newCI("leg-no-ci-child", f.Client1, ""), "", cfgFlags)
	compEmptyAsset := compose(pa1, "", newAsset("leg-empty", f.Client1), cfgFlags)
	elsewhere := newCI("leg-elsewhere", f.Client1, "")
	other := newCI("leg-other-parent", f.Client1, "")
	exec(`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, other, elsewhere)
	compOther := compose(pa1, other, "", cfgFlags)
	compSelf := compose(pa1, p1, "", cfgFlags)
	pa5 := newAsset("leg-pa5", f.Client1)
	p5 := newCI("leg-p5", f.Client1, "")
	link(p5, pa5, true)
	top := newCI("leg-top", f.Client1, "")
	exec(`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, p5, top)
	compCycle := compose(pa5, top, "", cfgFlags)
	pa6 := newAsset("leg-pa6", f.Client2)
	p6 := newCI("leg-p6", f.Client2, "")
	link(p6, pa6, true)
	foreign := newCI("leg-foreign", f.Client1, "")
	compForeign := compose(pa6, foreign, "", cfgFlags)
	mounted := newCI("leg-mounted", f.Client1, "")
	var rack string
	if err = tx.QueryRow(bg, `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 'leg-rack') RETURNING id::text`,
		f.OrgA, f.Room(t, f.Client1, "leg-rack-room")).Scan(&rack); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO rack_mount (organization_id, ci_id, rack_id, position_u) VALUES ($1, $2, $3, 1)`, f.OrgA, mounted, rack)
	compMounted := compose(pa1, mounted, "", cfgFlags)

	type state struct{ parent, mode, location string }
	read := func(id string) state {
		t.Helper()
		var s state
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(parent_ci_id::text, ''), COALESCE(child_mode, ''), COALESCE(location_id::text, '')
			FROM ci WHERE id = $1`, id).Scan(&s.parent, &s.mode, &s.location); qerr != nil {
			t.Fatal(qerr)
		}
		return s
	}
	tracked := []string{cfg, flagged, ownAsset, k1, k2, otherClient, several, other, p1, top, foreign, mounted}
	before := map[string]state{}
	for _, id := range tracked {
		before[id] = read(id)
	}
	compositionRows := func() string {
		t.Helper()
		var digest string
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(md5(string_agg(to_jsonb(c)::text, '|' ORDER BY id)), '')
			FROM composition c WHERE organization_id = $1`, f.OrgA).Scan(&digest); qerr != nil {
			t.Fatal(qerr)
		}
		return digest
	}
	compositionBefore := compositionRows()

	exec(up)

	want := map[string]state{
		cfg:         {p1, ci.ChildModeConfiguration, w.room1},
		flagged:     {p1, ci.ChildModeStandalone, ""},
		ownAsset:    {p1, ci.ChildModeStandalone, ""},
		k1:          {p1, ci.ChildModeStandalone, ""},
		k2:          {p1, ci.ChildModeStandalone, ""},
		otherClient: {p2, ci.ChildModeStandalone, ""},
		several:     before[several],
		other:       before[other],
		p1:          before[p1],
		top:         before[top],
		foreign:     before[foreign],
		mounted:     before[mounted],
	}
	for id, s := range want {
		if got := read(id); got != s {
			t.Errorf("CI %s after 000248: %+v, want %+v", id, got, s)
		}
	}

	// The quarantined rows by composition and rule.
	rows, err := tx.Query(bg, `SELECT row_data->>'id', row_data->>'rule' FROM migration_quarantine
		WHERE migration = '000248' AND source_table = 'composition' AND organization_id = $1`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string][]string{}
	for rows.Next() {
		var id, rule string
		if err = rows.Scan(&id, &rule); err != nil {
			t.Fatal(err)
		}
		rules[id] = append(rules[id], rule)
	}
	rows.Close()
	for comp, rule := range map[string]string{
		compSeveral: "parent_asset_without_ci", compNoCI: "parent_asset_without_ci", compEmptyAsset: "child_asset_without_ci",
		compOther: "other_parent", compSelf: "ci_parent_cycle", compCycle: "ci_parent_cycle",
		compForeign: "parent_of_other_client", compMounted: "rack_mount_composition_child",
	} {
		if !slices.Equal(rules[comp], []string{rule}) {
			t.Errorf("composition %s quarantined as %v, want [%s]", comp, rules[comp], rule)
		}
		delete(rules, comp)
	}
	if len(rules) != 0 {
		t.Errorf("other compositions quarantined (the mapped rows, e.g. %s, are not): %v", compCfg, rules)
	}

	// The before images: exactly the changed CIs with their former columns.
	images := map[string]state{}
	rows, err = tx.Query(bg, `SELECT row_data->>'id', COALESCE(row_data->>'parent_ci_id', ''), COALESCE(row_data->>'child_mode', ''),
			COALESCE(row_data->>'location_id', '')
		FROM migration_quarantine WHERE migration = '000248' AND source_table = 'ci' AND row_data->>'rule' = 'before_image'
		  AND organization_id = $1`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var s state
		if err = rows.Scan(&id, &s.parent, &s.mode, &s.location); err != nil {
			t.Fatal(err)
		}
		images[id] = s
	}
	rows.Close()
	wantImages := map[string]state{}
	for _, id := range []string{cfg, flagged, ownAsset, k1, k2, otherClient} {
		wantImages[id] = before[id]
	}
	if len(images) != len(wantImages) {
		t.Errorf("%d before images, want %d: %v", len(images), len(wantImages), images)
	}
	for id, s := range wantImages {
		if images[id] != s {
			t.Errorf("before image of %s: %+v, want %+v", id, images[id], s)
		}
	}
	if got := compositionRows(); got != compositionBefore {
		t.Error("000248 changed composition")
	}

	exec(down)
	for _, id := range tracked {
		if got := read(id); got != before[id] {
			t.Errorf("CI %s after the down migration: %+v, want %+v", id, got, before[id])
		}
	}
	var left int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000248'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d quarantine rows of 000248 after the down migration", left)
	}
}

// legacyAssets adapts the asset repository to ci.ParentAssetLookup as the
// server does (report_adapters.go).
type legacyAssets struct{ repo asset.Repository }

func (l legacyAssets) GetByID(ctx context.Context, orgID, id string) (*ci.ParentAssetRef, error) {
	a, err := l.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return &ci.ParentAssetRef{ID: a.ID, Name: a.Name, AssetTag: a.AssetTag, SerialNumber: a.SerialNumber}, nil
}

// legacyMux serves the CI routes behind the deprecation headers of
// api/openapi.yaml.
func legacyMux(t *testing.T, w *compWorld) *chi.Mux {
	t.Helper()
	validator := middleware.NewOpenAPIValidator()
	if loaded, err := validator.CheckSpec(true); !loaded || err != nil {
		t.Fatalf("api/openapi.yaml not loaded: %v", err)
	}
	mux := chi.NewRouter()
	mux.Use(validator.DeprecationHeaders)
	ci.NewHandler(w.svc).WithParentAssets(legacyAssets{repo: asset.NewPGRepository(w.f.App)}).RegisterRoutes(mux)
	return mux
}

func legacyGet(t *testing.T, w *compWorld, mux *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(w.ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestCompositionLegacyParentPath(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	mux := legacyMux(t, w)
	assetID := w.asset(t, "LEG-PARENT", "SN-LEG-PARENT", f.Client1, w.room1)
	parent, _ := w.create(t, map[string]any{"name": "leg-parent", "client_id": f.Client1, "asset_id": assetID}, http.StatusCreated)
	cfg, _ := w.create(t, map[string]any{"name": "leg-cfg", "client_id": f.Client1, "parent_ci_id": parent.ID,
		"child_mode": "configuration"}, http.StatusCreated)
	grandchild, _ := w.create(t, map[string]any{"name": "leg-grandchild", "client_id": f.Client1, "parent_ci_id": cfg.ID,
		"child_mode": "configuration"}, http.StatusCreated)
	standalone, _ := w.create(t, map[string]any{"name": "leg-standalone", "client_id": f.Client1, "parent_ci_id": parent.ID},
		http.StatusCreated)
	bare, _ := w.create(t, map[string]any{"name": "leg-bare", "client_id": f.Client1}, http.StatusCreated)
	bareChild, _ := w.create(t, map[string]any{"name": "leg-bare-child", "client_id": f.Client1, "parent_ci_id": bare.ID},
		http.StatusCreated)

	type answer struct {
		Composition map[string]json.RawMessage `json:"composition"`
		ParentAsset *ci.ParentAssetRef         `json:"parent_asset"`
		Inherited   []string                   `json:"inherited_fields"`
	}
	required := []string{"id", "organization_id", "parent_asset_id", "child_ci_id", "configuration_only",
		"independently_serialized", "independently_assignable", "independently_locatable",
		"independently_lifecycle_managed", "created_at", "updated_at"}
	for _, tc := range []struct {
		name          string
		child         ci.Item
		configuration bool
	}{
		{"configuration child", cfg, true},
		{"configuration grandchild (derived asset)", grandchild, true},
		{"standalone child", standalone, false},
	} {
		rec := legacyGet(t, w, mux, "/api/v1/cis/"+tc.child.ID+"/parent")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
		var body answer
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, key := range required {
			if _, ok := body.Composition[key]; !ok {
				t.Errorf("%s: composition without %s: %s", tc.name, key, rec.Body.String())
			}
		}
		child := w.get(t, tc.child.ID)
		str := func(key string) string {
			var s string
			_ = json.Unmarshal(body.Composition[key], &s)
			return s
		}
		flag := func(key string) bool {
			var b bool
			_ = json.Unmarshal(body.Composition[key], &b)
			return b
		}
		if str("id") != child.ID || str("child_ci_id") != child.ID || str("organization_id") != f.OrgA ||
			str("parent_asset_id") != assetID || str("created_at") != child.CreatedAt || str("updated_at") != child.UpdatedAt {
			t.Errorf("%s: composition %s, want the child %s with asset %s", tc.name, rec.Body.String(), child.ID, assetID)
		}
		if flag("configuration_only") != tc.configuration {
			t.Errorf("%s: configuration_only %v", tc.name, flag("configuration_only"))
		}
		for _, key := range []string{"independently_serialized", "independently_assignable", "independently_locatable",
			"independently_lifecycle_managed"} {
			if flag(key) == tc.configuration {
				t.Errorf("%s: %s %v", tc.name, key, flag(key))
			}
		}
		if body.ParentAsset == nil || body.ParentAsset.ID != assetID || body.ParentAsset.SerialNumber != "SN-LEG-PARENT" {
			t.Errorf("%s: parent_asset %+v", tc.name, body.ParentAsset)
		}
		if tc.configuration && (len(body.Inherited) != 11 || !slices.Contains(body.Inherited, "serial_number")) {
			t.Errorf("%s: inherited_fields %v", tc.name, body.Inherited)
		}
		if !tc.configuration && (body.Inherited == nil || len(body.Inherited) != 0) {
			t.Errorf("%s: inherited_fields %v, want []", tc.name, body.Inherited)
		}
		if got := rec.Header().Get("Deprecation"); got == "" || got[0] != '@' {
			t.Errorf("%s: Deprecation %q", tc.name, got)
		}
		if _, err := http.ParseTime(rec.Header().Get("Sunset")); err != nil {
			t.Errorf("%s: Sunset %q: %v", tc.name, rec.Header().Get("Sunset"), err)
		}
		if got, want := rec.Header().Get("Link"), "</api/v1/cis/"+tc.child.ID+`>; rel="successor-version"`; got != want {
			t.Errorf("%s: Link %q, want %q", tc.name, got, want)
		}
	}
	for name, id := range map[string]string{"without parent": parent.ID, "parent without asset": bareChild.ID, "unknown": f.ID()} {
		if rec := legacyGet(t, w, mux, "/api/v1/cis/"+id+"/parent"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s, want 404", name, rec.Code, rec.Body.String())
		}
	}
}

func TestCompositionLegacyAssetChildren(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	mux := legacyMux(t, w)
	assetID := w.asset(t, "LEG-KIDS", "SN-LEG-KIDS", f.Client1, w.room1)
	first, _ := w.create(t, map[string]any{"name": "leg-kids-1", "client_id": f.Client1, "asset_id": assetID}, http.StatusCreated)
	second, _ := w.create(t, map[string]any{"name": "leg-kids-2", "client_id": f.Client1, "asset_id": assetID}, http.StatusCreated)
	want := map[string]string{}
	for i, p := range []struct{ parent, mode string }{
		{first.ID, "configuration"}, {first.ID, "standalone"}, {first.ID, ""}, {second.ID, "configuration"},
	} {
		body := map[string]any{"name": "leg-kid-" + strconv.Itoa(i), "client_id": f.Client1, "parent_ci_id": p.parent}
		if p.mode != "" {
			body["child_mode"] = p.mode
		}
		kid, _ := w.create(t, body, http.StatusCreated)
		want[kid.ID] = kid.ChildMode
	}
	// A grandchild is no child of the asset's CIs.
	for id, mode := range want {
		if mode == ci.ChildModeConfiguration {
			w.create(t, map[string]any{"name": "leg-grandkid", "client_id": f.Client1, "parent_ci_id": id}, http.StatusCreated)
			break
		}
	}

	type page struct {
		Data []struct {
			ID        string `json:"id"`
			ChildMode string `json:"child_mode"`
		} `json:"data"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	got := map[string]string{}
	path := "/api/v1/assets/" + assetID + "/children?limit=3"
	var pages []int
	for path != "" && len(pages) < 5 {
		rec := legacyGet(t, w, mux, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		var p page
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get("Deprecation") != "" {
			t.Errorf("children list marked deprecated")
		}
		pages = append(pages, len(p.Data))
		for _, item := range p.Data {
			got[item.ID] = item.ChildMode
		}
		path = ""
		if p.NextCursor != "" {
			path = "/api/v1/assets/" + assetID + "/children?limit=3&cursor=" + p.NextCursor
		}
	}
	if !slices.Equal(pages, []int{3, 1}) {
		t.Errorf("pages %v, want [3 1]", pages)
	}
	if len(got) != len(want) {
		t.Errorf("children %v, want %v", got, want)
	}
	for id, mode := range want {
		if got[id] != mode {
			t.Errorf("child %s: mode %q, want %q", id, got[id], mode)
		}
	}
	rec := legacyGet(t, w, mux, "/api/v1/assets/"+f.ID()+"/children")
	var empty page
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &empty) != nil || len(empty.Data) != 0 {
		t.Errorf("children of an unknown asset: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCompositionLegacyChildAssetWritable(t *testing.T) {
	w := newCompWorld(t)
	f := w.f
	parent := w.asset(t, "LEG-LOCK-PARENT", "SN-LEG-LOCK-PARENT", f.Client1, w.room1)
	child := w.asset(t, "LEG-LOCK-CHILD", "", f.Client1, "")
	// A legacy row naming the asset as composition child; before WP-841 the
	// asset handler answered 409 for the parent-owned fields of such an asset.
	if _, err := f.Admin.Exec(w.bg, `INSERT INTO composition (organization_id, parent_asset_id, child_asset_id, configuration_only)
		VALUES ($1, $2, $3, true)`, f.OrgA, parent, child); err != nil {
		t.Fatal(err)
	}
	repo := asset.NewPGRepository(f.App)
	mux := chi.NewRouter()
	asset.NewHandler(repo).RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPatch, "/api/v1/assets/"+child,
		strings.NewReader(`{"serial_number": "SN-LEG-LOCK-CHILD", "invoice_number": "INV-LEG-LOCK"}`)).WithContext(w.ctx)
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH former child asset: %d %s, want 200", rec.Code, rec.Body.String())
	}
	got, err := repo.GetByID(w.ctx, f.OrgA, child)
	if err != nil {
		t.Fatal(err)
	}
	if got.SerialNumber != "SN-LEG-LOCK-CHILD" || got.InvoiceNumber != "INV-LEG-LOCK" {
		t.Errorf("asset serial %q, invoice %q after the PATCH", got.SerialNumber, got.InvoiceNumber)
	}
}
