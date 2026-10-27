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

// TestUnclassifiedReviewIsAboutTheResolvedCI covers WP-417 (DIS-02, F-030):
// the unclassified_device item is about the CI the record resolved to – its
// object, candidate, client and site – and exists only while that CI is a
// generic_device.
func TestUnclassifiedReviewIsAboutTheResolvedCI(t *testing.T) {
	ic := &IdentityContext{GenericDeviceTypeID: "type-generic"}
	// An org-wide record of a scope site; the CI belongs to client 2.
	record := IngestItem{CITypeName: "type-generic", Name: "10.0.0.9", ManagementIP: "10.0.0.9", SiteID: "site-of-scope",
		Attributes: map[string]any{"classified_by": "fallback"}}
	device := &ci.Item{ID: "ci-9", CITypeID: "type-generic", ClientID: "client-2", SiteID: "site-2"}

	item := unclassifiedReview("org-1", &record, device, ic)
	if item == nil {
		t.Fatal("no item for a generic_device CI")
	}
	if item.Kind != ReviewKindUnclassifiedDevice || item.Status != ReviewStatusOpen || item.OrganizationID != "org-1" ||
		item.ObjectKey != "ci:ci-9" || len(item.CandidateCIIDs) != 1 || item.CandidateCIIDs[0] != "ci-9" {
		t.Errorf("item = %+v, want an open unclassified_device item about ci-9", item)
	}
	if item.ClientID != "client-2" || item.SiteID != "site-2" {
		t.Errorf("client/site = %q/%q, want those of the CI (client-2/site-2)", item.ClientID, item.SiteID)
	}
	if item.Payload["reason"] != ReasonNoProfileMatch || item.Payload["ci_id"] != "ci-9" || item.Payload["management_ip"] != "10.0.0.9" {
		t.Errorf("payload = %v", item.Payload)
	}

	for name, tc := range map[string]struct {
		device *ci.Item
		ic     *IdentityContext
	}{
		"CI classified by another protocol": {&ci.Item{ID: "ci-9", CITypeID: "type-switch"}, ic},
		"no identity context":               {device, nil},
		"generic_device type unknown":       {device, &IdentityContext{}},
	} {
		if got := unclassifiedReview("org-1", &record, tc.device, tc.ic); got != nil {
			t.Errorf("%s: item %+v, want none", name, got)
		}
	}
}

