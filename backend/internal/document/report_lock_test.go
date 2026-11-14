package document

import (
	"context"
	"net/http"
	"testing"
)

// TestComplianceReportLocked (SEC-04, E-239, WP-1129): the document API
// neither creates nor sets the category compliance_report (422 on
// category), and a report the job compliance.report stored takes no PATCH,
// content, new version, soft delete, link or unlink (409): GET
// /api/v1/compliance/reports lists only reports the job generated.
//
// Retest:
//
//	cd backend && go test -count=1 -run TestComplianceReportLocked ./internal/document/
func TestComplianceReportLocked(t *testing.T) {
	repo, _, mux := securityFixture(t)
	const org = "org-report"
	if w := requestAs(t, mux, org, http.MethodPost, "/api/v1/documents", "application/json",
		`{"title": "forged", "file_name": "forged.pdf", "category": "compliance_report", "tags": ["standard:iso27001", "format:pdf"]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("create with category compliance_report: %d %s, want 422", w.Code, w.Body.String())
	}
	plain := newDoc(t, repo, org, "plain")
	if w := requestAs(t, mux, org, http.MethodPatch, "/api/v1/documents/"+plain, "application/json",
		`{"category": "compliance_report"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("PATCH to category compliance_report: %d %s, want 422", w.Code, w.Body.String())
	}
	report := &Document{OrganizationID: org, Title: "report", FileName: "report.pdf", MimeType: "application/pdf", Category: CategoryComplianceReport}
	if err := repo.Create(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, contentType, body string }{
		{http.MethodPatch, "/api/v1/documents/" + report.ID, "application/json", `{"tags": ["standard:nis2"]}`},
		{http.MethodPut, "/api/v1/documents/" + report.ID + "/content", "application/pdf", "%PDF-1.4 replaced"},
		{http.MethodPost, "/api/v1/documents/" + report.ID + "/versions", "application/pdf", "%PDF-1.4 replaced"},
		{http.MethodDelete, "/api/v1/documents/" + report.ID, "", ""},
		{http.MethodPost, "/api/v1/documents/" + report.ID + "/links", "application/json",
			`{"entity_type": "ci", "entity_id": "00000000-0000-4000-8000-000000000001"}`},
		{http.MethodDelete, "/api/v1/documents/" + report.ID + "/links/00000000-0000-4000-8000-000000000002", "", ""},
	} {
		if w := requestAs(t, mux, org, c.method, c.path, c.contentType, c.body); w.Code != http.StatusConflict {
			t.Errorf("%s %s on a report: %d %s, want 409", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
