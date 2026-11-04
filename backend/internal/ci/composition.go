package ci

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The child contract of the physical composition on parent_ci_id (AST-05,
// AST-04, WP-273, E-123). A CI with a parent is a configuration child or a
// standalone child:
//
//   - configuration: the CI has no asset of its own. Serial number,
//     location, physical lifecycle and inventory data come from the nearest
//     ancestor with an asset (a standalone child or the root) and are shown
//     read-only (Item.Asset, Item.AssetSourceCIID); its location follows
//     its parent's. Parent and child have the same client.
//   - standalone: own asset (WP-272), serial number, location and
//     lifecycle; the default for a parent set without a mode (API-01).
//
// The checks of the contract here hold in both profiles; the database
// (migration 000244) enforces it as well and derives the location.

// Child modes (ci.child_mode).
const (
	ChildModeConfiguration = "configuration"
	ChildModeStandalone    = "standalone"
)

var (
	// ErrConfigurationChildren: the CI to delete has configuration
	// children that are not deleted (409, E-123 (b)); they are moved or
	// deleted first.
	ErrConfigurationChildren = errors.New("the CI has configuration children; move or delete them first")
	// ErrConfigurationParentDeleted: the configuration child to restore has
	// a deleted parent (409, E-123 (b)).
	ErrConfigurationParentDeleted = errors.New("the parent of the configuration child is deleted; restore it first")
	// errMergeChildren: the children of the victim cannot move to the
	// survivor (a cycle, also the survivor as a child of the victim, or a
	// configuration child of another client); a merge conflict (409).
	errMergeChildren error = mergeChildrenConflict{}
)

// mergeChildrenConflict is errMergeChildren: its own text, answered as
// ErrMergeConflict.
type mergeChildrenConflict struct{}

func (mergeChildrenConflict) Error() string {
	return "the children of the victim cannot move to the survivor (cycle or configuration child of another client)"
}

func (mergeChildrenConflict) Is(target error) bool { return target == ErrMergeConflict } //nolint:errorlint // sentinel identity

// Violation details of the contract.
const (
	childModeUnknown          = "must be configuration or standalone"
	childModeWithoutParent    = "a child mode requires parent_ci_id"
	configurationHasAsset     = "a configuration child has no asset of its own; its asset data come from its parent"
	locationOwnedByParent     = "the location of a configuration child is the location of its parent"
	serialOwnedByParent       = "the serial number of a configuration child is its parent asset's; the CI keeps the observed value"
	configurationOtherClient  = "a configuration child and its parent belong to the same client"
	parentOfConfigurationKids = "the CI has configuration children of another client"
)

// checkCreateChildMode checks the child contract of a new CI (POST /cis):
// a known mode, only with a parent; a configuration child without asset and
// without a location of its own.
func checkCreateChildMode(item *Item) error {
	mode := strings.TrimSpace(item.ChildMode)
	if mode == "" {
		return nil
	}
	if mode != ChildModeConfiguration && mode != ChildModeStandalone {
		return childViolation("child_mode", childModeUnknown)
	}
	if strings.TrimSpace(item.ParentCIID) == "" {
		return childViolation("child_mode", childModeWithoutParent)
	}
	item.ChildMode = mode
	if mode != ChildModeConfiguration {
		return nil
	}
	if strings.TrimSpace(item.AssetID) != "" {
		return childViolation("asset_id", configurationHasAsset)
	}
	if strings.TrimSpace(item.LocationID) != "" {
		return childViolation("location_id", locationOwnedByParent)
	}
	return nil
}

// childModeAfter returns the parent and the child mode a CI has after the
// update req: no mode without a parent, standalone for a parent without
// one. explicit reports a mode the update names.
func childModeAfter(before *Item, req *UpdateRequest) (parent, mode string, explicit bool) {
	parent, mode = before.ParentCIID, before.ChildMode
	if req.ParentCIID != nil {
		parent = strings.TrimSpace(*req.ParentCIID)
	}
	if req.ChildMode != nil {
		mode, explicit = strings.TrimSpace(*req.ChildMode), strings.TrimSpace(*req.ChildMode) != ""
	}
	switch {
	case parent == "":
		mode = ""
	case mode == "":
		mode = ChildModeStandalone
	}
	return parent, mode, explicit
}

// checkChildContract checks an update of the CI before against the child
// contract: a mode is known and needs a parent; a configuration child has
// no asset (child_mode for a CI that keeps its asset, asset_id for an asset
// the update links); a manual write of the location or the serial number of
// a configuration child is refused (DB-05, like checkAssetOwnedFields).
func checkChildContract(before *Item, req *UpdateRequest) error {
	if req.ChildMode == nil && req.ParentCIID == nil && req.AssetID == nil &&
		(before.ChildMode != ChildModeConfiguration || req.Manual == nil) {
		return nil
	}
	if req.ChildMode != nil {
		if m := strings.TrimSpace(*req.ChildMode); m != "" && m != ChildModeConfiguration && m != ChildModeStandalone {
			return childViolation("child_mode", childModeUnknown)
		}
	}
	parent, mode, explicit := childModeAfter(before, req)
	if explicit && parent == "" {
		return childViolation("child_mode", childModeWithoutParent)
	}
	if mode != ChildModeConfiguration {
		return nil
	}
	if req.AssetID != nil && strings.TrimSpace(*req.AssetID) != "" {
		return childViolation("asset_id", configurationHasAsset)
	}
	if req.AssetID == nil && before.AssetID != "" {
		return childViolation("child_mode", configurationHasAsset)
	}
	if req.Manual == nil {
		return nil
	}
	var violations []Violation
	if req.LocationID != nil && strings.TrimSpace(*req.LocationID) != before.LocationID {
		violations = append(violations, Violation{Field: "location_id", Detail: locationOwnedByParent})
	}
	if req.SerialNumber != nil && *req.SerialNumber != before.SerialNumber {
		violations = append(violations, Violation{Field: "serial_number", Detail: serialOwnedByParent})
	}
	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	return nil
}

// applyChildMode sets the child mode a CI has after a write (the memory
// repository; migration 000244 does it in the database): standalone for a
// parent without one, none without a parent.
func applyChildMode(item *Item) {
	switch {
	case strings.TrimSpace(item.ParentCIID) == "":
		item.ChildMode = ""
	case item.ChildMode == "":
		item.ChildMode = ChildModeStandalone
	}
}

func childViolation(field, detail string) error {
	return &ValidationError{Violations: []Violation{{Field: field, Detail: detail}}}
}

// childContractError maps the constraints of migration 000244 onto their
// field (locationError); nil for another constraint.
func childContractError(pgErr *pgconn.PgError) error {
	switch pgErr.ConstraintName {
	case "ci_child_mode_check":
		return childViolation("child_mode", childModeUnknown)
	case "ci_child_mode_parent":
		return childViolation("child_mode", childModeWithoutParent)
	case "ci_configuration_child_asset":
		return childViolation("asset_id", configurationHasAsset)
	case "ci_location_from_parent":
		return childViolation("location_id", locationOwnedByParent)
	case "ci_configuration_child_client":
		return childViolation("parent_ci_id", configurationOtherClient)
	case "ci_configuration_parent_client":
		return childViolation("client_id", parentOfConfigurationKids)
	}
	return nil
}

// mergeChildrenError turns a refused move of the victim's children (a
// cycle or a client conflict of a configuration child) into a merge
// conflict (409).
func mergeChildrenError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "ci_parent_cycle", "ci_parent_not_self", "ci_configuration_child_client", "ci_mounted_composition_child",
			"rack_mount_composition_child":
			return errMergeChildren
		}
	}
	return err
}

