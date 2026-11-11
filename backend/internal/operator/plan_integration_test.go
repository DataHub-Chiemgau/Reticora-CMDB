package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
)

// planEnv is an operator router on PostgreSQL with organization A on a
// known plan: every feature of the plan with origin plan and the defaults.
type planEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	svc    *entitlement.Service
	store  cache.Store
	issuer *identity.SessionIssuer
}

func newPlanEnv(t *testing.T, plan entitlement.Plan) *planEnv {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "66")
	store := cache.NewMemoryStore()
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true, Cache: store})
	issuer := testIssuer(t)
	e := &planEnv{t: t, f: f, svc: svc, store: store, issuer: issuer,
		mux: testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, NewAuditor(f.App), f.App).WithEntitlements(svc))}
	bg := context.Background()
	if _, err := f.Admin.Exec(bg, `DELETE FROM entitlement WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET plan = $2, plan_changed_at = NULL WHERE id = $1`, f.OrgA, string(plan)); err != nil {
		t.Fatal(err)
	}
	ctx := orgContext(bg, f.OrgA)
	defaults := entitlement.PlanQuotaDefaults(plan)
	for _, feature := range entitlement.PlanFeatures(plan) {
		limits := map[string]int64{}
		for quota, value := range defaults {
			if owner, _ := entitlement.QuotaFeatureOf(quota); owner == feature {
				limits[quota] = value
			}
		}
		if _, err := svc.Grant(ctx, &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: feature, Plan: plan,
			Enabled: true, Limits: limits, Origin: entitlement.OriginPlan}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *planEnv) session(subject string) map[string]string {
	e.t.Helper()
	now := time.Now().UTC()
	token, err := e.issuer.Issue(&identity.SessionClaims{Subject: subject, Operator: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		e.t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + token}
}

// change sends PUT /plan as operator op.
func (e *planEnv) change(op, body string) (*httptest.ResponseRecorder, PlanChangeResponse) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/orgs/"+e.f.OrgA+"/plan", strings.NewReader(body))
	for k, v := range e.session(op) {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	var resp PlanChangeResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			e.t.Fatal(err)
		}
	}
	return w, resp
}

// rows reads the entitlement rows of organization A from the table.
func (e *planEnv) rows() map[string]entitlement.Entitlement {
	e.t.Helper()
	r, err := e.f.Admin.Query(context.Background(), `SELECT feature_key, plan, enabled, limits, origin
		FROM entitlement WHERE organization_id = $1`, e.f.OrgA)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Close()
	out := map[string]entitlement.Entitlement{}
	for r.Next() {
		var ent entitlement.Entitlement
		var plan string
		if err = r.Scan(&ent.FeatureKey, &plan, &ent.Enabled, &ent.Limits, &ent.Origin); err != nil {
			e.t.Fatal(err)
		}
		ent.Plan = entitlement.Plan(plan)
		out[ent.FeatureKey] = ent
	}
	return out
}

