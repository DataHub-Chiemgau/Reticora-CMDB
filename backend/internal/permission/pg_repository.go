package permission

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct{ pool *pgxpool.Pool }

// NewPGRepository creates a PostgreSQL permission repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// ListPermissions reads the global permission catalogue (E-10) in the
// caller's tenant transaction like every other request path.
func (r *PGRepository) ListPermissions(ctx context.Context) ([]Permission, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return nil, database.ErrNoTenantScope
	}
	items := make([]Permission, 0)
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT key, resource, action, description FROM permission ORDER BY resource, action, key")
		if err != nil {
			return fmt.Errorf("list permissions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item Permission
			if err := rows.Scan(&item.Key, &item.Resource, &item.Action, &item.Description); err != nil {
				return fmt.Errorf("scan permission: %w", err)
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *PGRepository) ListRolePermissions(ctx context.Context, orgID, roleID string) ([]RolePermissionGrant, error) {
	items := make([]RolePermissionGrant, 0)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, role_id::text, permission_key, granted_at, COALESCE(granted_by::text, '')
			FROM role_permission
			WHERE organization_id = $1 AND role_id = $2
			ORDER BY permission_key`, orgID, roleID)
		if err != nil {
			return fmt.Errorf("list role permissions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item RolePermissionGrant
			if err := rows.Scan(&item.OrganizationID, &item.RoleID, &item.PermissionKey, &item.GrantedAt, &item.GrantedBy); err != nil {
				return fmt.Errorf("scan role permission: %w", err)
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// ReplaceRolePermissions implements Repository. The role grants
// role_permission and role.permissions (AccessGrants); keys that neither
// holds are added and must be held by caller organization-wide
// (checkWidening), otherwise nothing changes.
func (r *PGRepository) ReplaceRolePermissions(ctx context.Context, orgID, roleID, grantedBy string, caller *identity.Principal, keys []string) ([]RolePermissionGrant, error) {
	items := make([]RolePermissionGrant, 0)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var current []string
		err := tx.QueryRow(ctx, `
			SELECT ARRAY(SELECT rp.permission_key FROM role_permission rp
			              WHERE rp.role_id = ro.id AND rp.organization_id = ro.organization_id
			             UNION SELECT jsonb_array_elements_text(COALESCE(ro.permissions, '[]'::jsonb)))
			  FROM role ro WHERE ro.id = $1 AND ro.organization_id = $2`, roleID, orgID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRoleNotFound
		}
		if err != nil {
			return fmt.Errorf("check role: %w", err)
		}
		seen := make(map[string]struct{})
		for _, key := range keys {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			var known bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM permission WHERE key = $1)", key).Scan(&known); err != nil {
				return fmt.Errorf("check permission: %w", err)
			}
			if !known {
				return unknownPermission(key)
			}
		}
		if wideningErr := checkWidening(caller, current, keys); wideningErr != nil {
			return wideningErr
		}
		if _, err = tx.Exec(ctx, "DELETE FROM role_permission WHERE organization_id = $1 AND role_id = $2", orgID, roleID); err != nil {
			return fmt.Errorf("clear role permissions: %w", err)
		}
		for key := range seen {
			if _, err = tx.Exec(ctx, `
				INSERT INTO role_permission (organization_id, role_id, permission_key, granted_by)
				VALUES ($1, $2, $3, NULLIF($4, '')::uuid)`, orgID, roleID, key, grantedBy); err != nil {
				return fmt.Errorf("grant permission: %w", err)
			}
		}
		items, err = scanRolePermissions(ctx, tx, orgID, roleID)
		return err
	})
	return items, err
}

func (r *PGRepository) EffectivePermissions(ctx context.Context, orgID, userID string) ([]string, error) {
	set := make(map[string]struct{})
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT key FROM (
				SELECT rp.permission_key AS key
				FROM role_assignment ra
				JOIN role_permission rp ON rp.role_id = ra.role_id AND rp.organization_id = ra.organization_id
				WHERE ra.organization_id = $1 AND ra.user_id = $2
				UNION
				SELECT jsonb_array_elements_text(COALESCE(r.permissions, '[]'::jsonb)) AS key
				FROM role_assignment ra
				JOIN role r ON r.id = ra.role_id AND r.organization_id = ra.organization_id
				WHERE ra.organization_id = $1 AND ra.user_id = $2
				UNION
				SELECT jsonb_array_elements_text(COALESCE(cr.permissions, '[]'::jsonb)) AS key
				FROM user_custom_role ucr
				JOIN custom_role cr ON cr.id = ucr.custom_role_id
				WHERE cr.organization_id = $1 AND ucr.user_id = $2
			) effective
			WHERE key IN (SELECT key FROM permission)
			ORDER BY key`, orgID, userID)
		if err != nil {
			return fmt.Errorf("list effective permissions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return fmt.Errorf("scan effective permission: %w", err)
			}
			set[key] = struct{}{}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

// AccessGrants reads every role assignment (role_assignment with
// scope_client_id/scope_site_id/scope_team_id) and custom role assignment
// (user_custom_role with scope_type/scope_id) of the user. NULL scope columns
// mean org-wide. Assignments of a client, site or team scope without scope ID
// are invalid and grant nothing (fail-closed). A site assignment is confined
// to the client of its site (site.client_id, RBA-03, TEN-09); a site outside
// the organization, or of another client than the assignment's
// scope_client_id, grants nothing (fail-closed, E-08). A team assignment
// restricts the team dimension to its team (Grant.Teams, RBA-03, E-11); a
// custom role assignment of a team outside the organization grants nothing.
// Only permissions of the catalogue are returned.
func (r *PGRepository) AccessGrants(ctx context.Context, orgID, userID string) ([]identity.Grant, error) {
	grants := make([]identity.Grant, 0)
	// Login and refresh resolve the scope here, so no principal scope exists
	// yet: the user's own assignments are read org-wide on behalf of the
	// organization the session names (E-08).
	scope := database.OrgWideScope(orgID, userID)
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT client_id, site_id, team_id,
			       ARRAY(SELECT DISTINCT k FROM unnest(keys) AS k WHERE k IN (SELECT key FROM permission) ORDER BY k)
			FROM (
				SELECT COALESCE(s.client_id, ra.scope_client_id)::text AS client_id, ra.scope_site_id::text AS site_id,
				       ra.scope_team_id::text AS team_id,
				       ARRAY(
				           SELECT rp.permission_key FROM role_permission rp
				           WHERE rp.role_id = ra.role_id AND rp.organization_id = ra.organization_id
				           UNION
				           SELECT jsonb_array_elements_text(COALESCE(ro.permissions, '[]'::jsonb))
				       ) AS keys
				FROM role_assignment ra
				JOIN role ro ON ro.id = ra.role_id AND ro.organization_id = ra.organization_id
				LEFT JOIN site s ON s.id = ra.scope_site_id AND s.organization_id = ra.organization_id
				WHERE ra.organization_id = $1 AND ra.user_id = $2
				  -- A client-scope role (C of RBA-02) grants only within an
				  -- assigned client, a site-scope role only within a site.
				  AND (ro.scope <> 'client' OR ra.scope_client_id IS NOT NULL)
				  AND (ro.scope <> 'site' OR ra.scope_site_id IS NOT NULL)
				  -- A site grants within its client only.
				  AND (ra.scope_site_id IS NULL OR s.client_id = COALESCE(ra.scope_client_id, s.client_id))
				UNION ALL
				SELECT CASE WHEN ucr.scope_type = 'client' THEN ucr.scope_id::text
				            WHEN ucr.scope_type = 'site' THEN s.client_id::text END,
				       CASE WHEN ucr.scope_type = 'site' THEN ucr.scope_id::text END,
				       CASE WHEN ucr.scope_type = 'team' THEN ucr.scope_id::text END,
				       ARRAY(SELECT jsonb_array_elements_text(COALESCE(cr.permissions, '[]'::jsonb)))
				FROM user_custom_role ucr
				JOIN custom_role cr ON cr.id = ucr.custom_role_id
				LEFT JOIN site s ON ucr.scope_type = 'site' AND s.id = ucr.scope_id AND s.organization_id = cr.organization_id
				-- scope_id has no foreign key: a team must exist in the organization.
				LEFT JOIN team tm ON ucr.scope_type = 'team' AND tm.id = ucr.scope_id AND tm.organization_id = cr.organization_id
				WHERE cr.organization_id = $1 AND ucr.user_id = $2
				  AND (ucr.scope_type = 'organization' OR ucr.scope_id IS NOT NULL)
				  AND (ucr.scope_type <> 'site' OR s.id IS NOT NULL)
				  AND (ucr.scope_type <> 'team' OR tm.id IS NOT NULL)
			) assignments`, orgID, userID)
		if err != nil {
			return fmt.Errorf("list role assignments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var clientID, siteID, teamID *string
			var keys []string
			if err := rows.Scan(&clientID, &siteID, &teamID, &keys); err != nil {
				return fmt.Errorf("scan role assignment: %w", err)
			}
			grant := identity.Grant{Permissions: toPermissions(keys)}
			if clientID != nil {
				grant.Clients = []string{*clientID}
			}
			if siteID != nil {
				grant.Sites = []string{*siteID}
			}
			if teamID != nil {
				grant.Teams = []string{*teamID}
			}
			grants = append(grants, grant)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return grants, nil
}

// RoleGrants reads the permissions of the named standard roles of the
// organization (role_permission and role.permissions, limited to the
// catalogue) as one org-wide grant per role. It resolves the IdP roles of a
// session (RBA-02); roles valid only in a client or site scope are skipped.
func (r *PGRepository) RoleGrants(ctx context.Context, orgID string, roleNames []string) ([]identity.Grant, error) {
	grants := make([]identity.Grant, 0, len(roleNames))
	if len(roleNames) == 0 {
		return grants, nil
	}
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT ARRAY(
				SELECT DISTINCT k FROM (
					SELECT rp.permission_key AS k FROM role_permission rp
					WHERE rp.role_id = ro.id AND rp.organization_id = ro.organization_id
					UNION
					SELECT jsonb_array_elements_text(COALESCE(ro.permissions, '[]'::jsonb))
				) keys
				WHERE k IN (SELECT key FROM permission)
				ORDER BY k)
			FROM role ro
			WHERE ro.organization_id = $1 AND ro.is_builtin AND ro.scope = 'org' AND ro.name = ANY($2)
			ORDER BY ro.name`, orgID, roleNames)
		if err != nil {
			return fmt.Errorf("read standard roles: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var keys []string
			if err := rows.Scan(&keys); err != nil {
				return fmt.Errorf("scan standard role: %w", err)
			}
			grants = append(grants, identity.OrgWideGrant(toPermissions(keys)))
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return grants, nil
}

func (r *PGRepository) HasPermission(ctx context.Context, orgID, userID, key string) (bool, error) {
	keys, err := r.EffectivePermissions(ctx, orgID, userID)
	if err != nil {
		return false, err
	}
	for _, candidate := range keys {
		if candidate == key {
			return true, nil
		}
	}
	return false, nil
}

func scanRolePermissions(ctx context.Context, tx pgx.Tx, orgID, roleID string) ([]RolePermissionGrant, error) {
	rows, err := tx.Query(ctx, `
		SELECT organization_id::text, role_id::text, permission_key, granted_at, COALESCE(granted_by::text, '')
		FROM role_permission WHERE organization_id = $1 AND role_id = $2 ORDER BY permission_key`, orgID, roleID)
	if err != nil {
		return nil, fmt.Errorf("list role permissions: %w", err)
	}
	defer rows.Close()
	items := make([]RolePermissionGrant, 0)
	for rows.Next() {
		var item RolePermissionGrant
		if err := rows.Scan(&item.OrganizationID, &item.RoleID, &item.PermissionKey, &item.GrantedAt, &item.GrantedBy); err != nil {
			return nil, fmt.Errorf("scan role permission: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
