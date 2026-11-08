package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// bulkRevertEnv wires bulk and revert like server/router.go and
// server/jobkinds.go: the PostgreSQL job repository as change set store,
// the CI service with audit and outbox, bulk jobs also below 100 ids, the
// reverter of ci.bulk and the kind bulk.revert (WP-305).
type bulkRevertEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	jobSvc *jobs.Service
	typeID string
	// creator is a user of client 1 with ci:write, ctx its request.
	creator string
	ctx     context.Context
}

func newBulkRevertEnv(t *testing.T) *bulkRevertEnv {
	t.Helper()
	f := scopetest.Seed(t, "92")
	e := &bulkRevertEnv{t: t, f: f, creator: f.AppUser(t, f.OrgA, "wp305-creator")}
	e.ctx = e.principal(e.creator, f.Client1, identity.PermCIWrite)
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp305-dev", Name: "wp305-dev", IsObservable: true}
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.creator})
	if err := types.Create(orgCtx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	e.typeID = typ.ID
	if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, &citype.AttributeDefinition{CITypeID: typ.ID, Key: "note", DataType: "string"}); err != nil {
		t.Fatalf("define note: %v", err)
	}
	store := jobs.NewPGRepository(f.App)
	e.jobSvc = jobs.NewService(store)
	e.jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	e.jobSvc.Register(jobs.Kind{Name: jobs.RevertKind, MaxAttempts: 1})
	svc := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())).
		WithFieldResolver(types).WithBulkJobs(e.jobSvc).WithCreatorPolicy(allRights)
	revert := ci.NewBulkRevert(svc, e.jobSvc, store)
	e.mux = chi.NewRouter()
	e.mux.Use(permission.FieldPolicyMiddleware(phase1Resolver{}))
	ci.NewHandler(svc).RegisterRoutes(e.mux)
	jobs.NewAPIHandler(e.jobSvc).
		WithKind(ci.BulkJobKind, identity.PermCIWrite, revert.Revert).
		WithBulkKind(ci.BulkJobKind).
		WithKind(jobs.RevertKind, identity.PermCIWrite, nil).
		RegisterRoutes(e.mux)
	return e
}

// allRights is the creator policy of a queue job: every right.
func allRights(context.Context, *jobs.Job) (*permission.FieldPolicy, error) {
	return permission.UnrestrictedPolicy(identity.AllPermissions()), nil
}

// principal is the request context of user, restricted to client (empty:
// org-wide), with job:read, ci:read and perms.
func (e *bulkRevertEnv) principal(user, client string, perms ...identity.Permission) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, user)
	if client != "" {
		scope.Clients = database.ScopeIDs(client)
	}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: user, ClientID: client})
	return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: append([]identity.Permission{identity.PermJobRead, identity.PermCIRead}, perms...)})
}

