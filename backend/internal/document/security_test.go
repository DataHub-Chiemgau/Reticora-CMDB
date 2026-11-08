package document

// Security tests of the document delivery, phase 2 (SEC-11, WP-829). They
// run without a database, with MemoryRepository and fakeBlobStore like
// content_test.go.
//
// Comparison with the existing evidence; this file holds only cases these
// tests do not cover:
//
//   - blob_isolation_test.go (WP-030): keys below org/<org>/ only
//     (TestCheckOrgKey, TestOwnsStorageKey), a requested storage_key is
//     ignored and a key of another organization is not presigned
//     (TestCreateIgnoresRequestedStorageKey), the CHECK of migration 000066
//     (TestStorageKeyRejectedOutsideOrganization). New here: a key of
//     another document of the same organization, a document of another
//     organization and a manipulated path never reach the presign.
//   - content_test.go (WP-030, WP-268, WP-733): MIME whitelist, size limit,
//     concurrency, the stored verified type of a plain upload, 409 before
//     the first scan of a new document and per scan status of version 1
//     (TestDownloadRequiresCleanScan). New here: the replacement upload on a
//     document already scanned clean, on both upload paths.
//   - sniff_test.go (WP-268): signature table and mismatches of single
//     formats (HTML as text/plain, PE as PDF, ...). New here: a polyglot
//     with a valid %PDF- head and embedded <script> through the handler.
//   - scan_status_integration_test.go (WP-733), scan_test.go and
//     scan_integration_test.go (WP-734): status transitions, the scan job
//     and the sweep. Not repeated.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// presignRecorder is a fakeBlobStore that records every presigned object.
type presignRecorder struct {
	*fakeBlobStore
	mu     sync.Mutex
	signed []string
	// downloads are the Download of each presigned object, in order.
	downloads []blob.Download
}

func (s *presignRecorder) PresignedGetURL(ctx context.Context, bucket, key string, dl blob.Download) (string, error) {
	s.mu.Lock()
	s.signed = append(s.signed, bucket+"/"+key)
	s.downloads = append(s.downloads, dl)
	s.mu.Unlock()
	return s.fakeBlobStore.PresignedGetURL(ctx, bucket, key, dl)
}

// take returns the objects presigned since the last call.
func (s *presignRecorder) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.signed
	s.signed = nil
	return out
}

// requestAs sends a request as a user of organization org.
func requestAs(t *testing.T, mux http.Handler, org, method, url, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org, UserID: "user-" + org}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// securityFixture is a handler over a memory repository with the
// recording blob store.
func securityFixture(t *testing.T) (*MemoryRepository, *presignRecorder, chi.Router) {
	t.Helper()
	repo := NewMemoryRepository()
	blobs := &presignRecorder{fakeBlobStore: newFakeBlobStore()}
	mux := chi.NewRouter()
	NewHandler(repo, blobs).RegisterRoutes(mux)
	return repo, blobs, mux
}

// newDoc creates a document of org and returns its id.
func newDoc(t *testing.T, repo *MemoryRepository, org, title string) string {
	t.Helper()
	d := &Document{OrganizationID: org, Title: title, FileName: title + ".pdf", MimeType: "application/pdf", Category: "general"}
	if err := repo.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// TestReplacementUploadOnCleanDocumentIsNotDelivered (SEC-09, E-42): a
// replacement upload over PUT …/content or POST …/versions on a document
// whose current version is clean adds a version pending_scan; until its
// scan the download presigns nothing (409 document-pending-scan), after
// an infected result 409 document-quarantined, with and without
// ?version=.
func TestReplacementUploadOnCleanDocumentIsNotDelivered(t *testing.T) {
	for _, path := range []struct{ method, suffix string }{
		{http.MethodPut, "/content"},
		{http.MethodPost, "/versions"},
	} {
		t.Run(path.method+path.suffix, func(t *testing.T) {
			repo, blobs, mux := securityFixture(t)
			id := newDoc(t, repo, "org-1", "handbuch")
			base := "/api/v1/documents/" + id

			if w := requestAs(t, mux, "org-1", http.MethodPut, base+"/content", "application/pdf", "%PDF-1.7 clean"); w.Code != http.StatusOK {
				t.Fatalf("first upload: %d %s", w.Code, w.Body.String())
			}
			markClean(t, repo, id, 1)
			if w := requestAs(t, mux, "org-1", http.MethodGet, base+"/content", "", ""); w.Code != http.StatusOK {
				t.Fatalf("download of the clean version: %d %s", w.Code, w.Body.String())
			}
			blobs.take()

			w := requestAs(t, mux, "org-1", path.method, base+path.suffix, "application/pdf", "%PDF-1.7 replacement")
			var replaced struct {
				Version    int    `json:"version"`
				ScanStatus string `json:"scan_status"`
				StorageKey string `json:"storage_key"`
			}
			if w.Code >= 300 || json.Unmarshal(w.Body.Bytes(), &replaced) != nil {
				t.Fatalf("replacement upload: %d %s", w.Code, w.Body.String())
			}
			if replaced.Version != 2 || replaced.ScanStatus != ScanPending {
				t.Fatalf("replacement: version %d status %q, want version 2 pending_scan", replaced.Version, replaced.ScanStatus)
			}
			doc, err := repo.GetByID(context.Background(), "org-1", id)
			if err != nil || doc.Version != 2 || doc.ScanStatus != ScanPending {
				t.Fatalf("document after the replacement: %+v %v", doc, err)
			}

			for _, c := range []struct{ status, problem string }{
				{ScanPending, ProblemPendingScan},
				{ScanFailed, ProblemPendingScan},
				{ScanInfected, ProblemQuarantined},
			} {
				if err := repo.SetScanResult(context.Background(), "org-1", id, 2, c.status, "Eicar-Signature"); err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{"", "?version=2"} {
					w = requestAs(t, mux, "org-1", http.MethodGet, base+"/content"+query, "", "")
					if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), c.problem) ||
						strings.Contains(w.Body.String(), "download_url") {
						t.Fatalf("download%s of the replacement at %s: %d %s, want 409 %s", query, c.status, w.Code, w.Body.String(), c.problem)
					}
				}
			}
			if signed := blobs.take(); len(signed) != 0 {
				t.Fatalf("presigned while the replacement is not clean: %v", signed)
			}
		})
	}
}

