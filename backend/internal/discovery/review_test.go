package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/go-chi/chi/v5"
)

func newTestRouter(h *Handler) *chi.Mux {
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

func TestReviewItemFromConflict(t *testing.T) {
	incoming := IngestItem{
		CITypeName:   "server",
		Name:         "srv1",
		SerialNumber: "SER-1",
		ManagementIP: "10.0.0.5",
	}
	result := ReconcileResult{
		Action:         ReconcileConflict,
		CandidateCIIDs: []string{"ci-a", "ci-b"},
		Criterion:      "serial_number",
	}

	item := reviewItemFromConflict("org-1", incoming, result)
	if item == nil {
		t.Fatal("expected review item, got nil")
	}
	if item.Kind != ReviewKindAmbiguousIdentity || item.Status != ReviewStatusOpen {
		t.Errorf("unexpected kind/status: %s/%s", item.Kind, item.Status)
	}
	if len(item.CandidateCIIDs) != 2 {
		t.Errorf("expected 2 candidates, got %d", len(item.CandidateCIIDs))
	}
	if item.Payload["criterion"] != "serial_number" {
		t.Errorf("expected criterion serial_number, got %v", item.Payload["criterion"])
	}
	if item.Payload["name"] != "srv1" {
		t.Errorf("expected name srv1, got %v", item.Payload["name"])
	}

	// No candidates yields no review item.
	if reviewItemFromConflict("org-1", incoming, ReconcileResult{Action: ReconcileConflict}) != nil {
		t.Error("expected nil review item when no candidates")
	}
}

func TestBulkIngestPersistsReviewItems(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	// Two existing CIs sharing a serial number → ambiguous match.
	ciRepo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", Name: "a", SerialNumber: "DUP", CITypeID: "server"})
	ciRepo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", Name: "b", SerialNumber: "DUP", CITypeID: "server"})

	repo := NewMemoryRepository()
	h := NewHandler(repo, ciRepo)
	mux := newTestRouter(h)

	body := `{"collector_id":"c1","items":[{"fingerprint":{},"raw_data":{},"ci_type_name":"server","name":"c","serial_number":"DUP"}]}`
	resp := doJSON(t, mux, "POST", "/api/v1/ingest/bulk", body)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", resp.Code, resp.Body.String())
	}
	var ingest BulkIngestResponse
	json.Unmarshal(resp.Body.Bytes(), &ingest)
	if ingest.Conflicts != 1 || ingest.ReviewItems != 1 {
		t.Fatalf("expected 1 conflict/review item, got %+v", ingest)
	}

	// The review item should be listable.
	listResp := doJSON(t, mux, "GET", "/api/v1/discovery/review-items", "")
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.Code)
	}
	var list api.ListResponse[ReviewItem]
	json.Unmarshal(listResp.Body.Bytes(), &list)
	if list.Total != 1 || len(list.Data) != 1 {
		t.Fatalf("expected 1 review item, got %+v", list)
	}
	if len(list.Data[0].CandidateCIIDs) != 2 {
		t.Errorf("expected 2 candidate CIs, got %v", list.Data[0].CandidateCIIDs)
	}
}

func TestResolveReviewItemMerge(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	a := &ci.Item{OrganizationID: "org-1", Name: "a", SerialNumber: "DUP", CITypeID: "server"}
	b := &ci.Item{OrganizationID: "org-1", Name: "b", SerialNumber: "DUP", CITypeID: "server"}
	ciRepo.Create(context.Background(), a)
	ciRepo.Create(context.Background(), b)

	repo := NewMemoryRepository()
	h := NewHandler(repo, ciRepo)
	mux := newTestRouter(h)

	item := &ReviewItem{
		OrganizationID: "org-1",
		Kind:           ReviewKindAmbiguousIdentity,
		Status:         ReviewStatusOpen,
		CandidateCIIDs: []string{a.ID, b.ID},
		Payload:        map[string]any{"management_ip": "10.0.0.5"},
	}
	repo.CreateReviewItem(item)

	// Merge into an invalid candidate → 400.
	bad := doJSON(t, mux, "POST", "/api/v1/discovery/review-items/"+item.ID+"/resolve", `{"action":"merge","ci_id":"nope"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid candidate, got %d", bad.Code)
	}

	// Merge into a valid candidate.
	good := doJSON(t, mux, "POST", "/api/v1/discovery/review-items/"+item.ID+"/resolve", `{"action":"merge","ci_id":"`+a.ID+`"}`)
	if good.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", good.Code, good.Body.String())
	}
	var resolved ReviewItem
	json.Unmarshal(good.Body.Bytes(), &resolved)
	if resolved.Status != ReviewStatusResolved {
		t.Errorf("expected resolved, got %s", resolved.Status)
	}

	// A second resolve should conflict.
	again := doJSON(t, mux, "POST", "/api/v1/discovery/review-items/"+item.ID+"/resolve", `{"action":"dismiss"}`)
	if again.Code != http.StatusConflict {
		t.Errorf("expected 409 on re-resolve, got %d", again.Code)
	}
}

func TestResolveReviewItemCreate(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	repo := NewMemoryRepository()
	h := NewHandler(repo, ciRepo)
	mux := newTestRouter(h)

	item := &ReviewItem{
		OrganizationID: "org-1",
		Kind:           ReviewKindAmbiguousIdentity,
		Status:         ReviewStatusOpen,
		CandidateCIIDs: []string{"ci-a"},
		Payload:        map[string]any{"name": "brand-new", "ci_type_name": "server"},
	}
	repo.CreateReviewItem(item)

	resp := doJSON(t, mux, "POST", "/api/v1/discovery/review-items/"+item.ID+"/resolve", `{"action":"create"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	items, _, _ := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10, Offset: 0})
	if len(items) != 1 || items[0].Name != "brand-new" {
		t.Fatalf("expected new CI created, got %+v", items)
	}
}

