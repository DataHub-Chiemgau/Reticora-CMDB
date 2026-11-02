package iga_test

// WP-461 (TEN-06, IGA-01; F-056): the IGA handler dropped the errors of its
// repository writes. decideAccess discarded the error of CreateTask after
// DecideAccessRequest had committed in its own transaction: when the task
// could not be written the API answered 200 and the request stayed
// 'approved' without a provisioning task, without a log line or a metric.
// Repository.ApproveAccessRequest now decides and stores the task in one
// transaction; a revocation and its task, a drift remediation and its task,
// the findings of a reconciliation and the sync state of a connector are
// stored as one unit too, and every failed write is answered (no raw
// database text), logged and counted in reticora_iga_write_failures_total.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//	  go test -count=1 -run '^(TestApprovalNeverLosesTheProvisioningTask|TestIGAWriteFailuresAreAnswered)$' ./internal/iga/

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// failingApproval is the PostgreSQL repository whose approvals fail like a
// database outage while the provisioning task is written.
type failingApproval struct{ iga.Repository }

func (failingApproval) ApproveAccessRequest(context.Context, string, string, string, string) (*iga.AccessRequest, error) {
	return nil, errors.New(`ERROR: insert into "iga_provisioning_task" failed (SQLSTATE 08006)`)
}

