package export_test

// WP-1151 (TLC-05): the routes of the full export on PostgreSQL through
// the session middleware, the tenant context and the route authorization of
// the server. The start needs org:export and a second factor (403
// mfa-required), a second start while one is queued is 409, an export of
// another organization is 404; the download hands out signed URLs at most
// until expires_at and is 410 after it, each call audited; the hourly
// cleanup removes the objects of expired exports, also those of cancelled
// and failed ones and of a result that cannot be decoded; starts at the
// same time queue one export; the job API hides the export from job:read
// without org:export; the success of the job
// writes org.export_completed, which reaches its initiator personally.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestOrgExport(Requires|Single|Foreign|Download|Cleanup|Completed|Hidden)' ./internal/export/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// orgExportVerifier verifies the session tokens of the test.
type orgExportVerifier map[string]*identity.SessionClaims

func (v orgExportVerifier) Validate(token string) (*identity.SessionClaims, error) {
	c, ok := v[token]
	if !ok {
		return nil, errors.New("unknown token")
	}
	return c, nil
}

// signingStore records the validity asked for a signed URL.
type signingStore struct {
	blob.Store
	mu     sync.Mutex
	maxAge []time.Duration
}

func (s *signingStore) PresignedGetURL(ctx context.Context, bucket, key string, dl blob.Download) (string, error) {
	s.mu.Lock()
	s.maxAge = append(s.maxAge, dl.MaxAge)
	s.mu.Unlock()
	return s.Store.PresignedGetURL(ctx, bucket, key, dl)
}

type orgExportAPIEnv struct {
	f       *scopetest.Fixture
	tokens  orgExportVerifier
	handler http.Handler
	svc     *jobs.Service
	blobs   *signingStore
	now     time.Time
	admin   string
}

func newOrgExportAPIEnv(t *testing.T) *orgExportAPIEnv {
	t.Helper()
	f := scopetest.Seed(t, "1c")
	e := &orgExportAPIEnv{f: f, tokens: orgExportVerifier{}, now: time.Now().UTC()}
	e.svc = jobs.NewService(jobs.NewPGRepository(f.App))
	e.svc.Register(jobs.Kind{Name: export.OrgExportJobKind, MaxAttempts: 3, Timeout: 2 * time.Hour, Cancellable: true})
	e.blobs = &signingStore{Store: blob.NewFileStore(t.TempDir())}
	h := export.NewOrgExportHandler(e.svc, f.App, e.blobs).WithClock(func() time.Time { return e.now })
	mux := chi.NewRouter()
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/org/exports"}, {http.MethodGet, "/api/v1/org/exports"},
		{http.MethodGet, "/api/v1/org/exports/{id}"}, {http.MethodGet, "/api/v1/org/exports/{id}/download"},
	} {
		var fn http.HandlerFunc
		switch {
		case route.method == http.MethodPost:
			fn = h.Create
		case strings.HasSuffix(route.path, "/download"):
			fn = h.Download
		case strings.HasSuffix(route.path, "{id}"):
			fn = h.Get
		default:
			fn = h.List
		}
		mux.With(server.AuthorizeRoute(route.method, route.path)).Method(route.method, route.path, fn)
	}
	e.handler = middleware.AuthMiddlewareWithVerifier(e.tokens)(middleware.TenantMiddleware(mux))
	user := f.AppUser(t, f.OrgA, "wp1151-admin")
	e.admin = e.session(t, "admin-mfa", f.OrgA, user, "org_admin", true)
	e.session(t, "admin-nomfa", f.OrgA, user, "org_admin", false)
	e.session(t, "engineer-mfa", f.OrgA, f.AppUser(t, f.OrgA, "wp1151-engineer"), "engineer", true)
	e.session(t, "foreign-mfa", f.OrgB, f.AppUser(t, f.OrgB, "wp1151-foreign"), "org_admin", true)
	return e
}

// session registers a token of user with the permissions of a standard
// role, organization-wide.
func (e *orgExportAPIEnv) session(t *testing.T, token, orgID, user, role string, mfa bool) string {
	t.Helper()
	keys, ok := permission.StandardRolePermissions(role)
	if !ok {
		t.Fatalf("standard role %s unknown", role)
	}
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}
	all := identity.ScopeSet{All: true}
	e.tokens[token] = &identity.SessionClaims{Subject: user, OrganizationID: orgID, Permissions: perms,
		Scope: &identity.Scope{Clients: all, Sites: all, Teams: all}, SessionID: "sid-" + token, MFA: mfa,
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	return user
}

