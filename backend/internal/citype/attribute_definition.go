package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// AttributeDefinition is one row of attribute_definition (MET-10, DB-03):
// the single source of attribute validation and indexing. A definition
// belongs to a CI type (scope "type"), to an attribute set (scope "set",
// WP-250) or applies to every type of the organization (scope "global", no
// CI type and no set); organization_id NULL marks a read-only system
// definition. ci_type.attribute_schema is generated from
// the type's definitions by the database. Validation is the rule AST of
// MET-45 (api/rules.schema.json), evaluated bindingly by internal/rules; a
// legacy structure in a request or a stored row is translated into it
// (WP-589), and a required rule makes the definition required. Computed
// is the expression of a computed attribute (WP-258, $defs/expr): such an
// attribute is neither required nor unique and has no default; it stays
// computed (or entered) for its lifetime, only its expression can change.
type AttributeDefinition struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id,omitempty"`
	CITypeID       string `json:"ci_type_id,omitempty"`
	// AttributeSetID names the set of a set attribute (scope "set").
	AttributeSetID string        `json:"attribute_set_id,omitempty"`
	Scope          string        `json:"scope"`
	Key            string        `json:"key"`
	DisplayName    LocalizedText `json:"display_name"`
	Description    string        `json:"description,omitempty"`
	DataType       string        `json:"data_type"`
	Required       bool          `json:"required"`
	// Default is any JSON value valid for the data type.
	Default         json.RawMessage             `json:"default,omitempty"`
	EnumValues      []string                    `json:"enum_values,omitempty"`
	MultiValue      bool                        `json:"multi_value"`
	Searchable      bool                        `json:"searchable"`
	Unique          bool                        `json:"unique"`
	UniqueScope     string                      `json:"unique_scope"`
	Inheritable     bool                        `json:"inheritable"`
	Validation      *rules.Rule                 `json:"validation,omitempty"`
	Computed        *rules.Expr                 `json:"computed,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                      `json:"reference_target,omitempty"`
	Group           string                      `json:"group,omitempty"`
	Order           int                         `json:"order"`
	Unit            string                      `json:"unit,omitempty"`
	IsSystem        bool                        `json:"is_system"`
	// AppliesTo restricts an organization's global attribute (MET-13,
	// WP-255): "all" (every type), "categories" (types whose category is
	// in AppliesToCategories) or "types" (the types in AppliesToTypeIDs and
	// their subtypes). System globals and type or set definitions are
	// "all". It is neither composed nor materialized.
	AppliesTo           string    `json:"applies_to"`
	AppliesToCategories []string  `json:"applies_to_categories"`
	AppliesToTypeIDs    []string  `json:"applies_to_type_ids"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	// IndexStatus is the index status (DB-04, WP-796); only the read
	// handlers set it (AttributeIndexStatuses), so the export, the audit
	// and the event ci_attribute.changed go without it.
	IndexStatus *AttributeIndexStatus `json:"index_status,omitempty"`
}

