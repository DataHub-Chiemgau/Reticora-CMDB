package citype

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// CITemplate is a template assigned to a CI (ci_template, MET-16, WP-710):
// the template, its key (read only), the published version the CI uses
// and its position in the order of the CI's templates. In a write only
// TemplateID and Version count; without a version the template's
// current_version applies, the position is the index in the list. ci uses
// the type for the template list of a CI, so ci needs no type of its own
// for the parameters of the template resolution (ci imports citype, not
// the other way round).
type CITemplate struct {
	TemplateID string `json:"template_id"`
	Key        string `json:"key,omitempty"`
	Version    int    `json:"version,omitempty"`
	Position   int    `json:"position"`
}

// Details of a refused template assignment (422 on templates[i]).
const (
	templateRefNotFound    = "template not found"
	templateRefTwice       = "the template is listed twice"
	templateRefInactive    = "the template is deactivated"
	templateRefOtherType   = "the template belongs to a CI type outside the type chain of the CI"
	templateRefNoVersion   = "the template has no such version"
	templateRefUnpublished = "only a published template version can be assigned"
)

// templateRefError is the 422 of the assignment at index i.
func templateRefError(i int, message string) *api.FieldError {
	return &api.FieldError{Field: fmt.Sprintf("templates[%d]", i), Message: message, Status: http.StatusUnprocessableEntity}
}

// ResolveTemplateRefsTx checks the template list of a CI of type ciTypeID
// in tx, under the row level security of the caller, because the foreign
// keys of ci_template are checked without it (WP-710): every entry names
// a different, active template of the organization or a system template
// whose CI type is the type of the CI or a type of its chain (WP-248), and
// a published version of it; without a version the current version
// applies. A refusal is a 422 on templates[i] (*api.FieldError). It
// returns the list with keys, versions and positions (the index).
func ResolveTemplateRefsTx(ctx context.Context, tx pgx.Tx, ciTypeID string, refs []CITemplate) ([]CITemplate, error) {
	out := make([]CITemplate, 0, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	ids := make([]string, len(refs))
	versions := make([]int32, len(refs))
	seen := map[string]bool{}
	for i := range refs {
		id := strings.ToLower(strings.TrimSpace(refs[i].TemplateID))
		switch {
		case !api.IsUUID(id) || !api.IsUUID(ciTypeID):
			return nil, templateRefError(i, templateRefNotFound)
		case seen[id]:
			return nil, templateRefError(i, templateRefTwice)
		case refs[i].Version < 0:
			return nil, templateRefError(i, templateRefNoVersion)
		}
		seen[id] = true
		ids[i], versions[i] = id, int32(min(refs[i].Version, 1<<30)) //nolint:gosec // bounded above
	}
	rows, err := tx.Query(ctx, `SELECT r.ord::int, COALESCE(t.id::text, ''), COALESCE(t.key, ''), COALESCE(t.is_active, false),
			COALESCE(t.ci_type_id IN (SELECT c.id FROM ci_type_ancestors($1::uuid) c), false),
			COALESCE(v.version, 0), COALESCE(v.status, '')
		FROM unnest($2::uuid[], $3::int[]) WITH ORDINALITY AS r(template_id, version, ord)
		LEFT JOIN template t ON t.id = r.template_id
		LEFT JOIN template_version v ON v.template_id = t.id
			AND v.version = COALESCE(NULLIF(r.version, 0), t.current_version)
		ORDER BY r.ord`, ciTypeID, ids, versions)
	if err != nil {
		return nil, fmt.Errorf("check ci templates: %w", err)
	}
	type check struct {
		ord             int
		id, key, status string
		active, inChain bool
		version         int
	}
	checks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (check, error) {
		var c check
		serr := row.Scan(&c.ord, &c.id, &c.key, &c.active, &c.inChain, &c.version, &c.status)
		return c, serr
	})
	if err != nil {
		return nil, fmt.Errorf("check ci templates: %w", err)
	}
	for i := range checks {
		c := &checks[i]
		switch {
		case c.id == "":
			return nil, templateRefError(i, templateRefNotFound)
		case !c.active:
			return nil, templateRefError(i, templateRefInactive)
		case !c.inChain:
			return nil, templateRefError(i, templateRefOtherType)
		case c.version == 0:
			return nil, templateRefError(i, templateRefNoVersion)
		case c.status != TemplateVersionPublished:
			return nil, templateRefError(i, templateRefUnpublished)
		}
		out = append(out, CITemplate{TemplateID: c.id, Key: c.key, Version: c.version, Position: i})
	}
	return out, nil
}

