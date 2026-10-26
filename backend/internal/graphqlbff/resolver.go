package graphqlbff

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// Page sizes of the list fields.
const (
	defaultPage      = 50
	maxPage          = 100
	maxRelationships = 100
)

// Error codes of the BFF (extensions.code).
const (
	codeUnauthenticated = "UNAUTHENTICATED"
	codeForbidden       = "FORBIDDEN"
	codeBadInput        = "BAD_USER_INPUT"
)

// Resolver resolves the queries of the BFF.
type Resolver struct {
	ciRepo       ci.Repository
	relRepo      relationship.Repository
	entitlements EntitlementChecker
	sources      Sources
}

// Query implements generated.ResolverRoot.
func (r *Resolver) Query() generated.QueryResolver { return &queryResolver{r} }

// Job implements generated.ResolverRoot.
func (r *Resolver) Job() generated.JobResolver { return &jobResolver{r} }

// RackMount implements generated.ResolverRoot.
func (r *Resolver) RackMount() generated.RackMountResolver { return &rackMountResolver{r} }

// Relationship implements generated.ResolverRoot.
func (r *Resolver) Relationship() generated.RelationshipResolver { return &relationshipResolver{r} }

// ReviewItem implements generated.ResolverRoot.
func (r *Resolver) ReviewItem() generated.ReviewItemResolver { return &reviewItemResolver{r} }

// TopologyNode implements generated.ResolverRoot.
func (r *Resolver) TopologyNode() generated.TopologyNodeResolver { return &topologyNodeResolver{r} }

type (
	queryResolver        struct{ *Resolver }
	jobResolver          struct{ *Resolver }
	rackMountResolver    struct{ *Resolver }
	relationshipResolver struct{ *Resolver }
	reviewItemResolver   struct{ *Resolver }
	topologyNodeResolver struct{ *Resolver }
)

// errUnavailable answers a query whose source is not configured.
var errUnavailable = gqlError("UNAVAILABLE", "this query is not available on this server")

func gqlError(code, message string) *gqlerror.Error {
	return &gqlerror.Error{Message: message, Extensions: map[string]any{"code": code}}
}

// authorize checks the field's read permission against the principal and
// the organization's entitlement; it fails closed and returns the
// organization.
func (r *Resolver) authorize(ctx context.Context, perm identity.Permission, feature string) (string, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	orgID := tenant.FromContext(ctx).OrganizationID
	if !ok || orgID == "" {
		return "", gqlError(codeUnauthenticated, "missing authenticated principal")
	}
	if perm != "" && !principal.Has(perm) {
		return "", gqlError(codeForbidden, "missing required permission: "+string(perm))
	}
	if feature != "" && (r.entitlements == nil || !r.entitlements.IsEnabled(ctx, orgID, feature)) {
		return "", gqlError(codeForbidden, "feature "+strconv.Quote(feature)+" is not included in the plan")
	}
	return orgID, nil
}

// pageSize applies the default and the maximum of a page.
func pageSize(first *int, def, maximum int) int {
	if first == nil || *first <= 0 {
		return def
	}
	return min(*first, maximum)
}

func (q *queryResolver) Cis(ctx context.Context, first *int, after *string, filter *generated.CIFilter) (*generated.CIConnection, error) {
	orgID, err := q.authorize(ctx, identity.PermCIRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	limit := pageSize(first, defaultPage, maxPage)
	offset := 0
	if after != nil && *after != "" {
		if offset, err = decodeCursor(*after); err != nil {
			return nil, gqlError(codeBadInput, "invalid cursor")
		}
	}
	items, total, err := q.ciRepo.List(ctx, orgID, ciFilter(filter), api.PaginationParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	conn := &generated.CIConnection{TotalCount: total, PageInfo: &generated.PageInfo{HasNextPage: offset+len(items) < total},
		Edges: make([]*generated.CIEdge, 0, len(items)), Nodes: make([]*ci.Item, 0, len(items))}
	for i := range items {
		item := &items[i]
		conn.Edges = append(conn.Edges, &generated.CIEdge{Cursor: encodeCursor(offset + i + 1), Node: item})
		conn.Nodes = append(conn.Nodes, item)
	}
	if n := len(conn.Edges); n > 0 {
		conn.PageInfo.EndCursor = &conn.Edges[n-1].Cursor
	}
	return conn, nil
}

func (q *queryResolver) Ci(ctx context.Context, id string) (*ci.Item, error) {
	orgID, err := q.authorize(ctx, identity.PermCIRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	item, err := q.ciRepo.GetByID(ctx, orgID, strings.TrimSpace(id))
	if err != nil {
		// Not visible or not there: null, as for any missing object.
		return nil, nil //nolint:nilnil // GraphQL null for an invisible CI
	}
	return item, nil
}

func (q *queryResolver) Relationships(ctx context.Context, ciID string, first *int) ([]*relationship.Relationship, error) {
	orgID, err := q.authorize(ctx, identity.PermRelationshipRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	rels, _, err := q.relRepo.List(ctx, orgID, strings.TrimSpace(ciID),
		api.PaginationParams{Limit: pageSize(first, maxRelationships, maxRelationships)})
	if err != nil {
		return nil, err
	}
	out := make([]*relationship.Relationship, len(rels))
	ids := make([]string, 0, 2*len(rels))
	for i := range rels {
		out[i] = &rels[i]
		ids = append(ids, rels[i].SourceCIID, rels[i].TargetCIID)
	}
	// Both ends of every relationship in one batch (GQL-02).
	q.prime(ctx, ids)
	return out, nil
}

func (q *queryResolver) CurrentUser(ctx context.Context) (*generated.User, error) {
	if _, err := q.authorize(ctx, "", ""); err != nil {
		return nil, err
	}
	t := tenant.FromContext(ctx)
	user := &generated.User{ID: t.UserID, OrganizationID: t.OrganizationID}
	if t.ClientID != "" {
		user.ClientID = &t.ClientID
	}
	return user, nil
}

func ciFilter(f *generated.CIFilter) ci.FilterParams {
	out := ci.FilterParams{}
	if f == nil {
		return out
	}
	value := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	out.Status, out.TypeID, out.ClientID, out.SiteID, out.Search =
		value(f.Status), value(f.TypeID), value(f.ClientID), value(f.SiteID), value(f.Search)
	return out
}

// Cursors are opaque offsets.
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("offset:" + strconv.Itoa(offset)))
}

func decodeCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(raw), "offset:"))
	if err != nil || n < 0 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

