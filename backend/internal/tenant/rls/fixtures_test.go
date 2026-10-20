package rls_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
)

// The RLS matrix (matrix_integration_test.go) needs one row per scope
// combination in every tenant table. The rows are synthesized from the
// catalog: every NOT NULL column without default gets a generated value of
// its type, the first allowed value of an "x = ANY (ARRAY[...])" check, or
// an entry of matrixOverrides. Seeding and the matrix itself run with
// session_replication_role = replica, which skips foreign keys and ordinary
// triggers but not row level security, so the matrix measures the policies
// alone and needs no parent rows.
//
// Inherited tables take their scope from other rows through a sub-select in
// a policy (document, maintenance_window, review_item, training_assignment);
// a synthesized row without parents would measure nothing. They are seeded
// through real parent rows (organizations, clients, sites, teams, users,
// CIs) with active triggers and foreign keys (inheritedModels, WP-353,
// F-354).

// matrixColumn is a NOT NULL column without default that a synthesized row
// has to fill.
type matrixColumn struct {
	name, typ string
	enumFirst string // first label for enum types
	allowed   string // first value of an "= ANY (ARRAY[...])" check
}

// matrixTable describes one tenant table for the matrix.
type matrixTable struct {
	name string
	// orgCol is organization_id, or id for organization itself.
	orgCol string
	// scope lists the columns per scope dimension (client, site, team):
	// <dim>_id, source_<dim>_id and target_<dim>_id, or id for the client
	// and site tables themselves. Every column of a dimension gets the same
	// value in a synthesized row.
	scope    map[string][]string
	nullable map[string]bool
	// privileges of reticora_app per command.
	privileges map[string]bool
	// inherited: a policy takes its scope from another table (sub-select);
	// the table is seeded and checked through inheritedModels.
	inherited bool
	fill      []matrixColumn
}

// matrixOverrides fixes values the generic rules cannot derive from the
// catalog (cross-column checks, patterns).
var matrixOverrides = map[string]map[string]string{
	"asset_movement":       {"asset_id": "@uuid"},
	"assignment":           {"asset_id": "@uuid"},
	"reservation":          {"asset_id": "@uuid"},
	"composition":          {"child_ci_id": "@uuid"},
	"desk_booking":         {"starts_at": "2026-01-01T00:00:00Z", "ends_at": "2026-01-01T01:00:00Z"},
	"maintenance_window":   {"starts_at": "2026-01-01T00:00:00Z", "ends_at": "2026-01-01T01:00:00Z"},
	"iga_connector":        {"type": "scim", "base_url": "https://scim.example"},
	"document":             {"storage_key": ""},
	"location":             {"kind": "site"},
	"event_outbox":         {"type": "ci.created"},
	"notification_channel": {"kind": "inapp", "provider": "inapp"},
	// Global rows (organization_id NULL) must be system rows.
	"lifecycle_definition":   {"is_system": "true"},
	"relationship_type":      {"is_system": "true"},
	"source_priority_policy": {"name": "@text"},
	// The i18n check needs a de/en map; the defaults trigger is skipped
	// under replica (MET-01, WP-115).
	"ci_type":              {"display_name": `{"de": "Matrix", "en": "Matrix"}`, "is_observable": "true"},
	"attribute_definition": {"display_name": `{"de": "Matrix", "en": "Matrix"}`, "scope": "type", "ci_type_id": "@uuid"},
}

var anyArrayCheck = regexp.MustCompile(`^CHECK \(\((\w+) = ANY \(\(?ARRAY\['([^']*)'`)

