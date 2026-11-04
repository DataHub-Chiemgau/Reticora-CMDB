package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func f64(v float64) *float64 { return &v }

func serverKindDefs() []fieldmeta.FieldDefinition {
	return []fieldmeta.FieldDefinition{
		{
			Name:       "server_kind",
			DataType:   fieldmeta.TypeEnum,
			Required:   true,
			EnumValues: []string{"physical", "virtual"},
		},
		{
			Name:     "rack_unit",
			DataType: fieldmeta.TypeInteger,
			Conditional: &fieldmeta.ConditionalRules{
				VisibleWhen:  []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
				RequiredWhen: []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
			},
			Validation: &fieldmeta.ValidationRules{Min: f64(1), Max: f64(48)},
		},
		{
			Name:     "hypervisor",
			DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{
				VisibleWhen:  []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "virtual"}},
				RequiredWhen: []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "virtual"}},
			},
		},
	}
}

func fieldsOf(err error) []string {
	ve, ok := AsValidationError(err)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(ve.Violations))
	for _, v := range ve.Violations {
		out = append(out, v.Field)
	}
	return out
}

func TestValidateAttributesRequiredField(t *testing.T) {
	err := validateAttributes(serverKindDefs(), map[string]any{}, map[string]any{}, nil)
	got := fieldsOf(err)
	if len(got) != 1 || got[0] != "server_kind" {
		t.Fatalf("expected server_kind required violation, got %v (err=%v)", got, err)
	}
}

func TestValidateAttributesConditionalRequired(t *testing.T) {
	values := map[string]any{"server_kind": "physical"}
	err := validateAttributes(serverKindDefs(), values, values, nil)
	if got := fieldsOf(err); len(got) != 1 || got[0] != "rack_unit" {
		t.Fatalf("expected rack_unit required for physical servers, got %v", got)
	}

	values = map[string]any{"server_kind": "virtual"}
	err = validateAttributes(serverKindDefs(), values, values, nil)
	if got := fieldsOf(err); len(got) != 1 || got[0] != "hypervisor" {
		t.Fatalf("expected hypervisor required for virtual servers, got %v", got)
	}
}

func TestValidateAttributesHiddenFieldIsNotRequired(t *testing.T) {
	// A virtual server must not be asked for rack_unit.
	values := map[string]any{"server_kind": "virtual", "hypervisor": "esx-01"}
	if err := validateAttributes(serverKindDefs(), values, values, nil); err != nil {
		t.Fatalf("expected no violations, got %v", err)
	}
}

func TestValidateAttributesRangeAndEnum(t *testing.T) {
	values := map[string]any{"server_kind": "physical", "rack_unit": 99}
	if got := fieldsOf(validateAttributes(serverKindDefs(), values, values, nil)); len(got) != 1 || got[0] != "rack_unit" {
		t.Fatalf("expected rack_unit range violation, got %v", got)
	}

	values = map[string]any{"server_kind": "container"}
	if got := fieldsOf(validateAttributes(serverKindDefs(), values, values, nil)); len(got) == 0 || got[0] != "server_kind" {
		t.Fatalf("expected server_kind enum violation, got %v", got)
	}
}

func TestValidateAttributesConditionalAllowedValues(t *testing.T) {
	defs := []fieldmeta.FieldDefinition{
		{Name: "env", DataType: fieldmeta.TypeText},
		{
			Name:     "tier",
			DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{
				AllowedValuesWhen: &fieldmeta.ConditionalValues{
					When:   []fieldmeta.Predicate{{Field: "env", Op: "eq", Value: "prod"}},
					Values: []any{"gold", "silver"},
				},
			},
		},
	}
	values := map[string]any{"env": "prod", "tier": "bronze"}
	if got := fieldsOf(validateAttributes(defs, values, values, nil)); len(got) != 1 || got[0] != "tier" {
		t.Fatalf("expected tier allowed-values violation, got %v", got)
	}
	values = map[string]any{"env": "dev", "tier": "bronze"}
	if err := validateAttributes(defs, values, values, nil); err != nil {
		t.Fatalf("tier is unconstrained outside prod, got %v", err)
	}
}

func TestValidateAttributesReadOnly(t *testing.T) {
	defs := []fieldmeta.FieldDefinition{
		{Name: "state", DataType: fieldmeta.TypeText},
		{
			Name:     "asset_tag",
			DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{
				ReadOnlyWhen: []fieldmeta.Predicate{{Field: "state", Op: "eq", Value: "disposed"}},
			},
		},
	}
	existing := map[string]any{"state": "disposed", "asset_tag": "A-1"}
	patch := map[string]any{"asset_tag": "A-2"}
	values := MergePatch(existing, patch)
	if got := fieldsOf(validateAttributes(defs, values, patch, existing)); len(got) != 1 || got[0] != "asset_tag" {
		t.Fatalf("expected asset_tag read-only violation, got %v", got)
	}
	// Re-sending the identical value is not a change and must be accepted.
	patch = map[string]any{"asset_tag": "A-1"}
	if err := validateAttributes(defs, MergePatch(existing, patch), patch, existing); err != nil {
		t.Fatalf("unchanged read-only value must pass, got %v", err)
	}
}

