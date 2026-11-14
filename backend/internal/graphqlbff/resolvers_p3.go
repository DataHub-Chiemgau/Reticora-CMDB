package graphqlbff

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/graphql"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// The P3 read views at the CI (API-06, GQL-02, WP-1136): findings,
// compliance results and score and workflow runs. Each field checks the
// module first (UNAVAILABLE when it is off), then the read right and the
// feature of its REST route (FORBIDDEN); a refused list field is [] with
// the error at its path, the other fields of the answer stay. The data is
// read under row level security and the scope of the request, per field in
// one statement for all CIs of the level (the sibling CIs a list handed
// out, see ciLoader.batchFor); no step input or output is exposed.

// The late P3 modules the CI fields are gated with.
const (
	ModuleFindings   = "findings"
	ModuleCompliance = "compliance"
	ModuleWorkflows  = "workflows"
)

// FindingBatchSource reads the findings of several CIs in one statement
// (security.PGRepository).
type FindingBatchSource interface {
	FindingsByCIs(ctx context.Context, orgID string, ciIDs, statuses []string, perCI int) ([]security.Finding, error)
}

// ComplianceBatchSource reads the compliance results and scores of several
// CIs in one statement each (compliance.PGRepository).
type ComplianceBatchSource interface {
	ResultsByCIs(ctx context.Context, orgID string, ciIDs []string, status string, perCI int) ([]compliance.CIResult, error)
	ScoresByCIs(ctx context.Context, orgID string, ciIDs []string) (map[string]compliance.ScoreGroup, error)
}

// WorkflowRunBatchSource reads the workflow runs of several CIs in one
// statement (workflow.PGRepository).
type WorkflowRunBatchSource interface {
	RunsByCIs(ctx context.Context, orgID string, ciIDs []string, status string, perCI int) ([]workflow.CIRun, error)
}

// defaultFindingStatuses are the statuses of findings without a filter.
var defaultFindingStatuses = []string{security.StatusOpen, security.StatusAcknowledged}

// refusedList answers a refused list field: [] with the error at its path.
func refusedList[T any](ctx context.Context, err error) ([]*T, error) {
	graphql.AddError(ctx, err)
	return []*T{}, nil
}

func (c *ciResolver) Findings(ctx context.Context, obj *ci.Item, first *int, status []string) ([]*generated.Finding, error) {
	orgID, err := c.authorizeModule(ctx, ModuleFindings, identity.PermFindingRead, entitlement.FeatureComplianceFindings)
	if err != nil {
		return refusedList[generated.Finding](ctx, err)
	}
	src := c.sources.Findings
	if src == nil {
		return refusedList[generated.Finding](ctx, errUnavailable())
	}
	statuses := status
	if len(statuses) == 0 {
		statuses = defaultFindingStatuses
	}
	perCI := pageSize(first, defaultPage, maxPage)
	key := "findings|" + strings.Join(statuses, ",") + "|" + strconv.Itoa(perCI)
	res, err := c.batchFor(ctx, orgID, key, obj.ID, func(ids []string) (map[string]any, error) {
		rows, ferr := src.FindingsByCIs(ctx, orgID, ids, statuses, perCI)
		if ferr != nil {
			return nil, ferr
		}
		out := map[string]any{}
		for i := range rows {
			f := &rows[i]
			list, _ := out[f.CIID].([]*generated.Finding)
			out[f.CIID] = append(list, findingOf(f))
		}
		return out, nil
	})
	if err != nil {
		return refusedList[generated.Finding](ctx, err)
	}
	list, _ := res.([]*generated.Finding)
	if list == nil {
		list = []*generated.Finding{}
	}
	return list, nil
}

func findingOf(f *security.Finding) *generated.Finding {
	out := &generated.Finding{ID: f.ID, CiID: optional(f.CIID), Kind: f.Kind, Severity: f.Severity, Title: f.Title, Status: f.Status,
		StatusReason: optional(f.StatusReason), Source: f.Source, CveIds: f.CVEIDs, CvssScore: f.CVSSScore, Kev: f.KEV,
		AdvisoryID: optional(f.AdvisoryID), PackageName: optional(f.PackageName), InstalledVersion: optional(f.InstalledVersion),
		FixedVersion: optional(f.FixedVersion), Reference: optional(f.Reference), DetectedAt: f.DetectedAt, LastSeenAt: f.LastSeenAt,
		ResolvedAt: f.ResolvedAt}
	if out.CveIds == nil {
		out.CveIds = []string{}
	}
	return out
}

