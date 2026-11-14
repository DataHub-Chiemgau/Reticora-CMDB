package tenantapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
)

// The database purge of an organization after the grace period of its
// deletion (TLC-06, WP-1154). The system job org.purge (hourly) finds the
// organizations whose grace period ended (requested, deletion_due_at
// passed) or whose purge runs (purging), sets purging with the
// operator_audit entry org.deletion.purge_started and deletes every row of
// the organization table by table, children before parents, in batches of
// PurgeBatchSize rows, each in a tenant transaction of its own, through
// org_purge_batch (migration 000356, role reticora_org_purger). Before it
// deletes a table it records the rows and the SHA-256 of their sorted
// keys; the progress lies in organization.settings (org_purge), so a new
// run continues where a crashed one stopped. At the end the row of the
// organization stays as a tombstone (name = id, slug purged-<id>, settings
// {}), the state becomes purged and operator_audit receives
// org.deletion.db_purged with rows and hashes per table, no personal
// value. The vault (org_dek, pii_subject), the object storage, the search
// index, the event stream and the caches follow before that transition
// (purge_external.go, WP-1155).
// The run also reminds the org_admins 7 and 1 day before the end of the
// grace period, once per threshold (notice_sent).

// OrgPurgeKind is the system job of the purge.
const OrgPurgeKind = "org.purge"

// PurgeBatchSize is the number of rows one statement of the purge deletes.
const PurgeBatchSize = 5000

// purgeOperator is the operator id of the purge's operator_audit entries.
const purgeOperator = OrgPurgeKind

// purgeProgressKey is the key of the progress in organization.settings.
const purgeProgressKey = "org_purge"

// maxPurgePasses bounds the passes over every table: the triggers of a
// delete may write rows of the organization again (history, search
// index), which a further pass removes.
const maxPurgePasses = 5

// reminderKind is the kind of the reminders in notice_sent.
const reminderKind = "org.deletion.reminder"

// PurgeTableResult is the purge of one table: the rows before and after,
// the rows deleted (also those triggers wrote in between) and the SHA-256
// of the sorted keys of the rows before.
type PurgeTableResult struct {
	Table      string `json:"table"`
	RowsBefore int64  `json:"rows_before"`
	RowsAfter  int64  `json:"rows_after"`
	Deleted    int64  `json:"deleted"`
	SHA256     string `json:"sha256"`
}

// purgeProgress is organization.settings.org_purge.
type purgeProgress struct {
	Done    []PurgeTableResult `json:"done"`
	Current *PurgeTableResult  `json:"current,omitempty"`
}

// FKEdge is a foreign key between two tables of the purge: Child refers to
// Parent; OnDelete is confdeltype (a no action, r restrict, c cascade, n
// set null, d set default).
type FKEdge struct {
	Child, Parent string
	OnDelete      string
}

// TableCatalog lists the tables the purge empties (export.OrgPurgeTables;
// injected, the export package depends on this one).
type TableCatalog func(ctx context.Context, tx pgx.Tx) ([]string, error)

// DeletionNotifier queues the reminders (notify.Notifier).
type DeletionNotifier interface {
	Notify(ctx context.Context, orgID string, req *notify.Request) (int, error)
}

// Purger runs org.purge.
type Purger struct {
	pool     *pgxpool.Pool
	auditor  *operator.Auditor
	tables   TableCatalog
	notifier DeletionNotifier
	resolver notify.Resolver
	now      func() time.Time
	batch    int
	// afterTable runs after every table of the first pass (tests): an
	// error ends the run like a crash.
	afterTable func(ctx context.Context, table string) error
	// external are the stores outside the tables (WP-1155); nil keeps the
	// vault and touches nothing outside PostgreSQL.
	external *ExternalPurge
}

// NewPurger builds the purge on the application pool with the table
// catalog.
func NewPurger(pool *pgxpool.Pool, tables TableCatalog) *Purger {
	return &Purger{pool: pool, auditor: operator.NewAuditor(pool), tables: tables, now: time.Now, batch: PurgeBatchSize}
}

// WithNotices sets the notifier and the resolver of the reminders; without
// them the run sends none.
func (p *Purger) WithNotices(n DeletionNotifier, r notify.Resolver) *Purger {
	p.notifier, p.resolver = n, r
	return p
}

// WithClock sets the clock of the reminders (tests).
func (p *Purger) WithClock(now func() time.Time) *Purger {
	p.now = now
	return p
}

