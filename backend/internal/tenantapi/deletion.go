package tenantapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// DeletionGracePeriod is the time between the deletion request of an
// organization and its purge (TLC-06): 30 days, in which the request can be
// cancelled.
const DeletionGracePeriod = 30 * 24 * time.Hour

// ProblemTypeMFARequired is the problem type of a request that needs a
// session opened with a second factor (WP-1152).
const ProblemTypeMFARequired = "urn:reticora:problem:mfa-required"

// States of the deletion of an organization (migration 000324).
const (
	DeletionNone      = "none"
	DeletionRequested = "requested"
	DeletionPurging   = "purging"
	DeletionPurged    = "purged"
)

// OrganizationDeletion is the deletion state of the organization: the
// state, when it was requested, when the grace period ends and how much of
// it is left.
type OrganizationDeletion struct {
	State       string     `json:"state"`
	RequestedAt *time.Time `json:"requested_at"`
	DueAt       *time.Time `json:"due_at"`
	RequestedBy *string    `json:"requested_by"`
	// RemainingSeconds is the rest of the grace period, 0 once it ended;
	// null without a request.
	RemainingSeconds *int64 `json:"remaining_seconds"`
}

// DeletionRequest is the body of POST /api/v1/org/deletion: the slug of the
// organization, typed by the requester as confirmation.
type DeletionRequest struct {
	ConfirmSlug string `json:"confirm_slug"`
}

// DeletionHandler serves /api/v1/org/deletion (TLC-06, WP-1152): an
// org-wide holder of org:delete with a second factor requests the deletion
// of the organization and may cancel it during the 30 days of grace; every
// user of the organization reads the state (the banner of WP-1157). The
// routes belong to the module org_lifecycle.
type DeletionHandler struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewDeletionHandler builds the handler on the application pool; without
// one (--no-db) the state is none and the writes answer 503.
func NewDeletionHandler(pool *pgxpool.Pool) *DeletionHandler {
	return &DeletionHandler{pool: pool, now: time.Now}
}

// RegisterRoutes registers the deletion routes.
func (h *DeletionHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/org/deletion", h.Get)
	r.Post("/api/v1/org/deletion", h.Request)
	r.Delete("/api/v1/org/deletion", h.Cancel)
}

var (
	errDeletionExists    = errors.New("the deletion of the organization is already requested")
	errDeletionNone      = errors.New("the deletion of the organization is not requested")
	errDeletionStarted   = errors.New("the purge of the organization has started; the request can no longer be cancelled")
	errDeletionWrongSlug = errors.New("confirm_slug does not match the slug of the organization")
	errDeletionNotFound  = errors.New("organization not found")
)

const deletionColumns = `deletion_state, deletion_requested_at, deletion_due_at, deletion_requested_by::text`

// Get answers the deletion state of the organization.
func (h *DeletionHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	if h.pool == nil {
		api.WriteJSON(w, http.StatusOK, OrganizationDeletion{State: DeletionNone})
		return
	}
	var out *OrganizationDeletion
	err := database.WithRequestTenant(r.Context(), h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = h.read(ctx, tx, orgID, false)
		return err
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Request requests the deletion: org:delete for the whole organization, a
// session with a second factor and the slug of the organization as
// confirmation. The state becomes requested, due after the grace period;
// org.deletion_requested is written in the same transaction (202).
func (h *DeletionHandler) Request(w http.ResponseWriter, r *http.Request) {
	orgID, p, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req DeletionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.ConfirmSlug) == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "confirm_slug", Message: "confirm_slug is required",
			Status: http.StatusUnprocessableEntity})
		return
	}
	if h.pool == nil {
		api.WriteUnavailable(w, "the deletion of the organization needs PostgreSQL")
		return
	}
	var out *OrganizationDeletion
	// The operator flag lets the transaction keep the requester's address
	// in org_deletion_notice (WP-1156) together with the request.
	err := database.WithOperatorTenant(r.Context(), h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var slug, state string
		err := tx.QueryRow(ctx, `SELECT slug, deletion_state FROM organization WHERE id = $1 FOR UPDATE`, orgID).Scan(&slug, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return errDeletionNotFound
		}
		if err != nil {
			return err
		}
		if req.ConfirmSlug != slug {
			return errDeletionWrongSlug
		}
		if state != DeletionNone {
			return errDeletionExists
		}
		if _, err = tx.Exec(ctx, `UPDATE organization SET deletion_state = 'requested', deletion_requested_at = now(),
				deletion_due_at = now() + make_interval(secs => $2), deletion_requested_by = $3
			WHERE id = $1`, orgID, DeletionGracePeriod.Seconds(), uuidOrNil(p.Subject)); err != nil {
			return err
		}
		if out, err = h.read(ctx, tx, orgID, false); err != nil {
			return err
		}
		if serr := storeNoticeTx(ctx, tx, orgID, p.Subject); serr != nil {
			return serr
		}
		data := events.OrgDeletionData{ID: orgID, DueAt: out.DueAt.UTC().Format(time.RFC3339)}
		if out.RequestedBy != nil {
			data.RequestedBy = *out.RequestedBy
		}
		return appendDeletionEvent(ctx, tx, orgID, events.OrgDeletionRequested, &data)
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, out)
}

