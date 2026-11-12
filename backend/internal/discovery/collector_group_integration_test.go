package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-947, PRB-03, COL-06):
//
//	cd backend && go test -count=1 -p 1 -run 'TestCollectorGroup' ./internal/discovery/

// collectorGroupRoutes are the routes of the environment with their audit.
var collectorGroupRoutes = []struct{ method, pattern string }{
	{http.MethodGet, "/api/v1/collector-groups"},
	{http.MethodPost, "/api/v1/collector-groups"},
	{http.MethodGet, "/api/v1/collector-groups/{id}"},
	{http.MethodPatch, "/api/v1/collector-groups/{id}"},
	{http.MethodDelete, "/api/v1/collector-groups/{id}"},
	{http.MethodPost, "/api/v1/collector-groups/{id}/members"},
	{http.MethodDelete, "/api/v1/collector-groups/{id}/members/{collector_id}"},
}

// cgAPIEnv is the collector group API of organization A on PostgreSQL
// behind the route authorization and the route audit of the server.
type cgAPIEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	mux *chi.Mux
}

func newCGAPIEnv(t *testing.T) *cgAPIEnv {
	t.Helper()
	f := scopetest.Seed(t, "94")
	inner := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), nil).RegisterCollectorGroupRoutes(inner)
	mux := chi.NewRouter()
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, rt := range collectorGroupRoutes {
		mode := middleware.AuditIntent
		if rt.method == http.MethodGet {
			mode = middleware.AuditNone
		}
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern), middleware.AuditRoute(rt.method, rt.pattern, auditor, mode, "")).
			Method(rt.method, rt.pattern, inner)
	}
	return &cgAPIEnv{t: t, f: f, mux: mux}
}

