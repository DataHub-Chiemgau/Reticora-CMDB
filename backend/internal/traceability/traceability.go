// Package traceability checks docs/traceability.csv (GLO-11, DOD-01).
//
// The file links every requirement ID of the catalogue (docs/spec/katalog-v3)
// to its epic, a test file and the work package that delivered it. The checks
// follow decision E-30: format, known IDs, existing test files and the rows the
// implementation plan prescribes for the work packages of a pull request are
// blocking. A row names a test file by its form (IsTestFile); other evidence
// is accepted only for the rows the plan prescribes (AllowedEvidence), the gate
// checklist never (F-121). Since gate G1 (WP-213) every [B] and [Q] ID needs a
// row or an entry in docs/acceptance/g1-ausnahmen.csv (E-76); completeness
// over the [Pn]/[A] IDs of later phases is reported.
package traceability

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Header is the mandatory first line of docs/traceability.csv.
const Header = "id;tag;epic;testdatei;wp"

// Entry is one row of docs/traceability.csv.
type Entry struct {
	Line     int
	ID       string
	Tag      string
	Epic     string
	TestFile string
	WP       string
}

func (e Entry) String() string {
	return strings.Join([]string{e.ID, e.Tag, e.Epic, e.TestFile, e.WP}, ";")
}

// requirementKey identifies a row independently of its test file.
func (e *Entry) requirementKey() string {
	return strings.Join([]string{e.ID, e.Tag, e.Epic, e.WP}, ";")
}

var (
	idPattern = regexp.MustCompile(`^(?:[A-Z]{2,5}-\d{2}|CH\d+)$`)
	wpPattern = regexp.MustCompile(`^WP-\d{3}$`)
	// wpReferences matches a WP number or a range "WP-332 – WP-335" (en or em
	// dash, hyphen) or "WP-010 bis WP-012" on one line.
	wpReferences = regexp.MustCompile(`WP-(\d{3})(?:(?:[ \t]*[–—-][ \t]*|[ \t]+bis[ \t]+)WP-(\d{3}))?`)
)

// ParseCSV reads docs/traceability.csv. It returns every well-formed row and
// one error per malformed line, so a single run reports all problems.
func ParseCSV(r io.Reader) ([]Entry, []error) {
	var (
		entries []Entry
		errs    []error
	)
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimRight(scanner.Text(), "\r")
		if line == 1 {
			if text != Header {
				errs = append(errs, fmt.Errorf("line 1: header must be %q, got %q", Header, text))
			}
			continue
		}
		if strings.TrimSpace(text) == "" {
			errs = append(errs, fmt.Errorf("line %d: empty line", line))
			continue
		}
		fields := strings.Split(text, ";")
		if len(fields) != 5 {
			errs = append(errs, fmt.Errorf("line %d: expected 5 fields separated by ';', got %d", line, len(fields)))
			continue
		}
		e := Entry{Line: line, ID: fields[0], Tag: fields[1], Epic: fields[2], TestFile: fields[3], WP: fields[4]}
		var problems []string
		for i, name := range strings.Split(Header, ";") {
			switch value := fields[i]; {
			case value == "":
				problems = append(problems, name+" is empty")
			case strings.TrimSpace(value) != value:
				problems = append(problems, name+" has surrounding whitespace")
			}
		}
		if e.ID != "" && !idPattern.MatchString(e.ID) {
			problems = append(problems, fmt.Sprintf("id %q is not a requirement ID", e.ID))
		}
		if e.WP != "" && !wpPattern.MatchString(e.WP) {
			problems = append(problems, fmt.Sprintf("wp %q must look like WP-nnn", e.WP))
		}
		if len(problems) > 0 {
			errs = append(errs, fmt.Errorf("line %d: %s", line, strings.Join(problems, "; ")))
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Errorf("read: %w", err))
	}
	if line == 0 {
		errs = append(errs, errors.New("file is empty"))
	}
	return entries, errs
}

// Catalog maps every known requirement ID to its tag. An empty tag means the
// ID is known but its tag was not delivered.
type Catalog map[string]string

var (
	idListToken = regexp.MustCompile(`^\s*(?:(,|/|und|bis)\s*)?([A-Z]{2,5}-\d{2}|CH\d+)\b`)
	leadingTag  = regexp.MustCompile(`^:?\s*(\[[^\]]+\])`)
)

