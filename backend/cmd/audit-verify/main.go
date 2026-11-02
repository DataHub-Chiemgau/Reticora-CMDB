// Package main provides the audit-verify CLI. It walks the audit hash chain
// of one organization — or of every organization with -all — prints the
// first integrity violation of each chain (sequence gap, broken link or
// hash mismatch, AUD-04) and exits non-zero when a chain is broken — the
// offline counterpart of POST /api/v1/audit/verify required by the
// specification.
//
// Entries whose personal values the pii.backfill run sealed after they were
// chained (migration 000188) are verified with the digest of their original
// and their seal; with RETICORA_MASTER_KEY (and RETICORA_MASTER_KEY_PREVIOUS
// during a rotation) set, the original is also rebuilt from the vault and a
// value that differs from it breaks the chain. The output names how many
// sealed entries were not confirmed that way (docs/ops/workers.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

func main() {
	dsn := flag.String("database-url", os.Getenv("RETICORA_DATABASE_URL"),
		"PostgreSQL connection string (defaults to RETICORA_DATABASE_URL)")
	orgID := flag.String("org", "", "organization ID whose audit chain is verified")
	all := flag.Bool("all", false, "verify the audit chain of every organization")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "audit-verify: database URL is required (set RETICORA_DATABASE_URL or pass -database-url)")
		os.Exit(2)
	}
	if (*orgID == "") == !*all {
		fmt.Fprintln(os.Stderr, "audit-verify: pass either -org <organization-id> or -all")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := database.NewAdminPool(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-verify: connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()
	// The vault confirms sealed entries against their original.
	if key := os.Getenv("RETICORA_MASTER_KEY"); key != "" {
		enc, encErr := crypto.NewEnvelopeEncryptor(key, strings.Split(os.Getenv("RETICORA_MASTER_KEY_PREVIOUS"), ",")...)
		if encErr != nil {
			fmt.Fprintf(os.Stderr, "audit-verify: master key: %v\n", encErr)
			os.Exit(2)
		}
		pii.SetDefault(pii.NewVault(pool, enc))
	}

	orgs := []string{*orgID}
	if *all {
		if orgs, err = organizations(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "audit-verify: list organizations: %v\n", err)
			os.Exit(2)
		}
	}
	os.Exit(run(ctx, os.Stdout, orgs, func(ctx context.Context, org string) (audit.VerifyResult, error) {
		return audit.Verify(ctx, pool, org)
	}))
}

// run verifies each organization's chain and returns the exit code: 0 when
// every chain is intact, 1 when one is broken, 2 when one cannot be read.
func run(ctx context.Context, out io.Writer, orgs []string, verify func(context.Context, string) (audit.VerifyResult, error)) int {
	code := 0
	for _, org := range orgs {
		result, err := verify(ctx, org)
		switch {
		case err != nil:
			fmt.Fprintf(out, "audit chain of organization %s could not be verified: %v\n", org, err)
			code = 2
		case !result.Intact:
			fmt.Fprintf(out, "audit chain BROKEN for organization %s: entry %s (seq %d): %s (%d entries intact before it)\n",
				org, result.BrokenID, result.BrokenAt, result.BrokenReason, result.Checked)
			if code == 0 {
				code = 1
			}
		case result.Sealed > 0:
			fmt.Fprintf(out, "audit chain intact for organization %s (%d entries verified, %d of them sealed, "+
				"%d sealed entries not confirmed against the vault)\n", org, result.Checked, result.Sealed, result.Unconfirmed)
		default:
			fmt.Fprintf(out, "audit chain intact for organization %s (%d entries verified)\n", org, result.Checked)
		}
	}
	return code
}

// organizations lists every organization; the system flag only widens the
// read (E-08).
func organizations(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var ids []string
	err := database.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}
