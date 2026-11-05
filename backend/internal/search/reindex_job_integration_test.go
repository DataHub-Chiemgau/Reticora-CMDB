package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// wp786FailID sorts after every other CI of the fixture, so it lies in the
// second batch of the CIs.
const wp786FailID = "fffffff0-0000-4000-8000-000000000786"

// TestSearchReindexJob covers WP-786 (SRC-02, E-08): POST
// /api/v1/search/reindex answers 202 with the job search.reindex (job_id,
// Location, the deprecated indexed 0); the job succeeds with the number of
// documents in its result. It rebuilds in batches of 1,000 entities per
// type, each in a transaction of its own: while a batch waits for a lock,
// the job runs and the search finds every entity. A client-scoped
// administrator rebuilds the whole organization. A soft-deleted CI is no
// longer in the index after the run. A failing batch fails the job with a
// sanitized error; the batches before it stay written and the final
// cleanup does not run.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchReindexJob(t *testing.T) {
	w := newSFWorld(t)
	f := w.f
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	w.exec(t, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp786 bulk ' || g, 'active'
		FROM generate_series(1, $2::int) AS g`, f.OrgA, search.ReindexBatchSize+50)
	gone := f.CI(t, f.OrgA, f.Client1, "wp786 gone")
	site, err := locations.NewPGRepository(f.App).Create(orgCtx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client2, Name: "wp786lager"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.repo.ReindexTenant(orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `UPDATE ci SET deleted_at = now() WHERE id = $1`, gone)

	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	svc.Register(jobs.Kind{Name: search.ReindexJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute})
	granted := map[string]bool{"ci:read": true, "asset:read": true, "location:read": true}
	mux := chi.NewRouter()
	search.NewHandler(w.repo, permissionStub{granted: granted}).WithGates(allFeatures{}, allModules).WithJobs(svc).RegisterRoutes(mux)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	serve := func(method, target string, scope *database.TenantScope) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		ctx = permission.WithPolicy(database.ContextWithTenantScope(ctx, scope), permission.UnrestrictedPolicy(frPerms))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	start := func() string {
		t.Helper()
		rec := serve(http.MethodPost, "/api/v1/search/reindex", &client1)
		var body map[string]any
		if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("POST /api/v1/search/reindex: %d %s", rec.Code, rec.Body.String())
		}
		id, _ := body["job_id"].(string)
		if id == "" || rec.Header().Get("Location") != "/api/v1/jobs/"+id || body["kind"] != search.ReindexJobKind || body["indexed"] != float64(0) {
			t.Fatalf("POST /api/v1/search/reindex: Location %q, body %v", rec.Header().Get("Location"), body)
		}
		return id
	}
	wait := func(id string, done func(*jobs.Job) bool) *jobs.Job {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for {
			job, gerr := svc.Get(orgCtx, f.OrgA, id)
			if gerr == nil && done(job) {
				return job
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s: %+v %v", id, job, gerr)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	ended := func(job *jobs.Job) bool {
		return job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed
	}
	// rebuilt counts the documents of the type the run since the time wrote.
	rebuilt := func(entityType string, since time.Time) (n int) {
		t.Helper()
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM search_document WHERE organization_id = $1 AND entity_type = $2 AND updated_at >= $3`,
			f.OrgA, entityType, since).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}
	indexed := func(entityID string) (n int) {
		t.Helper()
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM search_document WHERE entity_id = $1`, entityID).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}
	var liveCIs, live int
	if err = f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL),
		(SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL) + (SELECT count(*) FROM asset WHERE organization_id = $1)
		+ (SELECT count(*) FROM document WHERE organization_id = $1 AND deleted_at IS NULL) + (SELECT count(*) FROM ticket WHERE organization_id = $1)
		+ (SELECT count(*) FROM contact WHERE organization_id = $1) + (SELECT count(*) FROM location WHERE organization_id = $1)
		+ (SELECT count(*) FROM reservation WHERE organization_id = $1)`, f.OrgA).Scan(&liveCIs, &live); err != nil {
		t.Fatal(err)
	}
	if indexed(gone) != 1 {
		t.Fatal("the soft-deleted CI has no document before the run")
	}

	// A lock on the document of the asset holds the run in the batch of
	// the assets, after the CIs.
	lock, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(bg) }()
	if _, err = lock.Exec(bg, `SELECT 1 FROM search_document WHERE entity_type = 'asset' AND entity_id = $1 FOR UPDATE`, w.asset); err != nil {
		t.Fatal(err)
	}
	id := start()
	job := wait(id, func(job *jobs.Job) bool {
		return ended(job) || (job.Status == jobs.StatusRunning && job.StartedAt != nil && rebuilt("ci", *job.StartedAt) == liveCIs)
	})
	if job.Status != jobs.StatusRunning {
		t.Fatalf("job ended while the asset batch waits: %+v", job)
	}
	for q, want := range map[string][]string{"wp308": {w.alpha, w.beta, w.gamma, w.delta, w.asset}, "wp786lager": {site.ID}} {
		rec := serve(http.MethodGet, "/api/v1/search?"+url.Values{"q": {q}, "limit": {"100"}}.Encode(), ptr(database.OrgWideScope(f.OrgA, f.User)))
		var body struct {
			Data []search.Hit `json:"data"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("GET /api/v1/search?q=%s: %d %s", q, rec.Code, rec.Body.String())
		}
		hits := map[string]bool{}
		for i := range body.Data {
			hits[body.Data[i].EntityID] = true
		}
		for _, entityID := range want {
			if !hits[entityID] {
				t.Errorf("search %q during the run: %s is missing (%v)", q, entityID, hits)
			}
		}
	}
	if err = lock.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	job = wait(id, ended)
	var result search.ReindexResult
	if job.Status != jobs.StatusSucceeded || json.Unmarshal(job.Result, &result) != nil || result.Indexed != live {
		t.Fatalf("job: %s %s %s, want succeeded with %d documents", job.Status, job.Result, job.Error, live)
	}
	if indexed(gone) != 0 {
		t.Error("the soft-deleted CI is still indexed after the run")
	}
	// Organization-wide (E-08): the CI and the location of client 2 are
	// rebuilt although client 1 started the job.
	var startedAt time.Time
	if job.StartedAt != nil {
		startedAt = *job.StartedAt
	}
	for _, entityID := range []string{w.delta, site.ID} {
		var updated time.Time
		if err = f.Admin.QueryRow(bg, `SELECT updated_at FROM search_document WHERE entity_id = $1`, entityID).Scan(&updated); err != nil || updated.Before(startedAt) {
			t.Errorf("document of client 2 (%s) not rebuilt by the client-1 job: %v %v", entityID, updated, err)
		}
	}

	// A failing batch: the second batch of the CIs fails on the CI
	// wp786FailID; the first one stays written, the cleanup does not run.
	w.exec(t, `INSERT INTO ci (id, organization_id, ci_type_id, name, status)
		VALUES ($1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp786 fail', 'active')`, wp786FailID, f.OrgA)
	gone2 := f.CI(t, f.OrgA, f.Client1, "wp786 gone2")
	if _, err = w.repo.ReindexTenant(orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `UPDATE ci SET deleted_at = now() WHERE id = $1`, gone2)
	w.exec(t, `CREATE FUNCTION wp786_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'wp786 forced failure'; END $$`)
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(bg, `DROP TRIGGER IF EXISTS wp786_fail ON search_document`)
		_, _ = f.Admin.Exec(bg, `DROP FUNCTION IF EXISTS wp786_fail()`)
	})
	w.exec(t, `CREATE TRIGGER wp786_fail BEFORE INSERT OR UPDATE ON search_document
		FOR EACH ROW WHEN (NEW.title = 'wp786 fail') EXECUTE FUNCTION wp786_fail()`)
	id = start()
	job = wait(id, ended)
	if job.Status != jobs.StatusFailed || job.Error == "" || job.Error == "wp786 forced failure" {
		t.Fatalf("job with a failing batch: %s %q", job.Status, job.Error)
	}
	if got := rebuilt("ci", *job.StartedAt); got != search.ReindexBatchSize {
		t.Errorf("documents of the first CI batch after the failure: %d, want %d", got, search.ReindexBatchSize)
	}
	if indexed(gone2) != 1 {
		t.Error("the cleanup ran after a failed batch")
	}
}