// WithBatchSize sets the rows per statement (tests).
func (p *Purger) WithBatchSize(n int) *Purger {
	if n > 0 {
		p.batch = n
	}
	return p
}

// WithTableHook runs fn after every table of the first pass (tests).
func (p *Purger) WithTableHook(fn func(ctx context.Context, table string) error) *Purger {
	p.afterTable = fn
	return p
}

// Schedule is the hourly system job.
func (p *Purger) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: OrgPurgeKind, Spec: "41 * * * *", MaxCatchUp: 1, Timeout: 6 * time.Hour, Run: p.Run}
}

// dueOrganization is an organization the run handles.
type dueOrganization struct {
	id          string
	state       string
	requestedAt *time.Time
	requestedBy *string
	dueAt       *time.Time
}

// Run purges the organizations whose grace period ended or whose purge
// runs and reminds those whose grace period ends within seven days; it
// returns the first error after trying all of them.
func (p *Purger) Run(ctx context.Context) error {
	if p.pool == nil {
		return nil
	}
	var due []dueOrganization
	err := database.WithSystem(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, deletion_state, deletion_requested_at, deletion_requested_by::text, deletion_due_at
			FROM organization
			WHERE deletion_state = 'purging'
			   OR (deletion_state = 'requested' AND deletion_due_at <= $1)
			ORDER BY deletion_due_at, id`, p.now().Add(7*24*time.Hour))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o dueOrganization
			if err := rows.Scan(&o.id, &o.state, &o.requestedAt, &o.requestedBy, &o.dueAt); err != nil {
				return err
			}
			due = append(due, o)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("org purge: read due organizations: %w", err)
	}
	var first error
	for i := range due {
		o := &due[i]
		var err error
		switch {
		case o.state == DeletionPurging:
			_, err = p.PurgeOrganization(ctx, o.id)
		case o.dueAt != nil && !o.dueAt.After(p.now()):
			_, err = p.PurgeOrganization(ctx, o.id)
		default:
			err = p.remind(ctx, o)
		}
		if err != nil {
			slog.ErrorContext(ctx, "org purge", "organization_id", o.id, "error", err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// orgScope is the context of a system run of the organization:
// organization-wide, without user.
func orgScope(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(ctx, &scope)
}

// ErrNotPurgeable is the purge of an organization that is neither due nor
// purging.
var ErrNotPurgeable = errors.New("the organization is not due for its purge")

// PurgeOrganization purges one organization: it starts the purge of a due
// request (purging, org.deletion.purge_started), deletes its rows and
// leaves the tombstone (purged, org.deletion.db_purged). It returns the
// results per table.
func (p *Purger) PurgeOrganization(ctx context.Context, orgID string) ([]PurgeTableResult, error) {
	state, err := p.start(ctx, orgID)
	if err != nil {
		return nil, err
	}
	switch state {
	case DeletionPurged:
		return nil, nil
	case DeletionPurging:
	default:
		return nil, ErrNotPurgeable
	}
	results, err := p.purgeRows(ctx, orgID)
	if err != nil {
		return nil, err
	}
	// The vault, the objects, the search index, the event stream and the
	// caches (WP-1155): purged only once all of them succeeded.
	if p.external != nil {
		if _, err := p.purgeExternal(ctx, orgID); err != nil {
			return nil, err
		}
	}
	if err := p.finish(ctx, orgID, results); err != nil {
		return nil, err
	}
	return results, nil
}

// start moves a due request to purging with its operator_audit entry and
// returns the state of the organization.
func (p *Purger) start(ctx context.Context, orgID string) (string, error) {
	var state string
	var dueAt *time.Time
	err := database.WithTenant(ctx, p.pool, func() *database.TenantScope { s := database.OrgWideScope(orgID, ""); return &s }(),
		func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT deletion_state, deletion_due_at FROM organization WHERE id = $1`, orgID).Scan(&state, &dueAt)
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errDeletionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("org purge: read state: %w", err)
	}
	if state != DeletionRequested || dueAt == nil || dueAt.After(p.now()) {
		return state, nil
	}
	err = p.auditor.RecordChange(orgScope(ctx, orgID), orgID, func(ctx context.Context) (*operator.AuditEntry, error) {
		var requestedAt, due time.Time
		var requestedBy *string
		uerr := database.WithRequestTenant(ctx, p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `UPDATE organization SET deletion_state = 'purging', updated_at = now()
				WHERE id = $1 AND deletion_state = 'requested'
				RETURNING deletion_requested_at, deletion_requested_by::text, deletion_due_at`, orgID).Scan(&requestedAt, &requestedBy, &due)
		})
		if uerr != nil {
			return nil, uerr
		}
		details := map[string]any{"requested_at": requestedAt.UTC().Format(time.RFC3339Nano), "due_at": due.UTC().Format(time.RFC3339Nano)}
		if requestedBy != nil {
			details["requested_by"] = *requestedBy
		}
		return &operator.AuditEntry{OperatorID: purgeOperator, OperatorKind: operator.KindSystem, Action: "org.deletion.purge_started",
			TargetOrg: orgID, Status: 200, Details: details}, nil
	})
	if err != nil {
		return "", fmt.Errorf("org purge: start: %w", err)
	}
	return DeletionPurging, nil
}

