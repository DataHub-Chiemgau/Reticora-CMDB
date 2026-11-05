package discovery

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// The client and site binding of enrollment codes (F-406, COL-04, E-08,
// migration 000187).

// enrollmentBinding returns the client and site an enrollment code binds
// its collector to (F-406, COL-04, E-08). The creator's scope for
// collector:manage (enrollmentScope) decides per dimension: a requested
// id must lie in it (403 otherwise); without one, a creator restricted to
// exactly one client or site is bound to it, one restricted to several must
// choose (422), one restricted to none may not enroll (403). An org-wide
// dimension stays unbound ("") unless an id is requested: the collector
// then names its client at enrollment, as POST /collectors allows such a
// creator. The site decides the client (migration 000187).
func enrollmentBinding(ctx context.Context, clientID, siteID string) (client, site string, err error) {
	clients, sites, ok := enrollmentScope(ctx)
	if !ok {
		return "", "", &api.FieldError{Field: "client_id", Message: "no scope to register collectors in", Status: http.StatusForbidden}
	}
	pick := func(set identity.ScopeSet, id, field, plural string) (string, error) {
		id = strings.TrimSpace(id)
		if id != "" {
			if !api.IsUUID(id) {
				return "", &api.FieldError{Field: field, Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
			}
			if !set.All && !slices.ContainsFunc(set.IDs, func(s string) bool { return strings.EqualFold(s, id) }) {
				return "", &api.FieldError{Field: field, Message: "outside your scope", Status: http.StatusForbidden}
			}
			return strings.ToLower(id), nil
		}
		switch {
		case set.All:
			return "", nil
		case len(set.IDs) == 1:
			return strings.ToLower(set.IDs[0]), nil
		case len(set.IDs) == 0:
			return "", &api.FieldError{Field: field, Message: "no " + plural + " in your scope", Status: http.StatusForbidden}
		}
		return "", &api.FieldError{Field: field, Message: "required: your scope holds several " + plural, Status: http.StatusUnprocessableEntity}
	}
	if site, err = pick(sites, siteID, "site_id", "sites"); err != nil {
		return "", "", err
	}
	// With a site and an org-wide client dimension the site decides the
	// client (collector_enrollment_code_site_binding); a restricted client
	// dimension is checked against the site's client there.
	if site != "" && clientID == "" && clients.All {
		return "", site, nil
	}
	if client, err = pick(clients, clientID, "client_id", "clients"); err != nil {
		return "", "", err
	}
	return client, site, nil
}

// enrollmentScope returns the client and site scope in which the principal
// holds collector:manage: the scope of the permission for a session with
// resolved role scopes (identity.Principal.ScopeFor), the tenant scope of
// the request otherwise (API keys, whose client scope RLS applies). ok is
// false without either (fail-closed).
func enrollmentScope(ctx context.Context) (clients, sites identity.ScopeSet, ok bool) {
	if p, has := identity.PrincipalFromContext(ctx); has && p.Scope != nil {
		s, held := p.ScopeFor(identity.PermCollectorManage)
		if !held {
			return identity.ScopeSet{}, identity.ScopeSet{}, false
		}
		// The permission's scope lies in the session's; the request scope
		// RLS applies stays the outer bound.
		if ts, tok := database.TenantScopeFromContext(ctx); tok {
			return intersectScope(s.Clients, ts.Clients), intersectScope(s.Sites, ts.Sites), true
		}
		return s.Clients, s.Sites, true
	}
	ts, has := database.TenantScopeFromContext(ctx)
	if !has {
		return identity.ScopeSet{}, identity.ScopeSet{}, false
	}
	conv := func(set database.ScopeSet) identity.ScopeSet {
		if set.IsAll() {
			return identity.ScopeSet{All: true}
		}
		return identity.ScopeSet{IDs: set.IDs()}
	}
	return conv(ts.Clients), conv(ts.Sites), true
}

// intersectScope is the part of a permission's scope set that the request
// scope set also holds.
func intersectScope(perm identity.ScopeSet, req database.ScopeSet) identity.ScopeSet {
	if req.IsAll() {
		return perm
	}
	ids := req.IDs()
	if perm.All {
		return identity.ScopeSet{IDs: ids}
	}
	var out []string
	for _, id := range perm.IDs {
		if slices.ContainsFunc(ids, func(s string) bool { return strings.EqualFold(s, id) }) {
			out = append(out, id)
		}
	}
	return identity.ScopeSet{IDs: out}
}

// applyEnrollmentBinding binds the collector to the client and site of its
// enrollment code (F-406): a client or site the request names must be the
// code's (422 otherwise, nothing is registered and the code stays unused);
// without one the collector takes the code's. A dimension the code leaves
// unbound keeps the requested value.
func applyEnrollmentBinding(clientID, siteID string, c *Collector) error {
	bind := func(bound string, requested *string, field string) error {
		if bound == "" {
			return nil
		}
		if *requested != "" && !strings.EqualFold(strings.TrimSpace(*requested), bound) {
			return &api.FieldError{Field: field, Message: "outside the binding of the enrollment code", Status: http.StatusUnprocessableEntity}
		}
		*requested = bound
		return nil
	}
	if err := bind(clientID, &c.ClientID, "client_id"); err != nil {
		return err
	}
	return bind(siteID, &c.SiteID, "site_id")
}
