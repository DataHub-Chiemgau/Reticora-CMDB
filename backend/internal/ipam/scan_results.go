package ipam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// The results of the subnet scan (IPM-11, WP-1063, E-211): the system job
// ipam.scan.apply applies, every minute and per organization with a
// scanned subnet and the features ipam and monitoring, the state of the
// scan probes (probe_state, WP-944/WP-961) to the addresses of their
// targets (subnet_scan_target, WP-1062). The comparison is set based and
// idempotent: it does not depend on the order or a second delivery of the
// state changes.
//
//	probe up, degraded  → state active, last_seen_at = greatest(last_seen_at, last_result_at)
//	probe down          → state offline once coalesce(last_seen_at, since) is older than offline_after_s;
//	                      after an unknown phase (the address is unknown) the grace counts from
//	                      since, the start of the down state, so the time unknown does not count
//	probe unknown       → state unknown, never offline (collector_unreachable and parent_down
//	                      included: a lost collector group is a network down, not an offline host)
//	no probe state      → unchanged
//
// With discover_new_hosts an answering target without an address row
// becomes an address of type discovered, origin scan and state active.
// Every change of state writes ipam.address.state_changed through the
// outbox and is recorded in ipam_change with source scan; last_seen_at
// alone writes neither an event nor history (migration 000364). Afterwards
// at most 50 subnets whose targets are out of date get them synchronized
// again (SyncScanTargets), each under its own savepoint: a subnet that
// fails (probe budget, plan minimums after a downgrade) is logged, marked
// with scan_sync_failed_at and moves to the end of the order; the others
// are synchronized all the same.

// ScanApplyKind is the system job that applies the scan results.
const ScanApplyKind = "ipam.scan.apply"

// scanResyncBatch is the most subnets one run synchronizes again (AK6).
const scanResyncBatch = 50

// ScanUpdates counts the changes the scan applied: the new state of an
// address (active, offline, unknown) and the new hosts.
var ScanUpdates = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "reticora",
	Name:      "ipam_scan_updates_total",
	Help:      "Address changes applied from the subnet scan by kind: the new state active, offline or unknown, or new_host (IPM-11).",
}, []string{"change"})

// ScanApplyResult is the outcome of one run for one organization.
type ScanApplyResult struct {
	// Skipped: another run holds the lock of the organization.
	Skipped bool
	// Scanned: the organization has a subnet with scan_enabled.
	Scanned bool
	// Changed counts the addresses per new state; NewHosts the created ones.
	Changed  map[string]int
	NewHosts int
	// Resynced counts the subnets whose targets were synchronized again;
	// ResyncFailed the ones whose synchronization failed and was skipped.
	Resynced     int
	ResyncFailed int
}

// ScanApplier is the system job ipam.scan.apply.
type ScanApplier struct {
	pool *pgxpool.Pool
	repo *PGRepository
	// Licensed reports whether an organization has the features ipam and
	// monitoring; nil admits every organization.
	Licensed func(ctx context.Context, orgID string) bool
	orgs     func(ctx context.Context) ([]string, error)
}

// NewScanApplier returns the job on pool. repo writes the events (its
// outbox) and synchronizes targets (its scan probes, SetScanProbes).
func NewScanApplier(pool *pgxpool.Pool, repo *PGRepository) *ScanApplier {
	return &ScanApplier{pool: pool, repo: repo,
		orgs: func(ctx context.Context) ([]string, error) { return database.OrganizationIDs(ctx, pool) }}
}

// Schedule is the job's entry in the scheduler: every minute, a missed
// minute is not repeated (one run applies the current states).
func (a *ScanApplier) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: ScanApplyKind, Spec: "@every 1m", MaxCatchUp: 1, Timeout: time.Minute, Run: a.Run}
}

