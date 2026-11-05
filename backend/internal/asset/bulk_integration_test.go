// POST /api/v1/assets/bulk against PostgreSQL (BLK-01, BLK-02; WP-781),
// wired as the server does (server.AssetBulkDelegates):
//
//   - set_status: version + 1 per asset; an invalid status is 422 for the
//     whole request without a change.
//   - set_fields: a taken inventory number is 409 on inventory_no for the
//     id, like PATCH (the same write path); a field the policy only lets
//     read is 403 with fields.
//   - set_location: one transfer per asset in the ledger with the event
//     stock.movement, the linked CI follows; a location of another client
//     is 422 on to_location_id for the id; without asset:move 403.
//   - add_tags / remove_tags: idempotent (no new version for nothing to
//     change); an unknown catalog key is 422 on params.tags without a
//     change.
//   - assign: the holder of the asset; an assigned asset is 409
//     asset-assigned; without assignment:write 403.
//   - lifecycle_transition: a failed system guard is 422 lifecycle-guard
//     with the missing reference in fields.
//   - archive: retired and the tag archived together with the audit entry
//     asset.lifecycle_changed; from repair 422 invalid-transition and
//     neither tag nor state; an archived asset is ok without a change;
//     without lifecycle:transition 403.
//   - an asset of another client is 404 for its id; 100 ids run as the job
//     asset.bulk whose report GET /api/v1/jobs/{id}/result answers.
//   - export without its port (WP-782) is 422 on action.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -p 1 -run 'TestAssetBulk' ./internal/asset/
package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// assetBulkEnv is the asset handler with its bulk on PostgreSQL.
type assetBulkEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	mux *chi.Mux
}

func newAssetBulkEnv(t *testing.T) *assetBulkEnv {
	t.Helper()
	f := scopetest.Seed(t, "2b")
	repo := asset.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	ledger := movement.NewService(movement.NewPGRepository(f.App))
	lcRepo := lifecycle.NewPGRepository(f.App)
	transitions := lifecycle.NewService(lcRepo, lifecycle.NewPGStateStore(f.App), lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App))).
		WithGuards(lifecycle.NewPGGuards(f.App))
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: asset.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	delegates := server.AssetBulkDelegates(ledger, assignment.NewService(f.App, ledger, events.NewOutbox()), transitions,
		lifecycle.NewResolver(f.App), nil)
	h := asset.NewHandler(repo).WithBulkJobs(jobSvc).WithBulkDelegates(&delegates)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	jobs.NewAPIHandler(jobSvc).WithKind(asset.BulkJobKind, identity.PermAssetWrite, nil).RegisterRoutes(mux)
	// The system tag archived of the organization (WP-281).
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO tag (organization_id, key, display_name, is_system)
		VALUES ($1, 'archived', '{"de":"Archiviert"}', true) ON CONFLICT DO NOTHING`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	return &assetBulkEnv{t: t, f: f, mux: mux}
}

// principal is a request context of client 1 with perms and policy (nil:
// unrestricted).
func (e *assetBulkEnv) principal(policy *permission.FieldPolicy, perms ...identity.Permission) context.Context {
	perms = append([]identity.Permission{identity.PermAssetRead, identity.PermAssetWrite, identity.PermJobRead}, perms...)
	ctx := tenant.WithTenant(e.f.ClientCtx(e.f.Client1), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User, ClientID: e.f.Client1})
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp781", CorrelationID: "wp781", Source: audit.SourceUI})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: perms})
	if policy == nil {
		policy = permission.UnrestrictedPolicy(perms)
	}
	return permission.WithPolicy(ctx, policy)
}

// full holds every right of the actions.
func (e *assetBulkEnv) full() context.Context {
	return e.principal(nil, identity.PermLifecycleTransition, identity.PermAssetMove, identity.PermAssignmentWrite)
}

func (e *assetBulkEnv) do(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			e.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// bulk posts a bulk below 100 ids as ctx and returns the report.
func (e *assetBulkEnv) bulk(ctx context.Context, body map[string]any) ci.BulkResult {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/assets/bulk", body)
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		e.t.Fatalf("bulk %v: %d %s, want 200 with the report", body["action"], w.Code, w.Body.String())
	}
	return res
}

// refused posts a bulk as ctx and checks status and field or action of the
// whole request.
func (e *assetBulkEnv) refused(ctx context.Context, body map[string]any, code int, field, action string) {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/assets/bulk", body)
	var p struct {
		Field  string `json:"field"`
		Action string `json:"action"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != code || p.Field != field || p.Action != action {
		e.t.Fatalf("bulk %v: %d %s, want %d on field %q action %q", body["action"], w.Code, w.Body.String(), code, field, action)
	}
}

