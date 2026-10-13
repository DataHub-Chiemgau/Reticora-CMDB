package middleware

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type requestIDContextKey struct{}

type correlationIDContextKey struct{}

// correlationIDPattern bounds a caller-supplied correlation id.
var correlationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Chain composes middleware from left to right, so the first middleware becomes
// the outermost wrapper.
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}

// RequestID injects a request identifier into the request context and
// response, and the correlation id (AUD-07): the caller's X-Correlation-ID
// when it is well-formed, otherwise the request id.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		correlationID := r.Header.Get("X-Correlation-ID")
		if !correlationIDPattern.MatchString(correlationID) {
			correlationID = requestID
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Correlation-ID", correlationID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		ctx = context.WithValue(ctx, correlationIDContextKey{}, correlationID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CorrelationIDFromContext extracts the correlation id set by RequestID.
func CorrelationIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDContextKey{}).(string)
	return id
}

// RouteAuditor stores the audit entry of a successful write request.
type RouteAuditor interface {
	RecordRoute(ctx context.Context, entry *audit.Entry) error
}

// maxAuditBody bounds how much of a request body AuditRoute reads for the
// names of the written fields.
const maxAuditBody = 1 << 20

// AuditRoute is the audit middleware of one route (AUD-07): it attaches the
// request, correlation id and source of the change to the context, so the
// domain recorders write them; for a write route with record set it stores
// one entry after a successful response unless a domain recorder already
// wrote one for the request (with old and new values). The entry names who
// (actor), when, what (action, resource, the names of the written fields),
// the source and the request; values stay with the domain history, so the
// append-only log holds no personal data (CH16).
//
// source overrides the source derived from the principal (ingest routes of
// collectors and agents).
func AuditRoute(method, pattern string, auditor RouteAuditor, record bool, source string) func(http.Handler) http.Handler {
	resourceType, action := RouteAction(method, pattern)
	fixedSource := source
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actorID, actorType, source := auditActor(r.Context())
			if fixedSource != "" {
				source = fixedSource
			}
			ctx := audit.WithRequestInfo(r.Context(), audit.RequestInfo{
				RequestID: RequestIDFromContext(r.Context()), CorrelationID: CorrelationIDFromContext(r.Context()), Source: source,
			})
			r = r.WithContext(ctx)
			if !record || auditor == nil {
				next.ServeHTTP(w, r)
				return
			}
			fields := writtenFields(r)
			rw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			orgID := tenant.FromContext(ctx).OrganizationID
			if rw.status < 200 || rw.status >= 300 || orgID == "" || audit.Recorded(ctx) {
				return
			}
			resourceID := routeResourceID(r)
			if resourceID == "" {
				resourceID = responseID(rw.body.Bytes())
			}
			changes := map[string]interface{}{"status": rw.status, "route": method + " " + pattern}
			// Resources addressed by a key or name, or with ids that are no
			// UUID, keep it in the changes.
			if !audit.IsUUID(resourceID) {
				if resourceID != "" {
					changes["resource_key"] = resourceID
				}
				resourceID = ""
			}
			if len(fields) > 0 {
				changes["fields"] = fields
			}
			info, _ := audit.RequestInfoFrom(ctx)
			entry := audit.Entry{
				OrganizationID: orgID, ActorID: actorID, ActorType: actorType, Action: action,
				ResourceType: resourceType, ResourceID: resourceID, Changes: changes,
				Source: info.Source, RequestID: info.RequestID, CorrelationID: info.CorrelationID,
			}
			// The response is out: the entry is written even when the
			// client has gone.
			if err := auditor.RecordRoute(context.WithoutCancel(ctx), &entry); err != nil {
				slog.Error("audit entry of a write request failed", "error", err, "action", action,
					"organization_id", orgID, "request_id", info.RequestID)
			}
		})
	}
}

