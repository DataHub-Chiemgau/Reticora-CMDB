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

// matrixHiddenGlobalRows lists the tables whose global rows (organization_id
// NULL) no tenant principal reads; every other global row is visible to
// every principal (read-only).
var matrixHiddenGlobalRows = map[string]string{
	"job": "system jobs of the scheduler, readable only with app.system (JOB-02)",
}

// matrixPrincipal is one tenant context of the matrix: organization plus
// client, site and team scope (nil = all of the organization, TEN-04).
type matrixPrincipal struct {
	name  string
	org   string
	scope map[string][]string // client, site, team
}

func gucList(ids []string) string { return strings.Join(ids, ",") }

// dimRead pins the read semantics of TEN-05: a scoped principal sees rows of
// its scope and org-wide rows (NULL), nothing else.
func dimRead(scope []string, v string) bool {
	return scope == nil || v == "" || slices.Contains(scope, v)
}

// dimWrite: a scoped principal may neither create nor change nor delete
// rows outside its scope, org-wide rows (NULL) included (TEN-05).
func dimWrite(scope []string, v string) bool {
	return scope == nil || (v != "" && slices.Contains(scope, v))
}

func (p *matrixPrincipal) read(tbl *matrixTable, r scopeRow) bool {
	switch r.org {
	case p.org:
	case "":
		if _, hidden := matrixHiddenGlobalRows[tbl.name]; hidden {
			return false
		}
	default:
		return false
	}
	for _, dim := range scopeDimensions {
		if len(tbl.scope[dim]) > 0 && !dimRead(p.scope[dim], r.dims[dim]) {
			return false
		}
	}
	return true
}

func (p *matrixPrincipal) write(tbl *matrixTable, r scopeRow) bool {
	if r.org != p.org {
		return false
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

// scopeDenied reports whether err is a row level security violation or a
// rejection by a scope trigger (42501, e.g. require_visible_parent or
// require_linked_row of migration 000139).
func scopeDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

// TestRLSMatrix covers WP-065 (TEN-09, TST-02, CH25): for every tenant
// table it checks SELECT, INSERT, UPDATE and DELETE of the application role
// against rows of every scope combination (organization A and B, clients,
// sites and teams in and out of scope, org-wide rows with NULL scope columns,
// global catalog rows) for principals with organization, client, site,
// team and combined scopes. UPDATE also tries to move a row out of the
// principal's scope and to claim an org-wide row. Reads are pinned exactly
// (TEN-05): org-wide rows are visible to scoped principals, global rows to
// everyone except in matrixHiddenGlobalRows.
//
// Inherited tables (document, maintenance_window, review_item,
// training_assignment) run with every principal too (WP-353, F-354): their
// rows are seeded through real parents with active triggers, shared,
// one-sided, unlinked and org-wide-linked rows included (inheritedModels).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestRLSMatrix(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	requireMigrated(ctx, t, admin)

	ids := newMatrixIDs()
	row := func(name, org, client, site, team string) scopeRow {
		return scopeRow{name: name, org: org, dims: map[string]string{"client": client, "site": site, "team": team}}
	}
	seeds := []scopeRow{
		row("A/c1/s1/t1", ids.orgA, ids.c1, ids.s1, ids.t1),
		row("A/c2/s2/t2", ids.orgA, ids.c2, ids.s2, ids.t2),
		row("A/c1/s2/t1", ids.orgA, ids.c1, ids.s2, ids.t1),
		row("B/cB/sB/tB", ids.orgB, ids.cB, ids.sB, ids.tB),
	}
	principals := matrixPrincipals(ids)
	// The inherited tables use committed parent rows with ids of their own,
	// apart from the synthesized organization, client and site rows.
	world := seedMatrixWorld(ctx, t, admin, newMatrixIDs())
	models := inheritedModels(world.ids)
	worldPrincipals := matrixPrincipals(world.ids)

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
			if !tbl.inherited {
				m.run(seeds, principals)
				return
			}
			model := models[tbl.name]
			if model == nil {
				t.Fatalf("%s takes its scope from another table; model it in inheritedModels", tbl.name)
			}
			m.runInherited(world, model, worldPrincipals)
		})
	}
}

