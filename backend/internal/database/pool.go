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

// PoolMaxConns is the size of the application pool NewPool creates; the
// ingest lanes take at most a quarter of it (WP-334).
const PoolMaxConns = 25

// NewPool creates a pgx connection pool suitable for production use.
//
// Every connection switches into the restricted application role before it is
// handed out, so RLS policies apply even when the configured login role is a
// superuser or the schema owner. If isolation cannot be guaranteed, pool
// creation fails instead of silently serving cross-tenant data. Every
// connection uses the UTF-8 client encoding the quoting of the tenant
// transaction relies on (pinClientEncoding); another one is refused.
//
// On top of that the runtime login is checked: the effective rights must
// match the role contract (VerifyRoleContract). cmd/server is the caller.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := newAppRolePool(ctx, databaseURL)
	if err != nil {
		return nil, err
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

// NewAdminPool creates a pool with the same configuration as NewPool (size,
// client encoding, SET ROLE into the application role, refusal when that
// role bypasses row level security) but without the check of the runtime
// login (VerifyRLSEnforced, VerifyRoleContract). It is only for command-line
// tools that log in with the migration/admin login (cmd/seed,
// cmd/audit-verify, cmd/audit-seed, TEN-03, WP-351); their tenant work still
// runs under RLS as the application role. The server never uses it: every
// caller is listed with a reason in adminPoolCallers (tenantguard_allowlist.go)
// and checked by TestAdminPoolCallersAreListed.
func NewAdminPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := newAppRolePool(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := verifyCurrentRoleEnforcesRLS(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// newAppRolePool is the shared part of NewPool and NewAdminPool: a pool whose
// connections switch into the application role; the callers check the
// effective rights.
func newAppRolePool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("database URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}

	config.MaxConns = PoolMaxConns
	config.MinConns = 5
	pinClientEncoding(config)

	role := appRole()
	if role != "" {
		if err := ValidateRoleName(role); err != nil {
			return nil, err
		}
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if encErr := checkClientEncoding(conn); encErr != nil {
			return encErr
		}
		if role == "" {
			return nil
		}
		if _, roleErr := conn.Exec(ctx, "SET ROLE "+quoteIdentifier(role)); roleErr != nil {
			return fmt.Errorf("switch to application role %q: %w", role, roleErr)
		}
		return nil
	}

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
	pinClientEncoding(config)
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
// exposes all tenants to each other. It is part of the runtime login check of
// NewPool; NewAdminPool only runs verifyCurrentRoleEnforcesRLS.
func VerifyRLSEnforced(ctx context.Context, pool *pgxpool.Pool) error {
	return verifyCurrentRoleEnforcesRLS(ctx, pool)
}

// verifyCurrentRoleEnforcesRLS fails when current_user, the role the
// connections switched into, is a superuser or has BYPASSRLS.
func verifyCurrentRoleEnforcesRLS(ctx context.Context, pool *pgxpool.Pool) error {
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

// clientEncoding is the client encoding of every pooled connection. The
// statement that opens a tenant transaction carries the user id as a string
// literal (beginTenantSQL), and PostgreSQL converts a statement from the
// client encoding before it reads the literal. Doubling backslashes and
// quotes is safe in UTF-8, where no byte of a multi-byte character is a
// backslash or a quote; in client-only encodings such as SJIS or BIG5 it is
// not (a trailing 0x5C byte swallows the next backslash).
const clientEncoding = "UTF8"

// pinClientEncoding makes the connections of config use clientEncoding,
// whatever the database URL or the role defaults set: a startup parameter
// overrides both.
func pinClientEncoding(config *pgxpool.Config) {
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["client_encoding"] = clientEncoding
}

// checkClientEncoding refuses a connection whose client encoding, as the
// server reported it at startup, is not clientEncoding.
func checkClientEncoding(conn *pgx.Conn) error {
	if enc := conn.PgConn().ParameterStatus("client_encoding"); enc != clientEncoding {
		return fmt.Errorf("refusing connection: client_encoding is %q, want %s", enc, clientEncoding)
	}
	return nil
}

// WithTenant is the single database entry point for tenant-scoped work
// (TEN-06). It validates the scope, opens a transaction, sets every GUC of
// TEN-04 transaction-locally, runs fn and commits on success. Because the
// GUCs are set with is_local = true, they end with the transaction and never
// leak to the next user of the pooled connection. An incomplete scope fails
// before any connection is used.
//
// When ctx carries a transaction (ContextWithTx), fn runs in it if scope is
// its scope (ErrTxScopeMismatch otherwise); the owner commits. A statement
// error that fn swallowed rolls the transaction back (ErrTxAborted). Before
// the commit the BeforeCommit writes run, after it the AfterCommit
// callbacks.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, scope *TenantScope, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if v := ctx.Value(txKey{}); v != nil {
		return joinTx(ctx, v, scope, fn)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{BeginQuery: beginTenantSQL(scope)})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	t := &tenantTx{Tx: tx, scope: *scope}
	defer func() {
		if t.state.CompareAndSwap(txOpen, txRolledBack) {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := fn(ctx, t); err != nil {
		return err
	}
	if t.aborted() {
		return ErrTxAborted
	}
	if err := t.runBeforeCommit(); err != nil {
		return err
	}
	if t.aborted() {
		return ErrTxAborted
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	t.state.Store(txCommitted)
	t.runAfterCommit(WithoutTx(ctx))
	return nil
}

// beginTenantSQL opens the transaction and sets every GUC of TEN-04
// transaction-locally in one round trip (NFR-03): a statement without
// arguments goes with the simple protocol, which runs both commands of the
// string. All values are set in every transaction, including the empty
// ones, so a session-level value set elsewhere on this connection can never
// widen the scope. The ids are validated UUIDs; the user id is quoted.
func beginTenantSQL(scope *TenantScope) string {
	return "BEGIN; SELECT set_config('" + OrgGUC + "', " + quoteLiteral(scope.OrgID) + ", true), " +
		"set_config('" + UserGUC + "', " + quoteLiteral(scope.UserID) + ", true), " +
		"set_config('" + ClientScopeGUC + "', " + quoteLiteral(scope.Clients.gucValue()) + ", true), " +
		"set_config('" + SiteScopeGUC + "', " + quoteLiteral(scope.Sites.gucValue()) + ", true), " +
		"set_config('" + TeamScopeGUC + "', " + quoteLiteral(scope.Teams.gucValue()) + ", true)"
}

// quoteLiteral renders s as an escape string constant: backslashes and
// single quotes are doubled, which means the same under every setting of
// standard_conforming_strings and backslash_quote in the UTF-8 client
// encoding the pools pin (pinClientEncoding). Validate keeps NUL bytes and
// invalid UTF-8 out of the values.
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "E'" + strings.ReplaceAll(s, `'`, `''`) + "'"
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
// operator tables (operator_audit, signup_request, scheduler_run) honor it.
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

// WithOrganizationSetup runs fn in the one transaction that creates the
// organization orgID (self-signup, WP-111): the operator flag is set, so the
// operator tables (signup_request, operator_audit) are reachable, and the
// tenant context is orgID organization-wide without a user, so the
// organization row and everything its creation seeds pass the tenant
// policies. No other organization is reachable.
func WithOrganizationSetup(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if strings.TrimSpace(orgID) == "" {
		return ErrIncompleteScope
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization setup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT
		set_config('`+OperatorGUC+`', 'on', true),
		set_config('`+OrgGUC+`', $1, true),
		set_config('`+UserGUC+`', '', true),
		set_config('`+ClientScopeGUC+`', '', true),
		set_config('`+SiteScopeGUC+`', '', true),
		set_config('`+TeamScopeGUC+`', '', true)`, orgID); err != nil {
		return fmt.Errorf("set organization setup context: %w", err)
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

// OrganizationPlans returns the plan of every organization, keyed by id
// (system read for the entitlement provisioning at startup).
func OrganizationPlans(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	plans := map[string]string{}
	err := WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, plan FROM organization`)
		if err != nil {
			return fmt.Errorf("list organization plans: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, plan string
			if err := rows.Scan(&id, &plan); err != nil {
				return fmt.Errorf("scan organization plan: %w", err)
			}
			plans[id] = plan
		}
		return rows.Err()
	})
	return plans, err
}
