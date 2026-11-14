package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The report generator as a job on PostgreSQL (SEC-04, WP-1129, E-239
// option 2): the job stores the report as a document of the category
// compliance_report linked to the organization or the client, names it in
// its result and writes compliance.report.generated; a client report reads
// only that client's data and is hidden from another client; an
// organization report needs an org-wide principal; the JSON preview keeps
// its form.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestReportJob|TestClientReportScoped|TestOrgReportRequiresOrgWidePrincipal|TestReportPreviewUnchanged' ./internal/compliance/

type reportEnv struct {
	*evalEnv
	blobs *blob.FileStore
	jobs  *Jobs
	api   chi.Router
	user  string
	ciA   string
	ciB   string
	// ciNone is a CI without client: in the organization report, never in
	// a client report.
	ciNone string
	rule   *Rule
	gen    *ReportGenerator
}

func newReportEnv(t *testing.T) *reportEnv {
	t.Helper()
	e := &reportEnv{evalEnv: newEvalEnv(t, "c6"), blobs: blob.NewFileStore(t.TempDir())}
	f := e.f
	e.user = f.AppUser(t, f.OrgA, "cmp-report-user")
	e.ciA = f.CI(t, f.OrgA, f.Client1, "cmp-report-ci-a")
	e.ciB = f.CI(t, f.OrgA, f.Client2, "cmp-report-ci-b")
	e.ciNone = f.CI(t, f.OrgA, "", "cmp-report-ci-none")
	// A rule that fails on every CI, assigned to control A.5.1 of ISO
	// 27001.
	e.rule = e.evalEnv.rule(t, "report-fails", JSONMap{"op": "empty", "field": "name"})
	if _, err := e.eval.Run(e.ctx, f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	controls := e.controlIDs(t, "iso27001")
	var control string
	for _, id := range controls {
		control = id
		break
	}
	if err := e.repo.ReplaceRuleControls(e.ctx, f.OrgA, e.rule.ID, []string{control}); err != nil {
		t.Fatal(err)
	}
	gen := NewReportGenerator(f.App, e.repo, stubAuditVerifier{integrity: AuditIntegrity{Intact: true, Checked: 3}},
		stubEntitlements{items: []EntitlementStatus{{FeatureKey: "compliance_findings", Enabled: true}}}, e.blobs)
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	svc.Register(ReportJobKindConfig)
	e.gen = gen
	e.jobs = NewJobs(e.eval).WithJobs(svc).WithReports(gen)
	e.api = chi.NewRouter()
	NewHandler(e.repo, e.eval).WithJobs(e.jobs).WithReports(NewReportService(e.repo, nil, nil)).RegisterRoutes(e.api)
	return e
}

// as is the request context of the report user, org-wide or bound to
// clients.
func (e *reportEnv) as(clients ...string) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.user)
	if len(clients) > 0 {
		scope.Clients = database.ScopeIDs(clients...)
	}
	return tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
}

func (e *reportEnv) call(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	return rec
}

