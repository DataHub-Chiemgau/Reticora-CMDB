package discovery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestReconcilePriorityOrder(t *testing.T) {
	existing := []ci.Item{{ID: "ci-1", SerialNumber: "SN-1", Attributes: map[string]any{"fingerprint": map[string]any{"hardware_uuid": "uuid-1", "mac_addresses": []any{"aa:bb:cc"}}}, ManagementIP: "10.0.0.1", CITypeID: "server", Name: "srv-01"}}
	result := Reconcile(existing, IngestItem{SerialNumber: "SN-1", Fingerprint: map[string]any{"hardware_uuid": "uuid-2"}, Name: "other"})
	if result.Action != ReconcileMatched || result.MatchedCIID != "ci-1" || result.Criterion != "serial_number" {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
}

func TestReconcileConflict(t *testing.T) {
	existing := []ci.Item{{ID: "ci-1", Name: "srv-01"}, {ID: "ci-2", Name: "srv-01"}}
	result := Reconcile(existing, IngestItem{Name: "srv-01"})
	if result.Action != ReconcileConflict || len(result.CandidateCIIDs) != 2 {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
}

func TestBulkIngestUsesReconciliation(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	ciRepo := ci.NewMemoryRepository()
	if err := ciRepo.Create(&ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv-01", SerialNumber: "SN-1", Attributes: map[string]any{}}); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(discoveryRepo, ciRepo)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	payload := `{"collector_id":"col-1","items":[{"fingerprint":{"hardware_uuid":"uuid-1"},"raw_data":{"hostname":"srv-01"},"ci_type_name":"server","name":"srv-01","serial_number":"SN-1"},{"fingerprint":{"hardware_uuid":"uuid-2"},"raw_data":{"hostname":"srv-02"},"ci_type_name":"server","name":"srv-02","serial_number":"SN-2"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", bytes.NewBufferString(payload))
	req = discoveryTenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp BulkIngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Updated != 1 || resp.Created != 1 || resp.Conflicts != 0 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	items, total, err := ciRepo.List("org-1", ci.FilterParams{}, api.PaginationParams{Limit: 100, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 items, got total=%d len=%d", total, len(items))
	}
}

func discoveryTenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}
