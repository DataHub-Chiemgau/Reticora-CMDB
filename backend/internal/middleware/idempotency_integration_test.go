package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

// TestIdempotencyIsAtomicAndPrincipalBound covers WP-050 (API-04): parallel
// requests with one key execute the handler once (the others replay or get
// 409), and the key is bound to principal, method and path, so another user
// of the same organization or another route never replays the response.
func TestIdempotencyIsAtomicAndPrincipalBound(t *testing.T) {
	store := cache.NewMemoryStore()
	var calls atomic.Int32
	release := make(chan struct{})
	handler := Chain(
		AuthMiddleware,
		TenantMiddleware,
		IdempotencyWithStore(store),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`))
	}))
	post := func(subject, path string) *httptest.ResponseRecorder {
		token := testToken(t, Claims{Subject: subject, OrganizationID: "org-1", ExpiresAt: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "k-1")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// Ten parallel requests of one user: one executes, the others conflict.
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = post("alice", "/api/v1/cis").Code
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times for one key, want 1 (codes %v)", n, codes)
	}
	created, conflicts := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != 9 {
		t.Errorf("codes %v, want one 201 and nine 409", codes)
	}

	// Afterwards the same user replays the stored response.
	replay := post("alice", "/api/v1/cis")
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay: %d %v", replay.Code, replay.Header())
	}
	// Another user of the organization and another route execute anew.
	if w := post("bob", "/api/v1/cis"); w.Header().Get("Idempotency-Replayed") == "true" {
		t.Error("another principal replayed alice's response")
	}
	if w := post("alice", "/api/v1/assets"); w.Header().Get("Idempotency-Replayed") == "true" || !strings.Contains(w.Body.String(), "/api/v1/assets") {
		t.Errorf("another route replayed the response: %s", w.Body.String())
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("handler executions %d, want 3", n)
	}
}

// idempotentPost sends POST /api/v1/cis with an Idempotency-Key as alice of
// org-1 through h.
func idempotentPost(t *testing.T, h http.Handler, key string) *httptest.ResponseRecorder {
	t.Helper()
	token := testToken(t, Claims{Subject: "alice", OrganizationID: "org-1", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// problemTraceID returns the trace_id of a problem body.
func problemTraceID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("problem body %q: %v", w.Body.String(), err)
	}
	return body.TraceID
}

// TestIdempotencyStoresNo5xx covers F-206 (API-04, API-03, WP-569): a 5xx
// answer is not stored and its reservation is released, so a retry with
// the same Idempotency-Key reaches the handler (201) instead of replaying
// the error for 24 hours; this includes the 503 of AuditRoute when the
// audit intent cannot be stored (WP-504), where the handler never ran. A
// 4xx answer is still replayed verbatim: its body keeps the trace_id of the
// first request, the X-Request-ID header names the current one.
func TestIdempotencyStoresNo5xx(t *testing.T) {
	store := cache.NewMemoryStore()
	auditor := &intentStore{open: map[string]audit.RouteIntent{}}
	var calls, status atomic.Int32
	handler := Chain(
		RequestID,
		AuthMiddleware,
		TenantMiddleware,
		IdempotencyWithStore(store),
		AuditRoute(http.MethodPost, "/api/v1/cis", auditor, AuditIntent, ""),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch code := int(status.Load()); code {
		case http.StatusCreated:
			api.WriteJSON(w, code, map[string]string{"id": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"})
		default:
			api.WriteError(w, code, http.StatusText(code), "refused")
		}
	}))

	// The audit store is down: AuditRoute answers 503 with Retry-After and
	// the handler does not run. The retry after the outage reaches it.
	auditor.failIntend = true
	status.Store(http.StatusCreated)
	first := idempotentPost(t, handler, "k-audit")
	if first.Code != http.StatusServiceUnavailable || first.Header().Get("Retry-After") == "" || calls.Load() != 0 {
		t.Fatalf("audit outage: %d Retry-After %q, handler ran %d times", first.Code, first.Header().Get("Retry-After"), calls.Load())
	}
	auditor.failIntend = false
	retry := idempotentPost(t, handler, "k-audit")
	if retry.Code != http.StatusCreated || retry.Header().Get("Idempotency-Replayed") != "" || calls.Load() != 1 {
		t.Fatalf("retry after the 503: %d replayed %q, handler ran %d times: %s", retry.Code,
			retry.Header().Get("Idempotency-Replayed"), calls.Load(), retry.Body.String())
	}
	// The 201 is stored: the next retry replays it.
	if again := idempotentPost(t, handler, "k-audit"); again.Code != http.StatusCreated ||
		again.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
		t.Errorf("replay of the 201: %d replayed %q, handler ran %d times", again.Code, again.Header().Get("Idempotency-Replayed"), calls.Load())
	}

	// The handler fails with 500 and 503: neither is stored, every retry
	// reaches the handler, the last one with 201.
	calls.Store(0)
	for i, code := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusCreated} {
		status.Store(int32(code))
		w := idempotentPost(t, handler, "k-handler")
		if w.Code != code || w.Header().Get("Idempotency-Replayed") != "" || calls.Load() != int32(i+1) {
			t.Fatalf("attempt %d: %d (want %d) replayed %q, handler ran %d times", i+1, w.Code, code,
				w.Header().Get("Idempotency-Replayed"), calls.Load())
		}
	}

	// A 4xx stays stored: the retry replays it without the handler, with
	// the trace_id of the first request in the body and its own request id
	// in X-Request-ID.
	calls.Store(0)
	status.Store(http.StatusUnprocessableEntity)
	refused := idempotentPost(t, handler, "k-422")
	status.Store(http.StatusCreated)
	replay := idempotentPost(t, handler, "k-422")
	if refused.Code != http.StatusUnprocessableEntity || replay.Code != http.StatusUnprocessableEntity ||
		replay.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
		t.Fatalf("4xx replay: %d then %d replayed %q, handler ran %d times", refused.Code, replay.Code,
			replay.Header().Get("Idempotency-Replayed"), calls.Load())
	}
	firstID, replayID := refused.Header().Get("X-Request-ID"), replay.Header().Get("X-Request-ID")
	if trace := problemTraceID(t, replay); trace != firstID || trace != problemTraceID(t, refused) || replayID == "" || replayID == firstID {
		t.Errorf("4xx replay: body trace_id %q, first X-Request-ID %q, replay X-Request-ID %q", trace, firstID, replayID)
	}
}
