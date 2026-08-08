package compliance

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

type Evaluator struct {
	repo Repository
	cis  ci.Repository
}

func NewEvaluator(repo Repository, cis ci.Repository) *Evaluator {
	return &Evaluator{repo: repo, cis: cis}
}
func (e *Evaluator) Evaluate(ctx context.Context, orgID string) (EvaluationResponse, error) {
	rules, _, err := e.repo.ListRules(ctx, orgID, "", "", true, api.PaginationParams{Limit: 100})
	if err != nil {
		return EvaluationResponse{}, err
	}
	items, _, err := e.cis.List(ctx, orgID, ci.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil {
		return EvaluationResponse{}, err
	}
	results := []Result{}
	now := time.Now().UTC()
	for _, rule := range rules {
		for _, item := range items {
			status := "not_applicable"
			details := "CI type does not match rule"
			if rule.CITypeID == "" || rule.CITypeID == item.CITypeID {
				if eval(rule.Expression, item) {
					status = "pass"
					details = "rule matched"
				} else {
					status = "fail"
					details = rule.RemediationHint
					if details == "" {
						details = "expected state was not met"
					}
				}
			}
			results = append(results, Result{OrganizationID: orgID, RuleID: rule.ID, CIID: item.ID, CITypeID: item.CITypeID, Status: status, Details: details, EvaluatedAt: now})
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
	for _, r := range results {
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
	return EvaluationResponse{Overall: overall, ByCIType: out, Results: results}
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
func eval(expr JSONMap, item ci.Item) bool {
	field := str(expr, "field", "")
	op := str(expr, "op", "eq")
	want := expr["value"]
	got := lookup(item, field)
	switch op {
	case "eq":
		return fmt.Sprint(got) == fmt.Sprint(want)
	case "ne":
		return fmt.Sprint(got) != fmt.Sprint(want)
	case "exists":
		return got != nil
	case "not_empty":
		return fmt.Sprint(got) != ""
	case "contains":
		return strings.Contains(fmt.Sprint(got), fmt.Sprint(want))
	case "matches":
		re, err := regexp.Compile(fmt.Sprint(want))
		return err == nil && re.MatchString(fmt.Sprint(got))
	default:
		return false
	}
}
func lookup(item ci.Item, field string) any {
	switch field {
	case "name":
		return item.Name
	case "status":
		return item.Status
	case "manufacturer":
		return item.Manufacturer
	case "model":
		return item.Model
	case "serial_number":
		return item.SerialNumber
	case "management_ip":
		return item.ManagementIP
	case "firmware_version":
		return item.FirmwareVersion
	case "ci_type_id":
		return item.CITypeID
	}
	if strings.HasPrefix(field, "attributes.") {
		return item.Attributes[strings.TrimPrefix(field, "attributes.")]
	}
	return nil
}
func str(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
