package asset

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The link between an asset and its CIs (AST-04, AST-03, AST-01, WP-272):
// ci.asset_id is the link (n CIs per asset), the primary CI asset.ci_id
// (API name primary_ci_id, E-122) is optional and names one of them. Every
// link and unlink of the API runs in one transaction through the CI domain
// (ci.Service.Update with AssetID, Manual): the CI's version rises, one
// ci_change row asset_id and one ci.changed are written; only then is
// asset.ci_id written (lock order CI → asset). The database keeps both
// directions consistent (migration 000235): a CI that leaves its asset
// clears that asset's primary CI (ci_sync_asset_primary), a CI and its
// asset of different clients are refused (ci_asset_client).

// CILinker reads and writes CIs for the links: ci.Service in production,
// so the link is a manual CI change like PATCH /api/v1/cis/{id}.
type CILinker interface {
	GetByID(ctx context.Context, orgID, id string) (*ci.Item, error)
	Update(ctx context.Context, orgID, id string, req ci.UpdateRequest) (*ci.Item, error)
}

// LinkStore is the part of the repository the links need: one
// transaction the CI domain joins, the primary CI and the audit entries.
type LinkStore interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
	SetPrimaryCI(ctx context.Context, orgID, assetID, ciID string) (*Asset, error)
	RecordLink(ctx context.Context, orgID, action, assetID, ciID string, primary bool) error
}

// Audit actions of the link API (AUD-07).
const (
	ActionLinked   = "asset.linked"
	ActionUnlinked = "asset.unlinked"
)

var (
	// ErrCINotFound: the CI of a link is unknown, outside the caller's
	// scope or not linked to the asset (404, TEN-04).
	ErrCINotFound = errors.New("CI not found")
	// ErrNotWritable: the asset is visible but the caller may not change
	// it (a client-scoped principal and an org-wide asset, TEN-05, 403).
	ErrNotWritable = errors.New("the asset may not be changed in the caller's scope")
)

var _ LinkStore = (*PGRepository)(nil)

// WithCILinks attaches the CI domain the links write through; without it
// the link routes answer 503 and a primary_ci_id of POST/PATCH
// /api/v1/assets is linked by the trigger asset_sync_primary_ci only.
func (h *Handler) WithCILinks(cis CILinker) *Handler {
	h.links = cis
	return h
}

// linkStore returns the repository as LinkStore when links are attached.
func (h *Handler) linkStore() (LinkStore, bool) {
	if h.links == nil {
		return nil, false
	}
	store, ok := h.repo.(LinkStore)
	return store, ok
}

// requirePermission answers 403 unless the principal of the request holds
// perm (fail-closed without one).
func requirePermission(w http.ResponseWriter, r *http.Request, perm identity.Permission) bool {
	if p, ok := identity.PrincipalFromContext(r.Context()); ok && p.Has(perm) {
		return true
	}
	api.WriteError(w, http.StatusForbidden, "Forbidden", "the permission "+string(perm)+" is required")
	return false
}

// LinkCI handles POST /api/v1/assets/{id}/cis: links the CI ci_id to the
// asset (a CI of another asset moves, as with PATCH /cis/{id}) and, with
// primary, sets or clears the asset's primary CI. The route needs
// ci:write, the handler also asset:write.
func (h *Handler) LinkCI(w http.ResponseWriter, r *http.Request) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	store, ok := h.linkStore()
	if !ok {
		api.WriteUnavailable(w, "asset links are not available")
		return
	}
	if !requirePermission(w, r, identity.PermAssetWrite) {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req LinkCIRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ciID := strings.ToLower(strings.TrimSpace(req.CIID))
	if ciID == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "ci_id", Message: "is required"})
		return
	}
	if !api.IsUUID(ciID) {
		writeAssetError(w, r, ErrCINotFound)
		return
	}
	var link CILink
	err := store.RunInTx(r.Context(), orgID, func(ctx context.Context) error {
		current, err := h.repo.GetByID(ctx, orgID, strings.ToLower(id))
		if err != nil {
			return err
		}
		if linkErr := h.linkCI(ctx, orgID, current.ID, ciID); linkErr != nil {
			return linkErr
		}
		primary := strings.EqualFold(current.CIID, ciID)
		if req.Primary != nil && *req.Primary != primary {
			target := ""
			if *req.Primary {
				target = ciID
			}
			if _, setErr := store.SetPrimaryCI(ctx, orgID, current.ID, target); setErr != nil {
				return setErr
			}
			primary = *req.Primary
		}
		link = CILink{AssetID: current.ID, CIID: ciID, Primary: primary}
		return store.RecordLink(ctx, orgID, ActionLinked, current.ID, ciID, primary)
	})
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, link)
}

