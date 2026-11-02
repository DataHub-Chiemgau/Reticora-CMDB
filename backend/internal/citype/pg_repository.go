package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const typeSelectColumns = `
	id::text,
	COALESCE(organization_id::text, ''),
	COALESCE(key, ''),
	name,
	display_name,
	COALESCE(icon, ''),
	COALESCE(description, ''),
	COALESCE(category, ''),
	is_builtin,
	is_system,
	is_active,
	is_logical,
	is_observable,
	schema_version,
	version,
	COALESCE(cloned_from_id::text, ''),
	COALESCE(parent_type_id::text, ''),
	COALESCE(template_key, ''),
	COALESCE(lifecycle_definition_id::text, ''),
	capabilities,
	allowed_relationship_types,
	ui_schema,
	compliance_rules,
	discovery_mappings,
	attribute_schema,
	COALESCE(required_fields, '{}'),
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
	// indexes starts the index job of a definition whose searchable or
	// unique flag changed (DB-04); optional.
	indexes IndexScheduler
}

// WithIndexScheduler starts the attribute index jobs through s.
func (r *PGRepository) WithIndexScheduler(s IndexScheduler) *PGRepository {
	r.indexes = s
	return r
}

// NewPGRepository creates a new PostgreSQL-backed CI type repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// List returns CI types visible to the tenant: its own types plus the global
// system types (organization_id NULL, exposed by the RLS policy).
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Type, int, error) {
	var out []Type
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"true"}
		args := []any{}
		pos := 1
		if !filter.IncludeInactive {
			where = append(where, "is_active")
		}
		if filter.Category != "" {
			where = append(where, fmt.Sprintf("category = $%d", pos))
			args = append(args, filter.Category)
			pos++
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("(name ILIKE $%d OR display_name ->> 'de' ILIKE $%d OR display_name ->> 'en' ILIKE $%d)", pos, pos, pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ci_type WHERE "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count ci types: %w", err)
		}
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_type WHERE %s ORDER BY name ASC, id ASC LIMIT $%d OFFSET $%d",
			typeSelectColumns, clause, pos, pos+1), args...)
		if err != nil {
			return fmt.Errorf("list ci types: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanType(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByID returns one type including its field definitions.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Type, error) {
	var out *Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := scanType(tx.QueryRow(ctx,
			fmt.Sprintf("SELECT %s FROM ci_type WHERE id = $1", typeSelectColumns), id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get ci type: %w", err)
		}
		flds, err := listFieldsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		t.Fields = flds
		out = t
		return nil
	})
	return out, err
}

// Create inserts a new type with its field definitions. A parent is
// checked by trg_ci_type_parent_guard (MET-06); its errors and a chain that
// cannot be composed are reported on parent_type_id.
//
// A new subtype widens the subtree of its ancestors: the indexes of their
// indexed, inheritable definitions are rebuilt with it (WP-708).
func (r *PGRepository) Create(ctx context.Context, typ *Type) error {
	if typ.ParentTypeID != "" && !api.IsUUID(typ.ParentTypeID) {
		return errParentUnknown()
	}
	var reindex []string
	err := database.WithRequestTenant(ctx, r.pool, typ.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if typ.Key == "" {
			typ.Key = slugify(typ.Name)
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO ci_type (
				organization_id, key, name, display_name, icon, description, category,
				is_builtin, is_system, is_active, is_logical, version, template_key,
				lifecycle_definition_id, capabilities, allowed_relationship_types,
				ui_schema, compliance_rules, discovery_mappings, is_observable, parent_type_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,false,false,true,$8,1,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			RETURNING id::text, is_active, version, schema_version, created_at, updated_at`,
			nilIfEmpty(typ.OrganizationID), typ.Key, typ.Name, typ.DisplayName.WithFallback(typ.Name),
			nilIfEmpty(typ.Icon), nilIfEmpty(typ.Description), nilIfEmpty(typ.Category),
			typ.IsLogical, nilIfEmpty(typ.TemplateKey), nilIfEmpty(typ.LifecycleDefinitionID),
			jsonOrDefault(typ.Capabilities, emptyObject), jsonOrDefault(typ.AllowedRelationshipTypes, emptyArray),
			jsonOrDefault(typ.UISchema, emptyObject), jsonOrDefault(typ.ComplianceRules, emptyArray),
			jsonOrDefault(typ.DiscoveryMappings, emptyArray), typ.IsObservable, nilIfEmpty(typ.ParentTypeID),
		)
		typ.DisplayName = typ.DisplayName.WithFallback(typ.Name)
		if err := row.Scan(&typ.ID, &typ.IsActive, &typ.Version, &typ.SchemaVersion, &typ.CreatedAt, &typ.UpdatedAt); err != nil {
			return fmt.Errorf("create ci type: %w", typeDBError(err))
		}
		for _, f := range typ.Fields {
			f.Scope = "type"
			if _, err := upsertFieldTx(ctx, tx, typ.OrganizationID, typ.ID, &UpsertFieldRequest{
				Scope: "type", Name: f.Name, Label: f.Label, Description: f.Description,
				DataType: f.DataType, Required: f.Required, DefaultValue: f.DefaultValue,
				EnumValues: f.EnumValues, UIGroup: f.UIGroup, SortOrder: f.SortOrder,
				Validation: f.Validation, Conditional: f.Conditional, ReferenceTarget: f.ReferenceTarget,
			}); err != nil {
				return err
			}
		}
		if typ.ParentTypeID != "" {
			if err := checkSubtreeCompositionTx(ctx, tx, typ.ID); err != nil {
				return err
			}
			var ierr error
			if reindex, ierr = inheritedIndexedTx(ctx, tx, typ.OrganizationID, typ.ID); ierr != nil {
				return ierr
			}
		}
		return recordMetamodelAudit(ctx, tx, typ.OrganizationID, "ci_type.created", "ci_type", typ.ID, nil, typ)
	})
	if err == nil {
		r.scheduleIndexes(ctx, typ.OrganizationID, reindex)
	}
	return err
}

