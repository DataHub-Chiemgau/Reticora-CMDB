package readonly

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The fixtures of the client against the schemas of the ADMP documentation
// 9.14.0 (ADM-02, E-255, WP-1077). The documentation has no examples, the
// fixtures are built from its schemas and checked here against them with a
// checker of exactly the keywords the documentation uses; backend/go.mod
// has no JSON Schema library and gets none (E-255).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestFixturesMatchSpecSchemas|TestSpecSchemaCheckerDetectsViolations' ./internal/admp/readonly/

// specCopy is the unchanged copy of docs/plan/phase3/ADMP-API.
const specCopy = "testdata/admp-api-9.14.0.json"

// Keywords of the schemas of the documentation: the validating ones the
// checker implements, the annotations it ignores. Any other keyword fails
// the check, so a new documentation version with e.g. minimum is noticed.
var (
	specValidationKeywords = map[string]bool{"type": true, "required": true, "properties": true, "additionalProperties": true,
		"items": true, "enum": true, "anyOf": true, "$ref": true}
	specAnnotations = map[string]bool{"description": true, "title": true, "example": true, "contentMediaType": true}
)

// specChecker validates JSON values against the schemas of a document.
type specChecker struct {
	doc map[string]any
}

// loadSpec reads the copy of the documentation.
func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(specCopy)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// componentSchemas returns components.schemas of the document.
func (c *specChecker) componentSchemas() map[string]any {
	components, _ := c.doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	return schemas
}

// resolve returns the target of a $ref: #/components/schemas/<name> (the
// name may hold a slash, as in the broken reference
// #/components/schemas//schemas/object that names the schema
// "/schemas/object") or #/$defs/<name>; a name not among the component
// schemas is looked up in the embedded $defs, innermost first.
func (c *specChecker) resolve(ref string, defs []map[string]any) (map[string]any, error) {
	lookupDefs := func(name string) (map[string]any, bool) {
		for i := len(defs) - 1; i >= 0; i-- {
			if s, ok := defs[i][name].(map[string]any); ok {
				return s, true
			}
		}
		return nil, false
	}
	if name, ok := strings.CutPrefix(ref, "#/components/schemas/"); ok {
		if s, found := c.componentSchemas()[name].(map[string]any); found {
			return s, nil
		}
		if s, found := lookupDefs(name); found {
			return s, nil
		}
	}
	if name, ok := strings.CutPrefix(ref, "#/$defs/"); ok {
		if s, found := lookupDefs(name); found {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unresolvable $ref %q", ref)
}

// validate returns the violations of v against schema at path.
func (c *specChecker) validate(schema any, v any, path string, defs []map[string]any) []string {
	s, ok := schema.(map[string]any)
	if !ok {
		return []string{path + ": schema is no object"}
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !specValidationKeywords[k] && !specAnnotations[k] && k != "$defs" {
			return []string{fmt.Sprintf("%s: unknown schema keyword %q", path, k)}
		}
	}
	if d, has := s["$defs"].(map[string]any); has {
		defs = append(append([]map[string]any(nil), defs...), d)
	}
	var out []string
	if ref, has := s["$ref"].(string); has {
		target, err := c.resolve(ref, defs)
		if err != nil {
			return []string{path + ": " + err.Error()}
		}
		out = append(out, c.validate(target, v, path, defs)...)
	}
	if typ, has := s["type"]; has {
		if !matchesType(typ, v) {
			out = append(out, fmt.Sprintf("%s: %s is not of type %v", path, kindOf(v), typ))
		}
	}
	if enum, has := s["enum"].([]any); has {
		found := false
		for _, e := range enum {
			found = found || reflect.DeepEqual(e, v)
		}
		if !found {
			out = append(out, fmt.Sprintf("%s: %v not in enum %v", path, v, enum))
		}
	}
	if anyOf, has := s["anyOf"].([]any); has {
		matched := false
		for _, alt := range anyOf {
			if len(c.validate(alt, v, path, defs)) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, path+": no anyOf alternative matches")
		}
	}
	if obj, isObject := v.(map[string]any); isObject {
		out = append(out, c.validateObject(s, obj, path, defs)...)
	}
	if list, isArray := v.([]any); isArray {
		if items, has := s["items"]; has {
			for i, item := range list {
				out = append(out, c.validate(items, item, path+"["+strconv.Itoa(i)+"]", defs)...)
			}
		}
	}
	return out
}

// validateObject checks required, properties and additionalProperties.
func (c *specChecker) validateObject(s, obj map[string]any, path string, defs []map[string]any) []string {
	var out []string
	if required, has := s["required"].([]any); has {
		for _, r := range required {
			if name, _ := r.(string); name != "" {
				if _, present := obj[name]; !present {
					out = append(out, fmt.Sprintf("%s: required member %q missing", path, name))
				}
			}
		}
	}
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		if p, known := props[name]; known {
			out = append(out, c.validate(p, obj[name], path+"."+name, defs)...)
			continue
		}
		switch extra := s["additionalProperties"].(type) {
		case bool:
			if !extra {
				out = append(out, fmt.Sprintf("%s: member %q not allowed (additionalProperties: false)", path, name))
			}
		case map[string]any:
			out = append(out, c.validate(extra, obj[name], path+"."+name, defs)...)
		}
	}
	return out
}

