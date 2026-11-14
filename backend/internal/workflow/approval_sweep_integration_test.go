package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// The system job workflow.approval_sweep on PostgreSQL (WFL-02, WP-1099,
// E-230 option 2) with a simulated clock: expiry with reject, escalation to
// a replacement circle and its second expiry, the reminder once, and a step
// that is decided while the sweep runs.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestSweep' ./internal/workflow/

// sweepAt runs the sweep of the env's organization at the clock at.
func (e *approvalEnv) sweepAt(t *testing.T, at time.Time) ApprovalSweepResult {
	t.Helper()
	sweep := NewApprovalSweep(e.exec, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil }).
		WithClock(func() time.Time { return at })
	res, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("sweep at %v: %v", at, err)
	}
	return res
}

// approvalStep reads status, error, output, due_at and reminded_at of the
// approval step (index 0) of run.
func (e *approvalEnv) approvalStep(t *testing.T, runID string) (status, errText string, output map[string]any, due, reminded *time.Time) {
	t.Helper()
	var raw []byte
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status, error, output, due_at, reminded_at FROM workflow_step WHERE run_id = $1 AND step_index = 0`,
		runID).Scan(&status, &errText, &raw, &due, &reminded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	return status, errText, output, due, reminded
}

func (e *approvalEnv) runStatus(t *testing.T, runID string) string {
	t.Helper()
	var status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM workflow_run WHERE id = $1`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// stepEvents returns the outbox events of the approval step of run.
func (e *approvalEnv) stepEvents(t *testing.T, runID string) (types []string, data []map[string]any) {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT o.type, o.data FROM event_outbox o
		JOIN workflow_step s ON s.id::text = o.entity_id::text WHERE s.run_id = $1 AND s.step_index = 0 ORDER BY o.sequence`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		types, data = append(types, typ), append(data, m)
	}
	return types, data
}

// noticeRecipients returns the users notified under dedupKey.
func (e *approvalEnv) noticeRecipients(t *testing.T, dedupKey string) map[string]bool {
	t.Helper()
	all, err := e.deliveries.List(e.as(e.a, approverPerms), e.f.OrgA, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, d := range all {
		if d.DedupKey == dedupKey {
			out[d.Recipient.UserID] = true
		}
	}
	return out
}

// TestSweepRejectsExpired: after the deadline with reject, run and step are
// cancelled with "approval deadline expired", workflow.approval_decided
// with decision expired lies in the outbox, the initiator is notified; a
// second sweep and a late decision change nothing.
func TestSweepRejectsExpired(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"users": []any{e.a}}, "deadline": "1h"}, e.ciID)
	if res := e.sweepAt(t, time.Now().Add(30*time.Minute)); res.changed() {
		t.Fatalf("sweep before the deadline: %+v, want nothing", res)
	}
	at := time.Now().Add(2 * time.Hour)
	if res := e.sweepAt(t, at); res.Rejected != 1 {
		t.Fatalf("sweep after the deadline: %+v, want one rejected", res)
	}
	status, errText, output, _, _ := e.approvalStep(t, run.ID)
	if status != StatusCancelled || errText != approvalExpiredText || output["decision"] != DecisionExpired {
		t.Errorf("step %s %q %v, want cancelled, %q and decision expired", status, errText, output, approvalExpiredText)
	}
	if got := e.runStatus(t, run.ID); got != StatusCancelled {
		t.Errorf("run %s, want cancelled", got)
	}
	types, data := e.stepEvents(t, run.ID)
	found := false
	for i, typ := range types {
		if typ == "workflow.approval_decided" {
			found = true
			if data[i]["decision"] != DecisionExpired || data[i]["run_id"] != run.ID || data[i]["client_id"] != e.f.Client1 {
				t.Errorf("approval_decided data %v, want decision expired with run and client", data[i])
			}
		}
	}
	if !found {
		t.Errorf("events of the step %v, want workflow.approval_decided", types)
	}
	if got := e.noticeRecipients(t, run.ID+":0:"+DecisionExpired); !got[e.initiator] || len(got) != 1 {
		t.Errorf("notified of the expiry %v, want the initiator %s", got, e.initiator)
	}
	if res := e.sweepAt(t, at); res.changed() {
		t.Errorf("second sweep in the same minute: %+v, want nothing", res)
	}
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusBadRequest {
		t.Errorf("decision after the expiry: %d, want 400 (not waiting)", code)
	}
}

// TestSweepEscalatesOnce: escalate extends the circle by escalate_to and
// moves the deadline once; a member of the replacement circle may approve
// (the earlier approval keeps counting), a second expiry rejects.
func TestSweepEscalatesOnce(t *testing.T) {
	e := newApprovalEnv(t)
	action := func() JSONMap {
		return JSONMap{"approvers": map[string]any{"users": []any{e.a, e.b}}, "quorum": 2.0, "deadline": "1h",
			"on_timeout": "escalate", "escalate_to": map[string]any{"users": []any{e.outsider}}}
	}
	run := e.start(t, action(), "")
	if code, res := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK || res.Approvals != 1 {
		t.Fatalf("approval of a: %d %+v", code, res)
	}
	if code, _ := e.decide(t, e.as(e.outsider, approverPerms), run.ID, DecisionApproved); code != http.StatusForbidden {
		t.Fatalf("decision of the replacement circle before the escalation: %d, want 403", code)
	}
	_, _, _, firstDue, _ := e.approvalStep(t, run.ID)
	at := firstDue.Add(time.Second)
	if res := e.sweepAt(t, at); res.Escalated != 1 || res.Rejected != 0 {
		t.Fatalf("sweep after the deadline: %+v, want one escalated", res)
	}
	status, _, output, due, _ := e.approvalStep(t, run.ID)
	if status != StatusWaitingApproval || output["escalated_at"] == nil || output["escalated_to"] == nil {
		t.Fatalf("escalated step %s %v, want waiting with escalated_at and escalated_to", status, output)
	}
	if want := at.Add(time.Hour); due == nil || due.Sub(want).Abs() > time.Second {
		t.Errorf("due_at after the escalation %v, want %v", due, want)
	}
	if res := e.sweepAt(t, at); res.changed() {
		t.Errorf("second sweep in the same minute: %+v, want nothing", res)
	}
	types, data := e.stepEvents(t, run.ID)
	escalated := 0
	for i, typ := range types {
		if typ == "workflow.approval_requested" && data[i]["escalated"] == true {
			escalated++
		}
	}
	if escalated != 1 {
		t.Errorf("events %v, want one workflow.approval_requested of the escalation", types)
	}
	// The extended circle: the original approvers without a decision (b)
	// plus escalate_to (outsider); a decided already.
	if got := e.noticeRecipients(t, run.ID+":0:escalated"); !got[e.outsider] || !got[e.b] || got[e.a] {
		t.Errorf("notified of the escalation %v, want the extended circle without a decision (%s, %s), not a (%s)", got, e.outsider, e.b, e.a)
	}
	code, res := e.decide(t, e.as(e.outsider, approverPerms), run.ID, DecisionApproved)
	if code != http.StatusOK || res.Approvals != 2 || res.Status != StatusSucceeded {
		t.Fatalf("approval of the replacement circle: %d %+v, want the quorum of 2 reached and the run succeeded", code, res)
	}

	// A second expiry of an escalated step rejects.
	other := e.start(t, action(), "")
	_, _, _, due, _ = e.approvalStep(t, other.ID)
	if res := e.sweepAt(t, due.Add(time.Second)); res.Escalated != 1 {
		t.Fatalf("first expiry: %+v, want escalated", res)
	}
	_, _, _, due, _ = e.approvalStep(t, other.ID)
	if res := e.sweepAt(t, due.Add(time.Second)); res.Rejected != 1 || res.Escalated != 0 {
		t.Fatalf("second expiry: %+v, want rejected", res)
	}
	if status, errText, _, _, _ := e.approvalStep(t, other.ID); status != StatusCancelled || errText != approvalExpiredText {
		t.Errorf("step after the second expiry %s %q, want cancelled", status, errText)
	}
}

// TestSweepKeepsConcurrentVote: a vote committed between the candidate
// query and the lock of the sweep keeps its approvals in the step output.
func TestSweepKeepsConcurrentVote(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"users": []any{e.a, e.b, e.outsider}}, "quorum": 3.0, "deadline": "1h",
		"on_timeout": "escalate", "escalate_to": map[string]any{"users": []any{e.outsider}}}, "")
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK {
		t.Fatalf("approval of a: %d", code)
	}
	_, _, _, due, _ := e.approvalStep(t, run.ID)
	at := due.Add(time.Second)
	store := e.exec.repo.(approvalSweepStore)
	ctx := e.as(e.a, approverPerms)
	candidates, err := store.dueApprovals(ctx, e.f.OrgA, at, nil, approvalSweepBatch)
	if err != nil {
		t.Fatal(err)
	}
	var step *dueApproval
	for i := range candidates {
		if candidates[i].RunID == run.ID {
			step = &candidates[i]
		}
	}
	if step == nil {
		t.Fatalf("candidates %+v, want the step of run %s", candidates, run.ID)
	}
	// b votes after the candidate query, before the sweep locks the step.
	if code, res := e.decide(t, e.as(e.b, approverPerms), run.ID, DecisionApproved); code != http.StatusOK || res.Approvals != 2 {
		t.Fatalf("approval of b: %d %+v", code, res)
	}
	sweep := NewApprovalSweep(e.exec, nil).WithClock(func() time.Time { return at })
	var res ApprovalSweepResult
	if err := sweep.sweepStep(ctx, store, e.f.OrgA, step, at, &res); err != nil || res.Escalated != 1 {
		t.Fatalf("sweep of the stale candidate: %+v %v, want escalated", res, err)
	}
	_, _, output, _, _ := e.approvalStep(t, run.ID)
	if output["approvals"] != 2.0 || output["escalated_at"] == nil {
		t.Errorf("output after the escalation %v, want approvals 2 (b's vote kept) and escalated_at", output)
	}
}

// TestSweepRemindsOnce: remind_before notifies the approvers without a
// decision once; two sweeps make one reminder.
func TestSweepRemindsOnce(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"users": []any{e.a, e.b}}, "quorum": 2.0, "deadline": "2h", "remind_before": "1h"}, "")
	if code, _ := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK {
		t.Fatalf("approval of a: %d", code)
	}
	if res := e.sweepAt(t, time.Now().Add(30*time.Minute)); res.changed() {
		t.Fatalf("sweep before the reminder: %+v, want nothing", res)
	}
	at := time.Now().Add(90 * time.Minute)
	if res := e.sweepAt(t, at); res.Reminded != 1 {
		t.Fatalf("first sweep: %+v, want one reminder", res)
	}
	if res := e.sweepAt(t, at.Add(time.Minute)); res.changed() {
		t.Fatalf("second sweep: %+v, want nothing", res)
	}
	got := e.noticeRecipients(t, run.ID+":0:remind")
	if !got[e.b] || got[e.a] || len(got) != 1 {
		t.Errorf("reminded %v, want only b (%s), not a who decided", got, e.b)
	}
	status, _, _, _, reminded := e.approvalStep(t, run.ID)
	if status != StatusWaitingApproval || reminded == nil {
		t.Errorf("step after the reminder %s, reminded_at %v; want waiting with reminded_at", status, reminded)
	}
}

// TestSweepSkipsDecided: a step whose run a decision holds locked is left
// alone (SKIP LOCKED), and once approved it stays succeeded.
func TestSweepSkipsDecided(t *testing.T) {
	e := newApprovalEnv(t)
	run := e.start(t, JSONMap{"approvers": map[string]any{"users": []any{e.a}}, "deadline": "1h"}, "")
	at := time.Now().Add(2 * time.Hour)
	bg := context.Background()
	tx, err := e.f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, `SELECT 1 FROM workflow_run WHERE id = $1 FOR UPDATE`, run.ID); err != nil {
		t.Fatal(err)
	}
	if res := e.sweepAt(t, at); res.changed() {
		t.Errorf("sweep while a decision holds the run: %+v, want the step skipped", res)
	}
	if err = tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if code, res := e.decide(t, e.as(e.a, approverPerms), run.ID, DecisionApproved); code != http.StatusOK || res.Status != StatusSucceeded {
		t.Fatalf("approval: %d %+v", code, res)
	}
	if res := e.sweepAt(t, at); res.changed() {
		t.Errorf("sweep after the approval: %+v, want nothing", res)
	}
	if status, _, _, _, _ := e.approvalStep(t, run.ID); status != StatusSucceeded {
		t.Errorf("approved step %s after the sweep, want succeeded", status)
	}
	if got := e.runStatus(t, run.ID); got != StatusSucceeded {
		t.Errorf("run %s, want succeeded", got)
	}
}
