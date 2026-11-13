package probe

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// PGRepository is the Repository on PostgreSQL. Every call runs in one
// tenant transaction with the scope of the context (database.WithTenant),
// so RLS limits the probes, profiles and memberships to those the
// principal may see.
type PGRepository struct {
	pool *pgxpool.Pool
	// budget admits the writes against the probe budget (WP-940); nil
	// admits every write.
	budget BudgetGate
	// assign runs the assignment of the touched collector groups after a
	// write committed (WP-941 AK4); nil leaves it to the next tick.
	assign AssignTrigger
}

// SetAssignTrigger makes every committed write run the assignment of its
// collector groups (WP-941 AK4).
func (r *PGRepository) SetAssignTrigger(t AssignTrigger) { r.assign = t }

// afterWrite registers the assignment of groups (and the notice of the
// collectors the write raised itself) for after the commit; a rollback
// drops it.
func (r *PGRepository) afterWrite(ctx context.Context, tx pgx.Tx, orgID string, groups []string, raised []CollectorVersion) {
	if r.assign == nil {
		return
	}
	groups = slices.Compact(slices.Sorted(slices.Values(slices.DeleteFunc(groups, func(g string) bool { return g == "" }))))
	database.AfterCommit(database.ContextWithTx(ctx, tx), func(ctx context.Context) {
		r.assign.ProbesChanged(ctx, orgID, groups, raised)
	})
}

// assignedCollector returns the collector the probe is assigned to (empty
// without an assignment).
func assignedCollector(ctx context.Context, tx pgx.Tx, orgID, probeID string) (string, error) {
	var collector string
	err := tx.QueryRow(ctx, `SELECT collector_id::text FROM probe_assignment WHERE organization_id = $1 AND probe_id = $2`,
		orgID, probeID).Scan(&collector)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read probe assignment: %w", err)
	}
	return collector, nil
}

// releaseAssignment raises the config_version of the collector whose
// assignment went with the write (a delete, or a move to another collector
// group) after the write, so the row locks follow the order of the
// assignment engine (probe, assignment, then member; the engine locks the
// probes of its plan FOR SHARE first, lockPlanProbes); nothing for an empty
// id. The collector is read after the row lock of the probe: an engine run
// that moves the probe either committed before (the read sees the new
// collector) or waits for the write.
func releaseAssignment(ctx context.Context, tx pgx.Tx, orgID, collectorID string) ([]CollectorVersion, error) {
	if collectorID == "" {
		return nil, nil
	}
	return raiseVersions(ctx, tx, orgID, []string{collectorID})
}

// NewPGRepository returns the repository on pool.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// SetBudget implements Store.
func (r *PGRepository) SetBudget(gate BudgetGate) { r.budget = gate }

// planMin returns min_probe_interval_s of the organization for the
// resolution (0 without a budget gate).
func (r *PGRepository) planMin(ctx context.Context, orgID string) (int64, error) {
	if r.budget == nil {
		return 0, nil
	}
	return r.budget.MinProbeIntervalS(ctx, orgID)
}

// lockBudget takes the budget lock of the organization (WP-940 AK3) as
// the first lock of a write transaction, exclusive.
func (r *PGRepository) lockBudget(ctx context.Context, tx pgx.Tx, orgID string) error {
	if r.budget == nil {
		return nil
	}
	return database.LockQuota(database.ContextWithTx(ctx, tx), orgID, entitlement.ProbeBudgetLock)
}

