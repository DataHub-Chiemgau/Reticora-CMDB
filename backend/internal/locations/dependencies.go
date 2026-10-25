package locations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

	// Network and discovery objects bound to a site (E-71); the kind is the
	// name of their table.
	DependencyVLAN            = "vlan"
	DependencySubnet          = "subnet"
	DependencyCollector       = "collector"
	DependencyDiscoveryScope  = "discovery_scope"
	DependencyEnrollmentToken = "agent_enrollment_token"
	DependencyCIMerge         = "ci_merge"
)

// dependency is a kind of object that keeps the location $1; present is
// the SQL condition under which one exists.
type dependency struct {
	kind, present string
}

// dependencies are the objects that keep a location (LOC-11). Child
// locations always count: no delete path cascades into the subtree
// (F-023), the foreign keys of the tree are RESTRICT (000157).
var dependencies = []dependency{
	{DependencyLocation, `EXISTS (SELECT 1 FROM location c WHERE c.parent_id = $1)`},
	{DependencyCI, `EXISTS (SELECT 1 FROM ci WHERE ci.location_id = $1)`},
	{DependencyAsset, `EXISTS (SELECT 1 FROM asset a WHERE a.location_id = $1)`},
	{DependencyQuantityItem, `EXISTS (SELECT 1 FROM quantity_item q WHERE q.location_id = $1)`},
	{DependencyAssetMovement, `EXISTS (SELECT 1 FROM asset_movement m WHERE m.from_location_id = $1 OR m.to_location_id = $1)`},
	{DependencyRackMount, `EXISTS (SELECT 1 FROM rack_mount rm WHERE rm.rack_id = $1)`},
	{DependencyRoleAssignment, `EXISTS (SELECT 1 FROM role_assignment ra WHERE ra.scope_site_id = $1)`},
}

// siteObject is a kind of object bound to a site by its site_id. lapsed,
// when set, is the condition under which a row no longer keeps the site.
type siteObject struct {
	kind, lapsed string
}

// siteObjects are the network and discovery objects bound to a site. E-71
// is open; its transitional rule (option 1) applies: while one of them is
// bound to the site, the site is not deleted (409 with the kind, RESTRICT
// since 000157) and the objects have to be removed or moved first. Expired
// enrollment tokens and merge tombstones past tombstone_until do not count
// and are removed in the delete transaction (removeLapsed). Another option
// of E-71 changes only this list and the foreign keys of a follow-up
// migration.
var siteObjects = []siteObject{
	{DependencyVLAN, ""},
	{DependencySubnet, ""},
	{DependencyCollector, ""},
	{DependencyDiscoveryScope, ""},
	{DependencyEnrollmentToken, "expires_at <= now()"},
	{DependencyCIMerge, "tombstone_until <= now()"},
}

// referenceKinds maps the table of a violated foreign key to the kind it
// reports when it is not the name of a kind: the nodes of the tree are
// child locations, the site scope of a service account is a role
// assignment.
var referenceKinds = map[string]string{
	"location":             DependencyLocation,
	"building":             DependencyLocation,
	"room":                 DependencyLocation,
	"rack":                 DependencyLocation,
	"service_account_role": DependencyRoleAssignment,
}

// DependencyError reports the kinds of objects that still reference a
// location. It matches ErrInUse, and ErrHasChildren when child locations are
// among them.
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

// dependencyQuery lists the dependency kinds of the location $1: the
// dependencies and the site objects that have not lapsed.
var dependencyQuery = func() string {
	values := make([]string, 0, len(dependencies)+len(siteObjects))
	for _, d := range dependencies {
		values = append(values, fmt.Sprintf("('%s', %s)", d.kind, d.present))
	}
	for _, o := range siteObjects {
		present := "o.site_id = $1"
		if o.lapsed != "" {
			present += " AND NOT (" + o.lapsed + ")"
		}
		values = append(values, fmt.Sprintf("('%s', EXISTS (SELECT 1 FROM %s o WHERE %s))", o.kind, o.kind, present))
	}
	return "SELECT kind FROM (VALUES\n\t" + strings.Join(values, ",\n\t") + "\n) AS v(kind, present)\nWHERE present\nORDER BY kind"
}()

// DeleteNode deletes the location id in tx with stmt, the delete of its row
// in location or in its specialist table; it is the delete of the location
// API and of the site, building and room API alike. The site objects that
// have lapsed are removed first (E-71), remaining dependencies refuse the
// delete with a *DependencyError, those the caller cannot see through the
// foreign keys (DeleteError), and the delete is audited in the same
// transaction. A refused delete rolls the removal back with it.
func DeleteNode(ctx context.Context, tx pgx.Tx, id, stmt string, args ...any) error {
	cur, err := get(ctx, tx, id)
	if err != nil {
		return err
	}
	removed, err := removeLapsed(ctx, tx, id)
	if err != nil {
		return err
	}
	if depErr := CheckDeletable(ctx, tx, id); depErr != nil {
		return depErr
	}
	if delErr := exec(ctx, tx, stmt, args...); delErr != nil {
		if mapped := DeleteError(delErr); mapped != delErr {
			return mapped
		}
		return fmt.Errorf("delete location: %w", delErr)
	}
	changes := map[string]any{"before": cur}
	if len(removed) > 0 {
		changes["removed"] = removed
	}
	return recordAudit(ctx, tx, "location.deleted", cur, changes)
}

// removeLapsed deletes the site objects of the site id that no longer keep
// it and returns their number per kind. Rows the caller cannot see stay and
// refuse the delete through the foreign key.
func removeLapsed(ctx context.Context, tx pgx.Tx, id string) (map[string]int64, error) {
	removed := map[string]int64{}
	for _, o := range siteObjects {
		if o.lapsed == "" {
			continue
		}
		tag, err := tx.Exec(ctx, "DELETE FROM "+o.kind+" WHERE site_id = $1 AND "+o.lapsed, id)
		if err != nil {
			return nil, fmt.Errorf("remove lapsed %s: %w", o.kind, err)
		}
		if n := tag.RowsAffected(); n > 0 {
			removed[o.kind] = n
		}
	}
	return removed, nil
}

// CheckDeletable returns a *DependencyError when objects visible to the
// caller reference the location id. Rows the caller cannot see are caught
// by the RESTRICT foreign keys; map the delete error with DeleteError.
func CheckDeletable(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := tx.Query(ctx, dependencyQuery, id)
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
// a reference the caller cannot see, to a *DependencyError with the kind of
// the referencing table. Other errors, and a violation of a table that is
// no kind, are returned unchanged.
func DeleteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	kind, ok := referenceKinds[pgErr.TableName]
	if !ok && isKind(pgErr.TableName) {
		kind, ok = pgErr.TableName, true
	}
	if !ok {
		return err
	}
	return &DependencyError{Kinds: []string{kind}}
}

// isKind reports whether kind is one of the dependency kinds.
func isKind(kind string) bool {
	for _, d := range dependencies {
		if d.kind == kind {
			return true
		}
	}
	for _, o := range siteObjects {
		if o.kind == kind {
			return true
		}
	}
	return false
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
		},
		Dependencies: de.Kinds,
	})
	return true
}

// respondProblem writes an RFC 7807 problem with extension members.
func respondProblem(w http.ResponseWriter, status int, body any) {
	api.WriteProblemBody(w, status, body)
}
