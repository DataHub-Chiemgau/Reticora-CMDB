package history

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const selectColumns = `
	id::text, organization_id::text, entity_type, entity_id::text,
	COALESCE(actor_id::text, ''), change_type, COALESCE(field_name, ''),
	old_value, new_value, COALESCE(comment, ''), created_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed history repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// visibleEntity hides the changes of a CI or an asset that is not visible
// under the transaction's tenant scope: the ci and asset policies filter the
// subqueries by client and site scope, while entity_change carries no
// client column yet (WP-025). An asset is deleted for good, so the changes
// of a deleted asset are hidden too (WP-516). Other entity types follow
// with their packages' WithTenant migration.
const visibleEntity = `(entity_type <> 'ci' OR EXISTS (SELECT 1 FROM ci WHERE ci.id = entity_change.entity_id))
	AND (entity_type <> 'asset' OR EXISTS (SELECT 1 FROM asset WHERE asset.id = entity_change.entity_id))`

// visibleSQL is the visibility check ($1: the entity id) of the entity
// types with a state snapshot (JOB-04): the row under the caller's tenant
// scope; a deleted CI keeps its row (CI-05).
var visibleSQL = map[string]string{
	"ci":    "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)",
	"asset": "SELECT EXISTS (SELECT 1 FROM asset WHERE id = $1::uuid)",
}

// Record appends a change row to the entity_change trail.
func (r *PGRepository) Record(ctx context.Context, change *Change) error {
	return database.WithRequestTenant(ctx, r.pool, change.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if change.EntityType == "ci" {
			var visible bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)", change.EntityID).Scan(&visible); err != nil {
				return fmt.Errorf("check ci visibility: %w", err)
			}
			if !visible {
				return fmt.Errorf("not found")
			}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO entity_change (
				organization_id, entity_type, entity_id, actor_id,
				change_type, field_name, old_value, new_value, comment
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING id::text, created_at`,
			change.OrganizationID, change.EntityType, change.EntityID,
			nilIfEmpty(change.ActorID), change.ChangeType, nilIfEmpty(change.FieldName),
			jsonOrNil(change.OldValue), jsonOrNil(change.NewValue),
			nilIfEmpty(change.Comment),
		).Scan(&change.ID, &change.CreatedAt)
	})
}

// locationChanges selects, as entries of the history of entityType, the
// location_change rows (WP-055) of the locations the subquery locationIDs
// names: time, actor, the moved location (id and the name kept after its
// deletion, WP-411) and its old and new parent and path. $1 is the
// organization, $2 the entity.
func locationChanges(entityType, locationIDs string) string {
	return `
	SELECT id::text, organization_id::text, '` + entityType + `', $2::uuid::text, COALESCE(changed_by, ''), 'location_change', 'parent_id',
	       jsonb_build_object('location_id', location_id, 'location_name', location_name, 'parent_id', old_parent_id, 'path', old_path),
	       jsonb_build_object('location_id', location_id, 'location_name', location_name, 'parent_id', new_parent_id, 'path', new_path),
	       '', changed_at, 'location_change', site_id::text` + noChangeSet + `
	FROM location_change WHERE organization_id = $1 AND location_id IN (` + locationIDs + `)` + noChangeKind
}

// ancestorsOf selects a location and its ancestors along the ltree path; the
// argument is a subquery of the location id.
func ancestorsOf(locationID string) string {
	return `SELECT a.id FROM location a WHERE a.path @> (SELECT l.path FROM location l WHERE l.id = ` + locationID + `)`
}

// noChangeSet fills the AUD-03 columns of an entry that is no CI change
// (entity_change, location_change, audit): change_kind, change_set_id,
// field_changes, source, collector_id, request_id, correlation_id,
// observed_at and actor_user_id.
const noChangeSet = `, NULL::text, NULL::text, NULL::jsonb, NULL::text, NULL::text, NULL::text, NULL::text, NULL::timestamptz, NULL::text`

// noChangeKind ends the query of an entry without a change kind: under a
// change_kind filter ($3 not empty) it drops out.
const noChangeKind = ` AND $3::text = ''`

