//go:build auditdefect

package traceability

// Phase-1 acceptance audit, group T5-install-ops-gate: DEFECT tests (fail
// until the traceability check is fixed).
//
// WP-005 (GLO-11, DOD-01) P2 "Traceability check accepts any existing path
// as 'testdatei'; the G1 gate passes only through 5 rows backed by a
// declaration": Validate (traceability.go, testdatei switch) checks only the
// path form and existence, so Markdown documents and directories pass.
// WP-213 (GATE-02, GLO-11) P2 "PASS-Status ohne Test-/Traceability-Nachweis
// (Erklärungen, selbstreferenzielle Traceability …)": AUT-05, AUT-06,
// GATE-04, NET-04 and NFR-08 are traced only by docs/acceptance/g1-gate.md, the gate
// checklist itself, and still count as traced for G1.
//
// Requirement (GLO-11 "Jede Anforderung ist rückführbar … mindestens eine
// Testart", WP-005 AC1): a row names an existing test; non-test evidence is
// accepted only where explicitly allowed, and an ID backed only by the gate
// checklist is not traced.
//
// Retest:
//   cd backend && go test -tags auditdefect -count=1 -v \
//     -run 'TestAuditT5ValidateRejectsNonTestEvidence|TestAuditT5GateIDsAreNotTracedByTheChecklistItself' ./internal/traceability/

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

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
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, errs := ParseCSV(strings.NewReader(Header + "\n" +
				"GLO-11;[Q];A;" + tc.testFile + ";WP-001\n"))
			if len(errs) != 0 {
				t.Fatalf("format errors: %v", errs)
			}
			got := Validate(entries, testCatalog(t), testPlanPackages(t),
				existing("backend/x_test.go", "docs/x.md", "docs/", "README.md"))
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
	const checklist = "docs/acceptance/g1-gate.md"
	f, err := os.Open(filepath.Join(repoRoot, "docs", "traceability.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, errs := ParseCSV(f)
	if len(errs) != 0 {
		t.Fatalf("format errors: %v", errs)
	}
	catalog := Catalog{}
	specFiles, err := filepath.Glob(filepath.Join(repoRoot, "docs", "spec", "katalog-v3", "*.md"))
	if err != nil || len(specFiles) == 0 {
		t.Fatalf("no catalogue files: %v", err)
	}
	for _, name := range specFiles {
		readInto(t, name, catalog.ParseCatalogText)
	}
	readInto(t, filepath.Join(repoRoot, "docs", "plan", "abdeckung.csv"), catalog.AddCoverage)

	evidence := map[string][]string{}
	for _, e := range entries {
		evidence[e.ID] = append(evidence[e.ID], e.TestFile)
	}
	var selfOnly []string
	for _, id := range catalog.RequiredForGate() {
		files, ok := evidence[id]
		if !ok {
			continue // reported by TestTraceabilityFile
		}
		other := false
		for _, p := range files {
			if p != checklist {
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
			len(selfOnly), checklist, strings.Join(selfOnly, " "))
	}
}
