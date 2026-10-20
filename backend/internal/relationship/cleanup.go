package relationship

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Source policy (REL-08): the seven sources of a relationship and what each
// of them may do with the edges of another source.
//
//   - manual, api, import, integration, workflow, agent write their own
//     edges; nobody but a user (manual/api) changes or deletes another
//     source's edge, and discovery never removes them.
//   - discovery creates, re-confirms and – after its deadline – removes
//     only discovery edges; an existing edge of another source between the
//     same CIs stays untouched (manual wins).
//   - rule is not a source of its own (E-01: the catalog names seven):
//     edges of automation rules are workflow edges; "rule" is accepted on
//     input as an alias of workflow.
//
// Every edge carries its actor and time in attributes.provenance (who
// created it, how, when) next to the created/confirmed timestamps.

// Sources are the seven relationship sources.
var Sources = []string{"manual", "discovery", "agent", "import", "api", "integration", "workflow"}

// CanonicalSource maps an input source onto the seven; ok is false for an
// unknown one.
func CanonicalSource(source string) (canonical string, ok bool) {
	s := strings.ToLower(strings.TrimSpace(source))
	if s == "" {
		return "manual", true
	}
	if s == "rule" {
		return "workflow", true
	}
	for _, v := range Sources {
		if v == s {
			return s, true
		}
	}
	return "", false
}

// MayChange reports whether a writer of source by may change or remove an
// edge of source owner.
func MayChange(by, owner string) bool {
	switch by {
	case "manual", "api":
		return true
	}
	return by == owner
}

// Provenance returns the provenance attributes of an edge: actor, actor
// type, source and time.
func Provenance(actor, actorType, source string, at time.Time) map[string]any {
	return map[string]any{"actor": actor, "actor_type": actorType, "source": source, "at": at.UTC().Format(time.RFC3339)}
}

// --- cleanup (TOP-02) ---------------------------------------------------------

// CleanupKind is the scheduler kind of the run.
const CleanupKind = "relationship.cleanup"

// Cleanup removes stale and suppressed discovery edges (TOP-02): an edge
// of source discovery that is not verified and was not confirmed for
// max(7 days, 5 × the scope interval of its source CI) is removed with the
// event relationship.removed; so is a discovery edge a suppression covers.
type Cleanup struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// MinStaleAge is the shortest time after which an unconfirmed discovery
// edge is removed; StaleIntervals the multiple of the scope interval.
const (
	MinStaleAge    = 7 * 24 * time.Hour
	StaleIntervals = 5
	defaultSweep   = 24 * time.Hour
)

// NewCleanup creates the run on pool.
func NewCleanup(pool *pgxpool.Pool) *Cleanup {
	return &Cleanup{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (c *Cleanup) WithClock(now func() time.Time) *Cleanup {
	c.now = now
	return c
}

// Schedule registers the run every hour.
func (c *Cleanup) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: CleanupKind, Spec: "@every 1h", Run: func(ctx context.Context) error {
		_, err := c.Run(ctx)
		return err
	}}
}

// CleanupResult counts the removed edges.
type CleanupResult struct {
	Stale, Suppressed int
}

// Run cleans every organization. A failing organization does not stop the
// others (F-108): Run returns the errors joined at the end, the totals
// count the organizations that succeeded.
func (c *Cleanup) Run(ctx context.Context) (CleanupResult, error) {
	var total CleanupResult
	var orgs []string
	err := database.WithSystem(ctx, c.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return total, fmt.Errorf("list organizations: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		r, oerr := c.RunOrg(ctx, org)
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
			continue
		}
		total.Stale += r.Stale
		total.Suppressed += r.Suppressed
	}
	return total, errors.Join(errs...)
}

// staleAge is max(7 days, 5 × interval).
func staleAge(interval time.Duration) time.Duration {
	return max(MinStaleAge, StaleIntervals*interval)
}

type cleanupEdge struct {
	id, source, target, relType string
	confirmed                   time.Time
	schedules                   []string
	suppressed                  bool
}

