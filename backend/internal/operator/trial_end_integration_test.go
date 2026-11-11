package operator

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// trialEnv is organization A in a standard trial with fallback essential
// that ends at endsAt: the trial rows of the standard plan, cmdb_core with
// max_cis 100 (the trial limit), settings.trial.
func trialEnv(t *testing.T, endsAt time.Time) *planEnv {
	t.Helper()
	e := newPlanEnv(t, entitlement.PlanStandard)
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `UPDATE entitlement SET limits = limits || '{"max_cis": 100}', source = 'selfsignup'
		WHERE organization_id = $1 AND feature_key = 'cmdb_core'`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(bg, `UPDATE organization SET settings = jsonb_set(COALESCE(settings, '{}'), '{trial}',
		jsonb_build_object('plan', 'standard', 'ends_at', $2::text, 'fallback_plan', 'essential')) WHERE id = $1`,
		e.f.OrgA, endsAt.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(bg, `DELETE FROM notice_sent WHERE organization_id = $1 AND kind = $2`, e.f.OrgA, TrialEndKind); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *planEnv) trialEnd() *TrialEnd {
	return NewTrialEnd(e.f.App, e.svc)
}

// runTrialEnd runs the job and returns how many of organization A's trials
// it changed (other organizations of the shared database may be due too).
func (e *planEnv) runTrialEnd() int {
	e.t.Helper()
	before := e.count(`SELECT count(*) FROM operator_audit WHERE operator_kind = 'system' AND target_org = $1`, e.f.OrgA)
	if _, err := e.trialEnd().Run(context.Background()); err != nil {
		e.t.Fatalf("trial end: %v", err)
	}
	return e.count(`SELECT count(*) FROM operator_audit WHERE operator_kind = 'system' AND target_org = $1`, e.f.OrgA) - before
}

// TestTrialEndSwitchesToFallbackPlan covers ENT-07 (WP-884, E-157): an
// ended trial becomes essential with max_cis 500 and the rows of the
// matrix; rows and CIs stay; one plan.change of operator kind system and
// one entitlement.changed are written.
func TestTrialEndSwitchesToFallbackPlan(t *testing.T) {
	e := trialEnv(t, time.Now().Add(-time.Hour))
	rowsBefore := len(e.rows())
	cis := e.count(`SELECT count(*) FROM ci WHERE organization_id = $1`, e.f.OrgA)
	events0 := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA)
	mark := e.count(`SELECT COALESCE(max(id), 0) FROM operator_audit`)
	if n := e.runTrialEnd(); n != 1 {
		t.Fatalf("trial end changed %d times, want once", n)
	}
	if e.orgPlan() != "essential" {
		t.Errorf("plan after the trial end: %s", e.orgPlan())
	}
	e.checkMatrix(entitlement.PlanEssential)
	rows := e.rows()
	if got := rows[entitlement.FeatureCMDBCore].Limits[entitlement.LimitMaxCIs]; got != 500 {
		t.Errorf("max_cis after the trial end: %d, want 500", got)
	}
	if len(rows) != rowsBefore {
		t.Errorf("rows after the trial end: %d, want %d", len(rows), rowsBefore)
	}
	if n := e.count(`SELECT count(*) FROM ci WHERE organization_id = $1`, e.f.OrgA); n != cis {
		t.Errorf("CIs after the trial end: %d, want %d", n, cis)
	}
	if n := e.count(`SELECT count(*) FROM operator_audit WHERE target_org = $1 AND operator_kind = 'system'
		AND operator_id = 'entitlement.trial_end' AND action = 'plan.change' AND details->>'reason' = 'trial_end' AND id > $2`,
		e.f.OrgA, mark); n != 1 {
		t.Errorf("audit entries of the trial end: %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA); n != events0+1 {
		t.Errorf("entitlement.changed events: %d new, want 1", n-events0)
	}
	if result, err := NewAuditor(e.f.App).Verify(context.Background()); err != nil || !result.Valid {
		t.Errorf("verify the audit chain: %+v, %v", result, err)
	}
}

// TestTrialEndRunsOnce covers AK3: a second run changes nothing, also when
// the plan were reset by hand (notice_sent).
func TestTrialEndRunsOnce(t *testing.T) {
	e := trialEnv(t, time.Now().Add(-time.Hour))
	if n := e.runTrialEnd(); n != 1 {
		t.Fatalf("first run: %d", n)
	}
	if n := e.runTrialEnd(); n != 0 {
		t.Errorf("second run changed %d times", n)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE organization SET plan = 'standard', plan_changed_at = NULL WHERE id = $1`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if n := e.runTrialEnd(); n != 0 || e.orgPlan() != "standard" {
		t.Errorf("run after the recorded trial end: %d changes, plan %s", n, e.orgPlan())
	}
}

// TestTrialEndSkipsOperatorChangedPlan covers AK1: a plan an operator
// changed since the creation stays.
func TestTrialEndSkipsOperatorChangedPlan(t *testing.T) {
	e := trialEnv(t, time.Now().Add(-time.Hour))
	if w, _ := e.change("op-1", `{"plan":"pro","reason":"bought pro"}`); w.Code != 200 {
		t.Fatalf("operator change: %d %s", w.Code, w.Body.String())
	}
	if n := e.runTrialEnd(); n != 0 || e.orgPlan() != "pro" {
		t.Errorf("trial end after an operator change: %d changes, plan %s", n, e.orgPlan())
	}
}

// TestTrialEndBeforeEndsAtDoesNothing covers AK1: a running trial stays.
func TestTrialEndBeforeEndsAtDoesNothing(t *testing.T) {
	e := trialEnv(t, time.Now().Add(24*time.Hour))
	if n := e.runTrialEnd(); n != 0 || e.orgPlan() != "standard" {
		t.Errorf("running trial: %d changes, plan %s", n, e.orgPlan())
	}
	if got := e.rows()[entitlement.FeatureCMDBCore].Limits[entitlement.LimitMaxCIs]; got != 100 {
		t.Errorf("max_cis of the running trial: %d", got)
	}
}
