// Channel check of the credential secrets (CRD-06, COL-02, WP-899).
//
//   - TestCanaryNeverLeavesThroughAnyChannel – the complete router of the
//     server on PostgreSQL (route authorization, route audit, outbox) in
//     the middleware chain of cmd/server newHTTPHandler (request log,
//     HTTP metrics, OpenAPI validation and idempotency as route
//     middleware), a KV v2 test vault (httptest, TLS) behind the
//     server-side resolver and a NATS of its own for the reload notices:
//     a nats-server the test starts (TEST_NATS_SERVER_BIN, as in the CI),
//     else the NATS of TEST_NATS_URL; in the CI one of them is required. The test stores the
//     canary values of testdata/canary.json as a stored secret, an AppRole
//     login (role_id, secret_id), a token login of a collector-resolved
//     connection, the token the test vault issues and the value of a vault
//     reference; it creates stored and vault_ref credentials (resolve_at
//     server and collector), vault connections, bindings to a CI and a
//     subnet and calls every operation under /api/v1/credentials,
//     /api/v1/credential-bindings, /api/v1/vault-connections and
//     /api/v1/cis/{id}/credentials* (canaryRoutes), a replacement of the
//     secret, a connection test (job vault.test), a server-side resolution,
//     a bundle, a usage report with auth_failed and a run of
//     credential.age_check (credential.rotation_due). After every step it
//     scans each channel: answer body and headers, the slog output of the
//     server, audit_log, ci_change, event_outbox, notice_sent,
//     credential_usage, job, the NATS messages of the organization and the
//     Prometheus registry (GET /metrics). The opened bundle of a collector
//     never holds the value or the login of a server reference. Every
//     channel is searched for the plain value and for its Base64 forms
//     (standard and URL alphabet) at any byte offset inside a longer
//     encoded text. A find fails the subtest of its channel and names the
//     canary and the step that put it there, never the value.
//   - TestCanaryRouteListCoversAllCredentialRoutes – canaryRoutes holds
//     exactly the operations of the four prefixes in
//     internal/server/testdata/route_permissions.txt, so a new route fails
//     the test until it is called with the canaries (no database needed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<nats-server> (or TEST_NATS_URL=nats://127.0.0.1:4222) \
//	  go test -count=1 -run 'TestCanary' ./internal/credential/
package credential_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// canaryPrefixes are the route prefixes of the credential API (AK2).
var canaryPrefixes = []string{"/api/v1/credentials", "/api/v1/credential-bindings", "/api/v1/vault-connections", "/api/v1/cis/{id}/credentials"}

// canaryTables are the tables scanned after every step (AK3), with
// job for the result of the connection test.
var canaryTables = []string{"audit_log", "ci_change", "event_outbox", "notice_sent", "credential_usage", "job"}

// canaryChannels are the channels in the order of their subtests.
var canaryChannels = []string{"response", "log", "audit_log", "ci_change", "event_outbox", "notice_sent",
	"credential_usage", "job", "nats", "metrics", "bundle"}

