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

// TruncateGuardTrigger is the statement-level BEFORE TRUNCATE trigger every
// tenant table carries (migration 000144): TRUNCATE ignores row level
// security, and the trigger refuses it to the application and the owner role.
const TruncateGuardTrigger = "reticora_truncate_guard"

// RoleContractError lists every deviation of the effective database rights
// from the role contract of TEN-03/CH19. Role is current_user (the role the
// connections switched into), SessionUser the login of the connection.
type RoleContractError struct {
	Role        string
	SessionUser string
	Violations  []string
}

func (e *RoleContractError) Error() string {
	return fmt.Sprintf("refusing to start: database role %q (session user %q) violates the role contract (TEN-03, CH19); "+
		"apply the migrations, log in with a role that is neither SUPERUSER nor BYPASSRLS and reaches no such role, "+
		"and do not grant the application roles further rights:\n  - %s",
		e.Role, e.SessionUser, strings.Join(e.Violations, "\n  - "))
}

// escalationCTE lists, as escalation(msg), every role reachable from
// current_user or from session_user, inherited or by SET ROLE, that is
// SUPERUSER or BYPASSRLS. A session whose login is such a role, or reaches
// one, gets every right back with RESET ROLE or SET ROLE (F-057), whatever
// role the pool switched into. Role memberships are followed transitively.
const escalationCTE = `
reachable(start, oid, inherited) AS (
	SELECT r.rolname::text, r.oid, true FROM pg_roles r WHERE r.rolname IN (current_user, session_user)
	UNION
	SELECT reachable.start, m.roleid, reachable.inherited AND m.inherit_option
	  FROM pg_auth_members m JOIN reachable ON m.member = reachable.oid
),
escalation(msg) AS (
	SELECT CASE
	         WHEN reachable.start = current_user THEN
	           format('role %s (reachable from %s%s) is %s', r.rolname, reachable.start,
	                  CASE WHEN reachable.inherited THEN ', inherited' ELSE ' via SET ROLE' END, attrs.list)
	         WHEN r.rolname = reachable.start THEN
	           format('session user %s is %s', r.rolname, attrs.list)
	         ELSE
	           format('role %s (reachable from session user %s%s) is %s', r.rolname, reachable.start,
	                  CASE WHEN reachable.inherited THEN ', inherited' ELSE ' via SET ROLE' END, attrs.list)
	       END
	  FROM reachable
	  JOIN pg_roles r ON r.oid = reachable.oid
	 CROSS JOIN LATERAL (SELECT concat_ws(' and ', CASE WHEN r.rolsuper THEN 'SUPERUSER' END,
	                                      CASE WHEN r.rolbypassrls THEN 'BYPASSRLS' END)) AS attrs(list)
	 WHERE r.rolsuper OR r.rolbypassrls
)`

// escalationQuery returns one row per role of escalationCTE.
const escalationQuery = `WITH RECURSIVE` + escalationCTE + `
SELECT current_user::text, session_user::text, msg FROM escalation ORDER BY msg`

// roleContractQuery returns one row per violation of the contract for
// current_user and session_user with owner role $1. It looks at effective
// rights: role memberships are followed transitively, privileges are checked
// with has_*_privilege (which includes inherited ones).
const roleContractQuery = `WITH RECURSIVE` + escalationCTE + `,
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
),
-- Tenant tables current_user or the owner role could empty with TRUNCATE,
-- which ignores row level security; the hypertables behind the view barrier
-- belong here as well when one of them may truncate them.
truncatable AS (
	SELECT c.oid, c.oid::regclass::text AS name
	  FROM pg_class c
	 WHERE c.relnamespace = 'public'::regnamespace
	   AND c.relkind IN ('r', 'p')
	   AND (c.relname = 'organization' OR EXISTS (
	        SELECT 1 FROM pg_attribute a
	         WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
	   AND (has_table_privilege(current_user, c.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE')
	        OR EXISTS (SELECT 1 FROM owner WHERE has_table_privilege(owner.oid, c.oid, 'TRUNCATE')))
)
SELECT current_user::text, session_user::text, msg FROM (
SELECT msg FROM escalation
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
UNION ALL
SELECT format('tenant table %s has no enabled statement-level BEFORE TRUNCATE trigger %s', t.name, '` + TruncateGuardTrigger + `')
  FROM truncatable t
 WHERE NOT EXISTS (
       SELECT 1 FROM pg_trigger tg
        WHERE tg.tgrelid = t.oid
          AND tg.tgfoid = to_regprocedure('public.` + TruncateGuardTrigger + `()')
          AND tg.tgtype & 1 = 0 AND tg.tgtype & 2 = 2 AND tg.tgtype & 32 = 32 -- statement, BEFORE, TRUNCATE
          AND tg.tgenabled IN ('O', 'A'))
UNION ALL
SELECT format('session user %s may TRUNCATE tenant table %s; the guard only stops %s and %s',
              session_user, t.name, '` + DefaultAppRole + `', owner.rolname)
  FROM truncatable t CROSS JOIN owner
 WHERE session_user NOT IN ('` + DefaultAppRole + `', owner.rolname)
   AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = session_user AND r.rolsuper)
   AND has_table_privilege(session_user, t.oid, 'TRUNCATE')
) AS v(msg)
ORDER BY msg`

// VerifyRoleContract checks the effective rights of current_user and of the
// login (session_user) against the role contract (TEN-03, CH19) and returns a
// *RoleContractError listing every deviation:
//
//   - neither session_user nor current_user is SUPERUSER or BYPASSRLS, and no
//     role reachable from them, inherited or by SET ROLE, is;
//   - current_user can SET ROLE ownerRole but does not inherit it, and has no
//     CREATE on schema public of its own; ownerRole has it;
//   - every tenant table current_user can read or write (organization and
//     every table with organization_id) has ENABLE and FORCE ROW LEVEL
//     SECURITY and is owned by ownerRole;
//   - every tenant table current_user or ownerRole could truncate carries the
//     enabled TRUNCATE guard (TruncateGuardTrigger), and session_user, unless
//     it is one of the two roles the guard stops, has no TRUNCATE on any.
func VerifyRoleContract(ctx context.Context, q Querier, ownerRole string) error {
	return collectContractViolations(ctx, q, roleContractQuery, ownerRole)
}

// verifyLoginCannotEscalate fails with a *RoleContractError when the login
// (session_user) or current_user is SUPERUSER or BYPASSRLS or reaches such a
// role (escalationQuery).
func verifyLoginCannotEscalate(ctx context.Context, q Querier) error {
	return collectContractViolations(ctx, q, escalationQuery)
}

// collectContractViolations runs query, whose rows are (current_user,
// session_user, violation), and returns them as a *RoleContractError, or nil
// without rows.
func collectContractViolations(ctx context.Context, q Querier, query string, args ...any) error {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("verify database role contract: %w", err)
	}
	defer rows.Close()
	contractErr := &RoleContractError{}
	for rows.Next() {
		var msg string
		if err := rows.Scan(&contractErr.Role, &contractErr.SessionUser, &msg); err != nil {
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