// matrixPrincipals are the principals of the matrix: organization A,
// client c1, site s1, team t1, clients c1 and c2 with site s1, organization B.
func matrixPrincipals(ids matrixIDs) []*matrixPrincipal {
	return []*matrixPrincipal{
		{name: "org A", org: ids.orgA},
		{name: "client c1", org: ids.orgA, scope: map[string][]string{"client": {ids.c1}}},
		{name: "site s1", org: ids.orgA, scope: map[string][]string{"site": {ids.s1}}},
		{name: "team t1", org: ids.orgA, scope: map[string][]string{"team": {ids.t1}}},
		{name: "clients c1,c2 + site s1", org: ids.orgA, scope: map[string][]string{"client": {ids.c1, ids.c2}, "site": {ids.s1}}},
		{name: "org B", org: ids.orgB},
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
			case want && !got:
				fail("SELECT", r.name, "row in scope is invisible")
			case !want && got:
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
		if r.org != p.org {
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
	for _, dim := range scopeDimensions {
		if cols := movable(dim); len(cols) > 0 && p.scope[dim] != nil {
			if n, err = update(cols, matrixUUID()); !rlsDenied(err) {
				fail("UPDATE", r.name, "row moved out of the %s scope (%d rows, err %v)", dim, n, err)
			}
		}
	}
}

// runInherited seeds the rows of an inherited table on the parents of w with
// active triggers and checks every principal against model. INSERT attempts
// run in transactions of their own on a second connection and are rolled
// back after their deferred triggers fired.
func (m *matrixRun) runInherited(w *matrixWorld, model *inheritedModel, principals []*matrixPrincipal) {
	t, tbl := m.t, m.tbl
	var err error
	if m.tx, err = m.conn.Begin(m.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.tx.Rollback(m.ctx) }()
	rowIDs := make([]string, len(model.rows))
	for i, r := range model.rows {
		if rowIDs[i], err = model.insert(m.ctx, m.tx, w, r, false); err != nil {
			t.Fatalf("seed %s: %v", r.name, err)
		}
	}
	// Deferred constraint triggers of the seed rows run now, as the
	// maintenance role.
	m.exec("SET CONSTRAINTS ALL IMMEDIATE")
	insertOK := model.insertOK
	if insertOK == nil {
		insertOK = model.write
	}
	other, err := pgx.ConnectConfig(m.ctx, m.conn.Conn().Config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close(m.ctx) }()
	ident := pgx.Identifier{tbl.name}.Sanitize()
	const setScope = `SELECT set_config('app.org_id', $1, true), set_config('app.user_id', '', true),
		set_config('app.client_scope', $2, true), set_config('app.site_scope', $3, true),
		set_config('app.team_scope', $4, true)`

	for _, p := range principals {
		m.exec("SET LOCAL ROLE " + database.DefaultAppRole)
		m.exec(setScope, p.org, gucList(p.scope["client"]), gucList(p.scope["site"]), gucList(p.scope["team"]))
		fail := func(cmd, row, format string, args ...any) {
			t.Helper()
			t.Errorf("%s as %s on %s: %s", cmd, p.name, row, fmt.Sprintf(format, args...))
		}

		rows, err := m.tx.Query(m.ctx, `SELECT id::text FROM `+ident+` WHERE id = ANY ($1::uuid[])`, rowIDs)
		if err != nil {
			t.Fatalf("SELECT as %s: %v", p.name, err)
		}
		visible, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("SELECT as %s: %v", p.name, err)
		}
		for i, r := range model.rows {
			switch want, got := model.read(w, p, r), slices.Contains(visible, rowIDs[i]); {
			case want && !got:
				fail("SELECT", r.name, "row in scope is invisible")
			case !want && got:
				fail("SELECT", r.name, "row outside the scope is visible")
			}
		}

		for _, r := range model.rows {
			// SET CONSTRAINTS ALL IMMEDIATE fires the deferred triggers
			// before the rollback (E-09: require_linked_row rejects an
			// unlinked row at commit).
			err := pgx.BeginFunc(m.ctx, other, func(tx pgx.Tx) error {
				if _, e := tx.Exec(m.ctx, "SET LOCAL ROLE "+database.DefaultAppRole); e != nil {
					return e
				}
				if _, e := tx.Exec(m.ctx, setScope, p.org, gucList(p.scope["client"]), gucList(p.scope["site"]), gucList(p.scope["team"])); e != nil {
					return e
				}
				if _, e := model.insert(m.ctx, tx, w, r, true); e != nil {
					return e
				}
				if _, e := tx.Exec(m.ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
					return e
				}
				return errRollback
			})
			if errors.Is(err, errRollback) {
				err = nil
			}
			switch want := insertOK(w, p, r); {
			case want && err != nil:
				fail("INSERT", r.name, "refused: %v", err)
			case !want && !scopeDenied(err):
				fail("INSERT", r.name, "not refused by the scope (err %v)", err)
			}
		}

		for i, r := range model.rows {
			want := model.write(w, p, r)
			n, err := m.attempt(func() (int64, error) {
				tag, e := m.tx.Exec(m.ctx, `UPDATE `+ident+` SET organization_id = organization_id WHERE id = $1`, rowIDs[i])
				return tag.RowsAffected(), e
			})
			switch {
			case want && (err != nil || n != 1):
				fail("UPDATE", r.name, "row in scope not updated (%d rows, err %v)", n, err)
			case !want && !scopeDenied(err) && (err != nil || n != 0):
				fail("UPDATE", r.name, "row outside the write scope updated (%d rows, err %v)", n, err)
			}
			if want {
				if n, err = m.attempt(func() (int64, error) {
					tag, e := m.tx.Exec(m.ctx, `UPDATE `+ident+` SET organization_id = $2 WHERE id = $1`, rowIDs[i], matrixUUID())
					return tag.RowsAffected(), e
				}); !rlsDenied(err) {
					fail("UPDATE", r.name, "row moved to another organization (%d rows, err %v)", n, err)
				}
			}
			n, err = m.attempt(func() (int64, error) {
				tag, e := m.tx.Exec(m.ctx, `DELETE FROM `+ident+` WHERE id = $1`, rowIDs[i])
				return tag.RowsAffected(), e
			})
			switch {
			case want && (err != nil || n != 1):
				fail("DELETE", r.name, "row in scope not deleted (%d rows, err %v)", n, err)
			case !want && err == nil && n != 0:
				fail("DELETE", r.name, "row outside the write scope deleted")
			case !want && err != nil && !scopeDenied(err):
				fail("DELETE", r.name, "unexpected error %v", err)
			}
		}
		m.exec("RESET ROLE")
	}
}
