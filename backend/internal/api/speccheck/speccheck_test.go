package speccheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesOperations(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	content := `openapi: 3.1.0
info:
  title: Example
paths:
  /api/v1/things:
    get:
      operationId: listThings
      responses:
        '200':
          description: ok
    post:
      operationId: createThing
  /api/v1/things/{id}:
    parameters:
      - $ref: '#/components/parameters/ResourceID'
    delete:
      operationId: deleteThing
components:
  schemas:
    Thing:
      type: object
      properties:
        get:
          type: string
`
	if err := os.WriteFile(spec, []byte(content), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	ops, err := Load(spec)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := []Operation{
		{Method: "GET", Path: "/api/v1/things"},
		{Method: "POST", Path: "/api/v1/things"},
		{Method: "DELETE", Path: "/api/v1/things/{id}"},
	}
	if len(ops) != len(want) {
		t.Fatalf("got %d operations (%v), want %d", len(ops), ops, len(want))
	}
	for _, expected := range want {
		found := false
		for _, op := range ops {
			if op == expected {
				found = true
			}
		}
		if !found {
			t.Errorf("missing operation %s", expected)
		}
	}
}

func TestDiff(t *testing.T) {
	left := []Operation{{Method: "GET", Path: "/a"}, {Method: "POST", Path: "/b"}}
	right := []Operation{{Method: "GET", Path: "/a"}, {Method: "DELETE", Path: "/c"}}

	onlyLeft, onlyRight := Diff(left, right)
	if len(onlyLeft) != 1 || onlyLeft[0].Path != "/b" {
		t.Errorf("onlyLeft: %v", onlyLeft)
	}
	if len(onlyRight) != 1 || onlyRight[0].Path != "/c" {
		t.Errorf("onlyRight: %v", onlyRight)
	}
}

func TestPhasesReadsPathExtensions(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "openapi.yaml")
	content := `openapi: 3.1.0
paths:
  /api/v1/things:
    get:
      operationId: listThings
  /api/v1/tickets:
    x-phase: P2
    get:
      operationId: listTickets
components:
  schemas:
    Thing:
      x-phase: P9
`
	if err := os.WriteFile(spec, []byte(content), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	phases, err := Phases(spec)
	if err != nil {
		t.Fatalf("phases: %v", err)
	}
	if len(phases) != 1 || phases["/api/v1/tickets"] != "P2" {
		t.Fatalf("phases = %v, want only /api/v1/tickets: P2", phases)
	}
}

// TestFeaturesReadsPathAndOperationLevel covers WP-706 (API-06, E-38):
// x-feature on the path applies to each of its operations, an x-feature on
// an operation wins for that operation, and x-feature elsewhere (schemas,
// parameters) is not read. Phases ignores x-feature and keeps reading only
// the path level.
func TestFeaturesReadsPathAndOperationLevel(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "openapi.yaml")
	content := `openapi: 3.1.0
paths:
  /api/v1/things:
    get:
      operationId: listThings
  /api/v1/stock-movements:
    x-phase: P2
    x-feature: assets_basic
    get:
      operationId: listMovements
      x-phase: P9
    post:
      operationId: createMovement
      x-feature: stock
      requestBody:
        content:
          application/json:
            schema:
              x-feature: nested
  /api/v1/documents:
    get:
      operationId: listDocuments
    x-feature: documents
    x-phase: P2
components:
  schemas:
    Thing:
      x-feature: P9
`
	if err := os.WriteFile(spec, []byte(content), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	features, err := Features(spec)
	if err != nil {
		t.Fatalf("features: %v", err)
	}
	want := map[Operation]string{
		{Method: "GET", Path: "/api/v1/stock-movements"}:  "assets_basic",
		{Method: "POST", Path: "/api/v1/stock-movements"}: "stock",
		{Method: "GET", Path: "/api/v1/documents"}:        "documents",
	}
	if len(features) != len(want) {
		t.Fatalf("features = %v, want %v", features, want)
	}
	for op, feature := range want {
		if features[op] != feature {
			t.Errorf("%s: x-feature %q, want %q", op, features[op], feature)
		}
	}

	phases, err := Phases(spec)
	if err != nil {
		t.Fatalf("phases: %v", err)
	}
	if len(phases) != 2 || phases["/api/v1/stock-movements"] != "P2" || phases["/api/v1/documents"] != "P2" {
		t.Fatalf("phases = %v, want stock-movements and documents with P2", phases)
	}
}

// TestPlannedReadsPathAndOperationLevel covers the scaffold of E-38
// (WP-247): x-status: planned on the path level marks every operation of
// the path, on the operation level only that one; x-status deeper down is
// not read, and Load still lists every operation. Any other value of
// x-status is an error.
func TestPlannedReadsPathAndOperationLevel(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "openapi.yaml")
	content := `openapi: 3.1.0
paths:
  /api/v1/things:
    get:
      operationId: listThings
  /api/v1/tags:
    x-status: planned
    get:
      operationId: listTags
    post:
      operationId: createTag
  /api/v1/documents/{id}/versions:
    get:
      operationId: listVersions
    post:
      operationId: createVersion
      x-status: 'planned'
      requestBody:
        content:
          application/json:
            schema:
              x-status: nested
components:
  schemas:
    Thing:
      x-status: other
`
	if err := os.WriteFile(spec, []byte(content), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	planned, err := Planned(spec)
	if err != nil {
		t.Fatalf("planned: %v", err)
	}
	want := map[Operation]bool{
		{Method: "GET", Path: "/api/v1/tags"}:                     true,
		{Method: "POST", Path: "/api/v1/tags"}:                    true,
		{Method: "POST", Path: "/api/v1/documents/{id}/versions"}: true,
	}
	if len(planned) != len(want) {
		t.Fatalf("planned = %v, want %v", planned, want)
	}
	for op := range want {
		if !planned[op] {
			t.Errorf("%s is not planned", op)
		}
	}
	ops, err := Load(spec)
	if err != nil || len(ops) != 5 {
		t.Fatalf("load = %v, %v; want 5 operations", ops, err)
	}

	for name, doc := range map[string]string{
		"path level":      "paths:\n  /a:\n    x-status: done\n    get:\n      operationId: a\n",
		"operation level": "paths:\n  /a:\n    get:\n      x-status: deprecated\n",
		"empty":           "paths:\n  /a:\n    get:\n      x-status:\n",
	} {
		bad := filepath.Join(t.TempDir(), "bad.yaml")
		if werr := os.WriteFile(bad, []byte(doc), 0o600); werr != nil {
			t.Fatal(werr)
		}
		if _, perr := Planned(bad); perr == nil {
			t.Errorf("%s: an x-status other than planned is accepted", name)
		}
	}
}
