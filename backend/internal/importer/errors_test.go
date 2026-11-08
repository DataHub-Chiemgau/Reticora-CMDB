package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/go-chi/chi/v5"
)

// errorsPage is a page of GET /api/v1/imports/{id}/errors.
type errorsPage struct {
	Data       []RowError `json:"data"`
	Total      int        `json:"total"`
	HasMore    bool       `json:"has_more"`
	NextCursor string     `json:"next_cursor"`
}

func (s *importStack) rowErrors(t *testing.T, path string) errorsPage {
	t.Helper()
	w := s.do(t, http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("errors %s: %d %s", path, w.Code, w.Body.String())
	}
	var page errorsPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Data == nil {
		t.Fatalf("errors %s: %s (%v)", path, w.Body.String(), err)
	}
	return page
}

// endedJob enqueues an import job of docID, brings it to status and
// stores report as its report (nil: none).
func (s *importStack) endedJob(t *testing.T, docID string, status jobs.Status, report []byte) string {
	t.Helper()
	ctx := requestContext(testOrg, testUser, nil)
	job, _, err := s.jobs.Enqueue(ctx, testOrg, JobKind, JobParams{DocumentID: docID, DocumentVersion: 1, ProfileID: "p", Mode: "both"},
		jobs.EnqueueOptions{CreatedBy: testUser})
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case jobs.StatusCancelled:
		_, err = s.jobs.Cancel(ctx, testOrg, job.ID)
	case jobs.StatusRunning:
		_, err = s.jobs.ClaimJob(ctx, testOrg, job.ID)
	case jobs.StatusSucceeded:
		if _, err = s.jobs.ClaimJob(ctx, testOrg, job.ID); err == nil {
			_, err = s.jobs.Succeed(ctx, testOrg, job.ID, JobResult{ReportKey: ReportKey(testOrg, job.ID)})
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if report != nil {
		if err = s.files.Put(context.Background(), export.ExportBucket, ReportKey(testOrg, job.ID), bytes.NewReader(report),
			int64(len(report)), "text/csv"); err != nil {
			t.Fatal(err)
		}
	}
	return job.ID
}

// TestImportErrorsListsRowErrors: GET /imports/{id}/errors lists the
// issues of the stored report without its outcome lines, with line, field,
// code and message, paged by limit and next_cursor (finding of WP-322);
// queued, running, cancelled and invisible runs answer like the report
// route.
func TestImportErrorsListsRowErrors(t *testing.T) {
	s := newImportStack(t)

	// A real run: the two mapped columns missing in the header (no line)
	// and the malformed row 3; the written rows 2 and 4 are no errors.
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	docID := s.document(t, testOrg, "text/plain", []byte("a,b\n1,2\nx,y\"z\n3,4\n"), document.ScanClean, 0)
	jobID := s.start(t, docID, profileID)
	if job := s.wait(t, jobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job %s %s", job.Status, job.Error)
	}
	page := s.rowErrors(t, "/api/v1/imports/"+jobID+"/errors")
	if page.Total != 3 || len(page.Data) != 3 || page.HasMore || page.NextCursor != "" ||
		page.Data[0].Line != nil || page.Data[0].Code != CodeColumnMissing || page.Data[0].Field != "name" {
		t.Fatalf("errors of the run: %+v", page)
	}
	if e := page.Data[2]; e.Line == nil || *e.Line != 3 || e.Field != "4" || e.Code != CodeRowMalformed || e.Message == "" {
		t.Fatalf("malformed row: %+v", e)
	}

	// A stored report: outcome lines are left out, an error of the file
	// has no line, the apostrophe of a neutralized cell is dropped, and the
	// list pages by limit and cursor.
	rep := newReport()
	rep.addIssue(&Issue{Field: "serial", Code: CodeColumnMissing, Message: "the column serial is missing"})
	rep.addIssue(&Issue{Line: 1, Code: string(OutcomeCreated), Message: "ci-1"})
	rep.addIssue(&Issue{Line: 2, Field: "=cmd", Code: CodeTransformFailed, Message: "-1 is no date"})
	rep.addIssue(&Issue{Line: 3, Code: string(OutcomeSkipped), Message: "unchanged"})
	rep.addIssue(&Issue{Line: 3, Field: "tags", Code: CodeReadOnlyField, Message: "tags are read-only"})
	rep.addIssue(&Issue{Line: 4, Field: "name", Code: CodeInvalidValue, Message: "name is required"})
	content := rep.bytes()
	stored := s.endedJob(t, docID, jobs.StatusSucceeded, content)
	base := "/api/v1/imports/" + stored + "/errors"
	first := s.rowErrors(t, base+"?limit=3")
	if first.Total != 4 || len(first.Data) != 3 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	if e := first.Data[0]; e.Line != nil || e.Field != "serial" || e.Code != CodeColumnMissing {
		t.Fatalf("file error: %+v", e)
	}
	if e := first.Data[1]; e.Line == nil || *e.Line != 2 || e.Field != "=cmd" || e.Message != "-1 is no date" {
		t.Fatalf("neutralized error: %+v", e)
	}
	if e := first.Data[2]; e.Line == nil || *e.Line != 3 || e.Code != CodeReadOnlyField {
		t.Fatalf("note of a skipped row: %+v", e)
	}
	second := s.rowErrors(t, base+"?limit=3&cursor="+url.QueryEscape(first.NextCursor))
	if len(second.Data) != 1 || second.HasMore || second.NextCursor != "" || second.Data[0].Line == nil ||
		*second.Data[0].Line != 4 || second.Data[0].Code != CodeInvalidValue {
		t.Fatalf("second page: %+v", second)
	}
	wantProblem(t, s.do(t, http.MethodGet, base+"?cursor=forged", ""), http.StatusBadRequest, "")

	// Not ready while queued or running; cancelled, without report, of
	// another kind, for another organization and of a document the caller
	// does not see 404.
	notReady := ProblemType("import-report-not-ready")
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+s.endedJob(t, docID, jobs.StatusQueued, nil)+"/errors", ""),
		http.StatusConflict, notReady)
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+s.endedJob(t, docID, jobs.StatusRunning, nil)+"/errors", ""),
		http.StatusConflict, notReady)
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+s.endedJob(t, docID, jobs.StatusCancelled, content)+"/errors", ""),
		http.StatusNotFound, "")
	// A run without stored report (blob.ErrNotFound): 404 for the list
	// and the download link alike.
	unstored := s.endedJob(t, docID, jobs.StatusSucceeded, nil)
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+unstored+"/errors", ""), http.StatusNotFound, "")
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+unstored+"/report", ""), http.StatusNotFound, "")
	other, _, err := s.jobs.Enqueue(requestContext(testOrg, testUser, nil), testOrg, "ci.bulk", map[string]any{"document_id": docID},
		jobs.EnqueueOptions{CreatedBy: testUser})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+other.ID+"/errors", ""), http.StatusNotFound, "")
	wantProblem(t, s.call(t, requestContext(otherOrg, testUser, nil), http.MethodGet, base, ""), http.StatusNotFound, "")
	foreignDoc := s.document(t, otherOrg, "text/csv", csvRows(1), document.ScanClean, 0)
	wantProblem(t, s.do(t, http.MethodGet, "/api/v1/imports/"+s.endedJob(t, foreignDoc, jobs.StatusSucceeded, content)+"/errors", ""),
		http.StatusNotFound, "")

	// A report the list cannot read is 503, never a partial list.
	if err = s.files.Put(context.Background(), export.ExportBucket, ReportKey(testOrg, stored), strings.NewReader("garbage"), 7, "text/csv"); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, s.do(t, http.MethodGet, base, ""), http.StatusServiceUnavailable, "")
}

// brokenStore fails every read with an error other than a missing object.
type brokenStore struct{ blob.Store }

func (brokenStore) Get(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("connection refused")
}

// TestImportReportUnreachableStore: a blob store that cannot be read is 503
// for the error list and the report, never 404 (blob.ErrNotFound is the
// only missing object).
func TestImportReportUnreachableStore(t *testing.T) {
	s := newImportStack(t)
	docID := s.document(t, testOrg, "text/csv", csvRows(1), document.ScanClean, 0)
	jobID := s.endedJob(t, docID, jobs.StatusSucceeded, newReport().bytes())
	mux := chi.NewRouter()
	NewImportHandler(s.profiles, versionKeys{Repository: s.docs, keys: s.keys}, brokenStore{s.files}).
		WithJobs(s.jobs).RegisterRoutes(mux)
	for _, suffix := range []string{"/errors", "/report"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/imports/"+jobID+suffix, nil).
			WithContext(requestContext(testOrg, testUser, nil)))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", suffix, w.Code, w.Body.String())
		}
	}
}
