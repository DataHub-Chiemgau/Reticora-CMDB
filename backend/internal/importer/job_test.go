package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

const (
	testOrg     = "00000000-0000-4000-8000-00000000a001"
	otherOrg    = "00000000-0000-4000-8000-00000000b001"
	testUser    = "00000000-0000-4000-8000-0000000000f1"
	importAdmin = "00000000-0000-4000-8000-0000000000f2"
)

// gatedStore is a blob store whose reads of the gated key stop after the
// first limit bytes until release is closed.
type gatedStore struct {
	blob.Store
	mu      sync.Mutex
	key     string
	limit   int
	release chan struct{}
}

func (g *gatedStore) gate(key string, limit int) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.key, g.limit, g.release = key, limit, make(chan struct{})
	return g.release
}

func (g *gatedStore) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	rc, err := g.Store.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if key != g.key {
		return rc, nil
	}
	return &gatedReader{rc: rc, left: g.limit, release: g.release}, nil
}

type gatedReader struct {
	rc      io.ReadCloser
	left    int
	release chan struct{}
}

func (r *gatedReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		<-r.release
		return r.rc.Read(p)
	}
	if len(p) > r.left {
		p = p[:r.left]
	}
	n, err := r.rc.Read(p)
	r.left -= n
	return n, err
}

func (r *gatedReader) Close() error { return r.rc.Close() }

// versionKeys overrides the storage key of document versions (legacy and
// foreign keys, which CreateVersion refuses).
type versionKeys struct {
	document.Repository
	keys map[string]string
}

func (v versionKeys) GetVersion(ctx context.Context, orgID, docID string, n int) (*document.Version, error) {
	ver, err := v.Repository.GetVersion(ctx, orgID, docID, n)
	if err == nil {
		if key, ok := v.keys[docID]; ok {
			ver.StorageKey = key
		}
	}
	return ver, err
}

// importStack serves the import routes and the job API over in-memory
// repositories and a file blob store.
type importStack struct {
	mux      chi.Router
	jobs     *jobs.Service
	docs     *document.MemoryRepository
	keys     map[string]string
	profiles *MemoryRepository
	files    blob.Store
	blobs    *gatedStore
}

func newImportStack(t *testing.T) *importStack {
	t.Helper()
	s := &importStack{
		jobs:     jobs.NewService(jobs.NewMemoryRepository()),
		docs:     document.NewMemoryRepository(),
		keys:     map[string]string{},
		profiles: NewMemoryRepository(),
		files:    blob.NewFileStore(t.TempDir()),
	}
	s.blobs = &gatedStore{Store: s.files}
	s.jobs.Register(jobs.Kind{Name: JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true})
	s.jobs.Register(jobs.Kind{Name: "ci.bulk", MaxAttempts: 1})
	s.mux = chi.NewRouter()
	// The CI writer follows with WP-302: a writer that accepts every row.
	NewImportHandler(s.profiles, versionKeys{Repository: s.docs, keys: s.keys}, s.blobs).WithJobs(s.jobs).WithStarters(allowStarters, allowStarters).
		WithWriter(&fakeWriter{entity: "ci"}).RegisterRoutes(s.mux)
	jobs.NewAPIHandler(s.jobs).WithKind(JobKind, identity.PermImportRun, nil).RegisterRoutes(s.mux)
	t.Cleanup(func() { waitIdle(t) })
	return s
}

// waitIdle waits until no run holds a parse slot.
func waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(runSlots) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("an import run did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requestContext is the context of a principal of org with import:run and
// the write rights of CIs and locations.
func requestContext(org, user string, scope *database.TenantScope) context.Context {
	return requestContextWith(org, user, scope, identity.PermImportRun, identity.PermCIWrite, identity.PermLocationWrite)
}

// requestContextWith is the context of a principal of org with perms.
func requestContextWith(org, user string, scope *database.TenantScope, perms ...identity.Permission) context.Context {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: user})
	if scope == nil {
		s := database.OrgWideScope(org, user)
		scope = &s
	}
	ctx = database.ContextWithTenantScope(ctx, scope)
	// The route audit names a session's request source ui (AuditRoute).
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "req-import", Source: audit.SourceUI})
	return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser,
		Permissions: perms})
}

// fakeStarters are the user status and the role assignments a run reloads
// (WithStarters): an active user with one org-wide role of perms.
type fakeStarters struct {
	mu       sync.Mutex
	perms    []identity.Permission
	inactive bool
}

