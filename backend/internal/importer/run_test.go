package importer

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// fakeWriter is a writer of entity that records the rows it got; decide
// gives the result of a row (default: created).
type fakeWriter struct {
	entity string
	decide func(row *WriteRow) (RowResult, error)
	mu     sync.Mutex
	lines  []int
	runs   []*RunInfo
}

func (w *fakeWriter) Entity() string { return w.entity }

func (w *fakeWriter) Write(_ context.Context, row *WriteRow) (RowResult, error) {
	w.mu.Lock()
	w.lines = append(w.lines, row.Line)
	w.runs = append(w.runs, row.Run)
	w.mu.Unlock()
	if w.decide != nil {
		return w.decide(row)
	}
	return RowResult{Outcome: OutcomeCreated}, nil
}

func (w *fakeWriter) written() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.lines)
}

func strPtr(s string) *string { return &s }

func TestTransformations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cell  string
		steps []Transform
		want  Value
		fails bool
	}{
		{"trim", "  a b ", []Transform{{Op: TransformTrim}}, Value{Text: "a b"}, false},
		{"date go layout", "31.12.2024", []Transform{{Op: TransformDateFormat, Layout: "02.01.2006"}}, Value{Text: "2024-12-31"}, false},
		{"date token layout", "12/31/2024", []Transform{{Op: TransformDateFormat, Layout: "MM/DD/YYYY"}}, Value{Text: "2024-12-31"}, false},
		{"date with time", "2024-12-31 23:05", []Transform{{Op: TransformDateFormat, Layout: "YYYY-MM-DD HH:mm"}}, Value{Text: "2024-12-31T23:05:00Z"}, false},
		{"date empty stays empty", "", []Transform{{Op: TransformDateFormat, Layout: "DD.MM.YYYY"}}, Value{Text: ""}, false},
		{"date mismatch", "2024-31-12", []Transform{{Op: TransformDateFormat, Layout: "DD.MM.YYYY"}}, Value{}, true},
		{"enum exact", "Prod", []Transform{{Op: TransformEnumMap, Values: map[string]string{"Prod": "production"}}}, Value{Text: "production"}, false},
		{"enum case", "PROD", []Transform{{Op: TransformEnumMap, Values: map[string]string{"prod": "production"}}}, Value{Text: "production"}, false},
		{"enum missing", "test", []Transform{{Op: TransformEnumMap, Values: map[string]string{"prod": "production"}}}, Value{}, true},
		{"split and trim", "a; b;;c ", []Transform{{Op: TransformSplit, Separator: ";"}, {Op: TransformTrim}},
			Value{List: []string{"a", "b", "c"}, IsList: true}, false},
		{"split then enum", "x|y", []Transform{{Op: TransformSplit, Separator: "|"}, {Op: TransformEnumMap, Values: map[string]string{"x": "1", "y": "2"}}},
			Value{List: []string{"1", "2"}, IsList: true}, false},
		{"constant", "ignored", []Transform{{Op: TransformConstant, Value: strPtr("fixed")}}, Value{Text: "fixed"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transform(tc.cell, tc.steps)
			if tc.fails {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != tc.want.Text || got.IsList != tc.want.IsList || strings.Join(got.List, ",") != strings.Join(tc.want.List, ",") {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMappingDefaultsAndTransformFailed(t *testing.T) {
	m := &Mapping{
		Columns: map[string]ColumnMapping{
			"Hostname": {Target: "name", Transforms: []Transform{{Op: TransformTrim}}},
			"Env":      {Target: "attributes.environment", Transforms: []Transform{{Op: TransformEnumMap, Values: map[string]string{"p": "production"}}}},
			"Bought":   {Target: "attributes.purchase_date", Transforms: []Transform{{Op: TransformDateFormat, Layout: "DD.MM.YYYY"}}},
			"Gone":     {Target: "attributes.gone"},
		},
		Defaults: map[string]string{"attributes.environment": "staging", "attributes.owner": "it"},
	}
	mp, issues := newMapper(m, []string{"hostname ", "Env", "Bought"})
	if len(issues) != 1 || issues[0].Code != CodeColumnMissing || issues[0].Field != "Gone" {
		t.Fatalf("missing column issues %+v", issues)
	}
	row, rowIssues := mp.apply(&Row{Line: 2, Cells: []string{" web-1 ", "", "01.02.2024"}})
	if len(rowIssues) != 0 {
		t.Fatalf("issues %+v", rowIssues)
	}
	// The header matches without case; empty values take the defaults.
	if row.Text("name") != "web-1" || row.Text("attributes.environment") != "staging" || row.Text("attributes.owner") != "it" ||
		row.Text("attributes.purchase_date") != "2024-02-01" || row.Line != 2 {
		t.Fatalf("mapped %+v", row.Values)
	}
	_, rowIssues = mp.apply(&Row{Line: 3, Cells: []string{"web-2", "x", "2024"}})
	if len(rowIssues) != 2 {
		t.Fatalf("issues %+v", rowIssues)
	}
	for _, is := range rowIssues {
		if is.Code != CodeTransformFailed || is.Line != 3 || (is.Field != "attributes.environment" && is.Field != "attributes.purchase_date") {
			t.Fatalf("issue %+v", is)
		}
	}
}

func TestReportCSV(t *testing.T) {
	rep := newReport()
	rep.withHeader([]string{"Hostname", "Serial"})
	rep.add(&ParseError{Code: CodeRowMalformed, Line: 4, Column: 2, Message: "bad quote"})
	rep.add(&ParseError{Code: CodeRowLimit, Message: "too many rows", Fatal: true})
	rep.addIssue(&Issue{Line: 5, Field: "name", Code: CodeTransformFailed, Message: "=HYPERLINK(1)"})
	writeOutcome(rep, 6, &RowResult{Outcome: OutcomeCreated, Message: "site A"})
	raw := string(rep.bytes())
	if !strings.HasPrefix(raw, "\ufeff") {
		t.Fatalf("no BOM: %q", raw)
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\ufeff")))
	r.Comma = ';'
	records, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"Zeile", "Feld", "Code", "Meldung"},
		{"4", "Serial", CodeRowMalformed, "bad quote"},
		{"", "", CodeRowLimit, "too many rows"},
		{"5", "name", CodeTransformFailed, "'=HYPERLINK(1)"},
		{"6", "", "created", "site A"},
	}
	if fmt.Sprint(records) != fmt.Sprint(want) {
		t.Fatalf("report %q, want %q", records, want)
	}
}

// TestRunResultCountsOutcomes: the job result sums the rows per outcome,
// the report lists every row and the projection carries the counters.
func TestRunResultCountsOutcomes(t *testing.T) {
	s := newImportStack(t)
	h := NewImportHandler(s.profiles, s.docs, s.blobs).WithJobs(s.jobs).WithStarters(allowStarters, allowStarters)
	w := &fakeWriter{entity: "ci", decide: func(row *WriteRow) (RowResult, error) {
		switch row.Values.Text("name") {
		case "host-000001":
			return RowResult{Outcome: OutcomeUpdated}, nil
		case "host-000002":
			return RowResult{Outcome: OutcomeSkipped, Message: "known"}, nil
		case "host-000003":
			return RowResult{Outcome: OutcomeFailed, Issues: []Issue{{Field: "name", Code: CodeInvalidValue, Message: "refused"}}}, nil
		case "host-000004":
			return RowResult{}, fmt.Errorf("driver: connection reset")
		}
		return RowResult{Outcome: OutcomeCreated}, nil
	}}
	h.WithWriter(w)
	s.mux = newMux(h, s.jobs)
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	docID := s.document(t, testOrg, "text/csv", csvRows(6), document.ScanClean, 0)
	jobID := s.start(t, docID, profileID)
	job := s.wait(t, jobID)
	var result JobResult
	if err := json.Unmarshal(job.Result, &result); err != nil || job.Status != jobs.StatusSucceeded {
		t.Fatalf("job %s %s (%v)", job.Status, job.Result, err)
	}
	if result != (JobResult{ReportKey: ReportKey(testOrg, jobID), Total: 6, Created: 2, Updated: 1, Skipped: 1, Failed: 2}) {
		t.Fatalf("result %+v", result)
	}
	if run := s.run(t, jobID); *run.RowsTotal != 6 || *run.RowsFailed != 2 {
		t.Fatalf("projection %+v", run)
	}
	got := s.report(t, jobID)
	for _, line := range []string{"2;;created;", "3;;updated;", "4;;skipped;known", "5;name;invalid_value;refused",
		"6;;write_failed;the row could not be written", "7;;created;"} {
		if !strings.Contains(got, line+"\n") && !strings.Contains(got, line) {
			t.Errorf("report lacks %q: %q", line, got)
		}
	}
	// The database error does not reach the report.
	if strings.Contains(got, "connection reset") {
		t.Fatalf("report leaks the cause: %q", got)
	}
	// The run of the writer carries the job and the mode.
	if r := w.runs[0]; r.JobID != jobID || r.Mode != "both" || r.Entity != "ci" || r.InitiatedBy != testUser || r.DryRun {
		t.Fatalf("run %+v", r)
	}
}

// newMux serves the import routes of h and the job API.
func newMux(h *ImportHandler, svc *jobs.Service) chi.Router {
	m := chi.NewRouter()
	h.RegisterRoutes(m)
	jobs.NewAPIHandler(svc).WithKind(JobKind, identity.PermImportRun, nil).RegisterRoutes(m)
	return m
}

// TestRunPrechecksWithoutWrite: a missing writer, a module switched off, a
// feature not entitled and a dry run without database end the run before
// its first row.
func TestRunPrechecksWithoutWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(h *ImportHandler, w *fakeWriter)
		dryRun bool
		code   string
	}{
		{"no writer", func(h *ImportHandler, w *fakeWriter) { w.entity = "asset"; h.writers = map[string]Writer{"asset": w} }, false, CodeEntityNotSupported},
		{"module off", func(h *ImportHandler, _ *fakeWriter) { h.WithGates(denyRoutes{}, nil) }, false, CodeModuleDisabled},
		{"feature off", func(h *ImportHandler, _ *fakeWriter) { h.WithGates(nil, denyFeature{key: "import"}) }, false, CodeFeatureNotEntitled},
		{"dry run in memory", func(*ImportHandler, *fakeWriter) {}, true, CodeDryRunUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newImportStack(t)
			w := &fakeWriter{entity: "ci"}
			h := NewImportHandler(s.profiles, s.docs, s.blobs).WithJobs(s.jobs).WithStarters(allowStarters, allowStarters).WithWriter(w)
			tc.setup(h, w)
			s.mux = newMux(h, s.jobs)
			profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
			docID := s.document(t, testOrg, "text/csv", csvRows(3), document.ScanClean, 0)
			body := fmt.Sprintf(`{"document_id":%q,"profile_id":%q,"dry_run":%t}`, docID, profileID, tc.dryRun)
			resp := s.do(t, http.MethodPost, "/api/v1/imports", body)
			if resp.Code != http.StatusAccepted {
				t.Fatalf("start: %d %s", resp.Code, resp.Body.String())
			}
			var accepted struct {
				JobID string `json:"job_id"`
			}
			_ = json.Unmarshal(resp.Body.Bytes(), &accepted)
			job := s.wait(t, accepted.JobID)
			if job.Status != jobs.StatusFailed || errorCode(job.Error) != tc.code {
				t.Fatalf("job %s %q, want failed with %s", job.Status, job.Error, tc.code)
			}
			if w.written() != 0 {
				t.Fatalf("%d rows written", w.written())
			}
			if got := s.report(t, accepted.JobID); !strings.Contains(got, ";"+tc.code+";") {
				t.Fatalf("report %q", got)
			}
		})
	}
}

type denyRoutes struct{}

func (denyRoutes) RouteAllowed(string) bool { return false }

// denyFeature entitles every feature but key.
type denyFeature struct{ key string }

func (d denyFeature) Status(_ context.Context, orgID, featureKey string) (entitlement.Entitlement, entitlement.Status, error) {
	if featureKey == d.key {
		return entitlement.Entitlement{OrganizationID: orgID, FeatureKey: featureKey}, entitlement.StatusNotEntitled, nil
	}
	return entitlement.Entitlement{OrganizationID: orgID, FeatureKey: featureKey, Enabled: true}, entitlement.StatusActive, nil
}

// TestStartNeedsEntityWriteRight: import:run alone does not start an
// import of CIs (403); no job is created.
func TestStartNeedsEntityWriteRight(t *testing.T) {
	s := newImportStack(t)
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	docID := s.document(t, testOrg, "text/csv", csvRows(1), document.ScanClean, 0)
	ctx := requestContextWith(testOrg, testUser, nil, identity.PermImportRun, identity.PermLocationWrite)
	wantProblem(t, s.call(t, ctx, http.MethodPost, "/api/v1/imports", startBody(docID, profileID)), http.StatusForbidden, "")
	if _, total, err := s.jobs.List(context.Background(), testOrg, jobs.Filter{}, pageAll()); err != nil || total != 0 {
		t.Fatalf("jobs: %d (%v)", total, err)
	}
	// The job parameters hold no rights, scopes or groups of the starter,
	// only the request facts; the starter is the creator of the job.
	jobID := s.start(t, docID, profileID)
	job := s.wait(t, jobID)
	var raw map[string]any
	if err := json.Unmarshal(job.Params, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"principal", "permissions", "groups", "scope", "permission_scopes"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("params hold %s: %s", key, job.Params)
		}
	}
	if job.CreatedBy != testUser || raw["source"] != audit.SourceUI || job.Status != jobs.StatusSucceeded {
		t.Fatalf("job %s by %s, params %s", job.Status, job.CreatedBy, job.Params)
	}
}

