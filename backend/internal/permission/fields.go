package permission

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// ObjectRef describes the object a field, relation or action is checked
// on: its id (object grants) and its client, site and team (the scope of
// each role assignment). An empty client, site or team is a row without
// one, which RLS shows to scoped principals as well; an empty ObjectRef
// stands for an object not yet created.
type ObjectRef struct {
	ID       string
	ClientID string
	SiteID   string
	TeamID   string
}

// roleScope is one role assignment of a principal: the role, its
// permissions (capped by the principal) and the scope of the assignment.
type roleScope struct {
	key   string // "r:<role id>", "c:<custom role id>" or "" without role
	perms map[identity.Permission]bool
	grant identity.Grant
}

// inScope reports whether the assignment covers the object (RBA-03): an
// empty dimension of the assignment is unrestricted, an object without
// client, site or team is in every scope of that dimension (shared rows,
// team predicate of 000065, E-11).
func (r *roleScope) inScope(obj *ObjectRef) bool {
	return dimensionCovers(r.grant.Clients, obj.ClientID) && dimensionCovers(r.grant.Sites, obj.SiteID) &&
		dimensionCovers(r.grant.Teams, obj.TeamID)
}

func dimensionCovers(ids []string, id string) bool {
	return len(ids) == 0 || id == "" || slices.Contains(ids, id)
}

// objectRight is the default level of a role for the kind: write with
// <kind>:write, read with <kind>:read, otherwise none.
func objectRight(perms map[identity.Permission]bool, kind ObjectKind) Level {
	switch {
	case perms[permissionOf(kind, "write")]:
		return LevelWrite
	case perms[permissionOf(kind, "read")]:
		return LevelRead
	}
	return LevelNone
}

// snapshotIndex groups the snapshot of an organization for the resolution.
type snapshotIndex struct {
	field     map[string]map[ObjectKind][]*FieldGrant  // by role key
	object    map[ObjectKind]map[string][]*ObjectGrant // by object id
	sensitive map[ObjectKind][]*SensitiveField
	computed  map[string][]*ComputedSource // by attribute key (SR-58)
}

func roleKey(roleID, customRoleID *string) string {
	switch {
	case roleID != nil:
		return "r:" + *roleID
	case customRoleID != nil:
		return "c:" + *customRoleID
	}
	return ""
}

func newSnapshotIndex(s *Snapshot) *snapshotIndex {
	idx := &snapshotIndex{
		field:     map[string]map[ObjectKind][]*FieldGrant{},
		object:    map[ObjectKind]map[string][]*ObjectGrant{},
		sensitive: map[ObjectKind][]*SensitiveField{},
		computed:  map[string][]*ComputedSource{},
	}
	for i := range s.FieldGrants {
		g := &s.FieldGrants[i]
		key := roleKey(g.RoleID, g.CustomRoleID)
		if idx.field[key] == nil {
			idx.field[key] = map[ObjectKind][]*FieldGrant{}
		}
		idx.field[key][g.ObjectKind] = append(idx.field[key][g.ObjectKind], g)
	}
	for i := range s.ObjectGrants {
		g := &s.ObjectGrants[i]
		if idx.object[g.ObjectKind] == nil {
			idx.object[g.ObjectKind] = map[string][]*ObjectGrant{}
		}
		idx.object[g.ObjectKind][g.ObjectID] = append(idx.object[g.ObjectKind][g.ObjectID], g)
	}
	for i := range s.Sensitive {
		f := &s.Sensitive[i]
		idx.sensitive[f.ObjectKind] = append(idx.sensitive[f.ObjectKind], f)
	}
	for i := range s.Computed {
		c := &s.Computed[i]
		idx.computed[c.Attribute] = append(idx.computed[c.Attribute], c)
	}
	return idx
}

// FieldPolicy is the resolved field permission of one principal or set of
// grants (RBA-07, E-43). Without any field permission row of the
// organization it is unrestricted: every level is the object right of the
// permissions and every action is allowed with its permission, as in
// phase 1. A nil policy grants nothing (fail-closed).
type FieldPolicy struct {
	unrestricted bool
	// perms is the permission set of an unrestricted policy.
	perms map[identity.Permission]bool
	roles []roleScope
	teams map[string]bool
	// parents maps CI types to their parent type.
	parents map[string]string
	idx     *snapshotIndex
	// assetsOff closes computed attributes with asset sources: the
	// organization lacks the feature assets_basic (SR-58, WP-871).
	assetsOff bool
}

