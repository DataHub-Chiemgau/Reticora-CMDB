package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestComputedAttributesWritePath covers WP-717 (MET-10 computed) against
// PostgreSQL: the CI repository computes the values of computed attributes
// in the transaction of every write and records their provenance (source
// computed, rank 100, projected into _provenance):
//   - POST /cis computes them from the attributes, typed columns and the
//     linked asset; a computed key in the request is 422;
//   - PATCH of the full attribute map of a read is no change, another value
//     (null too) of a computed key 422, a changed input recomputes with
//     the same version step and history entry;
//   - a discovery update (ci.UpdateRecord) replaces an observed value of a
//     computed key and recomputes; the observed-value check refuses it;
//   - an override of an input recomputes in the override's transaction
//     (one version step, one ci_change row); PUT and DELETE of an override
//     of a computed key are 422;
//   - GET /cis/{id}/provenance answers the reason computed;
//   - a type change computes with the target type's definitions and drops
//     the values it does not define with their provenance; a merge
//     recomputes the survivor.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestComputedAttributesWritePath(t *testing.T) {
	f := scopetest.Seed(t, "17")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithComputed(types)
	svc := ci.NewService(repo).WithFieldResolver(types)
	fields := override.NewPGRepository(f.App).WithRecompute(repo)
	mux := chi.NewRouter()
	ci.NewHandler(svc).WithProvenance(fields).RegisterRoutes(mux)
	override.NewHandler(fields).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	expr := func(s string) *rules.Expr {
		t.Helper()
		e, err := rules.ParseExpr([]byte(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return &e
	}
	newType := func(key string) string {
		t.Helper()
		typ := &citype.Type{OrganizationID: f.OrgA, Key: key, Name: key, IsObservable: true}
		if err := types.Create(ctx, typ); err != nil {
			t.Fatalf("create type %s: %v", key, err)
		}
		return typ.ID
	}
	define := func(typeID, key string, computed *rules.Expr) {
		t.Helper()
		d := &citype.AttributeDefinition{CITypeID: typeID, Key: key, DataType: "text", Computed: computed}
		if err := types.CreateAttributeDefinition(ctx, f.OrgA, d); err != nil {
			t.Fatalf("define %s: %v", key, err)
		}
	}
	typeA, typeB := newType("wp717-a"), newType("wp717-b")
	define(typeA, "wp717_host", nil)
	define(typeA, "wp717_label", expr(`{"op":"upper","args":[{"op":"field","field":"wp717_host"}]}`))
	define(typeA, "wp717_where", expr(`{"op":"concat","args":[{"op":"lower","args":[{"op":"field","field":"hostname"}]},
		{"op":"literal","value":"@"},{"op":"field","field":"asset.asset_tag"}]}`))
	define(typeB, "wp717_host", nil)
	define(typeB, "wp717_lower", expr(`{"op":"lower","args":[{"op":"field","field":"wp717_host"}]}`))
	assetID := f.Asset(t, f.OrgA, f.Client1, "WP717-TAG")

	type ciBody struct {
		ID         string         `json:"id"`
		Version    int64          `json:"version"`
		Attributes map[string]any `json:"attributes"`
	}
	read := func(id string) ciBody {
		t.Helper()
		w := call(http.MethodGet, "/api/v1/cis/"+id, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", id, w.Code, w.Body.String())
		}
		var b ciBody
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	refused := func(name string, w *httptest.ResponseRecorder, field string) {
		t.Helper()
		var problem struct {
			Violations []ci.Violation `json:"violations"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || len(problem.Violations) != 1 || problem.Violations[0].Field != field ||
			problem.Violations[0].Detail != ci.CodeComputed {
			t.Errorf("%s: %d %s, want 422 on %s with detail computed", name, w.Code, w.Body.String(), field)
		}
	}
	// provenance is the computed provenance row of a field: value and rank,
	// and the source _provenance names.
	provenance := func(id, field string) (value any, rank int, projected string, found bool) {
		t.Helper()
		var raw []byte
		err := f.Admin.QueryRow(bg, `SELECT v.discovered_value, v.discovered_rank, COALESCE(c.attributes #>> ARRAY['_provenance', $2, 'source'], '')
			FROM ci_field_value v JOIN ci c ON c.id = v.ci_id
			WHERE v.ci_id = $1 AND v.field_name = $2 AND v.discovered_source = 'computed'`, id, field).Scan(&raw, &rank, &projected)
		if err != nil {
			return nil, 0, "", false
		}
		_ = json.Unmarshal(raw, &value)
		return value, rank, projected, true
	}
	changesOf := func(id string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Create: the values from the attributes, the typed host name and the
	// asset, with their provenance.
	w := call(http.MethodPost, "/api/v1/cis", `{"ci_type_id":"`+typeA+`","client_id":"`+f.Client1+`","name":"wp717-1",
		"hostname":"SRV-1","asset_id":"`+assetID+`","attributes":{"wp717_host":"alpha"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created ciBody
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ID
	if created.Attributes["wp717_label"] != "ALPHA" || created.Attributes["wp717_where"] != "srv-1@WP717-TAG" {
		t.Errorf("created attributes = %v", created.Attributes)
	}
	if v, rank, projected, ok := provenance(id, "wp717_label"); !ok || v != "ALPHA" || rank != ci.RankComputed || projected != ci.SourceComputed {
		t.Errorf("provenance of wp717_label = %v rank %d projected %q (found %v)", v, rank, projected, ok)
	}
	refused("POST with a computed key", call(http.MethodPost, "/api/v1/cis", `{"ci_type_id":"`+typeA+`","name":"wp717-x",
		"attributes":{"wp717_label":"MINE"}}`), "attributes.wp717_label")

	// PATCH of the attribute map as read: no change of a value and no
	// history (the version steps as for every manual PATCH); the computed
	// keys alone with their values write nothing at all.
	current := read(id)
	changes := changesOf(id)
	full, err := json.Marshal(map[string]any{"attributes": current.Attributes})
	if err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPatch, "/api/v1/cis/"+id, string(full)); w.Code != http.StatusOK {
		t.Fatalf("PATCH of the full attribute map: %d %s", w.Code, w.Body.String())
	}
	if after := read(id); changesOf(id) != changes || !reflect.DeepEqual(after.Attributes, current.Attributes) {
		t.Errorf("PATCH of the full attribute map changed the CI: %v → %v, %d → %d changes", current.Attributes, after.Attributes,
			changes, changesOf(id))
	}
	current = read(id)
	if w = call(http.MethodPatch, "/api/v1/cis/"+id, `{"attributes":{"wp717_label":"ALPHA","wp717_where":"srv-1@WP717-TAG"}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH of the computed keys: %d %s", w.Code, w.Body.String())
	}
	if after := read(id); after.Version != current.Version || changesOf(id) != changes {
		t.Errorf("PATCH of the computed keys with their values: v%d → v%d, %d → %d changes", current.Version, after.Version,
			changes, changesOf(id))
	}
	refused("PATCH with another value", call(http.MethodPatch, "/api/v1/cis/"+id, `{"attributes":{"wp717_label":"MINE"}}`),
		"attributes.wp717_label")
	refused("PATCH with null", call(http.MethodPatch, "/api/v1/cis/"+id, `{"attributes":{"wp717_label":null}}`),
		"attributes.wp717_label")

	// A changed input: recomputed in the same version step and history entry.
	if w = call(http.MethodPatch, "/api/v1/cis/"+id, `{"attributes":{"wp717_host":"beta"}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH of an input: %d %s", w.Code, w.Body.String())
	}
	after := read(id)
	if after.Attributes["wp717_label"] != "BETA" || after.Version != current.Version+1 || changesOf(id) != changes+1 {
		t.Errorf("after PATCH of an input: %v v%d (want v%d), %d changes (want %d)", after.Attributes, after.Version,
			current.Version+1, changesOf(id), changes+1)
	}
	if v, _, _, _ := provenance(id, "wp717_label"); v != "BETA" {
		t.Errorf("provenance after PATCH = %v", v)
	}
	var overridden int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'wp717_label'
		AND override_at IS NOT NULL`, id).Scan(&overridden); err != nil || overridden != 0 {
		t.Errorf("the manual PATCH overrode the computed value (%d, %v)", overridden, err)
	}

	// Discovery: an observed value of a computed key is invalid and, if it
	// reaches the write, replaced; the typed input recomputes.
	invalid, err := svc.InvalidObserved(ctx, f.OrgA, typeA, id, map[string]any{"wp717_label": "SEEN", "wp717_host": "seen"})
	if err != nil || !reflect.DeepEqual(invalid, []string{"wp717_label"}) {
		t.Errorf("InvalidObserved = %v, %v; want [wp717_label]", invalid, err)
	}
	host := "SRV-2"
	res, err := ci.UpdateRecord(ctx, repo, f.OrgA, id, &ci.RecordUpdate{Update: ci.UpdateRequest{Hostname: &host,
		Attributes: map[string]any{"wp717_label": "SEEN"}}})
	if err != nil || !res.Changed {
		t.Fatalf("UpdateRecord: %+v, %v", res, err)
	}
	if got := read(id); got.Attributes["wp717_label"] != "BETA" || got.Attributes["wp717_where"] != "srv-2@WP717-TAG" ||
		got.Version != after.Version {
		t.Errorf("after the discovery update: %v v%d (want v%d)", got.Attributes, got.Version, after.Version)
	}

	// Override of an input: recomputed in the override's transaction, one
	// version step and one history row.
	before := read(id)
	changes = changesOf(id)
	if w = call(http.MethodPut, "/api/v1/cis/"+id+"/fields/wp717_host/override", `{"value":"gamma","reason":"Typenschild"}`); w.Code != http.StatusOK {
		t.Fatalf("override of an input: %d %s", w.Code, w.Body.String())
	}
	if got := read(id); got.Attributes["wp717_label"] != "GAMMA" || got.Version != before.Version+1 || changesOf(id) != changes+1 {
		t.Errorf("after the override: %v v%d (want v%d), %d changes (want %d)", got.Attributes, got.Version, before.Version+1,
			changesOf(id), changes+1)
	}
	if v, _, _, _ := provenance(id, "wp717_label"); v != "GAMMA" {
		t.Errorf("provenance after the override = %v", v)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if w = call(method, "/api/v1/cis/"+id+"/fields/wp717_label/override", `{"value":"MINE","reason":"x"}`); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s override of a computed key: %d %s, want 422", method, w.Code, w.Body.String())
		}
	}

	// The provenance view names the reason computed.
	w = call(http.MethodGet, "/api/v1/cis/"+id+"/provenance", "")
	var view struct {
		Data []ci.FieldProvenance `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != http.StatusOK {
		t.Fatalf("provenance: %d %s", w.Code, w.Body.String())
	}
	found := false
	for _, p := range view.Data {
		if p.Field == "wp717_label" {
			found = true
			if p.Reason != ci.ReasonComputed || p.Source != ci.SourceComputed || p.Rank != 100 || p.Value != "GAMMA" || p.ObservedAt == nil {
				t.Errorf("provenance of wp717_label = %+v", p)
			}
		}
	}
	if !found {
		t.Error("the provenance view lacks wp717_label")
	}

	// Type change: the target type computes wp717_lower; wp717_label and
	// wp717_where go with their provenance.
	before = read(id)
	tc, err := repo.ChangeType(ctx, f.OrgA, id, typeB, ci.TypeChangeManual)
	if err != nil || !tc.Changed {
		t.Fatalf("change type: %+v, %v", tc, err)
	}
	got := read(id)
	_, hasLabel := got.Attributes["wp717_label"]
	if got.Attributes["wp717_lower"] != "gamma" || hasLabel || got.Version != before.Version+1 {
		t.Errorf("after the type change: %v v%d (want v%d)", got.Attributes, got.Version, before.Version+1)
	}
	if _, _, _, ok := provenance(id, "wp717_label"); ok {
		t.Error("the provenance of wp717_label survived the type change")
	}
	if _, _, _, ok := provenance(id, "wp717_lower"); !ok {
		t.Error("no provenance of wp717_lower after the type change")
	}

	// Merge: the survivor computes from the merged values.
	survivor := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typeA, Name: "wp717-s", Status: "active"}
	victim := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typeA, Name: "wp717-v", Status: "active",
		Attributes: map[string]any{"wp717_host": "zeta"}}
	for _, item := range []*ci.Item{survivor, victim} {
		if err = repo.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if _, has := survivor.Attributes["wp717_label"]; has || victim.Attributes["wp717_label"] != "ZETA" {
		t.Fatalf("before the merge: survivor %v, victim %v", survivor.Attributes, victim.Attributes)
	}
	if _, err = repo.Merge(ctx, f.OrgA, survivor.ID, victim.ID, ""); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged := read(survivor.ID); merged.Attributes["wp717_label"] != "ZETA" {
		t.Errorf("survivor after the merge: %v", merged.Attributes)
	}
	if v, _, _, ok := provenance(survivor.ID, "wp717_label"); !ok || v != "ZETA" {
		t.Errorf("survivor's provenance after the merge = %v (found %v)", v, ok)
	}
}
