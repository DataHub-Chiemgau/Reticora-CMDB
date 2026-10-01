// Package traceability checks docs/traceability.csv (GLO-11, DOD-01).
//
// The file links every requirement ID of the catalogue (docs/spec/katalog-v3)
// to its epic, a test file and the work package that delivered it. The checks
// follow decision E-30: format, known IDs, existing test files and the rows the
// implementation plan prescribes for the work packages of a pull request are
// blocking; completeness over all [B]/[Pn]/[A] IDs is reported and only
// becomes blocking at gate G1.
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
	idPattern    = regexp.MustCompile(`^(?:[A-Z]{2,5}-\d{2}|CH\d+)$`)
	wpPattern    = regexp.MustCompile(`^WP-\d{3}$`)
	wpReferences = regexp.MustCompile(`WP-\d{3}`)
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

// WorkPackage is the traceability part of one WP block of the plan.
type WorkPackage struct {
	ID   string
	Epic string
	Rows []Entry
}

var (
	wpHeading = regexp.MustCompile(`^### (WP-\d{3}) `)
	epicField = regexp.MustCompile(`\*\*Epic \(Traceability\):\*\* ([^·]+?)\s*(?:·|$)`)
)

// ParsePlan reads docs/plan/implementierungsplan.md and returns, per WP, the
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
			errs = append(errs, fmt.Errorf("line %d: %s is not a work package of docs/plan/implementierungsplan.md", e.Line, e.WP))
		case wp.Epic != e.Epic:
			errs = append(errs, fmt.Errorf("line %d: epic of %s must be %q, got %q", e.Line, e.WP, wp.Epic, e.Epic))
		}

		switch {
		case strings.HasPrefix(e.TestFile, "/") || strings.Contains(e.TestFile, ".."):
			errs = append(errs, fmt.Errorf("line %d: testdatei %q must be a path relative to the repository root", e.Line, e.TestFile))
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
			errs = append(errs, fmt.Errorf("%s is not a work package of docs/plan/implementierungsplan.md", id))
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
	covered := map[string]bool{}
	for _, e := range entries {
		covered[e.ID] = true
	}
	var missing []string
	for _, id := range catalog.Required() {
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// WorkPackagesIn extracts the distinct WP numbers mentioned in s, in order.
func WorkPackagesIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, wp := range wpReferences.FindAllString(s, -1) {
		if !seen[wp] {
			seen[wp] = true
			out = append(out, wp)
		}
	}
	return out
}
