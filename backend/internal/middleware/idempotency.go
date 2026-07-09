package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// idempotencyEntry stores the result of a previous POST request.
type idempotencyEntry struct {
	StatusCode int    `json:"status_code"`
	BodyHash   string `json:"body_hash"`
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

// Idempotency middleware handles POST request deduplication via Idempotency-Key header.
// Uses in-memory store; for production use IdempotencyWithStore.
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

		orgID := r.Header.Get("X-Organization-ID")
		storeKey := orgID + ":" + key

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
			if entry.BodyHash != bodyHash {
				httpx.IdempotencyMismatch(w, r, "request body differs from original request with same Idempotency-Key")
				return
			}
			w.WriteHeader(entry.StatusCode)
			return
		}

		// Capture response
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Store result
		globalIdempotencyStore.mu.Lock()
		globalIdempotencyStore.entries[storeKey] = idempotencyEntry{
			StatusCode: rec.status,
			BodyHash:   bodyHash,
		}
		globalIdempotencyStore.mu.Unlock()
	})
}

// IdempotencyWithStore returns middleware that uses a cache.Store (Redis) for
// distributed idempotency tracking with 24h TTL per org.
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

			orgID := r.Header.Get("X-Organization-ID")
			storeKey := "idempotency:" + orgID + ":" + key

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
				// On Redis errors, fall through (fail-open)
				executeAndStore(r.Context(), store, storeKey, bodyHash, w, r, next)
				return
			}

			if found {
				var entry idempotencyEntry
				if jsonErr := json.Unmarshal([]byte(existing), &entry); jsonErr == nil {
					if entry.BodyHash != bodyHash {
						httpx.IdempotencyMismatch(w, r, "request body differs from original request with same Idempotency-Key")
						return
					}
					w.WriteHeader(entry.StatusCode)
					return
				}
			}

			executeAndStore(r.Context(), store, storeKey, bodyHash, w, r, next)
		})
	}
}

func executeAndStore(ctx context.Context, store cache.Store, storeKey, bodyHash string, w http.ResponseWriter, r *http.Request, next http.Handler) {
	rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(rec, r)

	entry := idempotencyEntry{
		StatusCode: rec.status,
		BodyHash:   bodyHash,
	}
	data, _ := json.Marshal(entry)
	_ = store.Set(ctx, storeKey, string(data), idempotencyTTL)
}

func hashBody(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
