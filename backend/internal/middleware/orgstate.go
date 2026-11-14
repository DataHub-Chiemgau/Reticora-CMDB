package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Read-only grace period of an organization deletion (TLC-06, WP-1153):
// while the deletion of the organization is requested or its purge runs,
// every write request of the organization answers 423 with the problem
// type org-pending-deletion and the end of the grace period (due_at).
// Reads, the cancel of the request, the full export, the sign-out, the
// collector heartbeat (so a collector learns it is paused) and the POST
// routes that only read stay open.

// ProblemOrgPendingDeletion is the problem type of a write refused during
// the grace period.
const ProblemOrgPendingDeletion = api.ProblemOrgPendingDeletion

// OrgStateTTL is how long a process keeps the deletion state of an
// organization.
const OrgStateTTL = 15 * time.Second

// Deletion states that make an organization read-only (migration 000324).
const (
	OrgDeletionRequested = "requested"
	OrgDeletionPurging   = "purging"
)

// OrgStateExemptRoutes are the write routes ("METHOD pattern") that stay
// open during the grace period, each with its reason.
var OrgStateExemptRoutes = map[string]string{
	"DELETE /api/v1/org/deletion":            "cancels the requested deletion",
	"POST /api/v1/org/exports":               "the full export stays possible during the grace period (E-166)",
	"POST /api/v1/auth/logout":               "sign-out",
	"POST /api/v1/collectors/{id}/heartbeat": "the collector learns that it is paused",
	// The POST routes of auditExemptions that only read.
	"POST /api/v1/audit/verify": "reads the chain",
	"POST /api/v1/search/query": "a search, reads",
	"POST /api/v1/ai/ask":       "a question to the assistant, reads",
	"POST /bff/graphql":         "the BFF refuses mutations, reads",
}

// OrgStateExempt reports whether method and route pattern stay open.
func OrgStateExempt(method, pattern string) bool {
	_, ok := OrgStateExemptRoutes[method+" "+pattern]
	return ok
}

// OrgDeletionState is the deletion state of an organization.
type OrgDeletionState struct {
	State string
	DueAt *time.Time
}

// ReadOnly reports whether the state blocks writes.
func (s OrgDeletionState) ReadOnly() bool {
	return s.State == OrgDeletionRequested || s.State == OrgDeletionPurging
}

// OrgStateReader reads the deletion state of an organization in the tenant
// context of ctx.
type OrgStateReader func(ctx context.Context, orgID string) (OrgDeletionState, error)

// OrgStateCache keeps the deletion state per organization for at most ttl
// in the process (AK2); Invalidate drops an entry at once.
type OrgStateCache struct {
	read    OrgStateReader
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]orgStateEntry
}

type orgStateEntry struct {
	state OrgDeletionState
	at    time.Time
}

// NewOrgStateCache returns a cache on read; ttl <= 0 is OrgStateTTL.
func NewOrgStateCache(read OrgStateReader, ttl time.Duration) *OrgStateCache {
	if ttl <= 0 {
		ttl = OrgStateTTL
	}
	return &OrgStateCache{read: read, ttl: ttl, now: time.Now, entries: map[string]orgStateEntry{}}
}

// WithClock replaces the clock (tests).
func (c *OrgStateCache) WithClock(now func() time.Time) *OrgStateCache {
	c.now = now
	return c
}

// Lookup returns the state of the organization, read at most ttl ago.
func (c *OrgStateCache) Lookup(ctx context.Context, orgID string) (OrgDeletionState, error) {
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[orgID]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < c.ttl {
		return e.state, nil
	}
	state, err := c.read(ctx, orgID)
	if err != nil {
		return OrgDeletionState{}, err
	}
	c.mu.Lock()
	c.entries[orgID] = orgStateEntry{state: state, at: now}
	c.mu.Unlock()
	return state, nil
}

// Invalidate drops the entry of the organization.
func (c *OrgStateCache) Invalidate(orgID string) {
	c.mu.Lock()
	delete(c.entries, orgID)
	c.mu.Unlock()
}

