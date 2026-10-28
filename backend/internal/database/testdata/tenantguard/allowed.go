package tenantguard

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// What the tenant guard must not report: the pool handed to WithTenant,
// WithRequestTenant and WithSystem, the transaction they pass on, the
// methods of a pool that touch no table, and types of database/sql
// without a connection. The line marked "listed:" is a direct pool call
// that only the allowlist entry of this file in TestTenantGuardFixtures
// keeps from being reported.

type store struct {
	pool *pgxpool.Pool
}

// inTenant runs its SQL on the tenant transaction.
func (s *store) inTenant(ctx context.Context, orgID string) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, s.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM ci`)
		return err
	})
}

// inRequestTenant runs its SQL on the request's tenant transaction, with
// the transaction method taken as a value.
func (s *store) inRequestTenant(ctx context.Context, orgID string) error {
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := tx.QueryRow
		var n int
		return query(ctx, `SELECT count(*) FROM ci`).Scan(&n)
	})
}

// systemRead finds the organizations with the system flag.
func (s *store) systemRead(ctx context.Context) error {
	return database.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM organization`)
		if err != nil {
			return err
		}
		rows.Close()
		return nil
	})
}

// lifecycle touches no table.
func (s *store) lifecycle(ctx context.Context) (int32, error) {
	if err := s.pool.Ping(ctx); err != nil {
		return 0, err
	}
	n := s.pool.Stat().TotalConns()
	s.pool.Close()
	return n, nil
}

// lastLogin uses database/sql only for its null types (as user/pg_repository.go).
func lastLogin(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// listed is the direct access of a documented system path.
func (s *store) listed(ctx context.Context) error {
	var one int
	return s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one) // listed: call
}
