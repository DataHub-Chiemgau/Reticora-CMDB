package document

import (
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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// fakeBlobStore is an in-memory blob.Store for handler tests.
type fakeBlobStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{objects: map[string][]byte{}}
}

func (s *fakeBlobStore) Put(_ context.Context, bucket, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = data
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

func (s *fakeBlobStore) PresignedGetURL(_ context.Context, bucket, key string) (string, error) {
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
	docs, _, err := repo.List(context.Background(), "org-1", FilterParams{}, api.PaginationParams{Limit: 50})
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

func TestUploadEnforcesSizeLimit(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	id := documentID(t, repo)

	req := httptest.NewRequest("PUT", "/api/v1/documents/"+id+"/content",
		io.LimitReader(strings.NewReader("x"), int64(maxDocumentUpload)+1024))
	// Simulate a body larger than the limit: MaxBytesReader only trips when
	// the reader actually yields more than the limit, so wrap accordingly.
	req.Body = io.NopCloser(io.LimitReader(&infiniteReader{}, int64(maxDocumentUpload)+1))
	req.Header.Set("Content-Type", "application/octet-stream")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413", w.Code)
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

	docs, _, _ := repo.List(context.Background(), "org-1", FilterParams{}, api.PaginationParams{Limit: 50})
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
