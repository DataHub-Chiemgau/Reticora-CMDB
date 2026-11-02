package rls_test

import (
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
)

const (
	leakyBegin = "<!-- rls-leaky-indexes:begin -->"
	leakyEnd   = "<!-- rls-leaky-indexes:end -->"
)

// TestRLSIndexesAreLeakproof covers WP-836 (SR-04, K22; SRC-01, NFR-02,
// REC-02; E-133): every index of a table with row level security that
// rls.LeakyIndexQuery reports, because its expression or predicate calls a
// function that is not leakproof or its operator class has only operators
// that are not leakproof, is documented in rls.LeakyIndexes with the same
// table and findings. A reported index missing from the list (a new index
// the application role cannot use under the policies) and an entry that
// is no longer reported (a dropped or repaired index) both fail, so the
// list only shrinks. idx_ci_fts (to_tsvector over ci, migration 000021) is
// dropped by migration 000191 and must not come back. reticora_attr_text
// (migration 000162) is leakproof, so the attribute indexes idx_attr_* are
// never reported; the catalog proves the flag, no migration of phase 2 sets
// another function LEAKPROOF (E-133).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -run 'TestRLSIndexesAreLeakproof|TestLeakyIndexes' ./internal/tenant/rls/
func TestRLSIndexesAreLeakproof(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	defer pool.Close()
	requireMigrated(ctx, t, pool)

	rows, err := pool.Query(ctx, rls.LeakyIndexQuery)
	if err != nil {
		t.Fatalf("leaky index query: %v", err)
	}
	type found struct{ table, leaky string }
	reported := map[string]found{}
	for rows.Next() {
		var table, index, leaky string
		if err = rows.Scan(&table, &index, &leaky); err != nil {
			t.Fatal(err)
		}
		reported[index] = found{table, leaky}
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("leaky index query: %v", err)
	}

	var unlisted, closed, changed []string
	for _, index := range slices.Sorted(maps.Keys(reported)) {
		got := reported[index]
		known, ok := rls.LeakyIndexes[index]
		switch {
		case !ok:
			unlisted = append(unlisted, fmt.Sprintf("%s on %s: %s", index, got.table, got.leaky))
		case known.Table != got.table || known.Leaky != got.leaky:
			changed = append(changed, fmt.Sprintf("%s: listed on %s with %q, reported on %s with %q",
				index, known.Table, known.Leaky, got.table, got.leaky))
		}
	}
	for _, index := range slices.Sorted(maps.Keys(rls.LeakyIndexes)) {
		if _, ok := reported[index]; !ok {
			closed = append(closed, index)
		}
	}
	if len(unlisted) > 0 {
		t.Errorf("SR-04/K22 violated: indexes of tables with row level security the application role cannot use "+
			"(a function or operator class that is not leakproof); use a stored column with a leakproof operator, "+
			"a side table or reticora_attr_text instead (docs/schema-baseline.md, \"Indizes unter RLS\"):\n  %s",
			strings.Join(unlisted, "\n  "))
	}
	if len(closed) > 0 {
		t.Errorf("rls.LeakyIndexes lists indexes that are no longer reported; remove them and regenerate "+
			"docs/schema-baseline.md with %s=1:\n  %s", updateDocEnv, strings.Join(closed, "\n  "))
	}
	if len(changed) > 0 {
		t.Errorf("rls.LeakyIndexes differs from the catalog:\n  %s", strings.Join(changed, "\n  "))
	}
	if _, ok := reported["idx_ci_fts"]; ok {
		t.Error("idx_ci_fts exists again; migration 000191 dropped it, the full-text search reads search_document.search_vector")
	}
	for index := range reported {
		if strings.HasPrefix(index, "idx_attr_") {
			t.Errorf("attribute index %s is reported: idx_attr_* are built over reticora_attr_text (E-73)", index)
		}
	}

	// reticora_attr_text stays the only function of the schema that is
	// LEAKPROOF (000162, E-133); functions of extensions do not count.
	var leakproof []string
	if err = pool.QueryRow(ctx, `
		SELECT COALESCE(array_agg(p.oid::regprocedure::text ORDER BY 1), '{}')
		  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = 'public' AND p.proleakproof
		   AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                    WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')`).Scan(&leakproof); err != nil {
		t.Fatal(err)
	}
	if want := []string{"reticora_attr_text(jsonb,text)"}; !slices.Equal(leakproof, want) {
		t.Errorf("LEAKPROOF functions of schema public = %v, want %v (E-133: no further function LEAKPROOF)", leakproof, want)
	}
}

