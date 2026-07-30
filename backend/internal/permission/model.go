// Package permission provides the first-class RBAC permission catalogue.
package permission

import "time"

// Permission describes one global catalogue entry understood by the API.
type Permission struct {
	Key         string `json:"key"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Description string `json:"description"`
}

// RolePermissionGrant records a tenant-scoped role-to-permission grant.
type RolePermissionGrant struct {
	OrganizationID string    `json:"organization_id"`
	RoleID         string    `json:"role_id"`
	PermissionKey  string    `json:"permission_key"`
	GrantedAt      time.Time `json:"granted_at"`
	GrantedBy      string    `json:"granted_by,omitempty"`
}

// ReplaceRolePermissionsRequest replaces the complete permission set of a role.
type ReplaceRolePermissionsRequest struct {
	PermissionKeys []string `json:"permission_keys"`
}

// EffectivePermissionsResponse is returned for the authenticated caller.
type EffectivePermissionsResponse struct {
	UserID      string   `json:"user_id"`
	Permissions []string `json:"permissions"`
}
