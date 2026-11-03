package ci

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// FieldResolver returns the effective field definitions that apply to a CI:
// global attributes, the attributes of its CI type and — when ciID is
// non-empty — the instance-specific attributes of that single CI (spec §3),
// composed by the rules of MET-16.
type FieldResolver interface {
	ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error)
}

// TemplateFieldResolver is the optional part of a FieldResolver that
// composes the field definitions of a CI with a given template list in
// place of the stored one (MET-16, WP-710): the list a creation (POST /cis
// with templates) or a replacement (PUT /cis/{id}/templates) writes, so the
// write is validated against the schema it leads to. It checks the list
// like the write (422 on templates[i]). citype.PGRepository implements it;
// the signature of FieldResolver and its test stubs stay unchanged. The
// parameter type is citype's (no import cycle).
type TemplateFieldResolver interface {
	ResolveFieldsWithTemplates(ctx context.Context, orgID, ciTypeID, ciID string, templates []citype.CITemplate) ([]fieldmeta.FieldDefinition, error)
}

// RuleResolver is the optional part of a FieldResolver that also returns
// the active catalog rules of a CI (metamodel_rule, WP-716, WP-257):
// ResolveRules the rules alone, ResolveFieldsAndRules the field
// definitions and the rules in one pass (templates as in
// TemplateFieldResolver when set), so a write reads its rules without a
// second resolution of the layers (NFR-03). citype.PGRepository and
// citype.MemoryRepository implement it, the latter without rules;
// WithFieldResolver finds it by type assertion.
type RuleResolver interface {
	ResolveRules(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.CatalogRule, error)
	ResolveFieldsAndRules(ctx context.Context, orgID, ciTypeID, ciID string,
		templates *[]citype.CITemplate) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error)
}

// CodeReadOnly is the code of a violation of a readonly catalog rule
// (WP-257): the write changes a value the rule keeps.
const CodeReadOnly = "read_only"

// ErrTemplatesUnavailable: the field resolver keeps no template
// assignments (memory mode without a database); the handlers answer 503.
var ErrTemplatesUnavailable = errors.New("template assignments need the database")

// resolveFields resolves the field definitions; definitions that cannot be
// composed (MET-16) reject the write as a violation of the attribute.
func resolveFields(ctx context.Context, fields FieldResolver, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	defs, _, err := resolveWithRules(ctx, fields, nil, orgID, ciTypeID, ciID, nil)
	return defs, err
}

// resolveWithRules resolves the field definitions and, with a
// RuleResolver, the catalog rules of a CI in one pass, with the template
// list templates in place of the stored one when it is set
// (TemplateFieldResolver); a refused entry of the list is a violation of
// templates[i], definitions that cannot be composed (MET-16) are a
// violation of the attribute.
func resolveWithRules(ctx context.Context, fields FieldResolver, catalog RuleResolver, orgID, ciTypeID, ciID string,
	templates *[]citype.CITemplate) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	var (
		defs  []fieldmeta.FieldDefinition
		found []fieldmeta.CatalogRule
		err   error
	)
	resolver, withTemplates := fields.(TemplateFieldResolver)
	switch {
	case templates != nil && !withTemplates:
		return nil, nil, ErrTemplatesUnavailable
	case catalog != nil:
		defs, found, err = catalog.ResolveFieldsAndRules(ctx, orgID, ciTypeID, ciID, templates)
	case templates != nil:
		defs, err = resolver.ResolveFieldsWithTemplates(ctx, orgID, ciTypeID, ciID, *templates)
	default:
		defs, err = fields.ResolveFields(ctx, orgID, ciTypeID, ciID)
	}
	var conflict interface {
		CompositionKey() string
		CompositionCode() string
	}
	if errors.As(err, &conflict) {
		return nil, nil, &ValidationError{Violations: []Violation{{Field: conflict.CompositionKey(), Detail: err.Error(),
			Code: conflict.CompositionCode()}}}
	}
	if err != nil {
		return nil, nil, templateViolation(err)
	}
	return defs, found, nil
}

