package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// DefaultOwnerRole owns the tables of schema public (migration 000078). The
// application role may SET ROLE to it for runtime index DDL (CH19, DB-04) but
// does not inherit its privileges, so ordinary requests run without DDL
// rights. It is NOSUPERUSER/NOBYPASSRLS and every tenant table is FORCE ROW
// LEVEL SECURITY, so the policies bind the owner as well.
const DefaultOwnerRole = "reticora_owner"

// Querier is what VerifyRoleContract needs; *pgxpool.Pool, *pgx.Conn and
// pgx.Tx satisfy it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// RoleContractError lists every deviation of the effective database rights
// from the role contract of TEN-03/CH19.
type RoleContractError struct {
	Role       string
	Violations []string
}

func (e *RoleContractError) Error() string {
	return fmt.Sprintf("refusing to start: database role %q violates the role contract (TEN-03, CH19); "+
		"apply the migrations and do not grant the application roles further rights:\n  - %s",
		e.Role, strings.Join(e.Violations, "\n  - "))
}

// roleContractQuery returns one row per violation of the contract for
// current_user with owner role $1. It looks at effective rights: role
// memberships are followed transitively, privileges are checked with
// has_*_privilege (which includes inherited ones).
const roleContractQuery = `
WITH RECURSIVE reachable(oid, inherited) AS (
	SELECT r.oid, true FROM pg_roles r WHERE r.rolname = current_user
	UNION
	SELECT m.roleid, reachable.inherited AND m.inherit_option
	  FROM pg_auth_members m JOIN reachable ON m.member = reachable.oid
),
owner AS (
	SELECT r.oid, r.rolname FROM pg_roles r WHERE r.rolname = $1
),
tenant_tables AS (
	SELECT c.oid, c.oid::regclass::text AS name, c.relrowsecurity, c.relforcerowsecurity, c.relowner
	  FROM pg_class c
	 WHERE c.relnamespace = 'public'::regnamespace
	   AND c.relkind IN ('r', 'p')
	   AND (c.relname = 'organization' OR EXISTS (
	        SELECT 1 FROM pg_attribute a
	         WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
	   -- Tables the role cannot touch at all (hypertables behind a
	   -- security-barrier view, WP-040) are no attack surface.
	   AND has_table_privilege(current_user, c.oid, 'SELECT, INSERT, UPDATE, DELETE')
)
SELECT current_user::text, msg FROM (
SELECT format('role %s (reachable from %s%s) is %s', r.rolname, current_user,
              CASE WHEN reachable.inherited THEN ', inherited' ELSE ' via SET ROLE' END,
              concat_ws(' and ', CASE WHEN r.rolsuper THEN 'SUPERUSER' END, CASE WHEN r.rolbypassrls THEN 'BYPASSRLS' END))
  FROM reachable JOIN pg_roles r ON r.oid = reachable.oid
 WHERE r.rolsuper OR r.rolbypassrls
UNION ALL
SELECT format('owner role %s does not exist', $1::text)
 WHERE NOT EXISTS (SELECT 1 FROM owner)
UNION ALL
SELECT format('%s cannot SET ROLE %s for runtime index DDL', current_user, owner.rolname)
  FROM owner WHERE NOT pg_has_role(current_user, owner.oid, 'SET')
UNION ALL
SELECT format('%s inherits the privileges of %s; DDL rights must need an explicit SET ROLE', current_user, owner.rolname)
  FROM owner WHERE owner.rolname <> current_user AND pg_has_role(current_user, owner.oid, 'USAGE')
UNION ALL
SELECT format('%s has CREATE on schema public outside of %s', current_user, owner.rolname)
  FROM owner WHERE owner.rolname <> current_user AND has_schema_privilege(current_user, 'public', 'CREATE')
UNION ALL
SELECT format('owner role %s lacks CREATE on schema public', owner.rolname)
  FROM owner WHERE NOT has_schema_privilege(owner.oid, 'public', 'CREATE')
UNION ALL
SELECT format('tenant table %s has no ENABLE ROW LEVEL SECURITY', t.name)
  FROM tenant_tables t WHERE NOT t.relrowsecurity
UNION ALL
SELECT format('tenant table %s has no FORCE ROW LEVEL SECURITY', t.name)
  FROM tenant_tables t WHERE NOT t.relforcerowsecurity
UNION ALL
SELECT format('tenant table %s is owned by %s, not %s', t.name, pg_get_userbyid(t.relowner), owner.rolname)
  FROM tenant_tables t CROSS JOIN owner WHERE t.relowner <> owner.oid
) AS v(msg)
ORDER BY msg`

// VerifyRoleContract checks the effective rights of current_user against the
// role contract (TEN-03, CH19) and returns a *RoleContractError listing every
// deviation:
//
//   - no role reachable from current_user, inherited or by SET ROLE, is
//     SUPERUSER or BYPASSRLS;
//   - current_user can SET ROLE ownerRole but does not inherit it, and has no
//     CREATE on schema public of its own; ownerRole has it;
//   - every tenant table current_user can read or write (organization and
//     every table with organization_id) has ENABLE and FORCE ROW LEVEL
//     SECURITY and is owned by ownerRole.
func VerifyRoleContract(ctx context.Context, q Querier, ownerRole string) error {
	rows, err := q.Query(ctx, roleContractQuery, ownerRole)
	if err != nil {
		return fmt.Errorf("verify database role contract: %w", err)
	}
	defer rows.Close()
	contractErr := &RoleContractError{}
	for rows.Next() {
		var msg string
		if err := rows.Scan(&contractErr.Role, &msg); err != nil {
			return fmt.Errorf("verify database role contract: %w", err)
		}
		contractErr.Violations = append(contractErr.Violations, msg)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify database role contract: %w", err)
	}
	if len(contractErr.Violations) == 0 {
		return nil
	}
	return contractErr
}