func TestValidateAttributesIgnoresUndefinedAttributes(t *testing.T) {
	values := map[string]any{"server_kind": "virtual", "hypervisor": "esx-01", "discovered_only": 42}
	if err := validateAttributes(serverKindDefs(), values, values, nil); err != nil {
		t.Fatalf("undefined attributes must not be rejected, got %v", err)
	}
}

func TestValidateAttributesNoDefinitionsIsNoop(t *testing.T) {
	if err := validateAttributes(nil, map[string]any{"anything": 1}, nil, nil); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

// stubResolver returns fixed definitions regardless of scope.
type stubResolver struct{ defs []fieldmeta.FieldDefinition }

func (s stubResolver) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return s.defs, nil
}

func TestServiceCreateRejectsInvalidAttributes(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo).WithFieldResolver(stubResolver{defs: serverKindDefs()})

	item := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "srv-01", Attributes: map[string]any{}}
	err := svc.Create(context.Background(), item)
	if _, ok := AsValidationError(err); !ok {
		t.Fatalf("expected validation error, got %v", err)
	}

	item.Attributes = map[string]any{"server_kind": "virtual", "hypervisor": "esx-01"}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("valid payload must be accepted, got %v", err)
	}
}

// compositionConflict is a composition error as citype.CompositionError
// reports it (MET-16), without importing citype.
type compositionConflict struct{ key, code string }

func (c compositionConflict) Error() string {
	return "attribute " + c.key + ": no allowed value is left (MET-16)"
}
func (c compositionConflict) CompositionKey() string  { return c.key }
func (c compositionConflict) CompositionCode() string { return c.code }

// conflictResolver fails like a resolver whose layers cannot be composed.
type conflictResolver struct{ err error }

func (r conflictResolver) ResolveFields(context.Context, string, string, string) ([]fieldmeta.FieldDefinition, error) {
	return nil, r.err
}

// TestResolveFieldsCompositionCode covers WP-711 (MET-16): a composition
// conflict rejects the write with a violation of the key that carries the
// code of the conflict, and the violation's JSON contains it.
func TestResolveFieldsCompositionCode(t *testing.T) {
	conflict := compositionConflict{key: "env", code: "enum-empty"}
	_, err := resolveFields(context.Background(), conflictResolver{err: fmt.Errorf("resolve: %w", conflict)}, "org-1", "t", "")
	ve, ok := AsValidationError(err)
	if !ok {
		t.Fatalf("err = %v, want a validation error", err)
	}
	want := Violation{Field: "env", Detail: "resolve: " + conflict.Error(), Code: "enum-empty"}
	if len(ve.Violations) != 1 || ve.Violations[0] != want {
		t.Fatalf("violations = %+v, want [%+v]", ve.Violations, want)
	}
	raw, err := json.Marshal(ve.Violations[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["code"] != "enum-empty" || got["field"] != "env" {
		t.Errorf("violation JSON = %s, want field env and code enum-empty", raw)
	}
	// A violation without a code leaves the property out.
	raw, err = json.Marshal(Violation{Field: "env", Detail: "field is required"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "code") {
		t.Errorf("violation JSON = %s, want no code", raw)
	}
}

// TestReservedNamespaces covers WP-091 (GLO-13, CI-10): the four namespaces
// are rejected as attribute keys by create and PATCH (400 naming the field)
// and as field names; the read view hides the projected namespaces and
// shows the instance attributes under their names; the write form moves
// instance attributes into _instance.
func TestReservedNamespaces(t *testing.T) {
	if len(ReservedNamespaces) != 4 {
		t.Fatalf("namespaces: %v", ReservedNamespaces)
	}
	for _, ns := range ReservedNamespaces {
		if !IsReservedKey(ns) {
			t.Errorf("%s is not reserved", ns)
		}
		if err := fieldmeta.ValidateDefinition(&fieldmeta.FieldDefinition{Name: ns, DataType: fieldmeta.TypeText}); err == nil {
			t.Errorf("field definition named %s accepted", ns)
		}
	}
	if IsReservedKey("overrides") || IsReservedKey("_custom") {
		t.Error("an ordinary key is reserved")
	}

	svc := NewService(NewMemoryRepository())
	ctx := context.Background()
	var reserved *ReservedKeyError
	err := svc.Create(ctx, &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "x", Attributes: map[string]any{"_overrides": map[string]any{}, "rack": "R1"}})
	if !errors.As(err, &reserved) || len(reserved.Fields) != 1 || reserved.Fields[0] != "attributes._overrides" {
		t.Errorf("create with _overrides: %v", err)
	}
	item := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "x", Attributes: map[string]any{"rack": "R1"}}
	if err = svc.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	for _, ns := range ReservedNamespaces {
		if _, err = svc.Update(ctx, "org-1", item.ID, UpdateRequest{Attributes: map[string]any{ns: "x"}}); !errors.As(err, &reserved) {
			t.Errorf("PATCH %s: %v", ns, err)
		}
	}

	// The HTTP answer is 400 with the field.
	mux := chi.NewRouter()
	NewHandler(svc).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/cis/"+item.ID, strings.NewReader(`{"attributes":{"_provenance":{}}}`))
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "attributes._provenance") {
		t.Errorf("PATCH _provenance: %d %s", w.Code, w.Body.String())
	}

	stored := map[string]any{
		"rack": "R1", "_instance": map[string]any{"label": "A"},
		"_overrides": map[string]any{"model": map[string]any{"value": "m"}}, "_observed": map[string]any{}, "_provenance": map[string]any{},
	}
	view := presentAttributes(stored)
	if len(view) != 2 || view["rack"] != "R1" || view["label"] != "A" {
		t.Errorf("read view: %v", view)
	}
	back := storedAttributes(view, []string{"label"})
	if len(back) != 2 || back["rack"] != "R1" || back["_instance"].(map[string]any)["label"] != "A" {
		t.Errorf("write form: %v", back)
	}
	if out := storedAttributes(map[string]any{"_overrides": 1, "x": 2}, nil); len(out) != 1 {
		t.Errorf("namespaces in the write form: %v", out)
	}
}

