// Package main provides the audit-seed CLI. It creates a tenant and appends a
// short, cryptographically valid audit hash chain via the production
// PGRecorder, so the nightly backup/restore test can verify that a restored
// database still passes audit.Verify. It is used by the DR/restore test and,
// until the demo/scale seed of WP-203 exists, by `make seed` (SIM-01). It is
// never deployed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

func main() {
	dsn := flag.String("database-url", os.Getenv("RETICORA_DATABASE_URL"),
		"PostgreSQL connection string (defaults to RETICORA_DATABASE_URL)")
	orgSlug := flag.String("org-slug", "restore-test", "slug of the tenant to create")
	count := flag.Int("entries", 5, "number of audit entries to append")
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "audit-seed: database URL is required")
		os.Exit(2)
	}
	ctx := context.Background()
	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-seed: connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	// The organization row is only visible under its own app.org_id, so the
	// id is drawn first and the tenant is created inside its own scope. The
	// restore test always starts from an empty database; an existing slug is
	// reported instead of being reused.
	var orgID string
	if err = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&orgID); err != nil {
		fmt.Fprintf(os.Stderr, "audit-seed: generate organization id: %v\n", err)
		os.Exit(1)
	}

	recorder := audit.NewPGRecorder()
	scope := database.OrgWideScope(orgID, "")
	err = database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		tag, insErr := tx.Exec(ctx, `
			INSERT INTO organization (id, name, slug, plan)
			VALUES ($1, $2, $3, 'enterprise')
			ON CONFLICT (slug) DO NOTHING`, orgID, "Restore Test Org", *orgSlug)
		if insErr != nil {
			return fmt.Errorf("create organization: %w", insErr)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("create organization: slug %q already exists", *orgSlug)
		}
		for i := 0; i < *count; i++ {
			if _, recErr := recorder.Record(ctx, tx, audit.Entry{
				OrganizationID: orgID,
				ActorType:      "system",
				Action:         "restore.test",
				ResourceType:   "audit",
				Changes:        map[string]interface{}{"seq": i},
			}); recErr != nil {
				return fmt.Errorf("record: %w", recErr)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-seed: %v\n", err)
		os.Exit(1)
	}
	// Print the org ID so the workflow can pass it to audit-verify.
	fmt.Println(orgID)
}
