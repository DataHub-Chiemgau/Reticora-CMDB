package export

import (
	"context"
	"fmt"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// FormatDATEV is the format value of the DATEV export (EXP-01, E-141).
const FormatDATEV = "datev"

// moduleInventory is the late module the DATEV export belongs to (WP-214,
// E-03); export does not import server, so the key is repeated here.
const moduleInventory = "inventory"

// EntitlementChecker reports whether an organization may use a feature
// (entitlement.Service.IsEnabled; an error counts as not enabled).
type EntitlementChecker interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

// DATEVGate guards the DATEV format on every export path (ENT-02, ENT-05,
// WP-790): POST /api/v1/exports, POST /api/v1/export/jobs, GET
// /api/v1/export/cis?format=datev and the job worker. The format value sits
// in the body or the query, so the path gate (export_csv) cannot tell it
// apart. DATEV needs the module inventory, the entitlements export_datev
// and assets_basic and, for a request, the permission asset:read. A nil
// gate, or one without entitlements, refuses DATEV (fail closed).
type DATEVGate struct {
	entitlements     EntitlementChecker
	inventoryEnabled bool
}

// NewDATEVGate creates the gate. entitlements answers per organization;
// inventoryEnabled is the state of the module inventory at startup.
func NewDATEVGate(entitlements EntitlementChecker, inventoryEnabled bool) *DATEVGate {
	return &DATEVGate{entitlements: entitlements, inventoryEnabled: inventoryEnabled}
}

// DATEVRefusal is why the DATEV format is refused: a switched-off module, a
// missing entitlement or a missing permission. Its message names no
// internals, so a failed job keeps it (jobs.PublicError, F-372).
type DATEVRefusal struct {
	// Module is the switched-off module.
	Module string
	// Feature is the missing entitlement.
	Feature string
	// Permission is the missing permission.
	Permission identity.Permission
}

// validation: a fixed text with the module, feature key or permission.
func (e *DATEVRefusal) Error() string {
	switch {
	case e.Module != "":
		return fmt.Sprintf("format %q is not available: module %q is disabled", FormatDATEV, e.Module)
	case e.Feature != "":
		return (&entitlement.FeatureNotEntitledError{FeatureKey: e.Feature}).Error()
	default:
		return "missing required permission: " + string(e.Permission)
	}
}

// PublicMessage is the error of a failed DATEV job.
func (e *DATEVRefusal) PublicMessage() string { return e.Error() }

// Allow checks the module and the entitlements of the organization; ctx
// must carry the tenant scope the entitlements are read with. The
// permission is not checked here: a job counts it as checked when it was
// created. A refusal is a *DATEVRefusal.
func (g *DATEVGate) Allow(ctx context.Context, orgID string) error {
	if refusal := g.refuse(ctx, orgID); refusal != nil {
		return refusal
	}
	return nil
}

func (g *DATEVGate) refuse(ctx context.Context, orgID string) *DATEVRefusal {
	if g == nil || g.entitlements == nil {
		return &DATEVRefusal{Feature: entitlement.FeatureExportDATEV}
	}
	if !g.inventoryEnabled {
		return &DATEVRefusal{Module: moduleInventory}
	}
	for _, feature := range []string{entitlement.FeatureExportDATEV, entitlement.FeatureAssetsBasic} {
		if !g.entitlements.IsEnabled(ctx, orgID, feature) {
			return &DATEVRefusal{Feature: feature}
		}
	}
	return nil
}

// refuseRequest is refuse plus the permission asset:read of the caller: a
// request without a principal lacks it.
//
// validation: a DATEVRefusal names only the module, the feature key or the
// permission (fixed texts).
func (g *DATEVGate) refuseRequest(r *http.Request, orgID string) *DATEVRefusal {
	if refusal := g.refuse(r.Context(), orgID); refusal != nil {
		return refusal
	}
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok || !principal.Has(identity.PermAssetRead) {
		return &DATEVRefusal{Permission: identity.PermAssetRead}
	}
	return nil
}

// guardDATEV answers a DATEV request the gate refuses and reports whether
// the request may go on: a switched-off module is 400 like an unknown
// format, a missing entitlement 403 feature-not-entitled, a missing
// permission 403 like the route permission check (authz.go).
func guardDATEV(w http.ResponseWriter, r *http.Request, gate *DATEVGate, orgID, format string) bool {
	if format != FormatDATEV {
		return true
	}
	refusal := gate.refuseRequest(r, orgID)
	switch {
	case refusal == nil:
		return true
	case refusal.Module != "":
		api.WriteError(w, http.StatusBadRequest, "Bad Request", refusal.Error())
	case refusal.Feature != "":
		notEntitled := &entitlement.FeatureNotEntitledError{FeatureKey: refusal.Feature}
		api.WriteProblem(w, http.StatusForbidden, notEntitled.ProblemType(), "Forbidden", notEntitled.Error())
	default:
		api.WriteError(w, http.StatusForbidden, "Forbidden", refusal.Error())
	}
	return false
}
