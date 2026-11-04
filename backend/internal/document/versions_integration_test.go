package document_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// versionWorld is the link world with an HTTP router over the PostgreSQL
// repository, a blob store in a temporary directory and an entitlement
// service whose rows the tests grant.
type versionWorld struct {
	*linkWorld
	blobDir string
	ents    *entitlement.Service
	mux     chi.Router
}

func newVersionWorld(t *testing.T, enforce bool) *versionWorld {
	t.Helper()
	w := &versionWorld{linkWorld: newLinkWorld(t), blobDir: t.TempDir()}
	w.ents = entitlement.NewService(nil, entitlement.Options{Enforce: enforce})
	w.mux = chi.NewRouter()
	document.NewHandler(w.repo, blob.NewFileStore(w.blobDir)).WithLimits(w.ents).RegisterRoutes(w.mux)
	return w
}

// grant stores the documents row of organization A with plan and limits.
func (w *versionWorld) grant(t *testing.T, plan entitlement.Plan, limits map[string]int64) {
	t.Helper()
	if _, err := w.ents.Grant(context.Background(), entitlement.Entitlement{OrganizationID: w.f.OrgA,
		FeatureKey: entitlement.FeatureDocuments, Plan: plan, Enabled: true, Limits: limits}); err != nil {
		t.Fatalf("grant documents: %v", err)
	}
}

// call runs a request of organization A under scope.
func (w *versionWorld) call(scope *database.TenantScope, method, path, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.user})
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
	return rec
}

func (w *versionWorld) orgScope() *database.TenantScope {
	s := database.OrgWideScope(w.f.OrgA, w.user)
	return &s
}

