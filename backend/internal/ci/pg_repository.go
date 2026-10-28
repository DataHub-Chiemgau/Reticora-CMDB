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
	COALESCE(merged_into_id::text, '')
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool     *pgxpool.Pool
	recorder audit.TxRecorder
	outbox   *events.Outbox
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
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("encode event data: %w", err)
		}
		m := map[string]any{}
		if err = json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("encode event data: %w", err)
		}
		m["changed_fields"] = changedFields
		data = m
	}
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

// attributeFilterOf checks that an attribute may be filtered (API-02) and
// returns its filter kind and the CI types the filter is restricted to (nil
// for none). The definitions of a CI's type decide (WP-566, F-216): a
// definition of the type and the global ones (of the organization and the
// system; row level security hides those of other organizations) apply to
// it, and the attribute is searchable for the type when one of them is.
// types are the CI types the query names (named), else every type
// counts. The attribute must be defined for one of them (else 422) and
// searchable for one (else 400 with a hint); the searchable definitions
// give the kind, and when they compare differently (integer in one type,
// text in another) the filter is 400 with the hint to name the type. The
// hits are restricted to the types the attribute is searchable for unless
// a searchable global definition covers every type or every named type is
// one of them, so a filter on a type definition keeps the query shape that
// its index (partial on the type, WP-435) serves.
func attributeFilterOf(ctx context.Context, tx pgx.Tx, f api.Filter, key string, types []string, named bool) (kind string, restrict []string, err error) {
	query, args := `SELECT COALESCE(ci_type_id::text, ''), data_type, searchable FROM attribute_definition WHERE key = $1`, []any{key}
	if named {
		query += ` AND (ci_type_id IS NULL OR ci_type_id::text = ANY($2::text[]))`
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
		kinds, typed     []string
	)
	if _, err = pgx.ForEachRow(rows, []any{&typeID, &dataType, &searchable}, func() error {
		defined = true
		if !searchable {
			return nil
		}
		if k := fieldmeta.FilterKind(dataType); !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
		if typeID == "" {
			global = true
		} else {
			typed = append(typed, typeID)
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
	if global || (named && len(typed) == len(types)) {
		return kinds[0], nil, nil
	}
	return kinds[0], typed, nil
}

// List returns paginated CIs filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
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
		if filter.TypeID != "" {
			whereParts = append(whereParts, fmt.Sprintf("ci_type_id = $%d", argPos))
			args = append(args, filter.TypeID)
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
		orgNamed := false
		types, typesNamed := filter.namedTypes()
		for _, f := range filter.Filters {
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
		var pageErr error
		if len(whereParts) == 1 && sortColumn == "created_at" {
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
		// serve it).
		if page.Cursor == nil && len(items) < page.Limit && (len(items) > 0 || page.Offset == 0) {
			total = page.Offset + len(items)
		} else if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ci WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count cis: %w", err)
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

// GetByID retrieves a single CI by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	var item *Item

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL", ciSelectColumns)
		var err error
		item, err = scanCI(tx.QueryRow(ctx, query, id, orgID))
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
// organization and keeps the requested ones; beyond it, sending and
// planning the id array costs more than that read at the reference size of
// NFR-10 (10,000 objects). A larger organization pays one more read of its
// CIs, as the traversal that reached so many CIs reads all its edges.
const nodeOrgReadAbove = 2000

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

// Create inserts a new CI.
func (r *PGRepository) Create(ctx context.Context, item *Item) error {
	err := database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $17, $18, $19, $20,
				$21, $22, $23, $24, $25
			)
			RETURNING id::text, COALESCE(site_id::text, ''), COALESCE(room_id::text, ''),
				COALESCE(location_id::text, ''), health, version, schema_version, created_at, updated_at
		`

		if err := checkCITypes(ctx, tx, item.OrganizationID, []*Item{item}); err != nil {
			return err
		}
		if err := checkParents(ctx, tx, item.OrganizationID, []*Item{item}); err != nil {
			return err
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
			return locationError(fmt.Errorf("create ci: %w", err))
		}

		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if err := loadAssetFacts(ctx, tx, []*Item{item}); err != nil {
			return err
		}
		if err := r.recordCIChange(ctx, tx, newChangeSet(ChangeKindCreated), item.OrganizationID, item.ID, item.Version, "create", "", nil, item); err != nil {
			return err
		}
		if err := r.recordAudit(ctx, tx, "ci.created", item, map[string]interface{}{"after": item}); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CICreated, item, nil)
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
// deadlocking on the unique indexes.
func (r *PGRepository) CreateMany(ctx context.Context, orgID string, items []*Item) (CreateManyResult, error) {
	var res CreateManyResult
	if len(items) == 0 {
		return res, nil
	}
	for _, item := range items {
		if item.OrganizationID != orgID {
			return res, fmt.Errorf("create cis: item of organization %q in a batch of %q", item.OrganizationID, orgID)
		}
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkCITypes(ctx, tx, orgID, items); err != nil {
			return err
		}
		if err := checkParents(ctx, tx, orgID, items); err != nil {
			return err
		}
		created, refused, err := insertCIs(ctx, tx, orgID, items)
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
// created items and the positions of the refused ones.
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
			SELECT gen_random_uuid() AS id, r.*
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
			ORDER BY serial_number, hardware_uuid, primary_mac, ord
			ON CONFLICT DO NOTHING
			RETURNING id, COALESCE(site_id::text, '') AS site_id, COALESCE(room_id::text, '') AS room_id,
				COALESCE(location_id::text, '') AS location_id, health, version, schema_version, created_at, updated_at
		)
		SELECT input.ord, COALESCE(ins.id::text, ''), COALESCE(ins.site_id, ''), COALESCE(ins.room_id, ''),
			COALESCE(ins.location_id, ''), COALESCE(ins.health, ''), COALESCE(ins.version, 0),
			COALESCE(ins.schema_version, 0), ins.created_at, ins.updated_at
		FROM input LEFT JOIN ins ON ins.id = input.id
		ORDER BY input.ord`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ord int64
		var id, siteID, roomID, locationID, health string
		var version, schemaVersion int64
		var createdAt, updatedAt *time.Time
		if err := rows.Scan(&ord, &id, &siteID, &roomID, &locationID, &health, &version, &schemaVersion,
			&createdAt, &updatedAt); err != nil {
			return nil, nil, err
		}
		i := int(ord) - 1
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
	return created, refused, rows.Err()
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
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	var item *Item

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		w, err := r.updateRow(ctx, tx, orgID, id, &req)
		if err != nil {
			return err
		}
		item = w.after
		if len(w.changes) == 0 {
			return nil
		}
		set := newChangeSet(w.kind())
		if err := r.recordFieldChanges(ctx, tx, set, orgID, id, item.Version, w.changes); err != nil {
			return err
		}
		if err := r.recordUpdateAudit(ctx, tx, w); err != nil {
			return err
		}
		return r.recordEvent(ctx, tx, events.CIChanged, item, changedFieldNames(w.changes))
	})

	return item, r.uniqueField(ctx, orgID, err)
}

// rowUpdate is the UPDATE of a CI row: the row before and after it and the
// historized fields it changed (diffCI).
type rowUpdate struct {
	before, after *Item
	changes       []ciFieldChange
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
	beforeQuery := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL FOR UPDATE", ciSelectColumns)
	before, err := scanCI(tx.QueryRow(ctx, beforeQuery, id, orgID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get ci before update: %w", err)
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
		setClauses = append(setClauses, fmt.Sprintf("last_seen_at = $%d", argPos))
		args = append(args, parsed)
		argPos++
	}

	if len(setClauses) == 0 {
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
		"UPDATE ci SET %s WHERE id = $1 AND organization_id = $%d AND deleted_at IS NULL RETURNING %s",
		strings.Join(setClauses, ", "),
		argPos,
		ciSelectColumns,
	)
	args = append(args, orgID)

	item, err = scanCI(tx.QueryRow(ctx, query, args...))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, locationError(fmt.Errorf("update ci: %w", err))
	}

	if err := loadAssetFacts(ctx, tx, []*Item{item}); err != nil {
		return nil, err
	}
	w := &rowUpdate{before: before, after: item, changes: diffCI(before, item)}
	if len(w.changes) > 0 && req.Manual != nil {
		if err := recordManualOverrides(ctx, tx, orgID, before, item, &req); err != nil {
			return nil, err
		}
	}
	return w, nil
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
			if err := rows.Scan(
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
			); err != nil {
				return fmt.Errorf("scan ci change: %w", err)
			}
			if err := json.Unmarshal(oldValue, &change.OldValue); err != nil {
				return fmt.Errorf("decode old ci change value: %w", err)
			}
			if err := json.Unmarshal(newValue, &change.NewValue); err != nil {
				return fmt.Errorf("decode new ci change value: %w", err)
			}
			changes = append(changes, change)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate ci changes: %w", err)
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
	); err != nil {
		return nil, err
	}

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
// write comes from (UpdateRecord).
type changeSet struct {
	id, kind string
	record   *RecordOrigin
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

// recordCIChange writes one field row of the change set set.
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
			observed_at
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`, orgID, ciID, who.actorID, changeType, nilIfEmpty(fieldName), oldJSON, newJSON, version,
		set.kind, set.id, who.actorUserID, who.source, who.requestID, who.correlationID, who.collectorID, who.observedAt)
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
		       COALESCE(supplier, ''), COALESCE(invoice_number, '')
		FROM asset WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("load asset facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a AssetFacts
		if err := rows.Scan(&a.ID, &a.AssetTag, &a.Name, &a.SerialNumber, &a.LocationID, &a.LifecycleState, &a.Status,
			&a.Barcode, &a.RFIDTag, &a.PurchaseDate, &a.WarrantyEnd, &a.Supplier, &a.InvoiceNumber); err != nil {
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
// The asset link is structural, not an observed value.
var derivedFields = map[string]bool{"site_id": true, "room_id": true, "attributes": true, "asset_id": true, "parent_ci_id": true}

// recordManualOverrides gives every field a manual change touched a
// protected override, so discovery never overwrites it (OVR-01, CH9): the
// changed CI columns by name, the attributes per changed top-level member
// (a removed member is recorded with a null value).
func recordManualOverrides(ctx context.Context, tx pgx.Tx, orgID string, before, after *Item, req *UpdateRequest) error {
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
	for _, key := range changedKeys(before.Attributes, after.Attributes) {
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
	var key string
	if lerr := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT key FROM attribute_definition WHERE id = $1`, defID).Scan(&key)
	}); lerr == nil {
		ue.Fields = []string{key}
	}
	return err
}