func (e *orgExportAPIEnv) call(t *testing.T, token, method, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// start queues an export as the admin with MFA and returns it.
func (e *orgExportAPIEnv) start(t *testing.T) export.OrgExport {
	t.Helper()
	code, body := e.call(t, "admin-mfa", http.MethodPost, "/api/v1/org/exports")
	var out export.OrgExport
	if code != http.StatusAccepted || json.Unmarshal([]byte(body), &out) != nil || out.ID == "" || out.JobID != out.ID {
		t.Fatalf("POST /org/exports: %d %s", code, body)
	}
	return out
}

// complete runs the job of an export to success with a manifest of one
// table part and the manifest object in the store; it returns the keys.
func (e *orgExportAPIEnv) complete(t *testing.T, id string) []string {
	t.Helper()
	bg := context.Background()
	if _, err := e.svc.ClaimJob(bg, e.f.OrgA, id); err != nil {
		t.Fatal(err)
	}
	part := export.OrgExportPrefix(e.f.OrgA, id) + "/tables/ci/part-0001.jsonl"
	content := []byte(`{"id":"x"}` + "\n")
	if err := e.blobs.Put(bg, export.ExportBucket, part, bytes.NewReader(content), int64(len(content)), "application/x-ndjson"); err != nil {
		t.Fatal(err)
	}
	done := e.now
	m := &export.OrgExportManifest{OrganizationID: e.f.OrgA, JobID: id, Format: "jsonl", StartedAt: done, CompletedAt: &done,
		Tables: []export.OrgExportTable{{Name: "ci", Rows: 1, Parts: []export.OrgExportPart{{Key: part, Bytes: int64(len(content)), SHA256: "abc"}}}}}
	raw, _ := json.Marshal(m)
	manifestKey := export.OrgExportManifestKey(e.f.OrgA, id)
	if err := e.blobs.Put(bg, export.ExportBucket, manifestKey, bytes.NewReader(raw), int64(len(raw)), "application/json"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Succeed(bg, e.f.OrgA, id, m); err != nil {
		t.Fatal(err)
	}
	// The download period runs from the end of the job.
	var finished time.Time
	if err := e.f.Admin.QueryRow(bg, `SELECT finished_at FROM job WHERE id = $1`, id).Scan(&finished); err != nil {
		t.Fatal(err)
	}
	e.now = finished.UTC()
	return []string{part, manifestKey}
}

func TestOrgExportRequiresMFA(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	code, body := e.call(t, "admin-nomfa", http.MethodPost, "/api/v1/org/exports")
	if code != http.StatusForbidden || !strings.Contains(body, export.ProblemTypeMFARequired) {
		t.Fatalf("POST without MFA: %d %s, want 403 mfa-required", code, body)
	}
	ex := e.start(t)
	e.complete(t, ex.ID)
	if code, body = e.call(t, "admin-nomfa", http.MethodGet, "/api/v1/org/exports/"+ex.ID+"/download"); code != http.StatusForbidden ||
		!strings.Contains(body, export.ProblemTypeMFARequired) {
		t.Fatalf("download without MFA: %d %s, want 403 mfa-required", code, body)
	}
	// Reading the status needs no second factor.
	if code, _ = e.call(t, "admin-nomfa", http.MethodGet, "/api/v1/org/exports/"+ex.ID); code != http.StatusOK {
		t.Fatalf("GET without MFA: %d", code)
	}
}

func TestOrgExportRequiresPermission(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/org/exports"}, {http.MethodGet, "/api/v1/org/exports"},
	} {
		if code, body := e.call(t, "engineer-mfa", c.method, c.path); code != http.StatusForbidden {
			t.Errorf("%s %s as engineer: %d %s, want 403", c.method, c.path, code, body)
		}
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`,
		e.f.OrgA, export.OrgExportJobKind).Scan(&n); err != nil || n != 0 {
		t.Fatalf("jobs after the refusals: %d %v", n, err)
	}
}

func TestOrgExportSingleRunning(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	// Starts at the same time queue exactly one export: the check and the
	// insert hold a transaction lock of the organization.
	const parallel = 4
	codes := make([]int, parallel)
	bodies := make([]string, parallel)
	var wg sync.WaitGroup
	for i := range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/org/exports", nil)
			req.Header.Set("Authorization", "Bearer admin-mfa")
			w := httptest.NewRecorder()
			e.handler.ServeHTTP(w, req)
			codes[i], bodies[i] = w.Code, w.Body.String()
		}()
	}
	wg.Wait()
	var first export.OrgExport
	accepted := 0
	for i, code := range codes {
		switch code {
		case http.StatusAccepted:
			accepted++
			if err := json.Unmarshal([]byte(bodies[i]), &first); err != nil {
				t.Fatal(err)
			}
		case http.StatusConflict:
		default:
			t.Fatalf("parallel start: %d %s", code, bodies[i])
		}
	}
	var queued int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`,
		e.f.OrgA, export.OrgExportJobKind).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || queued != 1 {
		t.Fatalf("parallel starts: %d accepted (%v), %d jobs, want exactly one", accepted, codes, queued)
	}
	if code, body := e.call(t, "admin-mfa", http.MethodPost, "/api/v1/org/exports"); code != http.StatusConflict {
		t.Fatalf("second start while queued: %d %s, want 409", code, body)
	}
	if _, err := e.svc.ClaimJob(context.Background(), e.f.OrgA, first.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.call(t, "admin-mfa", http.MethodPost, "/api/v1/org/exports"); code != http.StatusConflict {
		t.Fatalf("second start while running: %d, want 409", code)
	}
	if _, err := e.svc.Succeed(context.Background(), e.f.OrgA, first.ID, &export.OrgExportManifest{}); err != nil {
		t.Fatal(err)
	}
	second := e.start(t)
	code, body := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports")
	var list struct {
		Data  []export.OrgExport `json:"data"`
		Total int                `json:"total"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || list.Total != 2 || list.Data[0].ID != second.ID ||
		list.Data[0].Status != string(jobs.StatusQueued) || list.Data[1].Status != export.OrgExportCompleted || list.Data[1].Manifest != nil {
		t.Fatalf("list: %d %s", code, body)
	}
}

func TestOrgExportForeignOrg404(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	ex := e.start(t)
	e.complete(t, ex.ID)
	for _, path := range []string{"/api/v1/org/exports/" + ex.ID, "/api/v1/org/exports/" + ex.ID + "/download"} {
		if code, _ := e.call(t, "foreign-mfa", http.MethodGet, path); code != http.StatusNotFound {
			t.Errorf("GET %s from another organization: %d, want 404", path, code)
		}
	}
	// Another job of the organization is no export.
	other, _, err := e.svc.Enqueue(context.Background(), e.f.OrgA, export.OrgExportJobKind, nil, jobs.EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE job SET kind = 'import.run' WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+other.ID); code != http.StatusNotFound {
		t.Errorf("GET of another job kind: %d, want 404", code)
	}
}

func TestOrgExportDownloadExpires(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	ex := e.start(t)
	keys := e.complete(t, ex.ID)
	audits := func() int {
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE organization_id = $1
			AND action = 'org.export_downloaded' AND resource_type = 'org' AND resource_id = $2`, e.f.OrgA, ex.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	code, body := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+ex.ID)
	var got export.OrgExport
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Status != export.OrgExportCompleted ||
		got.Manifest == nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(e.now.Add(24*time.Hour)) {
		t.Fatalf("GET: %d %s", code, body)
	}
	download := func() export.OrgExportDownload {
		t.Helper()
		status, raw := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+ex.ID+"/download")
		var d export.OrgExportDownload
		if status != http.StatusOK || json.Unmarshal([]byte(raw), &d) != nil || len(d.Objects) != len(keys) {
			t.Fatalf("download: %d %s", status, raw)
		}
		for _, o := range d.Objects {
			if o.URL == "" || o.URLExpiresAt.After(d.ExpiresAt) || o.Bytes == 0 || o.SHA256 == "" {
				t.Errorf("object %+v of the download, expires_at %s", o, d.ExpiresAt)
			}
		}
		if d.Objects[0].Name != "tables/ci/part-0001.jsonl" || d.Objects[1].Name != "manifest.json" {
			t.Errorf("names %q, %q", d.Objects[0].Name, d.Objects[1].Name)
		}
		return d
	}
	d := download()
	if want := e.now.Add(blob.PresignTTL); !d.Objects[0].URLExpiresAt.Equal(want) {
		t.Errorf("url_expires_at %s, want %s", d.Objects[0].URLExpiresAt, want)
	}
	if audits() != 1 {
		t.Fatalf("audit entries after one download: %d", audits())
	}

	// Five minutes before the end the URLs live five minutes, not fifteen.
	e.now = got.ExpiresAt.Add(-5 * time.Minute)
	e.blobs.maxAge = nil
	d = download()
	if !d.Objects[0].URLExpiresAt.Equal(*got.ExpiresAt) {
		t.Errorf("url_expires_at %s, want expires_at %s", d.Objects[0].URLExpiresAt, got.ExpiresAt)
	}
	for _, age := range e.blobs.maxAge {
		if age != 5*time.Minute {
			t.Errorf("signed with max age %s, want 5m", age)
		}
	}
	if audits() != 2 {
		t.Fatalf("audit entries after two downloads: %d", audits())
	}

	// The clock + 24 h: 410, the status expired, no audit entry.
	e.now = got.ExpiresAt.Add(time.Second)
	if status, raw := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+ex.ID+"/download"); status != http.StatusGone {
		t.Fatalf("download after expires_at: %d %s, want 410", status, raw)
	}
	code, body = e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+ex.ID)
	got = export.OrgExport{}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Status != export.OrgExportExpired || got.Manifest != nil {
		t.Fatalf("GET after expires_at: %d %s", code, body)
	}
	if audits() != 2 {
		t.Fatalf("audit entries after the refused download: %d", audits())
	}
}

