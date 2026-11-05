package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// apiRow is one JSON row of the API import.
type apiRow map[string]any

// jsonCIProfile stores a CI profile of format that maps ci_type, name,
// serial_number and model onto the targets of their names.
func (c *pgCI) jsonCIProfile(t *testing.T, format string) string {
	t.Helper()
	cols := map[string]ColumnMapping{}
	for _, target := range []string{"ci_type", "name", "serial_number", "model"} {
		cols[target] = ColumnMapping{Target: target}
	}
	p := &Profile{OrganizationID: c.f.OrgA, Name: "api-" + c.f.ID(), Entity: "ci", Format: format, Mode: "both",
		Mapping: Mapping{Columns: cols}}
	if err := c.profile.Create(c.f.OrgCtx(c.f.OrgA), p); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return p.ID
}

// rowsBody is the body of an API import.
func rowsBody(t *testing.T, profileID string, rows []apiRow, dryRun bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"profile_id": profileID, "rows": rows, "dry_run": dryRun})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// postImport sends POST /api/v1/imports to h with ctx and, when key is
// set, the header Idempotency-Key.
func postImport(ctx context.Context, h http.Handler, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/imports", strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// acceptedJob returns the job id of a 202.
func acceptedJob(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.JobID == "" {
		t.Fatalf("accepted %s: %v", w.Body.String(), err)
	}
	return body.JobID
}

// paramsOf returns the parameters of a job.
func (c *pgCI) paramsOf(t *testing.T, jobID string) (*jobs.Job, JobParams) {
	t.Helper()
	job, err := c.jobs.Get(c.f.OrgCtx(c.f.OrgA), c.f.OrgA, jobID)
	if err != nil {
		t.Fatal(err)
	}
	var p JobParams
	if err = json.Unmarshal(job.Params, &p); err != nil {
		t.Fatal(err)
	}
	return job, p
}

// staged reports whether the staging object of key exists in store.
func staged(t *testing.T, store blob.Store, key string) bool {
	t.Helper()
	rc, err := store.Get(context.Background(), export.ExportBucket, key)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read %s: %v", key, err)
		}
		return false
	}
	_ = rc.Close()
	return true
}

// stagedFiles counts the staging objects below dir (a file store).
func stagedFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "rows.json" && strings.Contains(filepath.ToSlash(path), "/imports/staging/") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// apiJobKeys are the only keys the parameters of an API run may hold.
var apiJobKeys = []string{"import_source", "staging_key", "row_count", "rows_sha256", "profile_id", "mode", "dry_run",
	"starter", "request_id", "source", "correlation_id"}