// loadCanaries reads testdata/canary.json: canary name → value.
func loadCanaries(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("testdata/canary.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]string
	if err = json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range all {
		if strings.HasPrefix(k, "_") {
			continue
		}
		if len(v) < 16 {
			t.Fatalf("canary %s is too short to be unique", k)
		}
		out[k] = v
	}
	for _, need := range []string{"stored_secret", "approle_role_id", "approle_secret_id", "vault_token", "vault_value", "collector_login"} {
		if out[need] == "" {
			t.Fatalf("testdata/canary.json lacks %s", need)
		}
	}
	return out
}

// lockedBuffer collects the log output of concurrent goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// canaryVault is a KV v2 test vault: the AppRole login of the canaries
// issues the canary token, which reads the canary value at kv/admp/api and
// at the test path reticora-test.
type canaryVault struct {
	roleID, secretID, token, value string
}

func (v *canaryVault) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role_id"] != v.roleID || body["secret_id"] != v.secretID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"auth":{"client_token":%q,"lease_duration":3600,"renewable":true}}`, v.token)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/token/lookup-self":
		_, _ = w.Write([]byte(`{"data":{"ttl":0}}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/kv/data/"):
		if r.Header.Get("X-Vault-Token") != v.token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/v1/kv/data/") {
		case "admp/api", "reticora-test":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"data": map[string]any{"api_key": v.value}, "metadata": map[string]any{"version": 1}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// canaryEnv is the complete router of organization A with the canaries.
type canaryEnv struct {
	t        *testing.T
	f        *scopetest.Fixture
	handler  http.Handler
	svc      *credential.Service
	canaries map[string]string
	logs     *lockedBuffer
	// natsMsgs are the messages on collector.<org>.> (nil without NATS).
	natsMu   sync.Mutex
	natsMsgs []string
	nc       *nats.Conn

	// The objects the steps create and use.
	stored, approle, collectorLogin, deletable string
	serverRef, collectorRef                    string
	serverConn, collectorConn, deletableConn   string
	binding, subnetBinding                     string
	ci1, ci2, subnet, collector                string
	vaultAddress, vaultCA                      string
	testJob                                    string

	// findings: channel → canary → the first step that showed it.
	findings map[string]map[string]string
	called   map[string]bool
}

// canaryStep is one operation of canaryRoutes.
type canaryStep struct {
	route string
	call  func(e *canaryEnv) *httptest.ResponseRecorder
}

// canaryRoutes call every operation of the four prefixes once, in an
// order that creates before it reads, changes and deletes. The route key
// is the line of route_permissions.txt without the permission.
var canaryRoutes = []canaryStep{
	{"POST /api/v1/credentials/", func(e *canaryEnv) *httptest.ResponseRecorder {
		rec := e.call(http.MethodPost, "/api/v1/credentials/", `{"name":"wp899-stored","kind":"ssh_password","max_age_days":30,
			"secret":{"username":"admin","password":"`+e.canaries["stored_secret"]+`"}}`)
		e.stored = e.idOf(rec)
		rec = e.call(http.MethodPost, "/api/v1/credentials/", `{"name":"wp899-approle","kind":"vault_approle",
			"secret":{"role_id":"`+e.canaries["approle_role_id"]+`","secret_id":"`+e.canaries["approle_secret_id"]+`"}}`)
		e.approle = e.idOf(rec)
		rec = e.call(http.MethodPost, "/api/v1/credentials/", `{"name":"wp899-collector-login","kind":"vault_token",
			"secret":{"token":"`+e.canaries["collector_login"]+`"}}`)
		e.collectorLogin = e.idOf(rec)
		rec = e.call(http.MethodPost, "/api/v1/credentials/", `{"name":"wp899-deletable","kind":"ssh_password",
			"secret":{"password":"`+e.canaries["stored_secret"]+`"}}`)
		e.deletable = e.idOf(rec)
		return rec
	}},
	{"POST /api/v1/vault-connections/", func(e *canaryEnv) *httptest.ResponseRecorder {
		server := func(name string) string {
			raw, _ := json.Marshal(map[string]any{"name": name, "kind": "openbao_kv2", "address": e.vaultAddress, "mount": "kv",
				"auth_method": "approle", "auth_credential_id": e.approle, "ca_pem": e.vaultCA, "resolve_at": "server"})
			return string(raw)
		}
		e.serverConn = e.idOf(e.call(http.MethodPost, "/api/v1/vault-connections/", server("wp899-server")))
		e.deletableConn = e.idOf(e.call(http.MethodPost, "/api/v1/vault-connections/", server("wp899-deletable")))
		raw, _ := json.Marshal(map[string]any{"name": "wp899-edge", "kind": "openbao_kv2", "address": "https://vault.example.com:8200",
			"mount": "kv", "auth_method": "token", "auth_credential_id": e.collectorLogin, "resolve_at": "collector",
			"collector_ids": []string{e.collector}})
		rec := e.call(http.MethodPost, "/api/v1/vault-connections/", string(raw))
		e.collectorConn = e.idOf(rec)
		// The references of both resolution sites.
		e.serverRef = e.idOf(e.call(http.MethodPost, "/api/v1/credentials/", `{"name":"wp899-server-ref","kind":"api_token",
			"vault_ref":{"connection_id":"`+e.serverConn+`","path":"admp/api","field_map":{"token":"api_key"}}}`))
		e.collectorRef = e.idOf(e.call(http.MethodPost, "/api/v1/credentials/", `{"name":"wp899-edge-ref","kind":"snmp_v2c",
			"vault_ref":{"connection_id":"`+e.collectorConn+`","path":"net/sw1","field_map":{"community":"ro"}}}`))
		return rec
	}},
	{"GET /api/v1/credentials/", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodGet, "/api/v1/credentials/", "")
	}},
	{"GET /api/v1/credentials/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		for _, id := range []string{e.approle, e.collectorLogin, e.serverRef, e.collectorRef} {
			e.call(http.MethodGet, "/api/v1/credentials/"+id, "")
		}
		return e.call(http.MethodGet, "/api/v1/credentials/"+e.stored, "")
	}},
	{"PATCH /api/v1/credentials/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		e.call(http.MethodPatch, "/api/v1/credentials/"+e.serverRef, `{"name":"wp899-server-ref-renamed"}`)
		return e.call(http.MethodPatch, "/api/v1/credentials/"+e.stored, `{"name":"wp899-stored-renamed","max_age_days":30}`)
	}},
	{"PUT /api/v1/credentials/{id}/secret", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodPut, "/api/v1/credentials/"+e.stored+"/secret",
			`{"secret":{"username":"admin","password":"`+e.canaries["stored_secret"]+`"}}`)
	}},
	{"POST /api/v1/credentials/rotate-keys", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodPost, "/api/v1/credentials/rotate-keys", "")
	}},
	{"POST /api/v1/credential-bindings/", func(e *canaryEnv) *httptest.ResponseRecorder {
		e.binding = e.idOf(e.call(http.MethodPost, "/api/v1/credential-bindings/",
			`{"credential_id":"`+e.stored+`","target_kind":"ci","target_id":"`+e.ci1+`","priority":100}`))
		rec := e.call(http.MethodPost, "/api/v1/credential-bindings/",
			`{"credential_id":"`+e.stored+`","target_kind":"subnet","target_id":"`+e.subnet+`","priority":100}`)
		e.subnetBinding = e.idOf(rec)
		return rec
	}},
	{"GET /api/v1/credential-bindings/", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodGet, "/api/v1/credential-bindings/", "")
	}},
	{"GET /api/v1/credential-bindings/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodGet, "/api/v1/credential-bindings/"+e.binding, "")
	}},
	{"PATCH /api/v1/credential-bindings/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodPatch, "/api/v1/credential-bindings/"+e.binding, `{"priority":50}`)
	}},
	{"PUT /api/v1/cis/{id}/credentials", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodPut, "/api/v1/cis/"+e.ci2+"/credentials",
			`{"bindings":[{"credential_id":"`+e.stored+`","priority":100}]}`)
	}},
	{"GET /api/v1/cis/{id}/credentials", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodGet, "/api/v1/cis/"+e.ci1+"/credentials", "")
	}},
	{"GET /api/v1/cis/{id}/credentials/effective", func(e *canaryEnv) *httptest.ResponseRecorder {
		e.call(http.MethodGet, "/api/v1/cis/"+e.ci2+"/credentials/effective", "")
		return e.call(http.MethodGet, "/api/v1/cis/"+e.ci1+"/credentials/effective", "")
	}},
	{"GET /api/v1/vault-connections/", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodGet, "/api/v1/vault-connections/", "")
	}},
	{"GET /api/v1/vault-connections/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		e.call(http.MethodGet, "/api/v1/vault-connections/"+e.collectorConn, "")
		return e.call(http.MethodGet, "/api/v1/vault-connections/"+e.serverConn, "")
	}},
	{"PATCH /api/v1/vault-connections/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodPatch, "/api/v1/vault-connections/"+e.serverConn, `{"name":"wp899-server-renamed"}`)
	}},
	{"POST /api/v1/vault-connections/{id}/test", func(e *canaryEnv) *httptest.ResponseRecorder {
		rec := e.call(http.MethodPost, "/api/v1/vault-connections/"+e.serverConn+"/test", "")
		var reply struct {
			JobID string `json:"job_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &reply)
		e.testJob = reply.JobID
		e.waitJob(e.testJob)
		return rec
	}},
	{"GET /api/v1/credentials/{id}/usage", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodGet, "/api/v1/credentials/"+e.stored+"/usage", "")
	}},
	{"DELETE /api/v1/credential-bindings/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodDelete, "/api/v1/credential-bindings/"+e.subnetBinding, "")
	}},
	{"DELETE /api/v1/credentials/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodDelete, "/api/v1/credentials/"+e.deletable, "")
	}},
	{"DELETE /api/v1/vault-connections/{id}", func(e *canaryEnv) *httptest.ResponseRecorder {
		return e.call(http.MethodDelete, "/api/v1/vault-connections/"+e.deletableConn, "")
	}},
}