// AttributeDefinitionPatch changes a definition; key, CI type, attribute
// set and scope are immutable. Validation is a rule AST or the legacy structure (see
// fieldmeta.ParseValidation); null or {} removes the rules. Computed is an
// expression ($defs/expr, WP-258); it can be changed but neither added to
// an entered definition nor removed (null) after creation.
type AttributeDefinitionPatch struct {
	DisplayName     LocalizedText               `json:"display_name,omitempty"`
	Description     *string                     `json:"description,omitempty"`
	DataType        *string                     `json:"data_type,omitempty"`
	Required        *bool                       `json:"required,omitempty"`
	Default         json.RawMessage             `json:"default,omitempty"`
	EnumValues      []string                    `json:"enum_values,omitempty"`
	MultiValue      *bool                       `json:"multi_value,omitempty"`
	Searchable      *bool                       `json:"searchable,omitempty"`
	Unique          *bool                       `json:"unique,omitempty"`
	UniqueScope     *string                     `json:"unique_scope,omitempty"`
	Inheritable     *bool                       `json:"inheritable,omitempty"`
	Validation      json.RawMessage             `json:"validation,omitempty"`
	Computed        json.RawMessage             `json:"computed,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget *string                     `json:"reference_target,omitempty"`
	Group           *string                     `json:"group,omitempty"`
	Order           *int                        `json:"order,omitempty"`
	Unit            *string                     `json:"unit,omitempty"`
	// AppliesTo and its lists restrict an organization's global attribute
	// (WP-255); a change of the mode drops the lists of the former mode
	// unless the request sends them.
	AppliesTo           *string  `json:"applies_to,omitempty"`
	AppliesToCategories []string `json:"applies_to_categories,omitempty"`
	AppliesToTypeIDs    []string `json:"applies_to_type_ids,omitempty"`
}

// Applicability modes of a global attribute (MET-13, WP-255).
const (
	AppliesToAll        = "all"
	AppliesToCategories = "categories"
	AppliesToTypes      = "types"
)

// AttributeFilter narrows the definition list.
type AttributeFilter struct {
	CITypeID       string
	AttributeSetID string
	Scope          string
	// ApplicableTo selects the definitions of a CI type and the global
	// definitions that apply to it (WP-255).
	ApplicableTo string
}

// Attribute definition errors.
var (
	ErrAttributeNotFound = errors.New("attribute definition not found")
	// ErrAttributeReadOnly: system definitions cannot be changed.
	ErrAttributeReadOnly = errors.New("system attribute definitions are read-only")
	// ErrAttributeNotIndexed: a rebuild of a definition that is neither
	// searchable nor unique (WP-796).
	ErrAttributeNotIndexed = errors.New("the attribute definition is neither searchable nor unique and has no index")
	// ErrIndexJobsUnavailable: no job service runs the index jobs
	// (WithIndexScheduler not set).
	ErrIndexJobsUnavailable = errors.New("attribute index jobs are not available")
)

// AttributeDefinitionStore is the attribute-definitions resource
// (API-05); the PostgreSQL repository implements it.
type AttributeDefinitionStore interface {
	// ListAttributeDefinitions returns every definition the filter selects
	// (export).
	ListAttributeDefinitions(ctx context.Context, orgID string, filter AttributeFilter) ([]AttributeDefinition, error)
	// ListAttributeDefinitionPage returns a page newest first on
	// (created_at, id), at most one definition more than the limit, and
	// the number the filter selects (API-02, see api.NewestFirstSQL).
	ListAttributeDefinitionPage(ctx context.Context, orgID string, filter AttributeFilter, page api.PaginationParams) ([]AttributeDefinition, int, error)
	GetAttributeDefinition(ctx context.Context, orgID, id string) (*AttributeDefinition, error)
	CreateAttributeDefinition(ctx context.Context, orgID string, def *AttributeDefinition) error
	UpdateAttributeDefinition(ctx context.Context, orgID, id string, patch *AttributeDefinitionPatch) (*AttributeDefinition, error)
	DeleteAttributeDefinition(ctx context.Context, orgID, id string) error
	// AttributeIndexStatuses returns the index status of the definitions
	// by id with one query (DB-04, WP-796).
	AttributeIndexStatuses(ctx context.Context, orgID string, defs []AttributeDefinition) (map[string]AttributeIndexStatus, error)
	// ReindexAttributeDefinition queues a rebuild of the definition's
	// index for createdBy unless an index job of it is open (created
	// false) and returns the index status (WP-796).
	ReindexAttributeDefinition(ctx context.Context, orgID, id, createdBy string) (status *AttributeIndexStatus, created bool, err error)
}

// Field is the legacy field view of the definition (/ci-types/{id}/fields).
func (d *AttributeDefinition) Field() Field {
	multiValue := d.MultiValue
	f := Field{
		ID: d.ID, CITypeID: d.CITypeID, Scope: d.Scope, Name: d.Key,
		Label: d.DisplayName.WithFallback(d.Key)["en"], Description: d.Description,
		DataType: d.DataType, Required: d.Required, DefaultValue: defaultString(d.Default),
		EnumValues: d.EnumValues, UIGroup: d.Group, SortOrder: d.Order,
		Validation: d.legacyValidation(), Conditional: d.Conditional, ReferenceTarget: d.ReferenceTarget,
		IsSystem: d.IsSystem, MultiValue: &multiValue, Inheritable: d.Inheritable,
		Computed: d.Computed, Unique: d.Unique,
	}
	if d.Scope == "global" {
		f.AppliesTo, f.AppliesToCategories, f.AppliesToTypeIDs = d.AppliesTo, d.AppliesToCategories, d.AppliesToTypeIDs
	}
	return f
}

// legacyValidation is the validation in the structure composition and the
// legacy field view read; Validate rejects a rule no value can satisfy.
func (d *AttributeDefinition) legacyValidation() *fieldmeta.ValidationRules {
	v, _, _ := fieldmeta.ValidationFromRule(d.Validation)
	return v
}

// definition is the shared fieldmeta shape used for validation.
func (d *AttributeDefinition) definition() fieldmeta.FieldDefinition {
	f := d.Field()
	return f.Definition()
}

// defaultString renders a JSON default as the legacy string form.
func defaultString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// Validate checks a definition before it is stored (MET-10).
func (d *AttributeDefinition) Validate() error {
	d.Key = strings.TrimSpace(d.Key)
	if d.Key == "" || utf8.RuneCountInString(d.Key) > 100 {
		return &api.FieldError{Field: "key", Message: "key is required (at most 100 characters)"}
	}
	if err := d.DisplayName.Validate(); err != nil {
		return &api.FieldError{Field: "display_name", Message: err.Error()}
	}
	d.DisplayName = d.DisplayName.WithFallback(d.Key)
	if d.UniqueScope == "" {
		d.UniqueScope = "org"
	}
	if d.UniqueScope != "org" && d.UniqueScope != "client" {
		return &api.FieldError{Field: "unique_scope", Message: "unique_scope must be org or client"}
	}
	if d.Validation != nil {
		if err := d.Validation.Validate(); err != nil {
			return &api.FieldError{Field: "validation", Message: err.Error()}
		}
		_, required, err := fieldmeta.ValidationFromRule(d.Validation)
		if err != nil {
			return &api.FieldError{Field: "validation", Message: err.Error()}
		}
		d.Required = d.Required || required
	}
	if err := d.validateAppliesTo(); err != nil {
		return err
	}
	if err := d.validateComputed(); err != nil {
		return err
	}
	fieldDef := d.definition()
	if err := fieldmeta.ValidateDefinition(&fieldDef); err != nil {
		return &api.FieldError{Field: definitionField(err), Message: err.Error()}
	}
	if len(d.Default) > 0 && string(d.Default) != "null" {
		var value any
		if err := json.Unmarshal(d.Default, &value); err != nil {
			return &api.FieldError{Field: "default", Message: "default must be a JSON value"}
		}
		// With multi_value ValidateValue checks a list element by element
		// and refuses a repeated value (WP-254).
		fieldDef := d.definition()
		if err := fieldmeta.ValidateValue(&fieldDef, value); err != nil {
			return &api.FieldError{Field: "default", Message: err.Error()}
		}
	}
	return nil
}

// errComputed refuses a computed expression (422 on computed, WP-258).
func errComputed(message string) error {
	return &api.FieldError{Field: "computed", Message: message, Status: http.StatusUnprocessableEntity}
}

// computedResults are the result types of an expression (rules.ResultType)
// each data type that can be computed takes; any, a reference to an
// attribute of the record, fits every one of them.
var computedResults = map[string][]string{
	fieldmeta.TypeInteger:   {rules.ResultNumber},
	fieldmeta.TypeDecimal:   {rules.ResultNumber},
	fieldmeta.TypeText:      {rules.ResultString, rules.ResultDate},
	fieldmeta.TypeMultiline: {rules.ResultString, rules.ResultDate},
	fieldmeta.TypeDate:      {rules.ResultDate},
	fieldmeta.TypeBoolean:   {rules.ResultBoolean},
}

// validateComputed checks the expression of a computed attribute (WP-258)
// against the definition: a single-valued data type of computedResults
// that takes the result type of the expression, no reference to the
// attribute itself, and neither required (also by a required rule) nor a
// default nor unique (CHECK attribute_definition_computed_check). A
// reference to another computed attribute is refused in the transaction
// (checkComputedReferencesTx).
// validation: fixed texts naming the data type or the setting that fails.
func (d *AttributeDefinition) validateComputed() error {
	if d.Computed == nil {
		return nil
	}
	results, computable := computedResults[fieldmeta.CanonicalType(d.DataType)]
	switch result := d.Computed.ResultType(); {
	case !computable || d.MultiValue:
		return errComputed(fmt.Sprintf("data type %s cannot be computed (integer, decimal, text, multiline, date, boolean; single-valued)", d.DataType))
	case result != rules.ResultAny && !slices.Contains(results, result):
		return errComputed(fmt.Sprintf("the expression yields %s, which does not fit data type %s", result, d.DataType))
	case slices.Contains(d.Computed.Fields(), d.Key):
		return errComputed("the expression refers to the attribute itself")
	case d.Required:
		return errComputed("a computed attribute cannot be required")
	case len(d.Default) > 0 && string(d.Default) != "null":
		return errComputed("a computed attribute cannot have a default")
	case d.Unique:
		return errComputed("a computed attribute cannot be unique")
	}
	return nil
}

// checkComputedReferencesTx refuses an expression that refers to another
// computed attribute, and a computed attribute another expression refers
// to (WP-258): computed attributes are computed from entered values only.
// It reads every definition the organization sees, like
// checkCompositionTx.
// validation: fixed texts naming the computed attribute.
func checkComputedReferencesTx(ctx context.Context, tx pgx.Tx, d *AttributeDefinition) error {
	if d.Computed == nil {
		return nil
	}
	var key string
	err := tx.QueryRow(ctx, `SELECT key FROM attribute_definition
		WHERE computed IS NOT NULL AND key <> $2 AND key = ANY($1::text[])
		ORDER BY key LIMIT 1`, d.Computed.Fields(), d.Key).Scan(&key)
	switch {
	case err == nil:
		return errComputed(fmt.Sprintf("the expression refers to the computed attribute %s", key))
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("read computed attributes: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT key FROM attribute_definition
		WHERE computed IS NOT NULL AND key <> $1
		  AND jsonb_path_exists(computed, '$.** ? (@.op == "field" && @.field == $key)', jsonb_build_object('key', $1::text))
		ORDER BY key LIMIT 1`, d.Key).Scan(&key)
	switch {
	case err == nil:
		return errComputed(fmt.Sprintf("the computed attribute %s refers to this attribute", key))
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("read computed attributes: %w", err)
	}
	return nil
}