// prime loads the CIs a list hands out in one batch; nested CI fields then
// read the cache. A failed batch is retried by the field loads.
func (r *Resolver) prime(ctx context.Context, ids []string) {
	if l := loaderFrom(ctx); l != nil && r.mayReadCIs(ctx) {
		_ = l.Prime(ctx, ids)
	}
}

// mayReadCIs reports whether the principal may read CIs (nested CI fields).
func (r *Resolver) mayReadCIs(ctx context.Context) bool {
	_, err := r.authorize(ctx, identity.PermCIRead, FeatureCMDBCore)
	return err == nil
}

// loadCI resolves a nested CI field through the dataloader; it needs
// ci:read like the ci query (GQL-04).
func (r *Resolver) loadCI(ctx context.Context, id string) (*ci.Item, error) {
	if _, err := r.authorize(ctx, identity.PermCIRead, FeatureCMDBCore); err != nil {
		return nil, err
	}
	if l := loaderFrom(ctx); l != nil {
		return l.Load(ctx, id)
	}
	item, err := r.ciRepo.GetByID(ctx, tenant.FromContext(ctx).OrganizationID, id)
	if err != nil {
		return nil, nil //nolint:nilnil // GraphQL null for an invisible CI
	}
	return item, nil
}

func (q *queryResolver) Topology(ctx context.Context, rootCiID *string, depth *int, ciTypeID, siteID *string) (*topology.Graph, error) {
	orgID, err := q.authorize(ctx, identity.PermTopologyRead, entitlement.FeatureTopology)
	if err != nil {
		return nil, err
	}
	if q.sources.Topology == nil {
		return nil, errUnavailable
	}
	d := topology.DefaultDepth
	if depth != nil {
		// A depth below 1 is bad input as on GET /topology (F-255).
		if d, err = topology.ParseDepth(strconv.Itoa(*depth)); err != nil {
			return nil, gqlError(codeBadInput, err.Error())
		}
	}
	graph, err := q.sources.Topology.Graph(ctx, orgID, value(rootCiID), d, &ci.FilterParams{TypeID: value(ciTypeID), SiteID: value(siteID)})
	if err != nil {
		var fe *api.FieldError
		if errors.As(err, &fe) {
			return nil, gqlError(codeBadInput, fe.Error())
		}
		return nil, err
	}
	ids := make([]string, len(graph.Nodes))
	for i := range graph.Nodes {
		ids[i] = graph.Nodes[i].ID
	}
	q.prime(ctx, ids)
	return &graph, nil
}

