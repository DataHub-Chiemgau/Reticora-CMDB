package probe_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// Retest (WP-946, PRB-01):
//
//	cd backend && go test -count=1 -p 1 -run 'TestProfileClientScope|TestProfileOrgLevelNeedsOrgWidePrincipal|TestProfileChange|TestGroupMemberAddRespectsProbeBudget|TestGroupUpdateRespectsProbeBudget|TestGroupMemberCountInCallerScope' ./internal/probe/

// profileRoutes are the profile and group routes with their audit.
var profileRoutes = []struct{ method, pattern string }{
	{http.MethodGet, "/api/v1/monitoring/profiles"},
	{http.MethodPost, "/api/v1/monitoring/profiles"},
	{http.MethodGet, "/api/v1/monitoring/profiles/{id}"},
	{http.MethodPatch, "/api/v1/monitoring/profiles/{id}"},
	{http.MethodDelete, "/api/v1/monitoring/profiles/{id}"},
	{http.MethodGet, "/api/v1/monitoring/groups"},
	{http.MethodPost, "/api/v1/monitoring/groups"},
	{http.MethodGet, "/api/v1/monitoring/groups/{id}"},
	{http.MethodPatch, "/api/v1/monitoring/groups/{id}"},
	{http.MethodDelete, "/api/v1/monitoring/groups/{id}"},
	{http.MethodGet, "/api/v1/monitoring/groups/{id}/members"},
	{http.MethodPost, "/api/v1/monitoring/groups/{id}/members"},
	{http.MethodDelete, "/api/v1/monitoring/groups/{id}/members/{ci_id}"},
	{http.MethodGet, "/api/v1/monitoring/probes/{id}/effective"},
}

// profileEnv946 is the profile and group API of organization A on
// PostgreSQL behind the route authorization and audit, with an org-wide
// collector group (cgroup) of one active collector and the CIs ci1 (client
// 1) and ci2 (client 2).
type profileEnv946 struct {
	*probeEnv
	cgroup, collector, ci1, ci2 string
}

// newProfileEnv946 builds the environment; limits nil runs without budget.
func newProfileEnv946(t *testing.T, limits map[string]int64) *profileEnv946 {
	t.Helper()
	f := scopetest.Seed(t, "93")
	repo := probe.NewPGRepository(f.App)
	h := probe.NewHandler(repo)
	if limits != nil {
		svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
		if _, err := svc.Grant(f.OrgCtx(f.OrgA), &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureMonitoring,
			Plan: entitlement.PlanStandard, Enabled: true, Limits: limits}); err != nil {
			t.Fatal(err)
		}
		h.WithBudget(svc)
	}
	inner := chi.NewRouter()
	h.RegisterRoutes(inner)
	mux := chi.NewRouter()
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, rt := range profileRoutes {
		mode := middleware.AuditIntent
		if rt.method == http.MethodGet {
			mode = middleware.AuditNone
		}
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern), middleware.AuditRoute(rt.method, rt.pattern, auditor, mode, "")).
			Method(rt.method, rt.pattern, inner)
	}
	e := &profileEnv946{probeEnv: &probeEnv{t: t, f: f, mux: mux}}
	e.cgroup = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp946') RETURNING id::text`, f.OrgA)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp946', 'active') RETURNING id::text`, f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, e.cgroup, e.collector)
	e.ci1 = f.CI(t, f.OrgA, f.Client1, "wp946-ci1")
	e.ci2 = f.CI(t, f.OrgA, f.Client2, "wp946-ci2")
	return e
}

func (e *profileEnv946) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// probe inserts an icmp probe of the CI (interval from params) and assigns
// it to the collector; it returns its id.
func (e *profileEnv946) probe(ciID, target, params string) string {
	e.t.Helper()
	id := e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target, params)
		VALUES ($1, $2, $3, 'icmp', $4, $5::jsonb) RETURNING id::text`, e.f.OrgA, ciID, e.cgroup, target, params)
	e.exec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1)`, id, e.f.OrgA, e.collector)
	return id
}

// version reads config_version of the collector.
func (e *profileEnv946) version() int64 {
	e.t.Helper()
	var v int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT config_version FROM collector_group_member WHERE collector_id = $1`,
		e.collector).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// count counts rows of the organization in table.
