package compliance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
)

// ErrNotFound: the rule is unknown or of another organization; both answer
// 404 alike (TEN-04).
var ErrNotFound = errors.New("compliance rule not found")

type Repository interface {
	ListRules(ctx context.Context, orgID, ciTypeID, category string, activeOnly bool, page api.PaginationParams) ([]Rule, int, error)
	GetRule(ctx context.Context, orgID, id string) (*Rule, error)
	CreateRule(ctx context.Context, rule *Rule) error
	UpdateRule(ctx context.Context, orgID, id string, req *UpdateRuleRequest) (*Rule, error)
	DeleteRule(ctx context.Context, orgID, id string) error
	// UpsertResults writes the results in one statement per call, keyed by
	// (organization, rule, CI); status_changed_at changes only with the
	// status (WP-1126 AK3).
	UpsertResults(ctx context.Context, orgID string, results []Result) error
	// DeleteStaleResults removes the results evaluated before before (the
	// end of a complete run).
	DeleteStaleResults(ctx context.Context, orgID string, before time.Time) error
	// Now is the clock evaluated_at is written with (the database clock):
	// a run reads its start from it, so a result an instance with another
	// clock writes during the run is never older than the start.
	Now(ctx context.Context, orgID string) (time.Time, error)
	ListResults(ctx context.Context, orgID, ciTypeID, status string, page api.PaginationParams) ([]Result, int, error)
	// FindingFacts returns the facts of the open findings of the CIs in one
	// read (per page of CIs, not per CI; CMP-02): open is open or
	// acknowledged, compliance_violation is left out. A CI without open
	// finding has no entry.
	FindingFacts(ctx context.Context, orgID string, ciIDs []string) (map[string]FindingFacts, error)
	// ControlStore: the control catalog and the assignment of rules
	// (WP-1128); the memory profile has no catalog
	// (ErrControlsUnavailable).
	ControlStore
	// ScoreStore: the aggregated score, the weights and the daily
	// snapshots (WP-1127); the memory profile keeps no snapshots
	// (ErrScoreHistoryUnavailable).
	ScoreStore
}

// FindingSource lists the findings of an organization (security.Repository
// of the memory profile).
type FindingSource interface {
	List(ctx context.Context, orgID string, filter *security.FilterParams, page api.PaginationParams) ([]security.Finding, int, error)
}

type MemoryRepository struct {
	mu       sync.RWMutex
	rules    map[string]*Rule
	results  map[string]*Result
	findings FindingSource
	next     int
	settings map[string]*Settings
}

// WithFindings makes FindingFacts read the findings of src (--no-db, the
// memory security repository); without one no CI has findings.
func (r *MemoryRepository) WithFindings(src FindingSource) *MemoryRepository {
	r.findings = src
	return r
}

// FindingFacts aggregates the open findings of the CIs like the query of
// the PG repository.
func (r *MemoryRepository) FindingFacts(ctx context.Context, orgID string, ciIDs []string) (map[string]FindingFacts, error) {
	out := map[string]FindingFacts{}
	if r.findings == nil || len(ciIDs) == 0 {
		return out, nil
	}
	list, _, err := r.findings.List(ctx, orgID, &security.FilterParams{}, api.PaginationParams{Limit: math.MaxInt32})
	if err != nil {
		return nil, err
	}
	for i := range list {
		f := &list[i]
		if !slices.Contains(ciIDs, f.CIID) || (f.Status != "open" && f.Status != "acknowledged") || f.Kind == kindComplianceViolation {
			continue
		}
		facts := out[f.CIID]
		if facts.OpenCount == nil {
			facts.OpenCount = map[string]int{}
		}
		facts.OpenCount[f.Kind]++
		facts.MaxSeverityRank = max(facts.MaxSeverityRank, SeverityRank(f.Severity))
		out[f.CIID] = facts
	}
	return out, nil
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{rules: map[string]*Rule{}, results: map[string]*Result{}}
}

