package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGRepository is the Repository on PostgreSQL. Every call runs in one
// tenant transaction with the scope of the context (database.WithTenant),
// so RLS limits the probes, profiles and memberships to those the
// principal may see.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository returns the repository on pool.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

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
		where := []string{"p.organization_id = $1"}
		args := []any{orgID}
		add := func(cond string, v any) {
			args = append(args, v)
			where = append(where, fmt.Sprintf(cond, len(args)))
		}
		for _, c := range []struct{ col, v string }{
			{"p.ci_id", f.CIID}, {"p.collector_group_id", f.CollectorGroupID}, {"p.client_id", f.ClientID},
		} {
			if c.v == "" {
				continue
			}
			if !api.IsUUID(c.v) {
				return nil // an invalid id matches nothing
			}
			add(c.col+" = $%d", c.v)
		}
		if f.Type != "" {
			add("p.type = $%d", f.Type)
		}
		if f.Enabled != nil {
			add("p.enabled = $%d", *f.Enabled)
		}
		query := `SELECT ` + probeColumns + ` FROM probe p WHERE ` + strings.Join(where, " AND ") + ` ORDER BY p.id`
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
