package permission

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct{ pool *pgxpool.Pool }

// NewPGRepository creates a PostgreSQL permission repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PGRepository) ListPermissions() ([]Permission, error) {
	rows, err := r.pool.Query(context.Background(), "SELECT key, resource, action, description FROM permission ORDER BY resource, action, key")
	if err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}
	defer rows.Close()
	var items []Permission
	for rows.Next() {
		var item Permission
		if err := rows.Scan(&item.Key, &item.Resource, &item.Action, &item.Description); err != nil {
			return nil, fmt.Errorf("scan permission: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PGRepository) ListRolePermissions(orgID, roleID string) ([]RolePermissionGrant, error) {
	ctx := context.Background()
	var items []RolePermissionGrant
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

func (r *PGRepository) ReplaceRolePermissions(orgID, roleID, grantedBy string, keys []string) ([]RolePermissionGrant, error) {
	ctx := context.Background()
	var items []RolePermissionGrant
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM role WHERE id = $1 AND organization_id = $2)", roleID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("check role: %w", err)
		}
		if !exists {
			return fmt.Errorf("role not found")
		}
		seen := make(map[string]struct{})
		for _, key := range keys {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			var known bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM permission WHERE key = $1)", key).Scan(&known); err != nil {
				return fmt.Errorf("check permission: %w", err)
			}
			if !known {
				return fmt.Errorf("unknown permission %q", key)
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM role_permission WHERE organization_id = $1 AND role_id = $2", orgID, roleID); err != nil {
			return fmt.Errorf("clear role permissions: %w", err)
		}
		for key := range seen {
			if _, err := tx.Exec(ctx, `
				INSERT INTO role_permission (organization_id, role_id, permission_key, granted_by)
				VALUES ($1, $2, $3, NULLIF($4, '')::uuid)`, orgID, roleID, key, grantedBy); err != nil {
				return fmt.Errorf("grant permission: %w", err)
			}
		}
		var err error
		items, err = scanRolePermissions(ctx, tx, orgID, roleID)
		return err
	})
	return items, err
}

func (r *PGRepository) EffectivePermissions(orgID, userID string) ([]string, error) {
	ctx := context.Background()
	set := make(map[string]struct{})
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

func (r *PGRepository) HasPermission(orgID, userID, key string) (bool, error) {
	keys, err := r.EffectivePermissions(orgID, userID)
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
	var items []RolePermissionGrant
	for rows.Next() {
		var item RolePermissionGrant
		if err := rows.Scan(&item.OrganizationID, &item.RoleID, &item.PermissionKey, &item.GrantedAt, &item.GrantedBy); err != nil {
			return nil, fmt.Errorf("scan role permission: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
