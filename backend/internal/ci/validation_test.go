package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
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
		if err := fieldmeta.ValidateDefinition(fieldmeta.FieldDefinition{Name: ns, DataType: fieldmeta.TypeText}); err == nil {
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