// ciChangeSets is the ci_change part of the history of a CI (AUD-03, E-82
// option 2): one entry per change set, the field rows of one write, with
// change_kind and field_changes [{field, old, new}] in the order of the
// field names; a row without change set (written before 000167) is an entry
// of its own. The entry keeps the id of its first row; field_name, old_value
// and new_value are those of a change set of one row and empty for one of
// several. $3 keeps the change sets of one change_kind.
const ciChangeSets = `
	SELECT (array_agg(id::text ORDER BY field_name NULLS FIRST, id))[1], organization_id::text, 'ci', ci_id::text,
	       COALESCE(min(actor_id::text), ''),
	       CASE WHEN count(DISTINCT change_type) = 1 THEN min(change_type) ELSE 'update' END,
	       CASE WHEN count(*) = 1 THEN COALESCE(min(field_name), '') ELSE '' END,
	       CASE WHEN count(*) = 1 THEN (array_agg(old_value))[1] END,
	       CASE WHEN count(*) = 1 THEN (array_agg(new_value))[1] END,
	       COALESCE(min(comment), ''), max(created_at), 'ci_change', COALESCE(min(site_id::text), ''),
	       min(change_kind) AS change_kind, min(change_set_id::text),
	       jsonb_agg(jsonb_build_object('field', field_name, 'old', old_value, 'new', new_value) ORDER BY field_name, id)
	           FILTER (WHERE COALESCE(field_name, '') <> ''),
	       min(source), min(collector_id::text), min(request_id), min(correlation_id), max(observed_at), min(actor_user_id::text)
	FROM ci_change WHERE organization_id = $1 AND ci_id = $2::uuid AND ($3::text = '' OR change_kind = $3::text)
	GROUP BY organization_id, ci_id, COALESCE(change_set_id, id)`

// unifiedCIChanges is the change part of the history of a CI (CI-06): its
// ci_change change sets (with the site at the time, overrides included),
// the entity_change rows and the location_change rows of its location and
// the location's ancestors (a room or rack re-parented with the CI in it).
var unifiedCIChanges = ciChangeSets + `
	UNION ALL
	SELECT id::text, organization_id::text, entity_type, entity_id::text, COALESCE(actor_id::text, ''), change_type,
	       COALESCE(field_name, ''), old_value, new_value, COALESCE(comment, ''), created_at, 'entity_change', ''` + noChangeSet + `
	FROM entity_change WHERE organization_id = $1 AND entity_type = 'ci' AND entity_id = $2::uuid` + noChangeKind + `
	UNION ALL` + locationChanges("ci", ancestorsOf("(SELECT ci.location_id FROM ci WHERE ci.id = $2::uuid)"))

// unifiedCIAudit adds the audit entries of the CI to its history; only a
// caller with audit:read reads them (E-78).
const unifiedCIAudit = `
	UNION ALL
	SELECT id::text, organization_id::text, 'ci', resource_id::text, COALESCE(actor_id::text, ''), action,
	       '', NULL, changes, '', "timestamp", 'audit', ''` + noChangeSet + `
	FROM audit_log WHERE organization_id = $1 AND resource_type = 'ci' AND resource_id = $2::uuid` + noChangeKind

// unifiedLocationHistory is the history of a location (LOC-11): its
// entity_change rows and the location_change rows of the location itself,
// found by location_id also after its deletion (WP-411), and of its
// ancestors while it exists.
var unifiedLocationHistory = `
	SELECT id::text, organization_id::text, entity_type, entity_id::text, COALESCE(actor_id::text, ''), change_type,
	       COALESCE(field_name, ''), old_value, new_value, COALESCE(comment, ''), created_at, 'entity_change', ''` + noChangeSet + `
	FROM entity_change WHERE organization_id = $1 AND entity_type = 'location' AND entity_id = $2::uuid` + noChangeKind + `
	UNION ALL` + locationChanges("location", "SELECT $2::uuid UNION "+ancestorsOf("$2::uuid"))

// listCI returns the unified history of a CI the caller can see, newest
// first, with its audit entries when opts.IncludeAudit is set and only the
// change sets of opts.ChangeKind when that is set; a CI the caller cannot
// see (unknown, or of another client or site) is api.ErrNotFound.
func (r *PGRepository) listCI(ctx context.Context, orgID, ciID string, page api.PaginationParams, opts ListOptions) ([]Change, int, error) {
	unified := unifiedCIChanges
	if opts.IncludeAudit {
		unified += unifiedCIAudit
	}
	return r.listUnified(ctx, orgID, ciID, page, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)", unified, opts.ChangeKind, api.ErrNotFound)
}

// listLocation returns the unified history of a location, newest first. A
// location of which the caller neither sees the row nor a location_change
// (unknown, deleted without a move, or of another client or site) is
// api.ErrNotFound.
func (r *PGRepository) listLocation(ctx context.Context, orgID, locationID string, page api.PaginationParams) ([]Change, int, error) {
	return r.listUnified(ctx, orgID, locationID, page,
		"SELECT EXISTS (SELECT 1 FROM location WHERE id = $1::uuid) OR EXISTS (SELECT 1 FROM location_change WHERE location_id = $1::uuid)",
		unifiedLocationHistory, "", api.ErrNotFound)
}

