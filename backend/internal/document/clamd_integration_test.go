package document_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/go-chi/chi/v5"
)

// The malware scan against a real clamd (SEC-09, E-42, WP-735). The CI job
// migrations starts clamd (the image of deploy/docker-compose) and sets
// TEST_CLAMD_ADDR; these tests are skipped only without that variable:
//
//	docker run -d --name clamd -p 3310:3310 -e CLAMAV_NO_FRESHCLAMD=true clamav/clamav:1.5.4@sha256:…
//	TEST_CLAMD_ADDR=localhost:3310 go test -run 'TestClamd' ./internal/document/

// clamdAddr returns TEST_CLAMD_ADDR or skips the test.
func clamdAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_CLAMD_ADDR")
	if addr == "" {
		t.Skip("TEST_CLAMD_ADDR not set (host:port of a clamd)")
	}
	return addr
}

// eicar is the EICAR anti-virus test file, assembled at run time so that
// this source file is not itself reported by a virus scanner.
func eicar() string {
	return `X5O!P%@AP[4\PZX54(P^)7CC)7}$` + "EICAR-STANDARD-ANTIVIRUS" + `-TEST-FILE!$H+H*`
}

// validPDF is a minimal, well-formed PDF with one empty page.
const validPDF = "%PDF-1.4\n" +
	"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
	"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
	"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>\nendobj\n" +
	"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000115 00000 n \n" +
	"trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n188\n%%EOF\n"

// TestClamdScansEICARAndPDF streams both files to the real clamd with the
// client of the job (zINSTREAM): EICAR is a finding with its signature, the
// PDF is clean.
func TestClamdScansEICARAndPDF(t *testing.T) {
	c := &document.Clamd{Addr: clamdAddr(t), Timeout: 30 * time.Second}
	ctx := context.Background()
	infected, sig, err := c.Scan(ctx, strings.NewReader(eicar()))
	if err != nil || !infected || !strings.Contains(strings.ToLower(sig), "eicar") {
		t.Fatalf("EICAR: infected %v, signature %q, error %v; want a finding with the EICAR signature", infected, sig, err)
	}
	infected, sig, err = c.Scan(ctx, strings.NewReader(validPDF))
	if err != nil || infected || sig != "" {
		t.Fatalf("valid PDF: infected %v, signature %q, error %v; want clean", infected, sig, err)
	}
}

// TestClamdDocumentScanJob runs the job document.scan of an upload against
// PostgreSQL and the real clamd: an EICAR text file becomes infected with
// the signature of clamd and stays locked, a valid PDF becomes clean and
// can be downloaded.
//
// It needs TEST_CLAMD_ADDR and PostgreSQL (TEST_DATABASE_URL, see
// scopetest.Seed).
func TestClamdDocumentScanJob(t *testing.T) {
	addr := clamdAddr(t)
	w := &versionWorld{linkWorld: newLinkWorld(t), blobDir: t.TempDir()}
	f := w.f
	ctx := context.Background()
	blobs := blob.NewFileStore(w.blobDir)
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	queue := jobs.NewQueue(svc, nil)
	scanner := document.NewScanner(w.repo, blobs, config.DocumentSettings{ClamdAddr: addr, ClamdTimeout: 30 * time.Second}).
		WithPool(f.App)
	queue.Handle(jobs.Kind{Name: document.ScanJobKind, MaxAttempts: 5, Timeout: 2 * time.Minute,
		Backoff: jobs.ExponentialBackoff(30*time.Second, 15*time.Minute)}, scanner.RunJob)
	w.mux = chi.NewRouter()
	document.NewHandler(w.repo, blobs).WithScans(queue).RegisterRoutes(w.mux)

	scan := func(t *testing.T, title, contentType, body string) string {
		t.Helper()
		d := w.doc(t, title, "asset", w.asset1)
		if rec := w.call(w.orgScope(), http.MethodPut, "/api/v1/documents/"+d+"/content", contentType, body); rec.Code != http.StatusOK {
			t.Fatalf("upload %s: %d %s", title, rec.Code, rec.Body.String())
		}
		id := w.scalar(t, `SELECT id::text FROM job WHERE organization_id = $1 AND idempotency_key = $2`, f.OrgA, "document.scan:"+d+":1")
		job, err := svc.Get(ctx, f.OrgA, id)
		if err != nil {
			t.Fatal(err)
		}
		if out := queue.Process(ctx, deliveryFor(t, job)); !out.Ran {
			t.Fatalf("scan job of %s did not run: %+v", title, out)
		}
		return d
	}

	infected := scan(t, "clamd-eicar", "text/plain", eicar())
	got := w.scalar(t, `SELECT scan_status || '|' || coalesce(scan_signature, '') FROM document_version WHERE document_id = $1`, infected)
	status, sig, _ := strings.Cut(got, "|")
	if status != document.ScanInfected || !strings.Contains(strings.ToLower(sig), "eicar") {
		t.Fatalf("EICAR upload after the scan: %s, want infected with the EICAR signature", got)
	}
	if rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/"+infected+"/content", "", ""); rec.Code != http.StatusConflict {
		t.Fatalf("download of the infected version: %d, want 409", rec.Code)
	}

	clean := scan(t, "clamd-pdf", "application/pdf", validPDF)
	if got := w.scalar(t, `SELECT scan_status || '|' || coalesce(scan_signature, '') FROM document_version WHERE document_id = $1`, clean); got != document.ScanClean+"|" {
		t.Fatalf("PDF upload after the scan: %s, want clean without signature", got)
	}
	if rec := w.call(w.orgScope(), http.MethodGet, "/api/v1/documents/"+clean+"/content", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("download of the clean version: %d %s", rec.Code, rec.Body.String())
	}
}
