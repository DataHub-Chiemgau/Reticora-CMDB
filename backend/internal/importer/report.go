package importer

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
)

// Codes of the rows of an import run (report, job.error). The parser codes
// (import-*) are in parse.go; the codes of the outcome of a written row
// are the outcomes (created, updated, skipped).
const (
	// CodeTransformFailed: a transformation of the mapping failed for the
	// value of the field (date_format, enum_map).
	CodeTransformFailed = "transform_failed"
	// CodeEntityNotSupported: no writer is registered for the entity of
	// the profile; the run writes no row.
	CodeEntityNotSupported = "entity_not_supported"
	// CodeModuleDisabled: the module of the entity is switched off
	// (release gate); the run writes no row.
	CodeModuleDisabled = "module_disabled"
	// CodeFeatureNotEntitled: the organization lacks a feature of the
	// entity or the feature import (K12); the run writes no row.
	CodeFeatureNotEntitled = "feature-not-entitled"
	// CodeOutOfScope: the row concerns an object outside the scope of the
	// user who started the run.
	CodeOutOfScope = "out_of_scope"
	// CodeAmbiguousLocation: a level of a location path matches more than
	// one location.
	CodeAmbiguousLocation = "ambiguous_location"
	// CodeLocationNotFound: a location reference names no location; a
	// reference does not create locations.
	CodeLocationNotFound = "location_not_found"
	// CodeColumnMissing: a column of the mapping is not in the header of
	// the file; its target stays empty (defaults apply).
	CodeColumnMissing = "column_missing"
	// CodeInvalidValue: a value the writer cannot use (a required field
	// is empty, a value of the wrong form).
	CodeInvalidValue = "invalid_value"
	// CodeWriteFailed: the row could not be written for a reason its
	// readers must not see (database error); the log keeps it.
	CodeWriteFailed = "write_failed"
	// CodeForbidden: the user who started the run no longer holds the
	// write right of the entity (current role assignments, jobContext).
	CodeForbidden = "import-forbidden"
	// CodePrincipalMissing: the principal who started the job cannot be
	// restored: no identity reference (StarterRef) or scope snapshot, no
	// active user of the organization (deactivated, deleted) or an API key
	// that no longer acts as the creator of the job (fail-closed).
	CodePrincipalMissing = "import-principal-missing"
	// CodeDryRunUnavailable: the profile store cannot roll a row back
	// (in-memory profile without database), so no dry run.
	CodeDryRunUnavailable = "import-dry-run-unavailable"
)

// Issue is one line of the report: the line of the file (0: the file as a
// whole), the field (target of the mapping or column), the code and the
// message.
type Issue struct {
	Line    int
	Field   string
	Code    string
	Message string
}

// reportBOM starts the report like every CSV export (EXP-01).
const reportBOM = "\ufeff"

// reportHeader are the columns of the report.
var reportHeader = []string{"Zeile", "Feld", "Code", "Meldung"}

// report is the CSV report of a run: UTF-8 with BOM, semicolons and the
// columns Zeile;Feld;Code;Meldung (EXP-01). It lists the parse and limit
// errors of the file and the outcome of every row, so a dry run has the
// report of the real run.
type report struct {
	buf    bytes.Buffer
	w      *csv.Writer
	header []string
}

func newReport() *report {
	r := &report{}
	r.buf.WriteString(reportBOM)
	r.w = csv.NewWriter(&r.buf)
	r.w.Comma = ';'
	_ = r.w.Write(reportHeader)
	return r
}

// withHeader names the columns of parser errors by the header of the file.
func (r *report) withHeader(header []string) { r.header = header }

// addIssue writes one line.
func (r *report) addIssue(is *Issue) {
	line := ""
	if is.Line > 0 {
		line = strconv.Itoa(is.Line)
	}
	_ = r.w.Write([]string{line, neutralize(is.Field), is.Code, neutralize(is.Message)})
}

// add writes an error of the parser; its column is named by the header.
func (r *report) add(e *ParseError) {
	field := ""
	if e.Column > 0 {
		field = strconv.Itoa(e.Column)
		if e.Column <= len(r.header) && strings.TrimSpace(r.header[e.Column-1]) != "" {
			field = r.header[e.Column-1]
		}
	}
	r.addIssue(&Issue{Line: e.Line, Field: field, Code: e.Code, Message: e.Message})
}

func (r *report) bytes() []byte {
	r.w.Flush()
	return r.buf.Bytes()
}

// formulaTriggers are the leading characters that make a spreadsheet read
// a cell as a formula (CSV injection, F-338).
const formulaTriggers = "=+-@\t\r"

// neutralize prefixes a cell that starts with a formula trigger with an
// apostrophe; field names and messages may echo values of the file.
func neutralize(cell string) string {
	if cell != "" && strings.IndexByte(formulaTriggers, cell[0]) >= 0 {
		return "'" + cell
	}
	return cell
}