func newStarters(perms ...identity.Permission) *fakeStarters {
	if perms == nil {
		perms = []identity.Permission{identity.PermImportRun, identity.PermCIWrite, identity.PermLocationWrite}
	}
	return &fakeStarters{perms: perms}
}

// allowStarters hold every right a test import needs; never changed.
var allowStarters = newStarters()

func (f *fakeStarters) UserActive(context.Context, string, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.inactive, nil
}

func (f *fakeStarters) AccessGrants(context.Context, string, string) ([]identity.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []identity.Grant{{RoleID: "import-role", Permissions: append([]identity.Permission(nil), f.perms...)}}, nil
}

func (s *importStack) call(t *testing.T, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func (s *importStack) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return s.call(t, requestContext(testOrg, testUser, nil), method, path, body)
}

// profile stores a profile of org with format and sheet.
func (s *importStack) profile(t *testing.T, org, name, format, sheet string) string {
	t.Helper()
	var req CreateProfileRequest
	if err := json.Unmarshal([]byte(validProfile(name, "")), &req); err != nil {
		t.Fatal(err)
	}
	req.Format = format
	if sheet != "" {
		req.Sheet = &sheet
	}
	// The columns of csvRows.
	req.Mapping = &Mapping{Columns: map[string]ColumnMapping{
		"name":   {Target: "name", Transforms: []Transform{{Op: TransformTrim}}},
		"serial": {Target: "attributes.serial"},
	}}
	req.IdentityKeys = []string{"name"}
	p := newProfile(org, testUser, &req)
	if err := s.profiles.Create(context.Background(), p); err != nil {
		t.Fatalf("profile %s: %v", name, err)
	}
	return p.ID
}

