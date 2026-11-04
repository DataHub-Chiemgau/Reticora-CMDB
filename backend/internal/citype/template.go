package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// Template is a versioned, ordered bundle of attribute definitions for one
// CI type (MET-16, WP-251); a CI takes several templates in a defined
// order (ci_template, WP-710). organization_id NULL marks a read-only
// system template (E-10). Templates introduce no attribute keys of their
// own (E-37): the items of a version reference attribute definitions of
// the template's type chain or global ones and only narrow them.
// It is not a type template of the catalog (Templates, GET
// /api/v1/ci-types/templates, ci_type.template_key).
type Template struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organization_id,omitempty"`
	Key            string        `json:"key"`
	CITypeID       string        `json:"ci_type_id"`
	DisplayName    LocalizedText `json:"display_name"`
	Description    string        `json:"description,omitempty"`
	IsSystem       bool          `json:"is_system"`
	IsActive       bool          `json:"is_active"`
	// CurrentVersion is the last published version; nil until one is
	// published.
	CurrentVersion *int `json:"current_version"`
	// ClonedFromID names the template a clone was made from.
	ClonedFromID string    `json:"cloned_from_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TemplatePatch changes a template. key and ci_type_id are immutable: the
// fields are read only to refuse them (422).
type TemplatePatch struct {
	DisplayName LocalizedText `json:"display_name,omitempty"`
	Description *string       `json:"description,omitempty"`
	IsActive    *bool         `json:"is_active,omitempty"`
	Key         *string       `json:"key,omitempty"`
	CITypeID    *string       `json:"ci_type_id,omitempty"`
}

// TemplateClone is the request of POST /api/v1/templates/{id}/clone: the
// new key and, optionally, its display name and description (default: the
// source's).
type TemplateClone struct {
	Key         string        `json:"key"`
	DisplayName LocalizedText `json:"display_name,omitempty"`
	Description *string       `json:"description,omitempty"`
}

// Status of a template version (MET-53).
const (
	TemplateVersionDraft     = "draft"
	TemplateVersionPublished = "published"
	TemplateVersionRetired   = "retired"
)

// TemplateVersion is one version of a template with its items in position
// order. Published and retired versions are immutable; a draft becomes
// published by POST …/publish (MET-04, MET-53).
type TemplateVersion struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id,omitempty"`
	TemplateID     string     `json:"template_id"`
	Version        int        `json:"version"`
	Status         string     `json:"status"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	PublishedBy    string     `json:"published_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// Items are the items in position order.
	Items []TemplateVersionItem `json:"items"`
}

// TemplateVersionItem references one attribute definition with the
// overrides of the version (MET-16). The overrides only narrow: required
// is OR-ed, the validation (a rule of MET-45 or the legacy structure)
// composes like a layer of Compose, the default must be valid for the
// narrowed definition. Key is the key of the definition (read only).
type TemplateVersionItem struct {
	AttributeDefinitionID string          `json:"attribute_definition_id"`
	Key                   string          `json:"key,omitempty"`
	Position              int             `json:"position"`
	RequiredOverride      *bool           `json:"required_override,omitempty"`
	DefaultOverride       json.RawMessage `json:"default_override,omitempty"`
	ValidationOverride    json.RawMessage `json:"validation_override,omitempty"`
}

// TemplateItemInput is an item of PUT …/versions/{version}. The fields
// searchable, unique, unique_scope and data_type cannot be overridden
// (MET-15: they stay with the attribute definition); they are read only
// to refuse them with 422.
type TemplateItemInput struct {
	TemplateVersionItem
	Searchable  json.RawMessage `json:"searchable,omitempty"`
	Unique      json.RawMessage `json:"unique,omitempty"`
	UniqueScope json.RawMessage `json:"unique_scope,omitempty"`
	DataType    json.RawMessage `json:"data_type,omitempty"`
}

