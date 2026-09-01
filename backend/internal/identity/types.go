// Package identity implements authentication and authorization for Reticora.
package identity

import "time"

// Permission represents a single access right.
type Permission string

// Defined permissions. Route-level authorization is driven by this catalog:
// every protected route must map to one of these keys.
const (
	PermCIRead            Permission = "ci:read"
	PermCIWrite           Permission = "ci:write"
	PermCIDelete          Permission = "ci:delete"
	PermCITypeManage      Permission = "citype:manage"
	PermSiteRead          Permission = "site:read"
	PermSiteWrite         Permission = "site:write"
	PermRackRead          Permission = "rack:read"
	PermRackWrite         Permission = "rack:write"
	PermRelationshipRead  Permission = "relationship:read"
	PermRelationshipWrite Permission = "relationship:write"
	PermContactRead       Permission = "contact:read"
	PermContactWrite      Permission = "contact:write"
	PermTopologyRead      Permission = "topology:read"
	PermDiscoveryRead     Permission = "discovery:read"
	PermDiscoveryWrite    Permission = "discovery:write"
	PermDiscoveryIngest   Permission = "discovery:ingest"
	PermCollectorManage   Permission = "collector:manage"
	PermCredentialRead    Permission = "credential:read"
	PermCredentialManage  Permission = "credential:manage"
	PermWebhookRead       Permission = "webhook:read"
	PermWebhookManage     Permission = "webhook:manage"
	PermExportRun         Permission = "export:run"
	PermUserRead          Permission = "user:read"
	PermUserManage        Permission = "user:manage"
	PermRoleRead          Permission = "role:read"
	PermRoleManage        Permission = "role:manage"
	PermPermissionRead    Permission = "permission:read"
	// PermPermissionManage grants permission-grant administration. The value
	// follows the canonical catalog key ("permission:write"), not the
	// constant name, for backwards compatibility with stored role grants.
	PermPermissionManage  Permission = "permission:write"
	PermEntitlementRead   Permission = "entitlement:read"
	PermEntitlementManage Permission = "entitlement:manage"
	PermAuditRead         Permission = "audit:read"
	PermAPIKeyManage      Permission = "apikey:manage"
	PermAssetRead         Permission = "asset:read"
	PermAssetWrite        Permission = "asset:write"
	PermAssignmentRead    Permission = "assignment:read"
	PermAssignmentWrite   Permission = "assignment:write"
	PermDocumentRead      Permission = "document:read"
	PermDocumentWrite     Permission = "document:write"
	PermStocktakeRead     Permission = "stocktake:read"
	PermStocktakeWrite    Permission = "stocktake:write"
	PermConsumableRead    Permission = "consumable:read"
	PermConsumableWrite   Permission = "consumable:write"
	PermOrderRead         Permission = "order:read"
	PermOrderWrite        Permission = "order:write"
	PermOrderApprove      Permission = "order:approve"
	PermMaintenanceRead   Permission = "maintenance:read"
	PermMaintenanceWrite  Permission = "maintenance:write"
	PermDisposalRead      Permission = "disposal:read"
	PermDisposalWrite     Permission = "disposal:write"
	PermTicketRead        Permission = "ticket:read"
	PermTicketWrite       Permission = "ticket:write"
	PermSLARead           Permission = "sla:read"
	PermSLAWrite          Permission = "sla:write"
	PermIPAMRead          Permission = "ipam:read"
	PermIPAMWrite         Permission = "ipam:write"
	PermFormRead          Permission = "form:read"
	PermFormWrite         Permission = "form:write"
	PermWorkflowRead      Permission = "workflow:read"
	PermWorkflowWrite     Permission = "workflow:write"
	PermComplianceRead    Permission = "compliance:read"
	PermComplianceWrite   Permission = "compliance:write"
	PermMonitoringRead    Permission = "monitoring:read"
	PermMonitoringWrite   Permission = "monitoring:write"
	PermIGARead           Permission = "iga:read"
	PermIGAWrite          Permission = "iga:write"
	PermSearchRead        Permission = "search:read"
	PermSearchWrite       Permission = "search:write"
	PermAIRead            Permission = "ai:read"
)

// AllPermissions returns the full set of permissions the identity layer can
// grant, used for admin role assignment and authorization consistency checks.
func AllPermissions() []Permission {
	return allPermissions()
}

func allPermissions() []Permission {
	return []Permission{
		PermCIRead,
		PermCIWrite,
		PermCIDelete,
		PermCITypeManage,
		PermSiteRead,
		PermSiteWrite,
		PermRackRead,
		PermRackWrite,
		PermRelationshipRead,
		PermRelationshipWrite,
		PermContactRead,
		PermContactWrite,
		PermTopologyRead,
		PermDiscoveryRead,
		PermDiscoveryWrite,
		PermDiscoveryIngest,
		PermCollectorManage,
		PermCredentialRead,
		PermCredentialManage,
		PermWebhookRead,
		PermWebhookManage,
		PermExportRun,
		PermUserRead,
		PermUserManage,
		PermRoleRead,
		PermRoleManage,
		PermPermissionRead,
		PermPermissionManage,
		PermEntitlementRead,
		PermEntitlementManage,
		PermAuditRead,
		PermAPIKeyManage,
		PermAssetRead,
		PermAssetWrite,
		PermAssignmentRead,
		PermAssignmentWrite,
		PermDocumentRead,
		PermDocumentWrite,
		PermStocktakeRead,
		PermStocktakeWrite,
		PermConsumableRead,
		PermConsumableWrite,
		PermOrderRead,
		PermOrderWrite,
		PermOrderApprove,
		PermMaintenanceRead,
		PermMaintenanceWrite,
		PermDisposalRead,
		PermDisposalWrite,
		PermTicketRead,
		PermTicketWrite,
		PermSLARead,
		PermSLAWrite,
		PermIPAMRead,
		PermIPAMWrite,
		PermFormRead,
		PermFormWrite,
		PermWorkflowRead,
		PermWorkflowWrite,
		PermComplianceRead,
		PermComplianceWrite,
		PermMonitoringRead,
		PermMonitoringWrite,
		PermIGARead,
		PermIGAWrite,
		PermSearchRead,
		PermSearchWrite,
		PermAIRead,
	}
}

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
