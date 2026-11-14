package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
)

// The order approval as a workflow on PostgreSQL (WFL-04, WP-1108, E-230
// option 2): the submit of an order starts the run of the template
// order_approval in its transaction, the order remembers the run, the
// direct routes approve and reject are 409 while it is open, the decision
// of the approvers becomes the status of the order with the approver as
// approved_by, two approvals above 1000.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestOrder' ./internal/workflow/

type orderEnv struct {
	*approvalEnv
	orders *order.PGRepository
}

var orderPerms = []identity.Permission{identity.PermWorkflowRead, identity.PermWorkflowApprove, identity.PermOrderRead,
	identity.PermOrderWrite, identity.PermOrderApprove}

func newOrderEnv(t *testing.T, starter order.RunStarter) *orderEnv {
	t.Helper()
	a := newApprovalEnv(t)
	e := &orderEnv{approvalEnv: a, orders: order.NewPGRepository(a.f.App)}
	e.exec.WithOrders(e.orders)
	// The approvers of an order hold order:approve as well (the right of
	// the direct routes); c holds only workflow:approve.
	var role string
	if err := a.f.Admin.QueryRow(context.Background(), `INSERT INTO role (organization_id, name, scope, permissions)
		VALUES ($1, 'wf-order-approvers', 'org', '["order:approve", "order:read"]'::jsonb) RETURNING id::text`, a.f.OrgA).Scan(&role); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{a.initiator, a.a, a.b} {
		if _, err := a.f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`,
			a.f.OrgA, u, role); err != nil {
			t.Fatal(err)
		}
	}
	if starter == nil {
		starter = NewOrderStarter(e.exec)
	}
	order.NewHandler(e.orders).WithRunStarter(starter).RegisterRoutes(e.api)
	return e
}

// activate stores the template order_approval as an active definition.
func (e *orderEnv) activate(t *testing.T) *Definition {
	t.Helper()
	tpl := UseCaseTemplateByKey("order_approval")
	def := tpl.Definition(e.f.OrgA, fmt.Sprintf("wf-order-%d", time.Now().UnixNano()))
	def.Active = true
	if err := validateWrite(def.Trigger, def.Conditions, def.Actions); err != nil {
		t.Fatalf("template: %v", err)
	}
	if err := e.repo.CreateDefinition(e.as(e.initiator, orderPerms), def); err != nil {
		t.Fatal(err)
	}
	return def
}

// newOrder stores a draft order of requester with total cost, of client
// if given.
func (e *orderEnv) newOrder(t *testing.T, requester string, total float64, client ...string) *order.Order {
	t.Helper()
	o := &order.Order{OrganizationID: e.f.OrgA, OrderNumber: fmt.Sprintf("B-%d", time.Now().UnixNano()), Title: "Switches",
		Status: order.StatusDraft, RequestedBy: requester, TotalCost: total, Currency: "EUR"}
	if len(client) > 0 {
		o.ClientID = client[0]
	}
	if err := e.orders.Create(e.as(requester, orderPerms), o); err != nil {
		t.Fatal(err)
	}
	return o
}

// post calls an order route as user and decodes the order.
func (e *orderEnv) post(t *testing.T, user, path string) (int, *order.Order, string) {
	t.Helper()
	rec := e.call(t, e.as(user, orderPerms), http.MethodPost, path, "")
	var o order.Order
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return rec.Code, &o, rec.Body.String()
}

func (e *orderEnv) order(t *testing.T, id string) *order.Order {
	t.Helper()
	o, err := e.orders.GetByID(e.as(e.initiator, orderPerms), e.f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOrderSubmitStartsApprovalRun(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 500)
	code, got, body := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	if code != http.StatusOK || got.Status != order.StatusSubmitted || got.ApprovalRunID == "" {
		t.Fatalf("submit: %d %s", code, body)
	}
	run := e.runOf(t, got.ApprovalRunID)
	if run.Trigger != TriggerOrderSubmitted || run.Status != StatusWaitingApproval || run.Context["order_id"] != o.ID ||
		run.Context["total_cost"] != float64(500) || run.Context["requested_by"] != e.initiator {
		t.Errorf("run %s %s context %v", run.Trigger, run.Status, run.Context)
	}
	if e.order(t, o.ID).ApprovalRunID != run.ID {
		t.Errorf("stored approval_run_id differs")
	}
	// A second active definition of the trigger is refused (409).
	second := UseCaseTemplateByKey("order_approval").Definition(e.f.OrgA, fmt.Sprintf("wf-order-2-%d", time.Now().UnixNano()))
	second.Active = true
	err := e.repo.CreateDefinition(e.as(e.initiator, orderPerms), second)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("second active order definition: %v, want a unique violation (409)", err)
	}
}

func TestOrderApprovalRunApproves(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 500)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	if code, res := e.decide(t, e.as(e.a, orderPerms), got.ApprovalRunID, DecisionApproved); code != http.StatusOK || res.Status != StatusSucceeded {
		t.Fatalf("approval: %d %+v", code, res)
	}
	if final := e.order(t, o.ID); final.Status != order.StatusApproved || final.ApprovedBy != e.a {
		t.Fatalf("order %s approved_by %s, want approved by the approver", final.Status, final.ApprovedBy)
	}
	run := e.runOf(t, got.ApprovalRunID)
	if run.Steps[0].Output["skipped"] != true || run.Steps[1].Output["skipped"] == true || run.Steps[2].Output["approved_by"] != e.a {
		t.Errorf("steps %+v", run.Steps)
	}
	// Approver = requester: the four-eyes rule fails order_decision.
	own := e.newOrder(t, e.a, 200)
	_, sub, _ := e.post(t, e.initiator, "/api/v1/orders/"+own.ID+"/submit")
	if _, res := e.decide(t, e.as(e.a, orderPerms), sub.ApprovalRunID, DecisionApproved); res == nil || res.Status != StatusFailed {
		t.Fatalf("approval by the requester: %+v, want the run failed", res)
	}
	steps := e.runOf(t, sub.ApprovalRunID).Steps
	if steps[2].Status != StatusFailed || !strings.Contains(steps[2].Error, "four-eyes") {
		t.Errorf("order_decision step %s %q, want failed with the four-eyes rule", steps[2].Status, steps[2].Error)
	}
	if e.order(t, own.ID).Status != order.StatusSubmitted {
		t.Errorf("order of the requester's own approval: %s, want submitted", e.order(t, own.ID).Status)
	}
}

func TestOrderApprovalRunRejects(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 500)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	// The rejection continues the run: order_decision rejects the order,
	// the requester is notified of the rejection, not of an approval.
	if code, res := e.decide(t, e.as(e.a, orderPerms), got.ApprovalRunID, DecisionRejected); code != http.StatusOK || res.Status != StatusSucceeded {
		t.Fatalf("rejection: %d %+v", code, res)
	}
	if final := e.order(t, o.ID); final.Status != order.StatusRejected || final.ApprovedBy != e.a {
		t.Fatalf("order %s by %s, want rejected by the approver", final.Status, final.ApprovedBy)
	}
	run := e.runOf(t, got.ApprovalRunID)
	if len(run.Steps) != 5 || run.Steps[2].Status != StatusSucceeded || run.Steps[2].Output["status"] != order.StatusRejected ||
		run.Steps[3].Output["skipped"] != true || run.Steps[4].Output["skipped"] == true || run.Context["order_status"] != order.StatusRejected {
		t.Fatalf("steps %+v context %v", run.Steps, run.Context)
	}
	if n := e.noticeRecipients(t, "workflow:"+run.ID+":4"); !n[e.initiator] || len(n) != 1 {
		t.Errorf("rejection notified %v, want the requester", n)
	}
	if n := e.noticeRecipients(t, "workflow:"+run.ID+":3"); len(n) != 0 {
		t.Errorf("approval notice of a rejected order to %v", n)
	}
}

// TestOrderApprovalNeedsOrderApprove: a user with workflow:approve but
// without order:approve is no approver of an order (403).
func TestOrderApprovalNeedsOrderApprove(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 500)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	if code, _ := e.decide(t, e.as(e.c, orderPerms), got.ApprovalRunID, DecisionApproved); code != http.StatusForbidden {
		t.Fatalf("approval without order:approve: %d, want 403", code)
	}
	if e.order(t, o.ID).Status != order.StatusSubmitted {
		t.Errorf("order after the refused approval: %s", e.order(t, o.ID).Status)
	}
}

// TestOrderDecisionOnlyInApprovalRun: order_decision of another trigger is
// a 422 on write, and a stored one does not decide the order (the step
// fails): neither threshold nor quorum of the approval run are bypassed.
func TestOrderDecisionOnlyInApprovalRun(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 1500)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	actions := []JSONMap{{"type": approvalActionType, "quorum": float64(1)}, {"type": orderDecisionActionType, "order_id": o.ID}}
	var fe *api.FieldError
	if err := validateWrite(JSONMap{"type": "manual"}, nil, actions); !errors.As(err, &fe) || fe.Field != "actions[1].type" {
		t.Fatalf("order_decision with trigger manual: %v, want 422 on actions[1].type", err)
	}
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-order-bypass-%d", time.Now().UnixNano()),
		Trigger: JSONMap{"type": "manual"}, Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(e.as(e.initiator, orderPerms), def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Trigger(e.as(e.initiator, orderPerms), e.f.OrgA, def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if _, res := e.decide(t, e.as(e.a, orderPerms), run.ID, DecisionApproved); res == nil || res.Status != StatusFailed {
		t.Fatalf("bypass run: %+v, want failed", res)
	}
	if final := e.order(t, o.ID); final.Status != order.StatusSubmitted || final.ApprovalRunID != got.ApprovalRunID {
		t.Errorf("order %s run %s, want submitted with its own approval run", final.Status, final.ApprovalRunID)
	}
}

// TestOrderDirectApproveIs409ForClientApprover: the approval run of an
// order of client 1 that an organization-wide user submitted lies in
// client 1; a client-scoped approver sees it and gets 409 on the direct
// routes while it is open.
func TestOrderDirectApproveIs409ForClientApprover(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 500, e.f.Client1)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	var client string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(client_id::text, '') FROM workflow_run WHERE id = $1`,
		got.ApprovalRunID).Scan(&client); err != nil {
		t.Fatal(err)
	}
	if client != e.f.Client1 {
		t.Errorf("client of the approval run: %q, want the order's", client)
	}
	ctx := e.as(e.a, orderPerms, e.f.Client1)
	for _, route := range []string{"approve", "reject"} {
		rec := e.call(t, ctx, http.MethodPost, "/api/v1/orders/"+o.ID+"/"+route, "")
		if rec.Code != http.StatusConflict {
			t.Errorf("%s by a client approver while the run is open: %d %s, want 409", route, rec.Code, rec.Body.String())
		}
	}
	// A run the caller cannot see (client 2 scope) is open as well.
	if open, err := NewOrderStarter(e.exec).RunOpen(e.as(e.a, orderPerms, e.f.Client2), e.f.OrgA, got.ApprovalRunID); err != nil || !open {
		t.Errorf("run invisible to the caller: open %v, %v; want open", open, err)
	}
	if code, res := e.decide(t, ctx, got.ApprovalRunID, DecisionApproved); code != http.StatusOK || res.Status != StatusSucceeded {
		t.Errorf("decision of the client approver: %d %+v", code, res)
	}
}

