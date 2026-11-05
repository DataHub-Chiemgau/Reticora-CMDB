package assignment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Problem types of the assignment service (RFC 7807, 409).
const (
	// ProblemHolderMismatch: expected_holder is not the current holder of
	// the asset.
	ProblemHolderMismatch = "https://reticora.io/problems/holder-mismatch"
	// ProblemAssetAssigned: the asset has a holder already.
	ProblemAssetAssigned = "https://reticora.io/problems/asset-assigned"
	// ProblemAssetNotAssigned: the asset has no holder.
	ProblemAssetNotAssigned = "https://reticora.io/problems/asset-not-assigned"
	// ProblemAssetReserved: the asset has an active reservation of another
	// user or of a project (WP-277); the reservation path answers an
	// asset reserved already with it as well.
	ProblemAssetReserved = "https://reticora.io/problems/asset-reserved"
)

var (
	// ErrAssetNotFound: the asset is unknown, of another organization,
	// outside the caller's scope or not lockable under its update policy
	// (404, TEN-04).
	ErrAssetNotFound = errors.New("asset not found")
	// ErrHolderMismatch: the current holder is not expected_holder (409).
	ErrHolderMismatch = errors.New("the asset has another holder than expected_holder")
	// ErrAlreadyAssigned: the asset to assign has a holder (409).
	ErrAlreadyAssigned = errors.New("the asset is assigned already; return or transfer it")
	// ErrNotAssigned: the asset to return or transfer has no holder (409).
	ErrNotAssigned = errors.New("the asset is not assigned")
	// ErrAssetReserved: the asset to assign or transfer has an active
	// reservation of another user or of a project (409, WP-277).
	ErrAssetReserved = errors.New("the asset is reserved for another user or a project")
)

