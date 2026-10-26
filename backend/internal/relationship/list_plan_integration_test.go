package relationship

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The volumes of TestRelationshipListPlanBudget: organization A as the demo
// organization of the NFR-01 seed (cmd/seed -objects 10000
// -relationships-per-ci 10), organization B with the rack mounts the 500
// further organizations of that seed hold (40 each).
const (
	listPlanCIs           = 10000
	listPlanRelsPerCI     = 10
	listPlanForeignMounts = 20000
)

// listBufferBudget bounds the shared buffers one List call of a CI with
// about 20 edges touches: its COUNT and its page together. With one arm per
// end of the edge both read about 140 buffers each; with the OR of both
// ends the planner joined all 10,000 CIs of the organization with the
// projection (about 70,000 buffers each).
const listBufferBudget = 1000

// orEdges is the edge set of List for the CI $2 before WP-609: the stored
// and the projected edges of the organization $1, each filtered by both
// ends of the edge in one OR.
const orEdges = `
	SELECT id, organization_id, source_ci_id, target_ci_id, rel_type, attributes, source, confidence,
	       first_seen_at, last_seen_at, verification_state, source_system, notes, created_at, updated_at,
	       source_interface_id, target_interface_id, last_confirmed_at, verified_by, verified_at,
	       false AS projected, NULL::uuid AS target_location_id
	FROM ci_relationship WHERE organization_id = $1 AND ` + visibleEndpoints + ` AND (source_ci_id = $2 OR target_ci_id = $2)
	UNION ALL
	SELECT * FROM (` + projectedEdges + `) p WHERE p.organization_id = $1 AND (source_ci_id = $2 OR target_ci_id = $2)`

// orEdgesPage selects the rows of orEdges in the columns and the order of
// a page of List.
const orEdgesPage = `SELECT id::text, organization_id::text, source_ci_id::text, COALESCE(target_ci_id::text, ''),
		rel_type, attributes, source, confidence, first_seen_at, last_seen_at, verification_state,
		COALESCE(source_system, ''), COALESCE(notes, ''), created_at, updated_at,
		COALESCE(source_interface_id::text, ''), COALESCE(target_interface_id::text, ''), last_confirmed_at,
		COALESCE(verified_by::text, ''), verified_at,
		projected, COALESCE(target_location_id::text, '')
	FROM (` + orEdges + `) e ORDER BY projected, created_at DESC, id`

// planStatement is one statement a pool sent, with its arguments.
type planStatement struct {
	sql  string
	args []any
}

// planTrace records the statements a pool sends.
type planTrace struct {
	mu    sync.Mutex
	stmts []planStatement
}

func (l *planTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmts = append(l.stmts, planStatement{sql: data.SQL, args: data.Args})
	return ctx
}

func (l *planTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// since returns the statements after the first n and the new mark.
func (l *planTrace) since(n int) ([]planStatement, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]planStatement(nil), l.stmts[n:]...), len(l.stmts)
}

// listPlanFixture holds the CIs and locations the test lists by.
type listPlanFixture struct {
	server, serverClient2, switchHub, pduHub string
	chassis, blade, mounted, selfEdge        string
	rack, room, foreign                      string
}

