// Package identity implements authentication and authorization for Reticora.
package identity

import (
	"encoding/json"
	"fmt"
	"time"
)

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

// SessionClaims represent the internal RS256 session JWT claims. On the wire
// they follow AUT-02 (see MarshalJSON): sub, org, scopes, cls[], sts[],
// tms[], name, email, plus jti, iat and exp as NumericDate.
type SessionClaims struct {
	Subject        string
	OrganizationID string
	// ClientScope is derived from Scope; only tokens without Scope carry it
	// on the wire (client_scope).
	ClientScope string
	Permissions []Permission
	// Scope is the union scope of all role grants resolved at login or
	// refresh (RBA-03); PermissionScopes lists permissions with a narrower
	// scope. Groups are the IdP groups of the login, kept so a refresh can
	// rebuild the IdP grant without the ID token.
	Scope            *Scope
	PermissionScopes map[Permission]Scope
	Groups           []string
	Name             string
	Email            string
	// ID is the token id (jti) a logout revokes.
	ID        string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// sessionScopeWire is a scope on the wire: one list per dimension, null for
// all of the organization (TEN-04), an empty list for none.
type sessionScopeWire struct {
	Clients json.RawMessage `json:"cls,omitempty"`
	Sites   json.RawMessage `json:"sts,omitempty"`
	Teams   json.RawMessage `json:"tms,omitempty"`
}

// sessionClaimsWire is the JWT payload of AUT-02.
type sessionClaimsWire struct {
	Subject        string       `json:"sub"`
	OrganizationID string       `json:"org"`
	Permissions    []Permission `json:"scopes"`
	sessionScopeWire
	ClientScope      string                          `json:"client_scope,omitempty"`
	PermissionScopes map[Permission]sessionScopeWire `json:"pscp,omitempty"`
	Groups           []string                        `json:"groups,omitempty"`
	Name             string                          `json:"name,omitempty"`
	Email            string                          `json:"email,omitempty"`
	ID               string                          `json:"jti,omitempty"`
	IssuedAt         int64                           `json:"iat,omitempty"`
	ExpiresAt        int64                           `json:"exp,omitempty"`
}

func scopeSetWire(s ScopeSet) json.RawMessage {
	if s.All {
		return json.RawMessage("null")
	}
	ids := s.IDs
	if ids == nil {
		ids = []string{}
	}
	raw, _ := json.Marshal(ids) //nolint:errchkjson // a string slice always marshals
	return raw
}

func scopeWire(s *Scope) sessionScopeWire {
	return sessionScopeWire{Clients: scopeSetWire(s.Clients), Sites: scopeSetWire(s.Sites), Teams: scopeSetWire(s.Teams)}
}

func (w *sessionScopeWire) present() bool {
	return w.Clients != nil || w.Sites != nil || w.Teams != nil
}

func scopeSetFromWire(raw json.RawMessage) (ScopeSet, error) {
	if raw == nil || string(raw) == "null" {
		return ScopeSet{All: true}, nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return ScopeSet{}, err
	}
	if len(ids) == 0 {
		ids = nil
	}
	return ScopeSet{IDs: ids}, nil
}

func (w *sessionScopeWire) scope() (Scope, error) {
	var s Scope
	var err error
	if s.Clients, err = scopeSetFromWire(w.Clients); err != nil {
		return s, err
	}
	if s.Sites, err = scopeSetFromWire(w.Sites); err != nil {
		return s, err
	}
	s.Teams, err = scopeSetFromWire(w.Teams)
	return s, err
}

func numericDate(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromNumericDate(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

// MarshalJSON encodes the claims in the AUT-02 format. The value receiver
// makes json.Marshal use it for values as well as pointers.
func (c SessionClaims) MarshalJSON() ([]byte, error) { //nolint:gocritic // see above
	w := sessionClaimsWire{
		Subject:        c.Subject,
		OrganizationID: c.OrganizationID,
		Permissions:    c.Permissions,
		Groups:         c.Groups,
		Name:           c.Name,
		Email:          c.Email,
		ID:             c.ID,
		IssuedAt:       numericDate(c.IssuedAt),
		ExpiresAt:      numericDate(c.ExpiresAt),
	}
	if w.Permissions == nil {
		w.Permissions = []Permission{}
	}
	if c.Scope != nil {
		w.sessionScopeWire = scopeWire(c.Scope)
	} else {
		w.ClientScope = c.ClientScope
	}
	if len(c.PermissionScopes) > 0 {
		w.PermissionScopes = make(map[Permission]sessionScopeWire, len(c.PermissionScopes))
		for p, s := range c.PermissionScopes {
			w.PermissionScopes[p] = scopeWire(&s)
		}
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes the AUT-02 format.
func (c *SessionClaims) UnmarshalJSON(data []byte) error {
	var w sessionClaimsWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*c = SessionClaims{
		Subject:        w.Subject,
		OrganizationID: w.OrganizationID,
		ClientScope:    w.ClientScope,
		Permissions:    w.Permissions,
		Groups:         w.Groups,
		Name:           w.Name,
		Email:          w.Email,
		ID:             w.ID,
		IssuedAt:       fromNumericDate(w.IssuedAt),
		ExpiresAt:      fromNumericDate(w.ExpiresAt),
	}
	if w.present() {
		scope, err := w.scope()
		if err != nil {
			return fmt.Errorf("identity: decode scope claims: %w", err)
		}
		c.Scope = &scope
		c.ClientScope = scope.LegacyClientScope()
	}
	if len(w.PermissionScopes) > 0 {
		c.PermissionScopes = make(map[Permission]Scope, len(w.PermissionScopes))
		for p, sw := range w.PermissionScopes {
			scope, err := sw.scope()
			if err != nil {
				return fmt.Errorf("identity: decode permission scope claims: %w", err)
			}
			c.PermissionScopes[p] = scope
		}
	}
	return nil
}

// APIKeyInfo holds resolved API key metadata.
type APIKeyInfo struct {
	ID             string
	OrganizationID string
	ClientScope    string
	Scopes         []Permission
	ExpiresAt      *time.Time
}
