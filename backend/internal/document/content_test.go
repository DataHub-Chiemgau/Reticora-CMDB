package document

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
