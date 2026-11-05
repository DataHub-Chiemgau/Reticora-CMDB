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
	// ProblemAppendOnly: an assignment is history and is not deleted.
	ProblemAppendOnly = "https://reticora.io/problems/assignment-append-only"
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
)

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
	// DueAt is the due date of the assignment; DueDate (YYYY-MM-DD, the
	// path POST /api/v1/assignments) sets it to the end of that day in
	// organization.timezone when DueAt is nil.
	DueAt   *time.Time
	DueDate string
	Notes   string

	// Fields of the transition row in assignment (until WP-747): the CI the
	// legacy path named, its assignment_type (default assignment) and the
	// handover signature (WP-656).
	CIID      string
	Type      string
	Signature *Signature
}

// ReturnInput is the return of an assigned asset.
type ReturnInput struct {
	AssetID         string
	ExpectedHolder  string
	ReturnCondition string
	Notes           string
	// Signature is the return signature of the closed transition rows
	// (path POST /api/v1/assignments/{id}/return).
	Signature *Signature
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
// cache (ErrAlreadyAssigned, ErrNotAssigned, ErrHolderMismatch), book the
// movements assignment and return through the movement service, set
// asset.assigned_user_id, assigned_team_id and assignment_due_at, write the
// transition rows of assignment (until WP-747) and register the events
// asset.returned and asset.assigned last before the commit
// (database.BeforeCommit). Any error rolls everything back.
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

// endOfDay is the SQL of the end of day $1 (a date) in the time zone of
// organization $2: the due_at of a due date, as in migration 000254.
const endOfDay = `SELECT (($1::date + 1)::timestamp AT TIME ZONE timezone) - interval '1 microsecond' FROM organization WHERE id = $2`

// parseDueDate reads the legacy due_date: a date or an RFC 3339 time,
// whose date counts.
func parseDueDate(value string) (string, error) {
	if d, err := time.Parse(time.DateOnly, value); err == nil {
		return d.Format(time.DateOnly), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.Format(time.DateOnly), nil
	}
	return "", &api.FieldError{Field: "due_date", Message: "must be a date (YYYY-MM-DD)", Status: http.StatusUnprocessableEntity}
}

// Assign assigns the free asset in.AssetID to in.Holder (see Service).
// The result is the assignment; its id is the id of the assignment
// movement.
func (s *Service) Assign(ctx context.Context, orgID string, in *AssignInput) (*Assignment, error) {
	if err := in.Holder.validate("assigned_user_id", "assigned_team_id"); err != nil {
		return nil, err
	}
	dueDate := ""
	if in.DueAt == nil && in.DueDate != "" {
		var err error
		if dueDate, err = parseDueDate(in.DueDate); err != nil {
			return nil, err
		}
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
		dueAt := in.DueAt
		if dueAt == nil && dueDate != "" {
			var at time.Time
			if err = tx.QueryRow(ctx, endOfDay, dueDate, orgID).Scan(&at); err != nil {
				return fmt.Errorf("due date: %w", err)
			}
			dueAt = &at
		}
		m := &movement.Movement{OrganizationID: orgID, ItemKind: "asset", AssetID: in.AssetID, MovementType: "assignment",
			ActorID: actor(ctx), AssigneeUserID: in.Holder.UserID, AssigneeTeamID: in.Holder.TeamID, DueAt: dueAt, Notes: in.Notes}
		if bookErr := s.movements.RecordTx(ctx, tx, m); bookErr != nil {
			return bookErr
		}
		if holderErr := setHolder(ctx, tx, orgID, in.AssetID, in.Holder, dueAt); holderErr != nil {
			return holderErr
		}
		kind := in.Type
		if kind == "" {
			kind = "assignment"
		}
		if out, err = s.open(ctx, tx, m, in.CIID, kind, in.Signature); err != nil {
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
		if closeErr := closeRows(ctx, tx, orgID, in.AssetID, "returned", in.ReturnCondition, in.Notes, in.Signature); closeErr != nil {
			return closeErr
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
		if closeErr := closeRows(ctx, tx, orgID, in.AssetID, "transferred", "", "", nil); closeErr != nil {
			return closeErr
		}
		if out, err = s.open(ctx, tx, next, "", "transfer", nil); err != nil {
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

// AssetOfCI returns the asset of a CI (ci.asset_id) visible in the
// caller's scope; "" when the CI has none. An unknown CI is ErrNotFound.
func (s *Service) AssetOfCI(ctx context.Context, orgID, ciID string) (string, error) {
	if !api.IsUUID(ciID) {
		return "", api.ErrNotFound
	}
	var assetID string
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(asset_id::text, '') FROM ci WHERE id = $1 AND organization_id = $2`, ciID, orgID).Scan(&assetID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", api.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("asset of ci: %w", err)
	}
	return assetID, nil
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

// open writes the transition row of the assignment movement m in
// assignment (until WP-747) when holder and actor are users, and returns
// the assignment: the row, or one built from the movement.
func (s *Service) open(ctx context.Context, tx pgx.Tx, m *movement.Movement, ciID, kind string, sig *Signature) (*Assignment, error) {
	a := &Assignment{ID: m.ID, OrganizationID: m.OrganizationID, AssetID: m.AssetID, CIID: ciID,
		AssignedTo: m.AssigneeUserID, AssignedTeamID: m.AssigneeTeamID, AssignedBy: m.ActorID, AssignmentType: kind,
		Status: "active", AssignedAt: m.OccurredAt, DueAt: m.DueAt, Notes: m.Notes, CheckoutSignature: sig,
		CreatedAt: m.CreatedAt, UpdatedAt: m.CreatedAt}
	if m.DueAt != nil {
		if err := tx.QueryRow(ctx, `SELECT to_char(($1::timestamptz AT TIME ZONE timezone)::date, 'YYYY-MM-DD') FROM organization WHERE id = $2`,
			*m.DueAt, m.OrganizationID).Scan(&a.DueDate); err != nil {
			return nil, fmt.Errorf("due date: %w", err)
		}
	}
	if m.AssigneeUserID == "" || !api.IsUUID(m.ActorID) {
		return a, nil
	}
	var isUser bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND organization_id = $2)`,
		m.ActorID, m.OrganizationID).Scan(&isUser); err != nil {
		return nil, fmt.Errorf("check actor: %w", err)
	}
	if !isUser {
		return a, nil
	}
	checkoutSig, err := sealSignature(ctx, tx, m.OrganizationID, sig)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assignment (id, organization_id, asset_id, ci_id, assigned_to, assigned_by, assignment_type,
			status, assigned_at, due_date, notes, checkout_signature, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9::date, $10, $11::jsonb, $8, $8)`,
		m.ID, m.OrganizationID, m.AssetID, nilIfEmpty(ciID), m.AssigneeUserID, m.ActorID, kind,
		m.OccurredAt, nilIfEmpty(a.DueDate), nilIfEmpty(m.Notes), checkoutSig); err != nil {
		return nil, fmt.Errorf("write transition row: %w", err)
	}
	return a, nil
}

// closeRows closes the open transition rows of the asset (status, the
// return time), also those with only a ci_id whose CI hangs on the asset
// (ci.asset_id). condition, notes and the return signature are set when
// given.
func closeRows(ctx context.Context, tx pgx.Tx, orgID, assetID, status, condition, notes string, sig *Signature) error {
	returnSig, err := sealSignature(ctx, tx, orgID, sig)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE assignment SET status = $3, returned_at = now(),
			return_condition = COALESCE($4, return_condition), notes = COALESCE($5, notes),
			return_signature = COALESCE($6::jsonb, return_signature), updated_at = now()
		WHERE organization_id = $1 AND status IN ('active', 'overdue')
		  AND (asset_id = $2 OR (asset_id IS NULL AND ci_id IN (SELECT id FROM ci WHERE organization_id = $1 AND asset_id = $2)))`,
		orgID, assetID, status, nilIfEmpty(condition), nilIfEmpty(notes), returnSig); err != nil {
		return fmt.Errorf("close transition rows: %w", err)
	}
	return nil
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
