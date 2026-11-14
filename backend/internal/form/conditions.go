package form

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Conditional form logic after MET-45 (WFL-01, WP-1101): ui_hints.fields.
// <field> may name visible_when, required_when and readonly_when, each a
// MET-45 condition (rules.ParseCondition) over the other values of the
// submission, nested objects by dot path (device.kind):
//
//	{"fields": {"serial": {"visible_when": {"op": "eq", "field": "device.kind", "value": "router"},
//	                       "required_when": {...}, "readonly_when": {...}}}}
//
// The semantics are those of the catalog rules of CIs (ci.catalogViolations):
// a field whose visible_when does not hold is dropped before the values are
// stored and is not validated, unless it is required (by the schema or an
// applying required_when): a required field is never hidden; required_when
// asks for a value; readonly_when refuses a value that differs from the
// prefill (request_form, WP-1102; without one an empty value). The field
// type signature ({"type": "string", "format": "signature"}) holds the id of
// a PNG or JPEG document of the organization the submitter may read.

// Condition kinds of ui_hints.fields.<field>.
const (
	VisibleWhen  = "visible_when"
	RequiredWhen = "required_when"
	ReadonlyWhen = "readonly_when"
)

// conditionKinds are the condition kinds in the order they are checked.
var conditionKinds = []string{VisibleWhen, RequiredWhen, ReadonlyWhen}

// FormatSignature is the format of a signature field.
const FormatSignature = "signature"

// signatureTypes are the content types a signature document may have.
var signatureTypes = []string{"image/png", "image/jpeg"}

// DocumentReader reads a document visible to the caller (document.
// Repository); the router sets it with the module documents.
type DocumentReader interface {
	GetByID(ctx context.Context, orgID, id string) (*document.Document, error)
}

// fieldConditions are the parsed conditions of one field.
type fieldConditions struct {
	field string
	rules map[string]*rules.Rule
}

// hintError is a refused condition of the ui_hints of a definition: 422 on
// ui_hints.fields.<field>[.<kind>].
func hintError(field, kind, message string) error {
	path := "ui_hints.fields." + field
	if kind != "" {
		path += "." + kind
	}
	return &api.FieldError{Field: path, Message: message, Status: http.StatusUnprocessableEntity}
}

// ValidateHints checks the conditions of hints against schema when a
// definition is written (AK1): every condition is a valid MET-45
// condition, every field it names and every field it is set on is a field
// of the schema; a refusal is a 422 field error on
// ui_hints.fields.<field>.<kind>. Other keys of a field's hints (label,
// widget, ...) are left to the client.
func ValidateHints(schema, hints JSONMap) error {
	_, err := parseFieldConditions(schema, hints, true)
	return err
}

// ValidateSignatureFields refuses a schema with a signature field when the
// organization's release has no documents module (docs nil, AK5): 422 on
// schema.properties.<field>.format. A signature is a property (of the form
// or of a nested object) of type string: format signature anywhere else
// (items, additionalProperties, ...), where the submission check would not
// reach it, or on another type is refused with 422 on that format.
func ValidateSignatureFields(schema JSONMap, docs DocumentReader) error {
	if path := misplacedSignature(schema, "schema", true); path != "" {
		return &api.FieldError{Field: path, Message: "signature is a property of type string, never inside items or other keywords",
			Status: http.StatusUnprocessableEntity}
	}
	if docs != nil {
		return nil
	}
	paths := signatureFields(schema, "")
	if len(paths) == 0 {
		return nil
	}
	return &api.FieldError{Field: "schema.properties." + strings.ReplaceAll(paths[0], ".", ".properties.") + ".format", Message: "signature fields need the module documents",
		Status: http.StatusUnprocessableEntity}
}

// misplacedSignature returns the path of the first format signature of
// node that is not a string property reached through properties only
// (property: node is such a property, or the root); "" without one.
func misplacedSignature(node map[string]any, path string, property bool) string {
	if format, _ := node["format"].(string); format == FormatSignature {
		if typ, _ := node["type"].(string); !property || typ != "string" {
			return path + ".format"
		}
	}
	keys := make([]string, 0, len(node))
	for k := range node {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch child := node[k].(type) {
		case map[string]any:
			if k == "properties" {
				names := make([]string, 0, len(child))
				for name := range child {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					if prop, ok := child[name].(map[string]any); ok {
						if p := misplacedSignature(prop, path+".properties."+name, true); p != "" {
							return p
						}
					}
				}
				continue
			}
			if p := misplacedSignature(child, path+"."+k, false); p != "" {
				return p
			}
		case []any:
			for i, item := range child {
				if m, ok := item.(map[string]any); ok {
					if p := misplacedSignature(m, fmt.Sprintf("%s.%s[%d]", path, k, i), false); p != "" {
						return p
					}
				}
			}
		}
	}
	return ""
}

