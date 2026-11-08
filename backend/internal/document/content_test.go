package document

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// fakeBlobStore is an in-memory blob.Store for handler tests.
type fakeBlobStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	// types holds the content type each object was stored with.
	types map[string]string
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{objects: map[string][]byte{}, types: map[string]string{}}
}

func (s *fakeBlobStore) Put(_ context.Context, bucket, key string, reader io.Reader, _ int64, contentType string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = data
	s.types[bucket+"/"+key] = contentType
	return nil
}

func (s *fakeBlobStore) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (s *fakeBlobStore) Delete(_ context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, bucket+"/"+key)
	return nil
}

func (s *fakeBlobStore) PresignedGetURL(_ context.Context, bucket, key string, _ blob.Download) (string, error) {
	return "https://minio.example.com/" + bucket + "/" + key + "?signature=fake", nil
}

func contentRequest(t *testing.T, mux http.Handler, method, url, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func newDocumentFixture(t *testing.T) (*MemoryRepository, chi.Router) {
	t.Helper()
	repo := NewMemoryRepository()
	doc := &Document{
		OrganizationID: "org-1",
		Title:          "Handbuch",
		FileName:       "handbuch.pdf",
		MimeType:       "application/pdf",
		Category:       "general",
	}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatalf("create: %v", err)
	}
	mux := chi.NewRouter()
	NewHandler(repo, newFakeBlobStore()).RegisterRoutes(mux)
	return repo, mux
}

func documentID(t *testing.T, repo *MemoryRepository) string {
	t.Helper()
	docs, _, err := repo.List(context.Background(), "org-1", &FilterParams{}, api.PaginationParams{Limit: 50})
	if err != nil || len(docs) == 0 {
		t.Fatalf("list: %v (n=%d)", err, len(docs))
	}
	return docs[0].ID
}

func TestUploadAndDownloadContent(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)

	w := contentRequest(t, mux, "PUT", "/api/v1/documents/"+id+"/content", "application/pdf", "%PDF-fake")
	if w.Code != http.StatusOK {
		t.Fatalf("upload: got %d: %s", w.Code, w.Body.String())
	}
	var updated Document
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.StorageKey == "" || updated.FileSize != int64(len("%PDF-fake")) {
		t.Fatalf("storage metadata not updated: %+v", updated)
	}
	if updated.ScanStatus != ScanPending || updated.ScannedAt != nil {
		t.Fatalf("scan status after the upload: %q %v, want pending_scan", updated.ScanStatus, updated.ScannedAt)
	}

	// Right after the upload the content is not scanned: 409 (WP-733).
	w = contentRequest(t, mux, "GET", "/api/v1/documents/"+id+"/content", "", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), ProblemPendingScan) ||
		strings.Contains(w.Body.String(), "download_url") {
		t.Fatalf("download before the scan: got %d %s, want 409 document-pending-scan", w.Code, w.Body.String())
	}
	markClean(t, repo, id, 1)

	w = contentRequest(t, mux, "GET", "/api/v1/documents/"+id+"/content", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("download: got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(payload.DownloadURL, updated.StorageKey) {
		t.Fatalf("download URL %q does not reference %q", payload.DownloadURL, updated.StorageKey)
	}
}

// markClean sets version n of the document to clean, as the malware scan
// of WP-734 will.
func markClean(t *testing.T, repo Repository, id string, n int) {
	t.Helper()
	if err := repo.SetScanResult(context.Background(), "org-1", id, n, ScanClean, ""); err != nil {
		t.Fatalf("SetScanResult(%s, %d, clean): %v", id, n, err)
	}
}

