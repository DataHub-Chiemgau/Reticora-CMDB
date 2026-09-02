// Package graphqlbff provides a lightweight GraphQL Backend-for-Frontend layer.
package graphqlbff

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// QueryResolver resolves a top-level GraphQL query field.
type QueryResolver func(ctx context.Context, args map[string]any) (any, error)

// Schema holds registered top-level query resolvers.
type Schema struct {
	queries map[string]QueryResolver
}

// NewSchema creates an empty schema.
func NewSchema() *Schema {
	return &Schema{queries: make(map[string]QueryResolver)}
}

// RegisterQuery registers a top-level query resolver.
func (s *Schema) RegisterQuery(name string, resolver QueryResolver) {
	s.queries[name] = resolver
}

// Handler serves the GraphQL BFF endpoint.
type Handler struct {
	ciRepo   ci.Repository
	relRepo  relationship.Repository
	ciLoader func(ctx context.Context, orgID, id string) (*ci.Item, error)
	schema   *Schema
}

// User represents the current authenticated user in GraphQL responses.
type User struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	ClientID       string `json:"clientId,omitempty"`
}

// CIConnection is a minimal Relay-style connection for CIs.
type CIConnection struct {
	Edges      []CIEdge  `json:"edges"`
	PageInfo   PageInfo  `json:"pageInfo"`
	TotalCount int       `json:"totalCount"`
	Nodes      []ci.Item `json:"nodes"`
}

// CIEdge represents a CI result edge.
type CIEdge struct {
	Cursor string  `json:"cursor"`
	Node   ci.Item `json:"node"`
}

// PageInfo describes pagination state.
type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor,omitempty"`
}

type graphQLRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName,omitempty"`
}

type graphQLResponse struct {
	Data   map[string]any `json:"data,omitempty"`
	Errors []graphQLError `json:"errors,omitempty"`
}

type graphQLError struct {
	Message string `json:"message"`
}

type parsedOperation struct {
	Type  string
	Name  string
	Field string
	Args  map[string]any
}

var defaultHandler *Handler

// NewHandler creates a GraphQL BFF handler with CI and relationship resolvers.
func NewHandler(ciRepo ci.Repository, relRepo relationship.Repository) *Handler {
	h := &Handler{
		ciRepo:  ciRepo,
		relRepo: relRepo,
		schema:  NewSchema(),
	}
	h.registerQueries()
	defaultHandler = h
	return h
}

// WithCILoader replaces the single-CI loader used by the `ci` query, letting
// the router route reads through the CI service so GraphQL responses carry
// the same effective (override-aware) attributes as the REST read model.
func (h *Handler) WithCILoader(loader func(ctx context.Context, orgID, id string) (*ci.Item, error)) *Handler {
	h.ciLoader = loader
	return h
}

// RegisterRoutes registers the GraphQL BFF endpoint using the most recently created handler.
func RegisterRoutes(r chi.Router) {
	if defaultHandler == nil {
		r.Post("/api/v1/graphql", func(w http.ResponseWriter, r *http.Request) {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "graphql handler is not initialized")
		})
		return
	}
	defaultHandler.RegisterRoutes(r)
}

// RegisterRoutes registers POST /api/v1/graphql on the provided router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/graphql", h.ServeHTTP)
}

// ServeHTTP handles GraphQL requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.WriteError(w, http.StatusMethodNotAllowed, "Method Not Allowed", "only POST is supported")
		return
	}

	var req graphQLRequest
	if err := api.ReadJSON(r, &req); err != nil {
		writeGraphQLErrors(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeGraphQLErrors(w, http.StatusBadRequest, fmt.Errorf("query is required"))
		return
	}

	resp, status := h.execute(r.Context(), req)
	api.WriteJSON(w, status, resp)
}

func (h *Handler) registerQueries() {
	h.schema.RegisterQuery("cis", h.resolveCIs)
	h.schema.RegisterQuery("ci", h.resolveCI)
	h.schema.RegisterQuery("relationships", h.resolveRelationships)
	h.schema.RegisterQuery("currentUser", h.resolveCurrentUser)
}