func newCanaryEnv(t *testing.T) *canaryEnv {
	t.Helper()
	canaries := loadCanaries(t)
	f := scopetest.Seed(t, "15")
	natsURL := canaryNATS(t)
	e := &canaryEnv{t: t, f: f, canaries: canaries, logs: &lockedBuffer{},
		findings: map[string]map[string]string{}, called: map[string]bool{}}

	// The slog output of the server in a buffer (the log channel).
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(e.logs), &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The test vault, reachable as example.com (the name of its
	// certificate) over the egress dialer.
	vault := &canaryVault{roleID: canaries["approle_role_id"], secretID: canaries["approle_secret_id"],
		token: canaries["vault_token"], value: canaries["vault_value"]}
	srv := httptest.NewTLSServer(http.HandlerFunc(vault.serve))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	e.vaultAddress = "https://example.com:" + u.Port()
	e.vaultCA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	e.nc = nc
	sub, err := nc.Subscribe("collector."+f.OrgA+".>", func(m *nats.Msg) {
		e.natsMu.Lock()
		e.natsMsgs = append(e.natsMsgs, m.Subject+" "+string(m.Data))
		e.natsMu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	policy := egress.Options{AllowPrivate: true, Resolver: loopbackResolver{}}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	resolver := credential.NewVaultResolver(policy, nc)
	t.Cleanup(resolver.Close)
	e.svc = credential.NewService(repos.Credential, encryptor).WithVault(resolver).
		WithRotation(credential.NewRotation(f.App, nc, discovery.NewBundleGate(f.App, nil)))
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	// The handler as cmd/server newHTTPHandler builds it: validation and
	// idempotency per route, request log and HTTP metrics around the
	// router (the principal comes from call, not from a login).
	mux, httpMetrics, err := server.NewRouter(repos, server.Options{
		Version: "wp899", Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: e.svc,
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      policy,
		AuditPool:   f.App,
		Audit:       audit.NewHandler(f.App),
		Events:      events.NewPGEmitter(f.App),
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation,
			middleware.IdempotencyWithStore(cache.NewMemoryStore())},
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	e.handler = middleware.Chain(middleware.RequestID, middleware.Recovery, middleware.Logger, middleware.SecurityHeaders,
		middleware.DeprecationHeaders, middleware.TenantMiddleware, httpMetrics)(mux)

	e.ci1 = f.CI(t, f.OrgA, f.Client1, "wp899-ci-1")
	e.ci2 = f.CI(t, f.OrgA, f.Client1, "wp899-ci-2")
	e.exec(`UPDATE ci SET management_ip = '10.99.1.10' WHERE id = $1`, e.ci1)
	e.subnet = e.scalar(`INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.99.1.0/24') RETURNING id::text`,
		f.OrgA, f.Client1)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp899-collector', 'active') RETURNING id::text`, f.OrgA)
	// A scope without a credential list: its bundle holds every credential,
	// so a replaced secret sends it a reload notice (NATS channel).
	e.exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, credential_ids)
		VALUES ($1, $2, 'wp899-scope', 'cidr', '10.99.1.0/24', '{}'::uuid[])`, f.OrgA, e.collector)
	return e
}

func (e *canaryEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *canaryEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// call sends one request as an org-wide principal with every permission,
// requires a 2xx answer and scans its body and headers. A failure names
// the route and the status, never the body (it could hold a canary).
func (e *canaryEnv) call(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	perms := identity.AllPermissions()
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "wp899"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req.WithContext(ctx))
	step := method + " " + path
	var headers strings.Builder
	for k, vs := range rec.Header() {
		headers.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	e.scan("response", step, rec.Body.String()+"\n"+headers.String())
	if rec.Code < 200 || rec.Code > 299 {
		problem := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &problem)
		e.t.Fatalf("%s = %d (%v)", step, rec.Code, problem["title"])
	}
	return rec
}

func (e *canaryEnv) idOf(rec *httptest.ResponseRecorder) string {
	e.t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.ID == "" {
		e.t.Fatalf("answer without id (%d)", rec.Code)
	}
	return v.ID
}

// waitJob waits until the detached job ended and requires its success.
func (e *canaryEnv) waitJob(id string) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status := e.scalar(`SELECT status FROM job WHERE id = $1`, id)
		switch status {
		case "succeeded":
			return
		case "failed", "cancelled", "dead":
			e.t.Fatalf("job %s ended %s", id, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("job %s did not end", id)
}

// forms returns the plain value of a canary and the stable cores of its
// Base64 forms (standard and URL alphabet, with or without padding) at
// each of the three byte offsets a value can have inside a longer encoded
// text, such as an encoded JSON body. A core drops the characters the
// neighbouring bytes change: the first group when the value does not
// start a group, the last incomplete group.
func forms(v string) []string {
	out := []string{v}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for k := range 3 {
			raw := enc.EncodeToString(append(make([]byte, k), v...))
			head := 0
			if k > 0 {
				head = 4
			}
			tail := 0
			switch (k + len(v)) % 3 {
			case 1:
				tail = 2
			case 2:
				tail = 3
			}
			if core := raw[head : len(raw)-tail]; len(core) >= 8 && !slices.Contains(out, core) {
				out = append(out, core)
			}
		}
	}
	return out
}

// canaryNATS returns the NATS of the reload notices: a nats-server of its
// own (TEST_NATS_SERVER_BIN, set in the CI), else TEST_NATS_URL; without
// both the test is skipped, in the CI it fails.
func canaryNATS(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("TEST_NATS_SERVER_BIN"); bin != "" {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		host, port, _ := net.SplitHostPort(addr)
		cmd := exec.Command(bin, "-a", host, "-p", port)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if conn, derr := net.DialTimeout("tcp", addr, time.Second); derr == nil {
				_ = conn.Close()
				return "nats://" + addr
			}
			if time.Now().After(deadline) {
				t.Fatalf("nats-server did not listen on %s:\n%s", addr, out.String())
			}
		}
	}
	if u := os.Getenv("TEST_NATS_URL"); u != "" {
		return u
	}
	if isCI, _ := strconv.ParseBool(os.Getenv("CI")); isCI {
		t.Fatal("neither TEST_NATS_SERVER_BIN nor TEST_NATS_URL set in the CI: the channel check (WP-899) includes the reload notices over NATS")
	}
	t.Skip("neither TEST_NATS_SERVER_BIN nor TEST_NATS_URL set")
	return ""
}

// scan records each canary found in text under channel, with the step.
func (e *canaryEnv) scan(channel, step, text string) {
	e.scanOnly(channel, step, text, nil)
}

// scanOnly scans for the canaries named in only (all when nil).
func (e *canaryEnv) scanOnly(channel, step, text string, only []string) {
	for name, value := range e.canaries {
		if only != nil && !slices.Contains(only, name) {
			continue
		}
		for _, form := range forms(value) {
			if !strings.Contains(text, form) {
				continue
			}
			if e.findings[channel] == nil {
				e.findings[channel] = map[string]string{}
			}
			if _, seen := e.findings[channel][name]; !seen {
				e.findings[channel][name] = step
			}
		}
	}
}

// scanChannels scans every stored channel after step: the log buffer, the
// tables, the NATS messages and the metrics.
func (e *canaryEnv) scanChannels(step string) {
	e.t.Helper()
	e.scan("log", step, e.logs.String())
	for _, table := range canaryTables {
		var text string
		if err := e.f.Admin.QueryRow(context.Background(),
			`SELECT coalesce(string_agg(t::text, E'\n'), '') FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&text); err != nil {
			e.t.Fatalf("read %s: %v", table, err)
		}
		e.scan(table, step, text)
	}
	if err := e.nc.Flush(); err != nil {
		e.t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	e.natsMu.Lock()
	msgs := strings.Join(e.natsMsgs, "\n")
	e.natsMu.Unlock()
	e.scan("nats", step, msgs)
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET /metrics = %d", rec.Code)
	}
	e.scan("metrics", step, rec.Body.String())
}

// TestCanaryNeverLeavesThroughAnyChannel covers AK1 to AK3 of WP-899.
func TestCanaryNeverLeavesThroughAnyChannel(t *testing.T) {
	e := newCanaryEnv(t)
	for _, step := range canaryRoutes {
		step.call(e)
		e.called[step.route] = true
		e.scanChannels(step.route)
	}

	// A server-side resolution, as a server connector (ADMP) does it: the
	// value of the reference passes through the server's cache only.
	got, err := e.svc.Decrypt(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, e.serverRef)
	if err != nil {
		t.Fatalf("resolve the server reference: %v", err)
	}
	if got["token"] != e.canaries["vault_value"] {
		t.Fatal("the server reference did not resolve to the canary value of the test vault")
	}
	e.scanChannels("server-side resolution")

	// The bundle of the collector of the edge connection, asked for every
	// credential: the stored secret and the edge login belong into it; the
	// server reference, its value and its login never.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealed, delivered, err := e.svc.Bundle(e.f.OrgCtx(e.f.OrgA), &credential.BundleTarget{OrganizationID: e.f.OrgA,
		CollectorID: e.collector, PublicKey: &key.PublicKey,
		CredentialIDs: []string{e.stored, e.serverRef, e.collectorRef, e.approle}})
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	content, err := credential.OpenBundle(sealed, key)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(delivered, e.serverRef) || slices.Contains(delivered, e.approle) {
		t.Errorf("the bundle delivers the server reference or its login")
	}
	if !slices.Contains(delivered, e.stored) || !slices.Contains(delivered, e.collectorRef) {
		t.Errorf("the bundle lacks the stored credential or the edge reference: the check would prove nothing")
	}
	sealedRaw, _ := json.Marshal(sealed)
	openRaw, _ := json.Marshal(content)
	// The scanner itself: the opened bundle holds the stored secret and the
	// edge login (they belong to the collector), so a scan finds them.
	probe := &canaryEnv{canaries: e.canaries, findings: map[string]map[string]string{}}
	probe.scan("probe", "bundle", string(openRaw))
	if probe.findings["probe"]["stored_secret"] == "" || probe.findings["probe"]["collector_login"] == "" {
		t.Fatal("the scan does not find the stored secret in the opened bundle: the check would prove nothing")
	}
	serverSide := []string{"approle_role_id", "approle_secret_id", "vault_token", "vault_value"}
	e.scanOnly("bundle", "bundle of the edge collector", string(sealedRaw)+"\n"+string(openRaw), serverSide)
	e.scanChannels("bundle of the edge collector")

	// A usage report with auth_failed, as the scope run records it.
	scope := database.OrgWideScope(e.f.OrgA, "")
	err = database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := credential.RecordUsage(ctx, tx, &credential.UsageInput{OrganizationID: e.f.OrgA,
			ScopeID: "6f0b6f0e-0000-4000-8000-000000000899", ScopeClientID: e.f.Client1,
			Bundle: credential.UsageBundle{CredentialIDs: map[string]bool{e.stored: true}},
			Entries: []credential.UsageReport{{Target: "10.99.1.10", Namespace: "ssh", CredentialID: e.stored,
				Cause: credential.UsageAuthFailed}}})
		return rerr
	})
	if err != nil {
		t.Fatalf("usage report: %v", err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM credential_usage WHERE credential_id = $1 AND last_error_class = 'auth_failed'`, e.stored); n != "1" {
		t.Fatalf("usage rows with auth_failed = %s, want 1", n)
	}
	e.call(http.MethodGet, "/api/v1/credentials/"+e.stored+"/usage", "")
	e.scanChannels("usage report auth_failed")

	// credential.age_check reports the aged stored secret
	// (credential.rotation_due, notice_sent).
	e.exec(`UPDATE credential SET rotated_at = now() - interval '40 days', created_at = now() - interval '40 days' WHERE id = $1`, e.stored)
	if _, err = credential.NewAgeCheck(e.f.App).Run(context.Background()); err != nil {
		t.Fatalf("credential.age_check: %v", err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3`,
		e.f.OrgA, events.CredentialRotationDue, e.stored); n != "1" {
		t.Fatalf("credential.rotation_due events = %s, want 1", n)
	}
	e.scanChannels("credential.age_check")

	// The channels must have seen the actions at all: the audit of the
	// writes, the rotation event of the replaced secret, a reload notice.
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1`, e.f.OrgA); n == "0" {
		t.Error("audit_log holds no entry of the organization: the audit channel was not exercised")
	}
	if n := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = $2`,
		e.f.OrgA, events.CredentialRotated); n == "0" {
		t.Error("event_outbox holds no credential.rotated: the event channel was not exercised")
	}
	e.natsMu.Lock()
	natsSeen := len(e.natsMsgs)
	e.natsMu.Unlock()
	if natsSeen == 0 {
		t.Error("no NATS message of the organization: the notice channel was not exercised")
	}

	for _, channel := range canaryChannels {
		t.Run(channel, func(t *testing.T) {
			found := e.findings[channel]
			names := make([]string, 0, len(found))
			for name := range found {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				t.Errorf("canary %s found in channel %s after %s", name, channel, found[name])
			}
		})
	}
	for _, step := range canaryRoutes {
		if !e.called[step.route] {
			t.Errorf("route %s was not called", step.route)
		}
	}
}

