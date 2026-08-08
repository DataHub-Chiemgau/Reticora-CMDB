// Package main provides the audit-verify CLI. It walks the audit hash chain
// for one organization, prints the first integrity violation and exits
// non-zero when the chain is broken — the offline counterpart of
// POST /api/v1/audit/verify required by the specification.
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
	orgID := flag.String("org", "", "organization ID whose audit chain is verified (required)")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "audit-verify: database URL is required (set RETICORA_DATABASE_URL or pass -database-url)")
		os.Exit(2)
	}
	if *orgID == "" {
		fmt.Fprintln(os.Stderr, "audit-verify: -org <organization-id> is required")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-verify: connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	result, err := audit.Verify(ctx, pool, *orgID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-verify: verification failed: %v\n", err)
		os.Exit(2)
	}

	if !result.Intact {
		fmt.Printf("audit chain BROKEN for organization %s: entry %s (position %d): %s (%d entries checked)\n",
			*orgID, result.BrokenID, result.BrokenAt, result.BrokenReason, result.Checked)
		os.Exit(1)
	}

	fmt.Printf("audit chain intact for organization %s (%d entries verified)\n", *orgID, result.Checked)
}
