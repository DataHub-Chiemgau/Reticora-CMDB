package lifecycle

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// System guards of LCY-03 (WP-279): named checks against the database that
// run whenever an asset enters one of the guarded states, whatever its
// lifecycle model (the system model physical_asset, a model of the
// organization or the model of the asset type chain). They are fixed in
// code: a model can add guard rules (WP-723) but neither remove nor weaken
// a system guard. A CI has no system guards.
const (
	// GuardStorageLocation (in_stock): asset.location_id is a visible
	// location of the storage branch (locations.StorageKinds).
	GuardStorageLocation = "storage_location"
	// GuardOperationsLocationOrHolder (deployed): asset.location_id is a
	// visible location of the operations branch (locations.OperationsKinds)
	// or the asset has a holder (assigned_user_id or assigned_team_id,
	// WP-275).
	GuardOperationsLocationOrHolder = "operations_location_or_holder"
	// GuardDisposalRecord (disposed): a visible disposal_record of the
	// asset exists.
	GuardDisposalRecord = "disposal_record"
	// GuardActiveReservation (reserved): the asset has exactly one active
	// reservation (AST-06, WP-277); not part of the wording of LCY-03.
	GuardActiveReservation = "active_reservation"
)

// systemGuards maps a guarded target state to its guard and the references
// a violation names as missing.
var systemGuards = map[string]struct {
	key     string
	missing []string
}{
	"in_stock": {GuardStorageLocation, []string{"location_id"}},
	"deployed": {GuardOperationsLocationOrHolder, []string{"location_id", "assignee"}},
	"disposed": {GuardDisposalRecord, []string{"disposal_record"}},
	"reserved": {GuardActiveReservation, []string{"reservation"}},
}

// SystemGuardFor returns the system guard an asset entering state must pass;
// ok is false for a state without a system guard.
func SystemGuardFor(state string) (guard string, ok bool) {
	g, ok := systemGuards[state]
	return g.key, ok
}

// GuardChecker evaluates the system guard of a transition against the
// database in the transaction of ctx (Service.WithGuards). A failed guard
// is a *GuardError with guard and missing; other errors abort the
// transition.
type GuardChecker interface {
	CheckGuard(ctx context.Context, guard string, check *TransitionCheck) error
}

// newSystemGuardError is the GuardError of a failed system guard: 422
// lifecycle-guard with guard, missing and one violation per missing
// reference.
func newSystemGuardError(guard, state, detail string) *GuardError {
	missing := append([]string(nil), systemGuards[state].missing...)
	violations := make([]GuardViolation, 0, len(missing))
	for _, ref := range missing {
		violations = append(violations, GuardViolation{Field: ref, Code: "guard." + guard,
			Message: fmt.Sprintf("lifecycle state %q requires %s (system guard %q)", state, ref, guard)})
	}
	if detail == "" {
		detail = fmt.Sprintf("the asset fails the system guard %q of lifecycle state %q", guard, state)
	}
	return &GuardError{Violations: violations, Guard: guard, Missing: missing, detail: detail}
}

// uncheckableGuardError refuses a guarded transition without a GuardChecker
// (profile --no-db): fail-closed.
func uncheckableGuardError(guard, state string) *GuardError {
	return newSystemGuardError(guard, state,
		fmt.Sprintf("the system guard %q of lifecycle state %q cannot be checked", guard, state))
}

// PGGuards implements GuardChecker with queries in the request's tenant
// scope (database.WithRequestTenant). They join the transaction of the
// transition, so they see writes of the same commit (the disposal_record of
// POST /api/v1/disposal-records) and run under the row lock of the asset.
// A location, disposal record or reservation outside the scope counts as
// missing.
type PGGuards struct {
	pool *pgxpool.Pool
}

// NewPGGuards creates the PostgreSQL system guards.
func NewPGGuards(pool *pgxpool.Pool) *PGGuards {
	return &PGGuards{pool: pool}
}

// guardQueries are the checks of the system guards; $1 is the asset, $2 the
// location kinds where the guard needs them. Each joins the visible asset,
// so rows of an asset outside the scope never count.
var guardQueries = map[string]string{
	GuardStorageLocation: `
		SELECT EXISTS (SELECT 1 FROM asset a
			JOIN location l ON l.id = a.location_id AND l.organization_id = a.organization_id
			WHERE a.id = $1 AND l.kind = ANY($2::text[]))`,
	GuardOperationsLocationOrHolder: `
		SELECT EXISTS (SELECT 1 FROM asset a
			WHERE a.id = $1 AND (a.assigned_user_id IS NOT NULL OR a.assigned_team_id IS NOT NULL
				OR EXISTS (SELECT 1 FROM location l
					WHERE l.id = a.location_id AND l.organization_id = a.organization_id
					  AND l.kind = ANY($2::text[]))))`,
	GuardDisposalRecord: `
		SELECT EXISTS (SELECT 1 FROM asset a
			JOIN disposal_record d ON d.asset_id = a.id AND d.organization_id = a.organization_id
			WHERE a.id = $1)`,
	GuardActiveReservation: `
		SELECT (SELECT count(*) FROM asset a
			JOIN reservation r ON r.asset_id = a.id AND r.organization_id = a.organization_id
			WHERE a.id = $1 AND r.state = 'active') = 1`,
}

// guardKinds are the location kinds of the guards that check the location.
var guardKinds = map[string][]locations.Kind{
	GuardStorageLocation:            locations.StorageKinds,
	GuardOperationsLocationOrHolder: locations.OperationsKinds,
}

// CheckGuard implements GuardChecker.
func (g *PGGuards) CheckGuard(ctx context.Context, guard string, check *TransitionCheck) error {
	query, ok := guardQueries[guard]
	if !ok {
		return fmt.Errorf("unknown lifecycle system guard %q", guard)
	}
	args := []any{check.EntityID}
	if kinds, withKinds := guardKinds[guard]; withKinds {
		names := make([]string, len(kinds))
		for i, k := range kinds {
			names[i] = string(k)
		}
		args = append(args, names)
	}
	var passed bool
	err := database.WithRequestTenant(ctx, g.pool, check.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if scanErr := tx.QueryRow(ctx, query, args...).Scan(&passed); scanErr != nil {
			return fmt.Errorf("check lifecycle guard %s: %w", guard, scanErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !passed {
		return newSystemGuardError(guard, check.To, "")
	}
	return nil
}
