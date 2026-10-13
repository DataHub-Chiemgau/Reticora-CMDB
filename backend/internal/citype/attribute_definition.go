package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// AttributeDefinition is one row of attribute_definition (MET-10, DB-03):
// the single source of attribute validation and indexing. A definition
// belongs to a CI type (scope "type") or applies to every type of the
// organization (scope "global", no CI type); organization_id NULL marks a
// read-only system definition. ci_type.attribute_schema is generated from
// the type's definitions by the database.
type AttributeDefinition struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organization_id,omitempty"`
	CITypeID       string        `json:"ci_type_id,omitempty"`
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
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                      `json:"reference_target,omitempty"`
	Group           string                      `json:"group,omitempty"`
	Order           int                         `json:"order"`
	Unit            string                      `json:"unit,omitempty"`
	IsSystem        bool                        `json:"is_system"`
	CreatedAt       time.Time                   `json:"created_at"`
	UpdatedAt       time.Time                   `json:"updated_at"`
}

// AttributeDefinitionPatch changes a definition; key, CI type and scope
// are immutable.
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
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget *string                     `json:"reference_target,omitempty"`
	Group           *string                     `json:"group,omitempty"`
	Order           *int                        `json:"order,omitempty"`
	Unit            *string                     `json:"unit,omitempty"`
}

// AttributeFilter narrows the definition list.
type AttributeFilter struct {
	CITypeID string
	Scope    string
}

// Attribute definition errors.
var (
	ErrAttributeNotFound = errors.New("attribute definition not found")
	// ErrAttributeReadOnly: system definitions cannot be changed.
	ErrAttributeReadOnly = errors.New("system attribute definitions are read-only")
)

// AttributeDefinitionStore is the attribute-definitions resource
// (API-05); the PostgreSQL repository implements it.
type AttributeDefinitionStore interface {
	ListAttributeDefinitions(ctx context.Context, orgID string, filter AttributeFilter) ([]AttributeDefinition, error)
	GetAttributeDefinition(ctx context.Context, orgID, id string) (*AttributeDefinition, error)
	CreateAttributeDefinition(ctx context.Context, orgID string, def *AttributeDefinition) error
	UpdateAttributeDefinition(ctx context.Context, orgID, id string, patch *AttributeDefinitionPatch) (*AttributeDefinition, error)
	DeleteAttributeDefinition(ctx context.Context, orgID, id string) error
}

// Field is the legacy field view of the definition (/ci-types/{id}/fields).
func (d *AttributeDefinition) Field() Field {
	return Field{
		ID: d.ID, CITypeID: d.CITypeID, Scope: d.Scope, Name: d.Key,
		Label: d.DisplayName.WithFallback(d.Key)["en"], Description: d.Description,
		DataType: d.DataType, Required: d.Required, DefaultValue: defaultString(d.Default),
		EnumValues: d.EnumValues, UIGroup: d.Group, SortOrder: d.Order,
		Validation: d.Validation, Conditional: d.Conditional, ReferenceTarget: d.ReferenceTarget,
	}
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
	if err := fieldmeta.ValidateDefinition(d.definition()); err != nil {
		return &api.FieldError{Field: "data_type", Message: err.Error()}
	}
	if len(d.Default) > 0 && string(d.Default) != "null" {
		var value any
		if err := json.Unmarshal(d.Default, &value); err != nil {
			return &api.FieldError{Field: "default", Message: "default must be a JSON value"}
		}
		values := []any{value}
		if list, ok := value.([]any); ok && d.MultiValue {
			values = list
		}
		for _, v := range values {
			if err := fieldmeta.ValidateValue(d.definition(), v); err != nil {
				return &api.FieldError{Field: "default", Message: err.Error()}
			}
		}
	}
	return nil
}

