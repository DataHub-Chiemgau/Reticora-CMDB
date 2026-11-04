package ci

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ciSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''),
	COALESCE(room_id::text, ''),
	COALESCE(location_id::text, ''),
	ci_type_id::text,
	name,
	status,
	COALESCE(manufacturer, ''),
	COALESCE(model, ''),
	COALESCE(serial_number, ''),
	COALESCE(hardware_uuid::text, ''),
	COALESCE(host(management_ip), ''),
	COALESCE(primary_mac::text, ''),
	COALESCE(hostname, ''),
	COALESCE(fqdn, ''),
	COALESCE(os_name, ''),
	COALESCE(os_version, ''),
	COALESCE(firmware_version, ''),
	COALESCE(sys_object_id, ''),
	attributes,
	COALESCE(discovery_source, ''),
	first_seen_at,
	last_seen_at,
	is_manual,
	created_at,
	updated_at,
	deleted_at,
	version,
	COALESCE(asset_id::text, ''),
	COALESCE(parent_ci_id::text, ''),
	COALESCE(lifecycle_state, ''),
	health,
	health_updated_at,
	schema_version,
	COALESCE(merged_into_id::text, ''),
	COALESCE((SELECT array_agg(t.key ORDER BY t.key) FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = ci.id), '{}')
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool     *pgxpool.Pool
	recorder audit.TxRecorder
	outbox   *events.Outbox
	// computed resolves the computed attribute definitions (WithComputed,
	// WP-717); nil computes nothing.
	computed FieldResolver
}

// WithEvents makes every CI mutation write its domain event into the
// outbox of its transaction (EVT-01, EVT-02).
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

// lastInTx writes the audit entries or events of a CI change: in a context
// transaction (the ingest, NFR-03) right before its commit, after every
// other write; else at once. Both take an organization-wide lock until the
// commit (audit chain, event outbox: migrations 000117, 000120), so the
// ingest lanes of an organization wait for each other only for these
// writes and the commit (WP-334). The writes keep their order.
func lastInTx(ctx context.Context, write func() error) error {
	if !database.InTx(ctx) {
		return write()
	}
	return database.BeforeCommit(ctx, write)
}

// snapshot copies item for a write that may run later: the caller may
// overwrite the CI it got back before the transaction commits.
func snapshot(item *Item) *Item {
	if item == nil {
		return nil
	}
	cp := *item
	return &cp
}

// snapshotChanges copies the CIs of an audit change set (see snapshot).
func snapshotChanges(changes map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(changes))
	for k, v := range changes {
		if item, ok := v.(*Item); ok {
			v = snapshot(item)
		}
		out[k] = v
	}
	return out
}

// recordEvent writes the domain event of a CI mutation in tx.
func (r *PGRepository) recordEvent(ctx context.Context, tx pgx.Tx, eventType string, item *Item, changedFields []string) error {
	if r.outbox == nil {
		return nil
	}
	item = snapshot(item)
	var data any = item
	if changedFields != nil {
		m, err := changeEventData(item, changedFields)
		if err != nil {
			return err
		}
		data = m
	}
	return r.appendEvent(ctx, tx, eventType, item, data)
}

// recordStatusEvent writes ci.status_changed after the ci.changed of an
// update whose changed fields contain status (EVT-03, WP-707): the data of
// ci.changed (CI snapshot with changed_fields) and the old and new status
// as from and to. Its entity_seq follows that of ci.changed.
func (r *PGRepository) recordStatusEvent(ctx context.Context, tx pgx.Tx, w *rowUpdate, changedFields []string) error {
	if r.outbox == nil || !slices.Contains(changedFields, "status") {
		return nil
	}
	item := snapshot(w.after)
	m, err := changeEventData(item, changedFields)
	if err != nil {
		return err
	}
	m["from"] = w.before.Status
	m["to"] = item.Status
	return r.appendEvent(ctx, tx, events.CIStatusChanged, item, m)
}

// changeEventData is the data of ci.changed: the CI snapshot item with the
// changed fields.
func changeEventData(item *Item, changedFields []string) (map[string]any, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("encode event data: %w", err)
	}
	m := map[string]any{}
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("encode event data: %w", err)
	}
	m["changed_fields"] = changedFields
	return m, nil
}

// appendEvent writes the event of CI item with data in tx (lastInTx).
func (r *PGRepository) appendEvent(ctx context.Context, tx pgx.Tx, eventType string, item *Item, data any) error {
	return lastInTx(ctx, func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: item.OrganizationID, Type: eventType, EntityID: item.ID, Data: data})
		return err
	})
}

// NewPGRepository creates a new PostgreSQL-backed CI repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return NewPGRepositoryWithAudit(pool, nil)
}

// NewPGRepositoryWithAudit creates a new PostgreSQL-backed CI repository with audit recording.
func NewPGRepositoryWithAudit(pool *pgxpool.Pool, recorder audit.TxRecorder) *PGRepository {
	return &PGRepository{pool: pool, recorder: recorder}
}

// withoutSort runs fn with sorting switched off for the planner (SET
// LOCAL). In a joined transaction (restore) it switches it back to the
// session's value afterwards; in a transaction of its own the setting ends
// with it, and the list's statements after the page (count, overrides,
// asset facts) do not sort. With a failed statement in fn the transaction
// is aborted and only fn's error counts.
func withoutSort(ctx context.Context, tx pgx.Tx, restore bool, fn func() error) error {
	if _, err := tx.Exec(ctx, "SET LOCAL enable_sort = off"); err != nil {
		return fmt.Errorf("list cis: %w", err)
	}
	ferr := fn()
	if !restore {
		return ferr
	}
	if _, err := tx.Exec(ctx, "SET LOCAL enable_sort TO DEFAULT"); err != nil && ferr == nil {
		return fmt.Errorf("list cis: %w", err)
	}
	return ferr
}

// typeScope returns the CI types the type filters of a list query name
// (namedTypes): per group the union of the subtrees of its types as the
// organization sees them (ci_type_subtree), the intersection of the
// groups. A value that is no UUID fails as the comparison with ci_type_id
// did before (22P02, 400).
func typeScope(ctx context.Context, tx pgx.Tx, groups [][]string) ([]string, error) {
	var ids []string
	var group []int32
	for g, values := range groups {
		for _, v := range values {
			ids = append(ids, v)
			group = append(group, int32(g)) //nolint:gosec // one group per type parameter
		}
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text FROM unnest($1::uuid[], $2::int[]) AS n(id, g), LATERAL ci_type_subtree(n.id) s
		GROUP BY s.id HAVING count(DISTINCT n.g) = $3 ORDER BY 1`, ids, group, len(groups))
	if err != nil {
		return nil, fmt.Errorf("ci type subtrees: %w", err)
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("ci type subtrees: %w", err)
	}
	return types, nil
}

// tagIDsOf returns the IDs of the organization's tags that the
// filter[tags] conditions name, by key (WP-754): one statement, none
// without a tag filter. Row level security restricts tag to the
// organization.
func tagIDsOf(ctx context.Context, tx pgx.Tx, filters []api.Filter) (map[string]string, error) {
	var keys []string
	for _, f := range filters {
		if f.Field == TagsField {
			keys = append(keys, tagFilterKeys(f)...)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT key, id::text FROM tag WHERE key = ANY($1::text[])`, keys)
	if err != nil {
		return nil, fmt.Errorf("tags of the filter: %w", err)
	}
	ids := map[string]string{}
	var key, id string
	if _, err = pgx.ForEachRow(rows, []any{&key, &id}, func() error {
		ids[key] = id
		return nil
	}); err != nil {
		return nil, fmt.Errorf("tags of the filter: %w", err)
	}
	return ids, nil
}

// attributeFilterOf checks that an attribute may be filtered (API-02) and
// returns its filter kind and the CI types the filter is restricted to (nil
// for none). The definitions of a CI's type decide (WP-566, F-216): a
// definition of the type, the inheritable definitions of its ancestors
// (MET-06, WP-708) and the global ones (of the organization and the
// system; row level security hides those of other organizations) apply to
// it, and the attribute is searchable for the type when one of them is.
// types are the CI types the query names (named) with their subtypes
// (typeScope), as the type filter includes them, else every type counts.
// The attribute must be defined for one of them (else 422) and searchable
// for one (else 400 with a hint); the searchable definitions give the
// kind, and when they compare differently (integer in one type, text in
// another) the filter is 400 with the hint to name the type. The hits are
// restricted to the types the attribute is searchable for (a type
// definition with inheritable covers the subtree of its type, WP-708)
// unless a searchable global definition covers every type. The
// restriction is the list of types of the definitions, so a filter on a
// type definition keeps the query shape whose predicate the index of the
// definition (partial on the type or on its subtree, WP-435, WP-708)
// proves in a custom plan, also when the named types are a part of it.
func attributeFilterOf(ctx context.Context, tx pgx.Tx, f api.Filter, key string, types []string, named bool) (kind string, restrict []string, err error) {
	const covers = `CASE WHEN d.ci_type_id IS NULL OR NOT d.searchable THEN '{}'::text[]
			WHEN d.inheritable THEN ARRAY(SELECT s.id::text FROM ci_type_subtree(d.ci_type_id) s)
			ELSE ARRAY[d.ci_type_id::text] END`
	query, args := `SELECT COALESCE(d.ci_type_id::text, ''), d.data_type, d.searchable, `+covers+`
		FROM attribute_definition d WHERE d.key = $1`, []any{key}
	if named {
		query += ` AND (d.ci_type_id IS NULL OR d.ci_type_id = ANY($2::uuid[])
			OR (d.inheritable AND d.ci_type_id IN (SELECT a.id FROM unnest($2::uuid[]) AS n(id), LATERAL ci_type_ancestors(n.id) a)))`
		args = append(args, types)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return "", nil, fmt.Errorf("attribute definition %s: %w", key, err)
	}
	var (
		typeID, dataType string
		searchable       bool
		defined, global  bool
		kinds, covered   []string
		subtree          []string
	)
	if _, err = pgx.ForEachRow(rows, []any{&typeID, &dataType, &searchable, &subtree}, func() error {
		defined = true
		if !searchable {
			return nil
		}
		if k := fieldmeta.FilterKind(dataType); !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
		if typeID == "" {
			global = true
		}
		for _, t := range subtree {
			if !slices.Contains(covered, t) {
				covered = append(covered, t)
			}
		}
		return nil
	}); err != nil {
		return "", nil, fmt.Errorf("attribute definition %s: %w", key, err)
	}
	field := "filter[" + f.Field + "]"
	switch {
	case !defined && named:
		return "", nil, &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("unknown attribute %q for the given ci_type_id", key)}
	case !defined:
		return "", nil, &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity, Message: fmt.Sprintf("unknown attribute %q", key)}
	case len(kinds) == 0:
		return "", nil, &api.FieldError{Field: field,
			Message: fmt.Sprintf("attribute %q is not searchable; mark it searchable in its attribute definition to filter on it", key)}
	case len(kinds) > 1:
		return "", nil, &api.FieldError{Field: field,
			Message: fmt.Sprintf("attribute %q has different data types in the CI types that make it searchable; give ci_type_id to filter on it", key)}
	}
	if global {
		return kinds[0], nil, nil
	}
	slices.Sort(covered)
	return kinds[0], covered, nil
}

// indexedSort are the sort columns whose order an index of the list holds
// for the CIs of an organization: idx_ci_list_sort (000156, WP-605) and
// idx_ci_list_name (WP-839). An unfiltered page in their order is read
// from the index without sorting.
var indexedSort = map[string]bool{"created_at": true, "name": true}

// List returns paginated CIs filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Item, int, error) {
	items := make([]Item, 0)
	var total int
	// joined: the list runs in the caller's transaction (database.ContextWithTx).
	joined := database.InTx(ctx)

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"deleted_at IS NULL"}
		args := make([]any, 0, 6)
		argPos := 1

		if filter.Status != "" {
			whereParts = append(whereParts, fmt.Sprintf("status = $%d", argPos))
			args = append(args, filter.Status)
			argPos++
		}
		// The type filters (ci_type_id, filter[ci_type_id] with exact or in)
		// include the subtypes (MET-06, WP-708). The subtrees are read
		// first and compared as a parameter: the planner estimates the
		// list of types from the statistics of ci_type_id and reads the
		// CIs of rare types through idx_ci_org_type, where it took a
		// semi-join with ci_type_subtree for a match of every CI and read
		// the whole organization in the sort order.
		groups, typesNamed := filter.namedTypes()
		var types []string
		if typesNamed {
			var serr error
			if types, serr = typeScope(ctx, tx, groups); serr != nil {
				return serr
			}
			whereParts = append(whereParts, fmt.Sprintf("ci_type_id = ANY($%d::uuid[])", argPos))
			args = append(args, types)
			argPos++
		}
		if filter.ClientID != "" {
			whereParts = append(whereParts, fmt.Sprintf("client_id = $%d", argPos))
			args = append(args, filter.ClientID)
			argPos++
		}
		if filter.SiteID != "" {
			whereParts = append(whereParts, fmt.Sprintf("site_id = $%d", argPos))
			args = append(args, filter.SiteID)
			argPos++
		}
		if filter.RoomID != "" {
			whereParts = append(whereParts, fmt.Sprintf("room_id = $%d", argPos))
			args = append(args, filter.RoomID)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("name ILIKE $%d", argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}
		tagIDs, terr := tagIDsOf(ctx, tx, filter.Filters)
		if terr != nil {
			return terr
		}
		orgNamed := false
		for _, f := range filter.Filters {
			if f.Field == "ci_type_id" && (f.Op == api.OpExact || f.Op == api.OpIn) {
				continue // in the type filter above
			}
			if f.Field == TagsField {
				clause, fargs := tagsPredicate(f, tagIDs, argPos)
				whereParts = append(whereParts, clause)
				args = append(args, fargs...)
				argPos += len(fargs)
				continue
			}
			expr := filterColumns[f.Field].expr
			if key, ok := f.Attribute(); ok {
				kind, restrict, err := attributeFilterOf(ctx, tx, f, key, types, typesNamed)
				if err != nil {
					return err
				}
				f.Kind = kind
				if cerr := f.Check(); cerr != nil {
					return cerr
				}
				// Only CIs whose type makes the attribute searchable
				// (WP-566). A parameter: with one type the custom plan
				// proves the predicate of the type definition's index.
				if restrict != nil {
					whereParts = append(whereParts, fmt.Sprintf("ci_type_id = ANY($%d::uuid[])", argPos))
					args = append(args, restrict)
					argPos++
				}
				// The expression of the attribute indexes (WP-435, E-73):
				// reticora_attr_text is leakproof, so the comparison is an
				// index condition under row level security, where ->> was
				// evaluated only after the policies of ci (F-114). The key
				// is a literal, as in the indexes (WP-607): with a parameter
				// a generic plan of the prepared statement matched no index.
				// The query names the organization as a value, which
				// ci_isolation (a setting) does not: it leads the index of
				// a key and is the predicate of a definition's own index.
				if !orgNamed {
					whereParts = append(whereParts, fmt.Sprintf("organization_id = $%d::uuid", argPos))
					args = append(args, orgID)
					argPos++
					orgNamed = true
				}
				expr = citype.KeyExpression(key)
			}
			if expr == "" {
				return &api.FieldError{Field: "filter[" + f.Field + "]", Status: http.StatusUnprocessableEntity, Message: "unknown filter field"}
			}
			clause, fargs := f.SQL(expr, argPos)
			whereParts = append(whereParts, clause)
			args = append(args, fargs...)
			argPos += len(fargs)
		}

		whereClause := strings.Join(whereParts, " AND ")
		sortColumn, sortDirection := NormalizeSort(filter)

		// Keyset pagination: when a cursor is supplied it replaces the OFFSET
		// so page boundaries stay stable while rows are inserted or removed.
		listWhere := whereClause
		listArgs := append([]any{}, args...)
		if page.Cursor != nil {
			if !page.Cursor.Matches(sortColumn, sortDirection) {
				return fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
			}
			listWhere += " AND " + api.KeysetClause(sortColumn, sortColumnCasts[sortColumn], sortDirection, argPos)
			listArgs = append(listArgs, page.Cursor.Value, page.Cursor.ID)
			argPos += 2
		}

		// Sort keys and limit (NFR-02, WP-605): the keys are qualified because
		// the select list names id::text "id", and ORDER BY id sorted by that
		// text, which no index delivers (the keyset clause compares the uuid).
		// The limit is a sub-select: the planner cannot estimate the client
		// predicate of ci_isolation and expects an org-wide principal to see
		// 0.5 % of the organization's CIs, so with LIMIT 50 known at 10,000
		// CIs it read and sorted the whole organization instead of the first
		// entries of idx_ci_list_sort. Without the value it assumes a page of
		// 10 % of the rows, as in the generic plan of a prepared statement.
		var listQuery string
		if page.Cursor != nil {
			listArgs = append(listArgs, page.Limit)
			listQuery = fmt.Sprintf(
				"SELECT %s FROM ci WHERE %s ORDER BY ci.%s %s, ci.id %s LIMIT (SELECT $%d::bigint)",
				ciSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			listQuery = fmt.Sprintf(
				"SELECT %s FROM ci WHERE %s ORDER BY ci.%s %s, ci.id %s LIMIT (SELECT $%d::bigint) OFFSET $%d",
				ciSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}

		readPage := func() error {
			rows, err := tx.Query(ctx, listQuery, listArgs...)
			if err != nil {
				return fmt.Errorf("list cis: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				item, err := scanCI(rows)
				if err != nil {
					return fmt.Errorf("scan ci: %w", err)
				}
				items = append(items, *item)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate cis: %w", err)
			}
			return nil
		}
		// The page of an unfiltered list in the default order is read from
		// idx_ci_list_sort in index order (NFR-02, WP-438, F-117). Once the
		// CIs carry a client and a site, the planner estimates the client and
		// site predicates of ci_isolation at 0.5 % each and expects one
		// visible CI; the limit then saves nothing in its estimate, and it
		// reads and sorts the whole organization whenever that is cheaper
		// than reading all of it through the index (with 10,000 CIs tens of
		// milliseconds instead of under one). Without sorting, the ordered
		// index scan is the only plan left; it reads offset + limit entries
		// (for a client- or site-scoped principal also the invisible ones in
		// between). Filters keep the planner's choice: a selective one is
		// cheaper through its own index and a sort.
		// The same holds for the order by name with idx_ci_list_name (WP-839,
		// E-17): the first page reads its entries from the index instead of
		// sorting the organization.
		var pageErr error
		if len(whereParts) == 1 && indexedSort[sortColumn] {
			pageErr = withoutSort(ctx, tx, joined, readPage)
		} else {
			pageErr = readPage()
		}
		if pageErr != nil {
			return pageErr
		}

		// total counts every matching CI. Without a cursor, a page that is
		// shorter than the limit and not past the end holds the last rows, so
		// it gives the count; otherwise the count reads the matching CIs once
		// more (without filters an index-only scan of idx_ci_list_sort can
		// serve it). A capped count (E-104, WP-839) stops after
		// api.TotalCountLimit matching CIs, or after the CI that follows the
		// page when that lies further: a total beyond api.TotalCap means
		// more, and offset + limit < total still tells whether more follow.
		if page.Cursor == nil && len(items) < page.Limit && (len(items) > 0 || page.Offset == 0) {
			total = page.Offset + len(items)
		} else {
			countSQL := "SELECT COUNT(*) FROM ci WHERE " + whereClause
			if filter.CappedTotal {
				countSQL = fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM ci WHERE %s LIMIT %d) AS capped",
					whereClause, max(api.TotalCountLimit, page.Offset+page.Limit+1))
			}
			if err := tx.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
				return fmt.Errorf("count cis: %w", err)
			}
		}

		ptrs := make([]*Item, len(items))
		for i := range items {
			ptrs[i] = &items[i]
		}
		if err := overlayOverrides(ctx, tx, ptrs); err != nil {
			return err
		}
		return loadAssetFacts(ctx, tx, ptrs)
	})

	return items, total, err
}