// readProgress reads the progress of the purge from the organization.
func (p *Purger) readProgress(ctx context.Context, orgID string) (*purgeProgress, error) {
	var raw []byte
	err := database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(settings -> $2, 'null'::jsonb) FROM organization WHERE id = $1`, orgID,
			purgeProgressKey).Scan(&raw)
	})
	if err != nil {
		return nil, err
	}
	var out purgeProgress
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode purge progress: %w", err)
		}
	}
	return &out, nil
}

// saveProgress stores the progress of the purge in the organization.
func (p *Purger) saveProgress(ctx context.Context, orgID string, progress *purgeProgress) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE organization SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), ARRAY[$2], $3::jsonb)
			WHERE id = $1 AND deletion_state = 'purging'`, orgID, purgeProgressKey, raw)
		return err
	})
}

// Order returns the tables of the purge in the order the purge deletes
// them, children before parents, with the foreign keys between them; the
// continuous aggregates follow at the end, after the hypertables they
// aggregate are empty (a refresh in between finds no row to materialize).
func (p *Purger) Order(ctx context.Context, orgID string) ([]string, []FKEdge, error) {
	var tables, aggregates []string
	var edges []FKEdge
	err := database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if tables, err = p.tables(ctx, tx); err != nil {
			return err
		}
		if aggregates, err = continuousAggregates(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT ch.relname::text, pa.relname::text, c.confdeltype::text
			FROM pg_constraint c
			JOIN pg_class ch ON ch.oid = c.conrelid
			JOIN pg_class pa ON pa.oid = c.confrelid
			WHERE c.contype = 'f' AND ch.relnamespace = 'public'::regnamespace AND pa.relnamespace = 'public'::regnamespace
			  AND c.conparentid = 0
			ORDER BY 1, 2, 3`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e FKEdge
			if err := rows.Scan(&e.Child, &e.Parent, &e.OnDelete); err != nil {
				return err
			}
			edges = append(edges, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("org purge: read the tables: %w", err)
	}
	order, _ := purgeOrder(tables, edges)
	return append(order, aggregates...), edges, nil
}

// continuousAggregatesSQL lists the continuous aggregates of public with
// organization_id (metric_sample_1h, probe_result_5m/1h/1d): views whose
// rows lie in materialization hypertables of _timescaledb_internal, which
// no table catalog lists. A refresh only recomputes its window, so the
// older buckets of a purged organization stay unless the purge empties
// them (org_purge_batch resolves the view to its hypertable). Empty
// without TimescaleDB.
const continuousAggregatesSQL = `
SELECT ca.view_name::text FROM timescaledb_information.continuous_aggregates ca
 WHERE ca.view_schema = 'public'
   AND EXISTS (SELECT 1 FROM pg_attribute a
                WHERE a.attrelid = format('%I.%I', ca.view_schema, ca.view_name)::regclass
                  AND a.attname = 'organization_id' AND NOT a.attisdropped)
 ORDER BY 1`

// continuousAggregates reads the continuous aggregates the purge empties.
func continuousAggregates(ctx context.Context, tx pgx.Tx) ([]string, error) {
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('timescaledb_information.continuous_aggregates') IS NOT NULL`).Scan(&present); err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	rows, err := tx.Query(ctx, continuousAggregatesSQL)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// PurgeOrder orders tables children before parents along the foreign keys
// between them (edges; keys to other tables and of a table to itself do
// not order). A cycle of foreign keys is resolved by the keys of it whose
// delete action removes or clears the child rows (cascade, set null, set
// default); what is left of a cycle of restricting keys is ordered by name,
// and the purge deletes those tables row by row (org_purge_rows), each row
// once nothing refers to it any more. It returns the order, the cycles
// found (the tables of each, sorted) and the tables of restricting cycles.
func PurgeOrder(tables []string, edges []FKEdge) (order []string, cycles [][]string, err error) {
	order, cycles = purgeOrder(tables, edges)
	return order, cycles, nil
}

// purgeOrder is PurgeOrder.
func purgeOrder(tables []string, edges []FKEdge) (order []string, cycles [][]string) {
	in := make(map[string]bool, len(tables))
	for _, t := range tables {
		in[t] = true
	}
	sorted := slices.Clone(tables)
	sort.Strings(sorted)
	adj := map[string][]string{}
	for _, e := range edges {
		if in[e.Child] && in[e.Parent] && e.Child != e.Parent && !slices.Contains(adj[e.Child], e.Parent) {
			adj[e.Child] = append(adj[e.Child], e.Parent)
		}
	}
	for k := range adj {
		sort.Strings(adj[k])
	}
	// Tarjan: the components come out parents first along child → parent.
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var comps [][]string
	next := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var comp []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			sort.Strings(comp)
			comps = append(comps, comp)
		}
	}
	for _, t := range sorted {
		if _, seen := index[t]; !seen {
			visit(t)
		}
	}
	for i := len(comps) - 1; i >= 0; i-- {
		comp := comps[i]
		if len(comp) == 1 {
			order = append(order, comp[0])
			continue
		}
		cycles = append(cycles, comp)
		inner, _ := orderCycle(comp, edges)
		order = append(order, inner...)
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i][0] < cycles[j][0] })
	return order, cycles
}

