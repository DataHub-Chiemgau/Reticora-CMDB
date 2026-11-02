package reviews

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestListReviewItemsRejectsUnknownStatusAndKind covers WP-697 (F-426,
// API-01/API-02, WP-088): GET /api/v1/review-items and its deprecated alias
// /api/v1/discovery/review-items answer a status or kind outside the
// documented enums with 400 problem+json naming the field, as
// /relationships?rel_type (WP-673), instead of 200 with an empty list. Every
// documented value and an absent filter still list.
func TestListReviewItemsRejectsUnknownStatusAndKind(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	if _, err := svc.Open(context.Background(), &Item{OrganizationID: "org", Kind: KindDuplicateIP, ObjectKey: "ip-1", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(svc).RegisterRoutes(mux)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org", UserID: "u1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	for _, base := range []string{"/api/v1/review-items", "/api/v1/discovery/review-items"} {
		for _, tc := range []struct{ query, field string }{
			{"status=bogus", "status"},
			{"status=OPEN", "status"},
			{"status=open%00", "status"},
			{"kind=bogus", "kind"},
			{"kind=Duplicate_IP", "kind"},
			{"status=open&kind=bogus", "kind"},
			{"status=bogus&kind=duplicate_ip", "status"},
		} {
			w := get(base + "?" + tc.query)
			var problem struct {
				Status int    `json:"status"`
				Field  string `json:"field"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &problem)
			if w.Code != http.StatusBadRequest || w.Header().Get("Content-Type") != "application/problem+json" ||
				problem.Status != http.StatusBadRequest || problem.Field != tc.field {
				t.Errorf("GET %s?%s = %d %s %s, want 400 problem+json with field %s",
					base, tc.query, w.Code, w.Header().Get("Content-Type"), w.Body.String(), tc.field)
			}
		}

		// The documented values and no filter keep listing.
		valid := []string{"", "status=", "kind=", "escalated=false"}
		for _, s := range []string{StatusOpen, StatusResolved, StatusDismissed} {
			valid = append(valid, "status="+s)
		}
		for _, k := range Kinds {
			valid = append(valid, "kind="+k)
		}
		for _, q := range valid {
			if w := get(base + "?" + q); w.Code != http.StatusOK {
				t.Errorf("GET %s?%s = %d %s, want 200", base, q, w.Code, w.Body.String())
			}
		}
		var list struct {
			Total int `json:"total"`
		}
		w := get(base + "?status=open&kind=duplicate_ip")
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || list.Total != 1 {
			t.Errorf("GET %s?status=open&kind=duplicate_ip = %d %s, want the one open item", base, w.Code, w.Body.String())
		}
	}
}
