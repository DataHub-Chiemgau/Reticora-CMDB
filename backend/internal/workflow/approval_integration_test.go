package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The approval model on PostgreSQL (WFL-02, RBA-05, NTF-05, WP-1098, E-230
// option 2): approver circle from users, teams and roles with
// workflow:approve in client and site of the run, quorum, exclusion of the
// initiator, one decision per approver (workflow_approval), the inbox and
// the events workflow.approval_requested and workflow.approval_decided.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestInitiatorMayNotApprove|TestQuorumTwoOfThree|TestRejectionEndsRun|TestNonApproverForbidden|TestDoubleDecisionConflict|TestApprovalInboxMine|TestApprovalEventsInOutbox|TestApprovalClientScope|TestApprovalActionValidation' ./internal/workflow/

type approvalEnv struct {
	*notifyEnv
	api chi.Router
	// initiator starts the runs; approvers a, b and c hold workflow:approve
	// through the role wf-approvers and are members of team; outsider holds
	// workflow:approve through another role and is in no team; noRight is
	// a member of team without workflow:approve.
	initiator, a, b, c, outsider, noRight string
	team                                  string
}

func newApprovalEnv(t *testing.T) *approvalEnv {
	t.Helper()
	n := newNotifyEnv(t)
	f, bg := n.f, context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	e := &approvalEnv{notifyEnv: n}
	approvers := scalar(`INSERT INTO role (organization_id, name, scope, permissions) VALUES ($1, 'wf-approvers', 'org', '["workflow:approve", "workflow:read"]'::jsonb) RETURNING id::text`, f.OrgA)
	others := scalar(`INSERT INTO role (organization_id, name, scope, permissions) VALUES ($1, 'wf-other-approvers', 'org', '["workflow:approve"]'::jsonb) RETURNING id::text`, f.OrgA)
	e.team = scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'wf-approval-team') RETURNING id::text`, f.OrgA)
	user := func(name, role string, member bool) string {
		id := f.AppUser(t, f.OrgA, name)
		if role != "" {
			exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, id, role)
		}
		if member {
			exec(`INSERT INTO team_member (team_id, user_id, organization_id) VALUES ($1, $2, $3)`, e.team, id, f.OrgA)
		}
		return id
	}
	e.initiator = user("wf-appr-initiator", approvers, true)
	e.a, e.b, e.c = user("wf-appr-a", approvers, true), user("wf-appr-b", approvers, true), user("wf-appr-c", approvers, true)
	e.outsider = user("wf-appr-outsider", others, false)
	e.noRight = user("wf-appr-noright", "", true)
	e.exec = NewExecutor(e.repo, nil, ci.NewPGRepository(f.App), nil, nil).
		WithNotifier(e.notifier, notify.NewPGResolver(f.App, permission.NewPGRepository(f.App))).
		WithApprovers(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)))
	e.api = chi.NewRouter()
	NewHandler(e.repo, e.exec).RegisterRoutes(e.api)
	return e
}

// as is the request context of user, org-wide or restricted to clients.
func (e *approvalEnv) as(user string, perms []identity.Permission, clients ...string) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, user)
	if len(clients) > 0 {
		scope.Clients = database.ScopeIDs(clients...)
	}
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	info := tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: user}
	if len(clients) == 1 {
		info.ClientID = clients[0]
	}
	ctx = tenant.WithTenant(ctx, info)
	return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: e.f.OrgA, Permissions: perms, Type: identity.PrincipalTypeUser})
}

var approverPerms = []identity.Permission{identity.PermWorkflowRead, identity.PermWorkflowApprove}

// start stores a definition with a require_approval action and a noop and
// starts a run of it as the initiator (in the request), on ciID if given.
func (e *approvalEnv) start(t *testing.T, approval JSONMap, ciID string) *Run {
	t.Helper()
	approval["type"] = approvalActionType
	ctx := e.as(e.initiator, approverPerms)
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-approval-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{approval, {"type": "noop"}}, Active: true}
	if err := validateApprovalActions(def.Actions); err != nil {
		t.Fatalf("approval action %v: %v", approval, err)
	}
	if err := e.repo.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	payload := JSONMap{}
	if ciID != "" {
		payload["ci_id"] = ciID
	}
	run, err := e.exec.Trigger(ctx, e.f.OrgA, def, "manual", payload)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusWaitingApproval {
		t.Fatalf("run %s steps %+v, want waiting_approval", run.Status, run.Steps)
	}
	return run
}

// decide posts a decision of user on run through the API.
func (e *approvalEnv) decide(t *testing.T, ctx context.Context, runID, decision string) (int, *ApprovalResult) {
	t.Helper()
	rec := e.call(t, ctx, http.MethodPost, "/api/v1/workflow-runs/"+runID+"/approval", `{"decision": "`+decision+`", "comment": "ok"}`)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var res struct {
		Run
		Approvals int `json:"approvals"`
		Quorum    int `json:"quorum"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decision %s: %v", rec.Body.String(), err)
	}
	return rec.Code, &ApprovalResult{Run: &res.Run, Approvals: res.Approvals, Quorum: res.Quorum}
}