// seedListPlan seeds organization A like the demo organization of the
// NFR-01 seed (cmd/seed): listPlanCIs CIs alternately of client 1 and 2,
// each in a room of its client, the first 40 of client 1 mounted in two
// racks; two switches and two PDUs as hubs; every server connected to a
// switch, powered by a PDU and depending on further servers up to
// listPlanRelsPerCI edges per CI. Besides: srv-00100 contains srv-00102
// and srv-00104, srv-00200 depends on itself. Organization B holds
// listPlanForeignMounts mounted CIs. Statistics are fresh (ANALYZE).
func seedListPlan(t *testing.T, f *scopetest.Fixture) listPlanFixture {
	t.Helper()
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	prefix := "wp609/" + f.OrgA
	ciID := func(i int) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT md5($1 || '/ci/' || $2::int)::uuid::text`, prefix, i).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	room1, room2 := f.Room(t, f.Client1, "list-plan-hq"), f.Room(t, f.Client2, "list-plan-branch")
	exec(`INSERT INTO rack (id, organization_id, room_id, name, height_units)
		SELECT md5($1 || '/rack/' || r)::uuid, $2, $3, 'R0' || r, 42 FROM generate_series(1, 2) AS r`, prefix, f.OrgA, room1)
	exec(`INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status)
		SELECT md5($1 || '/ci/' || i)::uuid, $2,
			CASE WHEN i <= 40 OR i % 2 = 0 THEN $3::uuid ELSE $4::uuid END,
			CASE WHEN i <= 20 THEN md5($1 || '/rack/1')::uuid WHEN i <= 40 THEN md5($1 || '/rack/2')::uuid
				WHEN i % 2 = 0 THEN $5::uuid ELSE $6::uuid END,
			t.id, CASE WHEN i <= 2 THEN 'sw-' WHEN i <= 4 THEN 'pdu-' ELSE 'srv-' END || lpad(i::text, 5, '0'), 'active'
		FROM generate_series(1, $7::int) AS i
		JOIN ci_type t ON t.organization_id IS NULL
			AND t.key = CASE WHEN i <= 2 THEN 'switch' WHEN i <= 4 THEN 'pdu' ELSE 'server' END`,
		prefix, f.OrgA, f.Client1, f.Client2, room1, room2, listPlanCIs)
	exec(`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face)
		SELECT $2, md5($1 || '/rack/' || CASE WHEN i <= 20 THEN 1 ELSE 2 END)::uuid, md5($1 || '/ci/' || i)::uuid,
			((i - 1) % 20) * 2 + 1, 1, 'front'
		FROM generate_series(1, 40) AS i`, prefix, f.OrgA)
	// The relationships of cmd/seed: LLDP to a switch, power from a PDU,
	// the uplink between the switches and dependencies between servers
	// up to listPlanRelsPerCI per CI.
	servers := listPlanCIs - 4
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		SELECT $2, md5($1 || '/ci/' || e.src)::uuid, md5($1 || '/ci/' || e.dst)::uuid, e.rel, e.source
		FROM (
			SELECT i AS src, 1 + i % 2 AS dst, 'connected_to' AS rel, 'discovery' AS source FROM generate_series(5, $3::int) AS i
			UNION ALL
			SELECT i, 3 + i % 2, 'powered_by', 'manual' FROM generate_series(5, $3::int) AS i
			UNION ALL
			SELECT 1, 2, 'connected_to', 'discovery'
			UNION ALL
			SELECT 5 + (d - 1) % $4::int, 5 + ((d - 1) % $4::int + 1 + (d - 1) / $4::int) % $4::int, 'depends_on', 'manual'
			FROM generate_series(1, $5::int) AS d
		) e`, prefix, f.OrgA, listPlanCIs, servers, listPlanRelsPerCI*listPlanCIs-(2*servers+1))
	fx := listPlanFixture{
		switchHub: ciID(1), pduHub: ciID(3), mounted: ciID(10),
		chassis: ciID(100), blade: ciID(102), selfEdge: ciID(200),
		server: ciID(8000), serverClient2: ciID(8001),
		room: room1,
	}
	exec(`UPDATE ci SET parent_ci_id = $1 WHERE id IN ($2, $3)`, fx.chassis, fx.blade, ciID(104))
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		VALUES ($1, $2, $2, 'runs_on', 'manual')`, f.OrgA, fx.selfEdge)
	if err := f.Admin.QueryRow(bg, `SELECT md5($1 || '/rack/1')::uuid::text`, prefix).Scan(&fx.rack); err != nil {
		t.Fatal(err)
	}

	// Organization B: the mounts of the other organizations.
	var roomB string
	if err := f.Admin.QueryRow(bg, `
		WITH c AS (INSERT INTO client (organization_id, name, slug) VALUES ($1, 'Client B', 'scopetest-0f-b1') RETURNING id),
		     s AS (INSERT INTO site (organization_id, client_id, name) SELECT $1, id, 'list-plan-b' FROM c RETURNING id),
		     b AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, 'list-plan-b' FROM s RETURNING id)
		INSERT INTO room (organization_id, building_id, name) SELECT $1, id, 'list-plan-b' FROM b RETURNING id::text`,
		f.OrgB).Scan(&roomB); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO rack (id, organization_id, room_id, name, height_units)
		SELECT md5($1 || '/b/rack/' || r)::uuid, $2, $3, 'B' || lpad(r::text, 4, '0'), 42
		FROM generate_series(1, ($4::int + 39) / 40) AS r`, prefix, f.OrgB, roomB, listPlanForeignMounts)
	exec(`INSERT INTO ci (id, organization_id, location_id, ci_type_id, name, status)
		SELECT md5($1 || '/b/ci/' || i)::uuid, $2, md5($1 || '/b/rack/' || ((i - 1) / 40 + 1))::uuid, t.id, 'list-plan-b-' || i, 'active'
		FROM generate_series(1, $3::int) AS i JOIN ci_type t ON t.organization_id IS NULL AND t.key = 'server'`,
		prefix, f.OrgB, listPlanForeignMounts)
	exec(`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face)
		SELECT $2, md5($1 || '/b/rack/' || ((i - 1) / 40 + 1))::uuid, md5($1 || '/b/ci/' || i)::uuid, (i - 1) % 40 + 1, 1, 'front'
		FROM generate_series(1, $3::int) AS i`, prefix, f.OrgB, listPlanForeignMounts)
	if err := f.Admin.QueryRow(bg, `SELECT md5($1 || '/b/ci/1')::uuid::text`, prefix).Scan(&fx.foreign); err != nil {
		t.Fatal(err)
	}
	exec(`ANALYZE ci, rack_mount, ci_relationship`)
	return fx
}

