package compliance

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	if err := repo.UpsertResults(ctx, "org-1", results); err != nil {
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

// ─── report generator (SEC-04, WP-1129) ───────────────────────────────────────

// fakeReportReader serves fixed controls, results and findings and pages
// the results by the keyset (rule, CI) like the PostgreSQL reader.
type fakeReportReader struct {
	controlsOf []ReportControl
	rows       []ReportResultRow
	findings   []ReportFindings
	calls      int
}

func (f *fakeReportReader) names(context.Context, string, string) (string, string, error) {
	return "Müller Netze GmbH", "", nil
}

func (f *fakeReportReader) controls(context.Context, string, string) ([]ReportControl, error) {
	return f.controlsOf, nil
}

func (f *fakeReportReader) results(_ context.Context, _, _ string, ruleIDs []string, status, afterRule, afterCI string, limit int) ([]ReportResultRow, error) {
	f.calls++
	sorted := slices.Clone(f.rows)
	slices.SortFunc(sorted, func(a, b ReportResultRow) int {
		if c := strings.Compare(a.RuleID, b.RuleID); c != 0 {
			return c
		}
		return strings.Compare(a.CIID, b.CIID)
	})
	var out []ReportResultRow
	for _, r := range sorted {
		if (status != "" && r.Status != status) || (ruleIDs != nil && !slices.Contains(ruleIDs, r.RuleID)) {
			continue
		}
		if afterRule != "" && (r.RuleID < afterRule || (r.RuleID == afterRule && r.CIID <= afterCI)) {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeReportReader) openFindings(context.Context, string, string) ([]ReportFindings, error) {
	return f.findings, nil
}

func reportGenerator(reader reportReader) *ReportGenerator {
	g := NewReportGenerator(nil, NewMemoryRepository(), stubAuditVerifier{integrity: AuditIntegrity{Intact: true, Checked: 42}},
		stubEntitlements{items: []EntitlementStatus{{FeatureKey: "compliance_findings", Enabled: true}}}, nil)
	g.reader = reader
	g.now = func() time.Time { return time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC) }
	return g
}

// failingRows returns n failing results over rules rules, CIs numbered.
func failingRows(rules, n int) []ReportResultRow {
	at := time.Date(2026, 11, 13, 2, 30, 0, 0, time.UTC)
	out := make([]ReportResultRow, 0, n)
	for i := range n {
		rule := i % rules
		out = append(out, ReportResultRow{RuleID: fmt.Sprintf("00000000-0000-4000-8000-%012d", rule), RuleName: fmt.Sprintf("Rule %03d", rule),
			Severity: "high", Category: "hardening", CIID: fmt.Sprintf("10000000-0000-4000-8000-%012d", i), CIName: fmt.Sprintf("sw-%04d", i),
			Status: "fail", EvaluatedAt: at})
	}
	return out
}

// pdfText returns the strings shown by the content streams of an
// uncompressed PDF (the operands of Tj).
func pdfText(raw []byte) string {
	var b strings.Builder
	for _, m := range regexp.MustCompile(`\(((?:\\.|[^\\)])*)\) ?Tj`).FindAllSubmatch(raw, -1) {
		b.Write(m[1])
		b.WriteByte('\n')
	}
	return b.String()
}

// TestReportPDFReadable: the PDF starts with %PDF-, has more than one page
// with 200 failing rules and shows standard and organization in the head.
func TestReportPDFReadable(t *testing.T) {
	g := reportGenerator(&fakeReportReader{rows: failingRows(200, 400),
		findings: []ReportFindings{{Severity: "critical", Open: 4, KEV: 1}, {Severity: "high", Open: 6}}})
	data, err := g.Collect(context.Background(), "org-1", &ReportParams{Standard: "iso27001", Format: FormatPDF})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Failing) != 200 {
		t.Fatalf("failing rules %d, want 200", len(data.Failing))
	}
	raw, err := renderPDF(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("%PDF-")) {
		t.Fatalf("output starts with %q", raw[:8])
	}
	pages := bytes.Count(raw, []byte("/Type /Page\n")) + bytes.Count(raw, []byte("/Type /Page ")) + bytes.Count(raw, []byte("/Type /Page>"))
	if pages <= 1 {
		t.Errorf("pages %d, want more than one", pages)
	}
	text := pdfText(raw)
	for _, want := range []string{"Compliance report ISO/IEC 27001", "Organization", "Standard", "Contents", "Page 1 of ",
		"4. Failing rules and affected CIs", "Rule 199", "sw-0399"} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF text lacks %q", want)
		}
	}
	// The name with umlauts is translated to cp1252, not dropped.
	if !bytes.Contains(raw, []byte("M\xfcller Netze GmbH")) {
		t.Error("the organization name is not in the head")
	}
	// The aliases of the table of contents are replaced by page numbers.
	if strings.Contains(text, "{toc") || strings.Contains(text, "{nb}") {
		t.Error("an alias of the table of contents or the page count is left")
	}
	compressed, err := Render(data, FormatPDF)
	if err != nil || !bytes.HasPrefix(compressed, []byte("%PDF-")) {
		t.Errorf("compressed PDF: %v", err)
	}
}

