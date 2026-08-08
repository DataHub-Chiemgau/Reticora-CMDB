package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type requestIDContextKey struct{}

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

// RequestID injects a request identifier into the request context and response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

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
