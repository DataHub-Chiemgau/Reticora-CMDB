package ci_test

// The undo of the bulk operations outside the field path (BLK-02, E-25;
// WP-783) against PostgreSQL, wired as server/router.go does: the CI
// service with the delegating actions (server.CIBulkDelegates), bulk jobs
// also below 100 ids, the reverter of ci.bulk and the kind bulk.revert.
// Per action a bulk, its change set and its revert:
//
//   - add_tags / remove_tags: only the tags that changed are rows (op add,
//     op remove) and go back; a tag deleted from the catalog since and a
//     tag state changed since are skipped.
//   - archive: the status and the tag archived go back in one write.
//   - set_owner: the owner goes back.
//   - add_relationship / remove_relationship: the edge is deleted or
//     created again; an edge changed since and an equal edge created since
//     are skipped (409); a reverter without relationship:write gets 403
//     skipped and the edge stays.
//   - lifecycle_transition: the transition back with the guards of the
//     model; a blocking guard is skipped with lifecycle-guard.
//   - change_template: the template list goes back and the defaults are
//     removed; a default changed since stays and is skipped; a replaced
//     template comes back.
//   - export: the revert of the export job is 409.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run 'TestBulkRevertExt' ./internal/ci/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// revertExtEnv is the CI handler with the delegating actions and the job
// API with the revert of ci.bulk on PostgreSQL.
type revertExtEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	jobSvc *jobs.Service
	user   string
	typeID string
	types  *citype.PGRepository
	worker *export.JobWorker
	// full is a principal of client 1 with every right of the actions,
	// writer one with ci:write alone.
	full, writer context.Context
}