func (e *profileEnv946) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE organization_id = $1`, e.f.OrgA).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// json decodes the body of a reply.
func bodyOf(t *testing.T, r reply) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
	return out
}

// mustDo sends a request and fails unless it answers code.
func (e *profileEnv946) mustDo(as principal, code int, method, path, body string) map[string]any {
	e.t.Helper()
	r := e.do(as, method, path, body)
	if r.Code != code {
		e.t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, r.Code, r.Body, code)
	}
	if r.Code == http.StatusNoContent {
		return nil
	}
	return bodyOf(e.t, r)
}

// wantLimit fails unless the reply is 403 entitlement-limit.
func wantLimit(t *testing.T, what string, r reply) {
	t.Helper()
	if r.Code != http.StatusForbidden || !strings.Contains(r.Body, api.ProblemEntitlementLimit) {
		t.Errorf("%s: %d %s, want 403 entitlement-limit", what, r.Code, r.Body)
	}
}

func TestProfileClientScope(t *testing.T) {
	e := newProfileEnv946(t, nil)
	client1 := principal{clients: []string{e.f.Client1}}
	other := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"client","level_id":%q,"probe_type":"icmp","params":{"interval_s":60}}`, e.f.Client2))
	own := e.mustDo(client1, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"client","level_id":%q,"probe_type":"icmp","params":{"interval_s":60}}`, e.f.Client1))
	e.mustDo(client1, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"ci","level_id":%q,"probe_type":"icmp","params":{"interval_s":90}}`, e.ci1))
	if r := e.do(client1, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"client","level_id":%q,"probe_type":"tcp","params":{}}`, e.f.Client2)); r.Code != http.StatusNotFound {
		t.Errorf("profile of another client: %d %s, want 404", r.Code, r.Body)
	}
	if r := e.do(client1, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"ci","level_id":%q,"probe_type":"icmp","params":{}}`, e.ci2)); r.Code != http.StatusNotFound {
		t.Errorf("profile of a CI of another client: %d %s, want 404", r.Code, r.Body)
	}
	if r := e.do(client1, http.MethodGet, "/api/v1/monitoring/profiles/"+other["id"].(string), ""); r.Code != http.StatusNotFound {
		t.Errorf("read of another client's profile: %d, want 404", r.Code)
	}
	if r := e.do(client1, http.MethodPatch, "/api/v1/monitoring/profiles/"+other["id"].(string), `{"params":{}}`); r.Code != http.StatusNotFound {
		t.Errorf("change of another client's profile: %d, want 404", r.Code)
	}
	if list := e.mustDo(client1, http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles", ""); list["total"] != float64(2) {
		t.Errorf("client 1 lists %v profiles, want its 2", list["total"])
	}
	if list := e.mustDo(orgAdmin, http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles", ""); list["total"] != float64(3) {
		t.Errorf("org admin lists %v profiles, want 3", list["total"])
	}
	// Groups: client 1 creates its own and sees the org-wide one, not those
	// of client 2.
	g2 := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups",
		fmt.Sprintf(`{"name":"c2","client_id":%q}`, e.f.Client2))
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"all"}`)
	e.mustDo(client1, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups",
		fmt.Sprintf(`{"name":"c1","client_id":%q}`, e.f.Client1))
	if r := e.do(client1, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"mine-org"}`); r.Code != http.StatusForbidden {
		t.Errorf("org-wide group of a scoped principal: %d %s, want 403", r.Code, r.Body)
	}
	if r := e.do(client1, http.MethodGet, "/api/v1/monitoring/groups/"+g2["id"].(string), ""); r.Code != http.StatusNotFound {
		t.Errorf("group of another client: %d, want 404", r.Code)
	}
	if list := e.mustDo(client1, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups", ""); list["total"] != float64(2) {
		t.Errorf("client 1 lists %v groups, want its own and the org-wide one", list["total"])
	}
	_ = own
}

