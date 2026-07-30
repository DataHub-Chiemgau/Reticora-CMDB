// Package entitlement provides the entitlement/licensing service.
// It controls which modules and feature limits are available per tenant.
//
// Entitlements are stored per organization in the `entitlement` table. Tenants
// without any stored rows fall back to the configured default plan, so a fresh
// organization is always usable while still being restricted to the feature set
// of its plan.
package entitlement

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Plan represents a subscription tier.
type Plan string

const (
	PlanEssential  Plan = "essential"
	PlanStandard   Plan = "standard"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise"
)

// Feature keys gated by the entitlement service. They mirror the product
// modules described in the README (section 13).
const (
	FeatureCMDB          = "cmdb"
	FeatureDiscovery     = "discovery"
	FeatureInventory     = "inventory"
	FeatureDocuments     = "documents"
	FeatureStocktake     = "stocktake"
	FeatureTicketing     = "ticketing"
	FeatureWebhooks      = "webhooks"
	FeatureExport        = "export"
	FeatureMonitoring    = "monitoring"
	FeatureIGA           = "iga"
	FeatureEndpointAgent = "endpoint_agent"
	FeatureWorkflowForms = "workflow_forms"
	FeatureCompliance    = "compliance"
)

// planFeatures maps a plan to the features it includes. Higher plans are
// supersets of the lower ones.
var planFeatures = map[Plan][]string{
	PlanEssential: {
		FeatureCMDB,
		FeatureDiscovery,
		FeatureInventory,
	},
	PlanStandard: {
		FeatureCMDB,
		FeatureDiscovery,
		FeatureInventory,
		FeatureDocuments,
		FeatureStocktake,
		FeatureTicketing,
		FeatureExport,
		FeatureWebhooks,
	},
	PlanPro: {
		FeatureCMDB,
		FeatureDiscovery,
		FeatureInventory,
		FeatureDocuments,
		FeatureStocktake,
		FeatureTicketing,
		FeatureExport,
		FeatureWebhooks,
		FeatureMonitoring,
		FeatureWorkflowForms,
	},
	PlanEnterprise: {
		FeatureCMDB,
		FeatureDiscovery,
		FeatureInventory,
		FeatureDocuments,
		FeatureStocktake,
		FeatureTicketing,
		FeatureExport,
		FeatureWebhooks,
		FeatureMonitoring,
		FeatureIGA,
		FeatureEndpointAgent,
		FeatureWorkflowForms,
		FeatureCompliance,
	},
}

// PlanFeatures returns the feature keys included in the given plan.
func PlanFeatures(plan Plan) []string {
	features := planFeatures[normalizePlan(plan)]
	out := make([]string, len(features))
	copy(out, features)
	return out
}

func normalizePlan(plan Plan) Plan {
	switch Plan(strings.ToLower(strings.TrimSpace(string(plan)))) {
	case PlanStandard:
		return PlanStandard
	case PlanPro:
		return PlanPro
	case PlanEnterprise:
		return PlanEnterprise
	default:
		return PlanEssential
	}
}