// listUnified pages through a unified history under the caller's tenant
// scope once visibleSQL ($1: the entity id) confirms the entity; an entity
// the caller cannot see yields invisibleErr (nil: an empty history).
// changeKind is $3 of unified (empty: no filter).
func (r *PGRepository) listUnified(ctx context.Context, orgID, id string, page api.PaginationParams, visibleSQL, unified, changeKind string, invisibleErr error) ([]Change, int, error) {
	out := []Change{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, visibleSQL, id).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return invisibleErr
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ("+unified+") h", orgID, id, changeKind).Scan(&total); err != nil {
			return fmt.Errorf("count history: %w", err)
		}
		rows, err := tx.Query(ctx, "SELECT * FROM ("+unified+") h ORDER BY 11 DESC, 1 LIMIT $4 OFFSET $5", orgID, id, changeKind, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list history: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c := Change{}
			var oldRaw, newRaw, fieldsRaw []byte
			var kind, set, source, collector, request, correlation, actorUser *string
			if err := rows.Scan(&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID, &c.ChangeType, &c.FieldName,
				&oldRaw, &newRaw, &c.Comment, &c.CreatedAt, &c.Source, &c.SiteID,
				&kind, &set, &fieldsRaw, &source, &collector, &request, &correlation, &c.ObservedAt, &actorUser); err != nil {
				return err
			}
			if len(oldRaw) > 0 {
				_ = json.Unmarshal(oldRaw, &c.OldValue)
			}
			if len(newRaw) > 0 {
				_ = json.Unmarshal(newRaw, &c.NewValue)
			}
			if len(fieldsRaw) > 0 {
				if err := json.Unmarshal(fieldsRaw, &c.FieldChanges); err != nil {
					return fmt.Errorf("decode field changes: %w", err)
				}
			}
			c.ChangeKind, c.ChangeSetID, c.ChangeSource = deref(kind), deref(set), deref(source)
			c.CollectorID, c.RequestID, c.CorrelationID, c.ActorUserID = deref(collector), deref(request), deref(correlation), deref(actorUser)
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return reveal(ctx, tx, orgID, out)
	})
	return out, total, err
}

// List returns the changes of one entity, newest first; for a CI the
// unified history of CI-06 (its audit entries only with opts.IncludeAudit,
// one entry per change set, AUD-03), for a location that of LOC-11. Only CI
// changes have a change kind: under opts.ChangeKind the history of another
// entity type is empty.
func (r *PGRepository) List(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams, opts ListOptions) ([]Change, int, error) {
	if entityType == "ci" {
		return r.listCI(ctx, orgID, entityID, page, opts)
	}
	if opts.ChangeKind != "" {
		return []Change{}, 0, nil
	}
	if entityType == "location" {
		return r.listLocation(ctx, orgID, entityID, page)
	}
	var out []Change
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND "+visibleEntity,
			orgID, entityType, entityID).Scan(&total); err != nil {
			return fmt.Errorf("count entity changes: %w", err)
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND %s ORDER BY created_at DESC LIMIT $4 OFFSET $5",
			selectColumns, visibleEntity), orgID, entityType, entityID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list entity changes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return reveal(ctx, tx, orgID, out)
	})
	return out, total, err
}

// Visible reports whether the entity of a state snapshot (ci or asset)
// exists under the caller's tenant scope; an unknown one, one of another
// organization, client or site, and an id that is no UUID are not.
func (r *PGRepository) Visible(ctx context.Context, orgID, entityType, entityID string) (bool, error) {
	query, ok := visibleSQL[entityType]
	if !ok || !api.IsUUID(entityID) {
		return false, nil
	}
	var visible bool
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, entityID).Scan(&visible)
	})
	return visible, err
}

// ciTrail is the change trail of a CI for its replay (JOB-04, WP-516): the
// field rows of its ci_change change sets (AUD-03, the row of its creation
// included) and its entity_change rows up to $3, with the change kind, both
// under the caller's tenant scope as in its history (unifiedCIChanges).
const ciTrail = `
	SELECT id::text AS id, organization_id::text AS organization_id, 'ci' AS entity_type, ci_id::text AS entity_id,
	       COALESCE(actor_id::text, '') AS actor_id, change_type, COALESCE(field_name, '') AS field_name, old_value, new_value,
	       COALESCE(comment, '') AS comment, created_at, change_kind, ci_version
	FROM ci_change WHERE organization_id = $1 AND ci_id = $2::uuid AND created_at <= $3
	UNION ALL
	SELECT id::text, organization_id::text, entity_type, entity_id::text, COALESCE(actor_id::text, ''), change_type,
	       COALESCE(field_name, ''), old_value, new_value, COALESCE(comment, ''), created_at, '', NULL::bigint
	FROM entity_change WHERE organization_id = $1 AND entity_type = 'ci' AND entity_id = $2::uuid AND created_at <= $3`