// UnlinkCI handles DELETE /api/v1/assets/{id}/cis/{ci_id}: unlinks the CI
// from the asset; a primary CI is cleared with it (ci_sync_asset_primary),
// and when the caller may not change the asset (TEN-05) nothing changes
// (403). A CI that is not linked to the asset is 404.
func (h *Handler) UnlinkCI(w http.ResponseWriter, r *http.Request) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	store, ok := h.linkStore()
	if !ok {
		api.WriteUnavailable(w, "asset links are not available")
		return
	}
	if !requirePermission(w, r, identity.PermAssetWrite) {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	ciID := strings.ToLower(chi.URLParam(r, "ci_id"))
	if !api.IsUUID(ciID) {
		writeAssetError(w, r, ErrCINotFound)
		return
	}
	err := store.RunInTx(r.Context(), orgID, func(ctx context.Context) error {
		current, err := h.repo.GetByID(ctx, orgID, strings.ToLower(id))
		if err != nil {
			return err
		}
		item, err := h.links.GetByID(ctx, orgID, ciID)
		if err != nil {
			return ciLinkError(err)
		}
		if !strings.EqualFold(item.AssetID, current.ID) {
			return ErrCINotFound
		}
		if unlinkErr := h.writeCILink(ctx, orgID, item.ID, ""); unlinkErr != nil {
			return unlinkErr
		}
		return store.RecordLink(ctx, orgID, ActionUnlinked, current.ID, item.ID, strings.EqualFold(current.CIID, item.ID))
	})
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// linkCI links the CI ciID to the asset assetID in the transaction of ctx
// unless it is linked to it already. An unknown or invisible CI is
// ErrCINotFound.
func (h *Handler) linkCI(ctx context.Context, orgID, assetID, ciID string) error {
	item, err := h.links.GetByID(ctx, orgID, ciID)
	if err != nil {
		return ciLinkError(err)
	}
	if strings.EqualFold(item.AssetID, assetID) {
		return nil
	}
	return h.writeCILink(ctx, orgID, item.ID, assetID)
}

// writeCILink sets ci.asset_id of the CI ("" unlinks) as a manual change
// of the CI domain (PATCH /cis/{id}): field permissions, protected
// override, version, ci_change, audit ci.updated and ci.changed; the CI was
// read in the same transaction, so any version is the base. A standalone
// asset without location takes the CI's location (E-81).
func (h *Handler) writeCILink(ctx context.Context, orgID, ciID, assetID string) error {
	anyVersion := api.IfMatchAny
	_, err := h.links.Update(ctx, orgID, ciID, ci.UpdateRequest{
		AssetID: &assetID, IfMatch: &anyVersion, Manual: &ci.ManualChange{Author: tenant.FromContext(ctx).UserID},
	})
	return ciLinkError(err)
}

// ciLinkError turns an error of the CI domain into the answer of the link:
// an unknown or invisible CI is ErrCINotFound, a violation (another
// client's asset, an asset that cannot take the CI's location) 422 on its
// field. Field permissions (403) and database errors pass.
func ciLinkError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ci.ErrNotFound) {
		return ErrCINotFound
	}
	if ve, ok := ci.AsValidationError(err); ok && len(ve.Violations) > 0 {
		v := ve.Violations[0]
		return &api.FieldError{Field: v.Field, Message: v.Detail, Status: http.StatusUnprocessableEntity}
	}
	return err
}

// primaryCIError reports an error of linking the primary CI of POST/PATCH
// /api/v1/assets on primary_ci_id (WP-407 error contract): an unknown CI
// and a violation on the CI's asset_id.
func primaryCIError(err error) error {
	var fe *api.FieldError
	switch {
	case errors.Is(err, ErrCINotFound):
		return &api.FieldError{Field: fieldPrimaryCI, Message: "CI not found", Status: http.StatusUnprocessableEntity}
	case errors.As(err, &fe) && fe.Field == "asset_id":
		return &api.FieldError{Field: fieldPrimaryCI, Message: fe.Message, Status: fe.Status, Code: fe.Code}
	}
	return err
}