func (e *cgAPIEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// collector inserts a collector of the client (empty: without client) and
// returns it with the group of its own.
func (e *cgAPIEnv) collector(clientID, name string) (id, group string) {
	e.t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	id = e.scalar(`INSERT INTO collector (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, e.f.OrgA, client, name)
	group = e.scalar(`SELECT group_id::text FROM collector_group_member WHERE collector_id = $1`, id)
	return id, group
}

// probe inserts a probe of client 1 in the group.
func (e *cgAPIEnv) probe(group, target string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
		VALUES ($1, $2, $3, 'icmp', $4) RETURNING id::text`, e.f.OrgA, e.f.Client1, group, target)
}

// do sends a request with every permission, org-wide or restricted to
// clients.
func (e *cgAPIEnv) do(clients []string, perms []identity.Permission, method, path, body string) cgReply {
	e.t.Helper()
	return e.doIn(clients, nil, perms, method, path, body)
}

// doIn is do restricted to clients and sites (nil: all).
func (e *cgAPIEnv) doIn(clients, sites []string, perms []identity.Permission, method, path, body string) cgReply {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if clients != nil {
		scope.Clients = database.ScopeIDs(clients...)
		idScope.Clients = identity.ScopeSet{IDs: clients}
	}
	if sites != nil {
		scope.Sites = database.ScopeIDs(sites...)
		idScope.Sites = identity.ScopeSet{IDs: sites}
	}
	if perms == nil {
		perms = identity.AllPermissions()
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope: idScope, Permissions: perms, Credential: "wp947"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	r := cgReply{Code: rec.Code, Body: rec.Body.String()}
	var problem struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	r.Field = problem.Field
	_ = json.Unmarshal(rec.Body.Bytes(), &r.Group)
	return r
}

func (e *cgAPIEnv) admin(method, path, body string) cgReply {
	e.t.Helper()
	return e.do(nil, nil, method, path, body)
}

// TestCollectorGroupMemberMustShareClient: a collector of another client
// is 422 collector_id (collector_group_member_client_rule), also into an
// org-wide group; a collector of the client joins.
func TestCollectorGroupMemberMustShareClient(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	r := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-c1","client_id":"`+f.Client1+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	group := r.Group.ID
	c2, _ := e.collector(f.Client2, "wp947-c2")
	orgCol, _ := e.collector("", "wp947-org")
	c1, _ := e.collector(f.Client1, "wp947-c1")
	cgWant(t, "collector of client 2", e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+c2+`"}`),
		http.StatusUnprocessableEntity, "collector_id")
	cgWant(t, "collector without client", e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+orgCol+`"}`),
		http.StatusUnprocessableEntity, "collector_id")
	orgWide := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-org"}`)
	cgWant(t, "create org-wide", orgWide, http.StatusCreated, "")
	cgWant(t, "client collector into an org-wide group", e.admin(http.MethodPost, "/api/v1/collector-groups/"+orgWide.Group.ID+"/members",
		`{"collector_id":"`+c1+`"}`), http.StatusUnprocessableEntity, "collector_id")
	r = e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+c1+`","priority":1,"weight":50}`)
	cgWant(t, "collector of the client", r, http.StatusOK, "")
	if len(r.Group.Members) != 1 || r.Group.Members[0].CollectorID != c1 || r.Group.Members[0].Weight != 50 {
		t.Fatalf("members %+v", r.Group.Members)
	}
	if got := e.scalar(`SELECT count(*)::text FROM collector_group_member WHERE collector_id = $1`, c1); got != "1" {
		t.Fatalf("collector in %s groups, want 1 (moved out of its own)", got)
	}
	// The move of the member is audited with the members of the group.
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2::uuid
		AND changes->>'route' = 'POST /api/v1/collector-groups/{id}/members' AND changes ? 'values'`, f.OrgA, group); got != "1" {
		t.Errorf("%s audit entries of the member, want 1", got)
	}
}

// TestCollectorGroupWithProbesCannotBeDeleted: 409 with the number of
// probes; without probes the group goes.
func TestCollectorGroupWithProbesCannotBeDeleted(t *testing.T) {
	e := newCGAPIEnv(t)
	_, group := e.collector(e.f.Client1, "wp947-del")
	p1 := e.probe(group, "10.47.0.1")
	e.probe(group, "10.47.0.2")
	r := e.admin(http.MethodDelete, "/api/v1/collector-groups/"+group, "")
	cgWant(t, "delete with probes", r, http.StatusConflict, "id")
	if !strings.Contains(r.Body, `"probes":2`) {
		t.Errorf("409 without the number of probes: %s", r.Body)
	}
	e.scalar(`DELETE FROM probe WHERE collector_group_id = $1 RETURNING '1'`, group)
	_ = p1
	cgWant(t, "delete without probes", e.admin(http.MethodDelete, "/api/v1/collector-groups/"+group, ""), http.StatusNoContent, "")
	if got := e.scalar(`SELECT count(*)::text FROM collector_group WHERE id = $1`, group); got != "0" {
		t.Errorf("group still there")
	}
}

// TestCollectorGroupLastMemberWithProbesStays: the last member of a group
// with probes can neither be removed nor moved away (409); with a second
// member it can, and its assignments of the group go with it.
func TestCollectorGroupLastMemberWithProbesStays(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	col, group := e.collector(f.Client1, "wp947-last")
	probe := e.probe(group, "10.47.1.1")
	e.scalar(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1) RETURNING '1'`, probe, f.OrgA, col)
	cgWant(t, "remove last member", e.admin(http.MethodDelete, "/api/v1/collector-groups/"+group+"/members/"+col, ""),
		http.StatusConflict, "collector_id")
	target := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-target","client_id":"`+f.Client1+`"}`)
	cgWant(t, "create target", target, http.StatusCreated, "")
	cgWant(t, "move last member", e.admin(http.MethodPost, "/api/v1/collector-groups/"+target.Group.ID+"/members", `{"collector_id":"`+col+`"}`),
		http.StatusConflict, "collector_id")
	if got := e.scalar(`SELECT group_id::text FROM collector_group_member WHERE collector_id = $1`, col); got != group {
		t.Fatalf("collector in %s after the refused move, want %s", got, group)
	}
	partner, _ := e.collector(f.Client1, "wp947-partner")
	cgWant(t, "partner joins", e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+partner+`"}`),
		http.StatusOK, "")
	cgWant(t, "move with a partner", e.admin(http.MethodPost, "/api/v1/collector-groups/"+target.Group.ID+"/members", `{"collector_id":"`+col+`"}`),
		http.StatusOK, "")
	if got := e.scalar(`SELECT count(*)::text FROM probe_assignment WHERE collector_id = $1`, col); got != "0" {
		t.Errorf("%s assignments left with the moved collector, want 0", got)
	}
	cgWant(t, "remove from a group of two", e.admin(http.MethodDelete, "/api/v1/collector-groups/"+target.Group.ID+"/members/"+col, ""),
		http.StatusNoContent, "")
	cgWant(t, "unknown member", e.admin(http.MethodDelete, "/api/v1/collector-groups/"+target.Group.ID+"/members/"+col, ""),
		http.StatusNotFound, "")
}

