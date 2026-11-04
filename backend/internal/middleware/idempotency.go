package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// idempotencyEntry stores the result of a previous POST request so that a
// retry can replay the original response verbatim.
type idempotencyEntry struct {
	StatusCode  int    `json:"status_code"`
	BodyHash    string `json:"body_hash"`
	ContentType string `json:"content_type,omitempty"`
	Body        []byte `json:"body,omitempty"`
}

const idempotencyTTL = 24 * time.Hour

// idempotencyLockTTL bounds a reservation: a request that never stores its
// result (crash, timeout) releases its key after this time.
const idempotencyLockTTL = 2 * time.Minute

// maxIdempotentRequestBody caps the request body the middleware buffers to
// hash it (API-04) and the body OpenAPIValidation reads. It is at least the
// largest route limit (maxIngestBody, 64 MiB, in discovery/devicerecord.go),
// so no route receives a truncated body; a larger body gets 413. A variable
// so tests can lower it.
var maxIdempotentRequestBody int64 = 64 << 20

// errRequestBodyTooLarge reports a body above maxIdempotentRequestBody.
var errRequestBodyTooLarge = errors.New("request body too large")

// readRequestBody reads the whole body up to maxIdempotentRequestBody and
// returns errRequestBodyTooLarge above it. It never truncates.
func readRequestBody(r *http.Request) ([]byte, error) {
	limit := maxIdempotentRequestBody
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errRequestBodyTooLarge
	}
	return body, nil
}

// writeRequestBodyTooLarge answers a body above maxIdempotentRequestBody.
func writeRequestBodyTooLarge(w http.ResponseWriter) {
	api.WriteError(w, http.StatusRequestEntityTooLarge, "Payload Too Large",
		fmt.Sprintf("request body exceeds %d bytes", maxIdempotentRequestBody))
}

// IdempotencyWithStore returns middleware that deduplicates POST requests
// with an Idempotency-Key for 24 hours (API-04). It runs per route after the
// route authorization, so a request without the route's permission never
// reaches a stored response. The key is bound to the principal, the method
// and the path; the first request reserves it atomically (an INCR in the
// shared store, the cache equivalent of INSERT … ON CONFLICT), and a
// concurrent duplicate gets 409 instead of executing twice. A 5xx response
// is not stored (executeAndStore).
//
// A JSON body (or one without Content-Type) is read and hashed before the
// handler. Any other body, such as the content of POST
// /api/v1/documents/{id}/versions, is neither read ahead nor cut (SEC-09,
// WP-268): the handler reads it from the connection while it is hashed
// (io.TeeReader), and a replay hashes it without a buffer.
func IdempotencyWithStore(store cache.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			storeKey := "idempotency:" + idempotencyScope(r) + ":" + key

			if !bufferedBody(r) {
				serveStreamed(store, storeKey, w, r, next)
				return
			}

			// Buffer and hash the whole body: the replay decision falls
			// before the handler. The handler receives exactly the hashed
			// body; a body above the cap gets 413 without reserving the
			// key.
			body, err := readRequestBody(r)
			if errors.Is(err, errRequestBodyTooLarge) {
				writeRequestBodyTooLarge(w)
				return
			}
			if err != nil {
				httpx.Internal(w, r, "failed to read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			bodyHash := hashBody(body)
			hashed := func() (string, bool) { return bodyHash, true }

			if entry, ok := lookupStored(r, store, storeKey); ok {
				replayIdempotent(w, r, entry, bodyHash)
				return
			}

			reserved, err := store.Increment(r.Context(), storeKey+":lock", idempotencyLockTTL)
			if err != nil {
				// On cache errors, fall through and execute (fail-open):
				// losing deduplication during a cache outage is preferable
				// to rejecting every POST.
				slog.Warn("idempotency: reservation failed, executing", "error", err)
				executeAndStore(r.Context(), store, storeKey, hashed, w, r, next)
				return
			}
			if reserved > 1 {
				// Another request holds the key: replay its result when it
				// has finished, otherwise report the conflict.
				if entry, ok := lookupStored(r, store, storeKey); ok {
					replayIdempotent(w, r, entry, bodyHash)
					return
				}
				httpx.Conflict(w, r, "a request with this Idempotency-Key is still in progress")
				return
			}

			executeAndStore(r.Context(), store, storeKey, hashed, w, r, next)
		})
	}
}

// bufferedBody reports whether the body is read ahead: a JSON body (also a
// +json type) or one without Content-Type, as before WP-268.
func bufferedBody(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.TrimSpace(ct) == "" || isJSONMediaType(ct)
}