// TestLeakyIndexQueryReportsLeakyIndexes is the negative case of
// TestRLSIndexesAreLeakproof: in a transaction that is rolled back it
// creates indexes on ci (row level security) and on a table without row
// level security; the query reports exactly the ci indexes over a
// function, an operator and a conversion that are not leakproof and over an
// operator class without a leakproof operator, not the index over
// reticora_attr_text and texteq, and nothing of the table without row level
// security.
func TestLeakyIndexQueryReportsLeakyIndexes(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	defer pool.Close()
	requireMigrated(ctx, t, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`SET LOCAL lock_timeout = '30s'`,
		`CREATE INDEX wp836_probe_fts ON ci USING gin (to_tsvector('simple', name))`,
		`CREATE INDEX wp836_probe_arrow ON ci (organization_id, (attributes ->> 'wp836'))`,
		`CREATE INDEX wp836_probe_pred ON ci (organization_id) WHERE lower(name) = 'wp836'`,
		`CREATE INDEX wp836_probe_cast ON ci ((id::text))`,
		`CREATE INDEX wp836_probe_gin ON ci USING gin (attributes)`,
		`CREATE INDEX wp836_probe_attr ON ci (organization_id, reticora_attr_text(attributes, 'wp836'))
			WHERE deleted_at IS NULL AND reticora_attr_text(attributes, 'wp836') IS NOT NULL AND name = 'wp836'`,
		`CREATE TABLE wp836_probe_plain (doc jsonb)`,
		`CREATE INDEX wp836_probe_plain_arrow ON wp836_probe_plain ((doc ->> 'k'))`,
	} {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rows, err := tx.Query(ctx, rls.LeakyIndexQuery)
	if err != nil {
		t.Fatalf("leaky index query: %v", err)
	}
	got := map[string]string{}
	for rows.Next() {
		var table, index, leaky string
		if err = rows.Scan(&table, &index, &leaky); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(index, "wp836_probe") {
			got[index] = table + ": " + leaky
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"wp836_probe_fts":   "ci: function to_tsvector(regconfig,text), operator class tsvector_ops",
		"wp836_probe_arrow": "ci: function jsonb_object_field_text(jsonb,text)",
		"wp836_probe_pred":  "ci: function lower(text)",
		"wp836_probe_cast":  "ci: expression coerceviaio",
		"wp836_probe_gin":   "ci: operator class jsonb_ops",
	}
	if !maps.Equal(got, want) {
		t.Errorf("leaky index query reports %v, want %v", got, want)
	}
}

// TestLeakyIndexesList validates rls.LeakyIndexes without a database: every
// entry names its table, its findings and a reason, the table is created by
// a migration, and no attribute index (idx_attr_*, over the leakproof
// reticora_attr_text) and not idx_ci_fts (dropped by 000191) is listed.
func TestLeakyIndexesList(t *testing.T) {
	tables := tablesCreatedUpTo(t, math.MaxInt)
	for _, index := range slices.Sorted(maps.Keys(rls.LeakyIndexes)) {
		e := rls.LeakyIndexes[index]
		if e.Table == "" || e.Leaky == "" || e.Reason == "" {
			t.Errorf("rls.LeakyIndexes: %s: table, findings and reason are required", index)
		}
		if !tables[e.Table] {
			t.Errorf("rls.LeakyIndexes: %s: table %q is not created by a migration", index, e.Table)
		}
		if strings.HasPrefix(index, "idx_attr_") || index == "idx_ci_fts" {
			t.Errorf("rls.LeakyIndexes: %s must not be listed (E-73, migration 000191)", index)
		}
		for _, f := range strings.Split(e.Leaky, ", ") {
			if !strings.HasPrefix(f, "function ") && !strings.HasPrefix(f, "operator class ") && !strings.HasPrefix(f, "expression ") {
				t.Errorf("rls.LeakyIndexes: %s: finding %q is not one of rls.LeakyIndexQuery", index, f)
			}
		}
	}
}

// TestLeakyIndexesDocumented requires the table of the indexes under row
// level security in docs/schema-baseline.md to match rls.LeakyIndexes. Set
// RETICORA_UPDATE_SCHEMA_BASELINE=1 to regenerate it.
func TestLeakyIndexesDocumented(t *testing.T) {
	data, err := os.ReadFile(baselineDocPath)
	if err != nil {
		t.Fatalf("read docs/schema-baseline.md: %v", err)
	}
	text := string(data)
	before, rest, ok := strings.Cut(text, leakyBegin)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", leakyBegin)
	}
	current, after, ok := strings.Cut(rest, leakyEnd)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", leakyEnd)
	}
	lines := []string{"| Index | Tabelle | Nicht leakproof |", "|---|---|---|"}
	for _, index := range slices.Sorted(maps.Keys(rls.LeakyIndexes)) {
		e := rls.LeakyIndexes[index]
		lines = append(lines, fmt.Sprintf("| `%s` | `%s` | %s |", index, e.Table, e.Leaky))
	}
	want := "\n\n" + strings.Join(lines, "\n") + "\n\n"
	if os.Getenv(updateDocEnv) == "1" {
		text = before + leakyBegin + want + leakyEnd + after
		if err := os.WriteFile(baselineDocPath, []byte(text), 0o600); err != nil {
			t.Fatalf("write docs/schema-baseline.md: %v", err)
		}
		return
	}
	if current != want {
		t.Errorf("docs/schema-baseline.md: indexes under row level security differ from rls.LeakyIndexes; "+
			"regenerate with %s=1:\ndocumented:%s\nexpected:%s", updateDocEnv, current, want)
	}
}