// Template errors.
var (
	ErrTemplateNotFound        = errors.New("template not found")
	ErrTemplateVersionNotFound = errors.New("template version not found")
	// ErrTemplateReadOnly: system templates cannot be changed, deleted,
	// versioned or published by an organization (E-10); they can be
	// cloned.
	ErrTemplateReadOnly = errors.New("system templates are read-only")
	// ErrTemplateVersionImmutable: a published or retired version cannot
	// be changed.
	ErrTemplateVersionImmutable = errors.New("published and retired template versions are immutable")
	// ErrTemplateVersionNotDraft: only a draft that is newer than the
	// current version can be published.
	ErrTemplateVersionNotDraft = errors.New("only a draft newer than the current version can be published")
)

// TemplateFilter narrows the template list.
type TemplateFilter struct {
	CITypeID string
}

// TemplateStore is the templates resource with its versions (API-06,
// MET-16, MET-53, WP-251); the PostgreSQL repository implements it.
type TemplateStore interface {
	// ListTemplatePage returns a page newest first on (created_at, id), at
	// most one template more than the limit, and the number the filter
	// selects.
	ListTemplatePage(ctx context.Context, orgID string, filter TemplateFilter, page api.PaginationParams) ([]Template, int, error)
	GetTemplate(ctx context.Context, orgID, id string) (*Template, error)
	// CreateTemplate creates a template of the organization with version
	// 1 as an empty draft.
	CreateTemplate(ctx context.Context, orgID string, t *Template) error
	UpdateTemplate(ctx context.Context, orgID, id string, patch *TemplatePatch) (*Template, error)
	DeleteTemplate(ctx context.Context, orgID, id string) error
	// CloneTemplate copies a template (also a system template) into a new
	// template of the organization whose version 1 is a draft with the
	// items of the source's current version.
	CloneTemplate(ctx context.Context, orgID, id string, req *TemplateClone) (*Template, error)
	ListTemplateVersionPage(ctx context.Context, orgID, id string, page api.PaginationParams) ([]TemplateVersion, int, error)
	GetTemplateVersion(ctx context.Context, orgID, id string, version int) (*TemplateVersion, error)
	// CreateTemplateVersion adds the next version as a draft copy of the
	// current one.
	CreateTemplateVersion(ctx context.Context, orgID, id string) (*TemplateVersion, error)
	// SetTemplateVersionItems replaces the item list of a draft.
	SetTemplateVersionItems(ctx context.Context, orgID, id string, version int, items []TemplateVersionItem) (*TemplateVersion, error)
	// PublishTemplateVersion publishes a draft after the composition check.
	PublishTemplateVersion(ctx context.Context, orgID, id string, version int) (*TemplateVersion, error)
}

// validateTemplateKey checks a new key.
func validateTemplateKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || utf8.RuneCountInString(key) > 100 {
		return "", &api.FieldError{Field: "key", Message: "key is required (at most 100 characters)"}
	}
	return key, nil
}

// Validate checks a new template.
func (t *Template) Validate() error {
	key, err := validateTemplateKey(t.Key)
	if err != nil {
		return err
	}
	t.Key = key
	if !api.IsUUID(t.CITypeID) {
		return &api.FieldError{Field: "ci_type_id", Message: "ci_type_id must name a ci type", Status: http.StatusUnprocessableEntity}
	}
	if err := t.DisplayName.Validate(); err != nil {
		return &api.FieldError{Field: "display_name", Message: err.Error()}
	}
	t.DisplayName = t.DisplayName.WithFallback(t.Key)
	return nil
}

// apply changes t; key and ci_type_id are immutable (422).
func (p *TemplatePatch) apply(t *Template) error {
	if p.Key != nil {
		return &api.FieldError{Field: "key", Message: "key is immutable", Status: http.StatusUnprocessableEntity}
	}
	if p.CITypeID != nil {
		return &api.FieldError{Field: "ci_type_id", Message: "ci_type_id is immutable", Status: http.StatusUnprocessableEntity}
	}
	if p.DisplayName != nil {
		if err := p.DisplayName.Validate(); err != nil {
			return &api.FieldError{Field: "display_name", Message: err.Error()}
		}
		t.DisplayName = p.DisplayName.WithFallback(t.Key)
	}
	setString(&t.Description, p.Description)
	setBool(&t.IsActive, p.IsActive)
	return nil
}