// ParseCatalogText adds the IDs defined in one catalogue file. A definition is
// a line that starts with one or more IDs, e.g. "GLO-11 [Q] …",
// "MET-10/MET-11: …" or "TEC-01 bis TEC-05, TEC-07 unverändert." (ranges are
// expanded). The tag directly following the IDs is recorded when present.
func (c Catalog) ParseCatalogText(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		rest := scanner.Text()
		var ids []string
		for {
			m := idListToken.FindStringSubmatchIndex(rest)
			if m == nil {
				break
			}
			sep := ""
			if m[2] >= 0 {
				sep = rest[m[2]:m[3]]
			}
			if sep == "" && len(ids) > 0 {
				// Two IDs without separator: the list has ended.
				break
			}
			id := rest[m[4]:m[5]]
			if sep == "bis" && len(ids) > 0 {
				ids = append(ids, expandRange(ids[len(ids)-1], id)...)
			} else {
				ids = append(ids, id)
			}
			rest = rest[m[1]:]
		}
		if len(ids) == 0 {
			continue
		}
		tag := ""
		if m := leadingTag.FindStringSubmatch(rest); m != nil {
			tag = m[1]
		}
		for _, id := range ids {
			if existing, ok := c[id]; !ok || existing == "" {
				c[id] = tag
			}
		}
	}
	return scanner.Err()
}

// expandRange returns the IDs after from up to and including to ("MET-01 bis
// MET-03" yields MET-02, MET-03). Ranges across prefixes yield only to.
func expandRange(from, to string) []string {
	fp, fn, ok1 := splitID(from)
	tp, tn, ok2 := splitID(to)
	if !ok1 || !ok2 || fp != tp || tn <= fn {
		return []string{to}
	}
	var out []string
	for n := fn + 1; n <= tn; n++ {
		out = append(out, fmt.Sprintf("%s-%02d", fp, n))
	}
	return out
}

func splitID(id string) (prefix string, number int, ok bool) {
	prefix, num, ok := strings.Cut(id, "-")
	if !ok {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return "", 0, false
	}
	return prefix, n, true
}

// AddCoverage adds the IDs of docs/plan/abdeckung.csv. It lists the audited
// IDs including those of catalogue parts whose text is still missing (E-01),
// and its tags take precedence because the plan derives the traceability rows
// from it.
func (c Catalog) AddCoverage(r io.Reader) error {
	reader := csv.NewReader(r)
	reader.Comma = ';'
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("parse coverage: %w", err)
	}
	if len(records) == 0 || len(records[0]) < 2 || records[0][0] != "id" || records[0][1] != "tag" {
		return errors.New("parse coverage: header must start with id;tag")
	}
	for _, rec := range records[1:] {
		if len(rec) < 2 || !idPattern.MatchString(rec[0]) {
			return fmt.Errorf("parse coverage: invalid row %q", strings.Join(rec, ";"))
		}
		c[rec[0]] = NormalizeTag(rec[1])
	}
	return nil
}

// NormalizeTag converts a tag into its docs/traceability.csv spelling: the
// separator ';' inside a tag is written as ','.
func NormalizeTag(tag string) string {
	return strings.TrimSpace(strings.ReplaceAll(tag, ";", ","))
}

var requiredTag = regexp.MustCompile(`\[(?:B|A|P[2-5])\]|\[P[2-5]–|\b(?:B|P[2-5])/G`)

