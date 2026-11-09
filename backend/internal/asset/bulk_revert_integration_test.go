// The undo of POST /api/v1/assets/bulk against PostgreSQL (BLK-02, E-25;
// WP-784), wired as server/router.go does: the asset handler with its
// delegating actions (server.AssetBulkDelegates), bulk jobs also below 100
// ids, the reverter of asset.bulk and the kind bulk.revert.
//
//   - a synchronous asset bulk is a job asset.bulk whose id the 200 report
//     names (bulk_job_id).
//   - set_status / set_fields: the old values go back; a field changed by
//     PATCH since the bulk stays and is reported skipped.
//   - set_location: the transfer back to the former location as a
//     movement, the ledger holds both; an asset moved since is skipped
//     (409 location_id); a reverter without asset:move gets 403 skipped.
//   - assign: the return movement; add_tags / remove_tags: the inverse
//     operation on asset_tag_link; archive: the transition back and the tag
//     archived removed.
//   - a caller without asset:write is refused (403); an asset outside the
//     caller's scope is skipped (404) and unchanged.
//   - module gate: through server.NewRouter without the module inventory
//     the revert of an asset.bulk job is 409 (kind without revert).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -p 1 -run 'TestAssetBulkRevert' ./internal/asset/
package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// assetRevertEnv is the asset bulk with the revert of asset.bulk.
type assetRevertEnv struct {
	*assetBulkEnv
	jobSvc *jobs.Service
}

