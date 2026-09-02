package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/go-chi/chi/v5"
)

// stubProvenance records calls and reports configured fields as protected.
type stubProvenance struct {
	mu        sync.Mutex
	protected map[string]bool
	recorded  map[string]any
}

func (s *stubProvenance) RecordDiscovered(_ context.Context, orgID, ciID, fieldName string, value any, source string) (*FieldProvenance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorded == nil {
		s.recorded = map[string]any{}
	}
	s.recorded[ciID+"/"+fieldName] = value
	return &FieldProvenance{}, nil
}

func (s *stubProvenance) IsProtected(_ context.Context, orgID, ciID, fieldName string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protected[ciID+"/"+fieldName], nil
}

func ingest(t *testing.T, h *Handler, payload string) BulkIngestResponse {
	t.Helper()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
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
	return resp
}

func TestBulkIngestRecordsProvenance(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	prov := &stubProvenance{protected: map[string]bool{}}

	h := NewHandler(discoveryRepo, ciRepo).WithProvenance(prov)
	resp := ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv-01","serial_number":"SN-1","attributes":{"rack":"R7"}}]}`)
	if resp.Created != 1 {
		t.Fatalf("expected 1 created, got %+v", resp)
	}
	items, _, err := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 100, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	ciID := items[0].ID
	for _, field := range []string{"name", "manufacturer", "model", "serial_number", "management_ip", "rack"} {
		if _, ok := prov.recorded[ciID+"/"+field]; !ok {
			t.Errorf("expected provenance record for %s", field)
		}
	}
	if got := prov.recorded[ciID+"/rack"]; got != "R7" {
		t.Errorf("expected attribute provenance rack=R7, got %v", got)
	}
}

func TestBulkIngestProtectedOverrideWins(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	existing := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "corrected-name", SerialNumber: "SN-1", Attributes: map[string]any{}}
	if err := ciRepo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	prov := &stubProvenance{protected: map[string]bool{existing.ID + "/name": true}}

	h := NewHandler(discoveryRepo, ciRepo).WithProvenance(prov)
	resp := ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"discovered-name","serial_number":"SN-1"}]}`)
	if resp.Updated != 1 {
		t.Fatalf("expected 1 updated, got %+v", resp)
	}
	if resp.ProtectedOverrides != 1 {
		t.Fatalf("expected protected_overrides=1, got %+v", resp)
	}
	updated, err := ciRepo.GetByID(context.Background(), "org-1", existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "corrected-name" {
		t.Fatalf("protected override must not be overwritten by discovery, got name %q", updated.Name)
	}
	// Provenance still records the discovered value so drift stays visible.
	if got := prov.recorded[existing.ID+"/name"]; got != "discovered-name" {
		t.Fatalf("expected discovered value recorded as provenance, got %v", got)
	}
}

func TestBulkIngestUnprotectedFieldUpdates(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	existing := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "old-name", SerialNumber: "SN-1", Attributes: map[string]any{}}
	if err := ciRepo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	prov := &stubProvenance{protected: map[string]bool{}}

	h := NewHandler(discoveryRepo, ciRepo).WithProvenance(prov)
	resp := ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"new-name","serial_number":"SN-1"}]}`)
	if resp.Updated != 1 || resp.ProtectedOverrides != 0 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	updated, err := ciRepo.GetByID(context.Background(), "org-1", existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "new-name" {
		t.Fatalf("unprotected field should follow discovery, got name %q", updated.Name)
	}
}

// TestBulkIngestPersistsDiscoveredAttributes guards the defect where discovery
// recorded custom attributes as provenance but never wrote them onto the CI,
// making them invisible to CI detail, search and attribute filtering.
func TestBulkIngestPersistsDiscoveredAttributes(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	prov := &stubProvenance{protected: map[string]bool{}}
	h := NewHandler(discoveryRepo, ciRepo).WithProvenance(prov)

	// Created path.
	ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv","serial_number":"SN-1","attributes":{"ram_gb":32,"cpu_model":"Xeon"}}]}`)
	items, _, err := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 CI, got %d", len(items))
	}
	if got := items[0].Attributes["ram_gb"]; got != float64(32) {
		t.Fatalf("discovered attribute must be persisted on create, got %#v", got)
	}
	if got := items[0].Attributes["cpu_model"]; got != "Xeon" {
		t.Fatalf("discovered attribute must be persisted on create, got %#v", got)
	}

	// Matched path: a later run updates the value.
	ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv","serial_number":"SN-1","attributes":{"ram_gb":64}}]}`)
	updated, err := ciRepo.GetByID(context.Background(), "org-1", items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Attributes["ram_gb"]; got != float64(64) {
		t.Fatalf("unprotected attribute should follow discovery, got %#v", got)
	}
	if got := updated.Attributes["cpu_model"]; got != "Xeon" {
		t.Fatalf("attributes must merge, not replace; cpu_model was lost: %#v", updated.Attributes)
	}
}

// TestBulkIngestProtectedAttributeOverrideWins is the canonical spec §17 case:
// discovery reports RAM = 32, an operator sets a protected override of 64, and
// a subsequent discovery run must not silently destroy the manual value.
func TestBulkIngestProtectedAttributeOverrideWins(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	existing := &ci.Item{
		OrganizationID: "org-1", CITypeID: "server", Name: "srv", SerialNumber: "SN-1",
		Attributes: map[string]any{"ram_gb": float64(64), "cpu_model": "Xeon"},
	}
	if err := ciRepo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	prov := &stubProvenance{protected: map[string]bool{existing.ID + "/ram_gb": true}}
	h := NewHandler(discoveryRepo, ciRepo).WithProvenance(prov)

	ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv","serial_number":"SN-1","attributes":{"ram_gb":32,"cpu_model":"Xeon Gold"}}]}`)

	updated, err := ciRepo.GetByID(context.Background(), "org-1", existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Attributes["ram_gb"]; got != float64(64) {
		t.Fatalf("protected attribute override must survive discovery, got %#v", got)
	}
	if got := updated.Attributes["cpu_model"]; got != "Xeon Gold" {
		t.Fatalf("unprotected attribute should still follow discovery, got %#v", got)
	}
	// The discovered value stays visible as provenance so the drift is reviewable.
	if got := prov.recorded[existing.ID+"/ram_gb"]; got != float64(32) {
		t.Fatalf("expected discovered value recorded as provenance, got %#v", got)
	}
}
