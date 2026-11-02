package traceability

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root seen from this package directory.
const repoRoot = "../../.."

// Environment variables of the CI job "traceability" (E-30).
const (
	// WorkPackagesEnv lists WPs to check by hand (e.g. "WP-005 WP-006" or
	// "WP-332 – WP-335"); every row the plan prescribes for them must be
	// present.
	WorkPackagesEnv = "TRACEABILITY_WPS"
	// PullRequestTitleEnv and PullRequestBodyEnv carry the title and the
	// description of the pull request (CI job "traceability"). The WPs in the
	// title and on the line "Arbeitspakete:" are checked like WorkPackagesEnv;
	// the texts are only read, never executed.
	PullRequestTitleEnv = "TRACEABILITY_PR_TITLE"
	PullRequestBodyEnv  = "TRACEABILITY_PR_BODY"
	// RequireCompleteEnv=1 also makes missing [Pn]/[A] IDs blocking; missing
	// [B]/[Q] IDs block since gate G1 regardless (WP-213).
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

const testRemediationPlan = "## K0 Prüfrahmen\n\n" +
	"### WP-336 – Korrektur\n\n" +
	"**Schlüssel:** `k` · **Meilenstein:** K0 · **Epic (Traceability):** A · **Aufwand:** 1,0 PT\n\n" +
	"**Traceability (`docs/traceability.csv`):**\n\n" +
	"```text\n" +
	"DOD-01;[Q];A;backend/x_test.go;WP-336\n" +
	"```\n"

func mergedTestPlans(t *testing.T) map[string]*WorkPackage {
	t.Helper()
	second, err := ParsePlan(strings.NewReader(testRemediationPlan))
	if err != nil {
		t.Fatalf("parse remediation plan: %v", err)
	}
	plan, err := MergePlans(map[string]map[string]*WorkPackage{
		"docs/plan/implementierungsplan.md":              testPlanPackages(t),
		"docs/plan/g1-korrektur/implementierungsplan.md": second,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return plan
}

func TestMergePlansCombinesPlansAndRemembersTheFile(t *testing.T) {
	plan := mergedTestPlans(t)
	if len(plan) != 3 {
		t.Fatalf("expected 3 work packages, got %d", len(plan))
	}
	if plan["WP-336"].Plan != "docs/plan/g1-korrektur/implementierungsplan.md" || plan["WP-001"].Plan != "docs/plan/implementierungsplan.md" {
		t.Fatalf("plan files: WP-336 %q, WP-001 %q", plan["WP-336"].Plan, plan["WP-001"].Plan)
	}
}

func TestMergePlansRejectsWorkPackageInTwoPlans(t *testing.T) {
	dup, err := ParsePlan(strings.NewReader(strings.ReplaceAll(testRemediationPlan, "WP-336", "WP-002")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = MergePlans(map[string]map[string]*WorkPackage{
		"docs/plan/implementierungsplan.md":              testPlanPackages(t),
		"docs/plan/g1-korrektur/implementierungsplan.md": dup,
	})
	if err == nil || !strings.Contains(err.Error(),
		"WP-002 is defined in docs/plan/g1-korrektur/implementierungsplan.md and in docs/plan/implementierungsplan.md") {
		t.Fatalf("expected duplicate error naming both plans, got %v", err)
	}
}

func TestCheckWorkPackagesRequiresRowsOfRemediationPlan(t *testing.T) {
	errs := CheckWorkPackages(nil, mergedTestPlans(t), []string{"WP-336", "WP-601"})
	assertErrors(t, errs, []string{
		"WP-336: missing traceability row DOD-01;[Q];A;backend/x_test.go;WP-336",
		"WP-601 is not a work package of docs/plan/implementierungsplan.md or docs/plan/g1-korrektur/implementierungsplan.md",
	})
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

func TestRequiredForGateSelectsBaselineAndQualityTags(t *testing.T) {
	c := Catalog{
		"A-01": "[B]", "A-02": "[P3]", "A-03": "[A]", "A-04": "[Q]", "A-05": "[O]",
		"A-06": "[B], Teilumfang [P5]", "A-07": "[P2–P4, A]", "A-08": "B/G1", "A-09": "nicht geliefert",
		"A-10": "", "A-11": "[Q], Bezug GA", "A-12": "[B–P4], [A]", "A-13": "B/G1, Teil P2/G2", "A-14": "P2/G2",
	}
	got := c.RequiredForGate()
	want := []string{"A-01", "A-04", "A-06", "A-08", "A-11", "A-12", "A-13"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("RequiredForGate() = %v, want %v", got, want)
	}
	if got := MissingForGate([]Entry{{ID: "A-01"}, {ID: "A-04"}}, c); strings.Join(got, ",") != "A-06,A-08,A-11,A-12,A-13" {
		t.Fatalf("MissingForGate() = %v", got)
	}
}

func TestWorkPackagesIn(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"WP-005: Traceability; Arbeitspakete: WP-005, WP-006 und WP-1234", []string{"WP-005", "WP-006", "WP-123"}},
		// F-286: ranges in PR titles and on the line "Arbeitspakete:".
		{"G1-Nachbesserung: WP-332 – WP-335 (NFR-02, NFR-03) (#64)", []string{"WP-332", "WP-333", "WP-334", "WP-335"}},
		{"Arbeitspakete: WP-010 bis WP-012", []string{"WP-010", "WP-011", "WP-012"}},
		{"Gate G1: WP-211 – WP-213 (#62)", []string{"WP-211", "WP-212", "WP-213"}},
		// Ranges of the remediation plan (WP-336 ff.).
		{"Korrektur: WP-336 – WP-338", []string{"WP-336", "WP-337", "WP-338"}},
		{"Rahmen WP-595—WP-597, WP-600", []string{"WP-595", "WP-596", "WP-597", "WP-600"}},
		{"WP-450 - WP-451 und WP-449", []string{"WP-450", "WP-451", "WP-449"}},
		// A descending range yields its ends; a dash without second WP or a
		// line break ends no range.
		{"WP-335 – WP-332", []string{"WP-335", "WP-332"}},
		{"WP-450 - Traceability", []string{"WP-450"}},
		{"WP-001\n– WP-003", []string{"WP-001", "WP-003"}},
		{"keine", nil},
	} {
		got := WorkPackagesIn(tc.in)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("WorkPackagesIn(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPullRequestWorkPackages(t *testing.T) {
	body := "<!--\nDie Zeile „Arbeitspakete:“ enthält nur WP-Nummern, z. B. WP-998.\n-->\n\n" +
		"**Arbeitspakete:** WP-332 – WP-334, WP-450 `$(touch /tmp/x)` \"WP-336\" $HOME ${{ github.token }}\r\n" +
		"## Zusammenfassung\n\nBezieht sich auf WP-999 und WP-213.\n" +
		"Arbeitspaket: WP-010 bis WP-011\n"
	got := PullRequestWorkPackages("WP-450: Traceability (t5-trace-evidence) `x` 'y' $Z", body)
	want := []string{"WP-450", "WP-332", "WP-333", "WP-334", "WP-336", "WP-010", "WP-011"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("PullRequestWorkPackages() = %v, want %v", got, want)
	}
	if got := PullRequestWorkPackages("Planung", "**Arbeitspakete:** keine\n"); len(got) != 0 {
		t.Fatalf("PullRequestWorkPackages() without WP = %v", got)
	}
}

func TestIsTestFile(t *testing.T) {
	for path, want := range map[string]bool{
		"backend/internal/traceability/traceability_test.go": true,
		"collector/collectorcmd/metrics_upload_test.go":      true,
		"frontend/src/components/CIList.test.tsx":            true,
		"frontend/src/api/client.test.ts":                    true,
		"frontend/e2e-real/stack-smoke.spec.ts":              true,
		"deploy/terraform/tests/plan.tftest.hcl":             true,
		"tests/rep01-structure.test.sh":                      true,
		"tests/audit/run-auditdefect.test.sh":                true,
		"tests/load/scenario.js":                             true,
		"tests/k8s/smoke.ts":                                 true,
		"README.md":                                          false,
		"docs/x.md":                                          false,
		"docs/":                                              false,
		"tests/load/":                                        false,
		"tests/audit/README.md":                              false,
		".github/workflows/ci.yml":                           false,
		ChecklistFile:                                        false,
		"backend/internal/traceability/traceability.go":      false,
		"scripts/install.sh":                                 false,
		"frontend/src/x.spec.tsx.md":                         false,
		"backend/_test.go/":                                  false,
	} {
		if got := IsTestFile(path); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestAuditT5ValidateRejectsNonTestEvidence and
// TestAuditT5GateIDsAreNotTracedByTheChecklistItself were the defect tests of
// the Phase-1 acceptance audit, group T5-install-ops-gate (F-121, WP-450):
//
// WP-005 (GLO-11, DOD-01) P2 "Traceability check accepts any existing path
// as 'testdatei'; the G1 gate passes only through 5 rows backed by a
// declaration": Validate checked only the path form and existence, so
// Markdown documents and directories passed. WP-213 (GATE-02, GLO-11) P2
// "PASS-Status ohne Test-/Traceability-Nachweis": AUT-05, AUT-06, GATE-04,
// NET-04 and NFR-08 were traced only by docs/acceptance/g1-gate.md, the gate
// checklist itself, and counted as traced for G1.
//
// Requirement (GLO-11 "Jede Anforderung ist rückführbar … mindestens eine
// Testart", WP-005 AC1): a row names an existing test; non-test evidence is
// accepted only where explicitly allowed, and an ID backed only by the gate
// checklist is not traced.
//
// Retest:
//
//	cd backend && go test -count=1 -v \
//	  -run 'TestAuditT5ValidateRejectsNonTestEvidence|TestAuditT5GateIDsAreNotTracedByTheChecklistItself|TestTraceabilityFile' ./internal/traceability/
func TestAuditT5ValidateRejectsNonTestEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		testFile string
		wantErr  bool
	}{
		{"test file (control)", "backend/x_test.go", false},
		{"markdown document", "docs/x.md", true},
		{"directory", "docs/", true},
		{"README", "README.md", true},
		{"gate checklist", ChecklistFile, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, errs := ParseCSV(strings.NewReader(Header + "\n" +
				"GLO-11;[Q];A;" + tc.testFile + ";WP-001\n"))
			if len(errs) != 0 {
				t.Fatalf("format errors: %v", errs)
			}
			got := Validate(entries, testCatalog(t), testPlanPackages(t),
				existing("backend/x_test.go", "docs/x.md", "docs/", "README.md", ChecklistFile))
			if tc.wantErr && len(got) == 0 {
				t.Errorf("Validate accepted testdatei %q (exists, but is no test and is not allow-listed); GLO-11 requires a test", tc.testFile)
			}
			if !tc.wantErr && len(got) != 0 {
				t.Errorf("Validate rejected the test file %q: %v", tc.testFile, got)
			}
		})
	}
}

func TestAuditT5GateIDsAreNotTracedByTheChecklistItself(t *testing.T) {
	repo := loadRepository(t)
	if len(repo.formatErrs) != 0 {
		t.Fatalf("format errors: %v", repo.formatErrs)
	}
	evidence := map[string][]string{}
	for _, e := range repo.entries {
		evidence[e.ID] = append(evidence[e.ID], e.TestFile)
	}
	var selfOnly []string
	for _, id := range repo.catalog.RequiredForGate() {
		files, ok := evidence[id]
		if !ok {
			continue // reported by TestTraceabilityFile
		}
		other := false
		for _, p := range files {
			if p != ChecklistFile {
				other = true
			}
		}
		if !other {
			selfOnly = append(selfOnly, id)
		}
	}
	sort.Strings(selfOnly)
	if len(selfOnly) > 0 {
		t.Errorf("%d [B]/[Q] IDs of gate G1 are traced only by the gate checklist %s (declaration, no test): %s",
			len(selfOnly), ChecklistFile, strings.Join(selfOnly, " "))
	}
}

func TestValidateAcceptsNonTestEvidenceOnlyFromTheAllowList(t *testing.T) {
	catalog := Catalog{"TST-04": "[Q]", "GLO-11": "[Q]", "AUT-05": "[B]", "OPS-06": "[Q]"}
	plan := map[string]*WorkPackage{
		"WP-001": {ID: "WP-001", Epic: "A"},
		"WP-005": {ID: "WP-005", Epic: "A"},
		"WP-104": {ID: "WP-104", Epic: "A"},
		"WP-213": {ID: "WP-213", Epic: "G1"},
	}
	entries, errs := ParseCSV(strings.NewReader(Header + "\n" +
		"TST-04;[Q];A;.github/workflows/ci.yml;WP-001\n" +
		"OPS-06;[Q];A;tests/outage/;WP-104\n" +
		"TST-04;[Q];A;.github/workflows/ci.yml;WP-005\n" +
		"GLO-11;[Q];A;.github/workflows/ci.yml;WP-001\n" +
		"AUT-05;[B];G1;docs/acceptance/g1-gate.md;WP-213\n" +
		"OPS-06;[Q];A;tests/load/;WP-104\n" +
		"GLO-11;[Q];A;tests/rep01-structure.test.sh;WP-005\n"))
	if len(errs) != 0 {
		t.Fatalf("format errors: %v", errs)
	}
	got := Validate(entries, catalog, plan, existing(".github/workflows/ci.yml", "tests/outage/", "tests/load/",
		ChecklistFile, "tests/rep01-structure.test.sh"))
	assertErrors(t, got, []string{
		`line 4: testdatei ".github/workflows/ci.yml" is no test file`,
		`line 5: testdatei ".github/workflows/ci.yml" is no test file`,
		"line 6: testdatei docs/acceptance/g1-gate.md is the gate checklist and no evidence of AUT-05",
		`line 7: testdatei "tests/load/" is no test file`,
	})
}

// TestPlannedRowsPassTheEvidenceRule requires that the allow-list holds
// exactly the planned non-test rows of docs/plan/implementierungsplan.md and
// that every row of the remediation plan, including the frame WPs WP-595 to
// WP-600, names a test file.
func TestPlannedRowsPassTheEvidenceRule(t *testing.T) {
	plan := loadRepository(t).plan
	planned := map[Evidence]string{}
	for _, wp := range plan {
		for _, row := range wp.Rows {
			if row.TestFile == ChecklistFile {
				t.Errorf("%s (%s line %d) names the gate checklist", row, wp.Plan, row.Line)
			}
			if !IsTestFile(row.TestFile) {
				planned[Evidence{ID: row.ID, WP: row.WP, Path: row.TestFile}] = wp.Plan
			}
		}
	}
	allowed := map[Evidence]bool{}
	for _, a := range AllowedEvidence {
		if allowed[a] {
			t.Errorf("allow-list entry %+v is listed twice", a)
		}
		allowed[a] = true
		if planned[a] != "docs/plan/implementierungsplan.md" {
			t.Errorf("allow-list entry %+v is no planned row of docs/plan/implementierungsplan.md", a)
		}
	}
	for row, file := range planned {
		if !allowed[row] {
			t.Errorf("planned row %+v of %s names no test file and is not allow-listed", row, file)
		}
	}
	if len(AllowedEvidence) != 21 {
		t.Errorf("allow-list has %d entries, the plan prescribes 21", len(AllowedEvidence))
	}
	for _, id := range []string{"WP-595", "WP-596", "WP-597", "WP-598", "WP-599", "WP-600"} {
		wp, ok := plan[id]
		if !ok || len(wp.Rows) == 0 {
			t.Errorf("%s has no planned rows", id)
			continue
		}
		for _, row := range wp.Rows {
			if !IsTestFile(row.TestFile) {
				t.Errorf("%s: planned row %s names no test file", id, row)
			}
		}
	}
}

// TestWorkPackageRangesResolveAgainstBothPlans checks the WP selection of the
// CI job against the real plans: WP-001..WP-007 and WP-336 have their rows,
// and a range of the remediation plan resolves to its WPs there.
func TestWorkPackageRangesResolveAgainstBothPlans(t *testing.T) {
	repo := loadRepository(t)
	if errs := CheckWorkPackages(repo.entries, repo.plan, WorkPackagesIn("WP-001 – WP-007, WP-336")); len(errs) != 0 {
		t.Errorf("planned rows missing: %v", errs)
	}
	wps := WorkPackagesIn("WP-595 – WP-597")
	if strings.Join(wps, ",") != "WP-595,WP-596,WP-597" {
		t.Fatalf("WorkPackagesIn(WP-595 – WP-597) = %v", wps)
	}
	for _, id := range wps {
		wp, ok := repo.plan[id]
		if !ok || wp.Plan != "docs/plan/g1-korrektur/implementierungsplan.md" {
			t.Errorf("%s is not resolved against the remediation plan: %+v", id, wp)
		}
	}
	// The frame WPs are not delivered yet: their rows are reported as missing,
	// never as unknown work packages.
	for _, err := range CheckWorkPackages(repo.entries, repo.plan, wps) {
		if !strings.Contains(err.Error(), ": missing traceability row ") {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

const testExceptions = ExceptionsHeader + "\n" +
	"API-01;erklaerung;Erklärung des Auftraggebers;E-76;A-02 (WP-455)\n" +
	"GLO-11;luecke;Nachweis fehlt;E-76;WP-450\n" +
	"TEN-05;np;Regel eines späteren Gates;E-03;keine\n"

func TestParseExceptionsReportsFormatErrors(t *testing.T) {
	exceptions, errs := ParseExceptions(strings.NewReader(testExceptions))
	if len(errs) != 0 || len(exceptions) != 3 {
		t.Fatalf("valid file: %d exceptions, errors %v", len(exceptions), errs)
	}
	if x := exceptions[1]; x.Line != 3 || x.ID != "GLO-11" || x.Kind != KindGap || x.Delivery != "WP-450" {
		t.Fatalf("exception = %+v", x)
	}

	_, errs = ParseExceptions(strings.NewReader("id;art;grund\n" +
		"API-01;bestanden;Test fehlt;E-76;A-02\n" +
		"\n" +
		"API-01;erklaerung;Erklärung;E-76\n" +
		"api-01;erklaerung;Erklärung;E-76;A-02\n" +
		"UI-01;erklaerung;;E-76;A-02\n" +
		"UI-02;erklaerung;Erklärung;E-76; A-02\n" +
		"UI-03;erklaerung;Erklärung;E-76;A-02\n" +
		"UI-03;np;GA;E-03;keine\n"))
	assertErrors(t, errs, []string{
		"g1-ausnahmen.csv line 1: header must be",
		`g1-ausnahmen.csv line 2: art "bestanden" must be erklaerung, luecke or np`,
		"g1-ausnahmen.csv line 3: empty line",
		"g1-ausnahmen.csv line 4: expected 5 fields",
		`g1-ausnahmen.csv line 5: id "api-01" is not a requirement ID`,
		"g1-ausnahmen.csv line 6: grund is empty",
		"g1-ausnahmen.csv line 7: lieferung has surrounding whitespace",
		"g1-ausnahmen.csv line 9: UI-03 is already listed in line 8",
	})
	_, errs = ParseExceptions(strings.NewReader(""))
	assertErrors(t, errs, []string{"g1-ausnahmen.csv is empty"})
}

func TestValidateExceptionsRequiresGateIDs(t *testing.T) {
	exceptions, errs := ParseExceptions(strings.NewReader(ExceptionsHeader + "\n" +
		"API-01;erklaerung;Erklärung;E-76;A-02\n" +
		"XYZ-99;erklaerung;Erklärung;E-76;A-02\n" +
		"DB-02;luecke;nicht geliefert;E-76;WP-450\n"))
	if len(errs) != 0 {
		t.Fatalf("format errors: %v", errs)
	}
	assertErrors(t, ValidateExceptions(exceptions, testCatalog(t)), []string{
		"g1-ausnahmen.csv line 3: unknown requirement ID XYZ-99",
		`g1-ausnahmen.csv line 4: DB-02 is tagged "nicht geliefert", not a [B]/[Q] ID of gate G1`,
	})
}

func TestNewGateReportSeparatesExceptions(t *testing.T) {
	catalog := testCatalog(t)
	exceptions, errs := ParseExceptions(strings.NewReader(testExceptions))
	if len(errs) != 0 {
		t.Fatalf("format errors: %v", errs)
	}
	// Rows for GLO-11 (exception, traced) and UI-01; none for API-01
	// (exception), TEN-05 (exception), UI-02, UI-03 and ABN-03.
	entries := []Entry{{ID: "GLO-11"}, {ID: "UI-01"}}
	r := NewGateReport(catalog.RequiredForGate(), MissingForGate(entries, catalog), exceptions)
	for name, c := range map[string]struct{ got, want []string }{
		"Traced":            {r.Traced, []string{"GLO-11", "UI-01"}},
		"Exceptions":        {r.Exceptions, []string{"API-01", "GLO-11", "TEN-05"}},
		"TracedNotAccepted": {r.TracedNotAccepted, []string{"GLO-11"}},
		"Blocking":          {r.Blocking, []string{"ABN-03", "UI-02", "UI-03"}},
	} {
		if strings.Join(c.got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
	if got, want := r.Summary(), "gate G1: 2 of 7 traced, 3 exceptions (1 traced, not accepted)"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}

	// Without the exception file every untraced ID blocks; an exception
	// never turns an untraced ID into a traced one.
	r = NewGateReport(catalog.RequiredForGate(), MissingForGate(entries, catalog), nil)
	if strings.Join(r.Blocking, ",") != "ABN-03,API-01,TEN-05,UI-02,UI-03" || len(r.Traced) != 2 {
		t.Errorf("without exceptions: blocking %v, traced %v", r.Blocking, r.Traced)
	}
}

// TestTraceabilityFile checks the real docs/traceability.csv and
// docs/acceptance/g1-ausnahmen.csv against the catalogue, both plans and the
// repository tree. It runs in every backend test run; the CI job
// "traceability" additionally passes the pull request (PullRequestTitleEnv,
// PullRequestBodyEnv).
func TestTraceabilityFile(t *testing.T) {
	repo := loadRepository(t)
	entries, catalog, plan := repo.entries, repo.catalog, repo.plan
	errs := append([]error(nil), repo.formatErrs...)

	errs = append(errs, Validate(entries, catalog, plan, repoPathExists)...)

	f, err := os.Open(filepath.Join(repoRoot, filepath.FromSlash(ExceptionsFile)))
	if err != nil {
		t.Fatalf("open %s: %v", ExceptionsFile, err)
	}
	defer f.Close()
	exceptions, exceptionErrs := ParseExceptions(f)
	errs = append(errs, exceptionErrs...)
	errs = append(errs, ValidateExceptions(exceptions, catalog)...)

	wps := WorkPackagesIn(os.Getenv(WorkPackagesEnv))
	for _, wp := range PullRequestWorkPackages(os.Getenv(PullRequestTitleEnv), os.Getenv(PullRequestBodyEnv)) {
		if !slices.Contains(wps, wp) {
			wps = append(wps, wp)
		}
	}
	if len(wps) > 0 {
		t.Logf("checking the planned rows of %s", strings.Join(wps, ", "))
		errs = append(errs, CheckWorkPackages(entries, plan, wps)...)
	} else if os.Getenv(PullRequestTitleEnv) != "" {
		t.Log("the pull request names no work package")
	}

	gate := NewGateReport(catalog.RequiredForGate(), MissingForGate(entries, catalog), exceptions)
	t.Log(gate.Summary())
	var untracedExceptions []string
	for _, id := range gate.Exceptions {
		if !slices.Contains(gate.Traced, id) {
			untracedExceptions = append(untracedExceptions, id)
		}
	}
	t.Logf("exceptions without a row (not traced): %s", strings.Join(untracedExceptions, " "))
	t.Logf("exceptions with a row (traced, not accepted): %s", strings.Join(gate.TracedNotAccepted, " "))
	if len(gate.Blocking) > 0 {
		errs = append(errs, &missingError{what: "[B]/[Q] IDs without traceability row and without exception in " + ExceptionsFile, ids: gate.Blocking})
	}
	required := catalog.Required()
	missing := Missing(entries, catalog)
	t.Logf("%d rows, %d known IDs; %d of %d [B]/[Pn]/[A] IDs traced, %d without a row",
		len(entries), len(catalog), len(required)-len(missing), len(required), len(missing))
	if len(missing) > 0 {
		t.Logf("IDs without a row: %s", strings.Join(missing, " "))
		if os.Getenv(RequireCompleteEnv) == "1" {
			errs = append(errs, &missingError{what: "required IDs without traceability row", ids: missing})
		}
	}

	for _, err := range errs {
		t.Error(err)
	}
}

type missingError struct {
	what string
	ids  []string
}

func (e *missingError) Error() string {
	return e.what + ": " + strings.Join(e.ids, " ")
}

// repository is the real traceability file, catalogue and plans.
type repository struct {
	entries    []Entry
	formatErrs []error
	catalog    Catalog
	plan       map[string]*WorkPackage
}

func loadRepository(t *testing.T) repository {
	t.Helper()
	var repo repository
	readInto(t, filepath.Join(repoRoot, "docs", "traceability.csv"), func(r io.Reader) error {
		repo.entries, repo.formatErrs = ParseCSV(r)
		return nil
	})

	repo.catalog = Catalog{}
	specFiles, err := filepath.Glob(filepath.Join(repoRoot, "docs", "spec", "katalog-v3", "*.md"))
	if err != nil || len(specFiles) == 0 {
		t.Fatalf("no catalogue files found: %v", err)
	}
	for _, name := range specFiles {
		readInto(t, name, repo.catalog.ParseCatalogText)
	}
	readInto(t, filepath.Join(repoRoot, "docs", "plan", "abdeckung.csv"), repo.catalog.AddCoverage)

	plans := map[string]map[string]*WorkPackage{}
	for _, name := range PlanFiles {
		readInto(t, filepath.Join(repoRoot, filepath.FromSlash(name)), func(r io.Reader) error {
			p, perr := ParsePlan(r)
			plans[name] = p
			return perr
		})
	}
	repo.plan, err = MergePlans(plans)
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	return repo
}

// repoPathExists reports whether a repository-relative path exists as named:
// a directory only with a trailing slash, otherwise a file.
func repoPathExists(p string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(p)))
	return err == nil && info.IsDir() == strings.HasSuffix(p, "/")
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