func (e *planEnv) orgPlan() string {
	e.t.Helper()
	var plan string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT plan FROM organization WHERE id = $1`, e.f.OrgA).Scan(&plan); err != nil {
		e.t.Fatal(err)
	}
	return plan
}

func (e *planEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// checkMatrix checks that the rows of origin plan are the matrix of plan.
func (e *planEnv) checkMatrix(plan entitlement.Plan) {
	e.t.Helper()
	rows := e.rows()
	features := entitlement.PlanFeatures(plan)
	for _, f := range features {
		if row, ok := rows[f]; !ok || !row.Enabled {
			e.t.Errorf("%s: feature %s of the plan missing or disabled: %+v", plan, f, row)
		}
	}
	for key, row := range rows {
		if row.Origin != entitlement.OriginPlan {
			continue
		}
		if row.Plan != plan || row.Enabled != slices.Contains(features, key) {
			e.t.Errorf("%s: row %s plan %s enabled %v", plan, key, row.Plan, row.Enabled)
		}
	}
}

// TestOperatorPlanUpgradeAndDowngrade covers PKG-02, PKG-01, ENT-04 and
// ENT-08 (WP-882): essential → pro → standard. After each change
// organization.plan and the rows of origin plan follow the matrix, rows
// outside the plan are disabled but kept, no CI is deleted, and one
// plan.change entry with before, after and reason is written.
func TestOperatorPlanUpgradeAndDowngrade(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	cis := e.count(`SELECT count(*) FROM ci WHERE organization_id = $1`, e.f.OrgA)
	var mark int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&mark); err != nil {
		t.Fatal(err)
	}

	w, resp := e.change("op-1", `{"plan":"pro","reason":"upgrade WP-882"}`)
	if w.Code != http.StatusOK || resp.Plan != entitlement.PlanPro || resp.PlanBefore != entitlement.PlanEssential ||
		resp.Unchanged || resp.PlanChangedAt == nil || len(resp.Entitlements) == 0 {
		t.Fatalf("upgrade: %d %s", w.Code, w.Body.String())
	}
	if e.orgPlan() != "pro" {
		t.Errorf("organization.plan %s after the upgrade", e.orgPlan())
	}
	e.checkMatrix(entitlement.PlanPro)
	total := len(e.rows())

	if w, _ = e.change("op-1", `{"plan":"standard","reason":"downgrade WP-882"}`); w.Code != http.StatusOK {
		t.Fatalf("downgrade: %d %s", w.Code, w.Body.String())
	}
	e.checkMatrix(entitlement.PlanStandard)
	rows := e.rows()
	if len(rows) != total {
		t.Errorf("downgrade deleted rows: %d, want %d", len(rows), total)
	}
	if row := rows[entitlement.FeatureWorkflows]; row.Enabled || row.Origin != entitlement.OriginPlan {
		t.Errorf("workflows after the downgrade: %+v, want disabled, kept", row)
	}
	if n := e.count(`SELECT count(*) FROM ci WHERE organization_id = $1`, e.f.OrgA); n != cis {
		t.Errorf("CIs after the plan changes: %d, want %d", n, cis)
	}
	var entries []struct {
		action  string
		details []byte
	}
	r, err := e.f.Admin.Query(context.Background(), `SELECT action, details FROM operator_audit WHERE id > $1 ORDER BY id`, mark)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x struct {
			action  string
			details []byte
		}
		if err = r.Scan(&x.action, &x.details); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, x)
	}
	r.Close()
	if len(entries) != 2 || entries[0].action != "plan.change" || entries[1].action != "plan.change" {
		t.Fatalf("operator audit: %d entries %v", len(entries), entries)
	}
	var details struct {
		Reason string `json:"reason"`
		Before struct {
			Plan         string                    `json:"plan"`
			Entitlements []entitlement.Entitlement `json:"entitlements"`
		} `json:"before"`
		After struct {
			Plan string `json:"plan"`
		} `json:"after"`
	}
	if err = json.Unmarshal(entries[1].details, &details); err != nil {
		t.Fatal(err)
	}
	if details.Reason != "downgrade WP-882" || details.Before.Plan != "pro" || details.After.Plan != "standard" || len(details.Before.Entitlements) == 0 {
		t.Errorf("audit details %s", entries[1].details)
	}
	if result, err := NewAuditor(e.f.App).Verify(context.Background()); err != nil || !result.Valid {
		t.Errorf("verify: %+v, %v", result, err)
	}

	// Validation: unknown plan, missing or too long reason, unknown org.
	for body, field := range map[string]string{
		`{"plan":"gold","reason":"x"}`:                               "plan",
		`{"plan":"pro","reason":"  "}`:                               "reason",
		`{"plan":"pro","reason":"` + strings.Repeat("x", 501) + `"}`: "reason",
		`{"plan":"Pro","reason":"case matters"}`:                     "plan",
		`{"plan":"pro"}`:                                             "reason",
	} {
		if w, _ = e.change("op-1", body); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 on %s", body, w.Code, w.Body.String(), field)
		}
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/orgs/00000000-0000-4000-8000-0000000000ff/plan",
		strings.NewReader(`{"plan":"pro","reason":"x"}`))
	req.Header.Set(BreakGlassHeader, testBreakGlass)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown organization: %d", rec.Code)
	}
	if e.orgPlan() != "standard" {
		t.Errorf("a refused change touched the plan: %s", e.orgPlan())
	}
}

// TestPlanChangeKeepsAddonsAndManualRows covers PKG-02: rows of origin
// addon and manual are not written, even when their key is in the new plan.
func TestPlanChangeKeepsAddonsAndManualRows(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	ctx := orgContext(context.Background(), e.f.OrgA)
	for _, ent := range []entitlement.Entitlement{
		{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureIGA, Plan: entitlement.PlanEssential, Enabled: true,
			Limits: map[string]int64{}, Origin: entitlement.OriginAddon},
		{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureMonitoringNetwork, Plan: entitlement.PlanEssential, Enabled: true,
			Limits: map[string]int64{}, Origin: entitlement.OriginManual},
	} {
		if _, err := e.svc.Grant(ctx, &ent); err != nil {
			t.Fatal(err)
		}
	}
	before := e.rows()
	if w, _ := e.change("op-1", `{"plan":"pro","reason":"keeps add-ons"}`); w.Code != http.StatusOK {
		t.Fatalf("upgrade: %d %s", w.Code, w.Body.String())
	}
	after := e.rows()
	for _, key := range []string{entitlement.FeatureIGA, entitlement.FeatureMonitoringNetwork} {
		if b, a := before[key], after[key]; a.Plan != b.Plan || a.Origin != b.Origin || !a.Enabled {
			t.Errorf("%s changed: before %+v, after %+v", key, b, a)
		}
	}
	if w, _ := e.change("op-1", `{"plan":"essential","reason":"downgrade keeps the manual grant"}`); w.Code != http.StatusOK {
		t.Fatalf("downgrade: %d %s", w.Code, w.Body.String())
	}
	if row := e.rows()[entitlement.FeatureMonitoringNetwork]; !row.Enabled || row.Origin != entitlement.OriginManual {
		t.Errorf("manual monitoring_network after the downgrade: %+v", row)
	}
}

// TestPlanChangeKeepsLimitOverrides covers the limit rule: max_cis 3,000
// (override) stays, max_users (default) follows the new plan.
func TestPlanChangeKeepsLimitOverrides(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanStandard)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE entitlement SET limits = limits || '{"max_cis": 3000}'
		WHERE organization_id = $1 AND feature_key = 'cmdb_core'`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if w, _ := e.change("op-1", `{"plan":"pro","reason":"limits"}`); w.Code != http.StatusOK {
		t.Fatalf("upgrade: %d %s", w.Code, w.Body.String())
	}
	rows := e.rows()
	if core := rows[entitlement.FeatureCMDBCore].Limits; core[entitlement.LimitMaxCIs] != 3000 || core[entitlement.LimitMaxUsers] != 100 {
		t.Errorf("cmdb_core limits %v, want max_cis 3000 and max_users 100", core)
	}
	if docs := rows[entitlement.FeatureDocuments].Limits; docs[entitlement.LimitMaxStorageMB] != 50000 {
		t.Errorf("documents limits %v, want 50000 MiB", docs)
	}
}

