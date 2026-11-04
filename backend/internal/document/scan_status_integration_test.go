package document_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
)

// scanBackfill returns the backfill statement of migration 000222 (the
// UPDATE of document_version) without its semicolon.
func scanBackfill(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/000222_document_scan_status.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "UPDATE document_version")
	if i < 0 {
		t.Fatal("migration 000222 has no backfill")
	}
	return src[i : i+strings.Index(src[i:], ";")]
}

// TestDocumentScanStatus covers WP-733 (SEC-09, MGT-01, E-42) against
// PostgreSQL: the backfill of migration 000222 sets every unscanned version
// to pending_scan; the download answers 409 document-pending-scan for
// pending_scan and scan_failed and 409 document-quarantined for infected,
// with and without ?version=, and 200 only after SetScanResult(clean). A
// quarantined document keeps its links, so it stays invisible to a
// principal of another client, and takes no new link (409).
// SetScanResult joins a transaction of the context; PendingScans returns
// the open versions of the own organization only.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentScanStatus(t *testing.T) {
	w := newVersionWorld(t, false)
	f := w.f
	ctx := context.Background()

	// Backfill: a version without scan becomes pending_scan, a scanned one
	// keeps its result.
	legacy := w.doc(t, "scan-legacy")
	for n, row := range []string{"'clean', NULL", "'clean', now()"} {
		if _, err := f.Admin.Exec(ctx, `INSERT INTO document_version (organization_id, document_id, version, storage_key, size, mime, scan_status, scanned_at)
			VALUES ($1, $2, $3, $4, 1, 'text/plain', `+row+`)`, f.OrgA, legacy, n+1, "org/"+f.OrgA+"/documents/"+legacy+"/"+f.ID()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Admin.Exec(ctx, scanBackfill(t)+" AND document_id = $1", legacy); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := w.scalar(t, `SELECT string_agg(version::text || ':' || scan_status, ',' ORDER BY version) FROM document_version WHERE document_id = $1`,
		legacy); got != "1:pending_scan,2:clean" {
		t.Fatalf("versions after the backfill: %s, want 1:pending_scan,2:clean", got)
	}
	if got := w.scalar(t, `SELECT count(*)::text FROM pg_indexes WHERE indexname = 'idx_document_version_scan_open'
		AND indexdef LIKE '%WHERE (scan_status <> ''clean''::text)%'`); got != "1" {
		t.Fatalf("partial index idx_document_version_scan_open: %s", got)
	}

	// Upload: pending_scan, the download is refused until the scan is clean.
	d := w.doc(t, "scan-doc", "asset", w.asset1)
	base := "/api/v1/documents/" + d
	if rec := w.call(w.orgScope(), http.MethodPut, base+"/content", "text/plain", "scan content"); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if got := w.scalar(t, `SELECT scan_status || '|' || (scanned_at IS NULL)::text FROM document_version WHERE document_id = $1`, d); got != "pending_scan|true" {
		t.Fatalf("new version %s, want pending_scan without scanned_at", got)
	}
	for _, c := range []struct {
		status, want string
		code         int
	}{
		{document.ScanPending, document.ProblemPendingScan, http.StatusConflict},
		{document.ScanFailed, document.ProblemPendingScan, http.StatusConflict},
		{document.ScanInfected, document.ProblemQuarantined, http.StatusConflict},
		{document.ScanClean, "download_url", http.StatusOK},
	} {
		if err := w.repo.SetScanResult(w.orgCtx, f.OrgA, d, 1, c.status, ""); err != nil {
			t.Fatalf("SetScanResult %s: %v", c.status, err)
		}
		for _, query := range []string{"", "?version=1"} {
			rec := w.call(w.orgScope(), http.MethodGet, base+"/content"+query, "", "")
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) ||
				(c.code != http.StatusOK && strings.Contains(rec.Body.String(), "download_url")) {
				t.Fatalf("download%s at %s: %d %s, want %d %s", query, c.status, rec.Code, rec.Body.String(), c.code, c.want)
			}
		}
		rec := w.call(w.orgScope(), http.MethodGet, base, "", "")
		var got document.Document
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ScanStatus != c.status ||
			(got.ScannedAt == nil) != (c.status == document.ScanPending) {
			t.Fatalf("GET document at %s: %d %s", c.status, rec.Code, rec.Body.String())
		}
	}

	// SetScanResult joins the transaction of the context: a rollback keeps
	// the status.
	rollback := errors.New("rollback")
	if err := database.WithRequestTenantTx(w.orgCtx, f.App, f.OrgA, func(txCtx context.Context) error {
		if err := w.repo.SetScanResult(txCtx, f.OrgA, d, 1, document.ScanInfected, "Win.Test.EICAR_HDB-1"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("transaction: %v", err)
	}
	if got := w.scalar(t, `SELECT scan_status FROM document_version WHERE document_id = $1`, d); got != document.ScanClean {
		t.Fatalf("status after the rollback %s, want clean", got)
	}

	// Quarantine is a status: blob, key and links stay.
	key := w.scalar(t, `SELECT storage_key FROM document WHERE id = $1`, d)
	if err := w.repo.SetScanResult(w.orgCtx, f.OrgA, d, 1, document.ScanInfected, "Win.Test.EICAR_HDB-1"); err != nil {
		t.Fatal(err)
	}
	if got := w.scalar(t, `SELECT scan_status || '|' || scan_signature || '|' || storage_key || '|' || (SELECT count(*) FROM document_link WHERE document_id = $1)::text
		FROM document_version WHERE document_id = $1`, d); got != "infected|Win.Test.EICAR_HDB-1|"+key+"|1" {
		t.Fatalf("quarantined version %s", got)
	}
	if n := w.blobs(t); n != 1 {
		t.Fatalf("blobs %d, want 1", n)
	}
	client2 := database.OrgWideScope(f.OrgA, w.user)
	client2.Clients = database.ScopeIDs(f.Client2)
	for _, path := range []string{base, base + "/content"} {
		if rec := w.call(&client2, http.MethodGet, path, "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("client-2 GET %s of the quarantined client-1 document: %d %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	rec := w.call(&client2, http.MethodGet, "/api/v1/documents", "", "")
	var list api.ListResponse[document.Document]
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("client-2 list: %d %s", rec.Code, rec.Body.String())
	}
	for _, item := range list.Data {
		if item.ID == d {
			t.Fatal("client-2 list shows the quarantined client-1 document")
		}
	}
	if rec = w.call(&w.client1Scope, http.MethodGet, base, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"scan_status":"infected"`) {
		t.Fatalf("client-1 GET of the quarantined document: %d %s", rec.Code, rec.Body.String())
	}

	// No new link to a quarantined document.
	rec = w.call(w.orgScope(), http.MethodPost, base+"/links", "application/json", `{"entity_type":"asset","entity_id":"`+w.asset2+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), document.ProblemQuarantined) {
		t.Fatalf("link to the quarantined document: %d %s, want 409 document-quarantined", rec.Code, rec.Body.String())
	}
	if _, err := w.link(w.orgCtx, d, "asset", w.asset2); !errors.Is(err, document.ErrQuarantined) {
		t.Fatalf("LinkDocument on the quarantined document: %v, want ErrQuarantined", err)
	}
	if got := w.linkCount(t, d); got != "1/1" {
		t.Fatalf("links of the quarantined document %s, want 1/1", got)
	}

	// PendingScans: the open versions of the own organization only.
	other := f.ID()
	if _, err := f.Admin.Exec(ctx, `INSERT INTO document (id, organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, $2, 'scan-org-b', 'b.txt', '', $3)`,
		other, f.OrgB, f.AppUser(t, f.OrgB, "scan-b")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(ctx, `INSERT INTO document_version (organization_id, document_id, version, storage_key, size, mime)
		VALUES ($1, $2, 1, $3, 1, 'text/plain')`, f.OrgB, other, "org/"+f.OrgB+"/documents/"+other+"/"+f.ID()); err != nil {
		t.Fatal(err)
	}
	if err := w.repo.SetScanResult(w.orgCtx, f.OrgA, legacy, 2, document.ScanFailed, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := w.repo.PendingScans(w.orgCtx, f.OrgA, 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range pending {
		if v.OrganizationID != f.OrgA {
			t.Fatalf("PendingScans returned version %s of organization %s", v.ID, v.OrganizationID)
		}
		got = append(got, v.DocumentID+":"+v.ScanStatus)
	}
	want := []string{legacy + ":pending_scan", legacy + ":scan_failed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("PendingScans %v, want %v", got, want)
	}
	if limited, limErr := w.repo.PendingScans(w.orgCtx, f.OrgA, 1); limErr != nil || len(limited) != 1 {
		t.Fatalf("PendingScans with limit 1: %d %v", len(limited), limErr)
	}
	if err := w.repo.SetScanResult(w.orgCtx, f.OrgA, d, 2, document.ScanClean, ""); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("SetScanResult of a missing version: %v, want ErrNotFound", err)
	}
}
