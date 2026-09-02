package composition

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

const (
	testOrgID   = "11111111-1111-4111-8111-111111111111"
	testAssetID = "22222222-2222-4222-8222-222222222222"
	testCIID    = "33333333-3333-4333-8333-333333333333"
)

// stubAssets is an AssetLookup returning a fixed parent projection.
type stubAssets struct {
	ref *ParentAssetRef
	err error
}

func (s stubAssets) GetByID(context.Context, string, string) (*ParentAssetRef, error) {
	return s.ref, s.err
}

// newParentRouter wires the handler the way the server does and returns a
// router serving GET /api/v1/cis/{id}/parent.
func newParentRouter(t *testing.T, assets AssetLookup) (*chi.Mux, *MemoryRepository) {
	t.Helper()
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	if assets != nil {
		h = h.WithAssets(assets)
	}
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r, repo
}

// doGetParent issues an authenticated request for a CI's parent.
func doGetParent(r *chi.Mux, ciID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+ciID+"/parent", nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: testOrgID}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func seedChild(t *testing.T, repo *MemoryRepository) {
	t.Helper()
	if err := repo.Create(context.Background(), &Composition{
		OrganizationID: testOrgID,
		ParentAssetID:  testAssetID,
		ChildCIID:      testCIID,
		Role:           "system",
	}); err != nil {
		t.Fatalf("seed composition: %v", err)
	}
}

// A child CI must expose the parent asset's shared inventory identity, since
// the CI record itself deliberately stores none of it (spec §5).
func TestParentOfCIReturnsInheritedInventoryData(t *testing.T) {
	parent := &ParentAssetRef{
		ID:           testAssetID,
		Name:         "Server Chassis ABC123",
		AssetTag:     "AST-1",
		SerialNumber: "SN-ABC123",
		WarrantyEnd:  "2030-01-01",
		Location:     "DC1/Rack4",
	}
	r, repo := newParentRouter(t, stubAssets{ref: parent})
	seedChild(t, repo)

	rec := doGetParent(r, testCIID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Composition     *Composition    `json:"composition"`
		ParentAsset     *ParentAssetRef `json:"parent_asset"`
		InheritedFields []string        `json:"inherited_fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Composition == nil || body.Composition.ParentAssetID != testAssetID {
		t.Fatalf("composition link missing or wrong: %+v", body.Composition)
	}
	if body.ParentAsset == nil {
		t.Fatal("parent_asset missing: the child would have no way to display inherited data")
	}
	if body.ParentAsset.SerialNumber != "SN-ABC123" || body.ParentAsset.WarrantyEnd != "2030-01-01" {
		t.Fatalf("inherited values not projected: %+v", body.ParentAsset)
	}
	if len(body.InheritedFields) != len(ParentOwnedFields) {
		t.Fatalf("inherited_fields = %v, want the %d parent-owned fields",
			body.InheritedFields, len(ParentOwnedFields))
	}
}

// A standalone CI (the simple 1 asset <-> 1 CI workflow) has no parent and
// must report that plainly rather than erroring.
func TestParentOfCIReturns404WhenNoParent(t *testing.T) {
	r, _ := newParentRouter(t, stubAssets{})

	rec := doGetParent(r, testCIID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// The composition link is meaningful on its own, so a failure to read the
// parent asset must not turn a valid composition into an error response.
func TestParentOfCISurvivesAssetLookupFailure(t *testing.T) {
	r, repo := newParentRouter(t, stubAssets{err: errors.New("asset unavailable")})
	seedChild(t, repo)

	rec := doGetParent(r, testCIID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["composition"]; !ok {
		t.Fatal("composition link should still be returned")
	}
	if _, ok := body["parent_asset"]; ok {
		t.Fatal("parent_asset must be absent when the lookup failed")
	}
}

// Without a tenant the endpoint must not leak another organization's data.
func TestParentOfCIRequiresTenant(t *testing.T) {
	r, repo := newParentRouter(t, stubAssets{})
	seedChild(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+testCIID+"/parent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A CI belonging to another organization must not resolve a parent.
func TestParentOfCIIsTenantScoped(t *testing.T) {
	r, repo := newParentRouter(t, stubAssets{ref: &ParentAssetRef{ID: testAssetID}})
	if err := repo.Create(context.Background(), &Composition{
		OrganizationID: "99999999-9999-4999-8999-999999999999",
		ParentAssetID:  testAssetID,
		ChildCIID:      testCIID,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doGetParent(r, testCIID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a foreign-tenant CI; body=%s", rec.Code, rec.Body.String())
	}
}