// UniqueConflicts implements UniqueChecker: the unique attributes of the
// CI type and the global ones whose value another active CI of the
// organization (scope org) or of the same client (scope client) holds.
// Soft-deleted CIs do not count (MET-15).
func (r *PGRepository) UniqueConflicts(ctx context.Context, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) ([]string, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return nil, fmt.Errorf("encode attributes: %w", err)
	}
	var keys []string
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT d.key FROM attribute_definition d
			WHERE d.is_unique AND (d.ci_type_id = $2::uuid OR d.ci_type_id IS NULL)
			  AND (d.organization_id = $1::uuid OR d.organization_id IS NULL)
			  AND $5::jsonb ->> d.key IS NOT NULL
			  AND EXISTS (
				SELECT 1 FROM ci c
				WHERE c.organization_id = $1::uuid AND c.deleted_at IS NULL
				  AND ($4::uuid IS NULL OR c.id <> $4::uuid)
				  AND (d.ci_type_id IS NULL OR c.ci_type_id = d.ci_type_id)
				  AND (d.unique_scope = 'org' OR c.client_id IS NOT DISTINCT FROM $3::uuid)
				  AND c.attributes ->> d.key = $5::jsonb ->> d.key)
			ORDER BY d.key`, orgID, ciTypeID, nilIfEmpty(clientID), nilIfEmpty(ciID), raw)
		if qerr != nil {
			return fmt.Errorf("check unique attributes: %w", qerr)
		}
		keys, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
		return qerr
	})
	return keys, err
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
		return r.recordEvent(ctx, tx, events.CIChanged, tc.result.CI, []string{"ci_type_id"})
	})
	if err != nil {
		return nil, err
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
}

// changeTypeRow changes the type of CI ciID to typeID in tx (CI-11); it
// writes no history, audit entry or event.
func (r *PGRepository) changeTypeRow(ctx context.Context, tx pgx.Tx, orgID, ciID, typeID, mode string) (*typeChange, error) {
	tc := &typeChange{result: TypeChangeResult{Findings: []TypeFinding{}}, mode: mode}
	query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NULL FOR UPDATE", ciSelectColumns)
	before, err := scanCI(tx.QueryRow(ctx, query, ciID, orgID))
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
	// A person cannot choose a deactivated type (CI-01); discovery
	// raises generic_device to the type it resolved, as it creates CIs.
	if mode == TypeChangeManual && !active {
		return nil, ciTypeViolation(ciTypeDeactivated)
	}
	if _, err = tx.Exec(ctx, `UPDATE ci SET ci_type_id = $3, version = version + 1, updated_at = NOW()
		WHERE id = $1 AND organization_id = $2`, ciID, orgID, typeID); err != nil {
		return nil, locationError(fmt.Errorf("change ci type: %w", err))
	}
	after, err := scanCI(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2", ciSelectColumns), ciID, orgID))
	if err != nil {
		return nil, fmt.Errorf("get changed ci: %w", err)
	}
	tc.result.CI, tc.result.Changed = after, true
	return tc, nil
}

// recordTypeChange writes the ci_type_id row of a type change into set.
func (r *PGRepository) recordTypeChange(ctx context.Context, tx pgx.Tx, set changeSet, orgID string, tc *typeChange) error {
	after := tc.result.CI
	change := map[string]any{"ci_type_id": after.CITypeID, "type": tc.toKey, "mode": tc.mode, "findings": tc.result.Findings}
	return r.recordCIChange(ctx, tx, set, orgID, after.ID, after.Version, "type_changed", "ci_type_id",
		map[string]any{"ci_type_id": tc.fromTypeID, "type": tc.fromKey}, change)
}

// recordTypeChangeAudit writes the audit entry ci.type_changed.
func (r *PGRepository) recordTypeChangeAudit(ctx context.Context, tx pgx.Tx, tc *typeChange) error {
	return r.recordAudit(ctx, tx, "ci.type_changed", tc.result.CI, map[string]interface{}{
		"from": tc.fromKey, "to": tc.toKey, "mode": tc.mode, "findings": tc.result.Findings,
	})
}

// UpdateRecord implements RecordUpdater (AUD-03, CI-11): the field update
// and the reclassification of one ingest record run in one transaction (the
// context's, else its own) and are recorded as one change set with the
// record's origin, one event ci.changed and the audit entries of the update
// and of the type change. A reclassification discovery may not make
// (ErrReclassificationRefused, ErrUnknownType) leaves the type as it is.
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
			case errors.Is(err, ErrReclassificationRefused), errors.Is(err, ErrUnknownType):
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
			changed = append(changed, "ci_type_id")
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
		return r.recordEvent(ctx, tx, events.CIChanged, item, changed)
	})
	if err != nil {
		return nil, r.uniqueField(ctx, orgID, err)
	}
	return result, nil
}