func (h *Handler) execute(ctx context.Context, req graphQLRequest) (graphQLResponse, int) {
	op, err := parseOperation(req.Query, req.Variables)
	if err != nil {
		return graphQLResponse{Errors: []graphQLError{{Message: err.Error()}}}, http.StatusBadRequest
	}

	resolver, ok := h.schema.queries[op.Field]
	if !ok {
		return graphQLResponse{Errors: []graphQLError{{Message: fmt.Sprintf("unsupported query field %q", op.Field)}}}, http.StatusBadRequest
	}

	result, err := resolver(ctx, op.Args)
	if err != nil {
		return graphQLResponse{
			Data:   map[string]any{op.Field: nil},
			Errors: []graphQLError{{Message: err.Error()}},
		}, http.StatusOK
	}

	return graphQLResponse{Data: map[string]any{op.Field: result}}, http.StatusOK
}

func (h *Handler) resolveCIs(ctx context.Context, args map[string]any) (any, error) {
	t := tenant.FromContext(ctx)
	if t.OrganizationID == "" {
		return nil, fmt.Errorf("missing tenant context")
	}

	filter := ci.FilterParams{}
	if rawFilter, ok := args["filter"].(map[string]any); ok {
		filter = ciFilterFromMap(rawFilter)
	}

	first := 50
	if v, ok := args["first"]; ok {
		parsed, err := toInt(v)
		if err != nil {
			return nil, fmt.Errorf("invalid first argument: %w", err)
		}
		if parsed > 0 {
			first = parsed
		}
	}
	if first > 100 {
		first = 100
	}

	offset := 0
	if v, ok := args["after"]; ok {
		offset = decodeCursor(asString(v))
	}

	items, total, err := h.ciRepo.List(ctx, t.OrganizationID, filter, api.PaginationParams{Limit: first, Offset: offset})
	if err != nil {
		return nil, err
	}

	edges := make([]CIEdge, 0, len(items))
	nodes := make([]ci.Item, 0, len(items))
	for idx, item := range items {
		cursor := encodeCursor(offset + idx + 1)
		edges = append(edges, CIEdge{Cursor: cursor, Node: item})
		nodes = append(nodes, item)
	}

	endCursor := ""
	if len(edges) > 0 {
		endCursor = edges[len(edges)-1].Cursor
	}

	return CIConnection{
		Edges:      edges,
		Nodes:      nodes,
		TotalCount: total,
		PageInfo: PageInfo{
			HasNextPage: offset+len(items) < total,
			EndCursor:   endCursor,
		},
	}, nil
}

func (h *Handler) resolveCI(ctx context.Context, args map[string]any) (any, error) {
	t := tenant.FromContext(ctx)
	if t.OrganizationID == "" {
		return nil, fmt.Errorf("missing tenant context")
	}

	id := strings.TrimSpace(asString(args["id"]))
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}

	if h.ciLoader != nil {
		return h.ciLoader(ctx, t.OrganizationID, id)
	}
	return h.ciRepo.GetByID(ctx, t.OrganizationID, id)
}

func (h *Handler) resolveRelationships(ctx context.Context, args map[string]any) (any, error) {
	t := tenant.FromContext(ctx)
	if t.OrganizationID == "" {
		return nil, fmt.Errorf("missing tenant context")
	}

	ciID := strings.TrimSpace(asString(firstNonNil(args["ciId"], args["ci_id"])))
	if ciID == "" {
		return nil, fmt.Errorf("ciId is required")
	}

	rels, _, err := h.relRepo.List(ctx, t.OrganizationID, ciID, api.PaginationParams{Limit: 100, Offset: 0})
	if err != nil {
		return nil, err
	}
	return rels, nil
}

func (h *Handler) resolveCurrentUser(ctx context.Context, _ map[string]any) (any, error) {
	t := tenant.FromContext(ctx)
	if t.OrganizationID == "" {
		return nil, fmt.Errorf("missing tenant context")
	}

	return User{
		ID:             t.UserID,
		OrganizationID: t.OrganizationID,
		ClientID:       t.ClientID,
	}, nil
}