// admit counts the budget of the organization before and after the write
// in the transaction (under the budget lock) and admits it: every active
// probe of the organization, read with the client and site scope widened
// (only the budget leaves it). A disabled next counts nothing and needs no
// budget lock: only the interval it asks for is checked against the plan
// minimum (admitBudget).
func (r *PGRepository) admit(ctx context.Context, tx pgx.Tx, orgID string, current, next *Probe) error {
	if r.budget == nil {
		return nil
	}
	txCtx := database.ContextWithTx(ctx, tx)
	planMin, err := r.budget.MinProbeIntervalS(txCtx, orgID)
	if err != nil {
		return err
	}
	if !next.Enabled {
		return admitBudget(txCtx, r.budget, orgID, nil, nil, current, next, Layers{PlanMinIntervalS: planMin})
	}
	var (
		active []Probe
		x      *layerIndex
	)
	err = (&pgWriteTx{tx: tx, orgID: orgID}).widened(ctx, func() error {
		var rerr error
		active, rerr = collectProbes(ctx, tx, `SELECT `+probeColumns+` FROM probe p WHERE p.organization_id = $1 AND p.enabled`, orgID)
		if rerr != nil {
			return rerr
		}
		x, rerr = loadLayers(ctx, tx, orgID, append(slices.Clone(active), *next))
		return rerr
	})
	if err != nil {
		return fmt.Errorf("count probe budget: %w", err)
	}
	x.planMin = planMin
	return admitBudget(txCtx, r.budget, orgID, active, x.layers, current, next, x.layers(next))
}

var _ Repository = (*PGRepository)(nil)

const probeColumns = `p.id::text, p.organization_id::text, COALESCE(p.client_id::text, ''), COALESCE(p.site_id::text, ''),
	COALESCE(p.ci_id::text, ''), p.collector_group_id::text, p.type, p.target, p.params, p.enabled,
	COALESCE(p.parent_probe_id::text, ''), COALESCE(p.created_by_surrogate::text, ''), p.created_at, p.updated_at`

func scanProbe(row pgx.Row) (Probe, error) {
	var p Probe
	err := row.Scan(&p.ID, &p.OrganizationID, &p.ClientID, &p.SiteID, &p.CIID, &p.CollectorGroupID, &p.Type, &p.Target,
		&p.Params, &p.Enabled, &p.ParentProbeID, &p.CreatedBySurrogate, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// tenant runs fn in a tenant transaction with the scope of ctx.
func (r *PGRepository) tenant(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx, orgID string) error) error {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return database.ErrNoTenantScope
	}
	return database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, tx, scope.OrgID)
	})
}

// Get implements Repository; an id that is no UUID names no probe.
func (r *PGRepository) Get(ctx context.Context, id string) (*Probe, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var p Probe
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		var err error
		p, err = scanProbe(tx.QueryRow(ctx, `SELECT `+probeColumns+` FROM probe p WHERE p.organization_id = $1 AND p.id = $2`, orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get probe: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// List implements Repository.
func (r *PGRepository) List(ctx context.Context, f *ListFilter) ([]Probe, error) {
	if f == nil {
		f = &ListFilter{}
	}
	out := make([]Probe, 0)
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		where, args, ok := listWhere(orgID, f)
		if !ok {
			return nil // an invalid id matches nothing
		}
		query := `SELECT ` + probeColumns + ` FROM probe p WHERE ` + where + ` ORDER BY p.id`
		if f.Limit > 0 {
			query += fmt.Sprintf(" LIMIT %d", f.Limit)
		}
		if f.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", f.Offset)
		}
		var err error
		out, err = collectProbes(ctx, tx, query, args...)
		return err
	})
	return out, err
}