// templateViolation turns the refusal of a template list entry (a
// *api.FieldError on templates[i], citype.ResolveTemplateRefsTx) into the
// violation of that field; any other error stays.
func templateViolation(err error) error {
	var fe *api.FieldError
	if errors.As(err, &fe) && strings.HasPrefix(fe.Field, "templates[") {
		return &ValidationError{Violations: []Violation{{Field: fe.Field, Detail: fe.Message}}}
	}
	return err
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
	// Code names the failing rule in machine-readable form, e.g. the code
	// of a composition conflict (MET-16) or of a catalog rule (WP-257: the
	// code of internal/rules, rules.CodeRequired or CodeReadOnly); omitted
	// when empty.
	Code string `json:"code,omitempty"`
	// Rule is the key of the violated catalog rule (WP-257); omitted for
	// the attribute rules.
	Rule string `json:"rule,omitempty"`
	// Message is the i18n message of the violated catalog rule (language
	// → text), when it has one. A pointer keeps Violation comparable.
	Message *map[string]string `json:"message,omitempty"`
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

// PublicMessage implements jobs.PublicError: a failed job shows the
// violations (F-372).
func (e *ValidationError) PublicMessage() string { return e.Error() }

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
	return validateWithRules(defs, nil, values, incoming, existing)
}

// validateWithRules is validateAttributes with the active catalog rules of
// the CI (WP-257, MET-10): their violations are AND-ed with those of the
// attribute rules (catalogViolations) and reported together.
func validateWithRules(defs []fieldmeta.FieldDefinition, catalog []fieldmeta.CatalogRule,
	values, incoming, existing map[string]any) error {
	if values == nil {
		values = map[string]any{}
	}
	violations := append(attributeViolations(defs, values, incoming, existing),
		catalogViolations(defs, catalog, values, incoming, existing)...)
	if len(violations) == 0 {
		return nil
	}
	sort.SliceStable(violations, func(i, j int) bool { return violations[i].Field < violations[j].Field })
	return &ValidationError{Violations: violations}
}

// catalogViolations evaluates the active catalog rules of a CI (WP-257)
// against the values after the write. A rule applies while its condition
// holds (rules.EvaluateCondition; no condition: always). Per kind:
// required asks for a value (rules.CodeRequired); readonly refuses a
// changed incoming value, as state.ReadOnly does (CodeReadOnly);
// validation checks the value with rules.Evaluate (its code); visible
// hides the field while its condition does not hold. The rules are AND-ed
// with the attribute rules and lift none of them: a hidden field is
// exempt from the catalog's readonly and validation rules only, the
// attribute rules still apply, and a field that is required in the
// current state (by its attribute rules or an applying required rule) is
// never hidden by a visible rule. A field the attribute rules hide counts
// as hidden for the catalog's readonly and validation rules too.
func catalogViolations(defs []fieldmeta.FieldDefinition, catalog []fieldmeta.CatalogRule,
	values, incoming, existing map[string]any) []Violation {
	if len(catalog) == 0 {
		return nil
	}
	required, hidden := map[string]bool{}, map[string]bool{}
	for i := range defs {
		state := fieldmeta.Evaluate(defs[i], values)
		switch {
		case !state.Visible:
			hidden[defs[i].Name] = true
		case state.Required:
			required[defs[i].Name] = true
		}
	}
	applies := make([]bool, len(catalog))
	for i := range catalog {
		applies[i] = catalog[i].Applies(values)
		if applies[i] && catalog[i].Kind == fieldmeta.CatalogRequired {
			required[catalog[i].AttributeKey] = true
		}
	}
	for i := range catalog {
		if catalog[i].Kind == fieldmeta.CatalogVisible && !applies[i] {
			hidden[catalog[i].AttributeKey] = true
		}
	}
	var out []Violation
	for i := range catalog {
		rule := &catalog[i]
		key := rule.AttributeKey
		if !applies[i] {
			continue
		}
		value, present := values[key]
		exempt := hidden[key] && !required[key]
		switch rule.Kind {
		case fieldmeta.CatalogRequired:
			if !present || isBlank(value) {
				out = append(out, catalogViolation(rule, rules.CodeRequired, "field is required"))
			}
		case fieldmeta.CatalogReadonly:
			if in, changed := incoming[key]; changed && !exempt && !sameValue(existing[key], in) {
				out = append(out, catalogViolation(rule, CodeReadOnly, "field is read-only in the current state"))
			}
		case fieldmeta.CatalogValidation:
			if exempt || rule.AST == nil {
				continue
			}
			for _, v := range rules.Evaluate(rule.AST, value, present) {
				out = append(out, catalogViolation(rule, v.Code, v.Message))
			}
		}
	}
	return out
}

// catalogViolation is the violation of the catalog rule rule.
func catalogViolation(rule *fieldmeta.CatalogRule, code, detail string) Violation {
	v := Violation{Field: rule.AttributeKey, Detail: detail, Code: code, Rule: rule.Key}
	if len(rule.Message) > 0 {
		message := rule.Message
		v.Message = &message
	}
	return v
}