var (
	_ ActiveCounter = (*PGRepository)(nil)
	_ TxRunner      = (*PGRepository)(nil)
)

// RunInTx implements TxRunner (as discovery.PGRepository.RunInTx does): fn
// runs in one transaction of the context's tenant scope, which every
// repository call with fn's context joins. Service admits max_cis and
// writes in it (WP-835).
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

// CountActive implements ActiveCounter: the number of active CIs of the
// organization, also of the clients and sites outside the caller's scope
// (max_cis, F-403). ci_active_count() (migration 000186) widens only the
// client and site scope for the one count, in the caller's transaction when
// ctx carries one, and returns nothing but the number.
func (r *PGRepository) CountActive(ctx context.Context, orgID string) (int64, error) {
	var n int64
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT ci_active_count()`).Scan(&n); err != nil {
			return fmt.Errorf("count active cis: %w", err)
		}
		return nil
	})
	return n, err
}

// GetByID retrieves a single CI by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	var item *Item

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s, %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL",
			ciSelectColumns, ciTemplatesColumn)
		var err error
		item, err = scanCIWithTemplates(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get ci by id: %w", err)
		}
		if err := overlayOverrides(ctx, tx, []*Item{item}); err != nil {
			return err
		}
		return loadAssetFacts(ctx, tx, []*Item{item})
	})
	if err != nil {
		return nil, err
	}

	return item, nil
}

// GetByIDs implements BatchReader: the visible, not deleted CIs among ids
// in one query.
func (r *PGRepository) GetByIDs(ctx context.Context, orgID string, ids []string) ([]Item, error) {
	items := []Item{}
	if len(ids) == 0 {
		return items, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM ci WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL",
			ciSelectColumns), orgID, ids)
		if err != nil {
			return fmt.Errorf("get cis by id: %w", err)
		}
		var ptrs []*Item
		for rows.Next() {
			item, serr := scanCI(rows)
			if serr != nil {
				rows.Close()
				return serr
			}
			ptrs = append(ptrs, item)
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return rerr
		}
		if oerr := overlayOverrides(ctx, tx, ptrs); oerr != nil {
			return oerr
		}
		if aerr := loadAssetFacts(ctx, tx, ptrs); aerr != nil {
			return aerr
		}
		for _, item := range ptrs {
			items = append(items, *item)
		}
		return nil
	})
	return items, err
}

// nodeOverrideFields are the override columns among the fields of a Node.
var nodeOverrideFields = []string{"name", "status", "management_ip"}

// nodeOrgReadAbove: a projection of more CIs reads the nodes of the whole
// organization and keeps the requested ones. The read by ids costs about
// 8 µs per id, the read of the organization about 2 µs per CI of it, so the
// break-even lies near a quarter of the organization: about 2,500 ids at
// the reference size of NFR-10 (10,000 objects) and 10,000 ids at the
// headroom of NFR-01 (50,000 objects; WP-839, E-17). The size of the
// organization is not known without one more statement per request, so
// the threshold is fixed where the larger of the two detours is smallest
// for both sizes (measured as the application role, ids at random, best
// of five: at 10,000 CIs 5,000 ids by id 44 ms instead of 23 ms; at 50,000
// CIs 5,001 ids from the organization 84 ms instead of 49 ms; with 2,000
// the detour at 50,000 CIs was 83 ms instead of 21 ms). A traversal that
// reached so many CIs reads all edges of the organization as well.
const nodeOrgReadAbove = 5000

// nodeQuery reads the node projection; %s narrows the CIs. The overrides
// of the projected fields are a second statement (nodeOverrideQuery),
// applied in Go (NFR-02, WP-611): the planner cannot estimate the client
// and site predicates of ci_isolation and expects one visible CI once the
// CIs carry a client and a site, so with the overrides of the organization
// aggregated in a joined subquery it ran a nested loop that aggregated all
// of them again for every CI (10,000 CIs with 500 overrides: 5.5 s instead
// of 25 ms, WP-438, F-117), and the lateral read per CI that replaced it
// still looked the overrides up once per CI.
const nodeQuery = `SELECT c.id::text, c.name, c.ci_type_id::text, c.status, COALESCE(c.client_id::text, ''),
		COALESCE(c.site_id::text, ''), COALESCE(host(c.management_ip), '')
	FROM ci c
	WHERE c.organization_id = $1 AND %s AND c.deleted_at IS NULL`

// nodeOverrideQuery reads the overrides of the projected fields ($2); %s
// narrows the CIs. The policy of ci_field_value shows the overrides of the
// CIs the scope shows.
const nodeOverrideQuery = `SELECT ci_id::text, field_name, override_value FROM ci_field_value
	WHERE organization_id = $1 AND field_name = ANY($2::text[]) AND override_at IS NOT NULL AND %s`

var (
	nodesByID          = fmt.Sprintf(nodeQuery, "c.id = ANY($2::uuid[])")
	nodesByOrg         = fmt.Sprintf(nodeQuery, "true")
	nodeOverridesByID  = fmt.Sprintf(nodeOverrideQuery, "ci_id = ANY($3::uuid[])")
	nodeOverridesByOrg = fmt.Sprintf(nodeOverrideQuery, "true")
)

// ListNodes implements NodeReader: the projection of the visible, not
// deleted CIs among ids with their overrides, one statement each, without
// attributes and asset facts.
func (r *PGRepository) ListNodes(ctx context.Context, orgID string, ids []string) ([]Node, error) {
	nodes := []Node{}
	if len(ids) == 0 {
		return nodes, nil
	}
	nodeSQL, nodeArgs := nodesByID, []any{orgID, ids}
	overrideSQL, overrideArgs := nodeOverridesByID, []any{orgID, nodeOverrideFields, ids}
	var wanted map[string]bool
	if len(ids) > nodeOrgReadAbove {
		nodeSQL, nodeArgs = nodesByOrg, nodeArgs[:1]
		overrideSQL, overrideArgs = nodeOverridesByOrg, overrideArgs[:2]
		wanted = make(map[string]bool, len(ids))
		for _, id := range ids {
			wanted[id] = true
		}
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, nodeSQL, nodeArgs...)
		if err != nil {
			return fmt.Errorf("list ci nodes: %w", err)
		}
		index := map[string]int{}
		for rows.Next() {
			var n Node
			if err = rows.Scan(&n.ID, &n.Name, &n.CITypeID, &n.Status, &n.ClientID, &n.SiteID, &n.ManagementIP); err != nil {
				rows.Close()
				return fmt.Errorf("scan ci node: %w", err)
			}
			if wanted != nil && !wanted[n.ID] {
				continue
			}
			index[n.ID] = len(nodes)
			nodes = append(nodes, n)
		}
		rows.Close()
		if err = rows.Err(); err != nil || len(nodes) == 0 {
			return err
		}
		rows, err = tx.Query(ctx, overrideSQL, overrideArgs...)
		if err != nil {
			return fmt.Errorf("read node overrides: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, field string
			var value any
			if err = rows.Scan(&id, &field, &value); err != nil {
				return fmt.Errorf("scan node override: %w", err)
			}
			if i, ok := index[id]; ok {
				applyNodeOverride(&nodes[i], field, value)
			}
		}
		return rows.Err()
	})
	return nodes, err
}

// applyNodeOverride sets an overridden field of a node like applyOverride
// on an item, so both reads return the same effective value.
func applyNodeOverride(n *Node, field string, value any) {
	text := ""
	if value != nil {
		text = fmt.Sprint(value)
	}
	switch field {
	case "name":
		n.Name = text
	case "status":
		n.Status = text
	case "management_ip":
		n.ManagementIP = text
	}
}

// Create inserts a new CI. A deactivated type writes nothing and is
// ErrTypeInactive (MET-05): the insert reads the flag itself, so items
// without ChosenType (discovery, agent) cost no statement more.
func (r *PGRepository) Create(ctx context.Context, item *Item) error {
	err := database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := takeLegacyTags(item); err != nil {
			return err
		}
		if item.Attributes == nil {
			item.Attributes = make(map[string]any)
		}

		query := `
			INSERT INTO ci (
				organization_id,
				client_id,
				location_id,
				ci_type_id,
				name,
				status,
				manufacturer,
				model,
				serial_number,
				hardware_uuid,
				management_ip,
				primary_mac,
				hostname,
				fqdn,
				os_name,
				os_version,
				firmware_version,
				sys_object_id,
				attributes,
				discovery_source,
				first_seen_at,
				last_seen_at,
				is_manual,
				asset_id,
				parent_ci_id
			)
			SELECT
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $17, $18, $19, $20,
				$21, $22, $23, $24, $25
			WHERE NOT EXISTS (SELECT 1 FROM ci_type WHERE id = $4 AND NOT is_active)
			RETURNING id::text, COALESCE(site_id::text, ''), COALESCE(room_id::text, ''),
				COALESCE(location_id::text, ''), health, version, schema_version, created_at, updated_at
		`

		if err := checkCITypes(ctx, tx, item.OrganizationID, []*Item{item}); err != nil {
			return err
		}
		if err := checkParents(ctx, tx, item.OrganizationID, []*Item{item}); err != nil {
			return err
		}
		// The computed attributes take their values before the row is
		// written (WP-717).
		computed, env, computeErr := r.computedOfNew(ctx, tx, item.OrganizationID, []*Item{item})
		if computeErr != nil {
			return computeErr
		}

		var createdAt time.Time
		var updatedAt time.Time
		if err := tx.QueryRow(ctx, query,
			item.OrganizationID,
			nilIfEmpty(item.ClientID),
			nilIfEmpty(item.LocationID),
			item.CITypeID,
			item.Name,
			item.Status,
			nilIfEmpty(item.Manufacturer),
			nilIfEmpty(item.Model),
			nilIfEmpty(item.SerialNumber),
			nilIfEmpty(item.HardwareUUID),
			nilIfEmpty(item.ManagementIP),
			nilIfEmpty(item.PrimaryMAC),
			nilIfEmpty(item.Hostname),
			nilIfEmpty(item.FQDN),
			nilIfEmpty(item.OSName),
			nilIfEmpty(item.OSVersion),
			nilIfEmpty(item.FirmwareVersion),
			nilIfEmpty(item.SysObjectID),
			storedAttributes(item.Attributes, nil),
			nilIfEmpty(item.DiscoverySource),
			item.FirstSeenAt,
			item.LastSeenAt,
			item.IsManual,
			nilIfEmpty(item.AssetID),
			nilIfEmpty(item.ParentCIID),
		).Scan(&item.ID, &item.SiteID, &item.RoomID, &item.LocationID, &item.Health, &item.Version, &item.SchemaVersion, &createdAt, &updatedAt); err != nil {
			// No row: the type is deactivated (MET-05).
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTypeInactive
			}
			return locationError(fmt.Errorf("create ci: %w", err))
		}

		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if len(computed) > 0 {
			if err := writeComputedProvenance(ctx, tx, item.OrganizationID, env.now,
				computedValuesOf(item.ID, nonEmpty(computed[0]))); err != nil {
				return err
			}
		}
		if err := loadAssetFacts(ctx, tx, []*Item{item}); err != nil {
			return err
		}
		if err := writeCreatedTags(ctx, tx, item.OrganizationID, []*Item{item}); err != nil {
			return err
		}
		// The templates of the creation (WP-710), checked under RLS in the
		// writing transaction.
		var assigned *templatesChange
		if len(item.Templates) > 0 {
			resolved, rerr := citype.ResolveTemplateRefsTx(ctx, tx, item.CITypeID, item.Templates)
			if rerr != nil {
				return templateViolation(rerr)
			}
			if werr := writeTemplatesTx(ctx, tx, item.OrganizationID, item.ID, resolved, false); werr != nil {
				return werr
			}
			item.Templates = resolved
			assigned = &templatesChange{before: []citype.CITemplate{}, after: resolved}
		}
		if err := r.recordCIChange(ctx, tx, newChangeSet(ChangeKindCreated), item.OrganizationID, item.ID, item.Version, "create", "", nil, item); err != nil {
			return err
		}
		if err := r.recordTemplatesHistory(ctx, tx, item, assigned); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.created", item, map[string]interface{}{"after": item}); err != nil {
			return err
		}
		if err := r.recordTemplatesAudit(ctx, tx, item, assigned); err != nil {
			return err
		}
		if err := r.recordEvent(ctx, tx, events.CICreated, item, nil); err != nil {
			return err
		}
		return r.recordTemplatesEvent(ctx, tx, item, assigned)
	})
	return r.uniqueField(ctx, item.OrganizationID, err)
}

// CreateMany implements BatchCreator: the CIs of one organization with one
// statement each for the rows, their history, their audit entries and their
// events, whatever the number of items (NFR-03, WP-333). Each CI is written
// as Create writes it. A CI a unique index refuses is skipped (ON CONFLICT DO
// NOTHING) and reported, so the caller can name its record and process it on
// its own; under row level security an error could not name it. The rows are
// inserted in the order of serial number, hardware UUID and primary MAC, so
// concurrent batches with the same identities wait for each other instead of
// deadlocking on the unique indexes. A CI of a deactivated type is never
// inserted: the statement flags it, CreateMany returns ErrTypeInactive and
// the transaction rolls back (MET-05); it is never reported as refused,
// which the discovery reads as a unique index refusal.
func (r *PGRepository) CreateMany(ctx context.Context, orgID string, items []*Item) (CreateManyResult, error) {
	var res CreateManyResult
	if len(items) == 0 {
		return res, nil
	}
	for _, item := range items {
		if item.OrganizationID != orgID {
			return res, fmt.Errorf("create cis: item of organization %q in a batch of %q", item.OrganizationID, orgID)
		}
		if err := takeLegacyTags(item); err != nil {
			return res, err
		}
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkCITypes(ctx, tx, orgID, items); err != nil {
			return err
		}
		if err := checkParents(ctx, tx, orgID, items); err != nil {
			return err
		}
		// The computed attributes take their values before the rows are
		// written (WP-717); the caller records their provenance with that
		// of the batch (CreateManyResult.Computed).
		computed, env, computeErr := r.computedOfNew(ctx, tx, orgID, items)
		if computeErr != nil {
			return computeErr
		}
		if env != nil {
			res.Computed, res.ComputedAt = computed, env.now
		}
		created, refused, err := insertCIs(ctx, tx, orgID, items)
		if errors.Is(err, ErrTypeInactive) {
			return err
		}
		if err != nil {
			return locationError(fmt.Errorf("create cis: %w", err))
		}
		res.Refused = refused
		if len(created) == 0 {
			return nil
		}
		if err := loadAssetFacts(ctx, tx, created); err != nil {
			return err
		}
		if err := writeCreatedTags(ctx, tx, orgID, created); err != nil {
			return err
		}
		if err := r.recordCreates(ctx, tx, orgID, created); err != nil {
			return err
		}
		if err := r.recordCreatedAudit(ctx, tx, created); err != nil {
			return err
		}
		return r.recordCreatedEvents(ctx, tx, created)
	})
	return res, err
}

// insertCIs inserts the CI rows of items with one statement and returns the
// created items and the positions of the refused ones; an item of a
// deactivated type is ErrTypeInactive (MET-05).
func insertCIs(ctx context.Context, tx pgx.Tx, orgID string, items []*Item) (created []*Item, refused []int, err error) {
	n := len(items)
	text := func(value string) *string {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return &value
	}
	cols := make([][]*string, 21)
	for c := range cols {
		cols[c] = make([]*string, n)
	}
	firstSeen, lastSeen, manual := make([]*time.Time, n), make([]*time.Time, n), make([]bool, n)
	for i, item := range items {
		if item.Attributes == nil {
			item.Attributes = make(map[string]any)
		}
		attributes, merr := json.Marshal(storedAttributes(item.Attributes, nil))
		if merr != nil {
			return nil, nil, fmt.Errorf("encode attributes: %w", merr)
		}
		typeID, name, status, attrs := item.CITypeID, item.Name, item.Status, string(attributes)
		for c, v := range []*string{
			text(item.ClientID), text(item.LocationID), &typeID, &name, &status, text(item.Manufacturer), text(item.Model),
			text(item.SerialNumber), text(item.HardwareUUID), text(item.ManagementIP), text(item.PrimaryMAC),
			text(item.Hostname), text(item.FQDN), text(item.OSName), text(item.OSVersion), text(item.FirmwareVersion),
			text(item.SysObjectID), &attrs, text(item.DiscoverySource), text(item.AssetID), text(item.ParentCIID),
		} {
			cols[c][i] = v
		}
		firstSeen[i], lastSeen[i], manual[i] = item.FirstSeenAt, item.LastSeenAt, item.IsManual
	}
	args := []any{orgID}
	for _, col := range cols {
		args = append(args, col)
	}
	args = append(args, firstSeen, lastSeen, manual)
	rows, err := tx.Query(ctx, `
		WITH input AS MATERIALIZED (
			SELECT gen_random_uuid() AS id, r.*,
				EXISTS (SELECT 1 FROM ci_type t WHERE t.id = r.ci_type_id::uuid AND NOT t.is_active) AS type_inactive
			FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[],
			            $10::text[], $11::text[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[],
			            $17::text[], $18::text[], $19::text[], $20::text[], $21::text[], $22::text[],
			            $23::timestamptz[], $24::timestamptz[], $25::boolean[])
			     WITH ORDINALITY AS r(client_id, location_id, ci_type_id, name, status, manufacturer, model,
			          serial_number, hardware_uuid, management_ip, primary_mac, hostname, fqdn, os_name, os_version,
			          firmware_version, sys_object_id, attributes, discovery_source, asset_id, parent_ci_id,
			          first_seen_at, last_seen_at, is_manual, ord)
		), ins AS (
			INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status, manufacturer,
				model, serial_number, hardware_uuid, management_ip, primary_mac, hostname, fqdn, os_name, os_version,
				firmware_version, sys_object_id, attributes, discovery_source, first_seen_at, last_seen_at,
				is_manual, asset_id, parent_ci_id)
			SELECT id, $1, client_id::uuid, location_id::uuid, ci_type_id::uuid, name, status, manufacturer, model,
				serial_number, hardware_uuid::uuid, management_ip::inet, primary_mac::macaddr, hostname, fqdn,
				os_name, os_version, firmware_version, sys_object_id, attributes::jsonb, discovery_source,
				first_seen_at, last_seen_at, is_manual, asset_id::uuid, parent_ci_id::uuid
			FROM input
			WHERE NOT type_inactive
			ORDER BY serial_number, hardware_uuid, primary_mac, ord
			ON CONFLICT DO NOTHING
			RETURNING id, COALESCE(site_id::text, '') AS site_id, COALESCE(room_id::text, '') AS room_id,
				COALESCE(location_id::text, '') AS location_id, health, version, schema_version, created_at, updated_at
		)
		SELECT input.ord, COALESCE(ins.id::text, ''), COALESCE(ins.site_id, ''), COALESCE(ins.room_id, ''),
			COALESCE(ins.location_id, ''), COALESCE(ins.health, ''), COALESCE(ins.version, 0),
			COALESCE(ins.schema_version, 0), ins.created_at, ins.updated_at, input.type_inactive
		FROM input LEFT JOIN ins ON ins.id = input.id
		ORDER BY input.ord`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	inactive := false
	for rows.Next() {
		var ord int64
		var id, siteID, roomID, locationID, health string
		var version, schemaVersion int64
		var createdAt, updatedAt *time.Time
		var typeInactive bool
		if err := rows.Scan(&ord, &id, &siteID, &roomID, &locationID, &health, &version, &schemaVersion,
			&createdAt, &updatedAt, &typeInactive); err != nil {
			return nil, nil, err
		}
		i := int(ord) - 1
		if typeInactive {
			inactive = true
			continue
		}
		if id == "" {
			refused = append(refused, i)
			continue
		}
		item := items[i]
		item.ID, item.SiteID, item.RoomID, item.LocationID, item.Health = id, siteID, roomID, locationID, health
		item.Version, item.SchemaVersion = version, int(schemaVersion)
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		created = append(created, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if inactive {
		return nil, nil, ErrTypeInactive
	}
	return created, refused, nil
}

// recordCreates writes the history entry of each new CI, as Create does,
// with one statement; each is a change set of its own (AUD-03).
func (r *PGRepository) recordCreates(ctx context.Context, tx pgx.Tx, orgID string, items []*Item) error {
	ids, values, versions := make([]string, len(items)), make([]string, len(items)), make([]int64, len(items))
	sets := make([]string, len(items))
	for i, item := range items {
		sealed, err := sealChangeValue(ctx, tx, orgID, "", item)
		if err != nil {
			return err
		}
		if values[i], err = marshalJSONValue(sealed); err != nil {
			return fmt.Errorf("marshal new ci change value: %w", err)
		}
		ids[i], versions[i], sets[i] = item.ID, item.Version, newChangeSet(ChangeKindCreated).id
	}
	who := changeOrigin(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO ci_change (organization_id, ci_id, actor_id, change_type, field_name, old_value, new_value, ci_version,
			change_kind, change_set_id, actor_user_id, source, request_id, correlation_id)
		SELECT $1, c.ci_id::uuid, $2::uuid, 'create', NULL, 'null'::jsonb, c.new_value::jsonb, c.ci_version,
			$7, c.change_set_id::uuid, $8::uuid, $9, $10, $11
		FROM unnest($3::text[], $4::text[], $5::bigint[], $6::text[]) WITH ORDINALITY AS c(ci_id, new_value, ci_version, change_set_id, ord)
		ORDER BY c.ord`, orgID, who.actorID, ids, values, versions, sets, ChangeKindCreated,
		who.actorUserID, who.source, who.requestID, who.correlationID); err != nil {
		return fmt.Errorf("record ci changes: %w", err)
	}
	return nil
}