// nextID returns ids of UUID form, as the handler accepts only UUID ids
// (API-03); rules and results share the counter, so ids stay unique.
func (r *MemoryRepository) nextID(_ string) string {
	r.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.next)
}
func (r *MemoryRepository) ListRules(_ context.Context, orgID, ciTypeID, category string, activeOnly bool, page api.PaginationParams) ([]Rule, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Rule{}
	for _, rule := range r.rules {
		if rule.OrganizationID == orgID && (ciTypeID == "" || rule.CITypeID == ciTypeID) && (category == "" || rule.Category == category) && (!activeOnly || rule.Active) {
			out = append(out, *rule)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return pageRules(out, page)
}
func (r *MemoryRepository) GetRule(_ context.Context, orgID, id string) (*Rule, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rule := r.rules[id]
	if rule == nil || rule.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *rule
	return &cp, nil
}
func (r *MemoryRepository) CreateRule(_ context.Context, rule *Rule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule.ID = r.nextID("rule")
	now := time.Now().UTC()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	cp := *rule
	r.rules[rule.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateRule(_ context.Context, orgID, id string, req *UpdateRuleRequest) (*Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule := r.rules[id]
	if rule == nil || rule.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.CITypeID != nil {
		rule.CITypeID = *req.CITypeID
	}
	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Description != nil {
		rule.Description = *req.Description
	}
	if req.Severity != nil {
		rule.Severity = *req.Severity
	}
	if req.Category != nil {
		rule.Category = *req.Category
	}
	if req.Expression != nil {
		rule.Expression = req.Expression
	}
	if req.setAppliesWhen {
		rule.AppliesWhen = req.appliesWhen
	}
	if req.RemediationHint != nil {
		rule.RemediationHint = *req.RemediationHint
	}
	if req.Active != nil {
		rule.Active = *req.Active
	}
	rule.UpdatedAt = time.Now().UTC()
	cp := *rule
	return &cp, nil
}
func (r *MemoryRepository) DeleteRule(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule := r.rules[id]
	if rule == nil || rule.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.rules, id)
	return nil
}
func (r *MemoryRepository) UpsertResults(_ context.Context, orgID string, results []Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	byKey := map[string]*Result{}
	for _, res := range r.results {
		if res.OrganizationID == orgID {
			byKey[res.RuleID+"/"+res.CIID] = res
		}
	}
	now := time.Now().UTC()
	for i := range results {
		res := results[i]
		if res.EvaluatedAt.IsZero() {
			res.EvaluatedAt = now
		}
		if old := byKey[res.RuleID+"/"+res.CIID]; old != nil {
			if old.Status != res.Status {
				old.StatusChangedAt = res.EvaluatedAt
			}
			old.CITypeID, old.Status, old.Details, old.EvaluatedAt, old.UpdatedAt = res.CITypeID, res.Status, res.Details, res.EvaluatedAt, now
			results[i] = *old
			continue
		}
		res.ID = r.nextID("result")
		res.CreatedAt, res.UpdatedAt, res.StatusChangedAt = now, now, res.EvaluatedAt
		cp := res
		r.results[res.ID] = &cp
		byKey[res.RuleID+"/"+res.CIID] = &cp
		results[i] = res
	}
	return nil
}

func (r *MemoryRepository) DeleteStaleResults(_ context.Context, orgID string, before time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, res := range r.results {
		if res.OrganizationID == orgID && res.EvaluatedAt.Before(before) {
			delete(r.results, id)
		}
	}
	return nil
}

// Now implements Repository: the process clock.
func (r *MemoryRepository) Now(context.Context, string) (time.Time, error) {
	return time.Now().UTC(), nil
}

func (r *MemoryRepository) ListResults(_ context.Context, orgID, ciTypeID, status string, page api.PaginationParams) ([]Result, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Result{}
	for _, res := range r.results {
		if res.OrganizationID == orgID && (ciTypeID == "" || res.CITypeID == ciTypeID) && (status == "" || res.Status == status) {
			out = append(out, *res)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EvaluatedAt.After(out[j].EvaluatedAt) })
	return pageResults(out, page)
}
func pageRules(items []Rule, page api.PaginationParams) ([]Rule, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
func pageResults(items []Result, page api.PaginationParams) ([]Result, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}

// ListControls implements ControlStore: the memory profile has no catalog.
func (r *MemoryRepository) ListControls(context.Context, string, string, bool) ([]Control, error) {
	return nil, ErrControlsUnavailable
}

// RuleControlIDs implements ControlStore.
func (r *MemoryRepository) RuleControlIDs(context.Context, string, string) ([]string, error) {
	return []string{}, nil
}

// ReplaceRuleControls implements ControlStore.
func (r *MemoryRepository) ReplaceRuleControls(context.Context, string, string, []string) error {
	return ErrControlsUnavailable
}

// Score implements ScoreStore in Go over the stored results (the memory
// profile has no client, site or CI names: their keys are empty or the
// CI id).
func (r *MemoryRepository) Score(_ context.Context, orgID string, q *ScoreQuery) (*ScorePage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	weights := r.weightsOf(orgID)
	rules := map[string]*Rule{}
	for id, rule := range r.rules {
		if rule.OrganizationID == orgID {
			rules[id] = rule
		}
	}
	var results []Result
	for _, res := range r.results {
		if res.OrganizationID == orgID && (q.CITypeID == "" || res.CITypeID == q.CITypeID) {
			results = append(results, *res)
		}
	}
	key := func(res *Result, rule *Rule) (string, string) {
		switch q.GroupBy {
		case GroupCI:
			return res.CIID, res.CIID
		case GroupCIType:
			return res.CITypeID, res.CITypeID
		case GroupCategory:
			return rule.Category, rule.Category
		case GroupSeverity:
			return rule.Severity, rule.Severity
		}
		return "", ""
	}
	groups := tallyGroups(results, rules, weights, key)
	if q.GroupBy == "" && len(groups) == 0 {
		groups = []ScoreGroup{{}}
	}
	page := &ScorePage{Groups: groups}
	if q.GroupBy == GroupCI {
		if q.After != nil {
			start := len(groups)
			for i := range groups {
				if groups[i].Score > q.After.Score || (groups[i].Score == q.After.Score && groups[i].Key > q.After.Key) {
					start = i
					break
				}
			}
			groups = groups[start:]
		}
		if q.Limit > 0 && len(groups) > q.Limit {
			last := groups[q.Limit-1]
			groups, page.Next = groups[:q.Limit], &ScoreCursor{Score: last.Score, Key: last.Key}
		}
		page.Groups = groups
	}
	return page, nil
}

// weightsOf are the weights of the organization (the caller holds the lock).
func (r *MemoryRepository) weightsOf(orgID string) Weights {
	if s := r.settings[orgID]; s != nil {
		return s.Weights
	}
	return DefaultWeights()
}

// GetSettings implements ScoreStore.
func (r *MemoryRepository) GetSettings(_ context.Context, orgID string) (*Settings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s := r.settings[orgID]; s != nil {
		out := *s
		return &out, nil
	}
	return &Settings{Weights: DefaultWeights()}, nil
}

// PutSettings implements ScoreStore.
func (r *MemoryRepository) PutSettings(_ context.Context, orgID string, weights Weights) (*Settings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings == nil {
		r.settings = map[string]*Settings{}
	}
	now := time.Now().UTC()
	r.settings[orgID] = &Settings{Weights: weights, UpdatedAt: &now}
	out := *r.settings[orgID]
	return &out, nil
}

// SnapshotScores implements ScoreStore: the memory profile keeps none.
func (r *MemoryRepository) SnapshotScores(context.Context, string, time.Time) (int, error) {
	return 0, ErrScoreHistoryUnavailable
}

// ScoreHistory implements ScoreStore: the memory profile keeps none.
func (r *MemoryRepository) ScoreHistory(context.Context, string, *HistoryQuery) ([]HistoryPoint, error) {
	return nil, ErrScoreHistoryUnavailable
}
