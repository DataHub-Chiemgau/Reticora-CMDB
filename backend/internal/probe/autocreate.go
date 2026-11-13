package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Automatic ping probes (MON-02, PRB-01, WP-949, E-182 option 1): with the
// organization setting monitoring.auto_ping (bool, default false) and the
// feature monitoring, the system job probe.autocreate.dispatch submits
// every 15 minutes one job probe.autocreate per organization (at most one
// open). The job creates for every CI that is not deleted, has the status
// active, an observable type (ci_type.is_observable) and a management IP an
// icmp probe with target host(management_ip), origin auto and no
// parameters of its own (the type defaults 30 s / 10 s / 5 / 1), in the
// collector group of the collector of the CI's newest ci_change with source
// collector. Without such a collector the CI is skipped with no_collector,
// with a collector or group of another client with client_mismatch, with a
// suppression with suppressed. The job is idempotent: a CI with an icmp
// probe gets none more (probe_target_key, ON CONFLICT DO NOTHING); an auto
// probe follows a changed management IP, a manual probe is never touched.
// Every creation is admitted against the probe budget (WP-940) in the job's
// transaction under the budget lock; the first refusal (a quota or
// min_probe_interval_s) ends the creation for the remaining CIs (quota).
// The deletion of an auto probe writes a suppression (ci, probe_type) in
// its transaction: the job creates nothing more of that type for the CI.
// Switching the setting off deletes no auto probe.

// Kinds of the automatic ping probes.
const (
	AutocreateJobKind      = "probe.autocreate"
	AutocreateDispatchKind = "probe.autocreate.dispatch"
)

// AutocreateDispatchSpec submits the jobs every 15 minutes.
const AutocreateDispatchSpec = "@every 15m"

// SettingMonitoring and SettingAutoPing are the keys of the setting in
// organization.settings: {"monitoring": {"auto_ping": true}}.
const (
	SettingMonitoring = "monitoring"
	SettingAutoPing   = "auto_ping"
)

// Reasons a CI is skipped by the job.
const (
	SkipQuota          = "quota"
	SkipNoCollector    = "no_collector"
	SkipClientMismatch = "client_mismatch"
	SkipSuppressed     = "suppressed"
)

// MaxReportedIDs bounds the CI ids per reason in the report.
const MaxReportedIDs = 100

// AutoPingFromSettings reads monitoring.auto_ping of organization.settings
// (false without it or for a value that is no bool).
func AutoPingFromSettings(settings map[string]any) bool {
	m, _ := settings[SettingMonitoring].(map[string]any)
	on, _ := m[SettingAutoPing].(bool)
	return on
}

// SkipList is the CIs skipped for one reason: the number and at most
// MaxReportedIDs ids.
type SkipList struct {
	Count int      `json:"count"`
	CIIDs []string `json:"ci_ids"`
}

// AutocreateReport is the result of a job.
type AutocreateReport struct {
	Created int                  `json:"created"`
	Updated int                  `json:"updated"`
	Skipped map[string]*SkipList `json:"skipped"`
}

// NewAutocreateReport returns an empty report with every reason.
func NewAutocreateReport() *AutocreateReport {
	r := &AutocreateReport{Skipped: map[string]*SkipList{}}
	for _, reason := range []string{SkipQuota, SkipNoCollector, SkipClientMismatch, SkipSuppressed} {
		r.Skipped[reason] = &SkipList{CIIDs: []string{}}
	}
	return r
}

// Skip counts a skipped CI.
func (r *AutocreateReport) Skip(reason, ciID string) {
	l, ok := r.Skipped[reason]
	if !ok {
		l = &SkipList{CIIDs: []string{}}
		r.Skipped[reason] = l
	}
	l.Count++
	if len(l.CIIDs) < MaxReportedIDs {
		l.CIIDs = append(l.CIIDs, ciID)
	}
}

// AutoCandidate is a CI the job may create a ping probe for.
type AutoCandidate struct {
	CIID, ClientID, SiteID, ManagementIP string
	// Collector is the collector of the newest ci_change with source
	// collector, CollectorClient its client; Group and GroupClient the
	// collector group of the collector and its client.
	Collector, CollectorClient, Group, GroupClient string
	Suppressed                                     bool
	// AutoProbe and AutoTarget are the CI's icmp auto probe; HasManual
	// reports an icmp probe of origin manual.
	AutoProbe, AutoTarget string
	HasManual             bool
}

