package server

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
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
