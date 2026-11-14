package probe

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The assignment engine (PRB-03, PRB-11, E-174, WP-941): every active probe
// of a collector group runs on exactly one healthy member. In the mode
// distributed weighted rendezvous hashing picks the member (the score of
// hash(probe_id, collector_id) and weight, the highest wins), so a member
// that leaves moves only its own probes and a member that joins takes only
// probes to itself. In the mode failover every probe runs on the healthy
// member with the highest priority (on a tie the smaller collector id).
// A suspended probe (suspended_reason, WP-942) is treated like a disabled
// one: it loses its assignment. Without a healthy member the assignments
// stay as they are (network down, WP-964). A member is healthy by its
// monitoring liveness (member_status healthy or degraded, WP-963), not by
// the COL-05 status of its collector.
//
// Every changed assignment gets epoch + 1, the fencing token of PRB-11,
// and assigned_at; an unchanged one keeps its epoch. The epoch is counted
// from the high-water mark of the probe (table probe_epoch, migration
// 000338), the highest epoch ever issued for it, so it never goes down and
// is never issued twice, also when the assignment row went in between (a
// disable, a group change, a member that left) and the probe is assigned
// again; it is above the epoch of the probe's last state as well. The config_version of
// every collector whose set changed (the former and the new one) rises by
// one in the same transaction; after the commit AssignmentNotifier learns
// the collectors with their new version (the NATS hint .probes.changed
// follows with WP-948).

// AssignKind is the system job of the worker probe_assign.
const AssignKind = "probe.assign"

// DefaultAssignTick is the default interval of the system job probe.assign
// (RETICORA_PROBE_ASSIGN_TICK, WP-963 AK2): every tick sweeps the silent
// members (Liveness.Sweep) and then assigns every group.
const DefaultAssignTick = 5 * time.Second

// assignTimeout bounds an assignment run after a probe write.
const assignTimeout = 30 * time.Second

// Candidate is a member of a collector group as the assignment sees it.
// Capabilities is the capability report of its collector (PRT-18, WP-966);
// nil (no report yet, an older collector) counts as capable of everything.
type Candidate struct {
	CollectorID  string
	Priority     int
	Weight       int
	Healthy      bool
	Capabilities *probewire.ProbeCapabilities
}

// Capable reports whether the member can execute a probe of type typ on
// target (WP-966 AK5): the type is in its list of implemented types, a
// type that needs ICMP has an ICMP socket of the target's address family
// (either family for a host name), a type that needs the privileged UDP
// ports can bind 67 and 68. A member without report is capable.
func (c *Candidate) Capable(typ, target string) bool {
	caps := c.Capabilities
	if caps == nil {
		return true
	}
	if !caps.Implements(typ) {
		return false
	}
	t, ok := probewire.Lookup(typ)
	if !ok {
		return true
	}
	for _, need := range t.Capabilities {
		switch need {
		case probewire.NeedICMP:
			family := targetFamily(target)
			switch family {
			case 4, 6:
				if caps.ICMP(family) == probewire.CapabilityNone {
					return false
				}
			default:
				if caps.ICMP4 == probewire.CapabilityNone && caps.ICMP6 == probewire.CapabilityNone {
					return false
				}
			}
		case probewire.NeedPrivilegedUDPPorts:
			if !caps.UDP67 || !caps.UDP68 {
				return false
			}
		}
	}
	return true
}

// targetFamily returns the address family of an address literal in target
// (an address, host:port or [v6]:port): 4, 6 or 0 for a host name.
func targetFamily(target string) int {
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // zone of a link-local address
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return 0
	}
	if ip.Unmap().Is4() {
		return 4
	}
	return 6
}