// TestDownloadRequiresCleanScan (WP-733, SEC-09, E-42): only a version
// scanned clean is presigned; pending_scan and scan_failed answer 409
// document-pending-scan, infected 409 document-quarantined, with and
// without ?version=. A quarantined document takes no new link, its
// existing links stay.
func TestDownloadRequiresCleanScan(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)
	const target = "11111111-1111-4111-8111-111111111111"
	if err := repo.LinkDocument(context.Background(), &DocumentLink{OrganizationID: "org-1", DocumentID: id, EntityType: "ci", EntityID: target}); err != nil {
		t.Fatal(err)
	}
	if w := contentRequest(t, mux, http.MethodPut, "/api/v1/documents/"+id+"/content", "text/plain", "content"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct {
		status, problem string
		code            int
	}{
		{ScanPending, ProblemPendingScan, http.StatusConflict},
		{ScanFailed, ProblemPendingScan, http.StatusConflict},
		{ScanInfected, ProblemQuarantined, http.StatusConflict},
		{ScanClean, "download_url", http.StatusOK},
	} {
		if err := repo.SetScanResult(context.Background(), "org-1", id, 1, c.status, "Eicar-Signature"); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"", "?version=1"} {
			w := contentRequest(t, mux, http.MethodGet, "/api/v1/documents/"+id+"/content"+query, "", "")
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.problem) {
				t.Fatalf("download%s at %s: got %d %s, want %d %s", query, c.status, w.Code, w.Body.String(), c.code, c.problem)
			}
		}
		d, err := repo.GetByID(context.Background(), "org-1", id)
		if err != nil || d.ScanStatus != c.status || (d.ScannedAt == nil) != (c.status == ScanPending) {
			t.Fatalf("document at %s: %+v %v", c.status, d, err)
		}
	}

	if err := repo.SetScanResult(context.Background(), "org-1", id, 1, ScanInfected, "Eicar-Signature"); err != nil {
		t.Fatal(err)
	}
	body := `{"entity_type":"ci","entity_id":"22222222-2222-4222-8222-222222222222"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/"+id+"/links", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), ProblemQuarantined) {
		t.Fatalf("link to a quarantined document: got %d %s, want 409 document-quarantined", w.Code, w.Body.String())
	}
	if links, err := repo.GetLinks(context.Background(), "org-1", id); err != nil || len(links) != 1 || links[0].EntityID != target {
		t.Fatalf("links of the quarantined document: %+v %v, want the one link", links, err)
	}

	// A new version starts pending again; the infected one stays blocked.
	if w := contentRequest(t, mux, http.MethodPost, "/api/v1/documents/"+id+"/versions", "text/plain", "second"); w.Code != http.StatusCreated {
		t.Fatalf("second version: %d %s", w.Code, w.Body.String())
	}
	if w := contentRequest(t, mux, http.MethodGet, "/api/v1/documents/"+id+"/content", "", ""); !strings.Contains(w.Body.String(), ProblemPendingScan) {
		t.Fatalf("download of the new version: %d %s, want document-pending-scan", w.Code, w.Body.String())
	}
	if w := contentRequest(t, mux, http.MethodGet, "/api/v1/documents/"+id+"/content?version=1", "", ""); !strings.Contains(w.Body.String(), ProblemQuarantined) {
		t.Fatalf("download of the infected version: %d %s, want document-quarantined", w.Code, w.Body.String())
	}
	if err := repo.SetScanResult(context.Background(), "org-1", id, 3, ScanClean, ""); err == nil {
		t.Fatal("SetScanResult of a missing version succeeded")
	}
	if err := repo.SetScanResult(context.Background(), "org-1", id, 2, "unknown", ""); err == nil {
		t.Fatal("SetScanResult with an unknown status succeeded")
	}
	pending, err := repo.PendingScans(context.Background(), "org-1", 10)
	if err != nil || len(pending) != 1 || pending[0].Version != 2 {
		t.Fatalf("PendingScans: %+v %v, want version 2", pending, err)
	}
}

// TestUploadContentCreatesVersions (WP-731): every PUT …/content stores
// the next version under a key of its own, the document mirrors the latest
// one, the earlier version stays downloadable with ?version=n and the list
// pages from the highest version down.
func TestUploadContentCreatesVersions(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)

	var keys []string
	for i, body := range []string{"%PDF-one", "%PDF-second"} {
		w := contentRequest(t, mux, "PUT", "/api/v1/documents/"+id+"/content", "application/pdf", body)
		if w.Code != http.StatusOK {
			t.Fatalf("upload %d: got %d: %s", i+1, w.Code, w.Body.String())
		}
		var d Document
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if d.Version != i+1 || d.FileSize != int64(len(body)) || slices.Contains(keys, d.StorageKey) {
			t.Fatalf("upload %d: document %+v, earlier keys %v", i+1, d, keys)
		}
		keys = append(keys, d.StorageKey)
	}

	markClean(t, repo, id, 1)
	markClean(t, repo, id, 2)
	for n, key := range map[string]string{"1": keys[0], "2": keys[1], "": keys[1]} {
		url := "/api/v1/documents/" + id + "/content"
		if n != "" {
			url += "?version=" + n
		}
		w := contentRequest(t, mux, "GET", url, "", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), key) {
			t.Fatalf("download version %q: got %d %s, want key %s", n, w.Code, w.Body.String(), key)
		}
	}
	for url, want := range map[string]int{"?version=3": http.StatusNotFound, "?version=0": http.StatusBadRequest, "?version=x": http.StatusBadRequest} {
		if w := contentRequest(t, mux, "GET", "/api/v1/documents/"+id+"/content"+url, "", ""); w.Code != want {
			t.Fatalf("download %s: got %d, want %d", url, w.Code, want)
		}
	}

	w := contentRequest(t, mux, "POST", "/api/v1/documents/"+id+"/versions", "text/plain", "third")
	var created Version
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Version != 3 ||
		created.ScanStatus != ScanPending || created.ChecksumSHA256 == nil || len(*created.ChecksumSHA256) != 64 {
		t.Fatalf("POST versions: got %d %s", w.Code, w.Body.String())
	}

	var got []int
	url := "/api/v1/documents/" + id + "/versions?limit=2"
	for url != "" {
		w = contentRequest(t, mux, "GET", url, "", "")
		var page api.ListResponse[Version]
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("list versions: got %d %s", w.Code, w.Body.String())
		}
		for _, v := range page.Data {
			got = append(got, v.Version)
		}
		url = ""
		if page.NextCursor != "" {
			url = "/api/v1/documents/" + id + "/versions?limit=2&cursor=" + page.NextCursor
		}
	}
	if !slices.Equal(got, []int{3, 2, 1}) {
		t.Fatalf("versions %v, want [3 2 1]", got)
	}

	w = contentRequest(t, mux, "GET", "/api/v1/documents/usage", "", "")
	want := fmt.Sprintf(`{"storage_bytes":%d,"limit_mb":null}`, len("%PDF-one")+len("%PDF-second")+len("third"))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("usage: got %d %s, want %s", w.Code, w.Body.String(), want)
	}
}

// fixedLimit is a StorageLimiter with a fixed limit in bytes.
type fixedLimit int64

func (l fixedLimit) StorageLimitBytes(context.Context, string) (*int64, error) {
	v := int64(l)
	return &v, nil
}

// TestUploadBeyondStorageLimitStoresNothing (WP-731): an upload that does
// not fit the limit answers 403 entitlement-limit and leaves no blob.
func TestUploadBeyondStorageLimitStoresNothing(t *testing.T) {
	repo := NewMemoryRepository()
	doc := &Document{OrganizationID: "org-1", Title: "t", FileName: "f.txt", Category: "general"}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	blobs := newFakeBlobStore()
	mux := chi.NewRouter()
	NewHandler(repo, blobs).WithLimits(fixedLimit(10)).RegisterRoutes(mux)

	if w := contentRequest(t, mux, "PUT", "/api/v1/documents/"+doc.ID+"/content", "text/plain", "0123456789"); w.Code != http.StatusOK {
		t.Fatalf("upload at the limit: got %d %s", w.Code, w.Body.String())
	}
	w := contentRequest(t, mux, "POST", "/api/v1/documents/"+doc.ID+"/versions", "text/plain", "x")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), api.ProblemEntitlementLimit) ||
		!strings.Contains(w.Body.String(), `"trace_id"`) {
		t.Fatalf("upload beyond the limit: got %d %s", w.Code, w.Body.String())
	}
	if len(blobs.objects) != 1 {
		t.Fatalf("blobs %d, want 1", len(blobs.objects))
	}
	w = contentRequest(t, mux, "GET", "/api/v1/documents/usage", "", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"storage_bytes":10,"limit_mb":0}` {
		t.Fatalf("usage: got %d %s", w.Code, w.Body.String())
	}
}