// inheritedIndexedTx returns the indexed (searchable or unique),
// inheritable definitions of the organization on the ancestors of the type
// id: their indexes cover the subtree of their type (IndexStatement), which
// the type belongs to (WP-708).
func inheritedIndexedTx(ctx context.Context, tx pgx.Tx, orgID, id string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT d.id::text FROM ci_type_ancestors($1::uuid) a
		JOIN attribute_definition d ON d.ci_type_id = a.id
		WHERE a.id <> $1::uuid AND d.inheritable AND (d.searchable OR d.is_unique) AND d.organization_id = $2::uuid
		ORDER BY 1`, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("read inherited attribute indexes: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read inherited attribute indexes: %w", err)
	}
	return ids, nil
}

// scheduleIndexes starts the index jobs of the definitions ids after a
// change of the type tree (WP-708). The change is stored already; a job
// that cannot be queued is logged, as in scheduleIndex.
func (r *PGRepository) scheduleIndexes(ctx context.Context, orgID string, ids []string) {
	if r.indexes == nil {
		return
	}
	for _, id := range ids {
		if err := r.indexes.ScheduleIndex(context.WithoutCancel(ctx), orgID, id); err != nil {
			slog.Error("schedule attribute index job", "definition", id, "error", err)
		}
	}
}

// typeParentErrors maps the constraints of trg_ci_type_parent_guard and
// of the parent foreign key onto the errors of parent_type_id (MET-06).
var typeParentErrors = map[string]func() error{
	"ci_type_parent_unknown":      errParentUnknown,
	"ci_type_parent_type_id_fkey": errParentUnknown,
	"ci_type_parent_system":       errParentSystem,
	"ci_type_parent_cycle":        errParentCycle,
	"ci_type_parent_depth":        errParentDepth,
}

// typeDBError maps a failed type write onto parent_type_id: the parent
// rules and a type chain the regenerated schema cannot compose
// (attribute_definition_composition, with the text attributeDBError
// reports on key). Other errors pass unchanged.
func typeDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if mapped, ok := typeParentErrors[pgErr.ConstraintName]; ok {
		return mapped()
	}
	var fe *api.FieldError
	if pgErr.ConstraintName == compositionConstraint && errors.As(attributeDBError(err), &fe) {
		fe.Field = "parent_type_id"
		return fe
	}
	return err
}

// Update applies the mutable metadata of a type in place. A parent change
// (parent_type_id, "" makes the type a root) is checked by
// trg_ci_type_parent_guard and regenerates the moved subtree; the subtree
// must stay composable with the organization's global attributes as well
// (MET-06, MET-16). Parent errors are reported on parent_type_id, an
// unknown or system type is "not found". CIs of the moved subtree and of
// the new ancestors' subtrees must not share a value of an inherited unique
// attribute (409 on parent_type_id); the indexes of the indexed,
// inheritable definitions of the former and the new ancestors are rebuilt
// over their changed subtrees (WP-708).
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateTypeRequest) (*Type, error) {
	if !api.IsUUID(id) {
		return nil, fmt.Errorf("not found")
	}
	var out *Type
	var reindex []string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{id}
		pos := 2
		add := func(col string, val any) {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, val)
			pos++
		}
		if req.Name != nil {
			add("name", *req.Name)
		}
		if req.DisplayName != nil {
			// Missing languages fall back to the given one; an empty map
			// resets the label to the name (trigger ci_type_defaults).
			add("display_name", req.DisplayName.WithFallback(""))
		}
		if req.IsObservable != nil {
			add("is_observable", *req.IsObservable)
		}
		if req.Icon != nil {
			add("icon", nilIfEmpty(*req.Icon))
		}
		if req.Description != nil {
			add("description", nilIfEmpty(*req.Description))
		}
		if req.Category != nil {
			add("category", nilIfEmpty(*req.Category))
		}
		if req.IsLogical != nil {
			add("is_logical", *req.IsLogical)
		}
		if req.LifecycleDefinitionID != nil {
			add("lifecycle_definition_id", nilIfEmpty(*req.LifecycleDefinitionID))
		}
		if req.Capabilities != nil {
			add("capabilities", req.Capabilities)
		}
		if req.AllowedRelationshipTypes != nil {
			add("allowed_relationship_types", req.AllowedRelationshipTypes)
		}
		if req.UISchema != nil {
			add("ui_schema", req.UISchema)
		}
		if req.ComplianceRules != nil {
			add("compliance_rules", req.ComplianceRules)
		}
		if req.DiscoveryMappings != nil {
			add("discovery_mappings", req.DiscoveryMappings)
		}
		before, err := scanType(tx.QueryRow(ctx,
			fmt.Sprintf("SELECT %s FROM ci_type WHERE id = $1 AND organization_id IS NOT NULL", typeSelectColumns), id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return err
		}
		parentChanged := req.ParentTypeID != nil && *req.ParentTypeID != before.ParentTypeID
		if parentChanged {
			if *req.ParentTypeID != "" && !api.IsUUID(*req.ParentTypeID) {
				return errParentUnknown()
			}
			add("parent_type_id", nilIfEmpty(*req.ParentTypeID))
			if reindex, err = inheritedIndexedTx(ctx, tx, orgID, id); err != nil {
				return err
			}
		}
		if len(sets) == 0 {
			t, err := scanType(tx.QueryRow(ctx,
				fmt.Sprintf("SELECT %s FROM ci_type WHERE id = $1 AND organization_id IS NOT NULL", typeSelectColumns), id))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return err
			}
			out = t
			return nil
		}
		t, err := scanType(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE ci_type SET %s WHERE id = $1 AND organization_id IS NOT NULL RETURNING %s",
			strings.Join(sets, ", "), typeSelectColumns), args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update ci type: %w", typeDBError(err))
		}
		if parentChanged {
			// The moved subtree with the organization's layer; the database
			// composed system base and chain while regenerating it.
			if cerr := checkSubtreeCompositionTx(ctx, tx, id); cerr != nil {
				return cerr
			}
			if uerr := checkInheritedUniqueTx(ctx, tx, id); uerr != nil {
				return uerr
			}
			gained, ierr := inheritedIndexedTx(ctx, tx, orgID, id)
			if ierr != nil {
				return ierr
			}
			for _, d := range gained {
				if !slices.Contains(reindex, d) {
					reindex = append(reindex, d)
				}
			}
			// RETURNING precedes the regeneration of trg_ci_type_parent_regenerate.
			if t, err = scanType(tx.QueryRow(ctx,
				fmt.Sprintf("SELECT %s FROM ci_type WHERE id = $1", typeSelectColumns), id)); err != nil {
				return fmt.Errorf("read updated ci type: %w", err)
			}
		}
		flds, err := listFieldsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		t.Fields = flds
		out = t
		return recordMetamodelAudit(ctx, tx, orgID, "ci_type.updated", "ci_type", id, before, t)
	})
	if err == nil {
		r.scheduleIndexes(ctx, orgID, reindex)
	}
	return out, err
}

// Clone copies a type row plus its field definitions under a new key/name.
func (r *PGRepository) Clone(ctx context.Context, orgID, id string, req CloneRequest) (*Type, error) {
	var out *Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		src, err := scanType(tx.QueryRow(ctx,
			fmt.Sprintf("SELECT %s FROM ci_type WHERE id = $1", typeSelectColumns), id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return err
		}
		name := req.Name
		if name == "" {
			name = src.Name + "_copy"
		}
		key := req.Key
		if key == "" {
			key = slugify(name)
		}
		var cloneID string
		err = tx.QueryRow(ctx, `
			INSERT INTO ci_type (
				organization_id, key, name, display_name, icon, description, category,
				is_builtin, is_system, is_active, is_logical, version, cloned_from_id,
				template_key, lifecycle_definition_id, capabilities,
				allowed_relationship_types, ui_schema, compliance_rules, discovery_mappings,
				is_observable
			) VALUES ($1,$2,$3,$4,$5,$6,$7,false,false,true,$8,$9,$10,NULL,$11,$12,$13,$14,$15,$16,$17)
			RETURNING id::text`,
			orgID, key, name, LocalizedText{}.WithFallback(name), nilIfEmpty(src.Icon), nilIfEmpty(src.Description),
			nilIfEmpty(src.Category), src.IsLogical, src.Version+1, src.ID,
			nilIfEmpty(src.LifecycleDefinitionID), src.Capabilities,
			src.AllowedRelationshipTypes, src.UISchema, src.ComplianceRules,
			src.DiscoveryMappings, src.IsObservable,
		).Scan(&cloneID)
		if err != nil {
			return fmt.Errorf("clone ci type: %w", err)
		}
		// Copy the field definitions of the source type.
		if _, err := tx.Exec(ctx, `
			INSERT INTO attribute_definition (
				ci_type_id, scope, key, display_name, description, data_type, required,
				default_value, enum_values, multi_value, searchable, is_unique, unique_scope,
				inheritable, validation, conditional, reference_target, group_name, sort_order, unit
			)
			SELECT $1, 'type', key, display_name, description, data_type, required,
				default_value, enum_values, multi_value, searchable, is_unique, unique_scope,
				inheritable, validation, conditional, reference_target, group_name, sort_order, unit
			FROM attribute_definition
			WHERE ci_type_id = $2`, cloneID, id); err != nil {
			return fmt.Errorf("clone ci type fields: %w", err)
		}
		out = &Type{ID: cloneID, OrganizationID: orgID, Key: key, Name: name}
		return recordMetamodelAudit(ctx, tx, orgID, "ci_type.cloned", "ci_type", cloneID, nil,
			map[string]any{"cloned_from_id": src.ID, "key": key, "name": name, "version": src.Version + 1})
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, orgID, out.ID)
}

// SetActive toggles the active flag of a tenant-owned type.
func (r *PGRepository) SetActive(ctx context.Context, orgID, id string, active bool) (*Type, error) {
	var out *Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		t, err := scanType(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE ci_type SET is_active = $2 WHERE id = $1 AND organization_id IS NOT NULL RETURNING %s",
			typeSelectColumns), id, active))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("set ci type active: %w", err)
		}
		out = t
		action := "ci_type.deactivated"
		if active {
			action = "ci_type.activated"
		}
		return recordMetamodelAudit(ctx, tx, orgID, action, "ci_type", id, nil, map[string]any{"is_active": active})
	})
	return out, err
}

// Versions returns the lineage of a type: the root plus every clone.
func (r *PGRepository) Versions(ctx context.Context, orgID, id string) ([]Type, error) {
	var out []Type
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		root, err := scanType(tx.QueryRow(ctx,
			fmt.Sprintf("SELECT %s FROM ci_type WHERE id = $1", typeSelectColumns), id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return err
		}
		rootID := root.ClonedFromID
		if rootID == "" {
			rootID = root.ID
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_type WHERE id = $1 OR cloned_from_id = $1 ORDER BY version ASC",
			typeSelectColumns), rootID)
		if err != nil {
			return fmt.Errorf("list ci type versions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanType(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	return out, err
}

// TypeChain implements Repository: the chain root → id (ci_type_ancestors,
// MET-06) with the field definitions of each type, in one query. It runs
// on every attribute validation of a CI write (ResolveFields), also in the
// ingest record transaction, which reads no ci_type rows (budget of
// TestIngestStatementBudget): each level carries its ID and its
// definitions only; GetByID and TypeSubtree read the type columns. A type
// the organization does not see has an empty chain.
func (r *PGRepository) TypeChain(ctx context.Context, orgID, id string) ([]Type, error) {
	out := []Type{}
	if !api.IsUUID(id) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id::text, COALESCE((
				SELECT jsonb_agg(to_jsonb(a) ORDER BY a.sort_order, a.key)
				FROM attribute_definition a WHERE a.ci_type_id = c.id), '[]'::jsonb)
			FROM ci_type_ancestors($1::uuid) c ORDER BY c.level`, id)
		if err != nil {
			return fmt.Errorf("read ci type chain: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t Type
			var defs []byte
			if err = rows.Scan(&t.ID, &defs); err != nil {
				return fmt.Errorf("scan ci type chain: %w", err)
			}
			attrs, err := attributesFromJSON(defs)
			if err != nil {
				return err
			}
			for i := range attrs {
				t.Fields = append(t.Fields, attrs[i].Field())
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// TypeSubtree implements Repository: id and every descendant the
// organization sees (ci_type_subtree, MET-06), by depth and name.
func (r *PGRepository) TypeSubtree(ctx context.Context, orgID, id string) ([]Type, error) {
	out := []Type{}
	if !api.IsUUID(id) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+typeSelectColumns+` FROM (
				SELECT s.depth AS lvl, t.* FROM ci_type_subtree($1::uuid) s JOIN ci_type t ON t.id = s.id
			) subtree ORDER BY lvl, name, id`, id)
		if err != nil {
			return fmt.Errorf("read ci type subtree: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanType(rows)
			if err != nil {
				return fmt.Errorf("scan ci type subtree: %w", err)
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	return out, err
}

// extraColumns scans a row whose columns surround those of another
// scanner: before precede them, after follow them.
type extraColumns struct {
	row           interface{ Scan(dest ...any) error }
	before, after []any
}

func (e extraColumns) Scan(dest ...any) error {
	all := make([]any, 0, len(e.before)+len(dest)+len(e.after))
	all = append(append(append(all, e.before...), dest...), e.after...)
	return e.row.Scan(all...)
}

// attributeRowJSON is a row of attribute_definition as to_jsonb renders it.
type attributeRowJSON struct {
	ID              string          `json:"id"`
	OrganizationID  *string         `json:"organization_id"`
	CITypeID        *string         `json:"ci_type_id"`
	Scope           string          `json:"scope"`
	Key             string          `json:"key"`
	DisplayName     json.RawMessage `json:"display_name"`
	Description     *string         `json:"description"`
	DataType        string          `json:"data_type"`
	Required        bool            `json:"required"`
	DefaultValue    json.RawMessage `json:"default_value"`
	EnumValues      json.RawMessage `json:"enum_values"`
	MultiValue      bool            `json:"multi_value"`
	Searchable      bool            `json:"searchable"`
	IsUnique        bool            `json:"is_unique"`
	UniqueScope     string          `json:"unique_scope"`
	Inheritable     bool            `json:"inheritable"`
	Validation      json.RawMessage `json:"validation"`
	Conditional     json.RawMessage `json:"conditional"`
	ReferenceTarget *string         `json:"reference_target"`
	GroupName       *string         `json:"group_name"`
	SortOrder       int             `json:"sort_order"`
	Unit            *string         `json:"unit"`
}

// attributesFromJSON decodes a JSON array of attribute_definition rows the
// way scanAttribute reads a row.
func attributesFromJSON(raw []byte) ([]AttributeDefinition, error) {
	var rows []attributeRowJSON
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode attribute definitions: %w", err)
	}
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	out := make([]AttributeDefinition, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		d := AttributeDefinition{
			ID: row.ID, OrganizationID: str(row.OrganizationID), CITypeID: str(row.CITypeID), Scope: row.Scope,
			Key: row.Key, Description: str(row.Description), DataType: row.DataType, Required: row.Required,
			MultiValue: row.MultiValue, Searchable: row.Searchable, Unique: row.IsUnique, UniqueScope: row.UniqueScope,
			Inheritable: row.Inheritable, ReferenceTarget: str(row.ReferenceTarget), Group: str(row.GroupName),
			Order: row.SortOrder, Unit: str(row.Unit),
		}
		if err := json.Unmarshal(row.DisplayName, &d.DisplayName); err != nil {
			return nil, fmt.Errorf("decode display_name: %w", err)
		}
		if len(row.DefaultValue) > 0 && string(row.DefaultValue) != "null" {
			d.Default = row.DefaultValue
		}
		if len(row.EnumValues) > 0 {
			_ = json.Unmarshal(row.EnumValues, &d.EnumValues)
		}
		d.Validation = fieldmeta.StoredValidation(row.Validation)
		if len(row.Conditional) > 0 && string(row.Conditional) != "null" {
			var c fieldmeta.ConditionalRules
			if json.Unmarshal(row.Conditional, &c) == nil {
				d.Conditional = &c
			}
		}
		d.IsSystem = d.OrganizationID == ""
		out = append(out, d)
	}
	return out, nil
}

// ListFields returns the type-scope field definitions of a type.
func (r *PGRepository) ListFields(ctx context.Context, orgID, typeID string) ([]Field, error) {
	var out []Field
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		flds, err := listFieldsTx(ctx, tx, typeID)
		if err != nil {
			return err
		}
		out = flds
		return nil
	})
	return out, err
}

// ListGlobalFields returns the global field definitions (scope 'global',
// no CI type): the organization's and the system's.
func (r *PGRepository) ListGlobalFields(ctx context.Context, orgID string) ([]Field, error) {
	var out []Field
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		flds, err := listFieldsScopeTx(ctx, tx, "", "global")
		if err != nil {
			return err
		}
		out = flds
		return nil
	})
	return out, err
}