// loadMatrixTables returns every tenant table the application role can
// access: tables of schema public with RLS and an organization column (plus
// organization itself), without the view-protected hypertables.
func loadMatrixTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool) []*matrixTable {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname,
		       has_table_privilege('reticora_app', c.oid, 'SELECT'),
		       has_table_privilege('reticora_app', c.oid, 'INSERT'),
		       has_table_privilege('reticora_app', c.oid, 'UPDATE'),
		       has_table_privilege('reticora_app', c.oid, 'DELETE'),
		       EXISTS (SELECT 1 FROM pg_policies p WHERE p.schemaname = 'public' AND p.tablename = c.relname
		                  AND (COALESCE(p.qual, '') || COALESCE(p.with_check, '')) ~ '\mSELECT\M')
		  FROM pg_class c
		 WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p') AND c.relrowsecurity
		   AND (c.relname = 'organization' OR EXISTS (SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
		   AND has_table_privilege('reticora_app', c.oid, 'SELECT, INSERT, UPDATE, DELETE')
		 ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("load tenant tables: %v", err)
	}
	var tables []*matrixTable
	for rows.Next() {
		tbl := &matrixTable{privileges: map[string]bool{}, nullable: map[string]bool{}, scope: map[string][]string{}}
		var sel, ins, upd, del bool
		if err = rows.Scan(&tbl.name, &sel, &ins, &upd, &del, &tbl.inherited); err != nil {
			t.Fatal(err)
		}
		tbl.privileges["SELECT"], tbl.privileges["INSERT"], tbl.privileges["UPDATE"], tbl.privileges["DELETE"] = sel, ins, upd, del
		if _, view := rls.ViewProtectedTables[tbl.name]; !view {
			tables = append(tables, tbl)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, tbl := range tables {
		loadMatrixColumns(ctx, t, pool, tbl)
	}
	return tables
}

func loadMatrixColumns(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tbl *matrixTable) {
	t.Helper()
	checks := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = $1::regclass AND contype = 'c'`, tbl.name)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var def string
		if err = rows.Scan(&def); err != nil {
			t.Fatal(err)
		}
		if m := anyArrayCheck.FindStringSubmatch(def); m != nil {
			checks[m[1]] = m[2]
		}
	}
	rows.Close()

	rows, err = pool.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
		       a.atthasdef OR a.attidentity <> '' OR a.attgenerated <> '',
		       COALESCE((SELECT e.enumlabel FROM pg_enum e WHERE e.enumtypid = a.atttypid ORDER BY e.enumsortorder LIMIT 1), '')
		  FROM pg_attribute a
		 WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, tbl.name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col matrixColumn
		var notNull, hasDefault bool
		if err = rows.Scan(&col.name, &col.typ, &notNull, &hasDefault, &col.enumFirst); err != nil {
			t.Fatal(err)
		}
		dim := scopeDimension(tbl.name, col.name)
		switch {
		case col.name == "organization_id", tbl.name == "organization" && col.name == "id":
			tbl.orgCol = col.name
		case dim != "":
			tbl.scope[dim] = append(tbl.scope[dim], col.name)
		default:
			col.allowed = checks[col.name]
			_, overridden := matrixOverrides[tbl.name][col.name]
			if (notNull && !hasDefault) || overridden {
				tbl.fill = append(tbl.fill, col)
			}
			continue
		}
		tbl.nullable[col.name] = !notNull
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// scopeDimensions are the scope dimensions of TEN-04 below the organization.
var scopeDimensions = []string{"client", "site", "team"}

// scopeDimension returns the dimension a column carries, or "".
func scopeDimension(table, col string) string {
	for _, dim := range scopeDimensions {
		switch col {
		case dim + "_id", "source_" + dim + "_id", "target_" + dim + "_id":
			return dim
		case "id":
			if table == dim && dim != "team" { // team policies do not filter team itself
				return dim
			}
		}
	}
	return ""
}

// nullableDim reports whether every column of the dimension takes NULL.
func (tbl *matrixTable) nullableDim(dim string) bool {
	cols := tbl.scope[dim]
	for _, c := range cols {
		if !tbl.nullable[c] {
			return false
		}
	}
	return len(cols) > 0
}

// matrixUUID returns a random version 4 UUID.
func matrixUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// value generates a fresh value for a column of a synthesized row.
func (col *matrixColumn) value(table string, seq int) string {
	if v, ok := matrixOverrides[table][col.name]; ok {
		switch v {
		case "@uuid":
			return matrixUUID()
		case "@text":
			return fmt.Sprintf("m%d-%s", seq, matrixUUID()[:8])
		}
		return v
	}
	if col.allowed != "" {
		return col.allowed
	}
	if col.enumFirst != "" {
		return col.enumFirst
	}
	switch {
	case col.typ == "uuid":
		return matrixUUID()
	case strings.HasSuffix(col.typ, "[]"):
		return "{}"
	case col.typ == "text", strings.HasPrefix(col.typ, "character"), col.typ == "citext":
		return fmt.Sprintf("m%d-%s", seq, matrixUUID()[:8])
	case col.typ == "ltree":
		return fmt.Sprintf("m%d", seq)
	case col.typ == "inet":
		return fmt.Sprintf("10.%d.%d.%d", seq/65536%256, seq/256%256, seq%256)
	case col.typ == "cidr":
		return fmt.Sprintf("10.%d.%d.0/24", seq/256%256, seq%256)
	case col.typ == "macaddr":
		return fmt.Sprintf("02:00:00:%02x:%02x:%02x", seq/65536%256, seq/256%256, seq%256)
	case col.typ == "jsonb", col.typ == "json":
		return "{}"
	case col.typ == "boolean":
		return "false"
	case col.typ == "bytea":
		return `\x00`
	case strings.HasPrefix(col.typ, "timestamp"):
		return "2026-01-01T00:00:00Z"
	case col.typ == "date":
		return "2026-01-01"
	case col.typ == "interval":
		return "1 hour"
	default: // integer, bigint, smallint, numeric, real, double precision
		return "1"
	}
}

// scopeRow is the scope of one synthesized row; an empty value is NULL.
type scopeRow struct {
	name string
	org  string
	dims map[string]string // client, site, team
}

// key is the projection of r onto the scope columns the table has; two
// seeds with the same key would be the same row for the matrix.
func (tbl *matrixTable) key(r scopeRow) string {
	k := r.org
	for _, dim := range scopeDimensions {
		if len(tbl.scope[dim]) > 0 {
			k += "/" + r.dims[dim]
		}
	}
	return k
}

// insertSQL builds the INSERT of a synthesized row with the given scope and
// returns it with its arguments. It returns the ctid of the new row.
func (tbl *matrixTable) insertSQL(r scopeRow, seq int) (string, []any) {
	var cols, vals []string
	var args []any
	add := func(col, typ string, v any) {
		args = append(args, v)
		cols = append(cols, pgx.Identifier{col}.Sanitize())
		vals = append(vals, fmt.Sprintf("$%d::%s", len(args), typ))
	}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	add(tbl.orgCol, "uuid", nullable(r.org))
	for _, dim := range scopeDimensions {
		for _, col := range tbl.scope[dim] {
			add(col, "uuid", nullable(r.dims[dim]))
		}
	}
	for i := range tbl.fill {
		add(tbl.fill[i].name, tbl.fill[i].typ, tbl.fill[i].value(tbl.name, seq))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING ctid::text",
		pgx.Identifier{tbl.name}.Sanitize(), strings.Join(cols, ", "), strings.Join(vals, ", ")), args
}

// matrixIDs are the scope ids of the matrix: organizations A and B, clients
// c1 and c2 of A and cB of B, sites s1 and s3 of c1, s2 of c2 and sB of cB,
// teams t1 and t2 of A and tB of B.
type matrixIDs struct {
	orgA, orgB             string
	c1, c2, cB             string
	s1, s2, s3, sB         string
	t1, t2, tB             string
	ciType, userA, userB   string
	trainingA, trainingB   string
	trainingA2, trainingB2 string
}

func newMatrixIDs() matrixIDs {
	u := matrixUUID
	return matrixIDs{orgA: u(), orgB: u(), c1: u(), c2: u(), cB: u(), s1: u(), s2: u(), s3: u(), sB: u(), t1: u(), t2: u(), tB: u()}
}

// matrixParent is a real parent row of an inherited table: a CI (document
// links, window CIs, review candidates) or a user with team memberships
// (training assignments).
type matrixParent struct {
	id, org string
	dims    map[string]string // client and site of a CI
	teams   []string          // teams of a user
}

// inheritedRow is one seeded row of an inherited table. It takes its scope
// from parents (keys of matrixWorld.parents); a review item without
// candidates carries the client and site its writer set (scope).
type inheritedRow struct {
	name    string
	org     string
	parents []string
	scope   map[string]string
}

// inheritedModel seeds an inherited table and states what each principal
// may do with each row.
type inheritedModel struct {
	rows []inheritedRow
	// insert writes r with its links in q and returns its id; attempt
	// marks the INSERT checks (as opposed to seeding).
	insert func(ctx context.Context, q matrixQuerier, w *matrixWorld, r inheritedRow, attempt bool) (string, error)
	// read and write give the expected outcome of SELECT and of UPDATE
	// and DELETE; insert of INSERT (nil: write).
	read, write, insertOK func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool
}

// matrixQuerier is a transaction of the matrix.
type matrixQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// matrixWorld holds the parent rows the inherited tables are seeded from.
type matrixWorld struct {
	ids     matrixIDs
	parents map[string]matrixParent
}

// seedMatrixWorld inserts organizations, clients, sites, teams, users,
// trainings and CIs for the scope ids with triggers and foreign keys active
// and commits them, so INSERT attempts can run in transactions of their own
// (require_visible_parent accepts the parent of the own transaction only at
// the top level, not in a savepoint). The organizations are deleted with
// everything below them when the test ends.
func seedMatrixWorld(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ids matrixIDs) *matrixWorld {
	t.Helper()
	w := &matrixWorld{ids: ids, parents: map[string]matrixParent{}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	tag := matrixUUID()[:8]
	for _, o := range []string{ids.orgA, ids.orgB} {
		exec(`INSERT INTO organization (id, name, slug) VALUES ($1, $2, $2)`, o, "matrix-"+tag+"-"+o[:8])
	}
	t.Cleanup(func() {
		// team does not cascade with its organization.
		for _, sql := range []string{`DELETE FROM team WHERE organization_id IN ($1, $2)`, `DELETE FROM organization WHERE id IN ($1, $2)`} {
			if _, err := pool.Exec(context.Background(), sql, ids.orgA, ids.orgB); err != nil {
				t.Errorf("matrix cleanup: %v", err)
			}
		}
	})
	for _, c := range [][2]string{{ids.c1, ids.orgA}, {ids.c2, ids.orgA}, {ids.cB, ids.orgB}} {
		exec(`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, $3, $3)`, c[0], c[1], "matrix-"+c[0][:8])
	}
	for _, s := range [][3]string{{ids.s1, ids.orgA, ids.c1}, {ids.s3, ids.orgA, ids.c1}, {ids.s2, ids.orgA, ids.c2}, {ids.sB, ids.orgB, ids.cB}} {
		exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, s[0], s[1], s[2], "matrix-"+s[0][:8])
	}
	for _, team := range [][2]string{{ids.t1, ids.orgA}, {ids.t2, ids.orgA}, {ids.tB, ids.orgB}} {
		exec(`INSERT INTO team (id, organization_id, name) VALUES ($1, $2, $3)`, team[0], team[1], "matrix-"+team[0][:8])
	}
	user := func(org string, teams ...string) string {
		id := matrixUUID()
		exec(`INSERT INTO app_user (id, organization_id, email, display_name) VALUES ($1, $2, $3, $3)`, id, org, "matrix-"+id[:8]+"@matrix.invalid")
		for _, team := range teams {
			exec(`INSERT INTO team_member (team_id, user_id, organization_id) VALUES ($1, $2, $3)`, team, id, org)
		}
		return id
	}
	w.ids.userA, w.ids.userB = user(ids.orgA), user(ids.orgB)
	for key, u := range map[string]struct {
		org   string
		teams []string
	}{
		"user t1": {ids.orgA, []string{ids.t1}}, "user t2": {ids.orgA, []string{ids.t2}},
		"user t1+t2": {ids.orgA, []string{ids.t1, ids.t2}}, "user without team": {ids.orgA, nil},
		"user tB": {ids.orgB, []string{ids.tB}},
	} {
		w.parents[key] = matrixParent{id: user(u.org, u.teams...), org: u.org, teams: u.teams}
	}
	training := func(org string) string {
		id := matrixUUID()
		exec(`INSERT INTO training (id, organization_id, title) VALUES ($1, $2, 'matrix')`, id, org)
		return id
	}
	w.ids.trainingA, w.ids.trainingB = training(ids.orgA), training(ids.orgB)
	w.ids.trainingA2, w.ids.trainingB2 = training(ids.orgA), training(ids.orgB)
	if err := tx.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&w.ids.ciType); err != nil {
		t.Fatalf("server ci_type: %v", err)
	}
	for key, c := range map[string][3]string{
		"ci c1/s1": {ids.orgA, ids.c1, ids.s1}, "ci c1/s3": {ids.orgA, ids.c1, ids.s3},
		"ci c2/s2": {ids.orgA, ids.c2, ids.s2}, "org-wide ci": {ids.orgA, "", ""},
		"ci cB/sB": {ids.orgB, ids.cB, ids.sB},
	} {
		id := matrixUUID()
		exec(`INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status)
			VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, 'active')`, id, c[0], c[1], c[2], w.ids.ciType, "matrix-"+id[:8])
		w.parents[key] = matrixParent{id: id, org: c[0], dims: map[string]string{"client": c[1], "site": c[2]}}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit matrix parents: %v", err)
	}
	return w
}

func (w *matrixWorld) userFor(org string) string {
	if org == w.ids.orgA {
		return w.ids.userA
	}
	return w.ids.userB
}

// parentVisible: the principal sees the CI (TEN-05, NULL is visible).
func (w *matrixWorld) parentVisible(p *matrixPrincipal, key string) bool {
	for dim, v := range w.parents[key].dims {
		if !dimRead(p.scope[dim], v) {
			return false
		}
	}
	return true
}

// parentWritable: a link to the CI carries a client and site the principal
// may write (E-09: not NULL for a scoped principal).
func (w *matrixWorld) parentWritable(p *matrixPrincipal, key string) bool {
	for dim, v := range w.parents[key].dims {
		if !dimWrite(p.scope[dim], v) {
			return false
		}
	}
	return true
}

// orgWideCS: no client or site restriction (team scopes do not restrict
// documents, windows and review items).
func (p *matrixPrincipal) orgWideCS() bool { return p.scope["client"] == nil && p.scope["site"] == nil }

// linkedRows are the rows of document and maintenance_window: one per link
// combination, unlinked (org-wide), linked only to an org-wide CI, and one of
// organization B.
func linkedRows(ids matrixIDs) []inheritedRow {
	return []inheritedRow{
		{name: "A/c1s1", org: ids.orgA, parents: []string{"ci c1/s1"}},
		{name: "A/c1s3", org: ids.orgA, parents: []string{"ci c1/s3"}},
		{name: "A/c2s2", org: ids.orgA, parents: []string{"ci c2/s2"}},
		{name: "A/shared c1s1+c2s2", org: ids.orgA, parents: []string{"ci c1/s1", "ci c2/s2"}},
		{name: "A/shared c1s1+c1s3", org: ids.orgA, parents: []string{"ci c1/s1", "ci c1/s3"}},
		{name: "A/c1s1+org-wide CI", org: ids.orgA, parents: []string{"ci c1/s1", "org-wide ci"}},
		{name: "A/org-wide CI", org: ids.orgA, parents: []string{"org-wide ci"}},
		{name: "A/unlinked", org: ids.orgA},
		{name: "B/cBsB", org: ids.orgB, parents: []string{"ci cB/sB"}},
	}
}

// linkedModel: a row is visible without links or with one visible link; a
// principal restricted in the client or site scope changes, deletes and
// creates it only when every link is one it may write, so it never removes
// links of another client or org-wide links and creates no unlinked row
// (migrations 000064, 000139; WP-341, E-09).
func linkedModel(ids matrixIDs, insert func(ctx context.Context, q matrixQuerier, w *matrixWorld, r inheritedRow, attempt bool) (string, error)) *inheritedModel {
	return &inheritedModel{
		rows:   linkedRows(ids),
		insert: insert,
		read: func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool {
			if r.org != p.org {
				return false
			}
			if p.orgWideCS() || len(r.parents) == 0 {
				return true
			}
			return slices.ContainsFunc(r.parents, func(k string) bool { return w.parentVisible(p, k) })
		},
		write: func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool {
			if r.org != p.org {
				return false
			}
			if p.orgWideCS() {
				return true
			}
			return len(r.parents) > 0 && !slices.ContainsFunc(r.parents, func(k string) bool { return !w.parentWritable(p, k) })
		},
	}
}

// inheritedModels lists every table whose policy takes its scope from
// another table. A new one fails the matrix until it is modelled here.
func inheritedModels(ids matrixIDs) map[string]*inheritedModel {
	return map[string]*inheritedModel{
		"document": linkedModel(ids, func(ctx context.Context, q matrixQuerier, w *matrixWorld, r inheritedRow, attempt bool) (string, error) {
			var id string
			if err := q.QueryRow(ctx, `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
				VALUES ($1, 'matrix', 'f', '', $2) RETURNING id::text`, r.org, w.userFor(r.org)).Scan(&id); err != nil {
				return "", err
			}
			for _, k := range r.parents {
				if _, err := q.Exec(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`,
					r.org, id, w.parents[k].id); err != nil {
					return "", err
				}
			}
			return id, nil
		}),
		"maintenance_window": linkedModel(ids, func(ctx context.Context, q matrixQuerier, w *matrixWorld, r inheritedRow, attempt bool) (string, error) {
			var id string
			if err := q.QueryRow(ctx, `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at)
				VALUES ($1, 'matrix', '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z') RETURNING id::text`, r.org).Scan(&id); err != nil {
				return "", err
			}
			for _, k := range r.parents {
				if _, err := q.Exec(ctx, `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`,
					r.org, id, w.parents[k].id); err != nil {
					return "", err
				}
			}
			return id, nil
		}),
		"review_item":         reviewItemModel(ids),
		"training_assignment": trainingAssignmentModel(ids),
	}
}

