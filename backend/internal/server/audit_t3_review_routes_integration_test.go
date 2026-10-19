package server_test

// Phase-1 acceptance audit, group T3-data-ingest: REGRESSION test (passes
// today; closes an evidence gap).
//
// WP-088 (REC-07, AUTH-04; review:resolve) high test gap "review:resolve
// enforcement through the real router (repository test)": until now only a
// verifier probe showed that the complete NewRouter (authz + OpenAPI
// validation) refuses the resolve routes without review:resolve and the
// list without discovery:read. Asserted here:
// - POST /api/v1/review-items/{id}/resolve and the deprecated
//   /api/v1/discovery/review-items/{id}/resolve without review:resolve -> 403,
//   the item stays open;
// - GET /api/v1/review-items without discovery:read -> 403;
// - with review:resolve -> 200, the item is dismissed and resolved_by is the
//   caller when the body names none.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3ReviewResolveRequiresPermission' ./internal/server/

import (
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT3ReviewResolveRequiresPermission(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	item := r.reviewItem("duplicate_ip")
	status := func() string { return r.str(`SELECT status FROM review_item WHERE id = $1`, item) }

	for _, path := range []string{"/api/v1/review-items/" + item + "/resolve", "/api/v1/discovery/review-items/" + item + "/resolve"} {
		if w := r.call(auditT3Without(identity.PermReviewResolve), http.MethodPost, path, `{"action":"dismiss"}`); w.Code != http.StatusForbidden {
			t.Errorf("POST %s without review:resolve = %d, want 403", path, w.Code)
		}
		if got := status(); got != "open" {
			t.Errorf("item status after refused resolve via %s = %q, want open", path, got)
		}
	}
	if w := r.call(auditT3Without(identity.PermDiscoveryRead), http.MethodGet, "/api/v1/review-items", ""); w.Code != http.StatusForbidden {
		t.Errorf("GET /api/v1/review-items without discovery:read = %d, want 403", w.Code)
	}
	if w := r.call(identity.AllPermissions(), http.MethodGet, "/api/v1/review-items", ""); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/review-items with discovery:read = %d, want 200", w.Code)
	}

	w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"dismiss"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve with review:resolve = %d %s, want 200", w.Code, w.Body.String())
	}
	if got := status(); got != "dismissed" {
		t.Errorf("item status after dismiss = %q, want dismissed", got)
	}
	if got := r.str(`SELECT coalesce(resolved_by::text, '') FROM review_item WHERE id = $1`, item); got != r.user {
		t.Errorf("resolved_by = %q, want the caller %s", got, r.user)
	}
}