// TestPlanChangeRollsBackWithoutAuditEntry covers AK4: when the audit
// entry fails the answer is 500 and plan, rows and outbox are unchanged.
func TestPlanChangeRollsBackWithoutAuditEntry(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	bg := context.Background()
	const failing = "op-wp882-audit-fails"
	drop := func() {
		if _, err := e.f.Admin.Exec(bg, `ALTER TABLE operator_audit DROP CONSTRAINT IF EXISTS wp882_audit_fails`); err != nil {
			t.Fatal(err)
		}
	}
	drop()
	if _, err := e.f.Admin.Exec(bg, `ALTER TABLE operator_audit ADD CONSTRAINT wp882_audit_fails
		CHECK (operator_id <> '`+failing+`') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)
	before := e.rows()
	events0 := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA)
	if w, _ := e.change(failing, `{"plan":"enterprise","reason":"must roll back"}`); w.Code != http.StatusInternalServerError {
		t.Errorf("change with a failing audit: %d %s, want 500", w.Code, w.Body.String())
	}
	if e.orgPlan() != "essential" {
		t.Errorf("organization.plan %s after the rolled back change", e.orgPlan())
	}
	after := e.rows()
	if len(after) != len(before) {
		t.Errorf("rows after the rolled back change: %d, want %d", len(after), len(before))
	}
	for key, b := range before {
		if a := after[key]; a.Plan != b.Plan || a.Enabled != b.Enabled {
			t.Errorf("%s changed: %+v → %+v", key, b, a)
		}
	}
	if n := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA); n != events0 {
		t.Errorf("entitlement.changed events after the rolled back change: %d, want %d", n, events0)
	}
	if _, ok, _ := e.svc.Check(orgContext(bg, e.f.OrgA), e.f.OrgA, entitlement.FeatureMonitoringAccess); ok {
		t.Error("the rolled back change is visible through the cache")
	}
}

// TestPlanChangeSamePlanIsNoOp covers AK1: the current plan again answers
// 200 unchanged, writes nothing, emits no event and records "unchanged".
func TestPlanChangeSamePlanIsNoOp(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanStandard)
	events0 := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA)
	w, resp := e.change("op-1", `{"plan":"standard","reason":"no-op"}`)
	if w.Code != http.StatusOK || !resp.Unchanged || len(resp.Entitlements) != 0 || len(resp.Changed) != 0 || resp.Plan != entitlement.PlanStandard {
		t.Fatalf("same plan: %d %s", w.Code, w.Body.String())
	}
	if n := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.changed'`, e.f.OrgA); n != events0 {
		t.Errorf("same plan emitted %d events", n-events0)
	}
	var unchanged bool
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT (details->>'unchanged')::boolean FROM operator_audit
		WHERE action = 'plan.change' AND target_org = $1 ORDER BY id DESC LIMIT 1`, e.f.OrgA).Scan(&unchanged); err != nil || !unchanged {
		t.Errorf("audit entry of the no-op: unchanged %v, %v", unchanged, err)
	}
	var changedAt *time.Time
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT plan_changed_at FROM organization WHERE id = $1`, e.f.OrgA).Scan(&changedAt); err != nil || changedAt != nil {
		t.Errorf("plan_changed_at after the no-op: %v, %v", changedAt, err)
	}
}

