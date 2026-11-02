package server_test

// WP-503 (JOB-01, TEC-14, F-147): the job kinds the application submits run
// on the JOBS work queue on NATS; their handlers are registered in one
// place (jobkinds.go). Without NATS they stay on the in-process path.
// WP-514 (JOB-03, F-162): the registration declares which running kinds
// can be cancelled.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestEverySubmittedKindHasAHandler|TestJobKindsRunInProcessWithoutQueue|TestJobKindsDeclareCancellable' ./internal/server/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

const jobKindsOrg = "c0de0503-0000-4000-8000-000000000001"

// jobKindsEnv is the router on the in-memory repositories, with queue as
// its job queue (nil: without NATS).
type jobKindsEnv struct {
	handler http.Handler
	jobs    jobs.Repository
}

func newJobKindsEnv(t *testing.T, queue func(repo jobs.Repository) *jobs.Queue) (*jobKindsEnv, *jobs.Queue) {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.MemoryRepositories()
	// The CI of the state snapshots has a recorded state: an unknown
	// entity is 404 and no job (WP-516).
	if err = repos.History.Record(context.Background(), &history.Change{OrganizationID: jobKindsOrg, EntityType: "ci",
		EntityID: "c0de0503-0000-4000-8000-0000000000c1", ChangeType: "create", NewValue: map[string]any{"name": "wp-503"}}); err != nil {
		t.Fatal(err)
	}
	var q *jobs.Queue
	if queue != nil {
		q = queue(repos.Jobs)
	}
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	dispatcher := webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{PollInterval: -1})
	t.Cleanup(func() { _ = dispatcher.Shutdown(context.Background()) })
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-503", Entitlements: entitlements, Dispatcher: dispatcher,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		JobQueue:    q,
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return &jobKindsEnv{handler: middleware.TenantMiddleware(mux), jobs: repos.Jobs}, q
}

// jobKindsUser is the org_admin of the requests.
const jobKindsUser = "c0de0503-0000-4000-8000-0000000000f1"

// submit sends a request of an org_admin that answers with a job and
// returns the job id.
func (e *jobKindsEnv) submit(t *testing.T, path, body string) string {
	t.Helper()
	w := e.request(t, http.MethodPost, path, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST %s: %d %s, want 202 with a job", path, w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	id, _ := out["job_id"].(string)
	if id == "" {
		id, _ = out["id"].(string)
	}
	if id == "" {
		t.Fatalf("POST %s answered no job: %s", path, w.Body)
	}
	return id
}

// call sends a request without body of the org_admin.
func (e *jobKindsEnv) call(t *testing.T, method, path string) (int, string) {
	t.Helper()
	w := e.request(t, method, path, "")
	return w.Code, w.Body.String()
}

// request sends a request of an org_admin.
func (e *jobKindsEnv) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	keys, ok := permission.StandardRolePermissions("org_admin")
	if !ok {
		t.Fatal("standard role org_admin unknown")
	}
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: jobKindsUser, OrganizationID: jobKindsOrg, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "wp-503"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req.WithContext(ctx))
	return w
}

// bulkBody is a bulk status change of n ids, enough for a job.
func bulkBody(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%q", fmt.Sprintf("c0de0503-0000-4000-8000-%012x", 0x100+i))
	}
	return `{"action":"set_status","status":"maintenance","ids":[` + strings.Join(ids, ",") + `]}`
}

// delivery is a job message handed to Queue.Process.
type delivery struct {
	data  []byte
	acked bool
}

func (d *delivery) Data() []byte                     { return d.data }
func (d *delivery) Ack() error                       { d.acked = true; return nil }
func (d *delivery) NakWithDelay(time.Duration) error { return nil }