func (p *AttributeDefinitionPatch) apply(d *AttributeDefinition) {
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
	if p.Validation != nil {
		d.Validation = p.Validation
	}
	if p.Conditional != nil {
		d.Conditional = p.Conditional
	}
	setString(&d.ReferenceTarget, p.ReferenceTarget)
	setString(&d.Group, p.Group)
	if p.Order != nil {
		d.Order = *p.Order
	}
	setString(&d.Unit, p.Unit)
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
	"attribute_definition_ci_type_id_key_key":   "key",
	"attribute_definition_global_key":           "key",
	"attribute_definition_display_name_i18n":    "display_name",
	"attribute_definition_unique_scope_check":   "unique_scope",
	"attribute_definition_scope_check":          "scope",
	"attribute_definition_ci_type_id_fkey":      "ci_type_id",
	"attribute_definition_scope_type_check":     "ci_type_id",
	"attribute_definition_system_global_key":    "key",
	"attribute_definition_organization_id_fkey": "organization_id",
}

const attributeColumns = `id::text, COALESCE(organization_id::text, ''), COALESCE(ci_type_id::text, ''), scope, key,
	display_name, COALESCE(description, ''), data_type, required, default_value, enum_values,
	multi_value, searchable, is_unique, unique_scope, inheritable, validation, conditional,
	COALESCE(reference_target, ''), COALESCE(group_name, ''), sort_order, COALESCE(unit, ''),
	created_at, updated_at`

func scanAttribute(row pgx.Row) (*AttributeDefinition, error) {
	var (
		d                                                      AttributeDefinition
		displayName, def, enumRaw, validationRaw, conditionRaw []byte
	)
	if err := row.Scan(&d.ID, &d.OrganizationID, &d.CITypeID, &d.Scope, &d.Key, &displayName, &d.Description,
		&d.DataType, &d.Required, &def, &enumRaw, &d.MultiValue, &d.Searchable, &d.Unique, &d.UniqueScope,
		&d.Inheritable, &validationRaw, &conditionRaw, &d.ReferenceTarget, &d.Group, &d.Order, &d.Unit,
		&d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(displayName, &d.DisplayName); err != nil {
		return nil, fmt.Errorf("decode display_name: %w", err)
	}
	if len(def) > 0 && string(def) != "null" {
		d.Default = json.RawMessage(def)
	}
	if len(enumRaw) > 0 {
		_ = json.Unmarshal(enumRaw, &d.EnumValues)
	}
	if len(validationRaw) > 0 && string(validationRaw) != "{}" {
		var v fieldmeta.ValidationRules
		if json.Unmarshal(validationRaw, &v) == nil {
			d.Validation = &v
		}
	}
	if len(conditionRaw) > 0 && string(conditionRaw) != "null" {
		var c fieldmeta.ConditionalRules
		if json.Unmarshal(conditionRaw, &c) == nil {
			d.Conditional = &c
		}
	}
	d.IsSystem = d.OrganizationID == ""
	return &d, nil
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
	return []any{d.DisplayName, nilIfEmpty(d.Description), d.DataType, d.Required, def, enum, d.MultiValue,
		d.Searchable, d.Unique, d.UniqueScope, d.Inheritable, validation, conditional,
		nilIfEmpty(d.ReferenceTarget), nilIfEmpty(d.Group), d.Order, nilIfEmpty(d.Unit)}
}

// insertAttributeTx inserts a definition; a type-scoped definition takes
// the organization of its type (trigger), a global one orgID.
func insertAttributeTx(ctx context.Context, tx pgx.Tx, orgID string, d *AttributeDefinition) error {
	args := append([]any{nilIfEmpty(d.CITypeID), orgID, d.Scope, d.Key}, attributeArgs(d)...)
	created, err := scanAttribute(tx.QueryRow(ctx, `INSERT INTO attribute_definition (
			ci_type_id, organization_id, scope, key, display_name, description, data_type, required,
			default_value, enum_values, multi_value, searchable, is_unique, unique_scope, inheritable,
			validation, conditional, reference_target, group_name, sort_order, unit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		RETURNING `+attributeColumns, args...))
	if err != nil {
		return api.FieldDBError(err, attributeFields)
	}
	*d = *created
	return nil
}

func updateAttributeTx(ctx context.Context, tx pgx.Tx, d *AttributeDefinition) error {
	args := append([]any{d.ID}, attributeArgs(d)...)
	updated, err := scanAttribute(tx.QueryRow(ctx, `UPDATE attribute_definition SET
			display_name = $2, description = $3, data_type = $4, required = $5, default_value = $6,
			enum_values = $7, multi_value = $8, searchable = $9, is_unique = $10, unique_scope = $11,
			inheritable = $12, validation = $13, conditional = $14, reference_target = $15,
			group_name = $16, sort_order = $17, unit = $18
		WHERE id = $1 RETURNING `+attributeColumns, args...))
	if err != nil {
		return api.FieldDBError(err, attributeFields)
	}
	*d = *updated
	return nil
}

// checkAttributeType resolves the CI type of a new type-scoped definition:
// it must be a type of the organization (system types are extended by
// inheritance, MET-06, P2).
func checkAttributeType(ctx context.Context, tx pgx.Tx, typeID string) error {
	var org string
	err := tx.QueryRow(ctx, `SELECT COALESCE(organization_id::text, '') FROM ci_type WHERE id::text = $1`, typeID).Scan(&org)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &api.FieldError{Field: "ci_type_id", Message: "ci type not found", Status: 422}
	case err != nil:
		return fmt.Errorf("read ci type: %w", err)
	case org == "":
		return &api.FieldError{Field: "ci_type_id", Message: "system ci types cannot get organization attributes", Status: 422}
	}
	return nil
}

