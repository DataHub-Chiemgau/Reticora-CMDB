package integrity_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// The automated writers of phase 2 after ABN-03 (WP-826) against
// PostgreSQL (TEST_DATABASE_URL, scopetest.Seed):
//
//	cd backend && go test -count=1 -p 1 -run 'TestPhase2AutomatedWriters' ./internal/tenant/integrity/
//
// The writers run as the server wires them: the import (job import.run
// over a document and POST /api/v1/imports with rows, the CI writer of
// WP-302 with the identity locks of the ingest), the template migration
// (job template.migrate over POST /api/v1/templates/{id}/migrations,
// WP-712), the system job attribute.computed_refresh (WP-718), the bulk
// set_fields over POST /api/v1/cis/bulk (WP-304, WP-306), the HTTP ingest
// with four lanes and the serial link of WP-741, and
// discovery.Handler.AdoptUnlicensed with the max_cis admission of WP-835.
//
// Cases 1 to 4 run twice. First in the deterministic form: the automated
// writer has its value (the file, the request, the plan) before the manual
// change commits, and writes after it. The manual change runs in a
// transaction the test keeps open (heldChange) until the writer waits for
// a lock of it (pg_stat_activity, wait_event_type Lock); the computed
// refresh skips a locked CI (SKIP LOCKED) and ends while the change is
// open. Second with real parallel transactions over several rounds.
// Cases 5 to 7 run the writers first one after the other, then in
// parallel. After every case scopetest.IntegrityViolations is empty for
// the fixture organization, no request answers 5xx and every job ends
// succeeded (a 40001 or 40P01 appears at most as 409 or as a reported row).

// writerLimit is a settable max_cis of the import, the ingest and the
// adoption (ci.LimitGuard, discovery.QuotaGuard).
type writerLimit struct{ n atomic.Int64 }

func (l *writerLimit) AllowCreate(_ context.Context, _, feature string, current int64) error {
	if n := l.n.Load(); current >= n {
		return &entitlement.LimitExceededError{FeatureKey: feature, Limit: n, Current: current}
	}
	return nil
}

// ingestProvenance is the provenance of the ingest over the override
// repository, as the router wires it.
type ingestProvenance struct{ repo *override.PGRepository }

func (p ingestProvenance) RecordDiscovered(ctx context.Context, orgID, ciID, field string, value any, source string,
	at time.Time) (*discovery.FieldProvenance, error) {
	fv, err := p.repo.RecordDiscovered(ctx, orgID, ciID, field, value, source, at)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (p ingestProvenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, p.repo, w)
}

// unrestrictedCreator is the field policy of the creator of a bulk job.
func unrestrictedCreator(context.Context, *jobs.Job) (*permission.FieldPolicy, error) {
	return permission.UnrestrictedPolicy(identity.AllPermissions()), nil
}

// writers is the stack of the cases on organization A.
type writers struct {
	t         *testing.T
	f         *scopetest.Fixture
	editor    string // the person of the manual changes
	bulker    string // the caller of the bulk
	starter   string // the starter of the imports
	limit     *writerLimit
	types     *citype.PGRepository
	cis       *ci.PGRepository
	svc       *ci.Service
	jobs      *jobs.Service
	files     blob.Store
	staging   string
	docs      *document.PGRepository
	profiles  *importer.PGRepository
	mux       *chi.Mux
	instances []*chi.Mux
	adopter   *discovery.Handler
	refresh   *ci.ComputedRefresh
	server    string
}

func newWriters(t *testing.T, f *scopetest.Fixture) *writers {
	t.Helper()
	bg := context.Background()
	w := &writers{t: t, f: f, editor: f.AppUser(t, f.OrgA, "wp826-editor"), bulker: f.AppUser(t, f.OrgA, "wp826-bulk"),
		starter: f.AppUser(t, f.OrgA, "wp826-import"), limit: &writerLimit{}, staging: t.TempDir()}
	w.limit.n.Store(1 << 40)
	// The import reloads its starter (WithStarters): an org-wide role with
	// the rights of a CI import.
	role := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions)
		VALUES ($1, $2, 'wp826-import', '["import:run","ci:write","ci:read","location:write"]')`, role, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`,
		f.OrgA, w.starter, role); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&w.server); err != nil {
		t.Fatal(err)
	}

	w.types = citype.NewPGRepository(f.App)
	w.cis = ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()).WithComputed(w.types)
	ovr := override.NewPGRepository(f.App)
	w.jobs = jobs.NewService(jobs.NewPGRepository(f.App))
	w.jobs.Register(jobs.Kind{Name: importer.JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true})
	w.jobs.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	w.jobs.Register(jobs.Kind{Name: ci.TemplateMigrateJobKind, MaxAttempts: 3, Timeout: time.Hour, Cancellable: true})
	w.svc = ci.NewServiceWithLimits(w.cis, w.limit).WithFieldResolver(w.types).WithBulkJobs(w.jobs).
		WithCreatorPolicy(unrestrictedCreator)

	// An ingest instance: four lanes (identity and max_cis locks), the
	// provenance, the instance attributes and the serial link of WP-741.
	instance := func() *discovery.Handler {
		return discovery.NewHandler(discovery.NewPGRepository(f.App), w.cis).WithProvenance(ingestProvenance{repo: ovr}).
			WithInstanceFields(w.types).WithAssetLinks(asset.NewPGRepository(f.App)).WithLimits(w.limit).WithIngestLanes(4)
	}
	for range 2 {
		mux := chi.NewRouter()
		instance().RegisterRoutes(mux)
		w.instances = append(w.instances, mux)
	}
	w.adopter = instance()

	w.files = blob.NewFileStore(w.staging)
	w.docs = document.NewPGRepository(f.App)
	w.profiles = importer.NewPGRepository(f.App)
	locs := locations.NewPGRepository(f.App)
	policies := permission.NewFieldResolver(permission.NewPGRepository(f.App), nil, permission.NewPGFieldGrants(f.App), nil)
	writer := importer.NewCIWriter(w.svc, instance(), ovr, policies).WithInstanceFields(w.types).WithFields(w.types).
		WithLocations(locs)
	imports := importer.NewImportHandler(w.profiles, w.docs, w.files).WithJobs(w.jobs).
		WithStarters(user.NewPGRepository(f.App), permission.NewPGRepository(f.App)).WithWriter(writer)

	w.mux = chi.NewRouter()
	imports.RegisterRoutes(w.mux)
	ci.NewHandler(w.svc).RegisterRoutes(w.mux)
	ci.NewTemplateMigrationJobs(w.cis, w.types).WithJobs(w.jobs).RegisterRoutes(w.mux)
	w.refresh = ci.NewComputedRefresh(f.App, w.types)
	// The runs of the jobs end before the fixture goes.
	t.Cleanup(w.idle)
	return w
}

