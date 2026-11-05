package graphqlbff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// The resolved form of a CI (GQL-03, WP-792): ciForm resolves through
// citype.FormResolver of WP-719 (Sources.Forms) with the field policy of
// GET /api/v1/cis/{id}/form (WP-727). It needs ci:read and cmdb_core like
// the query ci and lies under no module gate (core prefix cis, E-105). The
// preview follows the rule of REST with citype:manage: a template is a
// preview (preview=true), which needs citype:manage; a role of the caller
// alone chooses the layout of that role as role_id without preview does;
// another role needs citype:manage and is then resolved as a preview of
// that role.

// CIForm implements generated.ResolverRoot.
func (r *Resolver) CIForm() generated.CIFormResolver { return &ciFormResolver{r} }

// FormSection implements generated.ResolverRoot.
func (r *Resolver) FormSection() generated.FormSectionResolver { return &formSectionResolver{r} }

// FormField implements generated.ResolverRoot.
func (r *Resolver) FormField() generated.FormFieldResolver { return &formFieldResolver{r} }

// FormRule implements generated.ResolverRoot.
func (r *Resolver) FormRule() generated.FormRuleResolver { return &formRuleResolver{r} }

type (
	ciFormResolver      struct{ *Resolver }
	formSectionResolver struct{ *Resolver }
	formFieldResolver   struct{ *Resolver }
	formRuleResolver    struct{ *Resolver }
)

func (q *queryResolver) CiForm(ctx context.Context, id string, template, role *string) (*citype.ResolvedForm, error) {
	orgID, err := q.authorize(ctx, identity.PermCIRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	if q.sources.Forms == nil {
		return nil, errUnavailable()
	}
	principal, _ := identity.PrincipalFromContext(ctx)
	manage := principal.Has(identity.PermCITypeManage)
	req := citype.FormRequest{CIID: strings.TrimSpace(id), TemplateID: value(template), RoleID: value(role)}
	if req.TemplateID != "" {
		if !manage {
			return nil, gqlError(codeForbidden, "a form preview requires "+string(identity.PermCITypeManage))
		}
		req.Preview = true
	}
	form, err := q.sources.Forms.ResolveForm(ctx, orgID, req)
	if errors.Is(err, citype.ErrFormRoleNotOwn) {
		if !manage {
			return nil, gqlError(codeForbidden, "another role than the caller's requires "+string(identity.PermCITypeManage))
		}
		req.Preview = true
		form, err = q.sources.Forms.ResolveForm(ctx, orgID, req)
	}
	if errors.Is(err, citype.ErrFormCINotFound) {
		// Not visible or not there: null, as for the query ci.
		return nil, nil //nolint:nilnil // GraphQL null for an invisible CI
	}
	if err != nil {
		return nil, formError(err)
	}
	return form, nil
}

// formError maps a failed resolution: an unknown template or role and a
// refused template are bad input.
func formError(err error) error {
	for _, notFound := range []error{citype.ErrFormTemplateNotFound, citype.ErrFormRoleNotFound} {
		if errors.Is(err, notFound) {
			return gqlError(codeBadInput, notFound.Error())
		}
	}
	var fe *api.FieldError
	if errors.As(err, &fe) {
		return gqlError(codeBadInput, fe.Error())
	}
	return err
}

func (c *ciFormResolver) TemplateID(_ context.Context, obj *citype.ResolvedForm) (*string, error) {
	return optional(obj.TemplateID), nil
}

func (c *ciFormResolver) RoleID(_ context.Context, obj *citype.ResolvedForm) (*string, error) {
	return optional(obj.RoleID), nil
}

func (c *ciFormResolver) LayoutID(_ context.Context, obj *citype.ResolvedForm) (*string, error) {
	return optional(obj.LayoutID), nil
}

// RequiredFields are the fields required in the current state, in the
// order of the form, each once.
func (c *ciFormResolver) RequiredFields(_ context.Context, obj *citype.ResolvedForm) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for i := range obj.Sections {
		for j := range obj.Sections[i].Fields {
			f := &obj.Sections[i].Fields[j]
			if f.State.Required && !seen[f.Name] {
				seen[f.Name] = true
				out = append(out, f.Name)
			}
		}
	}
	return out, nil
}

func (s *formSectionResolver) Title(_ context.Context, obj *citype.FormSection) (map[string]any, error) {
	out := make(map[string]any, len(obj.Title))
	for lang, text := range obj.Title {
		out[lang] = text
	}
	return out, nil
}

func (f *formFieldResolver) Definition(_ context.Context, obj *citype.FormField) (map[string]any, error) {
	return jsonObject(&obj.Definition)
}

func (f *formRuleResolver) Condition(_ context.Context, obj *citype.FormRule) (map[string]any, error) {
	if obj.Condition == nil {
		return nil, nil
	}
	return jsonObject(obj.Condition)
}

func (f *formRuleResolver) Ast(_ context.Context, obj *citype.FormRule) (map[string]any, error) {
	if obj.AST == nil {
		return nil, nil
	}
	return jsonObject(obj.AST)
}

func (f *formRuleResolver) Message(_ context.Context, obj *citype.FormRule) (map[string]any, error) {
	if obj.Message == nil {
		return nil, nil
	}
	out := make(map[string]any, len(obj.Message))
	for lang, text := range obj.Message {
		out[lang] = text
	}
	return out, nil
}

// jsonObject is the JSON form of v as an object, as REST shows it.
func jsonObject(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode %T: %w", v, err)
	}
	out := map[string]any{}
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode %T: %w", v, err)
	}
	return out, nil
}

// optional is s, null when empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
