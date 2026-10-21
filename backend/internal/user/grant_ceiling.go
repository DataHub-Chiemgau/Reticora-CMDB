package user

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Grant ceiling of invitations and role assignments (RBA-03, TLC-03, F-015):
// nobody hands out a role in a scope beyond their own rights. Issuing or
// renewing an invitation (user:manage) and assigning a role (role:manage,
// POST /role-assignments and the sunset path /roles/assign) need every
// permission of the role, and the permission of the route, in a scope that
// covers the target scope. The coverage rule is identity.GrantCeiling; this
// file builds the target grant and resolves the client of a site. A role
// fits its scope (F-305): a client role is assigned in a client, a site role
// in a site, an organization role in any scope.

// GrantCeilingError refuses an invitation or role assignment beyond the
// caller's rights (403). Missing are the permissions of the role, or of the
// route, that the caller does not hold in a scope covering the target scope.
type GrantCeilingError struct {
	Missing []identity.Permission
}

func (e *GrantCeilingError) Error() string {
	return "granting the role in this scope requires " + joinPermissions(e.Missing) + " in that scope"
}

// RoleWideningError refuses a change of a role definition that adds
// permissions the caller does not hold organization-wide (403, F-015): the
// definition applies to every assignment of the role, org-wide ones too.
type RoleWideningError struct {
	Missing []identity.Permission
}

func (e *RoleWideningError) Error() string {
	return "adding " + joinPermissions(e.Missing) + " to the role requires holding them organization-wide"
}

func joinPermissions(perms []identity.Permission) string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return strings.Join(out, ", ")
}

// Errors of a role assignment that answer 404: the role, the user or the team
// of the scope is not one of the organization. The text is the detail.
var (
	errRoleNotFound = errors.New("role not found")
	errUserNotFound = errors.New("user not found")
	errTeamNotFound = errors.New("team not found")
)

// targetRole is the role an invitation or assignment hands out: its scope
// class (role.scope org, client or site; a custom role is an organization
// role) and its catalog permissions as permission.AccessGrants resolves them.
type targetRole struct {
	Name        string
	Scope       string
	Permissions []identity.Permission
}

// readStandardRole reads a standard role (table role) of the organization;
// found is false when there is none.
func readStandardRole(ctx context.Context, tx pgx.Tx, orgID, roleID string) (role *targetRole, found bool, err error) {
	if !isUUID(roleID) {
		return nil, false, nil
	}
	role = &targetRole{}
	var keys []string
	err = tx.QueryRow(ctx, `
		SELECT ro.name, ro.scope, ARRAY(SELECT DISTINCT k FROM (
		           SELECT rp.permission_key AS k FROM role_permission rp
		            WHERE rp.role_id = ro.id AND rp.organization_id = ro.organization_id
		           UNION SELECT jsonb_array_elements_text(COALESCE(ro.permissions, '[]'::jsonb))) keys
		         WHERE k IN (SELECT key FROM permission) ORDER BY k)
		  FROM role ro WHERE ro.organization_id = $1 AND ro.id = $2`, orgID, roleID).Scan(&role.Name, &role.Scope, &keys)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read role: %w", err)
	}
	role.Permissions = toPermissions(keys)
	return role, true, nil
}

// readCustomRole reads a custom role of the organization; found is false when
// there is none.
func readCustomRole(ctx context.Context, tx pgx.Tx, orgID, roleID string) (role *targetRole, found bool, err error) {
	if !isUUID(roleID) {
		return nil, false, nil
	}
	role = &targetRole{Scope: "org"}
	var keys []string
	err = tx.QueryRow(ctx, `
		SELECT cr.name, ARRAY(SELECT DISTINCT k FROM jsonb_array_elements_text(COALESCE(cr.permissions, '[]'::jsonb)) AS k
		         WHERE k IN (SELECT key FROM permission) ORDER BY k)
		  FROM custom_role cr WHERE cr.organization_id = $1 AND cr.id = $2`, orgID, roleID).Scan(&role.Name, &keys)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read custom role: %w", err)
	}
	role.Permissions = toPermissions(keys)
	return role, true, nil
}

func toPermissions(keys []string) []identity.Permission {
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}
	return perms
}

// siteClient returns the client of a site of the organization that the caller
// can read (RLS); "" when there is none.
func siteClient(ctx context.Context, tx pgx.Tx, orgID, siteID string) (string, error) {
	if !isUUID(siteID) {
		return "", nil
	}
	var client string
	err := tx.QueryRow(ctx, `SELECT client_id::text FROM site WHERE organization_id = $1 AND id = $2`, orgID, siteID).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read site client: %w", err)
	}
	return client, nil
}

// checkRoleScope matches the role with the scope (F-305): a client role
// (role.scope client) needs scope_type client, a site role scope_type site; an
// organization role fits every scope. Otherwise 422 scope_type: a client role
// without client would grant nothing (permission.AccessGrants).
func checkRoleScope(roleScope, scopeType string) error {
	if (roleScope == "client" || roleScope == "site") && scopeType != roleScope {
		return &api.FieldError{Field: "scope_type", Status: 422,
			Message: "the role is a " + roleScope + " role and is granted with scope_type " + roleScope + " only"}
	}
	return nil
}