// orderCycle orders the tables of a cycle along its restricting keys
// only (Kahn, children first); the other keys clear or remove the child
// rows themselves. Tables of a cycle of restricting keys follow by name;
// they are returned as stuck.
func orderCycle(comp []string, edges []FKEdge) (order, stuck []string) {
	in := map[string]bool{}
	for _, t := range comp {
		in[t] = true
	}
	children := map[string]int{} // remaining restricting children per parent
	parents := map[string][]string{}
	for _, e := range edges {
		if !in[e.Child] || !in[e.Parent] || e.Child == e.Parent || (e.OnDelete != "a" && e.OnDelete != "r") {
			continue
		}
		if slices.Contains(parents[e.Child], e.Parent) {
			continue
		}
		parents[e.Child] = append(parents[e.Child], e.Parent)
		children[e.Parent]++
	}
	done := map[string]bool{}
	for len(order) < len(comp) {
		pick := ""
		for _, t := range comp {
			if !done[t] && children[t] == 0 {
				pick = t
				break
			}
		}
		if pick == "" {
			// A cycle of restricting keys: the first table left by name.
			for _, t := range comp {
				if !done[t] {
					pick = t
					break
				}
			}
			for _, t := range comp {
				if !done[t] && !slices.Contains(stuck, t) {
					stuck = append(stuck, t)
				}
			}
		}
		done[pick] = true
		order = append(order, pick)
		for _, parent := range parents[pick] {
			children[parent]--
		}
	}
	return order, stuck
}