// reviewItemModel: the client and site of a review item follow its
// candidate CIs (one common value, else NULL) or, without candidates, the
// writer's choice. A principal restricted in the client or site scope sees
// an item only when it sees every candidate, and an item without candidates
// only when it carries the restricted dimension (migration 000140); it
// writes it only when client and site are in its scope (E-09).
func reviewItemModel(ids matrixIDs) *inheritedModel {
	derived := func(w *matrixWorld, r inheritedRow) map[string]string {
		if len(r.parents) == 0 {
			return r.scope
		}
		out := map[string]string{}
		for _, dim := range []string{"client", "site"} {
			v := w.parents[r.parents[0]].dims[dim]
			for _, k := range r.parents[1:] {
				if w.parents[k].dims[dim] != v {
					v = ""
				}
			}
			out[dim] = v
		}
		return out
	}
	cols := func(w *matrixWorld, p *matrixPrincipal, r inheritedRow, ok func([]string, string) bool) bool {
		d := derived(w, r)
		return r.org == p.org && ok(p.scope["client"], d["client"]) && ok(p.scope["site"], d["site"])
	}
	read := func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool {
		if !cols(w, p, r, dimRead) {
			return false
		}
		if p.orgWideCS() {
			return true
		}
		if len(r.parents) == 0 {
			d := derived(w, r)
			return (p.scope["client"] == nil || d["client"] != "") && (p.scope["site"] == nil || d["site"] != "")
		}
		return !slices.ContainsFunc(r.parents, func(k string) bool { return !w.parentVisible(p, k) })
	}
	return &inheritedModel{
		rows: []inheritedRow{
			{name: "A/candidate c1s1", org: ids.orgA, parents: []string{"ci c1/s1"}},
			{name: "A/candidate c2s2", org: ids.orgA, parents: []string{"ci c2/s2"}},
			{name: "A/candidate c1s3", org: ids.orgA, parents: []string{"ci c1/s3"}},
			{name: "A/candidates c1s1+c2s2", org: ids.orgA, parents: []string{"ci c1/s1", "ci c2/s2"}},
			{name: "A/candidates c1s1+c1s3", org: ids.orgA, parents: []string{"ci c1/s1", "ci c1/s3"}},
			{name: "A/candidate org-wide CI", org: ids.orgA, parents: []string{"org-wide ci"}},
			{name: "A/no candidate c1s1", org: ids.orgA, scope: map[string]string{"client": ids.c1, "site": ids.s1}},
			{name: "A/no candidate c1", org: ids.orgA, scope: map[string]string{"client": ids.c1, "site": ""}},
			{name: "A/no candidate NULL", org: ids.orgA, scope: map[string]string{"client": "", "site": ""}},
			{name: "B/candidate cBsB", org: ids.orgB, parents: []string{"ci cB/sB"}},
		},
		insert: func(ctx context.Context, q matrixQuerier, w *matrixWorld, r inheritedRow, attempt bool) (string, error) {
			var cands []string
			for _, k := range r.parents {
				cands = append(cands, w.parents[k].id)
			}
			var id string
			err := q.QueryRow(ctx, `INSERT INTO review_item (organization_id, kind, candidate_ci_ids, client_id, site_id)
				VALUES ($1, 'scope_suggested', $2::uuid[], NULLIF($3, '')::uuid, NULLIF($4, '')::uuid) RETURNING id::text`,
				r.org, cands, r.scope["client"], r.scope["site"]).Scan(&id)
			return id, err
		},
		read: read,
		write: func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool {
			return read(w, p, r) && cols(w, p, r, dimWrite)
		},
		// INSERT checks only the derived columns (WITH CHECK).
		insertOK: func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool {
			return cols(w, p, r, dimWrite)
		},
	}
}