func newAssetRevertEnv(t *testing.T) *assetRevertEnv {
	t.Helper()
	f := scopetest.Seed(t, "2c")
	repo := asset.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	ledger := movement.NewService(movement.NewPGRepository(f.App))
	lcRepo := lifecycle.NewPGRepository(f.App)
	transitions := lifecycle.NewService(lcRepo, lifecycle.NewPGStateStore(f.App), lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App))).
		WithGuards(lifecycle.NewPGGuards(f.App))
	store := jobs.NewPGRepository(f.App)
	jobSvc := jobs.NewService(store)
	jobSvc.Register(jobs.Kind{Name: asset.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	jobSvc.Register(jobs.Kind{Name: jobs.RevertKind, MaxAttempts: 1})
	delegates := server.AssetBulkDelegates(ledger, assignment.NewService(f.App, ledger, events.NewOutbox()), transitions,
		lifecycle.NewResolver(f.App), nil)
	h := asset.NewHandler(repo).WithBulkJobs(jobSvc).WithBulkDelegates(&delegates)
	revert := asset.NewBulkRevert(h, jobSvc, store)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	jobs.NewAPIHandler(jobSvc).
		WithKind(asset.BulkJobKind, identity.PermAssetWrite, revert.Revert).
		WithBulkKind(asset.BulkJobKind).
		WithKind(jobs.RevertKind, identity.PermCIWrite, nil).
		RegisterRoutes(mux)
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO tag (organization_id, key, display_name, is_system)
		VALUES ($1, 'archived', '{"de":"Archiviert"}', true) ON CONFLICT DO NOTHING`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	return &assetRevertEnv{assetBulkEnv: &assetBulkEnv{t: t, f: f, mux: mux}, jobSvc: jobSvc}
}

// run posts a bulk as ctx and returns the report; every id must succeed
// and the report must name its job.
func (e *assetRevertEnv) run(ctx context.Context, body map[string]any) ci.BulkResult {
	e.t.Helper()
	res := e.bulk(ctx, body)
	if res.BulkJobID == "" || res.Succeeded != res.Total {
		e.t.Fatalf("bulk %v: %+v, want every id ok and the bulk job", body["action"], res)
	}
	return res
}

// revert posts the revert of the bulk job as ctx, waits for the revert job
// and returns its report.
func (e *assetRevertEnv) revert(ctx context.Context, bulkJobID string) ci.BulkRevertResult {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/jobs/"+bulkJobID+"/revert", nil)
	var accepted jobs.Job
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.Kind != jobs.RevertKind {
		e.t.Fatalf("revert of %s: %d %s, want 202 with the revert job", bulkJobID, w.Code, w.Body.String())
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		job, err := e.jobSvc.Get(context.Background(), e.f.OrgA, accepted.ID)
		if err == nil && job.Status.Terminal() {
			if job.Status != jobs.StatusSucceeded {
				e.t.Fatalf("revert job ended %s: %s", job.Status, job.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("revert job %s did not end", accepted.ID)
		}
	}
	w = e.do(ctx, http.MethodGet, "/api/v1/jobs/"+accepted.ID+"/result", nil)
	var res ci.BulkRevertResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.BulkJobID != bulkJobID {
		e.t.Fatalf("result of the revert job: %d %s", w.Code, w.Body.String())
	}
	return res
}

// outcome checks the revert outcome of id: result, code and (non-nil) the
// fields.
func outcome(t *testing.T, res *ci.BulkRevertResult, id, result string, code int, fields ...string) {
	t.Helper()
	for _, it := range res.Items {
		if it.ID != id {
			continue
		}
		if it.Result != result || it.Code != code || (fields != nil && strings.Join(it.Fields, ",") != strings.Join(fields, ",")) {
			t.Fatalf("revert outcome of %s: %+v, want %s %d %v", id, it, result, code, fields)
		}
		return
	}
	t.Fatalf("no revert outcome of %s in %+v", id, res.Items)
}

func TestAssetBulkRevertFieldsAgainstPostgres(t *testing.T) {
	e := newAssetRevertEnv(t)
	f := e.f
	a, b := f.Asset(t, f.OrgA, f.Client1, "wp784-f1"), f.Asset(t, f.OrgA, f.Client1, "wp784-f2")
	e.exec(`UPDATE asset SET notes = 'before', cost_center = 'CC-0' WHERE id = ANY($1::uuid[])`, []string{a, b})
	status := e.scalar(`SELECT status FROM asset WHERE id = $1`, a)

	// A synchronous bulk is a job asset.bulk with its change set.
	setStatus := e.run(e.full(), map[string]any{"status": "maintenance", "ids": []string{a, b}})
	if got := e.scalar(`SELECT kind || ' ' || status FROM job WHERE id = $1`, setStatus.BulkJobID); got != asset.BulkJobKind+" succeeded" {
		t.Fatalf("bulk job: %s", got)
	}
	if got := e.scalar(`SELECT count(*)::text FROM bulk_change_set WHERE job_id = $1 AND entity_type = 'asset' AND field_name = 'status'
		AND old_value = to_jsonb($2::text) AND client_id = $3`, setStatus.BulkJobID, status, f.Client1); got != "2" {
		t.Fatalf("change set rows of set_status: %s, want 2", got)
	}

	// set_fields; notes of a is changed by PATCH since the bulk.
	setFields := e.run(e.full(), map[string]any{"action": "set_fields", "ids": []string{a, b},
		"params": map[string]any{"fields": map[string]any{"notes": "bulk", "cost_center": "CC-784"}}})
	if w := e.do(e.full(), http.MethodPatch, "/api/v1/assets/"+a, map[string]any{"notes": "manual"}); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}

	// Without asset:write the revert is refused, also for the creator.
	noWrite := tenant.WithTenant(e.f.ClientCtx(e.f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client1})
	noWrite = identity.WithPrincipal(noWrite, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermAssetRead, identity.PermJobRead}})
	if w := e.do(noWrite, http.MethodPost, "/api/v1/jobs/"+setFields.BulkJobID+"/revert", nil); w.Code != http.StatusForbidden {
		t.Fatalf("revert without asset:write: %d %s, want 403", w.Code, w.Body.String())
	}

	res := e.revert(e.full(), setFields.BulkJobID)
	outcome(t, &res, a, "reverted", http.StatusOK, "notes")
	outcome(t, &res, b, "reverted", http.StatusOK, []string{}...)
	if got := e.scalar(`SELECT notes || ' ' || cost_center FROM asset WHERE id = $1`, a); got != "manual CC-0" {
		t.Errorf("a after the revert: %s, want manual CC-0", got)
	}
	if got := e.scalar(`SELECT notes || ' ' || cost_center FROM asset WHERE id = $1`, b); got != "before CC-0" {
		t.Errorf("b after the revert: %s, want before CC-0", got)
	}
	if got := e.scalar(`SELECT string_agg(field_name || ':' || revert_outcome, ',' ORDER BY field_name) FROM bulk_change_set
		WHERE job_id = $1 AND entity_id = $2`, setFields.BulkJobID, a); got != "cost_center:reverted,notes:skipped" {
		t.Errorf("marks of a: %s", got)
	}
	// The inverse write goes through the single path: trail and event.
	if got := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'asset.changed' AND entity_id = $1
		AND data->'changed_fields' ? 'cost_center'`, b); got != "2" {
		t.Errorf("asset.changed of cost_center of b: %s, want 2", got)
	}
	if w := e.do(e.full(), http.MethodPost, "/api/v1/jobs/"+setFields.BulkJobID+"/revert", nil); w.Code != http.StatusConflict {
		t.Errorf("second revert: %d %s, want 409", w.Code, w.Body.String())
	}

	res = e.revert(e.full(), setStatus.BulkJobID)
	outcome(t, &res, a, "reverted", http.StatusOK)
	if got := e.scalar(`SELECT status FROM asset WHERE id = $1`, b); got != status {
		t.Errorf("status of b after the revert: %s, want %s", got, status)
	}
}