func TestOrderApprovalQuorumAboveThreshold(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 1500)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	if _, res := e.decide(t, e.as(e.a, orderPerms), got.ApprovalRunID, DecisionApproved); res == nil || res.Status != StatusWaitingApproval ||
		res.Quorum != 2 {
		t.Fatalf("first approval: %+v, want waiting for a second", res)
	}
	if e.order(t, o.ID).Status != order.StatusSubmitted {
		t.Fatalf("order after one approval: %s", e.order(t, o.ID).Status)
	}
	if _, res := e.decide(t, e.as(e.b, orderPerms), got.ApprovalRunID, DecisionApproved); res == nil || res.Status != StatusSucceeded {
		t.Fatalf("second approval: %+v", res)
	}
	if final := e.order(t, o.ID); final.Status != order.StatusApproved || final.ApprovedBy != e.b {
		t.Errorf("order %s by %s, want approved by the second approver", final.Status, final.ApprovedBy)
	}
}

func TestOrderDirectApproveIs409WhileRunOpen(t *testing.T) {
	e := newOrderEnv(t, nil)
	e.activate(t)
	o := e.newOrder(t, e.initiator, 500)
	_, got, _ := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit")
	for _, route := range []string{"approve", "reject"} {
		code, _, body := e.post(t, e.a, "/api/v1/orders/"+o.ID+"/"+route)
		if code != http.StatusConflict || !strings.Contains(body, got.ApprovalRunID) {
			t.Errorf("%s while the run is open: %d %s, want 409 naming the run", route, code, body)
		}
	}
	// Once the run ended, the direct route decides.
	if _, err := e.exec.Cancel(e.as(e.initiator, orderPerms), e.f.OrgA, got.ApprovalRunID); err != nil {
		t.Fatal(err)
	}
	if code, final, body := e.post(t, e.a, "/api/v1/orders/"+o.ID+"/approve"); code != http.StatusOK || final.Status != order.StatusApproved {
		t.Errorf("approve after the run ended: %d %s", code, body)
	}
}