func (c *ciResolver) ComplianceResults(ctx context.Context, obj *ci.Item, first *int, status *string) ([]*generated.ComplianceResult, error) {
	orgID, err := c.authorizeModule(ctx, ModuleCompliance, identity.PermComplianceRead, entitlement.FeatureComplianceFindings)
	if err != nil {
		return refusedList[generated.ComplianceResult](ctx, err)
	}
	src := c.sources.Compliance
	if src == nil {
		return refusedList[generated.ComplianceResult](ctx, errUnavailable())
	}
	st := value(status)
	perCI := pageSize(first, defaultPage, maxPage)
	res, err := c.batchFor(ctx, orgID, "compliance.results|"+st+"|"+strconv.Itoa(perCI), obj.ID, func(ids []string) (map[string]any, error) {
		rows, rerr := src.ResultsByCIs(ctx, orgID, ids, st, perCI)
		if rerr != nil {
			return nil, rerr
		}
		out := map[string]any{}
		for i := range rows {
			r := &rows[i]
			list, _ := out[r.CIID].([]*generated.ComplianceResult)
			out[r.CIID] = append(list, &generated.ComplianceResult{RuleID: r.RuleID, RuleName: r.RuleName, Status: r.Status,
				Severity: r.Severity, EvaluatedAt: r.EvaluatedAt, StatusChangedAt: r.StatusChangedAt})
		}
		return out, nil
	})
	if err != nil {
		return refusedList[generated.ComplianceResult](ctx, err)
	}
	list, _ := res.([]*generated.ComplianceResult)
	if list == nil {
		list = []*generated.ComplianceResult{}
	}
	return list, nil
}

func (c *ciResolver) ComplianceScore(ctx context.Context, obj *ci.Item) (*generated.ComplianceScore, error) {
	orgID, err := c.authorizeModule(ctx, ModuleCompliance, identity.PermComplianceRead, entitlement.FeatureComplianceFindings)
	if err != nil {
		return nil, err
	}
	src := c.sources.Compliance
	if src == nil {
		return nil, errUnavailable()
	}
	res, err := c.batchFor(ctx, orgID, "compliance.score", obj.ID, func(ids []string) (map[string]any, error) {
		scores, serr := src.ScoresByCIs(ctx, orgID, ids)
		if serr != nil {
			return nil, serr
		}
		out := map[string]any{}
		for id, g := range scores {
			out[id] = &generated.ComplianceScore{Score: g.Score, Passed: g.Passed, Failed: g.Failed, NotApplicable: g.NotApplicable}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	score, _ := res.(*generated.ComplianceScore)
	return score, nil
}

func (c *ciResolver) WorkflowRuns(ctx context.Context, obj *ci.Item, first *int, status *string) ([]*generated.WorkflowRun, error) {
	orgID, err := c.authorizeModule(ctx, ModuleWorkflows, identity.PermWorkflowRead, entitlement.FeatureWorkflows)
	if err != nil {
		return refusedList[generated.WorkflowRun](ctx, err)
	}
	src := c.sources.WorkflowRuns
	if src == nil {
		return refusedList[generated.WorkflowRun](ctx, errUnavailable())
	}
	st := value(status)
	perCI := pageSize(first, defaultPage, maxPage)
	res, err := c.batchFor(ctx, orgID, "workflow.runs|"+st+"|"+strconv.Itoa(perCI), obj.ID, func(ids []string) (map[string]any, error) {
		rows, rerr := src.RunsByCIs(ctx, orgID, ids, st, perCI)
		if rerr != nil {
			return nil, rerr
		}
		out := map[string]any{}
		for i := range rows {
			r := &rows[i]
			run := &generated.WorkflowRun{ID: r.ID, WorkflowID: r.WorkflowID, WorkflowName: r.WorkflowName, Version: r.Version,
				Status: r.Status, Trigger: r.Trigger, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, CreatedAt: r.CreatedAt}
			list, _ := out[r.CIID].([]*generated.WorkflowRun)
			out[r.CIID] = append(list, run)
		}
		return out, nil
	})
	if err != nil {
		return refusedList[generated.WorkflowRun](ctx, err)
	}
	list, _ := res.([]*generated.WorkflowRun)
	if list == nil {
		list = []*generated.WorkflowRun{}
	}
	return list, nil
}

// batchFor returns the value of ciID of one batch read per request, field
// and arguments (key) over the sibling CIs of ciID (ciLoader.fieldBatch).
func (c *ciResolver) batchFor(ctx context.Context, orgID, key, ciID string, fetch func(ids []string) (map[string]any, error)) (any, error) {
	l := loaderFrom(ctx)
	if l == nil {
		l = loaderFrom(withLoader(ctx, c.ciRepo, orgID))
	}
	v, err := l.fieldBatch(key, ciID, fetch)
	if err != nil {
		// The text of a source error stays in the log (API-03).
		slog.WarnContext(ctx, "bff: read of a P3 field failed", "field", strings.SplitN(key, "|", 2)[0], "error", err)
		return nil, gqlError(codeInternal, "the field could not be read")
	}
	return v, nil
}
