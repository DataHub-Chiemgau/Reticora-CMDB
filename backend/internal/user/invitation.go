package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Invitations (TLC-03): an administrator invites an address with a
// predefined role and scope; the invitation is valid for InvitationTTL and
// accepted once, atomically, which creates the app_user with that role.
// Expired invitations can be renewed. Only the SHA-256 hash of the token is
// stored; the token goes to the invitee in the link of the invitation mail
// (NTF-05). With an IdP admin client (InvitationHandler.WithAccounts) the
// acceptance creates a verified IdP account of the address, so the token
// reaches that mailbox only: issue and renewal send the mail straight over
// the central SMTP server (InvitationDelivery.Mailer), and neither their
// response nor the event invitation.created carries it. Without the client
// the response of issue and renewal shows the token once, and the
// notifier mails the link of invitation.created over the organization's
// e-mail channels.

// InvitationTTL is the validity of an invitation (TLC-03).
const InvitationTTL = 7 * 24 * time.Hour

// Invitation errors.
var (
	ErrInvitationNotFound = errors.New("invitation not found")
	// ErrInvitationInvalid: unknown, revoked or already accepted token.
	ErrInvitationInvalid = errors.New("the invitation is invalid or was already accepted")
	ErrInvitationExpired = errors.New("the invitation has expired; ask for a renewal")
	// ErrInvitationAccepted: an accepted invitation cannot be renewed or revoked.
	ErrInvitationAccepted = errors.New("the invitation was already accepted")
	// ErrInvitationPending: the address already has a pending invitation.
	ErrInvitationPending = errors.New("a pending invitation for this address exists; renew it instead")
	// ErrInvitationUserExists: the organization already has a user with the address.
	ErrInvitationUserExists = errors.New("the organization already has a user with this e-mail address")
	// ErrInvitationMail: the central SMTP server did not take the
	// invitation mail; the issue or renewal was rolled back.
	ErrInvitationMail = errors.New("the invitation e-mail could not be sent")
)

// invitationMailTimeout bounds the send of an invitation mail. It runs
// inside the transaction of the issue or renewal (holding row locks only,
// see sendInvitation) and stays below the server's write timeout (15 s,
// cmd/server), so a slow SMTP server ends in ErrInvitationMail rather than
// in a lost response.
const invitationMailTimeout = 10 * time.Second

// Invitation statuses, derived from accepted_at and expires_at.
const (
	InvitationPending  = "pending"
	InvitationAccepted = "accepted"
	InvitationExpired  = "expired"
)