// validateAppliesTo checks and normalizes the applicability (MET-13,
// WP-255): only an organization's global attribute can be restricted, the
// list of the mode is not empty and the other one is (400 otherwise); a
// type ID that is no UUID is a 422 on applies_to_type_ids, like an unknown
// type (checkAppliesToTypesTx). Categories are trimmed, both lists
// deduplicated.
func (d *AttributeDefinition) validateAppliesTo() error {
	if d.AppliesTo == "" {
		d.AppliesTo = AppliesToAll
	}
	switch d.AppliesTo {
	case AppliesToAll, AppliesToCategories, AppliesToTypes:
	default:
		return &api.FieldError{Field: "applies_to", Message: "applies_to must be all, categories or types"}
	}
	categories := make([]string, 0, len(d.AppliesToCategories))
	for _, c := range d.AppliesToCategories {
		c = strings.TrimSpace(c)
		if c == "" {
			return &api.FieldError{Field: "applies_to_categories", Message: "a category must not be empty"}
		}
		if !slices.Contains(categories, c) {
			categories = append(categories, c)
		}
	}
	typeIDs := make([]string, 0, len(d.AppliesToTypeIDs))
	for _, id := range d.AppliesToTypeIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if !api.IsUUID(id) {
			return errAppliesToType()
		}
		if !slices.Contains(typeIDs, id) {
			typeIDs = append(typeIDs, id)
		}
	}
	d.AppliesToCategories, d.AppliesToTypeIDs = categories, typeIDs
	restricted := d.AppliesTo != AppliesToAll || len(categories) > 0 || len(typeIDs) > 0
	if restricted && (d.Scope != "global" || d.IsSystem) {
		return &api.FieldError{Field: "applies_to", Message: "only an organization's global attributes can be restricted"}
	}
	wantCategories, wantTypes := d.AppliesTo == AppliesToCategories, d.AppliesTo == AppliesToTypes
	switch {
	case wantCategories && len(categories) == 0:
		return &api.FieldError{Field: "applies_to_categories", Message: "applies_to categories needs at least one category"}
	case wantTypes && len(typeIDs) == 0:
		return &api.FieldError{Field: "applies_to_type_ids", Message: "applies_to types needs at least one ci type"}
	case !wantCategories && len(categories) > 0:
		return &api.FieldError{Field: "applies_to_categories", Message: "applies_to_categories needs applies_to categories"}
	case !wantTypes && len(typeIDs) > 0:
		return &api.FieldError{Field: "applies_to_type_ids", Message: "applies_to_type_ids needs applies_to types"}
	}
	return nil
}

// errAppliesToType refuses a type ID of applies_to_type_ids that is
// neither a type of the organization nor a system type (422).
func errAppliesToType() error {
	return &api.FieldError{Field: "applies_to_type_ids", Message: "ci type not found",
		Status: http.StatusUnprocessableEntity}
}