// checkGrantCeiling is the grant ceiling of an invitation (route user:manage)
// or role assignment (route role:manage). A site scope names the client of
// the site (site.client_id), read in the caller's RLS scope like the ceiling
// of service account roles; a site the caller cannot read keeps no client and
// then needs the permissions in all clients.
func checkGrantCeiling(ctx context.Context, tx pgx.Tx, orgID string, caller *identity.Principal, route identity.Permission,
	perms []identity.Permission, scopeType, scopeID string) error {
	var client string
	if scopeType == "site" {
		var err error
		if client, err = siteClient(ctx, tx, orgID, scopeID); err != nil {
			return err
		}
	}
	return grantCeiling(caller, route, perms, scopeType, scopeID, client)
}

// grantCeiling returns a GrantCeilingError unless caller holds route and every
// permission of perms in a scope covering the target scope
// (identity.GrantCeiling): the organization (org, organization), a client, a
// site with siteClient as its client ("" when unknown), or a team. A scope
// without ID or of an unknown type is checked as organization-wide
// (fail-closed). A nil caller holds nothing.
func grantCeiling(caller *identity.Principal, route identity.Permission, perms []identity.Permission, scopeType, scopeID, siteClient string) error {
	target := identity.Grant{Permissions: slices.Clone(perms)}
	if !slices.Contains(target.Permissions, route) {
		target.Permissions = append(target.Permissions, route)
	}
	if scopeID != "" {
		switch scopeType {
		case "client":
			target.Clients = []string{scopeID}
		case "site":
			target.Sites = []string{scopeID}
			if siteClient != "" {
				target.Clients = []string{siteClient}
			}
		case "team":
			target.Teams = []string{scopeID}
		}
	}
	if missing := identity.GrantCeiling(caller, []identity.Grant{target}); len(missing) > 0 {
		return &GrantCeilingError{Missing: missing}
	}
	return nil
}

// checkScopeTarget requires the client or site of a scope to be one of the
// organization that the caller can read (RLS), 422 scope_id otherwise. Other
// scope types pass; the team of an assignment is checked by checkTeamScope.
func checkScopeTarget(ctx context.Context, tx pgx.Tx, orgID, scopeType, scopeID string) error {
	var exists bool
	switch scopeType {
	case "client":
		if isUUID(scopeID) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client WHERE id = $1 AND organization_id = $2)`,
				scopeID, orgID).Scan(&exists); err != nil {
				return fmt.Errorf("check client of the scope: %w", err)
			}
		}
	case "site":
		client, err := siteClient(ctx, tx, orgID, scopeID)
		if err != nil {
			return err
		}
		exists = client != ""
	default:
		return nil
	}
	if !exists {
		return &api.FieldError{Field: "scope_id", Message: scopeType + " not found", Status: 422}
	}
	return nil
}

// validateAssignmentScope checks scope_type and scope_id of a role assignment
// (422): an organization-wide assignment names no scope_id, a client or site
// scope names its client or site; without one it would be stored org-wide.
// The team of a team scope is checked against the organization
// (checkTeamScope, 404).
func validateAssignmentScope(a *UserRoleAssignment) error {
	switch a.ScopeType {
	case "organization":
		if a.ScopeID != "" {
			return &api.FieldError{Field: "scope_id", Message: "an organization-wide assignment has no scope_id", Status: 422}
		}
	case "client", "site":
		if !isUUID(a.ScopeID) {
			return &api.FieldError{Field: "scope_id", Message: "scope_id must name the " + a.ScopeType, Status: 422}
		}
	case "team":
	default:
		return &api.FieldError{Field: "scope_type", Message: "scope_type must be organization, client, site or team", Status: 422}
	}
	return nil
}

// checkAssignmentTarget checks a role assignment before anything is stored
// and before the MFA binding of an org_admin grant (WP-358): the role fits
// the scope (422 scope_type), the caller holds role:manage and the role's
// permissions in a scope covering it (403), and the client or site (422
// scope_id) or team (404) is one of the organization.
func checkAssignmentTarget(ctx context.Context, tx pgx.Tx, orgID string, caller *identity.Principal, role *targetRole, a *UserRoleAssignment) error {
	if err := checkRoleScope(role.Scope, a.ScopeType); err != nil {
		return err
	}
	if err := checkGrantCeiling(ctx, tx, orgID, caller, identity.PermRoleManage, role.Permissions, a.ScopeType, a.ScopeID); err != nil {
		return err
	}
	if err := checkScopeTarget(ctx, tx, orgID, a.ScopeType, a.ScopeID); err != nil {
		return err
	}
	return checkTeamScope(ctx, tx, orgID, a)
}

// checkRoleWidening is the ceiling of a role definition change (F-015): every
// permission next adds to current must be held organization-wide
// (identity.GrantCeiling with an org-wide target). Removing permissions is not
// limited.
func checkRoleWidening(caller *identity.Principal, current, next []string) error {
	var added []identity.Permission
	for _, key := range next {
		if p := identity.Permission(key); !slices.Contains(current, key) && !slices.Contains(added, p) {
			added = append(added, p)
		}
	}
	if len(added) == 0 {
		return nil
	}
	if missing := identity.GrantCeiling(caller, []identity.Grant{identity.OrgWideGrant(added)}); len(missing) > 0 {
		return &RoleWideningError{Missing: missing}
	}
	return nil
}