// submittedKinds reads the backend sources (without tests) and returns the
// kinds every call Submit(ctx, orgID, kind, params, opts) submits to the
// job queue, by call site; a kind that is not a string constant of the
// sources is reported as unresolved.
func submittedKinds(t *testing.T) (kinds map[string]string, unresolved []string) {
	t.Helper()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	consts := map[string]string{}
	type call struct {
		site, pkg string
		kind      ast.Expr
	}
	var calls []call
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			pkg := file.Name.Name
			ast.Inspect(file, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.ValueSpec:
					for i, name := range n.Names {
						if i < len(n.Values) {
							if lit, ok := n.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
									consts[pkg+"."+name.Name] = v
								}
							}
						}
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Submit" && len(n.Args) == 5 {
						calls = append(calls, call{site: fset.Position(n.Pos()).String(), pkg: pkg, kind: n.Args[2]})
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	kinds = map[string]string{}
	for _, c := range calls {
		var key string
		switch k := c.kind.(type) {
		case *ast.BasicLit:
			if v, err := strconv.Unquote(k.Value); err == nil {
				kinds[c.site] = v
				continue
			}
		case *ast.Ident:
			key = c.pkg + "." + k.Name
		case *ast.SelectorExpr:
			if x, ok := k.X.(*ast.Ident); ok {
				key = x.Name + "." + k.Sel.Name
			}
		}
		if v, ok := consts[key]; ok {
			kinds[c.site] = v
			continue
		}
		unresolved = append(unresolved, c.site)
	}
	return kinds, unresolved
}

