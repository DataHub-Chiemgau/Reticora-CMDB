package lifecycle

import (
	"context"
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

// Offline detector (REC-09, LCY-05): an observable CI that discovery did
// not see for three times its scope interval gets the status unknown and
// the event ci.offline; seen again, it is active again with ci.online.
// Only CIs with is_manual = false, discovery_source not manual/import and a
// type with is_observable = true are checked. The scope interval is the
// sweep schedule of the discovery scopes covering the CI's management
// address (the shortest); without a scope the organization's sweep
// interval (settings.discovery.sweep_interval, default 24 h) applies.

// OfflineDetectorKind is the scheduler kind of the run.
const OfflineDetectorKind = "ci.offline_detector"

// OfflineFactor is the multiple of the scope interval after which a CI is
// offline.
const OfflineFactor = 3

// defaultSweep is the scope interval without scope or setting (DIS-10).
const defaultSweep = 24 * time.Hour

// OfflineDetector is the scheduler run.
type OfflineDetector struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewOfflineDetector creates the run on pool.
func NewOfflineDetector(pool *pgxpool.Pool) *OfflineDetector {
	return &OfflineDetector{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (d *OfflineDetector) WithClock(now func() time.Time) *OfflineDetector {
	d.now = now
	return d
}

// Schedule registers the run every five minutes.
func (d *OfflineDetector) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: OfflineDetectorKind, Spec: "@every 5m", Run: func(ctx context.Context) error {
		_, err := d.Run(ctx)
		return err
	}}
}

// OfflineChanges counts what a run changed.
type OfflineChanges struct {
	Offline, Online int
}

// Run checks every organization.
func (d *OfflineDetector) Run(ctx context.Context) (OfflineChanges, error) {
	var total OfflineChanges
	var orgs []string
	err := database.WithSystem(ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error {
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
	for _, org := range orgs {
		c, oerr := d.RunOrg(ctx, org)
		if oerr != nil {
			return total, fmt.Errorf("organization %s: %w", org, oerr)
		}
		total.Offline += c.Offline
		total.Online += c.Online
	}
	return total, nil
}

// observedCI is a CI the detector considers.
type observedCI struct {
	id, name, client, site, status string
	lastSeen                       time.Time
	schedules                      []string
	runGaps                        []float64
}

// scopeInterval returns the interval of the shortest covering scope.
func (c *observedCI) scopeInterval(fallback time.Duration) time.Duration {
	best := time.Duration(0)
	for i, s := range c.schedules {
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			// A cron schedule: the gap between the last and the next run.
			d = defaultSweep
			if i < len(c.runGaps) && c.runGaps[i] > 0 {
				d = time.Duration(c.runGaps[i] * float64(time.Second))
			}
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

// RunOrg checks the CIs of one organization.
func (d *OfflineDetector) RunOrg(ctx context.Context, orgID string) (OfflineChanges, error) {
	var c OfflineChanges
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	now := d.now()
	err := database.WithTenant(ctx, d.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var sweep string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(settings->'discovery'->>'sweep_interval', '') FROM organization WHERE id = $1`, orgID).Scan(&sweep); err != nil {
			return fmt.Errorf("organization settings: %w", err)
		}
		fallback := defaultSweep
		if v, perr := time.ParseDuration(sweep); perr == nil && v >= 5*time.Minute {
			fallback = v
		}
		rows, err := tx.Query(ctx, `
			SELECT c.id::text, c.name, COALESCE(c.client_id::text, ''), COALESCE(c.site_id::text, ''), c.status, c.last_seen_at,
				COALESCE(array_agg(s.schedule) FILTER (WHERE s.id IS NOT NULL), '{}'),
				COALESCE(array_agg(COALESCE(EXTRACT(EPOCH FROM s.next_run_at - s.last_run_at), 0)::float8) FILTER (WHERE s.id IS NOT NULL), '{}')
			FROM ci c
			JOIN ci_type t ON t.id = c.ci_type_id AND t.is_observable
			LEFT JOIN discovery_scope s ON s.organization_id = c.organization_id AND s.enabled AND (
				(s.kind = 'cidr' AND c.management_ip <<= s.target::inet)
				OR (s.kind = 'host' AND (host(c.management_ip) = s.target OR c.hostname_lc = s.target OR c.fqdn_lc = s.target))
				OR (s.kind = 'range' AND family(c.management_ip) = family(split_part(s.target, '-', 1)::inet)
					AND host(c.management_ip)::inet BETWEEN split_part(s.target, '-', 1)::inet AND split_part(s.target, '-', 2)::inet))
			WHERE c.organization_id = $1 AND c.deleted_at IS NULL AND NOT c.is_manual
			  AND c.discovery_source IS NOT NULL AND c.discovery_source NOT IN ('manual', 'import')
			  AND c.last_seen_at IS NOT NULL AND c.status IN ('active', 'unknown')
			GROUP BY c.id`, orgID)
		if err != nil {
			return fmt.Errorf("observable CIs: %w", err)
		}
		cis, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (observedCI, error) {
			var o observedCI
			scanErr := r.Scan(&o.id, &o.name, &o.client, &o.site, &o.status, &o.lastSeen, &o.schedules, &o.runGaps)
			return o, scanErr
		})
		if err != nil {
			return fmt.Errorf("observable CIs: %w", err)
		}
		outbox := events.NewOutbox()
		for i := range cis {
			ci := &cis[i]
			interval := ci.scopeInterval(fallback)
			offline := now.Sub(ci.lastSeen) > OfflineFactor*interval
			next, eventType := "", ""
			switch {
			case offline && ci.status == "active":
				next, eventType = "unknown", events.CIOffline
			case !offline && ci.status == "unknown":
				next, eventType = "active", events.CIOnline
			default:
				continue
			}
			if _, err = tx.Exec(ctx, `UPDATE ci SET status = $3 WHERE id = $1 AND organization_id = $2`, ci.id, orgID, next); err != nil {
				return fmt.Errorf("set CI %s %s: %w", ci.id, next, err)
			}
			if _, err = outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: eventType, EntityID: ci.id,
				Data: map[string]any{"id": ci.id, "name": ci.name, "client_id": ci.client, "site_id": ci.site, "status": next,
					"last_seen_at": ci.lastSeen.UTC().Format(time.RFC3339), "scope_interval": interval.String()}}); err != nil {
				return err
			}
			if eventType == events.CIOffline {
				c.Offline++
				slog.Warn("CI offline", "event", events.CIOffline, "organization_id", orgID, "ci_id", ci.id, "last_seen_at", ci.lastSeen,
					"scope_interval", interval.String())
			} else {
				c.Online++
				slog.Info("CI online again", "event", events.CIOnline, "organization_id", orgID, "ci_id", ci.id)
			}
		}
		return nil
	})
	return c, err
}
