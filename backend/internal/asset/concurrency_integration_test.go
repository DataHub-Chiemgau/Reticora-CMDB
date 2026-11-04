package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-740, AST-01, API-07, EVT-03):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssetVersion|TestAssetEvents' ./internal/asset/

// versionWorld mounts the asset routes over PostgreSQL with the event
// outbox, the history routes and a CI lookup of the primary CI.
type versionWorld struct {
	*assetWorld
	repo *asset.PGRepository
}

// fakeCIs resolves every CI to its identity (the CI lookup of the server).
type fakeCIs struct{}

func (fakeCIs) GetByID(_ context.Context, _, id string) (*asset.CIRef, error) {
	return &asset.CIRef{ID: id, Name: "wp740-ci", Status: "active", Hostname: "host-740"}, nil
}

func newVersionWorld(t *testing.T) *versionWorld {
	t.Helper()
	f := scopetest.Seed(t, "2b")
	repo := asset.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	mux := chi.NewRouter()
	asset.NewHandler(repo).WithCIs(fakeCIs{}).RegisterRoutes(mux)
	history.NewHandler(history.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &versionWorld{assetWorld: &assetWorld{f: f, mux: mux}, repo: repo}
}

// patchIf sends PATCH with If-Match (none when ifMatch is empty).
func (w *versionWorld) patchIf(t *testing.T, id, ifMatch, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, assetsPath+"/"+id, bytes.NewBufferString(body))
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	req = req.WithContext(tenant.WithTenant(w.f.OrgCtx(w.f.OrgA), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User}))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("PATCH %s If-Match %q %s: got %d %s, want %d", id, ifMatch, body, rec.Code, rec.Body.String(), want)
	}
	return rec
}