// checkAppliesToTypesTx resolves the type IDs a definition gained over
// before (nil: all of them) under row level security: each is a type of
// the organization or a system type, otherwise a 422 on
// applies_to_type_ids. IDs kept from before are not checked again, so a
// type deleted since is ignored rather than refused.
func checkAppliesToTypesTx(ctx context.Context, tx pgx.Tx, before, d *AttributeDefinition) error {
	var added []string
	for _, id := range d.AppliesToTypeIDs {
		if before == nil || !slices.Contains(before.AppliesToTypeIDs, id) {
			added = append(added, id)
		}
	}
	if len(added) == 0 {
		return nil
	}
	var found int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ci_type WHERE id = ANY($1::uuid[])`, added).Scan(&found); err != nil {
		return fmt.Errorf("read applies_to types: %w", err)
	}
	if found != len(added) {
		return errAppliesToType()
	}
	return nil
}

// definitionField is the request field of a fieldmeta.ValidateDefinition
// rejection (F-408): the field name is the key, the other attributes keep
// their request names; an error without attribute stays data_type.
func definitionField(err error) string {
	var de *fieldmeta.DefinitionError
	if !errors.As(err, &de) {
		return "data_type"
	}
	switch de.Attribute {
	case "name":
		return "key"
	case "reference_target", "validation", "conditional":
		return de.Attribute
	}
	return "data_type"
}

// apply changes d; a validation that is no valid rule is a *api.FieldError
// (400), an invalid computed expression one with 422 (WP-258).
func (p *AttributeDefinitionPatch) apply(d *AttributeDefinition) error {
	if p.Validation != nil {
		rule, err := fieldmeta.ParseValidation(p.Validation)
		if err != nil {
			return &api.FieldError{Field: "validation", Message: err.Error()}
		}
		d.Validation = rule
	}
	if p.Computed != nil {
		d.Computed = nil
		if string(p.Computed) != "null" {
			expr, err := rules.ParseExpr(p.Computed)
			if err != nil {
				return errComputed(err.Error())
			}
			d.Computed = &expr
		}
	}
	if p.DisplayName != nil {
		d.DisplayName = p.DisplayName
	}
	setString(&d.Description, p.Description)
	setString(&d.DataType, p.DataType)
	setBool(&d.Required, p.Required)
	if p.Default != nil {
		d.Default = p.Default
	}
	if p.EnumValues != nil {
		d.EnumValues = p.EnumValues
	}
	setBool(&d.MultiValue, p.MultiValue)
	setBool(&d.Searchable, p.Searchable)
	setBool(&d.Unique, p.Unique)
	setString(&d.UniqueScope, p.UniqueScope)
	setBool(&d.Inheritable, p.Inheritable)
	if p.Conditional != nil {
		d.Conditional = p.Conditional
	}
	setString(&d.ReferenceTarget, p.ReferenceTarget)
	setString(&d.Group, p.Group)
	if p.Order != nil {
		d.Order = *p.Order
	}
	setString(&d.Unit, p.Unit)
	if p.AppliesTo != nil {
		if *p.AppliesTo != d.AppliesTo {
			d.AppliesToCategories, d.AppliesToTypeIDs = nil, nil
		}
		d.AppliesTo = *p.AppliesTo
	}
	if p.AppliesToCategories != nil {
		d.AppliesToCategories = p.AppliesToCategories
	}
	if p.AppliesToTypeIDs != nil {
		d.AppliesToTypeIDs = p.AppliesToTypeIDs
	}
	return nil
}

func setString(dst, v *string) {
	if v != nil {
		*dst = *v
	}
}

func setBool(dst, v *bool) {
	if v != nil {
		*dst = *v
	}
}

// attributeFields maps database constraints onto request fields.
var attributeFields = map[string]string{
	"attribute_definition_ci_type_id_key_key":       "key",
	"attribute_definition_global_key":               "key",
	"attribute_definition_display_name_i18n":        "display_name",
	"attribute_definition_unique_scope_check":       "unique_scope",
	"attribute_definition_scope_check":              "scope",
	"attribute_definition_ci_type_id_fkey":          "ci_type_id",
	"attribute_definition_scope_type_check":         "ci_type_id",
	"attribute_definition_system_global_key":        "key",
	"attribute_definition_organization_id_fkey":     "organization_id",
	"attribute_definition_attribute_set_id_fkey":    "attribute_set_id",
	"attribute_definition_attribute_set_id_key_key": "key",
	"attribute_definition_applies_to_check":         "applies_to",
	"attribute_definition_applies_to_scope_check":   "applies_to",
	"attribute_definition_applies_to_list_check":    "applies_to",
}

const attributeColumns = `id::text, COALESCE(organization_id::text, ''), COALESCE(ci_type_id::text, ''),
	COALESCE(attribute_set_id::text, ''), scope, key,
	display_name, COALESCE(description, ''), data_type, required, default_value, enum_values,
	multi_value, searchable, is_unique, unique_scope, inheritable, validation, conditional,
	COALESCE(reference_target, ''), COALESCE(group_name, ''), sort_order, COALESCE(unit, ''),
	applies_to, applies_to_categories, applies_to_type_ids::text[], computed, created_at, updated_at`

func scanAttribute(row pgx.Row) (*AttributeDefinition, error) {
	var (
		d                                                                   AttributeDefinition
		displayName, def, enumRaw, validationRaw, conditionRaw, computedRaw []byte
	)
	if err := row.Scan(&d.ID, &d.OrganizationID, &d.CITypeID, &d.AttributeSetID, &d.Scope, &d.Key, &displayName, &d.Description,
		&d.DataType, &d.Required, &def, &enumRaw, &d.MultiValue, &d.Searchable, &d.Unique, &d.UniqueScope,
		&d.Inheritable, &validationRaw, &conditionRaw, &d.ReferenceTarget, &d.Group, &d.Order, &d.Unit,
		&d.AppliesTo, &d.AppliesToCategories, &d.AppliesToTypeIDs, &computedRaw, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Computed = storedComputed(computedRaw)
	if err := json.Unmarshal(displayName, &d.DisplayName); err != nil {
		return nil, fmt.Errorf("decode display_name: %w", err)
	}
	if len(def) > 0 && string(def) != "null" {
		d.Default = json.RawMessage(def)
	}
	if len(enumRaw) > 0 {
		_ = json.Unmarshal(enumRaw, &d.EnumValues)
	}
	// A legacy structure stored before WP-589 is read as its AST.
	d.Validation = fieldmeta.StoredValidation(validationRaw)
	if len(conditionRaw) > 0 && string(conditionRaw) != "null" {
		var c fieldmeta.ConditionalRules
		if json.Unmarshal(conditionRaw, &c) == nil {
			d.Conditional = &c
		}
	}
	d.IsSystem = d.OrganizationID == ""
	d.normalizeAppliesTo()
	return &d, nil
}

// storedComputed reads the computed column of attribute_definition; NULL
// is an entered attribute. The column holds only expressions the API
// accepted (ParseExpr); one that no longer parses is logged and read as
// none.
func storedComputed(raw []byte) *rules.Expr {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	expr, err := rules.ParseExpr(raw)
	if err != nil {
		slog.Error("stored computed expression is invalid", "error", err)
		return nil
	}
	return &expr
}

// normalizeAppliesTo renders the lists of a read definition as [] rather
// than null and a missing mode as all.
func (d *AttributeDefinition) normalizeAppliesTo() {
	d.AppliesTo = appliesToMode(d.AppliesTo)
	d.AppliesToCategories = nonNil(d.AppliesToCategories)
	d.AppliesToTypeIDs = nonNil(d.AppliesToTypeIDs)
}

// appliesToMode is the stored mode; "" is all.
func appliesToMode(mode string) string {
	if mode == "" {
		return AppliesToAll
	}
	return mode
}

// nonNil is a missing list as the empty array of the column default.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// attributeArgs are the stored values of a definition in the column order
// of writeAttributeTx.
func attributeArgs(d *AttributeDefinition) []any {
	var def any
	if len(d.Default) > 0 && string(d.Default) != "null" {
		def = d.Default
	}
	var enum any
	if d.EnumValues != nil {
		enum = d.EnumValues
	}
	validation := any(emptyObject)
	if d.Validation != nil {
		validation = d.Validation
	}
	var conditional any
	if d.Conditional != nil {
		conditional = d.Conditional
	}
	var computed any
	if d.Computed != nil {
		computed = d.Computed
	}
	return []any{d.DisplayName, nilIfEmpty(d.Description), d.DataType, d.Required, def, enum, d.MultiValue,
		d.Searchable, d.Unique, d.UniqueScope, d.Inheritable, validation, conditional,
		nilIfEmpty(d.ReferenceTarget), nilIfEmpty(d.Group), d.Order, nilIfEmpty(d.Unit),
		appliesToMode(d.AppliesTo), nonNil(d.AppliesToCategories), nonNil(d.AppliesToTypeIDs), computed}
}

// insertAttributeTx inserts a definition; a type-scoped definition takes
// the organization of its type, a set attribute that of its set
// (trigger), a global one orgID.
func insertAttributeTx(ctx context.Context, tx pgx.Tx, orgID string, d *AttributeDefinition) error {
	args := append([]any{nilIfEmpty(d.CITypeID), nilIfEmpty(d.AttributeSetID), orgID, d.Scope, d.Key}, attributeArgs(d)...)
	created, err := scanAttribute(tx.QueryRow(ctx, `INSERT INTO attribute_definition (
			ci_type_id, attribute_set_id, organization_id, scope, key, display_name, description, data_type,
			required, default_value, enum_values, multi_value, searchable, is_unique, unique_scope, inheritable,
			validation, conditional, reference_target, group_name, sort_order, unit,
			applies_to, applies_to_categories, applies_to_type_ids, computed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
			$23, $24, $25::uuid[], $26)
		RETURNING `+attributeColumns, args...))
	if err != nil {
		return attributeDBError(err)
	}
	*d = *created
	if err := checkComputedReferencesTx(ctx, tx, d); err != nil {
		return err
	}
	return checkDefinitionCompositionTx(ctx, tx, d)
}

func updateAttributeTx(ctx context.Context, tx pgx.Tx, d *AttributeDefinition) error {
	args := append([]any{d.ID}, attributeArgs(d)...)
	updated, err := scanAttribute(tx.QueryRow(ctx, `UPDATE attribute_definition SET
			display_name = $2, description = $3, data_type = $4, required = $5, default_value = $6,
			enum_values = $7, multi_value = $8, searchable = $9, is_unique = $10, unique_scope = $11,
			inheritable = $12, validation = $13, conditional = $14, reference_target = $15,
			group_name = $16, sort_order = $17, unit = $18,
			applies_to = $19, applies_to_categories = $20, applies_to_type_ids = $21::uuid[], computed = $22
		WHERE id = $1 RETURNING `+attributeColumns, args...))
	if err != nil {
		return attributeDBError(err)
	}
	*d = *updated
	if err := checkComputedReferencesTx(ctx, tx, d); err != nil {
		return err
	}
	return checkDefinitionCompositionTx(ctx, tx, d)
}

// checkDefinitionCompositionTx runs checkCompositionTx for a written
// definition of any scope; a set attribute is checked against every type
// its set is assigned to (WP-709).
func checkDefinitionCompositionTx(ctx context.Context, tx pgx.Tx, d *AttributeDefinition) error {
	return checkCompositionTx(ctx, tx, d.Key)
}

// compositionConstraint names the error the schema generator raises when
// the system base and a type's definitions cannot be composed (MET-16).
const compositionConstraint = "attribute_definition_composition"

// attributeDBError maps a failed definition write onto the request.
func attributeDBError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "attribute_definition_computed_check" {
		return errComputed("a computed attribute cannot be required, have a default or be unique")
	}
	if errors.As(err, &pgErr) && pgErr.ConstraintName == compositionConstraint {
		// compose_attribute_definition reports the code of the conflict in
		// DETAIL, as CompositionError.Code.
		return &api.FieldError{Field: "key", Message: pgErr.Message, Code: pgErr.Detail,
			Status: http.StatusUnprocessableEntity}
	}
	return api.FieldDBError(err, attributeFields)
}

// checkCompositionTx publishes a definition only when every effective
// schema that contains its key can still be composed (MET-16, WP-252): it
// runs CheckComposition for the global layers and for every type that
// defines the key, every type a set with the key is assigned to, and every
// type in their subtrees (ci_type_subtree), each as system base →
// organization's global attributes that apply to it → type chain (of the
// ancestors the inheritable definitions, of the type all, MET-06) →
// active sets of the chain (WP-709). A type attribute is so checked
// against the sets of its type, a set attribute against each type of its
// set. The database checks the materialized schemas (system base, chain
// and sets) itself; this adds the organization's layer. A conflict of the
// key is a 422 on key with its code. It serves POST/PATCH
// /attribute-definitions and the legacy paths PUT /ci-types/{id}/fields
// and PUT /ci-fields/global (upsertFieldTx).
func checkCompositionTx(ctx context.Context, tx pgx.Tx, key string) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT s.id::text FROM (
			SELECT a.ci_type_id AS type_id FROM attribute_definition a
			WHERE a.key = $1 AND a.ci_type_id IS NOT NULL
			UNION
			SELECT cs.ci_type_id FROM attribute_definition a
			JOIN ci_type_attribute_set cs ON cs.attribute_set_id = a.attribute_set_id
			WHERE a.key = $1 AND a.attribute_set_id IS NOT NULL
		) types CROSS JOIN LATERAL ci_type_subtree(types.type_id) s`, key)
	if err != nil {
		return fmt.Errorf("read types of %s: %w", key, err)
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read types of %s: %w", key, err)
	}
	if cerr := chainCompositionTx(ctx, tx, targets, key, nil); cerr != nil {
		var conflict *CompositionError
		if errors.As(cerr, &conflict) {
			return &api.FieldError{Field: "key", Message: cerr.Error(), Status: http.StatusUnprocessableEntity, Code: conflict.Code}
		}
		return cerr
	}
	return nil
}

