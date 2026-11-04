package permission

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Ceiling of narrowing a role definition (RBA-03, TEN-09, F-396): removing
// permissions from a role, or deleting it, takes them from every assignment
// of the role at once. Whoever may not revoke an assignment (DELETE
// /role-assignments/{id}: role:manage and every permission of the role in a
// scope covering the assignment's scope, WP-395) may not reach the same
// effect over the role definition. The assignments are those of users
// (role_assignment, user_custom_role) and of service accounts
// (service_account_role); an invitation grants nothing before it is accepted
// and becomes a role_assignment then.

// RoleNarrowingError refuses removing permissions from a role, or deleting
// it, that has an assignment the caller could not revoke (403). Missing are
// the permissions of the role, or role:manage, that the caller does not hold
// in a scope covering such an assignment.
type RoleNarrowingError struct {
	Missing []identity.Permission
	// Deletion marks the refusal of deleting the role (same rule, other
	// wording).
	Deletion bool
}

// validation: a fixed rule text with the missing permissions.
func (e *RoleNarrowingError) Error() string {
	keys := make([]string, len(e.Missing))
	for i, p := range e.Missing {
		keys[i] = string(p)
	}
	what := "removing permissions from the role"
	if e.Deletion {
		what = "deleting the role"
	}
	return what + " requires " + strings.Join(keys, ", ") + " in the scope of each of its assignments"
}

// CheckRoleNarrowing returns a RoleNarrowingError unless caller could revoke
// every assignment of the role roleID (standard or custom role): it must hold
// role:manage and every permission of perms (the role's permissions before
// the change) in a scope covering the scope of each assignment
// (identity.GrantCeiling, the rule of revoking an assignment). A site scope
// names the client of the site as the caller can read it (RLS); a site the
// caller cannot read keeps no client and then needs the permissions in all
// clients. An assignment of a client, site or team scope without scope ID is
// checked as organization-wide (fail-closed). The caller locks the role row
// (FOR UPDATE) before, so no assignment of the role is added concurrently:
// every insert of an assignment takes a key-share lock on it.
func CheckRoleNarrowing(ctx context.Context, tx pgx.Tx, orgID, roleID string, caller *identity.Principal, perms []identity.Permission, deletion bool) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT COALESCE(a.client_id, s.client_id::text), a.site_id, a.team_id
		  FROM (
		        SELECT ra.scope_client_id::text AS client_id, ra.scope_site_id::text AS site_id, ra.scope_team_id::text AS team_id
		          FROM role_assignment ra WHERE ra.organization_id = $1 AND ra.role_id = $2
		        UNION ALL
		        SELECT sar.scope_client_id::text, sar.scope_site_id::text, NULL
		          FROM service_account_role sar WHERE sar.organization_id = $1 AND sar.role_id = $2
		        UNION ALL
		        SELECT CASE WHEN ucr.scope_type = 'client' THEN ucr.scope_id::text END,
		               CASE WHEN ucr.scope_type = 'site' THEN ucr.scope_id::text END,
		               CASE WHEN ucr.scope_type = 'team' THEN ucr.scope_id::text END
		          FROM user_custom_role ucr WHERE ucr.organization_id = $1 AND ucr.custom_role_id = $2
		       ) a
		  LEFT JOIN site s ON s.id = a.site_id::uuid AND s.organization_id = $1`, orgID, roleID)
	if err != nil {
		return fmt.Errorf("read role assignments: %w", err)
	}
	var targets []identity.Grant
	for rows.Next() {
		var clientID, siteID, teamID *string
		if err := rows.Scan(&clientID, &siteID, &teamID); err != nil {
			rows.Close()
			return fmt.Errorf("scan role assignment: %w", err)
		}
		target := identity.Grant{Permissions: slices.Clone(perms)}
		if !slices.Contains(target.Permissions, identity.PermRoleManage) {
			target.Permissions = append(target.Permissions, identity.PermRoleManage)
		}
		if clientID != nil {
			target.Clients = []string{*clientID}
		}
		if siteID != nil {
			target.Sites = []string{*siteID}
		}
		if teamID != nil {
			target.Teams = []string{*teamID}
		}
		targets = append(targets, target)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate role assignments: %w", err)
	}
	var missing []identity.Permission
	for i := range targets {
		// One assignment at a time: the union of several would widen the
		// scope each of them needs.
		for _, perm := range identity.GrantCeiling(caller, []identity.Grant{targets[i]}) {
			if !slices.Contains(missing, perm) {
				missing = append(missing, perm)
			}
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return &RoleNarrowingError{Missing: missing, Deletion: deletion}
	}
	return nil
}