// document stores a document of org whose version 1 holds content with
// the verified type mime and the scan status scan; size overrides the
// recorded size when positive.
func (s *importStack) document(t *testing.T, org, mime string, content []byte, scan string, size int64) string {
	t.Helper()
	ctx := context.Background()
	d := &document.Document{OrganizationID: org, Title: "import", FileName: "import", UploadedBy: testUser}
	if err := s.docs.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	key := blob.OrgKey(org, "documents", d.ID, "v1")
	if size <= 0 {
		size = int64(len(content))
	}
	if _, err := s.docs.CreateVersion(ctx, &document.Version{ID: "v1", OrganizationID: org, DocumentID: d.ID,
		StorageKey: key, Size: size, MimeType: mime}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.docs.SetScanResult(ctx, org, d.ID, 1, scan, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.files.Put(ctx, document.Bucket, key, bytes.NewReader(content), int64(len(content)), mime); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func startBody(docID, profileID string) string {
	return fmt.Sprintf(`{"document_id":%q,"profile_id":%q}`, docID, profileID)
}

func wantProblem(t *testing.T, w *httptest.ResponseRecorder, status int, problem string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if problem != "" && !strings.Contains(w.Body.String(), `"type":"`+problem+`"`) {
		t.Fatalf("problem type: %s, want %s", w.Body.String(), problem)
	}
}

// start starts an import and returns its job id.
func (s *importStack) start(t *testing.T, docID, profileID string) string {
	t.Helper()
	w := s.do(t, http.MethodPost, "/api/v1/imports", startBody(docID, profileID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Kind != JobKind || body.JobID == "" {
		t.Fatalf("start answer %s (%v)", w.Body.String(), err)
	}
	if loc := w.Header().Get("Location"); loc != "/api/v1/jobs/"+body.JobID {
		t.Fatalf("Location %q", loc)
	}
	return body.JobID
}

// wait waits until the job reaches a terminal status.
func (s *importStack) wait(t *testing.T, jobID string) *jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := s.jobs.Get(context.Background(), testOrg, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status.Terminal() {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s", jobID, job.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitProgress waits until the running job reports progress.
func (s *importStack) waitProgress(t *testing.T, jobID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := s.jobs.Get(context.Background(), testOrg, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusRunning && job.Progress > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: %s with progress %d", jobID, job.Status, job.Progress)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *importStack) run(t *testing.T, jobID string) ImportRun {
	t.Helper()
	w := s.do(t, http.MethodGet, "/api/v1/imports/"+jobID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get import: %d %s", w.Code, w.Body.String())
	}
	var run ImportRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	return run
}

// report returns the report behind the download URL of the report route.
func (s *importStack) report(t *testing.T, jobID string) string {
	t.Helper()
	w := s.do(t, http.MethodGet, "/api/v1/imports/"+jobID+"/report", "")
	if w.Code != http.StatusOK {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	var link ReportLink
	if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link.DownloadURL)
	if err != nil || u.Scheme != "file" {
		t.Fatalf("download url %q (%v)", link.DownloadURL, err)
	}
	data, err := os.ReadFile(u.Path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	return string(data)
}

func pageAll() api.PaginationParams { return api.PaginationParams{Limit: 200} }

func csvRows(n int) []byte {
	var b strings.Builder
	b.WriteString("name;serial\n")
	for i := range n {
		fmt.Fprintf(&b, "host-%06d;serial-%06d-padding-padding\n", i, i)
	}
	return []byte(b.String())
}

func TestImportStartChecks(t *testing.T) {
	s := newImportStack(t)
	csvProfile := s.profile(t, testOrg, "csv", FormatCSV, "")
	jsonProfile := s.profile(t, testOrg, "json", "json", "")
	foreignProfile := s.profile(t, otherOrg, "foreign", FormatCSV, "")
	content := csvRows(3)
	clean := s.document(t, testOrg, "text/csv", content, document.ScanClean, 0)
	foreignDoc := s.document(t, otherOrg, "text/csv", content, document.ScanClean, 0)

	// Document or profile of another organization: 404.
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(foreignDoc, csvProfile)), http.StatusNotFound, "")
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(clean, foreignProfile)), http.StatusNotFound, "")
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody("doc-404", csvProfile)), http.StatusNotFound, "")

	// Scan status other than clean: 409 (E-42, fail-closed).
	for status, problem := range map[string]string{
		document.ScanPending:  document.ProblemPendingScan,
		document.ScanFailed:   document.ProblemPendingScan,
		document.ScanInfected: document.ProblemQuarantined,
	} {
		doc := s.document(t, testOrg, "text/csv", content, status, 0)
		wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(doc, csvProfile)), http.StatusConflict, problem)
	}

	// Larger than 50 MB: 422 import-file-too-large.
	big := s.document(t, testOrg, "text/csv", content, document.ScanClean, MaxFileBytes+1)
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(big, csvProfile)), http.StatusUnprocessableEntity,
		ProblemType(CodeFileTooLarge))
	exact := s.document(t, testOrg, "text/csv", content, document.ScanClean, MaxFileBytes)
	if w := s.do(t, http.MethodPost, "/api/v1/imports", startBody(exact, csvProfile)); w.Code != http.StatusAccepted {
		t.Fatalf("exactly 50 MB: %d %s", w.Code, w.Body.String())
	}

	// Profile format json and an .xls file: 422 import-format-unsupported.
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(clean, jsonProfile)), http.StatusUnprocessableEntity,
		ProblemType(CodeFormatUnsupported))
	xls := s.document(t, testOrg, "application/vnd.ms-excel", []byte("\xD0\xCF\x11\xE0"), document.ScanClean, 0)
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(xls, csvProfile)), http.StatusUnprocessableEntity,
		ProblemType(CodeFormatUnsupported))
	xlsxProfile := s.profile(t, testOrg, "xlsx", FormatXLSX, "")
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", startBody(clean, xlsxProfile)), http.StatusUnprocessableEntity,
		ProblemType(CodeFormatUnsupported))

	// Body errors.
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports", `{"document_id":"x"}`), http.StatusUnprocessableEntity, "")
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports",
		fmt.Sprintf(`{"document_id":%q,"profile_id":%q,"mode":"merge"}`, clean, csvProfile)), http.StatusUnprocessableEntity, "")

	// A valid start: 202 with a job of kind import.run that ends with the
	// counters and a report.
	jobID := s.start(t, clean, csvProfile)
	job := s.wait(t, jobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("job %s: %s", job.Status, job.Error)
	}
	var params JobParams
	if err := json.Unmarshal(job.Params, &params); err != nil || params.DocumentID != clean || params.DocumentVersion != 1 ||
		params.ProfileID != csvProfile || params.Mode != "both" || params.DryRun {
		t.Fatalf("params %s (%v)", job.Params, err)
	}
	run := s.run(t, jobID)
	if run.Kind != JobKind || run.Status != jobs.StatusSucceeded || run.RowsTotal == nil || *run.RowsTotal != 3 ||
		*run.RowsFailed != 0 || run.ErrorCode != nil || run.Progress != 100 {
		t.Fatalf("projection %+v", run)
	}
	var result JobResult
	if err := json.Unmarshal(job.Result, &result); err != nil || result.ReportKey != ReportKey(testOrg, jobID) {
		t.Fatalf("result %s (%v)", job.Result, err)
	}
	if got := s.report(t, jobID); !strings.HasPrefix(got, "\ufeffZeile;Feld;Code;Meldung\n") || strings.Count(got, ";created;") != 3 {
		t.Fatalf("report %q", got)
	}

	// mode and dry_run of the request are the job's.
	w := s.do(t, http.MethodPost, "/api/v1/imports",
		fmt.Sprintf(`{"document_id":%q,"profile_id":%q,"mode":"create_missing","dry_run":true}`, clean, csvProfile))
	if w.Code != http.StatusAccepted {
		t.Fatalf("start with mode: %d %s", w.Code, w.Body.String())
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &accepted)
	if run = s.run(t, accepted.JobID); run.Mode != "create_missing" || !run.DryRun {
		t.Fatalf("mode and dry run: %+v", run)
	}
	s.wait(t, accepted.JobID)
}