// purgeRows deletes every row of the organization (first pass with the
// digest per table and the progress, then passes until none is left).
func (p *Purger) purgeRows(ctx context.Context, orgID string) ([]PurgeTableResult, error) {
	order, _, err := p.Order(ctx, orgID)
	if err != nil {
		return nil, err
	}
	progress, err := p.readProgress(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("org purge: read progress: %w", err)
	}
	done := map[string]bool{}
	for _, r := range progress.Done {
		done[r.Table] = true
	}
	for _, table := range order {
		if done[table] {
			continue
		}
		var current *PurgeTableResult
		if progress.Current != nil && progress.Current.Table == table {
			current = progress.Current // the digest of the rows before, from the crashed run
		} else {
			rows, sum, err := p.digest(ctx, orgID, table)
			if err != nil {
				return nil, err
			}
			current = &PurgeTableResult{Table: table, RowsBefore: rows, SHA256: sum}
			progress.Current = current
			if err := p.saveProgress(ctx, orgID, progress); err != nil {
				return nil, fmt.Errorf("org purge: save progress: %w", err)
			}
		}
		deleted, err := p.deleteAll(ctx, orgID, table)
		if err != nil {
			return nil, err
		}
		current.Deleted += deleted
		progress.Done = append(progress.Done, *current)
		progress.Current = nil
		if err := p.saveProgress(ctx, orgID, progress); err != nil {
			return nil, fmt.Errorf("org purge: save progress: %w", err)
		}
		done[table] = true
		if p.afterTable != nil {
			if err := p.afterTable(ctx, table); err != nil {
				return nil, err
			}
		}
	}
	// Further passes remove what the triggers of the deletes wrote.
	index := map[string]int{}
	for i, r := range progress.Done {
		index[r.Table] = i
	}
	for pass := 1; pass < maxPurgePasses; pass++ {
		var total int64
		for _, table := range order {
			n, err := p.deleteAll(ctx, orgID, table)
			if err != nil {
				return nil, err
			}
			if i, ok := index[table]; ok {
				progress.Done[i].Deleted += n
			}
			total += n
		}
		if total == 0 {
			break
		}
	}
	var left []string
	for i := range progress.Done {
		rows, _, err := p.digest(ctx, orgID, progress.Done[i].Table)
		if err != nil {
			return nil, err
		}
		progress.Done[i].RowsAfter = rows
		if rows > 0 {
			left = append(left, progress.Done[i].Table)
		}
	}
	if len(left) > 0 {
		return nil, fmt.Errorf("org purge: rows left in %v", left)
	}
	return progress.Done, nil
}

// digest returns the rows of the organization in table and the SHA-256 of
// their sorted keys, one per line (org_purge_keys).
func (p *Purger) digest(ctx context.Context, orgID, table string) (rows int64, sum string, err error) {
	hash := sha256.New()
	err = database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		keys, qerr := tx.Query(ctx, `SELECT k FROM org_purge_keys($1, $2::regclass) AS k`, orgID, "public."+pgx.Identifier{table}.Sanitize())
		if qerr != nil {
			return qerr
		}
		defer keys.Close()
		for keys.Next() {
			var key string
			if serr := keys.Scan(&key); serr != nil {
				return serr
			}
			hash.Write([]byte(key))
			hash.Write([]byte{'\n'})
			rows++
		}
		return keys.Err()
	})
	if err != nil {
		return 0, "", fmt.Errorf("org purge: digest of %s: %w", table, err)
	}
	return rows, hex.EncodeToString(hash.Sum(nil)), nil
}

// Digest returns the rows of the organization in table and the SHA-256 of
// their sorted keys, as the purge records them before it deletes; the
// organization must be in purging.
func (p *Purger) Digest(ctx context.Context, orgID, table string) (PurgeTableResult, error) {
	rows, sum, err := p.digest(ctx, orgID, table)
	return PurgeTableResult{Table: table, RowsBefore: rows, SHA256: sum}, err
}

// deleteAll deletes the rows of the organization in table in batches, each
// in its own transaction, until a batch deletes none.
func (p *Purger) deleteAll(ctx context.Context, orgID, table string) (int64, error) {
	var total int64
	rel := "public." + pgx.Identifier{table}.Sanitize()
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		err := database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT org_purge_batch($1, $2::regclass, $3)`, orgID, rel, p.batch).Scan(&n)
		})
		var pgErr *pgconn.PgError
		if err != nil && errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// A row is still referenced (a restricting cycle, a key of the
			// table to itself, a cascade into a referenced row): the rows
			// nothing refers to go one by one, the rest in a later pass.
			err = database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT org_purge_rows($1, $2::regclass)`, orgID, rel).Scan(&n)
			})
			if err == nil {
				return total + n, nil
			}
		}
		if err != nil {
			return total, fmt.Errorf("org purge: delete from %s: %w", table, err)
		}
		total += n
		if n == 0 {
			return total, nil
		}
	}
}

