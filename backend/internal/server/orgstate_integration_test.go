package server_test

// WP-1153 (TLC-06): during the grace period of an organization deletion the
// organization is read-only. Against PostgreSQL with the complete router:
// every write route of route_permissions.txt that is not exempt answers 423
// org-pending-deletion, the exempt ones and GET do not; after the cancel no
// route answers 423. A job of the organization is refused (the import route
// with 423, jobs.Service.Enqueue with ErrOrgPendingDeletion, a job route
// whose cache is stale with 423 too), the full
// export is not; the collector status run pauses the collectors and sets
// them offline after the cancel; the NATS edge refuses the collectors.
//
// Retest:
//   cd backend && go test -p 1 ./internal/server/ -run TestOrgPendingDeletion

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

type orgStateRouter struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	router http.Handler
	cache  *middleware.OrgStateCache
	user   string
	enc    *crypto.EnvelopeEncryptor
}

func newOrgStateRouter(t *testing.T) *orgStateRouter {
	t.Helper()
	return newOrgStateRouterWith(t, false)
}

// newOrgStateRouterWith builds the router; stale makes its cache answer
// "none" whatever the database holds, like the cache of another process
// that read the state before the request (AK2).
func newOrgStateRouterWith(t *testing.T, stale bool) *orgStateRouter {
	t.Helper()
	f := scopetest.Seed(t, "53")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	staleReads := 0
	cache := middleware.NewOrgStateCache(func(ctx context.Context, orgID string) (middleware.OrgDeletionState, error) {
		state, due, rerr := tenantapi.ReadDeletionState(ctx, f.App, orgID)
		// The first read of the request is the stale one; the renewal
		// after a refused job reads the database.
		if stale && staleReads%2 == 0 {
			staleReads++
			return middleware.OrgDeletionState{State: tenantapi.DeletionNone}, rerr
		}
		staleReads++
		return middleware.OrgDeletionState{State: state, DueAt: due}, rerr
	}, 0)
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "orgstate", Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, enc),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      egress.Options{AllowPrivate: true},
		// As cmd/server/main.go: before validation and idempotency.
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OrgState(cache, middleware.OrgStateExempt)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &orgStateRouter{t: t, f: f, mux: mux, router: middleware.TenantMiddleware(mux), cache: cache,
		user: f.AppUser(t, f.OrgA, "orgstate-caller"), enc: enc}
}