// RunOrg cleans the discovery edges of one organization.
func (c *Cleanup) RunOrg(ctx context.Context, orgID string) (CleanupResult, error) {
	var res CleanupResult
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	now := c.now()
	err := database.WithTenant(ctx, c.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var sweep string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(settings->'discovery'->>'sweep_interval', '') FROM organization WHERE id = $1`, orgID).Scan(&sweep); err != nil {
			return fmt.Errorf("organization settings: %w", err)
		}
		fallback := defaultSweep
		if v, perr := time.ParseDuration(sweep); perr == nil && v >= 5*time.Minute {
			fallback = v
		}
		if err := reportInvalidScopes(ctx, tx, orgID); err != nil {
			return err
		}
		// Only discovery edges that nobody verified are candidates; the
		// earliest possible deadline (7 days) bounds the query, suppressed
		// edges are always candidates.
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.source_ci_id::text, r.target_ci_id::text, r.rel_type,
				COALESCE(r.last_confirmed_at, r.last_seen_at, r.created_at),
				COALESCE(array_agg(s.schedule) FILTER (WHERE s.id IS NOT NULL), '{}'),
				EXISTS (SELECT 1 FROM relationship_suppression sp JOIN relationship_type rt ON rt.id = sp.relationship_type_id
					WHERE sp.organization_id = r.organization_id AND rt.key = r.rel_type
					  AND ((sp.source_ci_id = r.source_ci_id AND sp.target_ci_id = r.target_ci_id)
					    OR (sp.source_ci_id = r.target_ci_id AND sp.target_ci_id = r.source_ci_id)))
			FROM ci_relationship r
			JOIN ci src ON src.id = r.source_ci_id
			LEFT JOIN discovery_scope s ON s.organization_id = r.organization_id AND s.enabled AND (
				(s.kind = 'cidr' AND CASE WHEN `+cidrTargetValid+` THEN src.management_ip <<= s.target::inet ELSE false END)
				OR (s.kind = 'host' AND strpos(s.target, '%') = 0
					AND (host(src.management_ip) = s.target OR lower(src.hostname) = s.target OR lower(src.fqdn) = s.target))
				OR (s.kind = 'range' AND CASE WHEN `+rangeTargetValid+` THEN family(src.management_ip) = family(split_part(s.target, '-', 1)::inet)
					AND host(src.management_ip)::inet BETWEEN split_part(s.target, '-', 1)::inet AND split_part(s.target, '-', 2)::inet ELSE false END))
			WHERE r.organization_id = $1 AND r.source = 'discovery' AND r.verification_state <> 'verified'
			GROUP BY r.id`, orgID)
		if err != nil {
			return fmt.Errorf("discovery edges: %w", err)
		}
		edges, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cleanupEdge, error) {
			var e cleanupEdge
			scanErr := row.Scan(&e.id, &e.source, &e.target, &e.relType, &e.confirmed, &e.schedules, &e.suppressed)
			return e, scanErr
		})
		if err != nil {
			return fmt.Errorf("discovery edges: %w", err)
		}
		outbox := events.NewOutbox()
		for i := range edges {
			e := &edges[i]
			reason := ""
			age := staleAge(scopeInterval(e.schedules, fallback))
			switch {
			case e.suppressed:
				reason = "suppressed"
			case now.Sub(e.confirmed) > age:
				reason = "stale"
			default:
				continue
			}
			if _, err = tx.Exec(ctx, `DELETE FROM ci_relationship WHERE id = $1 AND organization_id = $2 AND source = 'discovery'`, e.id, orgID); err != nil {
				return fmt.Errorf("remove edge %s: %w", e.id, err)
			}
			if _, err = outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.RelationshipRemoved, EntityID: e.id,
				Data: map[string]any{"id": e.id, "source_ci_id": e.source, "target_ci_id": e.target, "rel_type": e.relType,
					"source": "discovery", "reason": reason, "last_confirmed_at": e.confirmed.UTC().Format(time.RFC3339),
					"stale_after": age.String()}}); err != nil {
				return err
			}
			if reason == "suppressed" {
				res.Suppressed++
			} else {
				res.Stale++
			}
			slog.Info("discovery edge removed", "event", events.RelationshipRemoved, "organization_id", orgID, "relationship_id", e.id, "reason", reason)
		}
		return nil
	})
	return res, err
}

// The inet casts of discovery_scope.target sit behind a guard (F-108): a
// target PostgreSQL cannot read – an IPv6 zone (fe80::1%eth0), which the API
// accepted before WP-383, or another malformed row – would otherwise fail
// the query of the whole organization (22P02). Such a scope is skipped and
// reported once per run. The casts are inside CASE because PostgreSQL does
// not order the operands of AND. lifecycle.OfflineDetector uses the same
// guard.
const (
	cidrTargetValid  = `strpos(s.target, '%') = 0 AND pg_input_is_valid(s.target, 'inet')`
	rangeTargetValid = `s.target ~ '^[^-%/]+-[^-%/]+$' AND pg_input_is_valid(split_part(s.target, '-', 1), 'inet')
		AND pg_input_is_valid(split_part(s.target, '-', 2), 'inet')`
)

// reportInvalidScopes logs the enabled scopes of the organization whose
// target the guard skips (discovery.scope.invalid_target).
func reportInvalidScopes(ctx context.Context, tx pgx.Tx, orgID string) error {
	rows, err := tx.Query(ctx, `SELECT s.id::text, s.kind FROM discovery_scope s
		WHERE s.organization_id = $1 AND s.enabled AND NOT CASE s.kind
			WHEN 'cidr' THEN `+cidrTargetValid+`
			WHEN 'range' THEN `+rangeTargetValid+`
			ELSE strpos(s.target, '%') = 0 END
		ORDER BY s.id`, orgID)
	if err != nil {
		return fmt.Errorf("discovery scopes: %w", err)
	}
	invalid, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var v [2]string
		scanErr := r.Scan(&v[0], &v[1])
		return v, scanErr
	})
	if err != nil {
		return fmt.Errorf("discovery scopes: %w", err)
	}
	for _, v := range invalid {
		slog.Warn("discovery scope skipped: invalid target", "event", "discovery.scope.invalid_target",
			"job", CleanupKind, "organization_id", orgID, "scope_id", v[0], "kind", v[1])
	}
	return nil
}

// scopeInterval returns the shortest interval of the schedules (a cron
// schedule counts as the default sweep) or fallback.
func scopeInterval(schedules []string, fallback time.Duration) time.Duration {
	best := time.Duration(0)
	for _, s := range schedules {
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			d = defaultSweep
		}
		if best == 0 || d < best {
			best = d
		}
	}
	if best == 0 {
		return fallback
	}
	return best
}
