package permission

import (
	"context"
	"errors"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Action grants (RBA-07, E-43, WP-728): an action is the permission key of
// a route (ci:delete, override:write, lifecycle:transition, …). Without an
// action grant the action is allowed when a role whose assignment covers
// the object holds the permission; an action grant none of the type (its
// nearest ancestor first, then without type) or an object grant none of
// the object refuses it with 403 and the extension action. The order on a
// route is route permission → action grant → field permissions →
// validation → If-Match.

// CheckAction refuses the action on the object of the kind and type when
// the policy does not allow it (FieldPolicy.ActionAllowed): a
// *api.FieldForbiddenError with Action and without fields. A nil policy
// allows nothing (fail-closed).
func CheckAction(policy *FieldPolicy, kind ObjectKind, typeID string, ref ObjectRef, action identity.Permission) error { //nolint:gocritic // ObjectRef by value like FieldPolicy.Level
	if policy.ActionAllowed(kind, typeID, string(action), ref) {
		return nil
	}
	return &api.FieldForbiddenError{Fields: []string{}, Action: string(action)}
}

type actionKey struct{}

// WithAction names the action of the route for a write path several routes
// share (ci.Service.Update: PATCH with ci:write, the instance attribute
// routes with ci_instance_attribute:manage).
func WithAction(ctx context.Context, action identity.Permission) context.Context {
	return context.WithValue(ctx, actionKey{}, action)
}

// ActionFrom returns the action WithAction put into ctx; without one it is
// ci:write.
func ActionFrom(ctx context.Context) identity.Permission {
	if action, ok := ctx.Value(actionKey{}).(identity.Permission); ok && action != "" {
		return action
	}
	return identity.PermCIWrite
}

// WithPolicySource puts the policy source of a request into ctx outside
// HTTP, as FieldPolicyMiddleware does for a request: PolicyFrom resolves
// the policy of the principal of ctx once, on first need.
func WithPolicySource(ctx context.Context, resolver PolicyResolver) context.Context {
	return context.WithValue(ctx, policySourceKey{}, &policySource{resolver: resolver})
}

// ActionTarget is one object an action is checked on: its type (the CI
// type of a ci or asset) and its id and scope.
type ActionTarget struct {
	TypeID string
	Ref    ObjectRef
}

// ErrNoActionTarget is the error of a loader of CheckActionOn that cannot
// read the type and scope of the object although the policy needs them:
// the caller answers 500 (fail-closed).
var ErrNoActionTarget = errors.New("action permissions: the object of the action cannot be read")

// NeedsActionTarget reports whether an action check of the policy depends
// on the type and scope of the object: false only for the policy of an
// organization without field permissions, which allows an action with its
// permission alone (phase 1).
func (p *FieldPolicy) NeedsActionTarget() bool {
	return p == nil || !p.unrestricted
}

// actionPolicy returns the policy action checks of ctx run with, whether
// it was put into ctx by WithPolicy (a job that resolved its creator,
// tests) and whether checks run: else, with a principal, the policy of the
// request (PolicyFrom, ErrNoPolicySource without source). Without
// principal and without policy (ingest, system jobs) nothing is checked,
// as for the field permissions (WP-265).
func actionPolicy(ctx context.Context) (policy *FieldPolicy, fixed, check bool, err error) {
	if src, ok := ctx.Value(policySourceKey{}).(*policySource); ok && src.fixed != nil {
		return src.fixed, true, true, nil
	}
	if _, ok := identity.PrincipalFromContext(ctx); !ok {
		return nil, false, false, nil
	}
	if policy, err = PolicyFrom(ctx); err != nil {
		return nil, false, false, err
	}
	return policy, false, true, nil
}

// CheckActionOn checks the action on the objects load returns with the
// policy of ctx (CheckAction per object, the first refusal wins). load runs
// only when the policy needs the objects (NeedsActionTarget): a write of
// an organization without field permissions reads nothing more. There the
// route permission of the request governs as in phase 1 (the router checked
// it; the creator of a merge job reverts it without ci:delete), while a
// policy WithPolicy put into ctx (a queue job) still needs the permission
// of the action (a right revoked after the start takes effect). An object
// load does not return (not visible) is not checked; the operation itself
// answers it. Without principal and policy nothing is checked.
func CheckActionOn(ctx context.Context, kind ObjectKind, action identity.Permission, load func(context.Context) ([]ActionTarget, error)) error {
	policy, fixed, check, err := actionPolicy(ctx)
	if err != nil || !check {
		return err
	}
	if !policy.NeedsActionTarget() {
		if !fixed {
			return nil
		}
		return CheckAction(policy, kind, "", ObjectRef{}, action)
	}
	if load == nil {
		return ErrNoActionTarget
	}
	targets, err := load(ctx)
	if err != nil {
		return err
	}
	for i := range targets {
		if err := CheckAction(policy, kind, targets[i].TypeID, targets[i].Ref, action); err != nil {
			return err
		}
	}
	return nil
}

// CreatorStatus reports whether the creator of a job is an active user of
// the organization (user.PGRepository.UserActive, as the session refresh
// asks it).
type CreatorStatus interface {
	UserActive(ctx context.Context, orgID, userID string) (bool, error)
}

// ErrCreatorInactive refuses the policy of a job whose creator is no
// active user of the organization (deactivated, deleted, a service
// account or no user id): the job runs with no right (fail-closed).
var ErrCreatorInactive = errors.New("field permissions: the creator of the job is not an active user")

// CreatorScope is the scope snapshot of a job row (jobs.Scope): nil in a
// dimension is unrestricted, an empty list covers nothing.
type CreatorScope struct {
	Clients []string
	Sites   []string
	Teams   []string
}

// PolicyForCreator returns the policy of the user who created a job, for
// a queue job that runs without principal (ci.bulk, WP-728): the user must
// be active (users), the rights are the user's current role assignments
// (AccessGrants, as login and refresh resolve them; a right revoked after
// the start takes effect) with the user's teams, each assignment narrowed
// to the scope snapshot of the job row (nil: none, every assignment is
// dropped), so the run never reaches beyond the request that started it.
// Standard roles of IdP groups do not count: the groups of the session are
// not stored. Nothing of it is stored in the job parameters.
func (r *FieldResolver) PolicyForCreator(ctx context.Context, users CreatorStatus, orgID, userID string, scope *CreatorScope) (*FieldPolicy, error) {
	if users == nil || !api.IsUUID(userID) {
		return nil, ErrCreatorInactive
	}
	active, err := users.UserActive(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrCreatorInactive
	}
	held, err := r.access.AccessGrants(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	var grants []identity.Grant
	if scope != nil {
		for i := range held {
			if g, ok := narrowGrant(&held[i], scope); ok {
				grants = append(grants, g)
			}
		}
	}
	teams, err := r.repo.TeamIDs(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	return r.PolicyForGrants(ctx, orgID, grants, teams)
}

// narrowGrant limits a role assignment to the scope snapshot; false when
// it covers nothing of it.
func narrowGrant(g *identity.Grant, scope *CreatorScope) (identity.Grant, bool) {
	out := *g
	var ok bool
	if out.Clients, ok = narrowDimension(g.Clients, scope.Clients); !ok {
		return identity.Grant{}, false
	}
	if out.Sites, ok = narrowDimension(g.Sites, scope.Sites); !ok {
		return identity.Grant{}, false
	}
	if out.Teams, ok = narrowDimension(g.Teams, scope.Teams); !ok {
		return identity.Grant{}, false
	}
	return out, true
}

// narrowDimension intersects a dimension of an assignment (empty:
// unrestricted) with the snapshot (nil: unrestricted); false when nothing
// is left.
func narrowDimension(held, snap []string) ([]string, bool) {
	if snap == nil {
		return slices.Clone(held), true
	}
	var out []string
	for _, id := range snap {
		if len(held) == 0 || slices.Contains(held, id) {
			out = append(out, id)
		}
	}
	return out, len(out) > 0
}