func (e *approvalEnv) call(t *testing.T, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: fmt.Sprintf("wp1098-%d", time.Now().UnixNano()), Source: audit.SourceAPI})
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	return rec
}

// inbox lists the waiting approval step ids of GET /workflow-approvals.
func (e *approvalEnv) inbox(t *testing.T, ctx context.Context, query string) (int, []PendingApproval) {
	t.Helper()
	rec := e.call(t, ctx, http.MethodGet, "/api/v1/workflow-approvals"+query, "")
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var list api.ListResponse[PendingApproval]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("inbox %s: %v", rec.Body.String(), err)
	}
	return rec.Code, list.Data
}

func hasRun(items []PendingApproval, runID string) bool {
	for _, p := range items {
		if p.RunID == runID {
			return true
		}
	}
	return false
}

// TestInitiatorMayNotApprove: the initiator, who holds workflow:approve, is
// not in the circle (403); with exclude_initiator false it may approve.
func TestInitiatorMayNotApprove(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{}, "")
	if code, _ := e.decide(t, e.as(e.initiator, approverPerms), run.ID, DecisionApproved); code != http.StatusForbidden {
		t.Fatalf("decision of the initiator: %d, want 403", code)
	}
	own := e.start(t, JSONMap{"exclude_initiator": false}, "")
	code, res := e.decide(t, e.as(e.initiator, approverPerms), own.ID, DecisionApproved)
	if code != http.StatusOK || res.Status != StatusSucceeded || res.Approvals != 1 || res.Quorum != 1 {
		t.Fatalf("decision of the initiator with exclude_initiator false: %d %+v, want succeeded with 1 of 1", code, res)
	}
}

