// Package entitlement provides the entitlement/licensing service.
// It controls which modules and features are available per tenant.
package entitlement

import (
	"context"
	"sync"
)

// Plan represents a subscription tier.
type Plan string

const (
	PlanEssential  Plan = "essential"
	PlanStandard   Plan = "standard"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise"
)

// Entitlement represents a feature entitlement for a tenant.
type Entitlement struct {
	OrganizationID string `json:"organization_id"`
	FeatureKey     string `json:"feature_key"`
	Plan           Plan   `json:"plan"`
	Limit          *int64 `json:"limit,omitempty"` // nil = unlimited
	Enabled        bool   `json:"enabled"`
}

// Service provides entitlement checks.
type Service struct {
	mu sync.RWMutex
	// In production this would query the database; for now in-memory.
	entitlements map[string][]Entitlement
}

// NewService creates a new entitlement service.
func NewService() *Service {
	return &Service{
		entitlements: make(map[string][]Entitlement),
	}
}

// List returns all entitlements for the given organization.
func (s *Service) List(orgID string) []Entitlement {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := s.entitlements[orgID]
	result := make([]Entitlement, len(items))
	copy(result, items)
	return result
}

// IsEnabled checks if a feature is enabled for the given organization.
func (s *Service) IsEnabled(ctx context.Context, orgID, featureKey string) bool {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

	ents := s.entitlements[orgID]
	for i := range ents {
		if ents[i].FeatureKey == featureKey {
			ents[i].Plan = plan
			ents[i].Enabled = true
			s.entitlements[orgID] = ents
			return
		}
	}

	s.entitlements[orgID] = append(ents, Entitlement{
		OrganizationID: orgID,
		FeatureKey:     featureKey,
		Plan:           plan,
		Enabled:        true,
	})
}