func writeGraphQLErrors(w http.ResponseWriter, status int, errs ...error) {
	resp := graphQLResponse{Errors: make([]graphQLError, 0, len(errs))}
	for _, err := range errs {
		if err == nil {
			continue
		}
		resp.Errors = append(resp.Errors, graphQLError{Message: err.Error()})
	}
	api.WriteJSON(w, status, resp)
}

func ciFilterFromMap(values map[string]any) ci.FilterParams {
	return ci.FilterParams{
		Status:   asString(firstNonNil(values["status"], values["Status"])),
		TypeID:   asString(firstNonNil(values["ciTypeId"], values["ci_type_id"], values["typeId"], values["type_id"])),
		ClientID: asString(firstNonNil(values["clientId"], values["client_id"])),
		SiteID:   asString(firstNonNil(values["siteId"], values["site_id"])),
		Search:   asString(firstNonNil(values["search"], values["q"])),
		SortBy:   asString(firstNonNil(values["sortBy"], values["sort_by"])),
		SortDir:  asString(firstNonNil(values["sortDir"], values["sort_dir"])),
	}
}

func encodeCursor(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeCursor(cursor string) int {
	if cursor == "" {
		return 0
	}
	if decoded, err := base64.StdEncoding.DecodeString(cursor); err == nil {
		if n, err := strconv.Atoi(string(decoded)); err == nil && n >= 0 {
			return n
		}
	}
	if n, err := strconv.Atoi(cursor); err == nil && n >= 0 {
		return n
	}
	return 0
}

func parseOperation(query string, variables map[string]any) (*parsedOperation, error) {
	p := &parser{src: query, variables: variables}
	op := &parsedOperation{Type: "query"}

	p.skipIgnored()
	if p.peek() != '{' {
		name := p.readName()
		if name == "" {
			return nil, fmt.Errorf("invalid graphql query")
		}
		if name == "query" || name == "mutation" {
			op.Type = name
			p.skipIgnored()
			if p.peek() != '{' && p.peek() != '(' {
				op.Name = p.readName()
				p.skipIgnored()
			}
			if p.peek() == '(' {
				if err := p.skipBalanced('(', ')'); err != nil {
					return nil, err
				}
			}
		} else {
			return nil, fmt.Errorf("unsupported operation type %q", name)
		}
	}

	p.skipIgnored()
	if p.peek() != '{' {
		return nil, fmt.Errorf("graphql query must contain a selection set")
	}
	p.pos++
	p.skipIgnored()

	op.Field = p.readName()
	if op.Field == "" {
		return nil, fmt.Errorf("missing top-level query field")
	}

	p.skipIgnored()
	args := map[string]any{}
	if p.peek() == '(' {
		parsedArgs, err := p.parseArguments()
		if err != nil {
			return nil, err
		}
		args = parsedArgs
	}
	op.Args = args

	return op, nil
}

type parser struct {
	src       string
	pos       int
	variables map[string]any
}

func (p *parser) parseArguments() (map[string]any, error) {
	args := make(map[string]any)
	if p.peek() != '(' {
		return args, nil
	}
	p.pos++

	for {
		p.skipIgnored()
		if p.peek() == ')' {
			p.pos++
			return args, nil
		}

		name := p.readName()
		if name == "" {
			return nil, fmt.Errorf("invalid argument list")
		}
		p.skipIgnored()
		if p.peek() != ':' {
			return nil, fmt.Errorf("expected ':' after argument %q", name)
		}
		p.pos++
		p.skipIgnored()

		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		args[name] = value

		p.skipIgnored()
		if p.peek() == ',' {
			p.pos++
		}
	}
}

func (p *parser) parseValue() (any, error) {
	switch ch := p.peek(); {
	case ch == '"':
		return p.parseString()
	case ch == '$':
		p.pos++
		name := p.readName()
		if name == "" {
			return nil, fmt.Errorf("invalid variable reference")
		}
		if p.variables == nil {
			return nil, fmt.Errorf("variable %q not provided", name)
		}
		value, ok := p.variables[name]
		if !ok {
			return nil, fmt.Errorf("variable %q not provided", name)
		}
		return value, nil
	case ch == '{':
		return p.parseObject()
	case ch == '[':
		return p.parseArray()
	case ch == '-' || unicode.IsDigit(ch):
		return p.parseNumber()
	case isNameStart(ch):
		name := p.readName()
		switch name {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		default:
			return name, nil
		}
	default:
		return nil, fmt.Errorf("unsupported value near position %d", p.pos)
	}
}

func (p *parser) parseObject() (map[string]any, error) {
	obj := make(map[string]any)
	if p.peek() != '{' {
		return nil, fmt.Errorf("expected object")
	}
	p.pos++

	for {
		p.skipIgnored()
		if p.peek() == '}' {
			p.pos++
			return obj, nil
		}

		key := p.readName()
		if key == "" {
			return nil, fmt.Errorf("invalid object field")
		}
		p.skipIgnored()
		if p.peek() != ':' {
			return nil, fmt.Errorf("expected ':' after object field %q", key)
		}
		p.pos++
		p.skipIgnored()

		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		obj[key] = value

		p.skipIgnored()
		if p.peek() == ',' {
			p.pos++
		}
	}
}

func (p *parser) parseArray() ([]any, error) {
	if p.peek() != '[' {
		return nil, fmt.Errorf("expected array")
	}
	p.pos++

	var values []any
	for {
		p.skipIgnored()
		if p.peek() == ']' {
			p.pos++
			return values, nil
		}

		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, value)

		p.skipIgnored()
		if p.peek() == ',' {
			p.pos++
		}
	}
}