// UnrestrictedPolicy is the policy without field permissions for the
// permission set: levels follow the object permissions only.
func UnrestrictedPolicy(perms []identity.Permission) *FieldPolicy {
	return &FieldPolicy{unrestricted: true, perms: permissionSet(perms)}
}

func permissionSet(perms []identity.Permission) map[identity.Permission]bool {
	set := make(map[identity.Permission]bool, len(perms))
	for _, p := range perms {
		set[p] = true
	}
	return set
}

// maxChain bounds the type chain walk (the database allows 6 levels).
const maxChain = 64

// chain returns typeID and its ancestors, nearest first.
func (p *FieldPolicy) chain(kind ObjectKind, typeID string) []string {
	if typeID == "" || !kind.typed() {
		return nil
	}
	out := []string{typeID}
	for id := typeID; len(out) < maxChain; {
		parent, ok := p.parents[id]
		if !ok || parent == "" || slices.Contains(out, parent) {
			break
		}
		out = append(out, parent)
		id = parent
	}
	return out
}

// sensitive reports whether the field is marked sensitive for the kind, at
// the type, at an ancestor or without type; a subtype cannot lift it.
func (p *FieldPolicy) sensitive(kind ObjectKind, chain []string, fieldKey string) bool {
	if fieldKey == "" {
		return false
	}
	for _, s := range p.idx.sensitive[kind] {
		if s.FieldKey == fieldKey && (s.CITypeID == nil || slices.Contains(chain, *s.CITypeID)) {
			return true
		}
	}
	return false
}

// objectGrant returns the object grant level of the role for the object:
// the lowest of the grants of the role or of a team of the principal.
func (p *FieldPolicy) objectGrant(r *roleScope, kind ObjectKind, objectID string) (Level, bool) {
	if objectID == "" {
		return "", false
	}
	var level Level
	found := false
	for _, g := range p.idx.object[kind][objectID] {
		applies := (r.key != "" && roleKey(g.GranteeRoleID, g.GranteeCustomRoleID) == r.key) ||
			(g.GranteeTeamID != nil && p.teams[*g.GranteeTeamID])
		if !applies {
			continue
		}
		if !found {
			level, found = g.Level, true
			continue
		}
		level = minLevel(level, g.Level)
	}
	return level, found
}

// stage returns the lowest level of the grants of the role for the kind
// that match, at the nearest type of the chain first and then without
// type ("none wins" within a stage).
func (p *FieldPolicy) stage(r *roleScope, kind ObjectKind, chain []string, match func(*FieldGrant) bool) (Level, bool) {
	grants := p.idx.field[r.key][kind]
	if r.key == "" || len(grants) == 0 {
		return "", false
	}
	lowest := func(typeID *string) (Level, bool) {
		var level Level
		found := false
		for _, g := range grants {
			if !sameString(g.CITypeID, typeID) || !match(g) {
				continue
			}
			if !found {
				level, found = g.Level, true
				continue
			}
			level = minLevel(level, g.Level)
		}
		return level, found
	}
	for i := range chain {
		if level, ok := lowest(&chain[i]); ok {
			return level, true
		}
	}
	return lowest(nil)
}

// roleLevel resolves the level of one role for a field after E-43: object
// grant > field grant with type and field (nearest type) > field grant with
// field without type > grant of the type or relationship type > grant of
// the kind > object right. It is at most the object right of the role. A
// sensitive field is open only through a field grant with its key.
func (p *FieldPolicy) roleLevel(r *roleScope, kind ObjectKind, chain []string, relType, fieldKey string, obj *ObjectRef, sensitive bool) Level {
	right := objectRight(r.perms, kind)
	if right == LevelNone {
		return LevelNone
	}
	if !sensitive {
		if level, ok := p.objectGrant(r, kind, obj.ID); ok {
			return minLevel(level, right)
		}
	}
	if fieldKey != "" {
		if level, ok := p.stage(r, kind, chain, func(g *FieldGrant) bool { return g.FieldKey != nil && *g.FieldKey == fieldKey }); ok {
			return minLevel(level, right)
		}
	}
	if sensitive {
		return LevelNone
	}
	if kind == KindRelationship && relType != "" {
		if level, ok := p.stage(r, kind, nil, func(g *FieldGrant) bool {
			return g.RelationshipTypeID != nil && (*g.RelationshipTypeID == relType || g.RelationshipType == relType)
		}); ok {
			return minLevel(level, right)
		}
	}
	if level, ok := p.stage(r, kind, chain, func(g *FieldGrant) bool {
		return g.FieldKey == nil && g.RelationshipTypeID == nil && g.Action == nil
	}); ok {
		return minLevel(level, right)
	}
	return right
}