// sqlLiteral renders a statement argument as an SQL literal for EXECUTE:
// the uuids and the paging numbers of List.
func sqlLiteral(t *testing.T, v any) string {
	t.Helper()
	switch v := v.(type) {
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	t.Fatalf("no literal for %T", v)
	return ""
}

// planBuffers returns the shared buffers (hit and read) the execution of
// stmt touches as the application role in scope, from EXPLAIN (ANALYZE,
// BUFFERS) of the custom plan or, with generic, of the generic plan (pgx
// keeps prepared statements, and PostgreSQL may switch them to the generic
// plan after five executions), and the plan's outline.
func planBuffers(t *testing.T, app *pgxpool.Pool, scope *database.TenantScope, stmt planStatement, generic bool) (int, string) {
	t.Helper()
	var plan []byte
	err := database.WithTenant(context.Background(), app, scope, func(ctx context.Context, tx pgx.Tx) error {
		if !generic {
			return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan)
		}
		literals := make([]string, len(stmt.args))
		for i, a := range stmt.args {
			literals[i] = sqlLiteral(t, a)
		}
		for _, s := range []string{"SET LOCAL plan_cache_mode = force_generic_plan", "PREPARE wp609_list AS " + stmt.sql} {
			if _, err := tx.Exec(ctx, s); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE wp609_list("+strings.Join(literals, ", ")+")").Scan(&plan); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DEALLOCATE wp609_list")
		return err
	})
	if err != nil {
		t.Fatalf("explain %s: %v", stmt.sql, err)
	}
	var roots []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err = json.Unmarshal(plan, &roots); err != nil || len(roots) != 1 {
		t.Fatalf("plan %s: %v", plan, err)
	}
	hit, _ := roots[0].Plan["Shared Hit Blocks"].(float64)
	read, _ := roots[0].Plan["Shared Read Blocks"].(float64)
	return int(hit + read), planOutline(roots[0].Plan)
}

// planOutline renders the nodes of a plan that ran more than once or read
// a table, as "Node Type on relation (loops)".
func planOutline(node map[string]any) string {
	var parts []string
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		loops, _ := n["Actual Loops"].(float64)
		if rel, ok := n["Relation Name"].(string); ok || loops > 1 {
			parts = append(parts, fmt.Sprintf("%v on %s (%.0f loops)", n["Node Type"], rel, loops))
		}
		children, _ := n["Plans"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				walk(child)
			}
		}
	}
	walk(node)
	return strings.Join(parts, " > ")
}