// templateItem is an item of an assigned template version with its
// attribute definition.
type templateItem struct {
	def  AttributeDefinition
	item TemplateVersionItem
}

// templateItemsJSON is the JSON array of the items of the template version
// (tid, version) in position order, each with its attribute definition
// (to_jsonb) and its overrides; templateItemsFromJSON decodes it.
func templateItemsJSON(tid, version string) string {
	return `COALESCE((SELECT jsonb_agg(jsonb_build_object('def', to_jsonb(a), 'required_override', i.required_override,
			'default_override', i.default_override, 'validation_override', i.validation_override) ORDER BY i.position)
		FROM template_version v JOIN template_version_item i ON i.template_version_id = v.id
		JOIN attribute_definition a ON a.id = i.attribute_definition_id
		WHERE v.template_id = ` + tid + ` AND v.version = ` + version + `), '[]'::jsonb)`
}

// templateLayersFromJSON decodes a JSON array of templateItemsJSON arrays,
// one per template in position order.
func templateLayersFromJSON(raw []byte) ([][]templateItem, error) {
	var layers []json.RawMessage
	if err := json.Unmarshal(raw, &layers); err != nil {
		return nil, fmt.Errorf("decode ci templates: %w", err)
	}
	out := make([][]templateItem, 0, len(layers))
	for _, layer := range layers {
		items, err := templateItemsFromJSON(layer)
		if err != nil {
			return nil, err
		}
		out = append(out, items)
	}
	return out, nil
}