// conflict decodes a 409 of a stale If-Match.
func conflict(t *testing.T, rec *httptest.ResponseRecorder) (fields []string, current int64) {
	t.Helper()
	var p struct {
		Fields         []string `json:"fields"`
		CurrentVersion int64    `json:"current_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.CurrentVersion == 0 {
		t.Fatalf("409 body %s (%v)", rec.Body.String(), err)
	}
	if rec.Header().Get("ETag") != api.ETag(p.CurrentVersion) {
		t.Fatalf("409 ETag %q, want %q", rec.Header().Get("ETag"), api.ETag(p.CurrentVersion))
	}
	return p.Fields, p.CurrentVersion
}

func (w *versionWorld) version(t *testing.T, id string) int64 {
	t.Helper()
	var v int64
	if err := w.f.Admin.QueryRow(context.Background(), `SELECT version FROM asset WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestAssetVersionConcurrency covers AST-01 version and API-07 for assets
// (WP-740): GET, POST and PATCH carry the ETag; without If-Match under
// require_if_match and with an unknown version PATCH is 412; an older
// version is 409 with fields and current_version only when a field of the
// request changed since, 200 for an untouched field; a lifecycle
// transition (whose trail row carries no version) raises the version and
// makes any field of the request a conflict, a stock movement or another
// direct writer is traced field by field (WP-794); a
// change that changes nothing keeps the version; entity_change carries
// entity_version; a principal of client 1 sees the trail of a client-2
// asset neither through /history/asset/{id} nor through
// /assets/{id}/state.
func TestAssetVersionConcurrency(t *testing.T) {
	w := newVersionWorld(t)
	f := w.f
	bg := context.Background()

	created := w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"inventory_no":"V-1","name":"Laptop","client_id":"` + f.Client1 + `"}`, want: http.StatusCreated})
	a := decodeAsset(t, created)
	if a.Version != 1 || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("created: version %d ETag %q", a.Version, created.Header().Get("ETag"))
	}
	path := assetsPath + "/" + a.ID
	if got := w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK}); got.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET ETag %q", got.Header().Get("ETag"))
	}

	// A change based on the current version, then one on the older version.
	if rec := w.patchIf(t, a.ID, `"1"`, `{"model":"T14"}`, http.StatusOK); rec.Header().Get("ETag") != `"2"` || decodeAsset(t, rec).Version != 2 {
		t.Fatalf("PATCH ETag %q %s", rec.Header().Get("ETag"), rec.Body.String())
	}
	if fields, current := conflict(t, w.patchIf(t, a.ID, `"1"`, `{"model":"T16","notes":"x"}`, http.StatusConflict)); !slices.Equal(fields, []string{"model"}) || current != 2 {
		t.Fatalf("stale model: fields %v current %d", fields, current)
	}
	if rec := w.patchIf(t, a.ID, `"1"`, `{"manufacturer":"Lenovo"}`, http.StatusOK); rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("untouched field: ETag %q", rec.Header().Get("ETag"))
	}
	// An unchanged value keeps the version.
	if rec := w.patchIf(t, a.ID, `"3"`, `{"manufacturer":"Lenovo"}`, http.StatusOK); rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("no change: ETag %q", rec.Header().Get("ETag"))
	}
	w.patchIf(t, a.ID, `"99"`, `{"notes":"x"}`, http.StatusPreconditionFailed)
	w.patchIf(t, a.ID, `"abc"`, `{"notes":"x"}`, http.StatusPreconditionFailed)
	w.do(t, &call{method: http.MethodPatch, path: assetsPath + "/" + f.ID(), body: `{"notes":"x"}`, want: http.StatusNotFound})
	w.patchIf(t, a.ID, `"3"`, `{"lifecycle_state":"retired"}`, http.StatusUnprocessableEntity)

	// require_if_match: without If-Match 412, "*" passes.
	w.admin(t, `UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, f.OrgA)
	w.patchIf(t, a.ID, "", `{"notes":"x"}`, http.StatusPreconditionFailed)
	w.patchIf(t, a.ID, "*", `{"notes":"y"}`, http.StatusOK)
	w.admin(t, `UPDATE organization SET settings = settings - 'require_if_match' WHERE id = $1`, f.OrgA)

	// The trail carries the version of each write.
	var modelVersion int64
	if err := f.Admin.QueryRow(bg, `SELECT entity_version FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1
		AND field_name = 'model'`, a.ID).Scan(&modelVersion); err != nil || modelVersion != 2 {
		t.Fatalf("entity_version of model: %d (%v)", modelVersion, err)
	}
	var unversioned int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1
		AND entity_version IS NULL`, a.ID).Scan(&unversioned); err != nil || unversioned != 0 {
		t.Fatalf("rows without entity_version: %d (%v)", unversioned, err)
	}

	// A lifecycle transition (no trail row with a version) is a change of
	// every field of the request.
	before := w.version(t, a.ID)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	service := lifecycle.NewService(lifecycle.NewPGRepository(f.App), lifecycle.NewPGStateStore(f.App),
		lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	if _, err := service.Transition(ctx, f.OrgA, "asset", a.ID, "00000000-0000-0000-0000-000000000101",
		lifecycle.TransitionRequest{ToState: "received"}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	afterTransition := w.version(t, a.ID)
	if afterTransition != before+1 {
		t.Fatalf("version after the transition: %d, want %d", afterTransition, before+1)
	}
	if fields, _ := conflict(t, w.patchIf(t, a.ID, api.ETag(before), `{"cost_center":"K-1","name":"n"}`, http.StatusConflict)); !slices.Equal(fields, []string{"cost_center", "name"}) {
		t.Fatalf("after the transition: fields %v", fields)
	}
	// A stock movement is traced by the trigger asset_history (WP-794):
	// its rows name location_id and site_id with the version, so a field
	// of the request it did not change is no conflict, a changed one is.
	site, err := locations.NewPGRepository(f.App).Create(ctx, f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp740-site"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	w.admin(t, `UPDATE asset SET location_id = $2 WHERE id = $1`, a.ID, site.ID)
	if got := w.version(t, a.ID); got != afterTransition+1 {
		t.Fatalf("version after the movement: %d, want %d", got, afterTransition+1)
	}
	w.patchIf(t, a.ID, api.ETag(afterTransition), `{"cost_center":"K-1"}`, http.StatusOK)
	w.admin(t, `UPDATE asset SET cost_center = 'K-2' WHERE id = $1`, a.ID)
	if fields, _ := conflict(t, w.patchIf(t, a.ID, api.ETag(afterTransition+2), `{"cost_center":"K-3"}`, http.StatusConflict)); !slices.Equal(fields, []string{"cost_center"}) {
		t.Fatalf("after a direct change of cost_center: fields %v", fields)
	}

	// The trail of a client-2 asset stays hidden from a client-1 principal.
	other := w.create(t, `{"inventory_no":"V-2","name":"Client 2","client_id":"`+f.Client2+`"}`)
	w.patchIf(t, other.ID, "", `{"notes":"c2"}`, http.StatusOK)
	trail := func(scope context.Context, p string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, p, http.NoBody)
		scope = tenant.WithTenant(scope, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		scope = identity.WithPrincipal(scope, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
		scope = permission.WithPolicy(scope, permission.UnrestrictedPolicy(identity.AllPermissions()))
		rec := httptest.NewRecorder()
		w.mux.ServeHTTP(rec, req.WithContext(scope))
		return rec
	}
	historyPath := "/api/v1/history/asset/" + other.ID
	if rec := trail(f.OrgCtx(f.OrgA), historyPath); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"notes"`) {
		t.Fatalf("org-wide history: %d %s", rec.Code, rec.Body.String())
	}
	if rec := trail(f.ClientCtx(f.Client1), historyPath); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"notes"`) ||
		!strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("client-1 history of a client-2 asset: %d %s", rec.Code, rec.Body.String())
	}
	if rec := trail(f.ClientCtx(f.Client1), assetsPath+"/"+other.ID+"/state"); rec.Code != http.StatusNotFound {
		t.Fatalf("client-1 state of a client-2 asset: %d %s", rec.Code, rec.Body.String())
	}
	if rec := trail(f.OrgCtx(f.OrgA), assetsPath+"/"+other.ID+"/state"); rec.Code != http.StatusOK {
		t.Fatalf("org-wide state: %d %s", rec.Code, rec.Body.String())
	}
}

// outboxEvents returns the event types of the asset in event_outbox, in
// order, with the changed_fields of asset.changed.
func outboxEvents(t *testing.T, f *scopetest.Fixture, assetID string) (types []string, changed [][]string) {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT type, data FROM event_outbox WHERE entity_id = $1 ORDER BY sequence`, assetID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var data map[string]any
		if err = rows.Scan(&typ, &data); err != nil {
			t.Fatal(err)
		}
		if data["id"] != assetID {
			t.Fatalf("%s data without the asset: %v", typ, data)
		}
		types = append(types, typ)
		if typ == events.AssetChanged {
			var fields []string
			for _, v := range data["changed_fields"].([]any) {
				fields = append(fields, v.(string))
			}
			changed = append(changed, fields)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return types, changed
}

