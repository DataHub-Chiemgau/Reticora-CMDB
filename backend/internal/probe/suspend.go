package probe

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Suspension of probes whose monitoring feature was lost (PKG-09, ENT-08,
// WP-942, E-156 option 2): the system job probe.entitlement_reconcile runs
// every minute and checks per organization, under the advisory lock
// probe-suspend:<org>, the feature of every probe type
// (entitlement.ProbeTypeFeature). A probe whose feature has no active
// entitlement (row missing, disabled or past valid_until) gets
// suspended_reason = 'entitlement' and suspended_at; a suspended probe
// whose feature is active again loses both. enabled stays as the user set
// it in both directions, so a probe the user switched off stays off after
// the resumption. Quotas suspend nothing (ENT-08): a stock above
// max_monitored_targets after a downgrade keeps running, only new load is
// refused by the budget check (WP-940).
//
// In the same transaction the job runs the assignment engine for the
// collector groups of the changed probes (their group locks
// probe_assign:<group> taken before the probe rows, in the order of their
// ids): a suspended probe loses its assignment, a resumed one is assigned
// again, the config_version of the touched collectors rises and
// AssignmentNotifier learns them after the commit. A run that changed a
// probe writes one outbox event monitoring.suspension_changed as its last
// write; its delivery to the org_admins runs through the outbox. A second
// run without a change of the entitlements writes nothing.

// SuspendKind is the system job of the suspension.
const SuspendKind = "probe.entitlement_reconcile"

// SuspendSpec runs the reconciliation every minute.
const SuspendSpec = "@every 1m"

// Licensed reports whether a feature of the organization has an active
// entitlement (entitlement.Service.Status, StatusActive).
type Licensed func(ctx context.Context, orgID, feature string) (bool, error)

// LicensedBy answers Licensed from the entitlement service
// (entitlement.Service.ActiveUntilExpiry): a feature is active when its
// status is active and its valid_until (if any) lies in the future on the
// clock of the service. The status alone keeps an expired row active for
// every feature but discovery (expiryStops); for the monitoring an expiry
// ends the function (PKG-09, ENT-08). Without enforcement
// (RETICORA_ENTITLEMENT_ENFORCEMENT=false) every feature is active and the
// job pauses nothing.
func LicensedBy(svc *entitlement.Service) Licensed {
	return func(ctx context.Context, orgID, feature string) (bool, error) {
		if svc == nil {
			return true, nil
		}
		// The job runs without a request: the entitlements of the
		// organization are read in its org-wide scope.
		if _, ok := database.TenantScopeFromContext(ctx); !ok {
			scope := database.OrgWideScope(orgID, "")
			ctx = database.ContextWithTenantScope(ctx, &scope)
		}
		return svc.ActiveUntilExpiry(ctx, orgID, feature)
	}
}

// SuspensionChange is what a run changed in one organization.
type SuspensionChange struct {
	Paused     int      `json:"paused"`
	Resumed    int      `json:"resumed"`
	Features   []string `json:"features"`
	ProbeTypes []string `json:"probe_types"`
}

// suspensionData is the data of monitoring.suspension_changed: the frame
// of WP-867 (feature_key, suspended: the first changed feature and whether
// the run paused) with the counts, features and probe types of the run.
type suspensionData struct {
	events.MonitoringSuspensionData
	SuspensionChange
}

// Suspender runs the reconciliation of the probe suspensions.
type Suspender struct {
	pool     *pgxpool.Pool
	licensed Licensed
	notifier AssignmentNotifier
}

// NewSuspender returns the job on pool; licensed answers the entitlements.
func NewSuspender(pool *pgxpool.Pool, licensed Licensed) *Suspender {
	return &Suspender{pool: pool, licensed: licensed, notifier: LogNotifier{}}
}

// WithNotifier sets the notifier of the collectors whose assignment set
// changed.
func (s *Suspender) WithNotifier(n AssignmentNotifier) *Suspender {
	s.notifier = n
	return s
}

// Schedule is the system job probe.entitlement_reconcile.
func (s *Suspender) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: SuspendKind, Spec: SuspendSpec, MaxCatchUp: 1, Run: s.Run}
}

// Run reconciles every organization; a failing organization does not stop
// the others, the errors come back joined.
func (s *Suspender) Run(ctx context.Context) error {
	orgs, err := database.OrganizationIDs(ctx, s.pool)
	if err != nil {
		return err
	}
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if _, oerr := s.RunOrg(ctx, org); oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return errors.Join(errs...)
}

// splitTypes divides the probe types of the register into those whose
// feature is active (kept) and those whose feature is not (dropped).
func (s *Suspender) splitTypes(ctx context.Context, orgID string) (kept, dropped []string, err error) {
	status := map[string]bool{}
	for _, typ := range entitlement.ProbeTypes() {
		feature, ferr := entitlement.ProbeTypeFeature(typ)
		if ferr != nil {
			continue
		}
		ok, known := status[feature]
		if !known {
			if ok, err = s.licensed(ctx, orgID, feature); err != nil {
				return nil, nil, fmt.Errorf("entitlement %s: %w", feature, err)
			}
			status[feature] = ok
		}
		if ok {
			kept = append(kept, typ)
		} else {
			dropped = append(dropped, typ)
		}
	}
	return kept, dropped, nil
}