// blobs returns the number of stored objects.
func (w *versionWorld) blobs(t *testing.T) int {
	t.Helper()
	n := 0
	if err := filepath.WalkDir(w.blobDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// usage answers GET /documents/usage for the org-wide principal.
func (w *versionWorld) usage(t *testing.T) document.Usage {
	t.Helper()
	rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/usage", "", "")
	var u document.Usage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &u) != nil {
		t.Fatalf("GET usage: %d %s", rec.Code, rec.Body.String())
	}
	return u
}

// setUsage stores the usage of organization A directly.
func (w *versionWorld) setUsage(t *testing.T, bytes int64) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO organization_usage (organization_id, storage_bytes) VALUES ($1, $2)
		ON CONFLICT (organization_id) DO UPDATE SET storage_bytes = EXCLUDED.storage_bytes`, w.f.OrgA, bytes); err != nil {
		t.Fatal(err)
	}
}

// TestDocumentVersions covers WP-731 (MGT-01, API-06): every upload is a
// version with a key of its own, the document mirrors the latest, the
// earlier one stays downloadable, the list is paginated, the usage equals
// the sum of the version sizes and the audit log records
// document.version_created. A foreign organization and a client-scoped
// principal without a visible document see no versions.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentVersions(t *testing.T) {
	w := newVersionWorld(t, false)
	f := w.f
	ctx := context.Background()
	d := w.doc(t, "ver-doc", "asset", w.asset1)
	base := "/api/v1/documents/" + d

	var keys []string
	for i, body := range []string{"%PDF-first", "%PDF-second version"} {
		method := http.MethodPut
		path := base + "/content"
		if i == 1 {
			method, path = http.MethodPost, base+"/versions"
		}
		rec := w.call(w.orgScope(), method, path, "application/pdf", body)
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("upload %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
		row := w.scalar(t, `SELECT version::text || '|' || storage_key || '|' || file_size::text FROM document WHERE id = $1`, d)
		parts := strings.Split(row, "|")
		if parts[0] != strconv.Itoa(i+1) || parts[2] != strconv.Itoa(len(body)) || slices.Contains(keys, parts[1]) ||
			!strings.HasPrefix(parts[1], "org/"+f.OrgA+"/documents/"+d+"/") {
			t.Fatalf("upload %d: document %s, earlier keys %v", i+1, row, keys)
		}
		keys = append(keys, parts[1])
	}
	if n := w.blobs(t); n != 2 {
		t.Fatalf("blobs %d, want 2", n)
	}

	// Only versions scanned clean are downloadable (WP-733).
	for n := 1; n <= 2; n++ {
		if err := w.repo.SetScanResult(w.orgCtx, f.OrgA, d, n, document.ScanClean, ""); err != nil {
			t.Fatalf("scan result of version %d: %v", n, err)
		}
	}
	for query, key := range map[string]string{"?version=1": keys[0], "?version=2": keys[1], "": keys[1]} {
		rec := w.call(w.orgScope(), http.MethodGet, base+"/content"+query, "", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), key) {
			t.Fatalf("download %q: %d %s, want %s", query, rec.Code, rec.Body.String(), key)
		}
	}
	if rec := w.call(w.orgScope(), http.MethodGet, base+"/content?version=3", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("download of a missing version: %d", rec.Code)
	}

	// Paginated, highest first, with size, MIME type, checksum and status.
	var got []document.Version
	path := base + "/versions?limit=1"
	for pages := 0; path != ""; pages++ {
		if pages > 3 {
			t.Fatal("pagination does not end")
		}
		rec := w.call(w.orgScope(), http.MethodGet, path, "", "")
		var page api.ListResponse[document.Version]
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("list versions: %d %s", rec.Code, rec.Body.String())
		}
		got = append(got, page.Data...)
		path = ""
		if page.NextCursor != "" {
			path = base + "/versions?limit=1&cursor=" + page.NextCursor
		}
	}
	if len(got) != 2 || got[0].Version != 2 || got[1].Version != 1 {
		t.Fatalf("versions %+v", got)
	}
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, v := range got {
		if v.ScanStatus != document.ScanClean || v.ScannedAt == nil || v.ChecksumSHA256 == nil || !sha.MatchString(*v.ChecksumSHA256) ||
			v.MimeType != "application/pdf" || v.DocumentID != d {
			t.Fatalf("version %+v", v)
		}
	}

	if u, sum := w.usage(t), w.scalar(t, `SELECT sum(size)::text FROM document_version WHERE organization_id = $1`, f.OrgA); strconv.FormatInt(u.StorageBytes, 10) != sum ||
		u.LimitMB != nil {
		t.Fatalf("usage %+v, sum of the versions %s", u, sum)
	}
	if n := w.scalar(t, `SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2
		AND action = 'document.version_created' AND changes->>'version' IN ('1', '2') AND changes ? 'checksum_sha256'`, f.OrgA, d); n != "2" {
		t.Fatalf("document.version_created entries %s, want 2", n)
	}

	// A foreign organization and a principal of client 2 see no versions.
	otherOrg := database.OrgWideScope(f.OrgB, w.user)
	if _, _, err := w.repo.ListVersions(database.ContextWithTenantScope(ctx, &otherOrg), f.OrgB, d, api.PaginationParams{Limit: 10}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("foreign organization: %v", err)
	}
	client2 := database.OrgWideScope(f.OrgA, w.user)
	client2.Clients = database.ScopeIDs(f.Client2)
	if rec := w.call(&client2, http.MethodGet, base+"/versions", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("client 2 lists versions: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(&client2, http.MethodGet, base+"/content?version=1", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("client 2 downloads a version: %d %s", rec.Code, rec.Body.String())
	}
	var visible int
	if err := database.WithRequestTenant(database.ContextWithTenantScope(ctx, &client2), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM document_version WHERE document_id = $1`, d).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("client 2 reads %d version rows (err %v)", visible, err)
	}
	if rec := w.call(&client2, http.MethodPost, base+"/versions", "text/plain", "x"); rec.Code != http.StatusNotFound {
		t.Fatalf("client 2 uploads: %d %s", rec.Code, rec.Body.String())
	}

	// Deleting the document is a soft delete (WP-732): its bytes count
	// until the retention run purges it.
	before := w.usage(t)
	if rec := w.call(w.orgScope(), http.MethodDelete, base, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if u := w.usage(t); u.StorageBytes != before.StorageBytes || u.StorageBytes == 0 {
		t.Fatalf("usage after delete %+v, want %+v", u, before)
	}
}

// migrationStatement returns the statement of migration 000220 that starts
// with prefix, without its semicolon.
func migrationStatement(t *testing.T, prefix string) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/000220_document_versions_usage.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, prefix)
	if i < 0 {
		t.Fatalf("migration 000220 has no statement %q", prefix)
	}
	end := strings.Index(src[i:], ";")
	return src[i : i+end]
}