// Assign computes the assignment of the active probes of group over its
// members: one entry per enabled probe with the collector that runs it
// (the epoch is left to the store). Without a healthy member it returns
// nil: the stored assignments stay unchanged.
//
// A probe goes only to a healthy member that can execute it (Capable,
// PRT-18, WP-966); the minimal movement of the rendezvous hashing and the
// failover priority hold among those. Without a capable healthy member it
// is assigned by the rule before, among all healthy members, and reports
// unknown with reason unsupported there, never down.
func Assign(group *CollectorGroup, members []Candidate, probes []Probe) []Assignment {
	healthy := make([]Candidate, 0, len(members))
	for _, m := range members {
		if m.Healthy {
			healthy = append(healthy, m)
		}
	}
	if len(healthy) == 0 {
		return nil
	}
	sort.Slice(healthy, func(i, j int) bool { return healthy[i].CollectorID < healthy[j].CollectorID })
	pick := func(id string, among []Candidate) string {
		if group.Mode == ModeFailover {
			best := among[0]
			for _, m := range among[1:] {
				if m.Priority > best.Priority {
					best = m
				}
			}
			return best.CollectorID
		}
		return rendezvous(id, among)
	}
	capable := make([]Candidate, 0, len(healthy))
	out := make([]Assignment, 0, len(probes))
	for i := range probes {
		p := &probes[i]
		if !p.Enabled {
			continue
		}
		capable = capable[:0]
		for j := range healthy {
			if healthy[j].Capable(p.Type, p.Target) {
				capable = append(capable, healthy[j])
			}
		}
		among := healthy
		if len(capable) > 0 {
			among = capable
		}
		collector := pick(p.ID, among)
		out = append(out, Assignment{ProbeID: p.ID, OrganizationID: p.OrganizationID, ClientID: p.ClientID, SiteID: p.SiteID,
			CollectorID: collector})
	}
	return out
}

// rendezvous picks the member with the highest weighted score for the
// probe: -weight / ln(u) with u in (0, 1) from hash(probe, collector)
// (weighted rendezvous hashing).
func rendezvous(probeID string, members []Candidate) string {
	best, bestScore := "", math.Inf(-1)
	for _, m := range members {
		weight := float64(max(m.Weight, 1))
		score := -weight / math.Log(unitHash(probeID, m.CollectorID))
		if score > bestScore {
			best, bestScore = m.CollectorID, score
		}
	}
	return best
}

// unitHash maps (probe, collector) to (0, 1): FNV-1a 64 over both ids,
// mixed with the finalizer of splitmix64.
func unitHash(probeID, collectorID string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(probeID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(collectorID))
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return (float64(x>>11) + 0.5) / (1 << 53)
}

// AssignmentPlan is what a run changes in a group: the assignments to
// write (new or moved; the store raises their epoch), the probes whose
// assignment goes (no longer active) and the collectors whose set changes
// (sorted).
type AssignmentPlan struct {
	Upserts    []Assignment
	Removes    []string
	Collectors []string
}

// Empty reports whether the plan changes nothing.
func (p *AssignmentPlan) Empty() bool { return len(p.Upserts) == 0 && len(p.Removes) == 0 }

// PlanAssignments compares the stored assignments of a group with the
// computed ones (Assign). desired nil (no healthy member) changes nothing.
func PlanAssignments(current, desired []Assignment) AssignmentPlan {
	var plan AssignmentPlan
	if desired == nil {
		return plan
	}
	stored := make(map[string]string, len(current))
	for _, a := range current {
		stored[a.ProbeID] = a.CollectorID
	}
	touched := map[string]bool{}
	wanted := make(map[string]bool, len(desired))
	for _, a := range desired {
		wanted[a.ProbeID] = true
		old, ok := stored[a.ProbeID]
		if ok && old == a.CollectorID {
			continue
		}
		plan.Upserts = append(plan.Upserts, a)
		touched[a.CollectorID] = true
		if ok {
			touched[old] = true
		}
	}
	for _, a := range current {
		if !wanted[a.ProbeID] {
			plan.Removes = append(plan.Removes, a.ProbeID)
			touched[a.CollectorID] = true
		}
	}
	for c := range touched {
		plan.Collectors = append(plan.Collectors, c)
	}
	slices.Sort(plan.Collectors)
	slices.Sort(plan.Removes)
	return plan
}

// CollectorVersion is a collector with the version of its assignment set.
type CollectorVersion struct {
	CollectorID   string
	ConfigVersion int64
}

// AssignmentNotifier learns after the commit which collectors of the
// organization have a new assignment set (WP-941 AK5); WP-948 sends
// .probes.changed.
type AssignmentNotifier interface {
	AssignmentsChanged(ctx context.Context, orgID string, collectors []CollectorVersion)
}

// LogNotifier is the AssignmentNotifier of this stage: it logs the
// collectors with their new version.
type LogNotifier struct{}