// SkipReason returns why the job creates no probe for the candidate ("":
// it does). An existing probe is no skip (see Plan).
func (c *AutoCandidate) SkipReason() string {
	switch {
	case c.Suppressed:
		return SkipSuppressed
	case c.Collector == "" || c.Group == "":
		return SkipNoCollector
	case c.CollectorClient != "" && c.CollectorClient != c.ClientID,
		c.GroupClient != "" && c.GroupClient != c.ClientID:
		return SkipClientMismatch
	}
	return ""
}

// AutoAction is what the job does for a candidate.
type AutoAction int

// Actions of the job.
const (
	AutoNone AutoAction = iota
	AutoCreate
	AutoRetarget
	AutoSkip
)

// Plan returns the action for the candidate: an auto probe on another
// target follows the management IP, an auto probe on the target and a
// manual icmp probe leave the CI alone, otherwise a probe is created
// unless a skip reason applies.
func (c *AutoCandidate) Plan() AutoAction {
	switch {
	case c.AutoProbe != "" && c.AutoTarget != c.ManagementIP:
		return AutoRetarget
	case c.AutoProbe != "" || c.HasManual:
		return AutoNone
	case c.SkipReason() != "":
		return AutoSkip
	}
	return AutoCreate
}

// suppressAutocreate writes the suppression of the CI and type of a deleted
// auto probe (the client and site of the probe, the acting user).
func suppressAutocreate(ctx context.Context, tx pgx.Tx, p *Probe) error {
	var actor any
	if u := tenant.FromContext(ctx).UserID; api.IsUUID(u) {
		actor = u
	}
	if _, err := tx.Exec(ctx, `INSERT INTO probe_autocreate_suppression (organization_id, client_id, site_id, ci_id, probe_type, actor_id)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (organization_id, ci_id, probe_type) DO NOTHING`,
		p.OrganizationID, nullID(p.ClientID), nullID(p.SiteID), p.CIID, p.Type, actor); err != nil {
		return fmt.Errorf("write autocreate suppression: %w", err)
	}
	return nil
}

// Autocreator runs the job probe.autocreate on the probe repository: its
// pool, its budget gate and its assignment trigger.
type Autocreator struct {
	repo *PGRepository
}

// errNoDatabase: the job needs the probe store on PostgreSQL.
var errNoDatabase = errors.New("automatic ping probes need the database")

// NewAutocreator returns the job on repo (nil: every job fails).
func NewAutocreator(repo *PGRepository) *Autocreator { return &Autocreator{repo: repo} }

// Kind is the job kind: one attempt (a retry would count the budget again
// only), at most 15 minutes.
func (a *Autocreator) Kind() jobs.Kind {
	return jobs.Kind{Name: AutocreateJobKind, MaxAttempts: 1, Timeout: 15 * time.Minute}
}

// RunJob is the handler of probe.autocreate.
func (a *Autocreator) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	return a.Run(ctx, job.OrganizationID)
}

// readSettings reads organization.settings in the transaction.
func readSettings(ctx context.Context, tx pgx.Tx, orgID string) (map[string]any, error) {
	var settings map[string]any
	err := tx.QueryRow(ctx, `SELECT COALESCE(settings, '{}'::jsonb) FROM organization WHERE id = $1`, orgID).Scan(&settings)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return settings, err
}