// trainingAssignmentModel: a team-scoped principal sees and writes the
// assignments of users in one of its teams (migration 000065); a user
// without team is not in any team scope. Client and site scopes do not
// restrict trainings.
func trainingAssignmentModel(ids matrixIDs) *inheritedModel {
	access := func(w *matrixWorld, p *matrixPrincipal, r inheritedRow) bool {
		if r.org != p.org {
			return false
		}
		return p.scope["team"] == nil || slices.ContainsFunc(w.parents[r.parents[0]].teams, func(team string) bool {
			return slices.Contains(p.scope["team"], team)
		})
	}
	return &inheritedModel{
		rows: []inheritedRow{
			{name: "A/user t1", org: ids.orgA, parents: []string{"user t1"}},
			{name: "A/user t2", org: ids.orgA, parents: []string{"user t2"}},
			{name: "A/user t1+t2", org: ids.orgA, parents: []string{"user t1+t2"}},
			{name: "A/user without team", org: ids.orgA, parents: []string{"user without team"}},
			{name: "B/user tB", org: ids.orgB, parents: []string{"user tB"}},
		},
		// The seeded rows use the first training of the organization, the
		// INSERT attempts the second (one assignment per training and user).
		insert: func(ctx context.Context, q matrixQuerier, w *matrixWorld, r inheritedRow, attempt bool) (string, error) {
			training := w.ids.trainingA
			if r.org == w.ids.orgB {
				training = w.ids.trainingB
			}
			if attempt {
				training = w.ids.trainingA2
				if r.org == w.ids.orgB {
					training = w.ids.trainingB2
				}
			}
			var id string
			err := q.QueryRow(ctx, `INSERT INTO training_assignment (organization_id, training_id, user_id) VALUES ($1, $2, $3) RETURNING id::text`,
				r.org, training, w.parents[r.parents[0]].id).Scan(&id)
			return id, err
		},
		read:  access,
		write: access,
	}
}
