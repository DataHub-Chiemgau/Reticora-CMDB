package probe_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-939, PRB-01):
//
//	cd backend && go test -count=1 -p 1 -run 'TestProbe|TestConcurrentParentCycleRejected' ./internal/probe/

// probeRoutes are the routes of the environment with their audit.
var probeRoutes = []struct{ method, pattern string }{
	{http.MethodGet, "/api/v1/monitoring/probes"},
	{http.MethodPost, "/api/v1/monitoring/probes"},
	{http.MethodGet, "/api/v1/monitoring/probes/{id}"},
	{http.MethodPatch, "/api/v1/monitoring/probes/{id}"},
	{http.MethodDelete, "/api/v1/monitoring/probes/{id}"},
}

// probeEnv is the probe API of organization A on PostgreSQL behind the
// route authorization and the route audit of the server, with a collector
// group of client 1 (group1), of client 2 (group2) and one of
// organization B (groupB).
type probeEnv struct {
	t                      *testing.T
	f                      *scopetest.Fixture
	mux                    *chi.Mux
	group1, group2, groupB string
}

// principal is the scope of a request: org-wide without clients and sites.
type principal struct {
	clients, sites []string
}

func newProbeEnv(t *testing.T) *probeEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	inner := chi.NewRouter()
	probe.NewHandler(probe.NewPGRepository(f.App)).RegisterRoutes(inner)
	mux := chi.NewRouter()
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, rt := range probeRoutes {
		mode := middleware.AuditIntent
		if rt.method == http.MethodGet {
			mode = middleware.AuditNone
		}
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern), middleware.AuditRoute(rt.method, rt.pattern, auditor, mode, "")).
			Method(rt.method, rt.pattern, inner)
	}
	e := &probeEnv{t: t, f: f, mux: mux}
	e.group1 = e.scalar(`INSERT INTO collector_group (organization_id, client_id, name) VALUES ($1, $2, 'wp939-c1') RETURNING id::text`, f.OrgA, f.Client1)
	e.group2 = e.scalar(`INSERT INTO collector_group (organization_id, client_id, name) VALUES ($1, $2, 'wp939-c2') RETURNING id::text`, f.OrgA, f.Client2)
	e.groupB = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp939-b') RETURNING id::text`, f.OrgB)
	return e
}

func (e *probeEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// do sends a request of a principal with every permission in the scope of
// as.
func (e *probeEnv) do(as principal, method, path, body string) reply {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if as.clients != nil {
		scope.Clients = database.ScopeIDs(as.clients...)
		idScope.Clients = identity.ScopeSet{IDs: as.clients}
	}
	if as.sites != nil {
		scope.Sites = database.ScopeIDs(as.sites...)
		idScope.Sites = identity.ScopeSet{IDs: as.sites}
	}
	perms := identity.AllPermissions()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope: idScope, Permissions: perms, Credential: "wp939"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	r := reply{Code: rec.Code, Body: rec.Body.String()}
	var problem struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	r.Field = problem.Field
	_ = json.Unmarshal(rec.Body.Bytes(), &r.Probe)
	return r
}

var orgAdmin = principal{}

func (e *probeEnv) create(as principal, body string) probe.Probe {
	e.t.Helper()
	r := e.do(as, http.MethodPost, "/api/v1/monitoring/probes", body)
	if r.Code != http.StatusCreated {
		e.t.Fatalf("create %s: %d %s", body, r.Code, r.Body)
	}
	return r.Probe
}

// listTotal returns the total of GET /probes for the principal.
func (e *probeEnv) listTotal(as principal, query string) int {
	e.t.Helper()
	r := e.do(as, http.MethodGet, "/api/v1/monitoring/probes"+query, "")
	if r.Code != http.StatusOK {
		e.t.Fatalf("list: %d %s", r.Code, r.Body)
	}
	var body struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		e.t.Fatal(err)
	}
	return body.Total
}