// matchesType reports whether v is of the type (a name or a list).
func matchesType(typ, v any) bool {
	if list, ok := typ.([]any); ok {
		for _, t := range list {
			if matchesType(t, v) {
				return true
			}
		}
		return false
	}
	switch typ {
	case "null":
		return v == nil
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f) && !math.IsInf(f, 0)
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return false
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

// specResponses maps every operationId of the document to its responses.
func specResponses(doc map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	paths, _ := doc["paths"].(map[string]any)
	for _, item := range paths {
		ops, _ := item.(map[string]any)
		for _, op := range ops {
			o, _ := op.(map[string]any)
			id, _ := o["operationId"].(string)
			if id == "" {
				continue
			}
			responses, _ := o["responses"].(map[string]any)
			out[id] = responses
		}
	}
	return out
}

// assumedSchemas are the forms assumed for the answers without schema in
// the documentation (A-86, WP-1073): the inventory as AccessNode[], the
// services of a port as an array of numbers.
var assumedSchemas = map[string]map[string]any{
	"getV1InfrastructureAccessnodepicker":     {"type": "array", "items": map[string]any{"$ref": "#/components/schemas/AccessNode"}},
	"getV1InfrastructurePort:portIdDienstIds": {"type": "array", "items": map[string]any{"type": "number"}},
}

// responseSchema returns the JSON schema of the answer status of op, nil
// when the documentation has none.
func responseSchema(responses map[string]any, status int) any {
	r, _ := responses[strconv.Itoa(status)].(map[string]any)
	content, _ := r["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	return media["schema"]
}

// rawFixtures reads admp_fixtures.json with the members of every entry.
func rawFixtures(t *testing.T) map[string]map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/admp_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]json.RawMessage{}
	for key, v := range all {
		if strings.HasPrefix(key, "_") {
			continue
		}
		var entry map[string]json.RawMessage
		if err = json.Unmarshal(v, &entry); err != nil {
			t.Fatalf("fixture %s: %v", key, err)
		}
		out[key] = entry
	}
	return out
}

// TestFixturesMatchSpecSchemas: every entry of admp_fixtures.json names an
// operationId of the documentation ("<operationId>" or
// "<operationId>#<id>") and its body validates against the answer schema
// of that operation and status. The two answers without schema carry
// "assumed": "A-86" and validate against the assumed form; an entry with
// a documented schema carries no such mark.
func TestFixturesMatchSpecSchemas(t *testing.T) {
	doc := loadSpec(t)
	c := &specChecker{doc: doc}
	responses := specResponses(doc)
	fixtures := rawFixtures(t)
	if len(fixtures) == 0 {
		t.Fatal("no fixtures")
	}
	checked := 0
	for key, entry := range fixtures {
		op, _, _ := strings.Cut(key, "#")
		resp, known := responses[op]
		if !known {
			t.Errorf("fixture %s: operationId %s is not in the documentation", key, op)
			continue
		}
		var status int
		var body any
		var assumed string
		if err := json.Unmarshal(entry["status"], &status); err != nil {
			t.Errorf("fixture %s: status: %v", key, err)
			continue
		}
		if err := json.Unmarshal(entry["body"], &body); err != nil {
			t.Errorf("fixture %s: body: %v", key, err)
			continue
		}
		if raw, has := entry["assumed"]; has {
			_ = json.Unmarshal(raw, &assumed)
		}
		schema := responseSchema(resp, status)
		switch {
		case schema == nil && assumedSchemas[op] != nil:
			if assumed != "A-86" {
				t.Errorf("fixture %s: the documentation has no schema, want \"assumed\": \"A-86\", got %q", key, assumed)
			}
			schema = assumedSchemas[op]
		case schema == nil:
			t.Errorf("fixture %s: the documentation has no schema for status %d", key, status)
			continue
		case assumed != "":
			t.Errorf("fixture %s: marked assumed although the documentation has a schema", key)
		}
		if problems := c.validate(schema, body, key, nil); len(problems) != 0 {
			t.Errorf("fixture %s does not match the documentation:\n  %s", key, strings.Join(problems, "\n  "))
		}
		checked++
	}
	for op := range assumedSchemas {
		if _, has := fixtures[op]; !has {
			t.Errorf("no fixture of %s", op)
		}
	}
	t.Logf("%d fixtures checked against the documentation", checked)
}