func TestBulkIngestDerivesTopology(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	// Pre-existing PDU neighbor addressable by management IP.
	pdu := &ci.Item{OrganizationID: "org-1", Name: "pdu1", CITypeID: "pdu", ManagementIP: "10.0.0.2"}
	ciRepo.Create(context.Background(), pdu)

	relRepo := relationship.NewMemoryRepository()
	repo := NewMemoryRepository()
	h := NewHandler(repo, ciRepo, relRepo)
	mux := newTestRouter(h)

	body := `{"collector_id":"c1","items":[{"fingerprint":{},"raw_data":{},"ci_type_name":"server","name":"srv1","management_ip":"10.0.0.1","relationships":[{"target_ip":"10.0.0.2","type_key":"powered_by","confidence":0.8}]}]}`
	resp := doJSON(t, mux, "POST", "/api/v1/ingest/bulk", body)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", resp.Code, resp.Body.String())
	}
	var ingest BulkIngestResponse
	json.Unmarshal(resp.Body.Bytes(), &ingest)
	if ingest.Created != 1 {
		t.Fatalf("expected 1 CI created, got %+v", ingest)
	}
	if ingest.Relationships != 1 {
		t.Fatalf("expected 1 relationship derived, got %+v", ingest)
	}

	rels, _, _ := relRepo.List("org-1", pdu.ID, api.PaginationParams{Limit: 10, Offset: 0})
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship persisted, got %d", len(rels))
	}
	edge := rels[0]
	if edge.TargetCIID != pdu.ID || edge.RelType != "powered_by" || edge.Source != RelationshipSource {
		t.Errorf("unexpected edge %+v", edge)
	}
	if edge.Attributes["confidence"] != 0.8 {
		t.Errorf("expected confidence 0.8, got %v", edge.Attributes["confidence"])
	}

	// Re-ingesting the same data must not create a duplicate edge.
	resp2 := doJSON(t, mux, "POST", "/api/v1/ingest/bulk", body)
	var ingest2 BulkIngestResponse
	json.Unmarshal(resp2.Body.Bytes(), &ingest2)
	if ingest2.Relationships != 0 {
		t.Errorf("expected no new relationships on re-ingest, got %d", ingest2.Relationships)
	}
}

func TestDiscoveryJobsCRUD(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, nil)
	mux := newTestRouter(h)

	create := doJSON(t, mux, "POST", "/api/v1/discovery/jobs", `{"collector_id":"col-1","job_type":"sweep"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", create.Code, create.Body.String())
	}
	var job Job
	json.Unmarshal(create.Body.Bytes(), &job)
	if job.Status != JobStatusPending || job.ID == "" {
		t.Fatalf("unexpected job %+v", job)
	}

	get := doJSON(t, mux, "GET", "/api/v1/discovery/jobs/"+job.ID, "")
	if get.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", get.Code)
	}

	list := doJSON(t, mux, "GET", "/api/v1/discovery/jobs", "")
	var lr api.ListResponse[Job]
	json.Unmarshal(list.Body.Bytes(), &lr)
	if lr.Total != 1 {
		t.Fatalf("expected 1 job, got %d", lr.Total)
	}

	missing := doJSON(t, mux, "GET", "/api/v1/discovery/jobs/does-not-exist", "")
	if missing.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", missing.Code)
	}

	badType := doJSON(t, mux, "POST", "/api/v1/discovery/jobs", `{"collector_id":"col-1","job_type":"bogus"}`)
	if badType.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad job_type, got %d", badType.Code)
	}
}

func TestDiscoveryIngestAlias(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, ci.NewMemoryRepository())
	mux := newTestRouter(h)

	body := `{"collector_id":"c1","items":[{"fingerprint":{},"raw_data":{},"ci_type_name":"server","name":"srv1"}]}`
	resp := doJSON(t, mux, "POST", "/api/v1/discovery/ingest", body)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on alias, got %d: %s", resp.Code, resp.Body.String())
	}
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Buffer
	if body == "" {
		reader = bytes.NewBufferString("")
	} else {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