func TestImportRunProgressReportAndCancel(t *testing.T) {
	s := newImportStack(t)
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	content := csvRows(5000)
	docID := s.document(t, testOrg, "text/csv", content, document.ScanClean, 0)
	key := blob.OrgKey(testOrg, "documents", docID, "v1")

	// A run held after 100 KB: it reported its progress and is running.
	release := s.blobs.gate(key, 100<<10)
	jobID := s.start(t, docID, profileID)
	s.waitProgress(t, jobID)
	w := s.do(t, http.MethodGet, "/api/v1/jobs/"+jobID, "")
	var job jobs.Job
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.Kind != JobKind || !job.Cancellable || job.Progress == 0 {
		t.Fatalf("job while running: %s (%v)", w.Body.String(), err)
	}
	// The report is not ready while the run is running.
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+jobID+"/report", ""), http.StatusConflict,
		ProblemType("import-report-not-ready"))
	if run := s.run(t, jobID); run.Status != jobs.StatusRunning || !run.Cancellable || run.RowsTotal != nil {
		t.Fatalf("projection while running: %+v", run)
	}
	close(release)
	if done := s.wait(t, jobID); done.Status != jobs.StatusSucceeded {
		t.Fatalf("released run: %s %s", done.Status, done.Error)
	}
	if run := s.run(t, jobID); *run.RowsTotal != 5000 {
		t.Fatalf("rows %d", *run.RowsTotal)
	}
	s.report(t, jobID)

	// A holder of import:run (not the creator) cancels a running import;
	// the run ends at its next progress report and keeps the report of
	// the rows processed so far (WP-775); the report route stays 404.
	release = s.blobs.gate(key, 100<<10)
	jobID = s.start(t, docID, profileID)
	s.waitProgress(t, jobID)
	admin := requestContext(testOrg, importAdmin, nil)
	if w = s.call(t, admin, http.MethodDelete, "/api/v1/jobs/"+jobID, ""); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	close(release)
	waitIdle(t)
	done := s.wait(t, jobID)
	var kept JobResult
	if done.Status != jobs.StatusCancelled || json.Unmarshal(done.Result, &kept) != nil || kept.ReportKey != ReportKey(testOrg, jobID) {
		t.Fatalf("cancelled run: %s %s", done.Status, done.Result)
	}
	rc, err := s.files.Get(context.Background(), export.ExportBucket, ReportKey(testOrg, jobID))
	if err != nil {
		t.Fatalf("a cancelled run keeps its report: %v", err)
	}
	_ = rc.Close()
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+jobID+"/report", ""), http.StatusNotFound, "")
}

func TestImportRowLimitFailsWithReport(t *testing.T) {
	s := newImportStack(t)
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	docID := s.document(t, testOrg, "text/csv", csvRows(MaxRows+1), document.ScanClean, 0)
	jobID := s.start(t, docID, profileID)
	if job := s.wait(t, jobID); job.Status != jobs.StatusFailed {
		t.Fatalf("job %s", job.Status)
	}
	run := s.run(t, jobID)
	if run.ErrorCode == nil || *run.ErrorCode != CodeRowLimit || run.RowsTotal != nil {
		t.Fatalf("projection %+v", run)
	}
	if got := s.report(t, jobID); !strings.Contains(got, ";"+CodeRowLimit+";") {
		t.Fatalf("report %q", got)
	}
}