// templateItemsFromJSON decodes one templateItemsJSON array.
func templateItemsFromJSON(raw []byte) ([]templateItem, error) {
	var rows []struct {
		Def                json.RawMessage `json:"def"`
		RequiredOverride   *bool           `json:"required_override"`
		DefaultOverride    json.RawMessage `json:"default_override"`
		ValidationOverride json.RawMessage `json:"validation_override"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode template items: %w", err)
	}
	defs := make([]string, len(rows))
	for i := range rows {
		defs[i] = string(rows[i].Def)
	}
	attrs, err := attributesFromJSON([]byte("[" + strings.Join(defs, ",") + "]"))
	if err != nil {
		return nil, err
	}
	nonNull := func(v json.RawMessage) json.RawMessage {
		if len(v) == 0 || string(v) == "null" {
			return nil
		}
		return v
	}
	out := make([]templateItem, len(rows))
	for i := range rows {
		out[i] = templateItem{def: attrs[i], item: TemplateVersionItem{
			AttributeDefinitionID: attrs[i].ID, Key: attrs[i].Key, Position: i,
			RequiredOverride: rows[i].RequiredOverride, DefaultOverride: nonNull(rows[i].DefaultOverride),
			ValidationOverride: nonNull(rows[i].ValidationOverride),
		}}
	}
	return out, nil
}

// templateLayers turns the items of the templates of a CI, in position
// order, into their composition layers between the sets and the instance
// (MET-16): one layer per template with the overrides of each item. A key
// that no lower layer and no earlier template defines (a global attribute
// that does not apply to the type, WP-255) brings its definition first.
// The overrides only narrow: required is OR-ed, the validation composes,
// a default of a later template wins over an earlier one; an item without
// a default override keeps the default below it.
func templateLayers(lower [][]fieldmeta.FieldDefinition, templates [][]templateItem) ([][]fieldmeta.FieldDefinition, error) {
	if len(templates) == 0 {
		return nil, nil
	}
	present := map[string]bool{}
	for _, layer := range lower {
		for i := range layer {
			present[layer[i].Name] = true
		}
	}
	out := make([][]fieldmeta.FieldDefinition, 0, len(templates))
	for _, items := range templates {
		layer := make([]fieldmeta.FieldDefinition, 0, 2*len(items))
		for i := range items {
			base, top, err := templateItemLayers(&items[i].def, &items[i].item)
			if err != nil {
				return nil, fmt.Errorf("template item %s: %w", items[i].def.Key, err)
			}
			if !present[base.Name] {
				layer = append(layer, base)
				present[base.Name] = true
			}
			layer = append(layer, top)
		}
		out = append(out, layer)
	}
	return out, nil
}

// ciLayerLister reads the instance definitions and the template layers of a
// stored CI in one statement (PGRepository).
type ciLayerLister interface {
	ciLayers(ctx context.Context, orgID, ciID string) ([]InstanceField, [][]templateItem, error)
}

// ciLayers implements ciLayerLister: the instance definitions of the CI as
// ListInstanceFields reads them and the items of its templates (ci_template
// in position order) in one statement under row level security, because
// ResolveFields runs on every CI write, also in the budgeted ingest
// (TestIngestStatementBudget, TestRediscoverStatementBudget): the template
// layer costs no statement more.
func (r *PGRepository) ciLayers(ctx context.Context, orgID, ciID string) ([]InstanceField, [][]templateItem, error) {
	var (
		fields    []InstanceField
		templates [][]templateItem
	)
	if !api.IsUUID(ciID) {
		return fields, templates, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var instanceRaw, templateRaw []byte
		if err := tx.QueryRow(ctx, `SELECT
				COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.sort_order, d.name)
					FROM ci_instance_field_definition d
					WHERE d.ci_id = $1 AND d.organization_id = $2 AND EXISTS (SELECT 1 FROM ci WHERE ci.id = d.ci_id)), '[]'::jsonb),
				COALESCE((SELECT jsonb_agg(`+templateItemsJSON("ct.template_id", "ct.version")+` ORDER BY ct.position)
					FROM ci_template ct WHERE ct.ci_id = $1), '[]'::jsonb)`, ciID, orgID).Scan(&instanceRaw, &templateRaw); err != nil {
			return fmt.Errorf("list instance fields and templates: %w", err)
		}
		var err error
		if fields, err = instanceFieldsFromJSON(instanceRaw); err != nil {
			return err
		}
		templates, err = templateLayersFromJSON(templateRaw)
		return err
	})
	return fields, templates, err
}

// instanceFieldsFromJSON decodes ci_instance_field_definition rows
// (to_jsonb) as scanInstanceField reads them.
func instanceFieldsFromJSON(raw []byte) ([]InstanceField, error) {
	var rows []struct {
		InstanceField
		EnumValues  json.RawMessage `json:"enum_values"`
		Validation  json.RawMessage `json:"validation"`
		Conditional json.RawMessage `json:"conditional"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode instance fields: %w", err)
	}
	var out []InstanceField
	for i := range rows {
		f := rows[i].InstanceField
		f.EnumValues, f.Validation, f.Conditional = nil, nil, nil
		if raw := rows[i].EnumValues; len(raw) > 0 && string(raw) != "null" {
			_ = json.Unmarshal(raw, &f.EnumValues)
		}
		if raw := rows[i].Validation; len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" {
			var v fieldmeta.ValidationRules
			if err := json.Unmarshal(raw, &v); err == nil {
				f.Validation = &v
			}
		}
		if raw := rows[i].Conditional; len(raw) > 0 && string(raw) != "null" {
			var c fieldmeta.ConditionalRules
			if err := json.Unmarshal(raw, &c); err == nil {
				f.Conditional = &c
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// ResolveFieldsWithTemplates implements ci.TemplateFieldResolver: the
// effective field definitions of a CI of ciTypeID as ResolveFields
// composes them, with templates instead of the stored ci_template rows as
// the template layer — the list a creation (POST /cis) or a replacement
// (PUT /cis/{id}/templates) writes. The list is checked like the write
// checks it (ResolveTemplateRefsTx, 422 on templates[i]).
func (r *PGRepository) ResolveFieldsWithTemplates(ctx context.Context, orgID, ciTypeID, ciID string, templates []CITemplate) ([]fieldmeta.FieldDefinition, error) {
	return resolveFields(ctx, r, orgID, ciTypeID, ciID, func(ctx context.Context) ([][]templateItem, error) {
		var out [][]templateItem
		err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			resolved, err := ResolveTemplateRefsTx(ctx, tx, ciTypeID, templates)
			if err != nil || len(resolved) == 0 {
				return err
			}
			ids := make([]string, len(resolved))
			versions := make([]int32, len(resolved))
			for i := range resolved {
				ids[i], versions[i] = resolved[i].TemplateID, int32(resolved[i].Version) //nolint:gosec // a stored version
			}
			var raw []byte
			if err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(`+templateItemsJSON("r.template_id", "r.version")+` ORDER BY r.ord), '[]'::jsonb)
				FROM unnest($1::uuid[], $2::int[]) WITH ORDINALITY AS r(template_id, version, ord)`, ids, versions).Scan(&raw); err != nil {
				return fmt.Errorf("read template items: %w", err)
			}
			out, err = templateLayersFromJSON(raw)
			return err
		})
		return out, err
	})
}