// as is the request context of an org-wide principal of organization A
// with every right.
func (w *writers) as(userID string) context.Context {
	scope := database.OrgWideScope(w.f.OrgA, userID)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: userID})
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp826", Source: audit.SourceUI})
	perms := identity.AllPermissions()
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: userID, OrganizationID: w.f.OrgA,
		Type: identity.PrincipalTypeUser, Permissions: perms})
	return permission.WithPolicy(ctx, permission.UnrestrictedPolicy(perms))
}

// call sends a request to h; no answer may be 5xx.
func (w *writers) call(h http.Handler, ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			w.t.Error(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= http.StatusInternalServerError {
		w.t.Errorf("%s %s: %d %s; no request may end with 5xx", method, path, rec.Code, rec.Body.String())
	}
	return rec
}

func (w *writers) scalar(query string, args ...any) string {
	w.t.Helper()
	var s string
	if err := w.f.Admin.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return s
}

func (w *writers) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// field is a typed column or attribute of a CI as text ("" for none).
func (w *writers) field(id, field string) string {
	w.t.Helper()
	if strings.HasPrefix(field, "attributes.") {
		return w.scalar(`SELECT COALESCE(attributes->>$2, '') FROM ci WHERE id = $1`, id, strings.TrimPrefix(field, "attributes."))
	}
	return w.scalar(`SELECT COALESCE(`+field+`::text, '') FROM ci WHERE id = $1`, id)
}

// override is the value and the author of the override of a field ("" for
// none).
func (w *writers) override(id, field string) (value, author string) {
	w.t.Helper()
	err := w.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(override_value #>> '{}', ''), COALESCE(override_author::text, '')
		FROM ci_field_value WHERE ci_id = $1 AND field_name = $2 AND override_at IS NOT NULL`, id, field).Scan(&value, &author)
	if err != nil {
		return "", ""
	}
	return value, author
}

// clean requires an empty integrity check for organization A.
func (w *writers) clean(when string) {
	w.t.Helper()
	if v := scopetest.IntegrityViolations(w.t, w.f.Admin, w.f.OrgA); len(v) > 0 {
		w.t.Fatalf("integrity violations after %s: %v", when, v)
	}
}

// active counts the live CIs of organization A.
func (w *writers) active() int {
	return w.count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, w.f.OrgA)
}

// together starts fns at once and waits for all of them.
func together(fns ...func()) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	close(start)
	wg.Wait()
}

// heldChange is a manual change in a transaction the test keeps open.
type heldChange struct {
	release chan struct{}
	once    sync.Once
	done    chan error
}

// end lets the transaction of the held change end.
func (h *heldChange) end() { h.once.Do(func() { close(h.release) }) }

// hold runs change in a request transaction of ctx (the services join it,
// database.ContextWithTx) and keeps the transaction open, with its locks,
// until commit; a test that fails before ends it at its cleanup.
func (w *writers) hold(ctx context.Context, change func(ctx context.Context) error) *heldChange {
	w.t.Helper()
	h := &heldChange{release: make(chan struct{}), done: make(chan error, 1)}
	ready := make(chan error, 1)
	go func() {
		h.done <- database.WithRequestTenantTx(ctx, w.f.App, w.f.OrgA, func(txCtx context.Context) error {
			err := change(txCtx)
			ready <- err
			if err != nil {
				return err
			}
			<-h.release
			return nil
		})
	}()
	select {
	case err := <-ready:
		if err != nil {
			w.t.Fatalf("held manual change: %v", err)
		}
	case err := <-h.done:
		w.t.Fatalf("held manual change ended before its change: %v", err)
	}
	w.t.Cleanup(h.end)
	return h
}

// commit commits the held change.
func (h *heldChange) commit(t *testing.T) {
	t.Helper()
	h.end()
	if err := <-h.done; err != nil {
		t.Fatalf("commit of the held manual change: %v", err)
	}
}

// awaitLockWaiter waits until a backend of the database waits for a lock:
// the automated writer reached a lock of the held change.
func (w *writers) awaitLockWaiter(what string) {
	w.t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if w.count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) > 0 {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("%s did not wait for a lock of the held manual change", what)
		}
	}
}

// manual is the manual change of the editor (PATCH /cis/{id}): every
// changed field gets a protected override.
func (w *writers) manual(ctx context.Context, id string, req ci.UpdateRequest) error {
	req.Manual = &ci.ManualChange{Author: w.editor}
	req.ChangeReason = "wp826"
	_, err := w.svc.Update(ctx, w.f.OrgA, id, req)
	return err
}

// seedCI inserts a CI of the server type observed by SNMP.
func (w *writers) seedCI(name, serial, model string) string {
	w.t.Helper()
	id := w.f.ID()
	if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, ci_type_id, name, status, discovery_source,
			serial_number, model) VALUES ($1, $2, $3, $4, 'active', 'snmp', $5, $6)`, id, w.f.OrgA, w.server, name, serial, model); err != nil {
		w.t.Fatalf("seed %s: %v", name, err)
	}
	return id
}

// profile stores a CI profile of format that maps every target onto itself.
func (w *writers) profile(format string, targets []string) string {
	w.t.Helper()
	cols := map[string]importer.ColumnMapping{}
	for _, target := range targets {
		cols[target] = importer.ColumnMapping{Target: target}
	}
	p := &importer.Profile{OrganizationID: w.f.OrgA, Name: "wp826-" + w.f.ID(), Entity: "ci", Format: format, Mode: "both",
		Mapping: importer.Mapping{Columns: cols}}
	if err := w.profiles.Create(w.f.OrgCtx(w.f.OrgA), p); err != nil {
		w.t.Fatalf("profile: %v", err)
	}
	return p.ID
}

// startImport starts the import of rows (the first row names the targets)
// as the starter: as a CSV document (job import.run over a file) or, with
// api, as JSON rows of POST /api/v1/imports. It returns the job id.
func (w *writers) startImport(api bool, rows ...[]string) string {
	w.t.Helper()
	targets := rows[0]
	ctx := w.as(w.starter)
	var body map[string]any
	if api {
		data := make([]map[string]any, 0, len(rows)-1)
		for _, row := range rows[1:] {
			r := map[string]any{}
			for i, v := range row {
				if v != "" {
					r[targets[i]] = v
				}
			}
			data = append(data, r)
		}
		body = map[string]any{"profile_id": w.profile(importer.FormatJSON, targets), "rows": data}
	} else {
		lines := make([]string, len(rows))
		for i, row := range rows {
			lines[i] = strings.Join(row, ";")
		}
		body = map[string]any{"document_id": w.document(strings.Join(lines, "\n") + "\n"),
			"profile_id": w.profile(importer.FormatCSV, targets)}
	}
	rec := w.call(w.mux, ctx, http.MethodPost, "/api/v1/imports", body)
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.JobID == "" {
		w.t.Fatalf("start import: %d %s", rec.Code, rec.Body.String())
	}
	return accepted.JobID
}

// document stores an org-wide CSV document whose clean version 1 holds
// content.
func (w *writers) document(content string) string {
	w.t.Helper()
	ctx := w.f.OrgCtx(w.f.OrgA)
	d := &document.Document{OrganizationID: w.f.OrgA, Title: "wp826", FileName: "wp826.csv", UploadedBy: w.starter, Category: "general"}
	if err := w.docs.Create(ctx, d); err != nil {
		w.t.Fatalf("document: %v", err)
	}
	versionID := w.f.ID()
	key := blob.OrgKey(w.f.OrgA, "documents", d.ID, versionID)
	if _, err := w.docs.CreateVersion(ctx, &document.Version{ID: versionID, OrganizationID: w.f.OrgA, DocumentID: d.ID,
		StorageKey: key, Size: int64(len(content)), MimeType: "text/csv", UploadedBy: w.starter}, nil); err != nil {
		w.t.Fatalf("version: %v", err)
	}
	if err := w.docs.SetScanResult(ctx, w.f.OrgA, d.ID, 1, document.ScanClean, ""); err != nil {
		w.t.Fatalf("scan: %v", err)
	}
	if err := w.files.Put(context.Background(), document.Bucket, key, strings.NewReader(content), int64(len(content)), "text/csv"); err != nil {
		w.t.Fatal(err)
	}
	return d.ID
}

// job waits until the job ended and requires it succeeded.
func (w *writers) job(id string) *jobs.Job {
	w.t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		job, err := w.jobs.Get(context.Background(), w.f.OrgA, id)
		if err != nil {
			w.t.Fatalf("job %s: %v", id, err)
		}
		if job.Status.Terminal() {
			if job.Status != jobs.StatusSucceeded {
				w.t.Fatalf("job %s (%s) ended %s: %s", id, job.Kind, job.Status, job.Error)
			}
			return job
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("job %s still %s", id, job.Status)
		}
	}
}