// TestEverySubmittedKindHasAHandler covers WP-503 (JOB-01, TEC-14): with
// the job queue every kind the application submits has a handler on it —
// each Submit call of the sources and the jobs the API creates (bulk change
// from 100 ids, state snapshot) — and those jobs are queued for the queue,
// not run by a goroutine of the request; the queue's handler then runs
// them. Submitting a kind without a handler fails.
func TestEverySubmittedKindHasAHandler(t *testing.T) {
	env, queue := newJobKindsEnv(t, func(repo jobs.Repository) *jobs.Queue { return jobs.NewQueue(jobs.NewService(repo), nil) })

	sites, unresolved := submittedKinds(t)
	if len(unresolved) > 0 {
		t.Errorf("Submit calls whose kind is no string constant (the test cannot check them): %v", unresolved)
	}
	if len(sites) == 0 {
		t.Fatal("no Submit call found in the sources")
	}
	for site, kind := range sites {
		if !queue.Handles(kind) {
			t.Errorf("%s submits %s, which has no handler on the job queue", site, kind)
		}
	}

	ids := map[string]string{
		ci.BulkJobKind:          env.submit(t, "/api/v1/cis/bulk", bulkBody(ci.BulkJobFromIDs)),
		history.SnapshotJobKind: env.submit(t, "/api/v1/history/snapshots", `{"entity_type":"ci","entity_id":"c0de0503-0000-4000-8000-0000000000c1"}`),
	}
	ctx := context.Background()
	for kind, id := range ids {
		job, err := env.jobs.Get(ctx, jobKindsOrg, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Kind != kind || !queue.Handles(job.Kind) {
			t.Errorf("job %s of kind %s: no handler on the job queue for kind %s", id, kind, job.Kind)
		}
		// Queued for the queue: no goroutine of the request runs it.
		time.Sleep(50 * time.Millisecond)
		if job, _ = env.jobs.Get(ctx, jobKindsOrg, id); job.Status != jobs.StatusQueued || job.Attempts != 0 {
			t.Errorf("%s job after the request: %+v; want queued for the job queue", kind, job)
		}
		data, _ := json.Marshal(jobs.Message{OrganizationID: jobKindsOrg, JobID: id, Kind: kind})
		d := &delivery{data: data}
		if out := queue.Process(ctx, d); !out.Ran || !d.acked {
			t.Errorf("%s job on the queue: %+v", kind, out)
		}
		if job, _ = env.jobs.Get(ctx, jobKindsOrg, id); job.Status != jobs.StatusSucceeded || len(job.Result) == 0 {
			t.Errorf("%s job after its handler: %+v", kind, job)
		}
	}
	// The bulk report of the handler covers every id (here: unknown CIs).
	if job, _ := env.jobs.Get(ctx, jobKindsOrg, ids[ci.BulkJobKind]); job != nil {
		var report ci.BulkResult
		if err := json.Unmarshal(job.Result, &report); err != nil || report.Total != ci.BulkJobFromIDs || report.Failed != ci.BulkJobFromIDs {
			t.Errorf("bulk report: %+v %v", report, err)
		}
	}

	// Negative: a kind without a handler is refused, also one the job
	// service knows.
	if _, _, err := queue.Submit(ctx, jobKindsOrg, "wp503.unregistered", nil, jobs.EnqueueOptions{}); !errors.Is(err, jobs.ErrUnknownKind) {
		t.Errorf("submit of an unregistered kind: %v, want ErrUnknownKind", err)
	}
	if list, total, err := env.jobs.List(ctx, jobKindsOrg, jobs.Filter{Kind: "wp503.unregistered"}, api.PaginationParams{Limit: 10}); err != nil || total != 0 || len(list) != 0 {
		t.Errorf("refused submit stored a job: %d %v", total, err)
	}
}

// TestJobKindsRunInProcessWithoutQueue covers WP-503: without NATS the
// submitted kinds stay on the in-process path and their jobs end.
func TestJobKindsRunInProcessWithoutQueue(t *testing.T) {
	env, _ := newJobKindsEnv(t, nil)
	ctx := context.Background()
	for _, id := range []string{
		env.submit(t, "/api/v1/cis/bulk", bulkBody(ci.BulkJobFromIDs)),
		env.submit(t, "/api/v1/history/snapshots", `{"entity_type":"ci","entity_id":"c0de0503-0000-4000-8000-0000000000c1"}`),
	} {
		deadline := time.Now().Add(10 * time.Second)
		for {
			job, err := env.jobs.Get(ctx, jobKindsOrg, id)
			if err == nil && job.Status == jobs.StatusSucceeded {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("in-process job did not end: %+v %v", job, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestJobKindsDeclareCancellable covers WP-514 (JOB-03, UI-16, F-162): the
// registration of the router declares which running jobs can be cancelled
// (jobkinds.go): ci.bulk, history.snapshot and export can, a running
// mirrored review.merge cannot (409); the job API reports it as
// cancellable.
func TestJobKindsDeclareCancellable(t *testing.T) {
	env, _ := newJobKindsEnv(t, func(repo jobs.Repository) *jobs.Queue { return jobs.NewQueue(jobs.NewService(repo), nil) })
	ctx := context.Background()
	runner := jobs.NewService(env.jobs)
	running := map[string]string{
		ci.BulkJobKind:          env.submit(t, "/api/v1/cis/bulk", bulkBody(ci.BulkJobFromIDs)),
		history.SnapshotJobKind: env.submit(t, "/api/v1/history/snapshots", `{"entity_type":"ci","entity_id":"c0de0503-0000-4000-8000-0000000000c1"}`),
	}
	for kind, id := range running {
		if job, err := runner.ClaimJob(ctx, jobKindsOrg, id); err != nil || job == nil {
			t.Fatalf("claim %s: %+v %v", kind, job, err)
		}
	}
	for kind, ref := range map[string]string{"export": "export_job", "review.merge": "review_item"} {
		refID := "c0de0514-0000-4000-8000-0000000000a1"
		if err := runner.Track(ctx, jobKindsOrg, kind, ref, refID, jobKindsUser); err != nil {
			t.Fatal(err)
		}
		if err := runner.Mirror(ctx, jobKindsOrg, ref, refID, jobs.StatusRunning, ""); err != nil {
			t.Fatal(err)
		}
		job, err := env.jobs.GetByRef(ctx, jobKindsOrg, ref, refID)
		if err != nil || job.Status != jobs.StatusRunning {
			t.Fatalf("mirrored %s: %+v %v", kind, job, err)
		}
		running[kind] = job.ID
	}
	want := map[string]bool{ci.BulkJobKind: true, history.SnapshotJobKind: true, "export": true, "review.merge": false}
	for kind, id := range running {
		code, body := env.call(t, http.MethodGet, "/api/v1/jobs/"+id)
		var job map[string]any
		if err := json.Unmarshal([]byte(body), &job); err != nil || code != http.StatusOK || job["cancellable"] != want[kind] {
			t.Errorf("GET running %s: %d %s, want cancellable %v", kind, code, body, want[kind])
		}
		code, body = env.call(t, http.MethodDelete, "/api/v1/jobs/"+id)
		if wantCode := map[bool]int{true: http.StatusOK, false: http.StatusConflict}[want[kind]]; code != wantCode {
			t.Errorf("DELETE running %s: %d %s, want %d", kind, code, body, wantCode)
		}
	}
}