// recordCreatedAudit writes the ci.created audit entries of the new CIs in
// their order, with one statement where the recorder can.
func (r *PGRepository) recordCreatedAudit(ctx context.Context, tx pgx.Tx, items []*Item) error {
	if r.recorder == nil {
		return nil
	}
	batch, ok := r.recorder.(audit.TxBatchRecorder)
	if !ok {
		for _, item := range items {
			if err := r.recordAudit(ctx, tx, "ci.created", item, map[string]interface{}{"after": item}); err != nil {
				return err
			}
		}
		return nil
	}
	t := tenant.FromContext(ctx)
	actorType := "system"
	if strings.TrimSpace(t.UserID) != "" {
		actorType = "user"
	}
	entries := make([]audit.Entry, len(items))
	for i, item := range items {
		entries[i] = audit.Entry{
			OrganizationID: item.OrganizationID,
			ActorID:        strings.TrimSpace(t.UserID),
			ActorType:      actorType,
			Action:         "ci.created",
			ResourceType:   "ci",
			ResourceID:     item.ID,
			Changes:        map[string]interface{}{"after": snapshot(item)},
		}
	}
	return lastInTx(ctx, func() error {
		if _, err := batch.RecordMany(ctx, tx, entries); err != nil {
			return fmt.Errorf("record audit: %w", err)
		}
		return nil
	})
}

// recordCreatedEvents writes the ci.created events of the new CIs in their
// order with one statement.
func (r *PGRepository) recordCreatedEvents(ctx context.Context, tx pgx.Tx, items []*Item) error {
	if r.outbox == nil {
		return nil
	}
	es := make([]*events.Event, len(items))
	for i, item := range items {
		es[i] = &events.Event{OrgID: item.OrganizationID, Type: events.CICreated, EntityID: item.ID, Data: snapshot(item)}
	}
	return lastInTx(ctx, func() error {
		_, err := r.outbox.AppendMany(ctx, tx, es)
		return err
	})
}

// Update modifies an existing CI.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) { //nolint:gocritic // signature of Repository
	var item *Item

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		w, err := r.updateRow(ctx, tx, orgID, id, &req)
		if err != nil {
			return err
		}
		item = w.after
		return r.recordUpdate(ctx, tx, orgID, id, w)
	})

	return item, r.uniqueField(ctx, orgID, err)
}

// recordUpdate writes history, audit entry and events of the row update w
// of CI id in tx (AUD-03, K20).
func (r *PGRepository) recordUpdate(ctx context.Context, tx pgx.Tx, orgID, id string, w *rowUpdate) error {
	item := w.after
	if len(w.changes) == 0 {
		// A replaced template list alone (PUT /cis/{id}/templates).
		return r.recordTemplatesChange(ctx, tx, item, w.templates)
	}
	set := newChangeSet(w.kind())
	if err := r.recordFieldChanges(ctx, tx, set, orgID, id, item.Version, w.changes); err != nil {
		return err
	}
	if err := r.recordTemplatesHistory(ctx, tx, item, w.templates); err != nil {
		return err
	}
	if err := r.recordUpdateAudit(ctx, tx, w); err != nil {
		return err
	}
	if err := r.recordTemplatesAudit(ctx, tx, item, w.templates); err != nil {
		return err
	}
	changed := changedFieldNames(w.changes)
	if err := r.recordEvent(ctx, tx, events.CIChanged, item, changed); err != nil {
		return err
	}
	// A status change is also its own event (EVT-03, WP-707).
	if err := r.recordStatusEvent(ctx, tx, w, changed); err != nil {
		return err
	}
	return r.recordTemplatesEvent(ctx, tx, item, w.templates)
}

// rowUpdate is the UPDATE of a CI row: the row before and after it, the
// historized fields it changed (diffCI) and the replaced template list
// (nil when it stayed, WP-710).
type rowUpdate struct {
	before, after *Item
	changes       []ciFieldChange
	templates     *templatesChange
}

// templatesChange is a change of the template list of a CI (WP-710).
type templatesChange struct {
	before, after []citype.CITemplate
}

// ciTemplatesColumn is the template list of the CI row ci in position order
// (ci_template with the template's key, WP-710) as a JSON array: a column
// after ciSelectColumns that scanCIWithTemplates reads, so the CI and its
// templates come with one statement.
const ciTemplatesColumn = `(SELECT COALESCE(jsonb_agg(jsonb_build_object('template_id', ct.template_id, 'key', t.key,
		'version', ct.version, 'position', ct.position) ORDER BY ct.position), '[]'::jsonb)
	FROM ci_template ct JOIN template t ON t.id = ct.template_id WHERE ct.ci_id = ci.id)`

// templatesScanner appends the destination of ciTemplatesColumn to a scan
// of ciSelectColumns.
type templatesScanner struct {
	ciScanner
	templates *[]byte
}

func (s templatesScanner) Scan(dest ...any) error {
	return s.ciScanner.Scan(append(dest, s.templates)...)
}

// scanCIWithTemplates scans ciSelectColumns followed by ciTemplatesColumn.
func scanCIWithTemplates(row ciScanner) (*Item, error) {
	var raw []byte
	item, err := scanCI(templatesScanner{ciScanner: row, templates: &raw})
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &item.Templates); err != nil {
		return nil, fmt.Errorf("decode ci templates: %w", err)
	}
	return item, nil
}

// sameTemplates reports whether two template lists name the same templates
// and versions in the same order.
func sameTemplates(a, b []citype.CITemplate) bool {
	return slices.EqualFunc(a, b, func(x, y citype.CITemplate) bool {
		return strings.EqualFold(x.TemplateID, y.TemplateID) && x.Version == y.Version
	})
}

