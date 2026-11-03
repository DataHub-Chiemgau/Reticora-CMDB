package citype

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAttributeIndexStatus covers WP-796 (DB-04, UI-11) against
// PostgreSQL over HTTP: GET /attribute-definitions and
// GET /attribute-definitions/{id} carry the index_status of every
// definition, the state of its newest index job (attribute.index,
// attribute.index.rebuild, attribute.index.key) read organization-wide,
// and POST /attribute-definitions/{id}/reindex queues a rebuild:
//   - queued → running → succeeded over DDLJob.RunDue, the index valid;
//   - duplicates written directly make the build fail: queued with error
//     and attempts 1, failed with the error after max_attempts;
//   - the rebuild after the clean-up succeeds; a rebuild while a job is
//     open answers that job (200), two concurrent rebuilds leave one open
//     job;
//   - a system definition reports succeeded without job, its rebuild is
//     403; a definition without flags reports none, its rebuild is 409; an
//     unknown one is 404, a server without job service 503;
//   - organization B sees no status from jobs of organization A;
//   - the export holds no index_status;
//   - a searchable, not unique global definition of the organization
//     reports the index of its key and the status of its job;
//   - a client-scoped reader with ci:read sees the status an org-wide one
//     sees.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeIndexStatus(t *testing.T) {
	f := scopetest.Seed(t, "89")
	bg := context.Background()
	orgCtx := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	ctxA := orgCtx(f.OrgA)
	clientCtx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	ddl := NewDDLJob(f.App, svc)
	repo := NewPGRepository(f.App).WithIndexScheduler(ddl)
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	// Without job service.
	bare := chi.NewRouter()
	NewHandler(NewPGRepository(f.App)).RegisterRoutes(bare)

	call := func(h http.Handler, ctx context.Context, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader("{}")).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	// status reads the index status of a definition through GET
	// /attribute-definitions/{id}.
	status := func(ctx context.Context, id string) AttributeIndexStatus {
		t.Helper()
		w := call(mux, ctx, http.MethodGet, "/api/v1/attribute-definitions/"+id)
		if w.Code != http.StatusOK {
			t.Fatalf("GET definition %s: %d %s", id, w.Code, w.Body.String())
		}
		var d AttributeDefinition
		decode(w, &d)
		if d.IndexStatus == nil {
			t.Fatalf("GET definition %s without index_status: %s", id, w.Body.String())
		}
		return *d.IndexStatus
	}
	reindex := func(id string) (int, AttributeIndexStatus, string) {
		t.Helper()
		w := call(mux, ctxA, http.MethodPost, "/api/v1/attribute-definitions/"+id+"/reindex")
		var s AttributeIndexStatus
		if w.Code == http.StatusOK || w.Code == http.StatusAccepted {
			decode(w, &s)
		}
		return w.Code, s, w.Header().Get("Location")
	}
	runDue := func() {
		t.Helper()
		if _, err := ddl.RunDue(bg); err != nil {
			t.Fatalf("run index jobs: %v", err)
		}
	}
	openJobs := func(id string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = ANY($2)
			AND params->>'definition_id' = $3 AND status IN ('queued', 'running')`, f.OrgA, IndexJobKinds, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	typ := &Type{OrganizationID: f.OrgA, Key: "wp796-switch", Name: "wp796-switch", IsObservable: true}
	if err := repo.Create(ctxA, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	def := &AttributeDefinition{CITypeID: typ.ID, Key: "wp796_tag", DataType: "text", Searchable: true}
	if err := repo.CreateAttributeDefinition(ctxA, f.OrgA, def); err != nil {
		t.Fatalf("create definition: %v", err)
	}
	plain, uniq := IndexName(def.ID, false), IndexName(def.ID, true)
	t.Cleanup(func() {
		for _, name := range []string{plain, uniq} {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+name)
		}
	})

	// queued → running → succeeded over RunDue. A transaction that wrote to
	// ci keeps the concurrent build waiting, so the job is seen running.
	s := status(ctxA, def.ID)
	if s.State != IndexStateQueued || s.IndexName != plain || s.Valid || s.JobID == "" || s.Attempts != 0 ||
		s.MaxAttempts != 3 || s.UpdatedAt == nil {
		t.Fatalf("status after create = %+v, want queued with the job, %s not valid yet", s, plain)
	}
	firstJob := s.JobID
	ciA := f.CI(t, f.OrgA, f.Client1, "wp796-a")
	ciB := f.CI(t, f.OrgA, f.Client1, "wp796-b")
	blocker, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(bg, `UPDATE ci SET name = name WHERE id = $1`, ciA); err != nil {
		t.Fatal(err)
	}
	ran := make(chan error, 1)
	go func() {
		_, rerr := ddl.RunDue(bg)
		ran <- rerr
	}()
	deadline := time.Now().Add(30 * time.Second)
	for s = status(ctxA, def.ID); s.State != IndexStateRunning && time.Now().Before(deadline); s = status(ctxA, def.ID) {
		time.Sleep(50 * time.Millisecond)
	}
	_ = blocker.Rollback(bg)
	if rerr := <-ran; rerr != nil {
		t.Fatalf("run index jobs: %v", rerr)
	}
	if s.State != IndexStateRunning || s.JobID != firstJob || s.Attempts != 1 {
		t.Errorf("status while the build waits = %+v, want running, attempt 1 of %s", s, firstJob)
	}
	if s = status(ctxA, def.ID); s.State != IndexStateSucceeded || !s.Valid || s.JobID != firstJob || s.Error != "" {
		t.Fatalf("status after the run = %+v, want succeeded with a valid index", s)
	}

	// The client-scoped reader sees the status of the org-wide job.
	if got := status(clientCtx, def.ID); !reflect.DeepEqual(got, s) {
		t.Errorf("client-scoped reader: %+v, want %+v", got, s)
	}

	// Duplicates written directly (past the server-side check) make the
	// build of the unique index fail.
	yes := true
	if _, err = repo.UpdateAttributeDefinition(ctxA, f.OrgA, def.ID, &AttributeDefinitionPatch{Unique: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $1, attributes = '{"wp796_tag": "T-1"}' WHERE id = ANY($2)`,
		typ.ID, []string{ciA, ciB}); err != nil {
		t.Fatal(err)
	}
	runDue()
	s = status(ctxA, def.ID)
	if s.State != IndexStateQueued || s.Attempts != 1 || s.Error != ErrDuplicateValues.Error() || s.IndexName != uniq || s.Valid {
		t.Fatalf("status after a failed attempt = %+v, want queued with error %q and attempts 1", s, ErrDuplicateValues)
	}
	if strings.Contains(s.Error, "T-1") {
		t.Errorf("the status shows a value of a CI: %q", s.Error)
	}
	for range 2 {
		if _, err = f.Admin.Exec(bg, `UPDATE job SET run_after = now() WHERE id = $1`, s.JobID); err != nil {
			t.Fatal(err)
		}
		runDue()
	}
	failedJob := s.JobID
	if s = status(ctxA, def.ID); s.State != IndexStateFailed || s.Attempts != 3 || s.MaxAttempts != 3 ||
		s.Error != ErrDuplicateValues.Error() || s.JobID != failedJob {
		t.Fatalf("status after max_attempts = %+v, want failed with %q", s, ErrDuplicateValues)
	}

	// The rebuild after the clean-up succeeds.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, ciB); err != nil {
		t.Fatal(err)
	}
	code, s, location := reindex(def.ID)
	if code != http.StatusAccepted || s.State != IndexStateQueued || s.JobID == "" || s.JobID == failedJob ||
		location != "/api/v1/jobs/"+s.JobID || s.Attempts != 0 || s.Error != "" {
		t.Fatalf("reindex after the clean-up = %d %+v (Location %q), want 202 with a new queued job", code, s, location)
	}
	rebuild := s.JobID
	var createdBy, kind string
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(created_by::text, ''), kind FROM job WHERE id = $1`, rebuild).Scan(&createdBy, &kind); err != nil {
		t.Fatal(err)
	}
	if createdBy != f.User || kind != IndexJobKind {
		t.Errorf("rebuild job created by %q kind %q, want %q %s", createdBy, kind, f.User, IndexJobKind)
	}
	// While it is open a second request answers it.
	if code, s, _ = reindex(def.ID); code != http.StatusOK || s.JobID != rebuild || s.State != IndexStateQueued {
		t.Errorf("reindex with an open job = %d %+v, want 200 with job %s", code, s, rebuild)
	}
	runDue()
	if s = status(ctxA, def.ID); s.State != IndexStateSucceeded || !s.Valid || s.JobID != rebuild || s.IndexName != uniq {
		t.Fatalf("status after the rebuild = %+v, want succeeded with valid %s", s, uniq)
	}

	// Two concurrent requests leave one open job.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = call(mux, ctxA, http.MethodPost, "/api/v1/attribute-definitions/"+def.ID+"/reindex").Code
		}()
	}
	wg.Wait()
	if n := openJobs(def.ID); n != 1 || codes[0]+codes[1] != http.StatusOK+http.StatusAccepted {
		t.Errorf("two concurrent rebuilds: %d open jobs, answers %v; want one job, 202 and 200", n, codes)
	}
	runDue()

	// The list carries the status of every definition.
	none := &AttributeDefinition{CITypeID: typ.ID, Key: "wp796_note", DataType: "text"}
	if err = repo.CreateAttributeDefinition(ctxA, f.OrgA, none); err != nil {
		t.Fatal(err)
	}
	listed := func(ctx context.Context, query string) map[string]AttributeIndexStatus {
		t.Helper()
		w := call(mux, ctx, http.MethodGet, "/api/v1/attribute-definitions"+query)
		if w.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, w.Code, w.Body.String())
		}
		var page struct {
			Data []AttributeDefinition `json:"data"`
		}
		decode(w, &page)
		out := map[string]AttributeIndexStatus{}
		for _, d := range page.Data {
			if d.IndexStatus == nil {
				t.Fatalf("list %s: %s without index_status", query, d.Key)
			}
			out[d.ID] = *d.IndexStatus
		}
		return out
	}
	for _, q := range []string{"?ci_type_id=" + typ.ID, "?scope=type", "?applicable_to=" + typ.ID} {
		got := listed(ctxA, q)
		if got[def.ID].State != IndexStateSucceeded || !got[def.ID].Valid {
			t.Errorf("list %s: status of %s = %+v, want succeeded", q, def.Key, got[def.ID])
		}
		if got[none.ID].State != IndexStateNone || got[none.ID].JobID != "" {
			t.Errorf("list %s: status of %s = %+v, want none", q, none.Key, got[none.ID])
		}
	}

	// A definition without flags: none, its rebuild 409.
	if s = status(ctxA, none.ID); s.State != IndexStateNone || s.Valid || s.IndexName != IndexName(none.ID, false) {
		t.Errorf("status without flags = %+v, want none", s)
	}
	if code, _, _ = reindex(none.ID); code != http.StatusConflict {
		t.Errorf("reindex without flags = %d, want 409", code)
	}
	// Unknown 404, without job service 503.
	if code, _, _ = reindex("0796aaaa-0000-4000-8000-000000000001"); code != http.StatusNotFound {
		t.Errorf("reindex of an unknown definition = %d, want 404", code)
	}
	if w := call(bare, ctxA, http.MethodPost, "/api/v1/attribute-definitions/"+def.ID+"/reindex"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("reindex without job service = %d, want 503", w.Code)
	}

	// An indexed definition of the organization without job (its job could
	// not be queued): succeeded with a valid index, else failed.
	orphan := &AttributeDefinition{CITypeID: typ.ID, Key: "wp796_orphan", DataType: "text", Searchable: true}
	if err = NewPGRepository(f.App).CreateAttributeDefinition(ctxA, f.OrgA, orphan); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+IndexName(orphan.ID, false)) })
	if s = status(ctxA, orphan.ID); s.State != IndexStateFailed || s.Error != IndexMissing || s.JobID != "" || s.Valid {
		t.Errorf("status without job and index = %+v, want failed %q", s, IndexMissing)
	}
	if err = ddl.Ensure(bg, f.OrgA, orphan.ID); err != nil {
		t.Fatal(err)
	}
	if s = status(ctxA, orphan.ID); s.State != IndexStateSucceeded || !s.Valid || s.JobID != "" {
		t.Errorf("status without job with a valid index = %+v, want succeeded", s)
	}

	// A system definition: succeeded without job, its rebuild 403.
	run := time.Now().UnixNano()
	var system string
	if err = f.Admin.QueryRow(bg, `INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, searchable)
		VALUES (NULL, NULL, 'global', $1, 'text', true) RETURNING id::text`, fmt.Sprintf("wp796_system_%d", run)).Scan(&system); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE id = $1`, system) })
	s = status(ctxA, system)
	code, _, _ = reindex(system)
	_, _ = f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE id = $1`, system)
	if s.State != IndexStateSucceeded || s.JobID != "" || s.IndexName != IndexName(system, false) {
		t.Errorf("status of a system definition = %+v, want succeeded without job", s)
	}
	if code != http.StatusForbidden {
		t.Errorf("reindex of a system definition = %d, want 403", code)
	}

	// A searchable, not unique global definition of the organization uses
	// the index of its key (WP-607) and reports it with its job.
	key := fmt.Sprintf("wp796_rack_%d", run)
	global := &AttributeDefinition{Key: key, DataType: "text", Searchable: true}
	if err = repo.CreateAttributeDefinition(ctxA, f.OrgA, global); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+KeyIndexName(key)) })
	if s = status(ctxA, global.ID); s.State != IndexStateQueued || s.IndexName != KeyIndexName(key) || s.JobID == "" {
		t.Errorf("status of the global definition = %+v, want queued on %s", s, KeyIndexName(key))
	}
	globalJob := s.JobID
	runDue()
	if s = status(ctxA, global.ID); s.State != IndexStateSucceeded || !s.Valid || s.JobID != globalJob {
		t.Errorf("status of the global definition after the run = %+v, want succeeded on a valid %s", s, KeyIndexName(key))
	}

	// The export holds no index status.
	export, err := BuildExport(ctxA, repo, f.OrgA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), key) || strings.Contains(string(raw), "index_status") {
		t.Errorf("export: holds %s %v, index_status %v; want the definition without index_status",
			key, strings.Contains(string(raw), key), strings.Contains(string(raw), "index_status"))
	}

	// Organization B sees no status from the jobs of A: a definition of B
	// with A's id would find none (row level security on job).
	statuses, err := repo.AttributeIndexStatuses(orgCtx(f.OrgB), f.OrgB, []AttributeDefinition{*def})
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses[def.ID]; got.JobID != "" || got.Attempts != 0 || got.UpdatedAt != nil {
		t.Errorf("organization B reads the status %+v from the jobs of A", got)
	}
	if w := call(mux, orgCtx(f.OrgB), http.MethodGet, "/api/v1/attribute-definitions/"+def.ID); w.Code != http.StatusNotFound {
		t.Errorf("organization B reads the definition of A: %d", w.Code)
	}
}
