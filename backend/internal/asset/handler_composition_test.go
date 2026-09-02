package asset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type stubParentLookup struct{ isChild bool }

func (s stubParentLookup) ParentOfAsset(_ context.Context, _, _ string) (bool, error) {
	return s.isChild, nil
}

func updateRequest(t *testing.T, h *Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/assets/"+id, strings.NewReader(body))
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.Update(w, req)
	return w
}

func seedAsset(t *testing.T, repo Repository) *Asset {
	t.Helper()
	a := &Asset{OrganizationID: "org-1", AssetTag: "A-1", Name: "child blade", Category: "hardware", Status: "in_stock", CustomFields: map[string]any{}}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	return a
}

func TestUpdateRejectsParentOwnedFieldOnChild(t *testing.T) {
	repo := NewMemoryRepository()
	a := seedAsset(t, repo)
	h := NewHandler(repo).WithComposition(stubParentLookup{isChild: true})

	w := updateRequest(t, h, a.ID, `{"serial_number": "SN-CHILD-DUP"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for parent-owned field on child, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestUpdateAllowsParentOwnedFieldOnStandaloneAsset(t *testing.T) {
	repo := NewMemoryRepository()
	a := seedAsset(t, repo)
	h := NewHandler(repo).WithComposition(stubParentLookup{isChild: false})

	w := updateRequest(t, h, a.ID, `{"serial_number": "SN-STANDALONE"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for standalone asset, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestUpdateAllowsComponentFieldOnChild(t *testing.T) {
	repo := NewMemoryRepository()
	a := seedAsset(t, repo)
	h := NewHandler(repo).WithComposition(stubParentLookup{isChild: true})

	w := updateRequest(t, h, a.ID, `{"notes": "blade slot 3"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for component-specific field on child, got %d (%s)", w.Code, w.Body.String())
	}
}