// TemplateItems checks the shape of the items of a PUT: a field that
// cannot be overridden is a 422 on items[i].<field>. It returns the items.
func TemplateItems(in []TemplateItemInput) ([]TemplateVersionItem, error) {
	out := make([]TemplateVersionItem, 0, len(in))
	for i := range in {
		for _, f := range []struct {
			name  string
			value json.RawMessage
		}{
			{"searchable", in[i].Searchable}, {"unique", in[i].Unique},
			{"unique_scope", in[i].UniqueScope}, {"data_type", in[i].DataType},
		} {
			if f.value != nil {
				return nil, itemError(i, f.name, f.name+" cannot be overridden by a template (MET-15)")
			}
		}
		item := in[i].TemplateVersionItem
		item.Key = ""
		out = append(out, item)
	}
	return out, nil
}

// itemError is a 422 on a field of the item at index i.
func itemError(i int, field, message string) *api.FieldError {
	return &api.FieldError{Field: fmt.Sprintf("items[%d].%s", i, field), Message: message,
		Status: http.StatusUnprocessableEntity}
}

// templateItemLayers are the field definitions of an item in the template
// layer: base is the referenced definition, top its overrides. Compose
// merges top onto base like any narrower layer (MET-16).
// validation: the texts of fieldmeta.ParseValidation, rules.Rule.Validate
// and fieldmeta.ValidationFromRule for the validation override.
func templateItemLayers(def *AttributeDefinition, item *TemplateVersionItem) (base, top fieldmeta.FieldDefinition, err error) {
	base = def.definition()
	top = base
	top.Required = item.RequiredOverride != nil && *item.RequiredOverride
	top.DefaultValue = defaultString(item.DefaultOverride)
	top.Validation = nil
	rule, err := fieldmeta.ParseValidation(item.ValidationOverride)
	if err != nil {
		return base, top, err
	}
	if rule != nil {
		if verr := rule.Validate(); verr != nil {
			return base, top, verr
		}
		v, required, verr := fieldmeta.ValidationFromRule(rule)
		if verr != nil {
			return base, top, verr
		}
		top.Validation = v
		top.Required = top.Required || required
	}
	return base, top, nil
}

// checkTemplateItem checks the overrides of item i against its definition:
// the validation override must be a valid rule that composes with the
// definition (a conflict is a 422 with its code), the default override
// must be valid for the narrowed definition. It stores the validation
// override as its rule.
func checkTemplateItem(i int, def *AttributeDefinition, item *TemplateVersionItem) error {
	base, top, err := templateItemLayers(def, item)
	if err != nil {
		return itemError(i, "validation_override", err.Error())
	}
	if rule, _ := fieldmeta.ParseValidation(item.ValidationOverride); rule != nil {
		raw, merr := json.Marshal(rule)
		if merr != nil {
			return fmt.Errorf("encode validation override: %w", merr)
		}
		item.ValidationOverride = raw
	} else {
		item.ValidationOverride = nil
	}
	composed, err := Compose([]fieldmeta.FieldDefinition{base}, []fieldmeta.FieldDefinition{top})
	if err != nil {
		var conflict *CompositionError
		if errors.As(err, &conflict) {
			fe := itemError(i, "validation_override", err.Error())
			fe.Code = conflict.Code
			return fe
		}
		return err
	}
	if len(item.DefaultOverride) == 0 || string(item.DefaultOverride) == "null" {
		item.DefaultOverride = nil
		return nil
	}
	var value any
	if err := json.Unmarshal(item.DefaultOverride, &value); err != nil {
		return itemError(i, "default_override", "default_override must be a JSON value")
	}
	if err := fieldmeta.ValidateValue(&composed[0], value); err != nil {
		return itemError(i, "default_override", err.Error())
	}
	return nil
}
