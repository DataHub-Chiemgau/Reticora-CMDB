package entitlement

import (
	"context"
	"log/slog"
)

// UnlicensedAdopter creates the CIs of devices held as unlicensed_ci while
// max_cis allows (ENT-03, CH21). The discovery handler implements it.
type UnlicensedAdopter interface {
	AdoptUnlicensed(ctx context.Context, orgID string) (int, error)
}

// WithUnlicensed adopts held devices whenever cmdb_core (and with it
// max_cis) changes: raising the limit creates the waiting CIs.
func (s *Service) WithUnlicensed(adopter UnlicensedAdopter) *Service {
	s.adopter = adopter
	return s
}

// adoptAfterGrant runs the adoption after a change of cmdb_core. A failure
// leaves the items open (nothing is lost) and is logged; the next change or
// a manual resolve adopts them.
func (s *Service) adoptAfterGrant(ctx context.Context, ent *Entitlement) {
	if s.adopter == nil || ent.FeatureKey != FeatureCMDBCore {
		return
	}
	if _, err := s.adopter.AdoptUnlicensed(ctx, ent.OrganizationID); err != nil {
		slog.ErrorContext(ctx, "adopting unlicensed_ci items failed", "error", err, "organization_id", ent.OrganizationID)
	}
}