// Level returns the level of a field of an object (RBA-07, E-43): the
// highest level of the roles whose assignment covers the object (union,
// RBA-03), each at most its object right. typeID is the CI type of a ci or
// asset (its ancestors apply too) and for a relationship the id of its
// relationship_type row (WP-770: the organization's row before the system
// row of the key) or, without a resolved row, the key (rel_type); fieldKey is core.<column>, attributes.<key> or
// _instance.<key>, empty for the object as a whole.
func (p *FieldPolicy) Level(kind ObjectKind, typeID, fieldKey string, obj ObjectRef) Level {
	return p.level(kind, typeID, fieldKey, &obj, false)
}

// level is Level; forceSensitive treats the field as sensitive (a computed
// attribute with a sensitive asset source, SR-58).
func (p *FieldPolicy) level(kind ObjectKind, typeID, fieldKey string, obj *ObjectRef, forceSensitive bool) Level {
	if p == nil {
		return LevelNone
	}
	if p.unrestricted {
		return objectRight(p.perms, kind)
	}
	chain := p.chain(kind, typeID)
	relType := ""
	if kind == KindRelationship {
		relType = typeID
	}
	sensitive := forceSensitive || p.sensitive(kind, chain, fieldKey)
	best := LevelNone
	for i := range p.roles {
		r := &p.roles[i]
		if !r.inScope(obj) {
			continue
		}
		best = maxLevel(best, p.roleLevel(r, kind, chain, relType, fieldKey, obj, sensitive))
		if best == LevelWrite {
			break
		}
	}
	return best
}

// RelationLevel returns the level of the relationships of type relType
// (relationship_type.key) at a CI of type ciTypeID: a ci grant with that
// relationship type (nearest type first) caps the level, which is at most
// the CI and relationship rights of the role; an object grant none on the
// CI closes them (WP-770 enforces it).
func (p *FieldPolicy) RelationLevel(ciTypeID, relType string, ci ObjectRef) Level {
	if p == nil {
		return LevelNone
	}
	if p.unrestricted {
		return minLevel(objectRight(p.perms, KindCI), objectRight(p.perms, KindRelationship))
	}
	chain := p.chain(KindCI, ciTypeID)
	best := LevelNone
	for i := range p.roles {
		r := &p.roles[i]
		if !r.inScope(&ci) {
			continue
		}
		right := minLevel(objectRight(r.perms, KindCI), objectRight(r.perms, KindRelationship))
		if level, ok := p.objectGrant(r, KindCI, ci.ID); ok && level == LevelNone {
			right = LevelNone
		}
		if right == LevelNone {
			continue
		}
		if level, ok := p.stage(r, KindCI, chain, func(g *FieldGrant) bool {
			return g.RelationshipTypeID != nil && g.RelationshipType == relType
		}); ok {
			right = minLevel(level, right)
		}
		best = maxLevel(best, right)
	}
	return best
}

// ActionAllowed reports whether the principal may run the action (a key of
// the permission catalogue, e.g. lifecycle:transition) on the object: a
// role whose assignment covers the object holds the permission, no object
// grant none closes the object for it, and its action grant (nearest type
// first, then without type) is not none. Without an action grant the
// permission suffices.
func (p *FieldPolicy) ActionAllowed(kind ObjectKind, typeID, action string, obj ObjectRef) bool {
	if p == nil {
		return false
	}
	perm := identity.Permission(action)
	if p.unrestricted {
		return p.perms[perm]
	}
	chain := p.chain(kind, typeID)
	for i := range p.roles {
		r := &p.roles[i]
		if !r.perms[perm] || !r.inScope(&obj) {
			continue
		}
		if level, ok := p.objectGrant(r, kind, obj.ID); ok && level == LevelNone {
			continue
		}
		level, ok := p.stage(r, kind, chain, func(g *FieldGrant) bool { return g.Action != nil && *g.Action == action })
		if !ok || level != LevelNone {
			return true
		}
	}
	return false
}