// TestQuorumTwoOfThree: with quorum 2 the first approval keeps the run
// waiting, the second continues it, a third is 400 (no longer waiting).
func TestQuorumTwoOfThree(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"roles": []any{"wf-approvers"}}, "quorum": 2.0}, "")
	code, res := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved)
	if code != http.StatusOK || res.Status != StatusWaitingApproval || res.Approvals != 1 || res.Quorum != 2 {
		t.Fatalf("first approval: %d %+v, want waiting with 1 of 2", code, res)
	}
	code, res = e.decide(t, e.as(e.b, approverPerms), run.ID, DecisionApproved)
	if code != http.StatusOK || res.Status != StatusSucceeded || res.Approvals != 2 || len(res.Steps) != 2 || res.Steps[0].Status != StatusSucceeded {
		t.Fatalf("second approval: %d %+v, want succeeded with 2 of 2 and both steps", code, res)
	}
	if code, _ = e.decide(t, e.as(e.c, approverPerms), run.ID, DecisionApproved); code != http.StatusBadRequest {
		t.Fatalf("third approval: %d, want 400", code)
	}
	var rows string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT string_agg(decision, ',' ORDER BY decided_at) || '/' || count(DISTINCT approver_id)
		FROM workflow_approval WHERE run_id = $1`, run.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != "approved,approved/2" {
		t.Errorf("workflow_approval rows %s, want two approvals of two approvers", rows)
	}
}

// TestRejectionEndsRun: a rejection cancels step and run; the following
// action is not executed.
func TestRejectionEndsRun(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"quorum": 2.0}, "")
	code, res := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionRejected)
	if code != http.StatusOK || res.Status != StatusCancelled || res.FinishedAt == nil {
		t.Fatalf("rejection: %d %+v, want cancelled", code, res)
	}
	if len(res.Steps) != 1 || res.Steps[0].Status != StatusCancelled || res.Steps[0].Error != approvalRejectedText {
		t.Fatalf("steps after the rejection %+v, want only the cancelled approval step", res.Steps)
	}
	if code, _ = e.decide(t, e.as(e.b, approverPerms), run.ID, DecisionApproved); code != http.StatusBadRequest {
		t.Fatalf("approval after the rejection: %d, want 400", code)
	}
}

// TestNonApproverForbidden: a holder of workflow:approve outside the team
// and the role of the action is 403, so is a user without the right. This
// router has no authorization middleware: the 403 here is the one of the
// circle (which resolves only holders of workflow:approve). The 403 of the
// route itself (POST /workflow-runs/{id}/approval needs workflow:approve)
// is pinned in the server package: route_permissions.txt and
// TestSpecialActionsRequireTheirOwnPermission.
func TestNonApproverForbidden(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"teams": []any{e.team}}}, "")
	if code, _ := e.decide(t, e.as(e.outsider, approverPerms), run.ID, DecisionApproved); code != http.StatusForbidden {
		t.Fatalf("decision outside the team: %d, want 403", code)
	}
	if code, _ := e.decide(t, e.as(e.noRight, []identity.Permission{identity.PermWorkflowRead}), run.ID, DecisionApproved); code != http.StatusForbidden {
		t.Fatalf("decision without workflow:approve: %d, want 403", code)
	}
	code, res := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved)
	if code != http.StatusOK || res.Status != StatusSucceeded {
		t.Fatalf("decision of a team member: %d %+v, want succeeded", code, res)
	}
}

// TestDoubleDecisionConflict: the same approver decides twice and gets 409.
func TestDoubleDecisionConflict(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"quorum": 2.0}, "")
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK {
		t.Fatalf("first decision: %d", code)
	}
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusConflict {
		t.Fatalf("second decision of the same approver: %d, want 409", code)
	}
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionRejected); code != http.StatusConflict {
		t.Fatalf("a rejection after the own approval: %d, want 409", code)
	}
	got, err := e.repo.GetRun(e.as(e.a, approverPerms), e.f.OrgA, run.ID)
	if err != nil || got.Status != StatusWaitingApproval {
		t.Fatalf("run after the conflicts: %+v %v, want waiting", got, err)
	}
	// A principal that is no user of the organization never votes (a NULL
	// approver would escape the unique key and count again and again).
	var stepID string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM workflow_step WHERE run_id = $1 AND step_index = 0`, run.ID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	for _, anon := range []string{"", "00000000-0000-4000-8000-00000000dead", "service-account"} {
		_, err := e.repo.decideVote(e.as(e.a, approverPerms), e.f.OrgA, run.ID, &approvalVote{StepID: stepID, Approver: anon, Approved: true, Quorum: 2})
		if !errors.Is(err, ErrNotApprover) {
			t.Errorf("vote of %q: %v, want ErrNotApprover", anon, err)
		}
	}
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO workflow_approval (organization_id, run_id, step_id, approver_id, decision)
		VALUES ($1, $2, $3, NULL, 'approved')`, e.f.OrgA, run.ID, stepID); err == nil {
		t.Error("a decision without approver was stored, want it refused")
	}
}

// TestApprovalInboxMine: the inbox shows the steps of the own circle on
// which the caller has not decided; the list of every step needs
// workflow:write.
func TestApprovalInboxMine(t *testing.T) {
	e := newApprovalEnv(t)
	shared := e.start(t, JSONMap{"approvers": map[string]any{"roles": []any{"wf-approvers"}}, "quorum": 2.0, "deadline": "3d", "message": "Bitte freigeben"}, "")
	onlyB := e.start(t, JSONMap{"approvers": map[string]any{"users": []any{e.b}}}, "")

	_, mine := e.inbox(t, e.as(e.a, approverPerms), "?mine=true")
	if !hasRun(mine, shared.ID) || hasRun(mine, onlyB.ID) {
		t.Fatalf("inbox of a: %+v, want the shared step only", mine)
	}
	for _, p := range mine {
		if p.RunID == shared.ID && (p.Quorum != 2 || p.Approvals != 0 || p.DueAt == nil || p.Message != "Bitte freigeben" || p.StepIndex != 0 || p.WorkflowName == "") {
			t.Errorf("inbox entry %+v, want quorum 2, no approval, the deadline and the message", p)
		}
	}
	if _, own := e.inbox(t, e.as(e.initiator, approverPerms), "?mine=true"); hasRun(own, shared.ID) || hasRun(own, onlyB.ID) {
		t.Errorf("inbox of the initiator %+v, want neither step (excluded)", own)
	}
	if _, b := e.inbox(t, e.as(e.b, approverPerms), "?mine=true"); !hasRun(b, shared.ID) || !hasRun(b, onlyB.ID) {
		t.Errorf("inbox of b %+v, want both steps", b)
	}
	// Paging (offset cursor, API-02): limit=1 gives one entry and a cursor
	// to the second, a limit above 200 is capped.
	first := e.call(t, e.as(e.b, approverPerms), http.MethodGet, "/api/v1/workflow-approvals?mine=true&limit=1", "")
	var page api.ListResponse[PendingApproval]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || first.Code != http.StatusOK {
		t.Fatalf("first page: %d %s", first.Code, first.Body.String())
	}
	if len(page.Data) != 1 || page.Total != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page %+v, want one of two entries with a cursor", page)
	}
	second := e.call(t, e.as(e.b, approverPerms), http.MethodGet, "/api/v1/workflow-approvals?mine=true&limit=1&cursor="+page.NextCursor, "")
	var next api.ListResponse[PendingApproval]
	if err := json.Unmarshal(second.Body.Bytes(), &next); err != nil || second.Code != http.StatusOK {
		t.Fatalf("second page: %d %s", second.Code, second.Body.String())
	}
	if len(next.Data) != 1 || next.HasMore || next.NextCursor != "" || next.Data[0].StepID == page.Data[0].StepID {
		t.Fatalf("second page %+v after %+v, want the other entry and no cursor", next, page)
	}
	capped := e.call(t, e.as(e.b, approverPerms), http.MethodGet, "/api/v1/workflow-approvals?mine=true&limit=500", "")
	var all api.ListResponse[PendingApproval]
	if err := json.Unmarshal(capped.Body.Bytes(), &all); err != nil || capped.Code != http.StatusOK || all.Limit != api.MaxPageLimit || all.TotalCapped {
		t.Fatalf("limit=500: %d %+v, want limit %d", capped.Code, all, api.MaxPageLimit)
	}
	if code, _ := e.decide(t, e.as(e.a, approverPerms), shared.ID, DecisionApproved); code != http.StatusOK {
		t.Fatalf("approval of a: %d", code)
	}
	if _, mine = e.inbox(t, e.as(e.a, approverPerms), "?mine=true"); hasRun(mine, shared.ID) {
		t.Errorf("inbox of a after the own decision: %+v, want the step gone", mine)
	}
	if _, b := e.inbox(t, e.as(e.b, approverPerms), "?mine=true"); !hasRun(b, shared.ID) {
		t.Errorf("inbox of b after the decision of a %+v, want the step with 1 of 2", b)
	} else {
		for _, p := range b {
			if p.RunID == shared.ID && p.Approvals != 1 {
				t.Errorf("state of the shared step %+v, want 1 approval", p)
			}
		}
	}
	if code, _ := e.inbox(t, e.as(e.a, approverPerms), "?mine=false"); code != http.StatusForbidden {
		t.Errorf("every step without workflow:write: %d, want 403", code)
	}
	writer := append([]identity.Permission{identity.PermWorkflowWrite}, approverPerms...)
	if code, all := e.inbox(t, e.as(e.a, writer), "?mine=false"); code != http.StatusOK || !hasRun(all, shared.ID) || !hasRun(all, onlyB.ID) {
		t.Errorf("every step with workflow:write: %d %+v, want both", code, all)
	}
	if code, _ := e.inbox(t, e.as(e.a, approverPerms), "?mine=maybe"); code != http.StatusUnprocessableEntity {
		t.Errorf("mine=maybe: %d, want 422", code)
	}
}

// TestApprovalEventsInOutbox: workflow.approval_requested and
// workflow.approval_decided lie in the outbox with their data, and the
// circle got the notification of the request (category workflow).
func TestApprovalEventsInOutbox(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"users": []any{e.a}}, "deadline": "48h"}, e.ciID)
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK {
		t.Fatalf("approval: %d", code)
	}
	var stepID string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM workflow_step WHERE run_id = $1 AND step_index = 0`, run.ID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	rows, err := e.f.Admin.Query(context.Background(), `SELECT type, data FROM event_outbox WHERE organization_id = $1 AND entity_id = $2 ORDER BY sequence`, e.f.OrgA, stepID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ string
		var raw []byte
		if serr := rows.Scan(&typ, &raw); serr != nil {
			t.Fatal(serr)
		}
		types = append(types, typ)
		var data map[string]any
		if uerr := json.Unmarshal(raw, &data); uerr != nil {
			t.Fatal(uerr)
		}
		if data["run_id"] != run.ID || data["step_id"] != stepID || data["ci_id"] != e.ciID || data["client_id"] != e.f.Client1 || data["quorum"] != 1.0 {
			t.Errorf("%s data %v, want run, step, CI, client and quorum", typ, data)
		}
		switch typ {
		case "workflow.approval_requested":
			if due, _ := data["due_at"].(string); due == "" {
				t.Errorf("approval_requested without due_at: %v", data)
			}
		case "workflow.approval_decided":
			if data["decision"] != DecisionApproved || data["approver_id"] != e.a || data["approvals"] != 1.0 {
				t.Errorf("approval_decided data %v", data)
			}
		}
	}
	if strings.Join(types, ",") != "workflow.approval_requested,workflow.approval_decided" {
		t.Fatalf("events of the step %v, want requested and decided", types)
	}
	all, err := e.deliveries.List(e.as(e.a, approverPerms), e.f.OrgA, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	notified := 0
	for _, d := range all {
		if d.DedupKey == run.ID+":0:requested" {
			notified++
			if d.Category != notify.CategoryWorkflow || d.Recipient.UserID != e.a {
				t.Errorf("notification %+v, want category workflow to approver a", d)
			}
		}
	}
	if notified == 0 {
		t.Error("the approver got no notification of the request")
	}
}

