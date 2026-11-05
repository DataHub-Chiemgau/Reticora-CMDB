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
	DependencyStockLevel     = "stock_level"
	DependencyStockMovement  = "stock_movement"
	DependencyRackMount      = "rack_mount"
	DependencyRoleAssignment = "role_assignment"

	// DependencyCIAttribute is a CI whose reference attribute with target
	// location names the location (MET-12, WP-713).
	DependencyCIAttribute = "ci_attribute"

	// DependencyInternalOrder is an internal order whose target bin is the
	// location (STK-05, WP-847), RESTRICT since 000247.
	DependencyInternalOrder = "internal_order"

	// Stocktake references (INV-01, WP-848), RESTRICT since 000260: the
	// area of a stocktake, a location of its frozen expected stock and the
	// place found of a scan.
	DependencyStocktake     = "stocktake"
	DependencyStocktakeItem = "stocktake_item"
	DependencyStockScan     = "stock_scan"

	// Network and discovery objects bound to a site (E-71); the kind is the
	// name of their table.
	DependencyVLAN            = "vlan"
	DependencySubnet          = "subnet"
	DependencyCollector       = "collector"
	DependencyDiscoveryScope  = "discovery_scope"
	DependencyEnrollmentToken = "agent_enrollment_token"
	DependencyCIMerge         = "ci_merge"

	// DependencyEnrollmentCode is a collector enrollment code bound to the
	// site (000187), RESTRICT since 000189 (F-430).
	DependencyEnrollmentCode = "collector_enrollment_code"
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
	// The stock of consumables per bin (stock_level, WP-283; the former
	// quantity_item is dropped since WP-758).
	{DependencyStockLevel, `EXISTS (SELECT 1 FROM stock_level s WHERE s.location_id = $1)`},
	// Movements of the ledger stock_movement (WP-744; the former
	// asset_movement is dropped since 000246, WP-842).
	{DependencyStockMovement, `EXISTS (SELECT 1 FROM stock_movement m WHERE m.from_location_id = $1 OR m.to_location_id = $1)`},
	{DependencyRackMount, `EXISTS (SELECT 1 FROM rack_mount rm WHERE rm.rack_id = $1)`},
	{DependencyRoleAssignment, `EXISTS (SELECT 1 FROM role_assignment ra WHERE ra.scope_site_id = $1)`},
	{DependencyCIAttribute, ciAttributeReference},
	{DependencyInternalOrder, `EXISTS (SELECT 1 FROM internal_order o WHERE o.target_location_id = $1)`},
	{DependencyStocktake, `EXISTS (SELECT 1 FROM stocktake st WHERE st.location_id = $1)`},
	{DependencyStocktakeItem, `EXISTS (SELECT 1 FROM stocktake_item si WHERE si.location_id = $1)`},
	{DependencyStockScan, `EXISTS (SELECT 1 FROM stock_scan ss WHERE ss.location_id = $1)`},
}

// ciAttributeReference is the condition of DependencyCIAttribute (MET-12,
// WP-713): a visible CI that is not deleted (nor merged) holds the id of the
// location $1 as the value of an attribute with target location. Type,
// set and global definitions name the target through
// reticora_reference_target (a list of a multi_value attribute counts with
// each element); the key together with a value that is the id of this very
// location suffices, the applicability of a type definition to the type of
// the CI is not checked again. Instance definitions (ci_instance_field_
// definition) are those of the CI with data_type location or reference with
// reference_target location and their values in attributes->'_instance'.
// The comparison ignores the case of the stored id. A JSONB value has no
// foreign key: a CI the caller cannot see does not keep the location, its
// value then reads as missing (include=references).
const ciAttributeReference = `(EXISTS (SELECT 1 FROM attribute_definition d
		JOIN ci ON ci.attributes ? d.key AND (d.organization_id IS NULL OR d.organization_id = ci.organization_id)
		WHERE reticora_reference_target(d) = 'location' AND ci.deleted_at IS NULL
		AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(CASE jsonb_typeof(ci.attributes -> d.key)
			WHEN 'array' THEN ci.attributes -> d.key
			WHEN 'string' THEN jsonb_build_array(ci.attributes -> d.key)
			ELSE '[]'::jsonb END) v WHERE lower(v) = $1::text))
	OR EXISTS (SELECT 1 FROM ci_instance_field_definition f
		JOIN ci ON ci.id = f.ci_id
		WHERE ci.deleted_at IS NULL
		AND (f.data_type = 'location' OR (f.data_type = 'reference' AND f.reference_target = 'location'))
		AND lower(ci.attributes -> '_instance' ->> f.name) = $1::text))`

// siteObject is a kind of object bound to a site by its site_id. lapsed,
// when set, is the condition under which a row no longer keeps the site.
type siteObject struct {
	kind, lapsed string
}

// siteObjects are the network and discovery objects bound to a site. E-71
// is open; its transitional rule (option 1) applies: while one of them is
// bound to the site, the site is not deleted (409 with the kind, RESTRICT
// since 000157, for collector enrollment codes since 000189) and the objects
// have to be removed or moved first. Expired enrollment tokens, expired or
// used collector enrollment codes and merge tombstones past tombstone_until
// do not count and are removed in the delete transaction (removeLapsed).
// Another option of E-71 changes only this list and the foreign keys of a
// follow-up migration.
var siteObjects = []siteObject{
	{DependencyVLAN, ""},
	{DependencySubnet, ""},
	{DependencyCollector, ""},
	{DependencyDiscoveryScope, ""},
	{DependencyEnrollmentToken, "expires_at <= now()"},
	{DependencyCIMerge, "tombstone_until <= now()"},
	// A code no longer enrolls a collector once it expired or was used
	// (EnrollCollector).
	{DependencyEnrollmentCode, "expires_at <= now() OR used_at IS NOT NULL"},
}

// referenceKinds maps the table of a violated foreign key to the kind it
// reports when it is not the name of a kind: the nodes of the tree are
// child locations, the site scope of a service account is a role
// assignment. The ledger stock_movement and the stock per bin stock_level
// are kinds by their table names.
var referenceKinds = map[string]string{
	"location":             DependencyLocation,
	"building":             DependencyLocation,
	"room":                 DependencyLocation,
	"rack":                 DependencyLocation,
	"service_account_role": DependencyRoleAssignment,
	// A reservation carries the site of its asset (reservation_site_id_fkey,
	// 000258, WP-750) and references a site only while its object stands
	// there; the object is the dependency.
	"reservation": DependencyAsset,
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
		tag, err := tx.Exec(ctx, "DELETE FROM "+o.kind+" WHERE site_id = $1 AND ("+o.lapsed+")", id)
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
