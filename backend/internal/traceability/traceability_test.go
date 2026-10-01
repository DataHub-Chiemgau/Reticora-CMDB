package traceability

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root seen from this package directory.
const repoRoot = "../../.."

// Environment variables of the CI job "traceability" (E-30).
const (
	// WorkPackagesEnv lists the WPs of the pull request (e.g. "WP-005 WP-006");
	// every row the plan prescribes for them must be present.
	WorkPackagesEnv = "TRACEABILITY_WPS"
	// RequireCompleteEnv=1 makes missing [B]/[Pn]/[A] IDs blocking (gate G1).
	RequireCompleteEnv = "TRACEABILITY_REQUIRE_COMPLETE"
)

const testPlan = "## M0.0 Werkzeugkette\n\n" +
	"### WP-001 – Erstes Paket\n\n" +
	"**Schlüssel:** `a` · **Meilenstein:** M0.0 · **Epic (Traceability):** A · **Aufwand:** 1,0 PT\n\n" +
	"**Traceability (`docs/traceability.csv`):**\n\n" +
	"```text\n" +
	"GLO-11;[Q];A;backend/x_test.go;WP-001\n" +
	"DB-02;nicht geliefert;A;backend/x_test.go;WP-001\n" +
	"```\n\n" +
	"**Entscheidungsbedarf:** keiner.\n\n" +
	"### WP-002 – Zweites Paket\n\n" +
	"**Schlüssel:** `b` · **Meilenstein:** M0.0 · **Epic (Traceability):** B · **Aufwand:** 1,0 PT\n\n" +
	"**Traceability (`docs/traceability.csv`):**\n\n" +
	"```text\n" +
	"TEN-05;[B];B;backend/y_test.go;WP-002\n" +
	"```\n\n" +
	"## M0.1 Nächster Meilenstein\n\n" +
	"```text\n" +
	"IGNORED;x;x;x;x;x\n" +
	"```\n"

const testCatalogText = "### 2. Globale Regeln\n\n" +
	"GLO-01 bis GLO-03 unverändert.\n\n" +
	"GLO-11 [Q] Jede Anforderung ist rückführbar.\n\n" +
	"TEC-01 bis TEC-03, TEC-07, TEC-08 unverändert.\n\n" +
	"MET-10/MET-11: Felder von attribute_definition.\n\n" +
	"API-01 [B] /api/v1; OpenAPI deckt alle Endpunkte ab.\n\n" +
	"UI-01 bis UI-03 [B] wie v2.\n\n" +
	"CH8 (S) Betriebsmodell.\n\n" +
	"Text, der API-02 nur erwähnt.\n"

const testCoverage = "id;tag;status\n" +
	"TEN-05;[B];ABWEICHEND\n" +
	"DB-02;nicht geliefert;ABWEICHEND\n" +
	"ABN-03;\"[Q]; jeweilige Phase\";PARTIAL\n" +
	"API-01;[B];PARTIAL\n"

func testCatalog(t *testing.T) Catalog {
	t.Helper()
	c := Catalog{}
	if err := c.ParseCatalogText(strings.NewReader(testCatalogText)); err != nil {
		t.Fatalf("parse catalogue: %v", err)
	}
	if err := c.AddCoverage(strings.NewReader(testCoverage)); err != nil {
		t.Fatalf("parse coverage: %v", err)
	}
	return c
}

func testPlanPackages(t *testing.T) map[string]*WorkPackage {
	t.Helper()
	plan, err := ParsePlan(strings.NewReader(testPlan))
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	return plan
}

func existing(paths ...string) func(string) bool {
	return func(p string) bool {
		for _, q := range paths {
			if p == q {
				return true
			}
		}
		return false
	}
}

func TestParseCatalogTextExpandsIDListsAndTags(t *testing.T) {
	c := testCatalog(t)
	want := map[string]string{
		"GLO-01": "", "GLO-02": "", "GLO-03": "",
		"GLO-11": "[Q]",
		"TEC-01": "", "TEC-02": "", "TEC-03": "", "TEC-07": "", "TEC-08": "",
		"MET-10": "", "MET-11": "",
		"API-01": "[B]",
		"UI-01":  "[B]", "UI-02": "[B]", "UI-03": "[B]",
		"CH8":    "",
		"TEN-05": "[B]", "DB-02": "nicht geliefert", "ABN-03": "[Q], jeweilige Phase",
	}
	for id, tag := range want {
		got, ok := c[id]
		if !ok {
			t.Errorf("%s not parsed", id)
			continue
		}
		if got != tag {
			t.Errorf("%s: tag = %q, want %q", id, got, tag)
		}
	}
	if _, ok := c["API-02"]; ok {
		t.Error("API-02 is only mentioned in prose and must not be a known ID")
	}
	if len(c) != len(want) {
		t.Errorf("parsed %d IDs, want %d: %v", len(c), len(want), c)
	}
}

