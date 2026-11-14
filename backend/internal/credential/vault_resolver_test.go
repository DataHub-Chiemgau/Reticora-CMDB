package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/vault"
)

// Canary values of the server-side resolution: none may appear in a log
// line or an error text.
const (
	srvRoleID   = "srv-role-canary-11aa"
	srvSecretID = "srv-secret-id-canary-22bb"
	srvToken    = "srv-vault-token-canary-33cc"
	srvValue    = "srv-vault-value-canary-44dd"
)

// kvServer is a KV v2 test vault: one AppRole and the entries of the mount
// kv; it counts the requests.
type kvServer struct {
	mu       sync.Mutex
	requests int
	reads    int
	version  int
}

func (k *kvServer) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.requests++
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role_id"] != srvRoleID || body["secret_id"] != srvSecretID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"auth":{"client_token":%q,"lease_duration":3600,"renewable":true}}`, srvToken)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/kv/data/"):
		if r.Header.Get("X-Vault-Token") != srvToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/v1/kv/data/") {
		case "admp/api", "reticora-test":
			k.reads++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"data": map[string]any{"api_key": srvValue}, "metadata": map[string]any{"version": k.version}}})
		case "admp/denied":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (k *kvServer) counts() (requests, reads int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.requests, k.reads
}

// fixedResolver resolves every name to one address (the egress test
// resolver).
type fixedResolver struct{ addr netip.Addr }

func (f fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{f.addr}, nil
}

// vaultEnv is a memory service with an AppRole login, a KV v2 test vault
// reachable as example.com (the name of the test certificate) over the
// egress dialer, and a connection with resolve_at server to it.
type vaultEnv struct {
	svc      *Service
	kv       *kvServer
	resolver *VaultResolver
	conn     string
	login    string
	address  string
	caPEM    string
	now      time.Time
}

