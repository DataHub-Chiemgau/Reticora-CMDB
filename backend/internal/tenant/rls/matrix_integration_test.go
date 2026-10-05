package rls_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// tri is the expected outcome of a read: rows outside the principal's scope
// must stay invisible, rows inside must be visible; org-wide rows (scope
// column NULL) and global catalog rows (organization_id NULL) may be shown
// read-only, so the matrix accepts both.
type tri int

const (
	no tri = iota
	maybe
	yes
)

func and(a, b tri) tri { return min(a, b) }

// matrixPrincipal is one tenant context of the matrix: organization plus
// client, site and team scope (nil = all of the organization, TEN-04).
type matrixPrincipal struct {
	name  string
	org   string
	scope map[string][]string // client, site, team
}

func (p *matrixPrincipal) scoped() bool { return len(p.scope) > 0 }

func gucList(ids []string) string { return strings.Join(ids, ",") }

func dimRead(scope []string, v string) tri {
	switch {
	case scope == nil:
		return yes
	case v == "":
		return maybe
	case slices.Contains(scope, v):
		return yes
	}
	return no
}

// dimWrite: a scoped principal may neither create nor change nor delete
// rows outside its scope, org-wide rows (NULL) included (TEN-05).
func dimWrite(scope []string, v string) bool {
	return scope == nil || (v != "" && slices.Contains(scope, v))
}

func (p *matrixPrincipal) read(tbl *matrixTable, r scopeRow) tri {
	org := no
	switch r.org {
	case p.org:
		org = yes
	case "":
		org = maybe
	}
	if tbl.inherited {
		return org
	}
	res := org
	for _, dim := range scopeDimensions {
		if len(tbl.scope[dim]) > 0 {
			res = and(res, dimRead(p.scope[dim], r.dims[dim]))
		}
	}
	return res
}

func (p *matrixPrincipal) write(tbl *matrixTable, r scopeRow) bool {
	if r.org != p.org {
		return false
	}
	if tbl.inherited {
		return true
	}
	for _, dim := range scopeDimensions {
		if len(tbl.scope[dim]) > 0 && !dimWrite(p.scope[dim], r.dims[dim]) {
			return false
		}
	}
	return true
}

// rlsDenied reports whether err is a row level security violation.
func rlsDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "row-level security")
}

// TestRLSMatrix covers WP-065 (TEN-09, TST-02, CH25): for every tenant
// table it checks SELECT, INSERT, UPDATE and DELETE of the application role
// against rows of every scope combination (organization A and B, clients,
// sites and teams in and out of scope, org-wide rows with NULL scope columns,
// global catalog rows) for principals with organization, client, site,
// team and combined scopes. UPDATE also tries to move a row out of the
// principal's scope and to claim an org-wide row.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestRLSMatrix(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	defer admin.Close()
	requireMigrated(ctx, t, admin)

	var (
		orgA, orgB = matrixUUID(), matrixUUID()
		c1, c2, cB = matrixUUID(), matrixUUID(), matrixUUID()
		s1, s2, sB = matrixUUID(), matrixUUID(), matrixUUID()
		t1, t2, tB = matrixUUID(), matrixUUID(), matrixUUID()
	)
	row := func(name, org, client, site, team string) scopeRow {
		return scopeRow{name: name, org: org, dims: map[string]string{"client": client, "site": site, "team": team}}
	}
	seeds := []scopeRow{
		row("A/c1/s1/t1", orgA, c1, s1, t1),
		row("A/c2/s2/t2", orgA, c2, s2, t2),
		row("A/c1/s2/t1", orgA, c1, s2, t1),
		row("B/cB/sB/tB", orgB, cB, sB, tB),
	}
	principals := []*matrixPrincipal{
		{name: "org A", org: orgA},
		{name: "client c1", org: orgA, scope: map[string][]string{"client": {c1}}},
		{name: "site s1", org: orgA, scope: map[string][]string{"site": {s1}}},
		{name: "team t1", org: orgA, scope: map[string][]string{"team": {t1}}},
		{name: "clients c1,c2 + site s1", org: orgA, scope: map[string][]string{"client": {c1, c2}, "site": {s1}}},
		{name: "org B", org: orgB},
	}

	tables := loadMatrixTables(ctx, t, admin)
	if len(tables) < 50 {
		t.Fatalf("only %d tenant tables found; is the database migrated?", len(tables))
	}
	for _, tbl := range tables {
		t.Run(tbl.name, func(t *testing.T) {
			conn, err := admin.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			m := &matrixRun{t: t, ctx: ctx, conn: conn, tbl: tbl}
			m.run(seeds, principals)
		})
	}
}