func TestUploadRejectsDisallowedMimeType(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)

	for _, ct := range []string{"text/html", "application/javascript", "image/svg+xml"} {
		w := contentRequest(t, mux, "PUT", "/api/v1/documents/"+id+"/content", ct, "<script>alert(1)</script>")
		if w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s: got %d, want 415", ct, w.Code)
		}
	}
}

// TestUploadEnforcesSizeLimit (SEC-09, WP-268): the limit comes from the
// configuration (RETICORA_DOCUMENT_MAX_UPLOAD_MB); a body above it answers
// 413 and stores nothing, a body at it is stored.
func TestUploadEnforcesSizeLimit(t *testing.T) {
	repo := NewMemoryRepository()
	doc := &Document{OrganizationID: "org-1", Title: "t", FileName: "f.txt", Category: "general"}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	blobs := newFakeBlobStore()
	mux := chi.NewRouter()
	NewHandler(repo, blobs).WithUploads(config.DocumentSettings{MaxUploadMB: 1}).RegisterRoutes(mux)

	upload := func(size int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/documents/"+doc.ID+"/content", io.LimitReader(&infiniteReader{}, size))
		req.Header.Set("Content-Type", "text/plain")
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := upload(1<<20 + 1); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "1 MiB") {
		t.Fatalf("above the limit: got %d %s, want 413", w.Code, w.Body.String())
	}
	if len(blobs.objects) != 0 {
		t.Fatalf("blobs after a refused upload: %d", len(blobs.objects))
	}
	if w := upload(1 << 20); w.Code != http.StatusOK {
		t.Fatalf("at the limit: got %d %s, want 200", w.Code, w.Body.String())
	}

	// The default is 50 MiB (52,428,800 bytes).
	if got := NewHandler(repo, blobs).uploads.maxBytes; got != 52428800 {
		t.Fatalf("default limit %d", got)
	}
}

