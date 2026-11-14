package compliance

import (
	"bytes"
	"encoding/csv"
	"strings"
	"time"
)

// The CSV of a report (SEC-04, WP-1129 AK3): UTF-8 with a head row, one row
// per control × rule × CI of the standard; a control without rule has empty
// rule and CI columns, a rule without result empty CI columns. A cell that
// a spreadsheet would read as a formula (=, +, -, @, tab, CR) is prefixed
// with an apostrophe (CSV injection).

// ReportCSVHeader are the columns of the CSV.
var ReportCSVHeader = []string{"standard", "control_id", "control_title", "rule_id", "rule_name", "severity", "ci_id", "ci_name",
	"status", "evaluated_at"}

func renderCSV(d *ReportData) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(ReportCSVHeader); err != nil {
		return nil, err
	}
	write := func(cells ...string) error {
		for i := range cells {
			cells[i] = csvSafe(cells[i])
		}
		return w.Write(cells)
	}
	for _, c := range d.Controls {
		if len(c.Rules) == 0 {
			if err := write(d.Standard, c.ControlID, c.Title, "", "", "", "", "", "", ""); err != nil {
				return nil, err
			}
			continue
		}
		for _, rule := range c.Rules {
			results := d.Results[rule.RuleID]
			if len(results) == 0 {
				if err := write(d.Standard, c.ControlID, c.Title, rule.RuleID, rule.RuleName, rule.Severity, "", "", "", ""); err != nil {
					return nil, err
				}
				continue
			}
			for i := range results {
				r := &results[i]
				if err := write(d.Standard, c.ControlID, c.Title, rule.RuleID, rule.RuleName, rule.Severity, r.CIID, r.CIName, r.Status,
					r.EvaluatedAt.UTC().Format(time.RFC3339)); err != nil {
					return nil, err
				}
			}
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// csvSafe neutralizes a leading formula character.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