// TestPlanChangeEmitsEntitlementChanged covers AK5: one entitlement.changed
// in the outbox with organization, plans and changed keys; the
// subscription notifies the org_admins who may read entitlements.
func TestPlanChangeEmitsEntitlementChanged(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	bg := context.Background()
	var seq int64
	if err := e.f.Admin.QueryRow(bg, `SELECT COALESCE(max(sequence), 0) FROM event_outbox WHERE organization_id = $1`, e.f.OrgA).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if w, _ := e.change("op-1", `{"plan":"standard","reason":"event"}`); w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	r, err := e.f.Admin.Query(bg, `SELECT type, COALESCE(entity_id, ''), data
		FROM event_outbox WHERE organization_id = $1 AND sequence > $2 ORDER BY sequence`, e.f.OrgA, seq)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		typ, entity string
		data        []byte
	}
	var rows []row
	for r.Next() {
		var x row
		if err = r.Scan(&x.typ, &x.entity, &x.data); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()
	if len(rows) != 1 || rows[0].typ != events.EntitlementChanged || rows[0].entity != e.f.OrgA {
		t.Fatalf("outbox after the change: %+v, want one entitlement.changed of the organization", rows)
	}
	var data events.EntitlementChangedData
	if err = json.Unmarshal(rows[0].data, &data); err != nil {
		t.Fatal(err)
	}
	if data.ID != e.f.OrgA || data.Plan != "standard" || data.PlanBefore != "essential" ||
		!slices.Contains(data.Changed, entitlement.FeatureMonitoring) || !slices.Contains(data.Changed, entitlement.LimitMaxCIs) {
		t.Errorf("event data %s", rows[0].data)
	}
	sub, ok := notify.Subscriptions[events.EntitlementChanged]
	if !ok {
		t.Fatal("no subscription for entitlement.changed")
	}
	audience := sub.Audience(map[string]any{"id": e.f.OrgA})
	if audience == nil || audience.Permission != identity.PermEntitlementRead || !slices.Equal(audience.Roles, []string{"org_admin"}) {
		t.Errorf("audience %+v, want org_admin with entitlement:read", audience)
	}
}

// TestPlanChangeIsVisibleWithoutCacheDelay covers AK5: a Check right after
// the commit sees the new state, also on a second instance sharing the
// cache (Redis in production).
func TestPlanChangeIsVisibleWithoutCacheDelay(t *testing.T) {
	e := newPlanEnv(t, entitlement.PlanEssential)
	ctx := orgContext(context.Background(), e.f.OrgA)
	other := entitlement.NewService(entitlement.NewPGRepository(e.f.App), entitlement.Options{Enforce: true, Cache: e.store,
		CacheTTL: time.Hour})
	for _, svc := range []*entitlement.Service{e.svc, other} {
		if _, ok, err := svc.Check(ctx, e.f.OrgA, entitlement.FeatureMonitoring); err != nil || ok {
			t.Fatalf("essential entitled to monitoring: %v, %v", ok, err)
		}
	}
	if w, _ := e.change("op-1", `{"plan":"standard","reason":"cache"}`); w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	for i, svc := range []*entitlement.Service{e.svc, other} {
		if _, ok, err := svc.Check(ctx, e.f.OrgA, entitlement.FeatureMonitoring); err != nil || !ok {
			t.Errorf("instance %d: monitoring not entitled right after the change: %v, %v", i, ok, err)
		}
		if plan, _, err := svc.OrganizationPlan(ctx, e.f.OrgA); err != nil || plan != entitlement.PlanStandard {
			t.Errorf("instance %d: plan %s, %v", i, plan, err)
		}
	}
}
