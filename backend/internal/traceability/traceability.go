// Package traceability checks docs/traceability.csv (GLO-11, DOD-01).
//
// The file links every requirement ID of the catalogue (docs/spec/katalog-v3)
// to its epic, a test file and the work package that delivered it. The work
// packages come from three plans (PlanFiles): the Phase-1 plan, the G1
// remediation plan and the Phase-2 plan; the IDs from the catalogue and the
// coverage files of Phase 1 and Phase 2 (CoverageFiles). The checks follow
// decisions E-30 and E-48: format, known IDs, existing test files, the rows
// the plans prescribe for the work packages of a pull request and the
// consistency of the Phase-2 plan with its coverage are blocking. A row names a
// test file by its form (IsTestFile); other evidence is accepted only for the
// rows a plan prescribes (AllowedEvidence), the gate checklist never (F-121).
// Since gate G1 (WP-213) every [B] and [Q] ID needs a row or an entry in
// docs/acceptance/g1-ausnahmen.csv (E-76). Completeness over the [Pn]/[A] IDs
// and over Phase 2 (a row of the closing work package of every ID of the
// Phase-2 coverage) is reported and blocks per gate via
// TRACEABILITY_REQUIRE_COMPLETE (ParseRequireComplete).
package traceability

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
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
	// idPattern accepts IDs with a middle segment such as IMP-IO-01.
	idPattern = regexp.MustCompile(`^(?:[A-Z]{2,5}(?:-[A-Z]{2})?-\d{2}|CH\d+)$`)
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
	idListToken = regexp.MustCompile(`^\s*(?:(,|/|und|bis)\s*)?([A-Z]{2,5}(?:-[A-Z]{2})?-\d{2}|CH\d+)\b`)
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

// splitID splits an ID at its last '-' into prefix and running number
// (IMP-IO-02 yields IMP-IO and 2).
func splitID(id string) (prefix string, number int, ok bool) {
	i := strings.LastIndex(id, "-")
	if i < 0 {
		return "", 0, false
	}
	prefix, num := id[:i], id[i+1:]
	n, err := strconv.Atoi(num)
	if err != nil {
		return "", 0, false
	}
	return prefix, n, true
}

// CoverageFiles are the coverage files of the plans, relative to the
// repository root: Phase 1 and Phase 2. The Phase-2 file names the closing
// work package of every ID (column abschluss_wp).
var CoverageFiles = []string{
	"docs/plan/abdeckung.csv",
	"docs/plan/phase2/abdeckung.csv",
}

// NoClosingWP marks an ID of the Phase-2 coverage without closing work package
// (abschluss_wp "–", E-37: catalogue text not delivered).
const NoClosingWP = "–"

// Coverage is one coverage file: the tag of every listed ID and, for the
// Phase-2 file, its closing work package.
type Coverage struct {
	File string
	// Tags are the tags in their docs/traceability.csv spelling (NormalizeTag).
	Tags map[string]string
	// Closing maps an ID to its column abschluss_wp; empty for a file
	// without that column. CheckPhase2Consistency validates the values of the
	// Phase-2 file.
	Closing map[string]string
	// order keeps the IDs in file order for stable reports.
	order []string
}

// ParseCoverage reads one coverage file. name is used in error messages.
func ParseCoverage(name string, r io.Reader) (*Coverage, error) {
	reader := csv.NewReader(r)
	reader.Comma = ';'
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if len(records) == 0 || len(records[0]) < 2 || records[0][0] != "id" || records[0][1] != "tag" {
		return nil, fmt.Errorf("parse %s: header must start with id;tag", name)
	}
	closingCol := slices.Index(records[0], "abschluss_wp")
	cov := &Coverage{File: name, Tags: map[string]string{}, Closing: map[string]string{}}
	for i, rec := range records[1:] {
		if len(rec) < 2 || !idPattern.MatchString(rec[0]) {
			return nil, fmt.Errorf("parse %s: invalid row %q", name, strings.Join(rec, ";"))
		}
		if _, dup := cov.Tags[rec[0]]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", name, i+2, rec[0])
		}
		cov.Tags[rec[0]] = NormalizeTag(rec[1])
		cov.order = append(cov.order, rec[0])
		if closingCol >= 0 && closingCol < len(rec) {
			cov.Closing[rec[0]] = strings.TrimSpace(rec[closingCol])
		}
	}
	return cov, nil
}