// Count implements Store.
func (r *PGRepository) Count(ctx context.Context, f *ListFilter) (int, error) {
	if f == nil {
		f = &ListFilter{}
	}
	var n int
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		where, args, ok := listWhere(orgID, f)
		if !ok {
			return nil
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM probe p WHERE `+where, args...).Scan(&n); err != nil {
			return fmt.Errorf("count probes: %w", err)
		}
		return nil
	})
	return n, err
}

// listWhere builds the condition of a listing; ok false when an id filter
// is no UUID (it matches nothing).
func listWhere(orgID string, f *ListFilter) (where string, args []any, ok bool) {
	conds := []string{"p.organization_id = $1"}
	args = []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	for _, c := range []struct{ col, v string }{
		{"p.ci_id", f.CIID}, {"p.collector_group_id", f.CollectorGroupID}, {"p.client_id", f.ClientID},
	} {
		if c.v == "" {
			continue
		}
		if !api.IsUUID(c.v) {
			return "", nil, false
		}
		add(c.col+" = $%d", c.v)
	}
	if f.Type != "" {
		add("p.type = $%d", f.Type)
	}
	if f.Enabled != nil {
		add("p.enabled = $%d", *f.Enabled)
	}
	return strings.Join(conds, " AND "), args, true
}

func collectProbes(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]Probe, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list probes: %w", err)
	}
	defer rows.Close()
	out := make([]Probe, 0)
	for rows.Next() {
		p, err := scanProbe(rows)
		if err != nil {
			return nil, fmt.Errorf("scan probe: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Layers implements Repository.
func (r *PGRepository) Layers(ctx context.Context, p *Probe) (Layers, error) {
	var l Layers
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		x, err := loadLayers(ctx, tx, orgID, []Probe{*p})
		if err != nil {
			return err
		}
		if x.planMin, err = r.planMin(database.ContextWithTx(ctx, tx), orgID); err != nil {
			return err
		}
		l = x.layers(p)
		return nil
	})
	return l, err
}

// ResolveForCollector implements Repository: one statement for the
// assigned probes, one for the memberships of their CIs, one for the
// profiles (with BEGIN and COMMIT five statements).
func (r *PGRepository) ResolveForCollector(ctx context.Context, collectorID string) ([]Resolved, error) {
	if !api.IsUUID(collectorID) {
		return []Resolved{}, nil
	}
	var out []Resolved
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		rows, err := tx.Query(ctx, `SELECT `+probeColumns+`, a.epoch
			FROM probe_assignment a
			JOIN probe p ON p.organization_id = a.organization_id AND p.id = a.probe_id
			WHERE a.organization_id = $1 AND a.collector_id = $2
			ORDER BY p.id`, orgID, collectorID)
		if err != nil {
			return fmt.Errorf("read assigned probes: %w", err)
		}
		probes := make([]Probe, 0)
		epochs := map[string]int64{}
		for rows.Next() {
			var p Probe
			var epoch int64
			if scanErr := rows.Scan(&p.ID, &p.OrganizationID, &p.ClientID, &p.SiteID, &p.CIID, &p.CollectorGroupID, &p.Type, &p.Target,
				&p.Params, &p.Enabled, &p.ParentProbeID, &p.CreatedBySurrogate, &p.CreatedAt, &p.UpdatedAt, &epoch); scanErr != nil {
				rows.Close()
				return fmt.Errorf("scan assigned probe: %w", scanErr)
			}
			probes = append(probes, p)
			epochs[p.ID] = epoch
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return fmt.Errorf("read assigned probes: %w", err)
		}
		x, err := loadLayers(ctx, tx, orgID, probes)
		if err != nil {
			return err
		}
		if x.planMin, err = r.planMin(database.ContextWithTx(ctx, tx), orgID); err != nil {
			return err
		}
		out = x.resolveAll(probes, epochs)
		return nil
	})
	return out, err
}

// ResolveForGroup implements Repository like ResolveForCollector.
func (r *PGRepository) ResolveForGroup(ctx context.Context, groupID string) ([]Resolved, error) {
	if !api.IsUUID(groupID) {
		return []Resolved{}, nil
	}
	var out []Resolved
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		probes, err := collectProbes(ctx, tx, `SELECT `+probeColumns+` FROM probe p
			WHERE p.organization_id = $1 AND p.collector_group_id = $2 ORDER BY p.id`, orgID, groupID)
		if err != nil {
			return err
		}
		x, err := loadLayers(ctx, tx, orgID, probes)
		if err != nil {
			return err
		}
		if x.planMin, err = r.planMin(database.ContextWithTx(ctx, tx), orgID); err != nil {
			return err
		}
		out = x.resolveAll(probes, nil)
		return nil
	})
	return out, err
}

// loadLayers reads the memberships of the probes' CIs (one statement, none
// without CI) and the profiles of every level for their types (one
// statement) into an index.
func loadLayers(ctx context.Context, tx pgx.Tx, orgID string, probes []Probe) (*layerIndex, error) {
	x := newLayerIndex()
	if len(probes) == 0 {
		return x, nil
	}
	types, clients, cis := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range probes {
		types[probes[i].Type] = true
		if probes[i].ClientID != "" {
			clients[probes[i].ClientID] = true
		}
		if probes[i].CIID != "" {
			cis[probes[i].CIID] = true
		}
	}
	groupIDs := []string{}
	if len(cis) > 0 {
		rows, err := tx.Query(ctx, `SELECT m.ci_id::text, g.id::text, g.name, g.priority
			FROM monitoring_group_member m
			JOIN monitoring_group g ON g.organization_id = m.organization_id AND g.id = m.group_id
			WHERE m.organization_id = $1 AND m.ci_id = ANY ($2::uuid[])`, orgID, keys(cis))
		if err != nil {
			return nil, fmt.Errorf("read monitoring group memberships: %w", err)
		}
		seen := map[string]bool{}
		for rows.Next() {
			var ciID string
			var g memberGroup
			if err := rows.Scan(&ciID, &g.id, &g.name, &g.priority); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan monitoring group membership: %w", err)
			}
			x.groups[ciID] = append(x.groups[ciID], g)
			if !seen[g.id] {
				seen[g.id] = true
				groupIDs = append(groupIDs, g.id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read monitoring group memberships: %w", err)
		}
	}
	rows, err := tx.Query(ctx, `SELECT level, COALESCE(client_id::text, ''), COALESCE(group_id::text, ''),
			COALESCE(ci_id::text, ''), probe_type, params
		FROM monitoring_profile
		WHERE organization_id = $1 AND probe_type = ANY ($2::text[])
		  AND (level = 'org'
		    OR (level = 'client' AND client_id = ANY ($3::uuid[]))
		    OR (level = 'group' AND group_id = ANY ($4::uuid[]))
		    OR (level = 'ci' AND ci_id = ANY ($5::uuid[])))`,
		orgID, keys(types), keys(clients), groupIDs, keys(cis))
	if err != nil {
		return nil, fmt.Errorf("read monitoring profiles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p MonitoringProfile
		if err := rows.Scan(&p.Level, &p.ClientID, &p.GroupID, &p.CIID, &p.ProbeType, &p.Params); err != nil {
			return nil, fmt.Errorf("scan monitoring profile: %w", err)
		}
		x.add(&p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read monitoring profiles: %w", err)
	}
	return x, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- writes (WP-939) ---------------------------------------------------------

var _ Store = (*PGRepository)(nil)

// lockParents takes the advisory lock probe_parent:<org> until the end of
// the transaction: the checks of the parent chain of concurrent writes run
// one after the other, so A→B and B→A cannot both pass (AK3).
func lockParents(ctx context.Context, tx pgx.Tx, orgID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('probe_parent:' || $1, 0))`, orgID); err != nil {
		return fmt.Errorf("lock probe parents: %w", err)
	}
	return nil
}

