package citype

import (
	"strconv"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// Checks of the payloads of metamodel draft items (WP-720, MET-53): the
// checks of the resource routes that need no stored row, exported for the
// package metamodel, which captures a resource request into a draft
// instead of running it (POST …/items and ?draft). The checks against the
// stored metamodel (composition, uniqueness, data type conflicts) belong to
// the test run of the draft (WP-721). Each check answers like its resource
// route: a field error with 400 or 422.

// CheckCreateType checks the request of POST /api/v1/ci-types like Create:
// the name, display_name, the fields and the lifecycle model.
// validation: the texts of LocalizedText.Validate, fieldmeta.ValidateDefinition
// and CreateTypeRequest.LifecycleModel.
func CheckCreateType(req *CreateTypeRequest) error {
	if req.Name == "" {
		return &api.FieldError{Field: "name", Message: "name is required"}
	}
	if err := req.DisplayName.Validate(); err != nil {
		return &api.FieldError{Field: "display_name", Message: err.Error()}
	}
	for i := range req.Fields {
		if err := validateField(&req.Fields[i]); err != nil {
			return &api.FieldError{Field: "fields[" + strconv.Itoa(i) + "]." + definitionField(err), Message: err.Error()}
		}
	}
	_, err := req.LifecycleModel()
	return err
}

// CheckUpdateType checks the request of PATCH /api/v1/ci-types/{id} like
// Update: display_name and the lifecycle model.
// validation: the texts of LocalizedText.Validate and
// UpdateTypeRequest.LifecycleModel.
func CheckUpdateType(req *UpdateTypeRequest) error {
	if err := req.DisplayName.Validate(); err != nil {
		return &api.FieldError{Field: "display_name", Message: err.Error()}
	}
	_, err := req.LifecycleModel()
	return err
}

// CheckUpsertField checks the request of PUT /api/v1/ci-types/{id}/fields
// and PUT /api/v1/ci-fields/global like UpsertField: the definition
// (fieldmeta.ValidateDefinition).
// validation: the texts of fieldmeta.ValidateDefinition.
func CheckUpsertField(req *UpsertFieldRequest) error {
	fieldDef := fieldFromUpsert(req)
	if err := fieldmeta.ValidateDefinition(&fieldDef); err != nil {
		return &api.FieldError{Field: definitionField(err), Message: err.Error()}
	}
	return nil
}

// CheckNewAttributeDefinition checks the request of POST
// /api/v1/attribute-definitions like CreateAttributeDefinition and the
// repository before it writes: the patch applied to a new definition and
// AttributeDefinition.Validate.
// validation: the field errors of AttributeDefinitionPatch.apply and
// AttributeDefinition.Validate.
func CheckNewAttributeDefinition(patch *AttributeDefinitionPatch, ciTypeID, attributeSetID, key string) error {
	d := &AttributeDefinition{CITypeID: ciTypeID, AttributeSetID: attributeSetID, Key: key, UniqueScope: "org", Inheritable: true}
	if err := patch.apply(d); err != nil {
		return err
	}
	return d.Validate()
}

// CheckNewAttributeSet checks the request of POST /api/v1/attribute-sets:
// key and display_name (AttributeSet.Validate).
// validation: the field errors of AttributeSet.Validate.
func CheckNewAttributeSet(key string, displayName LocalizedText) error {
	s := &AttributeSet{Key: key, DisplayName: displayName}
	return s.Validate()
}

// CheckAttributeSetPatch checks the request of PATCH
// /api/v1/attribute-sets/{id} without the stored set: display_name.
// validation: the field errors of AttributeSetPatch.apply.
func CheckAttributeSetPatch(patch *AttributeSetPatch) error {
	return patch.apply(&AttributeSet{})
}

// CheckNewTemplate checks the request of POST /api/v1/templates: key,
// ci_type_id and display_name (Template.Validate).
// validation: the field errors of Template.Validate.
func CheckNewTemplate(key, ciTypeID string, displayName LocalizedText) error {
	t := &Template{Key: key, CITypeID: ciTypeID, DisplayName: displayName}
	return t.Validate()
}

// CheckTemplatePatch checks the request of PATCH /api/v1/templates/{id}
// without the stored template: the immutable fields and display_name.
// validation: the field errors of TemplatePatch.apply.
func CheckTemplatePatch(patch *TemplatePatch) error {
	return patch.apply(&Template{})
}

// CheckTemplateItems checks the items of PUT
// /api/v1/templates/{id}/versions/{version} (TemplateItems).
// validation: the field errors of TemplateItems.
func CheckTemplateItems(items []TemplateItemInput) error {
	_, err := TemplateItems(items)
	return err
}

// CheckNewRule checks the request of POST /api/v1/rules without the
// target: key, attribute, kind, condition, AST and message.
// validation: the field errors of newRule.
func CheckNewRule(in *RuleInput) error {
	_, err := newRule(in)
	return err
}

// NewDraftLayout checks the request of POST /api/v1/layouts without the
// target and returns the layout it describes (newLayout): the draft
// preview of the form lays it over the stored layouts (E-113).
// validation: the field errors of newLayout.
func NewDraftLayout(in *LayoutInput) (*FormLayout, error) {
	return newLayout(in)
}

// ApplyLayoutPatch applies the request of PATCH /api/v1/layouts/{id} to l
// (LayoutPatch.apply); with an empty layout it checks the request without
// the stored layout.
// validation: the field errors of LayoutPatch.apply.
func ApplyLayoutPatch(patch *LayoutPatch, l *FormLayout) error {
	return patch.apply(l)
}