// TestRunReloadsStarter: the run rebuilds its starter from the creator of
// the job and their current role assignments (WP-775): a write right
// revoked after the start ends the run with import-forbidden; a
// deactivated or deleted user, a start by an API key without key reload
// and a handler without starter sources with import-principal-missing. No
// row is written.
func TestRunReloadsStarter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		setup  func(h *ImportHandler, st *fakeStarters)
		code   string
	}{
		{"right revoked", audit.SourceUI, func(_ *ImportHandler, st *fakeStarters) {
			st.perms = []identity.Permission{identity.PermImportRun, identity.PermLocationWrite}
		}, CodeForbidden},
		{"user deactivated", audit.SourceUI, func(_ *ImportHandler, st *fakeStarters) { st.inactive = true }, CodePrincipalMissing},
		{"api key", audit.SourceAPI, func(*ImportHandler, *fakeStarters) {}, CodePrincipalMissing},
		{"no starter sources", audit.SourceUI, func(h *ImportHandler, _ *fakeStarters) { h.WithStarters(nil, nil) }, CodePrincipalMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newImportStack(t)
			w := &fakeWriter{entity: "ci"}
			st := newStarters()
			h := NewImportHandler(s.profiles, s.docs, s.blobs).WithJobs(s.jobs).WithStarters(st, st).WithWriter(w)
			profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
			docID := s.document(t, testOrg, "text/csv", csvRows(2), document.ScanClean, 0)
			params := JobParams{DocumentID: docID, DocumentVersion: 1, ProfileID: profileID, Mode: "both",
				Starter:     &StarterRef{Type: identity.PrincipalTypeUser, Subject: testUser},
				ScopeParams: jobs.ScopeParams{RequestID: "req-775", Source: tc.source}}
			if tc.source == audit.SourceAPI {
				params.Starter = &StarterRef{Type: identity.PrincipalTypeAPIKey, Subject: testUser, KeyPrefix: "k779"}
			}
			job, _, err := s.jobs.Enqueue(requestContext(testOrg, testUser, nil), testOrg, JobKind, params,
				jobs.EnqueueOptions{CreatedBy: testUser})
			if err != nil {
				t.Fatal(err)
			}
			// Revoked after the start, before the run.
			tc.setup(h, st)
			queue := jobs.NewQueue(s.jobs, nil)
			queue.Handle(jobs.Kind{Name: JobKind, MaxAttempts: 1, Cancellable: true}, h.RunJob)
			data, _ := json.Marshal(jobs.Message{OrganizationID: testOrg, JobID: job.ID, Kind: JobKind})
			queue.Process(context.Background(), &memDelivery{data: data})
			done, _ := s.jobs.Get(context.Background(), testOrg, job.ID)
			if done.Status != jobs.StatusFailed || errorCode(done.Error) != tc.code {
				t.Fatalf("job %s %q, want failed with %s", done.Status, done.Error, tc.code)
			}
			if w.written() != 0 {
				t.Fatalf("%d rows written", w.written())
			}
		})
	}
}

