package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BulkAnalyzeTables are the tables AnalyzeAfterBulk analyzes: those a mass
// creation of CIs grows and whose statistics the plans of the ingest and of
// the lists depend on (WP-834, SR-08).
var BulkAnalyzeTables = []string{"ci", "ci_field_value", "search_document", "event_outbox", "audit_log"}

// BulkAnalyzeInterval is how often AnalyzeAfterBulk analyzes a table at
// most, across the cluster: a table analyzed more recently (by it, by an
// operator or by autovacuum) is left out.
const BulkAnalyzeInterval = 10 * time.Minute

// bulkAnalyzeLockClass and bulkAnalyzeLockKey name the session advisory
// lock that keeps runs of AnalyzeAfterBulk from overlapping. The class is
// apart from those of the audit chain (0x52657469), the event outbox
// (0x45766e74), its publisher (0x45766e75) and the ingest locks.
const (
	bulkAnalyzeLockClass int32 = 0x52416e7a // "RAnz"
	bulkAnalyzeLockKey   int32 = 0
)

// AnalyzeAfterBulk refreshes the planner statistics after a mass creation
// (seed, onboarding, an ingest window with many new CIs) with
// BulkAnalyzeInterval; see AnalyzeAfterBulkEvery.
func AnalyzeAfterBulk(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	return AnalyzeAfterBulkEvery(ctx, pool, BulkAnalyzeInterval)
}

// AnalyzeAfterBulkEvery runs ANALYZE on each table of BulkAnalyzeTables
// that was not analyzed within interval (pg_stat_user_tables.last_analyze
// and last_autoanalyze) and returns the tables it analyzed. Until
// autovacuum catches up after a mass creation, the plans read stale
// statistics and scan the whole organization (WP-834, SR-08).
//
// The application role does not own the tables and may not analyze them:
// the run takes a connection of its own, switches to DefaultOwnerRole
// (migration 000078, like the attribute index job) and closes the
// connection afterwards, so the role switch never returns to the pool.
// Runs never overlap: a run that does not get the session advisory lock
// returns at once without analyzing. Call it after the commit
// (AfterCommit), never inside a transaction that holds locks.
func AnalyzeAfterBulkEvery(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("analyze: acquire connection: %w", err)
	}
	pc := conn.Hijack()
	defer func() { _ = pc.Close(context.WithoutCancel(ctx)) }()
	if _, err = pc.Exec(ctx, "SET ROLE "+pgx.Identifier{DefaultOwnerRole}.Sanitize()); err != nil {
		return nil, fmt.Errorf("analyze: switch to the owner role: %w", err)
	}
	var locked bool
	if err = pc.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, bulkAnalyzeLockClass, bulkAnalyzeLockKey).Scan(&locked); err != nil {
		return nil, fmt.Errorf("analyze: lock: %w", err)
	}
	if !locked {
		return nil, nil // another run analyzes
	}
	rows, err := pc.Query(ctx, `SELECT relname::text FROM pg_stat_user_tables
		WHERE schemaname = 'public' AND relname = ANY($1::text[])
		AND COALESCE(GREATEST(last_analyze, last_autoanalyze), '-infinity') < now() - $2::interval
		ORDER BY array_position($1::text[], relname::text)`, BulkAnalyzeTables, interval)
	if err != nil {
		return nil, fmt.Errorf("analyze: read the last analysis: %w", err)
	}
	due, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("analyze: read the last analysis: %w", err)
	}
	var done []string
	for _, table := range due {
		if _, err = pc.Exec(ctx, "ANALYZE "+pgx.Identifier{table}.Sanitize()); err != nil {
			return done, fmt.Errorf("analyze %s: %w", table, err)
		}
		done = append(done, table)
	}
	return done, nil
}