// Cancel withdraws a requested deletion during the grace period: the state
// becomes none, the columns are emptied and org.deletion_cancelled is
// written (200 with the state). Once the purge started it answers 409,
// without a request 404.
func (h *DeletionHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	orgID, p, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if h.pool == nil {
		api.WriteUnavailable(w, "the deletion of the organization needs PostgreSQL")
		return
	}
	var out *OrganizationDeletion
	err := database.WithOperatorTenant(r.Context(), h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := h.read(ctx, tx, orgID, true)
		if err != nil {
			return err
		}
		switch before.State {
		case DeletionNone:
			return errDeletionNone
		case DeletionPurging, DeletionPurged:
			return errDeletionStarted
		}
		if _, err = tx.Exec(ctx, `UPDATE organization SET deletion_state = 'none', deletion_requested_at = NULL,
				deletion_due_at = NULL, deletion_requested_by = NULL WHERE id = $1`, orgID); err != nil {
			return err
		}
		if out, err = h.read(ctx, tx, orgID, false); err != nil {
			return err
		}
		// The address of the protocol goes with the request (WP-1156).
		if derr := deleteNoticeTx(ctx, tx, orgID); derr != nil {
			return derr
		}
		data := events.OrgDeletionData{ID: orgID, DueAt: before.DueAt.UTC().Format(time.RFC3339), CancelledBy: uuidString(p.Subject)}
		if before.RequestedBy != nil {
			data.RequestedBy = *before.RequestedBy
		}
		return appendDeletionEvent(ctx, tx, orgID, events.OrgDeletionCancelled, &data)
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// authorize checks what the route permission (org:delete, routeRules)
// cannot: the right for the whole organization and a session with a
// second factor (403 mfa-required); an API key never has one.
func (h *DeletionHandler) authorize(w http.ResponseWriter, r *http.Request) (string, identity.Principal, bool) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return "", identity.Principal{}, false
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing principal")
		return "", identity.Principal{}, false
	}
	if !identity.HoldsOrgWide(r.Context(), identity.PermOrgDelete) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only a holder of org:delete for the whole organization may request or cancel its deletion")
		return "", identity.Principal{}, false
	}
	if !p.MFA || p.Type != identity.PrincipalTypeUser {
		api.WriteProblem(w, http.StatusForbidden, ProblemTypeMFARequired, "Forbidden",
			"the deletion of the organization needs a session opened with a second factor; sign in again with MFA")
		return "", identity.Principal{}, false
	}
	return orgID, p, true
}

// read returns the deletion state of the organization, locked with
// forUpdate.
func (h *DeletionHandler) read(ctx context.Context, tx pgx.Tx, orgID string, forUpdate bool) (*OrganizationDeletion, error) {
	q := `SELECT ` + deletionColumns + `, now() FROM organization WHERE id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	var out OrganizationDeletion
	var dbNow time.Time
	err := tx.QueryRow(ctx, q, orgID).Scan(&out.State, &out.RequestedAt, &out.DueAt, &out.RequestedBy, &dbNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errDeletionNotFound
	}
	if err != nil {
		return nil, err
	}
	if out.DueAt != nil {
		remaining := int64(max(0, out.DueAt.Sub(dbNow).Seconds()))
		out.RemainingSeconds = &remaining
	}
	return &out, nil
}

// appendDeletionEvent writes the event of the deletion as the last write of
// the transaction (Konvention 20).
func appendDeletionEvent(ctx context.Context, tx pgx.Tx, orgID, typ string, data *events.OrgDeletionData) error {
	return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
		_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: typ, EntityID: orgID, Data: *data})
		return err
	})
}

// writeError answers a failed deletion request, cancel or read; other
// errors reach the client sanitized (API-03).
func (h *DeletionHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errDeletionWrongSlug):
		api.WriteFieldError(w, &api.FieldError{Field: "confirm_slug", Message: errDeletionWrongSlug.Error(),
			Status: http.StatusUnprocessableEntity})
	case errors.Is(err, errDeletionExists):
		api.WriteError(w, http.StatusConflict, "Conflict", errDeletionExists.Error())
	case errors.Is(err, errDeletionStarted):
		api.WriteError(w, http.StatusConflict, "Conflict", errDeletionStarted.Error())
	case errors.Is(err, errDeletionNone):
		api.WriteError(w, http.StatusNotFound, "Not Found", errDeletionNone.Error())
	case errors.Is(err, errDeletionNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", errDeletionNotFound.Error())
	default:
		slog.ErrorContext(r.Context(), "organization deletion failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// uuidOrNil is the user id of the request, NULL for a subject that is no
// UUID (a development token).
func uuidOrNil(s string) any {
	if uuidRE.MatchString(s) {
		return s
	}
	return nil
}

// uuidString is s when it is a UUID, else empty.
func uuidString(s string) string {
	if uuidRE.MatchString(s) {
		return s
	}
	return ""
}

// ReadDeletionState reads the deletion state and the end of the grace
// period of the organization in the tenant context of ctx (the request,
// or the org-wide scope of a system run): the read-only grace period of
// WP-1153 (middleware.OrgState, collector status). An unknown organization
// is none.
func ReadDeletionState(ctx context.Context, pool *pgxpool.Pool, orgID string) (state string, dueAt *time.Time, err error) {
	err = database.WithRequestTenant(ctx, pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(max(deletion_state), 'none'), max(deletion_due_at) FROM organization WHERE id = $1`,
			orgID).Scan(&state, &dueAt)
	})
	return state, dueAt, err
}

// OrgPendingDeletion reports whether the organization is in the read-only
// grace period of its deletion (requested or purging), read through
// ReadDeletionState: the collector status run pauses its collectors
// (discovery.LicensedUnlessPendingDeletion, WP-1153).
func OrgPendingDeletion(ctx context.Context, pool *pgxpool.Pool, orgID string) (bool, error) {
	state, _, err := ReadDeletionState(ctx, pool, orgID)
	return state == DeletionRequested || state == DeletionPurging, err
}