// Invitation is a user_invitation row.
type Invitation struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Email          string     `json:"email"`
	RoleID         string     `json:"role_id"`
	ScopeType      string     `json:"scope_type"`
	ScopeID        string     `json:"scope_id,omitempty"`
	InvitedBy      string     `json:"invited_by"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	// Token is set only in the response of issue and renewal, and only
	// without an IdP admin client (InvitationHandler.tokenInResponse).
	Token string `json:"token,omitempty"`
}

// AcceptedInvitation is the result of an acceptance.
type AcceptedInvitation struct {
	UserID         string `json:"user_id"`
	OrganizationID string `json:"organization_id"`
	Email          string `json:"email"`
	// AccountCreated reports whether an IdP account was created with the
	// given password; otherwise the user signs in with an existing IdP
	// account of the address, which the first login links.
	AccountCreated bool `json:"account_created"`
}

// InvitationAccounts creates the IdP account of an accepting user (the
// Keycloak admin client, see internal/signup). role is the name of the
// invited standard role: an org_admin account is bound to MFA (WP-358); when
// that fails, the account is deleted again and the error wraps
// ErrMFAUnavailable.
type InvitationAccounts interface {
	CreateInvitedAccount(ctx context.Context, email, displayName, password, orgID, role string) (string, error)
	DeleteAccount(ctx context.Context, id string) error
}

// InvitationMailer sends the invitation mail straight to the invited
// address; the server uses notify.InvitationMailer over the central SMTP
// server of the installation (TEC-13).
type InvitationMailer interface {
	SendInvitation(ctx context.Context, mail *notify.InvitationMail) error
}

// InvitationDelivery is how an issue or renewal delivers the acceptance
// link of its token (NTF-05).
type InvitationDelivery struct {
	// Link is the acceptance link of the token.
	Link string
	// Mailer, when set, sends the invitation mail straight to the invited
	// address inside the transaction, before the audit entry and the event
	// take the organization's advisory locks (sendInvitation): when the SMTP
	// server does not take it, the issue or renewal rolls back
	// (ErrInvitationMail).
	// The event invitation.created then carries no link, so no channel of
	// the organization (e.g. its own SMTP server) and no other consumer of
	// the event gets the token. Without Mailer the event carries the link,
	// and the notifier mails it over the organization's e-mail channels.
	Mailer InvitationMailer
}

// InvitationRepository persists invitations.
type InvitationRepository interface {
	// IssueInvitation stores the invitation if caller may grant its role in
	// its scope (grant ceiling, GrantCeilingError), and in the same
	// transaction the event invitation.created; delivery delivers the
	// acceptance link of the token.
	IssueInvitation(ctx context.Context, caller *identity.Principal, inv *Invitation, tokenHash string, delivery InvitationDelivery) error
	// ListInvitations returns a page newest first on (created_at, id), at
	// most one invitation more than the limit, and the number of
	// invitations (API-02, see api.NewestFirstSQL).
	ListInvitations(ctx context.Context, orgID string, page api.PaginationParams) ([]Invitation, int, error)
	// RenewInvitation checks the grant ceiling against caller, the renewing
	// principal, not against the original issuer; like IssueInvitation it
	// appends invitation.created and delivers the link of the new token.
	RenewInvitation(ctx context.Context, orgID, id string, caller *identity.Principal, tokenHash string, delivery InvitationDelivery, expiresAt time.Time) (*Invitation, error)
	RevokeInvitation(ctx context.Context, orgID, id string) error
	// AcceptInvitation consumes the invitation of tokenHash atomically and
	// once, creates the user with the invited role and scope, and runs
	// createAccount with the name of the invited role inside the
	// transaction. With quota, max_users is checked against the active
	// users first (ENT-03); its refusal rolls back: the invitation stays
	// open and createAccount does not run.
	AcceptInvitation(ctx context.Context, orgID, tokenHash, displayName string, quota QuotaGuard,
		createAccount func(ctx context.Context, inv *Invitation, role string) error) (*AcceptedInvitation, error)
}

// InvitationLink returns the acceptance link of token: acceptURL with the
// query parameter token.
func InvitationLink(acceptURL *url.URL, token string) string {
	u := *acceptURL
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

// NewInvitationToken returns a token for orgID and its stored hash. The
// token names the organization so the acceptance runs in its tenant
// context; the random part is the credential.
func NewInvitationToken(orgID string) (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("invitation token: %w", err)
	}
	token = orgID + "." + base64.RawURLEncoding.EncodeToString(b)
	return token, HashInvitationToken(token), nil
}

// HashInvitationToken is the stored form of a token.
func HashInvitationToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// InvitationOrg returns the organization a token names.
func InvitationOrg(token string) (string, bool) {
	orgID, secret, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || secret == "" || !api.IsUUID(orgID) {
		return "", false
	}
	return strings.ToLower(orgID), true
}

// validateInvitation checks the request fields of an issue.
func validateInvitation(inv *Invitation) error {
	inv.Email = strings.TrimSpace(inv.Email)
	if addr, err := mail.ParseAddress(inv.Email); err != nil || addr.Address != inv.Email {
		return &api.FieldError{Field: "email", Message: "email must be a plain e-mail address"}
	}
	if !api.IsUUID(inv.RoleID) {
		return &api.FieldError{Field: "role_id", Message: "role_id must be a role id"}
	}
	switch inv.ScopeType {
	case "":
		inv.ScopeType = "org"
		fallthrough
	case "org":
		if inv.ScopeID != "" {
			return &api.FieldError{Field: "scope_id", Message: "an organization-wide invitation has no scope_id"}
		}
	case "client", "site":
		if !api.IsUUID(inv.ScopeID) {
			return &api.FieldError{Field: "scope_id", Message: "scope_id must name the " + inv.ScopeType}
		}
	default:
		return &api.FieldError{Field: "scope_type", Message: "scope_type must be org, client or site"}
	}
	return nil
}

// PGInvitations stores invitations in user_invitation.
type PGInvitations struct {
	pool *pgxpool.Pool
}

// NewPGInvitations creates the repository.
func NewPGInvitations(pool *pgxpool.Pool) *PGInvitations { return &PGInvitations{pool: pool} }

const invitationColumns = `id::text, organization_id::text, email, role_id::text, scope_type,
	COALESCE(scope_id::text, ''), invited_by::text, expires_at, accepted_at, created_at`

func scanInvitation(row pgx.Row) (*Invitation, error) {
	var inv Invitation
	if err := row.Scan(&inv.ID, &inv.OrganizationID, &inv.Email, &inv.RoleID, &inv.ScopeType, &inv.ScopeID,
		&inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
		return nil, err
	}
	inv.Status = invitationStatus(inv.AcceptedAt, inv.ExpiresAt, time.Now())
	return &inv, nil
}

func invitationStatus(acceptedAt *time.Time, expiresAt, now time.Time) string {
	switch {
	case acceptedAt != nil:
		return InvitationAccepted
	case !now.Before(expiresAt):
		return InvitationExpired
	}
	return InvitationPending
}

func recordInvitationAudit(ctx context.Context, tx pgx.Tx, orgID, actor, id, action string, changes map[string]any) error {
	actorType := "user"
	if actor == "" {
		actorType = "system"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID, ActorID: actor, ActorType: actorType, Action: action,
		ResourceType: "user_invitation", ResourceID: id, Changes: changes,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// sendInvitation is the first of the two steps in which an issue or renewal
// delivers inv in its transaction tx (NTF-05, InvitationDelivery). It runs
// right after the INSERT or UPDATE of the invitation, reads the
// organization as the mail and the event name it and, when delivery has a
// mailer, sends the mail straight to the invited address; a refused send
// rolls the issue or renewal back. Only then follow the audit entry and
// appendInvitationCreated: both take advisory locks of the organization
// (the audit chain, audit.AcquireAdvisoryLock and migration 000117; the
// event outbox, migration 000120) that last until commit, and holding them
// through the SMTP conversation would stall every audited or event-writing
// write of the organization for as long as the send takes. During the send
// tx holds row locks only: the invitation row and, on an issue, the
// foreign-key share locks on its organization and role rows. Should the
// audit entry, the event or the commit fail after the send, the mailed link
// names no stored token, and the acceptance answers 404.
func sendInvitation(ctx context.Context, tx pgx.Tx, inv *Invitation, delivery InvitationDelivery) (invitationOrganization, error) {
	var org invitationOrganization
	if err := tx.QueryRow(ctx, `SELECT name, locale, COALESCE(settings->'branding'->>'sender_name', '')
		FROM organization WHERE id = $1`, inv.OrganizationID).Scan(&org.Name, &org.Locale, &org.SenderName); err != nil {
		return org, fmt.Errorf("read organization: %w", err)
	}
	return org, delivery.send(ctx, inv, org)
}

// appendInvitationCreated appends invitation.created for inv in tx, the
// second step of the delivery after sendInvitation and the audit entry.
// Without a mailer the event carries the link, and its notification mails
// it over the organization's e-mail channels; with one it leaves the link
// out. The address travels as a vault surrogate (SEC-05); webhook
// deliveries leave out the link (webhook.go).
func appendInvitationCreated(ctx context.Context, tx pgx.Tx, inv *Invitation, delivery InvitationDelivery, orgName string) error {
	_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: inv.OrganizationID, Type: events.InvitationCreated,
		EntityID: inv.ID, Data: delivery.eventData(inv, orgName)})
	return err
}

// invitationOrganization is the organization of an invitation as its mail
// names it: name, locale (LOC-01) and settings.branding.sender_name.
type invitationOrganization struct {
	Name, Locale, SenderName string
}

// eventData is the data of invitation.created for inv: the link only
// without a mailer, when the notifier mails it over the organization's
// e-mail channels.
func (d InvitationDelivery) eventData(inv *Invitation, orgName string) map[string]any {
	data := map[string]any{"id": inv.ID, "email": inv.Email, "organization_name": orgName,
		"expires_at": inv.ExpiresAt.UTC().Format(time.RFC3339)}
	if d.Mailer == nil {
		data["link"] = d.Link
	}
	return data
}

// send sends the invitation mail of inv straight to the invited address
// when d has a mailer; a refused send is ErrInvitationMail.
func (d InvitationDelivery) send(ctx context.Context, inv *Invitation, org invitationOrganization) error {
	if d.Mailer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, invitationMailTimeout)
	defer cancel()
	if err := d.Mailer.SendInvitation(ctx, &notify.InvitationMail{To: inv.Email, Link: d.Link, ExpiresAt: inv.ExpiresAt,
		OrganizationName: org.Name, Locale: org.Locale, SenderName: org.SenderName}); err != nil {
		return fmt.Errorf("%w: %w", ErrInvitationMail, err)
	}
	return nil
}

// IssueInvitation implements InvitationRepository. The role and the scope
// must exist in the organization and be visible to the issuer (RLS), the
// role must fit the scope, and the issuer must hold user:manage and the
// role's permissions in a scope covering it (checkInvitationTargets).
func (r *PGInvitations) IssueInvitation(ctx context.Context, caller *identity.Principal, inv *Invitation, tokenHash string, delivery InvitationDelivery) error {
	if err := validateInvitation(inv); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, inv.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkInvitationTargets(ctx, tx, caller, inv); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_invitation WHERE organization_id = $1
			AND lower(email) = lower($2) AND accepted_at IS NULL AND expires_at > now())`,
			inv.OrganizationID, inv.Email).Scan(&pending); err != nil {
			return fmt.Errorf("check pending invitation: %w", err)
		}
		if pending {
			return &api.ConflictError{Field: "email", Message: ErrInvitationPending.Error()}
		}
		created, err := scanInvitation(tx.QueryRow(ctx, `INSERT INTO user_invitation
			(organization_id, email, role_id, scope_type, scope_id, invited_by, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid, $6, $7, $8)
			RETURNING `+invitationColumns,
			inv.OrganizationID, inv.Email, inv.RoleID, inv.ScopeType, inv.ScopeID, inv.InvitedBy, tokenHash, inv.ExpiresAt))
		if err != nil {
			return fmt.Errorf("insert invitation: %w", err)
		}
		*inv = *created
		// The mail goes out before the audit entry and the event take the
		// organization's advisory locks (sendInvitation).
		org, err := sendInvitation(ctx, tx, inv, delivery)
		if err != nil {
			return err
		}
		if err := recordInvitationAudit(ctx, tx, inv.OrganizationID, inv.InvitedBy, inv.ID, "invitation.issued",
			map[string]any{"role_id": inv.RoleID, "scope_type": inv.ScopeType, "scope_id": inv.ScopeID}); err != nil {
			return err
		}
		return appendInvitationCreated(ctx, tx, inv, delivery, org.Name)
	})
}

