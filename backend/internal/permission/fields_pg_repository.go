package permission

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGFieldGrants implements FieldGrantRepository on PostgreSQL with RLS.
// Role, custom role and team references carry the organization (foreign
// keys, 23503 for a foreign or unknown one); CI types, relationship types,
// actions, CI attribute and instance keys and the objects of object grants
// are checked under RLS in the request's transaction.
type PGFieldGrants struct{ pool *pgxpool.Pool }

// NewPGFieldGrants creates the PostgreSQL field permission repository.
func NewPGFieldGrants(pool *pgxpool.Pool) *PGFieldGrants { return &PGFieldGrants{pool: pool} }

var _ FieldGrantRepository = (*PGFieldGrants)(nil)

const fieldGrantColumns = `fg.id::text, fg.organization_id::text, fg.role_id::text, fg.custom_role_id::text, fg.object_kind,
	fg.ci_type_id::text, fg.field_key, fg.relationship_type_id::text, COALESCE(rt.key, ''), fg.action, fg.level,
	fg.created_by::text, fg.created_at, fg.updated_at`

const fieldGrantFrom = `field_grant fg LEFT JOIN relationship_type rt ON rt.id = fg.relationship_type_id`

func scanFieldGrant(row pgx.Row) (FieldGrant, error) {
	var g FieldGrant
	err := row.Scan(&g.ID, &g.OrganizationID, &g.RoleID, &g.CustomRoleID, &g.ObjectKind, &g.CITypeID, &g.FieldKey,
		&g.RelationshipTypeID, &g.RelationshipType, &g.Action, &g.Level, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

const objectGrantColumns = `id::text, organization_id::text, grantee_role_id::text, grantee_custom_role_id::text,
	grantee_team_id::text, object_kind, object_id::text, level, created_by::text, created_at`

func scanObjectGrant(row pgx.Row) (ObjectGrant, error) {
	var g ObjectGrant
	err := row.Scan(&g.ID, &g.OrganizationID, &g.GranteeRoleID, &g.GranteeCustomRoleID, &g.GranteeTeamID,
		&g.ObjectKind, &g.ObjectID, &g.Level, &g.CreatedBy, &g.CreatedAt)
	return g, err
}

const sensitiveColumns = `id::text, organization_id::text, object_kind, ci_type_id::text, field_key, created_by::text, created_at`

func scanSensitive(row pgx.Row) (SensitiveField, error) {
	var s SensitiveField
	err := row.Scan(&s.ID, &s.OrganizationID, &s.ObjectKind, &s.CITypeID, &s.FieldKey, &s.CreatedBy, &s.CreatedAt)
	return s, err
}

// collect runs a query and scans every row with scan.
func collect[T any](ctx context.Context, tx pgx.Tx, scan func(pgx.Row) (T, error), sql string, args ...any) ([]T, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]T, 0)
	for rows.Next() {
		item, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// exists runs a visibility check under the transaction's RLS.
func exists(ctx context.Context, tx pgx.Tx, query string, args ...any) (bool, error) {
	var ok bool
	if err := tx.QueryRow(ctx, query, args...).Scan(&ok); err != nil {
		return false, fmt.Errorf("check reference: %w", err)
	}
	return ok, nil
}

// checkVisible returns ErrUnknownReference unless the check finds a row.
func checkVisible(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	ok, err := exists(ctx, tx, query, args...)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnknownReference
	}
	return nil
}

func checkUUIDRef(id *string) error {
	if id != nil && !api.IsUUID(*id) {
		return ErrUnknownReference
	}
	return nil
}

// checkTypeVisible refuses a CI type the caller cannot see (system types
// and the organization's own).
func checkTypeVisible(ctx context.Context, tx pgx.Tx, typeID *string) error {
	if typeID == nil {
		return nil
	}
	if err := checkUUIDRef(typeID); err != nil {
		return err
	}
	return checkVisible(ctx, tx, `SELECT EXISTS (SELECT 1 FROM ci_type WHERE id = $1)`, *typeID)
}

// checkFieldKeyExists checks that a CI attribute key has a visible
// attribute definition and a CI instance key a visible instance field
// definition; other keys were checked by ParseFieldKey.
func checkFieldKeyExists(ctx context.Context, tx pgx.Tx, kind ObjectKind, key string) error {
	parsed, err := ParseFieldKey(kind, key)
	if err != nil {
		return err
	}
	var query string
	switch {
	case kind == KindCI && parsed.Namespace == NamespaceAttributes:
		query = `SELECT EXISTS (SELECT 1 FROM attribute_definition WHERE key = $1)`
	case kind == KindCI && parsed.Namespace == NamespaceInstance:
		query = `SELECT EXISTS (SELECT 1 FROM ci_instance_field_definition WHERE name = $1)`
	default:
		return nil
	}
	ok, err := exists(ctx, tx, query, parsed.Name)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnknownFieldKey
	}
	return nil
}

