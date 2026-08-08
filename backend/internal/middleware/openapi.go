package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

const defaultOpenAPISpecPath = "api/openapi.yaml"

var defaultOpenAPIValidator = NewOpenAPIValidator()

// RouteSpec describes lightweight request validation rules.
type RouteSpec struct {
	RequiredQueryParams []string
	RequiresBody        bool
	AllowedContentTypes []string
}

// OpenAPIValidator performs lightweight route-based request validation.
type OpenAPIValidator struct {
	routes    map[string]RouteSpec // key: "POST /api/v1/cis"
	specPath  string
	specBytes []byte
}

// NewOpenAPIValidator creates a new validator with known route rules.
func NewOpenAPIValidator() *OpenAPIValidator {
	validator := &OpenAPIValidator{
		routes:   make(map[string]RouteSpec),
		specPath: configuredSpecPath(),
	}
	validator.loadSpec()
	validator.registerKnownRoutes()
	return validator
}

// OpenAPIValidation validates incoming requests against the lightweight route registry.
func OpenAPIValidation(next http.Handler) http.Handler {
	return defaultOpenAPIValidator.Middleware(next)
}

// Middleware validates a request before it reaches the next handler.
func (v *OpenAPIValidator) Middleware(next http.Handler) http.Handler {
	if v == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec, ok := v.lookup(r.Method, r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		if shouldValidateContentType(r.Method) {
			allowed := spec.AllowedContentTypes
			if len(allowed) == 0 {
				allowed = []string{"application/json"}
			}
			if !contentTypeAllowed(r.Header.Get("Content-Type"), allowed) {
				httpx.ValidationError(w, r, "Content-Type must be application/json")
				return
			}
		}

		for _, name := range spec.RequiredQueryParams {
			if strings.TrimSpace(r.URL.Query().Get(name)) == "" {
				httpx.ValidationError(w, r, "missing required query parameter: "+name)
				return
			}
		}

		if spec.RequiresBody {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				httpx.ValidationError(w, r, "unable to read request body")
				return
			}
			if len(bytes.TrimSpace(body)) == 0 {
				httpx.ValidationError(w, r, "request body is required")
				return
			}
			if !json.Valid(body) {
				httpx.ValidationError(w, r, "request body must be valid JSON")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		next.ServeHTTP(w, r)
	})
}