func (p *parser) parseString() (string, error) {
	if p.peek() != '"' {
		return "", fmt.Errorf("expected string")
	}
	start := p.pos
	p.pos++
	escaped := false
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch == '\\' && !escaped {
			escaped = true
			p.pos++
			continue
		}
		if ch == '"' && !escaped {
			p.pos++
			var out string
			if err := json.Unmarshal([]byte(p.src[start:p.pos]), &out); err != nil {
				return "", fmt.Errorf("invalid string literal: %w", err)
			}
			return out, nil
		}
		escaped = false
		p.pos++
	}
	return "", fmt.Errorf("unterminated string literal")
}

func (p *parser) parseNumber() (any, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	for unicode.IsDigit(p.peek()) {
		p.pos++
	}
	if p.peek() == '.' {
		p.pos++
		for unicode.IsDigit(p.peek()) {
			p.pos++
		}
		return strconv.ParseFloat(p.src[start:p.pos], 64)
	}
	return strconv.Atoi(p.src[start:p.pos])
}

func (p *parser) skipBalanced(open, close rune) error {
	if p.peek() != open {
		return nil
	}
	depth := 0
	for p.pos < len(p.src) {
		ch := rune(p.src[p.pos])
		if ch == '"' {
			if _, err := p.parseString(); err != nil {
				return err
			}
			continue
		}
		if ch == open {
			depth++
		}
		if ch == close {
			depth--
			p.pos++
			if depth == 0 {
				return nil
			}
			continue
		}
		p.pos++
	}
	return fmt.Errorf("unterminated %q block", string(open))
}

func (p *parser) skipIgnored() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r', ',':
			p.pos++
		case '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *parser) readName() string {
	if !isNameStart(p.peek()) {
		return ""
	}
	start := p.pos
	p.pos++
	for isNamePart(p.peek()) {
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) peek() rune {
	if p.pos >= len(p.src) {
		return 0
	}
	return rune(p.src[p.pos])
}

func isNameStart(ch rune) bool {
	return ch == '_' || unicode.IsLetter(ch)
}

func isNamePart(ch rune) bool {
	return isNameStart(ch) || unicode.IsDigit(ch)
}

func toInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case json.Number:
		parsed, err := v.Int64()
		return int(parsed), err
	case string:
		return strconv.Atoi(strings.TrimSpace(v))
	default:
		return 0, fmt.Errorf("unsupported int value %T", value)
	}
}

func asString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