// UpsertField creates or updates a field definition (type or global scope)
// in attribute_definition, keyed by type and name (legacy field view). A
// type-bound field needs a type of the organization: an unknown type is
// "not found" (404), a system type a 422 on ci_type_id (checkFieldType).
func (r *PGRepository) UpsertField(ctx context.Context, orgID, typeID string, req UpsertFieldRequest) (*Field, error) {
	if err := fieldmeta.ValidateDefinition(fieldFromUpsert(req)); err != nil {
		return nil, err
	}
	if req.Scope == "" {
		req.Scope = "type"
	}
	if req.Scope == "global" {
		typeID = ""
	}
	var out *Field
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if req.Scope != "global" {
			if err := checkFieldType(ctx, tx, typeID); err != nil {
				return err
			}
		}
		before, err := findAttributeTx(ctx, tx, typeID, req.Name)
		if err != nil && !errors.Is(err, ErrAttributeNotFound) {
			return err
		}
		f, err := upsertFieldTx(ctx, tx, orgID, typeID, &req)
		if err != nil {
			return err
		}
		out = f
		after, err := findAttributeTx(ctx, tx, typeID, req.Name)
		if err != nil {
			return err
		}
		return recordAttributeAudit(ctx, tx, orgID, before, after)
	})
	return out, err
}