// failingStarter fails every start of an approval run.
type failingStarter struct{}

func (failingStarter) StartApproval(context.Context, string, map[string]any) (string, func(context.Context), error) {
	return "", nil, errors.New("queue unavailable")
}
func (failingStarter) RunOpen(context.Context, string, string) (bool, error) { return false, nil }

func TestOrderSubmitRollsBackWhenRunStartFails(t *testing.T) {
	e := newOrderEnv(t, failingStarter{})
	o := e.newOrder(t, e.initiator, 500)
	if code, _, body := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit"); code < 500 {
		t.Fatalf("submit with a failing start: %d %s, want 5xx", code, body)
	}
	if got := e.order(t, o.ID); got.Status != order.StatusDraft || got.ApprovalRunID != "" {
		t.Errorf("order after the failed start: %s run %q, want draft", got.Status, got.ApprovalRunID)
	}
}

func TestOrderDirectApproveWithoutDefinition(t *testing.T) {
	e := newOrderEnv(t, nil)
	o := e.newOrder(t, e.initiator, 500)
	if code, got, body := e.post(t, e.initiator, "/api/v1/orders/"+o.ID+"/submit"); code != http.StatusOK || got.ApprovalRunID != "" {
		t.Fatalf("submit without definition: %d %s", code, body)
	}
	if code, got, body := e.post(t, e.a, "/api/v1/orders/"+o.ID+"/approve"); code != http.StatusOK || got.Status != order.StatusApproved ||
		got.ApprovedBy != e.a {
		t.Errorf("direct approve: %d %s", code, body)
	}
}