func newRevertExtEnv(t *testing.T) *revertExtEnv {
	t.Helper()
	f := scopetest.Seed(t, "e3")
	bg := context.Background()
	e := &revertExtEnv{t: t, f: f, user: f.AppUser(t, f.OrgA, "bulk-revert-ext")}
	scope := database.OrgWideScope(f.OrgA, e.user)
	scope.Clients = database.ScopeIDs(f.Client1)
	principal := func(perms ...identity.Permission) context.Context {
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user, ClientID: f.Client1})
		ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp783", CorrelationID: "wp783", Source: audit.SourceUI})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: e.user, OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Permissions: append([]identity.Permission{identity.PermJobRead, identity.PermCIRead,
				identity.PermCIWrite}, perms...)})
	}
	e.full = principal(identity.PermRelationshipWrite, identity.PermLifecycleTransition, identity.PermExportRun)
	e.writer = principal()

	e.types = citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp783-dev", Name: "wp783-dev", IsObservable: true}
	if err := e.types.Create(e.full, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	e.typeID = typ.ID

	store := jobs.NewPGRepository(f.App)
	e.jobSvc = jobs.NewService(store)
	e.jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	e.jobSvc.Register(jobs.Kind{Name: jobs.RevertKind, MaxAttempts: 1})
	e.jobSvc.Register(jobs.Kind{Name: export.ExportJobKind, MaxAttempts: 1})
	pg := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	repo := ci.NewIndexingRepository(pg, nopIndexer{})

	rels := relationship.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	relHandler := relationship.NewHandler(rels, relationshiptype.NewPGRepository(f.App))
	lcRepo := lifecycle.NewPGRepository(f.App)
	lcService := lifecycle.NewService(lcRepo, lifecycle.NewPGStateStore(f.App), lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	resolver := lifecycle.NewResolver(f.App)
	exportJobs := export.NewPGJobRepository(f.App)
	blobs := blob.NewFileStore(t.TempDir())
	e.worker = export.NewJobWorker(exportJobs, repo, blobs)
	e.worker.Tracker = e.jobSvc
	delegates := server.CIBulkDelegates(relHandler, rels, lcService, resolver, export.NewJobHandler(exportJobs, e.worker, blobs), nil)
	delegates.Entitlements = &toggledFeatures{}

	svc := ci.NewService(repo).WithFieldResolver(e.types).WithBulkJobs(e.jobSvc).WithCreatorPolicy(phase1CreatorPolicy).
		WithBulkDelegates(delegates)
	revert := ci.NewBulkRevert(svc, e.jobSvc, store)
	e.mux = chi.NewRouter()
	e.mux.Use(fieldPolicySource())
	ci.NewHandler(svc).RegisterRoutes(e.mux)
	jobs.NewAPIHandler(e.jobSvc).
		WithKind(ci.BulkJobKind, identity.PermCIWrite, revert.Revert).
		WithBulkKind(ci.BulkJobKind).
		WithKind(jobs.RevertKind, identity.PermCIWrite, nil).
		WithKind(export.ExportJobKind, identity.PermExportRun, nil).
		RegisterRoutes(e.mux)
	lifecycle.NewHandler(lcRepo, lcService, resolver).RegisterRoutes(e.mux)
	return e
}

func (e *revertExtEnv) do(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	case nil:
	default:
		raw, _ = json.Marshal(b)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// bulk posts a bulk of the full principal below 100 ids and returns the
// report; every id must succeed.
func (e *revertExtEnv) bulk(action string, ids []string, params map[string]any) ci.BulkResult {
	e.t.Helper()
	w := e.do(e.full, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": action, "ids": ids, "params": params})
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.BulkJobID == "" || res.Succeeded != len(ids) {
		e.t.Fatalf("bulk %s: %d %s, want 200 with every id ok and the bulk job", action, w.Code, w.Body.String())
	}
	return res
}

// revert posts the revert of the bulk job as ctx, waits for the revert job
// and returns its report.
func (e *revertExtEnv) revert(ctx context.Context, bulkJobID string) ci.BulkRevertResult {
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

// outcome returns the report of id in the revert report.
func outcome(t *testing.T, res *ci.BulkRevertResult, id string) ci.BulkRevertItem {
	t.Helper()
	for _, it := range res.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no outcome of %s in %+v", id, res.Items)
	return ci.BulkRevertItem{}
}

// wantOutcome checks the outcome of id: result, code and (non-empty) the
// problem type.
func wantOutcome(t *testing.T, res *ci.BulkRevertResult, id, result string, code int, typ string) ci.BulkRevertItem {
	t.Helper()
	it := outcome(t, res, id)
	if it.Result != result || it.Code != code || (typ != "" && it.Type != typ) {
		t.Fatalf("outcome of %s: %+v, want %s %d %s", id, it, result, code, typ)
	}
	return it
}

func (e *revertExtEnv) count(query string, args ...any) int {
	e.t.Helper()
	return countRows(e.t, e.f, query, args...)
}

func (e *revertExtEnv) text(query string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return s
}

func (e *revertExtEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

// ci inserts a CI of the test type in client 1 and returns its id.
func (e *revertExtEnv) ci(name string) string {
	e.t.Helper()
	return e.text(`INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id::text`, e.f.OrgA, e.f.Client1, e.typeID, name)
}

// tags returns the tag keys of a CI, sorted and comma-separated.
func (e *revertExtEnv) tags(id string) string {
	e.t.Helper()
	return e.text(`SELECT COALESCE(string_agg(t.key, ',' ORDER BY t.key), '') FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id
		WHERE ct.ci_id = $1`, id)
}

// rows counts the change set rows of a job with op and field.
func (e *revertExtEnv) rows(jobID, op, field string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND op = $2 AND field_name = $3`, jobID, op, field)
}

// outcomes returns the revert outcomes of the change set rows of a job,
// sorted (reverted, skipped; "" for a row not handled).
func (e *revertExtEnv) outcomes(jobID string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT COALESCE(revert_outcome, '') FROM bulk_change_set WHERE job_id = $1
		ORDER BY 1`, jobID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// tagKey reads the tag key a row holds in column (old_value, new_value).
func (e *revertExtEnv) tagKeys(jobID, op, column string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT `+column+` #>> '{}' FROM bulk_change_set
		WHERE job_id = $1 AND op = $2 AND field_name = 'tags' ORDER BY 1`, jobID, op)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestBulkRevertExtTagsArchiveOwner covers add_tags, remove_tags, archive
// and set_owner.
func TestBulkRevertExtTagsArchiveOwner(t *testing.T) {
	e := newRevertExtEnv(t)
	for _, key := range []string{"dmz", "prod", "gone"} {
		e.exec(`INSERT INTO tag (organization_id, key, display_name) VALUES ($1, $2, jsonb_build_object('en', $2::text))`, e.f.OrgA, key)
	}
	assignTag := func(id, key string) {
		e.exec(`INSERT INTO ci_tag (organization_id, ci_id, tag_id) SELECT $1, $2, id FROM tag WHERE organization_id = $1 AND key = $3`,
			e.f.OrgA, id, key)
	}

	t.Run("add_tags", func(t *testing.T) {
		a, b := e.ci("w783-a"), e.ci("w783-b")
		assignTag(b, "dmz")
		res := e.bulk(ci.BulkAddTags, []string{a, b}, map[string]any{"tags": []string{"dmz", "prod"}})
		// Only the tags that changed: dmz and prod of a, prod of b.
		if got := e.tagKeys(res.BulkJobID, "add", "new_value"); !slices.Equal(got, []string{"dmz", "prod", "prod"}) {
			t.Fatalf("added tags in the change set: %v", got)
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1`, res.BulkJobID); n != 3 {
			t.Fatalf("%d change set rows, want 3", n)
		}
		rev := e.revert(e.full, res.BulkJobID)
		wantOutcome(t, &rev, a, "reverted", http.StatusOK, "")
		wantOutcome(t, &rev, b, "reverted", http.StatusOK, "")
		if e.tags(a) != "" || e.tags(b) != "dmz" {
			t.Errorf("tags after the revert: a %q b %q, want none and dmz", e.tags(a), e.tags(b))
		}
		if got := e.outcomes(res.BulkJobID); !slices.Equal(got, []string{"reverted", "reverted", "reverted"}) {
			t.Errorf("row outcomes %v", got)
		}
		// The inverse write is a tag change like PUT /cis/{id}/tags.
		if n := e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'tags'`, a); n != 2 {
			t.Errorf("%d ci_change rows of the tags of a, want 2 (bulk and revert)", n)
		}
	})

	t.Run("remove_tags", func(t *testing.T) {
		c, d := e.ci("w783-c"), e.ci("w783-d")
		for _, key := range []string{"dmz", "prod", "gone"} {
			assignTag(c, key)
		}
		assignTag(d, "prod")
		res := e.bulk(ci.BulkRemoveTags, []string{c, d}, map[string]any{"tags": []string{"prod", "gone"}})
		if got := e.tagKeys(res.BulkJobID, "remove", "old_value"); !slices.Equal(got, []string{"gone", "prod", "prod"}) {
			t.Fatalf("removed tags in the change set: %v", got)
		}
		// Since the bulk: gone left the catalog, d got prod back.
		e.exec(`DELETE FROM tag WHERE organization_id = $1 AND key = 'gone'`, e.f.OrgA)
		assignTag(d, "prod")
		rev := e.revert(e.full, res.BulkJobID)
		got := wantOutcome(t, &rev, c, "reverted", http.StatusOK, "")
		if !slices.Equal(got.Fields, []string{"tags"}) {
			t.Errorf("skipped fields of c: %v, want the tag gone", got.Fields)
		}
		wantOutcome(t, &rev, d, "skipped", http.StatusConflict, "")
		if e.tags(c) != "dmz,prod" || e.tags(d) != "prod" {
			t.Errorf("tags after the revert: c %q d %q, want dmz,prod and prod", e.tags(c), e.tags(d))
		}
		if got := e.outcomes(res.BulkJobID); !slices.Equal(got, []string{"reverted", "skipped", "skipped"}) {
			t.Errorf("row outcomes %v", got)
		}
	})

	t.Run("archive", func(t *testing.T) {
		f := e.ci("w783-f")
		res := e.bulk(ci.BulkArchive, []string{f}, nil)
		if e.rows(res.BulkJobID, "set", "status") != 1 || e.rows(res.BulkJobID, "add", "tags") != 1 {
			t.Fatalf("archive change set: status %d, tag %d rows", e.rows(res.BulkJobID, "set", "status"), e.rows(res.BulkJobID, "add", "tags"))
		}
		version := e.count(`SELECT version FROM ci WHERE id = $1`, f)
		rev := e.revert(e.full, res.BulkJobID)
		wantOutcome(t, &rev, f, "reverted", http.StatusOK, "")
		if s := e.text(`SELECT status FROM ci WHERE id = $1`, f); s != "active" || e.tags(f) != "" {
			t.Errorf("after the revert: status %s, tags %q; want active without archived", s, e.tags(f))
		}
		// One inverse write: status and tag in one transaction and version.
		if v := e.count(`SELECT version FROM ci WHERE id = $1`, f); v != version+1 {
			t.Errorf("version %d after the revert of the archive, want %d (one write)", v, version+1)
		}
	})

	t.Run("set_owner", func(t *testing.T) {
		g := e.ci("w783-g")
		res := e.bulk(ci.BulkSetOwner, []string{g}, map[string]any{"owner_user_id": e.user})
		if e.rows(res.BulkJobID, "set", "owner_user_id") != 1 {
			t.Fatalf("set_owner change set: %d rows", e.rows(res.BulkJobID, "set", "owner_user_id"))
		}
		rev := e.revert(e.full, res.BulkJobID)
		wantOutcome(t, &rev, g, "reverted", http.StatusOK, "")
		if owner := e.text(`SELECT COALESCE(owner_user_id::text, '') FROM ci WHERE id = $1`, g); owner != "" {
			t.Errorf("owner after the revert: %q, want none", owner)
		}
	})
}

// TestBulkRevertExtRelationships covers add_relationship and
// remove_relationship.
func TestBulkRevertExtRelationships(t *testing.T) {
	e := newRevertExtEnv(t)
	target := e.ci("w783-target")
	edges := func(source string) int {
		return e.count(`SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = 'depends_on'`,
			source, target)
	}
	edge := map[string]any{"rel_type": "depends_on", "target_ci_id": target}

	// add_relationship: the edge goes; an edge changed since stays.
	r1, r2 := e.ci("w783-r1"), e.ci("w783-r2")
	res := e.bulk(ci.BulkAddRelationship, []string{r1, r2}, edge)
	if n := e.rows(res.BulkJobID, "add", ci.ChangeFieldRelationship); n != 2 {
		t.Fatalf("%d edge rows, want 2", n)
	}
	if id := e.text(`SELECT new_value->>'id' FROM bulk_change_set WHERE job_id = $1 AND entity_id = $2`, res.BulkJobID, r1); !strings.EqualFold(id,
		e.text(`SELECT id::text FROM ci_relationship WHERE source_ci_id = $1`, r1)) {
		t.Fatalf("the change set row of r1 names edge %s", id)
	}
	e.exec(`UPDATE ci_relationship SET notes = 'changed since' WHERE source_ci_id = $1`, r2)
	rev := e.revert(e.full, res.BulkJobID)
	wantOutcome(t, &rev, r1, "reverted", http.StatusOK, "")
	wantOutcome(t, &rev, r2, "skipped", http.StatusConflict, "")
	if edges(r1) != 0 || edges(r2) != 1 {
		t.Errorf("edges after the revert: r1 %d (want 0), r2 %d (want 1)", edges(r1), edges(r2))
	}
	if got := e.outcomes(res.BulkJobID); !slices.Equal(got, []string{"reverted", "skipped"}) {
		t.Errorf("row outcomes %v", got)
	}

	// remove_relationship: the edge comes back; an equal edge created
	// since is skipped.
	r3, r4 := e.ci("w783-r3"), e.ci("w783-r4")
	e.bulk(ci.BulkAddRelationship, []string{r3, r4}, edge)
	e.exec(`UPDATE ci_relationship SET attributes = attributes || '{"port": 443}' WHERE source_ci_id = $1`, r3)
	res = e.bulk(ci.BulkRemoveRelationship, []string{r3, r4}, edge)
	if n := e.rows(res.BulkJobID, "remove", ci.ChangeFieldRelationship); n != 2 || edges(r3)+edges(r4) != 0 {
		t.Fatalf("%d removed edge rows, edges %d", n, edges(r3)+edges(r4))
	}
	e.exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source) VALUES ($1, $2, $3, 'depends_on', 'manual')`,
		e.f.OrgA, r4, target)
	rev = e.revert(e.full, res.BulkJobID)
	wantOutcome(t, &rev, r3, "reverted", http.StatusOK, "")
	wantOutcome(t, &rev, r4, "skipped", http.StatusConflict, "")
	if edges(r3) != 1 || edges(r4) != 1 {
		t.Errorf("edges after the revert: r3 %d, r4 %d, want 1 and 1", edges(r3), edges(r4))
	}
	if port := e.text(`SELECT COALESCE(attributes->>'port', '') FROM ci_relationship WHERE source_ci_id = $1`, r3); port != "443" {
		t.Errorf("the edge came back with attribute port %q, want 443", port)
	}

	// A reverter without relationship:write: 403 skipped, the edge stays.
	r5 := e.ci("w783-r5")
	res = e.bulk(ci.BulkAddRelationship, []string{r5}, edge)
	rev = e.revert(e.writer, res.BulkJobID)
	wantOutcome(t, &rev, r5, "skipped", http.StatusForbidden, "")
	if edges(r5) != 1 {
		t.Errorf("edge of r5 after the refused revert: %d, want 1", edges(r5))
	}
	if got := e.outcomes(res.BulkJobID); !slices.Equal(got, []string{"skipped"}) {
		t.Errorf("row outcomes %v", got)
	}

	// export has no revert: 409 for the export job.
	w := e.do(e.full, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": ci.BulkExport, "ids": []string{r1},
		"params": map[string]any{"format": "json"}})
	var acc struct {
		JobID string `json:"job_id"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &acc) != nil {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	for deadline := time.Now().Add(30 * time.Second); e.text(`SELECT status FROM export_job WHERE id = $1`, acc.JobID) != export.JobStatusCompleted; {
		if time.Now().After(deadline) {
			t.Fatal("export job not completed")
		}
		e.worker.ProcessOne(context.Background())
	}
	jobID := e.text(`SELECT id::text FROM job WHERE ref_table = 'export_job' AND ref_id = $1`, acc.JobID)
	if w = e.do(e.full, http.MethodPost, "/api/v1/jobs/"+jobID+"/revert", nil); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), jobs.ErrNotRevertible.Error()) {
		t.Errorf("revert of the export job: %d %s, want 409 not revertible", w.Code, w.Body.String())
	}
}

// TestBulkRevertExtLifecycle covers lifecycle_transition.
func TestBulkRevertExtLifecycle(t *testing.T) {
	e := newRevertExtEnv(t)
	w := e.do(e.full, http.MethodPost, "/api/v1/lifecycle-models", `{"key":"wp783_service","name":"Service","applies_to":"ci","states":[
		{"key":"draft","label":"Draft","is_initial":true,"sort_order":10},
		{"key":"production","label":"Production","sort_order":20},
		{"key":"retired","label":"Retired","sort_order":30}],
		"transitions":[{"from_state_key":"draft","to_state_key":"production","key":"go_live"},
		{"from_state_key":"production","to_state_key":"draft","key":"roll_back"},
		{"from_state_key":"draft","to_state_key":"retired","key":"retire"},
		{"from_state_key":"retired","to_state_key":"draft","key":"revive",
			"guard":{"fields":{"approval":{"op":"required"}}}}]}`)
	var model lifecycle.Definition
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &model) != nil {
		t.Fatalf("create model: %d %s", w.Code, w.Body.String())
	}
	serviceType := e.text(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
		VALUES ($1, 'wp783-service', 'WP-783 service', $2) RETURNING id::text`, e.f.OrgA, model.ID)
	l1, l2 := e.ci("w783-l1"), e.ci("w783-l2")
	e.exec(`UPDATE ci SET ci_type_id = $1, lifecycle_state = 'draft' WHERE id = ANY($2::uuid[])`, serviceType, []string{l1, l2})
	state := func(id string) string {
		return e.text(`SELECT COALESCE(lifecycle_state, '') FROM ci WHERE id = $1`, id)
	}

	// The transition back exists: reverted, with its history and event.
	res := e.bulk(ci.BulkLifecycleTransition, []string{l1}, map[string]any{"to_state": "production"})
	if old := e.text(`SELECT old_value #>> '{}' FROM bulk_change_set WHERE job_id = $1 AND field_name = 'lifecycle_state'`, res.BulkJobID); old != "draft" {
		t.Fatalf("change set old_value %q, want draft", old)
	}
	rev := e.revert(e.full, res.BulkJobID)
	wantOutcome(t, &rev, l1, "reverted", http.StatusOK, "")
	if s := state(l1); s != "draft" {
		t.Errorf("l1 after the revert: %q, want draft", s)
	}
	if n := e.count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'lifecycle.changed'`, l1); n != 2 {
		t.Errorf("%d lifecycle.changed of l1, want 2 (bulk and revert)", n)
	}
	if got := e.outcomes(res.BulkJobID); !slices.Equal(got, []string{"reverted"}) {
		t.Errorf("row outcomes %v", got)
	}

	// The guard of the transition back blocks: skipped with
	// lifecycle-guard, the state stays.
	res = e.bulk(ci.BulkLifecycleTransition, []string{l2}, map[string]any{"to_state": "retired"})
	rev = e.revert(e.full, res.BulkJobID)
	got := wantOutcome(t, &rev, l2, "skipped", http.StatusUnprocessableEntity, lifecycle.ProblemLifecycleGuard)
	if !slices.Equal(got.Fields, []string{ci.ChangeFieldLifecycle}) {
		t.Errorf("fields of the skipped transition: %v", got.Fields)
	}
	if s := state(l2); s != "retired" {
		t.Errorf("l2 after the blocked revert: %q, want retired", s)
	}
	if got := e.outcomes(res.BulkJobID); !slices.Equal(got, []string{"skipped"}) {
		t.Errorf("row outcomes %v", got)
	}
}

// TestBulkRevertExtChangeTemplate covers change_template.
func TestBulkRevertExtChangeTemplate(t *testing.T) {
	e := newRevertExtEnv(t)
	ctx := e.full
	define := func(key string) citype.AttributeDefinition {
		t.Helper()
		d := citype.AttributeDefinition{CITypeID: e.typeID, Key: key, DataType: "text"}
		if err := e.types.CreateAttributeDefinition(ctx, e.f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		return d
	}
	note, size := define("w783_note"), define("w783_size")
	item := func(def citype.AttributeDefinition, defaultValue string, position int) citype.TemplateVersionItem {
		it := citype.TemplateVersionItem{AttributeDefinitionID: def.ID, Position: position}
		if defaultValue != "" {
			it.DefaultOverride = json.RawMessage(defaultValue)
		}
		return it
	}
	template := func(key string, versions ...[]citype.TemplateVersionItem) citype.Template {
		t.Helper()
		tpl := citype.Template{Key: key, CITypeID: e.typeID, IsActive: true}
		if err := e.types.CreateTemplate(ctx, e.f.OrgA, &tpl); err != nil {
			t.Fatalf("template %s: %v", key, err)
		}
		for i, items := range versions {
			if i > 0 {
				if _, err := e.types.CreateTemplateVersion(ctx, e.f.OrgA, tpl.ID); err != nil {
					t.Fatalf("template %s v%d: %v", key, i+1, err)
				}
			}
			if _, err := e.types.SetTemplateVersionItems(ctx, e.f.OrgA, tpl.ID, i+1, items); err != nil {
				t.Fatalf("template %s v%d items: %v", key, i+1, err)
			}
			if _, err := e.types.PublishTemplateVersion(ctx, e.f.OrgA, tpl.ID, i+1); err != nil {
				t.Fatalf("publish %s v%d: %v", key, i+1, err)
			}
		}
		return tpl
	}
	assign := func(ciID string, tpl *citype.Template, version, position int) {
		e.exec(`INSERT INTO ci_template (organization_id, ci_id, template_id, version, position) VALUES ($1, $2, $3, $4, $5)`,
			e.f.OrgA, ciID, tpl.ID, version, position)
	}
	templateAt := func(ciID string, position int) string {
		return e.text(`SELECT template_id::text || ':' || version FROM ci_template WHERE ci_id = $1 AND position = $2`, ciID, position)
	}
	attribute := func(ciID, key string) string {
		return e.text(`SELECT COALESCE(attributes->>$2, '') FROM ci WHERE id = $1`, ciID, key)
	}

	primary := template("w783-primary", []citype.TemplateVersionItem{item(note, "", 0)},
		[]citype.TemplateVersionItem{item(note, `"from-v2"`, 0), item(size, `"5"`, 1)})
	other := template("w783-other", []citype.TemplateVersionItem{item(note, "", 0)})
	replacement := template("w783-replacement", []citype.TemplateVersionItem{item(size, "", 0)})

	// The migration: list and defaults go back; a default changed since
	// stays and is skipped.
	m1, m2 := e.ci("w783-m1"), e.ci("w783-m2")
	assign(m1, &primary, 1, 0)
	assign(m2, &primary, 1, 0)
	res := e.bulk(ci.BulkChangeTemplate, []string{m1, m2}, map[string]any{"template_id": primary.ID})
	if e.rows(res.BulkJobID, "set", ci.ChangeFieldTemplates) != 2 ||
		e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND field_name IN ('w783_note', 'w783_size') AND old_value IS NULL`,
			res.BulkJobID) != 4 {
		t.Fatalf("change set of the migration: %d template rows, %d default rows", e.rows(res.BulkJobID, "set", ci.ChangeFieldTemplates),
			e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND old_value IS NULL`, res.BulkJobID))
	}
	if attribute(m1, "w783_note") != "from-v2" || attribute(m1, "w783_size") != "5" {
		t.Fatalf("defaults of the migration: %q %q", attribute(m1, "w783_note"), attribute(m1, "w783_size"))
	}
	if w := e.do(ctx, http.MethodPatch, "/api/v1/cis/"+m2, map[string]any{"attributes": map[string]any{"w783_size": "manual"}}); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	rev := e.revert(e.full, res.BulkJobID)
	wantOutcome(t, &rev, m1, "reverted", http.StatusOK, "")
	got := wantOutcome(t, &rev, m2, "reverted", http.StatusOK, "")
	if !slices.Equal(got.Fields, []string{"w783_size"}) {
		t.Errorf("skipped fields of m2: %v, want w783_size", got.Fields)
	}
	for _, id := range []string{m1, m2} {
		if at := templateAt(id, 0); at != primary.ID+":1" {
			t.Errorf("%s: template %s after the revert, want version 1", id, at)
		}
		if note := attribute(id, "w783_note"); note != "" {
			t.Errorf("%s: default note %q after the revert, want none", id, note)
		}
	}
	if attribute(m1, "w783_size") != "" || attribute(m2, "w783_size") != "manual" {
		t.Errorf("size after the revert: m1 %q (want none), m2 %q (want manual)", attribute(m1, "w783_size"), attribute(m2, "w783_size"))
	}
	if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND revert_outcome = 'skipped'`, res.BulkJobID); n != 1 {
		t.Errorf("%d skipped rows, want 1 (the size of m2)", n)
	}

	// replace_template_id: the replaced template comes back.
	m3 := e.ci("w783-m3")
	assign(m3, &other, 1, 0)
	res = e.bulk(ci.BulkChangeTemplate, []string{m3}, map[string]any{"template_id": replacement.ID, "replace_template_id": other.ID})
	if templateAt(m3, 0) != replacement.ID+":1" || e.rows(res.BulkJobID, "set", ci.ChangeFieldTemplates) != 1 {
		t.Fatalf("replace: template %s, %d rows", templateAt(m3, 0), e.rows(res.BulkJobID, "set", ci.ChangeFieldTemplates))
	}
	rev = e.revert(e.full, res.BulkJobID)
	wantOutcome(t, &rev, m3, "reverted", http.StatusOK, "")
	if at := templateAt(m3, 0); at != other.ID+":1" {
		t.Errorf("m3 after the revert: %s, want the other template", at)
	}
}