// item checks the report of id: code, problem type (empty: any), fields
// (nil: any).
func item(t *testing.T, res *ci.BulkResult, id string, code int, typ string, fields ...string) {
	t.Helper()
	for _, it := range res.Items {
		if it.ID != id {
			continue
		}
		if it.Code != code || (typ != "" && it.Type != typ) || (fields != nil && !slices.Equal(it.Fields, fields)) {
			t.Fatalf("report of %s: %+v, want %d %s %v", id, it, code, typ, fields)
		}
		return
	}
	t.Fatalf("no report of %s in %+v", id, res.Items)
}

// scalar reads one value through the maintenance pool as text.
func (e *assetBulkEnv) scalar(query string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return s
}

func (e *assetBulkEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func TestAssetBulkFieldsAgainstPostgres(t *testing.T) {
	e := newAssetBulkEnv(t)
	f := e.f
	a, b := f.Asset(t, f.OrgA, f.Client1, "wp781-f1"), f.Asset(t, f.OrgA, f.Client1, "wp781-f2")
	foreign := f.Asset(t, f.OrgA, f.Client2, "wp781-f3")
	version := func(id string) string { return e.scalar(`SELECT version::text FROM asset WHERE id = $1`, id) }
	before := version(a)

	// set_status: BLK-01 for assets; the other client's asset is 404.
	res := e.bulk(e.full(), map[string]any{"status": "maintenance", "ids": []string{a, b, foreign}})
	if res.Succeeded != 2 || res.Failed != 1 {
		t.Fatalf("set_status: %+v", res)
	}
	item(t, &res, foreign, http.StatusNotFound, "")
	if got := e.scalar(`SELECT status FROM asset WHERE id = $1`, a); got != "maintenance" || version(a) == before {
		t.Errorf("asset after set_status: %s version %s (before %s)", got, version(a), before)
	}
	if got := e.scalar(`SELECT status FROM asset WHERE id = $1`, foreign); got == "maintenance" {
		t.Error("the asset of the other client changed")
	}
	e.refused(e.full(), map[string]any{"status": "inactive", "ids": []string{a}}, http.StatusUnprocessableEntity, "status", "")
	if got := e.scalar(`SELECT status FROM asset WHERE id = $1`, a); got != "maintenance" {
		t.Errorf("status after the refused request: %s", got)
	}

	// set_fields: a taken inventory number is 409 on inventory_no, like
	// PATCH (the shared write path).
	res = e.bulk(e.full(), map[string]any{"action": "set_fields", "ids": []string{b},
		"params": map[string]any{"fields": map[string]any{"inventory_no": "wp781-f1"}}})
	item(t, &res, b, http.StatusConflict, "", "inventory_no")
	w := e.do(e.full(), http.MethodPatch, "/api/v1/assets/"+b, map[string]any{"inventory_no": "wp781-f1"})
	if w.Code != http.StatusConflict {
		t.Errorf("PATCH with the taken inventory number: %d %s, want 409 like the bulk", w.Code, w.Body.String())
	}
	// A field the policy only lets read is 403 with the field.
	grants := permission.NewMemoryFieldGrants()
	role, key := "00000000-0000-4000-8000-000000000781", "core.notes"
	if _, err := grants.CreateFieldGrant(context.Background(), f.OrgA, &permission.FieldGrant{RoleID: &role,
		ObjectKind: permission.KindAsset, FieldKey: &key, Level: permission.LevelRead}); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, grants, nil).PolicyForGrants(context.Background(), f.OrgA,
		[]identity.Grant{{RoleID: role, Permissions: []identity.Permission{identity.PermAssetRead, identity.PermAssetWrite}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res = e.bulk(e.principal(policy), map[string]any{"action": "set_fields", "ids": []string{a},
		"params": map[string]any{"fields": map[string]any{"notes": "wp781"}}})
	item(t, &res, a, http.StatusForbidden, "", "notes")
	res = e.bulk(e.full(), map[string]any{"action": "set_fields", "ids": []string{a, b},
		"params": map[string]any{"fields": map[string]any{"notes": "wp781", "cost_center": "CC-781"}}})
	if res.Succeeded != 2 || e.scalar(`SELECT cost_center FROM asset WHERE id = $1`, b) != "CC-781" {
		t.Errorf("set_fields: %+v", res)
	}
}

func TestAssetBulkLocationAndTagsAgainstPostgres(t *testing.T) {
	e := newAssetBulkEnv(t)
	f := e.f
	room, foreignRoom := f.Room(t, f.Client1, "wp781-r1"), f.Room(t, f.Client2, "wp781-r2")
	a, b := f.Asset(t, f.OrgA, f.Client1, "wp781-l1"), f.Asset(t, f.OrgA, f.Client1, "wp781-l2")
	linked := f.CI(t, f.OrgA, f.Client1, "wp781-l1-ci")
	e.exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, linked, a)

	// set_location: a transfer per asset, stock.movement, the CI follows.
	res := e.bulk(e.full(), map[string]any{"action": "set_location", "ids": []string{a, b}, "params": map[string]any{"to_location_id": room}})
	if res.Succeeded != 2 {
		t.Fatalf("set_location: %+v", res)
	}
	for _, id := range []string{a, b} {
		if got := e.scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1 AND movement_type = 'transfer' AND to_location_id = $2`, id, room); got != "1" {
			t.Errorf("transfers of %s: %s, want 1", id, got)
		}
		if got := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1`, id); got != "1" {
			t.Errorf("stock.movement of %s: %s, want 1", id, got)
		}
		if got := e.scalar(`SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, id); got != room {
			t.Errorf("location of %s: %s, want %s", id, got, room)
		}
	}
	if got := e.scalar(`SELECT COALESCE(location_id::text, '') FROM ci WHERE id = $1`, linked); got != room {
		t.Errorf("location of the linked CI: %s, want %s", got, room)
	}
	res = e.bulk(e.full(), map[string]any{"action": "set_location", "ids": []string{a}, "params": map[string]any{"to_location_id": foreignRoom}})
	item(t, &res, a, http.StatusUnprocessableEntity, "", "to_location_id")
	e.refused(e.principal(nil), map[string]any{"action": "set_location", "ids": []string{a}, "params": map[string]any{"to_location_id": room}},
		http.StatusForbidden, "", string(identity.PermAssetMove))

	// add_tags / remove_tags through asset_tag_link, idempotent.
	e.exec(`INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'wp781', '{"de":"wp781"}')`, f.OrgA)
	add := map[string]any{"action": "add_tags", "ids": []string{a, b}, "params": map[string]any{"tags": []string{"wp781"}}}
	if res = e.bulk(e.full(), add); res.Succeeded != 2 {
		t.Fatalf("add_tags: %+v", res)
	}
	v := e.scalar(`SELECT version::text FROM asset WHERE id = $1`, a)
	if res = e.bulk(e.full(), add); res.Succeeded != 2 || e.scalar(`SELECT version::text FROM asset WHERE id = $1`, a) != v {
		t.Errorf("add_tags again: %+v, version changed", res)
	}
	if got := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'asset.changed' AND entity_id = $1 AND data->'changed_fields' ? 'tags'`, a); got != "1" {
		t.Errorf("asset.changed with tags: %s, want 1", got)
	}
	e.refused(e.full(), map[string]any{"action": "add_tags", "ids": []string{a}, "params": map[string]any{"tags": []string{"wp781-unknown"}}},
		http.StatusUnprocessableEntity, "params.tags", "")
	if res = e.bulk(e.full(), map[string]any{"action": "remove_tags", "ids": []string{a}, "params": map[string]any{"tags": []string{"wp781"}}}); res.Succeeded != 1 {
		t.Fatalf("remove_tags: %+v", res)
	}
	if got := e.scalar(`SELECT count(*)::text FROM asset_tag_link WHERE asset_id = $1`, a); got != "0" {
		t.Errorf("links after remove_tags: %s", got)
	}
}

func TestAssetBulkAssignLifecycleArchiveAgainstPostgres(t *testing.T) {
	e := newAssetBulkEnv(t)
	f := e.f
	holder := f.AppUser(t, f.OrgA, "wp781-holder")

	// assign: the holder; an assigned asset is 409 asset-assigned.
	a := f.Asset(t, f.OrgA, f.Client1, "wp781-a1")
	assign := map[string]any{"action": "assign", "ids": []string{a}, "params": map[string]any{"assigned_user_id": holder}}
	if res := e.bulk(e.full(), assign); res.Succeeded != 1 {
		t.Fatalf("assign: %+v", res)
	}
	if got := e.scalar(`SELECT COALESCE(assigned_user_id::text, '') FROM asset WHERE id = $1`, a); got != holder {
		t.Errorf("holder: %s, want %s", got, holder)
	}
	res := e.bulk(e.full(), assign)
	item(t, &res, a, http.StatusConflict, assignment.ProblemAssetAssigned)
	e.refused(e.principal(nil, identity.PermLifecycleTransition), assign, http.StatusForbidden, "", string(identity.PermAssignmentWrite))

	// lifecycle_transition: in_stock without a storage location fails the
	// system guard.
	g := f.Asset(t, f.OrgA, f.Client1, "wp781-g1")
	e.exec(`UPDATE asset SET lifecycle_state = 'received' WHERE id = $1`, g)
	res = e.bulk(e.full(), map[string]any{"action": "lifecycle_transition", "ids": []string{g}, "params": map[string]any{"to_state": "in_stock"}})
	item(t, &res, g, http.StatusUnprocessableEntity, lifecycle.ProblemLifecycleGuard, "location_id")

	// archive: retired and the tag together; from repair nothing changes.
	archived, repair := f.Asset(t, f.OrgA, f.Client1, "wp781-x1"), f.Asset(t, f.OrgA, f.Client1, "wp781-x2")
	e.exec(`UPDATE asset SET lifecycle_state = 'in_stock' WHERE id = $1`, archived)
	e.exec(`UPDATE asset SET lifecycle_state = 'repair' WHERE id = $1`, repair)
	repairVersion := e.scalar(`SELECT version::text FROM asset WHERE id = $1`, repair)
	archive := map[string]any{"action": "archive", "ids": []string{archived, repair}}
	e.refused(e.principal(nil), archive, http.StatusForbidden, "", string(identity.PermLifecycleTransition))
	res = e.bulk(e.full(), archive)
	item(t, &res, archived, http.StatusOK, "")
	item(t, &res, repair, http.StatusUnprocessableEntity, lifecycle.ProblemInvalidTransition)
	tagged := func(id string) string {
		return e.scalar(`SELECT count(*)::text FROM asset_tag_link l JOIN tag t ON t.id = l.tag_id WHERE l.asset_id = $1 AND t.key = 'archived'`, id)
	}
	if state := e.scalar(`SELECT lifecycle_state FROM asset WHERE id = $1`, archived); state != "retired" || tagged(archived) != "1" {
		t.Errorf("archived asset: state %s, tag %s", state, tagged(archived))
	}
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE resource_id = $1 AND action = 'asset.lifecycle_changed'`, archived); got != "1" {
		t.Errorf("audit entries asset.lifecycle_changed: %s, want 1", got)
	}
	if state := e.scalar(`SELECT lifecycle_state FROM asset WHERE id = $1`, repair); state != "repair" || tagged(repair) != "0" ||
		e.scalar(`SELECT version::text FROM asset WHERE id = $1`, repair) != repairVersion {
		t.Errorf("asset in repair after archive: state %s, tag %s", state, tagged(repair))
	}
	v := e.scalar(`SELECT version::text FROM asset WHERE id = $1`, archived)
	res = e.bulk(e.full(), map[string]any{"action": "archive", "ids": []string{archived}})
	item(t, &res, archived, http.StatusOK, "")
	if e.scalar(`SELECT version::text FROM asset WHERE id = $1`, archived) != v {
		t.Error("archive of an archived asset changed it")
	}

	// export is unknown while its port is not wired (WP-782).
	e.refused(e.full(), map[string]any{"action": "export", "ids": []string{a}, "params": map[string]any{"format": "csv"}},
		http.StatusUnprocessableEntity, "action", "")
}

func TestAssetBulkJobAgainstPostgres(t *testing.T) {
	e := newAssetBulkEnv(t)
	f := e.f
	ids := make([]string, 0, asset.BulkJobFromIDs)
	ids = append(ids, f.Asset(t, f.OrgA, f.Client1, "wp781-j0"))
	for len(ids) < asset.BulkJobFromIDs {
		ids = append(ids, f.ID())
	}
	ctx := e.full()
	w := e.do(ctx, http.MethodPost, "/api/v1/assets/bulk", map[string]any{"status": "lost", "ids": ids})
	var accepted struct {
		JobID string `json:"job_id"`
		Kind  string `json:"kind"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.Kind != asset.BulkJobKind {
		t.Fatalf("100 ids: %d %s, want 202 with the job asset.bulk", w.Code, w.Body.String())
	}
	var res ci.BulkResult
	deadline := time.Now().Add(20 * time.Second)
	for {
		r := e.do(ctx, http.MethodGet, "/api/v1/jobs/"+accepted.JobID+"/result", nil)
		if r.Code == http.StatusOK && json.Unmarshal(r.Body.Bytes(), &res) == nil && res.Total == asset.BulkJobFromIDs {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job result: %d %s", r.Code, r.Body.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res.Succeeded != 1 || res.Failed != asset.BulkJobFromIDs-1 {
		t.Errorf("job report: succeeded %d failed %d", res.Succeeded, res.Failed)
	}
	item(t, &res, ids[0], http.StatusOK, "")
	item(t, &res, ids[1], http.StatusNotFound, "")
	if got := e.scalar(`SELECT status FROM asset WHERE id = $1`, ids[0]); got != "lost" {
		t.Errorf("status after the job: %s", got)
	}
}