// AssignmentsChanged implements AssignmentNotifier.
func (LogNotifier) AssignmentsChanged(_ context.Context, orgID string, collectors []CollectorVersion) {
	for _, c := range collectors {
		slog.Info("probe assignment changed", "organization_id", orgID, "collector_id", c.CollectorID,
			"config_version", c.ConfigVersion)
	}
}

// AssignTrigger runs the assignment of collector groups after a probe
// write has committed (WP-941 AK4).
type AssignTrigger interface {
	// ProbesChanged is called once the write committed, with the groups
	// to assign again and the collectors whose version the write raised
	// itself (an assigned probe that was deleted).
	ProbesChanged(ctx context.Context, orgID string, groupIDs []string, raised []CollectorVersion)
}

// Assigner runs the assignment engine on PostgreSQL: the system job
// probe.assign of the worker probe_assign and the run after a probe write.
type Assigner struct {
	pool     *pgxpool.Pool
	notifier AssignmentNotifier
	// liveness sweeps the silent members before every run (WP-963); nil
	// leaves the member status as it is.
	liveness *Liveness
	tick     time.Duration
	now      func() time.Time
	// locked runs after the group lock is held (tests).
	locked func(groupID string)
}

// NewAssigner returns the engine on pool with the logging notifier.
func NewAssigner(pool *pgxpool.Pool) *Assigner {
	return &Assigner{pool: pool, notifier: LogNotifier{}, tick: DefaultAssignTick, now: time.Now}
}

// WithLiveness sweeps the members of every group with l before each run of
// the system job (WP-963 AK2).
func (a *Assigner) WithLiveness(l *Liveness) *Assigner {
	a.liveness = l
	return a
}

// WithTick sets the interval of the system job; a value of at most zero
// keeps DefaultAssignTick.
func (a *Assigner) WithTick(d time.Duration) *Assigner {
	if d > 0 {
		a.tick = d
	}
	return a
}

// WithNotifier sets the notifier of the changed collectors.
func (a *Assigner) WithNotifier(n AssignmentNotifier) *Assigner {
	a.notifier = n
	return a
}

// Schedule is the system job probe.assign (every tick, default 5 s).
func (a *Assigner) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: AssignKind, Spec: "@every " + a.tick.String(), MaxCatchUp: 1, Run: func(ctx context.Context) error {
		_, err := a.Tick(ctx, a.now())
		return err
	}}
}

// Tick is one run of the system job at now: the liveness sweep (it assigns
// the groups of the members it sets down at once) and then the assignment
// of every group. A failed sweep does not stop the assignment.
func (a *Assigner) Tick(ctx context.Context, now time.Time) (int, error) {
	var serr error
	if a.liveness != nil {
		_, serr = a.liveness.Sweep(ctx, now)
	}
	n, err := a.Run(ctx)
	return n, errors.Join(serr, err)
}

// Run assigns every collector group of every organization. A failing
// organization does not stop the others; the errors come back joined. It
// returns the groups that ran (a group another instance holds is skipped).
func (a *Assigner) Run(ctx context.Context) (int, error) {
	orgs, err := database.OrganizationIDs(ctx, a.pool)
	if err != nil {
		return 0, err
	}
	ran := 0
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		n, oerr := a.RunOrg(ctx, org)
		ran += n
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return ran, errors.Join(errs...)
}

// RunOrg assigns every collector group of the organization.
func (a *Assigner) RunOrg(ctx context.Context, orgID string) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	var groups []string
	err := database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM collector_group WHERE organization_id = $1 ORDER BY id`, orgID)
		if err != nil {
			return err
		}
		groups, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("list collector groups: %w", err)
	}
	ran := 0
	var errs []error
	for _, g := range groups {
		ok, gerr := a.RunGroup(ctx, orgID, g)
		if gerr != nil {
			errs = append(errs, fmt.Errorf("collector group %s: %w", g, gerr))
			continue
		}
		if ok {
			ran++
		}
	}
	return ran, errors.Join(errs...)
}

// RunGroup assigns the probes of one group in one transaction under the
// advisory lock probe_assign:<group> (pg_try_advisory_xact_lock): while
// another instance runs the group it returns false without a change.
func (a *Assigner) RunGroup(ctx context.Context, orgID, groupID string) (bool, error) {
	scope := database.OrgWideScope(orgID, "")
	ran := false
	var changed []CollectorVersion
	err := database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('probe_assign:' || $1, 0))`, groupID).Scan(&ran); err != nil {
			return fmt.Errorf("lock collector group: %w", err)
		}
		if !ran {
			return nil
		}
		if a.locked != nil {
			a.locked(groupID)
		}
		var err error
		changed, err = assignGroup(ctx, tx, orgID, groupID)
		if err != nil || len(changed) == 0 {
			return err
		}
		database.AfterCommit(database.ContextWithTx(ctx, tx), func(ctx context.Context) {
			if a.notifier != nil {
				a.notifier.AssignmentsChanged(ctx, orgID, changed)
			}
		})
		return nil
	})
	return ran, err
}

