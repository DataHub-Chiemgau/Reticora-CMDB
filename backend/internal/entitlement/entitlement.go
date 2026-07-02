// Package entitlement provides the entitlement/licensing service.
// It controls which modules and features are available per tenant.
package entitlement

import "context"

// Plan represents a subscription tier.
type Plan string

const (
	PlanEssential Plan = "essential"
	PlanStandard  Plan = "standard"
	PlanPro       Plan = "pro"
	PlanEnterprise Plan = "enterprise"
)

// Entitlement represents a feature entitlement for a tenant.
type Entitlement struct {
	OrganizationID string
	FeatureKey     string
	Plan           Plan
	Limit          *int64 // nil = unlimited
	Enabled        bool
}

// Service provides entitlement checks.
type Service struct {
	// In production this would query the database; for now in-memory.
	entitlements map[string][]Entitlement
}

// NewService creates a new entitlement service.
func NewService() *Service {
	return &Service{
		entitlements: make(map[string][]Entitlement),
	}
}

// IsEnabled checks if a feature is enabled for the given organization.
func (s *Service) IsEnabled(ctx context.Context, orgID, featureKey string) bool {
	ents, ok := s.entitlements[orgID]
	if !ok {
		return false
	}
	for _, e := range ents {
		if e.FeatureKey == featureKey && e.Enabled {
			return true
		}
	}
	return false
}

// Grant grants a feature entitlement to an organization.
func (s *Service) Grant(orgID, featureKey string, plan Plan) {
	s.entitlements[orgID] = append(s.entitlements[orgID], Entitlement{
		OrganizationID: orgID,
		FeatureKey:     featureKey,
		Plan:           plan,
		Enabled:        true,
	})
}