// TestCollectorGroupProbesOutsideTheSiteScopeCount: a principal of site S1
// does not see a probe of the group at site S2 (the site of its CI), but
// the probe runs in the group: removing or moving away the last member and
// deleting the group are 409, the delete with the real number of probes.
func TestCollectorGroupProbesOutsideTheSiteScopeCount(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	s1 := e.scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp947-site-1') RETURNING id::text`, f.OrgA, f.Client1)
	s2 := e.scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp947-site-2') RETURNING id::text`, f.OrgA, f.Client1)
	group := e.scalar(`INSERT INTO collector_group (organization_id, client_id, site_id, name) VALUES ($1, $2, $3, 'wp947-sited')
		RETURNING id::text`, f.OrgA, f.Client1, s1)
	col, _ := e.collector(f.Client1, "wp947-sited")
	cgWant(t, "join", e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+col+`"}`), http.StatusOK, "")
	for _, target := range []string{"10.47.3.1", "10.47.3.2"} {
		e.scalar(`INSERT INTO probe (organization_id, client_id, site_id, collector_group_id, type, target)
			VALUES ($1, $2, $3, $4, 'icmp', $5) RETURNING '1'`, f.OrgA, f.Client1, s2, group, target)
	}
	site1 := func(method, path, body string) cgReply {
		return e.doIn([]string{f.Client1}, []string{s1}, nil, method, path, body)
	}
	cgWant(t, "group visible at site 1", site1(http.MethodGet, "/api/v1/collector-groups/"+group, ""), http.StatusOK, "")
	cgWant(t, "remove the last member", site1(http.MethodDelete, "/api/v1/collector-groups/"+group+"/members/"+col, ""),
		http.StatusConflict, "collector_id")
	other := e.scalar(`INSERT INTO collector_group (organization_id, client_id, site_id, name) VALUES ($1, $2, $3, 'wp947-sited-2')
		RETURNING id::text`, f.OrgA, f.Client1, s1)
	cgWant(t, "move the last member", site1(http.MethodPost, "/api/v1/collector-groups/"+other+"/members", `{"collector_id":"`+col+`"}`),
		http.StatusConflict, "collector_id")
	r := site1(http.MethodDelete, "/api/v1/collector-groups/"+group, "")
	cgWant(t, "delete the group", r, http.StatusConflict, "id")
	if !strings.Contains(r.Body, `"probes":2`) {
		t.Errorf("409 without the real number of probes: %s", r.Body)
	}
	if got := e.scalar(`SELECT count(*)::text FROM collector_group_member WHERE group_id = $1`, group); got != "1" {
		t.Errorf("%s members left, want 1", got)
	}
}