// TestRunWithoutPrincipalFails: a job without the request facts of a
// session start fails before reading (fail-closed); on the job queue too.
func TestRunWithoutPrincipalFails(t *testing.T) {
	s := newImportStack(t)
	w := &fakeWriter{entity: "ci"}
	h := NewImportHandler(s.profiles, s.docs, s.blobs).WithJobs(s.jobs).WithStarters(allowStarters, allowStarters).WithWriter(w)
	profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
	docID := s.document(t, testOrg, "text/csv", csvRows(2), document.ScanClean, 0)
	ctx := requestContext(testOrg, testUser, nil)
	job, _, err := s.jobs.Enqueue(ctx, testOrg, JobKind, JobParams{DocumentID: docID, DocumentVersion: 1, ProfileID: profileID, Mode: "both"},
		jobs.EnqueueOptions{CreatedBy: testUser})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewQueue(s.jobs, nil)
	queue.Handle(jobs.Kind{Name: JobKind, MaxAttempts: 1, Cancellable: true}, h.RunJob)
	data, _ := json.Marshal(jobs.Message{OrganizationID: testOrg, JobID: job.ID, Kind: JobKind})
	queue.Process(context.Background(), &memDelivery{data: data})
	done, _ := s.jobs.Get(context.Background(), testOrg, job.ID)
	if done.Status != jobs.StatusFailed || errorCode(done.Error) != CodePrincipalMissing || done.Attempts != 1 {
		t.Fatalf("job %+v", done)
	}
	if w.written() != 0 {
		t.Fatal("rows written without principal")
	}
}