// upsertFieldTx writes a legacy field into attribute_definition: an
// existing definition of the same key keeps its MET-10 settings that the
// legacy field cannot express.
func upsertFieldTx(ctx context.Context, tx pgx.Tx, orgID, typeID string, req *UpsertFieldRequest) (*Field, error) {
	existing, err := findAttributeTx(ctx, tx, typeID, req.Name)
	if err != nil && !errors.Is(err, ErrAttributeNotFound) {
		return nil, err
	}
	d := existing
	if d == nil {
		d = &AttributeDefinition{CITypeID: typeID, Scope: req.Scope, Key: req.Name, UniqueScope: "org", Inheritable: true}
	}
	d.DisplayName = nil
	if strings.TrimSpace(req.Label) != "" {
		d.DisplayName = LocalizedText{"de": req.Label, "en": req.Label}
	}
	d.Description, d.DataType, d.Required = req.Description, req.DataType, req.Required
	d.Default = nil
	if req.DefaultValue != "" {
		raw, err := json.Marshal(req.DefaultValue)
		if err != nil {
			return nil, fmt.Errorf("encode default value: %w", err)
		}
		d.Default = raw
	}
	d.EnumValues, d.Group, d.Order = req.EnumValues, req.UIGroup, req.SortOrder
	// The legacy structure is stored as its rule AST (WP-589).
	d.Validation, d.Conditional, d.ReferenceTarget = req.Validation.AST(), req.Conditional, req.ReferenceTarget
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if existing != nil {
		if err := updateAttributeTx(ctx, tx, d); err != nil {
			return nil, err
		}
	} else if err := insertAttributeTx(ctx, tx, orgID, d); err != nil {
		return nil, err
	}
	f := d.Field()
	return &f, nil
}