// generate posts a report and waits for its job; it returns the job result.
func (e *reportEnv) generate(t *testing.T, ctx context.Context, body string) (string, ReportJobResult) {
	t.Helper()
	rec := e.call(ctx, http.MethodPost, "/api/v1/compliance/reports", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST reports %s: %d %s", body, rec.Code, rec.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var status string
		var result []byte
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status, COALESCE(result, 'null'::jsonb) FROM job WHERE id = $1`, job.ID).
			Scan(&status, &result); err != nil {
			t.Fatal(err)
		}
		switch status {
		case "succeeded":
			var res ReportJobResult
			if err := json.Unmarshal(result, &res); err != nil {
				t.Fatal(err)
			}
			return job.ID, res
		case "failed", "cancelled":
			var errText string
			_ = e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM job WHERE id = $1`, job.ID).Scan(&errText)
			t.Fatalf("report job %s: %s %s", job.ID, status, errText)
		}
		if time.Now().After(deadline) {
			t.Fatalf("report job %s still %s", job.ID, status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// content reads the stored content of a report document.
func (e *reportEnv) content(t *testing.T, docID string) []byte {
	t.Helper()
	var key string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT storage_key FROM document WHERE id = $1`, docID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	rc, err := e.blobs.Get(context.Background(), document.Bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestReportJobStoresDocument: the organization report is a document of
// category compliance_report linked to the organization, pending its scan,
// named by the job result, with the audit entry; the list shows it with
// standard, format, scope and the job status.
func TestReportJobStoresDocument(t *testing.T) {
	e := newReportEnv(t)
	jobID, res := e.generate(t, e.as(), `{"standard": "iso27001", "format": "pdf"}`)
	if !api.IsUUID(res.DocumentID) || res.Scope != "organization" || res.Standard != "iso27001" || res.Format != FormatPDF {
		t.Fatalf("job result %+v", res)
	}
	var category, mime, linkType, linkID, scan string
	var tags []string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT d.category, d.mime_type, d.tags, l.entity_type, l.entity_id::text, v.scan_status
		FROM document d JOIN document_link l ON l.document_id = d.id JOIN document_version v ON v.document_id = d.id AND v.version = 1
		WHERE d.id = $1`, res.DocumentID).Scan(&category, &mime, &tags, &linkType, &linkID, &scan); err != nil {
		t.Fatal(err)
	}
	if category != CategoryComplianceReport || mime != "application/pdf" || linkType != "organization" || linkID != e.f.OrgA ||
		scan != document.ScanPending {
		t.Errorf("document %s %s %s/%s scan %s", category, mime, linkType, linkID, scan)
	}
	if !strings.Contains(strings.Join(tags, ","), "standard:iso27001") || !strings.Contains(strings.Join(tags, ","), "job:"+jobID) {
		t.Errorf("tags %v", tags)
	}
	if raw := e.content(t, res.DocumentID); !bytes.HasPrefix(raw, []byte("%PDF-")) {
		t.Errorf("stored content starts with %q", raw[:min(8, len(raw))])
	}
	var changes []byte
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = $2
		AND resource_id = $3::uuid`, e.f.OrgA, ActionReportGenerated, res.DocumentID).Scan(&changes); err != nil {
		t.Fatalf("audit entry %s: %v", ActionReportGenerated, err)
	}
	var c map[string]any
	if err := json.Unmarshal(changes, &c); err != nil || c["standard"] != "iso27001" || c["scope"] != "organization" || c["document_id"] != res.DocumentID {
		t.Errorf("audit changes %s", changes)
	}
	rec := e.call(e.as(), http.MethodGet, "/api/v1/compliance/reports", "")
	var list api.ListResponse[ReportEntry]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Data) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if got := list.Data[0]; got.DocumentID != res.DocumentID || got.Standard != "iso27001" || got.Format != FormatPDF ||
		got.Scope != "organization" || got.JobID != jobID || got.JobStatus == "" {
		t.Errorf("list entry %+v", got)
	}
	// The CSV of the same organization holds both CIs of the failing rule.
	_, csvRes := e.generate(t, e.as(), `{"standard": "iso27001", "format": "csv"}`)
	raw := string(e.content(t, csvRes.DocumentID))
	if !strings.HasPrefix(raw, strings.Join(ReportCSVHeader, ",")) || !strings.Contains(raw, e.ciA) || !strings.Contains(raw, e.ciB) ||
		!strings.Contains(raw, e.ciNone) {
		t.Errorf("organization CSV lacks a CI:\n%s", raw)
	}
	// An organization report holds the data of every client: a principal
	// bound to a client neither lists it nor reads it under row level
	// security (document and document_link, migration 000369).
	if got := e.listed(t, e.as(e.f.Client1)); len(got) != 0 {
		t.Errorf("principal of client 1 lists the organization reports %v, want nothing", got)
	}
	var docs, links int
	if err := database.WithRequestTenant(e.as(e.f.Client1), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		if qerr := tx.QueryRow(ctx, `SELECT count(*) FROM document WHERE id = ANY ($1::uuid[])`, []string{res.DocumentID, csvRes.DocumentID}).
			Scan(&docs); qerr != nil {
			return qerr
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM document_link WHERE document_id = ANY ($1::uuid[])`, []string{res.DocumentID, csvRes.DocumentID}).
			Scan(&links)
	}); err != nil {
		t.Fatal(err)
	}
	if docs != 0 || links != 0 {
		t.Errorf("principal of client 1 reads %d report documents and %d links under RLS, want none", docs, links)
	}
}

// limitOf is a storage limit of a fixed number of bytes.
type limitOf int64

func (l limitOf) StorageLimitBytes(context.Context, string) (*int64, error) {
	n := int64(l)
	return &n, nil
}

// TestReportCountsAgainstStorageLimit: a report beyond max_storage_mb of
// the organization fails with the limit error, stores no document and
// leaves no object (E-239).
func TestReportCountsAgainstStorageLimit(t *testing.T) {
	e := newReportEnv(t)
	e.gen.WithStorageLimit(limitOf(1))
	_, err := e.gen.Generate(e.as(), e.f.OrgA, "", &ReportParams{Standard: "iso27001", Format: FormatCSV}, nil)
	var limit *entitlement.LimitExceededError
	if !errors.As(err, &limit) {
		t.Fatalf("report beyond the storage limit: %v, want *entitlement.LimitExceededError", err)
	}
	if n := e.count(t, `SELECT count(*) FROM document WHERE organization_id = $1 AND category = $2`, e.f.OrgA, CategoryComplianceReport); n != 0 {
		t.Errorf("report documents after the refusal: %d, want 0", n)
	}
	objects, err := e.blobs.List(context.Background(), document.Bucket, blob.OrgPrefix(e.f.OrgA))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Errorf("objects after the refusal: %v, want none", objects)
	}
}