// memDelivery is a queue message without NATS.
type memDelivery struct {
	data  []byte
	acked bool
}

func (d *memDelivery) Data() []byte                     { return d.data }
func (d *memDelivery) Ack() error                       { d.acked = true; return nil }
func (d *memDelivery) NakWithDelay(time.Duration) error { return nil }

// TestLocationWriterInMemory: the location writer resolves the path
// without regard to case, creates the missing levels and refuses a site
// without client.
func TestLocationWriterInMemory(t *testing.T) {
	repo := locations.NewMemoryRepository()
	ctx := requestContext(testOrg, testUser, nil)
	site, err := repo.Create(ctx, testOrg, locations.CreateRequest{Kind: locations.KindSite, ClientID: "00000000-0000-4000-8000-0000000000c1", Name: "Munich"})
	if err != nil {
		t.Fatal(err)
	}
	w := NewLocationWriter(repo)
	run := &RunInfo{OrganizationID: testOrg, Mode: "both"}
	write := func(values map[string]string) RowResult {
		t.Helper()
		row := &WriteRow{Line: 2, Values: &MappedRow{Line: 2, Values: map[string]Value{}}, Run: run}
		for k, v := range values {
			row.Values.Values[k] = Value{Text: v}
		}
		res, werr := w.Write(ctx, row)
		if werr != nil {
			t.Fatal(werr)
		}
		return res
	}
	if res := write(map[string]string{"kind": "room", "path": "munich / HQ / 1.01"}); res.Outcome != OutcomeCreated {
		t.Fatalf("room: %+v", res)
	}
	if res := write(map[string]string{"kind": "Room", "parent": "MUNICH/hq", "name": "1.01"}); res.Outcome != OutcomeSkipped {
		t.Fatalf("known room: %+v", res)
	}
	if res := write(map[string]string{"kind": "rack", "path": "Munich/HQ"}); res.Outcome != OutcomeFailed || res.Issues[0].Code != CodeInvalidValue {
		t.Fatalf("short path: %+v", res)
	}
	if res := write(map[string]string{"kind": "building", "path": "Berlin/B1"}); res.Outcome != OutcomeFailed ||
		res.Issues[0].Code != CodeInvalidValue || res.Issues[0].Field != LocationClientTarget {
		t.Fatalf("site without client: %+v", res)
	}
	run.Mode = "update_existing"
	if res := write(map[string]string{"kind": "building", "path": "Munich/Annex"}); res.Outcome != OutcomeSkipped {
		t.Fatalf("update_existing: %+v", res)
	}
	children, _ := repo.List(ctx, testOrg, locations.Filter{ParentID: site.ID})
	if len(children) != 1 || children[0].Name != "HQ" {
		t.Fatalf("children %+v", children)
	}
	// A reference resolves without creating.
	resolver := RunLocationResolver(run, repo)
	if id, is := resolver.Resolve(ctx, locations.KindRoom, []string{"Munich", "HQ", "1.01"}, "location"); is != nil || id == "" {
		t.Fatalf("resolve: %q %+v", id, is)
	}
	if _, is := resolver.Resolve(ctx, locations.KindRoom, []string{"Munich", "HQ", "9.99"}, "location"); is == nil || is.Code != CodeLocationNotFound {
		t.Fatalf("resolve missing: %+v", is)
	}
}
