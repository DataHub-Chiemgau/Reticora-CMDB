//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed). Cross-client reads and writes of multi-object and
// org-wide module rows through the complete PostgreSQL router.
//
// WP-020 (TEN-06, TKT-01, MGT-04):
//   - P0 "Compliance evaluation by a client-scoped principal returns and
//     overwrites other clients' results": compliance/evaluator.go replaces
//     the request scope with database.OrgWideScope, the POST response lists
//     the client-2 CI and the client-2 result row is deleted and re-inserted.
//   - P1 "Client-scoped principal can rename, cancel and delete maintenance
//     windows that cover another client's CIs" (windowVisible = any CI
//     visible; FK cascade removes the client-2 maintenance_window_ci rows).
// WP-028 (MGT-01, MGT-02, MGT-04, TKT-01; TEN-05, E-09):
//   - P1 "Partially scoped principal can modify/delete a shared document or
//     maintenance window; FK cascade deletes other clients' links".
//   - P2 "Unlinked documents/windows are org-wide but client-scoped
//     principals can create, change, delete and 'hijack' them".
// TEN-09: any cross-client exposure or write path is Critical.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v \
//     -run 'TestAuditT1ComplianceEvaluationStaysInCallerScope|TestAuditT1MaintenanceWindowWritesOutsideScope|TestAuditT1DocumentWritesOutsideScope' \
//     ./internal/tenant/rls/

import (
	"net/http"
	"strings"
	"testing"
)