// writeTemplatesTx writes the checked template list of CI ciID
// (citype.ResolveTemplateRefsTx); replace removes the rows it had first.
// A template or version deleted since the check is a foreign key violation
// (422 through api.WriteDBError).
func writeTemplatesTx(ctx context.Context, tx pgx.Tx, orgID, ciID string, list []citype.CITemplate, replace bool) error {
	if replace {
		if _, err := tx.Exec(ctx, `DELETE FROM ci_template WHERE ci_id = $1`, ciID); err != nil {
			return fmt.Errorf("replace ci templates: %w", err)
		}
	}
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	versions := make([]int32, len(list))
	positions := make([]int32, len(list))
	for i := range list {
		ids[i] = list[i].TemplateID
		versions[i], positions[i] = int32(list[i].Version), int32(list[i].Position) //nolint:gosec // checked versions, list indexes
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ci_template (organization_id, ci_id, template_id, version, position)
		SELECT $1, $2, u.template_id, u.version, u.position
		FROM unnest($3::uuid[], $4::int[], $5::int[]) AS u(template_id, version, position)`,
		orgID, ciID, ids, versions, positions); err != nil {
		return fmt.Errorf("assign ci templates: %w", err)
	}
	return nil
}

// recordTemplatesChange records a replaced template list (AUD-07, K20):
// history, audit entry ci.templates_changed and event ci.changed, nothing
// for nil.
func (r *PGRepository) recordTemplatesChange(ctx context.Context, tx pgx.Tx, item *Item, change *templatesChange) error {
	if err := r.recordTemplatesHistory(ctx, tx, item, change); err != nil {
		return err
	}
	if err := r.recordTemplatesAudit(ctx, tx, item, change); err != nil {
		return err
	}
	return r.recordTemplatesEvent(ctx, tx, item, change)
}

// recordTemplatesHistory writes the ci_change row update of the field
// templates in a change set of its own (WP-487); a template migration is
// the type template_migration (WP-253, MigrateTemplate).
func (r *PGRepository) recordTemplatesHistory(ctx context.Context, tx pgx.Tx, item *Item, change *templatesChange) error {
	if change == nil {
		return nil
	}
	return r.recordCIChange(ctx, tx, newChangeSet(ChangeKindUpdated), item.OrganizationID, item.ID, item.Version,
		"update", "templates", change.before, change.after)
}

// recordTemplatesAudit writes the audit entry ci.templates_changed with the
// list before and after (like ci.type_changed).
func (r *PGRepository) recordTemplatesAudit(ctx context.Context, tx pgx.Tx, item *Item, change *templatesChange) error {
	if change == nil {
		return nil
	}
	return r.recordAudit(ctx, tx, "ci.templates_changed", item, map[string]interface{}{
		"before": change.before, "after": change.after,
	})
}

// recordTemplatesEvent writes ci.changed with the changed field templates.
func (r *PGRepository) recordTemplatesEvent(ctx context.Context, tx pgx.Tx, item *Item, change *templatesChange) error {
	if change == nil {
		return nil
	}
	return r.recordEvent(ctx, tx, events.CIChanged, item, []string{"templates"})
}

// kind is the change kind of the field changes (AUD-03): a status change
// alone is status_changed, everything else updated.
func (w *rowUpdate) kind() string {
	if len(w.changes) == 1 && w.changes[0].Field == "status" {
		return ChangeKindStatusChanged
	}
	return ChangeKindUpdated
}

// recordFieldChanges writes the field rows of an update into the change set.
func (r *PGRepository) recordFieldChanges(ctx context.Context, tx pgx.Tx, set changeSet, orgID, id string, version int64, changes []ciFieldChange) error {
	changeType := "update"
	if len(changes) == 1 && changes[0].Field == "status" {
		changeType = "status_change"
	}
	for _, change := range changes {
		fieldChangeType := changeType
		if change.Field == "status" {
			fieldChangeType = "status_change"
		}
		if err := r.recordCIChange(ctx, tx, set, orgID, id, version, fieldChangeType, change.Field, change.Old, change.New); err != nil {
			return err
		}
	}
	return nil
}

// recordUpdateAudit writes the audit entry of the field changes of w.
func (r *PGRepository) recordUpdateAudit(ctx context.Context, tx pgx.Tx, w *rowUpdate) error {
	action := "ci.updated"
	if w.kind() == ChangeKindStatusChanged {
		action = "ci.status_changed"
	}
	return r.recordAudit(ctx, tx, action, w.after, map[string]interface{}{"before": w.before, "after": w.after, "fields": w.changes})
}

// changedFieldNames lists the fields of changes for the event ci.changed.
func changedFieldNames(changes []ciFieldChange) []string {
	names := make([]string, 0, len(changes))
	for _, change := range changes {
		names = append(names, change.Field)
	}
	return names
}

// updateRow applies req to the row of CI id in tx; it writes the manual
// overrides of a manual change but no history, audit entry or event.
func (r *PGRepository) updateRow(ctx context.Context, tx pgx.Tx, orgID, id string, reqp *UpdateRequest) (*rowUpdate, error) {
	req := *reqp
	var item *Item
	// FOR UPDATE: the merge patch is computed from this state.
	beforeQuery := fmt.Sprintf("SELECT %s, %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL FOR UPDATE",
		ciSelectColumns, ciTemplatesColumn)
	before, err := scanCIWithTemplates(tx.QueryRow(ctx, beforeQuery, id, orgID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get ci before update: %w", err)
	}
	if reviveErr := reviveSighted(before, &req); reviveErr != nil {
		return nil, reviveErr
	}
	// attributes.tags of an automated write (ingest) is the tag list like
	// the LegacyTags of the API (E-125).
	if req.LegacyTags == nil {
		rest, legacy, tagsErr := ExtractLegacyTags(req.Attributes)
		if tagsErr != nil {
			return nil, tagsErr
		}
		if legacy != nil {
			req.Attributes, req.LegacyTags = rest, legacy
		}
	}
	if req.Manual != nil {
		if preErr := checkPrecondition(ctx, tx, orgID, before, &req); preErr != nil {
			return nil, preErr
		}
	} else if dropErr := dropOverriddenFields(ctx, tx, id, &req); dropErr != nil {
		return nil, dropErr
	}
	if scopeErr := checkContainmentScope(ctx, tx, orgID, before, &req); scopeErr != nil {
		return nil, scopeErr
	}
	if adoptErr := r.adoptCILocation(ctx, tx, orgID, before, &req); adoptErr != nil {
		return nil, adoptErr
	}
	// A replaced template list (PUT /cis/{id}/templates, WP-710), checked
	// under RLS; an unchanged list writes nothing.
	var templates *templatesChange
	if req.Templates != nil {
		resolved, terr := citype.ResolveTemplateRefsTx(ctx, tx, before.CITypeID, *req.Templates)
		if terr != nil {
			return nil, templateViolation(terr)
		}
		if !sameTemplates(before.Templates, resolved) {
			if werr := writeTemplatesTx(ctx, tx, orgID, id, resolved, true); werr != nil {
				return nil, werr
			}
			prior := before.Templates
			if prior == nil {
				prior = []citype.CITemplate{}
			}
			templates = &templatesChange{before: prior, after: resolved}
		}
	}
	// A replaced tag assignment (attributes.tags, E-125) is written before
	// the row, so the row read back carries it; an unchanged one writes
	// nothing.
	tagsChanged := false
	if req.LegacyTags != nil {
		keys, names := tagKeys(*req.LegacyTags)
		if !slices.Equal(keys, before.Tags) {
			if werr := writeCITags(ctx, tx, orgID, []ciTagWrite{{ciID: id, keys: keys, names: names}}, true); werr != nil {
				return nil, werr
			}
			tagsChanged = true
		}
	}

	setClauses := make([]string, 0, 18)
	args := []any{id}
	argPos := 2

	addStringField := func(column string, value *string) {
		if value == nil {
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
		args = append(args, nilIfEmpty(*value))
		argPos++
	}

	addStringField("name", req.Name)
	addStringField("status", req.Status)
	addStringField("client_id", req.ClientID)
	addStringField("asset_id", req.AssetID)
	addStringField("parent_ci_id", req.ParentCIID)
	addStringField("location_id", req.LocationID)
	addStringField("manufacturer", req.Manufacturer)
	addStringField("model", req.Model)
	addStringField("serial_number", req.SerialNumber)
	addStringField("hardware_uuid", req.HardwareUUID)
	addStringField("management_ip", req.ManagementIP)
	addStringField("primary_mac", req.PrimaryMAC)
	addStringField("hostname", req.Hostname)
	addStringField("fqdn", req.FQDN)
	addStringField("os_name", req.OSName)
	addStringField("os_version", req.OSVersion)
	addStringField("firmware_version", req.FirmwareVersion)
	addStringField("sys_object_id", req.SysObjectID)
	addStringField("discovery_source", req.DiscoverySource)

	// The computed attributes follow the state the update leaves (WP-717):
	// their values are part of this row write, an update that writes
	// nothing computes nothing.
	var computedKeys map[string]bool
	var computedChanged map[string]any
	var computedAt time.Time
	if len(setClauses) > 0 || req.Attributes != nil || req.LastSeenAt != nil || templates != nil || tagsChanged {
		var computeErr error
		if computedKeys, computedChanged, computedAt, computeErr = r.computedUpdate(ctx, tx, orgID, before, &req); computeErr != nil {
			return nil, computeErr
		}
	}

	if req.Attributes != nil {
		// RFC 7396: nested objects merge, null removes a member (CI-04).
		// Instance attributes are stored under _instance; the projected
		// namespaces of the row stay (CI-10).
		instance, namesErr := instanceFieldNames(ctx, tx, id)
		if namesErr != nil {
			return nil, namesErr
		}
		setClauses = append(setClauses, fmt.Sprintf("attributes = $%d::jsonb || ci_attribute_namespaces(attributes)", argPos))
		args = append(args, storedAttributes(MergePatch(before.Attributes, req.Attributes), instance))
		argPos++
	}

	if req.LastSeenAt != nil {
		parsed, parseErr := time.Parse(time.RFC3339, *req.LastSeenAt)
		if parseErr != nil {
			// A malformed value is the client's error, not a 500 (F-229).
			return nil, &ValidationError{Violations: []Violation{{Field: "last_seen_at", Detail: "must be an RFC 3339 date-time"}}}
		}
		// A sighting (discovery, agent) keeps the later time: a record
		// observed before the last sighting does not move it back (GLO-12).
		if req.Manual == nil {
			setClauses = append(setClauses, fmt.Sprintf("last_seen_at = GREATEST(last_seen_at, $%d)", argPos))
		} else {
			setClauses = append(setClauses, fmt.Sprintf("last_seen_at = $%d", argPos))
		}
		args = append(args, parsed)
		argPos++
	}

	if len(setClauses) == 0 && templates == nil && !tagsChanged {
		return &rowUpdate{before: before, after: before}, nil
	}

	setClauses = append(setClauses, "updated_at = NOW()")
	if req.Manual != nil || req.Authoritative {
		// Rank >= 92 writes raise the version (API-07).
		setClauses = append(setClauses, "version = version + 1")
	}
	// The tenant predicate is redundant with row-level security but is kept
	// as defense in depth: a missing or wrong app.org_id must never allow a
	// cross-tenant write.
	query := fmt.Sprintf(
		"UPDATE ci SET %s WHERE id = $1 AND organization_id = $%d AND deleted_at IS NULL RETURNING %s, %s",
		strings.Join(setClauses, ", "),
		argPos,
		ciSelectColumns,
		ciTemplatesColumn,
	)
	args = append(args, orgID)

	item, err = scanCIWithTemplates(tx.QueryRow(ctx, query, args...))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, locationError(fmt.Errorf("update ci: %w", err))
	}

	if err := loadAssetFacts(ctx, tx, []*Item{item}); err != nil {
		return nil, err
	}
	if err := writeComputedProvenance(ctx, tx, orgID, computedAt, computedValuesOf(id, computedChanged)); err != nil {
		return nil, err
	}
	w := &rowUpdate{before: before, after: item, changes: diffCI(before, item), templates: templates}
	if len(w.changes) > 0 && req.Manual != nil {
		if err := recordManualOverrides(ctx, tx, orgID, before, item, &req, computedKeys); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// reviveSighted brings an unknown CI back to active for a sighting
// (UpdateRequest.Sighting) newer than the last one of the locked row
// before: like a status the record carries, a field override of the status
// keeps it (dropOverriddenFields).
func reviveSighted(before *Item, req *UpdateRequest) error {
	if !req.Sighting || req.Status != nil || req.LastSeenAt == nil || before.Status != StatusUnknown {
		return nil
	}
	seen, err := time.Parse(time.RFC3339, *req.LastSeenAt)
	if err != nil {
		return &ValidationError{Violations: []Violation{{Field: "last_seen_at", Detail: "must be an RFC 3339 date-time"}}}
	}
	if before.LastSeenAt == nil || seen.After(*before.LastSeenAt) {
		active := StatusActive
		req.Status = &active
	}
	return nil
}

// checkCITypes keeps the type of a CI within its organization (CI-01,
// TEN-09; F-144). The foreign key of ci_type_id has one column and bypasses
// row level security; the trigger ci_type_organization refuses a type of
// another organization on every path. Here a type ID that is no UUID is
// refused before any statement, and the types the writer chose (ChosenType)
// are read in the writing transaction under the principal's row level
// security with one statement: a private type of another organization is
// invisible and, like an unknown ID, "CI type not found"; a deactivated type
// is refused. Items without ChosenType (discovery, agent) cost no statement.
func checkCITypes(ctx context.Context, tx pgx.Tx, orgID string, items []*Item) error {
	var chosen []string
	for _, item := range items {
		if err := checkCITypeID(item.CITypeID); err != nil {
			return err
		}
		if item.ChosenType {
			chosen = append(chosen, item.CITypeID)
		}
	}
	if len(chosen) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT t.is_active
		FROM unnest($2::uuid[]) WITH ORDINALITY AS l(id, ord)
		LEFT JOIN ci_type t ON t.id = l.id AND (t.organization_id IS NULL OR t.organization_id = $1)
		ORDER BY l.ord`, orgID, chosen)
	if err != nil {
		return fmt.Errorf("read ci types: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var active *bool
		if err := rows.Scan(&active); err != nil {
			return fmt.Errorf("read ci types: %w", err)
		}
		switch {
		case active == nil:
			return ciTypeViolation(ciTypeNotFound)
		case !*active:
			return ciTypeViolation(ciTypeDeactivated)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read ci types: %w", err)
	}
	return nil
}

// parentOfAnotherClient is the violation of a parent outside the CI's client.
const parentOfAnotherClient = "the parent CI belongs to another client"

// checkParents keeps containment within a client (TEN-05, AST-05; F-086).
// The composite foreign key of parent_ci_id checks only the organization and
// bypasses row level security, so the parents of the items are read here, in
// the writing transaction under the principal's row level security, with one
// statement: a parent the principal cannot see, or a deleted one, is "parent
// CI not found"; a parent of another client than the CI is refused; a parent
// of the CI's client or an org-wide parent (E-09, E-62) is accepted. Items
// without a parent cost no statement.
func checkParents(ctx context.Context, tx pgx.Tx, orgID string, items []*Item) error {
	var parents, clients []*string
	for _, item := range items {
		if strings.TrimSpace(item.ParentCIID) == "" {
			continue
		}
		parent := item.ParentCIID
		var client *string
		if strings.TrimSpace(item.ClientID) != "" {
			client = &item.ClientID
		}
		parents, clients = append(parents, &parent), append(clients, client)
	}
	if len(parents) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT c.id IS NOT NULL, COALESCE(c.client_id IS NULL OR c.client_id = l.client_id::uuid, false)
		FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS l(parent_ci_id, client_id, ord)
		LEFT JOIN ci c ON c.id = l.parent_ci_id::uuid AND c.organization_id = $1 AND c.deleted_at IS NULL
		ORDER BY l.ord`, orgID, parents, clients)
	if err != nil {
		return fmt.Errorf("read parent cis: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var visible, sameClient bool
		if err := rows.Scan(&visible, &sameClient); err != nil {
			return fmt.Errorf("read parent cis: %w", err)
		}
		switch {
		case !visible:
			return &ValidationError{Violations: []Violation{{Field: "parent_ci_id", Detail: "parent CI not found"}}}
		case !sameClient:
			return &ValidationError{Violations: []Violation{{Field: "parent_ci_id", Detail: parentOfAnotherClient}}}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read parent cis: %w", err)
	}
	return nil
}

// checkContainmentScope applies checkParents to an update: to a new parent
// and to the parent of a CI that moves to another client. A CI that moves to
// a client must not contain CIs of another client or org-wide CIs, read the
// same way; a CI that becomes org-wide may contain CIs of any client.
func checkContainmentScope(ctx context.Context, tx pgx.Tx, orgID string, before *Item, req *UpdateRequest) error {
	after := &Item{ClientID: before.ClientID, ParentCIID: before.ParentCIID}
	if req.ClientID != nil {
		after.ClientID = strings.TrimSpace(*req.ClientID)
	}
	if req.ParentCIID != nil {
		after.ParentCIID = strings.TrimSpace(*req.ParentCIID)
	}
	moved := after.ClientID != before.ClientID
	if after.ParentCIID != before.ParentCIID || moved {
		if err := checkParents(ctx, tx, orgID, []*Item{after}); err != nil {
			return err
		}
	}
	if !moved || after.ClientID == "" {
		return nil
	}
	var foreign bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND parent_ci_id = $2
		AND deleted_at IS NULL AND client_id IS DISTINCT FROM $3::uuid)`, orgID, before.ID, after.ClientID).Scan(&foreign); err != nil {
		return fmt.Errorf("read contained cis: %w", err)
	}
	if foreign {
		return &ValidationError{Violations: []Violation{{Field: "client_id",
			Detail: "the CI contains CIs of another client or org-wide CIs (their parent_ci_id)"}}}
	}
	return nil
}

// adoptCILocation keeps the location of a CI that is linked to an asset
// without location (DB-05, E-81 option 1; F-239): the mirror trigger
// ci_mirror_asset_location would set the CI's location to the asset's NULL
// and so drop location, site and room and with them the scope of the CI.
// The asset takes the location the CI has after the update instead, in the
// writing transaction under the principal's row level security, with an
// audit entry on the asset; the trigger then mirrors it back. An asset with
// a location keeps it and the CI takes it as before. An asset without
// location the principal may read but not update is refused with 422 on
// asset_id rather than losing the location silently.
func (r *PGRepository) adoptCILocation(ctx context.Context, tx pgx.Tx, orgID string, before *Item, req *UpdateRequest) error {
	if req.AssetID == nil {
		return nil
	}
	assetID := strings.TrimSpace(*req.AssetID)
	if assetID == "" || assetID == before.AssetID || !api.IsUUID(assetID) {
		// Unlinking, no new link, or an ID the update itself refuses.
		return nil
	}
	location := before.LocationID
	if req.LocationID != nil {
		location = strings.TrimSpace(*req.LocationID)
	}
	if location == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE asset SET location_id = $3
		WHERE id = $1 AND organization_id = $2 AND location_id IS NULL`, assetID, orgID, location)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "42501":
				return &ValidationError{Violations: []Violation{{Field: "asset_id", Detail: assetCannotTakeLocation}}}
			case "23503":
				return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: "location not found"}}}
			}
		}
		return fmt.Errorf("asset takes the location of the CI: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The asset has a location (mirrored as before), is not visible
		// (the update refuses the link), or may not be updated here.
		var unlocated bool
		err = tx.QueryRow(ctx, `SELECT location_id IS NULL FROM asset WHERE id = $1 AND organization_id = $2`,
			assetID, orgID).Scan(&unlocated)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("read asset location: %w", err)
		case unlocated:
			return &ValidationError{Violations: []Violation{{Field: "asset_id", Detail: assetCannotTakeLocation}}}
		}
		return nil
	}
	return r.recordAssetLocationAudit(ctx, tx, orgID, assetID, before.ID, location)
}

// assetCannotTakeLocation refuses a link that would drop the CI's location.
const assetCannotTakeLocation = "the asset has no location and cannot take the location of the CI"

// recordAssetLocationAudit audits the location an asset took from the CI
// linked to it (adoptCILocation).
func (r *PGRepository) recordAssetLocationAudit(ctx context.Context, tx pgx.Tx, orgID, assetID, ciID, location string) error {
	if r.recorder == nil {
		return nil
	}
	t := tenant.FromContext(ctx)
	actorType := "system"
	if strings.TrimSpace(t.UserID) != "" {
		actorType = "user"
	}
	entry := audit.Entry{
		OrganizationID: orgID,
		ActorID:        strings.TrimSpace(t.UserID),
		ActorType:      actorType,
		Action:         "asset.updated",
		ResourceType:   "asset",
		ResourceID:     assetID,
		Changes: map[string]interface{}{
			"before": map[string]any{"location_id": nil},
			"after":  map[string]any{"location_id": location},
			"fields": []ciFieldChange{{Field: "location_id", Old: nil, New: location}},
			"reason": "location taken from the CI linked to the asset",
			"ci_id":  ciID,
		},
	}
	return lastInTx(ctx, func() error {
		if _, err := r.recorder.Record(ctx, tx, entry); err != nil {
			return fmt.Errorf("record audit: %w", err)
		}
		return nil
	})
}

// instanceFieldNames lists the names of the instance attributes of a CI.
func instanceFieldNames(ctx context.Context, tx pgx.Tx, ciID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT name FROM ci_instance_field_definition WHERE ci_id = $1`, ciID)
	if err != nil {
		return nil, fmt.Errorf("instance attributes: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Delete soft-deletes a CI.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL", ciSelectColumns)
		before, err := scanCI(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get ci for delete: %w", err)
		}

		// CI-05: the CI is decommissioned (its operative status; the
		// lifecycle state stays, LCY-01) and its asset link is released;
		// the change row keeps what a restore brings back.
		var assetPrimary bool
		if before.AssetID != "" {
			tag, uerr := tx.Exec(ctx, "UPDATE asset SET ci_id = NULL WHERE id = $1 AND ci_id = $2", before.AssetID, id)
			if uerr != nil {
				return fmt.Errorf("release asset link: %w", uerr)
			}
			assetPrimary = tag.RowsAffected() > 0
		}
		cmdTag, err := tx.Exec(ctx, `UPDATE ci SET deleted_at = NOW(), updated_at = NOW(), status = 'decommissioned', asset_id = NULL
			WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL`, id, orgID)
		if err != nil {
			return fmt.Errorf("delete ci: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return ErrNotFound
		}
		released := map[string]any{"status": StatusDecommissioned}
		if before.AssetID != "" {
			released["unlinked_asset_id"] = before.AssetID
			released["asset_primary"] = assetPrimary
		}
		if err := r.recordCIChange(ctx, tx, newChangeSet(ChangeKindDeleted), orgID, id, before.Version, "delete", "", before, released); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.deleted", before, map[string]interface{}{"before": before}); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CIDeleted, before, nil)
	})
}

