package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The monitoring liveness of the collector group members (PRB-03, PRB-10,
// PRB-11, WP-963, E-174, E-176). Every monitoring alive message
// (.probe.alive, every 10 s) writes collector_group_member.last_alive_at
// (WP-962). A member is down once now − last_alive_at exceeds the
// liveness timeout of its group (collector_group.liveness_timeout,
// default 30 s, 20–300 s); Sweep sets member_status = down, before every
// run of the assignment (system job probe.assign, every 5 s). A down member
// becomes healthy again (degraded when its clock still deviates by more
// than 60 s, WP-962) with the second alive message within one timeout: the
// alive message whose predecessor is at most one timeout old (ClockSkew
// .Observe). The status is independent of the COL-05 status of the
// collector (heartbeat, offline after 5 min). A member that never sent a
// monitoring alive message (last_alive_at NULL: a collector without the
// probe engine or not started yet) is not swept and takes no probes
// (groupCandidates); its first alive message assigns its group at once.
//
// A member that goes down is swept in the transaction that assigns its
// group again (under the lock probe_assign:<group>): its probes move to the
// remaining healthy members (rendezvous or failover priority, WP-941) with a
// new epoch, and the event collector_group.failover names the failed member
// and the number of probes that moved. Without a remaining healthy member
// the assignments stay (network down, WP-964) and no failover event is
// written. Results and changes of the failed member with the old epoch are
// history only (stale, PRB-11 split-brain protection, WP-961).

// Assignable reports whether a member of status takes probes: healthy or
// degraded (WP-963 AK2).
func Assignable(status string) bool { return status == MemberHealthy || status == MemberDegraded }

// SilentMember reports whether a member whose last alive message arrived at
// lastAlive is down at now under timeout (AK1). Without any alive message
// (nil) it is not swept.
func SilentMember(lastAlive *time.Time, now time.Time, timeout time.Duration) bool {
	return lastAlive != nil && now.Sub(*lastAlive) > timeout
}

// MemberReturns reports whether an alive message at at, whose predecessor
// arrived at prevAlive, is the second alive message within one timeout: a
// down member becomes assignable again (AK1).
func MemberReturns(prevAlive *time.Time, at time.Time, timeout time.Duration) bool {
	return prevAlive != nil && at.After(*prevAlive) && at.Sub(*prevAlive) <= timeout
}

// Liveness sweeps the group members whose alive messages stopped.
type Liveness struct {
	pool     *pgxpool.Pool
	notifier AssignmentNotifier
}

// NewLiveness returns the sweep on pool with the logging notifier.
func NewLiveness(pool *pgxpool.Pool) *Liveness {
	return &Liveness{pool: pool, notifier: LogNotifier{}}
}

// WithNotifier sets the notifier of the collectors whose assignment set a
// failover changed.
func (l *Liveness) WithNotifier(n AssignmentNotifier) *Liveness {
	l.notifier = n
	return l
}

// Failover is one member a sweep set down with the probes that moved away
// from it.
type Failover struct {
	OrganizationID string
	GroupID        string
	CollectorID    string
	MovedProbes    int
	// TakenOver is false when no healthy member remained (network down,
	// WP-964): the assignments stayed, no event was written.
	TakenOver bool
}

// Sweep sets every member down whose last alive message is older than the
// liveness timeout of its group at now and assigns its group again (see
// the package comment). A failing organization does not stop the others.
func (l *Liveness) Sweep(ctx context.Context, now time.Time) ([]Failover, error) {
	orgs, err := database.OrganizationIDs(ctx, l.pool)
	if err != nil {
		return nil, err
	}
	var out []Failover
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		f, oerr := l.SweepOrg(ctx, org, now)
		out = append(out, f...)
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return out, errors.Join(errs...)
}

// SweepOrg sweeps the groups of one organization.
func (l *Liveness) SweepOrg(ctx context.Context, orgID string, now time.Time) ([]Failover, error) {
	scope := database.OrgWideScope(orgID, "")
	var groups []string
	err := database.WithTenant(ctx, l.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT m.group_id::text FROM collector_group_member m
			JOIN collector_group g ON g.organization_id = m.organization_id AND g.id = m.group_id
			WHERE m.organization_id = $1 AND m.member_status <> 'down' AND m.last_alive_at < $2::timestamptz - g.liveness_timeout
			ORDER BY 1`, orgID, now.UTC())
		if err != nil {
			return err
		}
		groups, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("find silent group members: %w", err)
	}
	var out []Failover
	var errs []error
	for _, g := range groups {
		f, gerr := l.sweepGroup(ctx, orgID, g, now)
		if gerr != nil {
			errs = append(errs, fmt.Errorf("collector group %s: %w", g, gerr))
			continue
		}
		out = append(out, f...)
	}
	return out, errors.Join(errs...)
}

// sweepGroup sets the silent members of one group down and assigns the
// group again in one transaction under its assignment lock; a group another
// instance holds is left to the next sweep.
func (l *Liveness) sweepGroup(ctx context.Context, orgID, groupID string, now time.Time) ([]Failover, error) {
	scope := database.OrgWideScope(orgID, "")
	var out []Failover
	err := database.WithTenant(ctx, l.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		out = nil
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('probe_assign:' || $1, 0))`, groupID).Scan(&locked); err != nil {
			return fmt.Errorf("lock collector group: %w", err)
		}
		if !locked {
			return nil
		}
		rows, err := tx.Query(ctx, `UPDATE collector_group_member m SET member_status = 'down'
			  FROM collector_group g
			 WHERE g.organization_id = m.organization_id AND g.id = m.group_id
			   AND m.organization_id = $1 AND m.group_id = $2 AND m.member_status <> 'down'
			   AND m.last_alive_at < $3::timestamptz - g.liveness_timeout
			RETURNING m.collector_id::text`, orgID, groupID, now.UTC())
		if err != nil {
			return fmt.Errorf("set members down: %w", err)
		}
		down, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("set members down: %w", err)
		}
		if len(down) == 0 {
			return nil
		}
		before, err := assignedCounts(ctx, tx, orgID, down)
		if err != nil {
			return err
		}
		changed, err := assignGroup(ctx, tx, orgID, groupID)
		if err != nil {
			return err
		}
		after, err := assignedCounts(ctx, tx, orgID, down)
		if err != nil {
			return err
		}
		var client, site *string
		var remaining bool
		if err := tx.QueryRow(ctx, `SELECT g.client_id::text, g.site_id::text,
				EXISTS (SELECT 1 FROM collector_group_member m WHERE m.organization_id = g.organization_id AND m.group_id = g.id
				          AND m.member_status IN ('healthy', 'degraded') AND m.last_alive_at IS NOT NULL)
			  FROM collector_group g WHERE g.organization_id = $1 AND g.id = $2`, orgID, groupID).Scan(&client, &site, &remaining); err != nil {
			return fmt.Errorf("read collector group: %w", err)
		}
		for _, c := range down {
			out = append(out, Failover{OrganizationID: orgID, GroupID: groupID, CollectorID: c,
				MovedProbes: before[c] - after[c], TakenOver: remaining})
		}
		txCtx := database.ContextWithTx(ctx, tx)
		if len(changed) > 0 {
			database.AfterCommit(txCtx, func(ctx context.Context) {
				if l.notifier != nil {
					l.notifier.AssignmentsChanged(ctx, orgID, changed)
				}
			})
		}
		if !remaining {
			return nil
		}
		return database.BeforeCommit(txCtx, func() error {
			outbox := events.NewOutbox()
			for _, f := range out {
				data := events.CollectorGroupFailoverData{ID: groupID, CollectorGroupID: groupID, ClientID: deref(client),
					SiteID: deref(site), FailedCollectorID: f.CollectorID, MovedProbes: f.MovedProbes}
				if _, err := outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CollectorGroupFailover,
					EntityID: groupID, Data: data}); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	for _, f := range out {
		slog.Warn("collector group member down: no monitoring alive message within the liveness timeout",
			"organization_id", orgID, "collector_group_id", groupID, "collector_id", f.CollectorID,
			"moved_probes", f.MovedProbes, "taken_over", f.TakenOver)
	}
	return out, nil
}

// assignedCounts counts the assignments of the collectors.
func assignedCounts(ctx context.Context, tx pgx.Tx, orgID string, collectors []string) (map[string]int, error) {
	rows, err := tx.Query(ctx, `SELECT collector_id::text, count(*) FROM probe_assignment
		WHERE organization_id = $1 AND collector_id = ANY ($2::uuid[]) GROUP BY collector_id`, orgID, collectors)
	if err != nil {
		return nil, fmt.Errorf("count assignments: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			return nil, fmt.Errorf("count assignments: %w", err)
		}
		out[c] = n
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