// parseFieldConditions reads ui_hints.fields; strict reports every error,
// otherwise (a stored definition) an invalid condition is ignored.
func parseFieldConditions(schema, hints JSONMap, strict bool) ([]fieldConditions, error) {
	raw, has := hints["fields"]
	if !has || raw == nil {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		if strict {
			return nil, &api.FieldError{Field: "ui_hints.fields", Message: "must be an object", Status: http.StatusUnprocessableEntity}
		}
		return nil, nil
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []fieldConditions
	for _, name := range names {
		entry, isObject := fields[name].(map[string]any)
		if !isObject {
			if strict {
				return nil, hintError(name, "", "must be an object")
			}
			continue
		}
		fc := fieldConditions{field: name, rules: map[string]*rules.Rule{}}
		for _, kind := range conditionKinds {
			cond, set := entry[kind]
			if !set || cond == nil {
				continue
			}
			if strict && !schemaHasField(schema, name) {
				return nil, hintError(name, kind, "is not a field of the schema")
			}
			encoded, err := json.Marshal(cond)
			if err != nil {
				return nil, hintError(name, kind, "is no valid condition")
			}
			rule, err := parseHintCondition(schema, encoded)
			if err != nil {
				if strict {
					return nil, hintError(name, kind, err.Error())
				}
				continue
			}
			fc.rules[kind] = &rule
		}
		if len(fc.rules) > 0 {
			out = append(out, fc)
		}
	}
	return out, nil
}

// parseHintCondition checks one condition of ui_hints.fields: a valid
// MET-45 condition that names fields of schema only.
// validation: the texts of rules.ParseCondition, naming the path that fails,
// and of knownFields, naming the unknown field.
func parseHintCondition(schema JSONMap, raw []byte) (rules.Rule, error) {
	rule, err := rules.ParseCondition(raw)
	if err != nil {
		return rule, err
	}
	err = knownFields(schema, &rule)
	return rule, err
}

// knownFields refuses a condition that names a field the schema does not
// know.
// validation: a fixed text with the field name of the condition.
func knownFields(schema JSONMap, rule *rules.Rule) error {
	if rule.Field != "" && !schemaHasField(schema, rule.Field) {
		return fmt.Errorf("field %q is not a field of the schema", rule.Field)
	}
	for i := range rule.Rules {
		if err := knownFields(schema, &rule.Rules[i]); err != nil {
			return err
		}
	}
	return nil
}

// schemaHasField reports whether the dot path names a property of schema
// (through the properties of nested objects).
func schemaHasField(schema JSONMap, path string) bool {
	return propertySchema(schema, path) != nil
}

func propertySchema(schema map[string]any, path string) map[string]any {
	node := schema
	for _, part := range strings.Split(path, ".") {
		props, _ := node["properties"].(map[string]any)
		child, ok := props[part].(map[string]any)
		if !ok {
			return nil
		}
		node = child
	}
	return node
}

// signatureFields lists the dot paths of the signature fields of schema.
func signatureFields(schema map[string]any, prefix string) []string {
	props, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		child, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if format, _ := child["format"].(string); format == FormatSignature {
			out = append(out, path)
		}
		out = append(out, signatureFields(child, path)...)
	}
	return out
}

// EvaluationValues flattens values to the dot paths of nested objects
// (device.kind) besides their top-level keys, the values a condition reads.
func EvaluationValues(values map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			out[key] = v
			switch child := v.(type) {
			case map[string]any:
				walk(key, child)
			case JSONMap:
				walk(key, child)
			}
		}
	}
	walk("", values)
	return out
}

// SubmissionCheck is the input of ValidateSubmission.
type SubmissionCheck struct {
	OrganizationID string
	Schema         JSONMap
	UIHints        JSONMap
	Values         JSONMap
	// Prefill are the values the form was requested with (request_form,
	// WP-1102); readonly_when compares against them, nil: none.
	Prefill JSONMap
	// Documents reads the signature documents; nil without the module
	// documents.
	Documents DocumentReader
}