// candidates reads the CIs of the organization the job considers.
func candidates(ctx context.Context, tx pgx.Tx, orgID string) ([]AutoCandidate, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text, COALESCE(c.client_id::text, ''), COALESCE(c.site_id::text, ''), host(c.management_ip),
			COALESCE(col.id::text, ''), COALESCE(col.client_id::text, ''),
			COALESCE(m.group_id::text, ''), COALESCE(g.client_id::text, ''),
			EXISTS (SELECT 1 FROM probe_autocreate_suppression s
				WHERE s.organization_id = c.organization_id AND s.ci_id = c.id AND s.probe_type = 'icmp'),
			COALESCE(auto.id::text, ''), COALESCE(auto.target, ''),
			EXISTS (SELECT 1 FROM probe pm WHERE pm.organization_id = c.organization_id AND pm.ci_id = c.id
				AND pm.type = 'icmp' AND pm.origin = 'manual')
		FROM ci c
		JOIN ci_type t ON t.id = c.ci_type_id
		LEFT JOIN LATERAL (SELECT cc.collector_id FROM ci_change cc
			WHERE cc.organization_id = c.organization_id AND cc.ci_id = c.id AND cc.source = 'collector' AND cc.collector_id IS NOT NULL
			ORDER BY cc.created_at DESC, cc.id DESC LIMIT 1) last ON true
		LEFT JOIN collector col ON col.organization_id = c.organization_id AND col.id = last.collector_id
		LEFT JOIN collector_group_member m ON m.organization_id = c.organization_id AND m.collector_id = col.id
		LEFT JOIN collector_group g ON g.organization_id = c.organization_id AND g.id = m.group_id
		LEFT JOIN LATERAL (SELECT pa.id, pa.target FROM probe pa
			WHERE pa.organization_id = c.organization_id AND pa.ci_id = c.id AND pa.type = 'icmp' AND pa.origin = 'auto'
			ORDER BY pa.id LIMIT 1) auto ON true
		WHERE c.organization_id = $1 AND c.deleted_at IS NULL AND c.status = 'active' AND t.is_observable
		  AND c.management_ip IS NOT NULL
		ORDER BY c.id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("read autocreate candidates: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AutoCandidate, error) {
		var c AutoCandidate
		scanErr := row.Scan(&c.CIID, &c.ClientID, &c.SiteID, &c.ManagementIP, &c.Collector, &c.CollectorClient, &c.Group,
			&c.GroupClient, &c.Suppressed, &c.AutoProbe, &c.AutoTarget, &c.HasManual)
		return c, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("read autocreate candidates: %w", err)
	}
	return out, nil
}

// Run creates and updates the auto probes of the organization in one
// transaction and returns the report.
func (a *Autocreator) Run(ctx context.Context, orgID string) (*AutocreateReport, error) {
	r := a.repo
	if r == nil {
		return nil, errNoDatabase
	}
	report := NewAutocreateReport()
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		txCtx := database.ContextWithTx(ctx, tx)
		settings, err := readSettings(ctx, tx, orgID)
		if err != nil {
			return fmt.Errorf("read organization settings: %w", err)
		}
		if !AutoPingFromSettings(settings) {
			return nil
		}
		// The budget lock first, before every row lock (WP-940 AK3).
		if lerr := r.lockBudget(ctx, tx, orgID); lerr != nil {
			return lerr
		}
		list, err := candidates(ctx, tx, orgID)
		if err != nil {
			return err
		}
		var (
			planMin int64
			specs   []entitlement.EffectiveSpec
		)
		if r.budget != nil {
			if planMin, err = r.budget.MinProbeIntervalS(txCtx, orgID); err != nil {
				return err
			}
			active, aerr := collectProbes(ctx, tx, `SELECT `+probeColumns+` FROM probe p
				WHERE p.organization_id = $1 AND p.enabled AND p.suspended_reason IS NULL`, orgID)
			if aerr != nil {
				return aerr
			}
			x, lerr := loadLayers(ctx, tx, orgID, active)
			if lerr != nil {
				return lerr
			}
			x.planMin = planMin
			for i := range active {
				e := Resolve(&active[i], x.layers(&active[i]))
				specs = append(specs, budgetSpec(&active[i], &e))
			}
		}
		quotaHit := false
		var groups []string
		for i := range list {
			c := &list[i]
			switch c.Plan() {
			case AutoNone:
			case AutoSkip:
				report.Skip(c.SkipReason(), c.CIID)
			case AutoRetarget:
				p, uerr := scanProbe(tx.QueryRow(ctx, `UPDATE probe AS p SET target = $3
					WHERE p.organization_id = $1 AND p.id = $2 AND p.origin = 'auto'
					  AND NOT EXISTS (SELECT 1 FROM probe o WHERE o.organization_id = p.organization_id AND o.ci_id = p.ci_id
						AND o.type = p.type AND o.target = $3)
					RETURNING `+probeColumns, orgID, c.AutoProbe, c.ManagementIP))
				if errors.Is(uerr, pgx.ErrNoRows) {
					continue
				}
				if uerr != nil {
					return fmt.Errorf("follow management ip: %w", uerr)
				}
				report.Updated++
				groups = append(groups, p.CollectorGroupID)
				if eerr := emitConfigChanged(ctx, tx, &p, []string{"target"}); eerr != nil {
					return eerr
				}
			case AutoCreate:
				if quotaHit {
					report.Skip(SkipQuota, c.CIID)
					continue
				}
				next := &Probe{OrganizationID: orgID, CIID: c.CIID, ClientID: c.ClientID, Type: "icmp", Target: c.ManagementIP, Enabled: true}
				if r.budget != nil {
					// The effective values of the new probe over the
					// profiles of its levels.
					x, lerr := loadLayers(ctx, tx, orgID, []Probe{*next})
					if lerr != nil {
						return lerr
					}
					x.planMin = planMin
					e := Resolve(next, x.layers(next))
					after := make([]entitlement.EffectiveSpec, 0, len(specs)+1)
					after = append(append(after, specs...), budgetSpec(next, &e))
					if berr := r.budget.AllowProbeChange(txCtx, orgID, entitlement.BudgetOf(specs), entitlement.BudgetOf(after)); berr != nil {
						var le *entitlement.LimitExceededError
						var fe *api.FieldError
						if !errors.As(berr, &le) && !errors.As(berr, &fe) {
							return berr
						}
						quotaHit = true
						report.Skip(SkipQuota, c.CIID)
						continue
					}
					specs = after
				}
				p, ierr := scanProbe(tx.QueryRow(ctx, `INSERT INTO probe AS p (organization_id, ci_id, collector_group_id, type, target, params, origin)
					VALUES ($1, $2, $3, 'icmp', $4, '{}', 'auto')
					ON CONFLICT (organization_id, client_id, ci_id, type, target) WHERE managed_by IS NULL DO NOTHING RETURNING `+probeColumns,
					orgID, c.CIID, c.Group, c.ManagementIP))
				if errors.Is(ierr, pgx.ErrNoRows) {
					continue
				}
				if ierr != nil {
					return fmt.Errorf("create auto probe: %w", probeWriteError(ierr))
				}
				report.Created++
				groups = append(groups, p.CollectorGroupID)
				if eerr := emitConfigChanged(ctx, tx, &p, createdFields(&p)); eerr != nil {
					return eerr
				}
			}
		}
		r.afterWrite(ctx, tx, orgID, groups, nil)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// Submitter submits a job (jobs.Queue).
type Submitter interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// AutocreateDispatcher is the system job probe.autocreate.dispatch.
type AutocreateDispatcher struct {
	pool     *pgxpool.Pool
	submit   Submitter
	licensed Licensed
	now      func() time.Time
}

// NewAutocreateDispatcher returns the dispatch on pool; licensed answers the
// feature monitoring.
func NewAutocreateDispatcher(pool *pgxpool.Pool, submit Submitter, licensed Licensed) *AutocreateDispatcher {
	return &AutocreateDispatcher{pool: pool, submit: submit, licensed: licensed, now: time.Now}
}

// Schedule is the system job (@every 15m).
func (d *AutocreateDispatcher) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: AutocreateDispatchKind, Spec: AutocreateDispatchSpec, MaxCatchUp: 1, Run: func(ctx context.Context) error {
		_, err := d.Run(ctx)
		return err
	}}
}