type matrixRun struct {
	t    *testing.T
	ctx  context.Context
	conn *pgxpool.Conn
	tx   pgx.Tx
	tbl  *matrixTable
	seq  int
	// rows maps the ctid of every seeded row to its scope.
	rows  map[string]scopeRow
	order []string
}

func (m *matrixRun) exec(sql string, args ...any) {
	m.t.Helper()
	if _, err := m.tx.Exec(m.ctx, sql, args...); err != nil {
		m.t.Fatalf("%s: %v", sql, err)
	}
}

// attempt runs fn in a savepoint that is always rolled back.
func (m *matrixRun) attempt(fn func() (int64, error)) (int64, error) {
	m.t.Helper()
	m.exec("SAVEPOINT matrix")
	n, err := fn()
	m.exec("ROLLBACK TO SAVEPOINT matrix")
	return n, err
}

func (m *matrixRun) insert(r scopeRow) (string, error) {
	m.seq++
	sql, args := m.tbl.insertSQL(r, m.seq)
	var ctid string
	err := m.tx.QueryRow(m.ctx, sql, args...).Scan(&ctid)
	return ctid, err
}

func (m *matrixRun) run(seeds []scopeRow, principals []*matrixPrincipal) {
	t, tbl := m.t, m.tbl
	var err error
	if m.tx, err = m.conn.Begin(m.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.tx.Rollback(m.ctx) }()
	m.exec("SET LOCAL session_replication_role = replica")

	// Org-wide row: every nullable scope column NULL; global row:
	// organization_id NULL.
	all := slices.Clone(seeds)
	nullRow := scopeRow{name: "A/NULL scope", org: seeds[0].org, dims: maps.Clone(seeds[0].dims)}
	hasNull := false
	for _, dim := range scopeDimensions {
		if tbl.nullableDim(dim) {
			nullRow.dims[dim], hasNull = "", true
		}
	}
	if hasNull {
		all = append(all, nullRow)
	}
	if tbl.nullable[tbl.orgCol] {
		global := seeds[0]
		global.name, global.org = "global", ""
		all = append(all, global)
	}
	m.rows = map[string]scopeRow{}
	seen := map[string]bool{}
	var seeded []scopeRow
	for _, r := range all {
		if seen[tbl.key(r)] {
			continue
		}
		seen[tbl.key(r)] = true
		m.exec("SAVEPOINT seed")
		ctid, seedErr := m.insert(r)
		var pgErr *pgconn.PgError
		if errors.As(seedErr, &pgErr) && pgErr.Code == "23505" && r.org == seeds[0].org && r.name != seeds[0].name {
			// One row per organization (unique organization_id): the
			// first row of organization A stands for all of them.
			m.exec("ROLLBACK TO SAVEPOINT seed")
			continue
		}
		if seedErr != nil {
			t.Fatalf("seed %s: %v", r.name, seedErr)
		}
		m.exec("RELEASE SAVEPOINT seed")
		m.rows[ctid] = r
		m.order = append(m.order, ctid)
		seeded = append(seeded, r)
	}
	all = seeded

	for _, p := range principals {
		if tbl.inherited && p.scoped() {
			continue
		}
		m.exec("SET LOCAL ROLE " + database.DefaultAppRole)
		m.exec(`SELECT set_config('app.org_id', $1, true), set_config('app.user_id', '', true),
			set_config('app.client_scope', $2, true), set_config('app.site_scope', $3, true),
			set_config('app.team_scope', $4, true)`, p.org, gucList(p.scope["client"]), gucList(p.scope["site"]), gucList(p.scope["team"]))
		m.checkPrincipal(p, all)
		m.exec("RESET ROLE")
	}
}