// TestUploadRequiresContentType (SEC-09, WP-268): without a Content-Type,
// and with application/octet-stream, which is no longer a default nor on
// the whitelist, an upload answers 415.
func TestUploadRequiresContentType(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)
	for _, ct := range []string{"", "application/octet-stream"} {
		for _, method := range []string{http.MethodPut, http.MethodPost} {
			url := "/api/v1/documents/" + id + "/content"
			if method == http.MethodPost {
				url = "/api/v1/documents/" + id + "/versions"
			}
			if w := contentRequest(t, mux, method, url, ct, "%PDF-1.7"); w.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("%s with Content-Type %q: got %d %s, want 415", method, ct, w.Code, w.Body.String())
			}
		}
	}
}

// TestUploadStoresVerifiedType (SEC-09, WP-268): the stored type is the
// verified one on both paths, in the version, the document and the blob;
// content that does not match its type answers 422 mime-mismatch and
// stores nothing.
func TestUploadStoresVerifiedType(t *testing.T) {
	repo := NewMemoryRepository()
	doc := &Document{OrganizationID: "org-1", Title: "t", FileName: "f.png", MimeType: "application/pdf", Category: "general"}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	blobs := newFakeBlobStore()
	mux := chi.NewRouter()
	NewHandler(repo, blobs).RegisterRoutes(mux)
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

	w := contentRequest(t, mux, http.MethodPut, "/api/v1/documents/"+doc.ID+"/content", "Image/PNG; name=x", png)
	var d Document
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.MimeType != "image/png" {
		t.Fatalf("PUT content: got %d %s", w.Code, w.Body.String())
	}
	if got := blobs.types[documentBucket+"/"+d.StorageKey]; got != "image/png" {
		t.Fatalf("blob content type %q", got)
	}
	w = contentRequest(t, mux, http.MethodPost, "/api/v1/documents/"+doc.ID+"/versions", "text/csv; charset=utf-8", "a;b\n1;2\n")
	var v Version
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.MimeType != "text/csv" {
		t.Fatalf("POST versions: got %d %s", w.Code, w.Body.String())
	}
	current, err := repo.GetByID(context.Background(), "org-1", doc.ID)
	if err != nil || current.MimeType != "text/csv" {
		t.Fatalf("document mime type: %+v %v", current, err)
	}
	if got := blobs.types[documentBucket+"/"+current.StorageKey]; got != "text/csv" {
		t.Fatalf("blob content type %q", got)
	}

	for _, method := range []string{http.MethodPut, http.MethodPost} {
		url := "/api/v1/documents/" + doc.ID + "/content"
		if method == http.MethodPost {
			url = "/api/v1/documents/" + doc.ID + "/versions"
		}
		w = contentRequest(t, mux, method, url, "image/png", "%PDF-1.7 not an image")
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), ProblemMimeMismatch) ||
			!strings.Contains(w.Body.String(), `"trace_id"`) {
			t.Fatalf("%s PDF as image/png: got %d %s, want 422 mime-mismatch", method, w.Code, w.Body.String())
		}
	}
	if len(blobs.objects) != 2 {
		t.Fatalf("blobs %d, want 2", len(blobs.objects))
	}
}

