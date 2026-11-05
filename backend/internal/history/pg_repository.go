package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
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
// subqueries by client and site scope; the asset rows of entity_change
// carry the scope of their asset since WP-794, the rows of the other types
// none (WP-025). An asset is deleted for good, so the changes
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

// Record appends a change row to the entity_change trail. A CI or an asset
// must be visible under the transaction's tenant scope (as visibleEntity,
// WP-740 for assets).
func (r *PGRepository) Record(ctx context.Context, change *Change) error {
	return database.WithRequestTenant(ctx, r.pool, change.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if check, ok := visibleSQL[change.EntityType]; ok {
			var visible bool
			if err := tx.QueryRow(ctx, check, change.EntityID).Scan(&visible); err != nil {
				return fmt.Errorf("check %s visibility: %w", change.EntityType, err)
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
// caller with audit:read reads them (E-78). They are the entries about the
// CI under both forms of its type (CI-06, WP-681, F-410): the domain
// recorder writes "ci", the route audit the route segment "cis" (POST
// /cis/{id}/interfaces, /contacts, /lifecycle-transitions, /unmerge …).
// The route entries about its contact links and network interfaces name
// the link or the interface: an entry of such a row the CI still has, or
// whose values ({field, old, new}, WP-505) name the CI as ci_id (the
// delete of a link, the create, a move to another CI), is about the CI as
// well. visible is the scope rule of the audit list (audit.VisibleEntries,
// WP-660): a restricted principal reads only the entries the list shows
// it, so no entry of a deleted link or interface.
func unifiedCIAudit(visible string) string {
	return `
	UNION ALL
	SELECT id::text, organization_id::text, 'ci', resource_id::text, COALESCE(actor_id::text, ''), action,
	       '', NULL, changes, '', "timestamp", 'audit', ''` + noChangeSet + `
	FROM audit_log WHERE organization_id = $1 AND (
		(resource_type IN ('ci', 'cis') AND resource_id = $2::uuid)
		OR (resource_type = 'ci-contacts' AND resource_id IN (SELECT l.id FROM ci_contact l WHERE l.ci_id = $2::uuid))
		OR (resource_type = 'network-interfaces' AND resource_id IN (SELECT n.id FROM network_interface n WHERE n.ci_id = $2::uuid))
		OR (resource_type IN ('ci-contacts', 'network-interfaces') AND (
			changes->'values' @> jsonb_build_array(jsonb_build_object('field', 'ci_id', 'old', $2::uuid::text))
			OR changes->'values' @> jsonb_build_array(jsonb_build_object('field', 'ci_id', 'new', $2::uuid::text)))))
	  AND (` + visible + `)` + noChangeKind
}

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
		unified += unifiedCIAudit(audit.VisibleEntries(ctx))
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
		if out, err = scanUnified(rows); err != nil {
			return err
		}
		return reveal(ctx, tx, orgID, out)
	})
	return out, total, err
}

// scanUnified reads the entries of a unified history (the columns of
// ciChangeSets); it closes rows.
func scanUnified(rows pgx.Rows) ([]Change, error) {
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		c := Change{}
		var oldRaw, newRaw, fieldsRaw []byte
		var kind, set, source, collector, request, correlation, actorUser *string
		if err := rows.Scan(&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID, &c.ChangeType, &c.FieldName,
			&oldRaw, &newRaw, &c.Comment, &c.CreatedAt, &c.Source, &c.SiteID,
			&kind, &set, &fieldsRaw, &source, &collector, &request, &correlation, &c.ObservedAt, &actorUser); err != nil {
			return nil, err
		}
		if len(oldRaw) > 0 {
			_ = json.Unmarshal(oldRaw, &c.OldValue)
		}
		if len(newRaw) > 0 {
			_ = json.Unmarshal(newRaw, &c.NewValue)
		}
		if len(fieldsRaw) > 0 {
			if err := json.Unmarshal(fieldsRaw, &c.FieldChanges); err != nil {
				return nil, fmt.Errorf("decode field changes: %w", err)
			}
		}
		c.ChangeKind, c.ChangeSetID, c.ChangeSource = deref(kind), deref(set), deref(source)
		c.CollectorID, c.RequestID, c.CorrelationID, c.ActorUserID = deref(collector), deref(request), deref(correlation), deref(actorUser)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ciEdgeChanges is the relationship part of the timeline of a CI (WP-792):
// the rows of relationship_change whose source_ci_id or target_ci_id is
// the CI (indexes idx_relationship_change_source and _target), also of
// edges removed since, at their observed_at; the row level security of
// relationship_change hides a row with an endpoint outside the caller's
// scope. The values are read with relationship.ChangesTx afterwards.
const ciEdgeChanges = `
	UNION ALL
	SELECT c.id::text, c.organization_id::text, 'relationship', c.relationship_id::text, COALESCE(c.actor_id::text, ''),
	       c.change_kind, '', NULL::jsonb, NULL::jsonb, '', c.observed_at, '` + SourceRelationshipChange + `', ''` + noChangeSet + `
	FROM relationship_change c WHERE c.organization_id = $1 AND (c.source_ci_id = $2::uuid OR c.target_ci_id = $2::uuid)` + noChangeKind

// timelineColumns name the columns of a unified history for the window of
// the timeline; recorded_at is the time of recording of every part.
const timelineColumns = `id, organization_id, entity_type, entity_id, actor_id, change_type, field_name, old_value, new_value,
	comment, recorded_at, source, site_id, change_kind, change_set_id, field_changes, change_source, collector_id,
	request_id, correlation_id, observed_at, actor_user_id`

// timelineWindow keeps the entries recorded within [$4, $5] (NULL: open).
const timelineWindow = ` WHERE ($4::timestamptz IS NULL OR h.recorded_at >= $4::timestamptz)
	AND ($5::timestamptz IS NULL OR h.recorded_at <= $5::timestamptz)`

// CITimeline implements CITimeline under the caller's tenant scope.
func (r *PGRepository) CITimeline(ctx context.Context, orgID, ciID string, page api.PaginationParams, opts TimelineOptions) ([]Change, int, error) {
	if !api.IsUUID(ciID) {
		return nil, 0, api.ErrNotFound
	}
	unified := unifiedCIChanges
	if opts.IncludeAudit {
		unified += unifiedCIAudit(audit.VisibleEntries(ctx))
	}
	if opts.IncludeRelationships {
		unified += ciEdgeChanges
	}
	windowed := "SELECT * FROM (" + unified + ") AS h(" + timelineColumns + ")" + timelineWindow
	from, to := timeOrNil(opts.From), timeOrNil(opts.To)
	out := []Change{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(txCtx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(txCtx, visibleSQL["ci"], ciID).Scan(&visible); err != nil {
			return fmt.Errorf("check ci visibility: %w", err)
		}
		if !visible {
			return api.ErrNotFound
		}
		if err := tx.QueryRow(txCtx, "SELECT count(*) FROM ("+windowed+") w", orgID, ciID, "", from, to).Scan(&total); err != nil {
			return fmt.Errorf("count timeline: %w", err)
		}
		rows, err := tx.Query(txCtx, windowed+" ORDER BY h.recorded_at DESC, h.id LIMIT $6 OFFSET $7",
			orgID, ciID, "", from, to, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list timeline: %w", err)
		}
		if out, err = scanUnified(rows); err != nil {
			return err
		}
		if revealErr := reveal(txCtx, tx, orgID, out); revealErr != nil {
			return revealErr
		}
		return edgeEntries(txCtx, tx, orgID, out)
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// edgeEntries replaces the relationship entries of a timeline page with
// their rows of relationship_change (relationship.ChangesTx), projected by
// the relationship grant of the principal of ctx (relationship.ProjectChanges)
// and mapped as RelationshipEntries.
func edgeEntries(ctx context.Context, tx pgx.Tx, orgID string, entries []Change) error {
	var ids []string
	for i := range entries {
		if entries[i].Source == SourceRelationshipChange {
			ids = append(ids, entries[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := relationship.ChangesTx(ctx, tx, orgID, ids)
	if err != nil {
		return err
	}
	if rows, err = relationship.ProjectChanges(ctx, orgID, rows, nil); err != nil {
		return err
	}
	mapped := RelationshipEntries(rows)
	byID := make(map[string]*Change, len(mapped))
	for i := range mapped {
		mapped[i].OrganizationID = orgID
		byID[mapped[i].ID] = &mapped[i]
	}
	for i := range entries {
		if entries[i].Source != SourceRelationshipChange {
			continue
		}
		e, ok := byID[entries[i].ID]
		if !ok {
			return fmt.Errorf("relationship change %s vanished", entries[i].ID)
		}
		entries[i] = *e
	}
	return nil
}

// timeOrNil is t, nil for the zero time.
func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
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
	if entityType == "relationship" {
		changes, total, err := r.RelationshipChanges(ctx, orgID, entityID, page)
		if err != nil {
			return nil, 0, err
		}
		return RelationshipEntries(changes), total, nil
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

// RelationshipChanger reads the history of a relationship as rows of
// relationship_change, for the field permissions of the relationship
// grant (WP-296); PGRepository implements it.
type RelationshipChanger interface {
	RelationshipChanges(ctx context.Context, orgID, relationshipID string, page api.PaginationParams) ([]relationship.Change, int, error)
}

// RelationshipChanges returns the rows of relationship_change of a
// relationship under the caller's tenant scope, newest first, with the
// personal values revealed: the rows of GET
// /api/v1/relationships/{id}/changes (relationship.ListChangesTx). A
// relationship the caller sees neither as edge nor in its history has an
// empty history here.
func (r *PGRepository) RelationshipChanges(ctx context.Context, orgID, relationshipID string, page api.PaginationParams) ([]relationship.Change, int, error) {
	out := []relationship.Change{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		changes, n, err := relationship.ListChangesTx(ctx, tx, orgID, relationshipID, page)
		if errors.Is(err, api.ErrNotFound) {
			return nil
		}
		out, total = changes, n
		return err
	})
	return out, total, err
}

// RelationshipEntries maps rows of relationship_change to history entries:
// change_kind is change_type, before and after are old_value and
// new_value, observed_at is created_at and observed_at, the source of the
// change is change_source; change_kind and change_set_id (AUD-03) stay
// empty.
func RelationshipEntries(changes []relationship.Change) []Change {
	out := make([]Change, 0, len(changes))
	for i := range changes {
		c := &changes[i]
		observed := c.ObservedAt
		e := Change{ID: c.ID, EntityType: "relationship", EntityID: c.RelationshipID, ActorID: c.ActorID,
			ChangeType: c.ChangeKind, CreatedAt: observed, ObservedAt: &observed, Source: SourceRelationshipChange,
			ChangeSource: c.Source, RequestID: c.RequestID, CorrelationID: c.CorrelationID}
		if c.Before != nil {
			e.OldValue = c.Before
		}
		if c.After != nil {
			e.NewValue = c.After
		}
		out = append(out, e)
	}
	return out
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

// AssetFacts reads the type and scope of an asset the caller sees under
// its tenant scope (AssetLookup, WP-794); nil for an unknown, deleted or
// invisible one.
func (r *PGRepository) AssetFacts(ctx context.Context, orgID, assetID string) (*AssetFacts, error) {
	if !api.IsUUID(assetID) {
		return nil, nil
	}
	var facts *AssetFacts
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		f := AssetFacts{}
		err := tx.QueryRow(ctx, `SELECT COALESCE(asset_type_id::text, ''), COALESCE(client_id::text, ''), COALESCE(site_id::text, '')
			FROM asset WHERE id = $1`, assetID).Scan(&f.TypeID, &f.ClientID, &f.SiteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read asset facts: %w", err)
		}
		facts = &f
		return nil
	})
	return facts, err
}

// ciTrail is the change trail of a CI for its replay at $3 (JOB-04, WP-516,
// WP-312): the last ci_snapshot with taken_at <= $3 as its base, then the
// field rows of the ci_change change sets of the CI (AUD-03, the row of its
// creation included; after a merge found by origin_ci_id, so the survivor
// replays its own rows and the victim its rows before the merge) and its
// entity_change rows, each with taken_at < created_at <= $3; without a
// snapshot the whole trail up to $3. All under the caller's tenant scope as
// in its history (unifiedCIChanges). rank orders the rows of one time: the
// snapshot, then the creation, then the others.
const ciTrail = `
	WITH base AS (
		SELECT id, organization_id, taken_at, state, ci_version FROM ci_snapshot
		WHERE organization_id = $1 AND ci_id = $2::uuid AND taken_at <= $3
		ORDER BY taken_at DESC LIMIT 1)
	SELECT id::text AS id, organization_id::text AS organization_id, 'ci' AS entity_type, $2::uuid::text AS entity_id,
	       '' AS actor_id, 'snapshot' AS change_type, '' AS field_name, NULL::jsonb AS old_value, state AS new_value,
	       '' AS comment, taken_at AS created_at, '' AS change_kind, ci_version, '` + SourceCISnapshot + `' AS source, 0 AS rank
	FROM base
	UNION ALL
	SELECT id::text, organization_id::text, 'ci', $2::uuid::text,
	       COALESCE(actor_id::text, ''), change_type, COALESCE(field_name, ''), old_value, new_value,
	       COALESCE(comment, ''), created_at, change_kind, ci_version, '` + SourceCIChange + `', CASE WHEN change_type = 'create' THEN 1 ELSE 2 END
	FROM ci_change WHERE organization_id = $1 AND ((ci_id = $2::uuid AND origin_ci_id IS NULL) OR origin_ci_id = $2::uuid)
	  AND created_at <= $3 AND created_at > COALESCE((SELECT taken_at FROM base), '-infinity')
	UNION ALL
	SELECT id::text, organization_id::text, entity_type, entity_id::text, COALESCE(actor_id::text, ''), change_type,
	       COALESCE(field_name, ''), old_value, new_value, COALESCE(comment, ''), created_at, '', NULL::bigint,
	       '` + SourceEntityChange + `', CASE WHEN change_type = 'create' THEN 1 ELSE 2 END
	FROM entity_change WHERE organization_id = $1 AND entity_type = 'ci' AND entity_id = $2::uuid
	  AND created_at <= $3 AND created_at > COALESCE((SELECT taken_at FROM base), '-infinity')`

// TrailUpTo returns the changes of an entity up to at, oldest first, for
// point-in-time replay, with the personal values the surrogates stand for,
// as its history shows them (SEC-05). For a CI it is its last ci_snapshot
// up to at and the ci_change and entity_change rows after it (ciTrail),
// for another type its entity_change rows (an asset writes them since
// WP-516). The rows of one write share their time: the creation goes
// first, then the CI version orders them. An entity the caller cannot see
// has none.
func (r *PGRepository) TrailUpTo(ctx context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error) {
	out := []Change{}
	if !api.IsUUID(entityID) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if entityType == "ci" {
			var visible bool
			if err := tx.QueryRow(ctx, visibleSQL["ci"], entityID).Scan(&visible); err != nil {
				return fmt.Errorf("check ci visibility: %w", err)
			}
			if !visible {
				return nil
			}
		}
		var err error
		out, err = trailTx(ctx, tx, orgID, entityType, entityID, at, true)
		return err
	})
	return out, err
}

// trailTx reads the trail of an entity up to at in tx, oldest first: for a
// CI ciTrail, for another type its entity_change rows under visibleEntity.
// revealValues resolves the surrogates of the values (deleted ones are null).
func trailTx(ctx context.Context, tx pgx.Tx, orgID, entityType, entityID string, at time.Time, revealValues bool) ([]Change, error) {
	var out []Change
	if entityType == "ci" {
		trail, err := ciTrailTx(ctx, tx, orgID, entityID, at)
		if err != nil {
			return nil, err
		}
		out = trail
	} else {
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s, '' FROM entity_change WHERE organization_id = $1 AND entity_type = $2 AND entity_id = $3 AND created_at <= $4 AND %s ORDER BY created_at, change_type <> 'create', id",
			selectColumns, visibleEntity), orgID, entityType, entityID, at)
		if err != nil {
			return nil, fmt.Errorf("list entity trail: %w", err)
		}
		defer rows.Close()
		out = []Change{}
		for rows.Next() {
			c := Change{}
			var oldRaw, newRaw []byte
			if err := rows.Scan(&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID, &c.ChangeType, &c.FieldName,
				&oldRaw, &newRaw, &c.Comment, &c.CreatedAt, &c.ChangeKind); err != nil {
				return nil, err
			}
			if err := decodeValues(&c, oldRaw, newRaw); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if !revealValues {
		return out, nil
	}
	return out, reveal(ctx, tx, orgID, out)
}

// stateVisibleSQL is the visibility check of StateAt ($1: the entity id):
// the row under the caller's tenant scope, for a CI also a ci_snapshot, so
// a CI deleted for good (CI-05) stays readable until the retention.
var stateVisibleSQL = map[string]string{
	"ci":    "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid) OR EXISTS (SELECT 1 FROM ci_snapshot WHERE ci_id = $1::uuid)",
	"asset": visibleSQL["asset"],
}

// StateAt implements Repository: the state of the entity at req.At from
// its trail (trailTx) in one tenant transaction, for a CI with the
// ci_snapshot it started from and, with req.Relationships, its edges
// (edgesAt).
func (r *PGRepository) StateAt(ctx context.Context, orgID string, req *StateRequest) (*StateSnapshot, error) {
	check, ok := stateVisibleSQL[req.EntityType]
	if !ok || !api.IsUUID(req.EntityID) {
		return nil, api.ErrNotFound
	}
	var out *StateSnapshot
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, check, req.EntityID).Scan(&visible); err != nil {
			return fmt.Errorf("check %s visibility: %w", req.EntityType, err)
		}
		if !visible {
			return api.ErrNotFound
		}
		trail, err := trailTx(ctx, tx, orgID, req.EntityType, req.EntityID, req.At, req.Reveal)
		if err != nil {
			return err
		}
		if len(trail) == 0 {
			return &NoRecordedStateError{At: req.At}
		}
		s := Replay(trail)
		var base *Change
		if trail[0].Source == SourceCISnapshot {
			base = &trail[0]
			takenAt := base.CreatedAt
			s.BasedOnSnapshotAt = &takenAt
		}
		if req.Relationships && req.EntityType == "ci" {
			edges, eerr := edgesAt(ctx, tx, orgID, req.EntityID, base, req.At)
			if eerr != nil {
				return eerr
			}
			s.Relationships = &edges
		}
		out = &s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scopedSnapshotEdges reads the edges of a ci_snapshot ($2) whose stored
// endpoint scopes lie in the caller's scope, with the predicate of
// ci_relationship_isolation (000063, IMP-07): the weekly job reads
// organization-wide, so the snapshot may hold edges the caller does not
// see.
const scopedSnapshotEdges = `
	SELECT e.id::text, COALESCE(e.rel_type, ''), e.source_ci_id::text, e.target_ci_id::text
	FROM ci_snapshot s, jsonb_to_recordset(COALESCE(s.relationships, '[]'::jsonb))
		AS e(id uuid, rel_type text, source_ci_id uuid, target_ci_id uuid,
		     source_client_id uuid, source_site_id uuid, target_client_id uuid, target_site_id uuid)
	WHERE s.organization_id = $1 AND s.id = $2::uuid
	  AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
	       OR ((e.source_client_id IS NULL OR e.source_client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
	           AND (e.target_client_id IS NULL OR e.target_client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))))
	  AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
	       OR ((e.source_site_id IS NULL OR e.source_site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
	           AND (e.target_site_id IS NULL OR e.target_site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))))`

// edgesAt reconstructs the edges of the CI ciID at at (WP-793): the edges
// of the base snapshot in the caller's scope (scopedSnapshotEdges), each
// with the values of its last relationship_change row up to the snapshot,
// replayed with the rows of relationship_change whose source or target is
// the CI and with taken_at < observed_at <= at (ReplayEdges); without a
// snapshot every row up to at. The row level security of
// relationship_change hides a row with an endpoint outside the caller's
// scope; the values are revealed (relationship.ChangesTx) and projected by
// the relationship grant of the principal of ctx
// (relationship.ProjectChanges). An edge a merge or an unmerge rewired
// away from the CI is not part of the snapshot those write in the same
// transaction.
func edgesAt(ctx context.Context, tx pgx.Tx, orgID, ciID string, base *Change, at time.Time) ([]EdgeState, error) {
	var edges []EdgeState
	var since *time.Time
	if base != nil {
		since = &base.CreatedAt
		rows, err := tx.Query(ctx, scopedSnapshotEdges, orgID, base.ID)
		if err != nil {
			return nil, fmt.Errorf("read snapshot edges: %w", err)
		}
		edges, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (EdgeState, error) {
			var id, relType, source, target string
			scanErr := row.Scan(&id, &relType, &source, &target)
			return EdgeState{edgeID: id, edgeRelType: relType, edgeSourceCI: source, edgeTargetCI: target}, scanErr
		})
		if err != nil {
			return nil, fmt.Errorf("read snapshot edges: %w", err)
		}
		if verr := edgeValues(ctx, tx, orgID, edges, base.CreatedAt); verr != nil {
			return nil, verr
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM relationship_change
		WHERE organization_id = $1 AND (source_ci_id = $2::uuid OR target_ci_id = $2::uuid)
		  AND observed_at > COALESCE($3::timestamptz, '-infinity') AND observed_at <= $4`, orgID, ciID, since, at)
	if err != nil {
		return nil, fmt.Errorf("read edge changes: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read edge changes: %w", err)
	}
	changes, err := projectedEdgeChanges(ctx, tx, orgID, ids)
	if err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool {
		if !changes[i].ObservedAt.Equal(changes[j].ObservedAt) {
			return changes[i].ObservedAt.Before(changes[j].ObservedAt)
		}
		return changes[i].ID < changes[j].ID
	})
	replay := make([]EdgeChange, len(changes))
	for i := range changes {
		c := &changes[i]
		replay[i] = EdgeChange{RelationshipID: c.RelationshipID, Kind: c.ChangeKind, RelType: c.RelType,
			SourceCIID: c.SourceCIID, TargetCIID: c.TargetCIID, After: c.After}
	}
	return ReplayEdges(ciID, edges, replay), nil
}

// edgeValues fills the edges of a snapshot with the values of their last
// relationship_change row up to takenAt that recorded the edge after it;
// an edge without one keeps its id, type and endpoints.
func edgeValues(ctx context.Context, tx pgx.Tx, orgID string, edges []EdgeState, takenAt time.Time) error {
	if len(edges) == 0 {
		return nil
	}
	relIDs := make([]string, len(edges))
	for i, e := range edges {
		relIDs[i], _ = e[edgeID].(string)
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (relationship_id) id::text FROM relationship_change
		WHERE organization_id = $1 AND relationship_id = ANY ($2::uuid[]) AND observed_at <= $3 AND after IS NOT NULL
		ORDER BY relationship_id, observed_at DESC, id DESC`, orgID, relIDs, takenAt)
	if err != nil {
		return fmt.Errorf("read edge values: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read edge values: %w", err)
	}
	changes, err := projectedEdgeChanges(ctx, tx, orgID, ids)
	if err != nil {
		return err
	}
	byEdge := make(map[string]map[string]any, len(changes))
	for i := range changes {
		byEdge[changes[i].RelationshipID] = changes[i].After
	}
	for i, e := range edges {
		after := byEdge[relIDs[i]]
		if after == nil {
			continue
		}
		filled := EdgeState{}
		for k, v := range after {
			filled[k] = v
		}
		for _, k := range []string{edgeID, edgeRelType, edgeSourceCI, edgeTargetCI} {
			filled[k] = e[k]
		}
		edges[i] = filled
	}
	return nil
}

// projectedEdgeChanges reads the rows of relationship_change with the ids
// (relationship.ChangesTx, values revealed) projected by the relationship
// grant of the principal of ctx.
func projectedEdgeChanges(ctx context.Context, tx pgx.Tx, orgID string, ids []string) ([]relationship.Change, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	changes, err := relationship.ChangesTx(ctx, tx, orgID, ids)
	if err != nil {
		return nil, err
	}
	return relationship.ProjectChanges(ctx, orgID, changes, nil)
}

// ciTrailTx reads the trail of a CI up to at in tx (ciTrail), oldest first,
// with the stored values: the personal values stay surrogates.
func ciTrailTx(ctx context.Context, tx pgx.Tx, orgID, ciID string, at time.Time) ([]Change, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, organization_id, entity_type, entity_id, actor_id, change_type, field_name, old_value, new_value,
		       comment, created_at, change_kind, ci_version, source
		FROM (`+ciTrail+`) t
		ORDER BY created_at, rank, ci_version NULLS LAST, id`, orgID, ciID, at)
	if err != nil {
		return nil, fmt.Errorf("list ci trail: %w", err)
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		c := Change{}
		var oldRaw, newRaw []byte
		if err := rows.Scan(&c.ID, &c.OrganizationID, &c.EntityType, &c.EntityID, &c.ActorID, &c.ChangeType, &c.FieldName,
			&oldRaw, &newRaw, &c.Comment, &c.CreatedAt, &c.ChangeKind, &c.ciVersion, &c.Source); err != nil {
			return nil, err
		}
		if err := decodeValues(&c, oldRaw, newRaw); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
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
