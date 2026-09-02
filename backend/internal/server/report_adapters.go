package server

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/jackc/pgx/v5/pgxpool"
)

// auditVerifier adapts audit.Verify (which needs the pool) to the
// compliance.AuditVerifier port used by the security report.
type auditVerifier struct {
	pool *pgxpool.Pool
}

func (v auditVerifier) Verify(ctx context.Context, orgID string) (compliance.AuditIntegrity, error) {
	result, err := audit.Verify(ctx, v.pool, orgID)
	if err != nil {
		return compliance.AuditIntegrity{}, err
	}
	return compliance.AuditIntegrity{
		Intact:       result.Intact,
		Checked:      result.Checked,
		BrokenReason: result.BrokenReason,
	}, nil
}

// entitlementLister adapts entitlement.Service to the
// compliance.EntitlementLister port used by the security report.
type entitlementLister struct {
	svc *entitlement.Service
}

func (l entitlementLister) List(ctx context.Context, orgID string) ([]compliance.EntitlementStatus, error) {
	ents, err := l.svc.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]compliance.EntitlementStatus, len(ents))
	for i, ent := range ents {
		out[i] = compliance.EntitlementStatus{FeatureKey: ent.FeatureKey, Enabled: ent.Enabled}
	}
	return out, nil
}

// assetCreator adapts asset.Repository to the movement.AssetCreator port used
// by the quantity→serialized conversion endpoint (spec §6).
type assetCreator struct {
	repo asset.Repository
}

func (c assetCreator) Create(ctx context.Context, ref *movement.AssetRef) (string, error) {
	a := &asset.Asset{
		OrganizationID: ref.OrganizationID,
		ClientID:       ref.ClientID,
		AssetTag:       ref.AssetTag,
		Name:           ref.Name,
		Category:       ref.Category,
		Status:         "in_stock",
		SerialNumber:   ref.SerialNumber,
		CustomFields:   map[string]any{},
	}
	if ref.LocationID != "" {
		a.CustomFields["location_id"] = ref.LocationID
	}
	if err := c.repo.Create(ctx, a); err != nil {
		return "", err
	}
	return a.ID, nil
}