// checkSubtreeCompositionTx checks the subtree of a type that got a new
// parent (MET-06) or a new category: every type in it stays composable
// with the organization's global attributes that apply to it afterwards
// (MET-13, WP-255). A conflict is a 422 on field (parent_type_id or
// category) naming the key.
func checkSubtreeCompositionTx(ctx context.Context, tx pgx.Tx, typeID, field string) error {
	rows, err := tx.Query(ctx, `SELECT s.id::text FROM ci_type_subtree($1::uuid) s`, typeID)
	if err != nil {
		return fmt.Errorf("read ci type subtree: %w", err)
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read ci type subtree: %w", err)
	}
	if cerr := chainCompositionTx(ctx, tx, targets, "", nil); cerr != nil {
		var conflict *CompositionError
		if errors.As(cerr, &conflict) {
			return errTypeComposition(field, cerr.Error(), conflict.Code)
		}
		return cerr
	}
	return nil
}

// chainCompositionTx runs CheckComposition for the global layers and for
// each target type (system base → the organization's global attributes
// that apply to it, MET-13, WP-255 → chain root → type, MET-06 → active
// sets of that chain, WP-709), all with one reading of the global
// attributes; override replaces the set list of one type (nil: the stored
// lists). It returns the first *CompositionError of key ("" any key).
// validation: the *CompositionError texts of Compose (MET-16), naming the key.
func chainCompositionTx(ctx context.Context, tx pgx.Tx, targets []string, key string, override *setOverride) error {
	lister := newTxLister(tx, override)
	scopes := make([]CompositionScope, 0, len(targets)+1)
	scopes = append(scopes, CompositionScope{})
	for _, target := range targets {
		scopes = append(scopes, CompositionScope{TypeID: target})
	}
	for _, scope := range scopes {
		conflicts, err := lister.check(ctx, scope)
		if err != nil {
			return err
		}
		if c := firstConflict(conflicts, func(k string) bool { return key == "" || k == key }); c != nil {
			return c
		}
	}
	return nil
}

// setOverride is the set list a type is about to get (the PUT of
// /ci-types/{id}/attribute-sets before it is written).
type setOverride struct {
	typeID string
	sets   []AttributeSetAssignment
}

// errSystemTypeAttribute refuses an organization attribute on a system CI
// type: system types are read-only for tenants and are extended by
// inheritance (MET-06, P2). The row-level security of attribute_definition
// would refuse the row as well (the trigger copies the type's NULL
// organization), but as an opaque 42501.
func errSystemTypeAttribute() error {
	return &api.FieldError{Field: "ci_type_id", Message: "system ci types cannot get organization attributes",
		Status: http.StatusUnprocessableEntity}
}

