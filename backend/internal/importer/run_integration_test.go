package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// pgImport is an import stack on PostgreSQL (TEST_DATABASE_URL; skipped
// without it): profiles, documents, jobs, locations and the event outbox
// of the application role, the location writer and a file blob store.
type pgImport struct {
	f       *scopetest.Fixture
	user    string
	jobs    *jobs.Service
	h       *ImportHandler
	mux     chi.Router
	files   blob.Store
	docs    *document.PGRepository
	locs    *locations.PGRepository
	profile *PGRepository
	role    string
}

func newPGImport(t *testing.T) *pgImport {
	t.Helper()
	f := scopetest.Seed(t, "05")
	s := &pgImport{f: f, user: f.AppUser(t, f.OrgA, "import-run"), files: blob.NewFileStore(t.TempDir()),
		docs: document.NewPGRepository(f.App), locs: locations.NewPGRepository(f.App), profile: NewPGRepository(f.App)}
	s.jobs = jobs.NewService(jobs.NewPGRepository(f.App))
	s.jobs.Register(jobs.Kind{Name: JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true})
	// The run reloads the starter like the router wires it: user status
	// and role assignments; the import user holds an org-wide role with
	// import:run and location:write.
	s.role = f.ID()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO role (id, organization_id, name, permissions)
		VALUES ($1, $2, 'import-run', '["import:run","location:write"]')`, s.role, f.OrgA); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`,
		f.OrgA, s.user, s.role); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	s.h = NewImportHandler(s.profile, s.docs, s.files).WithJobs(s.jobs).
		WithStarters(user.NewPGRepository(f.App), permission.NewPGRepository(f.App)).
		WithWriter(NewLocationWriter(s.locs)).WithEvents(events.NewPGEmitter(f.App))
	s.mux = chi.NewRouter()
	s.h.RegisterRoutes(s.mux)
	jobs.NewAPIHandler(s.jobs).WithKind(JobKind, identity.PermImportRun, nil).RegisterRoutes(s.mux)
	t.Cleanup(func() { waitIdle(t) })
	return s
}

// ctx is the request context of the import user; clients restricts it.
func (s *pgImport) ctx(clients ...string) context.Context {
	scope := database.OrgWideScope(s.f.OrgA, s.user)
	if len(clients) > 0 {
		scope.Clients = database.ScopeIDs(clients...)
	}
	return requestContext(s.f.OrgA, s.user, &scope)
}

// locationProfile stores a profile of locations: Art (enum_map onto the
// kind), Pfad and Mandant, with the client of the fixture as default.
func (s *pgImport) locationProfile(t *testing.T, name string) string {
	t.Helper()
	p := &Profile{OrganizationID: s.f.OrgA, Name: name, Entity: "location", Format: FormatCSV, Mode: "both",
		Mapping: Mapping{
			Columns: map[string]ColumnMapping{
				"Art": {Target: LocationKindTarget, Transforms: []Transform{{Op: TransformTrim}, {Op: TransformEnumMap,
					Values: map[string]string{"Standort": "site", "Gebäude": "building", "Raum": "room", "Lager": "warehouse"}}}},
				"Pfad":    {Target: LocationPathTarget},
				"Mandant": {Target: LocationClientTarget, Transforms: []Transform{{Op: TransformTrim}}},
			},
			Defaults: map[string]string{LocationClientTarget: s.f.Client1},
		}, IdentityKeys: []string{LocationPathTarget}}
	if err := s.profile.Create(s.f.OrgCtx(s.f.OrgA), p); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return p.ID
}

