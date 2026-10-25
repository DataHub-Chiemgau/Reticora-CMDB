package ci

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// FieldResolver returns the effective field definitions that apply to a CI:
// global attributes, the attributes of its CI type and — when ciID is
// non-empty — the instance-specific attributes of that single CI (spec §3),
// composed by the rules of MET-16.
type FieldResolver interface {
	ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error)
}

// resolveFields resolves the field definitions; definitions that cannot be
// composed (MET-16) reject the write as a violation of the attribute.
func resolveFields(ctx context.Context, fields FieldResolver, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	defs, err := fields.ResolveFields(ctx, orgID, ciTypeID, ciID)
	var conflict interface{ CompositionKey() string }
	if errors.As(err, &conflict) {
		return nil, &ValidationError{Violations: []Violation{{Field: conflict.CompositionKey(), Detail: err.Error()}}}
	}
	return defs, err
}

// ValidationError reports one or more attribute values that violate the field
// metadata of the CI type. Handlers map it onto HTTP 422.
type ValidationError struct {
	Violations []Violation
}

// Violation is a single rejected field.
type Violation struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// validation: the violations of the field metadata (fieldmeta) and the
// fixed rule texts of the repository.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s: %s", v.Field, v.Detail))
	}
	return strings.Join(parts, "; ")
}

// AsValidationError reports whether err is (or wraps) a *ValidationError.
// validation: see ValidationError.Error.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

// validateAttributes enforces the field metadata server-side. UI-side
// evaluation of the same rules is a convenience only: every rule is re-checked
// here so a direct API call cannot bypass required fields, value domains,
// conditional requirements or read-only fields.
//
// values is the effective attribute map after the write (existing attributes
// merged with the incoming patch); incoming lists the attribute names the
// request actually carries, which is what read-only enforcement keys on.
// Attributes without a definition are left untouched: discovery and
// integrations legitimately store attributes the metadata does not describe.
func validateAttributes(defs []fieldmeta.FieldDefinition, values map[string]any, incoming map[string]any, existing map[string]any) error {
	if len(defs) == 0 {
		return nil
	}
	if values == nil {
		values = map[string]any{}
	}
	var violations []Violation
	for _, def := range defs {
		state := fieldmeta.Evaluate(def, values)
		value, present := values[def.Name]

		if !state.Visible {
			// A hidden field carries no requirement and its stored value is
			// not asserted against the rules of the branch it belongs to.
			continue
		}

		if state.ReadOnly {
			if _, changed := incoming[def.Name]; changed && !sameValue(existing[def.Name], incoming[def.Name]) {
				violations = append(violations, Violation{def.Name, "field is read-only in the current state"})
				continue
			}
		}

		if state.Required && (!present || isBlank(value)) {
			violations = append(violations, Violation{def.Name, "field is required"})
			continue
		}
		if !present || isBlank(value) {
			continue
		}
		if err := fieldmeta.ValidateValue(def, value); err != nil {
			violations = append(violations, Violation{def.Name, trimFieldPrefix(def.Name, err.Error())})
			continue
		}
		if len(state.AllowedValues) > 0 && !allowedValue(state.AllowedValues, value) {
			violations = append(violations, Violation{
				def.Name,
				"value is not allowed here, expected one of: " + strings.Join(state.AllowedValues, ", "),
			})
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Field < violations[j].Field })
	return &ValidationError{Violations: violations}
}

// trimFieldPrefix removes the "field <name>: " prefix fieldmeta adds, because
// the field name is already carried by the violation.
func trimFieldPrefix(name, msg string) string {
	return strings.TrimPrefix(msg, "field "+name+": ")
}