// Restore reactivates a soft-deleted CI (CI-13): an operator decided that a
// device seen again is the deleted one. It records the decision in the
// change history and the audit trail.
func (r *PGRepository) Restore(ctx context.Context, orgID, id string) (*Item, error) {
	return r.restore(ctx, orgID, id, 0, "reconciliation_decision", "resurrected_device")
}

// RestoreDeleted implements Restorer: POST /cis/{id}/restore within the
// retention (CI-05); an older deletion is ErrRetentionExpired, a CI that is
// not deleted ErrNotDeleted.
func (r *PGRepository) RestoreDeleted(ctx context.Context, orgID, id string, retention time.Duration) (*Item, error) {
	return r.restore(ctx, orgID, id, retention, "update", "api")
}

// restore clears deleted_at of a deleted CI (retention 0: no limit) and
// records the change, the audit entry and the ci.restored event. A CI that
// is not deleted is ErrNotDeleted, a merged one ErrMergeConflict.
func (r *PGRepository) restore(ctx context.Context, orgID, id string, retention time.Duration, changeType, reason string) (*Item, error) {
	var restored *Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var deletedAt *time.Time
		var mergedInto string
		if err := tx.QueryRow(ctx, "SELECT deleted_at, COALESCE(merged_into_id::text, '') FROM ci WHERE id = $1 AND organization_id = $2",
			id, orgID).Scan(&deletedAt, &mergedInto); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("get ci for restore: %w", err)
		}
		if deletedAt == nil {
			return ErrNotDeleted
		}
		// A merged CI comes back only through the unmerge (CI-12).
		if mergedInto != "" {
			return ErrMergeConflict
		}
		if retention > 0 && time.Since(*deletedAt) > retention {
			return ErrRetentionExpired
		}
		// The status and the asset link of the deletion come back (CI-05).
		var status, assetID string
		var primary bool
		err := tx.QueryRow(ctx, `SELECT COALESCE(old_value->>'status', ''), COALESCE(new_value->>'unlinked_asset_id', ''),
				COALESCE((new_value->>'asset_primary')::boolean, false)
			FROM ci_change WHERE ci_id = $1 AND change_type = 'delete' ORDER BY created_at DESC LIMIT 1`, id).Scan(&status, &assetID, &primary)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read deletion: %w", err)
		}
		if status == "" || status == StatusDecommissioned {
			status = StatusActive
		}
		tag, err := tx.Exec(ctx, `UPDATE ci SET deleted_at = NULL, updated_at = NOW(), version = version + 1,
				status = CASE WHEN status = 'decommissioned' THEN $3 ELSE status END,
				asset_id = COALESCE(asset_id, (SELECT a.id FROM asset a WHERE a.id = NULLIF($4, '')::uuid))
			WHERE id = $1 AND organization_id = $2 AND deleted_at IS NOT NULL`, id, orgID, status, assetID)
		if err != nil {
			// A reactivated CI may hold a value of a unique attribute that
			// another CI took in the meantime (MET-15).
			return locationError(fmt.Errorf("restore ci: %w", err))
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if primary && assetID != "" {
			if _, err = tx.Exec(ctx, "UPDATE asset SET ci_id = $1 WHERE id = $2 AND ci_id IS NULL", id, assetID); err != nil {
				return fmt.Errorf("restore asset link: %w", err)
			}
		}
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2", ciSelectColumns)
		if restored, err = scanCI(tx.QueryRow(ctx, query, id, orgID)); err != nil {
			return fmt.Errorf("get restored ci: %w", err)
		}
		if uerr := r.checkUniqueBack(ctx, tx, restored); uerr != nil {
			return uerr
		}
		if changeErr := r.recordCIChange(ctx, tx, newChangeSet(ChangeKindRestored), orgID, id, restored.Version, changeType, "deleted_at", "deleted", nil); changeErr != nil {
			return changeErr
		}
		if err := r.recordAudit(ctx, tx, "ci.restored", restored, map[string]interface{}{"reason": reason}); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CIRestored, restored, nil)
	})
	return restored, r.uniqueField(ctx, orgID, err)
}

// ListChanges returns paginated change history for a CI.
func (r *PGRepository) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	changes := []Change{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM ci_change
			WHERE organization_id = $1 AND ci_id = $2
		`, orgID, ciID).Scan(&total); err != nil {
			return fmt.Errorf("count ci changes: %w", err)
		}

		rows, err := tx.Query(ctx, `
			SELECT
				id::text,
				organization_id::text,
				ci_id::text,
				COALESCE(actor_id::text, ''),
				change_type,
				COALESCE(field_name, ''),
				COALESCE(old_value, 'null'::jsonb),
				COALESCE(new_value, 'null'::jsonb),
				COALESCE(comment, ''),
				created_at
			FROM ci_change
			WHERE organization_id = $1 AND ci_id = $2
			ORDER BY created_at DESC, id DESC
			LIMIT $3 OFFSET $4
		`, orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list ci changes: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var change Change
			var oldValue []byte
			var newValue []byte
			if scanErr := rows.Scan(
				&change.ID,
				&change.OrganizationID,
				&change.CIID,
				&change.ActorID,
				&change.ChangeType,
				&change.FieldName,
				&oldValue,
				&newValue,
				&change.Comment,
				&change.CreatedAt,
			); scanErr != nil {
				return fmt.Errorf("scan ci change: %w", scanErr)
			}
			if decodeErr := json.Unmarshal(oldValue, &change.OldValue); decodeErr != nil {
				return fmt.Errorf("decode old ci change value: %w", decodeErr)
			}
			if decodeErr := json.Unmarshal(newValue, &change.NewValue); decodeErr != nil {
				return fmt.Errorf("decode new ci change value: %w", decodeErr)
			}
			changes = append(changes, change)
		}
		if iterErr := rows.Err(); iterErr != nil {
			return fmt.Errorf("iterate ci changes: %w", iterErr)
		}
		// The history shows the personal values the surrogates stand for;
		// deleted ones are null (SEC-05).
		values := make([]any, 0, 2*len(changes))
		for i := range changes {
			values = append(values, changes[i].OldValue, changes[i].NewValue)
		}
		revealed, err := pii.RevealTx(ctx, tx, orgID, values)
		if err != nil {
			return fmt.Errorf("resolve ci change values: %w", err)
		}
		for i, v := range revealed.([]any) {
			if i%2 == 0 {
				changes[i/2].OldValue = v
			} else {
				changes[i/2].NewValue = v
			}
		}
		return nil
	})
	return changes, total, err
}

type ciScanner interface {
	Scan(dest ...any) error
}

func scanCI(scanner ciScanner) (*Item, error) {
	item := &Item{}
	var firstSeen sql.NullTime
	var lastSeen sql.NullTime
	var deletedAt sql.NullTime
	var healthUpdated sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time

	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.SiteID,
		&item.RoomID,
		&item.LocationID,
		&item.CITypeID,
		&item.Name,
		&item.Status,
		&item.Manufacturer,
		&item.Model,
		&item.SerialNumber,
		&item.HardwareUUID,
		&item.ManagementIP,
		&item.PrimaryMAC,
		&item.Hostname,
		&item.FQDN,
		&item.OSName,
		&item.OSVersion,
		&item.FirmwareVersion,
		&item.SysObjectID,
		&item.Attributes,
		&item.DiscoverySource,
		&firstSeen,
		&lastSeen,
		&item.IsManual,
		&createdAt,
		&updatedAt,
		&deletedAt,
		&item.Version,
		&item.AssetID,
		&item.ParentCIID,
		&item.LifecycleState,
		&item.Health,
		&healthUpdated,
		&item.SchemaVersion,
		&item.MergedIntoID,
		&item.Tags,
	); err != nil {
		return nil, err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}

	item.Overridden = overridden(item.Attributes)
	item.InstanceKeys = instanceKeys(item.Attributes)
	item.Attributes = presentAttributes(item.Attributes)
	if firstSeen.Valid {
		t := firstSeen.Time.UTC()
		item.FirstSeenAt = &t
	}
	if lastSeen.Valid {
		t := lastSeen.Time.UTC()
		item.LastSeenAt = &t
	}
	if deletedAt.Valid {
		t := deletedAt.Time.UTC()
		item.DeletedAt = &t
	}
	if healthUpdated.Valid {
		t := healthUpdated.Time.UTC()
		item.HealthUpdatedAt = &t
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)

	return item, nil
}

type ciFieldChange struct {
	Field string `json:"field"`
	Old   any    `json:"old_value"`
	New   any    `json:"new_value"`
}

func (r *PGRepository) recordAudit(ctx context.Context, tx pgx.Tx, action string, item *Item, changes map[string]interface{}) error {
	if r.recorder == nil {
		return nil
	}
	t := tenant.FromContext(ctx)
	actorType := "system"
	if strings.TrimSpace(t.UserID) != "" {
		actorType = "user"
	}
	entry := audit.Entry{
		OrganizationID: item.OrganizationID,
		ActorID:        strings.TrimSpace(t.UserID),
		ActorType:      actorType,
		Action:         action,
		ResourceType:   "ci",
		ResourceID:     item.ID,
		Changes:        snapshotChanges(changes),
	}
	return lastInTx(ctx, func() error {
		if _, err := r.recorder.Record(ctx, tx, entry); err != nil {
			return fmt.Errorf("record audit: %w", err)
		}
		return nil
	})
}

// Kinds of a CI change (AUD-03, CI-11); ci_change.change_kind holds for the
// change set (000167).
const (
	ChangeKindCreated             = "created"
	ChangeKindUpdated             = "updated"
	ChangeKindDeleted             = "deleted"
	ChangeKindRestored            = "restored"
	ChangeKindMerged              = "merged"
	ChangeKindTypeChanged         = "type_changed"
	ChangeKindRelationshipAdded   = "relationship_added"
	ChangeKindRelationshipRemoved = "relationship_removed"
	ChangeKindStatusChanged       = "status_changed"
)

// changeSet is one write of a CI (AUD-03, E-82 option 2): its field rows in
// ci_change share the id and the kind. record names the ingest record the
// write comes from (UpdateRecord). comment is written into the comment of
// its rows, "" none (the template migration, WP-253).
type changeSet struct {
	id, kind string
	record   *RecordOrigin
	comment  string
}

// newChangeSet returns a change set of kind with a new id.
func newChangeSet(kind string) changeSet {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return changeSet{id: fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), kind: kind}
}

// origin is who made a CI change and the request it came with (AUD-03,
// AUD-07); nil values are stored as NULL.
type origin struct {
	actorID, actorUserID, source, requestID, correlationID any
	collectorID, observedAt                                any
}

// changeOrigin returns the origin of a change made with ctx: the subject as
// actor_id, the user (not an API key) as actor_user_id, and the source,
// request and correlation id the audit middleware attached to the request.
// Without a request the source is system, as in the audit log.
func changeOrigin(ctx context.Context) origin {
	userID := tenant.FromContext(ctx).UserID
	o := origin{actorID: nilIfEmpty(userID), source: audit.SourceSystem}
	if p, ok := identity.PrincipalFromContext(ctx); (!ok || p.Type != identity.PrincipalTypeAPIKey) && audit.IsUUID(userID) {
		o.actorUserID = userID
	}
	if info, ok := audit.RequestInfoFrom(ctx); ok {
		if info.Source != "" {
			o.source = info.Source
		}
		o.requestID, o.correlationID = nilIfEmpty(info.RequestID), nilIfEmpty(info.CorrelationID)
	}
	return o
}

// withRecord adds the ingest record a change comes from (AUD-03): its source
// is the collector, with the collector's id (a UUID, else none) and the time
// of the observation; the record id is the correlation id.
func (o *origin) withRecord(rec *RecordOrigin) {
	if rec == nil {
		return
	}
	o.source = audit.SourceCollector
	if audit.IsUUID(rec.CollectorID) {
		o.collectorID = rec.CollectorID
	}
	if !rec.ObservedAt.IsZero() {
		o.observedAt = rec.ObservedAt.UTC()
	}
	if strings.TrimSpace(rec.RecordID) != "" {
		o.correlationID = rec.RecordID
	}
}

// recordCIChange writes one field row of the change set set, with the
// comment of the set when it has one.
func (r *PGRepository) recordCIChange(ctx context.Context, tx pgx.Tx, set changeSet, orgID, ciID string, version int64, changeType, fieldName string, oldValue, newValue any) error {
	// Personal values are stored as vault surrogates (SEC-05).
	oldValue, err := sealChangeValue(ctx, tx, orgID, fieldName, oldValue)
	if err != nil {
		return err
	}
	if newValue, err = sealChangeValue(ctx, tx, orgID, fieldName, newValue); err != nil {
		return err
	}
	oldJSON, err := marshalJSONValue(oldValue)
	if err != nil {
		return fmt.Errorf("marshal old ci change value: %w", err)
	}
	newJSON, err := marshalJSONValue(newValue)
	if err != nil {
		return fmt.Errorf("marshal new ci change value: %w", err)
	}
	who := changeOrigin(ctx)
	who.withRecord(set.record)
	_, err = tx.Exec(ctx, `
		INSERT INTO ci_change (
			organization_id,
			ci_id,
			actor_id,
			change_type,
			field_name,
			old_value,
			new_value,
			ci_version,
			change_kind,
			change_set_id,
			actor_user_id,
			source,
			request_id,
			correlation_id,
			collector_id,
			observed_at,
			comment
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, orgID, ciID, who.actorID, changeType, nilIfEmpty(fieldName), oldJSON, newJSON, version,
		set.kind, set.id, who.actorUserID, who.source, who.requestID, who.correlationID, who.collectorID, who.observedAt,
		nilIfEmpty(set.comment))
	if err != nil {
		return fmt.Errorf("record ci change: %w", err)
	}
	return nil
}

// sealChangeValue replaces the personal data in a historized value.
func sealChangeValue(ctx context.Context, tx pgx.Tx, orgID, fieldName string, value any) (any, error) {
	sealed, _, err := pii.SealFieldTx(ctx, tx, orgID, fieldName, value)
	if err != nil {
		return nil, fmt.Errorf("seal ci change value: %w", err)
	}
	return sealed, nil
}

