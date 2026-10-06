package permission_test

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

const catalogueTextPath = "../../../docs/spec/katalog-v3/02-mandanten-auth-entitlements.md"

// permissionKey matches a key or the text's shorthand for several actions of
// one resource ("ci:read/write/delete").
var permissionKey = regexp.MustCompile(`\b([a-z_]+):([a-z_]+(?:/[a-z_]+)*)`)

// rba01Keys reads the permission keys of RBA-01 from the catalogue text.
func rba01Keys(t *testing.T) []string {
	t.Helper()
	file, err := os.Open(catalogueTextPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "RBA-01 ") {
			// "location:* ersetzt site:* aus v2" names wildcards, no keys.
			var keys []string
			for _, m := range permissionKey.FindAllStringSubmatch(line, -1) {
				for _, action := range strings.Split(m[2], "/") {
					keys = append(keys, m[1]+":"+action)
				}
			}
			return keys
		}
	}
	t.Fatal("RBA-01 not found in the catalogue text")
	return nil
}

func catalogueKeys() []string {
	keys := make([]string, 0, len(permission.Catalogue))
	for _, p := range permission.Catalogue {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return keys
}

// TestCatalogueHoldsTheRBA01Keys covers WP-068 (RBA-01): the Go catalogue
// and the identity vocabulary contain every RBA-01 key under its exact name;
// location:* replaces site:*, and the near-duplicates that RBA-01 keys
// replace are gone.
func TestCatalogueHoldsTheRBA01Keys(t *testing.T) {
	keys := rba01Keys(t)
	if len(keys) != 26 {
		t.Fatalf("RBA-01 lists %d keys, want 26: %v", len(keys), keys)
	}
	catalogue := catalogueKeys()
	identityKeys := map[string]bool{}
	for _, p := range identity.AllPermissions() {
		identityKeys[string(p)] = true
	}
	for _, k := range keys {
		if !slices.Contains(catalogue, k) {
			t.Errorf("RBA-01 key %s missing from the catalogue", k)
		}
		if !identityKeys[k] {
			t.Errorf("RBA-01 key %s missing from identity.AllPermissions", k)
		}
	}
	for _, k := range catalogue {
		if strings.HasPrefix(k, "site:") || k == "discovery:write" || k == "reconciliation:resolve" {
			t.Errorf("catalogue still holds the replaced key %s", k)
		}
	}
	seen := map[string]bool{}
	for _, k := range catalogue {
		if seen[k] {
			t.Errorf("catalogue lists %s twice", k)
		}
		seen[k] = true
	}
}

// TestSQLCatalogueEqualsGoCatalogue covers WP-068 (RBA-01): the permission
// table the migrations seed equals the Go catalogue exactly.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestSQLCatalogueEqualsGoCatalogue(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var sqlKeys []string
	if err = pool.QueryRow(ctx, `SELECT array_agg(key ORDER BY key) FROM permission`).Scan(&sqlKeys); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(sqlKeys, " "), strings.Join(catalogueKeys(), " "); got != want {
		t.Errorf("SQL catalogue differs from the Go catalogue:\n sql %s\n go  %s", got, want)
	}
}
