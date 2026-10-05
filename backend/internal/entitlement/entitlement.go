// Package entitlement provides the entitlement/licensing service.
// It controls which modules and feature limits are available per tenant.
//
// Entitlements are stored per organization in the `entitlement` table
// (ENT-01): one row per feature with enabled, limits (named quotas),
// valid_until and source. An organization is entitled exactly to its rows
// (ENT-05): a missing row means not entitled, only cmdb_core is always
// active. Every organization is provisioned with the rows of its plan once.
package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// Status of a feature for an organization.
type Status string

const (
	// StatusActive grants the feature.
	StatusActive Status = "active"
	// StatusExpired: the discovery license passed valid_until (CH21);
	// discovery and ingest stop, everything else stays available.
	StatusExpired Status = "expired"
	// StatusNotEntitled: no row, or the row is disabled.
	StatusNotEntitled Status = "not_entitled"
)

// expiryStops reports whether valid_until stops the feature. After expiry
// only discovery and ingest stop (CH21); reading, editing, export and every
// other feature stay available.
func expiryStops(featureKey string) bool { return featureKey == FeatureDiscovery }

// status is the entitlement's status at the given time. cmdb_core is
// always active (ENT-02).
func (e *Entitlement) status(now time.Time) Status {
	if e.FeatureKey == FeatureCMDBCore {
		return StatusActive
	}
	if !e.Enabled {
		return StatusNotEntitled
	}
	if e.ValidUntil != nil && !now.Before(*e.ValidUntil) && expiryStops(e.FeatureKey) {
		return StatusExpired
	}
	return StatusActive
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

// ProblemType is the RFC 7807 type of the error (ENT-03).
func (e *LimitExceededError) ProblemType() string { return api.ProblemEntitlementLimit }

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

// ProblemType is the RFC 7807 type of the error.
func (e *FeatureNotEntitledError) ProblemType() string { return api.ProblemFeatureNotEntitled }

// LicenseExpiredError is returned when discovery or ingest is used after the
// discovery license expired (CH21).
type LicenseExpiredError struct {
	ValidUntil time.Time
}

func (e *LicenseExpiredError) Error() string {
	return fmt.Sprintf("the discovery license expired at %s; discovery and ingest are stopped",
		e.ValidUntil.UTC().Format(time.RFC3339))
}

// ProblemType is the RFC 7807 type of the error (CH21).
func (e *LicenseExpiredError) ProblemType() string { return api.ProblemLicenseExpired }

// Options configures the entitlement service.
type Options struct {
	// Cache holds the entitlements of each organization (Redis in
	// production, so a change invalidates every instance; ENT-03). Nil
	// uses an in-memory store.
	Cache cache.Store
	// CacheTTL controls how long the entitlements of an organization are
	// cached (default 60 s, ENT-03).
	CacheTTL time.Duration
	// Enforce toggles enforcement. When false, every check succeeds; the
	// service still reports the configured entitlements.
	Enforce bool
}

// DefaultCacheTTL is the cache lifetime of ENT-03.
const DefaultCacheTTL = 60 * time.Second

func cacheKey(orgID string) string { return "entitlements:" + orgID }

// Service provides entitlement checks backed by a repository and a shared
// per-organization cache. An organization is entitled exactly to its stored
// rows (ENT-05): a missing row means not entitled, only cmdb_core is always
// active.
type Service struct {
	repo    Repository
	opts    Options
	cache   cache.Store
	now     func() time.Time
	adopter UnlicensedAdopter
}

// NewService creates an entitlement service backed by the given repository.
// A nil repository falls back to an in-memory store.
func NewService(repo Repository, opts Options) *Service {
	if repo == nil {
		repo = NewMemoryRepository()
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = DefaultCacheTTL
	}
	store := opts.Cache
	if store == nil {
		store = cache.NewMemoryStore()
	}
	return &Service{
		repo:  repo,
		opts:  opts,
		cache: store,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// List returns the stored entitlements of the organization with their
// effective state, plus cmdb_core, which is always active.
func (s *Service) List(ctx context.Context, orgID string) ([]Entitlement, error) {
	stored, err := s.load(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return s.effective(orgID, stored), nil
}

// Status returns the stored entitlement of a feature and its status. A
// feature without a row is not entitled (no plan fallback, ENT-05); with
// enforcement off every feature is active.
func (s *Service) Status(ctx context.Context, orgID, featureKey string) (Entitlement, Status, error) {
	stored, err := s.load(ctx, orgID)
	if err != nil {
		return Entitlement{}, StatusNotEntitled, err
	}
	ent := Entitlement{OrganizationID: orgID, FeatureKey: featureKey, Limits: map[string]int64{}, Source: "manual"}
	for i := range stored {
		if stored[i].FeatureKey == featureKey {
			ent = stored[i]
			break
		}
	}
	status := ent.status(s.now())
	if !s.opts.Enforce {
		status = StatusActive
	}
	ent.Enabled = status == StatusActive
	return ent, status, nil
}

// Check returns the effective entitlement for a feature and whether it grants
// access.
func (s *Service) Check(ctx context.Context, orgID, featureKey string) (Entitlement, bool, error) {
	ent, status, err := s.Status(ctx, orgID, featureKey)
	if err != nil {
		return Entitlement{}, false, err
	}
	return ent, status == StatusActive, nil
}

// Provision stores the features and default quotas of the plan for an
// organization that has no entitlement rows yet. Since a missing row means
// not entitled, every organization is provisioned once (at startup and on
// creation). It reports whether rows were written.
func (s *Service) Provision(ctx context.Context, orgID string, plan Plan) (bool, error) {
	stored, err := s.repo.List(ctx, orgID)
	if err != nil {
		return false, err
	}
	if len(stored) > 0 {
		return false, nil
	}
	plan = normalizePlan(plan)
	for _, feature := range PlanFeatures(plan) {
		ent := Entitlement{OrganizationID: orgID, FeatureKey: feature, Plan: plan, Enabled: true,
			Limits: map[string]int64{}, Source: "manual"}
		for quota, owner := range QuotaFeature {
			if v, ok := planQuotas[plan][quota]; ok && owner == feature {
				ent.Limits[quota] = v
			}
		}
		if _, err = s.Grant(ctx, ent); err != nil {
			return false, fmt.Errorf("provision %s for %s: %w", feature, orgID, err)
		}
	}
	return true, nil
}

// ProvisionOrganizations provisions every listed organization (id to plan)
// that has no entitlement rows yet, each in its own tenant context, and
// returns how many were provisioned.
func (s *Service) ProvisionOrganizations(ctx context.Context, plans map[string]Plan) (int, error) {
	n := 0
	for orgID, plan := range plans {
		scope := database.OrgWideScope(orgID, "")
		done, err := s.Provision(database.ContextWithTenantScope(ctx, &scope), orgID, plan)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
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

	s.invalidate(ctx, ent.OrganizationID)
	s.adoptAfterGrant(ctx, &stored)
	return stored, nil
}

func (s *Service) load(ctx context.Context, orgID string) ([]Entitlement, error) {
	key := cacheKey(orgID)
	if raw, ok, err := s.cache.Get(ctx, key); err != nil {
		slog.Warn("entitlement cache read failed", "error", err, "organization_id", orgID)
	} else if ok {
		var items []Entitlement
		if err = json.Unmarshal([]byte(raw), &items); err == nil {
			return items, nil
		}
	}

	items, err := s.repo.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if raw, err := json.Marshal(items); err == nil {
		if err = s.cache.Set(ctx, key, string(raw), s.opts.CacheTTL); err != nil {
			slog.Warn("entitlement cache write failed", "error", err, "organization_id", orgID)
		}
	}
	return items, nil
}

// invalidate drops the cached entitlements of the organization; with Redis
// the change reaches every instance.
func (s *Service) invalidate(ctx context.Context, orgID string) {
	if err := s.cache.Delete(ctx, cacheKey(orgID)); err != nil {
		slog.Error("entitlement cache invalidation failed", "error", err, "organization_id", orgID)
	}
}

// effective returns the stored entitlements with their effective state plus
// cmdb_core, which is always active.
func (s *Service) effective(orgID string, stored []Entitlement) []Entitlement {
	now := s.now()
	out := make([]Entitlement, 0, len(stored)+1)
	core := false
	for i := range stored {
		ent := stored[i]
		core = core || ent.FeatureKey == FeatureCMDBCore
		ent.Enabled = !s.opts.Enforce || ent.status(now) == StatusActive
		out = append(out, ent)
	}
	if !core {
		out = append(out, Entitlement{OrganizationID: orgID, FeatureKey: FeatureCMDBCore, Enabled: true,
			Limits: map[string]int64{}, Source: "manual"})
	}
	return out
}
