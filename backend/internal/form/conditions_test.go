package form

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// The conditional form logic after MET-45 (WFL-01, WP-1101): visible_when,
// required_when and readonly_when of ui_hints.fields evaluated on the server
// with the semantics of the catalog rules, checked when a definition is
// written, and the field type signature.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestVisibleWhenDropsHiddenField|TestRequiredWhen|TestReadonlyWhenAgainstPrefill|TestRequiredNeverHidden|TestInvalidConditionRejected|TestSignatureField' ./internal/form/

// deviceSchema is a form with a nested device object, a serial number and
// a note.
var deviceSchema = JSONMap{"type": "object", "properties": map[string]any{
	"device": map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}}},
	"serial": map[string]any{"type": "string", "minLength": 3.0},
	"note":   map[string]any{"type": "string"},
	"count":  map[string]any{"type": "integer"},
}}

func hints(field string, kinds map[string]any) JSONMap {
	return JSONMap{"fields": map[string]any{field: kinds}}
}

var isRouter = map[string]any{"op": "eq", "field": "device.kind", "value": "router"}

func check(t *testing.T, c *SubmissionCheck) (JSONMap, []FieldError, error) {
	t.Helper()
	if c.Schema == nil {
		c.Schema = deviceSchema
	}
	if err := ValidateHints(c.Schema, c.UIHints); err != nil {
		t.Fatalf("hints %v: %v", c.UIHints, err)
	}
	out, err := ValidateSubmission(context.Background(), c)
	var ve ValidationError
	if errors.As(err, &ve) {
		return out, ve.Fields, nil
	}
	return out, nil, err
}

// TestVisibleWhenDropsHiddenField: a field whose visible_when does not hold
// is dropped and not validated (its too short value is no error); visible
// it is validated and kept.
func TestVisibleWhenDropsHiddenField(t *testing.T) {
	h := hints("serial", map[string]any{VisibleWhen: isRouter})
	out, fields, err := check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "switch"}, "serial": "x"}})
	if err != nil || len(fields) != 0 {
		t.Fatalf("hidden field: %v %v, want no error", fields, err)
	}
	if _, kept := out["serial"]; kept {
		t.Errorf("values %v, want the hidden serial dropped", out)
	}
	if _, fields, _ = check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "router"}, "serial": "x"}}); len(fields) != 1 ||
		fields[0].Field != "$.serial" {
		t.Errorf("visible field: %v, want the minimum length error of serial", fields)
	}
	out, _, _ = check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "router"}, "serial": "SN-1"}})
	if out["serial"] != "SN-1" {
		t.Errorf("values %v, want the visible serial kept", out)
	}
}

// TestRequiredWhen: while the condition holds a missing value is an error;
// otherwise the field is optional.
func TestRequiredWhen(t *testing.T) {
	h := hints("serial", map[string]any{RequiredWhen: map[string]any{"op": "all", "rules": []any{isRouter,
		map[string]any{"op": "gt", "field": "count", "value": 0.0}}}})
	_, fields, err := check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "router"}, "count": 2.0}})
	if err != nil || len(fields) != 1 || fields[0].Field != "$.serial" || fields[0].Message != "is required" {
		t.Fatalf("required serial missing: %v %v, want is required on $.serial", fields, err)
	}
	if _, fields, _ = check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "router"}, "serial": "  ", "count": 2.0}}); len(fields) == 0 ||
		fields[0].Message != "is required" {
		t.Errorf("blank required serial: %v, want is required", fields)
	}
	if _, fields, _ = check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "router"}, "count": 0.0}}); len(fields) != 0 {
		t.Errorf("condition does not hold: %v, want the field optional", fields)
	}
}

// TestReadonlyWhenAgainstPrefill: a read-only field keeps its prefill; a
// different value is refused, without prefill an empty value is required.
func TestReadonlyWhenAgainstPrefill(t *testing.T) {
	h := hints("serial", map[string]any{ReadonlyWhen: isRouter})
	router := map[string]any{"kind": "router"}
	prefill := JSONMap{"serial": "SN-42"}
	if _, fields, _ := check(t, &SubmissionCheck{UIHints: h, Prefill: prefill, Values: JSONMap{"device": router, "serial": "SN-43"}}); len(fields) != 1 ||
		fields[0].Message != "is read-only" {
		t.Errorf("changed read-only value: %v, want is read-only", fields)
	}
	out, fields, _ := check(t, &SubmissionCheck{UIHints: h, Prefill: prefill, Values: JSONMap{"device": router, "serial": "SN-42"}})
	if len(fields) != 0 || out["serial"] != "SN-42" {
		t.Errorf("prefilled value: %v %v, want accepted", out, fields)
	}
	if _, fields, _ = check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": router, "serial": "SN-1"}}); len(fields) != 1 {
		t.Errorf("value without prefill: %v, want is read-only", fields)
	}
	if _, fields, _ = check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "switch"}, "serial": "SN-1"}}); len(fields) != 0 {
		t.Errorf("condition does not hold: %v, want the value accepted", fields)
	}
}

