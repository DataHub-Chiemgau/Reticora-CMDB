//go:build auditdefect

// Defect test of a finding from the implementation of K4 (WP-502, F-358,
// audit/g1-reaudit/FINDINGS.md): POST /api/v1/discovery/jobs answers 201 and
// stores a discovery job as pending (in the generic job model: queued), but
// nothing executes it. No collector, worker or edge handler reads pending
// discovery jobs, and PGRepository.UpdateJobStatus, the only path that moves
// a job on, has no caller outside tests: a job created through the API stays
// pending (queued) forever.
//
// The test reads the production code of the backend and the collector (Go
// files without _test) and requires that the API either does not offer the
// creation of discovery jobs (operation createDiscoveryJob removed from
// api/openapi.yaml) or that production code moves a job on: a call of
// UpdateJobStatus or an UPDATE of discovery_job outside UpdateJobStatus
// itself.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditK4DiscoveryJobIsExecuted' ./internal/discovery/
package discovery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditK4DiscoveryJobIsExecuted(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	if !strings.Contains(string(spec), "operationId: createDiscoveryJob") {
		return // the API no longer creates discovery jobs
	}

	var movers []string
	for _, root := range []string{filepath.Join("..", ".."), filepath.Join("..", "..", "..", "collector")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "vendor", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name == "UpdateJobStatus" {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.SelectorExpr:
						if n.Sel.Name == "UpdateJobStatus" {
							movers = append(movers, fset.Position(n.Pos()).String()+" "+fn.Name.Name)
						}
					case *ast.BasicLit:
						if n.Kind == token.STRING && strings.Contains(strings.ToLower(n.Value), "update discovery_job") {
							movers = append(movers, fset.Position(n.Pos()).String()+" "+fn.Name.Name)
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(movers) == 0 {
		t.Error("POST /api/v1/discovery/jobs creates pending discovery jobs, but no production code of the backend or the collector " +
			"moves a discovery job on (no call of UpdateJobStatus, no UPDATE of discovery_job): a job created through the API " +
			"stays pending (queued in the job center) forever; want an execution path (collector or worker) or no create operation")
	}
}