// Restricted reports whether a field of the kind may resolve below the
// object right for this principal: a sensitive field, an object grant of
// the kind for one of its roles or teams, or a field grant of the kind for
// one of its roles. Without any it is false and callers can skip the
// projection. The CI kind is also restricted when a computed attribute
// with asset sources can be closed (computedHidden, SR-58).
func (p *FieldPolicy) Restricted(kind ObjectKind) bool {
	if p == nil {
		return true
	}
	if kind == KindCI && p.computedHidden() {
		return true
	}
	if p.unrestricted {
		return false
	}
	if len(p.idx.sensitive[kind]) > 0 {
		return true
	}
	for i := range p.roles {
		if key := p.roles[i].key; key != "" && len(p.idx.field[key][kind]) > 0 {
			return true
		}
	}
	for _, grants := range p.idx.object[kind] {
		for _, g := range grants {
			if g.GranteeTeamID != nil && p.teams[*g.GranteeTeamID] {
				return true
			}
			key := roleKey(g.GranteeRoleID, g.GranteeCustomRoleID)
			for i := range p.roles {
				if p.roles[i].key != "" && p.roles[i].key == key {
					return true
				}
			}
		}
	}
	return false
}

// buildPolicy resolves the grants against the snapshot. caps is the
// permission set of the principal (the intersection of an API key, AUT-04);
// nil leaves the role permissions uncapped.
func buildPolicy(snap *Snapshot, grants []identity.Grant, teamIDs []string, parents map[string]string, caps map[identity.Permission]bool) *FieldPolicy {
	p := &FieldPolicy{idx: newSnapshotIndex(snap), parents: parents, teams: make(map[string]bool, len(teamIDs))}
	for _, t := range teamIDs {
		p.teams[t] = true
	}
	for i := range grants {
		g := &grants[i]
		perms := make(map[identity.Permission]bool, len(g.Permissions))
		for _, perm := range g.Permissions {
			if caps == nil || caps[perm] {
				perms[perm] = true
			}
		}
		var key string
		switch {
		case g.RoleID != "":
			key = "r:" + g.RoleID
		case g.CustomRoleID != "":
			key = "c:" + g.CustomRoleID
		}
		p.roles = append(p.roles, roleScope{key: key, perms: perms, grant: *g})
	}
	return p
}

// DefaultFieldPolicyTTL is the lifetime of a cached snapshot (ENT-03
// pattern).
const DefaultFieldPolicyTTL = 60 * time.Second

// FieldResolver resolves the FieldPolicy of a principal (RBA-07). The
// snapshot of an organization is cached in the shared cache store under
// fieldgrants:<org> for DefaultFieldPolicyTTL; Invalidate drops it after a
// change. Roles, assignment scopes, teams and the type chain are read on
// every resolution, so role and membership changes need no invalidation.
type FieldResolver struct {
	access          identity.AccessResolver
	serviceAccounts identity.ServiceAccountAccessResolver
	repo            FieldGrantRepository
	cache           cache.Store
	ttl             time.Duration
	features        FeatureCheck
}

// NewFieldResolver creates a resolver. access resolves the roles of users
// and of IdP groups, serviceAccounts those of service accounts (nil: none);
// a nil store keeps the cache per process.
func NewFieldResolver(access identity.AccessResolver, serviceAccounts identity.ServiceAccountAccessResolver, repo FieldGrantRepository, store cache.Store) *FieldResolver {
	if store == nil {
		store = cache.NewMemoryStore()
	}
	return &FieldResolver{access: access, serviceAccounts: serviceAccounts, repo: repo, cache: store, ttl: DefaultFieldPolicyTTL}
}

// WithTTL sets the cache lifetime of the snapshot (tests).
func (r *FieldResolver) WithTTL(ttl time.Duration) *FieldResolver {
	if ttl > 0 {
		r.ttl = ttl
	}
	return r
}

// fieldGrantsKey is the cache key of the snapshot; v2 carries the computed
// sources (WP-871), so no instance reads a snapshot without them.
func fieldGrantsKey(orgID string) string { return "fieldgrants:v2:" + orgID }

// snapshot returns the cached snapshot of the organization or reads it.
func (r *FieldResolver) snapshot(ctx context.Context, orgID string) (Snapshot, error) {
	key := fieldGrantsKey(orgID)
	if raw, ok, err := r.cache.Get(ctx, key); err != nil {
		slog.WarnContext(ctx, "field permission cache read failed", "error", err, "organization_id", orgID)
	} else if ok {
		var snap Snapshot
		if err = json.Unmarshal([]byte(raw), &snap); err == nil {
			return snap, nil
		}
	}
	snap, err := r.repo.Snapshot(ctx, orgID)
	if err != nil {
		return Snapshot{}, err
	}
	if raw, err := json.Marshal(&snap); err == nil {
		if err = r.cache.Set(ctx, key, string(raw), r.ttl); err != nil {
			slog.WarnContext(ctx, "field permission cache write failed", "error", err, "organization_id", orgID)
		}
	}
	return snap, nil
}