func (e *bulkRevertEnv) do(ctx context.Context, method, path, ifMatch string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// cis inserts n active CIs of the test type of client and returns their ids.
func (e *bulkRevertEnv) cis(prefix, client string, n int) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, $3, $4 || g, 'active' FROM generate_series(1, $5::int) g RETURNING id::text`,
		e.f.OrgA, client, e.typeID, prefix, n)
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

func (e *bulkRevertEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (e *bulkRevertEnv) text(query string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return s
}

// syncBulk posts a bulk below 100 ids and returns its report (200).
func (e *bulkRevertEnv) syncBulk(ctx context.Context, body map[string]any) ci.BulkResult {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/cis/bulk", "", body)
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		e.t.Fatalf("bulk %v: %d %s, want 200", body["action"], w.Code, w.Body.String())
	}
	return res
}

// wait returns the job id once it ended.
func (e *bulkRevertEnv) wait(id string) *jobs.Job {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		job, err := e.jobSvc.Get(context.Background(), e.f.OrgA, id)
		if err == nil && job.Status.Terminal() {
			return job
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job %s did not end: %+v %v", id, job, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// revert posts the revert of job id and returns the status and the body.
func (e *bulkRevertEnv) revert(ctx context.Context, id string) (int, []byte, http.Header) {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/jobs/"+id+"/revert", "", nil)
	return w.Code, w.Body.Bytes(), w.Header()
}

// revertJob posts the revert of the bulk job id, expects 202 with the
// revert job and its Location and returns the revert job once it ended.
func (e *bulkRevertEnv) revertJob(ctx context.Context, id string) (*jobs.Job, ci.BulkRevertResult) {
	e.t.Helper()
	code, body, header := e.revert(ctx, id)
	var accepted jobs.Job
	if code != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil || accepted.Kind != jobs.RevertKind ||
		header.Get("Location") != "/api/v1/jobs/"+accepted.ID {
		e.t.Fatalf("revert of %s: %d %s, Location %q; want 202 with the revert job", id, code, body, header.Get("Location"))
	}
	done := e.wait(accepted.ID)
	if done.Status != jobs.StatusSucceeded {
		e.t.Fatalf("revert job ended %s: %s", done.Status, done.Error)
	}
	w := e.do(ctx, http.MethodGet, "/api/v1/jobs/"+accepted.ID+"/result", "", nil)
	var res ci.BulkRevertResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.BulkJobID != id {
		e.t.Fatalf("result of the revert job: %d %s", w.Code, w.Body.String())
	}
	return done, res
}

// problemType decodes the type of a problem body.
func problemType(body []byte) string {
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &p)
	return p.Type
}

// TestBulkRevertAgainstPostgres covers BLK-02 (undo of the CI field
// operations) and JOB-03 (revert of the bulk kinds over POST
// /jobs/{id}/revert), WP-305, against PostgreSQL with the runtime login:
//
//	(a) a bulk below 100 ids is a job ci.bulk (bulk_job_id), a bulk of 100
//	    ids a job as well; each changed field is a change set row with old
//	    and new value and the override before and after;
//	(b) the revert is 202 with the job bulk.revert, which writes the old
//	    values back with ci_change, audit entry and ci.changed per CI and
//	    marks the rows;
//	(c) a field changed by PATCH since the bulk stays and is skipped;
//	(d) 25 hours after the bulk the revert is 409 revert-expired;
//	(e) a client-scoped principal reverts only the CIs it sees;
//	(f) the creator without ci:write is refused (403);
//	(g) a second POST while the revert job runs answers the same job, the
//	    bulk job then records the revert and a further POST is 409; the
//	    revert of bulk.revert and a bulk without change set rows are 409;
//	    a bulk of set_owner is reverted since WP-783.
//
// The case with the job queue (h) is TestBulkRevertOnTheQueue.
func TestBulkRevertAgainstPostgres(t *testing.T) {
	e := newBulkRevertEnv(t)
	f := e.f

	t.Run("a: change set of synchronous and job bulks", func(t *testing.T) {
		ids := e.cis("wp305-a-", f.Client1, 3)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
		if res.BulkJobID == "" || res.Succeeded != 3 {
			t.Fatalf("synchronous bulk: %+v, want 3 ok and bulk_job_id", res)
		}
		if job := e.wait(res.BulkJobID); job.Kind != ci.BulkJobKind || job.Status != jobs.StatusSucceeded || job.CreatedBy != e.creator {
			t.Fatalf("bulk job of the synchronous bulk: %+v", job)
		}
		for _, id := range ids {
			if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND entity_id = $2 AND entity_type = 'ci'
				AND op = 'set' AND field_name = 'status' AND old_value = '"active"' AND new_value = '"maintenance"'
				AND override_before IS NULL AND override_after->>'override_reason' = $3
				AND override_after->>'override_author' = $4 AND (override_after->>'protected')::boolean
				AND client_id = $5 AND reverted_at IS NULL`, res.BulkJobID, id, "bulk:"+res.BulkJobID, e.creator, f.Client1); n != 1 {
				t.Errorf("CI %s: %d change set rows of status, want 1", id, n)
			}
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1`, res.BulkJobID); n != 3 {
			t.Errorf("change set of the synchronous bulk: %d rows, want 3", n)
		}

		// 100 ids: the job; one CI has an override of model before.
		many := e.cis("wp305-a100-", f.Client1, ci.BulkJobFromIDs)
		if w := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+many[0], "*", map[string]any{"model": "pre"}); w.Code != http.StatusOK {
			t.Fatalf("PATCH before the bulk: %d %s", w.Code, w.Body.String())
		}
		w := e.do(e.ctx, http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": "set_fields", "ids": many,
			"params": map[string]any{"fields": map[string]any{"model": "m-305", "attributes": map[string]any{"note": "n-305"}}}})
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.JobID == "" {
			t.Fatalf("bulk of 100 ids: %d %s, want 202", w.Code, w.Body.String())
		}
		if job := e.wait(accepted.JobID); job.Status != jobs.StatusSucceeded {
			t.Fatalf("bulk job: %s %s", job.Status, job.Error)
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND field_name = 'model'
			AND new_value = '"m-305"' AND override_after->>'override_value' = 'm-305'`, accepted.JobID); n != len(many) {
			t.Errorf("rows of model: %d, want %d", n, len(many))
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND field_name = 'note'
			AND old_value = 'null' AND new_value = '"n-305"' AND override_before IS NULL AND override_after IS NOT NULL`, accepted.JobID); n != len(many) {
			t.Errorf("rows of the attribute note: %d, want %d", n, len(many))
		}
		if got := e.text(`SELECT concat_ws('|', old_value::text, override_before->>'override_value', override_before->>'override_reason')
			FROM bulk_change_set WHERE job_id = $1 AND entity_id = $2 AND field_name = 'model'`, accepted.JobID, many[0]); got != `"pre"|pre|manual change` {
			t.Errorf("row of the overridden model: %s", got)
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1`, accepted.JobID); n != 2*len(many) {
			t.Errorf("change set of the job: %d rows, want %d", n, 2*len(many))
		}
		// A single PATCH writes no change set.
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE entity_id = $1 AND job_id <> $2`, many[0], accepted.JobID); n != 0 {
			t.Errorf("rows outside the bulk job: %d", n)
		}
	})

	t.Run("b: revert writes the old values", func(t *testing.T) {
		ids := e.cis("wp305-b-", f.Client1, 3)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
		_, report := e.revertJob(e.ctx, res.BulkJobID)
		if report.Total != 3 || report.Reverted != 3 || report.Skipped != 0 || report.Failed != 0 {
			t.Errorf("revert report: %+v", report)
		}
		for _, item := range report.Items {
			if item.Result != "reverted" || item.Code != http.StatusOK || len(item.Fields) != 0 {
				t.Errorf("revert item %+v", item)
			}
		}
		for _, id := range ids {
			if s := e.text(`SELECT status FROM ci WHERE id = $1`, id); s != "active" {
				t.Errorf("CI %s after the revert: %s", id, s)
			}
			if n := e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'status'`, id); n != 2 {
				t.Errorf("CI %s: %d ci_change rows of status, want bulk and revert", id, n)
			}
			if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.status_changed'`, id); n != 2 {
				t.Errorf("CI %s: %d audit entries, want bulk and revert", id, n)
			}
			if n := e.count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'`, id); n != 2 {
				t.Errorf("CI %s: %d events ci.changed, want bulk and revert", id, n)
			}
			// The CI had no status override before the bulk: the revert
			// removes the one the bulk created, also the one its manual
			// inverse write gave the field (WP-306).
			if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'status'
				AND override_at IS NOT NULL`, id); n != 0 {
				t.Errorf("CI %s: %d status overrides after the revert, want none as before the bulk", id, n)
			}
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND revert_outcome = 'reverted'
			AND reverted_at IS NOT NULL`, res.BulkJobID); n != 3 {
			t.Errorf("rows marked reverted: %d, want 3", n)
		}
	})

	t.Run("c: a field changed since the bulk is skipped", func(t *testing.T) {
		ids := e.cis("wp305-c-", f.Client1, 3)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_fields", "ids": ids,
			"params": map[string]any{"fields": map[string]any{"model": "c-model", "hostname": "c-host"}}})
		if w := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+ids[0], "*", map[string]any{"model": "patched"}); w.Code != http.StatusOK {
			t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
		}
		_, report := e.revertJob(e.ctx, res.BulkJobID)
		if report.Reverted != 3 || len(report.Items) != 3 {
			t.Fatalf("revert report: %+v", report)
		}
		for _, item := range report.Items {
			want := []string(nil)
			if item.ID == ids[0] {
				want = []string{"model"}
			}
			if item.Result != "reverted" || !slices.Equal(item.Fields, want) {
				t.Errorf("revert item %+v, want skipped fields %v", item, want)
			}
		}
		if got := e.text(`SELECT concat_ws('|', model, COALESCE(hostname, '-')) FROM ci WHERE id = $1`, ids[0]); got != "patched|-" {
			t.Errorf("CI changed since the bulk: %s, want the PATCH kept and hostname reverted", got)
		}
		if got := e.text(`SELECT concat_ws('|', COALESCE(model, '-'), COALESCE(hostname, '-')) FROM ci WHERE id = $1`, ids[1]); got != "-|-" {
			t.Errorf("CI without change since: %s", got)
		}
		if got := e.text(`SELECT revert_outcome FROM bulk_change_set WHERE job_id = $1 AND entity_id = $2 AND field_name = 'model'`,
			res.BulkJobID, ids[0]); got != "skipped" {
			t.Errorf("row of the changed field: %s, want skipped", got)
		}
		if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND revert_outcome = 'reverted'`, res.BulkJobID); n != 5 {
			t.Errorf("rows reverted: %d, want 5", n)
		}
	})

	t.Run("d: the revert window is 24 hours", func(t *testing.T) {
		ids := e.cis("wp305-d-", f.Client1, 2)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
		if _, err := f.Admin.Exec(context.Background(), `UPDATE job SET created_at = created_at - interval '25 hours',
			run_after = run_after - interval '25 hours', started_at = started_at - interval '25 hours',
			finished_at = finished_at - interval '25 hours' WHERE id = $1`, res.BulkJobID); err != nil {
			t.Fatal(err)
		}
		code, body, _ := e.revert(e.ctx, res.BulkJobID)
		if code != http.StatusConflict || problemType(body) != jobs.ProblemRevertExpired {
			t.Errorf("revert after 25 hours: %d %s, want 409 %s", code, body, jobs.ProblemRevertExpired)
		}
		if n := e.count(`SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND status = 'maintenance'`, ids); n != 2 {
			t.Errorf("CIs changed by the refused revert: %d unchanged, want 2", n)
		}
	})

	t.Run("e: a client-scoped principal reverts the CIs it sees", func(t *testing.T) {
		ids := e.cis("wp305-e-", f.Client1, 3)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
		// One CI moves to client 2 after the bulk.
		if _, err := f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, ids[2], f.Client2); err != nil {
			t.Fatal(err)
		}
		other := e.principal(f.AppUser(t, f.OrgA, "wp305-client1"), f.Client1, identity.PermCIWrite)
		_, report := e.revertJob(other, res.BulkJobID)
		if report.Reverted != 2 || report.Skipped != 1 {
			t.Errorf("revert report: %+v, want 2 reverted and 1 skipped", report)
		}
		for _, item := range report.Items {
			if item.ID == ids[2] && (item.Result != "skipped" || item.Code != http.StatusNotFound) {
				t.Errorf("CI of client 2: %+v, want skipped 404", item)
			}
		}
		if s := e.text(`SELECT status FROM ci WHERE id = $1`, ids[2]); s != "maintenance" {
			t.Errorf("CI of client 2 after the revert of a client-1 principal: %s", s)
		}
		if n := e.count(`SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND status = 'active'`, ids[:2]); n != 2 {
			t.Errorf("client-1 CIs reverted: %d, want 2", n)
		}
	})

	t.Run("f: the creator needs ci:write", func(t *testing.T) {
		ids := e.cis("wp305-f-", f.Client1, 2)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
		withdrawn := e.principal(e.creator, f.Client1)
		if code, body, _ := e.revert(withdrawn, res.BulkJobID); code != http.StatusForbidden {
			t.Errorf("revert by the creator without ci:write: %d %s, want 403", code, body)
		}
		if n := e.count(`SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2 AND idempotency_key = $3`,
			f.OrgA, jobs.RevertKind, "revert:"+res.BulkJobID); n != 0 {
			t.Errorf("a refused revert queued %d revert jobs", n)
		}
	})

	t.Run("g: repeated POST, recorded revert and refusals", func(t *testing.T) {
		ids := e.cis("wp305-g-", f.Client1, 2)
		res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
		// A lock on a CI keeps the revert job running.
		bg := context.Background()
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err = tx.Exec(bg, `SELECT 1 FROM ci WHERE id = $1 FOR UPDATE`, ids[0]); err != nil {
			t.Fatal(err)
		}
		var first, second jobs.Job
		code, body, _ := e.revert(e.ctx, res.BulkJobID)
		if code != http.StatusAccepted || json.Unmarshal(body, &first) != nil {
			t.Fatalf("first revert: %d %s", code, body)
		}
		code, body, _ = e.revert(e.ctx, res.BulkJobID)
		if code != http.StatusAccepted || json.Unmarshal(body, &second) != nil || second.ID != first.ID || second.Status.Terminal() {
			t.Errorf("second revert while the first runs: %d %s, want 202 with the running job %s", code, body, first.ID)
		}
		if err = tx.Rollback(bg); err != nil {
			t.Fatal(err)
		}
		if done := e.wait(first.ID); done.Status != jobs.StatusSucceeded {
			t.Fatalf("revert job: %s %s", done.Status, done.Error)
		}
		bulk, err := e.jobSvc.Get(bg, f.OrgA, res.BulkJobID)
		if err != nil || bulk.RevertedAt == nil || bulk.RevertedBy != e.creator {
			t.Errorf("bulk job after its revert: %+v %v, want reverted_at and reverted_by", bulk, err)
		}
		if code, body, _ = e.revert(e.ctx, res.BulkJobID); code != http.StatusConflict || !bytes.Contains(body, []byte(jobs.ErrAlreadyReverted.Error())) {
			t.Errorf("revert after the recorded revert: %d %s, want 409 already reverted", code, body)
		}
		if code, body, _ = e.revert(e.ctx, first.ID); code != http.StatusConflict {
			t.Errorf("revert of bulk.revert: %d %s, want 409", code, body)
		}

		// A bulk that changed nothing has no change set row.
		same := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "active", "ids": e.cis("wp305-g0-", f.Client1, 2)})
		if code, body, _ = e.revert(e.ctx, same.BulkJobID); code != http.StatusConflict || !bytes.Contains(body, []byte(ci.ErrBulkNothingToRevert.Error())) {
			t.Errorf("revert of a bulk without change set: %d %s, want 409", code, body)
		}
		// set_owner had no revert in this work package; WP-783 reverts it
		// like the field operations.
		owner := e.syncBulk(e.ctx, map[string]any{"action": "set_owner", "ids": e.cis("wp305-go-", f.Client1, 1),
			"params": map[string]any{"owner_user_id": e.creator}})
		if _, ownerRes := e.revertJob(e.ctx, owner.BulkJobID); ownerRes.Reverted != 1 {
			t.Errorf("revert of set_owner: %+v, want the CI reverted", ownerRes)
		}
	})
}

// TestBulkRevertOnTheQueue covers case (h) of WP-305 with the job queue on
// NATS: a bulk below 100 ids is a job ci.bulk as well, and its revert job is
// submitted to the queue (Queue.Submit), whose handler runs it with the
// tenant and scope of its creator until succeeded. It runs only with
// TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
func TestBulkRevertOnTheQueue(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	e := newBulkRevertEnv(t)
	bg := context.Background()
	nc, err := nats.Connect(url, nats.Name("wp305"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	store := jobs.NewPGRepository(e.f.App)
	svc := jobs.NewService(store)
	queue := jobs.NewQueue(svc, js)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	queue.Stream, queue.SubjectPrefix, queue.SweepInterval = "JOBS_WP305_"+suffix, "wp305_"+suffix+".", time.Hour
	if err = queue.Setup(bg); err != nil {
		t.Fatal(err)
	}
	ciSvc := ci.NewService(ci.NewPGRepositoryWithAudit(e.f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())).
		WithFieldResolver(citype.NewPGRepository(e.f.App)).WithCreatorPolicy(allRights)
	revert := ci.NewBulkRevert(ciSvc, svc, store).WithQueue(queue)
	bulkKind := jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}
	queue.Handle(bulkKind, ciSvc.RunBulkJob)
	queue.Handle(jobs.Kind{Name: jobs.RevertKind, MaxAttempts: 1, Timeout: 30 * time.Minute}, revert.RunJob)
	ciSvc.WithBulkJobs(svc).WithBulkQueue(queue)
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)
	go queue.Run(ctx)

	e.jobSvc = svc
	e.mux = chi.NewRouter()
	e.mux.Use(permission.FieldPolicyMiddleware(phase1Resolver{}))
	ci.NewHandler(ciSvc).RegisterRoutes(e.mux)
	jobs.NewAPIHandler(svc).WithKind(ci.BulkJobKind, identity.PermCIWrite, revert.Revert).WithBulkKind(ci.BulkJobKind).
		WithKind(jobs.RevertKind, identity.PermCIWrite, nil).RegisterRoutes(e.mux)

	ids := e.cis("wp305-h-", e.f.Client1, 3)
	res := e.syncBulk(e.ctx, map[string]any{"action": "set_status", "status": "maintenance", "ids": ids})
	if job := e.wait(res.BulkJobID); res.BulkJobID == "" || job.Kind != ci.BulkJobKind || job.Status != jobs.StatusSucceeded {
		t.Fatalf("bulk below 100 ids with the queue: %+v %+v", res, job)
	}
	_, report := e.revertJob(e.ctx, res.BulkJobID)
	if report.Reverted != 3 {
		t.Errorf("revert on the queue: %+v", report)
	}
	if n := e.count(`SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND status = 'active'`, ids); n != 3 {
		t.Errorf("CIs reverted on the queue: %d, want 3", n)
	}
}