// notFound maps a missing row to api.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ErrNotFound
	}
	return err
}

func (r *PGFieldGrants) CreateFieldGrant(ctx context.Context, orgID string, g *FieldGrant) (FieldGrant, error) {
	if err := checkFieldGrant(g); err != nil {
		return FieldGrant{}, err
	}
	for _, id := range []*string{g.RoleID, g.CustomRoleID, g.CITypeID, g.RelationshipTypeID, g.CreatedBy} {
		if err := checkUUIDRef(id); err != nil {
			return FieldGrant{}, err
		}
	}
	var out FieldGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkTypeVisible(ctx, tx, g.CITypeID); err != nil {
			return err
		}
		if g.RelationshipTypeID != nil {
			if err := checkVisible(ctx, tx, `SELECT EXISTS (SELECT 1 FROM relationship_type WHERE id = $1)`, *g.RelationshipTypeID); err != nil {
				return err
			}
		}
		if g.Action != nil {
			if err := checkVisible(ctx, tx, `SELECT EXISTS (SELECT 1 FROM permission WHERE key = $1)`, *g.Action); err != nil {
				return err
			}
		}
		if g.FieldKey != nil {
			if err := checkFieldKeyExists(ctx, tx, g.ObjectKind, *g.FieldKey); err != nil {
				return err
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO field_grant (organization_id, role_id, custom_role_id, object_kind, ci_type_id, field_key,
			                         relationship_type_id, action, level, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
			orgID, g.RoleID, g.CustomRoleID, g.ObjectKind, g.CITypeID, g.FieldKey, g.RelationshipTypeID, g.Action,
			g.Level, g.CreatedBy).Scan(&id); err != nil {
			return fmt.Errorf("insert field grant: %w", err)
		}
		var err error
		out, err = scanFieldGrant(tx.QueryRow(ctx, `SELECT `+fieldGrantColumns+` FROM `+fieldGrantFrom+` WHERE fg.id = $1`, id))
		return err
	})
	return out, err
}

func (r *PGFieldGrants) GetFieldGrant(ctx context.Context, orgID, id string) (FieldGrant, error) {
	if !api.IsUUID(id) {
		return FieldGrant{}, api.ErrNotFound
	}
	var out FieldGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanFieldGrant(tx.QueryRow(ctx, `SELECT `+fieldGrantColumns+` FROM `+fieldGrantFrom+`
			WHERE fg.id = $1 AND fg.organization_id = $2`, id, orgID))
		return notFound(err)
	})
	return out, err
}

func (r *PGFieldGrants) UpdateFieldGrantLevel(ctx context.Context, orgID, id string, level Level) (FieldGrant, error) {
	if !level.Valid() {
		return FieldGrant{}, fieldError("level", "level must be none, read or write")
	}
	if !api.IsUUID(id) {
		return FieldGrant{}, api.ErrNotFound
	}
	var out FieldGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var action *string
		if err := tx.QueryRow(ctx, `SELECT action FROM field_grant WHERE id = $1 AND organization_id = $2 FOR UPDATE`,
			id, orgID).Scan(&action); err != nil {
			return notFound(err)
		}
		if action != nil && level == LevelRead {
			return fieldError("level", "an action grant is none or write")
		}
		if _, err := tx.Exec(ctx, `UPDATE field_grant SET level = $3 WHERE id = $1 AND organization_id = $2`, id, orgID, level); err != nil {
			return fmt.Errorf("update field grant: %w", err)
		}
		var err error
		out, err = scanFieldGrant(tx.QueryRow(ctx, `SELECT `+fieldGrantColumns+` FROM `+fieldGrantFrom+` WHERE fg.id = $1`, id))
		return err
	})
	return out, err
}