func TestBulkIngestPersistsReviewItems(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	// Two existing CIs sharing a serial number → ambiguous match.
	ciRepo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", Name: "a", SerialNumber: "DUP", CITypeID: "server"})
	ciRepo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", Name: "b", SerialNumber: "DUP", CITypeID: "server"})

	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
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
	repo.SeedCIType("server", "server")
	h := NewHandler(repo, ciRepo)
	mux := newTestRouter(h)

	item := &ReviewItem{
		OrganizationID: "org-1",
		Kind:           ReviewKindAmbiguousIdentity,
		Status:         ReviewStatusOpen,
		CandidateCIIDs: []string{a.ID, b.ID},
		Payload:        map[string]any{"management_ip": "10.0.0.5"},
	}
	repo.CreateReviewItem(context.Background(), item)

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

// TestResolveReviewItemCreate: new creates the CI with the UUID of the type
// the item recorded, its name resolved as the ingest resolves it, or of the
// type the resolution names (ci_type_id); without a type that resolves it
// is 422 naming ci_type_id and the item stays open (WP-528, UI-16, F-176).
func TestResolveReviewItemCreate(t *testing.T) {
	const serverType = "0b6a2c1e-5f1d-4e43-9a51-2d8c0e7b5280"
	const chosenType = "6f4e1d2c-3b5a-4c79-8e01-a2b3c4d5e6f7"
	ciRepo := ci.NewMemoryRepository()
	repo := NewMemoryRepository()
	repo.SeedCIType("server", serverType)
	h := NewHandler(repo, ciRepo)
	mux := newTestRouter(h)
	open := func(name, typeName string) *ReviewItem {
		t.Helper()
		item := &ReviewItem{
			OrganizationID: "org-1",
			Kind:           ReviewKindAmbiguousIdentity,
			Status:         ReviewStatusOpen,
			CandidateCIIDs: []string{"ci-a"},
			// An org-wide sighting: the payload keeps the record's client
			// (WP-369).
			Payload: map[string]any{"name": name, "ci_type_name": typeName, "client_id": ""},
		}
		if err := repo.CreateReviewItem(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	created := func(name string) *ci.Item {
		t.Helper()
		items, _, _ := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10, Offset: 0})
		for i := range items {
			if items[i].Name == name {
				return &items[i]
			}
		}
		return nil
	}

	item := open("brand-new", "server")
	resp := doJSON(t, mux, "POST", "/api/v1/discovery/review-items/"+item.ID+"/resolve", `{"action":"create"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if c := created("brand-new"); c == nil || c.CITypeID != serverType || !isUUID(c.CITypeID) {
		t.Fatalf("expected new CI of type %s (a UUID, not the type name), got %+v", serverType, c)
	}

	// A type that resolves to nothing: 422 naming ci_type_id, no CI.
	unknown := open("of-unknown-type", "pv-unknown-type")
	resp = doJSON(t, mux, "POST", "/api/v1/review-items/"+unknown.ID+"/resolve", `{"action":"new"}`)
	var problem struct {
		Field  string `json:"field"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &problem)
	if resp.Code != http.StatusUnprocessableEntity || problem.Field != "ci_type_id" || problem.Detail != "ci_type_id: choose a CI type for the new CI" {
		t.Fatalf("new without a resolvable type: %d %s, want 422 with field ci_type_id", resp.Code, resp.Body.String())
	}
	if c := created("of-unknown-type"); c != nil {
		t.Fatalf("CI created without a type: %+v", c)
	}
	if got, _ := repo.GetReviewItem(context.Background(), "org-1", unknown.ID); got == nil || got.Status != ReviewStatusOpen {
		t.Fatalf("item after the refused new: %+v, want open", got)
	}
	// A ci_type_id that is no UUID is refused the same way.
	resp = doJSON(t, mux, "POST", "/api/v1/review-items/"+unknown.ID+"/resolve", `{"action":"new","ci_type_id":"server"}`)
	if resp.Code != http.StatusUnprocessableEntity || !bytes.Contains(resp.Body.Bytes(), []byte(`"field":"ci_type_id"`)) {
		t.Fatalf("new with ci_type_id server: %d %s, want 422 with field ci_type_id", resp.Code, resp.Body.String())
	}
	// The chosen type takes precedence over the recorded name.
	resp = doJSON(t, mux, "POST", "/api/v1/review-items/"+unknown.ID+"/resolve", `{"action":"new","ci_type_id":"`+chosenType+`"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("new with ci_type_id: %d %s", resp.Code, resp.Body.String())
	}
	if c := created("of-unknown-type"); c == nil || c.CITypeID != chosenType {
		t.Fatalf("expected new CI of the chosen type %s, got %+v", chosenType, c)
	}
}

func TestBulkIngestDerivesTopology(t *testing.T) {
	ciRepo := ci.NewMemoryRepository()
	// Pre-existing PDU neighbor addressable by management IP.
	pdu := &ci.Item{OrganizationID: "org-1", Name: "pdu1", CITypeID: "pdu", ManagementIP: "10.0.0.2"}
	ciRepo.Create(context.Background(), pdu)

	relRepo := relationship.NewMemoryRepository()
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	h := NewHandler(repo, ciRepo, relRepo)
	mux := newTestRouter(h)

	body := `{"collector_id":"c1","items":[{"fingerprint":{},"raw_data":{},"ci_type_name":"server","name":"srv1","serial_number":"SRV1-SN","management_ip":"10.0.0.1","relationships":[{"target_ip":"10.0.0.2","type_key":"powered_by","confidence":0.8}]}]}`
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

	rels, _, _ := relRepo.List(context.Background(), "org-1", pdu.ID, api.PaginationParams{Limit: 10, Offset: 0})
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship persisted, got %d", len(rels))
	}
	edge := rels[0]
	if edge.TargetCIID != pdu.ID || edge.RelType != "powered_by" || edge.Source != RelationshipSource {
		t.Errorf("unexpected edge %+v", edge)
	}
	if edge.Confidence == nil || *edge.Confidence != 0.8 {
		t.Errorf("expected confidence 0.8, got %v", edge.Confidence)
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