func TestRequiredSelectsBaselinePhaseAndAddOnTags(t *testing.T) {
	c := Catalog{
		"A-01": "[B]", "A-02": "[P3]", "A-03": "[A]", "A-04": "[Q]", "A-05": "[O]",
		"A-06": "[B], Teilumfang [P5]", "A-07": "[P2–P4, A]", "A-08": "B/G1", "A-09": "nicht geliefert",
		"A-10": "",
	}
	got := c.Required()
	want := []string{"A-01", "A-02", "A-03", "A-06", "A-07", "A-08"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Required() = %v, want %v", got, want)
	}
}

func TestParseCSVReportsFormatErrors(t *testing.T) {
	input := "id;tag;epic;wp\n" +
		"GLO-11;[Q];A;backend/x_test.go;WP-001\n" +
		"\n" +
		"GLO-11;[Q];A;backend/x_test.go\n" +
		"glo-11;[Q];A;backend/x_test.go;WP-001\n" +
		"GLO-11;;A;backend/x_test.go;WP-1\n" +
		"GLO-11;[Q]; A;backend/x_test.go;WP-001\n"
	entries, errs := ParseCSV(strings.NewReader(input))
	if len(entries) != 1 || entries[0].Line != 2 {
		t.Fatalf("expected only line 2 to be valid, got %+v", entries)
	}
	wantFragments := []string{
		"line 1: header must be",
		"line 3: empty line",
		"line 4: expected 5 fields",
		`line 5: id "glo-11" is not a requirement ID`,
		"line 6: tag is empty; wp \"WP-1\" must look like WP-nnn",
		"line 7: epic has surrounding whitespace",
	}
	assertErrors(t, errs, wantFragments)
}

func TestParseCSVRejectsEmptyFile(t *testing.T) {
	_, errs := ParseCSV(strings.NewReader(""))
	assertErrors(t, errs, []string{"file is empty"})
}

func TestValidateReportsUnknownIDsTagsEpicsAndMissingTestFiles(t *testing.T) {
	entries, errs := ParseCSV(strings.NewReader(Header + "\n" +
		"GLO-11;[Q];A;backend/x_test.go;WP-001\n" +
		"GLO-11;[Q];A;backend/x_test.go;WP-001\n" +
		"XYZ-99;[B];A;backend/x_test.go;WP-001\n" +
		"GLO-11;[B];A;backend/x_test.go;WP-001\n" +
		"GLO-11;[Q];B;backend/x_test.go;WP-001\n" +
		"GLO-11;[Q];A;backend/x_test.go;WP-999\n" +
		"GLO-11;[Q];A;backend/missing_test.go;WP-001\n" +
		"GLO-11;[Q];A;../outside_test.go;WP-001\n" +
		"MET-10;[B];A;backend/x_test.go;WP-001\n"))
	if len(errs) != 0 {
		t.Fatalf("unexpected format errors: %v", errs)
	}
	got := Validate(entries, testCatalog(t), testPlanPackages(t), existing("backend/x_test.go"))
	assertErrors(t, got, []string{
		"line 3: duplicate of line 2",
		"line 4: unknown requirement ID XYZ-99",
		`line 5: tag of GLO-11 must be "[Q]", got "[B]"`,
		`line 6: epic of WP-001 must be "A", got "B"`,
		"line 7: WP-999 is not a work package",
		`line 8: testdatei "backend/missing_test.go" does not exist`,
		`line 9: testdatei "../outside_test.go" must be a path relative`,
	})
}

func TestParsePlanReadsEpicAndRowsPerWorkPackage(t *testing.T) {
	plan := testPlanPackages(t)
	if len(plan) != 2 {
		t.Fatalf("expected 2 work packages, got %d", len(plan))
	}
	if plan["WP-001"].Epic != "A" || len(plan["WP-001"].Rows) != 2 {
		t.Fatalf("WP-001 = %+v", plan["WP-001"])
	}
	if plan["WP-002"].Epic != "B" || len(plan["WP-002"].Rows) != 1 || plan["WP-002"].Rows[0].ID != "TEN-05" {
		t.Fatalf("WP-002 = %+v", plan["WP-002"])
	}
}

func TestParsePlanRejectsBlockWithoutEpic(t *testing.T) {
	_, err := ParsePlan(strings.NewReader("### WP-001 – Ohne Epic\n\nText\n"))
	if err == nil || !strings.Contains(err.Error(), "WP-001 has no") {
		t.Fatalf("expected missing-epic error, got %v", err)
	}
}