// Entitlement represents a feature entitlement for a tenant.
type Entitlement struct {
	OrganizationID string     `json:"organization_id"`
	FeatureKey     string     `json:"feature_key"`
	Plan           Plan       `json:"plan"`
	Limit          *int64     `json:"limit,omitempty"` // nil = unlimited
	Enabled        bool       `json:"enabled"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
}

// active reports whether the entitlement grants access at the given time.
func (e Entitlement) active(now time.Time) bool {
	if !e.Enabled {
		return false
	}
	if e.ExpiresAt != nil && !now.Before(*e.ExpiresAt) {
		return false
	}
	return true
}

// LimitExceededError is returned when a tenant exceeds a licensed limit.
type LimitExceededError struct {
	FeatureKey string
	Limit      int64
	Current    int64
}

func (e *LimitExceededError) Error() string {
	return fmt.Sprintf("entitlement limit for %q exceeded: %d of %d in use", e.FeatureKey, e.Current, e.Limit)
}

// LimitExceeded marks the error as an entitlement limit violation so callers can
// map it to an HTTP 403 without importing this package.
func (e *LimitExceededError) LimitExceeded() bool { return true }

// FeatureNotEntitledError is returned when a feature is not part of the plan.
type FeatureNotEntitledError struct {
	FeatureKey string
}

func (e *FeatureNotEntitledError) Error() string {
	return fmt.Sprintf("feature %q is not included in the current plan", e.FeatureKey)
}

// LimitExceeded marks the error as an entitlement violation so callers can map
// it to an HTTP 403 without importing this package.
func (e *FeatureNotEntitledError) LimitExceeded() bool { return true }

// Options configures the entitlement service.
type Options struct {
	// DefaultPlan applies to organizations without stored entitlement rows.
	DefaultPlan Plan
	// CacheTTL controls how long repository lookups are cached per tenant.
	CacheTTL time.Duration
	// Enforce toggles enforcement. When false, every check succeeds; the
	// service still reports the configured entitlements.
	Enforce bool
}

type cacheEntry struct {
	entitlements []Entitlement
	fetchedAt    time.Time
}

// Service provides entitlement checks backed by a repository with a short-lived
// per-tenant cache.
type Service struct {
	repo  Repository
	opts  Options
	now   func() time.Time
	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// NewService creates an entitlement service backed by the given repository.
// A nil repository falls back to an in-memory store.
func NewService(repo Repository, opts Options) *Service {
	if repo == nil {
		repo = NewMemoryRepository()
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 30 * time.Second
	}
	opts.DefaultPlan = normalizePlan(opts.DefaultPlan)

	return &Service{
		repo:  repo,
		opts:  opts,
		now:   func() time.Time { return time.Now().UTC() },
		cache: make(map[string]cacheEntry),
	}
}

// List returns the effective entitlements for the organization, including the
// implicit ones derived from the default plan.
func (s *Service) List(ctx context.Context, orgID string) ([]Entitlement, error) {
	stored, err := s.load(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return s.effective(orgID, stored), nil
}

// Check returns the effective entitlement for a feature and whether it grants
// access.
func (s *Service) Check(ctx context.Context, orgID, featureKey string) (Entitlement, bool, error) {
	stored, err := s.load(ctx, orgID)
	if err != nil {
		return Entitlement{}, false, err
	}

	now := s.now()
	for _, ent := range stored {
		if ent.FeatureKey != featureKey {
			continue
		}
		if !s.opts.Enforce {
			return ent, true, nil
		}
		return ent, ent.active(now), nil
	}

	implied := Entitlement{
		OrganizationID: orgID,
		FeatureKey:     featureKey,
		Plan:           s.opts.DefaultPlan,
		Enabled:        planIncludes(s.opts.DefaultPlan, featureKey),
	}
	if !s.opts.Enforce {
		implied.Enabled = true
		return implied, true, nil
	}
	return implied, implied.Enabled, nil
}

// IsEnabled reports whether a feature is available for the organization.
// Repository failures are treated as "not entitled" so a broken lookup can
// never silently unlock paid modules.
func (s *Service) IsEnabled(ctx context.Context, orgID, featureKey string) bool {
	_, ok, err := s.Check(ctx, orgID, featureKey)
	if err != nil {
		return false
	}
	return ok
}

// AllowCreate enforces the numeric limit of a feature before another resource is
// created. current is the number of resources already stored.
func (s *Service) AllowCreate(ctx context.Context, orgID, featureKey string, current int64) error {
	ent, ok, err := s.Check(ctx, orgID, featureKey)
	if err != nil {
		return err
	}
	if !s.opts.Enforce {
		return nil
	}
	if !ok {
		return &FeatureNotEntitledError{FeatureKey: featureKey}
	}
	if ent.Limit == nil {
		return nil
	}
	if current >= *ent.Limit {
		return &LimitExceededError{FeatureKey: featureKey, Limit: *ent.Limit, Current: current}
	}
	return nil
}

// Grant stores (or updates) an entitlement for the organization.
func (s *Service) Grant(ctx context.Context, ent Entitlement) (Entitlement, error) {
	if ent.OrganizationID == "" {
		return Entitlement{}, fmt.Errorf("organization is required")
	}
	if strings.TrimSpace(ent.FeatureKey) == "" {
		return Entitlement{}, fmt.Errorf("feature_key is required")
	}
	ent.Plan = normalizePlan(ent.Plan)
	ent.UpdatedAt = s.now()

	stored, err := s.repo.Upsert(ctx, ent)
	if err != nil {
		return Entitlement{}, err
	}

	s.invalidate(ent.OrganizationID)
	return stored, nil
}

func (s *Service) load(ctx context.Context, orgID string) ([]Entitlement, error) {
	now := s.now()

	s.mu.RLock()
	entry, ok := s.cache[orgID]
	s.mu.RUnlock()
	if ok && now.Sub(entry.fetchedAt) < s.opts.CacheTTL {
		return entry.entitlements, nil
	}

	items, err := s.repo.List(ctx, orgID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[orgID] = cacheEntry{entitlements: items, fetchedAt: now}
	s.mu.Unlock()

	return items, nil
}

func (s *Service) invalidate(orgID string) {
	s.mu.Lock()
	delete(s.cache, orgID)
	s.mu.Unlock()
}

// effective merges the stored entitlements with the features implied by the
// default plan so callers see the complete picture.
func (s *Service) effective(orgID string, stored []Entitlement) []Entitlement {
	now := s.now()
	seen := make(map[string]bool, len(stored))
	out := make([]Entitlement, 0, len(stored))

	for _, ent := range stored {
		seen[ent.FeatureKey] = true
		if s.opts.Enforce {
			ent.Enabled = ent.active(now)
		} else {
			ent.Enabled = true
		}
		out = append(out, ent)
	}

	for _, feature := range PlanFeatures(s.opts.DefaultPlan) {
		if seen[feature] {
			continue
		}
		out = append(out, Entitlement{
			OrganizationID: orgID,
			FeatureKey:     feature,
			Plan:           s.opts.DefaultPlan,
			Enabled:        true,
		})
	}

	return out
}

func planIncludes(plan Plan, featureKey string) bool {
	for _, feature := range planFeatures[normalizePlan(plan)] {
		if feature == featureKey {
			return true
		}
	}
	return false
}
