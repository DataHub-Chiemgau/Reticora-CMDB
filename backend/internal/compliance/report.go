package compliance

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// AuditVerifier verifies the integrity of the tenant's hash-chained audit
// log. It is satisfied by a thin adapter around audit.Verify so the
// compliance package never imports the audit package directly (the audit
// handler already depends on the same pool).
type AuditVerifier interface {
	Verify(ctx context.Context, orgID string) (AuditIntegrity, error)
}

// AuditIntegrity is the audit-chain verdict embedded in a security report.
type AuditIntegrity struct {
	// Intact is true when every hash link checks out.
	Intact bool `json:"intact"`
	// Checked is the number of entries walked.
	Checked int `json:"checked"`
	// BrokenReason explains the first broken link, if any.
	BrokenReason string `json:"broken_reason,omitempty"`
}

// EntitlementLister reports which features are enabled for the tenant, so the
// report can document which security-relevant capabilities (audit, IGA,
// monitoring, …) are active (implemented by entitlement.Service).
type EntitlementLister interface {
	List(ctx context.Context, orgID string) ([]EntitlementStatus, error)
}

// EntitlementStatus is a single feature flag as shown in the report.
type EntitlementStatus struct {
	FeatureKey string `json:"feature_key"`
	Enabled    bool   `json:"enabled"`
}

// Finding is a single report line that an ISO 27001 / NIS2 auditor cares
// about: a failing compliance check, grouped per rule with affected CIs.
type Finding struct {
	RuleID          string   `json:"rule_id"`
	RuleName        string   `json:"rule_name"`
	Severity        string   `json:"severity"`
	Category        string   `json:"category"`
	RemediationHint string   `json:"remediation_hint,omitempty"`
	AffectedCIs     []string `json:"affected_cis"`
}

// Report is the tenant-scoped security & compliance posture document that
// supports ISO 27001 (A.5.35/A.8.15/A.10.1) and NIS2 evidence collection:
// cryptographic integrity of the audit trail, rule-based configuration
// compliance and the enabled security capabilities in one snapshot.
type Report struct {
	GeneratedAt    time.Time           `json:"generated_at"`
	Standard       string              `json:"standard"`
	AuditIntegrity AuditIntegrity      `json:"audit_integrity"`
	Score          Score               `json:"compliance_score"`
	BySeverity     map[string]int      `json:"failures_by_severity"`
	Findings       []Finding           `json:"findings"`
	Capabilities   []EntitlementStatus `json:"capabilities"`
}

// ReportService aggregates the inputs of the security report.
type ReportService struct {
	repo         Repository
	audit        AuditVerifier
	entitlements EntitlementLister
}

// NewReportService creates a report service; audit and entitlements may be
// nil, in which case the corresponding report sections are omitted.
func NewReportService(repo Repository, audit AuditVerifier, entitlements EntitlementLister) *ReportService {
	return &ReportService{repo: repo, audit: audit, entitlements: entitlements}
}

// Generate builds the security report for the tenant. standard is a free
// label ("iso27001", "nis2", …) recorded on the document for filtering.
func (s *ReportService) Generate(ctx context.Context, orgID, standard string) (*Report, error) {
	if standard == "" {
		standard = "iso27001"
	}

	results, _, err := s.repo.ListResults(ctx, orgID, "", "fail", api.PaginationParams{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list failing compliance results: %w", err)
	}
	allResults, _, err := s.repo.ListResults(ctx, orgID, "", "", api.PaginationParams{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list compliance results: %w", err)
	}

	rules, _, err := s.repo.ListRules(ctx, orgID, "", "", false, api.PaginationParams{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list compliance rules: %w", err)
	}
	rulesByID := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		rulesByID[rule.ID] = rule
	}

	report := &Report{
		GeneratedAt: time.Now().UTC(),
		Standard:    standard,
		Score:       summarize(allResults).Overall,
		BySeverity:  map[string]int{},
		Findings:    buildFindings(results, rulesByID),
	}

	if s.audit != nil {
		integrity, err := s.audit.Verify(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("verify audit chain: %w", err)
		}
		report.AuditIntegrity = integrity
	}

	if s.entitlements != nil {
		caps, err := s.entitlements.List(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("list entitlements: %w", err)
		}
		report.Capabilities = caps
	}

	return report, nil
}

func buildFindings(failures []Result, rules map[string]Rule) []Finding {
	byRule := map[string]*Finding{}
	order := []string{}
	for _, result := range failures {
		finding, ok := byRule[result.RuleID]
		if !ok {
			rule := rules[result.RuleID]
			finding = &Finding{
				RuleID:          result.RuleID,
				RuleName:        rule.Name,
				Severity:        rule.Severity,
				Category:        rule.Category,
				RemediationHint: rule.RemediationHint,
				AffectedCIs:     []string{},
			}
			byRule[result.RuleID] = finding
			order = append(order, result.RuleID)
		}
		finding.AffectedCIs = append(finding.AffectedCIs, result.CIID)
	}
	out := make([]Finding, 0, len(order))
	for _, id := range order {
		out = append(out, *byRule[id])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) < severityRank(out[j].Severity)
		}
		return out[i].RuleName < out[j].RuleName
	})
	return out
}

// severityRank orders findings most severe first.
func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}