func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func sameValue(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func allowedValue(allowed []string, value any) bool {
	check := func(v any) bool {
		s := fmt.Sprintf("%v", v)
		for _, a := range allowed {
			if a == s {
				return true
			}
		}
		return false
	}
	if list, ok := value.([]any); ok {
		for _, v := range list {
			if !check(v) {
				return false
			}
		}
		return true
	}
	return check(value)
}

// Reserved namespaces in attributes (CI-10, GLO-13). attributes holds the
// business keys without prefix and these four namespaces: _overrides,
// _observed and _provenance are projected from the field provenance by the
// database, _instance holds the values of the CI's instance attributes
// (MET-14). Only reconciliation and the override code write them; as a key
// of a write they are rejected.
const (
	NamespaceOverrides  = "_overrides"
	NamespaceObserved   = "_observed"
	NamespaceProvenance = "_provenance"
	NamespaceInstance   = "_instance"
)

// ReservedNamespaces lists the four reserved attribute keys.
var ReservedNamespaces = []string{NamespaceOverrides, NamespaceObserved, NamespaceProvenance, NamespaceInstance}

// IsReservedKey reports whether key is a reserved namespace (GLO-13).
func IsReservedKey(key string) bool {
	return fieldmeta.ReservedNames[key]
}

// ReservedKeyError reports a write that uses reserved attribute keys; the
// handlers answer 400 with the field.
type ReservedKeyError struct {
	Fields []string
}

// validation: a fixed rule text with the refused attribute keys.
func (e *ReservedKeyError) Error() string {
	return strings.Join(e.Fields, ", ") + ": reserved namespace, not allowed as an attribute key (GLO-13)"
}

// CheckReservedKeys rejects attributes that use a reserved namespace as a
// key; prefix names the attribute map in the error ("attributes").
func CheckReservedKeys(prefix string, attributes map[string]any) error {
	var fields []string
	for key := range attributes {
		if IsReservedKey(key) {
			fields = append(fields, prefix+"."+key)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	sort.Strings(fields)
	return &ReservedKeyError{Fields: fields}
}

// presentAttributes is the read view of stored attributes: the projected
// namespaces are left out (the resolved per-field view is the effective
// field view, REC-10) and the instance attributes appear under their names.
func presentAttributes(stored map[string]any) map[string]any {
	out := make(map[string]any, len(stored))
	for k, v := range stored {
		if !IsReservedKey(k) {
			out[k] = v
		}
	}
	if instance, ok := stored[NamespaceInstance].(map[string]any); ok {
		for k, v := range instance {
			out[k] = v
		}
	}
	return out
}

// storedAttributes is the write form of attributes: the values of the
// instance attributes named in instance move into _instance. The projected
// namespaces are kept by the statement that writes the row.
func storedAttributes(attributes map[string]any, instance []string) map[string]any {
	out := make(map[string]any, len(attributes))
	nested := map[string]any{}
	isInstance := make(map[string]bool, len(instance))
	for _, name := range instance {
		isInstance[name] = true
	}
	for k, v := range attributes {
		switch {
		case IsReservedKey(k):
		case isInstance[k]:
			nested[k] = v
		default:
			out[k] = v
		}
	}
	if len(nested) > 0 {
		out[NamespaceInstance] = nested
	}
	return out
}

// typedColumns are the typed CI columns (CI-01) by name with their data
// type (MET-11). A field definition of the same name applies to the
// column, so a typed hostname fulfils a required "hostname" definition;
// the column's own data type is checked on every write.
var typedColumns = map[string]string{
	"name": fieldmeta.TypeText, "manufacturer": fieldmeta.TypeText, "model": fieldmeta.TypeText,
	"serial_number": fieldmeta.TypeText, "hardware_uuid": fieldmeta.TypeUUID,
	"management_ip": fieldmeta.TypeIP, "primary_mac": fieldmeta.TypeMAC, "hostname": fieldmeta.TypeText,
	"fqdn": fieldmeta.TypeText, "os_name": fieldmeta.TypeText, "os_version": fieldmeta.TypeText,
	"firmware_version": fieldmeta.TypeText, "sys_object_id": fieldmeta.TypeText,
}

// typedValues returns the non-empty typed columns of a CI.
func typedValues(item *Item) map[string]any {
	out := map[string]any{}
	for name, value := range map[string]string{
		"name": item.Name, "manufacturer": item.Manufacturer, "model": item.Model,
		"serial_number": item.SerialNumber, "hardware_uuid": item.HardwareUUID,
		"management_ip": item.ManagementIP, "primary_mac": item.PrimaryMAC, "hostname": item.Hostname,
		"fqdn": item.FQDN, "os_name": item.OSName, "os_version": item.OSVersion,
		"firmware_version": item.FirmwareVersion, "sys_object_id": item.SysObjectID,
	} {
		if value != "" {
			out[name] = value
		}
	}
	return out
}

// typedPatch returns the typed columns an update sets ("" clears).
func typedPatch(req *UpdateRequest) map[string]any {
	out := map[string]any{}
	for name, value := range map[string]*string{
		"name": req.Name, "manufacturer": req.Manufacturer, "model": req.Model,
		"serial_number": req.SerialNumber, "hardware_uuid": req.HardwareUUID,
		"management_ip": req.ManagementIP, "primary_mac": req.PrimaryMAC, "hostname": req.Hostname,
		"fqdn": req.FQDN, "os_name": req.OSName, "os_version": req.OSVersion,
		"firmware_version": req.FirmwareVersion, "sys_object_id": req.SysObjectID,
	} {
		if value != nil {
			out[name] = *value
		}
	}
	return out
}

// withTyped is the value map the field metadata is checked against: the
// attributes, with the typed columns taking precedence for their names.
func withTyped(attributes, typed map[string]any) map[string]any {
	out := make(map[string]any, len(attributes)+len(typed))
	for k, v := range attributes {
		out[k] = v
	}
	for k, v := range typed {
		out[k] = v
	}
	return out
}

// validateTypedColumns checks the data type of every typed column a write
// sets.
func validateTypedColumns(typed map[string]any) error {
	var violations []Violation
	for name, value := range typed {
		if isBlank(value) {
			continue
		}
		def := fieldmeta.FieldDefinition{Name: name, DataType: typedColumns[name]}
		if err := fieldmeta.ValidateValue(def, value); err != nil {
			violations = append(violations, Violation{name, trimFieldPrefix(name, err.Error())})
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Field < violations[j].Field })
	return &ValidationError{Violations: violations}
}

// InvalidObserved returns the fields whose observed values (typed columns
// and attributes, by field name) violate the field metadata of the CI
// type: the same value rules as a manual write (CI-03, MET-11). Required
// fields do not apply, an observation may lack them. Discovery drops the
// returned values instead of storing them.
func (s *Service) InvalidObserved(ctx context.Context, orgID, ciTypeID, ciID string, values map[string]any) ([]string, error) {
	if s.fields == nil || len(values) == 0 {
		return nil, nil
	}
	defs, err := resolveFields(ctx, s.fields, orgID, ciTypeID, ciID)
	if err != nil {
		return nil, err
	}
	return invalidObserved(defs, values), nil
}

// ObservedRules resolves the field definitions of a CI type for new CIs once
// and returns the check InvalidObserved applies to the observed values of a
// new CI of that type: a batch of discovered devices resolves the
// definitions once per type instead of once per device (NFR-03, WP-333).
func (s *Service) ObservedRules(ctx context.Context, orgID, ciTypeID string) (func(values map[string]any) []string, error) {
	if s.fields == nil {
		return func(map[string]any) []string { return nil }, nil
	}
	defs, err := resolveFields(ctx, s.fields, orgID, ciTypeID, "")
	if err != nil {
		return nil, err
	}
	return func(values map[string]any) []string { return invalidObserved(defs, values) }, nil
}

// invalidObserved returns the fields of defs whose observed value violates
// the definition, sorted.
func invalidObserved(defs []fieldmeta.FieldDefinition, values map[string]any) []string {
	var invalid []string
	for i := range defs {
		def := &defs[i]
		value, ok := values[def.Name]
		if !ok || isBlank(value) {
			continue
		}
		if err := fieldmeta.ValidateValue(*def, value); err != nil {
			invalid = append(invalid, def.Name)
		}
	}
	sort.Strings(invalid)
	return invalid
}
