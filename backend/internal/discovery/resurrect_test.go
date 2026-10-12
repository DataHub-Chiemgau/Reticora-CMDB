package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

// deletedInventory adds soft-deleted CIs to the memory repository.
type deletedInventory struct {
	*discovery.MemoryRepository
	deleted []ci.Item
}

func (d *deletedInventory) DeletedIdentityCandidates(context.Context, string, *discovery.IdentityQuery) ([]ci.Item, error) {
	return d.deleted, nil
}

func ingestRecords(t *testing.T, mux http.Handler, ctx context.Context, records ...plugins.DeviceRecord) discovery.BulkIngestResponse {
	t.Helper()
	body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	var resp discovery.BulkIngestResponse
	if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func resurrectRecord(serial string) plugins.DeviceRecord {
	return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "back-" + serial, IP: "192.0.2.87", Serial: serial},
		time.Now().Add(-time.Minute))
}

// TestDeletedDeviceSeenAgainIsReviewed covers WP-087 (CI-13) without a
// database: a device that matches a soft-deleted CI creates no CI but one
// resurrected_device review, also when it is seen again.
func TestDeletedDeviceSeenAgainIsReviewed(t *testing.T) {
	repo := &deletedInventory{MemoryRepository: discovery.NewMemoryRepository(),
		deleted: []ci.Item{{ID: "gone", CITypeID: "server", SerialNumber: "RES-SN"}}}
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})

	if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN")); resp.Created != 0 || resp.Resurrected != 1 || resp.ReviewItems != 1 {
		t.Fatalf("deleted device seen again: %+v", resp)
	}
	if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN")); resp.Created != 0 || resp.Resurrected != 1 || resp.ReviewItems != 0 {
		t.Fatalf("seen once more: %+v", resp)
	}
	reviews, _, _ := repo.ListReviewItems(context.Background(), "org-1", discovery.ReviewFilter{Kind: discovery.ReviewKindResurrectedDevice},
		api.PaginationParams{Limit: 10})
	if len(reviews) != 1 || len(reviews[0].CandidateCIIDs) != 1 || reviews[0].CandidateCIIDs[0] != "gone" {
		t.Fatalf("reviews: %+v", reviews)
	}
	if _, total, _ := cis.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 1}); total != 0 {
		t.Errorf("CIs created: %d", total)
	}
	// Another device still becomes a CI.
	if resp := ingestRecords(t, mux, ctx, resurrectRecord("OTHER-SN")); resp.Created != 1 || resp.Resurrected != 0 {
		t.Errorf("unrelated device: %+v", resp)
	}
}

// TestResurrectedDeviceIsReactivatedOrNew covers WP-087 (CI-13, PRI-03)
// against PostgreSQL: identity resolution includes soft-deleted CIs, a hit
// is reviewed instead of creating a duplicate, merge reactivates the deleted
// CI (change history and audit) and create makes a new CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestResurrectedDeviceIsReactivatedOrNew(t *testing.T) {
	f := scopetest.Seed(t, "79")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	repo := discovery.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	deleteDevice := func(name, serial string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, "", name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = $2 WHERE id = $1`, id, serial); err != nil {
			t.Fatal(err)
		}
		if err := cis.Delete(ctx, f.OrgA, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	reviewOf := func(ciID string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'resurrected_device'
			AND status = 'open' AND $2::uuid = ANY(candidate_ci_ids)`, f.OrgA, ciID).Scan(&id); err != nil {
			t.Fatalf("review of %s: %v", ciID, err)
		}
		return id
	}
	resolve := func(reviewID, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/review-items/"+reviewID+"/resolve", strings.NewReader(body))
		mux.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("resolve %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	liveCIs := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`,
			f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	gone := deleteDevice("res-gone", "RES-SN-1")
	for range 2 {
		if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN-1")); resp.Created != 0 || resp.Resurrected != 1 {
			t.Fatalf("deleted device seen again: %+v", resp)
		}
	}
	if n := liveCIs("RES-SN-1"); n != 0 {
		t.Fatalf("duplicate created: %d live CIs", n)
	}

	// Merge reactivates the deleted CI.
	resolve(reviewOf(gone), `{"action":"merge","ci_id":"`+gone+`"}`)
	if n := liveCIs("RES-SN-1"); n != 1 {
		t.Fatalf("reactivated CIs: %d", n)
	}
	var audits, changes int
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.restored'),
		(SELECT count(*) FROM ci_change WHERE ci_id = $1::uuid AND change_type = 'reconciliation_decision' AND field_name = 'deleted_at')`,
		gone).Scan(&audits, &changes); err != nil || audits != 1 || changes != 1 {
		t.Errorf("restore recorded: audit %d, change %d, %v", audits, changes, err)
	}
	// The reactivated CI matches the next record directly.
	if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN-1")); resp.Updated != 1 || resp.Resurrected != 0 {
		t.Errorf("after reactivation: %+v", resp)
	}

	// Create makes a new CI and leaves the deleted one deleted.
	other := deleteDevice("res-other", "RES-SN-2")
	ingestRecords(t, mux, ctx, resurrectRecord("RES-SN-2"))
	resolve(reviewOf(other), `{"action":"create"}`)
	var deletedStill bool
	if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1`, other).Scan(&deletedStill); err != nil || !deletedStill {
		t.Errorf("deleted CI after create: deleted=%v, %v", deletedStill, err)
	}
	if n := liveCIs("RES-SN-2"); n != 1 {
		t.Errorf("new CIs after create: %d", n)
	}
}