// ciTypeOrganizationTx reads the organization of a CI type the tenant can
// see: "" for a system type (organization_id NULL); found is false for an
// unknown type or a type of another organization.
func ciTypeOrganizationTx(ctx context.Context, tx pgx.Tx, typeID string) (org string, found bool, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE(organization_id::text, '') FROM ci_type WHERE id::text = $1`, typeID).Scan(&org)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read ci type: %w", err)
	}
	return org, true, nil
}

// checkAttributeType resolves the CI type of a new type-scoped definition:
// it must be a type of the organization (system types are extended by
// inheritance, MET-06, P2). An unknown type is a 422 on ci_type_id, the
// field of the request body.
func checkAttributeType(ctx context.Context, tx pgx.Tx, typeID string) error {
	org, found, err := ciTypeOrganizationTx(ctx, tx, typeID)
	switch {
	case err != nil:
		return err
	case !found:
		return &api.FieldError{Field: "ci_type_id", Message: "ci type not found", Status: http.StatusUnprocessableEntity}
	case org == "":
		return errSystemTypeAttribute()
	}
	return nil
}

// errSetNotAssignable refuses a set of an assignment or of a new set
// attribute: 422 on attribute_set_id.
func errSetNotAssignable(message string) error {
	return &api.FieldError{Field: "attribute_set_id", Message: message, Status: http.StatusUnprocessableEntity}
}

// checkAttributeSetOfDefinition resolves the set of a new set attribute
// (WP-250): a set the organization does not see is a 422 on
// attribute_set_id, a system set is read-only (403).
func checkAttributeSetOfDefinition(ctx context.Context, tx pgx.Tx, setID string) error {
	if !api.IsUUID(setID) {
		return errSetNotAssignable("attribute set not found")
	}
	var org string
	err := tx.QueryRow(ctx, `SELECT COALESCE(organization_id::text, '') FROM attribute_set WHERE id = $1`, setID).Scan(&org)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errSetNotAssignable("attribute set not found")
	case err != nil:
		return fmt.Errorf("read attribute set: %w", err)
	case org == "":
		return ErrAttributeSetReadOnly
	}
	return nil
}

// checkSetAssignmentTx checks the new set list of a type (WP-250) and
// returns the current one: the type is a type of the organization (an
// unknown type is a 422 on ci_type_id; a system type is one too, because
// its materialized schema is shared by every organization, E-108); every
// set is listed once at its own position (0 or more) and is visible to the
// organization (its own or a system set), and a set that is not assigned
// yet is active; otherwise a 422 on attribute_set_id or position. Before
// anything is written the new list is composed (WP-709, MET-16) with the
// system base, the organization's global attributes and the type chain,
// for the type and each type of its subtree, which takes the inheritable
// set attributes: a *CompositionError is a 422 on attribute_set_id with
// the code of the conflict.
func checkSetAssignmentTx(ctx context.Context, tx pgx.Tx, typeID string, sets []AttributeSetAssignment) (*TypeAttributeSets, error) {
	if !api.IsUUID(typeID) {
		return nil, &api.FieldError{Field: "ci_type_id", Message: "ci type not found", Status: http.StatusUnprocessableEntity}
	}
	org, found, err := ciTypeOrganizationTx(ctx, tx, typeID)
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, &api.FieldError{Field: "ci_type_id", Message: "ci type not found", Status: http.StatusUnprocessableEntity}
	case org == "":
		return nil, &api.FieldError{Field: "ci_type_id", Message: "system ci types cannot get attribute sets",
			Status: http.StatusUnprocessableEntity}
	}
	current, err := typeAttributeSetsTx(ctx, tx, typeID)
	if err != nil {
		return nil, err
	}
	assigned := map[string]bool{}
	for _, a := range current.AttributeSets {
		assigned[a.AttributeSetID] = true
	}
	listed, positions := map[string]bool{}, map[int]bool{}
	for _, a := range sets {
		if !api.IsUUID(a.AttributeSetID) {
			return nil, errSetNotAssignable("attribute set not found")
		}
		if listed[a.AttributeSetID] {
			return nil, errSetNotAssignable("attribute set listed twice")
		}
		if a.Position < 0 || positions[a.Position] {
			return nil, &api.FieldError{Field: "position", Message: "each set needs its own position (0 or more)",
				Status: http.StatusUnprocessableEntity}
		}
		listed[a.AttributeSetID], positions[a.Position] = true, true
		var active bool
		err = tx.QueryRow(ctx, `SELECT is_active FROM attribute_set WHERE id = $1`, a.AttributeSetID).Scan(&active)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, errSetNotAssignable("attribute set not found")
		case err != nil:
			return nil, fmt.Errorf("read attribute set: %w", err)
		case !active && !assigned[a.AttributeSetID]:
			return nil, errSetNotAssignable("an inactive attribute set cannot be assigned")
		}
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text FROM ci_type_subtree($1::uuid) s`, typeID)
	if err != nil {
		return nil, fmt.Errorf("read ci type subtree: %w", err)
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read ci type subtree: %w", err)
	}
	if cerr := chainCompositionTx(ctx, tx, targets, "", &setOverride{typeID: typeID, sets: sets}); cerr != nil {
		var conflict *CompositionError
		if errors.As(cerr, &conflict) {
			return nil, &api.FieldError{Field: "attribute_set_id", Message: cerr.Error(),
				Status: http.StatusUnprocessableEntity, Code: conflict.Code}
		}
		return nil, cerr
	}
	return current, nil
}

// checkFieldType resolves the CI type of the legacy field upsert (PUT
// /ci-types/{id}/fields): an unknown type is the documented 404 of the path
// ("not found", see writeFieldDefinitionError), a system type the 422 of
// checkAttributeType. It runs before the definition is looked up, so an
// existing system definition of the same key is refused the same way
// instead of reaching an UPDATE that row-level security hides.
func checkFieldType(ctx context.Context, tx pgx.Tx, typeID string) error {
	org, found, err := ciTypeOrganizationTx(ctx, tx, typeID)
	switch {
	case err != nil:
		return err
	case !found:
		return fmt.Errorf("not found")
	case org == "":
		return errSystemTypeAttribute()
	}
	return nil
}

// ListAttributeDefinitions implements AttributeDefinitionStore: the
// organization's and the system's definitions.
func (r *PGRepository) ListAttributeDefinitions(ctx context.Context, orgID string, filter AttributeFilter) ([]AttributeDefinition, error) {
	var out []AttributeDefinition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := attributeFilterSQL(filter)
		var err error
		out, err = queryAttributes(ctx, tx, `SELECT `+attributeColumns+` FROM attribute_definition WHERE `+
			where+` ORDER BY ci_type_id NULLS FIRST, sort_order, key`, args)
		return err
	})
	return out, err
}

