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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
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

// trapCredential inserts a credential of the kind and client (empty: org
// wide) in the organization.
func (e *cgAPIEnv) trapCredential(org, clientID, kind, name string) string {
	e.t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	return e.scalar(`INSERT INTO credential (organization_id, client_id, name, kind, ciphertext) VALUES ($1, $2, $3, $4, '\x00')
		RETURNING id::text`, org, client, name, kind)
}

// TestCollectorGroupTrapConfigPatch covers WP-977 AK1 and AK2: a new group
// has the trap receiver off with port 162; PATCH trap_config stores
// enabled, port and the credentials (ids only, no secret) and is audited
// over the route; an invalid port is 422 trap_config.port.
func TestCollectorGroupTrapConfigPatch(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	r := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp977-traps","client_id":"`+f.Client1+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	group := r.Group.ID
	if tc := r.Group.TrapConfig; tc.Enabled || tc.Port != 162 || len(tc.CredentialIDs) != 0 {
		t.Fatalf("trap_config of a new group %+v, want off with port 162", tc)
	}
	v2c := e.trapCredential(f.OrgA, f.Client1, "snmp_v2c", "wp977-v2c")
	v3 := e.trapCredential(f.OrgA, "", "snmp_v3", "wp977-v3")
	r = e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group,
		`{"trap_config":{"enabled":true,"port":1162,"credential_ids":["`+v2c+`","`+v3+`"]}}`)
	cgWant(t, "patch trap_config", r, http.StatusOK, "")
	tc := r.Group.TrapConfig
	if !tc.Enabled || tc.Port != 1162 || len(tc.CredentialIDs) != 2 {
		t.Fatalf("trap_config %+v", tc)
	}
	if strings.Contains(r.Body, "community") || strings.Contains(r.Body, "ciphertext") {
		t.Fatalf("response carries a secret: %s", r.Body)
	}
	got := e.admin(http.MethodGet, "/api/v1/collector-groups/"+group, "")
	cgWant(t, "get", got, http.StatusOK, "")
	if g := got.Group.TrapConfig; !g.Enabled || g.Port != 1162 || len(g.CredentialIDs) != 2 {
		t.Fatalf("stored trap_config %+v", g)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2::uuid
		AND changes->>'route' = 'PATCH /api/v1/collector-groups/{id}'`, f.OrgA, group); n != "1" {
		t.Errorf("%s audit entries of the patch, want 1", n)
	}
	for _, body := range []string{`{"trap_config":{"enabled":true,"port":0}}`, `{"trap_config":{"enabled":true,"port":70000}}`} {
		cgWant(t, body, e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group, body), http.StatusUnprocessableEntity, "trap_config.port")
	}
	cgWant(t, "unknown field", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group, `{"trap_config":{"community":"x"}}`),
		http.StatusUnprocessableEntity, "trap_config")
	// Off again: no credentials needed.
	r = e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group, `{"trap_config":{"enabled":false}}`)
	cgWant(t, "off", r, http.StatusOK, "")
	if tc := r.Group.TrapConfig; tc.Enabled || tc.Port != 162 || len(tc.CredentialIDs) != 0 {
		t.Fatalf("trap_config after off %+v", tc)
	}
}

// TestCollectorGroupTrapConfigRejectsForeignCredential covers WP-977 AK2: a
// credential of another organization, of another client or of another kind
// (and an unknown one) is 422 trap_config.credential_ids; nothing changes.
func TestCollectorGroupTrapConfigRejectsForeignCredential(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	r := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp977-foreign","client_id":"`+f.Client1+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	group := r.Group.ID
	for name, id := range map[string]string{
		"other organization": e.trapCredential(f.OrgB, "", "snmp_v2c", "wp977-org-b"),
		"other client":       e.trapCredential(f.OrgA, f.Client2, "snmp_v2c", "wp977-client-2"),
		"other kind":         e.trapCredential(f.OrgA, f.Client1, "ssh_password", "wp977-ssh"),
		"unknown":            f.ID(),
	} {
		cgWant(t, name, e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group,
			`{"trap_config":{"enabled":true,"credential_ids":["`+id+`"]}}`), http.StatusUnprocessableEntity, "trap_config.credential_ids")
	}
	cgWant(t, "not a uuid", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group,
		`{"trap_config":{"enabled":true,"credential_ids":["x"]}}`), http.StatusUnprocessableEntity, "trap_config.credential_ids")
	if got := e.scalar(`SELECT trap_config->>'enabled' FROM collector_group WHERE id = $1`, group); got != "false" {
		t.Fatalf("trap_config changed by a refused patch: enabled %s", got)
	}
}

// TestCollectorGroupTrapConfigBumpsConfigVersion covers WP-977 AK2: a
// changed trap configuration raises the config_version of every member, an
// unchanged one or another field does not.
func TestCollectorGroupTrapConfigBumpsConfigVersion(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	r := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp977-bump","client_id":"`+f.Client1+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	group := r.Group.ID
	a, _ := e.collector(f.Client1, "wp977-a")
	b, _ := e.collector(f.Client1, "wp977-b")
	for _, c := range []string{a, b} {
		cgWant(t, "member", e.admin(http.MethodPost, "/api/v1/collector-groups/"+group+"/members", `{"collector_id":"`+c+`"}`),
			http.StatusOK, "")
	}
	versions := func() string {
		return e.scalar(`SELECT string_agg(config_version::text, ',' ORDER BY collector_id) FROM collector_group_member WHERE group_id = $1`, group)
	}
	before := versions()
	v2c := e.trapCredential(f.OrgA, "", "snmp_v2c", "wp977-bump-v2c")
	body := `{"trap_config":{"enabled":true,"port":1162,"credential_ids":["` + v2c + `"]}}`
	cgWant(t, "patch", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group, body), http.StatusOK, "")
	after := versions()
	if before == after {
		t.Fatalf("config versions %s unchanged after a changed trap_config", after)
	}
	for _, x := range strings.Split(after, ",") {
		if x == "0" {
			t.Fatalf("config versions %s: every member must be raised", after)
		}
	}
	cgWant(t, "same again", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group, body), http.StatusOK, "")
	cgWant(t, "rename", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+group, `{"name":"wp977-bump-2"}`), http.StatusOK, "")
	if again := versions(); again != after {
		t.Fatalf("config versions %s after an unchanged trap_config and a rename, want %s", again, after)
	}
}