// findAttributeTx returns the definition of key on typeID (empty: global
// of the organization).
func findAttributeTx(ctx context.Context, tx pgx.Tx, typeID, key string) (*AttributeDefinition, error) {
	query := `SELECT ` + attributeColumns + ` FROM attribute_definition WHERE ci_type_id::text = $1 AND key = $2`
	args := []any{typeID, key}
	if typeID == "" {
		query = `SELECT ` + attributeColumns + ` FROM attribute_definition
			WHERE ci_type_id IS NULL AND organization_id IS NOT NULL AND key = $1`
		args = []any{key}
	}
	d, err := scanAttribute(tx.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttributeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read attribute definition: %w", err)
	}
	return d, nil
}

// DeleteField removes a field definition by name: of the type, otherwise
// the organization's global one.
func (r *PGRepository) DeleteField(ctx context.Context, orgID, typeID, name string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := findAttributeTx(ctx, tx, typeID, name)
		if errors.Is(err, ErrAttributeNotFound) {
			before, err = findAttributeTx(ctx, tx, "", name)
		}
		if errors.Is(err, ErrAttributeNotFound) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return err
		}
		cmd, err := tx.Exec(ctx, "DELETE FROM attribute_definition WHERE id = $1", before.ID)
		if err != nil {
			return fmt.Errorf("delete field definition: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return recordAttributeAudit(ctx, tx, orgID, before, nil)
	})
}