// TestApprovalClientScope: an approver restricted to client 1 does not see
// a run on a CI of client 2 (404) and finds it in no inbox.
func TestApprovalClientScope(t *testing.T) {
	e := newApprovalEnv(t)
	other := e.f.CI(t, e.f.OrgA, e.f.Client2, "wf-approval-ci2")
	run := e.start(t, JSONMap{}, other)
	ctx := e.as(e.a, approverPerms, e.f.Client1)
	if code, _ := e.decide(t, ctx, run.ID, DecisionApproved); code != http.StatusNotFound {
		t.Fatalf("decision on a run of client 2 by an approver of client 1: %d, want 404", code)
	}
	if _, mine := e.inbox(t, ctx, "?mine=true"); hasRun(mine, run.ID) {
		t.Errorf("inbox of the client-1 approver lists the run of client 2: %+v", mine)
	}
	if _, err := e.exec.Decide(ctx, e.f.OrgA, run.ID, DecisionApproved, ""); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Decide in the scope of client 1: %v, want ErrRunNotFound", err)
	}
	if code, res := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK || res.Status != StatusSucceeded {
		t.Errorf("decision of the org-wide approver: %d %+v, want succeeded", code, res)
	}
}

// TestApprovalActionValidation: invalid values of require_approval are 422
// on actions[i].<field> when the definition is written (AK2); no database.
func TestApprovalActionValidation(t *testing.T) {
	for field, action := range map[string]JSONMap{
		"quorum":            {"quorum": 0.0},
		"exclude_initiator": {"exclude_initiator": "no"},
		"deadline":          {"deadline": "31d"},
		"on_timeout":        {"on_timeout": "ignore"},
		"escalate_to":       {"on_timeout": "escalate", "deadline": "1d"},
		"remind_before":     {"deadline": "1d", "remind_before": "2d"},
		"approvers.users":   {"approvers": map[string]any{"users": []any{"no-uuid"}}},
		"approvers.groups":  {"approvers": map[string]any{"groups": []any{"x"}}},
	} {
		action["type"] = approvalActionType
		err := validateWrite(JSONMap{"type": "manual"}, nil, []JSONMap{{"type": "noop"}, action})
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "actions[1]."+field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422 on actions[1].%s", field, err, field)
		}
	}
	ok := JSONMap{"type": approvalActionType, "approvers": map[string]any{"roles": []any{"approvers"}}, "quorum": 3.0, "deadline": "3d",
		"on_timeout": "escalate", "escalate_to": map[string]any{"roles": []any{"managers"}}, "remind_before": "12h", "exclude_initiator": false}
	if err := validateWrite(JSONMap{"type": "manual"}, nil, []JSONMap{ok}); err != nil {
		t.Errorf("valid action: %v", err)
	}
}