func (q *queryResolver) RackLayout(ctx context.Context, rackID string) (*generated.RackLayout, error) {
	// Racks are the rack_view feature, not the core (ENT-05, F-070).
	orgID, err := q.authorize(ctx, identity.PermRackRead, entitlement.FeatureRackView)
	if err != nil {
		return nil, err
	}
	if q.sources.Racks == nil {
		return nil, errUnavailable
	}
	rk, err := q.sources.Racks.GetRack(ctx, orgID, rackID)
	if err != nil {
		return nil, nil //nolint:nilnil // GraphQL null for an invisible rack
	}
	mounts, _, err := q.sources.Racks.ListMounts(ctx, orgID, rk.ID, api.PaginationParams{Limit: 2 * maxPage})
	if err != nil {
		return nil, err
	}
	free := rack.FreeUnits(rk.HeightUnits, mounts)
	layout := &generated.RackLayout{Rack: rk, FreeFront: free["front"], FreeRear: free["rear"],
		Mounts: make([]*rack.RackMount, len(mounts))}
	ids := make([]string, len(mounts))
	for i := range mounts {
		layout.Mounts[i] = &mounts[i]
		ids[i] = mounts[i].CIID
	}
	q.prime(ctx, ids)
	return layout, nil
}

func (q *queryResolver) SearchEverything(ctx context.Context, text string, types []string, first *int) (*generated.SearchResult, error) {
	orgID, err := q.authorize(ctx, identity.PermSearchRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	if q.sources.Search == nil {
		return nil, errUnavailable
	}
	res, err := q.sources.Search.Query(ctx, search.Query{OrganizationID: orgID, UserID: tenant.FromContext(ctx).UserID,
		Text: text, EntityTypes: types, Limit: pageSize(first, defaultPage, maxPage)})
	if err != nil {
		return nil, gqlError(codeBadInput, err.Error())
	}
	// Only the entity types the caller may read (SRC-01).
	principal, _ := identity.PrincipalFromContext(ctx)
	out := &generated.SearchResult{Hits: []*search.Hit{}}
	for i := range res.Data {
		perm, known := search.ReadPermission[res.Data[i].EntityType]
		if known && principal.Has(identity.Permission(perm)) {
			out.Hits = append(out.Hits, &res.Data[i])
		}
	}
	out.Total = len(out.Hits)
	return out, nil
}

func (q *queryResolver) ReviewInbox(ctx context.Context, status, kind *string, first *int) ([]*reviews.Item, error) {
	orgID, err := q.authorize(ctx, identity.PermDiscoveryRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	if q.sources.Reviews == nil {
		return nil, errUnavailable
	}
	items, _, err := q.sources.Reviews.ListReviewItems(ctx, orgID, reviews.Filter{Status: value(status), Kind: value(kind)},
		api.PaginationParams{Limit: pageSize(first, defaultPage, maxPage)})
	if err != nil {
		return nil, err
	}
	out := make([]*reviews.Item, len(items))
	var ids []string
	for i := range items {
		out[i] = &items[i]
		ids = append(ids, items[i].CandidateCIIDs...)
	}
	q.prime(ctx, ids)
	return out, nil
}

func (q *queryResolver) Jobs(ctx context.Context, kind, status *string, first *int) ([]*jobs.Job, error) {
	orgID, err := q.authorize(ctx, identity.PermJobRead, FeatureCMDBCore)
	if err != nil {
		return nil, err
	}
	if q.sources.Jobs == nil {
		return nil, errUnavailable
	}
	list, _, err := q.sources.Jobs.List(ctx, orgID, jobs.Filter{Kind: value(kind), Status: jobs.Status(value(status))},
		api.PaginationParams{Limit: pageSize(first, defaultPage, maxPage)})
	if err != nil {
		return nil, err
	}
	out := make([]*jobs.Job, len(list))
	for i := range list {
		out[i] = &list[i]
	}
	return out, nil
}

func (j *jobResolver) Status(_ context.Context, obj *jobs.Job) (string, error) {
	return string(obj.Status), nil
}

func (m *rackMountResolver) Ci(ctx context.Context, obj *rack.RackMount) (*ci.Item, error) {
	// A rack field: it checks rack_view like rackLayout (F-070).
	if _, err := m.authorize(ctx, identity.PermRackRead, entitlement.FeatureRackView); err != nil {
		return nil, err
	}
	return m.loadCI(ctx, obj.CIID)
}

func (r *relationshipResolver) SourceCi(ctx context.Context, obj *relationship.Relationship) (*ci.Item, error) {
	return r.loadCI(ctx, obj.SourceCIID)
}

func (r *relationshipResolver) TargetCi(ctx context.Context, obj *relationship.Relationship) (*ci.Item, error) {
	return r.loadCI(ctx, obj.TargetCIID)
}

func (r *reviewItemResolver) Candidates(ctx context.Context, obj *reviews.Item) ([]*ci.Item, error) {
	out := make([]*ci.Item, 0, len(obj.CandidateCIIDs))
	for _, id := range obj.CandidateCIIDs {
		item, err := r.loadCI(ctx, id)
		if err != nil {
			return nil, err
		}
		if item != nil {
			out = append(out, item)
		}
	}
	return out, nil
}

func (n *topologyNodeResolver) Ci(ctx context.Context, obj *topology.Node) (*ci.Item, error) {
	return n.loadCI(ctx, obj.ID)
}

func value(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}