func TestOrgExportCleanupRemovesObjects(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	bg := context.Background()
	old := e.start(t)
	oldKeys := e.complete(t, old.ID)
	// The first export completed 25 hours ago, a second, fresh one stays.
	if _, err := e.f.Admin.Exec(bg, `UPDATE job SET finished_at = finished_at - interval '25 hours' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh := e.start(t)
	freshKeys := e.complete(t, fresh.ID)
	completedAt := e.now

	cleanup := export.NewOrgExportCleanup(e.f.App, e.blobs).WithClock(func() time.Time { return completedAt.Add(time.Minute) })
	if n, err := cleanup.RunOrg(bg, e.f.OrgA); err != nil || n != 1 {
		t.Fatalf("cleanup: %d %v, want one export", n, err)
	}
	for _, key := range oldKeys {
		if _, err := e.blobs.Get(bg, export.ExportBucket, key); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("object %s after the cleanup: %v", key, err)
		}
	}
	for _, key := range freshKeys {
		rc, err := e.blobs.Get(bg, export.ExportBucket, key)
		if err != nil {
			t.Errorf("object %s of the fresh export: %v", key, err)
			continue
		}
		_ = rc.Close()
	}
	code, body := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+old.ID)
	var got export.OrgExport
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Status != export.OrgExportExpired {
		t.Fatalf("GET after the cleanup: %d %s", code, body)
	}
	if code, _ := e.call(t, "admin-mfa", http.MethodGet, "/api/v1/org/exports/"+old.ID+"/download"); code != http.StatusGone {
		t.Fatalf("download after the cleanup: %d, want 410", code)
	}
	// A second run finds nothing.
	if n, err := cleanup.RunOrg(bg, e.f.OrgA); err != nil || n != 0 {
		t.Fatalf("second cleanup: %d %v", n, err)
	}
	if err := cleanup.Run(bg); err != nil {
		t.Fatalf("cleanup of all organizations: %v", err)
	}
	// The job stays succeeded; the result records expired_at.
	var status, expiredAt string
	if err := e.f.Admin.QueryRow(bg, `SELECT status, COALESCE(result->>'expired_at', '') FROM job WHERE id = $1`, old.ID).
		Scan(&status, &expiredAt); err != nil || status != string(jobs.StatusSucceeded) || expiredAt == "" {
		t.Fatalf("job after the cleanup: %s %q %v, want succeeded with expired_at", status, expiredAt, err)
	}

	// A cancel after the last check of the run left its objects; a
	// completed export whose result cannot be decoded: both lose their
	// objects through manifest.json in the store.
	age := func(id string) {
		t.Helper()
		if _, err := e.f.Admin.Exec(bg, `UPDATE job SET finished_at = finished_at - interval '25 hours' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	cancelled := e.start(t)
	cancelledKeys := e.complete(t, cancelled.ID)
	if _, err := e.f.Admin.Exec(bg, `UPDATE job SET status = 'cancelled', result = NULL WHERE id = $1`, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	age(cancelled.ID)
	corrupt := e.start(t)
	corruptKeys := e.complete(t, corrupt.ID)
	if _, err := e.f.Admin.Exec(bg, `UPDATE job SET result = '"not a manifest"'::jsonb WHERE id = $1`, corrupt.ID); err != nil {
		t.Fatal(err)
	}
	age(corrupt.ID)
	cleanup.WithClock(func() time.Time { return e.now.Add(time.Minute) })
	if n, err := cleanup.RunOrg(bg, e.f.OrgA); err != nil || n != 2 {
		t.Fatalf("cleanup of the cancelled and the corrupt export: %d %v, want two", n, err)
	}
	for _, key := range append(cancelledKeys, corruptKeys...) {
		if _, err := e.blobs.Get(bg, export.ExportBucket, key); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("object %s after the cleanup: %v", key, err)
		}
	}

	// A manifest that cannot be read keeps the export unmarked: the next
	// run tries again and the error is returned.
	broken := e.start(t)
	e.complete(t, broken.ID)
	if _, err := e.f.Admin.Exec(bg, `UPDATE job SET status = 'failed', result = NULL WHERE id = $1`, broken.ID); err != nil {
		t.Fatal(err)
	}
	age(broken.ID)
	key := export.OrgExportManifestKey(e.f.OrgA, broken.ID)
	if err := e.blobs.Put(bg, export.ExportBucket, key, strings.NewReader("{"), 1, "application/json"); err != nil {
		t.Fatal(err)
	}
	cleanup.WithClock(func() time.Time { return e.now.Add(time.Minute) })
	// (The fresh export of the start has expired by now and goes too.)
	if _, err := cleanup.RunOrg(bg, e.f.OrgA); err == nil || !strings.Contains(err.Error(), broken.ID) {
		t.Fatalf("cleanup with an unreadable manifest: %v, want the error of the export", err)
	}
	if err := e.f.Admin.QueryRow(bg, `SELECT COALESCE(result->>'expired_at', '') FROM job WHERE id = $1`, broken.ID).Scan(&expiredAt); err != nil || expiredAt != "" {
		t.Fatalf("export with an unreadable manifest: expired_at %q %v, want none", expiredAt, err)
	}
}

// TestOrgExportHiddenFromJobRead: the job of a full export names every
// table, its rows and objects; job:read without org:export (an engineer)
// does not see it in the job API, org_admin does.
func TestOrgExportHiddenFromJobRead(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	ex := e.start(t)
	mux := chi.NewRouter()
	jobs.NewAPIHandler(e.svc).
		WithKind(export.OrgExportJobKind, identity.PermOrgExport, nil).
		WithReadKind(export.OrgExportJobKind, identity.PermOrgExport).
		RegisterRoutes(mux)
	handler := middleware.AuthMiddlewareWithVerifier(e.tokens)(middleware.TenantMiddleware(mux))
	get := func(token, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	for _, path := range []string{"/api/v1/jobs/" + ex.ID, "/api/v1/jobs/" + ex.ID + "/result"} {
		if code, body := get("engineer-mfa", path); code != http.StatusNotFound {
			t.Errorf("engineer GET %s: %d %s, want 404", path, code, body)
		}
	}
	for _, path := range []string{"/api/v1/jobs", "/api/v1/jobs?kind=" + export.OrgExportJobKind} {
		if code, body := get("engineer-mfa", path); code != http.StatusOK || strings.Contains(body, ex.ID) {
			t.Errorf("engineer GET %s: %d %s, want 200 without the export", path, code, body)
		}
	}
	if code, body := get("admin-mfa", "/api/v1/jobs/"+ex.ID); code != http.StatusOK || !strings.Contains(body, ex.ID) {
		t.Errorf("org_admin GET the job: %d %s", code, body)
	}
}

func TestOrgExportCompletedEventAndNotification(t *testing.T) {
	e := newOrgExportAPIEnv(t)
	ex := e.start(t)
	e.complete(t, ex.ID)
	var raw []byte
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT data FROM event_outbox WHERE organization_id = $1
		AND type = $2 AND entity_id = $3`, e.f.OrgA, events.OrgExportCompleted, ex.ID).Scan(&raw); err != nil {
		t.Fatalf("org.export_completed in the outbox: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data["id"] != ex.ID || data["initiated_by"] != e.admin || data["rows"] != float64(1) || data["bytes"] != float64(11) {
		t.Fatalf("event data %v", data)
	}
	expires, err := time.Parse(time.RFC3339, data["expires_at"].(string))
	if err != nil || !expires.Equal(e.now.Add(24*time.Hour).Truncate(time.Second)) {
		t.Fatalf("expires_at %v (%v), want %s", data["expires_at"], err, e.now.Add(24*time.Hour))
	}

	sub, ok := notify.Subscriptions[events.OrgExportCompleted]
	if !ok || !sub.Personal || sub.Audience == nil {
		t.Fatalf("subscription %+v", sub)
	}
	if aud := sub.Audience(data); aud == nil || len(aud.DirectUserIDs) != 1 || aud.DirectUserIDs[0] != e.admin {
		t.Fatalf("audience %+v, want the initiator", aud)
	}
	for _, locale := range []string{notify.LocaleDE, notify.LocaleEN} {
		render, ok := notify.Templates[events.OrgExportCompleted][locale]
		if !ok {
			t.Fatalf("no %s text", locale)
		}
		if text := render(data); text.Subject == "" || !strings.Contains(text.Body, data["expires_at"].(string)) {
			t.Errorf("%s text %+v", locale, text)
		}
	}
}