// TestSpecSchemaCheckerDetectsViolations: the checker fails a fixture with
// an unknown member under additionalProperties: false, a wrong type, a
// missing required member and a schema with an unknown validation keyword
// (minimum); a schema with title and description ($defs.Int) passes, and
// the broken reference to "/schemas/object" resolves.
func TestSpecSchemaCheckerDetectsViolations(t *testing.T) {
	c := &specChecker{doc: loadSpec(t)}
	user := map[string]any{"id": 42.0, "organizationId": 7.0, "ldapDN": "dn", "ldapCN": "cn", "ldapEmail": "e", "ldapUsername": "u", "anwUser": false}
	ref := map[string]any{"$ref": "#/components/schemas/AuthUser"}
	if p := c.validate(ref, user, "AuthUser", nil); len(p) != 0 {
		t.Fatalf("a valid AuthUser fails: %v", p)
	}
	extra := map[string]any{"unknownMember": 1.0}
	for k, v := range user {
		extra[k] = v
	}
	if p := c.validate(ref, extra, "AuthUser", nil); len(p) == 0 {
		t.Error("an unknown member under additionalProperties: false passes")
	}
	wrong := map[string]any{}
	for k, v := range user {
		wrong[k] = v
	}
	wrong["id"] = "42"
	if p := c.validate(ref, wrong, "AuthUser", nil); len(p) == 0 {
		t.Error("a string id passes as number")
	}
	delete(wrong, "id")
	if p := c.validate(ref, wrong, "AuthUser", nil); len(p) == 0 {
		t.Error("a missing required member passes")
	}
	if p := c.validate(map[string]any{"type": "number", "minimum": 1.0}, 5.0, "minimum", nil); len(p) == 0 {
		t.Error("a schema with the unknown validation keyword minimum passes")
	}
	intDef := map[string]any{"type": "integer", "description": "an integer", "title": "int"}
	if p := c.validate(intDef, 3.0, "Int", nil); len(p) != 0 {
		t.Errorf("$defs.Int with title and description fails: %v", p)
	}
	if p := c.validate(intDef, 3.5, "Int", nil); len(p) == 0 {
		t.Error("3.5 passes as integer")
	}
	withDefs := map[string]any{"$ref": "#/$defs/Int", "$defs": map[string]any{"Int": intDef}}
	if p := c.validate(withDefs, 7.0, "defs", nil); len(p) != 0 {
		t.Errorf("a $defs reference fails: %v", p)
	}
	object := map[string]any{"$ref": "#/components/schemas//schemas/object"}
	if p := c.validate(object, map[string]any{"a": 1.0}, "object", nil); len(p) != 0 {
		t.Errorf("the reference to /schemas/object fails for an object: %v", p)
	}
	if p := c.validate(object, "text", "object", nil); len(p) == 0 {
		t.Error("a string passes as /schemas/object")
	}
	if p := c.validate(map[string]any{"$ref": "#/components/schemas/NoSuchSchema"}, 1.0, "missing", nil); len(p) == 0 {
		t.Error("an unresolvable reference passes")
	}
	// A fixture with an unknown member at the root of its documented
	// answer fails like a schema violation of the documentation.
	login := map[string]any{"message": "Login successful", "token": "x"}
	responses := specResponses(c.doc)
	if p := c.validate(responseSchema(responses["postV1UserAuthentication"], 200), login, "login", nil); len(p) == 0 {
		t.Error("a login answer with an unknown member passes")
	}
}
