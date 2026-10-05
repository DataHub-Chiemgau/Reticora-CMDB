// Package entitlement provides the entitlement/licensing service.
// It controls which modules and feature limits are available per tenant.
//
// Entitlements are stored per organization in the `entitlement` table
// (ENT-01): one row per feature with enabled, limits (named quotas),
// valid_until and source. Tenants without stored rows fall back to the
// configured default plan, so a fresh organization is always usable while
// still being restricted to the feature set and quotas of its plan.
package entitlement

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// Feature keys of phase 1 (ENT-02). cmdb_core is always active.
const (
	FeatureCMDBCore           = "cmdb_core"
	FeatureDiscovery          = "discovery"
	FeatureTopology           = "topology"
	FeatureRackView           = "rack_view"
	FeatureExportCSV          = "export_csv"
	FeatureWebhooks           = "webhooks"
	FeatureAPIAccess          = "api_access"
	FeatureNotificationsEmail = "notifications_email"
)

// Phase1Features are the feature keys of ENT-02; every plan includes them
// (ENT-06).
var Phase1Features = []string{
	FeatureCMDBCore, FeatureDiscovery, FeatureTopology, FeatureRackView,
	FeatureExportCSV, FeatureWebhooks, FeatureAPIAccess, FeatureNotificationsEmail,
}

// Module feature keys of later phases (RBA-05, ENT-06); they gate the modules
// already present in the code.
const (
	FeatureInventory     = "inventory"
	FeatureDocuments     = "documents"
	FeatureStocktake     = "stocktake"
	FeatureTicketing     = "ticketing"
	FeatureMonitoring    = "monitoring"
	FeatureIGA           = "iga"
	FeatureEndpointAgent = "endpoint_agent"
	FeatureWorkflowForms = "workflow_forms"
	FeatureCompliance    = "compliance"
	FeatureAIAssistant   = "ai_assistant"
)

// Quotas of ENT-02, stored in limits.
const (
	LimitMaxCIs        = "max_cis"
	LimitMaxCollectors = "max_collectors"
	LimitMaxUsers      = "max_users"
	LimitMaxAPIKeys    = "max_api_keys"
)

// Quotas are the quota keys of ENT-02.
var Quotas = []string{LimitMaxCIs, LimitMaxCollectors, LimitMaxUsers, LimitMaxAPIKeys}

// QuotaFeature is the feature a quota belongs to: the quota is stored in the
// limits of that feature's row, and a quota of a feature the organization is
// not entitled to allows nothing.
var QuotaFeature = map[string]string{
	LimitMaxCIs:        FeatureCMDBCore,
	LimitMaxCollectors: FeatureDiscovery,
	LimitMaxUsers:      FeatureCMDBCore,
	LimitMaxAPIKeys:    FeatureAPIAccess,
}

// planQuotas are the default quotas per plan (ENT-06, V); a missing quota is
// unlimited.
var planQuotas = map[Plan]map[string]int64{
	PlanEssential:  {LimitMaxCIs: 500, LimitMaxCollectors: 2, LimitMaxUsers: 5},
	PlanStandard:   {LimitMaxCIs: 2500, LimitMaxCollectors: 5, LimitMaxUsers: 25},
	PlanPro:        {LimitMaxCIs: 10000, LimitMaxCollectors: 20, LimitMaxUsers: 100},
	PlanEnterprise: {LimitMaxCIs: 50000},
}

// Sources of an entitlement row (ENT-01).
var Sources = []string{"manual", "selfsignup", "billing", "reseller"}

// planFeatures maps a plan to the features it includes. Every plan includes
// the phase-1 features; higher plans add modules.
var planFeatures = map[Plan][]string{
	PlanEssential: append(slices.Clone(Phase1Features),
		FeatureInventory),
	PlanStandard: append(slices.Clone(Phase1Features),
		FeatureInventory, FeatureDocuments, FeatureStocktake, FeatureTicketing),
	PlanPro: append(slices.Clone(Phase1Features),
		FeatureInventory, FeatureDocuments, FeatureStocktake, FeatureTicketing,
		FeatureMonitoring, FeatureWorkflowForms, FeatureAIAssistant),
	PlanEnterprise: append(slices.Clone(Phase1Features),
		FeatureInventory, FeatureDocuments, FeatureStocktake, FeatureTicketing,
		FeatureMonitoring, FeatureIGA, FeatureEndpointAgent, FeatureWorkflowForms,
		FeatureCompliance, FeatureAIAssistant),
}

// PlanFeatures returns the feature keys included in the given plan.
func PlanFeatures(plan Plan) []string {
	return slices.Clone(planFeatures[normalizePlan(plan)])
}

