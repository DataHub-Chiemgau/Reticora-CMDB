package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

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

// IdempotencyWithStore returns middleware that deduplicates POST requests
// with an Idempotency-Key for 24 hours (API-04). It runs per route after the
// route authorization, so a request without the route's permission never
// reaches a stored response. The key is bound to the principal, the method
// and the path; the first request reserves it atomically (an INCR in the
// shared store, the cache equivalent of INSERT … ON CONFLICT), and a
// concurrent duplicate gets 409 instead of executing twice.
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

			// Read and hash the body (limit to 1MB for safety)
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				httpx.Internal(w, r, "failed to read request body")
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			bodyHash := hashBody(body)

			if replayStored(w, r, store, storeKey, bodyHash) {
				return
			}

			reserved, err := store.Increment(r.Context(), storeKey+":lock", idempotencyLockTTL)
			if err != nil {
				// On cache errors, fall through and execute (fail-open):
				// losing deduplication during a cache outage is preferable
				// to rejecting every POST.
				slog.Warn("idempotency: reservation failed, executing", "error", err)
				executeAndStore(r.Context(), store, storeKey, bodyHash, w, r, next)
				return
			}
			if reserved > 1 {
				// Another request holds the key: replay its result when it
				// has finished, otherwise report the conflict.
				if replayStored(w, r, store, storeKey, bodyHash) {
					return
				}
				httpx.Conflict(w, r, "a request with this Idempotency-Key is still in progress")
				return
			}

			executeAndStore(r.Context(), store, storeKey, bodyHash, w, r, next)
		})
	}
}

// replayStored replays a stored response for the key and reports whether it
// did.
func replayStored(w http.ResponseWriter, r *http.Request, store cache.Store, storeKey, bodyHash string) bool {
	existing, found, err := store.Get(r.Context(), storeKey)
	if err != nil {
		slog.Warn("idempotency: cache lookup failed", "error", err)
		return false
	}
	if !found {
		return false
	}
	var entry idempotencyEntry
	if jsonErr := json.Unmarshal([]byte(existing), &entry); jsonErr != nil {
		slog.Warn("idempotency: failed to unmarshal cached entry", "key", storeKey, "error", jsonErr)
		return false
	}
	replayIdempotent(w, r, entry, bodyHash)
	return true
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

func executeAndStore(ctx context.Context, store cache.Store, storeKey, bodyHash string, w http.ResponseWriter, r *http.Request, next http.Handler) {
	rec := newResponseRecorder(w)
	next.ServeHTTP(rec, r)

	entry := idempotencyEntry{
		StatusCode:  rec.status,
		BodyHash:    bodyHash,
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