// TestDocumentVersionBackfill (WP-731): the backfill of migration 000220
// turns a document with content, legacy key included, into version 1 with
// size and MIME type of the document, no checksum and pending_scan, and the
// usage into the sum of the version sizes. The statements of the migration
// run restricted to a document and organization of the test.
func TestDocumentVersionBackfill(t *testing.T) {
	w := newVersionWorld(t, false)
	f := w.f
	ctx := context.Background()
	id := f.ID()
	legacy := "documents/" + f.OrgA + "/" + id + "/handbook.pdf"
	if _, err := f.Admin.Exec(ctx, `INSERT INTO document (id, organization_id, title, file_name, file_size, mime_type, storage_key, uploaded_by)
		VALUES ($1, $2, 'legacy', 'handbook.pdf', 1234, 'application/pdf', $3, $4)`, id, f.OrgA, legacy, w.user); err != nil {
		t.Fatal(err)
	}
	backfill := migrationStatement(t, "INSERT INTO document_version") + " AND id = $1"
	if _, err := f.Admin.Exec(ctx, backfill, id); err != nil {
		t.Fatalf("backfill %s: %v", backfill, err)
	}
	if got := w.scalar(t, `SELECT version::text || '|' || storage_key || '|' || size::text || '|' || mime || '|' ||
		COALESCE(checksum_sha256, 'NULL') || '|' || scan_status || '|' || uploaded_by_surrogate::text
		FROM document_version WHERE document_id = $1`, id); got != "1|"+legacy+"|1234|application/pdf|NULL|pending_scan|"+w.user {
		t.Fatalf("backfilled version %s", got)
	}
	usage := strings.Replace(migrationStatement(t, "INSERT INTO organization_usage"), "GROUP BY", "WHERE organization_id = $1 GROUP BY", 1)
	if _, err := f.Admin.Exec(ctx, usage, f.OrgA); err != nil {
		t.Fatalf("usage backfill %s: %v", usage, err)
	}
	if u := w.usage(t); u.StorageBytes != 1234 {
		t.Fatalf("usage %+v, want 1234", u)
	}
	// The legacy version stays downloadable once scanned clean (WP-733).
	rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/"+id+"/content?version=1", "", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), document.ProblemPendingScan) {
		t.Fatalf("download of the unscanned legacy version: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if err := w.repo.SetScanResult(w.orgCtx, f.OrgA, id, 1, document.ScanClean, ""); err != nil {
		t.Fatal(err)
	}
	rec = w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/"+id+"/content?version=1", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), legacy) {
		t.Fatalf("download of the legacy version: %d %s", rec.Code, rec.Body.String())
	}
	// After the backfill the key check refuses a key of another document.
	_, err := f.Admin.Exec(ctx, `INSERT INTO document_version (organization_id, document_id, version, storage_key, size, mime)
		VALUES ($1, $2, 2, $3, 1, 'text/plain')`, f.OrgA, id, "org/"+f.OrgA+"/documents/"+f.ID()+"/x")
	if err == nil || !strings.Contains(err.Error(), "outside the document") {
		t.Fatalf("foreign key path accepted: %v", err)
	}
}