// probeWriteError maps the constraints of probe onto the request fields.
func probeWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "probe_target_key":
		return ErrTargetTaken
	case "probe_parent_not_self":
		return ErrParentCycle
	case "probe_ci_fkey":
		return unprocessable("ci_id", "ci not found")
	case "probe_client_fkey", "probe_client_required":
		return unprocessable("client_id", "client not found")
	case "probe_site_fkey", "probe_site_client":
		return unprocessable("site_id", "site not found or not a site of the client")
	case "probe_parent_fkey":
		return unprocessable("parent_probe_id", "parent probe not found")
	case "probe_collector_group_fkey", "probe_collector_group_client":
		return unprocessable("collector_group_id", "collector group not found or not one of the probe's client")
	case "probe_target_check":
		return unprocessable("target", "target must not be blank and at most 1024 characters")
	case "probe_type_check":
		return unprocessable("type", "unknown probe type")
	case "probe_params_check":
		return unprocessable("params", "params must be a JSON object")
	}
	return err
}

// emitConfigChanged appends config.changed as the last write of the
// transaction (Konvention 20).
func emitConfigChanged(ctx context.Context, tx pgx.Tx, p *Probe, fields []string) error {
	if fields == nil {
		fields = []string{}
	}
	data := events.ProbeConfigChangedData{ID: p.ID, CIID: p.CIID, ClientID: p.ClientID, SiteID: p.SiteID, ChangedFields: fields}
	return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
		_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: p.OrganizationID, Type: events.ProbeConfigChanged,
			EntityID: p.ID, Data: data})
		return err
	})
}