// Run submits a job for every organization with monitoring.auto_ping, the
// feature monitoring and no open job; it returns the submitted ones. A
// failing organization does not stop the others.
func (d *AutocreateDispatcher) Run(ctx context.Context) (int, error) {
	orgs, err := database.OrganizationIDs(ctx, d.pool)
	if err != nil {
		return 0, err
	}
	submitted := 0
	var errs []error
	for _, org := range orgs {
		ok, oerr := d.runOrg(ctx, org)
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
			continue
		}
		if ok {
			submitted++
		}
	}
	return submitted, errors.Join(errs...)
}

func (d *AutocreateDispatcher) runOrg(ctx context.Context, orgID string) (bool, error) {
	scope := database.OrgWideScope(orgID, "")
	var on, open bool
	err := database.WithTenant(ctx, d.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		settings, err := readSettings(ctx, tx, orgID)
		if err != nil {
			return fmt.Errorf("read organization settings: %w", err)
		}
		if on = AutoPingFromSettings(settings); !on {
			return nil
		}
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE organization_id = $1 AND kind = $2
			AND status IN ('queued', 'running'))`, orgID, AutocreateJobKind).Scan(&open)
	})
	if err != nil || !on || open {
		return false, err
	}
	if d.licensed != nil {
		licensed, lerr := d.licensed(ctx, orgID, entitlement.FeatureMonitoring)
		if lerr != nil || !licensed {
			return false, lerr
		}
	}
	slot := d.now().UTC().Truncate(15 * time.Minute).Format(time.RFC3339)
	if _, _, err = d.submit.Submit(ctx, orgID, AutocreateJobKind, nil,
		jobs.EnqueueOptions{IdempotencyKey: AutocreateJobKind + ":" + slot}); err != nil {
		return false, err
	}
	slog.Debug("probe autocreate submitted", "organization_id", orgID)
	return true, nil
}
