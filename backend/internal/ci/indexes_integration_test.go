package ci_test

import (
	"context"
	"os"
	"regexp"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