// TestDocumentStorageLimit (WP-731, MGT-01, E-44): an explicit
// max_storage_mb refuses an upload beyond it with 403 entitlement-limit and
// a trace_id, and no blob stays behind; a client-scoped principal is
// counted with the whole organization; of two parallel uploads at the limit
// exactly one succeeds.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentStorageLimit(t *testing.T) {
	w := newVersionWorld(t, true)
	f := w.f
	w.grant(t, entitlement.PlanEnterprise, map[string]int64{entitlement.LimitMaxStorageMB: 1})
	const limit = 1 << 20
	d1 := w.doc(t, "lim-doc1", "asset", w.asset1)
	d2 := w.doc(t, "lim-doc2", "asset", w.asset2)

	// Bytes of client 2 count for the principal of client 1 as well.
	if rec := w.call(w.orgScope(), http.MethodPut, "/api/v1/documents/"+d2+"/content", "text/plain", "client two"); rec.Code != http.StatusOK {
		t.Fatalf("upload of client 2: %d %s", rec.Code, rec.Body.String())
	}
	w.setUsage(t, limit-12)
	client1 := database.OrgWideScope(f.OrgA, w.user)
	client1.Clients = database.ScopeIDs(f.Client1)
	before := w.blobs(t)
	rec := w.call(&client1, http.MethodPost, "/api/v1/documents/"+d1+"/versions", "text/plain", "thirteen byte")
	var problem api.ProblemDetail
	if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &problem) != nil ||
		problem.Type != api.ProblemEntitlementLimit || problem.TraceID == "" {
		t.Fatalf("upload beyond the limit: %d %s", rec.Code, rec.Body.String())
	}
	if n := w.blobs(t); n != before {
		t.Fatalf("blobs %d after the refusal, want %d", n, before)
	}
	if u := w.usage(t); u.StorageBytes != limit-12 || u.LimitMB == nil || *u.LimitMB != 1 {
		t.Fatalf("usage %+v", u)
	}

	// The binding check in the transaction refuses as well (the
	// non-binding one is passed by calling the repository directly) and
	// leaves the usage unchanged.
	lim := int64(limit)
	v := &document.Version{ID: f.ID(), OrganizationID: f.OrgA, DocumentID: d1, Size: 13, MimeType: "text/plain",
		StorageKey: blob.OrgKey(f.OrgA, "documents", d1, "direct")}
	var exceeded *entitlement.LimitExceededError
	if _, err := w.repo.CreateVersion(database.ContextWithTenantScope(context.Background(), &client1), v, &lim); !errors.As(err, &exceeded) ||
		exceeded.Current != limit-12 || exceeded.Limit != limit {
		t.Fatalf("binding check: %v", err)
	}

	// Two parallel uploads of 8 bytes with 12 bytes left: exactly one.
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = w.call(&client1, http.MethodPost, "/api/v1/documents/"+d1+"/versions", "text/plain", "parallel").Code
		}(i)
	}
	wg.Wait()
	slices.Sort(codes)
	if codes[0] != http.StatusCreated || codes[1] != http.StatusForbidden {
		t.Fatalf("parallel uploads %v, want one 201 and one 403", codes)
	}
	if n := w.blobs(t); n != before+1 {
		t.Fatalf("blobs %d after the parallel uploads, want %d", n, before+1)
	}
	if u := w.usage(t); u.StorageBytes != limit-4 {
		t.Fatalf("usage %+v, want %d", u, limit-4)
	}
}

// TestStorageLimitBytes (WP-731, E-34, E-44): a limit stored in the
// documents row goes first, else the plan default (essential 1,000 MiB,
// enterprise unlimited); without enforcement there is no limit and without
// the documents row the feature is not entitled.
func TestStorageLimitBytes(t *testing.T) {
	ctx := context.Background()
	const org = "c0de3400-0000-4000-8000-00000000beef"
	for _, c := range []struct {
		name    string
		enforce bool
		ent     *entitlement.Entitlement
		want    int64 // -1: unlimited
	}{
		{"stored limit", true, &entitlement.Entitlement{Plan: entitlement.PlanEnterprise, Limits: map[string]int64{entitlement.LimitMaxStorageMB: 5}}, 5 << 20},
		{"essential default", true, &entitlement.Entitlement{Plan: entitlement.PlanEssential}, 1000 << 20},
		{"standard default", true, &entitlement.Entitlement{Plan: entitlement.PlanStandard}, 10000 << 20},
		{"pro default", true, &entitlement.Entitlement{Plan: entitlement.PlanPro}, 50000 << 20},
		{"enterprise unlimited", true, &entitlement.Entitlement{Plan: entitlement.PlanEnterprise}, -1},
		{"not enforced", false, &entitlement.Entitlement{Plan: entitlement.PlanEssential, Limits: map[string]int64{entitlement.LimitMaxStorageMB: 1}}, -1},
		{"not entitled", true, nil, 0},
	} {
		svc := entitlement.NewService(nil, entitlement.Options{Enforce: c.enforce})
		if c.ent != nil {
			c.ent.OrganizationID, c.ent.FeatureKey, c.ent.Enabled = org, entitlement.FeatureDocuments, true
			if _, err := svc.Grant(ctx, *c.ent); err != nil {
				t.Fatal(err)
			}
		}
		got, err := svc.StorageLimitBytes(ctx, org)
		var notEntitled *entitlement.FeatureNotEntitledError
		switch {
		case c.ent == nil:
			if !errors.As(err, &notEntitled) {
				t.Errorf("%s: %v, want not entitled", c.name, err)
			}
		case err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want < 0 && got != nil:
			t.Errorf("%s: limit %d, want unlimited", c.name, *got)
		case c.want >= 0 && (got == nil || *got != c.want):
			t.Errorf("%s: limit %v, want %d", c.name, got, c.want)
		}
	}
}
