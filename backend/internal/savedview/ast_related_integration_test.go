package savedview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// relGrants grants exactly the listed permission keys (PermissionChecker)
// and every feature (EntitlementChecker).
type relGrants map[string]bool

func (g relGrants) HasPermission(_ context.Context, _, _, key string) (bool, error) {
	return g[key], nil
}
func (relGrants) IsEnabled(context.Context, string, string) bool { return true }

// relEnv is the fixture of the WP-789 tests in organization A: hosts in the
// racks R12 and R7 with attributes.warranty_days_left, VMs that run on
// them, an intermediate CI and a deleted one, and a host of client 2.
type relEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	engine *PGQueryEngine
	owner  string
	r12    string
}

func newRelEnv(t *testing.T) *relEnv {
	t.Helper()
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	e := &relEnv{t: t, f: f, engine: NewPGQueryEngine(f.App), owner: f.AppUser(t, f.OrgA, "wp789-owner")}
	room := f.Room(t, f.Client1, "wp789-room")
	rack := func(name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, room, name).Scan(&id); err != nil {
			t.Fatalf("rack %s: %v", name, err)
		}
		return id
	}
	e.r12 = rack("R12")
	r7 := rack("R7")
	exec(`INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, searchable)
		VALUES ($1, NULL, 'global', 'warranty_days_left', 'integer', true)`, f.OrgA)
	ci := func(client, name, typeKey, location string, days int) string {
		t.Helper()
		id := f.CI(t, f.OrgA, client, name)
		var loc any
		if location != "" {
			loc = location
		}
		exec(`UPDATE ci SET ci_type_id = (SELECT id FROM ci_type WHERE key = $2 AND organization_id IS NULL),
			location_id = $3, attributes = jsonb_build_object('warranty_days_left', $4::int) WHERE id = $1`, id, typeKey, loc, days)
		return id
	}
	hostA := ci(f.Client1, "wp789-host-a", "server", e.r12, 10)  // in R12, warranty ends soon
	hostB := ci(f.Client1, "wp789-host-b", "server", r7, 10)     // outside R12
	hostC := ci(f.Client1, "wp789-host-c", "server", e.r12, 400) // later warranty
	// A host of client 2 in R12: the rack is a location of client 1, which
	// the location trigger refuses, so the client is set past the triggers.
	hostD := ci(f.Client1, "wp789-host-d", "server", e.r12, 10)
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, `SET LOCAL session_replication_role = replica`); err == nil {
		_, err = tx.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, hostD, f.Client2)
	}
	if err == nil {
		err = tx.Commit(bg)
	}
	if err != nil {
		_ = tx.Rollback(bg)
		t.Fatalf("move host d to client 2: %v", err)
	}
	mid := ci(f.Client1, "wp789-mid", "hypervisor", "", 999)
	gone := ci(f.Client1, "wp789-gone", "hypervisor", "", 999)
	chassis := ci(f.Client1, "wp789-chassis", "server", "", 999)
	vm := func(name string) string { return ci(f.Client1, name, "vm", "", 999) }
	vm1, vm2, vm3, vm4, vm5, vm6, vm7, vm8 := vm("wp789-vm1"), vm("wp789-vm2"), vm("wp789-vm3"), vm("wp789-vm4"),
		vm("wp789-vm5"), vm("wp789-vm6"), vm("wp789-vm7"), vm("wp789-vm8")
	for _, edge := range []struct{ src, dst, relType string }{
		{vm1, hostA, "runs_on"},
		{vm8, hostA, "hosted_on"},
		{vm2, hostB, "runs_on"},
		{vm3, hostC, "runs_on"},
		{vm4, hostD, "runs_on"},    // the host is invisible to client 1
		{vm5, hostA, "depends_on"}, // another type
		{vm6, mid, "runs_on"},      // two steps: vm6 -> mid -> hostA
		{mid, hostA, "hosted_on"},
		{vm7, gone, "runs_on"}, // through a deleted CI
		{gone, hostA, "hosted_on"},
	} {
		exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, $4)`,
			f.OrgA, edge.src, edge.dst, edge.relType)
	}
	exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, gone)
	// contains is structural (ci.parent_ci_id, F-244).
	exec(`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, hostA, chassis)
	return e
}

// run executes the spec as ctx and returns the sorted names of the rows.
func (e *relEnv) run(ctx context.Context, spec string) ([]string, int, error) {
	e.t.Helper()
	var s FilterSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		e.t.Fatal(err)
	}
	rows, total, err := e.engine.Query(ctx, e.f.OrgA, &s, api.PaginationParams{Limit: 100})
	names := []string{}
	for i := range rows {
		names = append(names, rows[i].Name)
	}
	sort.Strings(names)
	return names, total, err
}