// TestAuditT1ComplianceEvaluationStaysInCallerScope: a client-1 principal
// triggers POST /api/v1/compliance/evaluations. The response must not name
// the client-2 CI and the stored client-2 result must stay untouched
// (either the evaluation is limited to the caller's scope or it is refused).
func TestAuditT1ComplianceEvaluationStaysInCallerScope(t *testing.T) {
	w := newT1World(t, "d0")
	f := w.f
	own := f.CI(t, f.OrgA, f.Client1, "t1-comp-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "t1-comp-c2")
	client1 := w.as("client1", only(f.Client1), all())

	w.create(w.orgA, "/api/v1/compliance/rules", map[string]any{
		"name": "t1 has name", "severity": "low", "category": "hygiene",
		"expression": map[string]any{"field": "name", "op": "not_empty"},
	})
	// Positive control: the org-wide evaluation covers both clients.
	code, body := w.do(w.orgA, http.MethodPost, "/api/v1/compliance/evaluations", nil)
	if code != http.StatusOK || !strings.Contains(body, own) || !strings.Contains(body, foreign) {
		t.Fatalf("org-wide evaluation: %d, own=%v foreign=%v; want 200 with both CIs", code,
			strings.Contains(body, own), strings.Contains(body, foreign))
	}
	before := w.scalar(`SELECT id::text || '|' || evaluated_at::text FROM compliance_result WHERE ci_id = $1`, foreign)

	code, body = w.do(client1, http.MethodPost, "/api/v1/compliance/evaluations", nil)
	if strings.Contains(body, foreign) {
		t.Errorf("TEN-06/TEN-09: POST /compliance/evaluations as client 1 answered %d and lists the client-2 CI %s", code, foreign)
	}
	if after := w.scalar(`SELECT id::text || '|' || evaluated_at::text FROM compliance_result WHERE ci_id = $1`, foreign); after != before {
		t.Errorf("TEN-06/TEN-09: the client-2 compliance result was rewritten by a client-1 principal (before %s, after %s)", before, after)
	}
	// The scoped result list stays correct (regression guard).
	if code, body = w.do(client1, http.MethodGet, "/api/v1/compliance/results?limit=100", nil); code != http.StatusOK || strings.Contains(body, foreign) {
		t.Errorf("GET /compliance/results as client 1: %d, contains client-2 CI %v", code, strings.Contains(body, foreign))
	}
}

// TestAuditT1MaintenanceWindowWritesOutsideScope: a window that also covers
// a client-2 CI, and an org-wide window without CIs, must not be changed or
// deleted by a client-1 principal; a window of client 1 only stays editable
// by client 1.
func TestAuditT1MaintenanceWindowWritesOutsideScope(t *testing.T) {
	w := newT1World(t, "d1")
	f := w.f
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-mw-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-mw-c2")
	client1 := w.as("client1", only(f.Client1), all())
	window := func(title string, cis ...string) string {
		return w.create(w.orgA, "/api/v1/maintenance-windows", map[string]any{
			"title": title, "starts_at": "2030-01-01T00:00:00Z", "ends_at": "2030-01-01T02:00:00Z", "ci_ids": cis,
		})
	}
	mixed := window("t1-mw-mixed", ci1, ci2)
	orgWide := window("t1-mw-orgwide")
	ownOnly := window("t1-mw-own", ci1)

	for _, c := range []struct {
		name, id, title string
		linkedCI        string
	}{
		{"window covering client-1 and client-2 CIs (WP-020 P1, WP-028 P1)", mixed, "t1-mw-mixed", ci2},
		{"org-wide window without CIs (WP-028 P2, E-09)", orgWide, "t1-mw-orgwide", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := w.do(client1, http.MethodPatch, "/api/v1/maintenance-windows/"+c.id, map[string]any{"title": "t1-changed-by-c1", "status": "cancelled"})
			if !refused(code) {
				t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
			}
			if got := w.scalar(`SELECT title || '|' || status FROM maintenance_window WHERE id = $1`, c.id); !strings.HasPrefix(got, c.title+"|") || strings.HasSuffix(got, "|cancelled") {
				t.Errorf("window after PATCH by client 1: %q, want title %q and not cancelled", got, c.title)
			}
			code, body = w.do(client1, http.MethodDelete, "/api/v1/maintenance-windows/"+c.id, nil)
			if !refused(code) {
				t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
			}
			if n := w.scalar(`SELECT count(*)::text FROM maintenance_window WHERE id = $1`, c.id); n != "1" {
				t.Errorf("window rows after DELETE by client 1: %s, want 1", n)
			}
			if c.linkedCI != "" {
				if n := w.scalar(`SELECT count(*)::text FROM maintenance_window_ci WHERE maintenance_window_id = $1 AND ci_id = $2`, c.id, c.linkedCI); n != "1" {
					t.Errorf("client-2 link rows after DELETE by client 1: %s, want 1 (FK cascade removed another client's data)", n)
				}
			}
		})
	}
	t.Run("org-wide window created by a client-scoped principal (E-09)", func(t *testing.T) {
		code, body := w.do(client1, http.MethodPost, "/api/v1/maintenance-windows", map[string]any{
			"title": "t1-mw-created-by-c1", "starts_at": "2030-02-01T00:00:00Z", "ends_at": "2030-02-01T02:00:00Z",
		})
		if code >= 200 && code < 300 {
			t.Errorf("POST an org-wide window (no CIs) as client 1: %d %s, want refused", code, trim(body))
		}
	})
	t.Run("positive control: client 1 edits its own window", func(t *testing.T) {
		if code, body := w.do(client1, http.MethodPatch, "/api/v1/maintenance-windows/"+ownOnly, map[string]any{"title": "t1-mw-own-renamed"}); code != http.StatusOK {
			t.Errorf("PATCH own window as client 1: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(client1, http.MethodDelete, "/api/v1/maintenance-windows/"+ownOnly, nil); code != http.StatusNoContent {
			t.Errorf("DELETE own window as client 1: %d %s, want 204", code, trim(body))
		}
	})
}

// TestAuditT1DocumentWritesOutsideScope: a document shared by a client-1 CI
// and a client-2 asset, and an org-wide (unlinked) document, must not be
// changed, deleted or taken over by a client-1 principal.
func TestAuditT1DocumentWritesOutsideScope(t *testing.T) {
	w := newT1World(t, "d2")
	f := w.f
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-doc-c1")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "t1-doc-a2")
	client1 := w.as("client1", only(f.Client1), all())
	client2 := w.as("client2", only(f.Client2), all())
	doc := func(title string) string {
		return w.create(w.orgA, "/api/v1/documents", map[string]any{"title": title, "file_name": title + ".pdf"})
	}
	link := func(id, entityType, entity string) {
		t.Helper()
		w.create(w.orgA, "/api/v1/documents/"+id+"/links", map[string]any{"entity_type": entityType, "entity_id": entity})
	}
	shared := doc("t1-doc-shared")
	link(shared, "ci", ci1)
	link(shared, "asset", asset2)
	orgWide := doc("t1-doc-orgwide")
	orgWideCtl := doc("t1-doc-orgwide-ctl")
	ownOnly := doc("t1-doc-own")
	link(ownOnly, "ci", ci1)

	t.Run("shared document of client 1 and client 2 (WP-028 P1)", func(t *testing.T) {
		code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+shared, map[string]any{"title": "t1-renamed-by-c1"})
		if !refused(code) {
			t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
		}
		if got := w.scalar(`SELECT title FROM document WHERE id = $1`, shared); got != "t1-doc-shared" {
			t.Errorf("title after PATCH by client 1: %q", got)
		}
		code, body = w.do(client1, http.MethodDelete, "/api/v1/documents/"+shared, nil)
		if !refused(code) {
			t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
		}
		if n := w.scalar(`SELECT count(*)::text FROM document_link WHERE document_id = $1 AND entity_type = 'asset' AND entity_id = $2`, shared, asset2); n != "1" {
			t.Errorf("client-2 asset links after DELETE by client 1: %s, want 1", n)
		}
	})
	t.Run("org-wide unlinked document (WP-028 P2, TEN-05, E-09)", func(t *testing.T) {
		code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+orgWide, map[string]any{"title": "t1-renamed-by-c1"})
		if !refused(code) {
			t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
		}
		if got := w.scalar(`SELECT title FROM document WHERE id = $1`, orgWide); got != "t1-doc-orgwide" {
			t.Errorf("title after PATCH by client 1: %q", got)
		}
		code, body = w.do(client1, http.MethodPost, "/api/v1/documents", map[string]any{"title": "t1-doc-created-by-c1", "file_name": "x.pdf"})
		if code >= 200 && code < 300 {
			t.Errorf("POST an org-wide (unlinked) document as client 1: %d %s, want refused", code, trim(body))
		}
		// Linking the org-wide document to an own CI must not take it away
		// from client 2.
		w.do(client1, http.MethodPost, "/api/v1/documents/"+orgWide+"/links", map[string]any{"entity_type": "ci", "entity_id": ci1})
		if got, _ := w.do(client2, http.MethodGet, "/api/v1/documents/"+orgWide, nil); got != http.StatusOK {
			t.Errorf("GET the org-wide document as client 2 after client 1 linked it: %d, want 200", got)
		}
		code, body = w.do(client1, http.MethodDelete, "/api/v1/documents/"+orgWide, nil)
		if !refused(code) {
			t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
		}
		if n := w.scalar(`SELECT count(*)::text FROM document WHERE id = $1`, orgWide); n != "1" {
			t.Errorf("org-wide document rows after DELETE by client 1: %s, want 1", n)
		}
	})
	t.Run("positive control", func(t *testing.T) {
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/documents/"+orgWideCtl, map[string]any{"title": "t1-doc-orgwide-ctl-2"}); code != http.StatusOK {
			t.Errorf("PATCH org-wide document as org admin: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+ownOnly, map[string]any{"title": "t1-doc-own-2"}); code != http.StatusOK {
			t.Errorf("PATCH own document as client 1: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(client1, http.MethodDelete, "/api/v1/documents/"+ownOnly, nil); code != http.StatusNoContent {
			t.Errorf("DELETE own document as client 1: %d %s, want 204", code, trim(body))
		}
	})
}