// TestProbeClientAndSiteScope: a client principal sees and changes only the
// probes of its client; a client it does not see is 422 client_id; a
// site-restricted principal writes only probes of its sites (403 without
// site).
func TestProbeClientAndSiteScope(t *testing.T) {
	e := newProbeEnv(t)
	f := e.f
	p1 := e.create(orgAdmin, `{"type":"icmp","target":"10.9.1.1","client_id":"`+f.Client1+`"}`)
	p2 := e.create(orgAdmin, `{"type":"icmp","target":"10.9.2.1","client_id":"`+f.Client2+`"}`)
	if p1.CollectorGroupID != e.group1 || p2.CollectorGroupID != e.group2 {
		t.Fatalf("collector groups %s, %s; want the client's only group", p1.CollectorGroupID, p2.CollectorGroupID)
	}
	c1 := principal{clients: []string{f.Client1}}
	if n := e.listTotal(c1, ""); n != 1 {
		t.Errorf("client 1 lists %d probes, want 1", n)
	}
	if n := e.listTotal(orgAdmin, ""); n != 2 {
		t.Errorf("org admin lists %d probes, want 2", n)
	}
	want(t, "get own", e.do(c1, http.MethodGet, "/api/v1/monitoring/probes/"+p1.ID, ""), http.StatusOK, "")
	want(t, "get foreign", e.do(c1, http.MethodGet, "/api/v1/monitoring/probes/"+p2.ID, ""), http.StatusNotFound, "")
	want(t, "patch foreign", e.do(c1, http.MethodPatch, "/api/v1/monitoring/probes/"+p2.ID, `{"enabled":false}`), http.StatusNotFound, "")
	want(t, "delete foreign", e.do(c1, http.MethodDelete, "/api/v1/monitoring/probes/"+p2.ID, ""), http.StatusNotFound, "")
	want(t, "create foreign client", e.do(c1, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.9.2.2","client_id":"`+f.Client2+`"}`), http.StatusUnprocessableEntity, "client_id")
	want(t, "foreign collector group", e.do(c1, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.9.1.3","client_id":"`+f.Client1+`","collector_group_id":"`+e.group2+`"}`), http.StatusUnprocessableEntity, "collector_group_id")
	want(t, "foreign parent", e.do(c1, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.9.1.4","client_id":"`+f.Client1+`","parent_probe_id":"`+p2.ID+`"}`), http.StatusUnprocessableEntity, "parent_probe_id")
	own := e.create(c1, `{"type":"icmp","target":"10.9.1.2","client_id":"`+f.Client1+`"}`)
	want(t, "patch own", e.do(c1, http.MethodPatch, "/api/v1/monitoring/probes/"+own.ID, `{"enabled":false}`), http.StatusOK, "")
	if got := e.scalar(`SELECT enabled::text FROM probe WHERE id = $1`, p2.ID); got != "true" {
		t.Errorf("foreign probe changed: enabled %s", got)
	}

	// Site scope: a probe without site is outside the scope of a
	// site-restricted principal (row level security, 403); with its site
	// it is written.
	site := e.scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp939-site') RETURNING id::text`, f.OrgA, f.Client1)
	s1 := principal{clients: []string{f.Client1}, sites: []string{site}}
	want(t, "site principal without site", e.do(s1, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.9.1.5","client_id":"`+f.Client1+`"}`), http.StatusForbidden, "")
	withSite := e.create(s1, `{"type":"icmp","target":"10.9.1.6","client_id":"`+f.Client1+`","site_id":"`+site+`"}`)
	if withSite.SiteID != site {
		t.Errorf("site %q, want %s", withSite.SiteID, site)
	}
	want(t, "site principal patches a probe without site", e.do(s1, http.MethodPatch, "/api/v1/monitoring/probes/"+own.ID, `{"enabled":true}`),
		http.StatusNotFound, "")
	// A probe on a CI takes the client of the CI; a CI of another client
	// is invisible (422 ci_id).
	ci := f.CI(t, f.OrgA, f.Client1, "wp939-ci")
	ci2 := f.CI(t, f.OrgA, f.Client2, "wp939-ci-2")
	onCI := e.create(c1, `{"type":"icmp","target":"10.9.1.7","ci_id":"`+ci+`"}`)
	if onCI.ClientID != f.Client1 || onCI.CIID != ci {
		t.Errorf("probe on the CI: client %q ci %q", onCI.ClientID, onCI.CIID)
	}
	want(t, "ci of another client", e.do(c1, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.9.1.8","ci_id":"`+ci2+`"}`), http.StatusUnprocessableEntity, "ci_id")
}

// TestProbeOrgIsolation: probes, CIs, collector groups and parent probes of
// another organization are invisible: 404 and 422 alike unknown ids.
func TestProbeOrgIsolation(t *testing.T) {
	e := newProbeEnv(t)
	f := e.f
	clientB := e.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp939 b', 'wp939-b') RETURNING id::text`, f.OrgB)
	foreign := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
		VALUES ($1, $2, $3, 'icmp', '10.8.0.1') RETURNING id::text`, f.OrgB, clientB, e.groupB)
	foreignCI := e.scalar(`INSERT INTO ci (organization_id, ci_type_id, name, status)
		VALUES ($1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp939-b-ci', 'active') RETURNING id::text`, f.OrgB)
	want(t, "get", e.do(orgAdmin, http.MethodGet, "/api/v1/monitoring/probes/"+foreign, ""), http.StatusNotFound, "")
	want(t, "patch", e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+foreign, `{"enabled":false}`), http.StatusNotFound, "")
	want(t, "delete", e.do(orgAdmin, http.MethodDelete, "/api/v1/monitoring/probes/"+foreign, ""), http.StatusNotFound, "")
	if n := e.listTotal(orgAdmin, ""); n != 0 {
		t.Errorf("organization A lists %d probes, want 0", n)
	}
	base := `{"type":"icmp","target":"10.8.0.2","client_id":"` + f.Client1 + `"`
	want(t, "foreign parent", e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes", base+`,"parent_probe_id":"`+foreign+`"}`),
		http.StatusUnprocessableEntity, "parent_probe_id")
	want(t, "foreign group", e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes", base+`,"collector_group_id":"`+e.groupB+`"}`),
		http.StatusUnprocessableEntity, "collector_group_id")
	want(t, "foreign ci", e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.8.0.3","ci_id":"`+foreignCI+`"}`), http.StatusUnprocessableEntity, "ci_id")
	if got := e.scalar(`SELECT enabled::text FROM probe WHERE id = $1`, foreign); got != "true" {
		t.Errorf("probe of organization B changed: enabled %s", got)
	}
}

// TestProbeWriteEmitsAuditAndEvent: create, change and delete write the
// route audit with the probe's values and config.changed in the
// transaction of the write (same xmin as the probe row); a refused write
// writes neither.
func TestProbeWriteEmitsAuditAndEvent(t *testing.T) {
	e := newProbeEnv(t)
	f := e.f
	type outboxRow struct {
		xmin   string
		fields []string
		client string
	}
	lastEvent := func(id string) (outboxRow, int) {
		t.Helper()
		var row outboxRow
		var data []byte
		var n int
		if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) OVER (), xmin::text, data FROM event_outbox
			WHERE organization_id = $1 AND type = 'config.changed' AND entity_id = $2 ORDER BY sequence DESC LIMIT 1`,
			f.OrgA, id).Scan(&n, &row.xmin, &data); err != nil {
			t.Fatalf("config.changed of %s: %v", id, err)
		}
		var d struct {
			ClientID      string   `json:"client_id"`
			ChangedFields []string `json:"changed_fields"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatal(err)
		}
		row.fields, row.client = d.ChangedFields, d.ClientID
		return row, n
	}
	auditCount := func(id, route string) string {
		return e.scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2::uuid
			AND resource_type = 'monitoring' AND changes->>'route' = $3 AND changes ? 'values'`, f.OrgA, id, route)
	}

	p := e.create(orgAdmin, `{"type":"icmp","target":"10.7.0.1","client_id":"`+f.Client1+`","params":{"interval_s":60}}`)
	ev, n := lastEvent(p.ID)
	if n != 1 || ev.client != f.Client1 || ev.xmin != e.scalar(`SELECT xmin::text FROM probe WHERE id = $1`, p.ID) ||
		!strings.Contains(strings.Join(ev.fields, ","), "interval_s") {
		t.Fatalf("create: %d events, last %+v; want one in the transaction of the probe", n, ev)
	}
	if got := auditCount(p.ID, "POST /api/v1/monitoring/probes"); got != "1" {
		t.Errorf("create: %s audit entries with values, want 1", got)
	}

	r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+p.ID, `{"params":{"interval_s":90},"enabled":false}`)
	want(t, "patch", r, http.StatusOK, "")
	ev, n = lastEvent(p.ID)
	if n != 2 || ev.xmin != e.scalar(`SELECT xmin::text FROM probe WHERE id = $1`, p.ID) || strings.Join(ev.fields, ",") != "enabled,interval_s" {
		t.Fatalf("patch: %d events, last %+v; want changed_fields enabled,interval_s in the transaction of the change", n, ev)
	}
	if got := auditCount(p.ID, "PATCH /api/v1/monitoring/probes/{id}"); got != "1" {
		t.Errorf("patch: %s audit entries with values, want 1", got)
	}

	// A refused change writes no event and no entry.
	want(t, "refused patch", e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+p.ID, `{"params":{"interval_s":1}}`),
		http.StatusUnprocessableEntity, "interval_s")
	if _, n = lastEvent(p.ID); n != 2 {
		t.Errorf("refused patch: %d events, want still 2", n)
	}

	want(t, "delete", e.do(orgAdmin, http.MethodDelete, "/api/v1/monitoring/probes/"+p.ID, ""), http.StatusNoContent, "")
	ev, n = lastEvent(p.ID)
	if n != 3 || strings.Join(ev.fields, ",") != probe.DeletedField {
		t.Fatalf("delete: %d events, last %+v", n, ev)
	}
	if got := auditCount(p.ID, "DELETE /api/v1/monitoring/probes/{id}"); got != "1" {
		t.Errorf("delete: %s audit entries with values, want 1", got)
	}

	// Deleting a parent detaches its children (ON DELETE SET NULL), also a
	// child outside the caller's client scope: each gets config.changed
	// parent_probe_id in the transaction of the delete.
	parent := e.create(orgAdmin, `{"type":"icmp","target":"10.7.0.2","client_id":"`+f.Client1+`"}`)
	child := e.create(orgAdmin, `{"type":"icmp","target":"10.7.0.3","client_id":"`+f.Client2+`","parent_probe_id":"`+parent.ID+`"}`)
	want(t, "delete parent", e.do(principal{clients: []string{f.Client1}}, http.MethodDelete, "/api/v1/monitoring/probes/"+parent.ID, ""),
		http.StatusNoContent, "")
	ev, n = lastEvent(child.ID)
	if n != 2 || strings.Join(ev.fields, ",") != "parent_probe_id" || ev.client != f.Client2 {
		t.Fatalf("child of the deleted parent: %d events, last %+v; want config.changed parent_probe_id", n, ev)
	}
	if got := e.scalar(`SELECT COALESCE(parent_probe_id::text, '') FROM probe WHERE id = $1`, child.ID); got != "" {
		t.Fatalf("child keeps parent %s", got)
	}
}

// TestConcurrentParentCycleRejected: A→B and B→A sent at the same time
// cannot both pass: the checks wait for the advisory lock probe_parent:<org>
// one after the other, so the second sees the first and answers 409 with
// the cycle error (not a deadlock). Two concurrent reparents without a
// cycle (A→B, B→C) both pass.
func TestConcurrentParentCycleRejected(t *testing.T) {
	e := newProbeEnv(t)
	f := e.f
	a := e.create(orgAdmin, `{"type":"icmp","target":"10.6.0.1","client_id":"`+f.Client1+`"}`)
	b := e.create(orgAdmin, `{"type":"icmp","target":"10.6.0.2","client_id":"`+f.Client1+`"}`)
	c := e.create(orgAdmin, `{"type":"icmp","target":"10.6.0.3","client_id":"`+f.Client1+`"}`)

	replies := e.concurrentReparents([][2]string{{a.ID, b.ID}, {b.ID, a.ID}})
	ok, cycle := 0, 0
	for _, r := range replies {
		switch {
		case r.Code == http.StatusOK:
			ok++
		case r.Code == http.StatusConflict && r.Field == "parent_probe_id" && strings.Contains(r.Body, "cycle"):
			cycle++
		}
	}
	if ok != 1 || cycle != 1 {
		t.Fatalf("answers %+v, want one 200 and one 409 cycle error on parent_probe_id", replies)
	}
	cycles := e.scalar(`SELECT count(*)::text FROM probe x JOIN probe y ON y.id = x.parent_probe_id AND y.parent_probe_id = x.id
		WHERE x.organization_id = $1`, f.OrgA)
	if cycles != "0" {
		t.Fatalf("%s probes in a cycle", cycles)
	}

	// Reset and reparent without a cycle: A under B while B goes under C.
	for _, id := range []string{a.ID, b.ID} {
		want(t, "reset", e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+id, `{"parent_probe_id":null}`), http.StatusOK, "")
	}
	for _, r := range e.concurrentReparents([][2]string{{a.ID, b.ID}, {b.ID, c.ID}}) {
		want(t, "reparent without a cycle", r, http.StatusOK, "")
	}
}

// concurrentReparents sends PATCH parent_probe_id for each pair (probe,
// parent) at the same time: an outside transaction holds the advisory lock
// probe_parent:<org> until every request waits for it.
func (e *probeEnv) concurrentReparents(pairs [][2]string) []reply {
	t := e.t
	t.Helper()
	ctx := context.Background()
	holder, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('probe_parent:' || $1, 0))`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	replies := make([]reply, len(pairs))
	for i, pair := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies[i] = e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+pair[0], `{"parent_probe_id":"`+pair[1]+`"}`)
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err = e.f.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= len(pairs) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests wait for the lock, want %d", waiting, len(pairs))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	return replies
}