// InvalidateOrganization drops the cached snapshot of the organization and
// returns the error of the cache: the purge of a deleted organization
// (WP-1155) keeps its cache step open until the entry is gone.
func (r *FieldResolver) InvalidateOrganization(ctx context.Context, orgID string) error {
	return r.cache.Delete(ctx, fieldGrantsKey(orgID))
}

// Invalidate drops the cached snapshot of the organization; with Redis the
// change reaches every instance.
func (r *FieldResolver) Invalidate(ctx context.Context, orgID string) {
	if err := r.cache.Delete(ctx, fieldGrantsKey(orgID)); err != nil {
		slog.ErrorContext(ctx, "field permission cache invalidation failed", "error", err, "organization_id", orgID)
	}
}

// Resolve returns the policy of the principal: a session holds its role
// assignments (AccessGrants) and the standard roles of its IdP groups
// (RoleGrants); an API key of a service account the roles of the account,
// another API key those of its owner (identity/apikey.go). Every level is
// capped by Principal.Permissions (AUT-04). An organization without field
// permissions yields the unrestricted policy without reading roles, teams
// or the type chain. It reads outside a transaction of ctx: an org-wide
// read cannot join the request's transaction (ErrTxScopeMismatch). A
// subject that is not a user or account id holds no roles (fail-closed).
func (r *FieldResolver) Resolve(ctx context.Context, p identity.Principal) (*FieldPolicy, error) { //nolint:gocritic // the principal is passed by value like identity.WithPrincipal
	ctx = database.WithoutTx(ctx)
	orgID := p.OrganizationID
	snap, err := r.snapshot(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if snap.Empty() {
		return r.unrestricted(ctx, orgID, &snap, p.Permissions)
	}
	var grants []identity.Grant
	var teams []string
	switch {
	case p.Type == identity.PrincipalTypeAPIKey && p.ServiceAccountID != "":
		if r.serviceAccounts != nil {
			if grants, err = r.serviceAccounts.ServiceAccountGrants(ctx, orgID, p.ServiceAccountID); err != nil {
				return nil, err
			}
		}
	case api.IsUUID(p.Subject):
		if grants, err = r.access.AccessGrants(ctx, orgID, p.Subject); err != nil {
			return nil, err
		}
		if p.Type != identity.PrincipalTypeAPIKey {
			roles, roleErr := r.access.RoleGrants(ctx, orgID, identity.StandardRolesForGroups(p.Groups))
			if roleErr != nil {
				return nil, roleErr
			}
			grants = append(grants, roles...)
		}
		if teams, err = r.repo.TeamIDs(ctx, orgID, p.Subject); err != nil {
			return nil, err
		}
	}
	parents, err := r.repo.TypeChain(ctx, orgID)
	if err != nil {
		return nil, err
	}
	policy := buildPolicy(&snap, grants, teams, parents, permissionSet(p.Permissions))
	policy.assetsOff = r.assetsOff(ctx, orgID)
	return policy, nil
}

// unrestricted returns the policy of an organization without field
// permissions; with computed asset sources it keeps them, the type chain
// and the state of assets_basic (SR-58).
func (r *FieldResolver) unrestricted(ctx context.Context, orgID string, snap *Snapshot, perms []identity.Permission) (*FieldPolicy, error) {
	policy := UnrestrictedPolicy(perms)
	if len(snap.Computed) == 0 {
		return policy, nil
	}
	parents, err := r.repo.TypeChain(ctx, orgID)
	if err != nil {
		return nil, err
	}
	policy.idx = newSnapshotIndex(snap)
	policy.parents = parents
	policy.assetsOff = r.assetsOff(ctx, orgID)
	return policy, nil
}

// PolicyForGrants returns the policy of grants and team memberships for a
// caller without request (webhooks, queue jobs): the levels are not capped
// beyond the grants. An organization without field permissions yields the
// unrestricted policy of the union of the grants' permissions.
func (r *FieldResolver) PolicyForGrants(ctx context.Context, orgID string, grants []identity.Grant, teamIDs []string) (*FieldPolicy, error) {
	ctx = database.WithoutTx(ctx)
	snap, err := r.snapshot(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if snap.Empty() {
		var perms []identity.Permission
		for i := range grants {
			perms = append(perms, grants[i].Permissions...)
		}
		return r.unrestricted(ctx, orgID, &snap, perms)
	}
	parents, err := r.repo.TypeChain(ctx, orgID)
	if err != nil {
		return nil, err
	}
	policy := buildPolicy(&snap, grants, teamIDs, parents, nil)
	policy.assetsOff = r.assetsOff(ctx, orgID)
	return policy, nil
}
