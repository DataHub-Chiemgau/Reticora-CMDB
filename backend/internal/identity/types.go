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
	PermKeyRead           Permission = "key:read"
	PermKeyWrite          Permission = "key:write"
	PermTrainingRead      Permission = "training:read"
	PermTrainingWrite     Permission = "training:write"
	PermDeskRead          Permission = "desk:read"
	PermDeskWrite         Permission = "desk:write"
	PermAgentRead         Permission = "agent:read"
	PermAgentManage       Permission = "agent:manage"
	PermAgentIngest       Permission = "agent:ingest"
	PermSecurityRead      Permission = "security:read"
	PermSecurityWrite     Permission = "security:write"
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

	// Enterprise CMDB + asset/inventory extension (spec §21). These keys are
	// seeded into the permission catalogue by migration 000055.
	PermCITypeManageNew           Permission = "ci_type:manage"
	PermCIAttributeManage         Permission = "ci_attribute:manage"
	PermCIInstanceAttributeManage Permission = "ci_instance_attribute:manage"
	PermRelationshipTypeManage    Permission = "relationship_type:manage"
	PermAssetAssign               Permission = "asset:assign"
	PermAssetMove                 Permission = "asset:move"
	PermAssetReserve              Permission = "asset:reserve"
	PermInventoryManage           Permission = "inventory:manage"
	PermLifecycleManage           Permission = "lifecycle:manage"
	PermReconciliationResolve     Permission = "reconciliation:resolve"
	PermOverrideWrite             Permission = "override:write"
	PermSavedViewRead             Permission = "saved_view:read"
	PermSavedViewWrite            Permission = "saved_view:write"
	// Special actions with their own permission (RBA-06, WP-045).
	PermCredentialDecrypt    Permission = "credential:decrypt"
	PermLifecycleTransition  Permission = "lifecycle:transition"
	PermReconciliationManage Permission = "reconciliation:manage"
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
		PermKeyRead,
		PermKeyWrite,
		PermTrainingRead,
		PermTrainingWrite,
		PermDeskRead,
		PermDeskWrite,
		PermAgentRead,
		PermAgentManage,
		PermAgentIngest,
		PermSecurityRead,
		PermSecurityWrite,
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
		PermCITypeManageNew,
		PermCIAttributeManage,
		PermCIInstanceAttributeManage,
		PermRelationshipTypeManage,
		PermAssetAssign,
		PermAssetMove,
		PermAssetReserve,
		PermInventoryManage,
		PermLifecycleManage,
		PermReconciliationResolve,
		PermOverrideWrite,
		PermSavedViewRead,
		PermSavedViewWrite,
		PermCredentialDecrypt,
		PermLifecycleTransition,
		PermReconciliationManage,
	}
}

// SessionClaims represent the internal RS256 session JWT claims.
type SessionClaims struct {
	Subject        string       `json:"sub"`
	OrganizationID string       `json:"org_id"`
	ClientScope    string       `json:"client_scope,omitempty"`
	Permissions    []Permission `json:"permissions"`
	// Scope is the union scope of all role grants resolved at login or
	// refresh (RBA-03); PermissionScopes lists permissions with a narrower
	// scope. Groups are the IdP groups of the login, kept so a refresh can
	// rebuild the IdP grant without the ID token.
	Scope            *Scope               `json:"scope,omitempty"`
	PermissionScopes map[Permission]Scope `json:"permission_scopes,omitempty"`
	Groups           []string             `json:"groups,omitempty"`
	IssuedAt         time.Time            `json:"iat"`
	ExpiresAt        time.Time            `json:"exp"`
}

// APIKeyInfo holds resolved API key metadata.
type APIKeyInfo struct {
	ID             string
	OrganizationID string
	ClientScope    string
	Scopes         []Permission
	ExpiresAt      *time.Time
}