// PlanQuotas returns the default quotas of the plan.
func PlanQuotas(plan Plan) map[string]int64 {
	out := map[string]int64{}
	for k, v := range planQuotas[normalizePlan(plan)] {
		out[k] = v
	}
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

// Entitlement represents a feature entitlement for a tenant (ENT-01).
type Entitlement struct {
	OrganizationID string `json:"organization_id"`
	FeatureKey     string `json:"feature_key"`
	Plan           Plan   `json:"plan"`
	Enabled        bool   `json:"enabled"`
	// Limits are named quotas (max_cis, ...); a missing quota falls back to
	// the plan default.
	Limits     map[string]int64 `json:"limits"`
	ValidUntil *time.Time       `json:"valid_until,omitempty"`
	// Source is manual, selfsignup, billing or reseller.
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// active reports whether the entitlement grants access at the given time.
// cmdb_core is always active (ENT-02).
func (e *Entitlement) active(now time.Time) bool {
	if e.FeatureKey == FeatureCMDBCore {
		return true
	}
	if !e.Enabled {
		return false
	}
	if e.ValidUntil != nil && !now.Before(*e.ValidUntil) {
		return false
	}
	return true
}

// ErrCoreNotDeactivatable is returned when cmdb_core would be disabled or
// limited in time.
var ErrCoreNotDeactivatable = errors.New("cmdb_core is always active and cannot be disabled or expire")

// ValidationError is an entitlement that cannot be stored as given.
type ValidationError struct{ err error }

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

func invalid(err error) error { return &ValidationError{err: err} }

// Validate checks an entitlement before it is stored.
func (e *Entitlement) Validate() error {
	if strings.TrimSpace(e.FeatureKey) == "" {
		return invalid(errors.New("feature_key is required"))
	}
	if e.FeatureKey == FeatureCMDBCore && (!e.Enabled || e.ValidUntil != nil) {
		return invalid(ErrCoreNotDeactivatable)
	}
	if e.Source == "" {
		e.Source = "manual"
	}
	if !slices.Contains(Sources, e.Source) {
		return invalid(fmt.Errorf("source must be one of %s", strings.Join(Sources, ", ")))
	}
	for k, v := range e.Limits {
		if v < 0 {
			return invalid(fmt.Errorf("limit %s must not be negative", k))
		}
	}
	return nil
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
	for i := range stored {
		ent := stored[i]
		if ent.FeatureKey != featureKey {
			continue
		}
		if !s.opts.Enforce {
			return ent, true, nil
		}
		return ent, ent.active(now), nil
	}

	implied := s.implied(orgID, featureKey)
	if !s.opts.Enforce {
		implied.Enabled = true
		return implied, true, nil
	}
	return implied, implied.Enabled, nil
}

// implied is the entitlement the default plan gives a feature.
func (s *Service) implied(orgID, featureKey string) Entitlement {
	ent := Entitlement{
		OrganizationID: orgID,
		FeatureKey:     featureKey,
		Plan:           s.opts.DefaultPlan,
		Enabled:        featureKey == FeatureCMDBCore || planIncludes(s.opts.DefaultPlan, featureKey),
		Limits:         map[string]int64{},
		Source:         "manual",
	}
	for quota, feature := range QuotaFeature {
		if feature != featureKey {
			continue
		}
		if v, ok := planQuotas[s.opts.DefaultPlan][quota]; ok {
			ent.Limits[quota] = v
		}
	}
	return ent
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

// Quota returns the quota of the organization: the value stored in the
// limits of the quota's feature, else the plan default; nil is unlimited.
func (s *Service) Quota(ctx context.Context, orgID, quota string) (*int64, error) {
	feature, ok := QuotaFeature[quota]
	if !ok {
		return nil, fmt.Errorf("unknown quota %q", quota)
	}
	ent, _, err := s.Check(ctx, orgID, feature)
	if err != nil {
		return nil, err
	}
	if v, ok := ent.Limits[quota]; ok {
		return &v, nil
	}
	if v, ok := planQuotas[normalizePlan(ent.Plan)][quota]; ok {
		return &v, nil
	}
	return nil, nil
}

// AllowCreate enforces a quota (max_cis, max_collectors, max_users,
// max_api_keys) before another resource is created. current is the number
// of resources already stored.
func (s *Service) AllowCreate(ctx context.Context, orgID, quota string, current int64) error {
	feature, ok := QuotaFeature[quota]
	if !ok {
		return fmt.Errorf("unknown quota %q", quota)
	}
	if !s.opts.Enforce {
		return nil
	}
	_, entitled, err := s.Check(ctx, orgID, feature)
	if err != nil {
		return err
	}
	if !entitled {
		return &FeatureNotEntitledError{FeatureKey: feature}
	}
	limit, err := s.Quota(ctx, orgID, quota)
	if err != nil {
		return err
	}
	if limit != nil && current >= *limit {
		return &LimitExceededError{FeatureKey: quota, Limit: *limit, Current: current}
	}
	return nil
}

// Grant stores (or updates) an entitlement for the organization.
func (s *Service) Grant(ctx context.Context, ent Entitlement) (Entitlement, error) {
	if ent.OrganizationID == "" {
		return Entitlement{}, fmt.Errorf("organization is required")
	}
	if err := ent.Validate(); err != nil {
		return Entitlement{}, err
	}
	if ent.Limits == nil {
		ent.Limits = map[string]int64{}
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

	for i := range stored {
		ent := stored[i]
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
		ent := s.implied(orgID, feature)
		ent.Enabled = true
		out = append(out, ent)
	}

	return out
}

func planIncludes(plan Plan, featureKey string) bool {
	return slices.Contains(planFeatures[normalizePlan(plan)], featureKey)
}