// importRun is an ended import: its counters and its report lines (line,
// field, code, message).
type importRun struct {
	result importer.JobResult
	report [][]string
	// first is the report line of the first row: 2 in a file (after its
	// header), 1 for JSON rows.
	first int
}

// has reports whether the report has a line of the row (0: the first)
// with code (and field, unless empty).
func (r *importRun) has(row int, field, code string) bool {
	line := r.first + row
	for _, l := range r.report {
		if len(l) >= 3 && l[0] == fmt.Sprint(line) && l[2] == code && (field == "" || l[1] == field) {
			return true
		}
	}
	return false
}

// finishImport waits for the import job and reads its result and report.
func (w *writers) finishImport(id string) importRun {
	w.t.Helper()
	job := w.job(id)
	var run importRun
	if err := json.Unmarshal(job.Result, &run.result); err != nil {
		w.t.Fatalf("import result %s: %v", job.Result, err)
	}
	rc, err := w.files.Get(context.Background(), export.ExportBucket, importer.ReportKey(w.f.OrgA, id))
	if err != nil {
		w.t.Fatalf("import report: %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		w.t.Fatal(err)
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), "\uFEFF")))
	r.Comma, r.FieldsPerRecord = ';', -1
	lines, err := r.ReadAll()
	if err != nil {
		w.t.Fatal(err)
	}
	if len(lines) > 0 {
		run.report = lines[1:]
	}
	return run
}

// runImport starts the import of rows and waits for it.
func (w *writers) runImport(api bool, rows ...[]string) importRun {
	w.t.Helper()
	run := w.finishImport(w.startImport(api, rows...))
	run.first = 2 - btoi(api)
	return run
}

// idle waits until no staged rows of an API import are left: a run drops
// them after it recorded its end.
func (w *writers) idle() {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		staged := 0
		_ = filepath.WalkDir(w.staging, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == "rows.json" && strings.Contains(filepath.ToSlash(path), "/imports/staging/") {
				staged++
			}
			return nil
		})
		if staged == 0 {
			return
		}
	}
}

// device is a server record of SNMP with serial number, MAC and management
// IP (each optional).
func device(serial, mac, ip string) plugins.DeviceRecord {
	return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "wp826-" + serial, IP: ip, MAC: mac,
		Serial: serial}, time.Now().Add(-time.Second))
}

// ingest posts records to an ingest instance (POST /api/v1/discovery/ingest)
// as the HTTP ingest of a collector; a refused record (the other writer won
// a unique key) is delivered again, as the queue would. It returns the
// answer of the first delivery.
func (w *writers) ingest(instance int, recs ...plugins.DeviceRecord) int {
	ctx := tenant.WithTenant(w.f.OrgCtx(w.f.OrgA), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User})
	body := plugins.DeviceRecordBatch{Records: recs}
	rec := w.call(w.instances[instance], ctx, http.MethodPost, "/api/v1/discovery/ingest", body)
	if rec.Code != http.StatusAccepted || strings.Contains(rec.Body.String(), `"failed":[{`) {
		if again := w.call(w.instances[instance], ctx, http.MethodPost, "/api/v1/discovery/ingest", body); again.Code != http.StatusAccepted {
			w.t.Errorf("ingest on instance %d: %d %s, redelivered %d %s", instance, rec.Code, rec.Body.String(), again.Code, again.Body.String())
		}
	}
	return rec.Code
}

// TestPhase2AutomatedWriters runs the automated writers of phase 2 against
// manual changes, the ingest and max_cis (ABN-03 phase 2, WP-826): (1) the
// import against a manual change of the same field, (2) template.migrate
// against a manual change, (3) attribute.computed_refresh against a manual
// change of an input of its expression, (4) bulk set_fields against a
// manual change of the same field, (5) import and ingest of one new device,
// (6) a double ingest with the serial link to an existing asset and (7)
// import, ingest over lanes and AdoptUnlicensed at a max_cis just above
// the stock.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPhase2AutomatedWriters(t *testing.T) {
	f := scopetest.Seed(t, "a9")
	w := newWriters(t, f)
	t.Run("1 import against a manual change", func(t *testing.T) { w.t = t; writersImport(w) })
	t.Run("2 template migration against a manual change", func(t *testing.T) { w.t = t; writersTemplate(w) })
	t.Run("3 computed refresh against a manual change of an input", func(t *testing.T) { w.t = t; writersComputed(w) })
	t.Run("4 bulk set_fields against a manual change", func(t *testing.T) { w.t = t; writersBulk(w) })
	t.Run("5 import and ingest of one new device", func(t *testing.T) { w.t = t; writersImportIngest(w) })
	t.Run("6 double ingest with the serial link", func(t *testing.T) { w.t = t; writersSerialLink(w) })
	t.Run("7 max_cis across import, ingest and adoption", func(t *testing.T) { w.t = t; writersMaxCIs(w) })
}