// ReservationEventData is the data of the reservation events (WP-277):
// the reservation, its asset or quantity item, its state and times and
// the client and site of the locked asset. The reservation package writes
// it as well; it lives here because reservation imports this package for
// the conversion, not the other way round.
type ReservationEventData struct {
	ID             string     `json:"id"`
	ItemKind       string     `json:"item_kind"`
	AssetID        string     `json:"asset_id,omitempty"`
	QuantityItemID string     `json:"quantity_item_id,omitempty"`
	State          string     `json:"state"`
	ReservedUntil  *time.Time `json:"reserved_until,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ClientID       string     `json:"client_id,omitempty"`
	SiteID         string     `json:"site_id,omitempty"`
}

// MovementBooker books a movement in the transaction of the caller.
// Satisfied by movement.Service (WP-274).
type MovementBooker interface {
	RecordTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error
}

// Holder is the holder of an asset: a user (app_user.id) or a team.
type Holder struct {
	UserID string
	TeamID string
}

// id is the UUID of the holder, user or team.
func (h Holder) id() string {
	if h.UserID != "" {
		return h.UserID
	}
	return h.TeamID
}

// validate requires exactly one of user and team, each a UUID (422 on
// field).
func (h Holder) validate(userField, teamField string) error {
	switch {
	case h.UserID != "" && h.TeamID != "":
		return &api.FieldError{Field: userField, Message: "exactly one of " + userField + " and " + teamField + " is allowed", Status: http.StatusUnprocessableEntity}
	case h.UserID == "" && h.TeamID == "":
		return &api.FieldError{Field: userField, Message: "one of " + userField + " and " + teamField + " is required", Status: http.StatusUnprocessableEntity}
	case h.UserID != "" && !api.IsUUID(h.UserID):
		return &api.FieldError{Field: userField, Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
	case h.TeamID != "" && !api.IsUUID(h.TeamID):
		return &api.FieldError{Field: teamField, Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// AssignInput is an assignment of a free asset.
type AssignInput struct {
	AssetID string
	Holder  Holder
	// DueAt is the due date of the assignment.
	DueAt *time.Time
	Notes string
}

// ReturnInput is the return of an assigned asset.
type ReturnInput struct {
	AssetID         string
	ExpectedHolder  string
	ReturnCondition string
	Notes           string
}

// TransferInput is the change of the holder of an assigned asset.
type TransferInput struct {
	AssetID        string
	ExpectedHolder string
	Holder         Holder
	DueAt          *time.Time
	Notes          string
}

// Service is the one writer of assignments (AST-02, AST-07, WP-275):
// Assign, Return and Transfer run in one transaction of the request's
// tenant scope (database.WithRequestTenantTx) or join the transaction of
// the context. In this order they lock the asset (SELECT … FOR UPDATE in
// the caller's scope; not lockable: ErrAssetNotFound), check the holder
// cache (ErrAlreadyAssigned, ErrNotAssigned, ErrHolderMismatch), check
// the active reservation of the asset on Assign and Transfer (another
// user's or a project's: ErrAssetReserved; the new holder's is consumed,
// claimReservation, WP-277), book the
// movements assignment and return through the movement service, set
// asset.assigned_user_id, assigned_team_id and assignment_due_at and
// register the events asset.returned and asset.assigned last before the
// commit (database.BeforeCommit). Any error rolls everything back. The
// table assignment is not written anymore (WP-747): the assignments are
// the projection assignment_projection of the ledger (000257).
type Service struct {
	pool      *pgxpool.Pool
	movements MovementBooker
	outbox    *events.Outbox
}

// NewService creates the assignment service.
func NewService(pool *pgxpool.Pool, movements MovementBooker, outbox *events.Outbox) *Service {
	return &Service{pool: pool, movements: movements, outbox: outbox}
}

// inTx runs fn in the transaction of the request (WithRequestTenantTx),
// joining one the context carries, with ctx carrying it.
func (s *Service) inTx(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithRequestTenantTx(ctx, s.pool, orgID, func(txCtx context.Context) error {
		return database.WithRequestTenant(txCtx, s.pool, orgID, func(_ context.Context, tx pgx.Tx) error {
			return fn(txCtx, tx)
		})
	})
}

// lockedAsset is the state of the locked asset.
type lockedAsset struct {
	holder       Holder
	client, site string
}

// lock locks the asset in the caller's scope; the update policies apply
// (asset_scoped_update, 000079).
func lock(ctx context.Context, tx pgx.Tx, orgID, assetID string) (*lockedAsset, error) {
	if !api.IsUUID(assetID) {
		return nil, ErrAssetNotFound
	}
	a := &lockedAsset{}
	err := tx.QueryRow(ctx, `SELECT COALESCE(assigned_user_id::text, ''), COALESCE(assigned_team_id::text, ''),
			COALESCE(client_id::text, ''), COALESCE(site_id::text, '')
		FROM asset WHERE id = $1 AND organization_id = $2 FOR UPDATE`, assetID, orgID).
		Scan(&a.holder.UserID, &a.holder.TeamID, &a.client, &a.site)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock asset: %w", err)
	}
	return a, nil
}

// expect checks the holder of the locked asset against expected.
func (a *lockedAsset) expect(expected string) error {
	current := a.holder.id()
	if current == "" {
		return ErrNotAssigned
	}
	if !strings.EqualFold(current, expected) {
		return ErrHolderMismatch
	}
	return nil
}

// actor is the acting user of the request: the tenant user, else the
// principal's subject.
func actor(ctx context.Context) string {
	if id := tenant.FromContext(ctx).UserID; id != "" {
		return id
	}
	if p, ok := identity.PrincipalFromContext(ctx); ok {
		return p.Subject
	}
	return ""
}

// requireExpected requires expected_holder as a UUID (422).
func requireExpected(expected string) error {
	if expected == "" {
		return &api.FieldError{Field: "expected_holder", Message: "expected_holder is required", Status: http.StatusUnprocessableEntity}
	}
	if !api.IsUUID(expected) {
		return &api.FieldError{Field: "expected_holder", Message: "must be a UUID", Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// Assign assigns the free asset in.AssetID to in.Holder (see Service).
// The result is the assignment; its id is the id of the assignment
// movement.
func (s *Service) Assign(ctx context.Context, orgID string, in *AssignInput) (*Assignment, error) {
	if err := in.Holder.validate("assigned_user_id", "assigned_team_id"); err != nil {
		return nil, err
	}
	var out *Assignment
	err := s.inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		a, err := lock(ctx, tx, orgID, in.AssetID)
		if err != nil {
			return err
		}
		if a.holder.id() != "" {
			return ErrAlreadyAssigned
		}
		if resErr := s.claimReservation(ctx, tx, orgID, in.AssetID, in.Holder, a); resErr != nil {
			return resErr
		}
		m := &movement.Movement{OrganizationID: orgID, ItemKind: "asset", AssetID: in.AssetID, MovementType: "assignment",
			ActorID: actor(ctx), AssigneeUserID: in.Holder.UserID, AssigneeTeamID: in.Holder.TeamID, DueAt: in.DueAt, Notes: in.Notes}
		if bookErr := s.movements.RecordTx(ctx, tx, m); bookErr != nil {
			return bookErr
		}
		if holderErr := setHolder(ctx, tx, orgID, in.AssetID, in.Holder, in.DueAt); holderErr != nil {
			return holderErr
		}
		if out, err = assigned(ctx, tx, m, "assignment"); err != nil {
			return err
		}
		return s.emit(ctx, tx, orgID, in.AssetID, a, events.AssetAssigned)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Return takes the assigned asset back from its holder in.ExpectedHolder
// (see Service). The result is the booked return movement.
func (s *Service) Return(ctx context.Context, orgID string, in *ReturnInput) (*movement.Movement, error) {
	if err := requireExpected(in.ExpectedHolder); err != nil {
		return nil, err
	}
	var out *movement.Movement
	err := s.inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		a, err := lock(ctx, tx, orgID, in.AssetID)
		if err != nil {
			return err
		}
		if expectErr := a.expect(in.ExpectedHolder); expectErr != nil {
			return expectErr
		}
		m := &movement.Movement{OrganizationID: orgID, ItemKind: "asset", AssetID: in.AssetID, MovementType: "return",
			ActorID: actor(ctx), AssigneeUserID: a.holder.UserID, AssigneeTeamID: a.holder.TeamID,
			Reason: in.ReturnCondition, Notes: in.Notes}
		if bookErr := s.movements.RecordTx(ctx, tx, m); bookErr != nil {
			return bookErr
		}
		if holderErr := setHolder(ctx, tx, orgID, in.AssetID, Holder{}, nil); holderErr != nil {
			return holderErr
		}
		out = m
		return s.emit(ctx, tx, orgID, in.AssetID, a, events.AssetReturned)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Transfer changes the holder of the assigned asset from
// in.ExpectedHolder to in.Holder (see Service): a return of the old and an
// assignment of the new holder with the same occurred_at. The result is
// the new assignment.
func (s *Service) Transfer(ctx context.Context, orgID string, in *TransferInput) (*Assignment, error) {
	if err := requireExpected(in.ExpectedHolder); err != nil {
		return nil, err
	}
	if err := in.Holder.validate("assigned_user_id", "assigned_team_id"); err != nil {
		return nil, err
	}
	var out *Assignment
	err := s.inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		a, err := lock(ctx, tx, orgID, in.AssetID)
		if err != nil {
			return err
		}
		if expectErr := a.expect(in.ExpectedHolder); expectErr != nil {
			return expectErr
		}
		if resErr := s.claimReservation(ctx, tx, orgID, in.AssetID, in.Holder, a); resErr != nil {
			return resErr
		}
		by := actor(ctx)
		back := &movement.Movement{OrganizationID: orgID, ItemKind: "asset", AssetID: in.AssetID, MovementType: "return",
			ActorID: by, AssigneeUserID: a.holder.UserID, AssigneeTeamID: a.holder.TeamID, Notes: in.Notes}
		if returnErr := s.movements.RecordTx(ctx, tx, back); returnErr != nil {
			return returnErr
		}
		next := &movement.Movement{OrganizationID: orgID, ItemKind: "asset", AssetID: in.AssetID, MovementType: "assignment",
			ActorID: by, AssigneeUserID: in.Holder.UserID, AssigneeTeamID: in.Holder.TeamID, DueAt: in.DueAt, Notes: in.Notes}
		if assignErr := s.movements.RecordTx(ctx, tx, next); assignErr != nil {
			return assignErr
		}
		if holderErr := setHolder(ctx, tx, orgID, in.AssetID, in.Holder, in.DueAt); holderErr != nil {
			return holderErr
		}
		if out, err = assigned(ctx, tx, next, "transfer"); err != nil {
			return err
		}
		if emitErr := s.emit(ctx, tx, orgID, in.AssetID, a, events.AssetReturned); emitErr != nil {
			return emitErr
		}
		return s.emit(ctx, tx, orgID, in.AssetID, a, events.AssetAssigned)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// claimReservation checks the active reservation of the locked asset
// (WP-277), locking it after the asset like the reservation paths: none
// passes; one of another user or of a project is ErrAssetReserved; one of
// the user h is consumed in the same transaction and reservation.converted
// is registered before the commit, so no active reservation stays beside
// the assignment. The SQL stands here because the reservation package
// imports this one (convert).
func (s *Service) claimReservation(ctx context.Context, tx pgx.Tx, orgID, assetID string, h Holder, a *lockedAsset) error {
	d := events.Event{OrgID: orgID, Type: events.ReservationConverted}
	data := ReservationEventData{AssetID: assetID, ClientID: a.client, SiteID: a.site}
	var assignee string
	err := tx.QueryRow(ctx, `SELECT id::text, item_kind, COALESCE(assignee_id::text, ''), reserved_until, expires_at
		FROM reservation WHERE asset_id = $1 AND organization_id = $2 AND state = 'active' FOR UPDATE`, assetID, orgID).
		Scan(&data.ID, &data.ItemKind, &assignee, &data.ReservedUntil, &data.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check reservation: %w", err)
	}
	if h.UserID == "" || !strings.EqualFold(assignee, h.UserID) {
		return ErrAssetReserved
	}
	if err = tx.QueryRow(ctx, `UPDATE reservation SET state = 'consumed', updated_at = now()
		WHERE id = $1 AND organization_id = $2 AND state = 'active' RETURNING state`, data.ID, orgID).Scan(&data.State); err != nil {
		return fmt.Errorf("consume reservation: %w", err)
	}
	d.EntityID, d.Data = data.ID, data
	return database.BeforeCommit(ctx, func() error {
		if _, appendErr := s.outbox.Append(ctx, tx, &d); appendErr != nil {
			return fmt.Errorf("append %s: %w", d.Type, appendErr)
		}
		return nil
	})
}

// setHolder writes the holder cache of the asset; a user or team of
// another organization fails the composite foreign key (WP-745).
func setHolder(ctx context.Context, tx pgx.Tx, orgID, assetID string, h Holder, dueAt *time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE asset SET assigned_user_id = $3, assigned_team_id = $4, assignment_due_at = $5, updated_at = now()
		WHERE id = $1 AND organization_id = $2`,
		assetID, orgID, nilIfEmpty(h.UserID), nilIfEmpty(h.TeamID), dueAt)
	if err != nil {
		return fmt.Errorf("update holder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAssetNotFound
	}
	return nil
}

