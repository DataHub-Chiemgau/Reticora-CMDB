package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
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
	if err := ciRepo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv-01", SerialNumber: "SN-1", Attributes: map[string]any{}}); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(discoveryRepo, ciRepo)
	mux := chi.NewRouter()
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

	items, total, err := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 100, Offset: 0})
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

func TestReconcileMatchesPrimaryMAC(t *testing.T) {
	existing := []ci.Item{{ID: "ci-1", PrimaryMAC: "AA:BB:CC:DD:EE:FF"}}
	result := Reconcile(existing, IngestItem{PrimaryMAC: "aa-bb-cc-dd-ee-ff"})
	if result.Action != ReconcileMatched || result.MatchedCIID != "ci-1" || result.Criterion != "mac_addresses" {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
}

func TestReconcileMatchesHostnameFQDN(t *testing.T) {
	existing := []ci.Item{
		{ID: "ci-1", Hostname: "sw-core-01"},
		{ID: "ci-2", FQDN: "fw-01.example.net"},
		{ID: "ci-3", Name: "legacy-01"},
	}
	byHostname := Reconcile(existing, IngestItem{Hostname: "SW-CORE-01"})
	if byHostname.Action != ReconcileMatched || byHostname.MatchedCIID != "ci-1" || byHostname.Criterion != "hostname_fqdn" {
		t.Fatalf("hostname match: %+v", byHostname)
	}
	byFQDN := Reconcile(existing, IngestItem{FQDN: "fw-01.example.net"})
	if byFQDN.Action != ReconcileMatched || byFQDN.MatchedCIID != "ci-2" {
		t.Fatalf("fqdn match: %+v", byFQDN)
	}
	// Name fallback keeps sweep/SSH behavior where the name carries the hostname.
	byName := Reconcile(existing, IngestItem{Name: "legacy-01"})
	if byName.Action != ReconcileMatched || byName.MatchedCIID != "ci-3" {
		t.Fatalf("name fallback: %+v", byName)
	}
}

func TestReconcileMatchesHardwareUUIDColumn(t *testing.T) {
	existing := []ci.Item{{ID: "ci-1", HardwareUUID: "4C4C4544-004D-4210-8031-B8CAC04F5A32"}}
	result := Reconcile(existing, IngestItem{HardwareUUID: "4c4c4544-004d-4210-8031-b8cac04f5a32"})
	if result.Action != ReconcileMatched || result.Criterion != "hardware_uuid" {
		t.Fatalf("unexpected: %+v", result)
	}
}

func TestReconcileReportsValueConflicts(t *testing.T) {
	existing := []ci.Item{{
		ID:           "ci-1",
		SerialNumber: "SN-1",
		ManagementIP: "10.0.0.1",
		Hostname:     "old-name",
	}}
	result := Reconcile(existing, IngestItem{
		SerialNumber: "SN-1",
		ManagementIP: "10.0.0.9",
		Hostname:     "new-name",
	})
	if result.Action != ReconcileMatched {
		t.Fatalf("action = %s, want matched", result.Action)
	}
	if len(result.ValueConflicts) != 2 {
		t.Fatalf("ValueConflicts = %v, want management_ip + hostname", result.ValueConflicts)
	}
}

func TestReconcileNoValueConflictsWhenIncomingEmpty(t *testing.T) {
	existing := []ci.Item{{ID: "ci-1", SerialNumber: "SN-1", ManagementIP: "10.0.0.1"}}
	result := Reconcile(existing, IngestItem{SerialNumber: "SN-1"})
	if result.Action != ReconcileMatched {
		t.Fatalf("action = %s, want matched", result.Action)
	}
	if len(result.ValueConflicts) != 0 {
		t.Fatalf("ValueConflicts = %v, want none (empty incoming values never conflict)", result.ValueConflicts)
	}
}

func TestSourceTrustRanking(t *testing.T) {
	if SourceTrust(ci.SourceRedfish) <= SourceTrust(ci.SourceSNMP) {
		t.Error("redfish must outrank snmp")
	}
	if SourceTrust(ci.SourceSNMP) <= SourceTrust(ci.SourceSweep) {
		t.Error("snmp must outrank sweep")
	}
	if SourceTrust("does-not-exist") != SourceTrust("") {
		t.Error("unknown sources must rank like unset")
	}
}

func TestShouldApplyAttribute(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	stale := now.Add(-30 * 24 * time.Hour)

	// Equal or higher trust always applies.
	if !ShouldApplyAttribute(ci.SourceSweep, ci.SourceSNMP, recent, now, 7*24*time.Hour) {
		t.Error("higher-trust source must overwrite fresh data")
	}
	if !ShouldApplyAttribute(ci.SourceSNMP, ci.SourceSNMP, recent, now, 7*24*time.Hour) {
		t.Error("equal-trust source must overwrite")
	}
	// Lower trust is rejected while the stored value is fresh.
	if ShouldApplyAttribute(ci.SourceSNMP, ci.SourceSweep, recent, now, 7*24*time.Hour) {
		t.Error("lower-trust source must not overwrite fresh data")
	}
	// Lower trust applies once the stored value is stale.
	if !ShouldApplyAttribute(ci.SourceSNMP, ci.SourceSweep, stale, now, 7*24*time.Hour) {
		t.Error("lower-trust source must overwrite stale data")
	}
	// Without a staleness threshold lower trust never applies.
	if ShouldApplyAttribute(ci.SourceSNMP, ci.SourceSweep, stale, now, 0) {
		t.Error("staleness threshold 0 must reject lower-trust overwrites")
	}
}

func TestBulkIngestQueuesValueConflictsForReview(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	ciRepo := ci.NewMemoryRepository()
	if err := ciRepo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv-01", SerialNumber: "SN-1", ManagementIP: "10.0.0.1", DiscoverySource: ci.SourceRedfish, Attributes: map[string]any{}}); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(discoveryRepo, ciRepo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	payload := `{"collector_id":"col-1","items":[{"fingerprint":{},"ci_type_name":"server","name":"srv-01","serial_number":"SN-1","management_ip":"10.0.0.9","source":"snmp"}]}`
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
	if resp.Updated != 1 || resp.ReviewItems != 1 {
		t.Fatalf("expected 1 update + 1 review item, got %+v", resp)
	}

	// The low-trust sweep/SNMP sighting must not clobber the fresh
	// redfish-sourced management IP.
	items, _, err := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ManagementIP != "10.0.0.1" {
		t.Fatalf("source trust not enforced: %+v", items)
	}

	reviewItems, total, err := discoveryRepo.ListReviewItems(context.Background(), "org-1", ReviewFilter{}, api.PaginationParams{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || reviewItems[0].Kind != ReviewKindConflictingValues {
		t.Fatalf("expected conflicting_values review item, got %+v", reviewItems)
	}
}
