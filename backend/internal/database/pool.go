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
// 000056 creates the role and grants it to the migrating user.
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

// WithTenant executes fn within a database transaction that sets the RLS
// session variables app.org_id and app.client_scope. The callback receives
// the transaction (pgx.Tx) for executing queries within the tenant scope.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, orgID string, clientScope string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set app.org_id: %w", err)
	}

	if clientScope != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.client_scope', $1, true)", clientScope); err != nil {
			return fmt.Errorf("set app.client_scope: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