// ListAttributeDefinitionPage implements AttributeDefinitionStore: a page of
// the organization's and the system's definitions newest first on
// (created_at, id), at most one more than the limit, and their number
// (API-02, F-215).
func (r *PGRepository) ListAttributeDefinitionPage(ctx context.Context, orgID string, filter AttributeFilter, page api.PaginationParams) ([]AttributeDefinition, int, error) {
	var out []AttributeDefinition
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := attributeFilterSQL(filter)
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM attribute_definition WHERE `+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count attribute definitions: %w", err)
		}
		keyset, tail, pageArgs, err := api.NewestFirstSQL(page, len(args)+1)
		if err != nil {
			return err
		}
		out, err = queryAttributes(ctx, tx, `SELECT `+attributeColumns+` FROM attribute_definition WHERE `+
			where+` AND `+keyset+` `+tail, append(args, pageArgs...))
		return err
	})
	return out, total, err
}

// attributeFilterSQL renders the conditions of the definition filters.
func attributeFilterSQL(filter AttributeFilter) (where string, args []any) {
	conditions := []string{"TRUE"}
	args = []any{}
	if filter.CITypeID != "" {
		args = append(args, filter.CITypeID)
		conditions = append(conditions, fmt.Sprintf("ci_type_id::text = $%d", len(args)))
	}
	if filter.AttributeSetID != "" {
		args = append(args, filter.AttributeSetID)
		conditions = append(conditions, fmt.Sprintf("attribute_set_id::text = $%d", len(args)))
	}
	if filter.Scope != "" {
		args = append(args, filter.Scope)
		conditions = append(conditions, fmt.Sprintf("scope = $%d", len(args)))
	}
	if filter.ApplicableTo != "" {
		conditions = append(conditions, applicableToSQL(filter.ApplicableTo, &args))
	}
	return strings.Join(conditions, " AND "), args
}

// applicableToSQL selects the definitions of a type the organization sees
// and the global definitions that apply to it (MET-13, WP-255): system
// globals and globals with applies_to all always, categories when the
// type's category is listed, types when the type or one of its ancestors
// is listed. An ID that is no UUID selects nothing.
func applicableToSQL(typeID string, args *[]any) string {
	if !api.IsUUID(typeID) {
		return "FALSE"
	}
	*args = append(*args, typeID)
	n := len(*args)
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM ci_type t WHERE t.id = $%[1]d::uuid)
		AND (ci_type_id = $%[1]d::uuid OR (scope = 'global' AND (applies_to = 'all'
			OR (applies_to = 'categories'
				AND (SELECT t.category FROM ci_type t WHERE t.id = $%[1]d::uuid) = ANY (applies_to_categories))
			OR (applies_to = 'types'
				AND applies_to_type_ids && ARRAY(SELECT c.id FROM ci_type_ancestors($%[1]d::uuid) c)))))`, n)
}