// attributeViolations returns the violations of the attribute rules (see
// validateAttributes), unsorted.
func attributeViolations(defs []fieldmeta.FieldDefinition, values, incoming, existing map[string]any) []Violation {
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
				violations = append(violations, Violation{Field: def.Name, Detail: "field is read-only in the current state"})
				continue
			}
		}

		if state.Required && (!present || isBlank(value)) {
			violations = append(violations, Violation{Field: def.Name, Detail: "field is required"})
			continue
		}
		if !present || isBlank(value) {
			continue
		}
		if err := fieldmeta.ValidateValue(def, value); err != nil {
			violations = append(violations, Violation{Field: def.Name, Detail: trimFieldPrefix(def.Name, err.Error())})
			continue
		}
		if len(state.AllowedValues) > 0 && !allowedValue(state.AllowedValues, value) {
			violations = append(violations, Violation{
				Field:  def.Name,
				Detail: "value is not allowed here, expected one of: " + strings.Join(state.AllowedValues, ", "),
			})
		}
	}
	return violations
}

// Details of a reference value refused by checkReferences (MET-12). A
// target the caller cannot see is not found, exactly as one that does not
// exist.
const (
	referenceNotFound    = "reference target not found"
	referenceOtherClient = "reference target belongs to another client"
)

// referenceUse is one id a reference attribute of a write names.
type referenceUse struct {
	field, id string
}

// checkReferences checks the reference values of a write against their
// targets (MET-12, WP-254): the target must be available (module gate,
// E-03), exist, be visible in the caller's scope (lookup, under row level
// security) and be compatible with the client of the CI, which is the case
// when the CI is org-wide, the target is org-wide (TEN-02) or both have the
// same client. Only values the write sets or changes are checked (keys of
// incoming whose value differs from existing), all of them when the write
// changes the client; a hidden field is skipped as in validateAttributes.
// The lookup runs one query per target for all ids of the write; without
// a lookup (memory mode) only the module gate applies.
func checkReferences(ctx context.Context, lookup fieldmeta.ReferenceLookup, orgID string, defs []fieldmeta.FieldDefinition,
	scope writeScope, values, incoming, existing map[string]any) error {
	var violations []Violation
	byTarget := map[string][]referenceUse{}
	for i := range defs {
		def := &defs[i]
		target := fieldmeta.ReferenceTargetOf(def)
		if target == "" {
			continue
		}
		value, present := values[def.Name]
		if !present || isBlank(value) {
			continue
		}
		if !scope.clientChanged {
			in, set := incoming[def.Name]
			if !set || sameValue(existing[def.Name], in) {
				continue
			}
		}
		if !fieldmeta.Evaluate(*def, values).Visible {
			continue
		}
		if !fieldmeta.ReferenceTargetAvailable(target) {
			violations = append(violations, Violation{Field: def.Name, Detail: "reference target " + target + " not available"})
			continue
		}
		if lookup == nil {
			continue
		}
		for _, id := range referenceIDs(value) {
			byTarget[target] = append(byTarget[target], referenceUse{field: def.Name, id: strings.ToLower(id)})
		}
	}
	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	refused := map[string]bool{}
	for _, target := range targets {
		uses := byTarget[target]
		ids := make([]string, 0, len(uses))
		for _, u := range uses {
			if !slices.Contains(ids, u.id) {
				ids = append(ids, u.id)
			}
		}
		found, err := lookup.LookupReferences(ctx, orgID, target, ids)
		if err != nil {
			return fmt.Errorf("check references to %s: %w", target, err)
		}
		for _, u := range uses {
			if refused[u.field] {
				continue
			}
			client, ok := found[u.id]
			switch {
			case !ok:
				violations = append(violations, Violation{Field: u.field, Detail: referenceNotFound})
			case scope.clientID != "" && client != "" && !strings.EqualFold(client, scope.clientID):
				violations = append(violations, Violation{Field: u.field, Detail: referenceOtherClient})
			default:
				continue
			}
			refused[u.field] = true
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Field < violations[j].Field })
	return &ValidationError{Violations: violations}
}