// serveStreamed is IdempotencyWithStore for a body that is not read ahead:
// a stored response is replayed after the body is hashed without a buffer;
// otherwise the handler reads the body while it is hashed, and the rest the
// handler left is hashed afterwards (executeAndStore).
func serveStreamed(store cache.Store, storeKey string, w http.ResponseWriter, r *http.Request, next http.Handler) {
	replay := func(entry idempotencyEntry) {
		h := sha256.New()
		if _, err := io.Copy(h, r.Body); err != nil {
			httpx.Internal(w, r, "failed to read request body")
			return
		}
		replayIdempotent(w, r, entry, hex.EncodeToString(h.Sum(nil)))
	}
	if entry, ok := lookupStored(r, store, storeKey); ok {
		replay(entry)
		return
	}

	body := r.Body
	h := sha256.New()
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(body, h), body}
	// hashed reads what the handler left (at most the cap) and returns the
	// hash of the whole body; a body above the cap or a read error stores
	// nothing.
	hashed := func() (string, bool) {
		n, err := io.Copy(h, io.LimitReader(body, maxIdempotentRequestBody+1))
		if err != nil || n > maxIdempotentRequestBody {
			return "", false
		}
		return hex.EncodeToString(h.Sum(nil)), true
	}

	reserved, err := store.Increment(r.Context(), storeKey+":lock", idempotencyLockTTL)
	if err != nil {
		slog.Warn("idempotency: reservation failed, executing", "error", err)
		executeAndStore(r.Context(), store, storeKey, hashed, w, r, next)
		return
	}
	if reserved > 1 {
		if entry, ok := lookupStored(r, store, storeKey); ok {
			r.Body = body
			replay(entry)
			return
		}
		httpx.Conflict(w, r, "a request with this Idempotency-Key is still in progress")
		return
	}
	executeAndStore(r.Context(), store, storeKey, hashed, w, r, next)
}

// lookupStored returns the stored response of the key, if any.
func lookupStored(r *http.Request, store cache.Store, storeKey string) (idempotencyEntry, bool) {
	var entry idempotencyEntry
	existing, found, err := store.Get(r.Context(), storeKey)
	if err != nil {
		slog.Warn("idempotency: cache lookup failed", "error", err)
		return entry, false
	}
	if !found {
		return entry, false
	}
	if jsonErr := json.Unmarshal([]byte(existing), &entry); jsonErr != nil {
		slog.Warn("idempotency: failed to unmarshal cached entry", "key", storeKey, "error", jsonErr)
		return entry, false
	}
	return entry, true
}

// idempotencyScope binds a key to the authenticated principal (organization,
// principal type and subject), the method and the path (API-04), so neither
// another user of the organization nor another route can replay a response.
// Client-supplied headers are never consulted. Requests without a principal
// (public endpoints) share an unscoped namespace per path.
func idempotencyScope(r *http.Request) string {
	route := r.Method + ":" + r.URL.Path
	if principal, ok := PrincipalFromContext(r.Context()); ok && principal.OrganizationID != "" {
		return principal.OrganizationID + ":" + string(principal.Type) + ":" + principal.Subject + ":" + route
	}
	return "public:" + route
}

func replayIdempotent(w http.ResponseWriter, r *http.Request, entry idempotencyEntry, bodyHash string) {
	if entry.BodyHash != bodyHash {
		httpx.IdempotencyMismatch(w, r, "request body differs from original request with same Idempotency-Key")
		return
	}
	if entry.ContentType != "" {
		w.Header().Set("Content-Type", entry.ContentType)
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(entry.StatusCode)
	if len(entry.Body) > 0 {
		_, _ = w.Write(entry.Body)
	}
}

// executeAndStore runs the request and stores its response for replay. A
// 5xx response is not stored and the reservation is released (F-206): the
// failure is transient (a dependency outage, the 503 of AuditRoute without
// a stored audit intent), so a retry with the same key reaches the handler
// again instead of replaying the error for 24 hours. Any other response is
// replayed verbatim, a 4xx body with the trace_id of the first request.
// bodyHash returns the hash of the request body once the handler has run;
// without one (a streamed body above the cap) nothing is stored either.
func executeAndStore(ctx context.Context, store cache.Store, storeKey string, bodyHash func() (string, bool), w http.ResponseWriter,
	r *http.Request, next http.Handler) {
	rec := newResponseRecorder(w)
	next.ServeHTTP(rec, r)

	hash, hashed := "", false
	if rec.status < http.StatusInternalServerError {
		hash, hashed = bodyHash()
	}
	if !hashed {
		if err := store.Delete(context.WithoutCancel(ctx), storeKey+":lock"); err != nil {
			// The reservation expires after idempotencyLockTTL; until then
			// a retry gets 409.
			slog.Warn("idempotency: failed to release reservation", "key", storeKey, "error", err)
		}
		return
	}

	entry := idempotencyEntry{
		StatusCode:  rec.status,
		BodyHash:    hash,
		ContentType: rec.Header().Get("Content-Type"),
		Body:        rec.body,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("idempotency: failed to marshal entry", "key", storeKey, "error", err)
		return
	}
	if err := store.Set(ctx, storeKey, string(data), idempotencyTTL); err != nil {
		slog.Warn("idempotency: failed to store entry", "key", storeKey, "error", err)
	}
}

func hashBody(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// maxIdempotentBodySize caps the buffered response body so a large payload
// cannot exhaust memory or exceed the cache's value size limit. Responses
// above the cap are stored without a body (status-only replay).
const maxIdempotentBodySize = 1 << 20 // 1 MiB

type responseRecorder struct {
	http.ResponseWriter
	status   int
	body     []byte
	overflow bool
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if !r.overflow {
		if len(r.body)+len(data) <= maxIdempotentBodySize {
			r.body = append(r.body, data...)
		} else {
			// Overflow: drop the buffered body so the entry degrades to a
			// status-only replay instead of storing a partial tail.
			r.body = nil
			r.overflow = true
		}
	}
	return r.ResponseWriter.Write(data)
}

// Unwrap gives http.ResponseController the underlying writer, so a handler
// behind the middleware can extend its deadlines (WP-268).
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