// orgPendingDeletionProblem is the body of a refused write.
type orgPendingDeletionProblem struct {
	api.ProblemDetail
	DueAt *time.Time `json:"due_at,omitempty"`
}

// deletionRoute is the route of request and cancel of the deletion: a
// successful write there changes the state, the cache entry of the own
// process goes at once.
const deletionRoute = "/api/v1/org/deletion"

// OrgState refuses the writes (POST, PUT, PATCH, DELETE) of an organization
// in its grace period with 423 (AK1); exempt names the routes that stay
// open (OrgStateExempt). It runs as route middleware, after the route's
// authorization, so the route pattern is known; a request without
// organization passes. A failed lookup answers 503 (fail-closed).
func OrgState(cache *OrgStateCache, exempt func(method, pattern string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isWriteMethod(r.Method) || cache == nil {
				next.ServeHTTP(w, r)
				return
			}
			orgID := tenant.FromContext(r.Context()).OrganizationID
			pattern := r.URL.Path
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				pattern = rc.RoutePattern()
			}
			if orgID == "" {
				next.ServeHTTP(w, r)
				return
			}
			if exempt == nil || !exempt(r.Method, pattern) {
				state, err := cache.Lookup(r.Context(), orgID)
				if err != nil {
					slog.ErrorContext(r.Context(), "org state: read the deletion state", "organization_id", orgID, "error", err)
					api.WriteUnavailable(w, "the state of the organization could not be read")
					return
				}
				if state.ReadOnly() {
					writeLocked(w, state)
					return
				}
				// The cached state may be older than the database: a job
				// the request enqueues is refused there (jobs.Service), and
				// the response becomes 423 whatever status the handler
				// maps the refusal to.
				ctx, signal := api.WithOrgPendingDeletionSignal(r.Context())
				guard := &orgStateGuard{ResponseWriter: w, signal: signal, cache: cache, ctx: ctx, orgID: orgID}
				r = r.WithContext(ctx)
				w = guard
			}
			if pattern == deletionRoute {
				rec := &orgStateRecorder{ResponseWriter: w, status: http.StatusOK}
				next.ServeHTTP(rec, r)
				if rec.status < 300 {
					cache.Invalidate(orgID)
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeLocked answers 423 org-pending-deletion with the end of the grace
// period.
func writeLocked(w http.ResponseWriter, state OrgDeletionState) {
	api.WriteProblemBody(w, http.StatusLocked, orgPendingDeletionProblem{
		ProblemDetail: api.ProblemDetail{Type: ProblemOrgPendingDeletion, Title: "Organization pending deletion",
			Status: http.StatusLocked, Detail: "the organization is read-only until its deletion: only the cancel and the export are possible"},
		DueAt: state.DueAt,
	})
}

// orgStateGuard replaces an error response (status 400 and above) with 423
// when a write of the request was refused for the grace period (the
// signal); the stale cache entry goes, and the body of the handler is
// dropped.
type orgStateGuard struct {
	http.ResponseWriter
	signal   *api.OrgPendingDeletionSignal
	cache    *OrgStateCache
	ctx      context.Context
	orgID    string
	wrote    bool
	replaced bool
}

func (g *orgStateGuard) WriteHeader(code int) {
	if g.wrote {
		if !g.replaced {
			g.ResponseWriter.WriteHeader(code)
		}
		return
	}
	g.wrote = true
	if code >= http.StatusBadRequest && g.signal.Refused() {
		g.replaced = true
		g.cache.Invalidate(g.orgID)
		state, err := g.cache.Lookup(g.ctx, g.orgID)
		if err != nil || !state.ReadOnly() {
			state = OrgDeletionState{State: OrgDeletionRequested}
		}
		g.Header().Del("Content-Length")
		writeLocked(g.ResponseWriter, state)
		return
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *orgStateGuard) Write(p []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	if g.replaced {
		return len(p), nil
	}
	return g.ResponseWriter.Write(p)
}

func (g *orgStateGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// orgStateRecorder keeps the status a handler wrote.
type orgStateRecorder struct {
	http.ResponseWriter
	status int
}

func (s *orgStateRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *orgStateRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
