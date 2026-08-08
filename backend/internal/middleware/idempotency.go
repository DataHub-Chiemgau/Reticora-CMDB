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
	"sync"
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

// idempotencyMemStore is a simple in-memory store for development.
type idempotencyMemStore struct {
	mu      sync.RWMutex
	entries map[string]idempotencyEntry // key: org_id:idempotency_key
}

var globalIdempotencyStore = &idempotencyMemStore{
	entries: make(map[string]idempotencyEntry),
}

const idempotencyTTL = 24 * time.Hour

// Idempotency middleware handles POST request deduplication via the
// Idempotency-Key header. Entries are scoped to the authenticated tenant so
// keys can never collide or be poisoned across organizations. Uses an
// in-memory store; for production use IdempotencyWithStore.
func Idempotency(next http.Handler) http.Handler {
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

		// Check for existing entry
		globalIdempotencyStore.mu.RLock()
		entry, exists := globalIdempotencyStore.entries[storeKey]
		globalIdempotencyStore.mu.RUnlock()

		if exists {
			replayIdempotent(w, r, entry, bodyHash)
			return
		}

		// Capture response
		rec := newResponseRecorder(w)
		next.ServeHTTP(rec, r)

		// Store result
		globalIdempotencyStore.mu.Lock()
		globalIdempotencyStore.entries[storeKey] = idempotencyEntry{
			StatusCode:  rec.status,
			BodyHash:    bodyHash,
			ContentType: rec.Header().Get("Content-Type"),
			Body:        rec.body,
		}
		globalIdempotencyStore.mu.Unlock()
	})
}

// IdempotencyWithStore returns middleware that uses a cache.Store (Redis) for
// distributed idempotency tracking with 24h TTL per tenant.
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

			// Check for existing entry in Redis
			existing, found, err := store.Get(r.Context(), storeKey)
			if err != nil {
				// On cache errors, fall through and re-execute (fail-open).
				// Losing deduplication during a cache outage is preferable to
				// rejecting every POST; the tenant-scoped key still prevents
				// cross-tenant poisoning once the store recovers.
				slog.Warn("idempotency: cache lookup failed, re-executing", "error", err)
				executeAndStore(r.Context(), store, storeKey, bodyHash, w, r, next)
				return
			}

			if found {
				var entry idempotencyEntry
				if jsonErr := json.Unmarshal([]byte(existing), &entry); jsonErr != nil {
					slog.Warn("idempotency: failed to unmarshal cached entry, re-executing", "key", storeKey, "error", jsonErr)
				} else {
					replayIdempotent(w, r, entry, bodyHash)
					return
				}
			}

			executeAndStore(r.Context(), store, storeKey, bodyHash, w, r, next)
		})
	}
}

// idempotencyScope derives the deduplication scope from the authenticated
// principal. Client-supplied headers are never consulted: two tenants using
// the same Idempotency-Key must never see each other's responses. Requests
// without a principal (public endpoints) share an unscoped namespace so a
// caller can only replay its own key, never a tenant's.
func idempotencyScope(r *http.Request) string {
	if principal, ok := PrincipalFromContext(r.Context()); ok && principal.OrganizationID != "" {
		return principal.OrganizationID
	}
	return "public"
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