// checkNoConfigurationChildren refuses the deletion of a CI with
// configuration children that are not deleted (E-123 (b)).
func checkNoConfigurationChildren(ctx context.Context, tx pgx.Tx, orgID, id string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND parent_ci_id = $2
		AND child_mode = 'configuration' AND deleted_at IS NULL)`, orgID, id).Scan(&exists); err != nil {
		return fmt.Errorf("read configuration children: %w", err)
	}
	if exists {
		return ErrConfigurationChildren
	}
	return nil
}

// configurationParentDeletedSQL is true for a configuration child whose
// parent is deleted (restore, E-123 (b)); a column of a query on ci.
const configurationParentDeletedSQL = `(ci.child_mode = 'configuration' AND EXISTS (SELECT 1 FROM ci p
	WHERE p.id = ci.parent_ci_id AND p.organization_id = ci.organization_id AND p.deleted_at IS NOT NULL))`

// assetSourceSQL resolves the CI whose asset gives a CI its asset data, for
// all CIs of a page in one statement (loadAssetFacts): a linked CI ($3, its
// asset $4) is its own source; for a configuration child ($1, its parent
// $2) the walk goes up while the CI is a configuration child and stops at
// the first other one (a standalone child or the root), whose asset, if
// any, is the source. Ancestors the principal cannot see end the walk
// without a source. Rows: CI, source CI, asset.
const assetSourceSQL = `
	WITH RECURSIVE up (start, id, depth) AS (
		SELECT s.start, s.parent, 1 FROM unnest($1::uuid[], $2::uuid[]) AS s(start, parent)
		UNION ALL
		SELECT up.start, c.parent_ci_id, up.depth + 1
		FROM up JOIN ci c ON c.id = up.id
		WHERE c.child_mode = 'configuration' AND c.parent_ci_id IS NOT NULL AND up.depth < 1000
	), source AS (
		SELECT DISTINCT ON (up.start) up.start, c.id AS ci_id, c.asset_id
		FROM up JOIN ci c ON c.id = up.id
		WHERE c.child_mode IS DISTINCT FROM 'configuration'
		ORDER BY up.start, up.depth
	)
	SELECT source.start::text, source.ci_id::text, source.asset_id FROM source WHERE source.asset_id IS NOT NULL
	UNION ALL
	SELECT o.id::text, o.id::text, o.asset_id FROM unnest($3::uuid[], $4::uuid[]) AS o(id, asset_id)`