func TestImportRowErrorsAndXLSXSheet(t *testing.T) {
	s := newImportStack(t)
	csvProfile := s.profile(t, testOrg, "csv", FormatCSV, "")
	docID := s.document(t, testOrg, "text/plain", []byte("a,b\n1,2\nx,y\"z\n3,4\n"), document.ScanClean, 0)
	jobID := s.start(t, docID, csvProfile)
	if job := s.wait(t, jobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job %s %s", job.Status, job.Error)
	}
	if run := s.run(t, jobID); *run.RowsTotal != 3 || *run.RowsFailed != 1 {
		t.Fatalf("counters %+v", run)
	}
	if got := s.report(t, jobID); !strings.Contains(got, "3;4;"+CodeRowMalformed+";") {
		t.Fatalf("report %q", got)
	}

	// The sheet of an XLSX profile; an unknown one fails the run.
	data := workbook(t, map[string][][]string{"A": {{"x"}, {"1"}}, "B": {{"y"}, {"1"}, {"2"}}}, "A", "B")
	xlsxDoc := s.document(t, testOrg, mimeXLSX, data, document.ScanClean, 0)
	jobID = s.start(t, xlsxDoc, s.profile(t, testOrg, "sheet-b", FormatXLSX, "B"))
	s.wait(t, jobID)
	if run := s.run(t, jobID); run.Status != jobs.StatusSucceeded || *run.RowsTotal != 2 || run.Sheet == nil || *run.Sheet != "B" {
		t.Fatalf("sheet B: %+v", run)
	}
	jobID = s.start(t, xlsxDoc, s.profile(t, testOrg, "sheet-c", FormatXLSX, "C"))
	s.wait(t, jobID)
	if run := s.run(t, jobID); run.Status != jobs.StatusFailed || *run.ErrorCode != CodeSheetNotFound {
		t.Fatalf("sheet C: %+v", run)
	}
}

func TestImportReadsLegacyKeysOnly(t *testing.T) {
	s := newImportStack(t)
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	content := csvRows(2)

	// A legacy key documents/<org>/<doc>/… of the document is read.
	legacy := s.document(t, testOrg, "text/csv", content, document.ScanClean, 0)
	s.keys[legacy] = "documents/" + testOrg + "/" + legacy + "/old.csv"
	if err := s.files.Put(context.Background(), document.Bucket, s.keys[legacy], bytes.NewReader(content), int64(len(content)), "text/csv"); err != nil {
		t.Fatal(err)
	}
	jobID := s.start(t, legacy, profileID)
	if job := s.wait(t, jobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("legacy key: %s %s", job.Status, job.Error)
	}

	// A key of another organization is not read.
	foreign := s.document(t, testOrg, "text/csv", content, document.ScanClean, 0)
	s.keys[foreign] = blob.OrgKey(otherOrg, "documents", foreign, "v1")
	if err := s.files.Put(context.Background(), document.Bucket, s.keys[foreign], bytes.NewReader(content), int64(len(content)), "text/csv"); err != nil {
		t.Fatal(err)
	}
	jobID = s.start(t, foreign, profileID)
	if job := s.wait(t, jobID); job.Status != jobs.StatusFailed || !strings.HasPrefix(job.Error, "import-document-unavailable") {
		t.Fatalf("foreign key: %s %s", job.Status, job.Error)
	}
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, foreign)), http.StatusConflict, "")
}

func TestImportProjectionOnlyForImportJobs(t *testing.T) {
	s := newImportStack(t)
	ctx := requestContext(testOrg, testUser, nil)
	other, _, err := s.jobs.Enqueue(ctx, testOrg, "ci.bulk", map[string]any{"document_id": "doc-1"}, jobs.EnqueueOptions{CreatedBy: testUser})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+other.ID, ""), http.StatusNotFound, "")
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+other.ID+"/report", ""), http.StatusNotFound, "")
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/missing", ""), http.StatusNotFound, "")
}