func marshalJSONValue(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// diffCI lists the changed fields that are historized. last_seen_at is not
// (AUD-03, REC-07): a sighting alone writes no ci_change.
func diffCI(before, after *Item) []ciFieldChange {
	fields := []struct {
		name string
		old  any
		new  any
	}{
		{"client_id", before.ClientID, after.ClientID},
		{"asset_id", before.AssetID, after.AssetID},
		{"parent_ci_id", before.ParentCIID, after.ParentCIID},
		{"location_id", before.LocationID, after.LocationID},
		{"site_id", before.SiteID, after.SiteID},
		{"room_id", before.RoomID, after.RoomID},
		{"ci_type_id", before.CITypeID, after.CITypeID},
		{"name", before.Name, after.Name},
		{"status", before.Status, after.Status},
		{"manufacturer", before.Manufacturer, after.Manufacturer},
		{"model", before.Model, after.Model},
		{"serial_number", before.SerialNumber, after.SerialNumber},
		{"hardware_uuid", before.HardwareUUID, after.HardwareUUID},
		{"management_ip", before.ManagementIP, after.ManagementIP},
		{"primary_mac", before.PrimaryMAC, after.PrimaryMAC},
		{"hostname", before.Hostname, after.Hostname},
		{"fqdn", before.FQDN, after.FQDN},
		{"os_name", before.OSName, after.OSName},
		{"os_version", before.OSVersion, after.OSVersion},
		{"firmware_version", before.FirmwareVersion, after.FirmwareVersion},
		{"sys_object_id", before.SysObjectID, after.SysObjectID},
		{"attributes", before.Attributes, after.Attributes},
		{"discovery_source", before.DiscoverySource, after.DiscoverySource},
		{"first_seen_at", timePtrRFC3339(before.FirstSeenAt), timePtrRFC3339(after.FirstSeenAt)},
		{"is_manual", before.IsManual, after.IsManual},
		{"tags", before.Tags, after.Tags},
	}
	changes := make([]ciFieldChange, 0, len(fields))
	for _, field := range fields {
		if !reflect.DeepEqual(field.old, field.new) {
			changes = append(changes, ciFieldChange{Field: field.name, Old: field.old, New: field.new})
		}
	}
	return changes
}

func timePtrRFC3339(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// locationError turns a rejected location of a CI (unknown or invisible
// location, location of another client; trigger ci_derive_location of
// migration 000074) into a validation error on location_id.
func locationError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.Code == "23505" && strings.HasPrefix(pgErr.ConstraintName, "idx_attr_") {
		return uniqueIndexError(pgErr)
	}
	switch pgErr.ConstraintName {
	case "ci_location_id_fkey":
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: "location not found"}}}
	case "ci_location_client":
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: "the location belongs to another client"}}}
	case "ci_parent_ci_fkey":
		return &ValidationError{Violations: []Violation{{Field: "parent_ci_id", Detail: "parent CI not found"}}}
	case "ci_parent_not_self", "ci_parent_cycle":
		return &ValidationError{Violations: []Violation{{Field: "parent_ci_id", Detail: "a CI cannot contain itself, directly or through its parents"}}}
	case "ci_lifecycle_requires_model":
		return &ValidationError{Violations: []Violation{{Field: "lifecycle_state",
			Detail: "only CI types with a lifecycle model have a lifecycle state, and it must be a state of that model"}}}
	case "ci_health_read_only":
		return &ValidationError{Violations: []Violation{{Field: "health", Detail: "the health is computed from monitoring"}}}
	case "ci_asset_id_fkey":
		return &ValidationError{Violations: []Violation{{Field: "asset_id", Detail: "asset not found"}}}
	case "ci_ci_type_id_fkey", "ci_type_organization":
		// An unknown type and a type of another organization (trigger of
		// migration 000153) are the same answer (TEN-09).
		return ciTypeViolation(ciTypeNotFound)
	case "ci_location_from_asset":
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: locationOwnedByAsset}}}
	}
	return err
}

// locationOwnedByAsset is the violation of a location write on a CI linked
// to an asset (DB-05).
const locationOwnedByAsset = "the location of a CI linked to an asset is the asset's location"

// instanceKeys returns the sorted keys of the instance attributes of stored
// attributes (attributes._instance, MET-14); nil without any.
func instanceKeys(stored map[string]any) []string {
	instance, _ := stored[NamespaceInstance].(map[string]any)
	if len(instance) == 0 {
		return nil
	}
	keys := make([]string, 0, len(instance))
	for k := range instance {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// loadAssetFacts shows the canonical data of each linked asset on its CIs
// (DB-05). Assets the principal cannot see are left out.
func loadAssetFacts(ctx context.Context, tx pgx.Tx, items []*Item) error {
	byAsset := map[string][]*Item{}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.AssetID == "" {
			continue
		}
		if _, seen := byAsset[item.AssetID]; !seen {
			ids = append(ids, item.AssetID)
		}
		byAsset[item.AssetID] = append(byAsset[item.AssetID], item)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, asset_tag, name, COALESCE(serial_number, ''), COALESCE(location_id::text, ''),
		       COALESCE(lifecycle_state, ''), status, COALESCE(barcode, ''), COALESCE(rfid_tag, ''),
		       COALESCE(to_char(purchase_date, 'YYYY-MM-DD'), ''), COALESCE(to_char(warranty_end, 'YYYY-MM-DD'), ''),
		       COALESCE(supplier, ''), COALESCE(invoice_number, ''), COALESCE(asset_type_id::text, ''),
		       COALESCE(client_id::text, ''), COALESCE(site_id::text, ''),
		       COALESCE(manufacturer, ''), COALESCE(model, ''), COALESCE(cost_center, ''),
		       COALESCE(owner_user_id::text, ''), COALESCE(owner_team_id::text, ''),
		       COALESCE(supplier_id::text, ''), COALESCE(contract_id::text, '')
		FROM asset WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("load asset facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a AssetFacts
		if err := rows.Scan(&a.ID, &a.AssetTag, &a.Name, &a.SerialNumber, &a.LocationID, &a.LifecycleState, &a.Status,
			&a.Barcode, &a.RFIDTag, &a.PurchaseDate, &a.WarrantyEnd, &a.Supplier, &a.InvoiceNumber, &a.AssetTypeID,
			&a.ClientID, &a.SiteID, &a.Manufacturer, &a.Model, &a.CostCenter, &a.OwnerUserID, &a.OwnerTeamID,
			&a.SupplierID, &a.ContractID); err != nil {
			return fmt.Errorf("scan asset facts: %w", err)
		}
		for _, item := range byAsset[a.ID] {
			facts := a
			item.Asset = &facts
		}
	}
	return rows.Err()
}

// derivedFields are set by the database, not by the writer of a change.
// The asset link is structural, not an observed value. Tags are no field
// value (ci_tag, CHECK ci_field_value_no_tags; WP-281).
var derivedFields = map[string]bool{"site_id": true, "room_id": true, "attributes": true, "asset_id": true, "parent_ci_id": true,
	"tags": true}

// recordManualOverrides gives every field a manual change touched a
// protected override, so discovery never overwrites it (OVR-01, CH9): the
// changed CI columns by name, the attributes per changed top-level member
// (a removed member is recorded with a null value). Instance attributes of
// the CI get no override (MET-14, CI-04, WP-714): automation never writes
// them, the central write decision refuses them (ReasonInstanceAttribute).
// Nor do the computed attributes (computed, WP-717): their values follow
// their inputs.
func recordManualOverrides(ctx context.Context, tx pgx.Tx, orgID string, before, after *Item, req *UpdateRequest, computed map[string]bool) error {
	reason := strings.TrimSpace(req.ChangeReason)
	if reason == "" {
		reason = DefaultChangeReason
	}
	for _, change := range diffCI(before, after) {
		if derivedFields[change.Field] || (change.Field == "location_id" && after.AssetID != "") {
			// The location of a linked CI is the asset's (DB-05).
			continue
		}
		value := change.New
		if req.isNull(change.Field) {
			// A field the patch set to null is overridden with null (CH9).
			value = nil
		}
		if err := override.SetOverrideTx(ctx, tx, orgID, after.ID, change.Field, value, req.Manual.Author, reason, true); err != nil {
			return err
		}
	}
	keys := changedKeys(before.Attributes, after.Attributes)
	if len(keys) == 0 {
		return nil
	}
	names, err := instanceFieldNames(ctx, tx, after.ID)
	if err != nil {
		return err
	}
	instance := make(map[string]bool, len(names))
	for _, name := range names {
		instance[name] = true
	}
	for _, key := range keys {
		// A computed value is no manual value (WP-717): it follows its
		// inputs, an override would freeze it.
		if instance[key] || computed[key] {
			continue
		}
		if err := override.SetOverrideTx(ctx, tx, orgID, after.ID, key, after.Attributes[key], req.Manual.Author, reason, true); err != nil {
			return err
		}
	}
	return nil
}

// overrideColumns are CI columns a manual override replaces in the effective
// view; structuralFields are CI columns that are not overridden there.
// Every other field name is an attribute.
var (
	overrideColumns = map[string]func(*Item, string){
		"name":             func(i *Item, v string) { i.Name = v },
		"status":           func(i *Item, v string) { i.Status = v },
		"manufacturer":     func(i *Item, v string) { i.Manufacturer = v },
		"model":            func(i *Item, v string) { i.Model = v },
		"serial_number":    func(i *Item, v string) { i.SerialNumber = v },
		"hardware_uuid":    func(i *Item, v string) { i.HardwareUUID = v },
		"management_ip":    func(i *Item, v string) { i.ManagementIP = v },
		"primary_mac":      func(i *Item, v string) { i.PrimaryMAC = v },
		"hostname":         func(i *Item, v string) { i.Hostname = v },
		"fqdn":             func(i *Item, v string) { i.FQDN = v },
		"os_name":          func(i *Item, v string) { i.OSName = v },
		"os_version":       func(i *Item, v string) { i.OSVersion = v },
		"firmware_version": func(i *Item, v string) { i.FirmwareVersion = v },
		"sys_object_id":    func(i *Item, v string) { i.SysObjectID = v },
	}
	structuralFields = map[string]bool{
		"id": true, "organization_id": true, "client_id": true, "location_id": true, "asset_id": true, "parent_ci_id": true, "site_id": true,
		"room_id": true, "ci_type_id": true, "attributes": true, "discovery_source": true, "is_manual": true,
		"first_seen_at": true, "last_seen_at": true, "created_at": true, "updated_at": true, "deleted_at": true,
	}
)

// overlayOverrides applies the manual overrides of the CIs, so every read
// returns the effective value: an override before the observed value
// (OVR-01, CI-10). The CI rows normally carry it already (manual writes
// and override changes update them); the overlay keeps reads consistent
// for rows written before.
func overlayOverrides(ctx context.Context, tx pgx.Tx, items []*Item) error {
	if len(items) == 0 {
		return nil
	}
	byID := make(map[string]*Item, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		byID[item.ID] = item
		ids = append(ids, item.ID)
	}
	rows, err := tx.Query(ctx, `SELECT ci_id::text, field_name, override_value FROM ci_field_value
		WHERE ci_id = ANY($1::uuid[]) AND override_at IS NOT NULL`, ids)
	if err != nil {
		return fmt.Errorf("read overrides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ciID, field string
		var value any
		if err = rows.Scan(&ciID, &field, &value); err != nil {
			return fmt.Errorf("scan override: %w", err)
		}
		applyOverride(byID[ciID], field, value)
	}
	return rows.Err()
}

func applyOverride(item *Item, field string, value any) {
	if item == nil || structuralFields[field] {
		return
	}
	if set, ok := overrideColumns[field]; ok {
		text := ""
		if value != nil {
			text = fmt.Sprint(value)
		}
		set(item, text)
		return
	}
	if value == nil {
		delete(item.Attributes, field)
		return
	}
	if item.Attributes == nil {
		item.Attributes = map[string]any{}
	}
	item.Attributes[field] = value
}

// checkPrecondition enforces If-Match on a manual change (API-07, API-03):
// a missing If-Match fails when the organization requires one (412); an
// older version fails only when a field of the request changed since by a
// write of rank >= 92 (409, field-granular); a version the CI never had
// fails (412). Attributes count as one field.
func checkPrecondition(ctx context.Context, tx pgx.Tx, orgID string, before *Item, req *UpdateRequest) error {
	if req.IfMatch == nil {
		var required bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE((settings->>'require_if_match')::boolean, false) FROM organization WHERE id = $1`,
			orgID).Scan(&required); err != nil {
			return fmt.Errorf("read if-match setting: %w", err)
		}
		if required {
			return ErrPreconditionRequired
		}
		return nil
	}
	switch {
	case *req.IfMatch == before.Version, *req.IfMatch == api.IfMatchAny:
		return nil
	case *req.IfMatch > before.Version || *req.IfMatch < 1:
		return ErrPreconditionFailed
	case req.Templates != nil:
		// The template list is replaced as a whole (WP-710): only the
		// current version is a valid base.
		return ErrPreconditionFailed
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT field_name FROM ci_change
		WHERE ci_id = $1 AND ci_version > $2 AND field_name = ANY($3) ORDER BY field_name`,
		before.ID, *req.IfMatch, requestedFields(req))
	if err != nil {
		return fmt.Errorf("read changes since if-match: %w", err)
	}
	fields, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read changes since if-match: %w", err)
	}
	if len(fields) > 0 {
		return &ConflictError{Fields: fields, Current: before.Version}
	}
	return nil
}

// dropOverriddenFields removes from an automated write every field with a
// manual override (OVR-01, REC-03). The central write decision runs before
// the write; a manual change that committed in between holds its override
// now, and the row lock of the update keeps it so until the commit (ABN-03:
// discovery during an edit never overwrites the manual value).
func dropOverriddenFields(ctx context.Context, tx pgx.Tx, ciID string, req *UpdateRequest) error {
	rows, err := tx.Query(ctx, `SELECT field_name FROM ci_field_value WHERE ci_id = $1 AND override_at IS NOT NULL`, ciID)
	if err != nil {
		return fmt.Errorf("read overrides: %w", err)
	}
	fields, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read overrides: %w", err)
	}
	columns := map[string]**string{
		"name": &req.Name, "status": &req.Status, "client_id": &req.ClientID, "location_id": &req.LocationID,
		"manufacturer": &req.Manufacturer, "model": &req.Model, "serial_number": &req.SerialNumber,
		"hardware_uuid": &req.HardwareUUID, "management_ip": &req.ManagementIP, "primary_mac": &req.PrimaryMAC,
		"hostname": &req.Hostname, "fqdn": &req.FQDN, "os_name": &req.OSName, "os_version": &req.OSVersion,
		"firmware_version": &req.FirmwareVersion, "sys_object_id": &req.SysObjectID,
	}
	for _, field := range fields {
		if column, ok := columns[field]; ok {
			*column = nil
			continue
		}
		if _, ok := req.Attributes[field]; ok {
			attributes := make(map[string]any, len(req.Attributes))
			for k, v := range req.Attributes {
				if k != field {
					attributes[k] = v
				}
			}
			req.Attributes = attributes
		}
	}
	return nil
}

// requestedFields lists the ci_change field names a PATCH writes.
func requestedFields(req *UpdateRequest) []string {
	var out []string
	for name, set := range map[string]bool{
		"name": req.Name != nil, "status": req.Status != nil, "client_id": req.ClientID != nil,
		"location_id": req.LocationID != nil, "manufacturer": req.Manufacturer != nil, "model": req.Model != nil,
		"serial_number": req.SerialNumber != nil, "hardware_uuid": req.HardwareUUID != nil,
		"management_ip": req.ManagementIP != nil, "primary_mac": req.PrimaryMAC != nil, "hostname": req.Hostname != nil,
		"fqdn": req.FQDN != nil, "os_name": req.OSName != nil, "os_version": req.OSVersion != nil,
		"firmware_version": req.FirmwareVersion != nil, "sys_object_id": req.SysObjectID != nil,
		"attributes": req.Attributes != nil, "discovery_source": req.DiscoverySource != nil,
		"tags": req.LegacyTags != nil,
	} {
		if set {
			out = append(out, name)
		}
	}
	return out
}

// uniqueIndexError turns a violated unique attribute index (DB-04) into
// the conflict of the attribute: a concurrent write took the value after
// the service's check. PostgreSQL leaves out the key of a table with row
// level security, so uniqueField names the attribute by the index.
func uniqueIndexError(pgErr *pgconn.PgError) error {
	return &UniqueError{Fields: []string{"attributes"}, index: pgErr.ConstraintName}
}

// uniqueField names the attribute of a violated attribute index: the
// index name carries the definition id (idx_attr_<32 hex>_u, E-04).
func (r *PGRepository) uniqueField(ctx context.Context, orgID string, err error) error {
	var ue *UniqueError
	if !errors.As(err, &ue) || ue.index == "" {
		return err
	}
	// The index of a rebuild carries the suffix until it takes the name of
	// the old one (citype.DDLJob.Ensure).
	hexID := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(ue.index, citype.IndexPrefix), citype.RebuildSuffix), "_u")
	if len(hexID) != 32 {
		return err
	}
	defID := hexID[0:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:]
	// The violation aborted the writing transaction; when the caller's
	// transaction carried it (the max_cis admission of Restore, WP-835),
	// the lookup must not join it and reads in an own one.
	var key string
	if lerr := database.WithRequestTenant(database.WithoutTx(ctx), r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT key FROM attribute_definition WHERE id = $1`, defID).Scan(&key)
	}); lerr == nil {
		ue.Fields = []string{key}
	}
	return err
}

// UniqueConflicts implements UniqueChecker: the unique attributes of the
// CI type, the inheritable ones of its ancestors and the global ones whose
// value another active CI of the organization (scope org) or of the same
// client (scope client) holds. A type definition with inheritable compares
// with the CIs of the subtree of its type, one without only with the CIs
// of the type (MET-06, WP-708). Soft-deleted CIs do not count (MET-15).
//
// The check covers the CIs the caller does not see (F-421): for a caller
// restricted in the client or site scope it runs in an own transaction,
// which only reads, with the organization-wide scope of the caller's
// organization (database.OrgWideScope, app.org_id stays), so the value of
// a CI of another client or site is found also while the unique index
// does not exist yet (DB-04). It returns only the keys of the attributes,
// never the other CI. When ctx carries the caller's transaction, the
// check also runs in it, so a CI written earlier in that transaction
// counts as well.
func (r *PGRepository) UniqueConflicts(ctx context.Context, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) ([]string, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return nil, fmt.Errorf("encode attributes: %w", err)
	}
	var keys []string
	check := func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT d.key FROM attribute_definition d
			WHERE d.is_unique
			  AND (d.ci_type_id = $2::uuid OR d.ci_type_id IS NULL
			       OR (d.inheritable AND d.ci_type_id IN (SELECT a.id FROM ci_type_ancestors($2::uuid) a)))
			  AND (d.organization_id = $1::uuid OR d.organization_id IS NULL)
			  AND $5::jsonb ->> d.key IS NOT NULL
			  AND EXISTS (
				SELECT 1 FROM ci c
				WHERE c.organization_id = $1::uuid AND c.deleted_at IS NULL
				  AND ($4::uuid IS NULL OR c.id <> $4::uuid)
				  AND (d.ci_type_id IS NULL
				       OR (NOT d.inheritable AND c.ci_type_id = d.ci_type_id)
				       OR (d.inheritable AND c.ci_type_id IN (SELECT s.id FROM ci_type_subtree(d.ci_type_id) s)))
				  AND (d.unique_scope = 'org' OR c.client_id IS NOT DISTINCT FROM $3::uuid)
				  AND c.attributes ->> d.key = $5::jsonb ->> d.key)
			ORDER BY d.key`, orgID, ciTypeID, nilIfEmpty(clientID), nilIfEmpty(ciID), raw)
		if qerr != nil {
			return fmt.Errorf("check unique attributes: %w", qerr)
		}
		found, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, key := range found {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		return qerr
	}
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok || (scope.Clients.IsAll() && scope.Sites.IsAll()) {
		err = database.WithRequestTenant(ctx, r.pool, orgID, check)
		return keys, err
	}
	if !strings.EqualFold(scope.OrgID, orgID) {
		return nil, fmt.Errorf("%w: %q", database.ErrTenantMismatch, orgID)
	}
	if database.InTx(ctx) {
		if terr := database.WithRequestTenant(ctx, r.pool, orgID, check); terr != nil {
			return nil, terr
		}
	}
	wide := database.OrgWideScope(scope.OrgID, scope.UserID)
	if werr := database.WithTenant(database.WithoutTx(ctx), r.pool, &wide, check); werr != nil {
		return nil, werr
	}
	slices.Sort(keys)
	return keys, nil
}