// TrailUpTo returns the changes of an entity up to at, oldest first, for
// point-in-time replay, with the personal values the surrogates stand for,
// as its history shows them (SEC-05). For a CI it is its ci_change field
// rows and its entity_change rows, for another type its entity_change rows
// (an asset writes them since WP-516). The rows of one write share their
// time: the creation goes first, then the CI version orders them. An entity
// the caller cannot see has none.
func (r *PGRepository) TrailUpTo(ctx context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error) {
	out := []Change{}
	if !api.IsUUID(entityID) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		if entityType == "ci" {
			rows, err = tx.Query(ctx, `
				SELECT id, organization_id, entity_type, entity_id, actor_id, change_type, field_name, old_value, new_value,
				       comment, created_at, change_kind
				FROM (`+ciTrail+`) t
				WHERE EXISTS (SELECT 1 FROM ci WHERE ci.id = $2::uuid)
				ORDER BY created_at, change_type <> 'create', ci_version NULLS LAST, id`, orgID, entityID, at)
		} else {
			rows, err = tx.Query(ctx, fmt.Sprintf(
				"SELECT %s, '' FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND created_at <= $4 AND %s ORDER BY created_at, change_type <> 'create', id",
				selectColumns, visibleEntity), orgID, entityType, entityID, at)
		}
		if err != nil {
			return fmt.Errorf("list entity trail: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c := Change{}
			var oldRaw, newRaw []byte
			if err := rows.Scan(&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID, &c.ChangeType, &c.FieldName,
				&oldRaw, &newRaw, &c.Comment, &c.CreatedAt, &c.ChangeKind); err != nil {
				return err
			}
			if err := decodeValues(&c, oldRaw, newRaw); err != nil {
				return err
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return reveal(ctx, tx, orgID, out)
	})
	return out, err
}

// decodeValues decodes the stored old and new value of a trail row.
func decodeValues(c *Change, oldRaw, newRaw []byte) error {
	if len(oldRaw) > 0 {
		if err := json.Unmarshal(oldRaw, &c.OldValue); err != nil {
			return fmt.Errorf("decode old value: %w", err)
		}
	}
	if len(newRaw) > 0 {
		if err := json.Unmarshal(newRaw, &c.NewValue); err != nil {
			return fmt.Errorf("decode new value: %w", err)
		}
	}
	return nil
}

// reveal replaces the surrogate references in the old and new values and
// the field changes of changes with the personal values they stand for, as
// the CI change list and the audit log show them; a deleted one is null
// (SEC-05).
func reveal(ctx context.Context, tx pgx.Tx, orgID string, changes []Change) error {
	values := make([]any, 0, 2*len(changes))
	for i := range changes {
		values = append(values, changes[i].OldValue, changes[i].NewValue)
		for _, fc := range changes[i].FieldChanges {
			values = append(values, fc.Old, fc.New)
		}
	}
	revealed, err := pii.RevealTx(ctx, tx, orgID, values)
	if err != nil {
		return fmt.Errorf("resolve personal values: %w", err)
	}
	out, ok := revealed.([]any)
	if !ok || len(out) != len(values) {
		return fmt.Errorf("resolve personal values: %d of %d values", len(out), len(values))
	}
	n := 0
	for i := range changes {
		changes[i].OldValue, changes[i].NewValue = out[n], out[n+1]
		n += 2
		for j := range changes[i].FieldChanges {
			changes[i].FieldChanges[j].Old, changes[i].FieldChanges[j].New = out[n], out[n+1]
			n += 2
		}
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Change, error) {
	c := &Change{}
	var oldRaw, newRaw []byte
	if err := s.Scan(
		&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID,
		&c.ChangeType, &c.FieldName, &oldRaw, &newRaw, &c.Comment, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	if len(oldRaw) > 0 {
		_ = json.Unmarshal(oldRaw, &c.OldValue)
	}
	if len(newRaw) > 0 {
		_ = json.Unmarshal(newRaw, &c.NewValue)
	}
	return c, nil
}

func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(raw)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