func newVaultEnv(t *testing.T, allowPrivate bool) *vaultEnv {
	t.Helper()
	kv := &kvServer{version: 1}
	srv := httptest.NewTLSServer(http.HandlerFunc(kv.serve))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	e := &vaultEnv{kv: kv, address: "https://example.com:" + u.Port(), now: time.Now(),
		caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))}
	e.resolver = NewVaultResolver(egress.Options{AllowPrivate: allowPrivate,
		Resolver: fixedResolver{netip.MustParseAddr("127.0.0.1")}}, nil)
	e.resolver.cache = vault.NewCache(vault.DefaultTTL, func() time.Time { return e.now })
	t.Cleanup(e.resolver.Close)
	e.svc = NewService(NewMemoryRepository(), testEncryptor(t)).WithVault(e.resolver)
	ctx := context.Background()
	login, err := e.svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "approle", Kind: KindVaultAppRole,
		Secret: map[string]any{"role_id": srvRoleID, "secret_id": srvSecretID}})
	if err != nil {
		t.Fatal(err)
	}
	e.login = login.ID
	c, err := e.svc.Vaults().Create(ctx, "org-1", &VaultConnection{Name: "server", Kind: VaultKindOpenBao, Address: e.address,
		Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: login.ID, CAPEM: e.caPEM, ResolveAt: ResolveAtServer, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	e.conn = c.ID
	return e
}

// ref creates a vault_ref credential of kind api_token reading path.
func (e *vaultEnv) ref(t *testing.T, conn, path, field string) string {
	t.Helper()
	c, err := e.svc.Create(context.Background(), CreateRequest{OrganizationID: "org-1", Name: "ref-" + path + "-" + field,
		Kind: KindAPIToken, VaultRef: &VaultRef{ConnectionID: conn, Path: path, FieldMap: map[string]string{"token": field}}})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func resolveCount(result string) float64 {
	return testutil.ToFloat64(VaultResolves.WithLabelValues(result))
}

// TestServerResolveCachesUntilTTL covers AK1 and AK5 of WP-896: Decrypt of
// a reference with resolve_at server logs in, reads the entry, maps it
// over field_map and caches it; within the TTL a second Decrypt reads the
// cache (metric result cache), not the vault.
func TestServerResolveCachesUntilTTL(t *testing.T) {
	e := newVaultEnv(t, true)
	id := e.ref(t, e.conn, "admp/api", "api_key")
	ok, cache := resolveCount(VaultResultOK), resolveCount(VaultResultCache)
	ctx := context.Background()
	for range 3 {
		secret, err := e.svc.Decrypt(ctx, "org-1", id)
		if err != nil {
			t.Fatal(err)
		}
		if secret["token"] != srvValue || len(secret) != 1 {
			t.Fatalf("secret = %v", secret)
		}
		e.now = e.now.Add(time.Minute)
	}
	if _, reads := e.kv.counts(); reads != 1 {
		t.Fatalf("reads = %d, want 1 within the TTL", reads)
	}
	if resolveCount(VaultResultOK)-ok != 1 || resolveCount(VaultResultCache)-cache != 2 {
		t.Fatalf("metric ok +%v, cache +%v", resolveCount(VaultResultOK)-ok, resolveCount(VaultResultCache)-cache)
	}
	// The cache is per organization: another organization's resolver key
	// differs.
	if _, hit := e.resolver.cache.Get(vault.CacheKey{Tenant: "org-2", Connection: e.conn, Path: "admp/api"}); hit {
		t.Fatal("the cache entry is visible under another organization")
	}
}

// TestServerResolveRefreshAfterExpiry covers AK1: after the TTL the value
// is read again and a new version is seen.
func TestServerResolveRefreshAfterExpiry(t *testing.T) {
	e := newVaultEnv(t, true)
	id := e.ref(t, e.conn, "admp/api", "api_key")
	ctx := context.Background()
	if _, err := e.svc.Decrypt(ctx, "org-1", id); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(vault.DefaultTTL)
	if _, err := e.svc.Decrypt(ctx, "org-1", id); err != nil {
		t.Fatal(err)
	}
	if _, reads := e.kv.counts(); reads != 2 {
		t.Fatalf("reads = %d, want 2 after the TTL", reads)
	}
}

// TestServerResolveBlocksPrivateWithoutAllowPrivate covers AK2 and AK3: a
// vault that resolves to a private address is refused with
// ErrEgressBlocked before any connection; storing a connection with
// resolve_at server and a private literal address is 422 address.
func TestServerResolveBlocksPrivateWithoutAllowPrivate(t *testing.T) {
	e := newVaultEnv(t, false)
	id := e.ref(t, e.conn, "admp/api", "api_key")
	blocked := resolveCount(VaultClassEgressBlocked)
	if _, err := e.svc.Decrypt(context.Background(), "org-1", id); !errors.Is(err, ErrEgressBlocked) {
		t.Fatalf("Decrypt = %v, want ErrEgressBlocked", err)
	}
	if requests, _ := e.kv.counts(); requests != 0 {
		t.Fatalf("%d requests reached the vault", requests)
	}
	if resolveCount(VaultClassEgressBlocked)-blocked != 1 {
		t.Fatal("the metric did not count egress_blocked")
	}
	_, err := e.svc.Vaults().Create(context.Background(), "org-1", &VaultConnection{Name: "private", Kind: VaultKindOpenBao,
		Address: "https://10.0.0.5:8200", Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: e.login,
		ResolveAt: ResolveAtServer, Enabled: true})
	if fieldOf(err) != "address" {
		t.Fatalf("private server connection = %v, want 422 address", err)
	}
	// A collector connection is dialed by its collectors: no egress check.
	if _, err = e.svc.Vaults().Create(context.Background(), "org-1", &VaultConnection{Name: "edge", Kind: VaultKindOpenBao,
		Address: "https://10.0.0.5:8200", Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: e.login,
		ResolveAt: ResolveAtCollector, Enabled: true, CollectorIDs: []string{"00000000-0000-4000-8000-000000000001"}}); err != nil {
		t.Fatalf("collector connection: %v", err)
	}
}

// TestServerResolveErrorClasses covers AK2: denied, missing entry,
// missing field and an unreachable vault come back as their error, and
// neither the error text nor the log carries a value, token or secret id.
func TestServerResolveErrorClasses(t *testing.T) {
	logs := &bytes.Buffer{}
	var mu sync.Mutex
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	e := newVaultEnv(t, true)
	ctx := context.Background()
	gone, err := e.svc.Vaults().Create(ctx, "org-1", &VaultConnection{Name: "gone", Kind: VaultKindOpenBao,
		Address: "https://example.com:1", Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: e.login,
		CAPEM: e.caPEM, ResolveAt: ResolveAtServer, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		conn, path, field string
		want              error
		class             string
	}{
		{e.conn, "admp/denied", "api_key", ErrVaultDenied, "vault_denied"},
		{e.conn, "admp/none", "api_key", ErrVaultNotFound, "vault_not_found"},
		{e.conn, "admp/api", "absent", ErrFieldMissing, "field_missing"},
		{gone.ID, "admp/api", "api_key", ErrVaultUnreachable, "vault_unreachable"},
	} {
		_, derr := e.svc.Decrypt(ctx, "org-1", e.ref(t, c.conn, c.path, c.field))
		if !errors.Is(derr, c.want) || VaultErrorClass(derr) != c.class {
			t.Errorf("%s/%s: %v (class %q), want %v", c.path, c.field, derr, VaultErrorClass(derr), c.want)
		}
		if derr != nil && strings.Contains(derr.Error(), srvValue) {
			t.Errorf("the error carries the value: %v", derr)
		}
	}
	mu.Lock()
	out := logs.String()
	mu.Unlock()
	if !strings.Contains(out, "error_class=vault_denied") {
		t.Fatalf("control: the log lacks the failure:\n%s", out)
	}
	for _, canary := range []string{srvValue, srvToken, srvRoleID, srvSecretID} {
		if strings.Contains(out, canary) {
			t.Errorf("the log carries %q", canary)
		}
	}
	// Without a resolver the server does not read: ErrVaultNotConfigured.
	plain := NewService(NewMemoryRepository(), testEncryptor(t))
	if _, perr := plain.resolveWithout(ctx); !errors.Is(perr, ErrVaultNotConfigured) {
		t.Fatalf("without a resolver: %v", perr)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// resolveWithout decrypts a server reference of a service without a
// resolver.
func (s *Service) resolveWithout(ctx context.Context) (map[string]any, error) {
	login, err := s.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "t", Kind: KindVaultToken, Secret: map[string]any{"token": "x"}})
	if err != nil {
		return nil, err
	}
	conn, err := s.Vaults().Create(ctx, "org-1", &VaultConnection{Name: "s", Kind: VaultKindOpenBao, Address: "https://vault.example.com",
		Mount: "kv", AuthMethod: VaultAuthToken, AuthCredentialID: login.ID, ResolveAt: ResolveAtServer, Enabled: true})
	if err != nil {
		return nil, err
	}
	ref, err := s.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "r", Kind: KindAPIToken,
		VaultRef: &VaultRef{ConnectionID: conn.ID, Path: "p", FieldMap: map[string]string{"token": "t"}}})
	if err != nil {
		return nil, err
	}
	return s.Decrypt(ctx, "org-1", ref.ID)
}

// TestBundleNeverResolvesServerReferences covers AK6: the bundle of a
// collector carries no credential of a server connection and never asks
// the resolver; the connections it carries are those with resolve_at
// collector that list the collector, with their login.
func TestBundleNeverResolvesServerReferences(t *testing.T) {
	e := newVaultEnv(t, true)
	ctx := context.Background()
	serverRef := e.ref(t, e.conn, "admp/api", "api_key")
	const collectorID = "00000000-0000-4000-8000-0000000000c1"
	edge, err := e.svc.Vaults().Create(ctx, "org-1", &VaultConnection{Name: "edge", Kind: VaultKindOpenBao, Address: e.address,
		Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: e.login, ResolveAt: ResolveAtCollector, Enabled: true,
		CollectorIDs: []string{collectorID}})
	if err != nil {
		t.Fatal(err)
	}
	edgeRef := e.ref(t, edge.ID, "net/sw1", "community")
	key := newCollectorKey(t)
	sealed, ids, err := e.svc.Bundle(ctx, &BundleTarget{OrganizationID: "org-1", CollectorID: collectorID, PublicKey: &key.PublicKey,
		CredentialIDs: []string{serverRef, edgeRef}})
	if err != nil {
		t.Fatal(err)
	}
	if requests, _ := e.kv.counts(); requests != 0 {
		t.Fatalf("building the bundle sent %d requests to the vault", requests)
	}
	content, err := OpenBundle(sealed, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range content.Credentials {
		if c.ID == serverRef {
			t.Fatal("a reference of a server connection is in the bundle")
		}
	}
	if len(content.VaultConnections) != 1 || content.VaultConnections[0].ConnectionID != edge.ID ||
		content.VaultConnections[0].LoginID != e.login {
		t.Fatalf("vault connections = %+v", content.VaultConnections)
	}
	if !containsAll(ids, edgeRef, e.login) || len(ids) != 2 {
		t.Fatalf("ids = %v, want the edge reference and its login", ids)
	}
	raw, _ := json.Marshal(content)
	if strings.Contains(string(raw), srvValue) {
		t.Fatal("the bundle carries a value of the vault")
	}
	// Another collector gets neither the connection nor its login.
	sealed, ids, err = e.svc.Bundle(ctx, &BundleTarget{OrganizationID: "org-1", CollectorID: "00000000-0000-4000-8000-0000000000c2",
		PublicKey: &key.PublicKey, CredentialIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if content, err = OpenBundle(sealed, key); err != nil || len(content.VaultConnections) != 0 || len(ids) != 0 {
		t.Fatalf("other collector: %+v, %v, %v", content, ids, err)
	}
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, v := range list {
			found = found || v == w
		}
		if !found {
			return false
		}
	}
	return true
}

// inlineJobs runs a job at once and keeps its result (the job service in
// this process).
type inlineJobs struct {
	mu     sync.Mutex
	params []any
	result any
	err    error
}

func (j *inlineJobs) RunDetached(ctx context.Context, _, kind string, params any, _ string,
	run func(ctx context.Context) (any, error)) (*jobs.Job, error) {
	res, err := run(ctx)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.params = append(j.params, params)
	j.result, j.err = res, err
	return &jobs.Job{ID: "00000000-0000-4000-8000-00000000j0b1", Kind: kind, Status: jobs.StatusSucceeded}, nil
}

// postTest posts a connection test through the routes of the handler.
func postTest(t *testing.T, h *Handler, connID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	req := withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/vault-connections/"+connID+"/test", strings.NewReader(body)), "org-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestVaultTestJobServer covers AK4 for resolve_at server: POST
// /vault-connections/{id}/test answers 202 with the job vault.test, whose
// result is ok, or vault_denied for a path the login may not read; the
// result carries nothing else.
func TestVaultTestJobServer(t *testing.T) {
	e := newVaultEnv(t, true)
	runner := &inlineJobs{}
	h := NewHandler(e.svc).WithJobs(runner)
	for _, c := range []struct{ body, want string }{
		{``, "ok"},
		{`{"path":"admp/denied"}`, "vault_denied"},
		{`{"path":"admp/none"}`, "vault_not_found"},
	} {
		rec := postTest(t, h, e.conn, c.body)
		if rec.Code != http.StatusAccepted || rec.Header().Get("Location") == "" {
			t.Fatalf("POST test %s = %d %s", c.body, rec.Code, rec.Body)
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["kind"] != VaultTestJobKind {
			t.Fatalf("answer = %v", resp)
		}
		res, ok := runner.result.(*VaultTestResult)
		if !ok || runner.err != nil || res.Result != c.want || res.ResolveAt != ResolveAtServer {
			t.Fatalf("result of %s = %+v, %v; want %s", c.body, runner.result, runner.err, c.want)
		}
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), srvValue) || strings.Contains(string(raw), srvToken) {
			t.Fatalf("the result carries a secret: %s", raw)
		}
	}
	if p := runner.params[0].(VaultTestParams); p.ConnectionID != e.conn || p.Path != "" {
		t.Fatalf("params = %+v", p)
	}
	if _, reads := e.kv.counts(); reads != 1 {
		t.Fatalf("the default test path reticora-test was read %d times", reads)
	}
	if rec := postTest(t, h, "00000000-0000-4000-8000-0000000000ff", ``); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connection = %d", rec.Code)
	}
	if rec := postTest(t, h, e.conn, `{"path":"../x"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid path = %d", rec.Code)
	}
	// An empty chunked body (no Content-Length) is no body: the default
	// path, not 400.
	router := chi.NewRouter()
	h.RegisterRoutes(router)
	chunked := withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/vault-connections/"+e.conn+"/test", strings.NewReader("")), "org-1")
	chunked.ContentLength, chunked.TransferEncoding = -1, []string{"chunked"}
	if rec := httptest.NewRecorder(); func() int { router.ServeHTTP(rec, chunked); return rec.Code }() != http.StatusAccepted {
		t.Fatalf("empty chunked body = %d %s", rec.Code, rec.Body)
	}
	if rec := postTest(t, NewHandler(e.svc), e.conn, ``); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without the job model = %d", rec.Code)
	}
	// The queue runs the same test from the job row.
	job := &jobs.Job{OrganizationID: "org-1", Kind: VaultTestJobKind, Params: json.RawMessage(`{"connection_id":"` + e.conn + `"}`)}
	res, err := h.RunVaultTestJob(context.Background(), job)
	if err != nil || res.(*VaultTestResult).Result != "ok" {
		t.Fatalf("queue job = %+v, %v", res, err)
	}
}

// TestVaultTestJobCollector covers AK4 for resolve_at collector: the job
// asks the collectors of the connection over
// collector.<org>.<id>.vault.test and takes the answer of the first that
// replies; a collector that does not answer is skipped.
func TestVaultTestJobCollector(t *testing.T) {
	natsURL := os.Getenv("TEST_NATS_URL")
	if natsURL == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	e := newVaultEnv(t, true)
	e.resolver.nc = nc
	e.resolver.collectorTimeout = 300 * time.Millisecond
	org := fmt.Sprintf("org-%d", time.Now().UnixNano())
	const silent, answering = "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	ctx := context.Background()
	login, err := e.svc.Create(ctx, CreateRequest{OrganizationID: org, Name: "approle", Kind: KindVaultAppRole,
		Secret: map[string]any{"role_id": srvRoleID, "secret_id": srvSecretID}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := e.svc.Vaults().Create(ctx, org, &VaultConnection{Name: "edge", Kind: VaultKindHashiCorp, Address: e.address,
		Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: login.ID, ResolveAt: ResolveAtCollector, Enabled: true,
		CollectorIDs: []string{silent, answering}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got map[string]string
	var replyTo string
	sub, err := nc.Subscribe("collector."+org+"."+answering+".vault.test", func(m *nats.Msg) {
		mu.Lock()
		_ = json.Unmarshal(m.Data, &got)
		replyTo = m.Reply
		mu.Unlock()
		_ = m.Respond([]byte(`{"result":"vault_denied"}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TestVaultConnection(ctx, org, VaultTestParams{ConnectionID: conn.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != "vault_denied" || res.CollectorID != answering || res.ResolveAt != ResolveAtCollector {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if got["connection_id"] != conn.ID || got["path"] != "reticora-test" || len(got) != 2 {
		t.Fatalf("request = %v", got)
	}
	// The reply subject lies in the collector's own tree: the endpoint
	// lets it publish nowhere else.
	if !strings.HasPrefix(replyTo, "collector."+org+"."+answering+".vault.reply.") {
		t.Fatalf("reply subject %q is outside the collector's subject tree", replyTo)
	}
	if requests, _ := e.kv.counts(); requests != 0 {
		t.Fatal("the server read the vault of a collector connection")
	}
	// No collector answers: vault_unreachable.
	_ = sub.Unsubscribe()
	if res, err = e.svc.TestVaultConnection(ctx, org, VaultTestParams{ConnectionID: conn.ID}); err != nil || res.Result != "vault_unreachable" {
		t.Fatalf("no collector = %+v, %v", res, err)
	}

	// Five collectors that receive the request and never answer: the test
	// ends with vault_unreachable before the deadline of its job, not with
	// the job's timeout.
	hanging := make([]string, 5)
	for i := range hanging {
		hanging[i] = fmt.Sprintf("00000000-0000-4000-8000-0000000000b%d", i)
		hsub, herr := nc.Subscribe("collector."+org+"."+hanging[i]+".vault.test", func(*nats.Msg) {})
		if herr != nil {
			t.Fatal(herr)
		}
		defer func() { _ = hsub.Unsubscribe() }()
	}
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	stuck, err := e.svc.Vaults().Create(ctx, org, &VaultConnection{Name: "stuck", Kind: VaultKindHashiCorp, Address: e.address,
		Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: login.ID, ResolveAt: ResolveAtCollector, Enabled: true,
		CollectorIDs: hanging})
	if err != nil {
		t.Fatal(err)
	}
	budget := vaultTestReserve + 700*time.Millisecond
	jctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	start := time.Now()
	res, err = e.svc.TestVaultConnection(jctx, org, VaultTestParams{ConnectionID: stuck.ID})
	if err != nil || res.Result != VaultClassUnreachable {
		t.Fatalf("hanging collectors = %+v, %v", res, err)
	}
	if elapsed := time.Since(start); elapsed >= budget-time.Second {
		t.Fatalf("the test took %v of a %v job budget; the reserve of %v is gone", elapsed, budget, vaultTestReserve)
	}
}

// TestServerResolveForgetsChangedConnection: a PATCH of the mount (or
// address, login) of a connection never serves the value read before;
// deleting the connection drops its values and client.
func TestServerResolveForgetsChangedConnection(t *testing.T) {
	e := newVaultEnv(t, true)
	id := e.ref(t, e.conn, "admp/api", "api_key")
	ctx := context.Background()
	if secret, err := e.svc.Decrypt(ctx, "org-1", id); err != nil || secret["token"] != srvValue {
		t.Fatalf("Decrypt = %v, %v", secret, err)
	}
	if _, err := e.svc.Vaults().Update(ctx, "org-1", e.conn, func(c *VaultConnection) error {
		c.Mount = "other"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if e.resolver.cache.Len() != 0 || len(e.resolver.clients) != 0 {
		t.Fatalf("after the PATCH: cache %d, clients %d", e.resolver.cache.Len(), len(e.resolver.clients))
	}
	if secret, err := e.svc.Decrypt(ctx, "org-1", id); err == nil {
		t.Fatalf("the value of mount kv was served for mount other: %v", secret)
	}
	// The key carries the mount: a value cached for one mount is not hit
	// for another even without the PATCH hook.
	conn := &VaultConnection{ID: e.conn, Address: e.address, Mount: "kv"}
	other := *conn
	other.Mount = "other"
	if vaultSource(conn, vault.Auth{}) == vaultSource(&other, vault.Auth{}) {
		t.Fatal("the cache key does not tell the mounts apart")
	}
}

// TestServerLoginDoesNotBlockOtherOrganizations: a vault that hangs in the
// login of one organization does not delay the resolutions of another.
func TestServerLoginDoesNotBlockOtherOrganizations(t *testing.T) {
	e := newVaultEnv(t, true)
	id := e.ref(t, e.conn, "admp/api", "api_key")
	release := make(chan struct{})
	hung := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer hung.Close()
	defer close(release)
	u, _ := url.Parse(hung.URL)
	ctx := context.Background()
	login, err := e.svc.Create(ctx, CreateRequest{OrganizationID: "org-2", Name: "approle", Kind: KindVaultAppRole,
		Secret: map[string]any{"role_id": srvRoleID, "secret_id": srvSecretID}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := e.svc.Vaults().Create(ctx, "org-2", &VaultConnection{Name: "slow", Kind: VaultKindOpenBao,
		Address: "https://example.com:" + u.Port(), Mount: "kv", AuthMethod: VaultAuthAppRole, AuthCredentialID: login.ID,
		CAPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hung.Certificate().Raw})),
		ResolveAt: ResolveAtServer, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	slow, err := e.svc.Create(ctx, CreateRequest{OrganizationID: "org-2", Name: "slow-ref", Kind: KindAPIToken,
		VaultRef: &VaultRef{ConnectionID: conn.ID, Path: "admp/api", FieldMap: map[string]string{"token": "api_key"}}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = e.svc.Decrypt(ctx, "org-2", slow.ID)
	}()
	<-started
	time.Sleep(200 * time.Millisecond) // the login of org-2 hangs now
	done := make(chan error, 1)
	go func() {
		_, derr := e.svc.Decrypt(ctx, "org-1", id)
		done <- derr
	}()
	select {
	case derr := <-done:
		if derr != nil {
			t.Fatal(derr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the hanging login of org-2 blocked the resolution of org-1")
	}
}