func TestProfileOrgLevelNeedsOrgWidePrincipal(t *testing.T) {
	e := newProfileEnv946(t, nil)
	client1 := principal{clients: []string{e.f.Client1}}
	if r := e.do(client1, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{"interval_s":60}}`); r.Code != http.StatusForbidden || r.Field != "level" {
		t.Errorf("org profile of a client-scoped principal: %d %s, want 403 level", r.Code, r.Body)
	}
	if r := e.do(principal{sites: []string{}}, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{}}`); r.Code != http.StatusForbidden {
		t.Errorf("org profile of a site-scoped principal: %d %s, want 403", r.Code, r.Body)
	}
	created := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{"interval_s":60}}`)
	id := created["id"].(string)
	if r := e.do(client1, http.MethodPatch, "/api/v1/monitoring/profiles/"+id, `{"params":{"interval_s":120}}`); r.Code != http.StatusForbidden {
		t.Errorf("change of the org profile by a scoped principal: %d %s, want 403", r.Code, r.Body)
	}
	if r := e.do(client1, http.MethodDelete, "/api/v1/monitoring/profiles/"+id, ""); r.Code != http.StatusForbidden {
		t.Errorf("delete of the org profile by a scoped principal: %d %s, want 403", r.Code, r.Body)
	}
	// The scoped principal reads it (org-wide rows are read by everyone).
	e.mustDo(client1, http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles/"+id, "")
	// The route audit wrote the create with the snapshot of the row.
	var entries int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2::uuid
		AND changes->>'route' = 'POST /api/v1/monitoring/profiles' AND changes ? 'values'`,
		e.f.OrgA, id).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries == 0 {
		t.Error("no audit entry for the profile create")
	}
}

func TestProfileChangeBumpsCollectorConfigVersion(t *testing.T) {
	e := newProfileEnv946(t, nil)
	e.probe(e.ci1, "10.96.0.1", `{}`)
	v0 := e.version()
	created := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{"interval_s":60}}`)
	if v := e.version(); v != v0+1 {
		t.Fatalf("org profile: config_version %d, want %d", v, v0+1)
	}
	// A profile of client 2 and one of another type touch no probe of the
	// collector.
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"client","level_id":%q,"probe_type":"icmp","params":{}}`, e.f.Client2))
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"tcp","params":{}}`)
	if v := e.version(); v != v0+1 {
		t.Errorf("profiles outside the probe's scope raised config_version to %d, want %d", v, v0+1)
	}
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"ci","level_id":%q,"probe_type":"icmp","params":{"interval_s":120}}`, e.ci1))
	e.mustDo(orgAdmin, http.StatusOK, http.MethodPatch, "/api/v1/monitoring/profiles/"+created["id"].(string), `{"params":{"interval_s":90}}`)
	g := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"g"}`)
	gid := g["id"].(string)
	e.mustDo(orgAdmin, http.StatusOK, http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members", fmt.Sprintf(`{"ci_ids":[%q]}`, e.ci1))
	e.mustDo(orgAdmin, http.StatusOK, http.MethodPatch, "/api/v1/monitoring/groups/"+gid, `{"priority":5}`)
	e.mustDo(orgAdmin, http.StatusNoContent, http.MethodDelete, "/api/v1/monitoring/groups/"+gid+"/members/"+e.ci1, "")
	e.mustDo(orgAdmin, http.StatusNoContent, http.MethodDelete, "/api/v1/monitoring/profiles/"+created["id"].(string), "")
	if v := e.version(); v != v0+7 {
		t.Errorf("after ci profile, patch, member add, group change, member remove and delete: config_version %d, want %d", v, v0+7)
	}
}

func TestProfileChangeRespectsProbeBudget(t *testing.T) {
	e := newProfileEnv946(t, map[string]int64{entitlement.LimitMaxProbesPerMinute: 4})
	e.probe(e.ci1, "10.96.1.1", `{}`)
	e.probe(e.ci2, "10.96.1.2", `{}`)
	wantLimit(t, "org profile halving the interval", e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"org","probe_type":"icmp","params":{"interval_s":15}}`))
	if n := e.count("monitoring_profile"); n != 0 {
		t.Errorf("%d profiles stored after the refusal, want 0", n)
	}
	created := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{"interval_s":60}}`)
	id := created["id"].(string)
	wantLimit(t, "patch to a shorter interval", e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/profiles/"+id, `{"params":{"interval_s":15}}`))
	got := e.mustDo(orgAdmin, http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles/"+id, "")
	if p, _ := got["params"].(map[string]any); p["interval_s"] != float64(60) {
		t.Errorf("refused patch changed the profile: %v", got)
	}
	// A longer interval passes; removing the profile returns to 30 s, at
	// the limit of 4/min exactly.
	e.mustDo(orgAdmin, http.StatusOK, http.MethodPatch, "/api/v1/monitoring/profiles/"+id, `{"params":{"interval_s":120}}`)
	e.mustDo(orgAdmin, http.StatusNoContent, http.MethodDelete, "/api/v1/monitoring/profiles/"+id, "")
}

func TestGroupMemberAddRespectsProbeBudget(t *testing.T) {
	e := newProfileEnv946(t, map[string]int64{entitlement.LimitMaxProbesPerMinute: 4})
	e.probe(e.ci1, "10.96.2.1", `{}`)
	e.probe(e.ci2, "10.96.2.2", `{}`)
	g := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"fast"}`)
	gid := g["id"].(string)
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"group","level_id":%q,"probe_type":"icmp","params":{"interval_s":15}}`, gid))
	wantLimit(t, "member over the budget", e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members",
		fmt.Sprintf(`{"ci_ids":[%q]}`, e.ci1)))
	if n := e.count("monitoring_group_member"); n != 0 {
		t.Errorf("%d members after the refusal, want 0", n)
	}
}

func TestGroupUpdateRespectsProbeBudget(t *testing.T) {
	e := newProfileEnv946(t, map[string]int64{entitlement.LimitMaxProbesPerMinute: 2})
	e.probe(e.ci1, "10.96.3.1", `{}`)
	slow := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"slow","priority":10}`)
	fast := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"fast","priority":0}`)
	for _, g := range []map[string]any{slow, fast} {
		e.mustDo(orgAdmin, http.StatusOK, http.MethodPost, "/api/v1/monitoring/groups/"+g["id"].(string)+"/members", fmt.Sprintf(`{"ci_ids":[%q]}`, e.ci1))
	}
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"group","level_id":%q,"probe_type":"icmp","params":{"interval_s":60}}`, slow["id"]))
	// The fast group loses against the slow one: its profile raises nothing.
	e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		fmt.Sprintf(`{"level":"group","level_id":%q,"probe_type":"icmp","params":{"interval_s":15}}`, fast["id"]))
	wantLimit(t, "raising the fast group's priority", e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/groups/"+fast["id"].(string),
		`{"priority":20,"name":"fast2"}`))
	got := e.mustDo(orgAdmin, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+fast["id"].(string), "")
	if got["priority"] != float64(0) || got["name"] != "fast" {
		t.Errorf("refused change took effect: %v", got)
	}
	// Renaming the slow group keeps its priority: passes.
	e.mustDo(orgAdmin, http.StatusOK, http.MethodPatch, "/api/v1/monitoring/groups/"+slow["id"].(string), `{"name":"slow2"}`)
}