func (r *orgStateRouter) call(method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	principal := identity.Principal{Subject: r.user, OrganizationID: r.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: identity.AllPermissions(), Credential: "orgstate"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	r.router.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func (r *orgStateRouter) exec(query string, args ...any) {
	r.t.Helper()
	if _, err := r.f.Admin.Exec(context.Background(), query, args...); err != nil {
		r.t.Fatalf("%s: %v", query, err)
	}
}

// request puts organization A into the grace period.
func (r *orgStateRouter) request() {
	r.exec(`UPDATE organization SET deletion_state = 'requested', deletion_requested_at = now(),
		deletion_due_at = now() + interval '30 days' WHERE id = $1`, r.f.OrgA)
	r.cache.Invalidate(r.f.OrgA)
}

// cancel ends the grace period as the cancel does.
func (r *orgStateRouter) cancel() {
	r.exec(`UPDATE organization SET deletion_state = 'none', deletion_requested_at = NULL, deletion_due_at = NULL WHERE id = $1`, r.f.OrgA)
	r.cache.Invalidate(r.f.OrgA)
}

var routeParam = regexp.MustCompile(`\{[^}]+\}`)

// writeRoutes are the write routes of route_permissions.txt the router
// registers behind its authorization (not public, not the operator path).
func (r *orgStateRouter) writeRoutes() [][2]string {
	r.t.Helper()
	registered := map[string]bool{}
	if err := chi.Walk(r.mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
	file, err := os.Open("testdata/route_permissions.txt")
	if err != nil {
		r.t.Fatal(err)
	}
	defer file.Close()
	var out [][2]string
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 3 || fields[0] == http.MethodGet || fields[0] == http.MethodHead {
			continue
		}
		if fields[2] == "public" || fields[2] == "operator" || !registered[fields[0]+" "+fields[1]] {
			continue
		}
		out = append(out, [2]string{fields[0], fields[1]})
	}
	if len(out) < 300 {
		r.t.Fatalf("only %d write routes", len(out))
	}
	return out
}

func concrete(pattern string) string {
	return routeParam.ReplaceAllString(pattern, "0f000000-0000-4000-8000-000000001153")
}

func TestOrgPendingDeletionWriteMatrix(t *testing.T) {
	r := newOrgStateRouter(t)
	routes := r.writeRoutes()
	r.request()
	for _, rt := range routes {
		method, pattern := rt[0], rt[1]
		if middleware.OrgStateExempt(method, pattern) {
			continue
		}
		w := r.call(method, concrete(pattern), "{}")
		if w.Code != http.StatusLocked || !strings.Contains(w.Body.String(), middleware.ProblemOrgPendingDeletion) ||
			!strings.Contains(w.Body.String(), `"due_at"`) {
			t.Errorf("%s %s during the grace period: %d %s", method, pattern, w.Code, w.Body.String())
		}
	}
	// Reads stay open.
	for _, c := range [][3]string{
		{http.MethodGet, "/api/v1/cis", ""},
		{http.MethodGet, "/api/v1/org/deletion", ""},
		{http.MethodPost, "/api/v1/search/query", `{"q":"x"}`},
		{http.MethodPost, "/bff/graphql", `{"query":"{ __typename }"}`},
	} {
		if w := r.call(c[0], c[1], c[2]); w.Code == http.StatusLocked {
			t.Errorf("%s %s during the grace period: 423", c[0], c[1])
		}
	}
	// Every exempt route is a registered write route of the real router
	// (a typo in a pattern would block it) and stays open; only the full
	// export of WP-1151 lives in a module this router leaves out.
	registered := map[string]bool{}
	if err := chi.Walk(r.mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for key := range middleware.OrgStateExemptRoutes {
		method, pattern, _ := strings.Cut(key, " ")
		if !registered[key] {
			if key != "POST /api/v1/org/exports" {
				t.Errorf("exemption %q names no registered write route", key)
			}
			continue
		}
		if w := r.call(method, concrete(pattern), "{}"); w.Code == http.StatusLocked {
			t.Errorf("exempt %s during the grace period: 423 %s", key, w.Body.String())
		}
	}

	// After the cancel no write route answers 423 (the bodies are empty, so
	// the handlers refuse or find nothing).
	r.cancel()
	for _, rt := range routes {
		if w := r.call(rt[0], concrete(rt[1]), "{}"); w.Code == http.StatusLocked {
			t.Errorf("%s %s after the cancel: 423", rt[0], rt[1])
		}
	}
}

func TestOrgPendingDeletionRefusesJobs(t *testing.T) {
	r := newOrgStateRouter(t)
	r.request()
	if w := r.call(http.MethodPost, "/api/v1/imports", `{"document_id":"0f000000-0000-4000-8000-000000000001","profile_id":"0f000000-0000-4000-8000-000000000002"}`); w.Code != http.StatusLocked {
		t.Fatalf("import during the grace period: %d %s", w.Code, w.Body.String())
	}
	svc := jobs.NewService(jobs.NewPGRepository(r.f.App))
	svc.Register(jobs.Kind{Name: "import.run", MaxAttempts: 1})
	svc.Register(jobs.Kind{Name: "org.export", MaxAttempts: 1})
	ctx := r.f.OrgCtx(r.f.OrgA)
	if _, _, err := svc.Enqueue(ctx, r.f.OrgA, "import.run", nil, jobs.EnqueueOptions{}); !errors.Is(err, jobs.ErrOrgPendingDeletion) {
		t.Fatalf("import job during the grace period: %v", err)
	}
	if _, _, err := svc.Enqueue(ctx, r.f.OrgA, "org.export", nil, jobs.EnqueueOptions{}); err != nil {
		t.Fatalf("full export during the grace period: %v", err)
	}
	// The neighbour organization is not affected.
	if _, _, err := svc.Enqueue(r.f.OrgCtx(r.f.OrgB), r.f.OrgB, "import.run", nil, jobs.EnqueueOptions{}); err != nil {
		t.Fatalf("job of the other organization: %v", err)
	}
	r.cancel()
	if _, _, err := svc.Enqueue(ctx, r.f.OrgA, "import.run", nil, jobs.EnqueueOptions{}); err != nil {
		t.Fatalf("import job after the cancel: %v", err)
	}
}

// TestOrgPendingDeletionRefusedJobIs423: the cache of the process still
// holds "none" while the database says requested; the route lets the
// write through, jobs.Service refuses the job, and the route answers 423
// org-pending-deletion instead of the status the handler maps it to.
func TestOrgPendingDeletionRefusedJobIs423(t *testing.T) {
	r := newOrgStateRouterWith(t, true)
	r.exec(`UPDATE organization SET deletion_state = 'requested', deletion_requested_at = now(),
		deletion_due_at = now() + interval '30 days' WHERE id = $1`, r.f.OrgA)
	w := r.call(http.MethodPost, "/api/v1/search/reindex", "")
	if w.Code != http.StatusLocked || !strings.Contains(w.Body.String(), middleware.ProblemOrgPendingDeletion) ||
		!strings.Contains(w.Body.String(), `"due_at"`) {
		t.Fatalf("job route with a stale cache: %d %s", w.Code, w.Body.String())
	}
	var jobsOfA int
	if err := r.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE organization_id = $1`, r.f.OrgA).Scan(&jobsOfA); err != nil {
		t.Fatal(err)
	}
	if jobsOfA != 0 {
		t.Fatalf("%d jobs stored during the grace period", jobsOfA)
	}
}

// collector inserts an active collector of organization A.
func (r *orgStateRouter) collector(name string) string {
	r.t.Helper()
	id := r.f.ID()
	r.exec(`INSERT INTO collector (id, organization_id, name, status, last_heartbeat_at) VALUES ($1, $2, $3, 'active', now())`, id, r.f.OrgA, name)
	return id
}

func (r *orgStateRouter) status(id string) string {
	r.t.Helper()
	var s string
	if err := r.f.Admin.QueryRow(context.Background(), `SELECT status FROM collector WHERE id = $1`, id).Scan(&s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

func TestOrgPendingDeletionPausesCollectors(t *testing.T) {
	r := newOrgStateRouter(t)
	id := r.collector("orgstate-collector")
	monitor := discovery.NewStatusMonitor(r.f.App)
	// As cmd/server/workers.go wires it (the license is active here).
	monitor.Licensed = discovery.LicensedUnlessPendingDeletion(
		func(context.Context, string) (bool, error) { return true, nil },
		func(ctx context.Context, orgID string) (bool, error) {
			return tenantapi.OrgPendingDeletion(ctx, r.f.App, orgID)
		})
	r.request()
	if _, err := monitor.RunOrg(context.Background(), r.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if s := r.status(id); s != discovery.CollectorPaused {
		t.Fatalf("status during the grace period %s", s)
	}
	r.cancel()
	if _, err := monitor.RunOrg(context.Background(), r.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if s := r.status(id); s != "offline" {
		t.Fatalf("status after the cancel %s", s)
	}
}

func TestOrgPendingDeletionRefusesEdge(t *testing.T) {
	r := newOrgStateRouter(t)
	id := r.collector("orgstate-edge")
	// The collector CA is global: a CA of another key is removed (as the
	// edge tests of the discovery package do).
	clean := func() { _, _ = r.f.Admin.Exec(context.Background(), `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	if err := r.enc.UseEphemeralDerivationRoot(); err != nil {
		t.Fatal(err)
	}
	ca := discovery.NewCA(r.f.App, r.enc)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: id}}, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.ContextWithTenantScope(context.Background(), ptr(database.OrgWideScope(r.f.OrgA, "")))
	issued, err := ca.Sign(ctx, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), r.f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	authz := discovery.NewEdgeAuthorizer(r.f.App, ca)
	if _, err = authz.Authorize(context.Background(), []string{issued.CertificatePEM}); err != nil {
		t.Fatalf("control: %v", err)
	}
	// The connection the collector opened before the request stays, but
	// its data requests are refused (the heartbeat passes).
	edge := discovery.NewHandler(discovery.NewPGRepository(r.f.App), nil)
	sys := database.ContextWithTenantScope(context.Background(), ptr(database.OrgWideScope(r.f.OrgA, "")))
	r.request()
	if _, err = authz.Authorize(context.Background(), []string{issued.CertificatePEM}); !errors.Is(err, discovery.ErrOrgPendingDeletion) {
		t.Fatalf("edge during the grace period: %v", err)
	}
	if reply := edge.EdgeIngest(sys, r.f.OrgA, id, []byte(`{"records":[]}`)); reply.Status != http.StatusLocked {
		t.Fatalf("edge ingest of a connected collector during the grace period: %+v", reply)
	}
	if reply := edge.EdgeHeartbeat(sys, r.f.OrgA, id, nil); reply.Status != http.StatusNoContent {
		t.Fatalf("edge heartbeat during the grace period: %+v", reply)
	}
	r.cancel()
	if _, err = authz.Authorize(context.Background(), []string{issued.CertificatePEM}); err != nil {
		t.Fatalf("edge after the cancel: %v", err)
	}
	if reply := edge.EdgeIngest(sys, r.f.OrgA, id, []byte(`{"records":[]}`)); reply.Status == http.StatusLocked {
		t.Fatalf("edge ingest after the cancel: %+v", reply)
	}
}

func ptr[T any](v T) *T { return &v }
