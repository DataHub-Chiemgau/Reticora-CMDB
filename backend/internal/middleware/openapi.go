package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
	"gopkg.in/yaml.v3"
)

const defaultOpenAPISpecPath = "api/openapi.yaml"

var defaultOpenAPIValidator = NewOpenAPIValidator()

// RouteSpec describes lightweight request validation rules: the required
// query parameters, whether the operation requires a body and the media
// types its body may have.
type RouteSpec struct {
	RequiredQueryParams []string
	RequiresBody        bool
	AllowedContentTypes []string
}

// OpenAPIValidator performs lightweight route-based request validation.
// Its rules come from the requestBody and parameters of the OpenAPI
// document (API-04); without a document it falls back to the registry of
// registerKnownRoutes, which never demands more than the document.
type OpenAPIValidator struct {
	routes    map[string]RouteSpec // key: "POST /api/v1/cis"
	specPath  string
	specBytes []byte
	// deprecated holds the lifecycle of the operations the specification
	// marks deprecated (API-01), keyed like routes.
	deprecated map[string]Deprecation
}

// Deprecation is the lifecycle of a deprecated operation (API-01): when it
// was deprecated, when it is switched off (at least 12 months later) and
// its successor.
type Deprecation struct {
	DeprecatedAt time.Time
	Sunset       time.Time
	Successor    string
}

// MinSunsetNotice is the notice API-01 requires between deprecation and
// sunset.
const MinSunsetNotice = 365 * 24 * time.Hour