// referenceIDs returns the ids of a reference value: the value or, for a
// list (multi_value), its elements. The form check has run before.
func referenceIDs(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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

// overridden reads the fields under a manual override and the value reported
// last for each from the projected namespaces of stored attributes (REC-12).
func overridden(stored map[string]any) map[string]*Observation {
	overrides, _ := stored[NamespaceOverrides].(map[string]any)
	if len(overrides) == 0 {
		return nil
	}
	observed, _ := stored[NamespaceObserved].(map[string]any)
	out := make(map[string]*Observation, len(overrides))
	for field := range overrides {
		out[field] = nil
		if entry, ok := observed[field].(map[string]any); ok {
			out[field] = &Observation{Value: entry["value"]}
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
			violations = append(violations, Violation{Field: name, Detail: trimFieldPrefix(name, err.Error())})
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Field < violations[j].Field })
	return &ValidationError{Violations: violations}
}

// InvalidTypedColumns returns the typed CI columns among values whose value
// violates the column's MET-11 data type, sorted: the check
// validateTypedColumns applies to a manual write. Discovery drops these
// values before it resolves identities, so a malformed identity value
// never reaches a query.
func InvalidTypedColumns(values map[string]any) []string {
	return invalidObserved(nil, values)
}

// InvalidObserved returns the fields whose observed values (typed columns
// and attributes, by field name) violate the field metadata of the CI
// type or, for a typed column, its own data type: the same value rules as
// a manual write (CI-03, MET-11). The catalog rules of kind validation
// apply as well (WP-257), their condition evaluated against the observed
// values; required, readonly and visible do not apply to an observation,
// nor do required fields, an observation may lack them. The rules are
// resolved with the field definitions in one pass (RuleResolver).
// Discovery drops the returned values instead of storing them.
func (s *Service) InvalidObserved(ctx context.Context, orgID, ciTypeID, ciID string, values map[string]any) ([]string, error) {
	if s.fields == nil || len(values) == 0 {
		return invalidObserved(nil, values), nil
	}
	defs, catalog, err := resolveWithRules(ctx, s.fields, s.catalog, orgID, ciTypeID, ciID, nil)
	if err != nil {
		return nil, err
	}
	return invalidObservedWith(defs, catalog, values), nil
}

// ObservedRules resolves the field definitions and the catalog rules of a
// CI type for new CIs once and returns the check InvalidObserved applies to
// the observed values of a new CI of that type: a batch of discovered
// devices resolves them once per type instead of once per device (NFR-03,
// WP-333).
func (s *Service) ObservedRules(ctx context.Context, orgID, ciTypeID string) (func(values map[string]any) []string, error) {
	if s.fields == nil {
		return func(values map[string]any) []string { return invalidObserved(nil, values) }, nil
	}
	defs, catalog, err := resolveWithRules(ctx, s.fields, s.catalog, orgID, ciTypeID, "", nil)
	if err != nil {
		return nil, err
	}
	return func(values map[string]any) []string { return invalidObservedWith(defs, catalog, values) }, nil
}

// invalidObserved returns the fields whose observed value violates their
// definition in defs or, for a typed column, the column's data type (an
// implicit definition, as validateTypedColumns checks a manual write),
// sorted and each once.
func invalidObserved(defs []fieldmeta.FieldDefinition, values map[string]any) []string {
	return invalidObservedWith(defs, nil, values)
}

// invalidObservedWith is invalidObserved with the catalog rules of the CI
// (WP-257): an observed value a validation rule refuses while its
// condition holds on the observed values is invalid as well.
func invalidObservedWith(defs []fieldmeta.FieldDefinition, catalog []fieldmeta.CatalogRule, values map[string]any) []string {
	invalid := map[string]bool{}
	for name, dataType := range typedColumns {
		value, ok := values[name]
		if !ok || isBlank(value) {
			continue
		}
		if err := fieldmeta.ValidateValue(fieldmeta.FieldDefinition{Name: name, DataType: dataType}, value); err != nil {
			invalid[name] = true
		}
	}
	for i := range defs {
		def := &defs[i]
		value, ok := values[def.Name]
		if !ok || isBlank(value) || invalid[def.Name] {
			continue
		}
		if err := fieldmeta.ValidateValue(*def, value); err != nil {
			invalid[def.Name] = true
		}
	}
	for i := range catalog {
		rule := &catalog[i]
		if rule.Kind != fieldmeta.CatalogValidation || rule.AST == nil || invalid[rule.AttributeKey] {
			continue
		}
		value, ok := values[rule.AttributeKey]
		if !ok || isBlank(value) || !rule.Applies(values) {
			continue
		}
		if len(rules.Evaluate(rule.AST, value, true)) > 0 {
			invalid[rule.AttributeKey] = true
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	out := make([]string, 0, len(invalid))
	for name := range invalid {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
