package server

import (
	"context"
	"errors"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// Adapters of the field permissions (RBA-07, E-43, WP-727) for the CI
// history and the resolved forms: the type, scope and instance attribute
// keys of a CI (overrideCILookup), the policy of the creator of a snapshot
// job and the citype.FieldPolicy of a form.

// historyCILookup gives history.Handler the facts of a CI (WithCIs).
type historyCILookup struct{ cis overrideCILookup }

// HistoryCIs is the CI lookup of the CI history (history.Handler.WithCIs):
// type, scope and instance attribute keys of a CI the caller sees.
func HistoryCIs(repos *Repositories) history.CILookup {
	return historyCILookup{cis: overrideCILookup{repo: repos.CI, fields: repos.CIType}}
}

func (l historyCILookup) CIFacts(ctx context.Context, orgID, ciID string) (*history.CIFacts, error) {
	scope, err := l.cis.FieldScope(ctx, orgID, ciID)
	if errors.Is(err, override.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &history.CIFacts{TypeID: scope.TypeID, ClientID: scope.ClientID, SiteID: scope.SiteID, InstanceKeys: scope.InstanceKeys}, nil
}

// SnapshotCreatorPolicy resolves the policy of the creator of a snapshot
// job (history.Handler.WithCreatorPolicy) from the creator's role
// assignments (AccessGrants) and teams (FieldResolver.PolicyForGrants):
// the queue runs the job without principal. A creator that is no user
// holds no grants.
func SnapshotCreatorPolicy(repos *Repositories, resolver *permission.FieldResolver) history.CreatorPolicy {
	return func(ctx context.Context, orgID, userID string) (*permission.FieldPolicy, error) {
		ctx = database.WithoutTx(ctx)
		var grants []identity.Grant
		var teamIDs []string
		if api.IsUUID(userID) {
			var err error
			if grants, err = repos.Permission.AccessGrants(ctx, orgID, userID); err != nil {
				return nil, err
			}
			if teamIDs, err = repos.FieldGrants.TeamIDs(ctx, orgID, userID); err != nil {
				return nil, err
			}
		}
		return resolver.PolicyForGrants(ctx, orgID, grants, teamIDs)
	}
}

// FormPolicyFactory builds the factory of the field policy of the resolved
// forms (citype.Handler.WithFormPolicy, WP-727): the policy of the request
// (permission.PolicyFrom), or with a preview of a role the policy of that
// role (FieldResolver.PolicyForGrants with the role's permissions,
// organization-wide), for the type and scope of the CI (a new CI of a type
// has no scope). A policy that restricts no CI field gives
// citype.NoopFieldPolicy, as in phase 1.
func FormPolicyFactory(repos *Repositories, resolver *permission.FieldResolver) citype.FormPolicyFactory {
	cis := overrideCILookup{repo: repos.CI, fields: repos.CIType}
	return func(ctx context.Context, orgID string, s citype.FormPolicySubject) (citype.FieldPolicy, error) {
		var policy *permission.FieldPolicy
		var err error
		if s.PreviewRoleID != "" {
			grant, gerr := previewGrant(ctx, repos, orgID, s.PreviewRoleID)
			if gerr != nil {
				return nil, gerr
			}
			policy, err = resolver.PolicyForGrants(ctx, orgID, []identity.Grant{grant}, nil)
		} else {
			policy, err = permission.PolicyFrom(ctx)
		}
		if err != nil {
			return nil, err
		}
		if !permission.NeedsProjection(policy, permission.KindCI) {
			return citype.NoopFieldPolicy{}, nil
		}
		f := &formFieldPolicy{policy: policy, typeID: s.CITypeID}
		if s.CIID != "" {
			scope, serr := cis.FieldScope(ctx, orgID, s.CIID)
			if errors.Is(serr, override.ErrNotFound) {
				return nil, citype.ErrFormCINotFound
			}
			if serr != nil {
				return nil, serr
			}
			f.typeID, f.instance = scope.TypeID, scope.InstanceKeys
			f.ref = permission.ObjectRef{ID: s.CIID, ClientID: scope.ClientID, SiteID: scope.SiteID}
		}
		return f, nil
	}
}

// previewGrant is the organization-wide grant of the role of a preview: a
// standard role with its permissions, otherwise a custom role. A role
// that is neither grants nothing (the resolution answers 404).
func previewGrant(ctx context.Context, repos *Repositories, orgID, roleID string) (identity.Grant, error) {
	grant := identity.Grant{}
	if repos.Permission != nil {
		perms, err := repos.Permission.ListRolePermissions(ctx, orgID, roleID)
		if err != nil {
			return grant, err
		}
		if len(perms) > 0 {
			grant.RoleID = roleID
			for i := range perms {
				grant.Permissions = append(grant.Permissions, identity.Permission(perms[i].PermissionKey))
			}
			return grant, nil
		}
	}
	if repos.User != nil {
		role, err := repos.User.GetRole(ctx, orgID, roleID)
		switch {
		case errors.Is(err, user.ErrNotFound):
		case err != nil:
			return grant, err
		case role != nil && !role.IsBuiltin:
			grant.CustomRoleID = roleID
			for _, p := range role.Permissions {
				grant.Permissions = append(grant.Permissions, identity.Permission(p))
			}
		}
	}
	return grant, nil
}

// formFieldPolicy is citype.FieldPolicy over a field policy for one CI:
// the field names of a form are the bare names (a typed column by its
// name, an attribute by its key, an instance attribute as _instance.<key>).
type formFieldPolicy struct {
	policy   *permission.FieldPolicy
	typeID   string
	ref      permission.ObjectRef
	instance []string
}

// Readable implements citype.FieldPolicy.
func (f *formFieldPolicy) Readable(name string) bool {
	return permission.Readable(f.policy, permission.KindCI, f.typeID, f.ref, permission.CIFieldPath(name),
		permission.WithInstanceKeys(f.instance))
}

// Editable implements citype.FieldPolicy.
func (f *formFieldPolicy) Editable(name string) bool {
	return permission.CheckWrite(f.policy, permission.KindCI, f.typeID, f.ref, []string{permission.CIFieldPath(name)},
		permission.WithInstanceKeys(f.instance)) == nil
}