// RunOrg reconciles one organization and returns what it changed.
func (s *Suspender) RunOrg(ctx context.Context, orgID string) (SuspensionChange, error) {
	var change SuspensionChange
	kept, dropped, err := s.splitTypes(ctx, orgID)
	if err != nil {
		return change, err
	}
	scope := database.OrgWideScope(orgID, "")
	var notify []CollectorVersion
	err = database.WithTenant(ctx, s.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if _, lerr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('probe-suspend:' || $1, 0))`, orgID); lerr != nil {
			return fmt.Errorf("lock probe suspension: %w", lerr)
		}
		// The groups of the probes to change, locked before their rows
		// (the order of the assignment engine: group lock, then probes).
		rows, qerr := tx.Query(ctx, `SELECT DISTINCT collector_group_id::text FROM probe
			WHERE organization_id = $1
			  AND ((suspended_reason IS NULL AND type = ANY ($2::text[]))
			    OR (suspended_reason = 'entitlement' AND type = ANY ($3::text[])))
			ORDER BY 1`, orgID, dropped, kept)
		if qerr != nil {
			return fmt.Errorf("read probes to reconcile: %w", qerr)
		}
		groups, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			return fmt.Errorf("read probes to reconcile: %w", qerr)
		}
		if len(groups) == 0 {
			return nil
		}
		for _, g := range groups {
			if _, lerr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('probe_assign:' || $1, 0))`, g); lerr != nil {
				return fmt.Errorf("lock collector group: %w", lerr)
			}
		}
		paused, perr := changedTypes(ctx, tx, `UPDATE probe SET suspended_reason = 'entitlement', suspended_at = now()
			WHERE organization_id = $1 AND suspended_reason IS NULL AND type = ANY ($2::text[]) RETURNING type`, orgID, dropped)
		if perr != nil {
			return fmt.Errorf("suspend probes: %w", perr)
		}
		resumed, rerr := changedTypes(ctx, tx, `UPDATE probe SET suspended_reason = NULL, suspended_at = NULL
			WHERE organization_id = $1 AND suspended_reason = 'entitlement' AND type = ANY ($2::text[]) RETURNING type`, orgID, kept)
		if rerr != nil {
			return fmt.Errorf("resume probes: %w", rerr)
		}
		change = summarize(paused, resumed)
		for _, g := range groups {
			changed, aerr := assignGroup(ctx, tx, orgID, g)
			if aerr != nil {
				return fmt.Errorf("assign collector group %s: %w", g, aerr)
			}
			notify = append(notify, changed...)
		}
		if len(notify) > 0 && s.notifier != nil {
			database.AfterCommit(database.ContextWithTx(ctx, tx), func(ctx context.Context) {
				s.notifier.AssignmentsChanged(ctx, orgID, notify)
			})
		}
		if change.Paused+change.Resumed == 0 {
			return nil
		}
		data := suspensionData{SuspensionChange: change}
		data.FeatureKey, data.Suspended = frameFeature(paused, resumed), len(paused) > 0
		return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
			_, aerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.MonitoringSuspensionChanged,
				EntityID: orgID, Data: data})
			return aerr
		})
	})
	if err != nil {
		return SuspensionChange{}, err
	}
	return change, nil
}

// changedTypes runs an update returning the type of every changed probe.
func changedTypes(ctx context.Context, tx pgx.Tx, sql, orgID string, types []string) ([]string, error) {
	if len(types) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, sql, orgID, types)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// frameFeature is the feature_key of the WP-867 frame: the first sorted
// feature of the paused types when the run paused a probe (suspended =
// true), otherwise that of the resumed types. A run that pauses one
// feature and resumes another thus names a feature it paused.
func frameFeature(paused, resumed []string) string {
	types := paused
	if len(types) == 0 {
		types = resumed
	}
	return summarize(types, nil).Features[0]
}

// summarize counts the changed probes and names their features and types,
// sorted.
func summarize(paused, resumed []string) SuspensionChange {
	c := SuspensionChange{Paused: len(paused), Resumed: len(resumed), Features: []string{}, ProbeTypes: []string{}}
	for _, typ := range slices.Concat(paused, resumed) {
		if !slices.Contains(c.ProbeTypes, typ) {
			c.ProbeTypes = append(c.ProbeTypes, typ)
		}
		if f, err := entitlement.ProbeTypeFeature(typ); err == nil && !slices.Contains(c.Features, f) {
			c.Features = append(c.Features, f)
		}
	}
	slices.Sort(c.ProbeTypes)
	slices.Sort(c.Features)
	return c
}