// ProbesChanged implements AssignTrigger: it notifies the collectors the
// write raised and assigns the groups at once, detached from the request.
func (a *Assigner) ProbesChanged(ctx context.Context, orgID string, groupIDs []string, raised []CollectorVersion) {
	if len(raised) > 0 && a.notifier != nil {
		a.notifier.AssignmentsChanged(ctx, orgID, raised)
	}
	if len(groupIDs) == 0 {
		return
	}
	go func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, assignTimeout)
		defer cancel()
		for _, g := range groupIDs {
			if _, err := a.RunGroup(ctx, orgID, g); err != nil {
				slog.Warn("probe assignment after a probe write failed", "organization_id", orgID,
					"collector_group_id", g, "error", err)
			}
		}
	}(context.WithoutCancel(ctx))
}

// assignGroup reads the group, its members and probes and the stored
// assignments, writes the plan and raises the config_version of the
// touched collectors; it returns them with their new version.
func assignGroup(ctx context.Context, tx pgx.Tx, orgID, groupID string) ([]CollectorVersion, error) {
	group := CollectorGroup{ID: groupID, OrganizationID: orgID}
	err := tx.QueryRow(ctx, `SELECT mode FROM collector_group WHERE organization_id = $1 AND id = $2`, orgID, groupID).Scan(&group.Mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read collector group: %w", err)
	}
	members, err := groupCandidates(ctx, tx, orgID, groupID)
	if err != nil {
		return nil, err
	}
	// A suspended probe (WP-942) runs nowhere: it counts as disabled.
	rows, err := tx.Query(ctx, `SELECT p.id::text, p.enabled AND p.suspended_reason IS NULL, p.type, p.target FROM probe p
		WHERE p.organization_id = $1 AND p.collector_group_id = $2 ORDER BY p.id`, orgID, groupID)
	if err != nil {
		return nil, fmt.Errorf("read probes: %w", err)
	}
	probes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Probe, error) {
		p := Probe{OrganizationID: orgID}
		scanErr := row.Scan(&p.ID, &p.Enabled, &p.Type, &p.Target)
		return p, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("read probes: %w", err)
	}
	rows, err = tx.Query(ctx, `SELECT a.probe_id::text, a.collector_id::text, a.epoch FROM probe_assignment a
		JOIN probe p ON p.organization_id = a.organization_id AND p.id = a.probe_id
		WHERE a.organization_id = $1 AND p.collector_group_id = $2`, orgID, groupID)
	if err != nil {
		return nil, fmt.Errorf("read probe assignments: %w", err)
	}
	current, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Assignment, error) {
		var a Assignment
		scanErr := row.Scan(&a.ProbeID, &a.CollectorID, &a.Epoch)
		return a, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("read probe assignments: %w", err)
	}
	plan := PlanAssignments(current, Assign(&group, members, probes))
	if plan.Empty() {
		return nil, nil
	}
	return writePlan(ctx, tx, orgID, groupID, &plan)
}