// finish leaves the tombstone, sets purged and writes
// org.deletion.db_purged with the results per table in one transaction.
func (p *Purger) finish(ctx context.Context, orgID string, results []PurgeTableResult) error {
	var total int64
	tables := make([]map[string]any, 0, len(results))
	for _, r := range results {
		total += r.RowsBefore
		tables = append(tables, map[string]any{"table": r.Table, "rows_before": r.RowsBefore, "rows_after": r.RowsAfter,
			"deleted": r.Deleted, "sha256": r.SHA256})
	}
	err := p.auditor.RecordChange(orgScope(ctx, orgID), orgID, func(ctx context.Context) (*operator.AuditEntry, error) {
		err := database.WithRequestTenant(ctx, p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE organization SET name = id::text, slug = 'purged-' || id::text, settings = '{}'::jsonb,
				deletion_state = 'purged', updated_at = now()
				WHERE id = $1 AND deletion_state = 'purging'`, orgID)
			if err == nil && tag.RowsAffected() == 0 {
				err = ErrNotPurgeable
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		return &operator.AuditEntry{OperatorID: purgeOperator, OperatorKind: operator.KindSystem, Action: "org.deletion.db_purged",
			TargetOrg: orgID, Status: 200, Details: map[string]any{"tables": tables, "rows": total}}, nil
	})
	if err != nil {
		return fmt.Errorf("org purge: finish: %w", err)
	}
	return nil
}

// remind sends the reminder of the threshold the end of the grace period
// reached (7 or 1 day before), once per threshold (notice_sent).
func (p *Purger) remind(ctx context.Context, o *dueOrganization) error {
	if p.notifier == nil || p.resolver == nil || o.dueAt == nil {
		return nil
	}
	left := o.dueAt.Sub(p.now())
	days := 0
	switch {
	case left <= 0:
		return nil
	case left <= 24*time.Hour:
		days = 1
	case left <= 7*24*time.Hour:
		days = 7
	default:
		return nil
	}
	due := o.dueAt.UTC().Format(time.RFC3339)
	key := "org.deletion:" + o.id + ":" + due + ":" + strconv.Itoa(days)
	var fresh bool
	err := database.WithRequestTenant(orgScope(ctx, o.id), p.pool, o.id, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO notice_sent (organization_id, kind, dedup_key, sent_at)
			VALUES ($1, $2, $3, now()) ON CONFLICT DO NOTHING`, o.id, reminderKind, key)
		fresh = err == nil && tag.RowsAffected() == 1
		return err
	})
	if err != nil || !fresh {
		return err
	}
	// The claim holds only once the reminder is handed to the notifier: a
	// failure releases it, so the next run sends the reminder of the
	// threshold (the notifier's DedupKey keeps it at most once).
	release := func(cause error) error {
		rerr := database.WithRequestTenant(orgScope(context.WithoutCancel(ctx), o.id), p.pool, o.id, func(ctx context.Context, tx pgx.Tx) error {
			_, derr := tx.Exec(ctx, `DELETE FROM notice_sent WHERE organization_id = $1 AND kind = $2 AND dedup_key = $3`, o.id, reminderKind, key)
			return derr
		})
		if rerr != nil {
			return errors.Join(cause, fmt.Errorf("org purge: release reminder claim: %w", rerr))
		}
		return cause
	}
	recipients, err := p.resolver.Resolve(ctx, o.id, &notify.Audience{Permission: identity.PermLocationRead, Roles: []string{"org_admin"}})
	if err != nil {
		return release(fmt.Errorf("org purge: reminder recipients: %w", err))
	}
	hours := int(math.Ceil(left.Hours()))
	texts := map[string]notify.Text{
		notify.LocaleDE: {Subject: fmt.Sprintf("Löschung der Organisation in %d Tag(en)", days),
			Body: "Die beantragte Löschung der Organisation wird am " + due + " endgültig ausgeführt (in etwa " + strconv.Itoa(hours) +
				" Stunden). Bis dahin kann sie widerrufen werden; ein Vollexport ist weiterhin möglich."},
		notify.LocaleEN: {Subject: fmt.Sprintf("Deletion of the organization in %d day(s)", days),
			Body: "The requested deletion of the organization is carried out for good at " + due + " (in about " + strconv.Itoa(hours) +
				" hours). Until then it can be cancelled; a full export is still possible."},
	}
	_, err = p.notifier.Notify(ctx, o.id, &notify.Request{Category: "org_deletion", Critical: true, Personal: true,
		Subject: texts[notify.LocaleDE].Subject, Body: texts[notify.LocaleDE].Body, Texts: texts,
		Data: map[string]any{"due_at": due, "days": days}, DedupKey: key, Recipients: recipients})
	if err != nil {
		return release(fmt.Errorf("org purge: reminder: %w", err))
	}
	return nil
}
