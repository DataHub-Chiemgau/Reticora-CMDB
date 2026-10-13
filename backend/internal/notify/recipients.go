package notify

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
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
	// DirectUserIDs are users the notification concerns personally (the
	// initiator of an export): active users, without the permission
	// check. Direct are addresses outside the organization's users (an
	// invitee).
	DirectUserIDs []string
	Direct        []Recipient
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
	var users, direct []candidate
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
		if len(a.DirectUserIDs) > 0 {
			rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(email, ''), COALESCE(display_name, '') FROM app_user
				WHERE organization_id = $1 AND is_active AND id::text = ANY($2) ORDER BY display_name, id`, orgID, a.DirectUserIDs)
			if err != nil {
				return fmt.Errorf("resolve direct users: %w", err)
			}
			direct, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidate, error) {
				var c candidate
				scanErr := row.Scan(&c.id, &c.email, &c.name)
				return c, scanErr
			})
			if err != nil {
				return fmt.Errorf("resolve direct users: %w", err)
			}
		}
		if a.CIID != "" && len(a.ContactRoles) > 0 {
			// Name and e-mail of a contact live in the PII vault (AUD-01);
			// rows not moved there yet keep them in the columns.
			rows, err := tx.Query(ctx, `
				SELECT DISTINCT COALESCE(c.email, ''), COALESCE(c.display_name, ''), COALESCE(c.pii_subject_id::text, '')
				FROM ci_contact cc JOIN contact c ON c.id = cc.contact_id AND c.organization_id = cc.organization_id
				WHERE cc.organization_id = $1 AND cc.ci_id::text = $2 AND cc.relationship_type = ANY($3)`,
				orgID, a.CIID, a.ContactRoles)
			if err != nil {
				return fmt.Errorf("resolve contacts: %w", err)
			}
			type linked struct{ email, name, subject string }
			found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linked, error) {
				var c linked
				scanErr := row.Scan(&c.email, &c.name, &c.subject)
				return c, scanErr
			})
			if err != nil {
				return fmt.Errorf("resolve contacts: %w", err)
			}
			var subjects map[string]*pii.Subject
			if vault := pii.Default(); vault != nil {
				ids := make([]string, 0, len(found))
				for _, c := range found {
					if c.subject != "" {
						ids = append(ids, c.subject)
					}
				}
				if subjects, err = vault.ResolveTx(ctx, tx, orgID, ids); err != nil {
					return fmt.Errorf("resolve contacts: %w", err)
				}
			}
			for _, c := range found {
				if s := subjects[c.subject]; s != nil {
					c.email, c.name = s.Data["email"], s.Data["name"]
				}
				if c.email != "" {
					contacts = append(contacts, Recipient{Email: c.email, Name: c.name})
				}
			}
			sort.Slice(contacts, func(i, j int) bool {
				if contacts[i].Name != contacts[j].Name {
					return contacts[i].Name < contacts[j].Name
				}
				return contacts[i].Email < contacts[j].Email
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := []Recipient{}
	seen := map[string]bool{}
	add := func(r Recipient) {
		if (r.UserID != "" && seen[r.UserID]) || (r.Email != "" && seen["mail:"+strings.ToLower(r.Email)]) {
			return
		}
		if r.UserID != "" {
			seen[r.UserID] = true
		}
		if r.Email != "" {
			seen["mail:"+strings.ToLower(r.Email)] = true
		}
		out = append(out, r)
	}
	for _, u := range direct {
		add(Recipient{UserID: u.id, Email: u.email, Name: u.name})
	}
	for _, u := range users {
		grants, err := r.grants.AccessGrants(ctx, orgID, u.id)
		if err != nil {
			return nil, fmt.Errorf("grants of %s: %w", u.id, err)
		}
		if !permission.Covers(grants, a.Permission, a.ClientID, a.SiteID) {
			continue
		}
		add(Recipient{UserID: u.id, Email: u.email, Name: u.name})
	}
	sort.SliceStable(contacts, func(i, j int) bool { return contacts[i].Name < contacts[j].Name })
	for _, c := range contacts {
		add(c)
	}
	for _, r := range a.Direct {
		add(r)
	}
	return out, nil
}
