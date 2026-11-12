package compliance

import (
	"context"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

type Evaluator struct {
	repo Repository
	cis  ci.Repository
}

func NewEvaluator(repo Repository, cis ci.Repository) *Evaluator {
	return &Evaluator{repo: repo, cis: cis}
}

// Evaluate checks every active rule against the organization's CIs and
// replaces the stored results. The results cover the whole organization, so
// only an org-wide principal may run it (E-09); a caller restricted in any
// scope dimension gets ErrOrgWideScopeRequired before anything is read or
// written (E-08: no implicit org-wide access). System callers without a
// request scope evaluate the whole organization. Listing results stays
// scoped to the caller.
func (e *Evaluator) Evaluate(ctx context.Context, orgID string) (EvaluationResponse, error) {
	if scope, ok := database.TenantScopeFromContext(ctx); ok {
		if scope.OrgID != orgID {
			return EvaluationResponse{}, database.ErrTenantMismatch
		}
		if !scope.Clients.IsAll() || !scope.Sites.IsAll() || !scope.Teams.IsAll() {
			return EvaluationResponse{}, ErrOrgWideScopeRequired
		}
	}
	ruleList, _, err := e.repo.ListRules(ctx, orgID, "", "", true, api.PaginationParams{Limit: 100})
	if err != nil {
		return EvaluationResponse{}, err
	}
	results := []Result{}
	now := time.Now().UTC()
	// One page of CIs at a time; the facts of their open findings are read
	// once per page (FindingFacts, CMP-02), never per CI.
	for offset := 0; ; {
		items, total, err := e.cis.List(ctx, orgID, &ci.FilterParams{}, api.PaginationParams{Limit: evaluationPage, Offset: offset})
		if err != nil {
			return EvaluationResponse{}, err
		}
		ids := make([]string, len(items))
		for j := range items {
			ids[j] = items[j].ID
		}
		facts, err := e.repo.FindingFacts(ctx, orgID, ids)
		if err != nil {
			return EvaluationResponse{}, err
		}
		for j := range items {
			item := &items[j]
			inputs := Inputs(item, facts[item.ID])
			for i := range ruleList {
				status, details := evaluateRule(&ruleList[i], item, inputs)
				results = append(results, Result{OrganizationID: orgID, RuleID: ruleList[i].ID, CIID: item.ID, CITypeID: item.CITypeID, Status: status, Details: details, EvaluatedAt: now})
			}
		}
		offset += len(items)
		if len(items) == 0 || offset >= total {
			break
		}
	}
	if err := e.repo.ReplaceResults(ctx, orgID, results); err != nil {
		return EvaluationResponse{}, err
	}
	return summarize(results), nil
}
func summarize(results []Result) EvaluationResponse {
	by := map[string]*Score{}
	overall := Score{}
	for i := range results {
		r := &results[i]
		score := by[r.CITypeID]
		if score == nil {
			score = &Score{CITypeID: r.CITypeID}
			by[r.CITypeID] = score
		}
		add(score, r.Status)
		add(&overall, r.Status)
	}
	out := []Score{}
	for _, s := range by {
		finish(s)
		out = append(out, *s)
	}
	finish(&overall)
	// results is a required array of the contract: [] without results (F-401).
	return EvaluationResponse{Overall: overall, ByCIType: out, Results: api.NonNilSlice(results)}
}
func add(s *Score, status string) {
	switch status {
	case "pass":
		s.Passed++
	case "fail":
		s.Failed++
	default:
		s.NotApplicable++
	}
}
func finish(s *Score) {
	den := s.Passed + s.Failed
	if den > 0 {
		s.Score = float64(s.Passed) / float64(den) * 100
	}
}

// evaluationPage is the number of CIs evaluated per finding read.
const evaluationPage = 100

// evaluateRule is the result of one rule for one CI: not_applicable when
// the CI type does not match or applies_when is false, otherwise pass or
// fail by expression (rules.EvaluateCondition over Inputs).
func evaluateRule(rule *Rule, item *ci.Item, inputs map[string]any) (status, details string) {
	if rule.CITypeID != "" && rule.CITypeID != item.CITypeID {
		return "not_applicable", "CI type does not match rule"
	}
	if rule.AppliesWhen != nil {
		applies, ok := holds(rule.AppliesWhen, inputs)
		if !ok {
			return "not_applicable", "rule condition is no valid MET-45 condition"
		}
		if !applies {
			return "not_applicable", "applies_when does not hold"
		}
	}
	pass, ok := holds(rule.Expression, inputs)
	switch {
	case !ok:
		return "not_applicable", "rule expression is no valid MET-45 condition"
	case pass:
		return "pass", "rule matched"
	case rule.RemediationHint != "":
		return "fail", rule.RemediationHint
	}
	return "fail", "expected state was not met"
}