// document stores an org-wide document whose clean version 1 holds content.
func (s *pgImport) document(t *testing.T, content string) string {
	t.Helper()
	ctx := s.f.OrgCtx(s.f.OrgA)
	d := &document.Document{OrganizationID: s.f.OrgA, Title: "import", FileName: "import.csv", UploadedBy: s.user, Category: "general"}
	if err := s.docs.Create(ctx, d); err != nil {
		t.Fatalf("document: %v", err)
	}
	versionID := s.f.ID()
	key := blob.OrgKey(s.f.OrgA, "documents", d.ID, versionID)
	if _, err := s.docs.CreateVersion(ctx, &document.Version{ID: versionID, OrganizationID: s.f.OrgA, DocumentID: d.ID,
		StorageKey: key, Size: int64(len(content)), MimeType: "text/csv", UploadedBy: s.user}, nil); err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := s.docs.SetScanResult(ctx, s.f.OrgA, d.ID, 1, document.ScanClean, ""); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := s.files.Put(context.Background(), document.Bucket, key, strings.NewReader(content), int64(len(content)), "text/csv"); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func (s *pgImport) call(t *testing.T, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
	return w
}

// start starts an import with ctx and returns its job id.
func (s *pgImport) start(t *testing.T, ctx context.Context, docID, profileID string, dryRun bool) string {
	t.Helper()
	w := s.call(t, ctx, http.MethodPost, "/api/v1/imports", fmt.Sprintf(`{"document_id":%q,"profile_id":%q,"dry_run":%t}`, docID, profileID, dryRun))
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted.JobID
}

// wait waits until the job ended.
func (s *pgImport) wait(t *testing.T, jobID string) *jobs.Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		job, err := s.jobs.Get(s.f.OrgCtx(s.f.OrgA), s.f.OrgA, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status.Terminal() {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s", jobID, job.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func resultOf(t *testing.T, job *jobs.Job) JobResult {
	t.Helper()
	var r JobResult
	if err := json.Unmarshal(job.Result, &r); err != nil {
		t.Fatalf("result %s: %v", job.Result, err)
	}
	return r
}

// report reads the report of a job from the blob store.
func (s *pgImport) report(t *testing.T, jobID string) string {
	t.Helper()
	rc, err := s.files.Get(context.Background(), export.ExportBucket, ReportKey(s.f.OrgA, jobID))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// locationCount counts the locations of organization A.
func (s *pgImport) locationCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM location WHERE organization_id = $1`, s.f.OrgA).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// completed returns the data of the import.completed events of a job.
func (s *pgImport) completed(t *testing.T, jobID string) []events.ImportCompletedData {
	t.Helper()
	rows, err := s.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = 'import.completed' AND entity_id = $2 ORDER BY sequence`, s.f.OrgA, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.ImportCompletedData
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d events.ImportCompletedData
		if err = json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestImportRunPartialFailureDryRunAuditAndEvent covers IMP-IO-01 and
// JOB-01 (WP-775): the dry run writes nothing and reports what the real
// run then does row by row; the real run writes the valid rows and reports
// the rest (partial failure); every location it creates is audited with
// source import, the starter as actor and the job as correlation id; both
// runs report import.completed with their counters.
func TestImportRunPartialFailureDryRunAuditAndEvent(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "locations")
	content := strings.Join([]string{
		"Art;Pfad;Mandant",
		"Standort;Munich;",
		"Gebäude;Munich / HQ;",
		"Raum;Munich/HQ/1.01;",
		"Raum;Munich/HQ;",             // a room needs site/building/room
		"Keller;Munich/HQ/K1;",        // no entry in enum_map
		"Lager;Munich/Depot;",         // a warehouse below the site
		"Standort;Hamburg;not-a-uuid", // client of the column
	}, "\n") + "\n"
	docID := s.document(t, content)

	dry := s.start(t, s.ctx(), docID, profileID, true)
	dryJob := s.wait(t, dry)
	if dryJob.Status != jobs.StatusSucceeded {
		t.Fatalf("dry run: %s %s", dryJob.Status, dryJob.Error)
	}
	want := JobResult{ReportKey: ReportKey(s.f.OrgA, dry), DryRun: true, Total: 7, Created: 4, Failed: 3}
	if got := resultOf(t, dryJob); got != want {
		t.Fatalf("dry run result %+v, want %+v", got, want)
	}
	if n := s.locationCount(t); n != 0 {
		t.Fatalf("the dry run wrote %d locations", n)
	}
	dryReport := s.report(t, dry)

	real := s.start(t, s.ctx(), docID, profileID, false)
	realJob := s.wait(t, real)
	if realJob.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %s %s", realJob.Status, realJob.Error)
	}
	want.ReportKey, want.DryRun = ReportKey(s.f.OrgA, real), false
	if got := resultOf(t, realJob); got != want {
		t.Fatalf("result %+v, want %+v", got, want)
	}
	if got := s.report(t, real); got != dryReport {
		t.Fatalf("the dry run reported\n%s\nthe run\n%s", dryReport, got)
	}
	for _, line := range []string{"5;path;invalid_value;", "6;kind;transform_failed;", "7;;created;warehouse Munich/Depot",
		"8;client_id;invalid_value;client_id must be a UUID"} {
		if !strings.Contains(dryReport, line) {
			t.Errorf("report lacks %q:\n%s", line, dryReport)
		}
	}
	// Site, building, room and warehouse.
	if n := s.locationCount(t); n != 4 {
		t.Fatalf("%d locations written, want 4", n)
	}

	// Audit (AUD-07): source import, the starter as actor, the job as
	// correlation id.
	var audited, other int
	if err := s.f.Admin.QueryRow(context.Background(), `SELECT
			count(*) FILTER (WHERE source = 'import' AND actor_id = $2::uuid AND correlation_id = $3),
			count(*) FILTER (WHERE NOT (source = 'import' AND actor_id = $2::uuid AND correlation_id = $3))
		FROM audit_log WHERE organization_id = $1 AND action = 'location.created'`, s.f.OrgA, s.user, real).Scan(&audited, &other); err != nil {
		t.Fatal(err)
	}
	if audited != 4 || other != 0 {
		t.Fatalf("audit: %d entries of the import, %d others", audited, other)
	}

	// import.completed, also for the dry run.
	for _, c := range []struct {
		job string
		dry bool
	}{{dry, true}, {real, false}} {
		got := s.completed(t, c.job)
		wantData := events.ImportCompletedData{ID: c.job, ProfileID: profileID, Entity: "location", Mode: "both", DryRun: c.dry,
			Total: 7, Created: 4, Failed: 3, InitiatedBy: s.user}
		if len(got) != 1 || got[0] != wantData {
			t.Fatalf("import.completed of %s: %+v, want %+v", c.job, got, wantData)
		}
	}

	// A second run finds every location: skipped, nothing written.
	again := s.start(t, s.ctx(), docID, profileID, false)
	if got := resultOf(t, s.wait(t, again)); got.Skipped != 4 || got.Created != 0 || got.Failed != 3 {
		t.Fatalf("second run %+v", got)
	}
	if n := s.locationCount(t); n != 4 {
		t.Fatalf("%d locations after the second run", n)
	}
}

// TestImportRunScopeAndAmbiguousPath: a starter restricted to client 1
// creates no site of client 2 (out_of_scope); a path whose level matches
// two locations without regard to case is ambiguous_location.
func TestImportRunScopeAndAmbiguousPath(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "scoped")
	ctx := context.Background()
	var siteID string
	if err := s.f.Admin.QueryRow(ctx, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'Twin Site') RETURNING id::text`,
		s.f.OrgA, s.f.Client1).Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.f.Admin.Exec(ctx, `INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, 'Twin'), ($1, $2, 'TWIN')`,
		s.f.OrgA, siteID); err != nil {
		t.Fatal(err)
	}
	content := "Art;Pfad;Mandant\n" +
		"Standort;Foreign;" + s.f.Client2 + "\n" +
		"Standort;Own;" + s.f.Client1 + "\n" +
		"Raum;twin site/twin/R1;\n"
	docID := s.document(t, content)
	jobID := s.start(t, s.ctx(s.f.Client1), docID, profileID, false)
	job := s.wait(t, jobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("run: %s %s", job.Status, job.Error)
	}
	if got := resultOf(t, job); got.Total != 3 || got.Created != 1 || got.Failed != 2 {
		t.Fatalf("result %+v", got)
	}
	rep := s.report(t, jobID)
	for _, line := range []string{"2;client_id;out_of_scope;", "3;;created;site Own", "4;path;ambiguous_location;"} {
		if !strings.Contains(rep, line) {
			t.Errorf("report lacks %q:\n%s", line, rep)
		}
	}
	var foreign int
	if err := s.f.Admin.QueryRow(ctx, `SELECT count(*) FROM site WHERE organization_id = $1 AND client_id = $2`, s.f.OrgA, s.f.Client2).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if foreign != 0 {
		t.Fatalf("%d sites of client 2 written", foreign)
	}
}

// blockingWriter holds the row at line until release is closed, after it
// signalled reached.
type blockingWriter struct {
	Writer
	line    int
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	if row.Line == w.line {
		w.once.Do(func() { close(w.reached) })
		<-w.release
	}
	return w.Writer.Write(ctx, row)
}

// warehouses is a location file of n warehouses below one site.
func warehouses(n int) string {
	var b strings.Builder
	b.WriteString("Art;Pfad;Mandant\n")
	for i := range n {
		fmt.Fprintf(&b, "Lager;Depot/W%05d;\n", i)
	}
	return b.String()
}

// TestImportRunCancelBetweenRowsAndNoRetry: DELETE /api/v1/jobs/{id} ends
// the run before its next row; the rows written so far stay with their
// report and counters. A failed run is not repeated (MaxAttempts 1): a
// redelivery of its message writes nothing.
func TestImportRunCancelBetweenRowsAndNoRetry(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "cancel")
	docID := s.document(t, warehouses(150))
	bw := &blockingWriter{Writer: NewLocationWriter(s.locs), line: rowProgressEvery + 1,
		reached: make(chan struct{}), release: make(chan struct{})}
	s.h.WithWriter(bw)
	jobID := s.start(t, s.ctx(), docID, profileID, false)
	select {
	case <-bw.reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not reach the held row")
	}
	// Another holder of import:run cancels the running import.
	admin := requestContext(s.f.OrgA, s.f.User, nil)
	if w := s.call(t, admin, http.MethodDelete, "/api/v1/jobs/"+jobID, ""); w.Code != http.StatusOK {
		close(bw.release)
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	close(bw.release)
	waitIdle(t)
	job := s.wait(t, jobID)
	got := resultOf(t, job)
	// The held row was the 100th data row: it ends, the progress report
	// after it notices the cancel.
	if job.Status != jobs.StatusCancelled || got.Total != rowProgressEvery || got.Created != rowProgressEvery {
		t.Fatalf("cancelled job %s %+v", job.Status, got)
	}
	// Site Depot and 100 warehouses; no row after the cancel.
	if n := s.locationCount(t); n != rowProgressEvery+1 {
		t.Fatalf("%d locations after the cancel", n)
	}
	if rep := s.report(t, jobID); strings.Count(rep, ";created;") != rowProgressEvery {
		t.Fatalf("report of the cancelled run:\n%s", rep)
	}
	if len(s.completed(t, jobID)) != 0 {
		t.Fatal("a cancelled run reported import.completed")
	}

	// No second attempt: a run that fails on the job queue stays failed
	// and a redelivery writes nothing.
	failing := &finishFails{fakeWriter: fakeWriter{entity: "location"}}
	s.h.WithWriter(failing)
	queue := jobs.NewQueue(s.jobs, nil)
	queue.Handle(jobs.Kind{Name: JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}, s.h.RunJob)
	s.h.WithQueue(queue)
	defer s.h.WithQueue(nil)
	failedID := s.start(t, s.ctx(), docID, profileID, false)
	data, _ := json.Marshal(jobs.Message{OrganizationID: s.f.OrgA, JobID: failedID, Kind: JobKind})
	for range 2 {
		queue.Process(context.Background(), &memDelivery{data: data})
	}
	failed := s.wait(t, failedID)
	if failed.Status != jobs.StatusFailed || failed.Attempts != 1 || failed.MaxAttempts != 1 || errorCode(failed.Error) != "import-failed" {
		t.Fatalf("failed job %+v", failed)
	}
	if n := failing.written(); n != 150 {
		t.Fatalf("the writer got %d rows, want 150 (no second attempt)", n)
	}
}

// finishFails is a writer whose Finish fails: the run fails after its
// rows.
type finishFails struct {
	fakeWriter
}

func (w *finishFails) Finish(context.Context, *RunInfo) error {
	return fmt.Errorf("finish failed")
}

// TestImportRunWithoutSnapshotFails: a job whose row holds no scope
// snapshot of its starter fails without reading (fail-closed, E-08).
func TestImportRunWithoutSnapshotFails(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "snapshot")
	docID := s.document(t, warehouses(2))
	p := JobParams{DocumentID: docID, DocumentVersion: 1, ProfileID: profileID, Mode: "both",
		ScopeParams: jobs.ScopeParams{Source: audit.SourceUI}}
	// Queued outside a request: no snapshot.
	job, _, err := s.jobs.Enqueue(context.Background(), s.f.OrgA, JobKind, p, jobs.EnqueueOptions{CreatedBy: s.user})
	if err != nil {
		t.Fatal(err)
	}
	if job.Scope != nil {
		t.Fatalf("job with snapshot %+v", job.Scope)
	}
	s.h.run(context.Background(), s.f.OrgA, job.ID)
	done := s.wait(t, job.ID)
	if done.Status != jobs.StatusFailed || errorCode(done.Error) != CodePrincipalMissing {
		t.Fatalf("job %s %q", done.Status, done.Error)
	}
	if n := s.locationCount(t); n != 0 {
		t.Fatalf("%d locations written", n)
	}
}

// TestImportRunGatesAndRowLimitWithoutWrite: a module switched off, a
// feature not entitled and more than 50000 rows end the run before its
// first write.
func TestImportRunGatesAndRowLimitWithoutWrite(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "gates")
	small := s.document(t, warehouses(3))
	for _, tc := range []struct {
		name  string
		gates func()
		doc   string
		code  string
	}{
		{"module", func() { s.h.WithGates(denyRoutes{}, nil) }, small, CodeModuleDisabled},
		{"feature", func() { s.h.WithGates(nil, denyFeature{key: "import"}) }, small, CodeFeatureNotEntitled},
		{"rows", func() { s.h.WithGates(nil, nil) }, s.document(t, warehouses(MaxRows+1)), CodeRowLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.gates()
			jobID := s.start(t, s.ctx(), tc.doc, profileID, false)
			job := s.wait(t, jobID)
			if job.Status != jobs.StatusFailed || errorCode(job.Error) != tc.code {
				t.Fatalf("job %s %q, want %s", job.Status, job.Error, tc.code)
			}
			if n := s.locationCount(t); n != 0 {
				t.Fatalf("%d locations written", n)
			}
			if rep := s.report(t, jobID); !strings.Contains(rep, ";"+tc.code+";") {
				t.Fatalf("report %q", rep)
			}
		})
	}
}

// TestImportRunOnJobQueue: with a job queue the start submits the job and
// a worker runs it with the handler of import.run (TEC-14): without NATS
// through Queue.Process, with TEST_NATS_URL through JetStream.
func TestImportRunOnJobQueue(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "queue")
	docID := s.document(t, warehouses(5))
	queue := jobs.NewQueue(s.jobs, nil)
	kind := jobs.Kind{Name: JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}
	queue.Handle(kind, s.h.RunJob)
	s.h.WithQueue(queue)
	jobID := s.start(t, s.ctx(), docID, profileID, false)
	time.Sleep(50 * time.Millisecond)
	if job, _ := s.jobs.Get(s.f.OrgCtx(s.f.OrgA), s.f.OrgA, jobID); job.Status != jobs.StatusQueued {
		t.Fatalf("submitted job %s, want queued for the queue", job.Status)
	}
	data, _ := json.Marshal(jobs.Message{OrganizationID: s.f.OrgA, JobID: jobID, Kind: JobKind})
	d := &memDelivery{data: data}
	if out := queue.Process(context.Background(), d); !out.Ran || !d.acked {
		t.Fatalf("process %+v", out)
	}
	job := s.wait(t, jobID)
	if got := resultOf(t, job); job.Status != jobs.StatusSucceeded || got.Created != 5 || got.Total != 5 {
		t.Fatalf("queued run %s %+v", job.Status, got)
	}
	if len(s.completed(t, jobID)) != 1 {
		t.Fatal("no import.completed of the queued run")
	}

	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Log("TEST_NATS_URL not set; the run over JetStream is skipped")
		return
	}
	nc, err := nats.Connect(url, nats.Name("wp775-import"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	natsQueue := jobs.NewQueue(s.jobs, js)
	natsQueue.Stream, natsQueue.SubjectPrefix = "JOBS_WP775_"+suffix, "wp775_"+suffix+"."
	if err = natsQueue.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	natsQueue.Handle(kind, s.h.RunJob)
	runCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go natsQueue.Run(runCtx)
	s.h.WithQueue(natsQueue)
	natsDoc := s.document(t, strings.ReplaceAll(warehouses(3), "Depot", "Nats"))
	natsJob := s.wait(t, s.start(t, s.ctx(), natsDoc, profileID, false))
	if got := resultOf(t, natsJob); natsJob.Status != jobs.StatusSucceeded || got.Created != 3 {
		t.Fatalf("run over JetStream %s %+v", natsJob.Status, got)
	}
}

// TestImportJobHidesStarterRightsAndReloadsThem covers the job parameters
// of WP-775: GET /api/v1/jobs/{id} of an import job shows no rights,
// scopes or groups of its starter, and the run reloads the starter's
// current role assignments. A write right revoked after the start ends the
// run with import-forbidden before its first row; a deactivated starter
// ends it with import-principal-missing.
func TestImportJobHidesStarterRightsAndReloadsThem(t *testing.T) {
	s := newPGImport(t)
	profileID := s.locationProfile(t, "reload")
	queue := jobs.NewQueue(s.jobs, nil)
	queue.Handle(jobs.Kind{Name: JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}, s.h.RunJob)
	s.h.WithQueue(queue)
	process := func(jobID string) *jobs.Job {
		t.Helper()
		data, _ := json.Marshal(jobs.Message{OrganizationID: s.f.OrgA, JobID: jobID, Kind: JobKind})
		queue.Process(context.Background(), &memDelivery{data: data})
		return s.wait(t, jobID)
	}

	jobID := s.start(t, s.ctx(), s.document(t, warehouses(2)), profileID, false)
	w := s.call(t, s.ctx(), http.MethodGet, "/api/v1/jobs/"+jobID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("job: %d %s", w.Code, w.Body.String())
	}
	var view struct {
		CreatedBy string                     `json:"created_by"`
		Params    map[string]json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.Params == nil {
		t.Fatalf("job %s (%v)", w.Body.String(), err)
	}
	for _, key := range []string{"principal", "permissions", "groups", "scope", "permission_scopes"} {
		if _, ok := view.Params[key]; ok {
			t.Fatalf("the job shows %s of its starter: %s", key, w.Body.String())
		}
	}
	if body := w.Body.String(); strings.Contains(body, `"permissions"`) || strings.Contains(body, `"groups"`) ||
		strings.Contains(body, string(identity.PermLocationWrite)) {
		t.Fatalf("the job shows rights of its starter: %s", body)
	}
	if view.CreatedBy != s.user {
		t.Fatalf("created_by %q", view.CreatedBy)
	}
	if done := process(jobID); done.Status != jobs.StatusSucceeded || resultOf(t, done).Created != 2 {
		t.Fatalf("run with the right %s %q", done.Status, done.Error)
	}

	// Revoked after the start: the role loses location:write.
	revoked := s.start(t, s.ctx(), s.document(t, strings.ReplaceAll(warehouses(2), "Depot", "Revoked")), profileID, false)
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE role SET permissions = '["import:run"]' WHERE id = $1`, s.role); err != nil {
		t.Fatal(err)
	}
	before := s.locationCount(t)
	if done := process(revoked); done.Status != jobs.StatusFailed || errorCode(done.Error) != CodeForbidden {
		t.Fatalf("run after the revocation %s %q, want failed with %s", done.Status, done.Error, CodeForbidden)
	}
	if got := s.report(t, revoked); !strings.Contains(got, ";"+CodeForbidden+";") {
		t.Fatalf("report %q", got)
	}

	// Deactivated after the start.
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE role SET permissions = '["import:run","location:write"]' WHERE id = $1`, s.role); err != nil {
		t.Fatal(err)
	}
	inactive := s.start(t, s.ctx(), s.document(t, strings.ReplaceAll(warehouses(2), "Depot", "Inactive")), profileID, false)
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE app_user SET is_active = false WHERE id = $1`, s.user); err != nil {
		t.Fatal(err)
	}
	if done := process(inactive); done.Status != jobs.StatusFailed || errorCode(done.Error) != CodePrincipalMissing {
		t.Fatalf("run of a deactivated starter %s %q, want failed with %s", done.Status, done.Error, CodePrincipalMissing)
	}
	if n := s.locationCount(t); n != before {
		t.Fatalf("%d locations written without the right", n-before)
	}
}
