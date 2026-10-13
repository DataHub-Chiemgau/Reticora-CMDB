// Package graphqlbff is the GraphQL Backend-for-Frontend of the web UI
// (GQL-01): a gqlgen server under /bff/graphql that only answers session
// principals, is not part of the public REST contract and enforces the
// limits of SEC-10 (depth 10, a complexity budget, a 10 s deadline).
package graphqlbff

import (
	"context"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/go-chi/chi/v5"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// Path is the route of the BFF (GQL-01).
const Path = "/bff/graphql"

// Limits of SEC-10 (E-16, provisional until the SEC-10 text): the depth of
// a query, its complexity budget and the deadline of one request.
const (
	MaxDepth      = 10
	MaxComplexity = 5000
	Deadline      = 10 * time.Second
)

// FeatureCMDBCore is the entitlement of the core CMDB fields (always active
// for an organization, ENT-02).
const FeatureCMDBCore = "cmdb_core"

// EntitlementChecker reports whether an organization may use a feature.
type EntitlementChecker interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

// Handler serves the GraphQL BFF. The route requires an authenticated
// session; every resolver checks its own read permission and entitlement
// and reads through the tenant-scoped repositories (RLS with the
// principal's client, site and team scope).
type Handler struct {
	resolver *Resolver
	server   *handler.Server
	deadline time.Duration
}

// NewHandler creates the BFF with the CI and relationship resolvers.
func NewHandler(ciRepo ci.Repository, relRepo relationship.Repository) *Handler {
	r := &Resolver{ciRepo: ciRepo, relRepo: relRepo}
	cfg := generated.Config{Resolvers: r}
	// A page of CIs costs its size times the selection (SEC-10).
	cfg.Complexity.Query.Cis = func(child int, first *int, _ *string, _ *generated.CIFilter) int {
		return child * pageSize(first, defaultPage, maxPage)
	}
	cfg.Complexity.Query.Relationships = func(child int, _ string, first *int) int {
		return child * pageSize(first, maxRelationships, maxRelationships)
	}
	srv := handler.New(generated.NewExecutableSchema(cfg))
	// POST only: no GET queries (no CSRF-able reads), no subscriptions.
	srv.AddTransport(transport.POST{})
	srv.Use(extension.FixedComplexityLimit(MaxComplexity))
	srv.Use(depthLimit{max: MaxDepth})
	// Every request carries a session (ServeHTTP), so introspection is
	// never anonymous (SEC-10).
	srv.Use(extension.Introspection{})
	return &Handler{resolver: r, server: srv, deadline: Deadline}
}

// WithEntitlements attaches the entitlement check of feature-gated fields.
// Without a checker, gated fields are refused.
func (h *Handler) WithEntitlements(entitlements EntitlementChecker) *Handler {
	h.resolver.entitlements = entitlements
	return h
}

// RegisterRoutes registers POST /bff/graphql.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post(Path, h.ServeHTTP)
}

// ServeHTTP answers session principals only (GQL-01): API keys and
// service tokens use the REST API. The request runs under the deadline of
// SEC-10.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "the BFF requires a session")
		return
	}
	if principal.Type != identity.PrincipalTypeUser {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the BFF accepts session tokens only; use the REST API with API keys")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deadline)
	defer cancel()
	h.server.ServeHTTP(w, r.WithContext(ctx))
}

// depthLimit refuses operations nested deeper than max fields (SEC-10).
type depthLimit struct{ max int }

var _ interface {
	graphql.HandlerExtension
	graphql.OperationContextMutator
} = depthLimit{}

func (depthLimit) ExtensionName() string                     { return "DepthLimit" }
func (depthLimit) Validate(_ graphql.ExecutableSchema) error { return nil }

func (d depthLimit) MutateOperationContext(_ context.Context, oc *graphql.OperationContext) *gqlerror.Error {
	if depth := selectionDepth(oc.Operation.SelectionSet, oc.Doc.Fragments); depth > d.max {
		return gqlerror.Errorf("the query is %d levels deep, more than the limit of %d", depth, d.max)
	}
	return nil
}

// selectionDepth is the deepest field nesting of a selection set; inline
// fragments and fragment spreads add no level. Validation has rejected
// fragment cycles before.
func selectionDepth(set ast.SelectionSet, fragments ast.FragmentDefinitionList) int {
	deepest := 0
	for _, sel := range set {
		var depth int
		switch s := sel.(type) {
		case *ast.Field:
			depth = 1 + selectionDepth(s.SelectionSet, fragments)
		case *ast.InlineFragment:
			depth = selectionDepth(s.SelectionSet, fragments)
		case *ast.FragmentSpread:
			if def := fragments.ForName(s.Name); def != nil {
				depth = selectionDepth(def.SelectionSet, fragments)
			}
		}
		deepest = max(deepest, depth)
	}
	return deepest
}
