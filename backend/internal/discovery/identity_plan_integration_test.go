package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// auditSQLCapture is a pgx tracer that records the statements of a pool
// whose SQL contains one of match (every statement when match is empty),
// with their arguments.
type auditSQLCapture struct {
	match []string
	mu    sync.Mutex
	sql   []string
	args  [][]any
}

func (c *auditSQLCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if len(c.match) == 0 || slices.ContainsFunc(c.match, func(m string) bool { return strings.Contains(data.SQL, m) }) {
		c.mu.Lock()
		c.sql = append(c.sql, data.SQL)
		c.args = append(c.args, append([]any(nil), data.Args...))
		c.mu.Unlock()
	}
	return ctx
}

func (c *auditSQLCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// last returns the latest recorded statement.
func (c *auditSQLCapture) last(t *testing.T) (string, []any) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sql) == 0 {
		t.Fatal("the repository sent no matching statement")
	}
	return c.sql[len(c.sql)-1], c.args[len(c.args)-1]
}

// take returns the recorded statements and forgets them.
func (c *auditSQLCapture) take() (sql []string, args [][]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sql, args = c.sql, c.args
	c.sql, c.args = nil, nil
	return sql, args
}

// tracedPool returns a copy of the application pool of f whose statements
// capture records.
func tracedPool(t *testing.T, f *scopetest.Fixture, capture *auditSQLCapture) *pgxpool.Pool {
	t.Helper()
	cfg := f.App.Config()
	cfg.ConnConfig.Tracer = capture
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// explainAs returns the plan (EXPLAIN COSTS OFF) of sql as the application
// role inside a transaction of scope (row level security applies).
func explainAs(t *testing.T, f *scopetest.Fixture, scope *database.TenantScope, sql string, args ...any) string {
	t.Helper()
	var lines []string
	if err := database.WithTenant(context.Background(), f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("explain: %v\n%s", err, sql)
	}
	return strings.Join(lines, "\n")
}

// TestAuditIdentitySearchWithMACsUsesIndexes covers WP-205 and WP-434
// (NFR-01, REC-02 "indexbasiert"; retest of F-114, identity part): the
// identity lookup of a record with MACs reads only identity indexes under
// row level security. Before migration 000161 identityFilter added
// `attributes @> ANY($n::jsonb[])` for the interface MACs; jsonb_contains is
// not leakproof, so below the RLS barrier of the application role the whole
// OR could not become index conditions and PostgreSQL scanned every CI of the
// organization for each incoming record. The MACs now come from
// ci_identity_mac with leakproof equality only.
//
// The test seeds 10,000 CIs into one organization, captures the SQL that
// PGRepository.IdentityCandidates and DeletedIdentityCandidates really send
// (pgx tracer on a copy of the application pool) and EXPLAINs it as the
// application role inside the tenant transaction, org-wide and restricted
// to a client. Requirement: no Seq Scan on ci, only identity indexes, the
// primary key and the index of ci_identity_mac serve the lookup. The same
// lookup without MACs is the control: it shows that the setup lets the
// planner use the identity indexes under RLS. The hits equal those of the
// former predicate on primary_mac and the fingerprint.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditIdentitySearchWithMACsUsesIndexes' ./internal/discovery/
func TestAuditIdentitySearchWithMACsUsesIndexes(t *testing.T) {
	f := scopetest.Seed(t, "dd")
	bg := context.Background()
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	// 10,000 discovered CIs with serial, MAC, IP and an interface MAC
	// fingerprint, like a seeded inventory.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, serial_number, primary_mac, management_ip, attributes, discovery_source)
		SELECT $1, $2, 'audit-idx-' || i, 'AUDSN' || i,
			('02:00:' || lpad(to_hex((i >> 24) & 255), 2, '0') || ':' || lpad(to_hex((i >> 16) & 255), 2, '0') || ':' ||
				lpad(to_hex((i >> 8) & 255), 2, '0') || ':' || lpad(to_hex(i & 255), 2, '0'))::macaddr,
			('10.201.' || (i / 256) || '.' || (i % 256))::inet,
			jsonb_build_object('fingerprint', jsonb_build_object('mac_addresses', jsonb_build_array('06:00:00:00:' ||
				lpad(to_hex((i >> 8) & 255), 2, '0') || ':' || lpad(to_hex(i & 255), 2, '0')))),
			'snmp'
		FROM generate_series(1, 10000) AS i`, f.OrgA, typeID); err != nil {
		t.Fatalf("seed 10k CIs: %v", err)
	}
	for _, table := range []string{"ci", "ci_identity_mac"} {
		if _, err := f.Admin.Exec(bg, `ANALYZE `+table); err != nil {
			t.Fatal(err)
		}
	}

	capture := &auditSQLCapture{match: []string{"SELECT id::text, created_at FROM ci", "deleted_at IS NOT NULL AND"}}
	repo := discovery.NewPGRepository(tracedPool(t, f, capture))

	scanLine := regexp.MustCompile(`(?m)^.*\b(Seq Scan|Index Scan|Index Only Scan|Bitmap Index Scan)\b.*$`)
	// The side table is read through its index with the MACs in the index
	// condition, not only the organization.
	macIndex := regexp.MustCompile(`using idx_ci_identity_mac on ci_identity_mac\n\s+Index Cond: \(.*\bmac = ANY\b`)
	identityIndexes := []string{"idx_ci_serial_number_lc", "idx_ci_serial", "uq_ci_org_serial", "idx_ci_primary_mac", "uq_ci_org_primary_mac", "idx_ci_mgmt_ip",
		"idx_ci_mgmt_ip_sys_object", "idx_ci_hardware_uuid", "uq_ci_org_hardware_uuid", "idx_ci_hostname_lc", "idx_ci_fqdn_lc",
		"ci_pkey", "ci_id_organization_key", "idx_ci_identity_mac"}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	offending := func(plan string) []string {
		var out []string
		for _, line := range scanLine.FindAllString(plan, -1) {
			ok := false
			for _, idx := range identityIndexes {
				if strings.Contains(line, " "+idx+" ") || strings.HasSuffix(line, " "+idx) {
					ok = true
				}
			}
			if !ok {
				out = append(out, strings.TrimSpace(line))
			}
		}
		return out
	}
	// explain runs the live search of q, requires the one matching CI and
	// returns the plan of its statement for the org-wide and the client
	// scope.
	explain := func(t *testing.T, q *discovery.IdentityQuery) (plans []string) {
		t.Helper()
		ids, qerr := repo.IdentityCandidates(f.OrgCtx(f.OrgA), f.OrgA, q)
		if qerr != nil || len(ids) != 1 {
			t.Fatalf("IdentityCandidates = %v, %v; want the one matching CI", ids, qerr)
		}
		sql, args := capture.last(t)
		return []string{explainAs(t, f, &orgWide, sql, args...), explainAs(t, f, &client1, sql, args...)}
	}

	// Control: serial number and management IP only.
	for _, plan := range explain(t, &discovery.IdentityQuery{SerialNumber: "AUDSN4242", ManagementIP: "10.201.16.146"}) {
		if bad := offending(plan); len(bad) > 0 {
			t.Fatalf("control: the lookup without MACs is not index-based under RLS: %v\n%s", bad, plan)
		}
	}
	for _, q := range []discovery.IdentityQuery{
		{SerialNumber: "AUDSN4242", MACs: []string{"02:00:00:00:10:92"}, ManagementIP: "10.201.16.146"},
		{MACs: []string{"06:00:00:00:10:92"}},
		{MACs: []string{"02:00:00:00:10:92", "06:00:00:00:10:92"}},
	} {
		for _, plan := range explain(t, &q) {
			if bad := offending(plan); len(bad) > 0 || strings.Contains(plan, "Seq Scan on ci") {
				t.Errorf("NFR-01/REC-02 violated: the identity lookup of a record with MACs %v scans the organization's inventory under RLS (%v) instead of using only identity indexes:\n%s",
					q.MACs, bad, plan)
			}
			if !macIndex.MatchString(plan) {
				t.Errorf("the identity lookup of MACs %v does not use the index of ci_identity_mac on the MACs:\n%s", q.MACs, plan)
			}
		}
	}

	// The search for soft-deleted candidates (CI-13) shares the filter.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE organization_id = $1 AND serial_number IN ('AUDSN17', 'AUDSN18')`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	gone, err := repo.DeletedIdentityCandidates(f.OrgCtx(f.OrgA), f.OrgA, &discovery.IdentityQuery{MACs: []string{"06:00:00:00:00:11", "02:00:00:00:00:12"}})
	if err != nil || len(gone) != 2 {
		t.Fatalf("DeletedIdentityCandidates = %d CIs, %v; want the two soft-deleted CIs by their MACs", len(gone), err)
	}
	sql, args := capture.last(t)
	for _, scope := range []*database.TenantScope{&orgWide, &client1} {
		plan := explainAs(t, f, scope, sql, args...)
		if bad := offending(plan); len(bad) > 0 || !macIndex.MatchString(plan) {
			t.Errorf("the search for soft-deleted candidates by MAC is not index-based under RLS (%v):\n%s", bad, plan)
		}
	}

	// The hits equal those of the former predicate (primary_mac and jsonb
	// containment of the fingerprint), live and soft-deleted.
	former := func(deleted bool, macs []string) []string {
		docs := make([]string, len(macs))
		for i, mac := range macs {
			docs[i] = `{"fingerprint":{"mac_addresses":["` + mac + `"]}}`
		}
		rows, qerr := f.Admin.Query(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND (deleted_at IS NOT NULL) = $2
			AND (primary_mac = ANY($3::macaddr[]) OR attributes @> ANY($4::jsonb[])) ORDER BY id`, f.OrgA, deleted, macs, docs)
		if qerr != nil {
			t.Fatal(qerr)
		}
		ids, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			t.Fatal(qerr)
		}
		return ids
	}
	for _, macs := range [][]string{
		{"02:00:00:00:00:01"}, {"06:00:00:00:00:02"}, {"02:00:00:00:27:0f", "06:00:00:00:27:10", "06:00:00:00:00:03"},
		{"06:00:00:00:00:11", "02:00:00:00:00:12"}, {"0a:00:00:00:00:01"},
	} {
		live, qerr := repo.IdentityCandidates(f.OrgCtx(f.OrgA), f.OrgA, &discovery.IdentityQuery{MACs: macs})
		if qerr != nil {
			t.Fatal(qerr)
		}
		slices.Sort(live)
		if want := former(false, macs); !slices.Equal(live, want) {
			t.Errorf("MACs %v: live hits %v, former predicate %v", macs, live, want)
		}
		items, qerr := repo.DeletedIdentityCandidates(f.OrgCtx(f.OrgA), f.OrgA, &discovery.IdentityQuery{MACs: macs})
		if qerr != nil {
			t.Fatal(qerr)
		}
		var deleted []string
		for i := range items {
			deleted = append(deleted, items[i].ID)
		}
		slices.Sort(deleted)
		if want := former(true, macs); !slices.Equal(deleted, want) {
			t.Errorf("MACs %v: deleted hits %v, former predicate %v", macs, deleted, want)
		}
	}
}

// TestIdentityMACTableFollowsCI covers migration 000161 (WP-434, TEN-05,
// E-08, E-09, CI-13): the trigger on ci keeps ci_identity_mac exact for
// writes of the application role from org-wide, client- and site-scoped
// sessions under FORCE ROW LEVEL SECURITY, without widening their rights:
//   - INSERT: primary MAC and the fingerprint strings that are MACs (any
//     spelling, duplicates once; numbers, non-MACs and EUI-64 skipped) with
//     the organization, client and site of the CI;
//   - UPDATE of the fingerprint and of primary_mac, a move to another site
//     through location_id (site_id is derived, not in the SET list) and to
//     another client: the rows follow;
//   - soft delete and restore keep the rows (the deleted candidate search
//     finds the CI by an interface MAC), a hard delete removes them;
//   - reads follow TEN-05 (a client-scoped principal sees its client and
//     rows without client, organization B nothing), direct writes outside
//     the scope or without client are refused (E-09), a scoped principal
//     changes no CI outside its scope and so no row of it;
//   - the identity search of a client-scoped principal finds only its own
//     CI of a MAC two clients share;
//   - both functions are SECURITY INVOKER with a fixed search_path.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIdentityMACTableFollowsCI(t *testing.T) {
	f := scopetest.Seed(t, "d3")
	bg := context.Background()
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	site1, site2, site3 := f.ID(), f.ID(), f.ID()
	for _, s := range []struct{ id, client string }{{site1, f.Client1}, {site2, f.Client1}, {site3, f.Client2}} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s.id, f.OrgA, s.client, "mac-site-"+s.id); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	clients12 := database.OrgWideScope(f.OrgA, f.User)
	clients12.Clients = database.ScopeIDs(f.Client1, f.Client2)
	onSite1 := database.OrgWideScope(f.OrgA, f.User)
	onSite1.Sites = database.ScopeIDs(site1)
	orgB := database.OrgWideScope(f.OrgB, f.User)
	as := func(scope *database.TenantScope, sql string, args ...any) (int64, error) {
		var n int64
		err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	insert := func(scope *database.TenantScope, name, client, location, primary, fingerprint string) string {
		t.Helper()
		var id string
		err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status, primary_mac, attributes)
				VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, 'active', NULLIF($6, '')::macaddr, $7::jsonb) RETURNING id::text`,
				f.OrgA, client, location, typeID, name, primary, fingerprint).Scan(&id)
		})
		if err != nil {
			t.Fatalf("insert %s as %v: %v", name, scope.Clients.IDs(), err)
		}
		return id
	}
	// rows returns the rows of ci as "mac client site" with "-" for NULL,
	// read without row level security.
	rows := func(ci string) []string {
		t.Helper()
		r, err := f.Admin.Query(bg, `SELECT mac::text || ' ' || COALESCE(client_id::text, '-') || ' ' || COALESCE(site_id::text, '-')
			FROM ci_identity_mac WHERE ci_id = $1 AND organization_id = $2 ORDER BY mac`, ci, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(r, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := func(label, ci string, client, site string, macs ...string) {
		t.Helper()
		var exp []string
		for _, mac := range macs {
			exp = append(exp, fmt.Sprintf("%s %s %s", mac, client, site))
		}
		if got := rows(ci); !slices.Equal(got, exp) {
			t.Errorf("%s: rows %q, want %q", label, got, exp)
		}
	}
	rlsRefused := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "42501"
	}

	// INSERT by an org-wide principal: primary MAC plus the fingerprint
	// strings that are MAC addresses.
	a := insert(&orgWide, "mac-a", f.Client1, site1, "00:1b:21:0a:00:01",
		`{"fingerprint": {"mac_addresses": ["00:1b:22:0a:00:01", "AA-BB-CC-0A-00-01", "001b.210a.0001", "not-a-mac", 42, "08:00:2b:01:02:03:04:05", ""]}}`)
	want("insert, org-wide", a, f.Client1, site1, "00:1b:21:0a:00:01", "00:1b:22:0a:00:01", "aa:bb:cc:0a:00:01")
	plain := insert(&orgWide, "mac-none", "", "", "", `{"fingerprint": {"mac_addresses": "00:1b:22:0a:00:09"}}`)
	want("fingerprint without array", plain, "-", "-")

	// INSERT by a client-scoped and by a site-scoped principal.
	b := insert(&client1, "mac-b", f.Client1, "", "00:1b:21:0b:00:01", `{"fingerprint": {"mac_addresses": ["00:1b:22:0b:00:01"]}}`)
	want("insert, client 1", b, f.Client1, "-", "00:1b:21:0b:00:01", "00:1b:22:0b:00:01")
	c := insert(&onSite1, "mac-c", f.Client1, site1, "", `{"fingerprint": {"mac_addresses": ["00:1b:22:0c:00:01", "00:1b:22:0c:00:02"]}}`)
	want("insert, site 1", c, f.Client1, site1, "00:1b:22:0c:00:01", "00:1b:22:0c:00:02")
	// A MAC client 2 shares with client 1's CI b.
	d := insert(&clients12, "mac-d", f.Client2, site3, "", `{"fingerprint": {"mac_addresses": ["00:1b:22:0b:00:01"]}}`)
	want("insert, clients 1 and 2", d, f.Client2, site3, "00:1b:22:0b:00:01")
	e := insert(&orgWide, "mac-e", "", "", "00:1b:21:0e:00:01", `{}`)
	want("insert without client", e, "-", "-", "00:1b:21:0e:00:01")

	// UPDATE of the MACs by the client-scoped principal.
	if _, err := as(&client1, `UPDATE ci SET attributes = jsonb_set(attributes, '{fingerprint,mac_addresses}', '["00:1b:22:0b:00:02", "00:1b:21:0b:00:01"]')
		WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	want("fingerprint changed", b, f.Client1, "-", "00:1b:21:0b:00:01", "00:1b:22:0b:00:02")
	if _, err := as(&client1, `UPDATE ci SET primary_mac = NULL, attributes = '{}' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	want("MACs removed", b, f.Client1, "-")
	if _, err := as(&client1, `UPDATE ci SET primary_mac = '00:1b:21:0b:00:03' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	want("primary MAC set", b, f.Client1, "-", "00:1b:21:0b:00:03")
	// Other columns leave the rows alone.
	if _, err := as(&client1, `UPDATE ci SET name = 'mac-b2', attributes = attributes || '{"rack": "R1"}' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	want("other columns", b, f.Client1, "-", "00:1b:21:0b:00:03")

	// Moves: the site through location_id by the site-scoped principal
	// is refused (site 2 is outside its scope: its location is invisible,
	// 23503), by the client-scoped one it follows; then client 1 → client 2
	// by the principal of both.
	var pgErr *pgconn.PgError
	if _, err := as(&onSite1, `UPDATE ci SET location_id = $2 WHERE id = $1`, c, site2); !errors.As(err, &pgErr) || (pgErr.Code != "42501" && pgErr.Code != "23503") {
		t.Errorf("site-scoped move out of the scope: err %v, want a refusal", err)
	}
	want("refused move", c, f.Client1, site1, "00:1b:22:0c:00:01", "00:1b:22:0c:00:02")
	if _, err := as(&client1, `UPDATE ci SET location_id = $2 WHERE id = $1`, c, site2); err != nil {
		t.Fatal(err)
	}
	want("moved to site 2", c, f.Client1, site2, "00:1b:22:0c:00:01", "00:1b:22:0c:00:02")
	if _, err := as(&clients12, `UPDATE ci SET client_id = $2, location_id = $3 WHERE id = $1`, c, f.Client2, site3); err != nil {
		t.Fatal(err)
	}
	want("moved to client 2", c, f.Client2, site3, "00:1b:22:0c:00:01", "00:1b:22:0c:00:02")

	// A scoped principal changes no CI outside its scope, so no row of it.
	if n, err := as(&client1, `UPDATE ci SET attributes = '{}', primary_mac = NULL WHERE id = ANY($1::uuid[])`, []string{c, d}); err != nil || n != 0 {
		t.Errorf("client 1 updated %d CIs of client 2 (err %v)", n, err)
	}
	want("CI of client 2 untouched", d, f.Client2, site3, "00:1b:22:0b:00:01")

	// Reads (TEN-05).
	visible := func(scope *database.TenantScope) []string {
		t.Helper()
		var ids []string
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			r, err := tx.Query(ctx, `SELECT DISTINCT ci_id::text FROM ci_identity_mac ORDER BY 1`)
			if err != nil {
				return err
			}
			ids, err = pgx.CollectRows(r, pgx.RowTo[string])
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	if got := visible(&orgWide); !slices.Equal(got, sorted(a, b, c, d, e)) {
		t.Errorf("org-wide principal sees rows of %v, want %v", got, sorted(a, b, c, d, e))
	}
	if got := visible(&client1); !slices.Equal(got, sorted(a, b, e)) {
		t.Errorf("client-1 principal sees rows of %v, want %v (client 1 and rows without client)", got, sorted(a, b, e))
	}
	if got := visible(&onSite1); !slices.Equal(got, sorted(a, b, e)) {
		t.Errorf("site-1 principal sees rows of %v, want %v (site 1 and rows without site)", got, sorted(a, b, e))
	}
	if got := visible(&orgB); len(got) != 0 {
		t.Errorf("organization B sees rows of %v", got)
	}

	// Direct writes: outside the scope, without client (E-09) and into
	// another organization are refused.
	for _, w := range []struct {
		label string
		scope *database.TenantScope
		sql   string
		args  []any
	}{
		{"client 1 writes a row of client 2", &client1, `INSERT INTO ci_identity_mac (organization_id, client_id, site_id, ci_id, mac) VALUES ($1, $2, $3, $4, '00:1b:2f:00:00:01')`,
			[]any{f.OrgA, f.Client2, site3, d}},
		{"client 1 writes a row without client", &client1, `INSERT INTO ci_identity_mac (organization_id, ci_id, mac) VALUES ($1, $2, '00:1b:2f:00:00:02')`,
			[]any{f.OrgA, b}},
		{"site 1 writes a row without site", &onSite1, `INSERT INTO ci_identity_mac (organization_id, client_id, ci_id, mac) VALUES ($1, $2, $3, '00:1b:2f:00:00:03')`,
			[]any{f.OrgA, f.Client1, b}},
		{"client 1 moves a row to client 2", &client1, `UPDATE ci_identity_mac SET client_id = $2 WHERE ci_id = $1`, []any{b, f.Client2}},
		{"organization B writes a row of A", &orgB, `INSERT INTO ci_identity_mac (organization_id, ci_id, mac) VALUES ($1, $2, '00:1b:2f:00:00:04')`,
			[]any{f.OrgA, a}},
	} {
		if _, err := as(w.scope, w.sql, w.args...); !rlsRefused(err) {
			t.Errorf("%s: err %v, want a row level security refusal", w.label, err)
		}
	}
	if n, err := as(&client1, `DELETE FROM ci_identity_mac WHERE ci_id = $1`, d); err != nil || n != 0 {
		t.Errorf("client 1 deleted %d rows of client 2 (err %v)", n, err)
	}

	// The identity search of a client-scoped principal finds its own CI of
	// a MAC client 2 shares, the org-wide one both.
	repo := discovery.NewPGRepository(f.App)
	if _, err := as(&clients12, `UPDATE ci SET primary_mac = '00:1b:22:0b:00:01' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	shared := &discovery.IdentityQuery{MACs: []string{"00:1b:22:0b:00:01"}}
	if ids, err := repo.IdentityCandidates(f.ClientCtx(f.Client1), f.OrgA, shared); err != nil || !slices.Equal(ids, []string{b}) {
		t.Errorf("client-1 identity search: %v (err %v), want only %s", ids, err, b)
	}
	if ids, err := repo.IdentityCandidates(f.OrgCtx(f.OrgA), f.OrgA, shared); err != nil || !slices.Equal(sorted(ids...), sorted(b, d)) {
		t.Errorf("org-wide identity search: %v (err %v), want %s and %s", ids, err, b, d)
	}

	// Soft delete and restore keep the rows: the CI stays a candidate of
	// the deleted search (CI-13) by an interface MAC.
	if _, err := as(&client1, `UPDATE ci SET deleted_at = now(), status = 'decommissioned' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	want("soft-deleted", a, f.Client1, site1, "00:1b:21:0a:00:01", "00:1b:22:0a:00:01", "aa:bb:cc:0a:00:01")
	byInterface := &discovery.IdentityQuery{MACs: []string{"aa:bb:cc:0a:00:01"}}
	if gone, err := repo.DeletedIdentityCandidates(f.ClientCtx(f.Client1), f.OrgA, byInterface); err != nil || len(gone) != 1 || gone[0].ID != a {
		t.Errorf("deleted search by interface MAC: %d CIs (err %v), want %s", len(gone), err, a)
	}
	if ids, err := repo.IdentityCandidates(f.ClientCtx(f.Client1), f.OrgA, byInterface); err != nil || len(ids) != 0 {
		t.Errorf("live search finds the soft-deleted CI: %v (err %v)", ids, err)
	}
	if _, err := as(&client1, `UPDATE ci SET deleted_at = NULL, status = 'active' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if ids, err := repo.IdentityCandidates(f.ClientCtx(f.Client1), f.OrgA, byInterface); err != nil || !slices.Equal(ids, []string{a}) {
		t.Errorf("live search after restore: %v (err %v), want %s", ids, err, a)
	}

	// A hard delete removes the rows with the CI.
	if n, err := as(&client1, `DELETE FROM ci WHERE id = $1`, a); err != nil || n != 1 {
		t.Fatalf("hard delete: %d rows, err %v", n, err)
	}
	want("hard-deleted", a, "", "")

	// Every remaining row matches its CI, and no function widens rights.
	var stale int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND (
			(SELECT COALESCE(array_agg(m.mac ORDER BY m.mac), '{}') FROM ci_identity_mac m WHERE m.ci_id = c.id)
				IS DISTINCT FROM (SELECT COALESCE(array_agg(x ORDER BY x), '{}') FROM unnest(ci_identity_macs(c.primary_mac, c.attributes)) AS x)
			OR EXISTS (SELECT 1 FROM ci_identity_mac m WHERE m.ci_id = c.id
				AND (m.organization_id, m.client_id, m.site_id) IS DISTINCT FROM (c.organization_id, c.client_id, c.site_id)))`, f.OrgA).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("%d CIs with rows that do not match them (err %v)", stale, err)
	}
	r, err := f.Admin.Query(bg, `SELECT proname || ' definer=' || prosecdef || ' ' || COALESCE(array_to_string(proconfig, ','), '')
		FROM pg_proc WHERE proname IN ('sync_ci_identity_mac', 'ci_identity_macs') AND pronamespace = 'public'::regnamespace ORDER BY proname`)
	if err != nil {
		t.Fatal(err)
	}
	funcs, err := pgx.CollectRows(r, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if exp := []string{"ci_identity_macs definer=false search_path=pg_catalog",
		"sync_ci_identity_mac definer=false search_path=pg_catalog, public, pg_temp"}; !slices.Equal(funcs, exp) {
		t.Errorf("functions %q, want %q", funcs, exp)
	}
}
