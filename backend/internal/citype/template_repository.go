package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// templateFields maps database constraints onto request fields.
var templateFields = map[string]string{
	"template_org_key":           "key",
	"template_key_check":         "key",
	"template_display_name_i18n": "display_name",
	"template_ci_type_id_fkey":   "ci_type_id",
}

const templateColumns = `id::text, COALESCE(organization_id::text, ''), key, ci_type_id::text, display_name,
	COALESCE(description, ''), is_system, is_active, current_version, COALESCE(cloned_from_id::text, ''),
	created_at, updated_at`

const templateVersionColumns = `id::text, COALESCE(organization_id::text, ''), template_id::text, version, status,
	published_at, COALESCE(published_by::text, ''), created_at, updated_at`

func scanTemplate(row pgx.Row) (*Template, error) {
	var (
		t           Template
		displayName []byte
	)
	if err := row.Scan(&t.ID, &t.OrganizationID, &t.Key, &t.CITypeID, &displayName, &t.Description, &t.IsSystem,
		&t.IsActive, &t.CurrentVersion, &t.ClonedFromID, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(displayName, &t.DisplayName); err != nil {
		return nil, fmt.Errorf("decode display_name: %w", err)
	}
	return &t, nil
}

func scanTemplateVersion(row pgx.Row) (*TemplateVersion, error) {
	var v TemplateVersion
	if err := row.Scan(&v.ID, &v.OrganizationID, &v.TemplateID, &v.Version, &v.Status, &v.PublishedAt,
		&v.PublishedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// getTemplateTx reads a template the transaction sees (the organization's
// and the system templates); lock locks an own template for a change.
func getTemplateTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (*Template, error) {
	if !api.IsUUID(id) {
		return nil, ErrTemplateNotFound
	}
	query := `SELECT ` + templateColumns + ` FROM template WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	t, err := scanTemplate(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read template: %w", err)
	}
	return t, nil
}

// lockOwnTemplateTx locks a template of the organization for a change; a
// system template is visible but read-only (403), so it is recognized
// before locking (the UPDATE policy hides it from FOR UPDATE).
func lockOwnTemplateTx(ctx context.Context, tx pgx.Tx, id string) (*Template, error) {
	t, err := getTemplateTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if t.OrganizationID == "" {
		return nil, ErrTemplateReadOnly
	}
	return getTemplateTx(ctx, tx, id, true)
}

// templateVersionTx reads one version of a template with its items; lock
// locks it for a change.
func templateVersionTx(ctx context.Context, tx pgx.Tx, templateID string, version int, lock bool) (*TemplateVersion, error) {
	query := `SELECT ` + templateVersionColumns + ` FROM template_version WHERE template_id = $1 AND version = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	v, err := scanTemplateVersion(tx.QueryRow(ctx, query, templateID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTemplateVersionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read template version: %w", err)
	}
	if v.Items, err = templateItemsTx(ctx, tx, v.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// templateItemsTx reads the items of a version in position order, each with
// the key of its definition.
func templateItemsTx(ctx context.Context, tx pgx.Tx, versionID string) ([]TemplateVersionItem, error) {
	rows, err := tx.Query(ctx, `SELECT i.attribute_definition_id::text, a.key, i.position, i.required_override,
			i.default_override, i.validation_override
		FROM template_version_item i JOIN attribute_definition a ON a.id = i.attribute_definition_id
		WHERE i.template_version_id = $1 ORDER BY i.position`, versionID)
	if err != nil {
		return nil, fmt.Errorf("read template items: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TemplateVersionItem, error) {
		var (
			it                  TemplateVersionItem
			defaults, validates []byte
		)
		if serr := row.Scan(&it.AttributeDefinitionID, &it.Key, &it.Position, &it.RequiredOverride,
			&defaults, &validates); serr != nil {
			return it, serr
		}
		if len(defaults) > 0 {
			it.DefaultOverride = json.RawMessage(defaults)
		}
		if len(validates) > 0 {
			it.ValidationOverride = json.RawMessage(validates)
		}
		return it, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read template items: %w", err)
	}
	if items == nil {
		items = []TemplateVersionItem{}
	}
	return items, nil
}

// sourceItemsTx returns the items a new version or a clone copies: those
// of the current (last published) version, without one those of the
// highest version.
func sourceItemsTx(ctx context.Context, tx pgx.Tx, t *Template) ([]TemplateVersionItem, error) {
	var versionID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM template_version WHERE template_id = $1
		ORDER BY (version = $2) DESC, version DESC LIMIT 1`, t.ID, t.CurrentVersion).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read current template version: %w", err)
	}
	return templateItemsTx(ctx, tx, versionID)
}

// insertTemplateVersionTx inserts a draft version with its items.
func insertTemplateVersionTx(ctx context.Context, tx pgx.Tx, templateID string, version int, items []TemplateVersionItem) (*TemplateVersion, error) {
	v, err := scanTemplateVersion(tx.QueryRow(ctx, `INSERT INTO template_version (template_id, version)
		VALUES ($1, $2) RETURNING `+templateVersionColumns, templateID, version))
	if err != nil {
		return nil, fmt.Errorf("create template version: %w", err)
	}
	if ierr := insertTemplateItemsTx(ctx, tx, v.ID, items); ierr != nil {
		return nil, ierr
	}
	if v.Items, err = templateItemsTx(ctx, tx, v.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// insertTemplateItemsTx writes the items of a version.
func insertTemplateItemsTx(ctx context.Context, tx pgx.Tx, versionID string, items []TemplateVersionItem) error {
	for i := range items {
		it := &items[i]
		var defaults, validates any
		if len(it.DefaultOverride) > 0 {
			defaults = it.DefaultOverride
		}
		if len(it.ValidationOverride) > 0 {
			validates = it.ValidationOverride
		}
		if _, err := tx.Exec(ctx, `INSERT INTO template_version_item (template_version_id, attribute_definition_id,
				position, required_override, default_override, validation_override)
			VALUES ($1, $2, $3, $4, $5, $6)`, versionID, it.AttributeDefinitionID, it.Position, it.RequiredOverride,
			defaults, validates); err != nil {
			return api.FieldDBError(err, map[string]string{
				"template_version_item_attribute_definition_id_fkey": fmt.Sprintf("items[%d].attribute_definition_id", i),
			})
		}
	}
	return nil
}

// checkTemplateItemsTx checks the items of a version of a template of
// typeID (MET-16, E-37): each references a different, visible attribute
// definition of the type chain (WP-248) or a global one; set attributes
// (scope set, WP-250) are refused by their scope, since they have no CI
// type either. Positions are distinct and not negative. The overrides
// only narrow (checkTemplateItem). Every refusal is a 422 on the field of
// the item. It sets the key of each item and returns the definitions by
// id.
func checkTemplateItemsTx(ctx context.Context, tx pgx.Tx, typeID string, items []TemplateVersionItem) (map[string]*AttributeDefinition, error) {
	ids := make([]string, 0, len(items))
	seenDef, seenPos := map[string]bool{}, map[int]bool{}
	for i := range items {
		it := &items[i]
		switch {
		case !api.IsUUID(it.AttributeDefinitionID):
			return nil, itemError(i, "attribute_definition_id", "attribute definition not found")
		case seenDef[it.AttributeDefinitionID]:
			return nil, itemError(i, "attribute_definition_id", "the attribute definition is listed twice")
		case it.Position < 0:
			return nil, itemError(i, "position", "position must not be negative")
		case seenPos[it.Position]:
			return nil, itemError(i, "position", "the position is taken by another item")
		}
		seenDef[it.AttributeDefinitionID], seenPos[it.Position] = true, true
		ids = append(ids, it.AttributeDefinitionID)
	}
	defs := map[string]*AttributeDefinition{}
	if len(items) == 0 {
		return defs, nil
	}
	found, err := queryAttributes(ctx, tx, `SELECT `+attributeColumns+` FROM attribute_definition
		WHERE id = ANY($1::uuid[]) ORDER BY key`, []any{ids})
	if err != nil {
		return nil, err
	}
	for i := range found {
		defs[found[i].ID] = &found[i]
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM ci_type_ancestors($1::uuid)`, typeID)
	if err != nil {
		return nil, fmt.Errorf("read type chain: %w", err)
	}
	chain, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read type chain: %w", err)
	}
	inChain := map[string]bool{}
	for _, id := range chain {
		inChain[id] = true
	}
	for i := range items {
		def, ok := defs[items[i].AttributeDefinitionID]
		switch {
		case !ok:
			return nil, itemError(i, "attribute_definition_id", "attribute definition not found")
		case def.Scope == ScopeSet:
			return nil, itemError(i, "attribute_definition_id", "set attributes cannot be template items")
		case def.Scope != "global" && !inChain[def.CITypeID]:
			return nil, itemError(i, "attribute_definition_id",
				"the attribute definition belongs to a ci type outside the template's type chain")
		}
		if cerr := checkTemplateItem(i, def, &items[i]); cerr != nil {
			return nil, cerr
		}
		items[i].Key = def.Key
	}
	return defs, nil
}

// checkTemplateCompositionTx composes the template version versionID on
// its type (MET-16) with CheckComposition (WP-252, a type with
// TemplateVersionIDs): system base → organization's global attributes that
// apply to the type → type chain → sets of the chain → the template
// version (each item's definition where no lower layer has the key, then
// its overrides). The template layer is not materialized; templates are
// composed per CI (WP-710). A conflict of an item's key is a 422 on that
// key with the code of the conflict.
func checkTemplateCompositionTx(ctx context.Context, tx pgx.Tx, typeID, versionID string, items []TemplateVersionItem) error {
	conflicts, err := CheckComposition(ctx, tx, CompositionScope{TypeID: typeID, TemplateVersionIDs: []string{versionID}})
	if err != nil {
		return err
	}
	inItems := func(key string) bool {
		return slices.ContainsFunc(items, func(it TemplateVersionItem) bool { return it.Key == key })
	}
	if c := firstConflict(conflicts, inItems); c != nil {
		return &api.FieldError{Field: c.Key, Message: c.Error(), Status: http.StatusUnprocessableEntity, Code: c.Code}
	}
	return nil
}

// ListTemplatePage implements TemplateStore: the organization's and the
// system templates newest first (API-02), optionally of one CI type.
func (r *PGRepository) ListTemplatePage(ctx context.Context, orgID string, filter TemplateFilter, page api.PaginationParams) ([]Template, int, error) {
	out := []Template{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		const where = `($1 = '' OR ci_type_id::text = $1)`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM template WHERE `+where, filter.CITypeID).Scan(&total); err != nil {
			return fmt.Errorf("count templates: %w", err)
		}
		keyset, tail, args, err := api.NewestFirstSQL(page, 2)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+templateColumns+` FROM template WHERE `+where+` AND `+keyset+` `+tail,
			append([]any{filter.CITypeID}, args...)...)
		if err != nil {
			return fmt.Errorf("list templates: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Template, error) {
			t, serr := scanTemplate(row)
			if serr != nil {
				return Template{}, serr
			}
			return *t, nil
		})
		if err != nil {
			return fmt.Errorf("list templates: %w", err)
		}
		return nil
	})
	return out, total, err
}

// GetTemplate implements TemplateStore.
func (r *PGRepository) GetTemplate(ctx context.Context, orgID, id string) (*Template, error) {
	var out *Template
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = getTemplateTx(ctx, tx, id, false)
		return err
	})
	return out, err
}

// insertTemplateTx inserts a template of the organization with version 1
// as a draft holding items, and audits both (action names the template
// entry: template.created or template.cloned).
func insertTemplateTx(ctx context.Context, tx pgx.Tx, orgID, action string, t *Template, items []TemplateVersionItem) error {
	created, err := scanTemplate(tx.QueryRow(ctx, `INSERT INTO template
			(organization_id, key, ci_type_id, display_name, description, is_system, is_active, cloned_from_id)
		VALUES ($1, $2, $3, $4, $5, false, $6, $7) RETURNING `+templateColumns,
		orgID, t.Key, t.CITypeID, t.DisplayName, nilIfEmpty(t.Description), t.IsActive, nilIfEmpty(t.ClonedFromID)))
	if err != nil {
		return api.FieldDBError(err, templateFields)
	}
	*t = *created
	if aerr := recordMetamodelAudit(ctx, tx, orgID, action, "template", t.ID, nil, t); aerr != nil {
		return aerr
	}
	v, err := insertTemplateVersionTx(ctx, tx, t.ID, 1, items)
	if err != nil {
		return err
	}
	return recordMetamodelAudit(ctx, tx, orgID, "template_version.created", "template_version", v.ID, nil, v)
}

// CreateTemplate implements TemplateStore: a template of the organization
// for a CI type it sees (its own or a system type; templates are composed
// per CI, so a system type is allowed, WP-710). An unknown type is a 422
// on ci_type_id, a taken key 409.
func (r *PGRepository) CreateTemplate(ctx context.Context, orgID string, t *Template) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, found, err := ciTypeOrganizationTx(ctx, tx, t.CITypeID); err != nil {
			return err
		} else if !found {
			return &api.FieldError{Field: "ci_type_id", Message: "ci type not found", Status: http.StatusUnprocessableEntity}
		}
		t.ClonedFromID = ""
		return insertTemplateTx(ctx, tx, orgID, "template.created", t, nil)
	})
}

// UpdateTemplate implements TemplateStore: display name, description and
// is_active; a system template is read-only (403).
func (r *PGRepository) UpdateTemplate(ctx context.Context, orgID, id string, patch *TemplatePatch) (*Template, error) {
	var out *Template
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := lockOwnTemplateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before := *t
		if perr := patch.apply(t); perr != nil {
			return perr
		}
		updated, err := scanTemplate(tx.QueryRow(ctx, `UPDATE template
			SET display_name = $2, description = $3, is_active = $4 WHERE id = $1 RETURNING `+templateColumns,
			id, t.DisplayName, nilIfEmpty(t.Description), t.IsActive))
		if err != nil {
			return api.FieldDBError(err, templateFields)
		}
		out = updated
		return recordMetamodelAudit(ctx, tx, orgID, "template.updated", "template", id, &before, updated)
	})
	return out, err
}

// DeleteTemplate implements TemplateStore: the template goes with its
// versions and items; a system template is read-only (403).
func (r *PGRepository) DeleteTemplate(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := lockOwnTemplateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM template WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete template: %w", err)
		}
		return recordMetamodelAudit(ctx, tx, orgID, "template.deleted", "template", id, t, nil)
	})
}

// CloneTemplate implements TemplateStore: the source may be a system
// template. The clone is a template of the organization on the source's
// type with the new key; its version 1 is a draft with the items of the
// source's current version, cloned_from_id names the source.
func (r *PGRepository) CloneTemplate(ctx context.Context, orgID, id string, req *TemplateClone) (*Template, error) {
	key, kerr := validateTemplateKey(req.Key)
	if kerr != nil {
		return nil, kerr
	}
	if req.DisplayName != nil {
		if verr := req.DisplayName.Validate(); verr != nil {
			return nil, &api.FieldError{Field: "display_name", Message: verr.Error()}
		}
	}
	var out *Template
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		src, err := getTemplateTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		items, err := sourceItemsTx(ctx, tx, src)
		if err != nil {
			return err
		}
		clone := &Template{Key: key, CITypeID: src.CITypeID, DisplayName: src.DisplayName, Description: src.Description,
			IsActive: true, ClonedFromID: src.ID}
		if req.DisplayName != nil {
			clone.DisplayName = req.DisplayName.WithFallback(key)
		}
		setString(&clone.Description, req.Description)
		if ierr := insertTemplateTx(ctx, tx, orgID, "template.cloned", clone, items); ierr != nil {
			return ierr
		}
		out = clone
		return nil
	})
	return out, err
}

// ListTemplateVersionPage implements TemplateStore: the versions of a
// visible template newest first (API-02), each with its items.
func (r *PGRepository) ListTemplateVersionPage(ctx context.Context, orgID, id string, page api.PaginationParams) ([]TemplateVersion, int, error) {
	out := []TemplateVersion{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTemplateTx(ctx, tx, id, false); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM template_version WHERE template_id = $1`, id).Scan(&total); err != nil {
			return fmt.Errorf("count template versions: %w", err)
		}
		keyset, tail, args, err := api.NewestFirstSQL(page, 2)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+templateVersionColumns+` FROM template_version
			WHERE template_id = $1 AND `+keyset+` `+tail, append([]any{id}, args...)...)
		if err != nil {
			return fmt.Errorf("list template versions: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (TemplateVersion, error) {
			v, serr := scanTemplateVersion(row)
			if serr != nil {
				return TemplateVersion{}, serr
			}
			return *v, nil
		})
		if err != nil {
			return fmt.Errorf("list template versions: %w", err)
		}
		for i := range out {
			if out[i].Items, err = templateItemsTx(ctx, tx, out[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, total, err
}

// GetTemplateVersion implements TemplateStore: one version with its items.
func (r *PGRepository) GetTemplateVersion(ctx context.Context, orgID, id string, version int) (*TemplateVersion, error) {
	var out *TemplateVersion
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTemplateTx(ctx, tx, id, false); err != nil {
			return err
		}
		var err error
		out, err = templateVersionTx(ctx, tx, id, version, false)
		return err
	})
	return out, err
}

// CreateTemplateVersion implements TemplateStore: the next version number,
// a draft copy of the current (last published) version, without one of the
// highest version. A system template is read-only (403).
func (r *PGRepository) CreateTemplateVersion(ctx context.Context, orgID, id string) (*TemplateVersion, error) {
	var out *TemplateVersion
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := lockOwnTemplateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		items, err := sourceItemsTx(ctx, tx, t)
		if err != nil {
			return err
		}
		var next int
		if err = tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) + 1 FROM template_version WHERE template_id = $1`,
			id).Scan(&next); err != nil {
			return fmt.Errorf("read template versions: %w", err)
		}
		if out, err = insertTemplateVersionTx(ctx, tx, id, next, items); err != nil {
			return err
		}
		return recordMetamodelAudit(ctx, tx, orgID, "template_version.created", "template_version", out.ID, nil, out)
	})
	return out, err
}

// SetTemplateVersionItems implements TemplateStore: the list replaces the
// items of a draft as a whole after checkTemplateItemsTx. A published or
// retired version is 409, a system template 403.
func (r *PGRepository) SetTemplateVersionItems(ctx context.Context, orgID, id string, version int, items []TemplateVersionItem) (*TemplateVersion, error) {
	var out *TemplateVersion
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := lockOwnTemplateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before, err := templateVersionTx(ctx, tx, id, version, true)
		if err != nil {
			return err
		}
		if before.Status != TemplateVersionDraft {
			return ErrTemplateVersionImmutable
		}
		if _, err = checkTemplateItemsTx(ctx, tx, t.CITypeID, items); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM template_version_item WHERE template_version_id = $1`, before.ID); err != nil {
			return fmt.Errorf("replace template items: %w", err)
		}
		if ierr := insertTemplateItemsTx(ctx, tx, before.ID, items); ierr != nil {
			return ierr
		}
		if _, err = tx.Exec(ctx, `UPDATE template_version SET updated_at = now() WHERE id = $1`, before.ID); err != nil {
			return fmt.Errorf("touch template version: %w", err)
		}
		if out, err = templateVersionTx(ctx, tx, id, version, false); err != nil {
			return err
		}
		return recordMetamodelAudit(ctx, tx, orgID, "template_version.updated", "template_version", out.ID, before, out)
	})
	return out, err
}

// PublishTemplateVersion implements TemplateStore (MET-53, MET-04): a draft
// newer than the current version is checked item by item and composed on
// the template's type (checkTemplateCompositionTx: a conflict is a 422 on
// the key with its code); then, in the same transaction, it becomes
// published with published_at and published_by, and the template's
// current_version names it. Earlier published versions stay referable. A
// system template is 403, any other version 409. Publishing through drafts
// follows with WP-260/WP-261.
func (r *PGRepository) PublishTemplateVersion(ctx context.Context, orgID, id string, version int) (*TemplateVersion, error) {
	var out *TemplateVersion
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := lockOwnTemplateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before, err := templateVersionTx(ctx, tx, id, version, true)
		if err != nil {
			return err
		}
		if before.Status != TemplateVersionDraft || (t.CurrentVersion != nil && version <= *t.CurrentVersion) {
			return ErrTemplateVersionNotDraft
		}
		items := append([]TemplateVersionItem(nil), before.Items...)
		if _, err = checkTemplateItemsTx(ctx, tx, t.CITypeID, items); err != nil {
			return err
		}
		if cerr := checkTemplateCompositionTx(ctx, tx, t.CITypeID, before.ID, items); cerr != nil {
			return cerr
		}
		var publisher any
		if user := strings.TrimSpace(tenant.FromContext(ctx).UserID); api.IsUUID(user) {
			publisher = user
		}
		if _, err = tx.Exec(ctx, `UPDATE template_version SET status = 'published', published_at = $2, published_by = $3
			WHERE id = $1`, before.ID, time.Now().UTC(), publisher); err != nil {
			return fmt.Errorf("publish template version: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE template SET current_version = $2 WHERE id = $1`, id, version); err != nil {
			return fmt.Errorf("set current template version: %w", err)
		}
		if out, err = templateVersionTx(ctx, tx, id, version, false); err != nil {
			return err
		}
		return recordMetamodelAudit(ctx, tx, orgID, "template_version.published", "template_version", out.ID, before, out)
	})
	return out, err
}
