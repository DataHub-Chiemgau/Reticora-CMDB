package audit

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// recordedTypes match the resource types the domain recorders write: an
// entry literal and the metamodel recorder of internal/citype.
var recordedTypes = []*regexp.Regexp{
	regexp.MustCompile(`ResourceType:\s*"([^"]+)"`),
	regexp.MustCompile(`recordMetamodelAudit\(ctx, tx, [^,]+, [^,]+, "([^"]+)"`),
}

// TestEveryRecordedResourceTypeHasAScopeRule is the architecture test of
// the audit list scope for the domain recorders (TEN-06, AUD-05, WP-660,
// F-389): every resource type a recorder of the backend writes has a scope
// rule (scope.go), so a restricted principal sees its entries exactly when
// it may read the object. The route audit's types are checked over the
// route table (server.TestEveryAuditedRouteHasAScopeRule).
func TestEveryRecordedResourceTypeHasAScopeRule(t *testing.T) {
	found := map[string][]string{}
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, re := range recordedTypes {
				for _, m := range re.FindAllSubmatch(src, -1) {
					found[string(m[1])] = append(found[string(m[1])], path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	types := make([]string, 0, len(found))
	for rt := range found {
		types = append(types, rt)
	}
	sort.Strings(types)
	for _, rt := range types {
		if !HasScopeRule(rt) {
			t.Errorf("resource type %q (%s) has no scope rule in scope.go", rt, strings.Join(found[rt], ", "))
		}
	}
	for _, want := range []string{"ci", "asset", "location", "ci_type", "attribute_definition", "user", "audit_log"} {
		if found[want] == nil {
			t.Errorf("the scan finds no recorder of %q: %v", want, types)
		}
	}
}

// TestRestrictedResourceRule checks the generated condition: every type of
// the register is a branch, singular and plural types name their tables,
// tables whose policy does not filter client or site check the rows they
// refer to or the scope snapshot they carry, and an unknown type is hidden
// (fail-closed).
func TestRestrictedResourceRule(t *testing.T) {
	for rt, tables := range resourceTables {
		if n := strings.Count(restrictedResource, "'"+rt+"'"); n != 1 && len(tables) > 0 {
			t.Errorf("type %q appears %d times, want in one branch", rt, n)
		}
	}
	for _, want := range []string{
		"CASE\n\tWHEN audit_log.resource_type IN ('audit', 'audit-log', 'audit_log', 'ci-fields', 'notification', 'org', 'org_dek', " +
			"'organization', 'reconciliation', 'relationship-types', 'retention', 'search') THEN true",
		"WHEN audit_log.resource_type IN ('racks') THEN EXISTS (SELECT 1 FROM rack t WHERE t.id = audit_log.resource_id)\n",
		"WHEN audit_log.resource_type IN ('relationships') THEN EXISTS (SELECT 1 FROM ci_relationship t WHERE t.id = audit_log.resource_id)\n",
		"WHEN audit_log.resource_type IN ('asset', 'assets') THEN EXISTS (SELECT 1 FROM asset t WHERE t.id = audit_log.resource_id)\n",
		"WHEN audit_log.resource_type IN ('ticket', 'tickets') THEN EXISTS (SELECT 1 FROM ticket t WHERE t.id = audit_log.resource_id)\n",
		"WHEN audit_log.resource_type IN ('buildings') THEN EXISTS (SELECT 1 FROM building t WHERE t.id = audit_log.resource_id " +
			"AND (t.site_id IS NULL OR EXISTS (SELECT 1 FROM site p WHERE p.id = t.site_id)))\n",
		"WHEN audit_log.resource_type IN ('export', 'exports') THEN EXISTS (SELECT 1 FROM export_job t WHERE t.id = audit_log.resource_id " +
			"AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR (t.scope_clients IS NOT NULL",
		"\n\tELSE false END",
	} {
		if !strings.Contains(restrictedResource, want) {
			t.Errorf("condition lacks %q:\n%s", want, restrictedResource)
		}
	}
	for _, hidden := range []string{"'notifications'", "'privacy'", "'zz-new-kind'"} {
		if strings.Contains(restrictedResource, hidden) {
			t.Errorf("type %s passes the condition", hidden)
		}
	}
	if HasScopeRule("zz-new-kind") || !HasScopeRule("racks") || !HasScopeRule("organization") || !HasScopeRule("notifications") {
		t.Error("HasScopeRule does not follow the register")
	}
}