func (v *OpenAPIValidator) loadSpec() {
	for _, candidate := range resolveSpecCandidates(v.specPath) {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		v.specPath = candidate
		v.specBytes = data
		var doc struct {
			Paths map[string]map[string]json.RawMessage `json:"paths"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for path, methods := range doc.Paths {
				for method := range methods {
					v.register(strings.ToUpper(method), path, RouteSpec{})
				}
			}
		}
		return
	}
}

func (v *OpenAPIValidator) registerKnownRoutes() {
	bodyJSON := RouteSpec{RequiresBody: true, AllowedContentTypes: []string{"application/json"}}
	jsonOnly := RouteSpec{AllowedContentTypes: []string{"application/json"}}

	for _, route := range []string{
		"GET /healthz",
		"GET /api/v1/cis",
		"GET /api/v1/cis/{id}",
		"DELETE /api/v1/cis/{id}",
		"GET /api/v1/cis/{id}/relationships",
		"GET /api/v1/cis/{id}/changes",
		"GET /api/v1/audit",
		"DELETE /api/v1/relationships/{id}",
		"GET /api/v1/webhooks",
		"GET /api/v1/webhooks/{id}",
		"DELETE /api/v1/webhooks/{id}",
		"POST /api/v1/webhooks/{id}/test",
		"POST /api/v1/audit/verify",
		"GET /api/v1/collectors",
		"GET /api/v1/auth/me",
		"GET /api/v1/entitlements",
		"GET /api/v1/entitlements/check/{feature}",
		"GET /api/v1/users",
		"GET /api/v1/users/{id}",
		"DELETE /api/v1/users/{id}",
		"GET /api/v1/users/{id}/roles",
		"GET /api/v1/teams",
		"GET /api/v1/teams/{id}",
		"DELETE /api/v1/teams/{id}",
		"GET /api/v1/teams/{id}/members",
		"DELETE /api/v1/teams/{teamId}/members/{userId}",
		"GET /api/v1/roles",
		"GET /api/v1/roles/{id}",
		"DELETE /api/v1/roles/{id}",
		"GET /api/v1/assets",
		"GET /api/v1/assets/{id}",
		"DELETE /api/v1/assets/{id}",
		"GET /api/v1/tickets",
		"GET /api/v1/tickets/{id}",
		"DELETE /api/v1/tickets/{id}",
		"GET /api/v1/tickets/{id}/comments",
		"GET /api/v1/documents",
		"GET /api/v1/documents/{id}",
		"DELETE /api/v1/documents/{id}",
		"GET /api/v1/documents/{id}/links",
		"GET /api/v1/assignments",
		"GET /api/v1/assignments/{id}",
		"DELETE /api/v1/assignments/{id}",
		"GET /api/v1/stocktakes",
		"GET /api/v1/stocktakes/{id}",
		"DELETE /api/v1/stocktakes/{id}",
		"GET /api/v1/stocktakes/{id}/scans",
		"GET /api/v1/export/cis",
		"GET /api/v1/monitoring/metrics",
		"GET /api/v1/monitoring/alerts",
		"DELETE /api/v1/monitoring/alerts/{id}",
	} {
		parts := strings.SplitN(route, " ", 2)
		v.register(parts[0], parts[1], RouteSpec{})
	}

	for _, route := range []string{
		"POST /api/v1/cis",
		"PATCH /api/v1/cis/{id}",
		"POST /api/v1/relationships",
		"POST /api/v1/webhooks",
		"POST /api/v1/collectors",
		"POST /api/v1/ingest/bulk",
		"POST /api/v1/auth/callback",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/entitlements",
		"POST /api/v1/users",
		"PATCH /api/v1/users/{id}",
		"POST /api/v1/teams",
		"PATCH /api/v1/teams/{id}",
		"POST /api/v1/teams/{id}/members",
		"POST /api/v1/roles",
		"PATCH /api/v1/roles/{id}",
		"POST /api/v1/roles/assign",
		"POST /api/v1/assets",
		"PATCH /api/v1/assets/{id}",
		"POST /api/v1/tickets",
		"PATCH /api/v1/tickets/{id}",
		"POST /api/v1/tickets/{id}/comments",
		"POST /api/v1/documents",
		"PATCH /api/v1/documents/{id}",
		"POST /api/v1/documents/{id}/links",
		"POST /api/v1/assignments",
		"POST /api/v1/assignments/{id}/transfer",
		"POST /api/v1/stocktakes",
		"PATCH /api/v1/stocktakes/{id}",
		"POST /api/v1/stocktakes/{id}/scans",
		"POST /api/v1/monitoring/metrics",
		"POST /api/v1/monitoring/alerts",
	} {
		parts := strings.SplitN(route, " ", 2)
		v.register(parts[0], parts[1], bodyJSON)
	}

	for _, route := range []string{
		"POST /api/v1/collectors/{id}/heartbeat",
		"POST /api/v1/assignments/{id}/return",
	} {
		parts := strings.SplitN(route, " ", 2)
		v.register(parts[0], parts[1], jsonOnly)
	}
}

func (v *OpenAPIValidator) register(method, path string, spec RouteSpec) {
	if v.routes == nil {
		v.routes = make(map[string]RouteSpec)
	}
	v.routes[routeKey(method, path)] = spec
}

func (v *OpenAPIValidator) lookup(method, path string) (RouteSpec, bool) {
	cleanPath := cleanRoutePath(path)
	if spec, ok := v.routes[routeKey(method, cleanPath)]; ok {
		return spec, true
	}
	for key, spec := range v.routes {
		parts := strings.SplitN(key, " ", 2)
		if len(parts) != 2 || parts[0] != method {
			continue
		}
		if routePatternMatches(parts[1], cleanPath) {
			return spec, true
		}
	}
	return RouteSpec{}, false
}

func configuredSpecPath() string {
	for _, key := range []string{"RETICORA_OPENAPI_SPEC_PATH", "OPENAPI_SPEC_PATH"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultOpenAPISpecPath
}

func resolveSpecCandidates(path string) []string {
	if path == "" {
		return nil
	}
	if filepath.IsAbs(path) {
		return []string{path}
	}
	return []string{
		path,
		filepath.Join("..", path),
		filepath.Join("..", "..", path),
		filepath.Join("..", "..", "..", path),
	}
}

func shouldValidateContentType(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

func contentTypeAllowed(header string, allowed []string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(header, ";")[0])
	}
	for _, candidate := range allowed {
		if mediaType == candidate {
			return true
		}
	}
	return false
}

func routePatternMatches(pattern, path string) bool {
	pattern = cleanRoutePath(pattern)
	path = cleanRoutePath(path)
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if pattern == "/" && path == "/" {
		return true
	}
	if len(patternParts) != len(pathParts) {
		return false
	}
	for i := range patternParts {
		if isPathPlaceholder(patternParts[i]) {
			continue
		}
		if patternParts[i] != pathParts[i] {
			return false
		}
	}
	return true
}

func isPathPlaceholder(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

func routeKey(method, path string) string {
	return method + " " + cleanRoutePath(path)
}

func cleanRoutePath(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return clean
}