// TestClientReportScoped: the report of client 1 holds no CI of client 2,
// is linked to client 1 and is not visible to a principal of client 2.
func TestClientReportScoped(t *testing.T) {
	e := newReportEnv(t)
	_, res := e.generate(t, e.as(), fmt.Sprintf(`{"standard": "iso27001", "format": "csv", "client_id": %q}`, e.f.Client1))
	if res.Scope != "client" || res.ClientID != e.f.Client1 {
		t.Fatalf("job result %+v", res)
	}
	raw := string(e.content(t, res.DocumentID))
	if !strings.Contains(raw, e.ciA) || strings.Contains(raw, e.ciB) || strings.Contains(raw, e.ciNone) {
		t.Errorf("client-1 CSV must hold CI %s and neither CI %s of client 2 nor CI %s without client:\n%s", e.ciA, e.ciB, e.ciNone, raw)
	}
	var linkType, linkID, linkClient string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT entity_type, entity_id::text, COALESCE(client_id::text, '') FROM document_link
		WHERE document_id = $1`, res.DocumentID).Scan(&linkType, &linkID, &linkClient); err != nil {
		t.Fatal(err)
	}
	if linkType != "client" || linkID != e.f.Client1 || linkClient != e.f.Client1 {
		t.Errorf("link %s %s client %s, want client %s", linkType, linkID, linkClient, e.f.Client1)
	}
	if got := e.listed(t, e.as(e.f.Client2)); len(got) != 0 {
		t.Errorf("principal of client 2 lists %v, want nothing", got)
	}
	if got := e.listed(t, e.as(e.f.Client1)); len(got) != 1 || got[0] != res.DocumentID {
		t.Errorf("principal of client 1 lists %v, want the report", got)
	}
	// The PDF of the client report reads the same scope.
	_, pdf := e.generate(t, e.as(e.f.Client1), fmt.Sprintf(`{"standard": "nis2", "format": "pdf", "client_id": %q}`, e.f.Client1))
	if text := pdfText(e.contentUncompressed(t, pdf.DocumentID)); strings.Contains(text, "cmp-report-ci-b") || strings.Contains(text, "cmp-report-ci-none") ||
		!strings.Contains(text, "cmp-report-ci-a") {
		t.Error("the client-1 PDF names the CI of client 2 or the CI without client, or lacks its own")
	}
	if rec := e.call(e.as(e.f.Client2), http.MethodPost, "/api/v1/compliance/reports",
		fmt.Sprintf(`{"standard": "nis2", "format": "pdf", "client_id": %q}`, e.f.Client1)); rec.Code != http.StatusNotFound {
		t.Errorf("report of client 1 by a principal of client 2: %d, want 404", rec.Code)
	}
}

// contentUncompressed renders the report data of a client report again
// without compression: the stored PDF has compressed streams.
func (e *reportEnv) contentUncompressed(t *testing.T, docID string) []byte {
	t.Helper()
	raw := e.content(t, docID)
	if !bytes.HasPrefix(raw, []byte("%PDF-")) {
		t.Fatal("no PDF")
	}
	data, err := e.jobs.reports.Collect(e.as(e.f.Client1), e.f.OrgA, &ReportParams{Standard: "nis2", Format: FormatPDF, ClientID: e.f.Client1})
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderPDF(data, false)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *reportEnv) listed(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rec := e.call(ctx, http.MethodGet, "/api/v1/compliance/reports", "")
	var list api.ListResponse[ReportEntry]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var out []string
	for _, r := range list.Data {
		out = append(out, r.DocumentID)
	}
	return out
}

// TestOrgReportRequiresOrgWidePrincipal: a principal of client 1 gets 403
// for the organization report and no job exists; invalid values are 422.
func TestOrgReportRequiresOrgWidePrincipal(t *testing.T) {
	e := newReportEnv(t)
	if rec := e.call(e.as(e.f.Client1), http.MethodPost, "/api/v1/compliance/reports", `{"standard": "iso27001", "format": "pdf"}`); rec.Code != http.StatusForbidden {
		t.Errorf("organization report of a client-1 principal: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, e.f.OrgA, ReportJobKind); n != 0 {
		t.Errorf("jobs after the refusal: %d, want 0", n)
	}
	for body, field := range map[string]string{`{"standard": "soc2", "format": "pdf"}`: "standard", `{"standard": "nis2", "format": "docx"}`: "format"} {
		rec := e.call(e.as(), http.MethodPost, "/api/v1/compliance/reports", body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 on %s", body, rec.Code, rec.Body.String(), field)
		}
	}
	if rec := e.call(e.as(), http.MethodPost, "/api/v1/compliance/reports",
		`{"standard": "nis2", "format": "pdf", "client_id": "00000000-0000-4000-8000-000000000000"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown client: %d, want 404", rec.Code)
	}
}

// TestReportPreviewUnchanged: GET /api/v1/compliance/report answers the
// JSON preview in its former form.
func TestReportPreviewUnchanged(t *testing.T) {
	e := newReportEnv(t)
	rec := e.call(e.as(), http.MethodGet, "/api/v1/compliance/report?standard=nis2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generated_at", "standard", "audit_integrity", "compliance_score", "failures_by_severity", "findings"} {
		if _, ok := body[key]; !ok {
			t.Errorf("preview lacks %s: %v", key, body)
		}
	}
	if body["standard"] != "nis2" {
		t.Errorf("standard %v", body["standard"])
	}
}