// TestCollectorGroupMemberMoveCarriesTrapConfig covers WP-977 AK2/AK4 for
// a move: the configuration follows the group. A collector moved from a
// group with the receiver on (port 1162) into one with it off gets a new
// version, and its next set (also one fetched without if_none_match, from
// the distribution's cache) carries the new group's trap_config.
func TestCollectorGroupMemberMoveCarriesTrapConfig(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	group := func(name string) string {
		r := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"`+name+`","client_id":"`+f.Client1+`"}`)
		cgWant(t, "create "+name, r, http.StatusCreated, "")
		return r.Group.ID
	}
	on, off := group("wp977-move-on"), group("wp977-move-off")
	v2c := e.trapCredential(f.OrgA, "", "snmp_v2c", "wp977-move-v2c")
	cgWant(t, "on", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+on,
		`{"trap_config":{"enabled":true,"port":1162,"credential_ids":["`+v2c+`"]}}`), http.StatusOK, "")
	cgWant(t, "off", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+off, `{"trap_config":{"enabled":false}}`), http.StatusOK, "")
	c, _ := e.collector(f.Client1, "wp977-move")
	cgWant(t, "join", e.admin(http.MethodPost, "/api/v1/collector-groups/"+on+"/members", `{"collector_id":"`+c+`"}`), http.StatusOK, "")
	dist := probe.NewDistributor(probe.NewPGRepository(f.App), discovery.NewHandler(discovery.NewPGRepository(f.App), nil).CheckCollectorChannel)
	set := func() *probewire.AssignmentSet {
		t.Helper()
		got, ok := dist.Answer(context.Background(), f.OrgA, c, nil).(probewire.AssignmentSet)
		if !ok {
			t.Fatalf("answer %#v, want a set", got)
		}
		return &got
	}
	before := set()
	if tc := before.TrapConfig; tc == nil || !tc.Enabled || tc.Port != 1162 {
		t.Fatalf("set in the group with the receiver on: trap_config %+v", tc)
	}
	cgWant(t, "move", e.admin(http.MethodPost, "/api/v1/collector-groups/"+off+"/members", `{"collector_id":"`+c+`"}`), http.StatusOK, "")
	after := set()
	if after.Version <= before.Version {
		t.Fatalf("version %d after the move, want above %d", after.Version, before.Version)
	}
	if tc := after.TrapConfig; tc == nil || tc.Enabled || len(tc.CredentialIDs) != 0 {
		t.Fatalf("set after the move: trap_config %+v, want the receiver off", tc)
	}
	req, _ := json.Marshal(probewire.AssignmentRequest{IfNoneMatch: before.Version})
	if _, nm := dist.Answer(context.Background(), f.OrgA, c, req).(probewire.NotModified); nm {
		t.Fatalf("if_none_match of the former version answered not_modified after the move")
	}
}

// TestCollectorGroupTrapConfigDefaultKeepsEnvReceiver covers WP-977 AK5
// on the server: a group whose receiver was never configured (the column
// default, every group of an upgraded installation) sends no trap_config,
// so the collector keeps RETICORA_SNMP_TRAP_LISTEN; a rename does not
// configure it, the first PATCH of trap_config does and raises the version.
func TestCollectorGroupTrapConfigDefaultKeepsEnvReceiver(t *testing.T) {
	e := newCGAPIEnv(t)
	f := e.f
	r := e.admin(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp977-default","client_id":"`+f.Client1+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	g := r.Group.ID
	c, _ := e.collector(f.Client1, "wp977-default")
	cgWant(t, "join", e.admin(http.MethodPost, "/api/v1/collector-groups/"+g+"/members", `{"collector_id":"`+c+`"}`), http.StatusOK, "")
	dist := probe.NewDistributor(probe.NewPGRepository(f.App), discovery.NewHandler(discovery.NewPGRepository(f.App), nil).CheckCollectorChannel)
	answer := func() probewire.AssignmentSet {
		t.Helper()
		got, ok := dist.Answer(context.Background(), f.OrgA, c, nil).(probewire.AssignmentSet)
		if !ok {
			t.Fatalf("answer %#v, want a set", got)
		}
		return got
	}
	if set := answer(); set.TrapConfig != nil {
		t.Fatalf("set of a group never configured carries trap_config %+v", set.TrapConfig)
	}
	cgWant(t, "rename", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+g, `{"name":"wp977-default-2"}`), http.StatusOK, "")
	before := answer()
	if before.TrapConfig != nil {
		t.Fatalf("set after a rename carries trap_config %+v", before.TrapConfig)
	}
	cgWant(t, "off explicitly", e.admin(http.MethodPatch, "/api/v1/collector-groups/"+g, `{"trap_config":{"enabled":false}}`), http.StatusOK, "")
	after := answer()
	if after.Version <= before.Version || after.TrapConfig == nil || after.TrapConfig.Enabled {
		t.Fatalf("set after the first configuration: version %d (was %d), trap_config %+v; want a new version with the receiver off",
			after.Version, before.Version, after.TrapConfig)
	}
}
