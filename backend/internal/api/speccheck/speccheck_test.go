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
