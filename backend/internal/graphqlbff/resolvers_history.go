package graphqlbff

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

// The history and the metrics of a CI (GQL-03, WP-792). Rights and gates
// are those of REST (GQL-04): ciHistory needs ci:read like GET
// /api/v1/history/ci/{id} (E-78), its audit entries audit:read and its
// relationship entries relationship:read (GET
// /api/v1/relationships/{id}/changes); the entries follow the field
// permissions of the CI (RBA-07, history.ProjectCITimeline), those of the
// relationships the relationship grant of their type. ciMetrics needs
// ci:read and the feature monitoring like GET /api/v1/cis/{id}/metrics.
// Both read the CI through the request loader first (loadCI): an invisible
// CI has no history (null) and no series (an empty list).

// defaultMetricsWindow is the window of ciMetrics without from, as on GET
// /api/v1/cis/{id}/metrics.
const defaultMetricsWindow = 24 * time.Hour

// HistoryEntry implements generated.ResolverRoot.
func (r *Resolver) HistoryEntry() generated.HistoryEntryResolver { return &historyEntryResolver{r} }

type historyEntryResolver struct{ *Resolver }

// window checks the time window of a query: to before from is bad input.
func window(from, to *time.Time) (start, end time.Time, err error) {
	if from != nil {
		start = from.UTC()
	}
	if to != nil {
		end = to.UTC()
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return start, end, gqlError(codeBadInput, "to must be greater than or equal to from")
	}
	return start, end, nil
}

// visibleCI returns the CI of id through the request loader; nil when it
// is not visible or id is no UUID.
func (r *Resolver) visibleCI(ctx context.Context, id string) (*ci.Item, error) {
	id = strings.TrimSpace(id)
	if !api.IsUUID(id) {
		return nil, nil //nolint:nilnil // GraphQL null for an invisible CI
	}
	return r.loadCI(ctx, id)
}

func (q *queryResolver) CiHistory(ctx context.Context, id string, from, to *time.Time, first *int, after *string) (*generated.HistoryConnection, error) {
	orgID, err := q.authorize(ctx, identity.PermCIRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	if q.sources.History == nil {
		return nil, errUnavailable()
	}
	start, end, err := window(from, to)
	if err != nil {
		return nil, err
	}
	offset := 0
	if raw := value(after); raw != "" {
		if offset, err = decodeCursor(raw); err != nil {
			return nil, gqlError(codeBadInput, "invalid cursor")
		}
	}
	item, err := q.visibleCI(ctx, id)
	if err != nil || item == nil {
		return nil, err
	}
	p, err := policy(ctx)
	if err != nil {
		return nil, err
	}
	principal, _ := identity.PrincipalFromContext(ctx)
	opts := history.TimelineOptions{From: start, To: end, IncludeAudit: principal.Has(identity.PermAuditRead),
		IncludeRelationships: principal.Has(identity.PermRelationshipRead)}
	entries, total, err := q.sources.History.CITimeline(ctx, orgID, item.ID,
		api.PaginationParams{Limit: pageSize(first, defaultPage, maxPage), Offset: offset}, opts)
	if errors.Is(err, api.ErrNotFound) {
		return nil, nil //nolint:nilnil // GraphQL null for an invisible CI
	}
	if err != nil {
		return nil, err
	}
	entries = history.ProjectCITimeline(p, item.ID, &history.CIFacts{TypeID: item.CITypeID, ClientID: item.ClientID,
		SiteID: item.SiteID, InstanceKeys: item.InstanceKeys}, entries)
	conn := &generated.HistoryConnection{TotalCount: total, PageInfo: &generated.PageInfo{HasNextPage: offset+len(entries) < total},
		Nodes: make([]*history.Change, len(entries))}
	for i := range entries {
		conn.Nodes[i] = &entries[i]
	}
	if len(entries) > 0 {
		cursor := encodeCursor(offset + len(entries))
		conn.PageInfo.EndCursor = &cursor
	}
	return conn, nil
}

func (q *queryResolver) CiMetrics(ctx context.Context, id string, name *string, from, to *time.Time, step *string) ([]*monitoring.CISeries, error) {
	orgID, err := q.authorize(ctx, identity.PermCIRead, entitlement.FeatureMonitoring)
	if err != nil {
		return nil, err
	}
	if q.sources.Metrics == nil {
		return nil, errUnavailable()
	}
	query := monitoring.MetricQuery{OrgID: orgID, Name: value(name)}
	if query.From, query.To, err = window(from, to); err != nil {
		return nil, err
	}
	if raw := value(step); raw != "" {
		if query.Step, err = time.ParseDuration(raw); err != nil {
			return nil, gqlError(codeBadInput, "invalid step duration")
		}
	}
	item, err := q.visibleCI(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []*monitoring.CISeries{}
	if item == nil {
		return out, nil
	}
	query.CIID = item.ID
	if query.From.IsZero() {
		query.From = time.Now().UTC().Add(-defaultMetricsWindow)
	}
	names := []string{query.Name}
	if query.Name == "" {
		names = nil
		if namer, ok := q.sources.Metrics.(monitoring.MetricNamer); ok {
			if names, err = namer.MetricNames(ctx, orgID, query.CIID, query.From); err != nil {
				return nil, err
			}
		}
	}
	for _, metric := range names[:min(len(names), maxPage)] {
		mq := query
		mq.Name = metric
		points, qerr := q.sources.Metrics.Query(ctx, &mq)
		if qerr != nil {
			return nil, qerr
		}
		if points == nil {
			points = []monitoring.MetricPoint{}
		}
		out = append(out, &monitoring.CISeries{Name: metric, Points: points})
	}
	return out, nil
}

func (h *historyEntryResolver) SiteID(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.SiteID), nil
}

func (h *historyEntryResolver) ActorID(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.ActorID), nil
}

func (h *historyEntryResolver) ActorUserID(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.ActorUserID), nil
}

func (h *historyEntryResolver) ChangeKind(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.ChangeKind), nil
}

func (h *historyEntryResolver) ChangeSetID(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.ChangeSetID), nil
}

func (h *historyEntryResolver) FieldName(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.FieldName), nil
}

func (h *historyEntryResolver) FieldChanges(_ context.Context, obj *history.Change) ([]*history.FieldChange, error) {
	out := make([]*history.FieldChange, len(obj.FieldChanges))
	for i := range obj.FieldChanges {
		out[i] = &obj.FieldChanges[i]
	}
	return out, nil
}

func (h *historyEntryResolver) Comment(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.Comment), nil
}

func (h *historyEntryResolver) ChangeSource(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.ChangeSource), nil
}

func (h *historyEntryResolver) RequestID(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.RequestID), nil
}

func (h *historyEntryResolver) CorrelationID(_ context.Context, obj *history.Change) (*string, error) {
	return optional(obj.CorrelationID), nil
}
