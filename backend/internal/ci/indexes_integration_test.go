package ci_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestCIIndexesFollowCI02 covers CI-02 (WP-121) against PostgreSQL: the
// migrated schema has every index CI-02 lists on ci, with its columns,
// predicate and operator class — not just an index of that name.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestCIIndexesFollowCI02(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)

	rows, err := admin.Query(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'ci'`)
	if err != nil {
		t.Fatal(err)
	}
	var defs []string
	for rows.Next() {
		var def string
		if err = rows.Scan(&def); err != nil {
			t.Fatal(err)
		}
		defs = append(defs, def)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	coalesce := func(col string) string { return `COALESCE\(` + col + `, ''::text\)` }
	for name, want := range map[string]string{
		"(org, type)":            `USING btree \(organization_id, ci_type_id\)$`,
		"(org, status)":          `USING btree \(organization_id, status\)$`,
		"(org, location_id)":     `USING btree \(organization_id, location_id\)`,
		"(org, asset_id)":        `USING btree \(organization_id, asset_id\)`,
		"(org, parent_ci_id)":    `USING btree \(organization_id, parent_ci_id\)`,
		"partial serial":         `USING btree \((organization_id, )?serial_number\) WHERE \(serial_number IS NOT NULL\)$`,
		"partial hardware_uuid":  `USING btree \((organization_id, )?hardware_uuid\) WHERE \(hardware_uuid IS NOT NULL\)$`,
		"partial management_ip":  `USING btree \((organization_id, )?management_ip\) WHERE \(management_ip IS NOT NULL\)$`,
		"GIN jsonb_path_ops":     `USING gin \(attributes jsonb_path_ops\)$`,
		"GIN full text (simple)": `USING gin \(to_tsvector\('simple'::regconfig, .*` + coalesce("name") + `.*` + coalesce("hostname") + `.*` + coalesce("manufacturer") + `.*` + coalesce("model") + `.*` + coalesce("serial_number"),
	} {
		re := regexp.MustCompile(want)
		found := false
		for _, def := range defs {
			if re.MatchString(def) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("index %s missing (want %s)", name, want)
		}
	}
}

// ciIndex is an index on ci as the catalog shows it.
type ciIndex struct {
	oid  uint32
	name string
	def  string
}

// indexesOnCI returns the number of indexes on ci.
func indexesOnCI(t *testing.T, admin *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_index WHERE indrelid = 'public.ci'::regclass`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// attrIndexesOf returns the indexes on ci over the value of the attribute
// key (reticora_attr_text(attributes, '<key>')), ordered by name.
func attrIndexesOf(t *testing.T, admin *pgxpool.Pool, key string) []ciIndex {
	t.Helper()
	rows, err := admin.Query(context.Background(), `
		SELECT c.oid, c.relname::text, pg_get_indexdef(c.oid) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = 'public.ci'::regclass AND strpos(pg_get_indexdef(c.oid), $1) > 0 ORDER BY 2`,
		attrValue(key))
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ciIndex, error) {
		var x ciIndex
		return x, r.Scan(&x.oid, &x.name, &x.def)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// attrValue is the value of the attribute key as the catalog and EXPLAIN
// print the expression of the attribute indexes (a key of [a-z0-9_]).
func attrValue(key string) string {
	return "reticora_attr_text(attributes, '" + key + "'::text)"
}

// keyIndexProblem returns why indexes are not the one index of key that
// E-73 (supplement of 2026-10-26) asks for, or "": exactly one index,
// (organization_id, reticora_attr_text(attributes, '<key>')) without a
// predicate on the organization, for CIs that are not soft-deleted and hold
// the attribute.
func keyIndexProblem(indexes []ciIndex, key string) string {
	if len(indexes) != 1 {
		defs := make([]string, 0, len(indexes))
		for _, x := range indexes {
			defs = append(defs, x.def)
		}
		return fmt.Sprintf("%d indexes over the key, want one:\n  %s", len(indexes), strings.Join(defs, "\n  "))
	}
	value := attrValue(key)
	def := indexes[0].def
	if !strings.Contains(def, "USING btree (organization_id, "+value+") WHERE ((deleted_at IS NULL) AND ("+value+" IS NOT NULL))") ||
		strings.Contains(def, "organization_id = ") || strings.Contains(def, "ci_type_id") || strings.Contains(def, "UNIQUE") {
		return "the index is not (organization_id, value) without a predicate on the organization: " + def
	}
	return ""
}

// TestAttributeIndexPerKey covers WP-607 (NFR-01, NFR-02; E-73, supplement
// of 2026-10-26; cause U1 of the load runs of WP-439): the searchable
// definitions of several organizations with the same key, not unique and
// without a CI type, share one index on ci. Four organizations (two
// fixtures) make the key searchable through the citype repository, the DDL
// job (Ensure) builds the indexes: the number of indexes on ci grows by one
// with the first organization and stays with the three others; the index
// leads with organization_id and has no predicate on the organization.
// Deleting the definitions one by one (the job of each deletion run
// directly) keeps the same index while a definition uses the key, also
// after the second to last one, and drops it with the last one. Before
// WP-607 the DDL job built one partial index per definition and
// organization (organization_id = '<org>' in the predicate): four indexes
// here, 528 instead of 28 on ci at the 500 organizations of NFR-01.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAttributeIndexPerKey' ./internal/ci/
func TestAttributeIndexPerKey(t *testing.T) {
	fixtures := []*scopetest.Fixture{scopetest.Seed(t, "cf"), scopetest.Seed(t, "cc")}
	bg := context.Background()
	admin := fixtures[0].Admin
	// A key of this run: deleting a fixture organization removes its
	// definitions without an index job, so the index of an earlier run's
	// key may still exist.
	key := fmt.Sprintf("wp607_cost_center_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, x := range attrIndexesOf(t, admin, key) {
			_, _ = admin.Exec(bg, `DROP INDEX IF EXISTS `+pgx.Identifier{x.name}.Sanitize())
		}
	})
	ddl := citype.NewDDLJob(fixtures[0].App, jobs.NewService(jobs.NewPGRepository(fixtures[0].App)))
	types := citype.NewPGRepository(fixtures[0].App)

	type orgDefinition struct {
		ctx context.Context
		org string
		def *citype.AttributeDefinition
	}
	before := indexesOnCI(t, admin)
	var defs []orgDefinition
	for _, f := range fixtures {
		for _, org := range []string{f.OrgA, f.OrgB} {
			ctx := tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
			d := &citype.AttributeDefinition{Key: key, DataType: "text", Searchable: true}
			if err := types.CreateAttributeDefinition(ctx, org, d); err != nil {
				t.Fatalf("create the definition in organization %s: %v", org, err)
			}
			// Two CIs with the attribute and one without.
			if _, err := admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, attributes)
				SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp607-' || i, 'active',
					CASE WHEN i < 3 THEN jsonb_build_object($2::text, 'CC-' || i) ELSE '{}' END
				FROM generate_series(1, 3) AS i`, org, key); err != nil {
				t.Fatalf("seed CIs: %v", err)
			}
			if err := ddl.Ensure(bg, org, d.ID); err != nil {
				t.Fatalf("index job of organization %s: %v", org, err)
			}
			defs = append(defs, orgDefinition{ctx, org, d})
			if got := indexesOnCI(t, admin); got != before+1 {
				t.Errorf("NFR-01/E-73: %d organizations make %s searchable: %d indexes on ci, want %d (one per key, not per organization)",
					len(defs), key, got, before+1)
			}
		}
	}
	indexes := attrIndexesOf(t, admin, key)
	if problem := keyIndexProblem(indexes, key); problem != "" {
		t.Fatalf("E-73: %s", problem)
	}
	shared := indexes[0]
	t.Logf("four organizations, one index: %s", shared.def)

	// Reference counting: the index goes with the last definition of the key.
	for i, d := range defs {
		if err := types.DeleteAttributeDefinition(d.ctx, d.org, d.def.ID); err != nil {
			t.Fatalf("delete the definition of organization %s: %v", d.org, err)
		}
		if err := ddl.Ensure(bg, d.org, d.def.ID); err != nil {
			t.Fatalf("index job after the deletion in organization %s: %v", d.org, err)
		}
		left := attrIndexesOf(t, admin, key)
		if i < len(defs)-1 {
			if len(left) != 1 || left[0].oid != shared.oid {
				t.Errorf("after deleting %d of %d definitions of %s: indexes %+v, want the same index %s (oid %d) as long as a definition uses the key",
					i+1, len(defs), key, left, shared.name, shared.oid)
			}
			continue
		}
		if n := indexesOnCI(t, admin); len(left) != 0 || n != before {
			t.Errorf("after deleting the last definition of %s: indexes %+v, %d indexes on ci; want none over the key and %d on ci",
				key, left, n, before)
		}
	}
}