// queryAttributes reads the definitions a query selects.
func queryAttributes(ctx context.Context, tx pgx.Tx, query string, args []any) ([]AttributeDefinition, error) {
	out := []AttributeDefinition{}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list attribute definitions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanAttribute(rows)
		if err != nil {
			return nil, fmt.Errorf("scan attribute definition: %w", err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func getAttributeTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (*AttributeDefinition, error) {
	if !isUUIDText(id) {
		return nil, ErrAttributeNotFound
	}
	query := `SELECT ` + attributeColumns + ` FROM attribute_definition WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	d, err := scanAttribute(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttributeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read attribute definition: %w", err)
	}
	return d, nil
}

// lockOwnAttributeTx locks a definition of the organization for a change.
// System definitions are visible but not lockable (the UPDATE policy hides
// them from FOR UPDATE), so they are recognized before locking.
func lockOwnAttributeTx(ctx context.Context, tx pgx.Tx, id string) (*AttributeDefinition, error) {
	d, err := getAttributeTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if d.IsSystem {
		return nil, ErrAttributeReadOnly
	}
	return getAttributeTx(ctx, tx, id, true)
}

// GetAttributeDefinition implements AttributeDefinitionStore.
func (r *PGRepository) GetAttributeDefinition(ctx context.Context, orgID, id string) (*AttributeDefinition, error) {
	var out *AttributeDefinition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = getAttributeTx(ctx, tx, id, false)
		return err
	})
	return out, err
}

// CreateAttributeDefinition implements AttributeDefinitionStore: without
// CI type and set the definition is global, with attribute_set_id a set
// attribute (WP-250) of a set of the organization.
func (r *PGRepository) CreateAttributeDefinition(ctx context.Context, orgID string, d *AttributeDefinition) error {
	switch {
	case d.CITypeID != "" && d.AttributeSetID != "":
		return &api.FieldError{Field: "attribute_set_id", Message: "a definition belongs to a ci type or to an attribute set, not both",
			Status: http.StatusUnprocessableEntity}
	case d.AttributeSetID != "":
		d.Scope = ScopeSet
	case d.CITypeID != "":
		d.Scope = "type"
	default:
		d.Scope = "global"
	}
	if err := d.Validate(); err != nil {
		return err
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if d.CITypeID != "" {
			if err := checkAttributeType(ctx, tx, d.CITypeID); err != nil {
				return err
			}
		}
		if d.AttributeSetID != "" {
			if err := checkAttributeSetOfDefinition(ctx, tx, d.AttributeSetID); err != nil {
				return err
			}
		}
		if err := checkAppliesToTypesTx(ctx, tx, nil, d); err != nil {
			return err
		}
		if err := insertAttributeTx(ctx, tx, orgID, d); err != nil {
			return err
		}
		if err := checkUniqueValuesTx(ctx, tx, nil, d); err != nil {
			return err
		}
		return recordAttributeAudit(ctx, tx, orgID, nil, d)
	})
	if err == nil {
		r.scheduleIndex(ctx, orgID, nil, d)
		r.scheduleComputed(ctx, orgID, nil, d)
	}
	return err
}

// UpdateAttributeDefinition implements AttributeDefinitionStore.
func (r *PGRepository) UpdateAttributeDefinition(ctx context.Context, orgID, id string, patch *AttributeDefinitionPatch) (*AttributeDefinition, error) {
	var out, prev *AttributeDefinition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := lockOwnAttributeTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before := *d
		if perr := patch.apply(d); perr != nil {
			return perr
		}
		if (before.Computed == nil) != (d.Computed == nil) {
			return errComputed("a definition cannot switch between computed and entered after creation")
		}
		if verr := d.Validate(); verr != nil {
			return verr
		}
		if terr := checkAppliesToTypesTx(ctx, tx, &before, d); terr != nil {
			return terr
		}
		if uerr := updateAttributeTx(ctx, tx, d); uerr != nil {
			return uerr
		}
		if uerr := checkUniqueValuesTx(ctx, tx, &before, d); uerr != nil {
			return uerr
		}
		out, prev = d, &before
		return recordAttributeAudit(ctx, tx, orgID, &before, d)
	})
	if err == nil {
		r.scheduleIndex(ctx, orgID, prev, out)
		r.scheduleComputed(ctx, orgID, prev, out)
	}
	return out, err
}

// DeleteAttributeDefinition implements AttributeDefinitionStore.
func (r *PGRepository) DeleteAttributeDefinition(ctx context.Context, orgID, id string) error {
	var deleted *AttributeDefinition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := lockOwnAttributeTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM attribute_definition WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete attribute definition: %w", err)
		}
		deleted = before
		return recordAttributeAudit(ctx, tx, orgID, before, nil)
	})
	if err == nil {
		r.scheduleIndex(ctx, orgID, deleted, nil)
		r.scheduleComputed(ctx, orgID, deleted, nil)
	}
	return err
}

// AttributeIndexStatuses implements AttributeDefinitionStore.
func (r *PGRepository) AttributeIndexStatuses(ctx context.Context, orgID string, defs []AttributeDefinition) (map[string]AttributeIndexStatus, error) {
	return indexStatuses(ctx, r.pool, orgID, defs)
}

// ReindexAttributeDefinition implements AttributeDefinitionStore: the
// definition is read in the principal's scope (unknown 404); a system
// definition is indexed by its migration (ErrAttributeReadOnly), one
// without searchable and unique has no index (ErrAttributeNotIndexed).
func (r *PGRepository) ReindexAttributeDefinition(ctx context.Context, orgID, id, createdBy string) (*AttributeIndexStatus, bool, error) {
	d, err := r.GetAttributeDefinition(ctx, orgID, id)
	switch {
	case err != nil:
		return nil, false, err
	case d.IsSystem:
		return nil, false, ErrAttributeReadOnly
	case !d.Searchable && !d.Unique:
		return nil, false, ErrAttributeNotIndexed
	case r.indexes == nil:
		return nil, false, ErrIndexJobsUnavailable
	}
	created, err := r.indexes.Reindex(ctx, orgID, d.ID, createdBy)
	if err != nil {
		return nil, false, err
	}
	statuses, err := indexStatuses(ctx, r.pool, orgID, []AttributeDefinition{*d})
	if err != nil {
		return nil, false, err
	}
	s := statuses[d.ID]
	return &s, created, nil
}

// scheduleIndex starts the index job when the change touches the index
// (DB-04). The change is stored already; a job that cannot be queued is
// logged, and the server-side check of the CI service keeps the values
// unique in the meantime (MET-15).
func (r *PGRepository) scheduleIndex(ctx context.Context, orgID string, before, after *AttributeDefinition) {
	if r.indexes == nil || !indexRelevant(before, after) {
		return
	}
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	if err := r.indexes.ScheduleIndex(context.WithoutCancel(ctx), orgID, id); err != nil {
		slog.Error("schedule attribute index job", "definition", id, "error", err)
	}
}

// ComputedBackfillJobKind recomputes the values of the computed attributes
// of an organization's CIs after a computed definition was created, its
// expression changed or it was deleted (WP-718, JOB-02); the CI package
// runs it (ci.ComputedRefresh).
const ComputedBackfillJobKind = "attribute.computed_backfill"

// ComputedBackfill names the definition whose change a backfill follows
// (the params of ComputedBackfillJobKind): its key and, for a type
// definition, its type, which narrow the CIs to recompute to the subtree of
// the type and the CIs with a computed value of the key.
type ComputedBackfill struct {
	DefinitionID string `json:"definition_id"`
	Key          string `json:"key"`
	CITypeID     string `json:"ci_type_id,omitempty"`
	Deleted      bool   `json:"deleted,omitempty"`
}

// ComputedScheduler queues the backfill of computed values after a change
// of a computed definition (WP-718); the CI package implements it
// (ci.ComputedRefresh), so citype does not import it.
type ComputedScheduler interface {
	ScheduleBackfill(ctx context.Context, orgID string, b ComputedBackfill) error
}

// computedRelevant reports whether a change from before to after (nil for
// a missing definition) changes computed values of CIs: a computed
// definition was created or deleted, or its expression, data type or reach
// (inheritable, applicability) changed.
func computedRelevant(before, after *AttributeDefinition) bool {
	computed := func(d *AttributeDefinition) bool { return d != nil && d.Computed != nil }
	switch {
	case !computed(before) && !computed(after):
		return false
	case !computed(before) || !computed(after):
		return true
	}
	return !before.Computed.Equal(after.Computed) || before.DataType != after.DataType ||
		before.Inheritable != after.Inheritable || before.AppliesTo != after.AppliesTo ||
		!slices.Equal(before.AppliesToCategories, after.AppliesToCategories) ||
		!slices.Equal(before.AppliesToTypeIDs, after.AppliesToTypeIDs)
}

// scheduleComputed queues the backfill of the computed values when the
// change touches them (WP-718). The change is stored already; a job that
// cannot be queued is logged, and the hourly refresh brings the values in
// line (ci.ComputedRefresh).
func (r *PGRepository) scheduleComputed(ctx context.Context, orgID string, before, after *AttributeDefinition) {
	if r.computed == nil || !computedRelevant(before, after) {
		return
	}
	d := after
	if d == nil {
		d = before
	}
	b := ComputedBackfill{DefinitionID: d.ID, Key: d.Key, CITypeID: d.CITypeID, Deleted: after == nil}
	if err := r.computed.ScheduleBackfill(context.WithoutCancel(ctx), orgID, b); err != nil {
		slog.Error("schedule computed attribute backfill", "definition", d.ID, "error", err)
	}
}

// checkUniqueValuesTx refuses to make an attribute unique while CIs share
// a value of it in the scope (MET-15); the unique index could not be built.
// A type definition with inheritable covers the subtree of its type, one
// without the type (MET-06, WP-708), so a unique definition is checked
// also when inheritable changes (409 on inheritable).
func checkUniqueValuesTx(ctx context.Context, tx pgx.Tx, before, d *AttributeDefinition) error {
	if !d.Unique {
		return nil
	}
	field := "unique"
	if before != nil && before.Unique && before.UniqueScope == d.UniqueScope {
		if before.Inheritable == d.Inheritable || d.CITypeID == "" {
			return nil
		}
		field = "inheritable"
	}
	var duplicate bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ci
			WHERE deleted_at IS NULL AND attributes ->> $1 IS NOT NULL
			  AND ($2::uuid IS NULL
			       OR (NOT $4 AND ci_type_id = $2::uuid)
			       OR ($4 AND ci_type_id IN (SELECT s.id FROM ci_type_subtree($2::uuid) s)))
			GROUP BY organization_id, CASE WHEN $3 = 'client' THEN client_id END, attributes ->> $1
			HAVING count(*) > 1)`, d.Key, nilIfEmpty(d.CITypeID), d.UniqueScope, d.Inheritable).Scan(&duplicate)
	if err != nil {
		return fmt.Errorf("check attribute values: %w", err)
	}
	if duplicate {
		return &api.FieldError{Field: field, Message: "CIs already share a value of this attribute; resolve the duplicates first",
			Status: http.StatusConflict}
	}
	return nil
}

// checkInheritedUniqueTx refuses a parent change of the type id that
// brings CIs with the same value of an inherited unique attribute into one
// subtree (MET-06, MET-15, WP-708): the unique, inheritable definitions of
// the new ancestors, checked over the subtree of their type after the
// change (409 on parent_type_id).
func checkInheritedUniqueTx(ctx context.Context, tx pgx.Tx, id string) error {
	var duplicate bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM attribute_definition d
			JOIN ci_type_ancestors($1::uuid) a ON a.id = d.ci_type_id
			WHERE d.is_unique AND d.inheritable AND d.ci_type_id <> $1::uuid
			  AND EXISTS (
				SELECT 1 FROM ci c
				WHERE c.deleted_at IS NULL AND c.attributes ->> d.key IS NOT NULL
				  AND c.ci_type_id IN (SELECT s.id FROM ci_type_subtree(d.ci_type_id) s)
				GROUP BY c.organization_id, CASE WHEN d.unique_scope = 'client' THEN c.client_id END, c.attributes ->> d.key
				HAVING count(*) > 1))`, id).Scan(&duplicate)
	if err != nil {
		return fmt.Errorf("check inherited unique attributes: %w", err)
	}
	if duplicate {
		return &api.FieldError{Field: "parent_type_id", Status: http.StatusConflict,
			Message: "CIs of the type and of the new parent's subtree share a value of an inherited unique attribute; resolve the duplicates first"}
	}
	return nil
}

func isUUIDText(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}
