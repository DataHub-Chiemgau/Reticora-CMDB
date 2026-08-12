package compliance

import (
	"context"
	"testing"
)

type stubAuditVerifier struct {
	integrity AuditIntegrity
	err       error
}

func (s stubAuditVerifier) Verify(_ context.Context, _ string) (AuditIntegrity, error) {
	return s.integrity, s.err
}

type stubEntitlements struct {
	items []EntitlementStatus
	err   error
}

func (s stubEntitlements) List(_ context.Context, _ string) ([]EntitlementStatus, error) {
	return s.items, s.err
}

func TestReportAggregatesFindingsByRule(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()

	rule := &Rule{
		OrganizationID: "org-1", CITypeID: "type-server", Name: "encryption at rest",
		Severity: "high", Category: "ISO27001", Active: true,
		Expression:      JSONMap{"field": "attributes.encrypted", "op": "eq", "value": true},
		RemediationHint: "Enable encryption",
	}
	if err := repo.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	results := []Result{
		{OrganizationID: "org-1", RuleID: rule.ID, CIID: "ci-1", CITypeID: "type-server", Status: "fail"},
		{OrganizationID: "org-1", RuleID: rule.ID, CIID: "ci-2", CITypeID: "type-server", Status: "fail"},
		{OrganizationID: "org-1", RuleID: rule.ID, CIID: "ci-3", CITypeID: "type-server", Status: "pass"},
	}
	if err := repo.ReplaceResults(ctx, "org-1", results); err != nil {
		t.Fatal(err)
	}

	svc := NewReportService(repo,
		stubAuditVerifier{integrity: AuditIntegrity{Intact: true, Checked: 42}},
		stubEntitlements{items: []EntitlementStatus{{FeatureKey: "compliance", Enabled: true}}},
	)

	report, err := svc.Generate(ctx, "org-1", "nis2")
	if err != nil {
		t.Fatal(err)
	}

	if report.Standard != "nis2" {
		t.Fatalf("expected standard nis2, got %q", report.Standard)
	}
	if !report.AuditIntegrity.Intact || report.AuditIntegrity.Checked != 42 {
		t.Fatalf("unexpected audit integrity: %+v", report.AuditIntegrity)
	}
	if report.Score.Failed != 2 || report.Score.Passed != 1 {
		t.Fatalf("unexpected score: %+v", report.Score)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(report.Findings))
	}
	finding := report.Findings[0]
	if finding.RuleName != "encryption at rest" || finding.Severity != "high" {
		t.Fatalf("unexpected finding: %+v", finding)
	}
	if len(finding.AffectedCIs) != 2 {
		t.Fatalf("expected 2 affected CIs, got %+v", finding.AffectedCIs)
	}
	if finding.RemediationHint != "Enable encryption" {
		t.Fatalf("missing remediation hint: %+v", finding)
	}
	if len(report.Capabilities) != 1 || report.Capabilities[0].FeatureKey != "compliance" {
		t.Fatalf("unexpected capabilities: %+v", report.Capabilities)
	}
}

func TestReportDefaultsToISO27001(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewReportService(repo, nil, nil)

	report, err := svc.Generate(context.Background(), "org-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Standard != "iso27001" {
		t.Fatalf("expected default standard iso27001, got %q", report.Standard)
	}
}

func TestReportOmitsOptionalSectionsWithoutSources(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewReportService(repo, nil, nil)

	report, err := svc.Generate(context.Background(), "org-1", "iso27001")
	if err != nil {
		t.Fatal(err)
	}
	if report.AuditIntegrity.Checked != 0 {
		t.Fatalf("expected no audit section, got %+v", report.AuditIntegrity)
	}
	if report.Capabilities != nil {
		t.Fatalf("expected no capabilities, got %+v", report.Capabilities)
	}
}