// writersImport is case 1 (REC-03, OVR-02): an import of the same field
// against a manual change keeps the manual value and its override, and
// the import reports the deviation as override_kept with an open
// override_conflict review.
func writersImport(w *writers) {
	t := w.t
	header := []string{"ci_type", "serial_number", "model"}
	const manualModel = "wp826-manual"
	conflicts := func(id string) int {
		return w.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict' AND status = 'open'
			AND object_key = $2`, w.f.OrgA, "ci:"+id+"/model")
	}
	kept := func(when, id string) {
		t.Helper()
		if got := w.field(id, "model"); got != manualModel {
			t.Fatalf("%s: model %q, want the manual value", when, got)
		}
		if value, author := w.override(id, "model"); value != manualModel || author != w.editor {
			t.Fatalf("%s: override %q of %q, want the manual value of the editor", when, value, author)
		}
	}
	conflicted := func(when, id string, run *importRun) {
		t.Helper()
		if !run.has(0, "model", importer.CodeOverrideKept) || run.result.Failed != 0 {
			t.Fatalf("%s: result %+v, report %v; want the row reported with model %s", when, run.result, run.report, importer.CodeOverrideKept)
		}
		if n := conflicts(id); n != 1 {
			t.Fatalf("%s: %d open override_conflict reviews of the field, want 1", when, n)
		}
	}

	// Deterministic: the import starts (its rows are fixed) while the
	// manual change is open and writes after its commit.
	for i, api := range []bool{false, true} {
		serial := fmt.Sprintf("WP826-IMP-D%d", i)
		id := w.seedCI("wp826-imp-d"+fmt.Sprint(i), serial, "snmp-model")
		held := w.hold(w.as(w.editor), func(ctx context.Context) error {
			m := manualModel
			return w.manual(ctx, id, ci.UpdateRequest{Model: &m})
		})
		var run importRun
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			run = w.runImport(api, header, []string{"server", serial, "wp826-import"})
		}()
		w.awaitLockWaiter("the import")
		held.commit(t)
		<-finished
		when := fmt.Sprintf("deterministic import (api %v)", api)
		kept(when, id)
		conflicted(when, id, &run)
	}

	// Parallel rounds, alternately over a file and over rows.
	before := 0
	for round := range 8 {
		api := round%2 == 1
		serial := fmt.Sprintf("WP826-IMP-P%d", round)
		id := w.seedCI(fmt.Sprintf("wp826-imp-p%d", round), serial, "snmp-model")
		var run importRun
		var editErr error
		together(
			func() {
				m := manualModel
				editErr = w.manual(w.as(w.editor), id, ci.UpdateRequest{Model: &m})
			},
			func() { run = w.runImport(api, header, []string{"server", serial, "wp826-import"}) },
		)
		if editErr != nil {
			t.Fatalf("round %d: manual change: %v", round, editErr)
		}
		when := fmt.Sprintf("round %d", round)
		kept(when, id)
		if run.result.Failed != 0 || run.result.Total != 1 {
			t.Fatalf("%s: import %+v %v, want the row written or kept", when, run.result, run.report)
		}
		if run.has(0, "model", importer.CodeOverrideKept) {
			// The manual change committed first.
			conflicted(when, id, &run)
			before++
			continue
		}
		// The import committed first: its value is history, the manual
		// change came after it.
		if n := w.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'model' AND new_value #>> '{}' = 'wp826-import'`,
			id); n != 1 {
			t.Fatalf("%s: the import neither kept the override nor wrote its value (%d changes)", when, n)
		}
		// A later import of the same value now meets the override.
		again := w.runImport(!api, header, []string{"server", serial, "wp826-import"})
		kept(when+", later import", id)
		conflicted(when+", later import", id, &again)
	}
	t.Logf("the manual change committed first in %d of 8 rounds", before)
	w.clean("import against manual changes")
}