// deleteRow deletes a row of table by id; no row is api.ErrNotFound.
func (r *PGFieldGrants) deleteRow(ctx context.Context, table, orgID, id string) error {
	if !api.IsUUID(id) {
		return api.ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE id = $1 AND organization_id = $2`, id, orgID)
		if err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
		if tag.RowsAffected() == 0 {
			return api.ErrNotFound
		}
		return nil
	})
}

func (r *PGFieldGrants) DeleteFieldGrant(ctx context.Context, orgID, id string) error {
	return r.deleteRow(ctx, "field_grant", orgID, id)
}

// listQuery builds the filtered keyset query of a list; filters maps
// columns to values, empty values do not filter.
type listQuery struct {
	where []string
	args  []any
}

func (q *listQuery) add(column, value string) error {
	if value == "" {
		return nil
	}
	if strings.HasSuffix(column, "_id") && !api.IsUUID(value) {
		return fieldError(strings.TrimPrefix(column, "fg."), "must be a UUID")
	}
	q.args = append(q.args, value)
	q.where = append(q.where, column+" = $"+strconv.Itoa(len(q.args)))
	return nil
}

// page appends the cursor predicate and the order and limit.
func (q *listQuery) page(prefix string, page api.PaginationParams) string {
	where := strings.Join(q.where, " AND ")
	if page.Cursor != nil {
		created, err := time.Parse(cursorTime, page.Cursor.Value)
		if err == nil && api.IsUUID(page.Cursor.ID) {
			q.args = append(q.args, created, page.Cursor.ID)
			n := len(q.args)
			where += fmt.Sprintf(" AND (%[1]screated_at, %[1]sid) > ($%[2]d::timestamptz, $%[3]d::uuid)", prefix, n-1, n)
		} else {
			where += " AND false"
		}
	}
	q.args = append(q.args, pageLimit(page)+1)
	tail := fmt.Sprintf(" ORDER BY %[1]screated_at, %[1]sid LIMIT $%[2]d", prefix, len(q.args))
	if page.Cursor == nil && page.Offset > 0 {
		q.args = append(q.args, page.Offset)
		tail += fmt.Sprintf(" OFFSET $%d", len(q.args))
	}
	return " WHERE " + where + tail
}

// trim cuts a page of limit+1 rows to limit and returns the next cursor.
func trim[T any](items []T, page api.PaginationParams, key func(*T) (time.Time, string)) (rows []T, next string) {
	limit := pageLimit(page)
	if len(items) <= limit {
		return items, ""
	}
	items = items[:limit]
	created, id := key(&items[limit-1])
	return items, nextCursor(true, created, id)
}

func (r *PGFieldGrants) ListFieldGrants(ctx context.Context, orgID string, f *FieldGrantFilter, page api.PaginationParams) ([]FieldGrant, string, error) {
	if f == nil {
		f = &FieldGrantFilter{}
	}
	if err := checkCursor(page); err != nil {
		return nil, "", err
	}
	q := &listQuery{}
	for _, filter := range [][2]string{{"fg.organization_id", orgID}, {"fg.role_id", f.RoleID},
		{"fg.custom_role_id", f.CustomRoleID}, {"fg.object_kind", string(f.ObjectKind)}, {"fg.ci_type_id", f.CITypeID}} {
		if err := q.add(filter[0], filter[1]); err != nil {
			return nil, "", err
		}
	}
	var items []FieldGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = collect(ctx, tx, scanFieldGrant, `SELECT `+fieldGrantColumns+` FROM `+fieldGrantFrom+q.page("fg.", page), q.args...)
		if err != nil {
			return fmt.Errorf("list field grants: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	items, next := trim(items, page, func(g *FieldGrant) (time.Time, string) { return g.CreatedAt, g.ID })
	return items, next, nil
}

// objectTables names the table of each object kind (contract since WP-269).
var objectTables = map[ObjectKind]string{
	KindCI: "ci", KindAsset: "asset", KindContract: "contract", KindTicket: "ticket", KindDocument: "document",
	KindRelationship: "ci_relationship",
}

func (r *PGFieldGrants) CreateObjectGrant(ctx context.Context, orgID string, g *ObjectGrant) (ObjectGrant, error) {
	if err := checkObjectGrant(g); err != nil {
		return ObjectGrant{}, err
	}
	for _, id := range []*string{g.GranteeRoleID, g.GranteeCustomRoleID, g.GranteeTeamID, g.CreatedBy} {
		if err := checkUUIDRef(id); err != nil {
			return ObjectGrant{}, err
		}
	}
	table, ok := objectTables[g.ObjectKind]
	if !ok {
		return ObjectGrant{}, ErrUnknownReference
	}
	var out ObjectGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, g.ObjectID); err != nil {
			return err
		}
		var err error
		out, err = scanObjectGrant(tx.QueryRow(ctx, `
			INSERT INTO object_grant (organization_id, grantee_role_id, grantee_custom_role_id, grantee_team_id,
			                          object_kind, object_id, level, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+objectGrantColumns,
			orgID, g.GranteeRoleID, g.GranteeCustomRoleID, g.GranteeTeamID, g.ObjectKind, g.ObjectID, g.Level, g.CreatedBy))
		if err != nil {
			return fmt.Errorf("insert object grant: %w", err)
		}
		return nil
	})
	return out, err
}