// AddCoverage adds the IDs of docs/plan/abdeckung.csv. It lists the audited
// IDs including those of catalogue parts whose text is still missing (E-01),
// and its tags take precedence because the plan derives the traceability rows
// from it.
func (c Catalog) AddCoverage(r io.Reader) error {
	cov, err := ParseCoverage("coverage", r)
	if err != nil {
		return err
	}
	return c.AddCoverages(cov)
}

// AddCoverages adds the IDs of several coverage files. Their tags take
// precedence over the catalogue text without error; an ID whose tag differs
// between two coverage files is an error (';' and ',' are equal, NormalizeTag).
func (c Catalog) AddCoverages(covs ...*Coverage) error {
	var errs []error
	first := map[string]*Coverage{}
	for _, cov := range covs {
		for _, id := range cov.order {
			tag := cov.Tags[id]
			if prev, ok := first[id]; ok {
				if prev.Tags[id] != tag {
					errs = append(errs, fmt.Errorf("%s: tag %q in %s differs from %q in %s", id, tag, cov.File, prev.Tags[id], prev.File))
				}
				continue
			}
			first[id] = cov
			c[id] = tag
		}
	}
	return errors.Join(errs...)
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

// phase2Tag matches the tags with a Phase-2 part: "[P2]", "[P2–P3]",
// "P2/G2", "B/G1, Teil P2/G2", "[B], Teilumfang [P2]", "(V), [B–P4], [A]".
var phase2Tag = regexp.MustCompile(`\[P2[\]–]|\bP2/G|\[B–P[2-5]`)

// HasPhase2Part reports whether a tag carries a Phase-2 part; such an ID must
// be listed in the Phase-2 coverage.
func HasPhase2Part(tag string) bool {
	return phase2Tag.MatchString(tag)
}

// Completeness levels of TRACEABILITY_REQUIRE_COMPLETE (E-30, E-48).
type Completeness struct {
	// All makes every [B]/[Pn]/[A] ID without a row blocking ("1").
	All bool
	// Phase2 makes the Phase-2 completeness blocking ("P2", gate G2).
	Phase2 bool
}

// ParseRequireComplete reads a comma-separated list of levels. "B" and "Q"
// are accepted and change nothing, because [B]/[Q] block since gate G1;
// "P2" blocks on Phase 2, "1" on every [B]/[Pn]/[A] ID. An empty value only
// reports; any other value is an error.
func ParseRequireComplete(v string) (Completeness, error) {
	var c Completeness
	if strings.TrimSpace(v) == "" {
		return c, nil
	}
	for _, level := range strings.Split(v, ",") {
		switch strings.TrimSpace(level) {
		case "B", "Q":
		case "P2":
			c.Phase2 = true
		case "1":
			c.All = true
		default:
			return Completeness{}, fmt.Errorf("unknown completeness level %q (allowed: B, Q, P2, 1)", strings.TrimSpace(level))
		}
	}
	return c, nil
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
// repository root: the Phase-1 plan, the G1 remediation plan (WP-336) and the
// Phase-2 plan (WP-243). WP numbers are unique across all of them.
var PlanFiles = []string{
	"docs/plan/implementierungsplan.md",
	"docs/plan/g1-korrektur/implementierungsplan.md",
	Phase2PlanFile,
}

// Phase2PlanFile is the plan of Phase 2 (gate G2).
const Phase2PlanFile = "docs/plan/phase2/implementierungsplan.md"

// DeferredScope is the header field of the Phase-1 blocks WP-215 to WP-239,
// deferred by E-03 and replaced by Phase-2 work packages (E-46); their
// numbers are no work packages of a pull request.
const DeferredScope = "nein, verschoben (E-03)"

// OptionalScope starts the header field "G2-Umfang" of a work package
// without gate effect (GATE-05, [O]).
const OptionalScope = "optional"

// planNames names the plans in error messages.
func planNames() string {
	return strings.Join(PlanFiles, " or ")
}

// WorkPackage is the traceability part of one WP block of the plan.
type WorkPackage struct {
	ID   string
	Epic string
	Rows []Entry
	// Plan is the plan file the block was read from.
	Plan string
	// Line is the line of the heading "### WP-nnn".
	Line int
	// Scope is the header field "G1-Umfang" or "G2-Umfang" ("" without one).
	Scope string
}

// Location is "file:line" of the heading of the block.
func (wp *WorkPackage) Location() string {
	return fmt.Sprintf("%s:%d", wp.Plan, wp.Line)
}

// Deferred reports whether the block is a Phase-1 WP deferred by E-03.
func (wp *WorkPackage) Deferred() bool {
	return wp.Scope == DeferredScope
}

// Optional reports whether the block has no gate effect ("G2-Umfang: optional").
func (wp *WorkPackage) Optional() bool {
	return strings.HasPrefix(wp.Scope, OptionalScope)
}

// deferredError is the error for a deferred WP number used as work package.
func deferredError(prefix string, wp *WorkPackage) error {
	return fmt.Errorf("%s%s (%s) is deferred by E-03 and replaced by Phase-2 work packages (E-46); see the table "+
		"\"Verschobene Arbeitspakete aus Phase 1 (WP-215 bis WP-239)\" of %s", prefix, wp.ID, wp.Location(), Phase2PlanFile)
}

// MergePlans combines the work packages of several plans, keyed by plan
// file. A WP number that occurs in more than one plan is an error naming both
// locations (file:line).
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
				if wp.Plan == "" {
					wp.Plan = name
				}
				errs = append(errs, fmt.Errorf("%s is defined in %s and in %s", id, prev.Location(), wp.Location()))
				continue
			}
			if wp.Plan == "" {
				wp.Plan = name
			}
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
	wpHeading    = regexp.MustCompile(`^### (WP-\d{3}) `)
	wpAnyHeading = regexp.MustCompile(`^### WP-`)
	epicField    = regexp.MustCompile(`\*\*Epic \(Traceability\):\*\* ([^·]+?)\s*(?:·|$)`)
	scopeField   = regexp.MustCompile(`\*\*G[12]-Umfang:\*\* ([^·]+?)\s*(?:·|$)`)
)