// TestRequiredNeverHidden: a field required by the schema or an applying
// required_when is never hidden by visible_when.
func TestRequiredNeverHidden(t *testing.T) {
	schema := JSONMap{"type": "object", "required": []any{"serial"}, "properties": deviceSchema["properties"]}
	h := hints("serial", map[string]any{VisibleWhen: isRouter})
	if _, fields, _ := check(t, &SubmissionCheck{Schema: schema, UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "switch"}}}); len(fields) != 1 ||
		fields[0].Field != "$.serial" || fields[0].Message != "is required" {
		t.Errorf("schema-required field hidden: %v, want is required", fields)
	}
	h = hints("note", map[string]any{VisibleWhen: isRouter, RequiredWhen: map[string]any{"op": "empty", "field": "serial"}})
	out, fields, _ := check(t, &SubmissionCheck{UIHints: h, Values: JSONMap{"device": map[string]any{"kind": "switch"}, "note": "kept"}})
	if len(fields) != 0 || out["note"] != "kept" {
		t.Errorf("required_when field hidden: %v %v, want kept", out, fields)
	}
	// A nested field required by its parent object is never hidden either:
	// sent it is kept, missing it is required.
	nested := JSONMap{"type": "object", "properties": map[string]any{
		"kind":   map[string]any{"type": "string"},
		"device": map[string]any{"type": "object", "required": []any{"serial"}, "properties": map[string]any{"serial": map[string]any{"type": "string"}}},
	}}
	h = hints("device.serial", map[string]any{VisibleWhen: map[string]any{"op": "eq", "field": "kind", "value": "router"}})
	out, fields, _ = check(t, &SubmissionCheck{Schema: nested, UIHints: h, Values: JSONMap{"kind": "switch", "device": map[string]any{"serial": "SN-1"}}})
	if device, _ := out["device"].(map[string]any); len(fields) != 0 || device["serial"] != "SN-1" {
		t.Errorf("nested required field hidden: %v %v, want kept", out, fields)
	}
	if _, fields, _ = check(t, &SubmissionCheck{Schema: nested, UIHints: h, Values: JSONMap{"kind": "switch", "device": map[string]any{}}}); len(fields) != 1 ||
		fields[0].Field != "$.device.serial" || fields[0].Message != "is required" {
		t.Errorf("nested required field missing: %v, want is required", fields)
	}
}

// TestInvalidConditionRejected: an unknown operator, a field the schema
// does not know (in the condition or as the hinted field) are 422 with the
// path ui_hints.fields.<field>.<kind>.
func TestInvalidConditionRejected(t *testing.T) {
	for want, h := range map[string]JSONMap{
		"ui_hints.fields.serial.visible_when":  hints("serial", map[string]any{VisibleWhen: map[string]any{"op": "like", "field": "note", "value": "x"}}),
		"ui_hints.fields.serial.required_when": hints("serial", map[string]any{RequiredWhen: map[string]any{"op": "eq", "field": "device.vendor", "value": "x"}}),
		"ui_hints.fields.serial.readonly_when": hints("serial", map[string]any{ReadonlyWhen: "always"}),
		"ui_hints.fields.unknown.visible_when": hints("unknown", map[string]any{VisibleWhen: isRouter}),
		"ui_hints.fields":                      {"fields": []any{}},
	} {
		err := ValidateHints(deviceSchema, h)
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != want || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %v, want 422 on %s", h, err, want)
		}
	}
	if err := ValidateHints(deviceSchema, hints("serial", map[string]any{VisibleWhen: isRouter, "label": "Seriennummer"})); err != nil {
		t.Errorf("valid hints with a label: %v", err)
	}
	// A stored definition with an invalid condition (before WP-1101) is
	// evaluated without it.
	if _, err := ValidateSubmission(context.Background(), &SubmissionCheck{Schema: deviceSchema,
		UIHints: hints("serial", map[string]any{VisibleWhen: "x"}), Values: JSONMap{"serial": "SN-1"}}); err != nil {
		t.Errorf("stored invalid condition: %v", err)
	}
}

