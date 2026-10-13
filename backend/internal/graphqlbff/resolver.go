package graphqlbff

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
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
}

// Query implements generated.ResolverRoot.
func (r *Resolver) Query() generated.QueryResolver { return &queryResolver{r} }

type queryResolver struct{ *Resolver }

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
	for i := range rels {
		out[i] = &rels[i]
	}
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