// TestPolyglotIsStoredWithTheVerifiedType (SEC-09): a polyglot with a
// valid %PDF- head and an embedded <script> is stored as application/pdf
// when declared so; declared as HTML it is refused (415), declared as text
// it is refused as well, so it is never stored or delivered as text/html.
func TestPolyglotIsStoredWithTheVerifiedType(t *testing.T) {
	const polyglot = "%PDF-1.7\n<html><body><script>alert(document.cookie)</script></body></html>\n%%EOF\n"
	repo, blobs, mux := securityFixture(t)
	id := newDoc(t, repo, "org-1", "polyglot")
	base := "/api/v1/documents/" + id

	w := requestAs(t, mux, "org-1", http.MethodPut, base+"/content", "application/pdf", polyglot)
	var d Document
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.MimeType != "application/pdf" {
		t.Fatalf("polyglot as application/pdf: %d %s", w.Code, w.Body.String())
	}
	if got := blobs.types[documentBucket+"/"+d.StorageKey]; got != "application/pdf" {
		t.Fatalf("blob content type of the polyglot: %q, want application/pdf", got)
	}

	for declared, want := range map[string]int{
		"text/html":                http.StatusUnsupportedMediaType,
		"text/html; charset=utf-8": http.StatusUnsupportedMediaType,
		"application/xhtml+xml":    http.StatusUnsupportedMediaType,
		"image/svg+xml":            http.StatusUnsupportedMediaType,
		"text/plain":               http.StatusUnprocessableEntity,
		"text/csv":                 http.StatusUnprocessableEntity,
		"application/xml":          http.StatusUnprocessableEntity,
		"application/octet-stream": http.StatusUnsupportedMediaType,
	} {
		for _, method := range []string{http.MethodPut, http.MethodPost} {
			url := base + "/content"
			if method == http.MethodPost {
				url = base + "/versions"
			}
			if w = requestAs(t, mux, "org-1", method, url, declared, polyglot); w.Code != want {
				t.Errorf("%s polyglot as %q: %d %s, want %d", method, declared, w.Code, w.Body.String(), want)
			}
		}
	}
	for key, ct := range blobs.types {
		if ct != "application/pdf" {
			t.Errorf("blob %s stored as %q, want only application/pdf", key, ct)
		}
	}
	current, err := repo.GetByID(context.Background(), "org-1", id)
	if err != nil || current.MimeType != "application/pdf" {
		t.Fatalf("document type after the attempts: %+v %v", current, err)
	}
}