// fakeDocuments is the document reader of the signature tests.
type fakeDocuments map[string]*document.Document

func (f fakeDocuments) GetByID(_ context.Context, orgID, id string) (*document.Document, error) {
	d := f[id]
	if d == nil || d.OrganizationID != orgID {
		return nil, api.ErrNotFound
	}
	return d, nil
}

// TestSignatureField: a signature holds a PNG or JPEG document of the
// organization the submitter may read; a foreign document, a PDF, no
// document:read and a missing module documents are refused.
func TestSignatureField(t *testing.T) {
	const (
		png     = "11111111-1111-4111-8111-111111111111"
		pdf     = "22222222-2222-4222-8222-222222222222"
		foreign = "33333333-3333-4333-8333-333333333333"
	)
	docs := fakeDocuments{
		png:     {ID: png, OrganizationID: "org-1", MimeType: "image/png"},
		pdf:     {ID: pdf, OrganizationID: "org-1", MimeType: "application/pdf"},
		foreign: {ID: foreign, OrganizationID: "org-2", MimeType: "image/jpeg"},
	}
	schema := JSONMap{"type": "object", "properties": map[string]any{"signature": map[string]any{"type": "string", "format": FormatSignature}}}
	reader := identity.WithPrincipal(context.Background(), identity.Principal{Subject: "u", OrganizationID: "org-1",
		Permissions: []identity.Permission{identity.PermFormWrite, identity.PermDocumentRead}})
	submit := func(ctx context.Context, value string, docs DocumentReader) error {
		_, err := ValidateSubmission(ctx, &SubmissionCheck{OrganizationID: "org-1", Schema: schema, Values: JSONMap{"signature": value}, Documents: docs})
		return err
	}
	if err := submit(reader, png, docs); err != nil {
		t.Fatalf("PNG signature: %v", err)
	}
	for name, value := range map[string]string{"foreign": foreign, "pdf": pdf, "no uuid": "sig.png", "unknown": "44444444-4444-4444-8444-444444444444"} {
		err := submit(reader, value, docs)
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "values.signature" || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s document: %v, want 422 on values.signature", name, err)
		}
	}
	noRead := identity.WithPrincipal(context.Background(), identity.Principal{Subject: "u", OrganizationID: "org-1", Permissions: []identity.Permission{identity.PermFormWrite}})
	var fe *api.FieldError
	if err := submit(noRead, png, docs); !errors.As(err, &fe) || fe.Field != "values.signature" {
		t.Errorf("submitter without document:read: %v, want 422 on values.signature", err)
	}
	if err := ValidateSignatureFields(schema, nil); err == nil {
		t.Error("signature field without the module documents, want 422")
	} else if !errors.As(err, &fe) || fe.Field != "schema.properties.signature.format" {
		t.Errorf("signature field without documents: %v, want 422 on schema.properties.signature.format", err)
	}
	if err := ValidateSignatureFields(schema, docs); err != nil {
		t.Errorf("signature field with documents: %v", err)
	}
	// A signature only as a string property: inside items, under
	// additionalProperties or on another type the submission check would
	// not reach it.
	for want, bad := range map[string]JSONMap{
		"schema.properties.signatures.items.format": {"type": "object", "properties": map[string]any{
			"signatures": map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": FormatSignature}}}},
		"schema.properties.extra.additionalProperties.format": {"type": "object", "properties": map[string]any{
			"extra": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "format": FormatSignature}}}},
		"schema.properties.signature.format": {"type": "object", "properties": map[string]any{"signature": map[string]any{"type": "integer", "format": FormatSignature}}},
	} {
		if err := ValidateSignatureFields(bad, docs); !errors.As(err, &fe) || fe.Field != want || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("misplaced signature: %v, want 422 on %s", err, want)
		}
	}
	// The schema validator takes a signature for a string only.
	if err := Validate(schema, map[string]any{"signature": 42.0}); err == nil {
		t.Error("a number as signature passed the schema, want expected string")
	}
}
