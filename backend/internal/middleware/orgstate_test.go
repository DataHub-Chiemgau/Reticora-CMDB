package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// WP-1153 (TLC-06): during the grace period of a deletion the writes of
// the organization answer 423 org-pending-deletion with due_at; reads, the
// exempt routes and other organizations pass; the state is kept for at
// most 15 s and a successful request or cancel drops it at once.
//
// Retest:
//   cd backend && go test ./internal/middleware/ -run TestOrgState

const orgA = "0a000000-0000-4000-8000-000000001153"

type stateSource struct {
	state OrgDeletionState
	reads atomic.Int32
}

func (s *stateSource) read(_ context.Context, orgID string) (OrgDeletionState, error) {
	s.reads.Add(1)
	if orgID != orgA {
		return OrgDeletionState{State: "none"}, nil
	}
	return s.state, nil
}

// orgStateRouter mounts a few routes behind OrgState like the route
// middleware of the API.
func orgStateRouter(cache *OrgStateCache) http.Handler {
	r := chi.NewRouter()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := OrgState(cache, OrgStateExempt)
	r.With(mw).Get("/api/v1/cis", ok)
	r.With(mw).Post("/api/v1/cis", ok)
	r.With(mw).Patch("/api/v1/cis/{id}", ok)
	r.With(mw).Delete("/api/v1/cis/{id}", ok)
	r.With(mw).Post("/api/v1/ingest/bulk", ok)
	r.With(mw).Post("/api/v1/search/query", ok)
	r.With(mw).Post("/bff/graphql", ok)
	r.With(mw).Post("/api/v1/org/exports", ok)
	r.With(mw).Post("/api/v1/collectors/{id}/heartbeat", ok)
	r.With(mw).Post("/api/v1/org/deletion", ok)
	r.With(mw).Delete("/api/v1/org/deletion", ok)
	return r
}