// TestUploadConcurrencyLimit (SEC-09, WP-268): with four uploads running,
// the fifth answers 503 with Retry-After; once they end, the next is taken.
func TestUploadConcurrencyLimit(t *testing.T) {
	repo, _ := newDocumentFixture(t)
	id := documentID(t, repo)
	h := NewHandler(repo, newFakeBlobStore()).WithUploads(config.DocumentSettings{UploadConcurrency: 4})
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	var wg sync.WaitGroup
	writers := make([]*io.PipeWriter, 4)
	codes := make([]int, 4)
	for i := range writers {
		pr, pw := io.Pipe()
		writers[i] = pw
		req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/"+id+"/versions", pr)
		req.Header.Set("Content-Type", "text/plain")
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
		wg.Add(1)
		go func(i int, req *http.Request) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i, req)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.uploads.slots) < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("uploads running: %d, want 4", len(h.uploads.slots))
		}
		time.Sleep(5 * time.Millisecond)
	}

	w := contentRequest(t, mux, http.MethodPut, "/api/v1/documents/"+id+"/content", "text/plain", "fifth")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("fifth upload: got %d (Retry-After %q) %s, want 503", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}

	for _, pw := range writers {
		_, _ = pw.Write([]byte("running upload"))
		_ = pw.Close()
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusCreated {
			t.Fatalf("upload %d: got %d, want 201", i+1, code)
		}
	}
	if w = contentRequest(t, mux, http.MethodPut, "/api/v1/documents/"+id+"/content", "text/plain", "after"); w.Code != http.StatusOK {
		t.Fatalf("upload after the others: got %d %s", w.Code, w.Body.String())
	}
}

// slowReader yields its chunks with a pause before each.
type slowReader struct {
	chunks [][]byte
	pause  time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.pause)
	n := copy(p, s.chunks[0])
	s.chunks[0] = s.chunks[0][n:]
	if len(s.chunks[0]) == 0 {
		s.chunks = s.chunks[1:]
	}
	return n, nil
}

// TestSlowUploadOutlastsServerTimeout (SEC-09, WP-268): an upload throttled
// over more than 2 s reaches a server with a ReadTimeout and WriteTimeout of
// 1 s completely, because the upload extends its deadlines.
func TestSlowUploadOutlastsServerTimeout(t *testing.T) {
	repo, _ := newDocumentFixture(t)
	id := documentID(t, repo)
	blobs := newFakeBlobStore()
	mux := chi.NewRouter()
	NewHandler(repo, blobs).RegisterRoutes(mux)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv.Config.ReadTimeout = time.Second
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	defer srv.Close()

	chunks := [][]byte{[]byte("%PDF-1.7\n")}
	for i := range 5 {
		chunks = append(chunks, fmt.Appendf(nil, "chunk %d\n", i))
	}
	want := string(bytes.Join(chunks, nil))
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/documents/"+id+"/content",
		&slowReader{chunks: chunks, pause: 450 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/pdf")
	start := time.Now()
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("slow upload: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("slow upload: got %d %s", resp.StatusCode, body)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("upload took %s, want more than 2 s", elapsed)
	}
	var d Document
	if uerr := json.Unmarshal(body, &d); uerr != nil {
		t.Fatal(uerr)
	}
	blobs.mu.Lock()
	got := string(blobs.objects[documentBucket+"/"+d.StorageKey])
	blobs.mu.Unlock()
	if got != want {
		t.Fatalf("stored %q, want %q", got, want)
	}
}

type infiniteReader struct{}

func (r *infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestUploadWithoutBlobStoreReturns503(t *testing.T) {
	repo := NewMemoryRepository()
	doc := &Document{OrganizationID: "org-1", Title: "t", FileName: "f.pdf", Category: "general"}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatalf("create: %v", err)
	}
	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)

	docs, _, _ := repo.List(context.Background(), "org-1", &FilterParams{}, api.PaginationParams{Limit: 50})
	w := contentRequest(t, mux, "PUT", "/api/v1/documents/"+docs[0].ID+"/content", "application/pdf", "x")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	w = contentRequest(t, mux, "GET", "/api/v1/documents/"+docs[0].ID+"/content", "", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

func TestDownloadWithoutContentReturns404(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)
	w := contentRequest(t, mux, "GET", "/api/v1/documents/"+id+"/content", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}
