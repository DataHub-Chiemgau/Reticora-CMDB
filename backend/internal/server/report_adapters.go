package server

import (
	"context"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/composition"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
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

func (a overrideProvenance) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*discovery.FieldProvenance, error) {
	fv, err := a.repo.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source, observedAt)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

// RecordDiscoveredMany records the fields of a new CI in one statement when
// the repository can (NFR-03).
func (a overrideProvenance) RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	if many, ok := a.repo.(interface {
		RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error
	}); ok {
		return many.RecordDiscoveredMany(ctx, orgID, ciID, values, source, observedAt)
	}
	for field, value := range values {
		if _, err := a.repo.RecordDiscovered(ctx, orgID, ciID, field, value, source, observedAt); err != nil {
			return err
		}
	}
	return nil
}

// ClearOverride accepts the observed value of an override_conflict (REC-12).
func (a overrideProvenance) ClearOverride(ctx context.Context, orgID, ciID, field string) error {
	_, err := a.repo.ClearOverride(ctx, orgID, ciID, field)
	return err
}

func (a overrideProvenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, a.repo, w)
}

// edgeLicense adapts the entitlement service to the discovery license check
// of the collectors' edge data path.
type edgeLicense struct{ svc *entitlement.Service }

func (l edgeLicense) DiscoveryLicense(ctx context.Context, orgID string) (state, problemType string, err error) {
	ent, status, err := l.svc.Status(ctx, orgID, entitlement.FeatureDiscovery)
	if err != nil {
		return "", "", err
	}
	switch status {
	case entitlement.StatusActive:
		return discovery.LicenseActive, "", nil
	case entitlement.StatusExpired:
		expired := &entitlement.LicenseExpiredError{}
		if ent.ValidUntil != nil {
			expired.ValidUntil = *ent.ValidUntil
		}
		return discovery.LicenseExpired, expired.ProblemType(), nil
	default:
		return discovery.LicenseNotEntitled, (&entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureDiscovery}).ProblemType(), nil
	}
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

// bffSources hands the read models of topology, rack layout, search, the
// review inbox and jobs to the GraphQL BFF (GQL-02). Without the job model
// (no database) the jobs query is unavailable.
func bffSources(repos *Repositories, topo *topology.Handler, jobSource graphqlbff.JobSource) *graphqlbff.Sources {
	sources := &graphqlbff.Sources{Topology: topo, Racks: repos.Rack, Search: repos.Search, Jobs: jobSource}
	if reviewSource, ok := repos.Discovery.(graphqlbff.ReviewSource); ok {
		sources.Reviews = reviewSource
	}
	return sources
}

// ciAddressResolver assigns collector metric samples to the CI whose
// management IP the collector polled (MON-02).
type ciAddressResolver struct{ repo ci.Repository }

func (a ciAddressResolver) ResolveCI(ctx context.Context, orgID, address string) (ciID string, ok bool) {
	items, _, err := a.repo.List(ctx, orgID, ci.FilterParams{Filters: []api.Filter{
		{Field: "management_ip", Op: api.OpExact, Kind: api.KindText, Values: []string{address}},
	}}, api.PaginationParams{Limit: 2})
	if err != nil || len(items) != 1 {
		return "", false
	}
	return items[0].ID, true
}