func TestCheckWorkPackagesRequiresThePlannedRows(t *testing.T) {
	plan := testPlanPackages(t)
	entries := []Entry{
		// The test file may differ from the plan: rows name the file actually created.
		{ID: "GLO-11", Tag: "[Q]", Epic: "A", TestFile: "backend/other_test.go", WP: "WP-001"},
	}
	errs := CheckWorkPackages(entries, plan, []string{"WP-001", "WP-002", "WP-777"})
	assertErrors(t, errs, []string{
		"WP-001: missing traceability row DB-02;nicht geliefert;A;backend/x_test.go;WP-001",
		"WP-002: missing traceability row TEN-05;[B];B;backend/y_test.go;WP-002",
		"WP-777 is not a work package",
	})
}

func TestMissingListsRequiredIDsWithoutRow(t *testing.T) {
	entries := []Entry{{ID: "UI-02"}, {ID: "TEN-05"}}
	got := Missing(entries, testCatalog(t))
	want := []string{"API-01", "UI-01", "UI-03"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
}

func TestWorkPackagesIn(t *testing.T) {
	got := WorkPackagesIn("WP-005: Traceability; Arbeitspakete: WP-005, WP-006 und WP-1234")
	want := []string{"WP-005", "WP-006", "WP-123"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("WorkPackagesIn() = %v, want %v", got, want)
	}
}

// TestTraceabilityFile checks the real docs/traceability.csv against the
// catalogue, the plan and the repository tree. It runs in every backend test
// run; the CI job "traceability" additionally sets WorkPackagesEnv.
func TestTraceabilityFile(t *testing.T) {
	f, err := os.Open(filepath.Join(repoRoot, "docs", "traceability.csv"))
	if err != nil {
		t.Fatalf("open docs/traceability.csv: %v", err)
	}
	defer f.Close()
	entries, errs := ParseCSV(f)

	catalog := Catalog{}
	specFiles, err := filepath.Glob(filepath.Join(repoRoot, "docs", "spec", "katalog-v3", "*.md"))
	if err != nil || len(specFiles) == 0 {
		t.Fatalf("no catalogue files found: %v", err)
	}
	for _, name := range specFiles {
		readInto(t, name, catalog.ParseCatalogText)
	}
	readInto(t, filepath.Join(repoRoot, "docs", "plan", "abdeckung.csv"), catalog.AddCoverage)

	var plan map[string]*WorkPackage
	readInto(t, filepath.Join(repoRoot, "docs", "plan", "implementierungsplan.md"), func(r io.Reader) error {
		var err error
		plan, err = ParsePlan(r)
		return err
	})

	errs = append(errs, Validate(entries, catalog, plan, func(p string) bool {
		_, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(p)))
		return err == nil
	})...)

	wps := WorkPackagesIn(os.Getenv(WorkPackagesEnv))
	if len(wps) > 0 {
		t.Logf("checking the planned rows of %s", strings.Join(wps, ", "))
		errs = append(errs, CheckWorkPackages(entries, plan, wps)...)
	}

	required := catalog.Required()
	missing := Missing(entries, catalog)
	t.Logf("%d rows, %d known IDs; %d of %d [B]/[Pn]/[A] IDs traced, %d without a row",
		len(entries), len(catalog), len(required)-len(missing), len(required), len(missing))
	if len(missing) > 0 {
		t.Logf("IDs without a row: %s", strings.Join(missing, " "))
		if os.Getenv(RequireCompleteEnv) == "1" {
			errs = append(errs, &missingError{ids: missing})
		}
	}

	for _, err := range errs {
		t.Error(err)
	}
}

type missingError struct{ ids []string }

func (e *missingError) Error() string {
	return "required IDs without traceability row: " + strings.Join(e.ids, " ")
}

func readInto(t *testing.T, name string, parse func(io.Reader) error) {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	if err := parse(f); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

func assertErrors(t *testing.T, got []error, wantFragments []string) {
	t.Helper()
	var msgs []string
	for _, err := range got {
		msgs = append(msgs, err.Error())
	}
	sort.Strings(msgs)
	if len(msgs) != len(wantFragments) {
		t.Fatalf("got %d errors, want %d:\n%s", len(msgs), len(wantFragments), strings.Join(msgs, "\n"))
	}
	for _, frag := range wantFragments {
		found := false
		for _, m := range msgs {
			if strings.Contains(m, frag) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no error contains %q; got:\n%s", frag, strings.Join(msgs, "\n"))
		}
	}
}