// Required returns the sorted IDs whose tag is [B], [Pn] or [A]; GLO-11
// requires a traceability row for each of them.
func (c Catalog) Required() []string {
	var ids []string
	for id, tag := range c {
		if requiredTag.MatchString(tag) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

var gateTag = regexp.MustCompile(`\[[BQ]\]|\[B–|\bB/G1\b`)

// RequiredForGate returns the sorted IDs whose tag is [B] or [Q], alone or
// with a later-phase part ("[B], Teilumfang [P2]", "B/G1"). Gate G1 requires a
// traceability row for each of them (GATE-02, E-30).
func (c Catalog) RequiredForGate() []string {
	var ids []string
	for id, tag := range c {
		if gateTag.MatchString(tag) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// PlanFiles are the plans whose WP blocks the check reads, relative to the
// repository root: the Phase-1 plan and the G1 remediation plan (WP-336). WP
// numbers are unique across all of them.
var PlanFiles = []string{
	"docs/plan/implementierungsplan.md",
	"docs/plan/g1-korrektur/implementierungsplan.md",
}

// planNames names the plans in error messages.
func planNames() string {
	return strings.Join(PlanFiles, " or ")
}

// WorkPackage is the traceability part of one WP block of the plan.
type WorkPackage struct {
	ID   string
	Epic string
	Rows []Entry
	// Plan is the plan file the block was read from (set by MergePlans).
	Plan string
}

// MergePlans combines the work packages of several plans, keyed by plan
// file. A WP number that occurs in more than one plan is an error naming both
// files.
func MergePlans(plans map[string]map[string]*WorkPackage) (map[string]*WorkPackage, error) {
	names := make([]string, 0, len(plans))
	for name := range plans {
		names = append(names, name)
	}
	sort.Strings(names)
	merged := map[string]*WorkPackage{}
	var errs []error
	for _, name := range names {
		for id, wp := range plans[name] {
			if prev, dup := merged[id]; dup {
				errs = append(errs, fmt.Errorf("%s is defined in %s and in %s", id, prev.Plan, name))
				continue
			}
			wp.Plan = name
			merged[id] = wp
		}
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
		return nil, errors.Join(errs...)
	}
	return merged, nil
}

var (
	wpHeading = regexp.MustCompile(`^### (WP-\d{3}) `)
	epicField = regexp.MustCompile(`\*\*Epic \(Traceability\):\*\* ([^·]+?)\s*(?:·|$)`)
)

// ParsePlan reads one plan (see PlanFiles) and returns, per WP, the
// epic of its block and the rows listed under "Traceability".
func ParsePlan(r io.Reader) (map[string]*WorkPackage, error) {
	plan := map[string]*WorkPackage{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		current *WorkPackage
		inTrace bool
		inBlock bool
		line    int
	)
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if m := wpHeading.FindStringSubmatch(text); m != nil {
			current = &WorkPackage{ID: m[1]}
			plan[m[1]] = current
			inTrace, inBlock = false, false
			continue
		}
		if strings.HasPrefix(text, "## ") {
			current, inTrace, inBlock = nil, false, false
			continue
		}
		if current == nil {
			continue
		}
		if m := epicField.FindStringSubmatch(text); m != nil {
			current.Epic = strings.TrimSpace(m[1])
		}
		if strings.HasPrefix(text, "**Traceability") {
			inTrace = true
			continue
		}
		if !inTrace {
			continue
		}
		if strings.HasPrefix(text, "```") {
			if inBlock {
				inTrace = false
			}
			inBlock = !inBlock
			continue
		}
		if inBlock && strings.TrimSpace(text) != "" {
			fields := strings.Split(text, ";")
			if len(fields) != 5 {
				return nil, fmt.Errorf("plan line %d: traceability row %q must have 5 fields", line, text)
			}
			current.Rows = append(current.Rows, Entry{
				Line: line, ID: fields[0], Tag: fields[1], Epic: fields[2], TestFile: fields[3], WP: fields[4],
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for id, wp := range plan {
		if wp.Epic == "" {
			return nil, fmt.Errorf("plan: %s has no \"Epic (Traceability)\" field", id)
		}
	}
	return plan, nil
}

// ChecklistFile is the gate checklist of G1. It records the acceptance and is
// never evidence for a requirement (F-121, E-76).
const ChecklistFile = "docs/acceptance/g1-gate.md"

// testFilePattern matches Go tests (*_test.go), Vitest and Playwright files
// (*.test.ts, *.test.tsx, *.spec.ts), Terraform tests (*.tftest.hcl) and the
// scripts under tests/ (*.sh, *.js, *.ts).
var testFilePattern = regexp.MustCompile(
	`^(?:[^/]+/)*[^/]+(?:_test\.go|\.test\.tsx?|\.spec\.ts|\.tftest\.hcl)$|^tests/(?:[^/]+/)*[^/]+\.(?:sh|js|ts)$`)

// IsTestFile reports whether a repository-relative path names a test file by
// its form (GLO-11: every row names at least one kind of test).
func IsTestFile(path string) bool {
	return testFilePattern.MatchString(path)
}

// Evidence is a non-test file the plan prescribes as evidence of one
// requirement in one work package.
type Evidence struct {
	ID   string
	WP   string
	Path string
}

// AllowedEvidence lists the 21 rows of docs/plan/implementierungsplan.md
// whose evidence is no test file: the CI workflow, scan, review, pentest and
// installation records, the operating reference model and the load and
// outage harnesses. Any other row must name a test file (F-121).
var AllowedEvidence = []Evidence{
	{"TST-04", "WP-001", ".github/workflows/ci.yml"},
	{"TST-04", "WP-002", ".github/workflows/ci.yml"},
	{"TST-04", "WP-003", ".github/workflows/ci.yml"},
	{"TST-02", "WP-003", ".github/workflows/ci.yml"},
	{"TST-04", "WP-004", "docs/security/abhaengigkeitsscans.md"},
	{"SEC-11", "WP-004", "docs/security/abhaengigkeitsscans.md"},
	{"OPS-03", "WP-099", ".github/workflows/ci.yml"},
	{"OPS-06", "WP-104", "tests/outage/"},
	{"PRI-11", "WP-105", "docs/betrieb/referenzmodell.md"},
	{"NFR-09", "WP-105", "docs/betrieb/referenzmodell.md"},
	{"SIM-01", "WP-106", ".github/workflows/ci.yml"},
	{"NFR-10", "WP-206", "tests/load/"},
	{"NFR-01", "WP-206", "tests/load/"},
	{"NFR-02", "WP-206", "tests/load/"},
	{"NFR-03", "WP-206", "tests/load/"},
	{"ABN-02", "WP-206", "tests/load/"},
	{"GATE-03", "WP-209", "docs/security/review-g1.md"},
	{"SEC-11", "WP-209", "docs/security/review-g1.md"},
	{"GATE-03", "WP-210", "docs/security/pentest-g1.md"},
	{"GATE-03", "WP-211", "docs/acceptance/g1-installation.md"},
	{"INS-01", "WP-211", "docs/acceptance/g1-installation.md"},
}

func allowedEvidence(e *Entry) bool {
	for _, a := range AllowedEvidence {
		if a.ID == e.ID && a.WP == e.WP && a.Path == e.TestFile {
			return true
		}
	}
	return false
}

// Validate checks every entry against the catalogue, the plan and the
// repository. exists reports whether a repository-relative path exists.
func Validate(entries []Entry, catalog Catalog, plan map[string]*WorkPackage, exists func(string) bool) []error {
	var errs []error
	seen := map[string]int{}
	for _, e := range entries {
		if first, dup := seen[e.String()]; dup {
			errs = append(errs, fmt.Errorf("line %d: duplicate of line %d", e.Line, first))
			continue
		}
		seen[e.String()] = e.Line

		tag, known := catalog[e.ID]
		switch {
		case !known:
			errs = append(errs, fmt.Errorf("line %d: unknown requirement ID %s", e.Line, e.ID))
		case tag != "" && tag != e.Tag:
			errs = append(errs, fmt.Errorf("line %d: tag of %s must be %q, got %q", e.Line, e.ID, tag, e.Tag))
		}

		wp, inPlan := plan[e.WP]
		switch {
		case !inPlan:
			errs = append(errs, fmt.Errorf("line %d: %s is not a work package of %s", e.Line, e.WP, planNames()))
		case wp.Epic != e.Epic:
			errs = append(errs, fmt.Errorf("line %d: epic of %s must be %q, got %q", e.Line, e.WP, wp.Epic, e.Epic))
		}

		switch {
		case strings.HasPrefix(e.TestFile, "/") || strings.Contains(e.TestFile, ".."):
			errs = append(errs, fmt.Errorf("line %d: testdatei %q must be a path relative to the repository root", e.Line, e.TestFile))
		case e.TestFile == ChecklistFile:
			errs = append(errs, fmt.Errorf("line %d: testdatei %s is the gate checklist and no evidence of %s; list the ID in %s (E-76)",
				e.Line, ChecklistFile, e.ID, ExceptionsFile))
		case !IsTestFile(e.TestFile) && !allowedEvidence(&e):
			errs = append(errs, fmt.Errorf("line %d: testdatei %q is no test file (*_test.go, *.test.ts, *.test.tsx, *.spec.ts, *.tftest.hcl, tests/**/*.sh|js|ts) and not allowed as evidence of %s in %s",
				e.Line, e.TestFile, e.ID, e.WP))
		case !exists(e.TestFile):
			errs = append(errs, fmt.Errorf("line %d: testdatei %q does not exist", e.Line, e.TestFile))
		}
	}
	return errs
}

// CheckWorkPackages requires that every row the plan prescribes for the given
// work packages is present (same ID, tag, epic and WP; the test file may
// differ because the row names the test file actually created).
func CheckWorkPackages(entries []Entry, plan map[string]*WorkPackage, wps []string) []error {
	have := map[string]bool{}
	for _, e := range entries {
		have[e.requirementKey()] = true
	}
	var errs []error
	for _, id := range wps {
		wp, ok := plan[id]
		if !ok {
			errs = append(errs, fmt.Errorf("%s is not a work package of %s", id, planNames()))
			continue
		}
		for _, row := range wp.Rows {
			if !have[row.requirementKey()] {
				errs = append(errs, fmt.Errorf("%s: missing traceability row %s (plan line %d)", id, row, row.Line))
			}
		}
	}
	return errs
}

// Missing returns the required IDs without any traceability row.
func Missing(entries []Entry, catalog Catalog) []string {
	return missingOf(entries, catalog.Required())
}

// MissingForGate returns the [B]/[Q] IDs without any traceability row.
func MissingForGate(entries []Entry, catalog Catalog) []string {
	return missingOf(entries, catalog.RequiredForGate())
}

func missingOf(entries []Entry, required []string) []string {
	covered := map[string]bool{}
	for _, e := range entries {
		covered[e.ID] = true
	}
	var missing []string
	for _, id := range required {
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// ExceptionsFile lists the [B]/[Q] IDs of gate G1 that are not accepted by
// evidence (E-76), relative to the repository root.
const ExceptionsFile = "docs/acceptance/g1-ausnahmen.csv"

// ExceptionsHeader is the mandatory first line of ExceptionsFile.
const ExceptionsHeader = "id;art;grund;entscheidung;lieferung"

// Kinds (art) of an exception.
const (
	// KindDeclaration: accepted by a declaration of the client, the evidence
	// is still to be delivered (E-76).
	KindDeclaration = "erklaerung"
	// KindGap: a known gap with the work package that closes it.
	KindGap = "luecke"
	// KindNotApplicable: a rule of a later gate, not applicable to G1.
	KindNotApplicable = "np"
)

// Exception is one row of ExceptionsFile.
type Exception struct {
	Line     int
	ID       string
	Kind     string
	Reason   string
	Decision string
	Delivery string
}

// ParseExceptions reads ExceptionsFile. Like ParseCSV it returns every
// well-formed row and one error per malformed line.
func ParseExceptions(r io.Reader) ([]Exception, []error) {
	var (
		exceptions []Exception
		errs       []error
	)
	seen := map[string]int{}
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimRight(scanner.Text(), "\r")
		if line == 1 {
			if text != ExceptionsHeader {
				errs = append(errs, fmt.Errorf("%s line 1: header must be %q, got %q", ExceptionsFile, ExceptionsHeader, text))
			}
			continue
		}
		if strings.TrimSpace(text) == "" {
			errs = append(errs, fmt.Errorf("%s line %d: empty line", ExceptionsFile, line))
			continue
		}
		fields := strings.Split(text, ";")
		if len(fields) != 5 {
			errs = append(errs, fmt.Errorf("%s line %d: expected 5 fields separated by ';', got %d", ExceptionsFile, line, len(fields)))
			continue
		}
		x := Exception{Line: line, ID: fields[0], Kind: fields[1], Reason: fields[2], Decision: fields[3], Delivery: fields[4]}
		var problems []string
		for i, name := range strings.Split(ExceptionsHeader, ";") {
			switch value := fields[i]; {
			case strings.TrimSpace(value) == "":
				problems = append(problems, name+" is empty")
			case strings.TrimSpace(value) != value:
				problems = append(problems, name+" has surrounding whitespace")
			}
		}
		if x.ID != "" && !idPattern.MatchString(x.ID) {
			problems = append(problems, fmt.Sprintf("id %q is not a requirement ID", x.ID))
		}
		switch x.Kind {
		case KindDeclaration, KindGap, KindNotApplicable, "":
		default:
			problems = append(problems, fmt.Sprintf("art %q must be %s, %s or %s", x.Kind, KindDeclaration, KindGap, KindNotApplicable))
		}
		if first, dup := seen[x.ID]; dup && x.ID != "" {
			problems = append(problems, fmt.Sprintf("%s is already listed in line %d", x.ID, first))
		}
		if len(problems) > 0 {
			errs = append(errs, fmt.Errorf("%s line %d: %s", ExceptionsFile, line, strings.Join(problems, "; ")))
			continue
		}
		seen[x.ID] = line
		exceptions = append(exceptions, x)
	}
	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Errorf("read %s: %w", ExceptionsFile, err))
	}
	if line == 0 {
		errs = append(errs, fmt.Errorf("%s is empty", ExceptionsFile))
	}
	return exceptions, errs
}

// ValidateExceptions requires that every exception names a [B]/[Q] ID of the
// catalogue: only those are subject to gate G1.
func ValidateExceptions(exceptions []Exception, catalog Catalog) []error {
	gate := map[string]bool{}
	for _, id := range catalog.RequiredForGate() {
		gate[id] = true
	}
	var errs []error
	for _, x := range exceptions {
		tag, known := catalog[x.ID]
		switch {
		case !known:
			errs = append(errs, fmt.Errorf("%s line %d: unknown requirement ID %s", ExceptionsFile, x.Line, x.ID))
		case !gate[x.ID]:
			errs = append(errs, fmt.Errorf("%s line %d: %s is tagged %q, not a [B]/[Q] ID of gate G1", ExceptionsFile, x.Line, x.ID, tag))
		}
	}
	return errs
}

// GateReport is the state of the [B]/[Q] IDs at gate G1 (E-30, E-76).
type GateReport struct {
	// Required are the [B]/[Q] IDs.
	Required []string
	// Traced are the required IDs with at least one traceability row.
	Traced []string
	// Exceptions are the required IDs listed in ExceptionsFile.
	Exceptions []string
	// TracedNotAccepted are the exceptions with a traceability row: traced,
	// but not accepted until the delivery is in.
	TracedNotAccepted []string
	// Blocking are the required IDs that are neither traced nor listed as
	// exception.
	Blocking []string
}

// NewGateReport classifies the required [B]/[Q] IDs. missing are the required
// IDs without a row (MissingForGate). An exception without a row does not
// count as traced; an exception with a row is traced, not accepted.
func NewGateReport(required, missing []string, exceptions []Exception) GateReport {
	untraced := map[string]bool{}
	for _, id := range missing {
		untraced[id] = true
	}
	listed := map[string]bool{}
	for _, x := range exceptions {
		listed[x.ID] = true
	}
	r := GateReport{Required: required}
	for _, id := range required {
		switch {
		case !untraced[id]:
			r.Traced = append(r.Traced, id)
			if listed[id] {
				r.TracedNotAccepted = append(r.TracedNotAccepted, id)
			}
		case !listed[id]:
			r.Blocking = append(r.Blocking, id)
		}
		if listed[id] {
			r.Exceptions = append(r.Exceptions, id)
		}
	}
	return r
}

// Summary is the one-line report of the gate.
func (r *GateReport) Summary() string {
	return fmt.Sprintf("gate G1: %d of %d traced, %d exceptions (%d traced, not accepted)",
		len(r.Traced), len(r.Required), len(r.Exceptions), len(r.TracedNotAccepted))
}

// WorkPackagesIn extracts the distinct WP numbers mentioned in s, in order.
// Ranges on one line ("WP-332 – WP-335", "WP-010 bis WP-012") are expanded; a
// descending range yields its two ends.
func WorkPackagesIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n int) {
		wp := fmt.Sprintf("WP-%03d", n)
		if !seen[wp] {
			seen[wp] = true
			out = append(out, wp)
		}
	}
	for _, m := range wpReferences.FindAllStringSubmatch(s, -1) {
		from, _ := strconv.Atoi(m[1])
		if m[2] == "" {
			add(from)
			continue
		}
		to, _ := strconv.Atoi(m[2])
		if to < from {
			add(from)
			add(to)
			continue
		}
		for n := from; n <= to; n++ {
			add(n)
		}
	}
	return out
}

// workPackagesLine matches the line "Arbeitspakete:" of the PR template, also
// in bold ("**Arbeitspakete:**").
var workPackagesLine = regexp.MustCompile(`(?i)^[*_ ]*Arbeitspakete?[*_ ]*:`)

// PullRequestWorkPackages returns the WPs of a pull request: those in its title
// and on the lines "Arbeitspakete:" of its description (DOD-01). Both texts
// are untrusted input and only read here.
func PullRequestWorkPackages(title, body string) []string {
	lines := []string{title}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if workPackagesLine.MatchString(line) {
			lines = append(lines, line)
		}
	}
	return WorkPackagesIn(strings.Join(lines, "\n"))
}
