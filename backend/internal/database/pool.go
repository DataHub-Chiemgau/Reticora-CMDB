package database

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultAppRole is the NOSUPERUSER/NOBYPASSRLS role that every pooled
// connection switches into. Row Level Security is silently bypassed for
// superusers, BYPASSRLS roles and (without FORCE) table owners, so connecting
// as the database owner would disable every tenant-isolation policy. Migration
// 000056 creates the role and grants it to the migrating user; migration
// 000078 lets it SET ROLE to DefaultOwnerRole for runtime index DDL.
const DefaultAppRole = "reticora_app"

// AppRoleEnv overrides the role name. Set it to an empty value only when the
// login role itself is already restricted; enforcement is verified at startup
// either way.
const AppRoleEnv = "RETICORA_DB_APP_ROLE"

// appRole resolves the role every pooled connection switches into.
func appRole() string {
	if v, ok := os.LookupEnv(AppRoleEnv); ok {
		return strings.TrimSpace(v)
	}
	return DefaultAppRole
}

// NewPool creates a pgx connection pool suitable for production use.
//
// Every connection switches into the restricted application role before it is
// handed out, so RLS policies apply even when the configured login role is a
// superuser or the schema owner. If isolation cannot be guaranteed, pool
// creation fails instead of silently serving cross-tenant data.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("database URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}

	config.MaxConns = 25
	config.MinConns = 5

	if role := appRole(); role != "" {
		if err := ValidateRoleName(role); err != nil {
			return nil, err
		}
		config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			if _, err := conn.Exec(ctx, "SET ROLE "+quoteIdentifier(role)); err != nil {
				return fmt.Errorf("switch to application role %q: %w", role, err)
			}
			return nil
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := VerifyRLSEnforced(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	// The effective rights, including inherited ones, must match the role
	// contract (TEN-03, CH19); any deviation refuses the start.
	if err := VerifyRoleContract(ctx, pool, DefaultOwnerRole); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

// NewMaintenancePool creates a pool that keeps the privileges of the configured
// login role: it neither switches into the restricted application role nor
// requires that row level security is enforced. It exists for schema
// maintenance (migrations, fixtures, supervised corrections) and must never be
// used to serve API traffic, because RLS would not apply to it.
func NewMaintenancePool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}
	config.MaxConns = 5
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// VerifyRLSEnforced fails when the effective database role can bypass Row Level
// Security. Tenant isolation in Reticora is enforced by RLS policies keyed on
// app.org_id; a superuser or BYPASSRLS role turns every policy into a no-op and
// exposes all tenants to each other.
func VerifyRLSEnforced(ctx context.Context, pool *pgxpool.Pool) error {
	var current string
	var super, bypass bool
	err := pool.QueryRow(ctx, `
		SELECT current_user,
		       COALESCE(rolsuper, false),
		       COALESCE(rolbypassrls, false)
		FROM pg_roles WHERE rolname = current_user`).Scan(&current, &super, &bypass)
	if err != nil {
		return fmt.Errorf("verify row level security enforcement: %w", err)
	}
	if super || bypass {
		return fmt.Errorf(
			"refusing to start: database role %q bypasses row level security (superuser=%t, bypassrls=%t) "+
				"and tenant isolation would be disabled; apply the migrations so the %q role exists and connect "+
				"with a role that is a member of it, or point %s at a NOSUPERUSER/NOBYPASSRLS role",
			current, super, bypass, DefaultAppRole, AppRoleEnv)
	}
	return nil
}

// ValidateRoleName rejects role names that are not plain identifiers so the
// SET ROLE statement can never be abused for injection.
func ValidateRoleName(role string) error {
	for _, r := range role {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf("invalid %s value %q: only letters, digits and underscore are allowed", AppRoleEnv, role)
		}
	}
	return nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// WithTenant is the single database entry point for tenant-scoped work
// (TEN-06). It validates the scope, opens a transaction, sets every GUC of
// TEN-04 transaction-locally, runs fn and commits on success. Because the
// GUCs are set with is_local = true, they end with the transaction and never
// leak to the next user of the pooled connection. An incomplete scope fails
// before any connection is used.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, scope *TenantScope, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// All values are set in every transaction, including the empty ones, so a
	// session-level value set elsewhere on this connection can never widen
	// the scope.
	if _, err := tx.Exec(ctx, `SELECT
		set_config('`+OrgGUC+`', $1, true),
		set_config('`+UserGUC+`', $2, true),
		set_config('`+ClientScopeGUC+`', $3, true),
		set_config('`+SiteScopeGUC+`', $4, true),
		set_config('`+TeamScopeGUC+`', $5, true)`,
		scope.OrgID, scope.UserID,
		scope.Clients.gucValue(), scope.Sites.gucValue(), scope.Teams.gucValue(),
	); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SystemGUC is the flag WithSystem sets. Only SELECT policies honor it
// (migration 000060): a system transaction can find rows across tenants but
// change none of them.
const SystemGUC = "app.system"

// WithSystem runs fn in a read-only transaction with the system flag and no
// tenant: the organization and the client, site and team scope are the nil
// UUID, which matches no row and keeps the strict ::uuid casts of the tenant
// policies valid, so
// only the SELECT-only system exceptions (organization, webhook_delivery,
// webhook_dead_letter, export_job, alert_rule, collector_enrollment_code,
// api_key)
// return rows. Workers use it to find due work and then change rows per
// organization in WithTenant (E-08). Every caller is listed in the
// allow-list of the architecture test (WP-041).
func WithSystem(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin system transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT
		set_config('`+SystemGUC+`', 'on', true),
		set_config('`+OrgGUC+`', $1, true),
		set_config('`+UserGUC+`', '', true),
		set_config('`+ClientScopeGUC+`', $1, true),
		set_config('`+SiteScopeGUC+`', $1, true),
		set_config('`+TeamScopeGUC+`', $1, true)`, noAccessScope); err != nil {
		return fmt.Errorf("set system context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OperatorGUC is the flag WithOperator sets. Only the policies of the
// operator tables (operator_audit, migration 000083) honor it.
const OperatorGUC = "app.operator"

// WithOperator runs fn in a transaction of the operator path (/admin, SEC-07):
// the operator flag is set and no tenant is, so tenant tables return no rows
// and accept no writes; only the operator tables are reachable. Organization
// data an operator reads goes through WithSystem or WithTenant of the target
// organization.
func WithOperator(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin operator transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT
		set_config('`+OperatorGUC+`', 'on', true),
		set_config('`+OrgGUC+`', $1, true),
		set_config('`+UserGUC+`', '', true),
		set_config('`+ClientScopeGUC+`', $1, true),
		set_config('`+SiteScopeGUC+`', $1, true),
		set_config('`+TeamScopeGUC+`', $1, true)`, noAccessScope); err != nil {
		return fmt.Errorf("set operator context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OrganizationIDs lists the ids of all organizations for workers that iterate
// tenants. It is a system read (WithSystem).
func OrganizationIDs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var ids []string
	err := WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return fmt.Errorf("list organizations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan organization: %w", err)
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}
