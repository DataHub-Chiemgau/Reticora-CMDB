package ci_test

// Phase-1 acceptance audit, group T7-ui: test harness (no test of its own).
// PostgreSQL integration through the production CI and job handlers
// (ci.NewHandler + jobs.NewAPIHandler on the PG repositories). Used by
// bulk_integration_test.go (TestAuditT7BulkReportMatchesStoredStatus,
// WP-368), bulk_jobs_integration_test.go (TestAuditT7JobAPIHonoursClientScope,
// WP-404) and audit_t7_bulk_jobs_auditdefect_test.go (WP-181 defect).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// auditT7BulkEnv wires the production CI and job handlers on PostgreSQL.
type auditT7BulkEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	jobSvc *jobs.Service
	user   string
	// ctx is a client-1 user (the creator of the bulk jobs).
	ctx context.Context
}

func newAuditT7BulkEnv(t *testing.T, tag string) *auditT7BulkEnv {
	t.Helper()
	f := scopetest.Seed(t, tag)
	user := f.AppUser(t, f.OrgA, "audit-t7-bulk")
	scope := database.OrgWideScope(f.OrgA, user)
	scope.Clients = database.ScopeIDs(f.Client1)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user, ClientID: f.Client1})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}})
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithBulkJobs(jobSvc)).RegisterRoutes(mux)
	jobs.NewAPIHandler(jobSvc).WithKind(ci.BulkJobKind, identity.PermCIWrite, nil).RegisterRoutes(mux)
	return &auditT7BulkEnv{t: t, f: f, mux: mux, jobSvc: jobSvc, user: user, ctx: ctx}
}

func (e *auditT7BulkEnv) do(ctx context.Context, method, path, ifMatch string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// state reads status, version and whether a status override exists.
func (e *auditT7BulkEnv) state(id string) (status string, version int64, override bool) {
	e.t.Helper()
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status, version,
		EXISTS (SELECT 1 FROM ci_field_value v WHERE v.ci_id = ci.id AND v.field_name = 'status' AND v.override_at IS NOT NULL)
		FROM ci WHERE id = $1`, id).Scan(&status, &version, &override); err != nil {
		e.t.Fatalf("read ci %s: %v", id, err)
	}
	return status, version, override
}

// seedCIs inserts n active client-1 server CIs and returns their ids.
func (e *auditT7BulkEnv) seedCIs(prefix string, n int) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), $3 || g, 'active'
		FROM generate_series(1, $4::int) g RETURNING id::text`, e.f.OrgA, e.f.Client1, prefix, n)
	if err != nil {
		e.t.Fatalf("seed cis: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != n {
		e.t.Fatalf("seeded %d cis, want %d", len(ids), n)
	}
	return ids
}

func (e *auditT7BulkEnv) countStatus(ids []string, status string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND status = $2`, ids, status).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// startJob posts a bulk set_status that runs as a job and returns its id.
func (e *auditT7BulkEnv) startJob(ctx context.Context, ids []string, status string) string {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": ci.BulkSetStatus, "ids": ids, "status": status})
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("bulk with %d ids: %d %s, want 202", len(ids), w.Code, w.Body.String())
	}
	var acc struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &acc); err != nil || acc.JobID == "" {
		e.t.Fatalf("bulk 202 body %s: %v", w.Body.String(), err)
	}
	return acc.JobID
}

func (e *auditT7BulkEnv) waitTerminal(jobID string, within time.Duration) *jobs.Job {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for {
		j, err := e.jobSvc.Get(e.ctx, e.f.OrgA, jobID)
		if err == nil && j != nil && j.Status.Terminal() {
			return j
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job %s not terminal after %s (last %+v, err %v)", jobID, within, j, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// checkReport asserts the per-ID report against the stored status: an item
// reported ok must carry the target status in the database (API-08 "result
// per id"; BLK-01 partial failure report).
func (e *auditT7BulkEnv) checkReport(t *testing.T, res ci.BulkResult, target string, focus map[string]string) {
	t.Helper()
	for _, item := range res.Items {
		label, watched := focus[item.ID]
		if !watched {
			continue
		}
		got, _, _ := e.state(item.ID)
		if item.Result == "ok" && got != target {
			t.Errorf("%s: bulk report says %s/%d but ci.status = %q, want %q (the manual bulk change must win over the earlier manual override, or the item must report an error)",
				label, item.Result, item.Code, got, target)
		}
	}
}