// TestCollectorGroupClientScope: a client principal sees and writes only
// the groups of its client (and reads org-wide ones); writing needs
// collector:manage, reading monitoring:read.
func TestCollectorGroupClientScope(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	c1 := []string{f.Client1}
	_, g1 := e.collector(f.Client1, "wp947-s1")
	_, g2 := e.collector(f.Client2, "wp947-s2")
	orgWide := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-s-org"}`)
	cgWant(t, "create org-wide", orgWide, http.StatusCreated, "")
	list := e.do(c1, nil, http.MethodGet, "/api/v1/collector-groups?limit=50", "")
	var page struct {
		Data  []discovery.CollectorGroup `json:"data"`
		Total int                        `json:"total"`
	}
	if err := json.Unmarshal([]byte(list.Body), &page); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, g := range page.Data {
		seen[g.ID] = true
	}
	if !seen[g1] || seen[g2] || !seen[orgWide.Group.ID] || page.Total != len(page.Data) {
		t.Fatalf("client 1 lists %v (total %d); want its own and the org-wide group, not client 2's", seen, page.Total)
	}
	cgWant(t, "get foreign", e.do(c1, nil, http.MethodGet, "/api/v1/collector-groups/"+g2, ""), http.StatusNotFound, "")
	cgWant(t, "patch foreign", e.do(c1, nil, http.MethodPatch, "/api/v1/collector-groups/"+g2, `{"name":"x"}`), http.StatusNotFound, "")
	cgWant(t, "delete foreign", e.do(c1, nil, http.MethodDelete, "/api/v1/collector-groups/"+g2, ""), http.StatusNotFound, "")
	cgWant(t, "patch org-wide", e.do(c1, nil, http.MethodPatch, "/api/v1/collector-groups/"+orgWide.Group.ID, `{"name":"x"}`),
		http.StatusNotFound, "")
	cgWant(t, "create for client 2", e.do(c1, nil, http.MethodPost, "/api/v1/collector-groups", `{"name":"x","client_id":"`+f.Client2+`"}`),
		http.StatusUnprocessableEntity, "client_id")
	cgWant(t, "create org-wide", e.do(c1, nil, http.MethodPost, "/api/v1/collector-groups", `{"name":"x"}`), http.StatusForbidden, "")
	cgWant(t, "create own", e.do(c1, nil, http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-s-own","client_id":"`+f.Client1+`"}`),
		http.StatusCreated, "")
	cgWant(t, "patch own", e.do(c1, nil, http.MethodPatch, "/api/v1/collector-groups/"+g1, `{"mode":"failover"}`), http.StatusOK, "")
	// monitoring:read reads, collector:manage writes.
	read := []identity.Permission{identity.PermMonitoringRead}
	cgWant(t, "read with monitoring:read", e.do(nil, read, http.MethodGet, "/api/v1/collector-groups/"+g1, ""), http.StatusOK, "")
	cgWant(t, "write without collector:manage", e.do(nil, []identity.Permission{identity.PermMonitoringRead, identity.PermMonitoringWrite},
		http.MethodPatch, "/api/v1/collector-groups/"+g1, `{"mode":"distributed"}`), http.StatusForbidden, "")
	cgWant(t, "read without monitoring:read", e.do(nil, []identity.Permission{identity.PermCollectorManage},
		http.MethodGet, "/api/v1/collector-groups/"+g1, ""), http.StatusForbidden, "")
}

// TestCollectorGroupMemberStatusAndProbeCount: the members carry their
// status, last sign of life, clock offset and the number of probes
// assigned to them; the group its status.
func TestCollectorGroupMemberStatusAndProbeCount(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	a, group := e.collector(f.Client1, "wp947-a")
	b, _ := e.collector(f.Client1, "wp947-b")
	cgWant(t, "b joins", e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+b+`","priority":1}`),
		http.StatusOK, "")
	e.scalar(`UPDATE collector_group_member SET member_status = 'degraded', last_alive_at = now(), clock_offset_ms = 42
		WHERE collector_id = $1 RETURNING '1'`, a)
	e.scalar(`UPDATE collector_group SET status = 'unreachable', status_changed_at = now() WHERE id = $1 RETURNING '1'`, group)
	for i, target := range []string{"10.47.2.1", "10.47.2.2", "10.47.2.3"} {
		p := e.probe(group, target)
		collector := a
		if i == 2 {
			collector = b
		}
		e.scalar(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1) RETURNING '1'`, p, f.OrgA, collector)
	}
	check := func(g discovery.CollectorGroup) {
		t.Helper()
		if g.Status != "unreachable" || len(g.Members) != 2 {
			t.Fatalf("group %+v", g)
		}
		for _, m := range g.Members {
			switch m.CollectorID {
			case a:
				if m.MemberStatus != "degraded" || m.LastAliveAt == nil || m.ClockOffsetMS == nil || *m.ClockOffsetMS != 42 || m.AssignedProbes != 2 {
					t.Errorf("member a %+v", m)
				}
			case b:
				if m.MemberStatus != "healthy" || m.LastAliveAt != nil || m.AssignedProbes != 1 || m.Priority != 1 {
					t.Errorf("member b %+v", m)
				}
			}
		}
	}
	r := e.admin(http.MethodGet, "/api/v1/collector-groups/"+group, "")
	cgWant(t, "get", r, http.StatusOK, "")
	check(r.Group)
	list := e.admin(http.MethodGet, "/api/v1/collector-groups?client_id="+f.Client1+"&limit=50", "")
	var page struct {
		Data []discovery.CollectorGroup `json:"data"`
	}
	if err := json.Unmarshal([]byte(list.Body), &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range page.Data {
		if g.ID == group {
			found = true
			check(g)
		}
	}
	if !found {
		t.Fatalf("group missing from the list: %s", list.Body)
	}
}