// TestTypedColumnsTakePartInValidation covers WP-117 (CI-03, MET-11): a
// typed column fulfils the field definition of its name (create and
// PATCH), its own data type is checked, a manual create keeps every typed
// field, and observed values are checked with the same rules.
func TestTypedColumnsTakePartInValidation(t *testing.T) {
	defs := []fieldmeta.FieldDefinition{
		{Name: "hostname", DataType: fieldmeta.TypeText, Required: true,
			Validation: &fieldmeta.ValidationRules{Pattern: `^[a-z0-9-]+$`}},
		{Name: "os_name", DataType: fieldmeta.TypeEnum, EnumValues: []string{"linux", "windows"}},
		{Name: "rack_unit", DataType: fieldmeta.TypeInteger},
	}
	repo := NewMemoryRepository()
	svc := NewService(repo).WithFieldResolver(stubResolver{defs: defs})
	ctx := context.Background()

	// Required: the typed hostname counts, an attribute is not needed.
	missing := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "srv", Attributes: map[string]any{}}
	if got := fieldsOf(svc.Create(ctx, missing)); len(got) != 1 || got[0] != "hostname" {
		t.Fatalf("missing hostname: %v", got)
	}
	item := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "srv", Hostname: "srv-01", OSName: "linux", Attributes: map[string]any{}}
	if err := svc.Create(ctx, item); err != nil {
		t.Fatalf("typed hostname rejected: %v", err)
	}

	// The definition's rules apply to the typed column.
	bad := "SRV_01"
	if got := fieldsOf(updateErr(svc.Update(ctx, "org-1", item.ID, UpdateRequest{Hostname: &bad}))); len(got) != 1 || got[0] != "hostname" {
		t.Errorf("hostname pattern: %v", got)
	}
	empty := ""
	if got := fieldsOf(updateErr(svc.Update(ctx, "org-1", item.ID, UpdateRequest{Hostname: &empty}))); len(got) != 1 || got[0] != "hostname" {
		t.Errorf("clearing a required typed column: %v", got)
	}
	bsd := "freebsd"
	if got := fieldsOf(updateErr(svc.Update(ctx, "org-1", item.ID, UpdateRequest{OSName: &bsd}))); len(got) != 1 || got[0] != "os_name" {
		t.Errorf("os_name enum: %v", got)
	}

	// The typed columns' own data types, with or without definitions.
	plain := NewService(NewMemoryRepository())
	for field, item := range map[string]*Item{
		"management_ip": {OrganizationID: "org-1", CITypeID: testServerType, Name: "a", ManagementIP: "10.0.0.300"},
		"primary_mac":   {OrganizationID: "org-1", CITypeID: testServerType, Name: "b", PrimaryMAC: "not-a-mac"},
		"hardware_uuid": {OrganizationID: "org-1", CITypeID: testServerType, Name: "c", HardwareUUID: "1234"},
		"name":          {OrganizationID: "org-1", CITypeID: testServerType, Name: "two\nlines"},
	} {
		if got := fieldsOf(plain.Create(ctx, item)); len(got) != 1 || got[0] != field {
			t.Errorf("%s: %v", field, got)
		}
	}

	// A manual create keeps every typed field of the request.
	mux := chi.NewRouter()
	NewHandler(plain).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{"ci_type_id":"`+testSwitchType+`","name":"sw-01",
		"hostname":"sw-01","fqdn":"sw-01.example","os_name":"ios","os_version":"17","primary_mac":"00:1a:2b:3c:4d:5e",
		"hardware_uuid":"c0de1170-0000-4000-8000-000000000009","sys_object_id":"1.3.6.1.4.1.9"}`))
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	for _, want := range []string{`"hostname":"sw-01"`, `"fqdn":"sw-01.example"`, `"os_name":"ios"`, `"os_version":"17"`,
		`"primary_mac":"00:1a:2b:3c:4d:5e"`, `"hardware_uuid":"c0de1170-0000-4000-8000-000000000009"`, `"sys_object_id":"1.3.6.1.4.1.9"`} {
		if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), want) {
			t.Errorf("create dropped %s: %d %s", want, w.Code, w.Body.String())
		}
	}

	// Observed values: the same value rules, required fields do not apply.
	invalid, err := svc.InvalidObserved(ctx, "org-1", "t", "", map[string]any{
		"hostname": "Bad Host", "os_name": "linux", "rack_unit": "high", "unknown": "kept"})
	if err != nil || strings.Join(invalid, ",") != "hostname,rack_unit" {
		t.Errorf("InvalidObserved = %v, %v", invalid, err)
	}
	if invalid, _ := svc.InvalidObserved(ctx, "org-1", "t", "", map[string]any{"os_name": "windows"}); len(invalid) != 0 {
		t.Errorf("missing required observed field reported: %v", invalid)
	}
}

func updateErr(_ *Item, err error) error { return err }

// TestObservedTypedValuesFollowManualWrites covers WP-501 (MET-11, CI-03,
// F-143): a typed column is an implicit definition of the observed-value
// check, with or without field definitions of the CI type and in both
// forms (InvalidObserved, ObservedRules), so discovery drops exactly the
// values a manual write refuses. The same value answers 422 on POST /cis,
// a valid one 201 there and passes the observed check.
func TestObservedTypedValuesFollowManualWrites(t *testing.T) {
	ctx := context.Background()
	defs := []fieldmeta.FieldDefinition{
		// A definition of the same name adds its rules to the column's type.
		{Name: "hostname", DataType: fieldmeta.TypeText, Validation: &fieldmeta.ValidationRules{Pattern: `^[a-z0-9-]+$`}},
		{Name: "rack_unit", DataType: fieldmeta.TypeInteger},
	}
	services := map[string]*Service{
		"without definitions": NewService(NewMemoryRepository()),
		"with definitions":    NewService(NewMemoryRepository()).WithFieldResolver(stubResolver{defs: defs}),
	}
	invalid := map[string]string{
		"hostname":      "line1\nline2",
		"hardware_uuid": "not-a-uuid",
		"primary_mac":   "zz:zz",
		"management_ip": "999.1.1.1",
		"name":          "two\nlines",
		"os_name":       "linux\r",
	}
	valid := map[string]string{
		"hostname": "valid-host", "hardware_uuid": "c0de0501-0000-4000-8000-000000000002",
		"primary_mac": "00:1a:2b:3c:4d:5f", "management_ip": "192.0.2.10", "name": "srv-501", "os_name": "linux",
	}
	for label, svc := range services {
		rules, err := svc.ObservedRules(ctx, "org-1", testServerType)
		if err != nil {
			t.Fatal(err)
		}
		for field, value := range invalid {
			values := map[string]any{field: value, "serial_number": "SN-501"}
			got, err := svc.InvalidObserved(ctx, "org-1", testServerType, "", values)
			if err != nil || strings.Join(got, ",") != field {
				t.Errorf("%s: InvalidObserved(%s=%q) = %v, %v; want only %s", label, field, value, got, err, field)
			}
			if got := rules(values); strings.Join(got, ",") != field {
				t.Errorf("%s: ObservedRules(%s=%q) = %v; want only %s", label, field, value, got, field)
			}
		}
		values := map[string]any{}
		for field, value := range valid {
			values[field] = value
		}
		if got, err := svc.InvalidObserved(ctx, "org-1", testServerType, "", values); err != nil || len(got) != 0 {
			t.Errorf("%s: valid values reported: %v, %v", label, got, err)
		}
		if got := rules(values); len(got) != 0 {
			t.Errorf("%s: valid values reported by ObservedRules: %v", label, got)
		}
	}
	// A value violating the column type and a definition is reported once.
	got, _ := services["with definitions"].InvalidObserved(ctx, "org-1", testServerType, "", map[string]any{"hostname": "Bad\nHost", "rack_unit": "x"})
	if strings.Join(got, ",") != "hostname,rack_unit" {
		t.Errorf("InvalidObserved = %v, want hostname,rack_unit each once", got)
	}
	if got := InvalidTypedColumns(map[string]any{"hardware_uuid": "1234", "rack_unit": "x", "fqdn": "a.example"}); strings.Join(got, ",") != "hardware_uuid" {
		t.Errorf("InvalidTypedColumns = %v, want hardware_uuid (definitions do not apply)", got)
	}

	// The manual write: the same values answer 422 with the field, valid
	// ones 201.
	mux := chi.NewRouter()
	NewHandler(NewService(NewMemoryRepository())).RegisterRoutes(mux)
	create := func(field, value string) *httptest.ResponseRecorder {
		body := map[string]any{"ci_type_id": testServerType, "name": "srv-501-" + field}
		body[field] = value
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", bytes.NewReader(raw))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	for field, value := range invalid {
		if w := create(field, value); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), field) {
			t.Errorf("POST /cis with %s=%q: %d %s, want 422 naming %s", field, value, w.Code, w.Body.String(), field)
		}
	}
	for field, value := range valid {
		if w := create(field, value); w.Code != http.StatusCreated {
			t.Errorf("POST /cis with %s=%q: %d %s, want 201", field, value, w.Code, w.Body.String())
		}
	}
}

// catalogRule builds a catalog rule from its JSON condition and value rule
// ("" for none), as citype.ResolveRules returns it.
func catalogRule(t *testing.T, key, attribute, kind, condition, ast string) fieldmeta.CatalogRule {
	t.Helper()
	out := fieldmeta.CatalogRule{Key: key, AttributeKey: attribute, Kind: kind}
	if condition != "" {
		cond, err := rules.ParseCondition([]byte(condition))
		if err != nil {
			t.Fatal(err)
		}
		out.Condition = &cond
	}
	if ast != "" {
		rule, err := rules.Parse([]byte(ast))
		if err != nil {
			t.Fatal(err)
		}
		out.AST = &rule
	}
	return out
}

// violationsOf returns the violations of err as field/rule/code triples.
func violationsOf(err error) []string {
	ve, ok := AsValidationError(err)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(ve.Violations))
	for _, v := range ve.Violations {
		out = append(out, v.Field+"/"+v.Rule+"/"+v.Code)
	}
	return out
}

// TestCatalogRuleKinds covers WP-257 (MET-10): the four kinds of a catalog
// rule with and without condition, the condition evaluated against the
// values after the write, and the AND with the attribute rules.
func TestCatalogRuleKinds(t *testing.T) {
	physical := `{"op":"eq","field":"server_kind","value":"physical"}`
	defs := serverKindDefs()
	cases := []struct {
		name             string
		rule             fieldmeta.CatalogRule
		values, incoming map[string]any
		existing         map[string]any
		want             string
	}{
		{name: "required always, missing",
			rule:   catalogRule(t, "need-owner", "owner", fieldmeta.CatalogRequired, "", ""),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx"}, want: "owner/need-owner/required"},
		{name: "required always, present",
			rule:   catalogRule(t, "need-owner", "owner", fieldmeta.CatalogRequired, "", ""),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "owner": "ops"}},
		{name: "required with condition that holds",
			rule:   catalogRule(t, "need-room", "room", fieldmeta.CatalogRequired, physical, ""),
			values: map[string]any{"server_kind": "physical", "rack_unit": 4}, want: "room/need-room/required"},
		{name: "required with condition that does not hold",
			rule:   catalogRule(t, "need-room", "room", fieldmeta.CatalogRequired, physical, ""),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx"}},
		{name: "readonly refuses a changed value",
			rule:     catalogRule(t, "fixed-tag", "asset_tag", fieldmeta.CatalogReadonly, "", ""),
			existing: map[string]any{"asset_tag": "A-1"}, incoming: map[string]any{"asset_tag": "A-2"},
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "A-2"}, want: "asset_tag/fixed-tag/read_only"},
		{name: "readonly keeps an unchanged value",
			rule:     catalogRule(t, "fixed-tag", "asset_tag", fieldmeta.CatalogReadonly, "", ""),
			existing: map[string]any{"asset_tag": "A-1"}, incoming: map[string]any{"asset_tag": "A-1"},
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "A-1"}},
		{name: "readonly with condition that does not hold",
			rule:     catalogRule(t, "fixed-tag", "asset_tag", fieldmeta.CatalogReadonly, physical, ""),
			existing: map[string]any{"asset_tag": "A-1"}, incoming: map[string]any{"asset_tag": "A-2"},
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "A-2"}},
		{name: "validation refuses a value",
			rule:   catalogRule(t, "tag-form", "asset_tag", fieldmeta.CatalogValidation, "", `{"op":"pattern","pattern":"^A-[0-9]+$"}`),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "x"}, want: "asset_tag/tag-form/pattern"},
		{name: "validation with condition that holds",
			rule:   catalogRule(t, "small-rack", "rack_unit", fieldmeta.CatalogValidation, physical, `{"op":"range","max":10}`),
			values: map[string]any{"server_kind": "physical", "rack_unit": 12}, want: "rack_unit/small-rack/range.max"},
		{name: "validation with condition that does not hold",
			rule:   catalogRule(t, "tag-form", "asset_tag", fieldmeta.CatalogValidation, physical, `{"op":"pattern","pattern":"^A-[0-9]+$"}`),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "x"}},
		{name: "validation ignores an absent value",
			rule:   catalogRule(t, "tag-form", "asset_tag", fieldmeta.CatalogValidation, "", `{"op":"pattern","pattern":"^A-[0-9]+$"}`),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx"}},
		{name: "visible hides a field from the catalog's validation",
			rule:   catalogRule(t, "tag-visible", "asset_tag", fieldmeta.CatalogVisible, physical, ""),
			values: map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "x"}},
		{name: "visible does not hide a field the attribute rules require",
			rule:   catalogRule(t, "kind-visible", "server_kind", fieldmeta.CatalogVisible, `{"op":"eq","field":"owner","value":"ops"}`, ""),
			values: map[string]any{"hypervisor": "esx"}, want: "server_kind//"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := []fieldmeta.CatalogRule{tc.rule}
			if tc.rule.Kind == fieldmeta.CatalogVisible {
				// A validation of the same field, so hiding shows.
				catalog = append(catalog, catalogRule(t, "tag-form", tc.rule.AttributeKey, fieldmeta.CatalogValidation, "",
					`{"op":"pattern","pattern":"^A-[0-9]+$"}`))
			}
			incoming := tc.incoming
			if incoming == nil {
				incoming = tc.values
			}
			got := strings.Join(violationsOf(validateWithRules(defs, catalog, tc.values, incoming, tc.existing)), ",")
			if got != tc.want {
				t.Errorf("violations %q, want %q", got, tc.want)
			}
		})
	}

	// Required by an applying catalog rule: a visible rule does not hide
	// the field, the catalog validation still applies.
	catalog := []fieldmeta.CatalogRule{
		catalogRule(t, "tag-visible", "asset_tag", fieldmeta.CatalogVisible, physical, ""),
		catalogRule(t, "need-tag", "asset_tag", fieldmeta.CatalogRequired, "", ""),
		catalogRule(t, "tag-form", "asset_tag", fieldmeta.CatalogValidation, "", `{"op":"pattern","pattern":"^A-[0-9]+$"}`),
	}
	values := map[string]any{"server_kind": "virtual", "hypervisor": "esx", "asset_tag": "x"}
	if got := strings.Join(violationsOf(validateWithRules(defs, catalog, values, values, nil)), ","); got != "asset_tag/tag-form/pattern" {
		t.Errorf("visible on a required field: %q, want the validation to apply", got)
	}

	// AND with the attribute rules: both report, neither lifts the other.
	catalog = []fieldmeta.CatalogRule{catalogRule(t, "small-rack", "rack_unit", fieldmeta.CatalogValidation, "", `{"op":"range","max":10}`)}
	values = map[string]any{"server_kind": "physical", "rack_unit": 99}
	if got := strings.Join(violationsOf(validateWithRules(defs, catalog, values, values, nil)), ","); got != "rack_unit//,rack_unit/small-rack/range.max" {
		t.Errorf("attribute and catalog rule: %q, want both violations", got)
	}
	// A catalog visible rule does not lift the attribute rules.
	catalog = []fieldmeta.CatalogRule{catalogRule(t, "rack-visible", "rack_unit", fieldmeta.CatalogVisible, `{"op":"empty","field":"owner"}`, "")}
	values = map[string]any{"server_kind": "physical", "rack_unit": 99, "owner": "ops"}
	if got := strings.Join(violationsOf(validateWithRules(defs, catalog, values, values, nil)), ","); got != "rack_unit//" {
		t.Errorf("visible rule and attribute range: %q, want the attribute violation", got)
	}

	// The violation carries the rule's message.
	rule := catalogRule(t, "need-owner", "owner", fieldmeta.CatalogRequired, "", "")
	rule.Message = map[string]string{"de": "Verantwortlicher fehlt", "en": "owner missing"}
	ve, ok := AsValidationError(validateWithRules(nil, []fieldmeta.CatalogRule{rule}, map[string]any{}, nil, nil))
	if !ok || len(ve.Violations) != 1 || ve.Violations[0].Message == nil || (*ve.Violations[0].Message)["de"] != "Verantwortlicher fehlt" {
		t.Fatalf("violation without the rule message: %+v", ve)
	}
	raw, err := json.Marshal(ve.Violations[0])
	if err != nil || !strings.Contains(string(raw), `"rule":"need-owner"`) || !strings.Contains(string(raw), `"message":{"de":"Verantwortlicher fehlt","en":"owner missing"}`) {
		t.Errorf("violation JSON %s (%v)", raw, err)
	}
	if raw, _ := json.Marshal(Violation{Field: "a", Detail: "b"}); string(raw) != `{"field":"a","detail":"b"}` {
		t.Errorf("attribute violation JSON %s, want unchanged", raw)
	}
}

// TestInvalidObservedWithCatalogRule covers WP-257 (CI-03, MET-11):
// discovery drops an observed value a validation rule of the catalog
// refuses while its condition holds on the observed values; required,
// readonly and visible do not apply to observations.
func TestInvalidObservedWithCatalogRule(t *testing.T) {
	catalog := []fieldmeta.CatalogRule{
		catalogRule(t, "host-form", "hostname", fieldmeta.CatalogValidation, `{"op":"eq","field":"os_name","value":"linux"}`,
			`{"op":"pattern","pattern":"^[a-z]+$"}`),
		catalogRule(t, "need-owner", "owner", fieldmeta.CatalogRequired, "", ""),
		catalogRule(t, "fixed-model", "model", fieldmeta.CatalogReadonly, "", ""),
		catalogRule(t, "model-visible", "model", fieldmeta.CatalogVisible, `{"op":"eq","field":"os_name","value":"none"}`, ""),
	}
	if got := invalidObservedWith(nil, catalog, map[string]any{"hostname": "host01", "os_name": "linux", "model": "x"}); strings.Join(got, ",") != "hostname" {
		t.Errorf("invalidObserved = %v, want hostname", got)
	}
	if got := invalidObservedWith(nil, catalog, map[string]any{"hostname": "host01", "os_name": "windows"}); len(got) != 0 {
		t.Errorf("invalidObserved with a condition that does not hold = %v, want none", got)
	}

	// The service resolves the rules with the definitions, also for new
	// CIs once per type (ObservedRules).
	svc := NewService(NewMemoryRepository()).WithFieldResolver(stubRuleResolver{catalog: catalog})
	values := map[string]any{"hostname": "host01", "os_name": "linux"}
	got, err := svc.InvalidObserved(context.Background(), "org-1", testServerType, "", values)
	if err != nil || strings.Join(got, ",") != "hostname" {
		t.Errorf("InvalidObserved = %v, %v; want hostname", got, err)
	}
	check, err := svc.ObservedRules(context.Background(), "org-1", testServerType)
	if err != nil {
		t.Fatal(err)
	}
	if got := check(values); strings.Join(got, ",") != "hostname" {
		t.Errorf("ObservedRules = %v, want hostname", got)
	}
}

// stubRuleResolver is a FieldResolver and RuleResolver with fixed
// definitions and catalog rules.
type stubRuleResolver struct {
	defs    []fieldmeta.FieldDefinition
	catalog []fieldmeta.CatalogRule
}

func (s stubRuleResolver) ResolveFields(context.Context, string, string, string) ([]fieldmeta.FieldDefinition, error) {
	return s.defs, nil
}

func (s stubRuleResolver) ResolveRules(context.Context, string, string, string) ([]fieldmeta.CatalogRule, error) {
	return s.catalog, nil
}

func (s stubRuleResolver) ResolveFieldsAndRules(context.Context, string, string, string,
	*[]citype.CITemplate) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	return s.defs, s.catalog, nil
}

// TestServiceAppliesCatalogRules covers WP-257: WithFieldResolver finds the
// RuleResolver, Create and Update refuse a violation of a catalog rule
// with the rule, code and message, and the conditions see the typed
// columns after the write.
func TestServiceAppliesCatalogRules(t *testing.T) {
	ctx := context.Background()
	rule := catalogRule(t, "need-owner", "owner", fieldmeta.CatalogRequired, `{"op":"eq","field":"hostname","value":"core"}`, "")
	rule.Message = map[string]string{"en": "core hosts need an owner"}
	repo := NewMemoryRepository()
	svc := NewService(repo).WithFieldResolver(stubRuleResolver{catalog: []fieldmeta.CatalogRule{rule}})

	item := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "srv-257", Hostname: "core", Attributes: map[string]any{}}
	err := svc.Create(ctx, item)
	if got := strings.Join(violationsOf(err), ","); got != "owner/need-owner/required" {
		t.Fatalf("Create: %v (%q), want the catalog violation", err, got)
	}
	item.Hostname = "edge"
	if err = svc.Create(ctx, item); err != nil {
		t.Fatalf("Create with a condition that does not hold: %v", err)
	}
	core := "core"
	_, err = svc.Update(ctx, "org-1", item.ID, UpdateRequest{Hostname: &core})
	if got := strings.Join(violationsOf(err), ","); got != "owner/need-owner/required" {
		t.Fatalf("Update: %v (%q), want the catalog violation", err, got)
	}
	if _, err = svc.Update(ctx, "org-1", item.ID, UpdateRequest{Hostname: &core, Attributes: map[string]any{"owner": "ops"}}); err != nil {
		t.Fatalf("Update with the owner: %v", err)
	}
}

// TestValidateAttributesForTestRun covers the exported wrapper of the test
// run of metamodel drafts (WP-721, MET-53): it reports missing required
// fields, values outside the value domain and violated catalog rules, does
// not check hidden fields and reports no read-only violation (there are no
// incoming values).
func TestValidateAttributesForTestRun(t *testing.T) {
	defs := append(serverKindDefs(), fieldmeta.FieldDefinition{
		Name: "asset_tag", DataType: fieldmeta.TypeText,
		Conditional: &fieldmeta.ConditionalRules{
			ReadOnlyWhen: []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
		},
	})
	catalog := []fieldmeta.CatalogRule{
		catalogRule(t, "need-owner", "owner", fieldmeta.CatalogRequired, "", ""),
		catalogRule(t, "fixed-tag", "asset_tag", fieldmeta.CatalogReadonly, "", ""),
		catalogRule(t, "tag-form", "asset_tag", fieldmeta.CatalogValidation, "", `{"op":"pattern","pattern":"^A-[0-9]+$"}`),
	}
	describe := func(vs []Violation) string {
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, v.Field+"/"+v.Rule+"/"+v.Code)
		}
		return strings.Join(out, ",")
	}

	// A missing required field (server_kind) and a violated required rule
	// (owner); the read-only definition and rule report nothing, the
	// stored value of the read-only field is not "changed".
	if got := describe(ValidateAttributes(defs, catalog, map[string]any{"asset_tag": "A-1"})); got != "owner/need-owner/required,server_kind//" {
		t.Errorf("missing values: %s", got)
	}
	// A value outside the range, one outside the enum, a value the
	// validation rule refuses.
	if got := describe(ValidateAttributes(defs, nil, map[string]any{"server_kind": "physical", "rack_unit": 99})); got != "rack_unit//" {
		t.Errorf("range: %s", got)
	}
	if got := describe(ValidateAttributes(defs, nil, map[string]any{"server_kind": "cloud"})); !strings.HasPrefix(got, "server_kind/") {
		t.Errorf("enum: %s", got)
	}
	values := map[string]any{"server_kind": "virtual", "hypervisor": "esx", "owner": "ops", "asset_tag": "x"}
	if got := describe(ValidateAttributes(defs, catalog, values)); got != "asset_tag/tag-form/pattern" {
		t.Errorf("validation rule: %s", got)
	}
	// rack_unit is hidden for a virtual server: its invalid value is not
	// checked.
	values = map[string]any{"server_kind": "virtual", "hypervisor": "esx", "owner": "ops", "rack_unit": 99}
	if vs := ValidateAttributes(defs, catalog, values); vs != nil {
		t.Errorf("hidden field checked: %s", describe(vs))
	}
}

// TestStoredValuesOfATestRun: the stored attributes of a CI are checked in
// their read view (instance attributes under their names, projected
// namespaces left out) with the non-empty typed columns taking precedence.
func TestStoredValuesOfATestRun(t *testing.T) {
	values := StoredValues(map[string]any{
		"hostname": "stored", "role": "web", NamespaceInstance: map[string]any{"rack": "R1"},
		NamespaceObserved: map[string]any{"role": map[string]any{"value": "db"}},
	}, map[string]string{"hostname": "typed", "fqdn": "", "unknown": "x"})
	want := map[string]any{"hostname": "typed", "role": "web", "rack": "R1"}
	if fmt.Sprint(values) != fmt.Sprint(want) {
		t.Errorf("StoredValues = %v, want %v", values, want)
	}
	if names := TypedColumnNames(); len(names) != len(typedColumns) || names[0] != "firmware_version" {
		t.Errorf("TypedColumnNames = %v", names)
	}
}
