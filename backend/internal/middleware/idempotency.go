package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// idempotencyEntry stores the result of a previous POST request.
type idempotencyEntry struct {
	StatusCode int
	BodyHash   string
	ExpiresAt  time.Time
}

// idempotencyStore is a simple in-memory store for development.
// In production, this should be backed by Redis with 24h TTL per org.
type idempotencyStore struct {
	mu      sync.RWMutex
	entries map[string]idempotencyEntry // key: org_id:idempotency_key
}

var globalIdempotencyStore = &idempotencyStore{
	entries: make(map[string]idempotencyEntry),
}

// Idempotency middleware handles POST request deduplication via Idempotency-Key header.
// Stores status code and body hash per org for 24h. Same key + same body returns
// cached response; same key + different body returns 409 idempotency-mismatch.
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

		if exists && time.Now().Before(entry.ExpiresAt) {
			if entry.BodyHash != bodyHash {
				httpx.IdempotencyMismatch(w, r, "request body differs from original request with same Idempotency-Key")
				return
			}
			// Return cached status (body reconstruction would need full response caching)
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
			ExpiresAt:  time.Now().Add(24 * time.Hour),
		}
		globalIdempotencyStore.mu.Unlock()
	})
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