// leaked reports a response body that carries database or injected text.
func leaked(body string) bool {
	for _, s := range []string{"wp461", "iga_provisioning_task", "SQLSTATE", "P0001", "08006", "injected"} {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

// TestApprovalNeverLosesTheProvisioningTask covers F-056 on PostgreSQL. The
// task write is made to fail in the database: a BEFORE INSERT trigger on
// iga_provisioning_task raises for the test entitlement. The approval then
// answers non-2xx without raw text, the request stays pending and no task
// exists; a normal approval stores exactly one add_group_member task. A
// revocation whose task fails keeps the review item undecided, a drift
// remediation whose task fails keeps the finding open. The wrapper variant
// fails ApproveAccessRequest itself: a generic 5xx, the request pending.
func TestApprovalNeverLosesTheProvisioningTask(t *testing.T) {
	f := scopetest.Seed(t, "d6")
	bg := context.Background()
	subject := f.AppUser(t, f.OrgA, "wp461-iga-subject")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: subject})
	pg := iga.NewPGRepository(f.App)
	conn := &iga.ConnectorConfig{OrganizationID: f.OrgA, Name: "wp461-iga", Type: "scim", Status: "active"}
	if err := pg.CreateConnector(ctx, conn); err != nil {
		t.Fatalf("connector: %v", err)
	}

	// The fault: every task of the organization whose entitlement or drift
	// is marked to fail is refused by the database.
	fn := fmt.Sprintf("wp461_fail_task_%d", time.Now().UnixNano())
	if _, err := f.Admin.Exec(bg, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.organization_id = '`+f.OrgA+`' AND (NEW.payload->>'entitlement' LIKE 'wp461-fail%' OR NEW.external_id = 'wp461-fail') THEN
		RAISE EXCEPTION 'wp461 injected task failure';
	END IF;
	RETURN NEW;
END $$`); err != nil {
		t.Fatalf("trigger function: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(bg, `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`); err != nil {
			t.Errorf("drop trigger function: %v", err)
		}
	})
	if _, err := f.Admin.Exec(bg, `CREATE TRIGGER `+fn+` BEFORE INSERT ON iga_provisioning_task FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	request := func(entitlement string) *iga.AccessRequest {
		t.Helper()
		ar := &iga.AccessRequest{OrganizationID: f.OrgA, RequesterID: subject, SubjectUserID: subject, ConnectorID: conn.ID, Entitlement: entitlement, Status: "pending"}
		if err := pg.CreateAccessRequest(ctx, ar); err != nil {
			t.Fatalf("access request: %v", err)
		}
		return ar
	}
	do := func(repo iga.Repository, path, body string) (int, string) {
		t.Helper()
		r := chi.NewRouter()
		iga.NewHandler(repo, nil, nil, nil, nil).RegisterRoutes(r)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	approve := func(repo iga.Repository, id string) (int, string) {
		t.Helper()
		return do(repo, "/api/v1/iga/access-requests/"+id+"/approve", `{}`)
	}
	state := func(id, entitlement string) (string, int) {
		t.Helper()
		var status string
		var tasks int
		if err := f.Admin.QueryRow(bg, `SELECT status FROM iga_access_request WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM iga_provisioning_task WHERE organization_id = $1 AND payload->>'entitlement' = $2`,
			f.OrgA, entitlement).Scan(&tasks); err != nil {
			t.Fatal(err)
		}
		return status, tasks
	}
	failures := func(operation string) float64 {
		return testutil.ToFloat64(iga.WriteFailures.WithLabelValues(operation))
	}

	// Positive control: a normal approval stores exactly one task.
	ok := request("wp461-grp-ok")
	if code, body := approve(pg, ok.ID); code != http.StatusOK {
		t.Fatalf("normal approve: %d %s, want 200", code, body)
	}
	if status, tasks := state(ok.ID, "wp461-grp-ok"); status != "approved" || tasks != 1 {
		t.Fatalf("normal approve: status %q, tasks %d; want approved and 1", status, tasks)
	}
	var action string
	if err := f.Admin.QueryRow(bg, `SELECT action FROM iga_provisioning_task WHERE organization_id = $1 AND payload->>'entitlement' = 'wp461-grp-ok'`, f.OrgA).Scan(&action); err != nil || action != iga.TaskActionAddGroupMember {
		t.Fatalf("normal approve: task action %q (%v), want %q", action, err, iga.TaskActionAddGroupMember)
	}

	// The database refuses the task: the approval is rolled back.
	before := failures("access_approval")
	failing := request("wp461-fail-grp")
	code, body := approve(pg, failing.ID)
	status, tasks := state(failing.ID, "wp461-fail-grp")
	if code >= 200 && code < 300 {
		t.Errorf("approve with a failing task write answered %d, want non-2xx", code)
	}
	if leaked(body) {
		t.Errorf("approve with a failing task write leaks database text: %s", body)
	}
	if status != "pending" || tasks != 0 {
		t.Errorf("approve with a failing task write: status %q, tasks %d; want pending and 0", status, tasks)
	}
	if got := failures("access_approval"); got != before+1 {
		t.Errorf("reticora_iga_write_failures_total{operation=access_approval} = %v, want %v", got, before+1)
	}

	// The wrapper fails ApproveAccessRequest itself: generic 5xx, pending.
	wrapped := request("wp461-grp-wrapped")
	code, body = approve(failingApproval{pg}, wrapped.ID)
	if code != http.StatusInternalServerError && code != http.StatusServiceUnavailable {
		t.Errorf("approve with a failing repository answered %d %s, want 5xx", code, body)
	}
	if leaked(body) {
		t.Errorf("approve with a failing repository leaks database text: %s", body)
	}
	if status, tasks := state(wrapped.ID, "wp461-grp-wrapped"); status != "pending" || tasks != 0 {
		t.Errorf("approve with a failing repository: status %q, tasks %d; want pending and 0", status, tasks)
	}

	// An unknown request is 404, not a failed write.
	before = failures("access_approval")
	if unknown, _ := approve(pg, "00000000-0000-4000-8000-0000000000d6"); unknown != http.StatusNotFound {
		t.Errorf("approve of an unknown request answered %d, want 404", unknown)
	}
	if got := failures("access_approval"); got != before {
		t.Errorf("an unknown request counted as a failed write: %v, want %v", got, before)
	}

	// A revocation whose deprovisioning task fails leaves the item undecided.
	rv := &iga.AccessReview{OrganizationID: f.OrgA, Name: "wp461-review"}
	if err := pg.CreateReview(ctx, rv, []iga.AccessReviewItem{{UserID: subject, ConnectorID: conn.ID, Entitlement: "wp461-fail-revoke"}}); err != nil {
		t.Fatalf("review: %v", err)
	}
	items, _, err := pg.ListReviewItems(ctx, f.OrgA, rv.ID, api.PaginationParams{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("review items: %v, %d", err, len(items))
	}
	code, body = do(pg, "/api/v1/iga/access-reviews/"+rv.ID+"/items/"+items[0].ID+"/decision", `{"decision":"revoke"}`)
	if code >= 200 && code < 300 || leaked(body) {
		t.Errorf("revoke with a failing task write answered %d %s, want non-2xx without database text", code, body)
	}
	var decision string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(decision,'') FROM iga_access_review_item WHERE id = $1`, items[0].ID).Scan(&decision); err != nil || decision != "" {
		t.Errorf("revoke with a failing task write: decision %q (%v), want none", decision, err)
	}

	// A remediation whose task fails keeps the finding open.
	finding := &iga.DriftFinding{OrganizationID: f.OrgA, ConnectorID: conn.ID, ExternalID: "wp461-fail", DriftType: "orphan_account", Expected: iga.JSONMap{}, Observed: iga.JSONMap{}}
	if err := pg.CreateDrift(ctx, finding); err != nil {
		t.Fatalf("drift: %v", err)
	}
	code, body = do(pg, "/api/v1/iga/drift/"+finding.ID+"/remediate", `{}`)
	if code >= 200 && code < 300 || leaked(body) {
		t.Errorf("remediate with a failing task write answered %d %s, want non-2xx without database text", code, body)
	}
	var driftStatus string
	var remediationTasks int
	if err := f.Admin.QueryRow(bg, `SELECT status, (SELECT count(*) FROM iga_provisioning_task WHERE external_id = 'wp461-fail' AND organization_id = $2) FROM iga_drift_finding WHERE id = $1`,
		finding.ID, f.OrgA).Scan(&driftStatus, &remediationTasks); err != nil || driftStatus != "open" || remediationTasks != 0 {
		t.Errorf("remediate with a failing task write: status %q, tasks %d (%v); want open and 0", driftStatus, remediationTasks, err)
	}

	// SCIM groups: the group and its members are stored in one
	// transaction. A member that is no user of the organization is 400
	// invalidValue and leaves no group; a valid member list is stored, a
	// member named twice once.
	scim := chi.NewRouter()
	iga.NewHandler(pg, user.NewPGRepository(f.App), nil, nil, nil).RegisterRoutes(scim)
	group := func(name string, members ...string) (int, string) {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/scim/v2/Groups",
			strings.NewReader(`{"displayName":"`+name+`","members":[{"value":"`+strings.Join(members, `"},{"value":"`)+`"}]}`))
		rec := httptest.NewRecorder()
		scim.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	teams := func(name string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM team WHERE organization_id = $1 AND name = $2`, f.OrgA, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code, body := group("wp461-group-bad", "00000000-0000-4000-8000-0000000000d7"); code != http.StatusBadRequest || leaked(body) || teams("wp461-group-bad") != 0 {
		t.Errorf("SCIM group with an unknown member: %d %s, %d groups; want 400 and none", code, body, teams("wp461-group-bad"))
	}
	if code, body := group("wp461-group-ok", subject, subject); code != http.StatusCreated || !strings.Contains(body, subject) || teams("wp461-group-ok") != 1 {
		t.Errorf("SCIM group with a member: %d %s, %d groups; want 201 with the member", code, body, teams("wp461-group-ok"))
	}
}

// failingWrites is a memory repository whose task, drift and sync writes
// fail like a database outage.
type failingWrites struct{ *iga.MemoryRepository }

var errOutage = errors.New(`ERROR: could not write to "iga_provisioning_task" (SQLSTATE 08006)`)

func (failingWrites) UpdateTask(context.Context, *iga.ProvisioningTask) error { return errOutage }
func (failingWrites) UpdateDrift(context.Context, *iga.DriftFinding) error    { return errOutage }
func (failingWrites) MarkConnectorSynced(context.Context, string, string, time.Time) error {
	return errOutage
}

// TestIGAWriteFailuresAreAnswered covers the other writes the handler
// dropped (F-056): retrying a task, remediating a drift finding and syncing
// a connector answer a generic 5xx and count the failure when the write
// fails; the task runner does not run an attempt it cannot record.
func TestIGAWriteFailuresAreAnswered(t *testing.T) {
	const org = "00000000-0000-4000-8000-0000000004a1"
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: "00000000-0000-4000-8000-0000000004a2"})
	mem := iga.NewMemoryRepository()
	conn := &iga.ConnectorConfig{OrganizationID: org, Name: "wp461", Type: iga.ConnectorTypeSCIM}
	if err := mem.CreateConnector(ctx, conn); err != nil {
		t.Fatal(err)
	}
	task := &iga.ProvisioningTask{OrganizationID: org, ConnectorID: conn.ID, Action: iga.TaskActionDisableAccount, ExternalID: "u1", Status: iga.TaskStatusFailed}
	if err := mem.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	finding := &iga.DriftFinding{OrganizationID: org, ConnectorID: conn.ID, ExternalID: "u1", DriftType: "orphan_account", Status: "open"}
	if err := mem.CreateDrift(ctx, finding); err != nil {
		t.Fatal(err)
	}
	repo := failingWrites{mem}
	r := chi.NewRouter()
	iga.NewHandler(repo, nil, nil, nil, nil).RegisterRoutes(r)

	for _, c := range []struct{ operation, path string }{
		{"task_retry", "/api/v1/iga/tasks/" + task.ID + "/retry"},
		{"drift_remediation", "/api/v1/iga/drift/" + finding.ID + "/remediate"},
		{"connector_sync", "/api/v1/iga/connectors/" + conn.ID + "/sync"},
	} {
		before := testutil.ToFloat64(iga.WriteFailures.WithLabelValues(c.operation))
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, c.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: answered %d %s, want 5xx", c.operation, rec.Code, rec.Body.String())
		}
		if leaked(rec.Body.String()) {
			t.Errorf("%s: leaks database text: %s", c.operation, rec.Body.String())
		}
		if got := testutil.ToFloat64(iga.WriteFailures.WithLabelValues(c.operation)); got != before+1 {
			t.Errorf("%s: reticora_iga_write_failures_total = %v, want %v", c.operation, got, before+1)
		}
	}

	// The runner records an attempt before it runs it; one it cannot
	// record does not run.
	before := testutil.ToFloat64(iga.WriteFailures.WithLabelValues("task_status"))
	runner := iga.NewTaskRunner(repo, iga.NewRegistry(nil, &http.Client{Timeout: time.Millisecond}), nil)
	if err := runner.RunTask(ctx, task); !errors.Is(err, errOutage) {
		t.Errorf("RunTask with a failing status write = %v, want the write error", err)
	}
	if task.Status != iga.TaskStatusRunning || task.Error != "" {
		t.Errorf("RunTask went on with the connector although the attempt was not recorded: status %q, error %q", task.Status, task.Error)
	}
	if got := testutil.ToFloat64(iga.WriteFailures.WithLabelValues("task_status")); got != before+1 {
		t.Errorf("task_status failures = %v, want %v", got, before+1)
	}
}