func call(h http.Handler, org, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func pending() *stateSource {
	due := time.Date(2026, 12, 13, 10, 0, 0, 0, time.UTC)
	return &stateSource{state: OrgDeletionState{State: OrgDeletionRequested, DueAt: &due}}
}

func TestOrgStateBlocksWrites(t *testing.T) {
	src := pending()
	h := orgStateRouter(NewOrgStateCache(src.read, 0))
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/cis"}, {http.MethodPatch, "/api/v1/cis/x"}, {http.MethodDelete, "/api/v1/cis/x"},
		{http.MethodPost, "/api/v1/ingest/bulk"}, {http.MethodPost, "/api/v1/org/deletion"},
	} {
		w := call(h, orgA, c.method, c.path)
		if w.Code != http.StatusLocked {
			t.Fatalf("%s %s: %d", c.method, c.path, w.Code)
		}
		var body struct {
			Type  string `json:"type"`
			DueAt string `json:"due_at"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Type != ProblemOrgPendingDeletion || body.DueAt != "2026-12-13T10:00:00Z" {
			t.Fatalf("%s %s: %s", c.method, c.path, w.Body.String())
		}
	}
	src.state.State = OrgDeletionPurging
	if w := call(freshOrgStateRouter(src), orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusLocked {
		t.Fatalf("purging: %d", w.Code)
	}
}

// freshOrgStateRouter builds a router with a fresh cache.
func freshOrgStateRouter(src *stateSource) http.Handler {
	return orgStateRouter(NewOrgStateCache(src.read, 0))
}

func TestOrgStateAllowsReadsAndExemptions(t *testing.T) {
	src := pending()
	h := orgStateRouter(NewOrgStateCache(src.read, 0))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/cis"}, {http.MethodPost, "/api/v1/search/query"}, {http.MethodPost, "/bff/graphql"},
		{http.MethodPost, "/api/v1/org/exports"}, {http.MethodPost, "/api/v1/collectors/c1/heartbeat"},
		{http.MethodDelete, "/api/v1/org/deletion"},
	} {
		if w := call(h, orgA, c.method, c.path); w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	// Another organization and a request without one write.
	if w := call(h, "0b000000-0000-4000-8000-000000001153", http.MethodPost, "/api/v1/cis"); w.Code != http.StatusOK {
		t.Fatalf("other organization: %d", w.Code)
	}
	if w := call(h, "", http.MethodPost, "/api/v1/cis"); w.Code != http.StatusOK {
		t.Fatalf("no organization: %d", w.Code)
	}
	// A failed read refuses the write (fail-closed).
	failing := orgStateRouter(NewOrgStateCache(func(context.Context, string) (OrgDeletionState, error) {
		return OrgDeletionState{}, context.DeadlineExceeded
	}, 0))
	if w := call(failing, orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed read: %d", w.Code)
	}
}

func TestOrgStateCacheExpires(t *testing.T) {
	src := &stateSource{state: OrgDeletionState{State: "none"}}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	cache := NewOrgStateCache(src.read, 0).WithClock(func() time.Time { return now })
	h := orgStateRouter(cache)
	if w := call(h, orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	// Another process requested the deletion: this one notices within 15 s.
	src.state = pending().state
	now = now.Add(10 * time.Second)
	if w := call(h, orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusOK {
		t.Fatalf("within the TTL: %d", w.Code)
	}
	now = now.Add(6 * time.Second)
	if w := call(h, orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusLocked {
		t.Fatalf("after the TTL: %d", w.Code)
	}
	if src.reads.Load() != 2 {
		t.Fatalf("%d reads", src.reads.Load())
	}
	// The cancel of this process drops the entry at once.
	src.state = OrgDeletionState{State: "none"}
	if w := call(h, orgA, http.MethodDelete, "/api/v1/org/deletion"); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if w := call(h, orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusOK {
		t.Fatalf("after the cancel: %d", w.Code)
	}
	// And a request of this process as well.
	src.state = pending().state
	if w := call(h, orgA, http.MethodPost, "/api/v1/org/deletion"); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if w := call(h, orgA, http.MethodPost, "/api/v1/cis"); w.Code != http.StatusLocked {
		t.Fatalf("after the request: %d", w.Code)
	}
}

// A process whose cached state is older than the database lets a write
// through; a job the handler enqueues is refused there (the signal), and
// the response becomes 423 with due_at whatever status the handler maps
// the refusal to. The stale entry goes. Without the signal an error
// response stays as the handler wrote it.
func TestOrgStateStaleCacheJobRefusalIs423(t *testing.T) {
	src := &stateSource{state: OrgDeletionState{State: "none"}}
	cache := NewOrgStateCache(src.read, time.Hour)
	refuse := atomic.Bool{}
	r := chi.NewRouter()
	r.With(OrgState(cache, OrgStateExempt)).Post("/api/v1/imports", func(w http.ResponseWriter, req *http.Request) {
		if refuse.Load() {
			// As jobs.Service.Enqueue does before it returns the error.
			api.SignalOrgPendingDeletion(req.Context())
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "the request could not be completed")
	})
	// The cache holds "none".
	if w := call(r, orgA, http.MethodPost, "/api/v1/imports"); w.Code != http.StatusInternalServerError {
		t.Fatalf("without refusal: %d", w.Code)
	}
	// The deletion is requested in another process: the cache is stale.
	src.state = pending().state
	refuse.Store(true)
	w := call(r, orgA, http.MethodPost, "/api/v1/imports")
	var body struct {
		Type  string     `json:"type"`
		DueAt *time.Time `json:"due_at"`
	}
	if w.Code != http.StatusLocked || json.Unmarshal(w.Body.Bytes(), &body) != nil ||
		body.Type != ProblemOrgPendingDeletion || body.DueAt == nil {
		t.Fatalf("stale cache with a refused job: %d %s", w.Code, w.Body.String())
	}
	// The entry was renewed: the next write is refused by the middleware.
	refuse.Store(false)
	if w := call(r, orgA, http.MethodPost, "/api/v1/imports"); w.Code != http.StatusLocked {
		t.Fatalf("after the renewal: %d", w.Code)
	}
}