// ValidateSubmission checks values against the schema and the conditional
// logic of the hints of one form version (AK2, AK3) and returns the values
// to store: the hidden fields dropped. A violation of the schema, a missing
// required_when value and a changed readonly_when value are a
// ValidationError (400 with the fields); an invalid signature document is
// a 422 field error on values.<field> (AK5).
func ValidateSubmission(ctx context.Context, c *SubmissionCheck) (JSONMap, error) {
	conds, _ := parseFieldConditions(c.Schema, c.UIHints, false)
	values := cloneValues(c.Values)
	flat := EvaluationValues(c.Values)
	required := map[string]bool{}
	if req, ok := c.Schema["required"].([]any); ok {
		for _, item := range req {
			if name, isString := item.(string); isString {
				required[name] = true
			}
		}
	}
	for _, fc := range conds {
		if r := fc.rules[RequiredWhen]; r != nil && rules.EvaluateCondition(r, flat) {
			required[fc.field] = true
		}
		// A nested field required by its parent object is required too.
		if i := strings.LastIndex(fc.field, "."); i > 0 && requiredByParent(c.Schema, fc.field[:i], fc.field[i+1:]) {
			required[fc.field] = true
		}
	}
	var fields []FieldError
	hidden := map[string]bool{}
	for _, fc := range conds {
		if r := fc.rules[VisibleWhen]; r != nil && !rules.EvaluateCondition(r, flat) && !required[fc.field] {
			hidden[fc.field] = true
			deletePath(values, fc.field)
		}
	}
	for _, fc := range conds {
		if hidden[fc.field] {
			continue
		}
		value, present := lookupPath(c.Values, fc.field)
		if r := fc.rules[RequiredWhen]; r != nil && rules.EvaluateCondition(r, flat) && (!present || blank(value)) {
			fields = append(fields, FieldError{Field: join("$", fc.field), Message: "is required"})
		}
		if r := fc.rules[ReadonlyWhen]; r != nil && rules.EvaluateCondition(r, flat) && present {
			prefill, _ := lookupPath(c.Prefill, fc.field)
			if !sameValue(prefill, value) {
				fields = append(fields, FieldError{Field: join("$", fc.field), Message: "is read-only"})
			}
		}
	}
	if err := Validate(withoutHidden(c.Schema, hidden), values); err != nil {
		var ve ValidationError
		if !errors.As(err, &ve) {
			return nil, err
		}
		fields = append(fields, ve.Fields...)
	}
	if len(fields) > 0 {
		return nil, ValidationError{Fields: fields}
	}
	if err := checkSignatures(ctx, c, values); err != nil {
		return nil, err
	}
	return values, nil
}

// requiredByParent reports whether name is in the required list of the
// object schema at the dot path parent.
func requiredByParent(schema JSONMap, parent, name string) bool {
	node := propertySchema(schema, parent)
	req, _ := node["required"].([]any)
	for _, item := range req {
		if s, _ := item.(string); s == name {
			return true
		}
	}
	return false
}

// withoutHidden returns schema without the hidden top-level fields in its
// required list: a hidden field is not validated (it is never required
// there, a required field is never hidden, but nested paths are dropped
// from their parent only).
func withoutHidden(schema JSONMap, hidden map[string]bool) JSONMap {
	if len(hidden) == 0 {
		return schema
	}
	req, ok := schema["required"].([]any)
	if !ok {
		return schema
	}
	out := JSONMap{}
	for k, v := range schema {
		out[k] = v
	}
	kept := make([]any, 0, len(req))
	for _, item := range req {
		if name, _ := item.(string); !hidden[name] {
			kept = append(kept, item)
		}
	}
	out["required"] = kept
	return out
}

// checkSignatures checks the signature fields of values (AK5): the id of a
// document of the organization the caller may read (document:read and the
// row level security of the document reader), PNG or JPEG.
func checkSignatures(ctx context.Context, c *SubmissionCheck, values JSONMap) error {
	for _, path := range signatureFields(c.Schema, "") {
		raw, present := lookupPath(values, path)
		if !present || raw == nil {
			continue
		}
		refuse := func(message string) error {
			return &api.FieldError{Field: "values." + path, Message: message, Status: http.StatusUnprocessableEntity}
		}
		id, ok := raw.(string)
		if !ok || !api.IsUUID(id) {
			return refuse("must be the id of a signature document")
		}
		if c.Documents == nil {
			return refuse("signature documents need the module documents")
		}
		if p, has := identity.PrincipalFromContext(ctx); has && !p.Has(identity.PermDocumentRead) {
			return refuse("document not found")
		}
		doc, err := c.Documents.GetByID(ctx, c.OrganizationID, id)
		if err != nil || doc == nil || doc.OrganizationID != c.OrganizationID {
			return refuse("document not found")
		}
		if !slices.Contains(signatureTypes, strings.ToLower(doc.MimeType)) {
			return refuse("must be a PNG or JPEG image")
		}
	}
	return nil
}

func cloneValues(v JSONMap) JSONMap {
	out := JSONMap{}
	for k, val := range v {
		if child, ok := val.(map[string]any); ok {
			out[k] = map[string]any(cloneValues(child))
			continue
		}
		out[k] = val
	}
	return out
}

func lookupPath(values map[string]any, path string) (any, bool) {
	node := values
	parts := strings.Split(path, ".")
	for i, part := range parts {
		v, ok := node[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		switch child := v.(type) {
		case map[string]any:
			node = child
		case JSONMap:
			node = child
		default:
			return nil, false
		}
	}
	return nil, false
}

func deletePath(values map[string]any, path string) {
	parts := strings.Split(path, ".")
	node := values
	for _, part := range parts[:len(parts)-1] {
		switch child := node[part].(type) {
		case map[string]any:
			node = child
		case JSONMap:
			node = child
		default:
			return
		}
	}
	delete(node, parts[len(parts)-1])
}

// blank reports an empty value like ci.isBlank.
func blank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// sameValue compares two values like ci.sameValue.
func sameValue(a, b any) bool {
	if blank(a) && blank(b) {
		return true
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