// Create implements Store.
func (r *PGRepository) Create(ctx context.Context, in *Probe, check Check) (*Probe, error) {
	var out Probe
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		p := *in
		p.OrganizationID = orgID
		if p.Enabled {
			if werr := r.lockBudget(ctx, tx, orgID); werr != nil {
				return werr
			}
		}
		if p.ParentProbeID != "" {
			if werr := lockParents(ctx, tx, orgID); werr != nil {
				return werr
			}
		}
		if check != nil {
			if err := check(ctx, &pgWriteTx{tx: tx, orgID: orgID}, &p, nil); err != nil {
				return err
			}
		}
		if err := r.admit(ctx, tx, orgID, nil, &p); err != nil {
			return err
		}
		created, err := scanProbe(tx.QueryRow(ctx, `INSERT INTO probe AS p (organization_id, client_id, site_id, ci_id,
				collector_group_id, type, target, params, enabled, parent_probe_id, created_by_surrogate)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+probeColumns,
			orgID, nullID(p.ClientID), nullID(p.SiteID), nullID(p.CIID), p.CollectorGroupID, p.Type, p.Target,
			[]byte(p.Params), p.Enabled, nullID(p.ParentProbeID), nullID(p.CreatedBySurrogate)))
		if err != nil {
			return probeWriteError(err)
		}
		out = created
		r.afterWrite(ctx, tx, orgID, []string{out.CollectorGroupID}, nil)
		return emitConfigChanged(ctx, tx, &out, createdFields(&out))
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Update implements Store.
func (r *PGRepository) Update(ctx context.Context, id string, change func(*Probe) (*Probe, []string, error), check Check) (*Probe, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var out Probe
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		// The budget lock comes before every row lock (WP-940 AK3); a
		// delete takes none, so the row lock below cannot deadlock with it.
		if err := r.lockBudget(ctx, tx, orgID); err != nil {
			return err
		}
		// FOR NO KEY UPDATE, not FOR UPDATE: the change never touches the
		// key, and the weaker lock does not conflict with the FOR KEY SHARE
		// that the foreign key probe_parent_fkey takes on a parent. With
		// FOR UPDATE, A→B and B→A deadlock between this row lock and the
		// advisory lock probe_parent:<org> instead of reaching the cycle
		// check (AK3).
		current, err := scanProbe(tx.QueryRow(ctx, `SELECT `+probeColumns+` FROM probe p
			WHERE p.organization_id = $1 AND p.id = $2 FOR NO KEY UPDATE`, orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read probe: %w", err)
		}
		next, fields, err := change(&current)
		if err != nil {
			return err
		}
		if next.ParentProbeID != current.ParentProbeID && next.ParentProbeID != "" {
			if werr := lockParents(ctx, tx, orgID); werr != nil {
				return werr
			}
		}
		if check != nil {
			if werr := check(ctx, &pgWriteTx{tx: tx, orgID: orgID}, next, &current); werr != nil {
				return werr
			}
		}
		if len(fields) == 0 {
			out = current
			return nil
		}
		if werr := r.admit(ctx, tx, orgID, &current, next); werr != nil {
			return werr
		}
		// A move to another collector group takes the assignment along
		// (migration 000308): its collector learns it with a new version.
		var moved string
		if next.CollectorGroupID != current.CollectorGroupID {
			if moved, err = assignedCollector(ctx, tx, orgID, id); err != nil {
				return err
			}
		}
		out, err = scanProbe(tx.QueryRow(ctx, `UPDATE probe AS p SET target = $3, params = $4, enabled = $5,
				parent_probe_id = $6, collector_group_id = $7
			WHERE p.organization_id = $1 AND p.id = $2 RETURNING `+probeColumns,
			orgID, id, next.Target, []byte(next.Params), next.Enabled, nullID(next.ParentProbeID), next.CollectorGroupID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return probeWriteError(err)
		}
		raised, err := releaseAssignment(ctx, tx, orgID, moved)
		if err != nil {
			return err
		}
		r.afterWrite(ctx, tx, orgID, []string{current.CollectorGroupID, out.CollectorGroupID}, raised)
		return emitConfigChanged(ctx, tx, &out, fields)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete implements Store. The foreign key probe_parent_fkey sets
// parent_probe_id of the child probes to NULL (ON DELETE SET NULL), also of
// children outside the caller's scope: their configuration changes, so each
// direct child gets its own config.changed (changed_fields parent_probe_id).
func (r *PGRepository) Delete(ctx context.Context, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		// The row lock blocks new children (FOR KEY SHARE of their foreign
		// key check) until the delete commits, so the children read below
		// are the ones the delete detaches.
		var locked string
		err := tx.QueryRow(ctx, `SELECT p.id::text FROM probe p WHERE p.organization_id = $1 AND p.id = $2 FOR UPDATE`,
			orgID, id).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read probe: %w", err)
		}
		children, err := (&pgWriteTx{tx: tx, orgID: orgID}).children(ctx, id)
		if err != nil {
			return err
		}
		assigned, err := assignedCollector(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		p, err := scanProbe(tx.QueryRow(ctx, `DELETE FROM probe AS p WHERE p.organization_id = $1 AND p.id = $2 RETURNING `+probeColumns,
			orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return probeWriteError(err)
		}
		// The assignment went with the probe (ON DELETE CASCADE); its
		// collector learns it with a new version (WP-941 AK3).
		raised, err := releaseAssignment(ctx, tx, orgID, assigned)
		if err != nil {
			return err
		}
		r.afterWrite(ctx, tx, orgID, []string{p.CollectorGroupID}, raised)
		for i := range children {
			if err := emitConfigChanged(ctx, tx, &children[i], []string{"parent_probe_id"}); err != nil {
				return err
			}
		}
		return emitConfigChanged(ctx, tx, &p, []string{DeletedField})
	})
}

// nullID maps an empty id to NULL.
func nullID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// pgWriteTx is the WriteTx of a PostgreSQL write transaction.
type pgWriteTx struct {
	tx    pgx.Tx
	orgID string
}

func (t *pgWriteTx) CI(ctx context.Context, id string) (clientID, siteID string, ok bool, err error) {
	err = t.tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM ci
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, t.orgID, id).Scan(&clientID, &siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("read ci: %w", err)
	}
	return clientID, siteID, true, nil
}

func (t *pgWriteTx) exists(ctx context.Context, table, id string) (bool, error) {
	var ok bool
	if err := t.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE organization_id = $1 AND id = $2)`,
		t.orgID, id).Scan(&ok); err != nil {
		return false, fmt.Errorf("read %s: %w", table, err)
	}
	return ok, nil
}

func (t *pgWriteTx) ClientVisible(ctx context.Context, id string) (bool, error) {
	return t.exists(ctx, "client", id)
}

func (t *pgWriteTx) ProbeVisible(ctx context.Context, id string) (bool, error) {
	return t.exists(ctx, "probe", id)
}

func (t *pgWriteTx) Site(ctx context.Context, id string) (client string, ok bool, err error) {
	err = t.tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM site WHERE organization_id = $1 AND id = $2`,
		t.orgID, id).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read site: %w", err)
	}
	return client, true, nil
}

func (t *pgWriteTx) CollectorGroup(ctx context.Context, id string) (client string, ok bool, err error) {
	err = t.tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM collector_group WHERE organization_id = $1 AND id = $2`,
		t.orgID, id).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read collector group: %w", err)
	}
	return client, true, nil
}