func TestAssetBulkRevertMovementsAndTagsAgainstPostgres(t *testing.T) {
	e := newAssetRevertEnv(t)
	f := e.f
	room1, room2 := f.Room(t, f.Client1, "wp784-r1"), f.Room(t, f.Client1, "wp784-r2")
	a, b, c := f.Asset(t, f.OrgA, f.Client1, "wp784-l1"), f.Asset(t, f.OrgA, f.Client1, "wp784-l2"), f.Asset(t, f.OrgA, f.Client1, "wp784-l3")
	location := func(id string) string {
		return e.scalar(`SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, id)
	}
	move := func(to string, ids ...string) string {
		return e.run(e.full(), map[string]any{"action": "set_location", "ids": ids, "params": map[string]any{"to_location_id": to}}).BulkJobID
	}

	// set_location: back to the former location; b moved on since.
	first := move(room1, a, b, c)
	second := move(room2, a, b, c)
	move(room1, b)
	res := e.revert(e.full(), second)
	outcome(t, &res, a, "reverted", http.StatusOK)
	outcome(t, &res, b, "skipped", http.StatusConflict, "location_id")
	if location(a) != room1 || location(b) != room1 {
		t.Errorf("locations after the revert: a %s b %s, want %s", location(a), location(b), room1)
	}
	if got := e.scalar(`SELECT string_agg(to_location_id::text, ',' ORDER BY occurred_at, created_at) FROM stock_movement
		WHERE asset_id = $1 AND movement_type = 'transfer'`, a); got != room1+","+room2+","+room1 {
		t.Errorf("transfers of a: %s, want the bulk's and the one back", got)
	}
	// The first bulk moved the assets from no location: no transfer back.
	res = e.revert(e.full(), first)
	outcome(t, &res, a, "skipped", http.StatusConflict)

	// A reverter without asset:move: c is skipped with 403.
	again := move(room2, c)
	res = e.revert(e.principal(nil), again)
	outcome(t, &res, c, "skipped", http.StatusForbidden)
	if location(c) != room2 {
		t.Errorf("location of c after the refused revert: %s, want %s", location(c), room2)
	}

	// assign: the return movement.
	holder := f.AppUser(t, f.OrgA, "wp784-holder")
	d := f.Asset(t, f.OrgA, f.Client1, "wp784-a1")
	assigned := e.run(e.full(), map[string]any{"action": "assign", "ids": []string{d}, "params": map[string]any{"assigned_user_id": holder}})
	res = e.revert(e.full(), assigned.BulkJobID)
	outcome(t, &res, d, "reverted", http.StatusOK)
	if got := e.scalar(`SELECT COALESCE(assigned_user_id::text, '') FROM asset WHERE id = $1`, d); got != "" {
		t.Errorf("holder after the revert: %s, want none", got)
	}
	if got := e.scalar(`SELECT string_agg(movement_type, ',' ORDER BY occurred_at, created_at) FROM stock_movement WHERE asset_id = $1`, d); got != "assignment,return" {
		t.Errorf("movements of d: %s, want assignment,return", got)
	}
	if got := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'asset.returned' AND entity_id = $1`, d); got != "1" {
		t.Errorf("asset.returned of d: %s, want 1", got)
	}

	// add_tags / remove_tags on asset_tag_link.
	e.exec(`INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'wp784', '{"de":"wp784"}'), ($1, 'wp784-keep', '{"de":"k"}')`, f.OrgA)
	tags := func(id string) string {
		return e.scalar(`SELECT COALESCE(string_agg(t.key, ',' ORDER BY t.key), '') FROM asset_tag_link l JOIN tag t ON t.id = l.tag_id
			WHERE l.asset_id = $1`, id)
	}
	added := e.run(e.full(), map[string]any{"action": "add_tags", "ids": []string{a, b}, "params": map[string]any{"tags": []string{"wp784", "wp784-keep"}}})
	removed := e.run(e.full(), map[string]any{"action": "remove_tags", "ids": []string{a}, "params": map[string]any{"tags": []string{"wp784-keep"}}})
	res = e.revert(e.full(), removed.BulkJobID)
	outcome(t, &res, a, "reverted", http.StatusOK)
	if tags(a) != "wp784,wp784-keep" {
		t.Errorf("tags of a after the revert of remove_tags: %s", tags(a))
	}
	res = e.revert(e.full(), added.BulkJobID)
	outcome(t, &res, b, "reverted", http.StatusOK)
	if tags(a) != "" || tags(b) != "" {
		t.Errorf("tags after the revert of add_tags: a %q b %q", tags(a), tags(b))
	}

	// archive: the transition back with the guards and the tag removed.
	x := f.Asset(t, f.OrgA, f.Client1, "wp784-x1")
	warehouse := e.scalar(`WITH s AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp784-site') RETURNING id)
		INSERT INTO location (organization_id, kind, parent_id, name) SELECT $1, 'warehouse', id, 'wp784-wh' FROM s RETURNING id::text`,
		f.OrgA, f.Client1)
	e.exec(`UPDATE asset SET lifecycle_state = 'in_stock', location_id = $2 WHERE id = $1`, x, warehouse)
	archived := e.run(e.full(), map[string]any{"action": "archive", "ids": []string{x}})
	res = e.revert(e.full(), archived.BulkJobID)
	outcome(t, &res, x, "reverted", http.StatusOK)
	if got := e.scalar(`SELECT lifecycle_state FROM asset WHERE id = $1`, x); got != "in_stock" || tags(x) != "" {
		t.Errorf("archived asset after the revert: state %s tags %q", got, tags(x))
	}
}

func TestAssetBulkRevertScopeAgainstPostgres(t *testing.T) {
	e := newAssetRevertEnv(t)
	f := e.f
	a, b := f.Asset(t, f.OrgA, f.Client1, "wp784-s1"), f.Asset(t, f.OrgA, f.Client1, "wp784-s2")
	foreign := f.Asset(t, f.OrgA, f.Client2, "wp784-s3")
	w := e.do(e.full(), http.MethodPost, "/api/v1/assets/bulk", map[string]any{"status": "lost", "ids": []string{a, b, foreign}})
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.BulkJobID == "" {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	item(t, &res, foreign, http.StatusNotFound, "")
	if got := e.scalar(`SELECT count(*)::text FROM bulk_change_set WHERE job_id = $1`, res.BulkJobID); got != "2" {
		t.Fatalf("change set rows: %s, want 2 (none of the foreign asset)", got)
	}
	// b leaves the caller's scope after the bulk: its row stays visible
	// (the scope of the row is the asset's after the bulk), the asset is
	// skipped with 404 and unchanged.
	e.exec(`UPDATE asset SET client_id = $2 WHERE id = $1`, b, f.Client2)
	rev := e.revert(e.full(), res.BulkJobID)
	if rev.Total != 2 {
		t.Errorf("revert report: %+v, want the two assets of the change set", rev)
	}
	outcome(t, &rev, a, "reverted", http.StatusOK)
	outcome(t, &rev, b, "skipped", http.StatusNotFound)
	if got := e.scalar(`SELECT status FROM asset WHERE id = $1`, b); got != "lost" {
		t.Errorf("status of the asset outside the scope: %s, want lost", got)
	}
}

// TestAssetBulkRevertModuleGateAgainstPostgres: /jobs belongs to no module;
// without the module inventory the kind asset.bulk has no revert (409).
func TestAssetBulkRevertModuleGateAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "2d")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp784-gate")
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	// Without the module inventory: the G1 profile (WP-830).
	modules := server.NoModules()
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	mux, _, err := server.NewRouter(repos, server.Options{Version: "wp784", Entitlements: entitlements, Modules: modules,
		CIService: ci.NewServiceWithLimits(repos.CI, entitlements), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		Credentials: credential.NewService(repos.Credential, encryptor)})
	if err != nil {
		t.Fatal(err)
	}
	// A finished asset.bulk job of the user.
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: asset.BulkJobKind, MaxAttempts: 1})
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	job, _, err := jobSvc.Enqueue(ctx, f.OrgA, asset.BulkJobKind, map[string]any{"action": "set_status", "status": "lost"},
		jobs.EnqueueOptions{CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobSvc.ClaimJob(ctx, f.OrgA, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = jobSvc.Succeed(ctx, f.OrgA, job.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/revert", bytes.NewReader(nil))
	principal := identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: identity.AllPermissions(), Credential: "wp784"}
	rctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	middleware.TenantMiddleware(mux).ServeHTTP(w, req.WithContext(rctx))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), asset.BulkJobKind) {
		t.Fatalf("revert of an asset.bulk job without inventory: %d %s, want 409 (kind without revert)", w.Code, w.Body.String())
	}
}
