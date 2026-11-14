package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The compliance evaluation as a job and incrementally (SEC-03, WP-1126,
// E-237 option 2): POST /api/v1/compliance/evaluations queues the job
// compliance.evaluate (one per organization at a time) that evaluates all
// CIs against all active rules in pages; the system job
// compliance.evaluate.nightly queues it every night for each organization
// with the feature and an active rule; the IncrementalConsumer evaluates
// one CI after its change or a change of its findings.

// EvaluateJobKind is the job of a full evaluation.
const EvaluateJobKind = "compliance.evaluate"

// NightlyKind is the system job that queues the nightly evaluations.
const NightlyKind = "compliance.evaluate.nightly"

// EvaluateJobKindConfig is the registration of EvaluateJobKind: three
// attempts of 30 minutes; a running evaluation can be cancelled between
// two pages.
var EvaluateJobKindConfig = jobs.Kind{Name: EvaluateJobKind, MaxAttempts: 3, Timeout: 30 * time.Minute, Cancellable: true,
	Backoff: jobs.ExponentialBackoff(time.Minute, 10*time.Minute)}

// JobService runs a job in this process (jobs.Service, without the queue)
// and reports its progress and cancellation.
type JobService interface {
	List(ctx context.Context, orgID string, filter jobs.Filter, page api.PaginationParams) ([]jobs.Job, int, error)
	RunDetachedWith(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions,
		run func(ctx context.Context) (any, error)) (*jobs.Job, bool, error)
	// ProgressResult records the percent and processed/total of the
	// running job after each page (AK2).
	ProgressResult(ctx context.Context, orgID, id string, percent int, interim any) error
	IsCancelled(ctx context.Context, orgID, id string) (bool, error)
}

// NewJobService returns a job service with EvaluateJobKind registered: a
// caller without the job queue (the nightly system job) runs the
// evaluation in this process.
func NewJobService(repo jobs.Repository) *jobs.Service {
	svc := jobs.NewService(repo)
	svc.Register(EvaluateJobKindConfig)
	return svc
}

// JobQueue submits the job to the job queue on NATS (jobs.Queue).
type JobQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// Jobs submits and runs the evaluation jobs.
type Jobs struct {
	evaluator *Evaluator
	svc       JobService
	queue     JobQueue
	// reports generates the report jobs compliance.report (WithReports,
	// WP-1129); nil without the database and the object storage.
	reports *ReportGenerator
}

// NewJobs returns the evaluation jobs of evaluator.
func NewJobs(evaluator *Evaluator) *Jobs { return &Jobs{evaluator: evaluator} }

// WithJobs sets the job service (progress, cancel, the run without queue).
func (j *Jobs) WithJobs(svc JobService) *Jobs {
	j.svc = svc
	return j
}

// WithQueue makes Submit queue the jobs on NATS.
func (j *Jobs) WithQueue(q JobQueue) *Jobs {
	j.queue = q
	return j
}

// ErrJobsUnavailable: neither job service nor queue is wired.
var ErrJobsUnavailable = errors.New("the compliance evaluation needs the job service")

// Submit queues the evaluation of the organization for createdBy (empty:
// the system), or returns the queued or running job of the organization
// (AK1). The caller checked the scope. Two submits at once (two requests,
// a request and the nightly run) create one job: both name the job after
// the newest ended evaluation of the organization (evaluationKey), and the
// idempotency key of the job table admits one job per key.
func (j *Jobs) Submit(ctx context.Context, orgID, createdBy string) (*jobs.Job, error) {
	if j == nil || j.svc == nil {
		return nil, ErrJobsUnavailable
	}
	for _, status := range []jobs.Status{jobs.StatusRunning, jobs.StatusQueued} {
		active, _, err := j.svc.List(ctx, orgID, jobs.Filter{Kind: EvaluateJobKind, Status: status}, api.PaginationParams{Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(active) > 0 {
			return &active[0], nil
		}
	}
	key, err := j.evaluationKey(ctx, orgID)
	if err != nil {
		return nil, err
	}
	opts := jobs.EnqueueOptions{CreatedBy: createdBy, IdempotencyKey: key}
	if j.queue != nil {
		job, _, serr := j.queue.Submit(ctx, orgID, EvaluateJobKind, map[string]string{}, opts)
		return job, serr
	}
	// The run learns its job id once RunDetachedWith has returned it.
	ids := make(chan string, 1)
	job, _, err := j.svc.RunDetachedWith(ctx, orgID, EvaluateJobKind, map[string]string{}, opts, func(ctx context.Context) (any, error) {
		return j.evaluate(ctx, orgID, <-ids)
	})
	if job != nil {
		ids <- job.ID
	} else {
		ids <- ""
	}
	return job, err
}

// evaluationKey is the idempotency key of the next evaluation job of the
// organization: after the newest ended one (succeeded, failed, cancelled),
// "first" without one. While no evaluation ends, every submit names the
// same key; the end of one opens the next.
func (j *Jobs) evaluationKey(ctx context.Context, orgID string) (string, error) {
	var newest *jobs.Job
	for _, status := range []jobs.Status{jobs.StatusSucceeded, jobs.StatusFailed, jobs.StatusCancelled} {
		ended, _, err := j.svc.List(ctx, orgID, jobs.Filter{Kind: EvaluateJobKind, Status: status}, api.PaginationParams{Limit: 1})
		if err != nil {
			return "", err
		}
		if len(ended) > 0 && (newest == nil || ended[0].CreatedAt.After(newest.CreatedAt)) {
			newest = &ended[0]
		}
	}
	if newest == nil {
		return "after:first", nil
	}
	return "after:" + newest.ID, nil
}

// RunJob is the handler of EvaluateJobKind on the job queue: the run as the
// creator of the job (TEN-09), progress per page, cancellation between
// pages.
func (j *Jobs) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	ctx = jobs.CreatorContext(ctx, job)
	if job.CreatedBy == "" {
		// A system job (the nightly run) evaluates organization-wide.
		scope := database.OrgWideScope(job.OrganizationID, "")
		ctx = tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: job.OrganizationID})
	}
	return j.evaluate(ctx, job.OrganizationID, job.ID)
}