func (t *pgWriteTx) CollectorGroupsOfClient(ctx context.Context, clientID string) ([]string, error) {
	rows, err := t.tx.Query(ctx, `SELECT id::text FROM collector_group
		WHERE organization_id = $1 AND client_id IS NOT DISTINCT FROM $2::uuid ORDER BY id LIMIT 2`, t.orgID, nullID(clientID))
	if err != nil {
		return nil, fmt.Errorf("read collector groups: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read collector groups: %w", err)
	}
	return ids, nil
}

// widened runs fn with the client, site and team scope widened
// (app_scope_widen, migration 000139; pattern ipam heldAddresses): only
// probe ids leave it.
func (t *pgWriteTx) widened(ctx context.Context, fn func() error) error {
	var previous string
	if err := t.tx.QueryRow(ctx, `SELECT app_scope_widen()::text`).Scan(&previous); err != nil {
		return fmt.Errorf("widen scope: %w", err)
	}
	err := fn()
	if _, restoreErr := t.tx.Exec(ctx, `SELECT app_scope_restore($1::text[])`, previous); restoreErr != nil && err == nil {
		err = fmt.Errorf("restore scope: %w", restoreErr)
	}
	return err
}

func (t *pgWriteTx) Ancestors(ctx context.Context, id string, limit int) ([]string, error) {
	var out []string
	err := t.widened(ctx, func() error {
		rows, err := t.tx.Query(ctx, `WITH RECURSIVE chain (id, parent, depth, path) AS (
				SELECT p.id, p.parent_probe_id, 1, ARRAY[p.id] FROM probe p WHERE p.organization_id = $1 AND p.id = $2
				UNION ALL
				SELECT p.id, p.parent_probe_id, c.depth + 1, c.path || p.id
				  FROM chain c JOIN probe p ON p.organization_id = $1 AND p.id = c.parent
				 WHERE c.depth < $3 AND NOT p.id = ANY (c.path))
			SELECT id::text FROM chain ORDER BY depth`, t.orgID, id, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read parent chain: %w", err)
	}
	return out, nil
}

// children returns the direct child probes of id with the scope widened:
// the delete of a parent detaches them all, visible or not. Only the
// identifiers and the scope columns of the event leave the widened scope.
func (t *pgWriteTx) children(ctx context.Context, id string) ([]Probe, error) {
	var out []Probe
	err := t.widened(ctx, func() error {
		rows, err := t.tx.Query(ctx, `SELECT p.id::text, COALESCE(p.ci_id::text, ''), COALESCE(p.client_id::text, ''),
				COALESCE(p.site_id::text, '') FROM probe p
			WHERE p.organization_id = $1 AND p.parent_probe_id = $2 ORDER BY p.id`, t.orgID, id)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Probe, error) {
			c := Probe{OrganizationID: t.orgID}
			scanErr := row.Scan(&c.ID, &c.CIID, &c.ClientID, &c.SiteID)
			return c, scanErr
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read child probes: %w", err)
	}
	return out, nil
}

func (t *pgWriteTx) SubtreeHeight(ctx context.Context, id string, limit int) (int, error) {
	var height int
	err := t.widened(ctx, func() error {
		return t.tx.QueryRow(ctx, `WITH RECURSIVE tree (id, depth, path) AS (
				SELECT p.id, 1, ARRAY[p.id] FROM probe p WHERE p.organization_id = $1 AND p.parent_probe_id = $2
				UNION ALL
				SELECT p.id, t.depth + 1, t.path || p.id
				  FROM tree t JOIN probe p ON p.organization_id = $1 AND p.parent_probe_id = t.id
				 WHERE t.depth < $3 AND NOT p.id = ANY (t.path))
			SELECT COALESCE(max(depth), 0) FROM tree`, t.orgID, id, limit).Scan(&height)
	})
	if err != nil {
		return 0, fmt.Errorf("read child probes: %w", err)
	}
	return height, nil
}

func (t *pgWriteTx) Layers(ctx context.Context, p *Probe) (Layers, error) {
	x, err := loadLayers(ctx, t.tx, t.orgID, []Probe{*p})
	if err != nil {
		return Layers{}, err
	}
	return x.layers(p), nil
}