// checkInvitationTargets verifies role and scope inside the organization: the
// role is a role of the organization (422 role_id) that fits the scope
// (checkRoleScope, 422 scope_type), the issuer holds user:manage and every
// permission of the role in a scope covering the invitation's scope
// (checkGrantCeiling, 403), and the client or site is one of the organization
// the issuer can read (422 scope_id).
func checkInvitationTargets(ctx context.Context, tx pgx.Tx, caller *identity.Principal, inv *Invitation) error {
	role, found, err := readStandardRole(ctx, tx, inv.OrganizationID, inv.RoleID)
	if err != nil {
		return err
	}
	if !found {
		return &api.FieldError{Field: "role_id", Message: "role not found", Status: 422}
	}
	if scopeErr := checkRoleScope(role.Scope, inv.ScopeType); scopeErr != nil {
		return scopeErr
	}
	if ceilingErr := checkGrantCeiling(ctx, tx, inv.OrganizationID, caller, identity.PermUserManage, role.Permissions, inv.ScopeType, inv.ScopeID); ceilingErr != nil {
		return ceilingErr
	}
	return checkScopeTarget(ctx, tx, inv.OrganizationID, inv.ScopeType, inv.ScopeID)
}

// ListInvitations implements InvitationRepository (newest first).
func (r *PGInvitations) ListInvitations(ctx context.Context, orgID string, page api.PaginationParams) ([]Invitation, int, error) {
	items := []Invitation{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_invitation WHERE organization_id = $1`, orgID).Scan(&total); err != nil {
			return fmt.Errorf("count invitations: %w", err)
		}
		where, tail, args, err := api.NewestFirstSQL(page, 2)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+invitationColumns+` FROM user_invitation
			WHERE organization_id = $1 AND `+where+` `+tail, append([]any{orgID}, args...)...)
		if err != nil {
			return fmt.Errorf("list invitations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			inv, err := scanInvitation(rows)
			if err != nil {
				return fmt.Errorf("scan invitation: %w", err)
			}
			items = append(items, *inv)
		}
		return rows.Err()
	})
	return items, total, err
}

// RenewInvitation implements InvitationRepository: a pending or expired
// invitation gets a new token and a new validity; the old token stops
// working, the invitee gets a mail with the new link (sendInvitation; a
// refused direct send keeps the old token and expiry). The renewing
// principal must hold user:manage and the role's permissions in a scope
// covering the invitation's scope (grant ceiling).
func (r *PGInvitations) RenewInvitation(ctx context.Context, orgID, id string, caller *identity.Principal, tokenHash string, delivery InvitationDelivery, expiresAt time.Time) (*Invitation, error) {
	if !api.IsUUID(id) {
		return nil, ErrInvitationNotFound
	}
	var inv *Invitation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+` FROM user_invitation
			WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotFound
		}
		if err != nil {
			return fmt.Errorf("read invitation: %w", err)
		}
		if current.AcceptedAt != nil {
			return ErrInvitationAccepted
		}
		role, found, err := readStandardRole(ctx, tx, orgID, current.RoleID)
		if err != nil {
			return err
		}
		var perms []identity.Permission
		if found {
			perms = role.Permissions
		}
		if ceilingErr := checkGrantCeiling(ctx, tx, orgID, caller, identity.PermUserManage, perms, current.ScopeType, current.ScopeID); ceilingErr != nil {
			return ceilingErr
		}
		inv, err = scanInvitation(tx.QueryRow(ctx, `UPDATE user_invitation SET token_hash = $3, expires_at = $4
			WHERE organization_id = $1 AND id = $2 RETURNING `+invitationColumns, orgID, id, tokenHash, expiresAt))
		if err != nil {
			return fmt.Errorf("renew invitation: %w", err)
		}
		// The mail goes out before the audit entry and the event take the
		// organization's advisory locks (sendInvitation).
		org, err := sendInvitation(ctx, tx, inv, delivery)
		if err != nil {
			return err
		}
		if aerr := recordInvitationAudit(ctx, tx, orgID, tenant.FromContext(ctx).UserID, id, "invitation.renewed",
			map[string]any{"expires_at": expiresAt.UTC().Format(time.RFC3339)}); aerr != nil {
			return aerr
		}
		return appendInvitationCreated(ctx, tx, inv, delivery, org.Name)
	})
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// RevokeInvitation implements InvitationRepository: a pending or expired
// invitation is deleted; an accepted one stays as a record.
func (r *PGInvitations) RevokeInvitation(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrInvitationNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var acceptedAt *time.Time
		err := tx.QueryRow(ctx, `DELETE FROM user_invitation WHERE organization_id = $1 AND id = $2 AND accepted_at IS NULL
			RETURNING accepted_at`, orgID, id).Scan(&acceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_invitation WHERE organization_id = $1 AND id = $2)`,
				orgID, id).Scan(&exists); qerr != nil {
				return fmt.Errorf("read invitation: %w", qerr)
			}
			if exists {
				return ErrInvitationAccepted
			}
			return ErrInvitationNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke invitation: %w", err)
		}
		return recordInvitationAudit(ctx, tx, orgID, tenant.FromContext(ctx).UserID, id, "invitation.revoked", nil)
	})
}

// AcceptInvitation implements InvitationRepository. It runs without a
// principal in the organization the token names (org-wide, like the first
// login). The conditional UPDATE consumes the invitation exactly once: a
// concurrent second acceptance waits for the row lock and then finds it
// accepted.
func (r *PGInvitations) AcceptInvitation(ctx context.Context, orgID, tokenHash, displayName string, quota QuotaGuard,
	createAccount func(ctx context.Context, inv *Invitation, role string) error) (*AcceptedInvitation, error) {
	var accepted *AcceptedInvitation
	scope := loginScope(orgID)
	err := database.WithTenant(ctx, r.pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		inv, err := scanInvitation(tx.QueryRow(ctx, `UPDATE user_invitation SET accepted_at = now()
			WHERE organization_id = $1 AND token_hash = $2 AND accepted_at IS NULL AND expires_at > now()
			RETURNING `+invitationColumns, orgID, tokenHash))
		if errors.Is(err, pgx.ErrNoRows) {
			var expired bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_invitation WHERE organization_id = $1
				AND token_hash = $2 AND accepted_at IS NULL)`, orgID, tokenHash).Scan(&expired); qerr != nil {
				return fmt.Errorf("read invitation: %w", qerr)
			}
			if expired {
				return ErrInvitationExpired
			}
			return ErrInvitationInvalid
		}
		if err != nil {
			return fmt.Errorf("accept invitation: %w", err)
		}

		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE organization_id = $1 AND lower(email) = lower($2))`,
			orgID, inv.Email).Scan(&exists); err != nil {
			return fmt.Errorf("check existing user: %w", err)
		}
		if exists {
			return ErrInvitationUserExists
		}
		if quotaErr := allowUserTx(ctx, tx, scope, quota, orgID); quotaErr != nil {
			return quotaErr
		}
		if strings.TrimSpace(displayName) == "" {
			displayName = inv.Email
		}
		var userID string
		if err = tx.QueryRow(ctx, `INSERT INTO app_user (organization_id, email, display_name)
			VALUES ($1, $2, $3) RETURNING id::text`, orgID, inv.Email, strings.TrimSpace(displayName)).Scan(&userID); err != nil {
			return fmt.Errorf("insert invited user: %w", err)
		}
		var clientScope, siteScope *string
		switch inv.ScopeType {
		case "client":
			clientScope = &inv.ScopeID
		case "site":
			siteScope = &inv.ScopeID
		}
		if _, err = tx.Exec(ctx, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id)
			VALUES ($1, $2, $3, $4, $5)`, orgID, userID, inv.RoleID, clientScope, siteScope); err != nil {
			return fmt.Errorf("assign invited role: %w", err)
		}
		if createAccount != nil {
			var role string
			if err = tx.QueryRow(ctx, `SELECT name FROM role WHERE id::text = $1 AND organization_id = $2`,
				inv.RoleID, orgID).Scan(&role); err != nil {
				return fmt.Errorf("read invited role: %w", err)
			}
			if aerr := createAccount(ctx, inv, role); aerr != nil {
				return aerr
			}
		}
		accepted = &AcceptedInvitation{UserID: userID, OrganizationID: orgID, Email: inv.Email}
		return recordInvitationAudit(ctx, tx, orgID, userID, inv.ID, "invitation.accepted",
			map[string]any{"user_id": userID, "role_id": inv.RoleID, "scope_type": inv.ScopeType, "scope_id": inv.ScopeID})
	})
	if err != nil {
		return nil, err
	}
	return accepted, nil
}