// Run applies the scan results of every licensed organization; the error
// of one organization does not stop the others.
func (a *ScanApplier) Run(ctx context.Context) error {
	orgs, err := a.orgs(ctx)
	if err != nil {
		return fmt.Errorf("ipam scan apply: %w", err)
	}
	var errs []error
	for _, orgID := range orgs {
		if a.Licensed != nil && !a.Licensed(ctx, orgID) {
			continue
		}
		if _, err := a.RunOrg(ctx, orgID); err != nil {
			slog.ErrorContext(ctx, "ipam scan apply", "organization_id", orgID, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RunOrg applies the results of one organization in a tenant transaction
// under the advisory lock ipam_scan_apply:<org> and then synchronizes the
// targets of the changed subnets in a second one.
func (a *ScanApplier) RunOrg(ctx context.Context, orgID string) (ScanApplyResult, error) {
	var res ScanApplyResult
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		res, err = a.repo.ApplyScanResults(ctx, tx, orgID)
		return err
	})
	if err != nil {
		return res, err
	}
	for state, n := range res.Changed {
		ScanUpdates.WithLabelValues(state).Add(float64(n))
	}
	if res.NewHosts > 0 {
		ScanUpdates.WithLabelValues("new_host").Add(float64(res.NewHosts))
	}
	if res.Skipped || !res.Scanned || a.repo.scanProbes == nil {
		return res, nil
	}
	err = database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		ids, ferr := scanSubnetsToResync(ctx, tx, orgID)
		if ferr != nil || len(ids) == 0 {
			return ferr
		}
		// The budget lock first, outside the savepoints, so the order
		// budget → subnet holds for every subnet (WP-940 AK3).
		if lerr := a.repo.scanProbes.LockProbeBudget(ctx, tx, orgID); lerr != nil {
			return lerr
		}
		ctx = database.ContextWithTx(ctx, tx)
		for _, id := range ids {
			serr := database.Savepoint(ctx, func(ctx context.Context) error {
				return a.repo.SyncScanTargets(ctx, tx, orgID, []string{id})
			})
			if serr == nil {
				res.Resynced++
				continue
			}
			// Skipped: the others go on; the subnet moves to the end.
			res.ResyncFailed++
			slog.WarnContext(ctx, "ipam scan resync skipped", "organization_id", orgID, "subnet_id", id, "error", serr)
			if _, ferr := tx.Exec(ctx, `UPDATE subnet SET scan_sync_failed_at = now() WHERE organization_id = $1 AND id = $2`,
				orgID, id); ferr != nil {
				return fmt.Errorf("mark scan resync failed: %w", ferr)
			}
		}
		return nil
	})
	return res, err
}

// scanHistorySlack widens the comparison of the history with
// scan_synced_at: both are now() of their transactions, so a change
// committed after a run by a transaction that began before it carries an
// older changed_at than scan_synced_at.
const scanHistorySlack = "10 minutes"

