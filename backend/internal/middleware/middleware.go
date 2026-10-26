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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// RouteAuditor stores the route audit of write requests (AUD-07, WP-504).
type RouteAuditor interface {
	// Intend stores the intent of a write request in a committed
	// transaction of its own, before the handler runs, and sets its id.
	Intend(ctx context.Context, intent *audit.RouteIntent) error
	// Complete takes the intent (DELETE ... RETURNING) and appends entry in
	// one transaction; an intent already taken by the scheduler gets no
	// entry. On an error the intent stays for the scheduler.
	Complete(ctx context.Context, intent *audit.RouteIntent, entry *audit.Entry) error
	// Discard deletes the intent without an entry.
	Discard(ctx context.Context, intent *audit.RouteIntent) error
	// RecordRoute stores the entry of a successful write request of a route
	// with a domain recorder that wrote none.
	RecordRoute(ctx context.Context, entry *audit.Entry) error
}

// RouteSnapshotter reads the row a write route changes (AUD-07, WP-505):
// the row of spec with the key values, in the tenant scope of the request,
// and its table; nil when there is none. A route auditor that implements
// it gets the old and new values into the entries of routes without a
// domain recorder.
type RouteSnapshotter interface {
	RouteSnapshot(ctx context.Context, orgID string, spec *audit.RouteSnapshot, keys []any) (map[string]any, string, error)
}

// AuditMode is how AuditRoute audits a write route.
type AuditMode int

const (
	// AuditNone writes no entry (exempt routes); the request context still
	// reaches the domain recorders.
	AuditNone AuditMode = iota
	// AuditIntent stores an intent before the handler runs and the entry
	// after a successful response; the scheduler writes the entry of an
	// intent no response completed (gap-free, WP-504).
	AuditIntent
	// AuditDomain leaves the entry to the domain recorder, which writes it
	// in the transaction of the change: no transaction of the route audit
	// (NFR-10, E-17). Only when the recorder wrote none for a successful
	// request does the route audit write one after the response.
	AuditDomain
)

// maxAuditBody bounds how much of a request body AuditRoute reads for the
// names of the written fields.
const maxAuditBody = 1 << 20

// AuditRoute is the audit middleware of one route (AUD-07): it attaches the
// request, correlation id and source of the change to the context, so the
// domain recorders write them. For a write route in mode AuditIntent it
// stores the intent before the handler runs (no intent, no change: 503) and,
// after a successful response, deletes the intent and writes one entry in
// one transaction, unless a domain recorder already wrote one for the
// request (with old and new values); a refused request deletes the intent
// without an entry. The entry names who (actor), when, what (action,
// resource, the names of the written fields), the source and the request.
// For a route of the snapshot register (audit.RouteSnapshotFor) and an
// auditor that reads snapshots, it reads the row before the handler and
// after a successful response in the principal's scope and adds the old
// and new value per changed column ("values", AUD-07, WP-505); excluded
// columns never appear, personal ones only as a surrogate (CH16). A row
// that cannot be read before the handler refuses the request (503) like a
// missing intent.
//
// source overrides the source derived from the principal (ingest routes of
// collectors and agents).
func AuditRoute(method, pattern string, auditor RouteAuditor, mode AuditMode, source string) func(http.Handler) http.Handler {
	resourceType, action := RouteAction(method, pattern)
	route := method + " " + pattern
	fixedSource := source
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actorID, actorType, source := auditActor(r.Context())
			if fixedSource != "" {
				source = fixedSource
			}
			info := audit.RequestInfo{
				RequestID: RequestIDFromContext(r.Context()), CorrelationID: CorrelationIDFromContext(r.Context()), Source: source,
			}
			ctx := audit.WithRequestInfo(r.Context(), info)
			r = r.WithContext(ctx)
			orgID := tenant.FromContext(ctx).OrganizationID
			if mode == AuditNone || auditor == nil || orgID == "" {
				next.ServeHTTP(w, r)
				return
			}
			fields := writtenFields(r)
			var intent *audit.RouteIntent
			if mode == AuditIntent {
				intent = &audit.RouteIntent{
					OrganizationID: orgID, RequestID: info.RequestID, CorrelationID: info.CorrelationID, Route: route,
					Action: action, ResourceType: resourceType, ResourceKey: routeResourceID(r),
					ActorID: actorID, ActorType: actorType, Source: source, Fields: fields,
				}
				if err := auditor.Intend(ctx, intent); err != nil {
					slog.Error("audit intent of a write request failed; request refused", "error", err, "action", action,
						"organization_id", orgID, "request_id", info.RequestID)
					api.WriteUnavailable(w, "the audit trail is unavailable")
					return
				}
			}
			snap := routeSnapshots(r, route, mode, auditor)
			if snap != nil && !snap.spec.FromResponse() {
				var err error
				if snap.before, err = snap.read(ctx, orgID, nil); err != nil {
					slog.Error("audit snapshot of a write request failed; request refused", "error", err, "action", action,
						"organization_id", orgID, "request_id", info.RequestID)
					if derr := auditor.Discard(context.WithoutCancel(ctx), intent); derr != nil {
						slog.Error("discarding the audit intent of a write request failed", "error", derr, "action", action,
							"organization_id", orgID, "request_id", info.RequestID)
					}
					api.WriteUnavailable(w, "the audit trail is unavailable")
					return
				}
			}
			rw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			// The response is out: the audit runs even when the client has
			// gone.
			bg := context.WithoutCancel(ctx)
			if rw.status < 200 || rw.status >= 300 || audit.Recorded(ctx) {
				if intent != nil {
					if err := auditor.Discard(bg, intent); err != nil {
						slog.Error("discarding the audit intent of a write request failed", "error", err, "action", action,
							"organization_id", orgID, "request_id", info.RequestID)
					}
				}
				return
			}
			entry := routeEntry(r, rw, route, fields)
			if snap != nil {
				snap.addValues(bg, orgID, rw.body.Bytes(), &entry)
			}
			entry.OrganizationID, entry.ActorID, entry.ActorType, entry.Action = orgID, actorID, actorType, action
			entry.ResourceType, entry.Source, entry.RequestID, entry.CorrelationID = resourceType, source, info.RequestID, info.CorrelationID
			var err error
			if intent != nil {
				// On failure the intent stays: the scheduler writes the
				// entry with outcome unknown.
				err = auditor.Complete(bg, intent, &entry)
			} else {
				err = auditor.RecordRoute(bg, &entry)
			}
			if err != nil {
				slog.Error("audit entry of a write request failed", "error", err, "action", action,
					"organization_id", orgID, "request_id", info.RequestID, "intent_kept", intent != nil)
			}
		})
	}
}