// writersTemplate is case 2 (OVR-03): template.migrate against a manual
// change keeps the manual value and its override; the defaults of the
// target version go only into empty fields.
func writersTemplate(w *writers) {
	t := w.t
	ctx := w.as(w.editor)
	typ := &citype.Type{OrganizationID: w.f.OrgA, Key: "wp826-tpl", Name: "wp826-tpl", IsObservable: true}
	if err := w.types.Create(ctx, typ); err != nil {
		t.Fatalf("type: %v", err)
	}
	defs := map[string]citype.AttributeDefinition{}
	for _, key := range []string{"wp826_owner", "wp826_site"} {
		d := citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: "text"}
		if err := w.types.CreateAttributeDefinition(ctx, w.f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		defs[key] = d
	}
	tpl := citype.Template{Key: "wp826-tpl", CITypeID: typ.ID, IsActive: true}
	if err := w.types.CreateTemplate(ctx, w.f.OrgA, &tpl); err != nil {
		t.Fatalf("template: %v", err)
	}
	if _, err := w.types.PublishTemplateVersion(ctx, w.f.OrgA, tpl.ID, 1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := w.types.CreateTemplateVersion(ctx, w.f.OrgA, tpl.ID); err != nil {
		t.Fatalf("v2: %v", err)
	}
	if _, err := w.types.SetTemplateVersionItems(ctx, w.f.OrgA, tpl.ID, 2, []citype.TemplateVersionItem{
		{AttributeDefinitionID: defs["wp826_owner"].ID, DefaultOverride: json.RawMessage(`"ops"`), Position: 0},
		{AttributeDefinitionID: defs["wp826_site"].ID, DefaultOverride: json.RawMessage(`"dc1"`), Position: 1},
	}); err != nil {
		t.Fatalf("v2 items: %v", err)
	}
	if _, err := w.types.PublishTemplateVersion(ctx, w.f.OrgA, tpl.ID, 2); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	onV1 := func(name string) string {
		t.Helper()
		item := &ci.Item{OrganizationID: w.f.OrgA, ClientID: w.f.Client1, CITypeID: typ.ID, Name: name, Status: "active"}
		if err := w.svc.Create(ctx, item); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO ci_template (organization_id, ci_id, template_id, version, position)
			VALUES ($1, $2, $3, 1, 0)`, w.f.OrgA, item.ID, tpl.ID); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	const manualOwner = "wp826-manual"
	change := func(ctx context.Context, id string) error {
		return w.manual(ctx, id, ci.UpdateRequest{Attributes: map[string]any{"wp826_owner": manualOwner}})
	}
	migrate := func(id string) ci.TemplateMigrationReport {
		t.Helper()
		rec := w.call(w.mux, w.as(w.editor), http.MethodPost, "/api/v1/templates/"+tpl.ID+"/migrations",
			map[string]any{"from_version": 1, "to_version": 2, "ci_ids": []string{id}})
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil {
			t.Errorf("migration: %d %s", rec.Code, rec.Body.String())
			return ci.TemplateMigrationReport{}
		}
		var report ci.TemplateMigrationReport
		if err := json.Unmarshal(w.job(accepted.JobID).Result, &report); err != nil {
			t.Error(err)
		}
		return report
	}
	check := func(when, id string, report *ci.TemplateMigrationReport) {
		t.Helper()
		if report.Totals.Migrated != 1 || report.Totals.Failed != 0 {
			t.Fatalf("%s: report %+v, want the CI migrated", when, report.Totals)
		}
		if got := w.field(id, "attributes.wp826_owner"); got != manualOwner {
			t.Fatalf("%s: wp826_owner %q, want the manual value", when, got)
		}
		if value, author := w.override(id, "wp826_owner"); value != manualOwner || author != w.editor {
			t.Fatalf("%s: override %q of %q, want the manual value of the editor", when, value, author)
		}
		if got := w.field(id, "attributes.wp826_site"); got != "dc1" {
			t.Fatalf("%s: wp826_site %q, want the default of the empty field", when, got)
		}
		if value, _ := w.override(id, "wp826_site"); value != "" {
			t.Fatalf("%s: the default of wp826_site holds an override %q", when, value)
		}
		if n := w.count(`SELECT count(*) FROM ci_template WHERE ci_id = $1 AND template_id = $2 AND version = 2`, id, tpl.ID); n != 1 {
			t.Fatalf("%s: the CI is not on version 2", when)
		}
	}

	// Deterministic: the migration is planned and waits for the row of the
	// open manual change; it then sees the override.
	id := onV1("wp826-tpl-d")
	held := w.hold(w.as(w.editor), func(ctx context.Context) error { return change(ctx, id) })
	var report ci.TemplateMigrationReport
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		report = migrate(id)
	}()
	w.awaitLockWaiter("the template migration")
	held.commit(t)
	<-finished
	check("deterministic migration", id, &report)
	if len(report.Items) != 1 || strings.Join(report.Items[0].Overridden, ",") != "wp826_owner" ||
		strings.Join(report.Items[0].Defaults, ",") != "wp826_site" {
		t.Fatalf("deterministic migration: items %+v, want wp826_owner overridden and the default of wp826_site", report.Items)
	}

	for round := range 8 {
		id := onV1(fmt.Sprintf("wp826-tpl-p%d", round))
		var editErr error
		var report ci.TemplateMigrationReport
		together(
			func() { editErr = change(w.as(w.editor), id) },
			func() { report = migrate(id) },
		)
		if editErr != nil {
			t.Fatalf("round %d: manual change: %v", round, editErr)
		}
		check(fmt.Sprintf("round %d", round), id, &report)
	}
	w.clean("template migrations against manual changes")
}

// writersComputed is case 3 (JOB-02, MET-10 computed): the refresh
// against a manual change of an input of an expression with today and an
// asset field. The manual change stays, and after both commits the
// computed value is the expression over the committed inputs.
func writersComputed(w *writers) {
	t := w.t
	bg := context.Background()
	ctx := w.as(w.editor)
	typ := &citype.Type{OrganizationID: w.f.OrgA, Key: "wp826-computed", Name: "wp826-computed", IsObservable: true}
	if err := w.types.Create(ctx, typ); err != nil {
		t.Fatalf("type: %v", err)
	}
	lead := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp826_lead", DataType: "integer"}
	if err := w.types.CreateAttributeDefinition(ctx, w.f.OrgA, lead); err != nil {
		t.Fatalf("definition wp826_lead: %v", err)
	}
	expr, err := rules.ParseExpr([]byte(`{"op":"add","args":[{"op":"field","field":"wp826_lead"},
		{"op":"days_between","args":[{"op":"today"},{"op":"field","field":"asset.warranty_end"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	due := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp826_due", DataType: "number", Computed: &expr}
	if err = w.types.CreateAttributeDefinition(ctx, w.f.OrgA, due); err != nil {
		t.Fatalf("definition wp826_due: %v", err)
	}
	warranty := time.Now().UTC().AddDate(0, 0, 40)
	newCI := func(name string) (id, assetID string) {
		t.Helper()
		assetID = w.f.Asset(t, w.f.OrgA, w.f.Client1, strings.ToUpper(name))
		if _, aerr := w.f.Admin.Exec(bg, `UPDATE asset SET warranty_end = $2 WHERE id = $1`, assetID, warranty.Format(time.DateOnly)); aerr != nil {
			t.Fatal(aerr)
		}
		item := &ci.Item{OrganizationID: w.f.OrgA, ClientID: w.f.Client1, CITypeID: typ.ID, Name: name, Status: "active",
			AssetID: assetID, Attributes: map[string]any{"wp826_lead": 1}}
		if cerr := w.svc.Create(ctx, item); cerr != nil {
			t.Fatalf("create %s: %v", name, cerr)
		}
		return item.ID, assetID
	}
	// moveWarranty changes the input of the asset without a write of the
	// CI: only the refresh (or a write of the CI) recomputes.
	moveWarranty := func(assetID string, days int) {
		t.Helper()
		if _, aerr := w.f.Admin.Exec(bg, `UPDATE asset SET warranty_end = warranty_end + $2::int WHERE id = $1`, assetID, days); aerr != nil {
			t.Fatal(aerr)
		}
	}
	change := func(ctx context.Context, id string, value int) error {
		return w.manual(ctx, id, ci.UpdateRequest{Attributes: map[string]any{"wp826_lead": value}})
	}
	// check compares the value with the expression over the committed
	// inputs: the lead plus the days from today in the organization's time
	// zone to the warranty end of the asset.
	check := func(when, id string, wantLead int) {
		t.Helper()
		var lead, value, want float64
		var hasValue bool
		if qerr := w.f.Admin.QueryRow(bg, `SELECT (c.attributes->>'wp826_lead')::float8, COALESCE((c.attributes->>'wp826_due')::float8, 0),
				c.attributes ? 'wp826_due',
				(c.attributes->>'wp826_lead')::float8 + (a.warranty_end - (now() AT TIME ZONE COALESCE(NULLIF(o.timezone, ''), 'UTC'))::date)
			FROM ci c JOIN asset a ON a.id = c.asset_id JOIN organization o ON o.id = c.organization_id WHERE c.id = $1`, id).
			Scan(&lead, &value, &hasValue, &want); qerr != nil {
			t.Fatal(qerr)
		}
		if lead != float64(wantLead) {
			t.Fatalf("%s: wp826_lead %v, want the manual value %d", when, lead, wantLead)
		}
		if ovr, author := w.override(id, "wp826_lead"); ovr != fmt.Sprint(wantLead) || author != w.editor {
			t.Fatalf("%s: override %q of %q, want %d of the editor", when, ovr, author, wantLead)
		}
		if !hasValue || value != want {
			t.Fatalf("%s: wp826_due %v (set %v), want %v over the committed inputs", when, value, hasValue, want)
		}
	}
	refresh := func(when string) {
		t.Helper()
		if _, rerr := w.refresh.RunOrg(bg, w.f.OrgA); rerr != nil {
			t.Fatalf("%s: refresh: %v", when, rerr)
		}
	}

	// Deterministic: the refresh runs and ends while the manual change of
	// the lead is open (it skips the locked CI); the commit of the change
	// computes the value.
	id, assetID := newCI("wp826-due-d")
	moveWarranty(assetID, 3)
	held := w.hold(w.as(w.editor), func(ctx context.Context) error { return change(ctx, id, 5) })
	ran := make(chan error, 1)
	go func() {
		_, rerr := w.refresh.RunOrg(bg, w.f.OrgA)
		ran <- rerr
	}()
	select {
	case rerr := <-ran:
		if rerr != nil {
			t.Fatalf("refresh next to the open change: %v", rerr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the refresh waited for the CI of the open manual change instead of skipping it")
	}
	held.commit(t)
	check("deterministic refresh during the change", id, 5)
	// And the refresh after the change of the asset alone, then the
	// manual change: each commit leaves the expression over its inputs.
	moveWarranty(assetID, 4)
	refresh("refresh before the change")
	if err = change(w.as(w.editor), id, 6); err != nil {
		t.Fatal(err)
	}
	check("refresh, then manual change", id, 6)

	for round := range 8 {
		id, assetID := newCI(fmt.Sprintf("wp826-due-p%d", round))
		moveWarranty(assetID, round+1)
		var editErr, refreshErr error
		together(
			func() { editErr = change(w.as(w.editor), id, 10+round) },
			func() { _, refreshErr = w.refresh.RunOrg(bg, w.f.OrgA) },
		)
		if editErr != nil || refreshErr != nil {
			t.Fatalf("round %d: manual change %v, refresh %v", round, editErr, refreshErr)
		}
		check(fmt.Sprintf("round %d", round), id, 10+round)
	}
	w.clean("computed refresh against manual changes")
}

// writersBulk is case 4 (OVR-03, BLK-01): bulk set_fields is a manual act
// like the change of the editor. The value of the writer that committed
// last stays with its override, both changes are in ci_change, the other
// fields of either are kept.
func writersBulk(w *writers) {
	t := w.t
	const manualModel, bulkModel, manualHost, bulkOS = "wp826-manual", "wp826-bulk", "wp826-host", "wp826-os"
	change := func(ctx context.Context, id string) error {
		m, h := manualModel, manualHost
		return w.manual(ctx, id, ci.UpdateRequest{Model: &m, Hostname: &h})
	}
	bulk := func(id string) {
		rec := w.call(w.mux, w.as(w.bulker), http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": ci.BulkSetFields,
			"ids": []string{id}, "params": map[string]any{"fields": map[string]any{"model": bulkModel, "os_name": bulkOS}}})
		var res ci.BulkResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Succeeded != 1 {
			t.Errorf("bulk set_fields: %d %s, want the id written", rec.Code, rec.Body.String())
		}
	}
	// check returns who committed last.
	check := func(when, id string) string {
		t.Helper()
		var last, actor string
		var changes int
		if err := w.f.Admin.QueryRow(context.Background(), `SELECT
				(SELECT new_value #>> '{}' FROM ci_change WHERE ci_id = $1 AND field_name = 'model' ORDER BY ci_version DESC, created_at DESC LIMIT 1),
				(SELECT COALESCE(actor_id::text, '') FROM ci_change WHERE ci_id = $1 AND field_name = 'model' ORDER BY ci_version DESC, created_at DESC LIMIT 1),
				(SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'model')`, id).Scan(&last, &actor, &changes); err != nil {
			t.Fatal(err)
		}
		if changes != 2 {
			t.Fatalf("%s: %d changes of model in ci_change, want the manual change and the bulk", when, changes)
		}
		model := w.field(id, "model")
		value, author := w.override(id, "model")
		want := map[string]string{manualModel: w.editor, bulkModel: w.bulker}
		if model != last || value != model || author != actor || want[model] != actor {
			t.Fatalf("%s: model %q, override %q of %q, last change %q of %q; want the value and override of the last writer",
				when, model, value, author, last, actor)
		}
		if host, os := w.field(id, "hostname"), w.field(id, "os_name"); host != manualHost || os != bulkOS {
			t.Fatalf("%s: hostname %q, os_name %q; want both writers' other fields kept", when, host, os)
		}
		if v, a := w.override(id, "hostname"); v != manualHost || a != w.editor {
			t.Fatalf("%s: override of hostname %q of %q", when, v, a)
		}
		if v, a := w.override(id, "os_name"); v != bulkOS || a != w.bulker {
			t.Fatalf("%s: override of os_name %q of %q", when, v, a)
		}
		return model
	}

	// Deterministic: the bulk waits for the open manual change and commits
	// last.
	id := w.seedCI("wp826-bulk-d", "WP826-BULK-D", "snmp-model")
	held := w.hold(w.as(w.editor), func(ctx context.Context) error { return change(ctx, id) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		bulk(id)
	}()
	w.awaitLockWaiter("the bulk")
	held.commit(t)
	<-done
	if got := check("deterministic bulk", id); got != bulkModel {
		t.Fatalf("deterministic bulk: model %q, want the bulk value committed last", got)
	}

	wins := map[string]int{}
	for round := range 8 {
		id := w.seedCI(fmt.Sprintf("wp826-bulk-p%d", round), fmt.Sprintf("WP826-BULK-P%d", round), "snmp-model")
		var editErr error
		together(
			func() { editErr = change(w.as(w.editor), id) },
			func() { bulk(id) },
		)
		if editErr != nil {
			t.Fatalf("round %d: manual change: %v", round, editErr)
		}
		wins[check(fmt.Sprintf("round %d", round), id)]++
	}
	t.Logf("committed last: %v", wins)
	w.clean("bulk against manual changes")
}

// identityCount counts the live CIs of organization A per serial number,
// MAC and management IP.
func (w *writers) identityCount(serial, mac, ip string) (bySerial, byMAC, byIP int) {
	w.t.Helper()
	if err := w.f.Admin.QueryRow(context.Background(), `SELECT
			count(*) FILTER (WHERE serial_number = $2), count(*) FILTER (WHERE lower(primary_mac::text) = lower($3)),
			count(*) FILTER (WHERE host(management_ip) = $4)
		FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, w.f.OrgA, serial, mac, ip).Scan(&bySerial, &byMAC, &byIP); err != nil {
		w.t.Fatal(err)
	}
	return bySerial, byMAC, byIP
}

// writersImportIngest is case 5 (E-56, REC-02): an import row that creates
// a device and the ingest of the same device (serial number, MAC, IP) end
// with one CI: the import takes the identity locks of the ingest.
func writersImportIngest(w *writers) {
	t := w.t
	header := []string{"ci_type", "name", "serial_number", "primary_mac", "management_ip"}
	one := func(when, serial, mac, ip string, run *importRun) {
		t.Helper()
		if s, m, i := w.identityCount(serial, mac, ip); s != 1 || m != 1 || i != 1 {
			t.Fatalf("%s: %d CIs by serial, %d by MAC, %d by IP; want exactly one", when, s, m, i)
		}
		if run.result.Total != 1 || run.result.Failed != 0 || run.result.Created+run.result.Updated+run.result.Skipped != 1 {
			t.Fatalf("%s: import %+v %v, want the row created or matched", when, run.result, run.report)
		}
	}
	keys := func(kind string, n int) (serial, mac, ip string) {
		return fmt.Sprintf("WP826-NEW-%s%02d", kind, n), fmt.Sprintf("00:1b:21:82:6%s:%02x", kind, n), fmt.Sprintf("198.18.82.%d", n+map[string]int{"a": 10, "b": 40, "c": 70}[kind])
	}

	// One after the other in both orders: the second writer matches the
	// CI of the first.
	for i := range 2 {
		serial, mac, ip := keys("a", i)
		var run importRun
		if i == 0 {
			w.ingest(0, device(serial, mac, ip))
			run = w.runImport(false, header, []string{"server", "wp826-" + serial, serial, mac, ip})
		} else {
			run = w.runImport(true, header, []string{"server", "wp826-" + serial, serial, mac, ip})
			w.ingest(1, device(serial, mac, ip))
		}
		one(fmt.Sprintf("one after the other (%d)", i), serial, mac, ip, &run)
	}

	// Parallel: the import, alternately over a file and over rows, next to
	// the ingest of the device on two instances with four lanes each. An
	// import row reaches its write only after the start of its job, so the
	// ingest starts later from round to round, across the time an import
	// takes: the creations meet in some rounds.
	began := time.Now()
	w.runImport(true, header, []string{"server", "wp826-WP826-NEW-PROBE", "WP826-NEW-PROBE", "00:1b:21:82:6f:ff", "198.18.82.250"})
	took := time.Since(began)
	const rounds = 12
	created := map[string]int{}
	for round := range rounds {
		serial, mac, ip := keys("b", round)
		delay := took * time.Duration(round) / rounds
		var run importRun
		together(
			func() {
				run = w.runImport(round%2 == 1, header, []string{"server", "wp826-" + serial, serial, mac, ip})
			},
			func() {
				time.Sleep(delay)
				w.ingest(0, device(serial, mac, ip))
			},
			func() {
				time.Sleep(delay)
				w.ingest(1, device(serial, "", ip))
			},
		)
		one(fmt.Sprintf("round %d", round), serial, mac, ip, &run)
		if run.result.Created == 1 {
			created["import"]++
		} else {
			created["ingest"]++
		}
	}
	t.Logf("an import takes %s; created the device first in %d rounds: %v", took, rounds, created)
	w.clean("import and ingest of one device")
}

// writersSerialLink is case 6 (WP-741, AST-03): a double ingest of a
// device whose serial number an asset holds, over two instances with four
// lanes each. The device ends as one CI linked to the asset, without a new
// asset and with at most one open serial_mismatch.
func writersSerialLink(w *writers) {
	t := w.t
	bg := context.Background()
	newAsset := func(serial string) string {
		t.Helper()
		id := w.f.ID()
		if _, err := w.f.Admin.Exec(bg, `INSERT INTO asset (id, organization_id, asset_tag, name, serial_number) VALUES ($1, $2, $3, $3, $4)`,
			id, w.f.OrgA, "WP826-TAG-"+serial, serial); err != nil {
			t.Fatalf("asset %s: %v", serial, err)
		}
		return id
	}
	check := func(when, serial, assetID string, assets int) {
		t.Helper()
		var n int
		var id string
		if err := w.f.Admin.QueryRow(bg, `SELECT count(*), COALESCE(min(id::text), '') FROM ci
			WHERE organization_id = $1 AND serial_number_lc = lower($2) AND deleted_at IS NULL`, w.f.OrgA, serial).Scan(&n, &id); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s: %d CIs of the device, want 1", when, n)
		}
		if linked := w.count(`SELECT count(*) FROM ci WHERE asset_id = $1 AND deleted_at IS NULL`, assetID); linked != 1 ||
			w.field(id, "asset_id") != assetID {
			t.Fatalf("%s: %d CIs linked to the asset (device linked to %q), want the device alone", when, linked, w.field(id, "asset_id"))
		}
		if got := w.count(`SELECT count(*) FROM asset WHERE organization_id = $1`, w.f.OrgA); got != assets {
			t.Fatalf("%s: %d assets, want %d: the link creates none", when, got, assets)
		}
		if open := w.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'serial_mismatch' AND status = 'open'
			AND payload->>'ci_id' = $2`, w.f.OrgA, id); open > 1 {
			t.Fatalf("%s: %d open serial_mismatch reviews of the CI, want at most one", when, open)
		}
	}
	// The records of one device carry different strongest keys (serial
	// number alone, serial number with MAC), as two collectors report it.
	records := func(serial string, n int) (plugins.DeviceRecord, plugins.DeviceRecord) {
		ip := fmt.Sprintf("198.18.83.%d", n+1)
		return device(serial, "", ip), device(serial, fmt.Sprintf("00:1b:21:83:00:%02x", n), ip)
	}

	for i := range 2 {
		serial := fmt.Sprintf("WP826-LINK-S%d", i)
		assetID := newAsset(serial)
		assets := w.count(`SELECT count(*) FROM asset WHERE organization_id = $1`, w.f.OrgA)
		first, second := records(serial, i)
		w.ingest(i, first)
		w.ingest(1-i, second)
		check(fmt.Sprintf("one after the other (%d)", i), serial, assetID, assets)
	}
	for round := range 8 {
		serial := fmt.Sprintf("WP826-LINK-P%d", round)
		assetID := newAsset(serial)
		assets := w.count(`SELECT count(*) FROM asset WHERE organization_id = $1`, w.f.OrgA)
		first, second := records(serial, 10+round)
		together(func() { w.ingest(0, first) }, func() { w.ingest(1, second) })
		check(fmt.Sprintf("round %d", round), serial, assetID, assets)
	}
	w.clean("double ingest with the serial link")
}

// writersMaxCIs is case 7 (ENT-03, SR-09, K21): the import, the ingest
// over lanes and AdoptUnlicensed against a max_cis just above the stock.
// The active CIs never pass the limit; surplus ingest records become
// unlicensed_ci, surplus import rows are reported with entitlement-limit.
func writersMaxCIs(w *writers) {
	t := w.t
	bg := context.Background()
	defer w.limit.n.Store(1 << 40)
	header := []string{"ci_type", "name", "serial_number"}
	held := func(serial string) int {
		return w.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'unlicensed_ci' AND status = 'open'
			AND strpos(payload::text, $2) > 0`, w.f.OrgA, `"`+serial+`"`)
	}
	exists := func(serial string) int {
		return w.count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, w.f.OrgA, serial)
	}
	rows := func(prefix string, n int) [][]string {
		out := [][]string{header}
		for i := range n {
			serial := fmt.Sprintf("%s-%02d", prefix, i)
			out = append(out, []string{"server", "wp826-" + serial, serial})
		}
		return out
	}
	devices := func(prefix string, net, n int) []plugins.DeviceRecord {
		out := make([]plugins.DeviceRecord, n)
		for i := range out {
			out[i] = device(fmt.Sprintf("%s-%02d", prefix, i), "", fmt.Sprintf("198.18.%d.%d", net, i+1))
		}
		return out
	}

	// One after the other: the ingest fills the headroom of two and holds
	// the third device, the import reports both rows, the raised limit
	// adopts the held device.
	start := w.active()
	w.limit.n.Store(int64(start + 2))
	w.ingest(0, devices("WP826-LIM-S", 84, 3)...)
	if n := w.active(); n != start+2 || held("WP826-LIM-S-02")+exists("WP826-LIM-S-02") != 1 {
		t.Fatalf("ingest of 3 devices with a headroom of 2: %d CIs, want %d and the third held", n-start, 2)
	}
	run := w.runImport(false, rows("WP826-LIM-SI", 2)...)
	if run.result.Created != 0 || run.result.Failed != 2 || !run.has(0, "", importer.CodeEntitlementLimit) ||
		!run.has(1, "", importer.CodeEntitlementLimit) {
		t.Fatalf("import at the limit: %+v %v, want both rows reported with %s", run.result, run.report, importer.CodeEntitlementLimit)
	}
	w.limit.n.Store(int64(start + 3))
	if adopted, err := w.adopter.AdoptUnlicensed(w.as(w.editor), w.f.OrgA); err != nil || adopted != 1 || w.active() != start+3 {
		t.Fatalf("adoption after raising the limit by one: %d adopted (%v), %d CIs; want 1 and %d", adopted, err, w.active()-start, 3)
	}
	w.clean("max_cis one after the other")

	// Parallel: 10 devices are held at the limit; with a headroom of 12 the
	// import of 10 rows, the ingest of 2 x 5 devices on two instances and
	// the adoption of the held devices run at once.
	base := w.active()
	w.limit.n.Store(int64(base))
	pending := devices("WP826-LIM-H", 85, 10)
	w.ingest(0, pending...)
	if w.active() != base || w.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'unlicensed_ci'
		AND status = 'open'`, w.f.OrgA) < 10 {
		t.Fatalf("the 10 devices at the limit are not held as unlicensed_ci")
	}
	limit := base + 12
	w.limit.n.Store(int64(limit))
	var peak atomic.Int64
	stop := make(chan struct{})
	var watch sync.WaitGroup
	watch.Add(1)
	go func() {
		defer watch.Done()
		for {
			var n int64
			if err := w.f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, w.f.OrgA).Scan(&n); err == nil &&
				n > peak.Load() {
				peak.Store(n)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	first, second := devices("WP826-LIM-A", 86, 5), devices("WP826-LIM-B", 87, 5)
	var runs [2]importRun
	var adopted atomic.Int64
	var adoptErr error
	done := make(chan struct{})
	var adoption sync.WaitGroup
	adoption.Add(1)
	go func() {
		defer adoption.Done()
		for {
			n, err := w.adopter.AdoptUnlicensed(w.as(w.editor), w.f.OrgA)
			if err != nil {
				adoptErr = err
				return
			}
			adopted.Add(int64(n))
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	together(
		func() { runs[0] = w.runImport(false, rows("WP826-LIM-F", 5)...) },
		func() { runs[1] = w.runImport(true, rows("WP826-LIM-R", 5)...) },
		func() { w.ingest(0, first...) },
		func() { w.ingest(1, second...) },
	)
	close(done)
	adoption.Wait()
	if adoptErr != nil {
		t.Fatalf("AdoptUnlicensed: %v", adoptErr)
	}
	// The remaining headroom goes to the held devices.
	if n, err := w.adopter.AdoptUnlicensed(w.as(w.editor), w.f.OrgA); err != nil {
		t.Fatalf("AdoptUnlicensed: %v", err)
	} else {
		adopted.Add(int64(n))
	}
	close(stop)
	watch.Wait()
	if n := w.active(); n != limit || peak.Load() > int64(limit) {
		t.Fatalf("headroom 12, demand 30: %d CIs (peak %d), want exactly the limit %d and never more", n, peak.Load(), limit)
	}
	for i, prefix := range []string{"WP826-LIM-F", "WP826-LIM-R"} {
		r := &runs[i]
		if r.result.Total != 5 || r.result.Created+r.result.Failed != 5 {
			t.Fatalf("import %s: %+v %v", prefix, r.result, r.report)
		}
		for row := range 5 {
			serial := fmt.Sprintf("%s-%02d", prefix, row)
			if exists(serial)+btoi(r.has(row, "", importer.CodeEntitlementLimit)) != 1 {
				t.Fatalf("import row %s: %d CIs, report %v; want a CI or the row reported with %s", serial, exists(serial), r.report,
					importer.CodeEntitlementLimit)
			}
		}
	}
	for _, recs := range [][]plugins.DeviceRecord{pending, first, second} {
		for i := range recs {
			serial := recs[i].Identity.SerialNumber
			if exists(serial)+held(serial) != 1 {
				t.Fatalf("device %s: %d CIs and %d open unlicensed_ci, want one of them", serial, exists(serial), held(serial))
			}
		}
	}
	t.Logf("created by the imports %d, adopted %d, peak %d of %d", runs[0].result.Created+runs[1].result.Created, adopted.Load(),
		peak.Load(), limit)
	w.clean("max_cis across import, ingest and adoption")
}

// btoi is 1 for true.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