// ParsePlan reads one plan (see PlanFiles) and returns, per WP, the
// epic of its block, its header field "G1-Umfang"/"G2-Umfang" and the rows
// listed under "Traceability". name is the plan file; every error names it
// with the line ("file:line").
func ParsePlan(name string, r io.Reader) (map[string]*WorkPackage, error) {
	plan := map[string]*WorkPackage{}
	var errs []error
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
			if prev, dup := plan[m[1]]; dup {
				errs = append(errs, fmt.Errorf("%s:%d: %s is defined twice (first at %s)", name, line, m[1], prev.Location()))
			}
			current = &WorkPackage{ID: m[1], Plan: name, Line: line}
			plan[m[1]] = current
			inTrace, inBlock = false, false
			continue
		}
		if wpAnyHeading.MatchString(text) {
			// WP-nnna and the like: wpPattern and the CI expect exactly three digits.
			errs = append(errs, fmt.Errorf("%s:%d: heading %q must start with \"### WP-nnn \" (three digits, no suffix)", name, line, text))
			current, inTrace, inBlock = nil, false, false
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
		if strings.HasPrefix(text, "**Schlüssel:**") {
			if m := scopeField.FindStringSubmatch(text); m != nil {
				current.Scope = strings.TrimSpace(m[1])
			}
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
				errs = append(errs, fmt.Errorf("%s:%d: traceability row %q must have 5 fields", name, line, text))
				continue
			}
			current.Rows = append(current.Rows, Entry{
				Line: line, ID: fields[0], Tag: fields[1], Epic: fields[2], TestFile: fields[3], WP: fields[4],
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	for _, wp := range plan {
		if wp.Epic == "" {
			errs = append(errs, fmt.Errorf("%s: %s has no \"Epic (Traceability)\" field", wp.Location(), wp.ID))
		}
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
		return nil, errors.Join(errs...)
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

// AllowedEvidence lists the 27 rows of the plans (PlanFiles) whose evidence is
// no test file: the CI workflows, scan, review, pentest and installation
// records, the operating reference model and the load and outage harnesses
// (21 rows of the Phase-1 plan, 6 of the Phase-2 plan; the remediation plan
// has none). Any other row must name a test file (F-121).
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
	// Phase-2 plan.
	{"INS-01", "WP-735", ".github/workflows/install-smoke.yml"},
	{"ABN-02", "WP-327", "tests/load/"},
	{"NFR-02", "WP-327", "tests/load/"},
	{"NFR-03", "WP-327", "tests/load/"},
	{"SEC-11", "WP-329", "docs/security/review-phase2.md"},
	{"TST-04", "WP-331", ".github/workflows/ci.yml"},
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
		case wp.Deferred():
			errs = append(errs, deferredError(fmt.Sprintf("line %d: ", e.Line), wp))
		case wp.Epic != e.Epic:
			errs = append(errs, fmt.Errorf("line %d: epic of %s (%s) must be %q, got %q", e.Line, e.WP, wp.Location(), wp.Epic, e.Epic))
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
		if wp.Deferred() {
			errs = append(errs, deferredError("", wp))
			continue
		}
		for _, row := range wp.Rows {
			if !have[row.requirementKey()] {
				errs = append(errs, fmt.Errorf("%s: missing traceability row %s (%s:%d)", id, row, wp.Plan, row.Line))
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

// MissingPhase2 returns the IDs of the Phase-2 coverage whose closing work
// package (abschluss_wp) has no row for them; rows of other work packages, in
// particular of Phase 1, do not count (E-48). IDs without closing work package
// ("–", E-37) and IDs closed by an optional work package are exempt.
func MissingPhase2(entries []Entry, cov *Coverage, plan map[string]*WorkPackage) []string {
	have := map[string]bool{}
	for _, e := range entries {
		have[e.ID+";"+e.WP] = true
	}
	var missing []string
	for _, id := range cov.order {
		closing := cov.Closing[id]
		if closing == "" || closing == NoClosingWP {
			continue
		}
		if wp, ok := plan[closing]; ok && wp.Optional() {
			continue
		}
		if !have[id+";"+closing] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

// Phase2WorkPackages returns the sorted work packages of the Phase-2 plan
// that the gate requires, i.e. without the optional ones.
func Phase2WorkPackages(plan map[string]*WorkPackage) []string {
	var ids []string
	for id, wp := range plan {
		if wp.Plan == Phase2PlanFile && !wp.Optional() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// CheckPhase2Consistency checks the Phase-2 plan against the catalogue and
// the Phase-2 coverage (E-48): (a) the closing work package of every ID is "–"
// or a work package of a plan whose block prescribes a row of that ID; (b)
// every known ID with a Phase-2 part in its tag (HasPhase2Part) is listed in
// the Phase-2 coverage; (c) every row of the Phase-2 plan names a known ID with
// its tag, the epic and the number of its block. Test files are not checked:
// they are created with the work package.
func CheckPhase2Consistency(catalog Catalog, cov *Coverage, plan map[string]*WorkPackage) []error {
	var errs []error
	for _, id := range cov.order {
		closing := cov.Closing[id]
		switch {
		case closing == NoClosingWP:
			continue
		case !wpPattern.MatchString(closing):
			errs = append(errs, fmt.Errorf("%s: abschluss_wp %q of %s must look like WP-nnn or be %q", cov.File, closing, id, NoClosingWP))
			continue
		}
		wp, ok := plan[closing]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: abschluss_wp %s of %s is not a work package of %s", cov.File, closing, id, planNames()))
			continue
		}
		if !slices.ContainsFunc(wp.Rows, func(row Entry) bool { return row.ID == id }) {
			errs = append(errs, fmt.Errorf("%s: abschluss_wp %s of %s (%s) prescribes no traceability row of %s", cov.File, closing, id, wp.Location(), id))
		}
	}
	for id, tag := range catalog {
		if _, listed := cov.Tags[id]; HasPhase2Part(tag) && !listed {
			errs = append(errs, fmt.Errorf("%s with Phase-2 part %q is not listed in %s", id, tag, cov.File))
		}
	}
	for _, wp := range plan {
		if wp.Plan != Phase2PlanFile {
			continue
		}
		for _, row := range wp.Rows {
			at := fmt.Sprintf("%s:%d", wp.Plan, row.Line)
			tag, known := catalog[row.ID]
			switch {
			case !idPattern.MatchString(row.ID) || !known:
				errs = append(errs, fmt.Errorf("%s: unknown requirement ID %s", at, row.ID))
			case tag != "" && tag != row.Tag:
				errs = append(errs, fmt.Errorf("%s: tag of %s must be %q, got %q", at, row.ID, tag, row.Tag))
			}
			if row.Epic != wp.Epic {
				errs = append(errs, fmt.Errorf("%s: epic of %s must be %q, got %q", at, wp.ID, wp.Epic, row.Epic))
			}
			if row.WP != wp.ID {
				errs = append(errs, fmt.Errorf("%s: row of block %s names %s", at, wp.ID, row.WP))
			}
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
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