// TestReportCSVColumns: the head row and one row per control × rule × CI;
// a control without rule has empty rule and CI columns.
func TestReportCSVColumns(t *testing.T) {
	ruleA, ruleB := "00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b"
	at := time.Date(2026, 11, 13, 2, 30, 0, 0, time.UTC)
	reader := &fakeReportReader{
		controlsOf: []ReportControl{
			{ControlID: "A.5.1", Title: "Policies", Rules: []ReportControlRule{{RuleID: ruleA, RuleName: "ssh", Severity: "high", Active: true},
				{RuleID: ruleB, RuleName: "=cmd()", Severity: "low", Active: true}}},
			{ControlID: "A.8.9", Title: "Configuration management"},
		},
		rows: []ReportResultRow{
			{RuleID: ruleA, RuleName: "ssh", Severity: "high", CIID: "c1", CIName: "sw-1", Status: "fail", EvaluatedAt: at},
			{RuleID: ruleA, RuleName: "ssh", Severity: "high", CIID: "c2", CIName: "sw-2", Status: "pass", EvaluatedAt: at},
		},
	}
	data, err := reportGenerator(reader).Collect(context.Background(), "org-1", &ReportParams{Standard: "nis2", Format: FormatCSV})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Render(data, FormatCSV)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		ReportCSVHeader,
		{"nis2", "A.5.1", "Policies", ruleA, "ssh", "high", "c1", "sw-1", "fail", "2026-11-13T02:30:00Z"},
		{"nis2", "A.5.1", "Policies", ruleA, "ssh", "high", "c2", "sw-2", "pass", "2026-11-13T02:30:00Z"},
		{"nis2", "A.5.1", "Policies", ruleB, "'=cmd()", "low", "", "", "", ""},
		{"nis2", "A.8.9", "Configuration management", "", "", "", "", "", "", ""},
	}
	if !slices.EqualFunc(records, want, slices.Equal) {
		t.Errorf("CSV\n%v\nwant\n%v", records, want)
	}
	if !utf8.Valid(raw) {
		t.Error("CSV is not UTF-8")
	}
}

// TestReportHasNoThousandLimit: 1,500 failing results are all read (keyset
// pages) and all in the report.
func TestReportHasNoThousandLimit(t *testing.T) {
	reader := &fakeReportReader{rows: failingRows(3, 1500)}
	data, err := reportGenerator(reader).Collect(context.Background(), "org-1", &ReportParams{Standard: "kritis", Format: FormatPDF})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, r := range data.Failing {
		total += len(r.CIs)
	}
	if total != 1500 || reader.calls < 3 {
		t.Fatalf("failing CIs %d in %d reads, want 1500 in pages", total, reader.calls)
	}
	raw, err := renderPDF(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if text := pdfText(raw); !strings.Contains(text, "sw-1499") || !strings.Contains(text, "sw-0000") {
		t.Error("the PDF lacks the first or the last failing CI")
	}
}