func listFieldsTx(ctx context.Context, tx pgx.Tx, typeID string) ([]Field, error) {
	return listFieldsScopeTx(ctx, tx, typeID, "type")
}

// listFieldsScopeTx lists the fields of a type, or the global fields when
// scope is "global".
func listFieldsScopeTx(ctx context.Context, tx pgx.Tx, typeID, scope string) ([]Field, error) {
	query := `SELECT ` + attributeColumns + ` FROM attribute_definition WHERE ci_type_id::text = $1 ORDER BY sort_order ASC, key ASC`
	args := []any{typeID}
	if scope == "global" {
		query = `SELECT ` + attributeColumns + ` FROM attribute_definition WHERE ci_type_id IS NULL ORDER BY sort_order ASC, key ASC`
		args = nil
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list field definitions: %w", err)
	}
	defer rows.Close()
	var out []Field
	for rows.Next() {
		d, err := scanAttribute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d.Field())
	}
	return out, rows.Err()
}

// ─── CI-instance field definitions ──────────────────────────────────────────

const instanceFieldSelectColumns = `
	id::text, organization_id::text, ci_id::text, name,
	COALESCE(label, ''), COALESCE(description, ''), data_type, required,
	COALESCE(default_value, ''), enum_values, COALESCE(ui_group, ''),
	sort_order, validation, conditional, COALESCE(reference_target, ''),
	created_at, updated_at
`

// visibleCI restricts instance field rows to CIs visible under the
// transaction's tenant scope: the ci policy filters the subquery by client
// scope, while ci_instance_field_definition carries no client column yet
// (WP-025).
const visibleCI = "EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_instance_field_definition.ci_id)"

// ListInstanceFields returns the field definitions of a single CI instance.
func (r *PGRepository) ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error) {
	var out []InstanceField
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM ci_instance_field_definition WHERE ci_id = $1 AND organization_id = $2 AND "+visibleCI+" ORDER BY sort_order ASC, name ASC",
			instanceFieldSelectColumns), ciID, orgID)
		if err != nil {
			return fmt.Errorf("list instance fields: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanInstanceField(rows)
			if err != nil {
				return err
			}
			out = append(out, *f)
		}
		return rows.Err()
	})
	return out, err
}

// InstanceFieldNames lists the names of the instance attributes of a CI.
func (r *PGRepository) InstanceFieldNames(ctx context.Context, orgID, ciID string) ([]string, error) {
	fields, err := r.ListInstanceFields(ctx, orgID, ciID)
	return fieldNames(fields), err
}