// RouteAction derives the audited resource type and action of a route:
// /api/v1/<resource>[/{id}[/<sub>]] gives <resource>.create, .update,
// .delete or .<sub>.
func RouteAction(method, pattern string) (resourceType, action string) {
	path := strings.TrimPrefix(strings.TrimPrefix(pattern, "/api/v1/"), "/scim/v2/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	resourceType = segments[0]
	verb := ""
	for _, seg := range segments[1:] {
		if seg != "" && !strings.HasPrefix(seg, "{") {
			verb = seg
		}
	}
	if verb == "" {
		switch method {
		case http.MethodPost:
			verb = "create"
		case http.MethodPut, http.MethodPatch:
			verb = "update"
		case http.MethodDelete:
			verb = "delete"
		}
	} else if method == http.MethodDelete {
		verb += ".delete"
	}
	return resourceType, resourceType + "." + verb
}

// auditActor names the principal of the request and the source of its
// changes: a user session is the UI, an API key the API.
func auditActor(ctx context.Context) (actorID, actorType, source string) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return "", "system", audit.SourceSystem
	}
	if audit.IsUUID(principal.Subject) {
		actorID = principal.Subject
	}
	if principal.Type == identity.PrincipalTypeAPIKey {
		return actorID, "api_key", audit.SourceAPI
	}
	return actorID, "user", audit.SourceUI
}

// writtenFields lists the top-level keys of a JSON request body.
func writtenFields(r *http.Request) []string {
	if r.Body == nil || !strings.Contains(r.Header.Get("Content-Type"), "json") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBody))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
	if err != nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return nil
	}
	fields := make([]string, 0, len(obj))
	for k := range obj {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return fields
}

// routeResourceID is the id, key or name the route addresses, if any.
func routeResourceID(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		for i, key := range rctx.URLParams.Keys {
			if key == "id" || key == "key" || key == "name" || strings.HasSuffix(key, "Id") {
				return rctx.URLParams.Values[i]
			}
		}
	}
	return ""
}

// responseID is the id of a created resource in the response body.
func responseID(body []byte) string {
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &obj) == nil {
		return obj.ID
	}
	return ""
}

// auditResponseWriter keeps the status and the start of the body.
type auditResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(data []byte) (int, error) {
	w.wroteHeader = true
	if room := 64<<10 - w.body.Len(); room > 0 {
		w.body.Write(data[:min(len(data), room)])
	}
	return w.ResponseWriter.Write(data)
}

// Flush passes streaming responses through.
func (w *auditResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap gives http.ResponseController the underlying writer.
func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Logger emits structured request logs.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		tenantInfo := tenantFromHeaderSafe(r)
		requestID := w.Header().Get("X-Request-ID")
		slog.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"bytes", rw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", requestID,
			"request_id", requestID,
			"organization_id", tenantInfo.OrganizationID,
			"org_id", tenantInfo.OrganizationID,
			"client_id", tenantInfo.ClientID,
			"user_id", tenantInfo.UserID,
			"remote_addr", r.RemoteAddr,
		)
	})
}

// Recovery recovers from panics and returns a structured 500 response.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("request panic",
					"panic", rec,
					"request_id", w.Header().Get("X-Request-ID"),
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequestIDFromContext extracts the request ID assigned by RequestID middleware.
func RequestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf)
}

func tenantFromHeaderSafe(r *http.Request) struct {
	OrganizationID string
	ClientID       string
	UserID         string
} {
	// Identity fields are sourced from the authenticated principal only; the
	// request headers are client-controlled and must not be trusted.
	var info struct {
		OrganizationID string
		ClientID       string
		UserID         string
	}
	if tenantInfo := tenant.FromContext(r.Context()); tenantInfo.OrganizationID != "" {
		info.OrganizationID = tenantInfo.OrganizationID
		info.ClientID = tenantInfo.ClientID
		info.UserID = tenantInfo.UserID
		return info
	}
	if principal, ok := PrincipalFromContext(r.Context()); ok {
		info.OrganizationID = principal.OrganizationID
		info.ClientID = principal.ClientScope
		info.UserID = principal.Subject
	}
	return info
}