// createLinked creates the asset with its primary CI in one transaction:
// the asset without it, the link through the CI domain, then asset.ci_id.
func (h *Handler) createLinked(ctx context.Context, store LinkStore, a *Asset) error {
	ciID := strings.ToLower(strings.TrimSpace(a.CIID))
	if err := api.RequireUUID(fieldPrimaryCI, ciID); err != nil {
		return err
	}
	return store.RunInTx(ctx, a.OrganizationID, func(ctx context.Context) error {
		a.CIID = ""
		if err := h.repo.Create(ctx, a); err != nil {
			return err
		}
		if err := primaryCIError(h.linkCI(ctx, a.OrganizationID, a.ID, ciID)); err != nil {
			return err
		}
		linked, err := store.SetPrimaryCI(ctx, a.OrganizationID, a.ID, ciID)
		if err != nil {
			return err
		}
		*a = *linked
		return nil
	})
}

// update runs PATCH /api/v1/assets/{id}: a sent primary CI is linked
// through the CI domain first, in the transaction of the asset update.
func (h *Handler) update(ctx context.Context, orgID, id string, req *UpdateRequest) (*Asset, error) {
	store, ok := h.linkStore()
	if !ok || req.CIID == nil || strings.TrimSpace(*req.CIID) == "" {
		return h.repo.Update(ctx, orgID, id, req)
	}
	ciID := strings.ToLower(strings.TrimSpace(*req.CIID))
	if err := api.RequireUUID(fieldPrimaryCI, ciID); err != nil {
		return nil, err
	}
	req.CIID = &ciID
	var out *Asset
	err := store.RunInTx(ctx, orgID, func(ctx context.Context) error {
		current, err := h.repo.GetByID(ctx, orgID, id)
		if err != nil {
			return err
		}
		if linkErr := primaryCIError(h.linkCI(ctx, orgID, current.ID, ciID)); linkErr != nil {
			return linkErr
		}
		out, err = h.repo.Update(ctx, orgID, id, req)
		return err
	})
	return out, err
}

// RunInTx runs fn in one transaction of the context's tenant scope: the
// repository calls with fn's context, those of the CI domain included,
// join it (WP-272).
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

// SetPrimaryCI writes the primary CI (asset.ci_id) of the asset; "" clears
// it. The asset's history records the change. An asset the caller sees but
// may not change (asset_scoped_update, 000079) is ErrNotWritable.
func (r *PGRepository) SetPrimaryCI(ctx context.Context, orgID, assetID, ciID string) (*Asset, error) {
	var out *Asset
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := r.getLocked(ctx, tx, orgID, assetID, "")
		if err != nil {
			return err
		}
		if strings.EqualFold(current.CIID, ciID) {
			out = current
			return nil
		}
		query := fmt.Sprintf(`UPDATE asset SET ci_id = NULLIF($3, '')::uuid, updated_at = NOW()
			WHERE id = $1 AND organization_id = $2 RETURNING %s`, assetSelectColumns)
		out, err = scanAsset(tx.QueryRow(ctx, query, assetID, orgID, ciID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotWritable
		}
		if err != nil {
			return fmt.Errorf("set primary ci: %w", assetFieldDBError(err, primaryCIConstraintFields))
		}
		// Trail with the version and asset.changed (WP-740).
		return r.recordUpdate(ctx, tx, orgID, current, out)
	})
	return out, err
}

// RecordLink writes the audit entry asset.linked or asset.unlinked (AUD-07)
// with the CI and whether it is the primary CI, last in the transaction
// (database.BeforeCommit); source and request come from the request.
func (r *PGRepository) RecordLink(ctx context.Context, orgID, action, assetID, ciID string, primary bool) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		actorID, actorType := "", "system"
		if user := strings.TrimSpace(tenant.FromContext(ctx).UserID); user != "" {
			actorType = "user"
			if api.IsUUID(user) {
				actorID = user
			}
		}
		entry := audit.Entry{
			OrganizationID: orgID,
			ActorID:        actorID,
			ActorType:      actorType,
			Action:         action,
			ResourceType:   "asset",
			ResourceID:     assetID,
			Changes:        map[string]any{"ci_id": ciID, "primary": primary},
		}
		return database.BeforeCommit(ctx, func() error {
			if _, err := audit.NewPGRecorder().Record(ctx, tx, entry); err != nil {
				return fmt.Errorf("audit %s: %w", action, err)
			}
			return nil
		})
	})
}
