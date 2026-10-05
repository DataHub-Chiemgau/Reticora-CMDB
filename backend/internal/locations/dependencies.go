package locations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kinds of objects that keep a location from being deleted (LOC-11).
const (
	DependencyLocation       = "location"
	DependencyCI             = "ci"
	DependencyAsset          = "asset"
	DependencyQuantityItem   = "quantity_item"
	DependencyAssetMovement  = "asset_movement"
	DependencyRackMount      = "rack_mount"
	DependencyRoleAssignment = "role_assignment"
)

// DependencyError reports the kinds of objects that still reference a
// location or its subtree. It matches ErrInUse, and ErrHasChildren when child
// locations are among them.
type DependencyError struct {
	Kinds []string
}

func (e *DependencyError) Error() string {
	return "location is still referenced by: " + strings.Join(e.Kinds, ", ")
}

// Is lets errors.Is match ErrInUse and, for child locations, ErrHasChildren.
func (e *DependencyError) Is(target error) bool {
	return target == ErrInUse || (target == ErrHasChildren && slices.Contains(e.Kinds, DependencyLocation))
}

// dependencyQuery lists the dependency kinds of $1. With $2 the whole
// subtree is checked, as deleting a site, building, room or rack row
// cascades to it; without, child locations count as a dependency.
const dependencyQuery = `
	WITH target AS (
		SELECT l.id FROM location l
		 WHERE CASE WHEN $2 THEN l.path <@ (SELECT r.path FROM location r WHERE r.id = $1) ELSE l.id = $1 END
	)
	SELECT kind FROM (VALUES
		('location', NOT $2 AND EXISTS (SELECT 1 FROM location c WHERE c.parent_id = $1)),
		('ci', EXISTS (SELECT 1 FROM ci WHERE ci.location_id IN (SELECT id FROM target))),
		('asset', EXISTS (SELECT 1 FROM asset a WHERE a.location_id IN (SELECT id FROM target))),
		('quantity_item', EXISTS (SELECT 1 FROM quantity_item q WHERE q.location_id IN (SELECT id FROM target))),
		('asset_movement', EXISTS (SELECT 1 FROM asset_movement m
			WHERE m.from_location_id IN (SELECT id FROM target) OR m.to_location_id IN (SELECT id FROM target))),
		('rack_mount', EXISTS (SELECT 1 FROM rack_mount rm WHERE rm.rack_id IN (SELECT id FROM target))),
		('role_assignment', EXISTS (SELECT 1 FROM role_assignment ra WHERE ra.scope_site_id IN (SELECT id FROM target)))
	) AS v(kind, present)
	WHERE present
	ORDER BY kind`

// CheckDeletable returns a *DependencyError when objects visible to the
// caller reference the location id or, with subtree, anything below it.
// Rows the caller cannot see are caught by the RESTRICT foreign keys; map
// the delete error with DeleteError.
func CheckDeletable(ctx context.Context, tx pgx.Tx, id string, subtree bool) error {
	rows, err := tx.Query(ctx, dependencyQuery, id, subtree)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return ErrNotFound
		}
		return fmt.Errorf("location dependencies: %w", err)
	}
	kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("location dependencies: %w", err)
	}
	if len(kinds) > 0 {
		return &DependencyError{Kinds: kinds}
	}
	return nil
}

// DeleteError maps a foreign key violation of a location delete, raised by
// a reference the caller cannot see, to a *DependencyError naming the
// referencing table.
func DeleteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName != "" {
		return &DependencyError{Kinds: []string{pgErr.TableName}}
	}
	return err
}

// dependencyProblem is the 409 problem of a refused delete.
type dependencyProblem struct {
	httpx.ProblemDetail
	Dependencies []string `json:"dependencies"`
}

// WriteDependencyConflict writes the 409 problem for err when it is a
// *DependencyError and reports whether it did.
func WriteDependencyConflict(w http.ResponseWriter, r *http.Request, err error) bool {
	var de *DependencyError
	if !errors.As(err, &de) {
		return false
	}
	respondProblem(w, http.StatusConflict, dependencyProblem{
		ProblemDetail: httpx.ProblemDetail{
			Type:     httpx.TypeConflict,
			Title:    "Conflict",
			Status:   http.StatusConflict,
			Detail:   "the location is still referenced; move or remove the dependent objects first",
			Instance: r.URL.Path,
			TraceID:  r.Header.Get("X-Request-ID"),
		},
		Dependencies: de.Kinds,
	})
	return true
}

// respondProblem writes an RFC 7807 problem with extension members.
func respondProblem(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