// checkUniqueBack refuses a CI that comes back with its values (restore,
// unmerge) while another active CI holds a value of one of its unique
// attributes (MET-15, F-429): the check of ci.Service (CheckUnique), run in
// the writing transaction tx, so it holds also while the unique index does
// not exist yet. The caller's transaction rolls back; the CI stays deleted
// or merged.
func (r *PGRepository) checkUniqueBack(ctx context.Context, tx pgx.Tx, item *Item) error {
	return CheckUnique(database.ContextWithTx(ctx, tx), r, item.OrganizationID, item.CITypeID, item.ClientID, item.ID, item.Attributes)
}

// ChangeType implements TypeChanger (CI-11): the CI keeps its row, so its
// relationships, mounts, asset link and history stay; required attributes
// of the target type the CI lacks are reported as findings.
func (r *PGRepository) ChangeType(ctx context.Context, orgID, ciID, typeID, mode string) (*TypeChangeResult, error) {
	if !api.IsUUID(typeID) {
		return nil, ErrUnknownType
	}
	var result *TypeChangeResult
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tc, err := r.changeTypeRow(ctx, tx, orgID, ciID, typeID, mode)
		if err != nil {
			return err
		}
		result = &tc.result
		if !tc.result.Changed {
			return nil
		}
		if changeErr := r.recordTypeChange(ctx, tx, newChangeSet(ChangeKindTypeChanged), orgID, tc); changeErr != nil {
			return changeErr
		}
		if auditErr := r.recordTypeChangeAudit(ctx, tx, tc); auditErr != nil {
			return auditErr
		}
		// EVT-02 has no own type event: ci.changed names ci_type_id.
		return r.recordEvent(ctx, tx, events.CIChanged, tc.result.CI, tc.changedFields())
	})
	if err != nil {
		// A concurrent write the check could not see: the index names the
		// attribute.
		return nil, r.uniqueField(ctx, orgID, err)
	}
	return result, nil
}

// typeChange is the change of the type of a CI row: the result, the type
// before and the keys of both types.
type typeChange struct {
	result         TypeChangeResult
	fromTypeID     string
	fromKey, toKey string
	mode           string
	// attributes is the change of the computed values the target type
	// brought (WP-717); nil when they stayed.
	attributes *ciFieldChange
}

// changeTypeRow changes the type of CI ciID to typeID in tx (CI-11); it
// writes no history, audit entry or event.
func (r *PGRepository) changeTypeRow(ctx context.Context, tx pgx.Tx, orgID, ciID, typeID, mode string) (*typeChange, error) {
	tc := &typeChange{result: TypeChangeResult{Findings: []TypeFinding{}, DroppedTemplates: []citype.CITemplate{}}, mode: mode}
	query := fmt.Sprintf("SELECT %s, %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL FOR UPDATE",
		ciSelectColumns, ciTemplatesColumn)
	before, err := scanCIWithTemplates(tx.QueryRow(ctx, query, ciID, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get ci for type change: %w", err)
	}
	// The target is read under the principal's row level security: a
	// private type of another organization is unknown (TEN-09).
	var required []string
	var active bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(key, name), COALESCE(required_fields, '{}'), is_active FROM ci_type
		WHERE id = $1 AND (organization_id = $2 OR organization_id IS NULL)`, typeID, orgID).Scan(&tc.toKey, &required, &active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnknownType
		}
		return nil, fmt.Errorf("get target type: %w", err)
	}
	if err = tx.QueryRow(ctx, "SELECT COALESCE(key, name) FROM ci_type WHERE id = $1", before.CITypeID).Scan(&tc.fromKey); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get current type: %w", err)
	}
	if mode == TypeChangeDiscovery && !DiscoveryMayReclassify(tc.fromKey, tc.toKey) {
		return nil, ErrReclassificationRefused
	}
	tc.fromTypeID = before.CITypeID
	tc.result.Findings = MissingRequired(required, before.Attributes)
	if before.CITypeID == typeID {
		tc.result.CI = before
		return tc, nil
	}
	// A person cannot choose a deactivated type (CI-01); discovery does
	// not raise generic_device to it either, the CI keeps its type
	// (keepsType, MET-05).
	if !active {
		if mode == TypeChangeDiscovery {
			return nil, ErrTypeInactive
		}
		return nil, ciTypeViolation(ciTypeDeactivated)
	}
	// The values the CI carries fall under the unique attributes of the
	// target type (MET-15, F-431): the check of ci.Service (CheckUnique) in
	// the writing transaction, before the row changes, so it holds also
	// while the unique index does not exist yet. A value another active CI
	// holds is a *UniqueError; the CI keeps its type.
	if uerr := CheckUnique(database.ContextWithTx(ctx, tx), r, orgID, typeID, before.ClientID, ciID, before.Attributes); uerr != nil {
		return nil, uerr
	}
	if _, err = tx.Exec(ctx, `UPDATE ci SET ci_type_id = $3, version = version + 1, updated_at = NOW()
		WHERE id = $1 AND organization_id = $2`, ciID, orgID, typeID); err != nil {
		return nil, locationError(fmt.Errorf("change ci type: %w", err))
	}
	// The templates that do not fit the target type or its chain go in the
	// same transaction (WP-710); the result names them.
	if len(before.Templates) > 0 {
		if tc.result.DroppedTemplates, err = dropForeignTemplates(ctx, tx, ciID, typeID); err != nil {
			return nil, err
		}
	}
	// The definitions of the target type compute the values (WP-717);
	// computed values without a definition there go with their provenance.
	if _, err = r.recomputeRow(ctx, tx, orgID, ciID); err != nil {
		return nil, err
	}
	after, err := scanCIWithTemplates(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s, %s FROM ci WHERE id = $1 AND organization_id = $2",
		ciSelectColumns, ciTemplatesColumn), ciID, orgID))
	if err != nil {
		return nil, fmt.Errorf("get changed ci: %w", err)
	}
	tc.result.CI, tc.result.Changed = after, true
	if !reflect.DeepEqual(before.Attributes, after.Attributes) {
		tc.attributes = &ciFieldChange{Field: "attributes", Old: before.Attributes, New: after.Attributes}
	}
	return tc, nil
}

// dropForeignTemplates removes the template assignments of CI ciID whose
// template's CI type is not typeID or a type of its chain (WP-248, WP-710)
// and returns them in position order.
func dropForeignTemplates(ctx context.Context, tx pgx.Tx, ciID, typeID string) ([]citype.CITemplate, error) {
	rows, err := tx.Query(ctx, `DELETE FROM ci_template ct USING template t
		WHERE ct.ci_id = $1 AND t.id = ct.template_id
			AND t.ci_type_id NOT IN (SELECT c.id FROM ci_type_ancestors($2::uuid) c)
		RETURNING ct.template_id::text, t.key, ct.version, ct.position`, ciID, typeID)
	if err != nil {
		return nil, fmt.Errorf("drop ci templates: %w", err)
	}
	dropped, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (citype.CITemplate, error) {
		var t citype.CITemplate
		serr := row.Scan(&t.TemplateID, &t.Key, &t.Version, &t.Position)
		return t, serr
	})
	if err != nil {
		return nil, fmt.Errorf("drop ci templates: %w", err)
	}
	slices.SortFunc(dropped, func(a, b citype.CITemplate) int { return a.Position - b.Position })
	if dropped == nil {
		dropped = []citype.CITemplate{}
	}
	return dropped, nil
}

// recordTypeChange writes the ci_type_id row of a type change into set.
func (r *PGRepository) recordTypeChange(ctx context.Context, tx pgx.Tx, set changeSet, orgID string, tc *typeChange) error {
	after := tc.result.CI
	change := map[string]any{"ci_type_id": after.CITypeID, "type": tc.toKey, "mode": tc.mode, "findings": tc.result.Findings}
	if err := r.recordCIChange(ctx, tx, set, orgID, after.ID, after.Version, "type_changed", "ci_type_id",
		map[string]any{"ci_type_id": tc.fromTypeID, "type": tc.fromKey}, change); err != nil {
		return err
	}
	if tc.attributes == nil {
		return nil
	}
	// The computed values of the target type belong to the type change.
	return r.recordFieldChanges(ctx, tx, set, orgID, after.ID, after.Version, []ciFieldChange{*tc.attributes})
}

// changedFields names the fields of a type change for ci.changed:
// ci_type_id and, when the computed values changed, attributes.
func (tc *typeChange) changedFields() []string {
	if tc.attributes != nil {
		return []string{"ci_type_id", "attributes"}
	}
	return []string{"ci_type_id"}
}

// recordTypeChangeAudit writes the audit entry ci.type_changed.
func (r *PGRepository) recordTypeChangeAudit(ctx context.Context, tx pgx.Tx, tc *typeChange) error {
	changes := map[string]interface{}{
		"from": tc.fromKey, "to": tc.toKey, "mode": tc.mode, "findings": tc.result.Findings,
	}
	if len(tc.result.DroppedTemplates) > 0 {
		changes["dropped_templates"] = tc.result.DroppedTemplates
	}
	return r.recordAudit(ctx, tx, "ci.type_changed", tc.result.CI, changes)
}

// UpdateRecord implements RecordUpdater (AUD-03, CI-11): the field update
// and the reclassification of one ingest record run in one transaction (the
// context's, else its own) and are recorded as one change set with the
// record's origin, one event ci.changed (and ci.online for a CI it brings
// back from unknown to active) and the audit entries of the update and of
// the type change. A reclassification discovery may not make
// (ErrReclassificationRefused, ErrUnknownType) or a value of a unique attribute
// of the target type refuses (keepsType, F-432) leaves the type as it is.
func (r *PGRepository) UpdateRecord(ctx context.Context, orgID, id string, rec *RecordUpdate) (*RecordUpdateResult, error) {
	result := &RecordUpdateResult{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		w, err := r.updateRow(ctx, tx, orgID, id, &rec.Update)
		if err != nil {
			return err
		}
		item := w.after
		var tc *typeChange
		if rec.TypeID != "" && rec.TypeID != item.CITypeID && api.IsUUID(rec.TypeID) {
			tc, err = r.changeTypeRow(ctx, tx, orgID, id, rec.TypeID, TypeChangeDiscovery)
			switch {
			case keepsType(ctx, err, id, rec.TypeID):
				// The unique check only reads: the transaction goes on.
				tc = nil
			case err != nil:
				return err
			case !tc.result.Changed:
				tc = nil
			default:
				item = tc.result.CI
				if factsErr := loadAssetFacts(ctx, tx, []*Item{item}); factsErr != nil {
					return factsErr
				}
				result.TypeChanged = true
			}
		}
		result.CI = item
		if len(w.changes) == 0 && tc == nil {
			return nil
		}
		result.Changed = true
		kind := w.kind()
		if tc != nil {
			kind = ChangeKindTypeChanged
		}
		set := newChangeSet(kind)
		set.record = &rec.Origin
		if changeErr := r.recordFieldChanges(ctx, tx, set, orgID, id, item.Version, w.changes); changeErr != nil {
			return changeErr
		}
		changed := changedFieldNames(w.changes)
		if tc != nil {
			if typeErr := r.recordTypeChange(ctx, tx, set, orgID, tc); typeErr != nil {
				return typeErr
			}
			for _, field := range tc.changedFields() {
				if !slices.Contains(changed, field) {
					changed = append(changed, field)
				}
			}
		}
		if len(w.changes) > 0 {
			if auditErr := r.recordUpdateAudit(ctx, tx, w); auditErr != nil {
				return auditErr
			}
		}
		if tc != nil {
			if auditErr := r.recordTypeChangeAudit(ctx, tx, tc); auditErr != nil {
				return auditErr
			}
		}
		// One event per record (EVT-02): ci.changed names every changed
		// field, ci_type_id included.
		if eventErr := r.recordEvent(ctx, tx, events.CIChanged, item, changed); eventErr != nil {
			return eventErr
		}
		// A record that brings an unknown CI back to active raises ci.online
		// after it (REC-09, LCY-05, F-268); the status before is the one of
		// the locked row, so a CI that was active already raises none.
		if w.before.Status == StatusUnknown && item.Status == StatusActive {
			return r.recordOnline(ctx, tx, item)
		}
		return nil
	})
	if err != nil {
		return nil, r.uniqueField(ctx, orgID, err)
	}
	return result, nil
}

// recordOnline writes the event ci.online of a CI the ingest brought back
// from unknown to active, like recordEvent; its data are those of the
// offline detector's ci.online (lifecycle.OfflineDetector) without the
// scope interval, which only the detector knows.
func (r *PGRepository) recordOnline(ctx context.Context, tx pgx.Tx, item *Item) error {
	if r.outbox == nil {
		return nil
	}
	data := map[string]any{"id": item.ID, "name": item.Name, "client_id": item.ClientID, "site_id": item.SiteID, "status": item.Status}
	if item.LastSeenAt != nil {
		data["last_seen_at"] = item.LastSeenAt.UTC().Format(time.RFC3339)
	}
	orgID, id := item.OrganizationID, item.ID
	return lastInTx(ctx, func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CIOnline, EntityID: id, Data: data})
		return err
	})
}

// takeLegacyTags moves attributes.tags of a CI to create into LegacyTags
// (E-125): the ingest and the other automated creations pass it in the
// attributes, the API already took it out (Service.create).
func takeLegacyTags(item *Item) error {
	if item.LegacyTags != nil {
		return nil
	}
	rest, legacy, err := ExtractLegacyTags(item.Attributes)
	if err != nil || legacy == nil {
		return err
	}
	if rest == nil {
		rest = map[string]any{}
	}
	item.Attributes, item.LegacyTags = rest, legacy
	return nil
}

// writeCreatedTags assigns the LegacyTags of new CIs (WP-281) and sets
// their Tags; CIs without tags cost no statement.
func writeCreatedTags(ctx context.Context, tx pgx.Tx, orgID string, items []*Item) error {
	var writes []ciTagWrite
	for _, item := range items {
		item.Tags = []string{}
		if item.LegacyTags == nil {
			continue
		}
		keys, names := tagKeys(*item.LegacyTags)
		if len(keys) > 0 {
			writes = append(writes, ciTagWrite{ciID: item.ID, keys: keys, names: names})
			item.Tags = keys
		}
	}
	if len(writes) == 0 {
		return nil
	}
	return writeCITags(ctx, tx, orgID, writes, false)
}

// ciTagWrite is the tag assignment a write gives one CI: the sorted keys
// and the text each key came from.
type ciTagWrite struct {
	ciID        string
	keys, names []string
}

// writeCITags writes the tag assignments of CIs in tx (ci_tag, WP-281):
// catalog tags missing for a key are created with the text as display name
// in de and en, then the CIs get the tags; replace removes the tags of the
// CIs the write does not name. The statement count does not grow with the
// number of CIs (NFR-03). The scope of the rows follows the CI (trigger
// ci_tag_scope).
func writeCITags(ctx context.Context, tx pgx.Tx, orgID string, writes []ciTagWrite, replace bool) error {
	var ciIDs, keys, names, all []string
	for _, w := range writes {
		all = append(all, w.ciID)
		for i, key := range w.keys {
			ciIDs, keys, names = append(ciIDs, w.ciID), append(keys, key), append(names, w.names[i])
		}
	}
	if len(keys) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO tag (organization_id, key, display_name)
			SELECT DISTINCT ON (u.key) $1, u.key, jsonb_build_object('de', u.name, 'en', u.name)
			FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS u(key, name, ord)
			ORDER BY u.key, u.ord
			ON CONFLICT (organization_id, key) DO NOTHING`, orgID, keys, names); err != nil {
			return fmt.Errorf("create catalog tags: %w", err)
		}
	}
	if replace {
		if _, err := tx.Exec(ctx, `DELETE FROM ci_tag ct WHERE ct.ci_id = ANY($1::uuid[]) AND NOT EXISTS (
				SELECT 1 FROM unnest($2::uuid[], $3::text[]) AS u(ci_id, key)
				JOIN tag t ON t.organization_id = $4 AND t.key = u.key
				WHERE u.ci_id = ct.ci_id AND t.id = ct.tag_id)`, all, ciIDs, keys, orgID); err != nil {
			return fmt.Errorf("replace ci tags: %w", err)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	author := tenant.FromContext(ctx).UserID
	if !api.IsUUID(author) {
		author = ""
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ci_tag (organization_id, ci_id, tag_id, created_by)
		SELECT $1, u.ci_id, t.id, NULLIF($4, '')::uuid
		FROM unnest($2::uuid[], $3::text[]) AS u(ci_id, key)
		JOIN tag t ON t.organization_id = $1 AND t.key = u.key
		ON CONFLICT (ci_id, tag_id) DO NOTHING`, orgID, ciIDs, keys, author); err != nil {
		return fmt.Errorf("assign ci tags: %w", err)
	}
	return nil
}

// ─── Tag catalog and tags of a CI (CI-08, WP-753) ─────────────────────────────

// tagColumns are the columns scanTag reads.
const tagColumns = `id::text, organization_id::text, key, display_name, color, is_system, created_at, updated_at`

// tagSortColumn sorts by bytes, as the in-memory store and the keyset
// cursor compare.
const tagSortColumn = `key COLLATE "C"`

func scanTag(row ciScanner) (*Tag, error) {
	var tag Tag
	var names []byte
	if err := row.Scan(&tag.ID, &tag.OrganizationID, &tag.Key, &names, &tag.Color, &tag.IsSystem, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTagNotFound
		}
		return nil, fmt.Errorf("scan tag: %w", err)
	}
	if err := json.Unmarshal(names, &tag.DisplayName); err != nil {
		return nil, fmt.Errorf("decode tag display name: %w", err)
	}
	return &tag, nil
}

// recordTagAudit writes the audit entry of a catalog change (tag.created,
// tag.updated, tag.deleted, tag.merged) in tx.
func (r *PGRepository) recordTagAudit(ctx context.Context, tx pgx.Tx, orgID, action, tagID string, changes map[string]any) error {
	if r.recorder == nil {
		return nil
	}
	t := tenant.FromContext(ctx)
	actorType := "system"
	if strings.TrimSpace(t.UserID) != "" {
		actorType = "user"
	}
	entry := audit.Entry{OrganizationID: orgID, ActorID: strings.TrimSpace(t.UserID), ActorType: actorType,
		Action: action, ResourceType: "tag", ResourceID: tagID, Changes: changes}
	return lastInTx(ctx, func() error {
		if _, err := r.recorder.Record(ctx, tx, entry); err != nil {
			return fmt.Errorf("record audit: %w", err)
		}
		return nil
	})
}

// lockTag reads a tag of the organization FOR UPDATE.
func lockTag(ctx context.Context, tx pgx.Tx, orgID, id string) (*Tag, error) {
	if !api.IsUUID(id) {
		return nil, ErrTagNotFound
	}
	return scanTag(tx.QueryRow(ctx, "SELECT "+tagColumns+" FROM tag WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, id))
}

// ListTags implements TagStore: the tags sorted by key and id, one row more
// than the page limit for the keyset cursor.
func (r *PGRepository) ListTags(ctx context.Context, orgID string, page api.PaginationParams) ([]Tag, int, error) {
	items := make([]Tag, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		pos := 2
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM tag WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count tags: %w", err)
		}
		if page.Cursor != nil {
			key, id, posErr := tagPosition(page.Cursor)
			if posErr != nil {
				return posErr
			}
			where += " AND " + api.KeysetClause(tagSortColumn, "text", tagDir, pos)
			args = append(args, key, id)
			pos += 2
		}
		tail := fmt.Sprintf(" ORDER BY %s ASC, id ASC LIMIT $%d", tagSortColumn, pos)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", pos+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, "SELECT "+tagColumns+" FROM tag WHERE "+where+tail, args...)
		if err != nil {
			return fmt.Errorf("list tags: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			tag, scanErr := scanTag(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, *tag)
		}
		return rows.Err()
	})
	return items, total, err
}

// GetTag implements TagStore.
func (r *PGRepository) GetTag(ctx context.Context, orgID, id string) (*Tag, error) {
	if !api.IsUUID(id) {
		return nil, ErrTagNotFound
	}
	var tag *Tag
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		tag, err = scanTag(tx.QueryRow(ctx, "SELECT "+tagColumns+" FROM tag WHERE organization_id = $1 AND id = $2", orgID, id))
		return err
	})
	return tag, err
}

// tagWriteError maps the constraint violations of a catalog write: a taken
// key is a conflict on key (409); a CHECK the service checked before is
// 422.
func tagWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505":
		return &api.ConflictError{Field: "key", Message: "a tag with this key exists"}
	case "23514":
		return &ValidationError{Violations: []Violation{{Field: "display_name", Detail: "violates the rules of the catalog"}}}
	}
	return err
}

// CreateTag implements TagStore: a new catalog tag with the audit entry
// tag.created.
func (r *PGRepository) CreateTag(ctx context.Context, tag *Tag) error {
	names, err := json.Marshal(tag.DisplayName)
	if err != nil {
		return fmt.Errorf("encode tag display name: %w", err)
	}
	return database.WithRequestTenant(ctx, r.pool, tag.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, insErr := scanTag(tx.QueryRow(ctx, `INSERT INTO tag (organization_id, key, display_name, color)
			VALUES ($1, $2, $3, $4) RETURNING `+tagColumns, tag.OrganizationID, tag.Key, names, tag.Color))
		if insErr != nil {
			return tagWriteError(insErr)
		}
		*tag = *created
		return r.recordTagAudit(ctx, tx, tag.OrganizationID, "tag.created", tag.ID, map[string]any{"after": created})
	})
}

// UpdateTag implements TagStore: display_name and color, with the audit
// entry tag.updated.
func (r *PGRepository) UpdateTag(ctx context.Context, orgID, id string, req *UpdateTagRequest) (*Tag, error) {
	var tag *Tag
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := lockTag(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		after := *before
		req.apply(&after)
		names, err := json.Marshal(after.DisplayName)
		if err != nil {
			return fmt.Errorf("encode tag display name: %w", err)
		}
		tag, err = scanTag(tx.QueryRow(ctx, `UPDATE tag SET display_name = $3, color = $4
			WHERE organization_id = $1 AND id = $2 RETURNING `+tagColumns, orgID, id, names, after.Color))
		if err != nil {
			return tagWriteError(err)
		}
		return r.recordTagAudit(ctx, tx, orgID, "tag.updated", id, map[string]any{"before": before, "after": tag})
	})
	return tag, err
}

// tagInUse reports ErrTagInUse when tag id is assigned to a CI or a ticket
// (ticket_tag, WP-764) (409). The repository checks it itself: the foreign
// key violation of ci_tag or ticket_tag would be 422 through
// api.ClassifyDBError. Rows outside the tenant scope are invisible; the
// foreign key catches them at the DELETE (deleteTagRow).
func tagInUse(ctx context.Context, tx pgx.Tx, id string) error {
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_tag WHERE tag_id = $1)
		OR EXISTS (SELECT 1 FROM ticket_tag WHERE tag_id = $1)`, id).Scan(&used); err != nil {
		return fmt.Errorf("check tag use: %w", err)
	}
	if used {
		return ErrTagInUse
	}
	return nil
}

// deleteTagRow deletes the row of tag id; a remaining assignment (one the
// tenant scope hides) is ErrTagInUse.
func deleteTagRow(ctx context.Context, tx pgx.Tx, orgID, id string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM tag WHERE organization_id = $1 AND id = $2`, orgID, id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrTagInUse
		}
		return fmt.Errorf("delete tag: %w", err)
	}
	return nil
}

// DeleteTag implements TagStore: a tag no CI carries and that is no
// system tag, with the audit entry tag.deleted.
func (r *PGRepository) DeleteTag(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := lockTag(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if tag.IsSystem {
			return ErrSystemTag
		}
		if useErr := tagInUse(ctx, tx, id); useErr != nil {
			return useErr
		}
		if delErr := deleteTagRow(ctx, tx, orgID, id); delErr != nil {
			return delErr
		}
		return r.recordTagAudit(ctx, tx, orgID, "tag.deleted", id, map[string]any{"before": tag})
	})
}

// manualTagChange is the UpdateRequest of a manual tag change of a CI:
// the keys, the author of the context, If-Match (API-07).
func manualTagChange(ctx context.Context, keys []string, ifMatch *int64) UpdateRequest {
	list := slices.Clone(keys)
	return UpdateRequest{LegacyTags: &list, IfMatch: ifMatch, Manual: &ManualChange{Author: tenant.FromContext(ctx).UserID}}
}

// ReplaceCITags implements TagStore (PUT /cis/{id}/tags): the keys must be
// catalog tags (422 per unknown key; FOR SHARE keeps them until the
// commit); the write is a manual update of the CI with ci_change (field
// tags), the audit entry ci.updated and the event ci.changed, no override.
func (r *PGRepository) ReplaceCITags(ctx context.Context, orgID, ciID string, keys []string, ifMatch *int64) (*Item, error) {
	sorted, violations := catalogKeys(keys)
	var item *Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT key FROM tag WHERE organization_id = $1 AND key = ANY($2) FOR SHARE`, orgID, sorted)
		if err != nil {
			return fmt.Errorf("read catalog tags: %w", err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read catalog tags: %w", err)
		}
		known := make(map[string]bool, len(found))
		for _, key := range found {
			known[key] = true
		}
		violations = append(violations, unknownTags(keys, known)...)
		if len(violations) > 0 {
			return &ValidationError{Violations: violations}
		}
		req := manualTagChange(ctx, sorted, ifMatch)
		w, err := r.updateRow(ctx, tx, orgID, ciID, &req)
		if err != nil {
			return err
		}
		item = w.after
		return r.recordUpdate(ctx, tx, orgID, ciID, w)
	})
	return item, err
}

