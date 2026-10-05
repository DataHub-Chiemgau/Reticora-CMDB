package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
