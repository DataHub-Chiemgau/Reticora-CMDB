package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Package limits of the workflow engine (WFL-08, WP-1112): the active
// definitions (max_workflow_definitions) and the form definitions
// (max_form_definitions) are checked when a definition is created or
// switched on, 403 entitlement-limit above the limit; the runs started per
// hour (max_workflow_runs_per_hour) are checked by the worker before it
// starts a queued run: a run above the limit waits (next_attempt_at) and
// is executed later, never dropped, and the organization is warned once
// per hour (entitlement.warning). The defaults come from the plan (Pro and
// Enterprise), a value stored on the workflows row overrides them. Without
// a database only the definition limits apply (the runs execute in the
// request).

// runQuotaWindow is the window of max_workflow_runs_per_hour.
const runQuotaWindow = time.Hour

// runsDelayed counts the runs the worker delayed above the hourly limit.
var runsDelayed = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "reticora_workflow_runs_delayed_total",
	Help: "Workflow runs delayed because the organization reached max_workflow_runs_per_hour.",
})

// DefinitionQuota admits one more active workflow definition.
type DefinitionQuota interface {
	AllowActiveDefinition(ctx context.Context, orgID string) error
}

// RunQuota returns max_workflow_runs_per_hour of the organization; nil is
// unlimited (no workflows, enforcement off).
type RunQuota interface {
	RunsPerHour(ctx context.Context, orgID string) (*int64, error)
}

// RunQuotaStore claims a run within max_workflow_runs_per_hour and warns
// of delayed runs (the PostgreSQL repository).
type RunQuotaStore interface {
	// ClaimRunWithinQuota is Repository.ClaimRun for a run that counts
	// against limit: the count of the runs started within the last
	// runQuotaWindow (database clock) and the claim or the delay happen in
	// one transaction under the quota lock of the organization, so parallel
	// workers never start more than limit runs (WP-1112 AK3). At the limit
	// the run stays pending with next_attempt_at at the moment the oldest
	// counted run leaves the window and its enqueues raised (Delayed).
	ClaimRunWithinQuota(ctx context.Context, orgID, id string, enqueue int, limit int64) (RunClaim, error)
	// WarnRunsDelayed writes entitlement.warning once per organization and
	// hour (notice_sent); it reports whether it wrote one.
	WarnRunsDelayed(ctx context.Context, orgID string, limit int64, until time.Time) (bool, error)
}

// Quotas checks the workflow limits on the entitlement service; the
// counts read organization-wide whatever the caller's scope. A nil Quotas
// admits everything.
type Quotas struct {
	svc       *entitlement.Service
	workflows Repository
	forms     form.Repository
}

// NewQuotas returns the limit checks of workflows and forms.
func NewQuotas(svc *entitlement.Service, workflows Repository, forms form.Repository) *Quotas {
	return &Quotas{svc: svc, workflows: workflows, forms: forms}
}

// orgWide is ctx with the organization-wide scope of the caller's user.
func orgWide(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, tenant.FromContext(ctx).UserID)
	return database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
}

// AllowActiveDefinition implements DefinitionQuota: the active definitions
// of the organization against max_workflow_definitions.
func (q *Quotas) AllowActiveDefinition(ctx context.Context, orgID string) error {
	if q == nil || q.svc == nil {
		return nil
	}
	_, total, err := q.workflows.ListDefinitions(orgWide(ctx, orgID), orgID, true, api.PaginationParams{Limit: 1})
	if err != nil {
		return err
	}
	return q.svc.AllowCreate(ctx, orgID, entitlement.LimitMaxWorkflowDefinitions, int64(total))
}

// AllowFormDefinition implements form.DefinitionQuota: the form
// definitions of the organization against max_form_definitions.
func (q *Quotas) AllowFormDefinition(ctx context.Context, orgID string) error {
	if q == nil || q.svc == nil {
		return nil
	}
	_, total, err := q.forms.ListDefinitions(orgWide(ctx, orgID), orgID, "", false, api.PaginationParams{Limit: 1})
	if err != nil {
		return err
	}
	return q.svc.AllowCreate(ctx, orgID, entitlement.LimitMaxFormDefinitions, int64(total))
}

// RunsPerHour implements RunQuota.
func (q *Quotas) RunsPerHour(ctx context.Context, orgID string) (*int64, error) {
	if q == nil || q.svc == nil {
		return nil, nil
	}
	if !q.svc.Enforcing() {
		return nil, nil
	}
	_, entitled, err := q.svc.Check(ctx, orgID, entitlement.FeatureWorkflows)
	if err != nil || !entitled {
		return nil, err
	}
	return q.svc.Quota(ctx, orgID, entitlement.LimitMaxWorkflowRunsPerHour)
}

// WithRunQuota makes the worker check max_workflow_runs_per_hour before it
// starts a queued run (WP-1112 AK3).
func (e *Executor) WithRunQuota(q RunQuota) *Executor {
	e.runQuota = q
	return e
}