func (m *matrixRun) checkPrincipal(p *matrixPrincipal, all []scopeRow) {
	t, tbl := m.t, m.tbl
	ident := pgx.Identifier{tbl.name}.Sanitize()
	fail := func(cmd, row, format string, args ...any) {
		t.Helper()
		t.Errorf("%s as %s on %s: %s", cmd, p.name, row, fmt.Sprintf(format, args...))
	}

	if tbl.privileges["SELECT"] {
		rows, err := m.tx.Query(m.ctx, `SELECT ctid::text FROM `+ident+` WHERE ctid = ANY ($1::tid[])`, m.order)
		if err != nil {
			t.Fatalf("SELECT as %s: %v", p.name, err)
		}
		visible, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("SELECT as %s: %v", p.name, err)
		}
		for _, ctid := range m.order {
			r := m.rows[ctid]
			switch want, got := p.read(tbl, r), slices.Contains(visible, ctid); {
			case want == yes && !got:
				fail("SELECT", r.name, "row in scope is invisible")
			case want == no && got:
				fail("SELECT", r.name, "row outside the scope is visible")
			}
		}
	}

	if tbl.privileges["INSERT"] {
		for _, r := range all {
			_, err := m.attempt(func() (int64, error) { _, e := m.insert(r); return 0, e })
			switch want := p.write(tbl, r); {
			case want && rlsDenied(err):
				fail("INSERT", r.name, "refused: %v", err)
			case !want && !rlsDenied(err):
				fail("INSERT", r.name, "not refused by row level security (err %v)", err)
			}
		}
	}

	for _, ctid := range m.order {
		r := m.rows[ctid]
		want := p.write(tbl, r)
		if tbl.privileges["UPDATE"] {
			m.checkUpdate(p, ctid, r, want, fail)
		}
		if tbl.privileges["DELETE"] {
			n, err := m.attempt(func() (int64, error) {
				tag, e := m.tx.Exec(m.ctx, `DELETE FROM `+ident+` WHERE ctid = $1::tid`, ctid)
				return tag.RowsAffected(), e
			})
			switch {
			case want && (err != nil || n != 1):
				fail("DELETE", r.name, "row in scope not deleted (%d rows, err %v)", n, err)
			case !want && err == nil && n != 0:
				fail("DELETE", r.name, "row outside the write scope deleted")
			case !want && err != nil && !rlsDenied(err):
				fail("DELETE", r.name, "unexpected error %v", err)
			}
		}
	}
}

// checkUpdate rewrites the row unchanged (allowed exactly within the write
// scope), then tries to move it out of the scope and to claim org-wide or
// global rows (both refused).
func (m *matrixRun) checkUpdate(p *matrixPrincipal, ctid string, r scopeRow, want bool, fail func(cmd, row, format string, args ...any)) {
	tbl := m.tbl
	// update sets every column of cols to value, or to itself when value
	// is empty.
	update := func(cols []string, value string) (int64, error) {
		return m.attempt(func() (int64, error) {
			args := []any{ctid}
			var set []string
			for _, col := range cols {
				c := pgx.Identifier{col}.Sanitize()
				if value == "" {
					set = append(set, c+" = "+c)
					continue
				}
				if len(args) == 1 {
					args = append(args, value)
				}
				set = append(set, c+" = $2::uuid")
			}
			tag, e := m.tx.Exec(m.ctx, `UPDATE `+pgx.Identifier{tbl.name}.Sanitize()+` SET `+strings.Join(set, ", ")+` WHERE ctid = $1::tid`, args...)
			return tag.RowsAffected(), e
		})
	}
	refused := func(n int64, err error) bool { return rlsDenied(err) || (err == nil && n == 0) }
	// movable: id columns (client, site tables) are keys, not scope
	// assignments.
	movable := func(dim string) []string {
		return slices.DeleteFunc(slices.Clone(tbl.scope[dim]), func(c string) bool { return c == "id" })
	}

	org := []string{tbl.orgCol}
	n, err := update(org, "")
	switch {
	case want && (err != nil || n != 1):
		fail("UPDATE", r.name, "row in scope not updated (%d rows, err %v)", n, err)
	case !want && !refused(n, err):
		fail("UPDATE", r.name, "row outside the write scope updated (%d rows, err %v)", n, err)
	}
	if !want {
		// Claiming: an org-wide or global row must not become the
		// principal's own by setting its scope.
		if r.org == "" {
			if n, err = update(org, p.org); !refused(n, err) {
				fail("UPDATE", r.name, "global row claimed for the organization (err %v)", err)
			}
		}
		if r.org != p.org || tbl.inherited {
			return
		}
		for _, dim := range scopeDimensions {
			if cols := movable(dim); len(cols) > 0 && r.dims[dim] == "" && p.scope[dim] != nil {
				if n, err = update(cols, p.scope[dim][0]); !refused(n, err) {
					fail("UPDATE", r.name, "org-wide row claimed by setting the %s scope (err %v)", dim, err)
				}
			}
		}
		return
	}
	// Moving out of the write scope.
	if tbl.orgCol != "id" {
		if n, err = update(org, matrixUUID()); !rlsDenied(err) {
			fail("UPDATE", r.name, "row moved to another organization (%d rows, err %v)", n, err)
		}
	}
	if tbl.inherited {
		return
	}
	for _, dim := range scopeDimensions {
		if cols := movable(dim); len(cols) > 0 && p.scope[dim] != nil {
			if n, err = update(cols, matrixUUID()); !rlsDenied(err) {
				fail("UPDATE", r.name, "row moved out of the %s scope (%d rows, err %v)", dim, n, err)
			}
		}
	}
}