// UpsertInstanceField creates or updates an instance field definition and
// records the change in the audit log (MET-14).
func (r *PGRepository) UpsertInstanceField(ctx context.Context, orgID, ciID string, req UpsertInstanceFieldRequest) (*InstanceField, error) {
	if err := fieldmeta.ValidateDefinition(instanceFieldFromUpsert(req)); err != nil {
		return nil, err
	}
	enumJSON := "null"
	if req.EnumValues != nil {
		raw, err := json.Marshal(req.EnumValues)
		if err != nil {
			return nil, fmt.Errorf("marshal enum values: %w", err)
		}
		enumJSON = string(raw)
	}
	validation := any(emptyObject)
	if req.Validation != nil {
		validation = req.Validation
	}
	var conditional any
	if req.Conditional != nil {
		conditional = req.Conditional
	}
	var out *InstanceField
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := getInstanceField(ctx, tx, ciID, req.Name)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			INSERT INTO ci_instance_field_definition (
				organization_id, ci_id, name, label, description, data_type,
				required, default_value, enum_values, ui_group, sort_order,
				validation, conditional, reference_target
			)
			SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::text, $6::text, $7::boolean,
				$8::text, $9::jsonb, $10::text, $11::integer, $12::jsonb, $13::jsonb, $14::text
			WHERE EXISTS (SELECT 1 FROM ci WHERE ci.id = $2)
			ON CONFLICT (ci_id, name) DO UPDATE SET
				label = EXCLUDED.label,
				description = EXCLUDED.description,
				data_type = EXCLUDED.data_type,
				required = EXCLUDED.required,
				default_value = EXCLUDED.default_value,
				enum_values = EXCLUDED.enum_values,
				ui_group = EXCLUDED.ui_group,
				sort_order = EXCLUDED.sort_order,
				validation = EXCLUDED.validation,
				conditional = EXCLUDED.conditional,
				reference_target = EXCLUDED.reference_target
			RETURNING %s`, instanceFieldSelectColumns),
			orgID, ciID, req.Name, nilIfEmpty(req.Label), nilIfEmpty(req.Description),
			req.DataType, req.Required, nilIfEmpty(req.DefaultValue), enumJSON,
			nilIfEmpty(req.UIGroup), req.SortOrder, validation, conditional,
			nilIfEmpty(req.ReferenceTarget))
		if err != nil {
			return fmt.Errorf("upsert instance field: %w", err)
		}
		if rows.Next() {
			f, scanErr := scanInstanceField(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			out = f
		}
		rows.Close()
		if err = rows.Err(); err != nil || out == nil {
			return err
		}
		action, changes := "ci_instance_field.created", map[string]any{"after": out}
		if before != nil {
			action, changes = "ci_instance_field.updated", map[string]any{"before": before, "after": out}
		}
		return recordInstanceFieldAudit(ctx, tx, action, out, changes)
	})
	if out == nil && err == nil {
		return nil, fmt.Errorf("not found")
	}
	return out, err
}

// DeleteInstanceField removes an instance field definition by name.
func (r *PGRepository) DeleteInstanceField(ctx context.Context, orgID, ciID, name string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := getInstanceField(ctx, tx, ciID, name)
		if err != nil {
			return err
		}
		if before == nil {
			return fmt.Errorf("not found")
		}
		if _, err = tx.Exec(ctx,
			"DELETE FROM ci_instance_field_definition WHERE ci_id = $1 AND name = $2 AND "+visibleCI, ciID, name); err != nil {
			return fmt.Errorf("delete instance field: %w", err)
		}
		return recordInstanceFieldAudit(ctx, tx, "ci_instance_field.deleted", before, map[string]any{"before": before})
	})
}

// getInstanceField returns the visible instance field ciID/name or nil.
func getInstanceField(ctx context.Context, tx pgx.Tx, ciID, name string) (*InstanceField, error) {
	f, err := scanInstanceField(tx.QueryRow(ctx, fmt.Sprintf(
		"SELECT %s FROM ci_instance_field_definition WHERE ci_id::text = $1 AND name = $2 AND "+visibleCI,
		instanceFieldSelectColumns), ciID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get instance field: %w", err)
	}
	return f, nil
}

// recordInstanceFieldAudit writes the audit entry of an instance field
// definition change in the same transaction (MET-14). The resource is the
// CI, so the entry appears with the CI's other changes.
func recordInstanceFieldAudit(ctx context.Context, tx pgx.Tx, action string, f *InstanceField, changes map[string]any) error {
	actor := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	actorType := "system"
	if actor != "" {
		actorType = "user"
	}
	changes["field"] = f.Name
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: f.OrganizationID,
		ActorID:        actor,
		ActorType:      actorType,
		Action:         action,
		ResourceType:   "ci",
		ResourceID:     f.CIID,
		Changes:        changes,
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

type instanceFieldScanner interface {
	Scan(dest ...any) error
}

func scanInstanceField(scanner instanceFieldScanner) (*InstanceField, error) {
	f := &InstanceField{}
	var enumRaw, validationRaw, conditionalRaw []byte
	if err := scanner.Scan(
		&f.ID, &f.OrganizationID, &f.CIID, &f.Name, &f.Label, &f.Description,
		&f.DataType, &f.Required, &f.DefaultValue, &enumRaw, &f.UIGroup,
		&f.SortOrder, &validationRaw, &conditionalRaw, &f.ReferenceTarget,
		&f.CreatedAt, &f.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(enumRaw) > 0 {
		_ = json.Unmarshal(enumRaw, &f.EnumValues)
	}
	if len(validationRaw) > 0 && string(validationRaw) != "{}" {
		var v fieldmeta.ValidationRules
		if err := json.Unmarshal(validationRaw, &v); err == nil {
			f.Validation = &v
		}
	}
	if len(conditionalRaw) > 0 {
		var c fieldmeta.ConditionalRules
		if err := json.Unmarshal(conditionalRaw, &c); err == nil {
			f.Conditional = &c
		}
	}
	return f, nil
}

// ─── shared scan helpers ─────────────────────────────────────────────────────

type typeScanner interface {
	Scan(dest ...any) error
}

func scanType(scanner typeScanner) (*Type, error) {
	t := &Type{}
	var capabilities, allowedRels, uiSchema, compliance, discovery, displayName, attributeSchema []byte
	if err := scanner.Scan(
		&t.ID, &t.OrganizationID, &t.Key, &t.Name, &displayName, &t.Icon,
		&t.Description, &t.Category, &t.IsBuiltin, &t.IsSystem, &t.IsActive,
		&t.IsLogical, &t.IsObservable, &t.SchemaVersion, &t.Version, &t.ClonedFromID, &t.ParentTypeID, &t.TemplateKey,
		&t.LifecycleDefinitionID, &capabilities, &allowedRels, &uiSchema,
		&compliance, &discovery, &attributeSchema, &t.RequiredFields, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(displayName, &t.DisplayName); err != nil {
		return nil, fmt.Errorf("decode display_name: %w", err)
	}
	if len(attributeSchema) > 0 {
		_ = json.Unmarshal(attributeSchema, &t.AttributeSchema)
	}
	t.Capabilities = map[string]any{}
	t.UISchema = map[string]any{}
	t.AllowedRelationshipTypes = []string{}
	t.ComplianceRules = []any{}
	t.DiscoveryMappings = []any{}
	if len(capabilities) > 0 {
		_ = json.Unmarshal(capabilities, &t.Capabilities)
	}
	if len(uiSchema) > 0 {
		_ = json.Unmarshal(uiSchema, &t.UISchema)
	}
	if len(allowedRels) > 0 {
		_ = json.Unmarshal(allowedRels, &t.AllowedRelationshipTypes)
	}
	if len(compliance) > 0 {
		_ = json.Unmarshal(compliance, &t.ComplianceRules)
	}
	if len(discovery) > 0 {
		_ = json.Unmarshal(discovery, &t.DiscoveryMappings)
	}
	return t, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// Empty JSON documents used when a metadata column has no value. The columns
// are NOT NULL with a DEFAULT, and an explicit NULL parameter suppresses the
// default, so the empty document must be sent instead.
const (
	emptyObject = "{}"
	emptyArray  = "[]"
)

// jsonOrDefault substitutes an explicit empty JSON document for a nil value.
// Passing SQL NULL for these columns violates their NOT NULL constraint even
// though they declare a DEFAULT, because an explicit parameter suppresses the
// column default.
func jsonOrDefault(v any, fallback string) any {
	if v == nil || reflect.ValueOf(v).IsZero() {
		return fallback
	}
	return v
}

// recordMetamodelAudit writes the audit entry of a metamodel change in the
// transaction of the change (AUD-07, MET-54): the definition before and
// after; the request context adds request and correlation id and source.
// Metamodel definitions carry no personal data.
func recordMetamodelAudit(ctx context.Context, tx pgx.Tx, orgID, action, resourceType, resourceID string, before, after any) error {
	changes := map[string]any{}
	for key, v := range map[string]any{"before": before, "after": after} {
		if v == nil || (key == "before" && isNilPointer(v)) {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode audit %s: %w", key, err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return fmt.Errorf("decode audit %s: %w", key, err)
		}
		changes[key] = decoded
	}
	actor := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	actorType := "system"
	if actor != "" {
		actorType = "user"
	}
	if !audit.IsUUID(actor) {
		actor = ""
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID, ActorID: actor, ActorType: actorType, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Changes: changes,
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

// recordAttributeAudit audits a created (before nil), changed or deleted
// (after nil) attribute definition.
func recordAttributeAudit(ctx context.Context, tx pgx.Tx, orgID string, before, after *AttributeDefinition) error {
	action, id := "attribute_definition.updated", ""
	switch {
	case before == nil && after != nil:
		action, id = "attribute_definition.created", after.ID
	case after == nil && before != nil:
		action, id = "attribute_definition.deleted", before.ID
	case after != nil:
		id = after.ID
	}
	var b, a any
	if before != nil {
		b = before
	}
	if after != nil {
		a = after
	}
	return recordMetamodelAudit(ctx, tx, orgID, action, "attribute_definition", id, b, a)
}

func isNilPointer(v any) bool {
	switch p := v.(type) {
	case *Type:
		return p == nil
	case *AttributeDefinition:
		return p == nil
	}
	return false
}
