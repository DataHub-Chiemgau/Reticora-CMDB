package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// MFA binding of organization administrators (AUT-09, CH26, E-14, finding
// F-005, WP-358). The realm asks holders of the realm role mfa_required for
// a second factor (deploy/keycloak, browser flow browser-mfa); the app role
// org_admin lives in the database. Every grant of org_admin therefore maps
// mfa_required to the IdP account before it commits: role assignment
// (AssignRole), invitation acceptance (InvitationAccounts with the invited
// role), the first login with an invitation or of an account an
// administrator created (EnsureUser), and the reconciliation of the existing
// org_admins at start (ReconcileMFA). A failing binding fails the grant.
// The role is not taken away when org_admin is revoked: MFA stays.

// OrgAdminRole is the standard role whose holders must use MFA.
const OrgAdminRole = "org_admin"

// MFAEnforcer binds IdP accounts to the second factor (implemented by
// signup.KeycloakAccounts).
type MFAEnforcer interface {
	// RequireMFA makes the account of the OIDC subject (the Keycloak user
	// id) hold the realm role mfa_required; it is idempotent.
	RequireMFA(ctx context.Context, oidcSubject string) error
	// AccountByEmail returns the id of the IdP account with exactly this
	// e-mail address, or "" when there is none.
	AccountByEmail(ctx context.Context, email string) (string, error)
}

// ErrMFAUnavailable marks a grant of org_admin that failed because the
// account could not be bound to MFA (503).
var ErrMFAUnavailable = identity.ErrMFAUnavailable

// errNoAdminClient is the cause UnavailableMFA reports.
var errNoAdminClient = errors.New("no Keycloak admin client configured (RETICORA_KEYCLOAK_ADMIN_CLIENT_ID)")

// UnavailableMFA is the enforcer of an installation without Keycloak admin
// client outside development: it fails every binding, so no org_admin is
// granted without MFA (fail closed).
type UnavailableMFA struct{}

// RequireMFA implements MFAEnforcer and always fails.
func (UnavailableMFA) RequireMFA(context.Context, string) error { return errNoAdminClient }

// AccountByEmail implements MFAEnforcer and always fails.
func (UnavailableMFA) AccountByEmail(context.Context, string) (string, error) {
	return "", errNoAdminClient
}

// bindMFA binds the account of a new org_admin. Without subject (a user
// created through POST /users that never signed in) the account is looked
// up by e-mail; when there is none yet, the first login binds the account
// it links. A nil enforcer (tests, development without admin client) binds
// nothing.
func bindMFA(ctx context.Context, m MFAEnforcer, subject, email string) error {
	if m == nil {
		return nil
	}
	if subject == "" {
		id, err := m.AccountByEmail(ctx, email)
		if err != nil {
			return fmt.Errorf("%w: find the account of the address: %w", ErrMFAUnavailable, err)
		}
		if id == "" {
			return nil
		}
		subject = id
	}
	if err := m.RequireMFA(ctx, subject); err != nil {
		return fmt.Errorf("%w: %w", ErrMFAUnavailable, err)
	}
	return nil
}

// holdsOrgAdmin reports whether the user holds the standard role org_admin
// in any scope.
func holdsOrgAdmin(ctx context.Context, tx pgx.Tx, orgID, userID string) (bool, error) {
	var held bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM role_assignment ra JOIN role ro ON ro.id = ra.role_id AND ro.organization_id = ra.organization_id
			WHERE ra.organization_id = $1 AND ra.user_id::text = $2 AND ro.name = $3)
	`, orgID, userID, OrgAdminRole).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("read org_admin assignment: %w", err)
	}
	return held, nil
}

// ReconcileMFA binds every active org_admin with an IdP account (oidc
// subject) of every organization to MFA: the org_admins that signup,
// promotion or invitation created before the binding existed. It reads each
// organization in its own tenant transaction and is idempotent. A failing
// binding is logged per user and does not stop the run; it returns how many
// accounts were bound and how many failed.
func (r *PGRepository) ReconcileMFA(ctx context.Context) (bound, failed int, err error) {
	if r.mfa == nil {
		return 0, 0, nil
	}
	var orgs []string
	err = database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if qerr != nil {
			return qerr
		}
		orgs, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
		return qerr
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list organizations: %w", err)
	}
	type admin struct{ id, subject string }
	for _, org := range orgs {
		var admins []admin
		rerr := database.WithTenant(ctx, r.pool, loginScope(org), func(ctx context.Context, tx pgx.Tx) error {
			rows, qerr := tx.Query(ctx, `
				SELECT DISTINCT u.id::text, u.oidc_subject FROM app_user u
				JOIN role_assignment ra ON ra.user_id = u.id AND ra.organization_id = u.organization_id
				JOIN role ro ON ro.id = ra.role_id AND ro.organization_id = ra.organization_id
				WHERE u.organization_id = $1 AND u.is_active AND u.oidc_subject <> '' AND ro.name = $2
				ORDER BY 1
			`, org, OrgAdminRole)
			if qerr != nil {
				return qerr
			}
			admins, qerr = pgx.CollectRows(rows, func(row pgx.CollectableRow) (admin, error) {
				var a admin
				return a, row.Scan(&a.id, &a.subject)
			})
			return qerr
		})
		if rerr != nil {
			slog.ErrorContext(ctx, "mfa reconciliation: read org_admins", "organization_id", org, "error", rerr)
			failed++
			continue
		}
		for _, a := range admins {
			if berr := bindMFA(ctx, r.mfa, a.subject, ""); berr != nil {
				slog.ErrorContext(ctx, "mfa reconciliation: org_admin not bound to MFA",
					"organization_id", org, "user_id", a.id, "error", berr)
				failed++
				continue
			}
			bound++
		}
	}
	return bound, failed, nil
}