// ListAttributeDefinitions implements AttributeDefinitionStore: the
// organization's and the system's definitions.
func (r *PGRepository) ListAttributeDefinitions(ctx context.Context, orgID string, filter AttributeFilter) ([]AttributeDefinition, error) {
	out := []AttributeDefinition{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := []string{"TRUE"}, []any{}
		if filter.CITypeID != "" {
			args = append(args, filter.CITypeID)
			where = append(where, fmt.Sprintf("ci_type_id::text = $%d", len(args)))
		}
		if filter.Scope != "" {
			args = append(args, filter.Scope)
			where = append(where, fmt.Sprintf("scope = $%d", len(args)))
		}
		rows, err := tx.Query(ctx, `SELECT `+attributeColumns+` FROM attribute_definition WHERE `+
			strings.Join(where, " AND ")+` ORDER BY ci_type_id NULLS FIRST, sort_order, key`, args...)
		if err != nil {
			return fmt.Errorf("list attribute definitions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanAttribute(rows)
			if err != nil {
				return fmt.Errorf("scan attribute definition: %w", err)
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, err
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

// CreateAttributeDefinition implements AttributeDefinitionStore.
func (r *PGRepository) CreateAttributeDefinition(ctx context.Context, orgID string, d *AttributeDefinition) error {
	d.Scope = "global"
	if d.CITypeID != "" {
		d.Scope = "type"
	}
	if err := d.Validate(); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if d.CITypeID != "" {
			if err := checkAttributeType(ctx, tx, d.CITypeID); err != nil {
				return err
			}
		}
		return insertAttributeTx(ctx, tx, orgID, d)
	})
}

// UpdateAttributeDefinition implements AttributeDefinitionStore.
func (r *PGRepository) UpdateAttributeDefinition(ctx context.Context, orgID, id string, patch *AttributeDefinitionPatch) (*AttributeDefinition, error) {
	var out *AttributeDefinition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := lockOwnAttributeTx(ctx, tx, id)
		if err != nil {
			return err
		}
		patch.apply(d)
		if verr := d.Validate(); verr != nil {
			return verr
		}
		if uerr := updateAttributeTx(ctx, tx, d); uerr != nil {
			return uerr
		}
		out = d
		return nil
	})
	return out, err
}

// DeleteAttributeDefinition implements AttributeDefinitionStore.
func (r *PGRepository) DeleteAttributeDefinition(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockOwnAttributeTx(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM attribute_definition WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete attribute definition: %w", err)
		}
		return nil
	})
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