// errEvaluationCancelled ends a cancelled evaluation between two pages.
var errEvaluationCancelled = errors.New("the compliance evaluation was cancelled")

func (j *Jobs) evaluate(ctx context.Context, orgID, jobID string) (any, error) {
	result, err := j.evaluator.Run(ctx, orgID, func(p Progress) error {
		if j.svc == nil || jobID == "" {
			return nil
		}
		percent := 100
		if p.Total > 0 {
			percent = min(100, p.Processed*100/p.Total)
		}
		if perr := j.svc.ProgressResult(ctx, orgID, jobID, percent, p); perr != nil {
			slog.WarnContext(ctx, "compliance evaluation: progress", "job_id", jobID, "error", perr)
		}
		if cancelled, cerr := j.svc.IsCancelled(ctx, orgID, jobID); cerr == nil && cancelled {
			return errEvaluationCancelled
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

// incrementalTypes are the events that evaluate one CI again (AK4).
var incrementalTypes = map[string]bool{events.CICreated: true, events.CIChanged: true, events.LifecycleChanged: true,
	events.FindingCreated: true, events.FindingChanged: true, events.FindingResolved: true}

// IncrementalConsumer evaluates the CI of a changed CI, lifecycle state or
// finding against all active rules (events.Consumer of the publisher).
type IncrementalConsumer struct {
	evaluator *Evaluator
	// enabled reports the module compliance and the feature
	// compliance_findings of the organization.
	enabled func(ctx context.Context, orgID string) bool
}

// NewIncrementalConsumer returns the incremental evaluation of evaluator
// for the organizations enabled admits.
func NewIncrementalConsumer(evaluator *Evaluator, enabled func(ctx context.Context, orgID string) bool) *IncrementalConsumer {
	return &IncrementalConsumer{evaluator: evaluator, enabled: enabled}
}

// Consume implements events.Consumer: a database error is returned (the
// delivery is repeated), a CI no longer visible is no error.
func (c *IncrementalConsumer) Consume(ctx context.Context, env *events.Envelope) error {
	if !incrementalTypes[env.Type] {
		return nil
	}
	ciID := incrementalCI(env)
	if ciID == "" || !api.IsUUID(ciID) {
		return nil
	}
	scope := database.OrgWideScope(env.OrgID, "")
	octx := tenant.WithTenant(database.ContextWithTenantScope(database.WithoutTx(ctx), &scope), tenant.TenantInfo{OrganizationID: env.OrgID})
	if c.enabled == nil || !c.enabled(octx, env.OrgID) {
		return nil
	}
	if err := c.evaluator.EvaluateCI(octx, env.OrgID, ciID); err != nil {
		return fmt.Errorf("compliance evaluation of CI %s: %w", ciID, err)
	}
	return nil
}

// incrementalCI is the CI an event concerns: the entity of ci.*, the
// entity of lifecycle.changed of a CI, data.ci_id of finding.*.
func incrementalCI(env *events.Envelope) string {
	var data struct {
		EntityType string `json:"entity_type"`
		CIID       string `json:"ci_id"`
	}
	_ = json.Unmarshal(env.Data, &data)
	switch {
	case strings.HasPrefix(env.Type, "ci."):
		return env.EntityID
	case env.Type == events.LifecycleChanged:
		if data.EntityType == "ci" {
			return env.EntityID
		}
		return ""
	}
	return data.CIID
}

// Nightly is the system job compliance.evaluate.nightly (AK5).
type Nightly struct {
	jobs    *Jobs
	repo    Repository
	orgs    func(ctx context.Context) ([]string, error)
	enabled func(ctx context.Context, orgID string) bool
}

// NewNightly returns the nightly run over the organizations orgs lists that
// enabled admits (feature compliance_findings).
func NewNightly(j *Jobs, repo Repository, orgs func(ctx context.Context) ([]string, error), enabled func(ctx context.Context, orgID string) bool) *Nightly {
	return &Nightly{jobs: j, repo: repo, orgs: orgs, enabled: enabled}
}

// Schedule is the registration with the scheduler: daily 02:30 UTC, at most
// one missed run caught up.
func (n *Nightly) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: NightlyKind, Spec: "30 2 * * *", MaxCatchUp: 1, Timeout: 10 * time.Minute,
		Run: func(ctx context.Context) error {
			queued, err := n.Run(ctx)
			if queued > 0 {
				slog.InfoContext(ctx, "nightly compliance evaluations queued", "organizations", queued)
			}
			return err
		}}
}

// Run queues one evaluation as the system for every organization with the
// feature and at least one active rule and returns how many it queued.
func (n *Nightly) Run(ctx context.Context) (int, error) {
	orgs, err := n.orgs(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", NightlyKind, err)
	}
	queued := 0
	var errs []error
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		octx := tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
		if n.enabled == nil || !n.enabled(octx, orgID) {
			continue
		}
		rules, _, rerr := n.repo.ListRules(octx, orgID, "", "", true, api.PaginationParams{Limit: 1})
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		if len(rules) == 0 {
			continue
		}
		if _, serr := n.jobs.Submit(octx, orgID, ""); serr != nil {
			errs = append(errs, fmt.Errorf("%s: organization %s: %w", NightlyKind, orgID, serr))
			continue
		}
		queued++
	}
	return queued, errors.Join(errs...)
}