// assigned returns the assignment of the booked assignment movement m as
// the projection assignment_projection shows it (kind: assignment or
// transfer); due_date is the day of due_at in organization.timezone.
func assigned(ctx context.Context, tx pgx.Tx, m *movement.Movement, kind string) (*Assignment, error) {
	a := &Assignment{ID: m.ID, OrganizationID: m.OrganizationID, AssetID: m.AssetID,
		AssignedTo: m.AssigneeUserID, AssignedTeamID: m.AssigneeTeamID, AssignedBy: m.ActorID, AssignmentType: kind,
		Status: "active", AssignedAt: m.OccurredAt, DueAt: m.DueAt, Notes: m.Notes,
		CreatedAt: m.CreatedAt, UpdatedAt: m.CreatedAt}
	if m.DueAt != nil {
		if err := tx.QueryRow(ctx, `SELECT to_char(($1::timestamptz AT TIME ZONE timezone)::date, 'YYYY-MM-DD') FROM organization WHERE id = $2`,
			*m.DueAt, m.OrganizationID).Scan(&a.DueDate); err != nil {
			return nil, fmt.Errorf("due date: %w", err)
		}
	}
	return a, nil
}

// emit registers the event typ of the asset last before the commit (K20,
// database.BeforeCommit); the payload is the base form of WP-246.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, orgID, assetID string, a *lockedAsset, typ string) error {
	e := &events.Event{OrgID: orgID, Type: typ, EntityID: assetID,
		Data: events.AssetAssignmentData{ID: assetID, ClientID: a.client, SiteID: a.site}}
	return database.BeforeCommit(ctx, func() error {
		if _, err := s.outbox.Append(ctx, tx, e); err != nil {
			return fmt.Errorf("append %s: %w", typ, err)
		}
		return nil
	})
}
