package notify

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Audience is who a notification is meant for (NTF-03): users by role,
// team or subscription, and the contacts of a CI by contact role. Users
// receive it only if they may read the object: they need Permission in
// the object's client and site.
type Audience struct {
	// Permission is the read permission of the object (e.g. ci:read).
	Permission identity.Permission
	// ClientID and SiteID place the object; empty: organization-wide.
	ClientID, SiteID string
	// Roles names roles of the organization (role.name); their holders
	// are candidates.
	Roles []string
	// TeamIDs adds the members of teams.
	TeamIDs []string
	// UserIDs adds individual users (subscriptions).
	UserIDs []string
	// AllWithPermission makes every user of the organization a candidate
	// (filtered by Permission and scope).
	AllWithPermission bool
	// CIID and ContactRoles add the contacts of a CI in these roles
	// (owner, escalation, operator, responsible, vendor).
	CIID         string
	ContactRoles []string
}

// GrantSource reads the role grants of a user (permission repository).
type GrantSource interface {
	AccessGrants(ctx context.Context, orgID, userID string) ([]identity.Grant, error)
}

// Resolver turns an audience into recipients.
type Resolver interface {
	Resolve(ctx context.Context, orgID string, a *Audience) ([]Recipient, error)
}

// PGResolver resolves audiences against PostgreSQL.
type PGResolver struct {
	pool   *pgxpool.Pool
	grants GrantSource
}

// NewPGResolver creates the resolver.
func NewPGResolver(pool *pgxpool.Pool, grants GrantSource) *PGResolver {
	return &PGResolver{pool: pool, grants: grants}
}

type candidate struct {
	id, email, name string
}

// Resolve implements Resolver: active users among the candidates who hold
// the permission in the object's scope, then the CI contacts; each address
// once, users before contacts, ordered by name.
func (r *PGResolver) Resolve(ctx context.Context, orgID string, a *Audience) ([]Recipient, error) {
	if a.Permission == "" && (len(a.Roles) > 0 || len(a.TeamIDs) > 0 || len(a.UserIDs) > 0 || a.AllWithPermission) {
		return nil, fmt.Errorf("an audience of users needs the object's read permission")
	}
	var users []candidate
	var contacts []Recipient
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if len(a.Roles) > 0 || len(a.TeamIDs) > 0 || len(a.UserIDs) > 0 || a.AllWithPermission {
			rows, err := tx.Query(ctx, `
				SELECT u.id::text, COALESCE(u.email, ''), COALESCE(u.display_name, '')
				FROM app_user u
				WHERE u.organization_id = $1 AND u.is_active AND (
					$5
					OR u.id::text = ANY($2)
					OR EXISTS (SELECT 1 FROM role_assignment ra JOIN role ro ON ro.id = ra.role_id
					           WHERE ra.organization_id = $1 AND ra.user_id = u.id AND ro.name = ANY($3))
					OR EXISTS (SELECT 1 FROM team_member tm
					           WHERE tm.organization_id = $1 AND tm.user_id = u.id AND tm.team_id::text = ANY($4)))
				ORDER BY u.display_name, u.id`, orgID, a.UserIDs, a.Roles, a.TeamIDs, a.AllWithPermission)
			if err != nil {
				return fmt.Errorf("resolve users: %w", err)
			}
			users, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidate, error) {
				var c candidate
				scanErr := row.Scan(&c.id, &c.email, &c.name)
				return c, scanErr
			})
			if err != nil {
				return fmt.Errorf("resolve users: %w", err)
			}
		}
		if a.CIID != "" && len(a.ContactRoles) > 0 {
			rows, err := tx.Query(ctx, `
				SELECT DISTINCT c.email, c.display_name
				FROM ci_contact cc JOIN contact c ON c.id = cc.contact_id AND c.organization_id = cc.organization_id
				WHERE cc.organization_id = $1 AND cc.ci_id::text = $2 AND cc.relationship_type = ANY($3)
				  AND COALESCE(c.email, '') <> ''
				ORDER BY c.display_name, c.email`, orgID, a.CIID, a.ContactRoles)
			if err != nil {
				return fmt.Errorf("resolve contacts: %w", err)
			}
			contacts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recipient, error) {
				var c Recipient
				scanErr := row.Scan(&c.Email, &c.Name)
				return c, scanErr
			})
			if err != nil {
				return fmt.Errorf("resolve contacts: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := []Recipient{}
	seen := map[string]bool{}
	for _, u := range users {
		grants, err := r.grants.AccessGrants(ctx, orgID, u.id)
		if err != nil {
			return nil, fmt.Errorf("grants of %s: %w", u.id, err)
		}
		if !permission.Covers(grants, a.Permission, a.ClientID, a.SiteID) {
			continue
		}
		seen[u.id] = true
		if u.email != "" {
			seen["mail:"+u.email] = true
		}
		out = append(out, Recipient{UserID: u.id, Email: u.email, Name: u.name})
	}
	sort.SliceStable(contacts, func(i, j int) bool { return contacts[i].Name < contacts[j].Name })
	for _, c := range contacts {
		if seen["mail:"+c.Email] {
			continue
		}
		seen["mail:"+c.Email] = true
		out = append(out, c)
	}
	return out, nil
}