func (r *PGFieldGrants) GetObjectGrant(ctx context.Context, orgID, id string) (ObjectGrant, error) {
	if !api.IsUUID(id) {
		return ObjectGrant{}, api.ErrNotFound
	}
	var out ObjectGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanObjectGrant(tx.QueryRow(ctx, `SELECT `+objectGrantColumns+` FROM object_grant
			WHERE id = $1 AND organization_id = $2`, id, orgID))
		return notFound(err)
	})
	return out, err
}

func (r *PGFieldGrants) DeleteObjectGrant(ctx context.Context, orgID, id string) error {
	return r.deleteRow(ctx, "object_grant", orgID, id)
}

func (r *PGFieldGrants) ListObjectGrants(ctx context.Context, orgID string, f *ObjectGrantFilter, page api.PaginationParams) ([]ObjectGrant, string, error) {
	if f == nil {
		f = &ObjectGrantFilter{}
	}
	if err := checkCursor(page); err != nil {
		return nil, "", err
	}
	q := &listQuery{}
	for _, filter := range [][2]string{{"organization_id", orgID}, {"object_kind", string(f.ObjectKind)},
		{"object_id", f.ObjectID}, {"grantee_role_id", f.GranteeRoleID},
		{"grantee_custom_role_id", f.GranteeCustomRoleID}, {"grantee_team_id", f.GranteeTeamID}} {
		if err := q.add(filter[0], filter[1]); err != nil {
			return nil, "", err
		}
	}
	var items []ObjectGrant
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = collect(ctx, tx, scanObjectGrant, `SELECT `+objectGrantColumns+` FROM object_grant`+q.page("", page), q.args...)
		if err != nil {
			return fmt.Errorf("list object grants: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	items, next := trim(items, page, func(g *ObjectGrant) (time.Time, string) { return g.CreatedAt, g.ID })
	return items, next, nil
}

func (r *PGFieldGrants) CreateSensitiveField(ctx context.Context, orgID string, s *SensitiveField) (SensitiveField, error) {
	if err := checkSensitiveField(s); err != nil {
		return SensitiveField{}, err
	}
	for _, id := range []*string{s.CITypeID, s.CreatedBy} {
		if err := checkUUIDRef(id); err != nil {
			return SensitiveField{}, err
		}
	}
	var out SensitiveField
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkTypeVisible(ctx, tx, s.CITypeID); err != nil {
			return err
		}
		if err := checkFieldKeyExists(ctx, tx, s.ObjectKind, s.FieldKey); err != nil {
			return err
		}
		var err error
		out, err = scanSensitive(tx.QueryRow(ctx, `
			INSERT INTO field_sensitivity (organization_id, object_kind, ci_type_id, field_key, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+sensitiveColumns,
			orgID, s.ObjectKind, s.CITypeID, s.FieldKey, s.CreatedBy))
		if err != nil {
			return fmt.Errorf("insert field sensitivity: %w", err)
		}
		return nil
	})
	return out, err
}

func (r *PGFieldGrants) GetSensitiveField(ctx context.Context, orgID, id string) (SensitiveField, error) {
	if !api.IsUUID(id) {
		return SensitiveField{}, api.ErrNotFound
	}
	var out SensitiveField
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanSensitive(tx.QueryRow(ctx, `SELECT `+sensitiveColumns+` FROM field_sensitivity
			WHERE id = $1 AND organization_id = $2`, id, orgID))
		return notFound(err)
	})
	return out, err
}

func (r *PGFieldGrants) DeleteSensitiveField(ctx context.Context, orgID, id string) error {
	return r.deleteRow(ctx, "field_sensitivity", orgID, id)
}

func (r *PGFieldGrants) ListSensitiveFields(ctx context.Context, orgID string, f *SensitiveFieldFilter, page api.PaginationParams) ([]SensitiveField, string, error) {
	if f == nil {
		f = &SensitiveFieldFilter{}
	}
	if err := checkCursor(page); err != nil {
		return nil, "", err
	}
	q := &listQuery{}
	for _, filter := range [][2]string{{"organization_id", orgID}, {"object_kind", string(f.ObjectKind)}, {"ci_type_id", f.CITypeID}} {
		if err := q.add(filter[0], filter[1]); err != nil {
			return nil, "", err
		}
	}
	var items []SensitiveField
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = collect(ctx, tx, scanSensitive, `SELECT `+sensitiveColumns+` FROM field_sensitivity`+q.page("", page), q.args...)
		if err != nil {
			return fmt.Errorf("list field sensitivity: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	items, next := trim(items, page, func(s *SensitiveField) (time.Time, string) { return s.CreatedAt, s.ID })
	return items, next, nil
}

// withOrgWide reads in an org-wide transaction of the organization (like
// AccessGrants): the resolver needs every row, not the caller's scope.
func (r *PGFieldGrants) withOrgWide(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, r.pool, &scope, fn)
}

func (r *PGFieldGrants) Snapshot(ctx context.Context, orgID string) (Snapshot, error) {
	var snap Snapshot
	err := r.withOrgWide(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if snap.FieldGrants, err = collect(ctx, tx, scanFieldGrant, `SELECT `+fieldGrantColumns+` FROM `+fieldGrantFrom+`
			WHERE fg.organization_id = $1 ORDER BY fg.created_at, fg.id`, orgID); err != nil {
			return fmt.Errorf("read field grants: %w", err)
		}
		if snap.ObjectGrants, err = collect(ctx, tx, scanObjectGrant, `SELECT `+objectGrantColumns+` FROM object_grant
			WHERE organization_id = $1 ORDER BY created_at, id`, orgID); err != nil {
			return fmt.Errorf("read object grants: %w", err)
		}
		if snap.Sensitive, err = collect(ctx, tx, scanSensitive, `SELECT `+sensitiveColumns+` FROM field_sensitivity
			WHERE organization_id = $1 ORDER BY created_at, id`, orgID); err != nil {
			return fmt.Errorf("read field sensitivity: %w", err)
		}
		if snap.Computed, err = readComputedSources(ctx, tx, orgID); err != nil {
			return fmt.Errorf("read computed attribute sources: %w", err)
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// readComputedSources reads the computed attribute definitions visible to
// the organization (its own and the system ones) whose expression reads an
// asset field (SR-58, WP-871). An expression that does not parse counts as
// reading every asset field of rules.AssetFields (fail-closed).
func readComputedSources(ctx context.Context, tx pgx.Tx, orgID string) ([]ComputedSource, error) {
	rows, err := tx.Query(ctx, `SELECT ci_type_id::text, key, computed FROM attribute_definition
		WHERE computed IS NOT NULL AND (organization_id = $1 OR organization_id IS NULL)
		ORDER BY key, ci_type_id NULLS FIRST, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComputedSource
	for rows.Next() {
		var typeID *string
		var key string
		var raw []byte
		if err := rows.Scan(&typeID, &key, &raw); err != nil {
			return nil, err
		}
		if fields := ComputedAssetFields(raw); len(fields) > 0 {
			out = append(out, ComputedSource{CITypeID: typeID, Attribute: key, AssetFields: fields})
		}
	}
	return out, rows.Err()
}

// TeamIDs reads team_member org-wide: its policy otherwise filters by the
// caller's team scope.
func (r *PGFieldGrants) TeamIDs(ctx context.Context, orgID, userID string) ([]string, error) {
	if !api.IsUUID(userID) {
		return []string{}, nil
	}
	var teams []string
	err := r.withOrgWide(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		teams, err = collect(ctx, tx, scanString, `SELECT team_id::text FROM team_member
			WHERE organization_id = $1 AND user_id = $2 ORDER BY team_id`, orgID, userID)
		if err != nil {
			return fmt.Errorf("read team memberships: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return teams, nil
}

func scanString(row pgx.Row) (string, error) {
	var s string
	err := row.Scan(&s)
	return s, err
}

// TypeChain reads ci_type.parent_type_id of the system types and the
// organization's own.
func (r *PGFieldGrants) TypeChain(ctx context.Context, orgID string) (map[string]string, error) {
	var pairs [][2]string
	err := r.withOrgWide(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		pairs, err = collect(ctx, tx, func(row pgx.Row) ([2]string, error) {
			var pair [2]string
			scanErr := row.Scan(&pair[0], &pair[1])
			return pair, scanErr
		}, `SELECT id::text, parent_type_id::text FROM ci_type WHERE parent_type_id IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("read type chain: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	parents := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		parents[pair[0]] = pair[1]
	}
	return parents, nil
}

func scanExportedFieldGrant(row pgx.Row) (ExportedFieldGrant, error) {
	var e ExportedFieldGrant
	err := row.Scan(&e.Role, &e.RoleKind, &e.ObjectKind, &e.CIType, &e.FieldKey, &e.RelationshipType, &e.Action, &e.Level)
	return e, err
}

func scanExportedSensitive(row pgx.Row) (ExportedSensitiveField, error) {
	var e ExportedSensitiveField
	err := row.Scan(&e.ObjectKind, &e.CIType, &e.FieldKey)
	return e, err
}

// Export reads in the request's tenant transaction (and joins the
// snapshot transaction of the v2 export, WP-262).
func (r *PGFieldGrants) Export(ctx context.Context, orgID string) (FieldGrantExport, error) {
	var out FieldGrantExport
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out.FieldGrants, err = collect(ctx, tx, scanExportedFieldGrant, `
			SELECT COALESCE(ro.name, cr.name, ''), CASE WHEN fg.role_id IS NOT NULL THEN 'role' ELSE 'custom_role' END,
			       fg.object_kind, COALESCE(ct.key, ''), COALESCE(fg.field_key, ''), COALESCE(rt.key, ''),
			       COALESCE(fg.action, ''), fg.level
			  FROM field_grant fg
			  LEFT JOIN role ro ON ro.id = fg.role_id AND ro.organization_id = fg.organization_id
			  LEFT JOIN custom_role cr ON cr.id = fg.custom_role_id AND cr.organization_id = fg.organization_id
			  LEFT JOIN ci_type ct ON ct.id = fg.ci_type_id
			  LEFT JOIN relationship_type rt ON rt.id = fg.relationship_type_id
			 WHERE fg.organization_id = $1`, orgID); err != nil {
			return fmt.Errorf("export field grants: %w", err)
		}
		if out.Sensitivity, err = collect(ctx, tx, scanExportedSensitive, `
			SELECT fs.object_kind, COALESCE(ct.key, ''), fs.field_key
			  FROM field_sensitivity fs LEFT JOIN ci_type ct ON ct.id = fs.ci_type_id
			 WHERE fs.organization_id = $1`, orgID); err != nil {
			return fmt.Errorf("export field sensitivity: %w", err)
		}
		return nil
	})
	if err != nil {
		return FieldGrantExport{}, err
	}
	sortExport(&out)
	return out, nil
}
