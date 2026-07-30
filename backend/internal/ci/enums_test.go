package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	statusConstraintRe = regexp.MustCompile(`(?is)ci_status_check\s+CHECK\s*\(\s*status\s+IN\s*\(([^)]*)\)\s*\)`)
	sourceConstraintRe = regexp.MustCompile(`(?is)discovery_source\s+TEXT\s*CHECK\s*\(\s*discovery_source\s+IN\s*\(([^)]*)\)\s*\)`)
)

// migrationConstraintValues returns the values of the last CHECK constraint in
// migration order that matches the given expression, so later migrations that
// redefine a constraint win.
func migrationConstraintValues(t *testing.T, re *regexp.Regexp) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(files)

	var values []string
	for _, file := range files {
		content, err := os.ReadFile(file) //nolint:gosec // test-only read of repository files
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		matches := re.FindAllStringSubmatch(string(content), -1)
		if len(matches) == 0 {
			continue
		}
		last := matches[len(matches)-1][1]
		values = nil
		for _, raw := range strings.Split(last, ",") {
			value := strings.Trim(strings.TrimSpace(raw), "'")
			if value != "" {
				values = append(values, value)
			}
		}
	}
	if values == nil {
		t.Fatal("constraint not found in any migration")
	}
	return values
}

func assertSameSet(t *testing.T, name string, dbValues, goValues []string) {
	t.Helper()

	db := append([]string(nil), dbValues...)
	code := append([]string(nil), goValues...)
	sort.Strings(db)
	sort.Strings(code)

	if strings.Join(db, ",") != strings.Join(code, ",") {
		t.Errorf("%s drifted between database constraint and Go constants:\n  database: %v\n  Go:       %v", name, db, code)
	}
}

func TestStatusesMatchDatabaseConstraint(t *testing.T) {
	assertSameSet(t, "ci.status", migrationConstraintValues(t, statusConstraintRe), Statuses())
}

func TestDiscoverySourcesMatchDatabaseConstraint(t *testing.T) {
	assertSameSet(t, "ci.discovery_source", migrationConstraintValues(t, sourceConstraintRe), DiscoverySources())
}

func TestIsValidStatus(t *testing.T) {
	if !IsValidStatus(StatusActive) {
		t.Error("expected active to be a valid status")
	}
	if IsValidStatus("retired") {
		t.Error("expected retired to be an invalid status")
	}
}

func TestIsValidDiscoverySource(t *testing.T) {
	if !IsValidDiscoverySource(SourceSNMP) {
		t.Error("expected snmp to be a valid discovery source")
	}
	if IsValidDiscoverySource("carrier-pigeon") {
		t.Error("expected carrier-pigeon to be an invalid discovery source")
	}
}