// TestDownloadIsAnAttachmentWithTheVerifiedType (finding of WP-829): the
// download URL of a document is signed as an attachment with the file name
// of the document and its verified type; a stored type outside the upload
// whitelist (legacy data, e.g. text/html) is delivered as
// application/octet-stream.
func TestDownloadIsAnAttachmentWithTheVerifiedType(t *testing.T) {
	repo, blobs, mux := securityFixture(t)
	id := newDoc(t, repo, "org-1", "Prüfbericht")
	w := requestAs(t, mux, "org-1", http.MethodPut, "/api/v1/documents/"+id+"/content", "application/pdf", "%PDF-1.7 x")
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if err := repo.SetScanResult(context.Background(), "org-1", id, 1, ScanClean, ""); err != nil {
		t.Fatal(err)
	}
	blobs.downloads = nil
	for _, query := range []string{"", "?version=1"} {
		if w = requestAs(t, mux, "org-1", http.MethodGet, "/api/v1/documents/"+id+"/content"+query, "", ""); w.Code != http.StatusOK {
			t.Fatalf("download%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	want := blob.Download{FileName: "Prüfbericht.pdf", ContentType: "application/pdf"}
	if len(blobs.downloads) != 2 || blobs.downloads[0] != want || blobs.downloads[1] != want {
		t.Fatalf("signed downloads %+v, want twice %+v", blobs.downloads, want)
	}

	for stored, wantType := range map[string]string{
		"text/html":                blob.OctetStream,
		"image/svg+xml":            blob.OctetStream,
		"":                         blob.OctetStream,
		"application/pdf":          "application/pdf",
		"text/csv; charset=utf-8":  "text/csv",
		"application/octet-stream": blob.OctetStream,
	} {
		if got := downloadOf("x", stored); got.ContentType != wantType || got.FileName != "x" {
			t.Errorf("downloadOf(%q) = %+v, want type %s", stored, got, wantType)
		}
	}
}

// TestPresignOnlyForTheRequestedDocument (TEN-06): the download presigns
// exactly the key of the requested document of the caller's organization.
// A document of another organization, a manipulated path and a stored key
// of another document of the same organization presign nothing.
func TestPresignOnlyForTheRequestedDocument(t *testing.T) {
	repo, blobs, mux := securityFixture(t)
	a := newDoc(t, repo, "org-1", "a")
	b := newDoc(t, repo, "org-1", "b")
	foreign := newDoc(t, repo, "org-2", "c")
	keys := map[string]string{}
	for _, doc := range []struct{ org, id string }{{"org-1", a}, {"org-1", b}, {"org-2", foreign}} {
		w := requestAs(t, mux, doc.org, http.MethodPut, "/api/v1/documents/"+doc.id+"/content", "application/pdf", "%PDF-1.7 "+doc.id)
		var d Document
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &d) != nil {
			t.Fatalf("upload %s: %d %s", doc.id, w.Code, w.Body.String())
		}
		if err := repo.SetScanResult(context.Background(), doc.org, doc.id, 1, ScanClean, ""); err != nil {
			t.Fatal(err)
		}
		keys[doc.id] = d.StorageKey
	}

	// The requested document: exactly its key in the document bucket.
	w := requestAs(t, mux, "org-1", http.MethodGet, "/api/v1/documents/"+a+"/content", "", "")
	signed := blobs.take()
	if w.Code != http.StatusOK || len(signed) != 1 || signed[0] != documentBucket+"/"+keys[a] {
		t.Fatalf("download of a: %d %s, presigned %v, want only %s", w.Code, w.Body.String(), signed, keys[a])
	}

	// The document of another organization does not exist for org-1: 404,
	// nothing presigned, its key not named.
	for _, query := range []string{"", "?version=1"} {
		w = requestAs(t, mux, "org-1", http.MethodGet, "/api/v1/documents/"+foreign+"/content"+query, "", "")
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), keys[foreign]) || strings.Contains(w.Body.String(), "org-2") {
			t.Errorf("download of the foreign document%s: %d %s, want 404", query, w.Code, w.Body.String())
		}
	}

	// Manipulated paths reach no other document.
	for _, path := range []string{
		"/api/v1/documents/" + a + "%2F..%2F" + b + "/content",
		"/api/v1/documents/" + a + "%2F..%2F..%2Forg%2Forg-2/content",
		"/api/v1/documents/..%2F" + foreign + "/content",
		"/api/v1/documents/" + keys[foreign] + "/content",
	} {
		w = requestAs(t, mux, "org-1", http.MethodGet, path, "", "")
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "download_url") {
			t.Errorf("download over %s: %d %s, want no URL", path, w.Code, w.Body.String())
		}
	}

	// A stored key of another document of the same organization (left by
	// older data) is not presigned either: the key must lie below the
	// requested document.
	if _, err := repo.SetStorage(context.Background(), "org-1", a, keys[b], "application/pdf", 9); err != nil {
		t.Fatalf("plant the key of b: %v", err)
	}
	w = requestAs(t, mux, "org-1", http.MethodGet, "/api/v1/documents/"+a+"/content", "", "")
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "download_url") {
		t.Errorf("download of a with the key of b: %d %s, want 409 without URL", w.Code, w.Body.String())
	}
	if signed = blobs.take(); len(signed) != 0 {
		t.Fatalf("presigned outside the requested document: %v", signed)
	}
}