func TestImportPreview(t *testing.T) {
	s := newImportStack(t)
	docID := s.document(t, testOrg, "text/csv", csvRows(30), document.ScanClean, 0)
	w := s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, docID))
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var p Preview
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Format != FormatCSV || len(p.SampleRows) != PreviewRows || len(p.Sheets) != 0 ||
		strings.Join(p.Columns, ",") != "name,serial" || p.SampleRows[0][0] != "host-000000" {
		t.Fatalf("preview %+v", p)
	}
	// No job is created.
	if list, total, err := s.jobs.List(context.Background(), testOrg, jobs.Filter{}, pageAll()); err != nil || total != 0 || len(list) != 0 {
		t.Fatalf("jobs after the preview: %d (%v)", total, err)
	}

	data := workbook(t, map[string][][]string{"A": {{"x"}, {"1"}}, "B": {{"y", "z"}, {"1", "2"}}}, "A", "B")
	xlsxDoc := s.document(t, testOrg, mimeXLSX, data, document.ScanClean, 0)
	w = s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q,"sheet":"B"}`, xlsxDoc))
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != http.StatusOK || p.Format != FormatXLSX ||
		strings.Join(p.Sheets, ",") != "A,B" || strings.Join(p.Columns, ",") != "y,z" || len(p.SampleRows) != 1 {
		t.Fatalf("xlsx preview %d %s", w.Code, w.Body.String())
	}
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q,"sheet":"Z"}`, xlsxDoc)),
		http.StatusUnprocessableEntity, ProblemType(CodeSheetNotFound))

	// The checks of the start without profile.
	pending := s.document(t, testOrg, "text/csv", csvRows(1), document.ScanPending, 0)
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, pending)),
		http.StatusConflict, document.ProblemPendingScan)
	pdf := s.document(t, testOrg, "application/pdf", []byte("%PDF-1.4"), document.ScanClean, 0)
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, pdf)),
		http.StatusUnprocessableEntity, ProblemType(CodeFormatUnsupported))
	wide := s.document(t, testOrg, "text/csv", []byte(strings.Repeat("c;", MaxColumns)+"c\n"), document.ScanClean, 0)
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, wide)),
		http.StatusUnprocessableEntity, ProblemType(CodeFileTooComplex))
	foreign := s.document(t, otherOrg, "text/csv", csvRows(1), document.ScanClean, 0)
	wantProblem(t, s.do(t, http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, foreign)),
		http.StatusNotFound, "")
}

func TestImportRoutesWithoutJobsOrBlobs(t *testing.T) {
	docs := document.NewMemoryRepository()
	for name, h := range map[string]*ImportHandler{
		"no jobs":  NewImportHandler(NewMemoryRepository(), docs, blob.NewFileStore(t.TempDir())),
		"no blobs": NewImportHandler(NewMemoryRepository(), docs, nil).WithJobs(jobs.NewService(jobs.NewMemoryRepository())),
	} {
		mux := chi.NewRouter()
		h.RegisterRoutes(mux)
		ctx := requestContext(testOrg, testUser, nil)
		for _, op := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/imports", `{"document_id":"d","profile_id":"p"}`},
			{http.MethodGet, "/api/v1/imports/x/report", ""},
			{http.MethodGet, "/api/v1/imports/x/errors", ""},
		} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(op.method, op.path, strings.NewReader(op.body)).WithContext(ctx))
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: %s %s = %d", name, op.method, op.path, w.Code)
			}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/imports/preview", strings.NewReader(`{"document_id":"d"}`)).WithContext(ctx))
		if want := map[string]int{"no jobs": http.StatusNotFound, "no blobs": http.StatusServiceUnavailable}[name]; w.Code != want {
			t.Errorf("%s: preview = %d, want %d", name, w.Code, want)
		}
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/imports/x", nil).WithContext(ctx))
		if want := map[string]int{"no jobs": http.StatusServiceUnavailable, "no blobs": http.StatusNotFound}[name]; w.Code != want {
			t.Errorf("%s: get = %d, want %d", name, w.Code, want)
		}
	}
}

