package server

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/composition"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
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

// compositionParentLookup adapts composition.Repository to the
// asset.ParentLookup port (spec §5: child assets inherit shared inventory
// fields from the parent read-only).
type compositionParentLookup struct {
	repo composition.Repository
}

func (l compositionParentLookup) ParentOfAsset(ctx context.Context, orgID, assetID string) (bool, error) {
	return composition.ParentOfAsset(ctx, l.repo, orgID, assetID)
}

// overrideProvenance adapts override.Repository to the
// discovery.ProvenanceRecorder port (spec §13).
type overrideProvenance struct {
	repo override.Repository
}

func (a overrideProvenance) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string) (*discovery.FieldProvenance, error) {
	fv, err := a.repo.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (a overrideProvenance) IsProtected(ctx context.Context, orgID, ciID, fieldName string) (bool, error) {
	return override.IsProtected(ctx, a.repo, orgID, ciID, fieldName)
}

// ciLookup adapts ci.Repository to the asset.CILookup port (spec §4: the
// asset reads the linked CI's technical identity read-only).
type ciLookup struct {
	repo ci.Repository
}

func (l ciLookup) GetByID(ctx context.Context, orgID, id string) (*asset.CIRef, error) {
	item, err := l.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return &asset.CIRef{
		ID:              item.ID,
		Name:            item.Name,
		Status:          item.Status,
		Hostname:        item.Hostname,
		FQDN:            item.FQDN,
		ManagementIP:    item.ManagementIP,
		Manufacturer:    item.Manufacturer,
		Model:           item.Model,
		SerialNumber:    item.SerialNumber,
		OSName:          item.OSName,
		OSVersion:       item.OSVersion,
		DiscoverySource: item.DiscoverySource,
	}, nil
}

// parentAssetLookup adapts asset.Repository to the composition.AssetLookup
// port (spec §5: a child CI displays the parent asset's shared inventory
// identity read-only instead of duplicating it). It is the mirror image of
// ciLookup, which projects the opposite direction.
type parentAssetLookup struct {
	repo asset.Repository
}

func (l parentAssetLookup) GetByID(ctx context.Context, orgID, id string) (*composition.ParentAssetRef, error) {
	a, err := l.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return &composition.ParentAssetRef{
		ID:            a.ID,
		Name:          a.Name,
		AssetTag:      a.AssetTag,
		Status:        a.Status,
		SerialNumber:  a.SerialNumber,
		Barcode:       a.Barcode,
		RFIDTag:       a.RFIDTag,
		PurchaseDate:  a.PurchaseDate,
		PurchaseCost:  a.PurchaseCost,
		Currency:      a.Currency,
		Supplier:      a.Supplier,
		InvoiceNumber: a.InvoiceNumber,
		WarrantyEnd:   a.WarrantyEnd,
		Location:      a.Location,
	}, nil
}