// call runs a request as the owner with the permissions granted, the field
// policy and the client scope (nil: org-wide) and returns the status and
// the decoded body.
func (e *relEnv) call(granted relGrants, policy *permission.FieldPolicy, clients []string, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	mux := chi.NewRouter()
	NewHandler(NewPGRepository(e.f.App)).WithQueryEngine(e.engine).WithPermissions(granted).
		WithEntitlements(granted).WithInventory(true).RegisterRoutes(mux)
	org := e.f.OrgA
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: e.owner})
	scope := database.OrgWideScope(org, e.owner)
	if clients != nil {
		scope.Clients = database.ScopeIDs(clients...)
	}
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: e.owner, OrganizationID: org, Type: identity.PrincipalTypeUser})
	ctx = permission.WithPolicy(ctx, policy)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// srcQuery is the query of SRC-04: VMs on hosts in rack R12 whose warranty
// ends within 30 days, over runs_on and hosted_on with max_depth (0: none).
func (e *relEnv) srcQuery(maxDepth int) string {
	depth := ""
	if maxDepth != 0 {
		depth = fmt.Sprintf(`"max_depth": %d, `, maxDepth)
	}
	return `{"where": {"and": [{"field": "ci_type", "op": "eq", "value": "vm"},
		{"related": {"direction": "out", "rel_types": ["runs_on", "hosted_on"], ` + depth + `"where": {"and": [
			{"field": "location_id", "op": "eq", "value": "` + e.r12 + `"},
			{"field": "attributes.warranty_days_left", "op": "lt", "value": 30}]}}}]}}`
}

// TestStructuredQueryRelatedAgainstPostgres proves SRC-04 (WP-789): the
// related node finds exactly the VMs on hosts in rack R12 whose warranty
// ends within 30 days. Left out are a VM on a host outside R12, a VM on a
// host with a later warranty, a VM whose host the client-1 principal cannot
// see, a VM related by another type and, at depth 1, a VM behind an
// intermediate CI, which max_depth 2 finds, but not through a deleted CI;
// max_depth 6 is 422. in, both, the structural type contains and an unknown
// type (422) complete it; total counts the same query (K23).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredQueryRelatedAgainstPostgres(t *testing.T) {
	e := newRelEnv(t)
	client1 := e.f.ClientCtx(e.f.Client1)
	for _, c := range []struct {
		name string
		ctx  context.Context
		spec string
		want []string
	}{
		{"SRC-04 as client 1", client1, e.srcQuery(0), []string{"wp789-vm1", "wp789-vm8"}},
		{"SRC-04 org-wide sees the client-2 host", e.f.OrgCtx(e.f.OrgA), e.srcQuery(0), []string{"wp789-vm1", "wp789-vm4", "wp789-vm8"}},
		{"SRC-04 at depth 2", client1, e.srcQuery(2), []string{"wp789-vm1", "wp789-vm6", "wp789-vm8"}},
		{"in: hosts of vm1", client1, `{"where": {"related": {"direction": "in", "rel_types": ["runs_on"],
			"where": {"field": "name", "op": "eq", "value": "wp789-vm1"}}}}`, []string{"wp789-host-a"}},
		{"both without types", client1, `{"where": {"and": [{"field": "name", "op": "like", "value": "wp789-*"},
			{"related": {"direction": "both", "where": {"field": "name", "op": "eq", "value": "wp789-mid"}}}]}}`,
			[]string{"wp789-host-a", "wp789-vm6"}},
		{"structural contains", client1, `{"where": {"related": {"direction": "in", "rel_types": ["contains"],
			"where": {"field": "name", "op": "eq", "value": "wp789-chassis"}}}}`, []string{"wp789-host-a"}},
		{"not related", client1, `{"where": {"and": [{"field": "ci_type", "op": "eq", "value": "vm"},
			{"not": {"related": {"direction": "out", "max_depth": 5, "where": {"field": "name", "op": "like", "value": "wp789-host-*"}}}}]}}`,
			[]string{"wp789-vm4", "wp789-vm7"}},
	} {
		got, total, err := e.run(c.ctx, c.spec)
		if err != nil || !slices.Equal(got, c.want) || total != len(c.want) {
			t.Errorf("%s: %v (total %d, %v), want %v", c.name, got, total, err, c.want)
		}
	}
	for _, c := range []struct {
		name, spec, field string
	}{
		{"max_depth 6", e.srcQuery(6), "where.and[1].related.max_depth"},
		{"unknown relationship type", `{"where": {"related": {"direction": "out", "rel_types": ["runs_on", "wp789_nope"],
			"where": {"field": "name", "op": "eq", "value": "x"}}}}`, "where.related.rel_types[1]"},
		{"related on assets", `{"entity_kind": "asset", "where": {"related": {"direction": "out", "where": {"field": "name", "op": "eq", "value": "x"}}}}`, "where.related"},
		{"unknown attribute of the related CI", `{"where": {"related": {"direction": "out",
			"where": {"field": "attributes.wp789_nope", "op": "eq", "value": "x"}}}}`, "where.related.where"},
	} {
		_, _, err := e.run(client1, c.spec)
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != c.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422 on %s", c.name, err, c.field)
		}
		code, out := e.call(relGrants{"ci:read": true, "asset:read": true, "relationship:read": true}, permission.UnrestrictedPolicy(nil), nil,
			http.MethodPost, "/api/v1/search/query", c.spec)
		if code != http.StatusUnprocessableEntity || out["field"] != c.field {
			t.Errorf("%s via the API: %d %v, want 422 on %s", c.name, code, out, c.field)
		}
	}
	// Through the API as client 1: the same VMs.
	code, out := e.call(relGrants{"ci:read": true, "relationship:read": true}, permission.UnrestrictedPolicy(nil), []string{e.f.Client1},
		http.MethodPost, "/api/v1/search/query", e.srcQuery(0))
	data, _ := out["data"].([]any)
	if code != http.StatusOK || len(data) != 2 || out["total"] != float64(2) {
		t.Errorf("SRC-04 via the API: %d %v", code, out)
	}
}