// NewOpenAPIValidator creates a new validator with the rules of the
// OpenAPI document, or the known route rules when no document is found.
func NewOpenAPIValidator() *OpenAPIValidator {
	validator := &OpenAPIValidator{
		routes:   make(map[string]RouteSpec),
		specPath: configuredSpecPath(),
	}
	validator.loadSpec()
	if len(validator.routes) == 0 {
		validator.registerKnownRoutes()
	}
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
		// Every list accepts only cursors the server signed (API-02), even
		// a handler that would not look at the cursor.
		if err := api.ParsePagination(r).CursorError; err != nil {
			httpx.ValidationError(w, r, err.Error())
			return
		}
		v.writeDeprecation(w, r)
		spec, ok := v.lookup(r.Method, r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		// The Content-Type is checked only for a body that is sent or that
		// the operation requires: a bodiless POST such as the cookie-only
		// refresh (AUT-02) needs none.
		if shouldValidateContentType(r.Method) && (spec.RequiresBody || bodySent(r)) {
			allowed := spec.AllowedContentTypes
			if len(allowed) == 0 {
				allowed = []string{"application/json"}
			}
			if !contentTypeAllowed(r.Header.Get("Content-Type"), allowed) {
				httpx.ValidationError(w, r, "Content-Type must be "+strings.Join(allowed, " or "))
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
			// A binary body (document content) is streamed to its handler,
			// which limits and checks it.
			if !isJSONMediaType(r.Header.Get("Content-Type")) {
				if r.ContentLength == 0 {
					httpx.ValidationError(w, r, "request body is required")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				httpx.ValidationError(w, r, "unable to read request body")
				return
			}
			if len(bytes.TrimSpace(body)) == 0 {
				httpx.ValidationError(w, r, "request body is required")
				return
			}
			// A compressed body (collector ingest, DIS-05) is checked by its
			// handler after decompression.
			compressed := r.Header.Get("Content-Encoding") != "" && !strings.EqualFold(r.Header.Get("Content-Encoding"), "identity")
			if !compressed && !json.Valid(body) {
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
		deprecated, perr := ParseDeprecations(data)
		if perr != nil {
			slog.Warn("openapi: deprecations not loaded", "path", candidate, "error", perr)
		}
		v.deprecated = deprecated
		routes, rerr := ParseRouteSpecs(data)
		if rerr != nil {
			slog.Warn("openapi: validation rules not loaded, using the built-in registry", "path", candidate, "error", rerr)
			return
		}
		v.routes = routes
		return
	}
}

// specMethods are the operations of an OpenAPI path item.
var specMethods = []string{"get", "put", "post", "delete", "patch", "head", "options"}

// specParameter is an OpenAPI parameter or a reference to one.
type specParameter struct {
	Ref      string `yaml:"$ref"`
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
}

// specRequestBody is an OpenAPI requestBody or a reference to one.
type specRequestBody struct {
	Ref      string               `yaml:"$ref"`
	Required bool                 `yaml:"required"`
	Content  map[string]yaml.Node `yaml:"content"`
}

// ParseRouteSpecs derives the validation rules of every operation of an
// OpenAPI document (API-04): a required body only where the requestBody is
// required, the media types of its content, and the required query
// parameters of the path item and the operation. Operations without such a
// rule are left out.
func ParseRouteSpecs(spec []byte) (map[string]RouteSpec, error) {
	var doc struct {
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Parameters    map[string]specParameter   `yaml:"parameters"`
			RequestBodies map[string]specRequestBody `yaml:"requestBodies"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if len(doc.Paths) == 0 {
		return nil, errors.New("spec has no paths")
	}
	resolveParameter := func(p specParameter) (specParameter, error) {
		if p.Ref == "" {
			return p, nil
		}
		resolved, ok := doc.Components.Parameters[strings.TrimPrefix(p.Ref, "#/components/parameters/")]
		if !ok {
			return p, fmt.Errorf("unresolved parameter %s", p.Ref)
		}
		return resolved, nil
	}
	out := map[string]RouteSpec{}
	var errs []error
	for path, items := range doc.Paths {
		var shared []specParameter
		if node, ok := items["parameters"]; ok {
			if err := node.Decode(&shared); err != nil {
				errs = append(errs, fmt.Errorf("%s parameters: %w", path, err))
				continue
			}
		}
		for method := range items {
			if !slices.Contains(specMethods, method) {
				continue
			}
			node := items[method]
			key := routeKey(strings.ToUpper(method), path)
			var op struct {
				Parameters  []specParameter  `yaml:"parameters"`
				RequestBody *specRequestBody `yaml:"requestBody"`
			}
			if err := node.Decode(&op); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
				continue
			}
			// An operation parameter overrides the path item's parameter
			// of the same name and location.
			params := map[string]specParameter{}
			for _, raw := range append(slices.Clone(shared), op.Parameters...) {
				p, err := resolveParameter(raw)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", key, err))
					continue
				}
				params[p.In+" "+p.Name] = p
			}
			var route RouteSpec
			for _, p := range params {
				if p.In == "query" && p.Required {
					route.RequiredQueryParams = append(route.RequiredQueryParams, p.Name)
				}
			}
			slices.Sort(route.RequiredQueryParams)
			if body := op.RequestBody; body != nil {
				if body.Ref != "" {
					resolved, ok := doc.Components.RequestBodies[strings.TrimPrefix(body.Ref, "#/components/requestBodies/")]
					if !ok {
						errs = append(errs, fmt.Errorf("%s: unresolved requestBody %s", key, body.Ref))
						continue
					}
					body = &resolved
				}
				route.RequiresBody = body.Required
				for mediaType := range body.Content {
					route.AllowedContentTypes = append(route.AllowedContentTypes, mediaType)
				}
				slices.Sort(route.AllowedContentTypes)
			}
			// An operation without body and required query parameter has
			// no rule; it is not registered, so a body it does not declare
			// is left to its handler.
			if len(route.AllowedContentTypes) == 0 && len(route.RequiredQueryParams) == 0 {
				continue
			}
			out[key] = route
		}
	}
	return out, errors.Join(errs...)
}

// ParseDeprecations reads the operations marked deprecated in an OpenAPI
// document with their lifecycle: x-deprecated-at and x-sunset (dates),
// x-successor (path). A deprecated operation without x-sunset is an error,
// as is a sunset less than 12 months after the deprecation (API-01).
func ParseDeprecations(spec []byte) (map[string]Deprecation, error) {
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	out := map[string]Deprecation{}
	var errs []error
	for path, items := range doc.Paths {
		for method := range items {
			// Path items also hold parameters, summary and the like.
			if !slices.Contains(specMethods, method) {
				continue
			}
			var op struct {
				Deprecated   bool   `yaml:"deprecated"`
				DeprecatedAt string `yaml:"x-deprecated-at"`
				Sunset       string `yaml:"x-sunset"`
				Successor    string `yaml:"x-successor"`
			}
			node := items[method]
			if err := node.Decode(&op); err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", method, path, err))
				continue
			}
			if !op.Deprecated {
				continue
			}
			key := routeKey(strings.ToUpper(method), path)
			d := Deprecation{Successor: op.Successor}
			var err error
			if d.Sunset, err = time.Parse(time.DateOnly, op.Sunset); err != nil {
				errs = append(errs, fmt.Errorf("%s: x-sunset %q is not a date", key, op.Sunset))
				continue
			}
			if d.DeprecatedAt, err = time.Parse(time.DateOnly, op.DeprecatedAt); err != nil {
				errs = append(errs, fmt.Errorf("%s: x-deprecated-at %q is not a date", key, op.DeprecatedAt))
				continue
			}
			if d.Sunset.Sub(d.DeprecatedAt) < MinSunsetNotice {
				errs = append(errs, fmt.Errorf("%s: sunset %s is less than 12 months after the deprecation", key, op.Sunset))
			}
			out[key] = d
		}
	}
	return out, errors.Join(errs...)
}

// writeDeprecation sets the lifecycle headers of a deprecated operation
// (API-01): Deprecation (RFC 9745, the deprecation date), Sunset (RFC 8594)
// and a successor-version link.
func (v *OpenAPIValidator) writeDeprecation(w http.ResponseWriter, r *http.Request) {
	if len(v.deprecated) == 0 {
		return
	}
	d, ok := v.deprecated[routeKey(r.Method, cleanRoutePath(r.URL.Path))]
	if !ok {
		for key, candidate := range v.deprecated {
			parts := strings.SplitN(key, " ", 2)
			if parts[0] == r.Method && routePatternMatches(parts[1], cleanRoutePath(r.URL.Path)) {
				d, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return
	}
	w.Header().Set("Deprecation", "@"+strconv.FormatInt(d.DeprecatedAt.Unix(), 10))
	w.Header().Set("Sunset", d.Sunset.UTC().Format(http.TimeFormat))
	if d.Successor != "" {
		w.Header().Set("Link", "<"+d.Successor+">; rel=\"successor-version\"")
	}
}

// registerKnownRoutes is the fallback registry for a server without the
// OpenAPI document. It must never be stricter than the document
// (TestAuditT2ValidatorRegistryMatchesOpenAPIRequestBodies): bodiless
// operations such as the refresh, the audit verification and the webhook
// test are not registered.
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
	// Of several matching templates the one with the fewest placeholders
	// wins (/cis/search before /cis/{id}), as in the router.
	best, bestKey, found := RouteSpec{}, "", false
	for key, spec := range v.routes {
		parts := strings.SplitN(key, " ", 2)
		if len(parts) != 2 || parts[0] != method || !routePatternMatches(parts[1], cleanPath) {
			continue
		}
		if !found || moreSpecific(parts[1], strings.SplitN(bestKey, " ", 2)[1]) {
			best, bestKey, found = spec, key, true
		}
	}
	return best, found
}

// moreSpecific reports whether template a is more specific than b: the
// first segment where one is literal and the other a placeholder decides.
func moreSpecific(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := range min(len(as), len(bs)) {
		ap, bp := isPathPlaceholder(as[i]), isPathPlaceholder(bs[i])
		if ap != bp {
			return bp
		}
	}
	return a < b
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

// bodySent reports whether the request carries a body: a declared length
// or a body of unknown length (chunked).
func bodySent(r *http.Request) bool {
	return r.ContentLength != 0
}

func contentTypeAllowed(header string, allowed []string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	mediaType := parseMediaType(header)
	for _, candidate := range allowed {
		switch {
		case mediaType == candidate,
			// A binary body (application/octet-stream, */*) may have any
			// media type; its handler checks the type (document upload).
			candidate == "application/octet-stream", candidate == "*/*",
			strings.HasSuffix(candidate, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(candidate, "*")):
			return true
		}
	}
	return false
}

func parseMediaType(header string) string {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0]))
	}
	return mediaType
}

// isJSONMediaType reports whether the Content-Type names JSON
// (application/json or a +json type such as application/scim+json).
func isJSONMediaType(header string) bool {
	mediaType := parseMediaType(header)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
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
