package tenant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepositoriesUseCanonicalSessionVariable guards the invariant that migration
// 000023 established: every RLS policy reads `app.org_id`, so every repository
// must set exactly that variable. A repository that sets one of the retired
// names (`app.organization_id`, `app.current_org`) silently loses tenant
// isolation or fails at runtime with "unrecognized configuration parameter".
func TestRepositoriesUseCanonicalSessionVariable(t *testing.T) {
	retired := []string{"app.organization_id", "app.current_org"}

	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		content, err := os.ReadFile(path) //nolint:gosec // test-only read of repository files
		if err != nil {
			return err
		}
		for _, name := range retired {
			if strings.Contains(string(content), "set_config('"+name+"'") {
				t.Errorf("%s sets the retired RLS session variable %q; use app.org_id", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk backend sources: %v", err)
	}
}