// TestStructuredQueryRelatedRightsAgainstPostgres: related and the flat
// relationship fields need relationship:read on query and save (403
// missing permission relationship:read for a role with only ci:read and
// search:read), and every field of the filter of related passes the field
// permission check (403 with fields), also on save (WP-789).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredQueryRelatedRightsAgainstPostgres(t *testing.T) {
	e := newRelEnv(t)
	ciOnly := relGrants{"ci:read": true, "search:read": true}
	for _, body := range []string{
		e.srcQuery(0),
		`{"has_relationship": "runs_on"}`,
		`{"lacks_relationship": "runs_on"}`,
		`{"upstream_of": "` + e.r12 + `"}`,
		`{"downstream_of": "` + e.r12 + `"}`,
	} {
		code, out := e.call(ciOnly, permission.UnrestrictedPolicy(nil), nil, http.MethodPost, "/api/v1/search/query", body)
		if code != http.StatusForbidden || out["detail"] != "missing permission relationship:read" {
			t.Errorf("query %s: %d %v, want 403 missing permission relationship:read", body, code, out)
		}
		code, out = e.call(ciOnly, permission.UnrestrictedPolicy(nil), nil, http.MethodPost, "/api/v1/saved-views",
			`{"name": "wp789 rel", "entity_kind": "ci", "filter_spec": `+body+`}`)
		if code != http.StatusForbidden || out["detail"] != "missing permission relationship:read" {
			t.Errorf("save %s: %d %v, want 403 missing permission relationship:read", body, code, out)
		}
	}
	withRel := relGrants{"ci:read": true, "search:read": true, "relationship:read": true}
	if code, out := e.call(withRel, permission.UnrestrictedPolicy(nil), nil, http.MethodPost, "/api/v1/saved-views",
		`{"name": "wp789 saved", "entity_kind": "ci", "filter_spec": `+e.srcQuery(2)+`}`); code != http.StatusCreated {
		t.Errorf("save with relationship:read: %d %v", code, out)
	}
	// An asset field in the filter of related needs asset:read.
	assetBody := `{"where": {"related": {"direction": "out", "where": {"field": "asset.cost_center", "op": "eq", "value": "CC"}}}}`
	if code, out := e.call(withRel, permission.UnrestrictedPolicy(nil), nil, http.MethodPost, "/api/v1/search/query", assetBody); code != http.StatusForbidden ||
		out["detail"] != "missing permission asset:read" {
		t.Errorf("asset field of a related CI without asset:read: %d %v", code, out)
	}

	// Field permissions: warranty_days_left is unreadable for the role.
	const role = "00000000-0000-4000-8000-00000000f789"
	repo := permission.NewMemoryFieldGrants()
	bg := context.Background()
	key := "attributes.warranty_days_left"
	if _, err := repo.CreateFieldGrant(bg, e.f.OrgA, &permission.FieldGrant{RoleID: new(role), ObjectKind: permission.KindCI,
		FieldKey: &key, Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	perms := []identity.Permission{identity.PermCIRead, identity.PermRelationshipRead, "search:read"}
	policy, err := permission.NewFieldResolver(srcAccess{grants: []identity.Grant{{RoleID: role, Permissions: perms}}}, nil, repo, nil).
		Resolve(bg, identity.Principal{Subject: e.owner, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Permissions: perms})
	if err != nil {
		t.Fatal(err)
	}
	code, out := e.call(withRel, policy, nil, http.MethodPost, "/api/v1/search/query", e.srcQuery(0))
	if fields, _ := out["fields"].([]any); code != http.StatusForbidden || len(fields) != 1 || fields[0] != key || out["total"] != nil {
		t.Errorf("unreadable field in the filter of related: %d %v, want 403 with fields [%s]", code, out, key)
	}
	code, out = e.call(withRel, policy, nil, http.MethodPost, "/api/v1/saved-views",
		`{"name": "wp789 fr", "entity_kind": "ci", "filter_spec": `+e.srcQuery(0)+`}`)
	if fields, _ := out["fields"].([]any); code != http.StatusForbidden || len(fields) != 1 || fields[0] != key {
		t.Errorf("save with an unreadable field in the filter of related: %d %v", code, out)
	}
	readable := `{"where": {"related": {"direction": "out", "where": {"field": "location_id", "op": "eq", "value": "` + e.r12 + `"}}}}`
	if code, out = e.call(withRel, policy, nil, http.MethodPost, "/api/v1/search/query", readable); code != http.StatusOK {
		t.Errorf("readable fields in the filter of related: %d %v", code, out)
	}
}

// TestStructuredQueryRelatedIndexUseAgainstPostgres runs EXPLAIN on the
// statement of the query engine as the application role under row level
// security (SR-04, K22): the steps of the CTE read ci_relationship through
// idx_ci_rel_org_target (out: from the target to the source) or
// idx_ci_rel_org_source (in), both ways one of each, with a relationship
// type as unselective as in a filled CMDB (2,000 runs_on relationships).
// The plans are logged; their budget at 10,000 objects is WP-327.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredQueryRelatedIndexUseAgainstPostgres(t *testing.T) {
	e := newRelEnv(t)
	// A chain of 2,000 runs_on relationships makes the type as unselective
	// as in a filled CMDB; the statistics are refreshed.
	bg := context.Background()
	for _, sql := range []string{
		`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status)
			SELECT gen_random_uuid(), $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL),
				'wp789-bulk-' || n, 'active' FROM generate_series(1, 2001) n`,
		`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			SELECT $1, a.id, b.id, 'runs_on' FROM ci a JOIN ci b ON b.organization_id = a.organization_id
				AND b.name = 'wp789-bulk-' || (substr(a.name, 12)::int + 1)
			WHERE a.organization_id = $1 AND a.name LIKE 'wp789-bulk-%' AND $2::uuid IS NOT NULL`,
		`ANALYZE ci`,
		`ANALYZE ci_relationship`,
	} {
		args := []any{e.f.OrgA, e.f.Client1}
		if strings.HasPrefix(sql, "ANALYZE") {
			args = nil
		}
		if _, err := e.f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	explain := func(spec string) string {
		t.Helper()
		var s FilterSpec
		if err := json.Unmarshal([]byte(spec), &s); err != nil {
			t.Fatal(err)
		}
		var plan []string
		err := database.WithRequestTenant(e.f.ClientCtx(e.f.Client1), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			cat, err := resolveCatalog(ctx, tx, &s)
			if err != nil {
				return err
			}
			where, args, err := s.compile("ci", cat, []any{e.f.OrgA}, 2)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "EXPLAIN SELECT ci.id FROM ci WHERE ci.deleted_at IS NULL AND ci.organization_id = $1 AND "+where, args...)
			if err != nil {
				return err
			}
			plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		if err != nil {
			t.Fatalf("explain %s: %v", spec, err)
		}
		return strings.Join(plan, "\n")
	}
	for _, c := range []struct {
		name, spec string
		indexes    []string // all of them
	}{
		{"out (SRC-04)", e.srcQuery(2), []string{"idx_ci_rel_org_target"}},
		{"in", `{"where": {"related": {"direction": "in", "rel_types": ["runs_on"], "max_depth": 3,
			"where": {"field": "name", "op": "eq", "value": "wp789-vm1"}}}}`, []string{"idx_ci_rel_org_source"}},
		{"both", `{"where": {"related": {"direction": "both", "rel_types": ["runs_on"],
			"where": {"field": "name", "op": "eq", "value": "wp789-vm1"}}}}`, []string{"idx_ci_rel_org_source", "idx_ci_rel_org_target"}},
	} {
		plan := explain(c.spec)
		for _, index := range c.indexes {
			if !strings.Contains(plan, index) {
				t.Errorf("%s does not use %s:\n%s", c.name, index, plan)
			}
		}
		t.Logf("%s:\n%s", c.name, plan)
	}
}

// TestRelatedAttributeComparesInnerCI proves that the attribute comparison
// of a related node reads the related CI (rn<level>), not the CI of the
// enclosing query: outer and related CIs carry different values of the
// typed attribute warranty_days_left (system definition, 000225), at one
// and at two levels of nesting. The compiled SQL names the attribute of
// the alias of its level (reticora_attr_text(rn1.attributes, …)), never
// the bare column.
func TestRelatedAttributeComparesInnerCI(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	ci := func(name string, days int) string {
		t.Helper()
		id := f.CI(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = jsonb_build_object('warranty_days_left', $2::int) WHERE id = $1`, id, days); err != nil {
			t.Fatal(err)
		}
		return id
	}
	edge := func(src, dst string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'runs_on')`,
			f.OrgA, src, dst); err != nil {
			t.Fatal(err)
		}
	}
	// soonOnLate (10) runs on late (400), lateOnSoon (400) on soon (10);
	// top (400) runs on lateOnSoon, top2 (10) on soonOnLate.
	soon, late := ci("inner-soon", 10), ci("inner-late", 400)
	soonOnLate, lateOnSoon := ci("outer-soon-on-late", 10), ci("outer-late-on-soon", 400)
	top, top2 := ci("top-on-late-on-soon", 400), ci("top-on-soon-on-late", 10)
	edge(soonOnLate, late)
	edge(lateOnSoon, soon)
	edge(top, lateOnSoon)
	edge(top2, soonOnLate)

	engine := NewPGQueryEngine(f.App)
	ctx := f.OrgCtx(f.OrgA)
	lt30 := &Expr{Field: "attributes.warranty_days_left", Op: OpLt, Value: float64(30)}
	gt30 := &Expr{Field: "attributes.warranty_days_left", Op: OpGt, Value: float64(30)}
	related := func(where *Expr) *Expr {
		return &Expr{Related: &RelatedExpr{Direction: DirectionOut, Where: where}}
	}
	for _, c := range []struct {
		name  string
		where *Expr
		want  []string
	}{
		// CIs that run on a CI whose warranty ends within 30 days:
		// lateOnSoon (own value 400) and top2, not soonOnLate (own value
		// 10, runs on late), which a comparison of the outer CI would find.
		{"related lt 30", related(lt30), []string{lateOnSoon, top2}},
		{"related gt 30", related(gt30), []string{soonOnLate, top}},
		// Own value and related value differ.
		{"own gt 30, related lt 30", &Expr{And: []Expr{*gt30, *related(lt30)}}, []string{lateOnSoon}},
		{"own lt 30, related lt 30", &Expr{And: []Expr{*lt30, *related(lt30)}}, []string{top2}},
		// Two levels: runs on a CI (gt 30) that runs on one with lt 30.
		{"nested", related(&Expr{And: []Expr{*gt30, *related(lt30)}}), []string{top}},
		{"nested reverse", related(&Expr{And: []Expr{*lt30, *related(gt30)}}), []string{top2}},
	} {
		rows, _, err := engine.Query(ctx, f.OrgA, &FilterSpec{EntityKind: "ci", Where: c.where}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := []string{}
		for i := range rows {
			got = append(got, rows[i].ID)
		}
		want := append([]string{}, c.want...)
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", c.name, got, want)
		}
	}

	cat := &catalog{
		attrs:    map[attrRef]resolvedAttribute{{"ci", "warranty_days_left"}: {kind: kindNumber}},
		relAttrs: map[attrRef]resolvedAttribute{{"ci", "warranty_days_left"}: {kind: kindNumber}},
	}
	sql, _, _, err := related(&Expr{And: []Expr{*gt30, *related(lt30)}}).compile("ci", "ci", cat, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "reticora_attr_text(attributes") {
		t.Errorf("the attribute is not qualified by the alias of its level: %s", sql)
	}
	for _, alias := range []string{"rn1", "rn2"} {
		if !strings.Contains(sql, "reticora_attr_text("+alias+".attributes, 'warranty_days_left')") {
			t.Errorf("no comparison of %s.attributes: %s", alias, sql)
		}
	}
}