// scanSubnetsToResync returns at most scanResyncBatch scanned subnets whose
// targets are out of date (AK6). The address criteria compare states, not
// timestamps, and heal themselves: an address of the subnet without a
// target (created, moved in, renumbered), a target without an address of
// the subnet outside discover_new_hosts (deleted, moved out, renumbered).
// The history covers what the states cannot show: an address deleted or
// moved out of a subnet with discover_new_hosts keeps its target (only its
// PTR check goes), compared with scan_synced_at less scanHistorySlack (a
// subnet is then picked again for at most that long), and a changed CIDR
// (PATCH /subnets/{id} synchronizes it in its own transaction already;
// the history is the fallback). The oldest
// synchronization or failed attempt (scan_sync_failed_at) comes first. The
// scope is widened (sections with rights, WP-910): the job has no user.
func scanSubnetsToResync(ctx context.Context, tx pgx.Tx, orgID string) ([]string, error) {
	rows, err := tx.Query(ctx, `WITH w AS MATERIALIZED (SELECT app_scope_widen()::text AS previous)
		SELECT s.id::text FROM w, subnet s
		 WHERE s.organization_id = $1 AND s.scan_enabled
		   AND (s.scan_synced_at IS NULL
		        OR EXISTS (SELECT 1 FROM ip_address a
		                    WHERE a.organization_id = $1 AND a.subnet_id = s.id
		                      AND NOT EXISTS (SELECT 1 FROM subnet_scan_target t
		                                       WHERE t.subnet_id = s.id AND t.address = a.address_host::inet))
		        OR (NOT (s.discover_new_hosts AND family(s.cidr) = 4)
		            AND EXISTS (SELECT 1 FROM subnet_scan_target t
		                         WHERE t.organization_id = $1 AND t.subnet_id = s.id
		                           AND NOT EXISTS (SELECT 1 FROM ip_address a
		                                            WHERE a.organization_id = $1 AND a.subnet_id = s.id
		                                              AND a.address_host = host(t.address))))
		        OR (s.discover_new_hosts AND family(s.cidr) = 4
		            AND EXISTS (SELECT 1 FROM ipam_change c
		                         WHERE c.organization_id = $1 AND c.object_kind = 'ip_address'
		                           AND c.changed_at > s.scan_synced_at - $3::interval
		                           AND ((c.change_type = 'delete' AND c.old_value->>'subnet_id' = s.id::text)
		                                OR (c.change_type = 'update' AND c.field_name = 'subnet_id'
		                                    AND (c.old_value #>> '{}' = s.id::text OR c.new_value #>> '{}' = s.id::text)))))
		        OR EXISTS (SELECT 1 FROM ipam_change c
		                    WHERE c.organization_id = $1 AND c.object_kind = 'subnet' AND c.object_id = s.id
		                      AND c.field_name = 'cidr' AND c.changed_at > s.scan_synced_at))
		 ORDER BY GREATEST(s.scan_synced_at, s.scan_sync_failed_at) NULLS FIRST, s.id
		 LIMIT $2`, orgID, scanResyncBatch, scanHistorySlack)
	if err != nil {
		return nil, fmt.Errorf("find subnets to resync: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("find subnets to resync: %w", err)
	}
	return ids, nil
}

// ApplyScanResults applies the probe states of the scan targets of orgID in
// tx (AK2 to AK5) with at most six statements, whatever the number of
// targets: (1) the change source scan, the widened scope and the advisory
// lock with the check for a scanned subnet, (2) scan_applied_at of the
// scanned subnets (the subnet rows before the addresses), (3) the states
// and last_seen_at of the known addresses, (4) the new hosts, (5) the events in one outbox
// statement as the last write (Konvention 20). Only rows whose state or
// last_seen_at differ are written.
func (r *PGRepository) ApplyScanResults(ctx context.Context, tx pgx.Tx, orgID string) (ScanApplyResult, error) {
	res := ScanApplyResult{Changed: map[string]int{}}
	var locked bool
	if err := tx.QueryRow(ctx, `WITH w AS MATERIALIZED (
			SELECT set_config('app.ipam_change_source', 'scan', true) AS source, app_scope_widen()::text AS previous
		)
		SELECT pg_try_advisory_xact_lock(hashtextextended('ipam_scan_apply:' || $1::uuid::text, 0)),
		       EXISTS (SELECT 1 FROM subnet s WHERE s.organization_id = $1 AND s.scan_enabled)
		  FROM w`, orgID).Scan(&locked, &res.Scanned); err != nil {
		return res, fmt.Errorf("lock scan apply: %w", err)
	}
	if !locked {
		res.Skipped = true
		return res, nil
	}
	if !res.Scanned {
		return res, nil
	}
	// The subnet rows first, then the addresses: the order of PATCH
	// /subnets/{id}, whose trigger subnet_scope_propagation writes the
	// addresses after the subnet row (no deadlock between the two).
	if _, err := tx.Exec(ctx, `UPDATE subnet SET scan_applied_at = now() WHERE organization_id = $1 AND scan_enabled`, orgID); err != nil {
		return res, fmt.Errorf("mark scan applied: %w", err)
	}
	var batch []*events.Event
	rows, err := tx.Query(ctx, `WITH obs AS (
			SELECT DISTINCT ON (a.id) a.id, a.state AS old_state, a.last_seen_at AS old_seen,
			       ps.state AS probe_state, ps.since, ps.last_result_at, s.offline_after_s
			  FROM subnet_scan_target t
			  JOIN subnet s ON s.organization_id = t.organization_id AND s.id = t.subnet_id AND s.scan_enabled
			  JOIN probe_state ps ON ps.organization_id = t.organization_id AND ps.probe_id = t.probe_id
			  JOIN ip_address a ON a.organization_id = t.organization_id AND a.vrf_id = s.vrf_id
			                   AND a.address_host = host(t.address)
			 WHERE t.organization_id = $1
			 ORDER BY a.id, (a.subnet_id = t.subnet_id) DESC, masklen(s.cidr) DESC
		), next AS (
			SELECT id, old_state,
			       CASE WHEN probe_state IN ('up', 'degraded') THEN 'active'
			            -- After an unknown phase the grace counts from the start of
			            -- the down state: the time unknown does not count (AK2).
			            WHEN probe_state = 'down'
			                 AND GREATEST(COALESCE(old_seen, since), CASE WHEN old_state = 'unknown' THEN since END)
			                     < now() - make_interval(secs => offline_after_s) THEN 'offline'
			            WHEN probe_state = 'unknown' THEN 'unknown'
			            ELSE old_state END AS new_state,
			       CASE WHEN probe_state IN ('up', 'degraded') THEN GREATEST(old_seen, last_result_at)
			            ELSE old_seen END AS new_seen
			  FROM obs
		)
		UPDATE ip_address a
		   SET state = n.new_state, last_seen_at = n.new_seen,
		       state_changed_at = CASE WHEN a.state IS DISTINCT FROM n.new_state THEN now() ELSE a.state_changed_at END
		  FROM next n
		 WHERE a.organization_id = $1 AND a.id = n.id
		   AND (a.state IS DISTINCT FROM n.new_state OR a.last_seen_at IS DISTINCT FROM n.new_seen)
		RETURNING a.id::text, COALESCE(a.subnet_id::text, ''), COALESCE(a.client_id::text, ''), COALESCE(a.site_id::text, ''),
		          COALESCE(a.ci_id::text, ''), COALESCE(n.old_state, ''), COALESCE(a.state, '')`, orgID)
	if err != nil {
		return res, fmt.Errorf("apply scan states: %w", err)
	}
	for rows.Next() {
		var d events.AddressStateChangedData
		if err = rows.Scan(&d.ID, &d.SubnetID, &d.ClientID, &d.SiteID, &d.CIID, &d.From, &d.To); err != nil {
			rows.Close()
			return res, fmt.Errorf("apply scan states: %w", err)
		}
		if d.From == d.To {
			continue // last_seen_at only: no event
		}
		res.Changed[d.To]++
		batch = append(batch, &events.Event{OrgID: orgID, Type: events.IPAMAddressStateChanged, EntityID: d.ID, Data: d})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return res, fmt.Errorf("apply scan states: %w", err)
	}
	// New hosts (AK5): an answering target of a subnet with
	// discover_new_hosts without an address of its VRF; the narrowest
	// scanned subnet takes it. A row a discovery sync inserts at the same
	// time wins through the unique index of the VRF (000092).
	rows, err = tx.Query(ctx, `INSERT INTO ip_address (organization_id, subnet_id, address, type, origin, state, last_seen_at, state_changed_at)
		SELECT x.organization_id, x.subnet_id, x.address, 'discovered', 'scan', 'active', x.last_result_at, now()
		  FROM (SELECT DISTINCT ON (s.vrf_id, host(t.address)) t.organization_id, t.subnet_id, t.address, ps.last_result_at
		          FROM subnet_scan_target t
		          JOIN subnet s ON s.organization_id = t.organization_id AND s.id = t.subnet_id
		                       AND s.scan_enabled AND s.discover_new_hosts
		          JOIN probe_state ps ON ps.organization_id = t.organization_id AND ps.probe_id = t.probe_id
		                             AND ps.state IN ('up', 'degraded')
		         WHERE t.organization_id = $1
		           AND NOT EXISTS (SELECT 1 FROM ip_address a
		                            WHERE a.organization_id = $1 AND a.vrf_id = s.vrf_id AND a.address_host = host(t.address))
		         ORDER BY s.vrf_id, host(t.address), masklen(s.cidr) DESC) x
		ON CONFLICT DO NOTHING
		RETURNING id::text, COALESCE(subnet_id::text, ''), COALESCE(client_id::text, ''), COALESCE(site_id::text, '')`, orgID)
	if err != nil {
		return res, fmt.Errorf("create scanned hosts: %w", err)
	}
	for rows.Next() {
		d := events.AddressStateChangedData{To: "active"}
		if err = rows.Scan(&d.ID, &d.SubnetID, &d.ClientID, &d.SiteID); err != nil {
			rows.Close()
			return res, fmt.Errorf("create scanned hosts: %w", err)
		}
		res.NewHosts++
		batch = append(batch, &events.Event{OrgID: orgID, Type: events.IPAMAddressStateChanged, EntityID: d.ID, Data: d})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return res, fmt.Errorf("create scanned hosts: %w", err)
	}
	if r.outbox == nil || len(batch) == 0 {
		return res, nil
	}
	return res, database.BeforeCommit(ctx, func() error {
		_, err := r.outbox.AppendMany(ctx, tx, batch)
		return err
	})
}
