package compliance

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"
)

// The PDF of a report (SEC-04, WP-1129 AK3): codeberg.org/go-pdf/fpdf
// (the maintained home of go-pdf/fpdf) with the core font Helvetica; texts
// are translated to cp1252, so German names keep their umlauts. Every page
// carries the head (organization, client, standard, generation time) and
// its number of the total; the first page holds the table of contents of
// the sections with their pages, filled in through aliases once the
// sections are written.

// pdfStandardTitles are the names of the standards in the head.
var pdfStandardTitles = map[string]string{"iso27001": "ISO/IEC 27001", "nis2": "NIS2", "kritis": "KRITIS"}

// pdfSections are the sections in their order (AK2).
var pdfSections = []string{
	"Audit chain verification",
	"Compliance score and trend",
	"Control coverage",
	"Failing rules and affected CIs",
	"Open findings",
	"Active capabilities",
}

// StandardTitle returns the display name of a standard.
func StandardTitle(standard string) string {
	if t, ok := pdfStandardTitles[standard]; ok {
		return t
	}
	return strings.ToUpper(standard)
}

type pdfReport struct {
	pdf *fpdf.Fpdf
	tr  func(string) string
}

// renderPDF renders the report; compress false keeps the content streams
// readable (tests).
func renderPDF(d *ReportData, compress bool) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(compress)
	pdf.SetCreationDate(d.GeneratedAt)
	pdf.SetModificationDate(d.GeneratedAt)
	r := &pdfReport{pdf: pdf, tr: pdf.UnicodeTranslatorFromDescriptor("")}
	title := "Compliance report " + StandardTitle(d.Standard)
	pdf.SetTitle(title, true)
	pdf.SetAuthor("Reticora CMDB", true)
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(true, 18)
	pdf.AliasNbPages("{nb}")
	scope := d.OrganizationName
	if d.ClientName != "" {
		scope += " / " + d.ClientName
	}
	pdf.SetHeaderFunc(func() {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(0, 5, r.tr(title+" - "+scope), "", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 8)
		pdf.CellFormat(0, 4, r.tr("Generated "+d.GeneratedAt.UTC().Format(time.RFC3339)), "B", 1, "L", false, 0, "")
		pdf.Ln(3)
	})
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 8)
		pdf.CellFormat(0, 5, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
	})

	// Title page with the head and the table of contents.
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(0, 10, r.tr(title), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	r.pair("Organization", d.OrganizationName)
	client := "whole organization"
	if d.ClientName != "" {
		client = d.ClientName
	}
	r.pair("Client", client)
	r.pair("Standard", StandardTitle(d.Standard))
	r.pair("Generated at", d.GeneratedAt.UTC().Format(time.RFC3339))
	pdf.Ln(4)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.CellFormat(0, 8, "Contents", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	for i, s := range pdfSections {
		pdf.CellFormat(160, 6, r.tr(fmt.Sprintf("%d. %s", i+1, s)), "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6, fmt.Sprintf("{toc%d}", i+1), "", 1, "R", false, 0, "")
	}

	r.section(1)
	if d.AuditIntegrity == nil {
		r.text("The audit chain was not verified (no audit store).")
	} else {
		state := "intact"
		if !d.AuditIntegrity.Intact {
			state = "BROKEN: " + d.AuditIntegrity.BrokenReason
		}
		r.pair("Result", state)
		r.pair("Entries checked", fmt.Sprint(d.AuditIntegrity.Checked))
	}

	r.section(2)
	r.pair("Score", fmt.Sprintf("%.1f %%", d.Score.Score))
	r.pair("Results", fmt.Sprintf("%d passed, %d failed, %d not applicable", d.Score.Passed, d.Score.Failed, d.Score.NotApplicable))
	if len(d.Trend) == 0 {
		r.text("No score history in the last 400 days.")
	} else {
		pdf.Ln(2)
		r.row([]float64{40, 40, 40}, true, "Day", "Score", "Failed")
		for _, p := range trendSample(d.Trend, 30) {
			r.row([]float64{40, 40, 40}, false, p.Day, fmt.Sprintf("%.1f %%", p.Score), fmt.Sprint(p.Failed))
		}
	}

	r.section(3)
	if len(d.Controls) == 0 {
		r.text("The control catalog of the standard is empty.")
	}
	for _, c := range d.Controls {
		pdf.SetFont("Helvetica", "B", 10)
		pdf.MultiCell(0, 5, r.tr(c.ControlID+" "+c.Title), "", "L", false)
		pdf.SetFont("Helvetica", "", 9)
		if len(c.Rules) == 0 {
			r.text("  no rule assigned (gap)")
			continue
		}
		for _, rule := range c.Rules {
			state := failingState(d, rule.RuleID)
			if !rule.Active {
				state = "inactive"
			}
			r.text(fmt.Sprintf("  %s [%s] - %s", rule.RuleName, rule.Severity, state))
		}
	}

	r.section(4)
	if len(d.Failing) == 0 {
		r.text("No rule fails.")
	}
	for _, rule := range d.Failing {
		pdf.SetFont("Helvetica", "B", 10)
		pdf.MultiCell(0, 5, r.tr(fmt.Sprintf("%s [%s] - %d CIs", rule.RuleName, rule.Severity, len(rule.CIs))), "", "L", false)
		pdf.SetFont("Helvetica", "", 9)
		if rule.RemediationHint != "" {
			r.text("Remediation: " + rule.RemediationHint)
		}
		names := make([]string, 0, len(rule.CIs))
		for _, c := range rule.CIs {
			names = append(names, c.Name)
		}
		r.text(strings.Join(names, ", "))
	}

	r.section(5)
	total, kev := 0, 0
	r.row([]float64{50, 40, 40}, true, "Severity", "Open", "KEV")
	for _, f := range d.Findings {
		total += f.Open
		kev += f.KEV
		r.row([]float64{50, 40, 40}, false, f.Severity, fmt.Sprint(f.Open), fmt.Sprint(f.KEV))
	}
	share := 0.0
	if total > 0 {
		share = float64(kev) * 100 / float64(total)
	}
	r.pair("Open findings", fmt.Sprintf("%d, of which %d in CISA KEV (%.1f %%)", total, kev, share))

	r.section(6)
	active := 0
	for _, c := range d.Capabilities {
		if c.Enabled {
			active++
			r.text("- " + c.FeatureKey)
		}
	}
	if active == 0 {
		r.text("No capability listed.")
	}

	if err := pdf.Error(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// section starts section n on a new page and records its page for the
// table of contents.
func (r *pdfReport) section(n int) {
	r.pdf.AddPage()
	r.pdf.RegisterAlias(fmt.Sprintf("{toc%d}", n), fmt.Sprint(r.pdf.PageNo()))
	r.pdf.SetFont("Helvetica", "B", 13)
	r.pdf.CellFormat(0, 9, r.tr(fmt.Sprintf("%d. %s", n, pdfSections[n-1])), "", 1, "L", false, 0, "")
	r.pdf.SetFont("Helvetica", "", 10)
}

func (r *pdfReport) pair(label, value string) {
	r.pdf.SetFont("Helvetica", "B", 10)
	r.pdf.CellFormat(45, 6, r.tr(label), "", 0, "L", false, 0, "")
	r.pdf.SetFont("Helvetica", "", 10)
	r.pdf.MultiCell(0, 6, r.tr(value), "", "L", false)
}

func (r *pdfReport) text(s string) {
	r.pdf.MultiCell(0, 5, r.tr(s), "", "L", false)
}

func (r *pdfReport) row(widths []float64, head bool, cells ...string) {
	style := ""
	if head {
		style = "B"
	}
	r.pdf.SetFont("Helvetica", style, 9)
	for i, c := range cells {
		ln := 0
		if i == len(cells)-1 {
			ln = 1
		}
		r.pdf.CellFormat(widths[i], 5, r.tr(c), "1", ln, "L", false, 0, "")
	}
	r.pdf.SetFont("Helvetica", "", 10)
}

// failingState reports how many CIs a rule fails on.
func failingState(d *ReportData, ruleID string) string {
	for _, f := range d.Failing {
		if f.RuleID == ruleID {
			return fmt.Sprintf("fails on %d CIs", len(f.CIs))
		}
	}
	return "passes"
}

// trendSample keeps at most n points of the trend, evenly spread, with the
// newest.
func trendSample(points []HistoryPoint, n int) []HistoryPoint {
	if len(points) <= n {
		return points
	}
	out := make([]HistoryPoint, 0, n)
	step := float64(len(points)-1) / float64(n-1)
	for i := range n {
		out = append(out, points[int(float64(i)*step+0.5)])
	}
	return out
}
