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