// ticketTagEvent is the data of the ticket.updated of a tag merge (WP-764):
// the frame of the ticket (id, client_id, site_id) and the changed field
// tags, like the events of ticket.PGRepository.
type ticketTagEvent struct {
	ID            string   `json:"id"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	Change        string   `json:"change"`
	ChangedFields []string `json:"changed_fields"`
}

// mergeTicketTags moves the ticket assignments of tag sourceID to tag
// intoID (ticket_tag, WP-764): a duplicate drops; every ticket concerned
// gets updated_at and one ticket.updated with changed_fields ["tags"]. It
// returns the number of tickets.
func (r *PGRepository) mergeTicketTags(ctx context.Context, tx pgx.Tx, orgID, sourceID, intoID string) (int, error) {
	rows, err := tx.Query(ctx, `UPDATE ticket SET updated_at = NOW()
		WHERE organization_id = $1 AND id IN (SELECT ticket_id FROM ticket_tag WHERE tag_id = $2)
		RETURNING id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, '')`, orgID, sourceID)
	if err != nil {
		return 0, fmt.Errorf("read tagged tickets: %w", err)
	}
	tickets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ticketTagEvent, error) {
		ev := ticketTagEvent{Change: "updated", ChangedFields: []string{"tags"}}
		return ev, row.Scan(&ev.ID, &ev.ClientID, &ev.SiteID)
	})
	if err != nil {
		return 0, fmt.Errorf("read tagged tickets: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE ticket_tag SET tag_id = $2 WHERE tag_id = $1
		AND NOT EXISTS (SELECT 1 FROM ticket_tag d WHERE d.ticket_id = ticket_tag.ticket_id AND d.tag_id = $2)`, sourceID, intoID); err != nil {
		return 0, fmt.Errorf("move ticket tag assignments: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM ticket_tag WHERE tag_id = $1`, sourceID); err != nil {
		return 0, fmt.Errorf("drop duplicate ticket tag assignments: %w", err)
	}
	if r.outbox == nil || len(tickets) == 0 {
		return len(tickets), nil
	}
	slices.SortFunc(tickets, func(a, b ticketTagEvent) int { return strings.Compare(a.ID, b.ID) })
	es := make([]*events.Event, len(tickets))
	for i := range tickets {
		es[i] = &events.Event{OrgID: orgID, Type: events.TicketUpdated, EntityID: tickets[i].ID, Data: &tickets[i]}
	}
	return len(tickets), lastInTx(ctx, func() error {
		_, appendErr := r.outbox.AppendMany(ctx, tx, es)
		return appendErr
	})
}

// taggedCI is a CI of a tag merge with its tag keys.
type taggedCI struct {
	id   string
	keys []string
}

// MergeTag implements TagStore (POST /tags/{id}/merge): in one transaction
// every live CI with the source tag gets the target tag instead, as a
// manual update with history, audit entry ci.updated and event ci.changed
// (version + 1); the assignments of deleted CIs move without history; a
// duplicate drops. The source tag is deleted, the audit entry tag.merged
// records both tags. A system tag is no source (409).
func (r *PGRepository) MergeTag(ctx context.Context, orgID, sourceID, intoID string) (*TagMergeResult, error) {
	result := &TagMergeResult{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		source, err := lockTag(ctx, tx, orgID, sourceID)
		if err != nil {
			return err
		}
		into, err := lockTag(ctx, tx, orgID, intoID)
		if errors.Is(err, ErrTagNotFound) {
			return &ValidationError{Violations: []Violation{{Field: "into_id", Detail: "unknown tag"}}}
		}
		if err != nil {
			return err
		}
		if source.IsSystem {
			return ErrSystemTag
		}
		// The live CIs with the source tag, locked in id order, with their
		// keys.
		rows, err := tx.Query(ctx, `SELECT ci.id::text, COALESCE((SELECT array_agg(t.key ORDER BY t.key)
				FROM ci_tag x JOIN tag t ON t.id = x.tag_id WHERE x.ci_id = ci.id), '{}')
			FROM ci WHERE ci.organization_id = $1 AND ci.deleted_at IS NULL
				AND EXISTS (SELECT 1 FROM ci_tag ct WHERE ct.ci_id = ci.id AND ct.tag_id = $2)
			ORDER BY ci.id FOR UPDATE OF ci`, orgID, sourceID)
		if err != nil {
			return fmt.Errorf("read tagged cis: %w", err)
		}
		cis, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (taggedCI, error) {
			var c taggedCI
			return c, row.Scan(&c.id, &c.keys)
		})
		if err != nil {
			return fmt.Errorf("read tagged cis: %w", err)
		}
		anyVersion := api.IfMatchAny
		for _, c := range cis {
			keys := slices.DeleteFunc(c.keys, func(k string) bool { return k == source.Key })
			if !slices.Contains(keys, into.Key) {
				keys = append(keys, into.Key)
			}
			slices.Sort(keys)
			req := manualTagChange(ctx, keys, &anyVersion)
			w, uerr := r.updateRow(ctx, tx, orgID, c.id, &req)
			if uerr != nil {
				return uerr
			}
			if recErr := r.recordUpdate(ctx, tx, orgID, c.id, w); recErr != nil {
				return recErr
			}
			result.CIs = append(result.CIs, w.after)
		}
		result.Moved = len(cis)
		// Deleted CIs keep their tags for a restore: their rows move as
		// they are; a duplicate drops.
		if _, err = tx.Exec(ctx, `UPDATE ci_tag SET tag_id = $2 WHERE tag_id = $1
			AND NOT EXISTS (SELECT 1 FROM ci_tag d WHERE d.ci_id = ci_tag.ci_id AND d.tag_id = $2)`, sourceID, intoID); err != nil {
			return fmt.Errorf("move tag assignments: %w", err)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM ci_tag WHERE tag_id = $1`, sourceID); err != nil {
			return fmt.Errorf("drop duplicate tag assignments: %w", err)
		}
		tickets, err := r.mergeTicketTags(ctx, tx, orgID, sourceID, intoID)
		if err != nil {
			return err
		}
		if delErr := deleteTagRow(ctx, tx, orgID, sourceID); delErr != nil {
			return delErr
		}
		result.Tag = into
		return r.recordTagAudit(ctx, tx, orgID, "tag.merged", sourceID, map[string]any{
			"source": source, "into": into, "cis": result.Moved, "tickets": tickets,
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