// TestImportAPIRows covers the API import over JSON rows (WP-303,
// IMP-IO-01): 1,000 rows run through the pipeline of the files with a
// duplicate inside the request and one against an existing CI, a nested
// value (row error) and a field the starter may not write; the dry run
// writes nothing. The parameters and the job API show no row value, the
// staging object is gone after the end of the run, and only the starter
// reads the run and its report. The request checks answer 422, 413 and
// 503; rows above max_cis fail with entitlement-limit.
func TestImportAPIRows(t *testing.T) {
	c := newPGCI(t)
	bg := context.Background()
	profileID := c.jsonCIProfile(t, FormatJSON)
	existing := c.seed(t, c.server, "api-existing", "snmp", map[string]any{"serial_number": "API-EXIST-1"})
	if _, err := c.f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level)
		VALUES ($1, $2, 'ci', 'core.model', 'read')`, c.f.OrgA, c.role); err != nil {
		t.Fatal(err)
	}
	c.writer(nil)

	rows := make([]apiRow, 1000)
	for i := range rows {
		rows[i] = apiRow{"ci_type": "server", "name": fmt.Sprintf("api-%04d", i), "serial_number": fmt.Sprintf("API-SN-%04d", i)}
	}
	rows[1]["serial_number"] = "API-EXIST-1"                                                    // against the existing CI
	rows[2]["tags"] = []string{"x"}                                                             // nested value: row error
	rows[3]["model"] = "M-1"                                                                    // a field the starter may not write
	rows[999] = apiRow{"ci_type": "server", "name": "api-0000", "serial_number": "API-SN-0000"} // duplicate of row 1
	live := c.liveCIs(t)

	// Dry run: the report of the run, nothing written.
	dry := acceptedJob(t, postImport(c.ctx(), c.mux, rowsBody(t, profileID, rows, true), ""))
	res, _ := c.finish(t, dry)
	if !res.DryRun || res.Total != 1000 || res.Failed != 2 {
		t.Fatalf("dry run %+v", res)
	}
	waitIdle(t)
	if n := c.liveCIs(t); n != live {
		t.Fatalf("the dry run wrote %d CIs", n-live)
	}

	jobID := acceptedJob(t, postImport(c.ctx(), c.mux, rowsBody(t, profileID, rows, false), ""))
	res, report := c.finish(t, jobID)
	if res.Total != 1000 || res.Created != 996 || res.Updated+res.Skipped != 2 || res.Failed != 2 {
		t.Fatalf("result %+v", res)
	}
	if !hasLine(report, 3, "tags", CodeNestedValue) || !hasLine(report, 4, "model", CodeFieldForbidden) {
		t.Errorf("report: %v", report)
	}
	if n := c.liveCIs(t); n != live+996 {
		t.Errorf("%d CIs written, want 996", n-live)
	}
	for _, serial := range []string{"API-SN-0000", "API-EXIST-1"} {
		if n := c.count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`,
			c.f.OrgA, serial); n != 1 {
			t.Errorf("%d CIs with serial %s, want 1", n, serial)
		}
	}
	if c.column(t, existing, "deleted_at") != "" {
		t.Error("the existing CI was deleted")
	}

	// The job holds no row value: only the staging reference.
	waitIdle(t)
	job, params := c.paramsOf(t, jobID)
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(job.Params, &keys); err != nil {
		t.Fatal(err)
	}
	for key := range keys {
		if !slices.Contains(apiJobKeys, key) {
			t.Errorf("job parameter %s: %s", key, job.Params)
		}
	}
	if params.ImportSource != SourceAPI || params.RowCount != 1000 || params.DocumentID != "" ||
		params.StagingKey == "" || len(params.RowsSHA256) != 64 {
		t.Errorf("params %+v", params)
	}
	for _, path := range []string{"/api/v1/jobs/" + jobID, "/api/v1/jobs?kind=" + JobKind} {
		w := c.call(t, c.ctx(), http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		for _, value := range []string{"api-0005", "API-SN-0005", "API-EXIST-1", "M-1"} {
			if strings.Contains(w.Body.String(), value) {
				t.Errorf("%s shows the row value %s", path, value)
			}
		}
	}
	// The run deleted its staging object at its end, the dry run too.
	_, dryParams := c.paramsOf(t, dry)
	for _, key := range []string{params.StagingKey, dryParams.StagingKey} {
		if staged(t, c.files, key) {
			t.Errorf("staging object %s kept after the end of the run", key)
		}
	}

	// Projection and report: the starter only (no document to check).
	w := c.call(t, c.ctx(), http.MethodGet, "/api/v1/imports/"+jobID, "")
	var run struct {
		Source     string  `json:"source"`
		DocumentID *string `json:"document_id"`
		RowCount   *int    `json:"row_count"`
		RowsTotal  *int    `json:"rows_total"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &run) != nil || run.Source != SourceAPI || run.DocumentID != nil ||
		run.RowCount == nil || *run.RowCount != 1000 || run.RowsTotal == nil || *run.RowsTotal != 1000 {
		t.Fatalf("projection %d %s", w.Code, w.Body.String())
	}
	if w = c.call(t, c.ctx(), http.MethodGet, "/api/v1/imports/"+jobID+"/report", ""); w.Code != http.StatusOK {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	other := requestContext(c.f.OrgA, c.f.AppUser(t, c.f.OrgA, "api-other"), nil)
	for _, path := range []string{"/api/v1/imports/" + jobID, "/api/v1/imports/" + jobID + "/report"} {
		wantProblem(t, c.call(t, other, http.MethodGet, path, ""), http.StatusNotFound, "")
	}

	// Request checks (422).
	csvProfile := c.jsonCIProfile(t, FormatCSV)
	one := []apiRow{{"ci_type": "server", "name": "api-422"}}
	tooMany := make([]apiRow, MaxAPIRows+1)
	for i := range tooMany {
		tooMany[i] = apiRow{}
	}
	for name, body := range map[string]string{
		"rows and document_id": fmt.Sprintf(`{"profile_id":%q,"document_id":"d","rows":[{"name":"x"}]}`, profileID),
		"neither":              fmt.Sprintf(`{"profile_id":%q}`, profileID),
		"empty rows":           fmt.Sprintf(`{"profile_id":%q,"rows":[]}`, profileID),
		"5001 rows":            rowsBody(t, profileID, tooMany, false),
		"scalar row":           fmt.Sprintf(`{"profile_id":%q,"rows":[1]}`, profileID),
	} {
		if w = postImport(c.ctx(), c.mux, body, ""); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	wantProblem(t, postImport(c.ctx(), c.mux, rowsBody(t, csvProfile, one, false), ""), http.StatusUnprocessableEntity,
		ProblemType(CodeFormatUnsupported))
	// A body above 16 MiB: 413.
	huge := fmt.Sprintf(`{"profile_id":%q,"rows":[{"name":"%s"}]}`, profileID, strings.Repeat("x", MaxAPIBodyBytes))
	wantProblem(t, postImport(c.ctx(), c.mux, huge, ""), http.StatusRequestEntityTooLarge, "")
	// Without blob storage: 503.
	noBlobs := chi.NewRouter()
	NewImportHandler(c.profile, c.docs, nil).WithJobs(c.jobs).RegisterRoutes(noBlobs)
	wantProblem(t, postImport(c.ctx(), noBlobs, rowsBody(t, profileID, one, false), ""), http.StatusServiceUnavailable, "")

	// Rows above max_cis: entitlement-limit as row errors (WP-302).
	limit := int64(c.liveCIs(t) + 2)
	c.writer(fixedLimit{n: limit})
	limited := make([]apiRow, 4)
	for i := range limited {
		limited[i] = apiRow{"ci_type": "server", "name": fmt.Sprintf("api-limit-%d", i), "serial_number": fmt.Sprintf("API-LIMIT-%d", i)}
	}
	res, report = c.finish(t, acceptedJob(t, postImport(c.ctx(), c.mux, rowsBody(t, profileID, limited, false), "")))
	if res.Created != 2 || res.Failed != 2 || int64(c.liveCIs(t)) != limit ||
		!hasLine(report, 3, "", CodeEntitlementLimit) || !hasLine(report, 4, "", CodeEntitlementLimit) {
		t.Errorf("at max_cis: %+v %v", res, report)
	}
}

// enqueueOnly is a job queue that records the jobs without running them,
// as jobs.Queue.Submit does before a worker claims the job.
type enqueueOnly struct{ svc *jobs.Service }

func (q enqueueOnly) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	return q.svc.Enqueue(ctx, orgID, kind, params, opts)
}

// failingCache is a cache whose every call fails (an outage).
type failingCache struct{ cache.Store }

var errCacheDown = errors.New("cache down")

func (failingCache) Get(context.Context, string) (string, bool, error) {
	return "", false, errCacheDown
}
func (failingCache) Set(context.Context, string, string, time.Duration) error {
	return errCacheDown
}

func (failingCache) Increment(context.Context, string, time.Duration) (int64, error) {
	return 0, errCacheDown
}
func (failingCache) Decrement(context.Context, string) (int64, error) { return 0, errCacheDown }
func (failingCache) Delete(context.Context, string) error             { return errCacheDown }

// TestImportAPIIdempotency covers the two stages against double imports
// (WP-303, API-04): 5,000 rows (> 1 MiB) with Idempotency-Key are queued
// once; the repetition replays the 202 of the same job, another body with
// the key is 409 idempotency-mismatch. With the cache down the job key
// gives the same job and no second run, another principal and another
// body after the cache forgot the key get a job of their own; the job key
// does not hold the header value. A start that returns an existing job
// deletes its staging object at once, and a job cancelled before its start
// loses its object in the next import.staging.sweep.
func TestImportAPIIdempotency(t *testing.T) {
	c := newPGCI(t)
	profileID := c.jsonCIProfile(t, FormatJSON)
	dir := t.TempDir()
	files := blob.NewFileStore(dir)
	h := NewImportHandler(c.profile, c.docs, files).WithJobs(c.jobs).WithQueue(enqueueOnly{svc: c.jobs})
	route := func(store cache.Store) http.Handler {
		mux := chi.NewRouter()
		mux.With(middleware.IdempotencyWithStore(store)).Post("/api/v1/imports", h.Start)
		return mux
	}
	working, down := route(cache.NewMemoryStore()), route(failingCache{})

	rows := make([]apiRow, MaxAPIRows)
	for i := range rows {
		rows[i] = apiRow{"ci_type": "server", "name": fmt.Sprintf("idem-%04d", i), "serial_number": fmt.Sprintf("IDEM-%04d", i),
			"note": strings.Repeat("n", 220)}
	}
	body := rowsBody(t, profileID, rows, false)
	if len(body) <= 1<<20 {
		t.Fatalf("body of %d bytes, want more than 1 MiB", len(body))
	}
	const header = "idem-key-303"
	importJobs := func() int {
		return c.count(t, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, c.f.OrgA, JobKind)
	}

	first := postImport(c.ctx(), working, body, header)
	jobID := acceptedJob(t, first)
	if stagedFiles(t, dir) != 1 || importJobs() != 1 {
		t.Fatalf("after the start: %d staging objects, %d jobs", stagedFiles(t, dir), importJobs())
	}
	job, params := c.paramsOf(t, jobID)
	if want := jobIdempotencyKey(c.ctx(), header, []byte(body)); job.IdempotencyKey != want || job.IdempotencyKey == header {
		t.Errorf("job key %q, want %q", job.IdempotencyKey, want)
	}
	if strings.Contains(string(job.Params), header) || strings.Contains(string(job.Params), "IDEM-0001") {
		t.Errorf("params %s", job.Params)
	}

	// Repetition: the stored 202 of the same job.
	again := postImport(c.ctx(), working, body, header)
	if acceptedJob(t, again) != jobID || again.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %s %v", again.Body.String(), again.Header())
	}
	// Same key, another body: 409.
	wantProblem(t, postImport(c.ctx(), working, rowsBody(t, profileID, rows, true), header), http.StatusConflict,
		httpx.TypeIdempotencyMismatch)

	// Cache down: the job key returns the same job, no second job, and the
	// staging object of this start is deleted at once.
	failOpen := postImport(c.ctx(), down, body, header)
	if acceptedJob(t, failOpen) != jobID || failOpen.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("with the cache down: %s %v", failOpen.Body.String(), failOpen.Header())
	}
	if importJobs() != 1 || stagedFiles(t, dir) != 1 || !staged(t, files, params.StagingKey) {
		t.Fatalf("with the cache down: %d jobs, %d staging objects", importJobs(), stagedFiles(t, dir))
	}

	// Another principal with the same key: a job of its own.
	other := requestContext(c.f.OrgA, c.f.AppUser(t, c.f.OrgA, "idem-other"), nil)
	if id := acceptedJob(t, postImport(other, working, body, header)); id == jobID {
		t.Fatal("another principal got the job of the key")
	}
	// The same key with another body once the cache forgot it: a new job.
	if id := acceptedJob(t, postImport(c.ctx(), route(cache.NewMemoryStore()), rowsBody(t, profileID, rows[:10], false), header)); id == jobID {
		t.Fatal("another body got the job of the key")
	}
	if importJobs() != 3 || stagedFiles(t, dir) != 3 {
		t.Fatalf("%d jobs, %d staging objects, want 3 each", importJobs(), stagedFiles(t, dir))
	}

	// Cancelled before the start: the sweep deletes the object.
	if _, err := c.jobs.Cancel(c.f.OrgCtx(c.f.OrgA), c.f.OrgA, jobID); err != nil {
		t.Fatal(err)
	}
	n, err := NewStagingSweep(c.f.App, files).RunOrg(context.Background(), c.f.OrgA)
	if err != nil || n != 1 || staged(t, files, params.StagingKey) || stagedFiles(t, dir) != 2 {
		t.Fatalf("sweep: %d deleted (%v), %d staging objects left", n, err, stagedFiles(t, dir))
	}
	// A second sweep finds the object missing and counts it as deleted.
	if n, err = NewStagingSweep(c.f.App, files).RunOrg(context.Background(), c.f.OrgA); err != nil || n != 1 {
		t.Fatalf("second sweep: %d (%v)", n, err)
	}
}
