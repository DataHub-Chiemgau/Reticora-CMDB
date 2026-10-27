// Ad-hoc discovery jobs run on their collector (WP-632, JOB-01, DIS-08,
// F-358; decision of the client of 2026-10-27: option A, execution).
//
// TestAuditK4DiscoveryJobIsExecuted (moved from
// k4_job_execution_auditdefect_test.go) reads the production code of the
// backend and the collector and requires that POST /api/v1/discovery/jobs
// is either not offered or that production code moves a job on: a call of
// UpdateJobStatus or an UPDATE of discovery_job outside UpdateJobStatus.
//
// TestDiscoveryJobExecutionOverNATS drives the scope channel
// (ServeCollectorScopes) over a real NATS connection (TEST_NATS_URL) and
// PostgreSQL as reticora_app with a simulated collector:
//   - a job created through the handler is pending (queued in the job
//     center); only its collector receives it: a collector of another
//     client of the organization and one of another organization get
//     nothing and cannot move it;
//   - running is recorded once, for a pending job; completed with the
//     result moves a running job on; the job center follows
//     (running, succeeded);
//   - a result whose errors left no record fails the job with the cause of
//     the first error (DIS-08);
//   - the reaper fails a pending and a running job without a report within
//     the deadline with cause timeout; a younger job and a job of another
//     executor (config kind iga_relay) stay pending, and a job of another
//     kind is never handed out.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v \
//	  -run 'TestAuditK4DiscoveryJobIsExecuted|TestDiscoveryJobExecutionOverNATS' ./internal/discovery/
package discovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditK4DiscoveryJobIsExecuted(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	if !strings.Contains(string(spec), "operationId: createDiscoveryJob") {
		return // the API no longer creates discovery jobs
	}

	var movers []string
	for _, root := range []string{filepath.Join("..", ".."), filepath.Join("..", "..", "..", "collector")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "vendor", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name == "UpdateJobStatus" {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.SelectorExpr:
						if n.Sel.Name == "UpdateJobStatus" {
							movers = append(movers, fset.Position(n.Pos()).String()+" "+fn.Name.Name)
						}
					case *ast.BasicLit:
						if n.Kind == token.STRING && strings.Contains(strings.ToLower(n.Value), "update discovery_job") {
							movers = append(movers, fset.Position(n.Pos()).String()+" "+fn.Name.Name)
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(movers) == 0 {
		t.Error("POST /api/v1/discovery/jobs creates pending discovery jobs, but no production code of the backend or the collector " +
			"moves a discovery job on (no call of UpdateJobStatus, no UPDATE of discovery_job): a job created through the API " +
			"stays pending (queued in the job center) forever; want an execution path (collector or worker) or no create operation")
	}
}

func TestDiscoveryJobExecutionOverNATS(t *testing.T) {
	f := scopetest.Seed(t, "02")
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required next to TEST_DATABASE_URL: the jobs run over the collectors' NATS channel")
	}
	bg := context.Background()
	orgA, orgB := f.OrgCtx(f.OrgA), f.OrgCtx(f.OrgB)
	repo := discovery.NewPGRepository(f.App)
	h := discovery.NewHandler(repo, nil).WithJobTracker(jobs.NewService(jobs.NewPGRepository(f.App)))

	c1 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "wp632-c1", Status: discovery.CollectorActive, Config: map[string]any{}}
	c2 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "wp632-c2", Status: discovery.CollectorActive, Config: map[string]any{}}
	cb := &discovery.Collector{OrganizationID: f.OrgB, Name: "wp632-cb", Status: discovery.CollectorActive, Config: map[string]any{}}
	for _, c := range []struct {
		ctx context.Context
		col *discovery.Collector
	}{{orgA, c1}, {orgA, c2}, {orgB, cb}} {
		if err := repo.RegisterCollector(c.ctx, c.col); err != nil {
			t.Fatal(err)
		}
	}

	// createJob posts a discovery job of collectorID through the handler,
	// as POST /api/v1/discovery/jobs does.
	createJob := func(collectorID, jobType string) discovery.Job {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/jobs",
			strings.NewReader(`{"collector_id":"`+collectorID+`","job_type":"`+jobType+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(tenant.WithTenant(orgA, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		w := httptest.NewRecorder()
		h.CreateJob(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /discovery/jobs = %d %s", w.Code, w.Body.String())
		}
		var job discovery.Job
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	stored := func(id string) *discovery.Job {
		t.Helper()
		job, err := repo.GetJob(orgA, f.OrgA, id)
		if err != nil {
			t.Fatalf("get discovery job %s: %v", id, err)
		}
		return job
	}
	// center returns status and error of the job center's job of a
	// discovery job.
	center := func(id string) (status, errMsg string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(error, '') FROM job
			WHERE ref_table = 'discovery_job' AND ref_id::text = $1`, id).Scan(&status, &errMsg); err != nil {
			t.Fatalf("job center row of discovery job %s: %v", id, err)
		}
		return status, errMsg
	}

	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()
	ctx, cancel := context.WithCancel(bg)
	served := make(chan error, 1)
	go func() { served <- h.ServeCollectorScopes(ctx, nc) }()
	defer func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("ServeCollectorScopes: %v", serr)
		}
	}()
	request := func(subject string, body, out any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		deadline := time.Now().Add(5 * time.Second)
		for {
			msg, rerr := nc.Request(subject, raw, 3*time.Second)
			if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond) // subscriptions not active yet
				continue
			}
			if rerr != nil {
				t.Fatalf("request %s: %v", subject, rerr)
			}
			if uerr := json.Unmarshal(msg.Data, out); uerr != nil {
				t.Fatalf("reply of %s: %v: %s", subject, uerr, msg.Data)
			}
			return
		}
	}
	subjectOf := func(orgID, collectorID string) string { return "collector." + orgID + "." + collectorID }
	openJobs := func(subject string) discovery.JobsResponse {
		t.Helper()
		var resp discovery.JobsResponse
		request(subject+discovery.JobsRequestSuffix, map[string]any{}, &resp)
		return resp
	}
	report := func(subject, jobID, status string, result map[string]any) string {
		t.Helper()
		var resp map[string]string
		request(subject+discovery.JobsStatusSuffix, map[string]any{"job_id": jobID, "status": status, "result": result}, &resp)
		if resp["status"] == "recorded" {
			return "recorded"
		}
		return resp["error"]
	}
	ids := func(resp discovery.JobsResponse) []string {
		out := []string{}
		for _, j := range resp.Jobs {
			out = append(out, j.ID+"/"+j.JobType)
		}
		return out
	}
	s1, s2, sb := subjectOf(f.OrgA, c1.ID), subjectOf(f.OrgA, c2.ID), subjectOf(f.OrgB, cb.ID)

	job := createJob(c1.ID, discovery.JobTypeSweep)
	if job.Status != discovery.JobStatusPending {
		t.Fatalf("created job status %q, want pending", job.Status)
	}
	if status, _ := center(job.ID); status != string(jobs.StatusQueued) {
		t.Errorf("job center status of the new job %q, want queued", status)
	}

	t.Run("only the job's collector receives and moves it", func(t *testing.T) {
		if got := ids(openJobs(s2)); len(got) != 0 {
			t.Errorf("collector of client 2 received %v, want no job (cross-client)", got)
		}
		if got := ids(openJobs(sb)); len(got) != 0 {
			t.Errorf("collector of organization B received %v, want no job (cross-org)", got)
		}
		if resp := openJobs(subjectOf(f.OrgB, c1.ID)); len(resp.Jobs) != 0 || resp.Error != "not available" {
			t.Errorf("c1 under organization B's subject: %+v, want refused without jobs", resp)
		}
		for name, subject := range map[string]string{"client 2": s2, "organization B": sb, "c1 under organization B": subjectOf(f.OrgB, c1.ID)} {
			if got := report(subject, job.ID, discovery.JobStatusRunning, nil); got != "not recorded" && got != "not available" {
				t.Errorf("running reported by %s: %q, want refused", name, got)
			}
		}
		if got := stored(job.ID); got.Status != discovery.JobStatusPending || got.StartedAt != "" {
			t.Errorf("job after foreign reports: %+v, want pending without start", got)
		}
		if got := ids(openJobs(s1)); len(got) != 1 || got[0] != job.ID+"/sweep" {
			t.Errorf("own collector received %v, want [%s/sweep]", got, job.ID)
		}
	})

	t.Run("pending, running, completed", func(t *testing.T) {
		if got := report(s1, job.ID, discovery.JobStatusCompleted, map[string]any{"records": 1}); got != "not recorded" {
			t.Errorf("completed before running: %q, want not recorded", got)
		}
		if got := report(s1, job.ID, discovery.JobStatusRunning, nil); got != "recorded" {
			t.Fatalf("running: %q, want recorded", got)
		}
		if got := report(s1, job.ID, discovery.JobStatusRunning, nil); got != "not recorded" {
			t.Errorf("second running report: %q, want not recorded (the job runs once)", got)
		}
		running := stored(job.ID)
		if running.Status != discovery.JobStatusRunning || running.StartedAt == "" || running.CompletedAt != "" {
			t.Errorf("job after running: %+v", running)
		}
		if status, _ := center(job.ID); status != string(jobs.StatusRunning) {
			t.Errorf("job center status %q, want running", status)
		}
		if got := ids(openJobs(s1)); len(got) != 0 {
			t.Errorf("a running job is handed out again: %v", got)
		}
		if got := report(s2, job.ID, discovery.JobStatusCompleted, map[string]any{"records": 9}); got != "not recorded" {
			t.Errorf("completed by the collector of client 2: %q, want not recorded", got)
		}
		if got := report(s1, job.ID, discovery.JobStatusCompleted, map[string]any{"records": 3, "scopes": 1, "duration_ms": 120}); got != "recorded" {
			t.Fatalf("completed: %q, want recorded", got)
		}
		done := stored(job.ID)
		if done.Status != discovery.JobStatusCompleted || done.CompletedAt == "" || done.Error != nil {
			t.Errorf("completed job: %+v", done)
		}
		if records, _ := done.ResultSummary["records"].(float64); records != 3 || done.ResultSummary["status"] != discovery.ScopeStateOK {
			t.Errorf("result of the completed job: %v, want records 3, status ok", done.ResultSummary)
		}
		if status, _ := center(job.ID); status != string(jobs.StatusSucceeded) {
			t.Errorf("job center status %q, want succeeded", status)
		}
		if got := report(s1, job.ID, discovery.JobStatusFailed, nil); got != "not recorded" {
			t.Errorf("failed after completion: %q, want not recorded", got)
		}
	})

	t.Run("errors without records fail the job with their cause", func(t *testing.T) {
		polled := createJob(c1.ID, discovery.JobTypePoll)
		if got := report(s1, polled.ID, discovery.JobStatusRunning, nil); got != "recorded" {
			t.Fatalf("running: %q", got)
		}
		result := map[string]any{"records": 0, "scopes": 1, "error_count": 1, "errors": []map[string]any{
			{"protocol": "redfish", "target": "10.63.2.9", "message": "redfish: GET /redfish/v1/Systems: 401 Unauthorized"}}}
		if got := report(s1, polled.ID, discovery.JobStatusCompleted, result); got != "recorded" {
			t.Fatalf("result: %q", got)
		}
		failed := stored(polled.ID)
		cause := discovery.JobErrorOf(failed)
		if failed.Status != discovery.JobStatusFailed || cause == nil || cause.Cause != discovery.CauseAuthFailed ||
			!strings.Contains(cause.Message, "401 Unauthorized") {
			t.Errorf("job whose only error is a 401: status %s, cause %+v; want failed with auth_failed", failed.Status, cause)
		}
		if status, errMsg := center(polled.ID); status != string(jobs.StatusFailed) || !strings.Contains(errMsg, "401") {
			t.Errorf("job center: %s %q, want failed with the error", status, errMsg)
		}
	})

	t.Run("reaper fails jobs without report after the deadline", func(t *testing.T) {
		waiting := createJob(c1.ID, discovery.JobTypeFull)
		stuck := createJob(c1.ID, discovery.JobTypeSweep)
		if got := report(s1, stuck.ID, discovery.JobStatusRunning, nil); got != "recorded" {
			t.Fatalf("running: %q", got)
		}
		fresh := createJob(c1.ID, discovery.JobTypeSweep)
		relay := &discovery.Job{OrganizationID: f.OrgA, CollectorID: c1.ID, JobType: discovery.JobTypePoll,
			Config: map[string]any{"kind": "iga_relay", "action": "create_account"}}
		if err := repo.CreateJob(orgA, relay); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-discovery.JobDeadline - 10*time.Minute)
		for _, q := range []struct{ sql, id string }{
			{`UPDATE discovery_job SET created_at = $2 WHERE id = $1`, waiting.ID},
			{`UPDATE discovery_job SET created_at = $2, started_at = $2 WHERE id = $1`, stuck.ID},
			{`UPDATE discovery_job SET created_at = $2 WHERE id = $1`, relay.ID},
		} {
			if _, err := f.Admin.Exec(bg, q.sql, q.id, old); err != nil {
				t.Fatal(err)
			}
		}
		if got := ids(openJobs(s1)); len(got) != 2 || got[0] != waiting.ID+"/full" || got[1] != fresh.ID+"/sweep" {
			t.Errorf("open jobs %v, want the waiting and the fresh job (no iga_relay job)", got)
		}

		reaper := discovery.NewJobReaper(f.App, repo)
		if reaper == nil {
			t.Fatal("no reaper for the PG repository")
		}
		if _, err := reaper.Run(bg); err != nil {
			t.Fatalf("reaper: %v", err)
		}
		for _, id := range []string{waiting.ID, stuck.ID} {
			got := stored(id)
			cause := discovery.JobErrorOf(got)
			if got.Status != discovery.JobStatusFailed || got.CompletedAt == "" || cause == nil || cause.Cause != discovery.CauseTimeout {
				t.Errorf("overdue job %s: status %s, cause %+v; want failed with cause timeout", id, got.Status, cause)
			}
			if status, errMsg := center(id); status != string(jobs.StatusFailed) || !strings.Contains(errMsg, "within") {
				t.Errorf("job center of overdue job %s: %s %q, want failed with the deadline", id, status, errMsg)
			}
		}
		if got := stored(fresh.ID); got.Status != discovery.JobStatusPending {
			t.Errorf("job younger than the deadline: %s, want pending", got.Status)
		}
		if got := stored(relay.ID); got.Status != discovery.JobStatusPending {
			t.Errorf("iga_relay job: %s, want pending (another executor's job)", got.Status)
		}
		if got := report(s1, stuck.ID, discovery.JobStatusCompleted, map[string]any{"records": 2}); got != "not recorded" {
			t.Errorf("late result of a reaped job: %q, want not recorded", got)
		}
		if got := stored(stuck.ID); got.Status != discovery.JobStatusFailed {
			t.Errorf("reaped job after a late result: %s, want failed", got.Status)
		}
	})
}