// TestImportDocumentOutsideClientScopePostgres: a document linked only to
// a CI of client 2 is not found for a principal of client 1, at the start,
// in the preview, in the projection and for the report (TEST_DATABASE_URL;
// skipped without it).
func TestImportDocumentOutsideClientScopePostgres(t *testing.T) {
	f := scopetest.Seed(t, "05")
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "import-author")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "import-c2")
	ownCI := f.CI(t, f.OrgA, f.Client1, "import-c1")
	docs := document.NewPGRepository(f.App)
	profiles := NewPGRepository(f.App)
	files := blob.NewFileStore(t.TempDir())
	content := csvRows(2)

	var req CreateProfileRequest
	if err := json.Unmarshal([]byte(validProfile("scope", "")), &req); err != nil {
		t.Fatal(err)
	}
	profile := newProfile(f.OrgA, user, &req)
	if err := profiles.Create(orgCtx, profile); err != nil {
		t.Fatalf("profile: %v", err)
	}
	newDoc := func(title, ci string) string {
		t.Helper()
		d := &document.Document{OrganizationID: f.OrgA, Title: title, FileName: title + ".csv", UploadedBy: user, Category: "general"}
		if err := docs.Create(orgCtx, d); err != nil {
			t.Fatalf("document: %v", err)
		}
		if err := docs.LinkDocument(orgCtx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: d.ID, EntityType: "ci", EntityID: ci}); err != nil {
			t.Fatalf("link: %v", err)
		}
		versionID := f.ID()
		key := blob.OrgKey(f.OrgA, "documents", d.ID, versionID)
		if _, err := docs.CreateVersion(orgCtx, &document.Version{ID: versionID, OrganizationID: f.OrgA, DocumentID: d.ID,
			StorageKey: key, Size: int64(len(content)), MimeType: "text/csv", UploadedBy: user}, nil); err != nil {
			t.Fatalf("version: %v", err)
		}
		if err := docs.SetScanResult(orgCtx, f.OrgA, d.ID, 1, document.ScanClean, ""); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if err := files.Put(context.Background(), document.Bucket, key, bytes.NewReader(content), int64(len(content)), "text/csv"); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	foreignDoc := newDoc("import-foreign", foreignCI)
	ownDoc := newDoc("import-own", ownCI)

	jobSvc := jobs.NewService(jobs.NewMemoryRepository())
	jobSvc.Register(jobs.Kind{Name: JobKind, MaxAttempts: 1, Cancellable: true})
	mux := chi.NewRouter()
	NewImportHandler(profiles, docs, files).WithJobs(jobSvc).WithStarters(allowStarters, allowStarters).WithWriter(&fakeWriter{entity: "ci"}).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, user)
	scope.Clients = database.ScopeIDs(f.Client1)
	client1 := requestContext(f.OrgA, user, &scope)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(client1))
		return w
	}

	wantProblem(t, call(http.MethodPost, "/api/v1/imports", startBody(foreignDoc, profile.ID)), http.StatusNotFound, "")
	wantProblem(t, call(http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, foreignDoc)), http.StatusNotFound, "")
	if w := call(http.MethodPost, "/api/v1/imports/preview", fmt.Sprintf(`{"document_id":%q}`, ownDoc)); w.Code != http.StatusOK {
		t.Fatalf("preview of the own document: %d %s", w.Code, w.Body.String())
	}

	// A finished import job of client 1 that names the foreign document:
	// projection and report are 404.
	job, _, err := jobSvc.Enqueue(client1, f.OrgA, JobKind, JobParams{DocumentID: foreignDoc, DocumentVersion: 1, ProfileID: profile.ID, Mode: "both"},
		jobs.EnqueueOptions{CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobSvc.ClaimJob(client1, f.OrgA, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = jobSvc.Succeed(client1, f.OrgA, job.ID, JobResult{}); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, call(http.MethodGet, "/api/v1/imports/"+job.ID, ""), http.StatusNotFound, "")
	wantProblem(t, call(http.MethodGet, "/api/v1/imports/"+job.ID+"/report", ""), http.StatusNotFound, "")
	wantProblem(t, call(http.MethodGet, "/api/v1/imports/"+job.ID+"/errors", ""), http.StatusNotFound, "")

	// The own document runs through under the scope of client 1.
	w := call(http.MethodPost, "/api/v1/imports", startBody(ownDoc, profile.ID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &accepted)
	deadline := time.Now().Add(10 * time.Second)
	for {
		done, gerr := jobSvc.Get(context.Background(), f.OrgA, accepted.JobID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if done.Status.Terminal() {
			if done.Status != jobs.StatusSucceeded {
				t.Fatalf("own import: %s %s", done.Status, done.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("own import did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w = call(http.MethodGet, "/api/v1/imports/"+accepted.JobID+"/report", ""); w.Code != http.StatusOK {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodGet, "/api/v1/imports/"+accepted.JobID+"/errors", ""); w.Code != http.StatusOK {
		t.Fatalf("errors: %d %s", w.Code, w.Body.String())
	}
}