// lockPlanProbes locks the probes of plan FOR SHARE in the order of their
// id before any assignment row, the lock order of the probe writes (the
// probe row, then its assignment: a delete cascades, a group change drops
// it by trigger), so a run and a write wait for each other instead of
// deadlocking; the trigger probe_assignment_scope reads the probe FOR
// SHARE as well. Upserts whose probe went, was disabled or left the group
// since the run read it are dropped (the run after that write assigns it).
func lockPlanProbes(ctx context.Context, tx pgx.Tx, orgID, groupID string, plan *AssignmentPlan) error {
	ids := make([]string, 0, len(plan.Upserts)+len(plan.Removes))
	for _, a := range plan.Upserts {
		ids = append(ids, a.ProbeID)
	}
	ids = append(ids, plan.Removes...)
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text FROM probe p
		WHERE p.organization_id = $1 AND p.id = ANY ($2::uuid[]) AND p.enabled AND p.suspended_reason IS NULL
		  AND p.collector_group_id = $3
		ORDER BY p.id FOR SHARE`, orgID, ids, groupID)
	if err != nil {
		return fmt.Errorf("lock probes: %w", err)
	}
	valid, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("lock probes: %w", err)
	}
	keep := make(map[string]bool, len(valid))
	for _, id := range valid {
		keep[id] = true
	}
	plan.Upserts = slices.DeleteFunc(plan.Upserts, func(a Assignment) bool { return !keep[a.ProbeID] })
	return nil
}

// writePlan removes and writes the assignments of plan (the epoch is the
// high-water mark of the probe + 1) and raises the config_version of its
// collectors.
func writePlan(ctx context.Context, tx pgx.Tx, orgID, groupID string, plan *AssignmentPlan) ([]CollectorVersion, error) {
	if err := lockPlanProbes(ctx, tx, orgID, groupID, plan); err != nil {
		return nil, err
	}
	if len(plan.Removes) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM probe_assignment WHERE organization_id = $1 AND probe_id = ANY ($2::uuid[])`,
			orgID, plan.Removes); err != nil {
			return nil, fmt.Errorf("remove probe assignments: %w", err)
		}
	}
	if len(plan.Upserts) > 0 {
		probes := make([]string, len(plan.Upserts))
		collectors := make([]string, len(plan.Upserts))
		for i, a := range plan.Upserts {
			probes[i], collectors[i] = a.ProbeID, a.CollectorID
		}
		// The high-water mark rises first (the largest of it, the stored
		// assignment and the last state, + 1); the assignment takes it.
		if _, err := tx.Exec(ctx, `WITH u AS (
				SELECT * FROM unnest($2::uuid[], $3::uuid[]) AS u (probe_id, collector_id)
			), hw AS (
				INSERT INTO probe_epoch AS h (probe_id, organization_id, epoch)
				SELECT u.probe_id, $1, GREATEST(COALESCE(s.epoch, 0), COALESCE(a.epoch, 0)) + 1
				  FROM u
				  LEFT JOIN probe_state s ON s.organization_id = $1 AND s.probe_id = u.probe_id
				  LEFT JOIN probe_assignment a ON a.organization_id = $1 AND a.probe_id = u.probe_id
				ON CONFLICT (probe_id) DO UPDATE SET epoch = GREATEST(h.epoch, EXCLUDED.epoch - 1) + 1
				RETURNING h.probe_id, h.epoch
			)
			INSERT INTO probe_assignment AS a (probe_id, organization_id, collector_id, epoch, assigned_at)
			SELECT u.probe_id, $1, u.collector_id, hw.epoch, now() FROM u JOIN hw ON hw.probe_id = u.probe_id
			ON CONFLICT (probe_id) DO UPDATE SET collector_id = EXCLUDED.collector_id,
				epoch = EXCLUDED.epoch, assigned_at = EXCLUDED.assigned_at
			WHERE a.collector_id IS DISTINCT FROM EXCLUDED.collector_id`, orgID, probes, collectors); err != nil {
			return nil, fmt.Errorf("write probe assignments: %w", err)
		}
	}
	return raiseVersions(ctx, tx, orgID, plan.Collectors)
}

// raiseVersions raises the config_version of the collectors by one.
func raiseVersions(ctx context.Context, tx pgx.Tx, orgID string, collectors []string) ([]CollectorVersion, error) {
	if len(collectors) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `UPDATE collector_group_member SET config_version = config_version + 1
		WHERE organization_id = $1 AND collector_id = ANY ($2::uuid[])
		RETURNING collector_id::text, config_version`, orgID, collectors)
	if err != nil {
		return nil, fmt.Errorf("raise config versions: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CollectorVersion, error) {
		var v CollectorVersion
		scanErr := row.Scan(&v.CollectorID, &v.ConfigVersion)
		return v, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("raise config versions: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CollectorID < out[j].CollectorID })
	return out, nil
}