func TestGroupMemberCountInCallerScope(t *testing.T) {
	e := newProfileEnv946(t, nil)
	g := e.mustDo(orgAdmin, http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"all"}`)
	gid := g["id"].(string)
	e.mustDo(orgAdmin, http.StatusOK, http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members", fmt.Sprintf(`{"ci_ids":[%q,%q]}`, e.ci1, e.ci2))
	if got := e.mustDo(orgAdmin, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+gid, ""); got["member_count"] != float64(2) {
		t.Errorf("org admin member_count %v, want 2", got["member_count"])
	}
	client1 := principal{clients: []string{e.f.Client1}}
	if got := e.mustDo(client1, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+gid, ""); got["member_count"] != float64(1) {
		t.Errorf("client 1 member_count %v, want 1 (F-414)", got["member_count"])
	}
	list := e.mustDo(client1, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups", "")
	if data, _ := list["data"].([]any); len(data) != 1 || data[0].(map[string]any)["member_count"] != float64(1) {
		t.Errorf("client 1 list %v, want the group with 1 member", list)
	}
	if members := e.mustDo(client1, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+gid+"/members", ""); members["total"] != float64(1) {
		t.Errorf("client 1 members %v, want 1", members["total"])
	}
	// Reading the org-wide group does not let client 1 change its members,
	// not even for its own CI (AK5).
	e.mustDo(client1, http.StatusForbidden, http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members", fmt.Sprintf(`{"ci_ids":[%q]}`, e.ci1))
	e.mustDo(client1, http.StatusForbidden, http.MethodDelete, "/api/v1/monitoring/groups/"+gid+"/members/"+e.ci1, "")
	if got := e.mustDo(orgAdmin, http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+gid, ""); got["member_count"] != float64(2) {
		t.Errorf("member_count after refused writes %v, want 2", got["member_count"])
	}
}