// TestAssetEventsAndShape covers EVT-03 for assets and the answer form
// (WP-740): POST, PATCH and DELETE write asset.created, asset.changed with
// the changed fields (none for a change without effect) and asset.deleted
// in event_outbox, also the internal create of the inventory conversion
// (assetCreator, POST /api/v1/inventory/items/{id}/convert); GET answers
// the schema Asset with primary_ci and the ETag instead of {asset, ci}; a
// DELETE of an asset with a stock movement or a disposal record is 409.
func TestAssetEventsAndShape(t *testing.T) {
	w := newVersionWorld(t)
	f := w.f
	ciID := f.CI(t, f.OrgA, "", "wp740-ci")

	a := w.create(t, `{"inventory_no":"E-1","name":"Server","primary_ci_id":"`+ciID+`"}`)
	path := assetsPath + "/" + a.ID
	w.patch(t, a.ID, `{"model":"R650","custom_fields":{"rack":"r1"}}`)
	w.patch(t, a.ID, `{"model":"R650"}`)

	got := w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK})
	var body map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ref, _ := body["primary_ci"].(map[string]any)
	if body["id"] != a.ID || body["asset"] != nil || ref == nil || ref["id"] != ciID || ref["hostname"] != "host-740" ||
		body["version"] != float64(2) || got.Header().Get("ETag") != `"2"` {
		t.Fatalf("GET: %s ETag %q", got.Body.String(), got.Header().Get("ETag"))
	}

	w.do(t, &call{method: http.MethodDelete, path: path, want: http.StatusNoContent})
	types, changed := outboxEvents(t, f, a.ID)
	if !slices.Equal(types, []string{events.AssetCreated, events.AssetChanged, events.AssetDeleted}) ||
		!slices.Equal(changed[0], []string{"custom_fields", "model"}) {
		t.Fatalf("events %v changed %v", types, changed)
	}

	// The internal create of the inventory conversion.
	converted := &asset.Asset{OrganizationID: f.OrgA, AssetTag: "E-CONV", Name: "converted", Category: "hardware",
		Status: "in_stock", CustomFields: map[string]any{}}
	if err := w.repo.Create(f.OrgCtx(f.OrgA), converted); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if types, _ = outboxEvents(t, f, converted.ID); !slices.Equal(types, []string{events.AssetCreated}) {
		t.Fatalf("conversion events %v", types)
	}

	// Append-only records block the delete (E-121).
	moved := w.create(t, `{"inventory_no":"E-2","name":"moved"}`)
	w.admin(t, `INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type) VALUES ($1, 'asset', $2, 'transfer')`, f.OrgA, moved.ID)
	disposed := w.create(t, `{"inventory_no":"E-3","name":"disposed"}`)
	w.admin(t, `INSERT INTO disposal_record (organization_id, asset_id, method, performed_at) VALUES ($1, $2, 'recycling', now())`, f.OrgA, disposed.ID)
	for _, id := range []string{moved.ID, disposed.ID} {
		rec := w.do(t, &call{method: http.MethodDelete, path: assetsPath + "/" + id, want: http.StatusConflict})
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/problem+json") {
			t.Fatalf("409 content type %q", rec.Header().Get("Content-Type"))
		}
		if types, _ = outboxEvents(t, f, id); slices.Contains(types, events.AssetDeleted) {
			t.Fatalf("asset.deleted for a refused delete")
		}
	}

	// The list of API-02 over PostgreSQL: filters, a sort on a nullable
	// field with its keyset cursor, fields.
	w.patch(t, moved.ID, `{"warranty_until":"2028-01-31","manufacturer":"HP"}`)
	var seen []string
	url := assetsPath + "?filter[inventory_no][like]=E-*&filter[warranty_until][lte]=2030-01-01&sort=-warranty_until&fields=inventory_no&limit=1"
	if rec := w.do(t, &call{method: http.MethodGet, path: url, want: http.StatusOK}); !strings.Contains(rec.Body.String(), `"inventory_no":"E-2"`) ||
		strings.Contains(rec.Body.String(), `"name"`) {
		t.Fatalf("filtered list: %s", rec.Body.String())
	}
	url = assetsPath + "?filter[inventory_no][in]=E-2,E-3,E-CONV&sort=-manufacturer&limit=1"
	for pages := 0; pages < 5; pages++ {
		var page api.ListResponse[asset.Asset]
		if err := json.Unmarshal(w.do(t, &call{method: http.MethodGet, path: url, want: http.StatusOK}).Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for i := range page.Data {
			seen = append(seen, page.Data[i].AssetTag)
		}
		if page.NextCursor == "" {
			break
		}
		url = assetsPath + "?filter[inventory_no][in]=E-2,E-3,E-CONV&sort=-manufacturer&limit=1&cursor=" + page.NextCursor
	}
	if len(seen) != 3 || seen[0] != "E-2" {
		t.Fatalf("cursor walk %v, want E-2 first and three assets", seen)
	}
	w.do(t, &call{method: http.MethodGet, path: assetsPath + "?sort=notes", want: http.StatusUnprocessableEntity})
}
