// Package identity implements authentication and authorization for Reticora.
package identity

import "time"

// Permission represents a single access right.
type Permission string

// Defined permissions for Phase 1.
const (
	PermCIRead            Permission = "ci:read"
	PermCIWrite           Permission = "ci:write"
	PermCIDelete          Permission = "ci:delete"
	PermCITypeManage      Permission = "citype:manage"
	PermSiteRead          Permission = "site:read"
	PermSiteWrite         Permission = "site:write"
	PermRackWrite         Permission = "rack:write"
	PermRelationshipWrite Permission = "relationship:write"
	PermContactWrite      Permission = "contact:write"
	PermTopologyRead      Permission = "topology:read"
	PermDiscoveryIngest   Permission = "discovery:ingest"
	PermCollectorManage   Permission = "collector:manage"
	PermCredentialManage  Permission = "credential:manage"
	PermWebhookManage     Permission = "webhook:manage"
	PermExportRun         Permission = "export:run"
	PermUserManage        Permission = "user:manage"
	PermRoleManage        Permission = "role:manage"
	PermEntitlementManage Permission = "entitlement:manage"
	PermAuditRead         Permission = "audit:read"
	PermAPIKeyManage      Permission = "apikey:manage"
)

// SessionClaims represent the internal RS256 session JWT claims.
type SessionClaims struct {
	Subject        string       `json:"sub"`
	OrganizationID string       `json:"org_id"`
	ClientScope    string       `json:"client_scope,omitempty"`
	Permissions    []Permission `json:"permissions"`
	IssuedAt       time.Time    `json:"iat"`
	ExpiresAt      time.Time    `json:"exp"`
}

// APIKeyInfo holds resolved API key metadata.
type APIKeyInfo struct {
	ID             string
	OrganizationID string
	ClientScope    string
	Scopes         []Permission
	ExpiresAt      *time.Time
}