// TestRelationshipListPlanBudget covers NFR-02 and ABN-02 for the
// relationships of a CI (GET /cis/{id}/relationships, WP-609) with the
// NFR-01 volumes: 10,000 CIs with 10 edges per CI, and 20,000 rack mounts
// of another organization. With RLS the planner estimates one visible CI;
// with the OR of both ends of the edge, which stays a filter above the
// projection ci_structure_edge, and a large rack_mount it joined every CI of
// the organization with the projection (about 70,000 buffers per
// statement, in custom and generic plans). The plan depends on the size of
// rack_mount, not on the number of organizations (the planner knows the
// organization of the session, so it expects all 10,000 CIs of A under
// the policy's organization predicate): on PostgreSQL 16 with TimescaleDB
// 2.31.0 (the CI image) it flipped between 12,040 and 14,040 mounts with
// the foreign mounts in one organization, and between 8,040 and 20,040
// with 40 mounts in each of 200 and 500 organizations.
//
//   - Budget: the COUNT and the page of one call, as the application role,
//     touch at most listBufferBudget shared buffers together, in the custom
//     and in the generic plan, for a server (org-wide and for a principal
//     of client 1), a containing and a contained CI, a mounted CI and a CI
//     with an edge to itself. The hubs (5,000 edges) are logged only.
//   - Same answers: total and every row equal those of the old query (one
//     OR over both ends) for these CIs, a server of client 2, the two
//     hubs, the rack and the room as targets of mounted_in and
//     located_in, a CI of organization B and an unknown id, org-wide and
//     for a principal of client 1; a page in the middle equals the same
//     rows of the old order.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipListPlanBudget(t *testing.T) {
	f := scopetest.Seed(t, "0f")
	fx := seedListPlan(t, f)

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	type principal struct {
		name  string
		scope *database.TenantScope
	}
	principals := []principal{{"org-wide", &orgWide}, {"client 1", &client1}}
	ctxOf := func(p principal) context.Context {
		return tenant.WithTenant(database.ContextWithTenantScope(context.Background(), p.scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	}

	t.Run("same answers", func(t *testing.T) {
		repo := NewPGRepository(f.App)
		ends := []struct{ name, id string }{
			{"server", fx.server}, {"server of client 2", fx.serverClient2},
			{"switch hub", fx.switchHub}, {"PDU hub", fx.pduHub},
			{"contains: source", fx.chassis}, {"contains: target", fx.blade},
			{"mounted_in, located_in: source", fx.mounted}, {"mounted_in: target (rack)", fx.rack},
			{"located_in: target (room)", fx.room}, {"edge to itself", fx.selfEdge},
			{"CI of organization B", fx.foreign}, {"unknown id", "c0de0ff0-0000-4000-8000-0000000000ff"},
		}
		for _, p := range principals {
			for _, end := range ends {
				want := make([]Relationship, 0)
				var wantTotal int
				if err := database.WithTenant(context.Background(), f.App, p.scope, func(ctx context.Context, tx pgx.Tx) error {
					if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ("+orEdges+") e", f.OrgA, end.id).Scan(&wantTotal); err != nil {
						return err
					}
					rows, err := tx.Query(ctx, orEdgesPage, f.OrgA, end.id)
					if err != nil {
						return err
					}
					defer rows.Close()
					for rows.Next() {
						rel, scanErr := scanRelationship(rows, true)
						if scanErr != nil {
							return scanErr
						}
						want = append(want, *rel)
					}
					return rows.Err()
				}); err != nil {
					t.Fatalf("%s, %s: old query: %v", p.name, end.name, err)
				}
				got, total, err := repo.List(ctxOf(p), f.OrgA, end.id, api.PaginationParams{Limit: len(want) + 1})
				if err != nil {
					t.Fatalf("%s, %s: %v", p.name, end.name, err)
				}
				if total != wantTotal || total != len(want) || !reflect.DeepEqual(got, want) {
					t.Errorf("%s, %s: total %d and %d rows, old query total %d and %d rows, rows equal %v",
						p.name, end.name, total, len(got), wantTotal, len(want), reflect.DeepEqual(got, want))
				}
				if len(want) > 12 {
					page, pageTotal, pageErr := repo.List(ctxOf(p), f.OrgA, end.id, api.PaginationParams{Limit: 7, Offset: 5})
					if pageErr != nil || pageTotal != wantTotal || !reflect.DeepEqual(page, want[5:12]) {
						t.Errorf("%s, %s: page 5..11 differs from the old order (total %d, %v)", p.name, end.name, pageTotal, pageErr)
					}
				}
				t.Logf("%s, %s: %d edges", p.name, end.name, total)
			}
		}
		// Without a CI, the list of the organization is unchanged.
		_, total, err := repo.List(ctxOf(principals[0]), f.OrgA, "", api.PaginationParams{Limit: 1})
		var wantTotal int
		if qerr := database.WithTenant(context.Background(), f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT
				(SELECT COUNT(*) FROM ci_relationship WHERE organization_id = $1 AND `+visibleEndpoints+`) +
				(SELECT COUNT(*) FROM (`+projectedEdges+`) p WHERE p.organization_id = $1)`, f.OrgA).Scan(&wantTotal)
		}); qerr != nil {
			t.Fatal(qerr)
		}
		if err != nil || total != wantTotal {
			t.Errorf("relationships of the organization: total %d (%v), want %d", total, err, wantTotal)
		}
	})

	t.Run("buffers", func(t *testing.T) {
		cfg := f.App.Config()
		trace := &planTrace{}
		cfg.ConnConfig.Tracer = trace
		cfg.MinConns = 0
		pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		mux := chi.NewRouter()
		NewHandler(NewPGRepository(pool)).RegisterRoutes(mux)
		mark := 0
		// call lists the relationships of ciID as the API does and returns
		// the COUNT and the page it sent.
		call := func(p principal, ciID string) (planStatement, planStatement) {
			t.Helper()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+ciID+"/relationships", nil).WithContext(ctxOf(p)))
			if w.Code != http.StatusOK {
				t.Fatalf("GET /cis/%s/relationships: %d %s", ciID, w.Code, w.Body.String())
			}
			var stmts []planStatement
			stmts, mark = trace.since(mark)
			var count, page []planStatement
			for _, s := range stmts {
				switch {
				case strings.HasPrefix(s.sql, "SELECT COUNT(*) FROM ("):
					count = append(count, s)
				case strings.Contains(s.sql, "ORDER BY projected"):
					page = append(page, s)
				}
			}
			if len(count) != 1 || len(page) != 1 {
				t.Fatalf("GET /cis/%s/relationships: %d counts and %d pages among %d statements, want 1 each", ciID, len(count), len(page), len(stmts))
			}
			return count[0], page[0]
		}
		for _, c := range []struct {
			name string
			p    principal
			id   string
			hub  bool
		}{
			{"server", principals[0], fx.server, false},
			{"server, client 1", principals[1], fx.server, false},
			{"contains: source", principals[0], fx.chassis, false},
			{"contains: target", principals[0], fx.blade, false},
			{"mounted CI", principals[0], fx.mounted, false},
			{"edge to itself", principals[0], fx.selfEdge, false},
			{"switch hub", principals[0], fx.switchHub, true},
			{"PDU hub", principals[0], fx.pduHub, true},
		} {
			count, page := call(c.p, c.id)
			for _, generic := range []bool{false, true} {
				mode := "custom"
				if generic {
					mode = "generic"
				}
				countBuffers, countPlan := planBuffers(t, f.App, c.p.scope, count, generic)
				pageBuffers, pagePlan := planBuffers(t, f.App, c.p.scope, page, generic)
				used := countBuffers + pageBuffers
				switch {
				case c.hub:
					t.Logf("%s (%s plan, not budgeted): %d buffers (COUNT %d, page %d)", c.name, mode, used, countBuffers, pageBuffers)
				case used > listBufferBudget:
					t.Errorf("%s (%s plan): %d buffers per call (COUNT %d, page %d), want at most %d\nCOUNT: %s\npage: %s",
						c.name, mode, used, countBuffers, pageBuffers, listBufferBudget, countPlan, pagePlan)
				default:
					t.Logf("%s (%s plan): %d buffers per call (COUNT %d, page %d)", c.name, mode, used, countBuffers, pageBuffers)
				}
			}
		}
	})
}
