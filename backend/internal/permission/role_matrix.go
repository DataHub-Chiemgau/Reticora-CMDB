package permission

// StandardRole is a system role seeded into every organization
// (seed_standard_roles, migration 000073).
type StandardRole struct {
	Name string
	// Scope is the assignment scope the role is valid in: "org", or "client"
	// for a role that grants its permissions only within an assigned client
	// (the C of the RBA-02 matrix).
	Scope string
	// Permissions lists the role's keys; nil for org_admin, which holds the
	// whole catalogue.
	Permissions []string
}

// StandardRoles is the target role matrix (RBA-02, E-13), with the exact
// RBA-01 keys since migration 000081. The database seed
// must match it exactly; permission/role_matrix_integration_test.go compares
// both, and the RBA-02 rows of the catalogue text are checked there too.
var StandardRoles = []StandardRole{
	{Name: "org_admin", Scope: "org"},
	{Name: "engineer", Scope: "org", Permissions: []string{
		"agent:manage", "agent:read", "ai:read", "asset:assign",
		"asset:move", "asset:read", "asset:reserve", "asset:write",
		"assignment:read", "assignment:write", "ci:delete", "ci:read",
		"ci:write", "ci_instance_attribute:manage", "collector:manage", "compliance:read",
		"compliance:write", "consumable:read", "consumable:write", "contact:read",
		"contact:write", "credential:decrypt", "credential:manage", "credential:read",
		"credential:write", "desk:read", "desk:write", "discovery:ingest",
		"discovery:manage", "discovery:read", "disposal:read", "disposal:write",
		"document:read", "document:write", "entitlement:read", "export:run",
		"form:read", "form:write", "iga:read", "ipam:read",
		"ipam:write", "job:read", "key:read", "key:write",
		"lifecycle:transition", "location:read", "location:write", "maintenance:read",
		"maintenance:write", "monitoring:read", "monitoring:write", "order:read",
		"order:write", "override:write", "permission:read", "rack:read",
		"rack:write", "reconciliation:manage", "relationship:read", "relationship:write",
		"review:resolve", "role:read", "saved_view:read", "saved_view:write",
		"search:read", "search:write", "security:read", "security:write",
		"sla:read", "stocktake:read", "stocktake:write", "ticket:read",
		"ticket:write", "topology:read", "training:read", "training:write",
		"user:read", "vrf:manage", "webhook:read", "workflow:read",
		"workflow:write",
	}},
	{Name: "viewer", Scope: "org", Permissions: []string{
		"agent:read", "asset:read", "assignment:read", "ci:read",
		"compliance:read", "consumable:read", "contact:read", "desk:read",
		"discovery:read", "disposal:read", "document:read", "entitlement:read",
		"form:read", "iga:read", "ipam:read", "key:read",
		"location:read", "maintenance:read", "monitoring:read", "order:read",
		"permission:read", "rack:read", "relationship:read", "role:read",
		"saved_view:read", "search:read", "security:read", "sla:read",
		"stocktake:read", "ticket:read", "topology:read", "training:read",
		"user:read", "webhook:read", "workflow:read",
	}},
	{Name: "client_technician", Scope: "client", Permissions: []string{
		"asset:read", "assignment:read", "ci:read", "ci:write",
		"consumable:read", "contact:read", "contact:write", "desk:read",
		"document:read", "export:run", "ipam:read", "job:read",
		"key:read", "location:read", "maintenance:read", "monitoring:read",
		"order:read", "rack:read", "rack:write", "relationship:read",
		"relationship:write", "review:resolve", "saved_view:read", "search:read",
		"stocktake:read", "stocktake:write", "ticket:read", "ticket:write",
		"topology:read", "training:read",
	}},
}

// StandardRolePermissions returns the keys of the named standard role; the
// org_admin receives the whole catalogue. ok is false for unknown roles.
func StandardRolePermissions(name string) (keys []string, ok bool) {
	for _, role := range StandardRoles {
		if role.Name != name {
			continue
		}
		if role.Permissions == nil {
			keys = make([]string, 0, len(Catalogue))
			for _, p := range Catalogue {
				keys = append(keys, p.Key)
			}
			return keys, true
		}
		return append([]string(nil), role.Permissions...), true
	}
	return nil, false
}
