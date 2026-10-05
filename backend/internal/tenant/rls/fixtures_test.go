package rls_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
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
	// synthesized rows have no parent, so only the organization dimension
	// is checked here. Their scope is covered by module_object and
	// team_scope tests.
	inherited bool
	fill      []matrixColumn
}

// matrixOverrides fixes values the generic rules cannot derive from the
// catalog (cross-column checks, patterns).
var matrixOverrides = map[string]map[string]string{
	"asset_movement":     {"asset_id": "@uuid"},
	"assignment":         {"asset_id": "@uuid"},
	"reservation":        {"asset_id": "@uuid"},
	"composition":        {"child_ci_id": "@uuid"},
	"desk_booking":       {"starts_at": "2026-01-01T00:00:00Z", "ends_at": "2026-01-01T01:00:00Z"},
	"maintenance_window": {"starts_at": "2026-01-01T00:00:00Z", "ends_at": "2026-01-01T01:00:00Z"},
	"iga_connector":      {"type": "scim", "base_url": "https://scim.example"},
	"document":           {"storage_key": ""},
	"location":           {"kind": "site"},
	// Global rows (organization_id NULL) must be system rows.
	"lifecycle_definition":   {"is_system": "true"},
	"relationship_type":      {"is_system": "true"},
	"source_priority_policy": {"name": "@text"},
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