// claimRun claims the run of the job of enqueue (Repository.ClaimRun);
// a pending run that has not started counts against
// max_workflow_runs_per_hour (ClaimRunWithinQuota): at the limit it waits
// for its slot, its job is queued for then and the organization is warned
// once per hour.
func (e *Executor) claimRun(ctx context.Context, orgID string, run *Run, enqueue int) (RunClaim, error) {
	limit, err := e.runLimit(ctx, orgID, run)
	if err != nil {
		return RunClaim{}, err
	}
	store, ok := e.repo.(RunQuotaStore)
	if limit == nil || !ok {
		return e.repo.ClaimRun(ctx, orgID, run.ID, enqueue)
	}
	claim, err := store.ClaimRunWithinQuota(ctx, orgID, run.ID, enqueue, *limit)
	if err != nil || !claim.Delayed {
		return claim, err
	}
	runsDelayed.Inc()
	if _, werr := store.WarnRunsDelayed(orgWide(ctx, orgID), orgID, *limit, claim.Until); werr != nil {
		slog.ErrorContext(ctx, "workflow: failed to warn of delayed runs", "organization_id", orgID, "error", werr)
	}
	e.requeueAt(ctx, orgID, claim.Run, claim.Until)
	return claim, nil
}

// runLimit is max_workflow_runs_per_hour for a queued pending run that has
// not started; nil when no limit applies.
func (e *Executor) runLimit(ctx context.Context, orgID string, run *Run) (*int64, error) {
	if e.runQuota == nil || e.queue == nil || run.Status != StatusPending || !run.StartedAt.IsZero() {
		return nil, nil
	}
	return e.runQuota.RunsPerHour(ctx, orgID)
}

// ClaimRunWithinQuota implements RunQuotaStore. The claim reads
// organization-wide: the runs of every client count against the limit of
// the organization (the job's scope saw the run before, GetRun).
func (r *PGRepository) ClaimRunWithinQuota(ctx context.Context, orgID, id string, enqueue int, limit int64) (RunClaim, error) {
	var c RunClaim
	err := database.WithRequestTenant(orgWide(ctx, orgID), r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = claimRunTx(ctx, tx, orgID, id, enqueue, &limit)
		return err
	})
	return c, err
}

// delayAtQuota counts, under the quota lock of the organization (held until
// the commit), the runs started within the last hour other than id; at
// limit it delays the run until the oldest of them leaves the window
// (database clock, at least one second ahead) and raises its enqueues.
func delayAtQuota(ctx context.Context, tx pgx.Tx, orgID, id string, limit int64) (RunClaim, bool, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('workflow_run_quota:' || $1))", orgID); err != nil {
		return RunClaim{}, false, err
	}
	var n int64
	var until time.Time
	if err := tx.QueryRow(ctx, `SELECT count(*), GREATEST(COALESCE(min(started_at), now()) + make_interval(secs => $2), now() + interval '1 second')
		FROM workflow_run WHERE organization_id=$1 AND started_at > now() - make_interval(secs => $2) AND id <> $3::uuid`,
		orgID, runQuotaWindow.Seconds(), id).Scan(&n, &until); err != nil {
		return RunClaim{}, false, err
	}
	if n < limit {
		return RunClaim{}, false, nil
	}
	run, err := scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET next_attempt_at=$3, enqueues=enqueues+1, updated_at=now()
		WHERE organization_id=$1 AND id=$2 AND status='pending' RETURNING `+runCols, orgID, id, until))
	if err != nil {
		return RunClaim{}, false, err
	}
	return RunClaim{Delayed: true, Until: until.UTC(), Run: run}, true, nil
}

// WarnRunsDelayed implements RunQuotaStore: entitlement.warning on the
// workflows entitlement with quota, limit and delayed_until, deduplicated
// per hour (notice_sent, kind entitlement.warning, key
// workflow_runs_per_hour:<hour>).
func (r *PGRepository) WarnRunsDelayed(ctx context.Context, orgID string, limit int64, until time.Time) (bool, error) {
	sent := false
	now := time.Now().UTC()
	key := "workflow_runs_per_hour:" + now.Truncate(time.Hour).Format("2006-01-02T15")
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var entityID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT id::text FROM entitlement WHERE organization_id=$1 AND feature_key=$2), '')`,
			orgID, entitlement.FeatureWorkflows).Scan(&entityID); err != nil {
			return err
		}
		lim := limit
		var err error
		sent, err = jobs.SendNotice(ctx, tx, now, key, &events.Event{OrgID: orgID, Type: events.EntitlementWarning, EntityID: entityID,
			Data: &jobs.EntitlementNotice{Feature: entitlement.FeatureWorkflows, Quota: entitlement.LimitMaxWorkflowRunsPerHour,
				Limit: &lim, DelayedUntil: until.UTC().Format(time.RFC3339)}})
		if err != nil {
			return fmt.Errorf("warn of delayed workflow runs: %w", err)
		}
		return nil
	})
	return sent, err
}
