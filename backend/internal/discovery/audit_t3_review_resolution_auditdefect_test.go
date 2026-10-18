//go:build auditdefect

package discovery_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-087 (CI-13, REC-03, TEN-05) P1 "'new' on resurrected_device creates an
// org-wide CI without identity: cross-client visibility and an endless review
// loop": discovery/review.go ActionNew builds the CI from name, type,
// manufacturer, model, serial, IP, hostname and FQDN only (no ClientID,
// PrimaryMAC, HardwareUUID), and reviewItemFromConflict stores no client_id.
// Requirement: the new CI belongs to the record's client, carries the
// identity (MAC), is invisible to another client's principal, and the next
// sighting updates it without a new resurrected_device review.
//
// WP-173 (CI-12, CH29) P2 "Review resolution 'merge' of probable_duplicate
// soft-deletes the duplicate instead of a CI-12 merge": resolveCIReview only
// calls ciRepo.Delete(dup). Requirement: references move to the survivor and
// the duplicate becomes a merge victim (merged_into_id, ci_merge snapshot,
// 301 tombstone).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3ResurrectedNewKeepsClientAndIdentity|TestAuditT3ProbableDuplicateMergeIsCIMerge' ./internal/discovery/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

func TestAuditT3ResurrectedNewKeepsClientAndIdentity(t *testing.T) {
	f := scopetest.Seed(t, "f4")
	bg := context.Background()
	const mac = "00:1b:21:a3:f4:01"
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	gone := f.CI(t, f.OrgA, f.Client1, "audit-t3-gone")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, gone, mac); err != nil {
		t.Fatal(err)
	}
	if err := cis.Delete(ctx, f.OrgA, gone); err != nil {
		t.Fatal(err)
	}
	rec := func() plugins.DeviceRecord {
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "audit-t3-back", IP: "192.0.2.244"}, time.Now().Add(-time.Minute))
		r.Identity.PrimaryMAC = mac
		r.ClientID = f.Client1
		return r
	}
	openReviews := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'resurrected_device' AND status = 'open'`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ingestRecords(t, mux, ctx, rec())
	var reviewID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'resurrected_device' AND status = 'open'`, f.OrgA).Scan(&reviewID); err != nil {
		t.Fatalf("precondition: no open resurrected_device review: %v", err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/review-items/"+reviewID+"/resolve", strings.NewReader(`{"action":"new"}`)).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("resolve new: %d %s", w.Code, w.Body.String())
	}
	var newID, client, gotMAC string
	if err := f.Admin.QueryRow(bg, `SELECT id::text, coalesce(client_id::text, '<NULL>'), coalesce(primary_mac::text, '<NULL>')
		FROM ci WHERE organization_id = $1 AND name = 'audit-t3-back' AND deleted_at IS NULL`, f.OrgA).Scan(&newID, &client, &gotMAC); err != nil {
		t.Fatalf("new CI: %v", err)
	}
	if client != f.Client1 {
		t.Errorf("new CI client_id = %s, want the record's client %s (org-wide CI of a client-bound device)", client, f.Client1)
	}
	if gotMAC != mac {
		t.Errorf("new CI primary_mac = %s, want %s (identity lost)", gotMAC, mac)
	}
	client2 := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	if _, err := ci.NewPGRepository(f.App).GetByID(client2, f.OrgA, newID); err == nil {
		t.Errorf("a principal restricted to client 2 can read the new client-1 CI %s (TEN-05)", newID)
	}
	resp := ingestRecords(t, mux, ctx, rec())
	if resp.Updated != 1 || resp.Created != 0 || resp.Resurrected != 0 || openReviews() != 0 {
		t.Errorf("next sighting after 'new': created=%d updated=%d resurrected=%d open resurrected_device reviews=%d; want 0/1/0/0 (the device must update its new CI, not loop through review)",
			resp.Created, resp.Updated, resp.Resurrected, openReviews())
	}
}

func TestAuditT3ProbableDuplicateMergeIsCIMerge(t *testing.T) {
	f := scopetest.Seed(t, "f5")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	existing := f.CI(t, f.OrgA, f.Client1, "audit-t3-existing")
	dup := f.CI(t, f.OrgA, f.Client1, "audit-t3-dup")
	peer := f.CI(t, f.OrgA, f.Client1, "audit-t3-peer")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
		f.OrgA, dup, peer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth9')`, f.OrgA, dup); err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'probable_duplicate', 'open', jsonb_build_object('ci_id', $2::text), ARRAY[$3::uuid]) RETURNING id::text`,
		f.OrgA, dup, existing).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/review-items/"+itemID+"/resolve",
		strings.NewReader(`{"action":"merge","ci_id":"`+existing+`"}`)).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("resolve merge: %d %s", w.Code, w.Body.String())
	}
	var mergedInto string
	var merges, relsOnSurvivor, nicsOnSurvivor int
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT coalesce(merged_into_id::text, '') FROM ci WHERE id = $1),
		(SELECT count(*) FROM ci_merge WHERE victim_id = $1),
		(SELECT count(*) FROM ci_relationship WHERE source_ci_id = $2 AND target_ci_id = $3),
		(SELECT count(*) FROM network_interface WHERE ci_id = $2 AND name = 'eth9')`, dup, existing, peer).
		Scan(&mergedInto, &merges, &relsOnSurvivor, &nicsOnSurvivor); err != nil {
		t.Fatal(err)
	}
	if mergedInto != existing || merges != 1 {
		t.Errorf("duplicate after review merge: merged_into_id=%q ci_merge rows=%d; want %s and 1 (CI-12 merge with tombstone and snapshot, not a soft delete)",
			mergedInto, merges, existing)
	}
	if relsOnSurvivor != 1 || nicsOnSurvivor != 1 {
		t.Errorf("references on the survivor after review merge: relationships=%d interfaces=%d; want 1 and 1 (they stay on the deleted duplicate)",
			relsOnSurvivor, nicsOnSurvivor)
	}
}
