package permission

// StandardRole is a system role seeded into every organization
// (seed_standard_roles, migration 000192).
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
// RBA-01 keys since migration 000081, without the keys migration 000147
// retired (WP-363: ci_type:manage and the keys that gate no route), and with
// the phase-2 rows of E-106 (RBA-05, RBA-06; migration 000192): stock:*,
// reservation:* and contract:* replace consumable:*, inventory:manage and
// asset:reserve; engineer also holds stocktake:complete, import:run and
// tag:manage, the client technician stocktake:complete; metamodel:publish,
// field_permission:manage, import:manage, saved_view:manage,
// lifecycle:manage and relationship_type:manage stay with org_admin. The
// database seed must match it exactly; permission/role_matrix_integration_test.go
// compares both and checks the RBA-02, P2 and RBA-06 rows.
var StandardRoles = []StandardRole{
	{Name: "org_admin", Scope: "org"},
	{Name: "engineer", Scope: "org", Permissions: []string{
		"admp:read", "agent:manage", "agent:read", "ai:read",
		"alert:acknowledge", "asset:move", "asset:read", "asset:write",
		"assignment:read", "assignment:write", "ci:delete", "ci:read",
		"ci:write", "ci_instance_attribute:manage", "collector:manage", "compliance:read",
		"compliance:write", "contact:read", "contact:write", "contract:read",
		"contract:write", "credential:manage", "credential:read", "desk:read",
		"desk:write", "discovery:ingest", "discovery:manage", "discovery:read",
		"disposal:read", "disposal:write", "document:read", "document:write",
		"entitlement:read", "export:run", "finding:read", "finding:write",
		"form:read", "form:write", "iga:read", "import:run",
		"ipam:read", "ipam:write", "job:read", "key:read",
		"key:write", "lifecycle:transition", "location:read", "location:write",
		"maintenance:read", "maintenance:write", "monitoring:read", "monitoring:write",
		"order:read", "order:write", "override:write", "permission:read",
		"rack:read", "rack:write", "reconciliation:manage", "relationship:read",
		"relationship:write", "reservation:read", "reservation:write", "review:resolve",
		"role:read", "saved_view:read", "saved_view:write", "search:read",
		"search:write", "sla:read", "stock:read", "stock:write",
		"stocktake:complete", "stocktake:read", "stocktake:write", "tag:manage",
		"ticket:read", "ticket:write", "topology:read", "training:read",
		"training:write", "user:read", "vrf:manage", "webhook:read",
		"workflow:read", "workflow:write",
	}},
	{Name: "viewer", Scope: "org", Permissions: []string{
		"agent:read", "asset:read", "assignment:read", "ci:read",
		"compliance:read", "contact:read", "contract:read", "desk:read",
		"discovery:read", "disposal:read", "document:read", "entitlement:read",
		"finding:read", "form:read", "iga:read", "ipam:read",
		"key:read", "location:read", "maintenance:read", "monitoring:read",
		"order:read", "permission:read", "rack:read", "relationship:read",
		"reservation:read", "role:read", "saved_view:read", "search:read",
		"sla:read", "stock:read", "stocktake:read", "ticket:read",
		"topology:read", "training:read", "user:read", "webhook:read",
		"workflow:read",
	}},
	{Name: "client_technician", Scope: "client", Permissions: []string{
		"alert:acknowledge", "asset:read", "assignment:read", "ci:read",
		"ci:write", "contact:read", "contact:write", "contract:read",
		"desk:read", "document:read", "export:run", "ipam:read",
		"job:read", "key:read", "location:read", "maintenance:read",
		"monitoring:read", "order:read", "rack:read", "rack:write",
		"relationship:read", "relationship:write", "reservation:read", "review:resolve",
		"saved_view:read", "search:read", "stock:read", "stocktake:complete",
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
