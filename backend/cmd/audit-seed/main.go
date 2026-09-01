// Package main provides the audit-seed CLI. It creates a tenant and appends a
// short, cryptographically valid audit hash chain via the production
// PGRecorder, so the nightly backup/restore test can verify that a restored
// database still passes audit.Verify. It exists only for the DR/restore test
// and is never deployed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

	var orgID string
	err = pool.QueryRow(ctx, `
		INSERT INTO organization (name, slug, plan)
		VALUES ($1, $2, 'enterprise')
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id::text
	`, "Restore Test Org", *orgSlug).Scan(&orgID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-seed: create organization: %v\n", err)
		os.Exit(1)
	}

	recorder := audit.NewPGRecorder()
	tx, err := pool.Begin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-seed: begin: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback(ctx)
	for i := 0; i < *count; i++ {
		if _, err := recorder.Record(ctx, tx, audit.Entry{
			OrganizationID: orgID,
			ActorType:      "system",
			Action:         "restore.test",
			ResourceType:   "audit",
			Changes:        map[string]interface{}{"seq": i},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "audit-seed: record: %v\n", err)
			os.Exit(1)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "audit-seed: commit: %v\n", err)
		os.Exit(1)
	}
	// Print the org ID so the workflow can pass it to audit-verify.
	fmt.Println(orgID)
}
