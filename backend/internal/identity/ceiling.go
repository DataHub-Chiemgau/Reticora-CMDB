package identity

import "strings"

// GrantCeiling returns the permissions of the target grants that p does not
// hold in a scope covering the target's scope of that permission (RBA-03,
// RBA-08): whoever hands out, takes over or ends rights must hold them at
// least as widely. A scope covers another when it covers every dimension
// (clients, sites, teams): all objects cover everything, a list covers the
// lists it contains. Org-wide therefore covers a client and a site scope, a
// client scope covers that client. A site within a client is not mapped to
// its client here, so a client scope does not cover a site grant (fail-closed).
// The scope of a permission is its narrower PermissionScopes entry or the
// principal's scope; a principal without a resolved scope (Scope == nil) is
// org-wide, restricted only by its ClientScope, like middleware.TenantScopeFor.
// An empty result means the target lies within p's rights.
func GrantCeiling(p *Principal, target []Grant) []Permission {
	want := ResolveAccess(target)
	var missing []Permission
	for _, perm := range want.Permissions {
		need := want.Scope
		if s, ok := want.PermissionScopes[perm]; ok {
			need = s
		}
		held, ok := principalScopeFor(p, perm)
		if !ok || !scopeCovers(&held, &need) {
			missing = append(missing, perm)
		}
	}
	return missing
}

// principalScopeFor is the scope in which p holds perm.
func principalScopeFor(p *Principal, perm Permission) (Scope, bool) {
	if p == nil || !p.Has(perm) {
		return Scope{}, false
	}
	if s, ok := p.PermissionScopes[perm]; ok {
		return s, true
	}
	if p.Scope != nil {
		return *p.Scope, true
	}
	all := ScopeSet{All: true}
	scope := Scope{Clients: all, Sites: all, Teams: all}
	if clients := strings.TrimSpace(p.ClientScope); clients != "" {
		scope.Clients = ScopeSet{}
		for _, id := range strings.Split(clients, ",") {
			if id = strings.TrimSpace(id); id != "" {
				scope.Clients.IDs = append(scope.Clients.IDs, id)
			}
		}
	}
	return scope, true
}

func scopeCovers(held, need *Scope) bool {
	return setCovers(held.Clients, need.Clients) && setCovers(held.Sites, need.Sites) && setCovers(held.Teams, need.Teams)
}

func setCovers(held, need ScopeSet) bool {
	if held.All {
		return true
	}
	if need.All {
		return false
	}
	ids := make(map[string]bool, len(held.IDs))
	for _, id := range held.IDs {
		ids[strings.ToLower(strings.TrimSpace(id))] = true
	}
	for _, id := range need.IDs {
		if !ids[strings.ToLower(strings.TrimSpace(id))] {
			return false
		}
	}
	return true
}