// routeEntry is the entry of a successful write request: status, route,
// the addressed or created resource and the written fields.
func routeEntry(r *http.Request, rw *auditResponseWriter, route string, fields []string) audit.Entry {
	resourceID := routeResourceID(r)
	if resourceID == "" {
		resourceID = responseID(rw.body.Bytes())
	}
	changes := map[string]interface{}{"status": rw.status, "route": route}
	// Resources addressed by a key or name, or with ids that are no UUID,
	// keep it in the changes.
	if !audit.IsUUID(resourceID) {
		if resourceID != "" {
			changes["resource_key"] = resourceID
		}
		resourceID = ""
	}
	if len(fields) > 0 {
		changes["fields"] = fields
	}
	return audit.Entry{ResourceID: resourceID, Changes: changes}
}

// routeSnapshot is the snapshot of the row a write route changes.
type routeSnapshot struct {
	spec        *audit.RouteSnapshot
	snapshotter RouteSnapshotter
	r           *http.Request
	before      map[string]any
	table       string
}

// routeSnapshots returns the snapshot of a route without a domain recorder
// whose register entry names a table, when the auditor reads snapshots and
// the request carries the principal's tenant scope; else nil.
func routeSnapshots(r *http.Request, route string, mode AuditMode, auditor RouteAuditor) *routeSnapshot {
	if mode != AuditIntent {
		return nil
	}
	spec, ok := audit.RouteSnapshotFor(route)
	snapshotter, reads := auditor.(RouteSnapshotter)
	if !ok || !reads || spec.Table == "" {
		return nil
	}
	if _, scoped := database.TenantScopeFromContext(r.Context()); !scoped {
		return nil
	}
	return &routeSnapshot{spec: &spec, snapshotter: snapshotter, r: r}
}

// read reads the row; without key values there is none.
func (s *routeSnapshot) read(ctx context.Context, orgID string, response []byte) (map[string]any, error) {
	keys, ok := s.spec.KeyValues(func(from string) string {
		switch {
		case from == "$org":
			return orgID
		case from == "$user":
			return tenant.FromContext(s.r.Context()).UserID
		case strings.HasPrefix(from, "@"):
			return responseField(response, from[1:])
		}
		return chi.URLParam(s.r, from)
	})
	if !ok {
		return nil, nil
	}
	row, table, err := s.snapshotter.RouteSnapshot(ctx, orgID, s.spec, keys)
	if err != nil {
		return nil, err
	}
	if row != nil {
		s.table = table
	}
	return row, nil
}

// addValues reads the row after a successful request and adds the old and
// new value per changed column to the entry; a failed read marks the
// values unavailable.
func (s *routeSnapshot) addValues(ctx context.Context, orgID string, response []byte, entry *audit.Entry) {
	after, err := s.read(ctx, orgID, response)
	if err != nil {
		slog.Error("audit snapshot after a write request failed", "error", err, "organization_id", orgID,
			"request_id", RequestIDFromContext(s.r.Context()))
		entry.Changes["values_unavailable"] = true
		return
	}
	if s.before == nil && after == nil {
		return
	}
	entry.Changes["values"] = s.spec.ValueChanges(s.table, s.before, after)
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
func responseID(body []byte) string { return responseField(body, "id") }

// responseField is a string field of the JSON object in the response body.
func responseField(body []byte, name string) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return ""
	}
	var v string
	if json.Unmarshal(obj[name], &v) == nil {
		return v
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