// credentialRoutes reads the operations of the four prefixes from
// route_permissions.txt (method and pattern, without the permission).
func credentialRoutes(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../server/testdata/route_permissions.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		for _, prefix := range canaryPrefixes {
			if fields[1] == prefix || strings.HasPrefix(fields[1], prefix+"/") {
				out = append(out, fields[0]+" "+fields[1])
				break
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestCanaryRouteListCoversAllCredentialRoutes covers AK2: every
// operation of the four prefixes in route_permissions.txt is called by
// canaryRoutes, and canaryRoutes calls no route the file does not know.
func TestCanaryRouteListCoversAllCredentialRoutes(t *testing.T) {
	want := credentialRoutes(t)
	if len(want) < 20 {
		t.Fatalf("route_permissions.txt lists only %d credential routes: wrong file or prefixes", len(want))
	}
	have := map[string]bool{}
	for _, step := range canaryRoutes {
		if have[step.route] {
			t.Errorf("route %s is called twice in canaryRoutes", step.route)
		}
		have[step.route] = true
	}
	for _, route := range want {
		if !have[route] {
			t.Errorf("route %s is not called with the canaries: add it to canaryRoutes", route)
		}
		delete(have, route)
	}
	for route := range have {
		t.Errorf("canaryRoutes calls %s, which route_permissions.txt does not list", route)
	}
}

// TestCanaryFormsFindEmbeddedBase64: the scan finds a canary encoded
// inside a longer Base64 text at every byte offset, in the standard and
// the URL alphabet, with and without padding (no database needed).
func TestCanaryFormsFindEmbeddedBase64(t *testing.T) {
	canaries := loadCanaries(t)
	encodings := map[string]*base64.Encoding{"std": base64.StdEncoding, "raw-std": base64.RawStdEncoding,
		"url": base64.URLEncoding, "raw-url": base64.RawURLEncoding}
	for name, value := range canaries {
		for prefix := range 6 {
			for suffix := range 3 {
				plain := strings.Repeat("{", prefix) + `"password":"` + value + `"` + strings.Repeat("}", suffix)
				for encName, enc := range encodings {
					text := "data=" + enc.EncodeToString([]byte(plain)) + "; next"
					if !slices.ContainsFunc(forms(value), func(f string) bool { return strings.Contains(text, f) }) {
						t.Errorf("%s: %s encoding with %d+%d bytes around it not found", name, encName, prefix, suffix)
					}
				}
			}
		}
	}
	// No form of a canary is found in a text without it.
	text := base64.StdEncoding.EncodeToString([]byte(`{"password":"wp899-canary-other-value"}`))
	for name, value := range canaries {
		for _, f := range forms(value) {
			if strings.Contains(text, f) {
				t.Errorf("%s: a form matches a text without the canary", name)
			}
		}
	}
}
