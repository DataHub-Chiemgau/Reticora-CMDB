package jobs

import (
	"context"
	"errors"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
)

// Trigger scope of a queue job (WP-712, TEN-09, E-08): the request that
// queued a job is gone when a worker runs it. CreatorContext restores the
// tenant, the actor and the scope snapshot of the job row (WP-404, 000154);
// ScopeParams carries what the job row does not hold, the request id and
// the source of the request, in the job parameters. The worker then writes
// its changes like the request would have: the trigger is the actor, row
// level security shows it only the trigger's rows, and the audit entries
// name the request and, as correlation id, the job.

// ErrNoScopeSnapshot: the job row holds no scope snapshot of its creator
// (a system job or a job from before migration 000154). A job that writes
// on behalf of its creator fails instead of running organization-wide
// (fail-closed, E-08).
var ErrNoScopeSnapshot = errors.New("the job has no scope snapshot of its creator")

// ScopeParams are the request facts of a job's trigger that the job row
// does not hold; a kind embeds them in its parameters (CaptureScope).
// Clients, sites and teams are not among them: the scope snapshot of the
// job row is the only source of the scope.
type ScopeParams struct {
	RequestID string `json:"request_id,omitempty"`
	Source    string `json:"source,omitempty"`
}

// CaptureScope takes the request id and the source of the request in ctx
// (audit.RequestInfo of the route audit); empty outside a request.
func CaptureScope(ctx context.Context) ScopeParams {
	info, ok := audit.RequestInfoFrom(ctx)
	if !ok {
		return ScopeParams{}
	}
	return ScopeParams{RequestID: info.RequestID, Source: info.Source}
}

// Context returns ctx with the trigger of job restored: tenant, actor and
// scope snapshot of the job row (CreatorContext) and the request info of
// the audit with the captured request id and source and the job id as
// correlation id. A job without scope snapshot is ErrNoScopeSnapshot: it
// does not run organization-wide like CreatorContext would let it.
func (p ScopeParams) Context(ctx context.Context, job *Job) (context.Context, error) {
	if job == nil || job.Scope == nil {
		return nil, ErrNoScopeSnapshot
	}
	ctx = CreatorContext(ctx, job)
	return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: p.RequestID, CorrelationID: job.ID, Source: p.Source}), nil
}
