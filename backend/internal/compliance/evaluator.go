package compliance

import (
	"context"
	"errors"
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

// Evaluate runs the full evaluation (Run) and answers the results it
// wrote with their scores. The results cover the whole organization, so
// only an org-wide principal may run it (E-09); a caller restricted in any
// scope dimension gets ErrOrgWideScopeRequired before anything is read or
// written (E-08: no implicit org-wide access). System callers without a
// request scope evaluate the whole organization. Listing results stays
// scoped to the caller. The job compliance.evaluate calls Run without
// collecting the results (WP-1126).
func (e *Evaluator) Evaluate(ctx context.Context, orgID string) (EvaluationResponse, error) {
	results := []Result{}
	_, err := e.run(ctx, orgID, func(page []Result) { results = append(results, page...) }, nil)
	if err != nil {
		return EvaluationResponse{}, err
	}
	return summarize(results), nil
}

// requireOrgWide refuses a caller restricted in any scope dimension (E-09).
func requireOrgWide(ctx context.Context, orgID string) error {
	if scope, ok := database.TenantScopeFromContext(ctx); ok {
		if scope.OrgID != orgID {
			return database.ErrTenantMismatch
		}
		if !scope.Clients.IsAll() || !scope.Sites.IsAll() || !scope.Teams.IsAll() {
			return ErrOrgWideScopeRequired
		}
	}
	return nil
}

// Progress is the state of a full evaluation after a page of CIs.
type Progress struct {
	Processed int `json:"processed"`
	Total     int `json:"total"`
	Results   int `json:"results"`
}

// Run evaluates every active rule against every CI of the organization in
// pages of evaluationPage CIs (WP-1126 AK2/AK3): the results of a page are
// written in one upsert, status_changed_at changes only with the status;
// after a complete run the results evaluated before its start are removed
// (deactivated rules, assignments no longer applicable). progress, when
// set, is called after every page; an error from it (a cancelled job)
// ends the run without the removal.
func (e *Evaluator) Run(ctx context.Context, orgID string, progress func(Progress) error) (Progress, error) {
	return e.run(ctx, orgID, nil, progress)
}

func (e *Evaluator) run(ctx context.Context, orgID string, collect func([]Result), progress func(Progress) error) (Progress, error) {
	if err := requireOrgWide(ctx, orgID); err != nil {
		return Progress{}, err
	}
	ruleList, err := e.activeRules(ctx, orgID)
	if err != nil {
		return Progress{}, err
	}
	started, err := e.repo.Now(ctx, orgID)
	if err != nil {
		return Progress{}, err
	}
	// Keyset pages in a fixed order (created_at, id ascending): a CI
	// deleted or changed during the run moves no other CI past the cursor,
	// so no CI is skipped and loses its results to the removal at the end;
	// a CI created during the run comes last or is evaluated incrementally.
	filter := &ci.FilterParams{SortBy: "created_at", SortDir: "asc"}
	state := Progress{}
	page := api.PaginationParams{Limit: evaluationPage}
	for {
		items, total, err := e.cis.List(ctx, orgID, filter, page)
		if err != nil {
			return state, err
		}
		results, err := e.evaluatePage(ctx, orgID, ruleList, items, time.Now().UTC())
		if err != nil {
			return state, err
		}
		if err := e.repo.UpsertResults(ctx, orgID, results); err != nil {
			return state, err
		}
		if collect != nil {
			collect(results)
		}
		if state.Total == 0 {
			state.Total = total
		}
		state.Processed += len(items)
		state.Results += len(results)
		state.Total = max(state.Total, state.Processed)
		if progress != nil {
			if perr := progress(state); perr != nil {
				return state, perr
			}
		}
		next := ci.NextCursor(items, filter, evaluationPage)
		if next == "" || state.Processed >= total {
			break
		}
		cursor, cerr := api.DecodeCursor(next)
		if cerr != nil {
			return state, cerr
		}
		page = api.PaginationParams{Limit: evaluationPage, Cursor: &cursor}
	}
	if err := e.repo.DeleteStaleResults(ctx, orgID, started); err != nil {
		return state, err
	}
	return state, nil
}

// activeRules lists every active rule of the organization.
func (e *Evaluator) activeRules(ctx context.Context, orgID string) ([]Rule, error) {
	rules, _, err := e.repo.ListRules(ctx, orgID, "", "", true, api.PaginationParams{Limit: maxRules})
	return rules, err
}

// maxRules bounds the active rules one evaluation reads.
const maxRules = 100000

// evaluatePage evaluates the rules against a page of CIs; the facts of
// their open findings are read once per page (FindingFacts, CMP-02), never
// per CI.
func (e *Evaluator) evaluatePage(ctx context.Context, orgID string, ruleList []Rule, items []ci.Item, now time.Time) ([]Result, error) {
	ids := make([]string, len(items))
	for j := range items {
		ids[j] = items[j].ID
	}
	facts, err := e.repo.FindingFacts(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(items)*len(ruleList))
	for j := range items {
		item := &items[j]
		inputs := Inputs(item, facts[item.ID])
		for i := range ruleList {
			status, details := evaluateRule(&ruleList[i], item, inputs)
			results = append(results, Result{OrganizationID: orgID, RuleID: ruleList[i].ID, CIID: item.ID, CITypeID: item.CITypeID, Status: status, Details: details, EvaluatedAt: now})
		}
	}
	return results, nil
}

// EvaluateCI evaluates every active rule against one CI and upserts its
// results (the incremental evaluation, WP-1126 AK4); a CI that is not
// visible (deleted) is no error.
func (e *Evaluator) EvaluateCI(ctx context.Context, orgID, ciID string) error {
	item, err := e.cis.GetByID(ctx, orgID, ciID)
	if errors.Is(err, ci.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ruleList, err := e.activeRules(ctx, orgID)
	if err != nil || len(ruleList) == 0 {
		return err
	}
	page, err := e.evaluatePage(ctx, orgID, ruleList, []ci.Item{*item}, time.Now().UTC())
	if err != nil {
		return err
	}
	return e.repo.UpsertResults(ctx, orgID, page)
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

// evaluationPage is the number of CIs evaluated per page: one finding
// read and one upsert each (WP-1126 AK2).
const evaluationPage = 500

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
