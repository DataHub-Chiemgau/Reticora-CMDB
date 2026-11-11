package collectorcmd

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/vault"
)

// Canary values of the vault tests: none may appear in a log line.
const (
	testRoleID   = "role-canary-7f3a"
	testSecretID = "secret-id-canary-91bd"
	testToken    = "vault-token-canary-c0de"
	testValue    = "vault-community-canary-5e1f"
)

// testVault is a KV v2 server with one AppRole, the mount secret and its
// entries (path → version → data); it counts the reads.
type testVault struct {
	mu       sync.Mutex
	entries  map[string]map[int]map[string]any
	reads    int
	logins   int
	requests int
}

func newTestVault(t *testing.T) (*testVault, *httptest.Server) {
	t.Helper()
	v := &testVault{entries: map[string]map[int]map[string]any{
		"network/sw1":   {1: {"snmp_ro": testValue}},
		"network/ssh":   {1: {"user": "ops"}},
		"reticora-test": {1: {"ok": "yes"}},
	}}
	srv := httptest.NewTLSServer(http.HandlerFunc(v.serve))
	t.Cleanup(srv.Close)
	return v, srv
}

func (v *testVault) serve(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.requests++
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		v.logins++
		if body["role_id"] != testRoleID || body["secret_id"] != testSecretID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"auth":{"client_token":%q,"lease_duration":3600,"renewable":true}}`, testToken)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
		if r.Header.Get("X-Vault-Token") != testToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		if path == "network/denied" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		data, ok := v.entries[path][1]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		v.reads++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data, "metadata": map[string]any{"version": 1}}})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (v *testVault) counts() (logins, reads int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.logins, v.reads
}

func (v *testVault) requestCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.requests
}

// vaultConn is a connection of the test vault with the login "login".
func vaultConn(srv *httptest.Server) bundleVaultConnection {
	return bundleVaultConnection{ConnectionID: "conn-1", Kind: "openbao_kv2", Address: srv.URL, Mount: "secret",
		CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})), AuthMethod: "approle",
		LoginID: "login"}
}

func vaultLogin(secretID string) bundleCredential {
	return bundleCredential{ID: "login", Kind: "vault_approle", Source: "stored",
		Secret: map[string]any{"role_id": testRoleID, "secret_id": secretID}}
}

func vaultRefCred(id, kind string, conn bundleVaultConnection, path string, fieldMap map[string]string) bundleCredential {
	return bundleCredential{ID: id, Kind: kind, Source: sourceVaultRef,
		VaultRef: &bundleVaultRef{bundleVaultConnection: conn, Path: path, FieldMap: fieldMap}}
}

// useVaultResolver installs a fresh resolver and an empty bundle for one
// test.
func useVaultResolver(t *testing.T) {
	t.Helper()
	previous := collectorVault
	collectorVault = newVaultResolver()
	t.Cleanup(func() {
		collectorVault.close()
		collectorVault = previous
		collectorBundle.set(nil)
	})
}

// TestBundleWithReferenceResolvesInMemory covers AK3 of WP-895 (CRD-04): a
// bundle with a vault reference and the login of its connection resolves
// in memory; the value lands under the namespace of the kind, so the
// resolution per scope and per target works unchanged, and a second pass
// within the TTL reads the cache, not the vault.
func TestBundleWithReferenceResolvesInMemory(t *testing.T) {
	useVaultResolver(t)
	tv, srv := newTestVault(t)
	conn := vaultConn(srv)
	collectorBundle.set(&bundleContent{
		Credentials: []bundleCredential{
			vaultLogin(testSecretID),
			vaultRefCred("ref", "snmp_v2c", conn, "network/sw1", map[string]string{"community": "snmp_ro"}),
			{ID: "scope-snmp", Kind: "snmp_v2c", Source: "stored", Secret: map[string]any{"community": "scope-community"}},
		},
		Targets: []bundleTarget{{CIID: "ci-1", ScopeID: "s1", ManagementIP: "192.0.2.10", Namespace: "snmp", CredentialID: "ref", Origin: "ci"}},
	})
	if got := collectorBundle.resolve([]string{"ref"}); len(got) != 0 {
		t.Fatalf("an unresolved reference applied: %v", got)
	}
	ctx := context.Background()
	if out := collectorVault.resolveBundle(ctx, collectorBundle); out["ref"] != vaultResultOK {
		t.Fatalf("resolution = %v", out)
	}
	if got := collectorBundle.resolve([]string{"ref", "scope-snmp"}); !maps.Equal(got, map[string]string{"snmp.community": testValue}) {
		t.Fatalf("resolve = %v", got)
	}
	cfg := collectorConfig{}
	scope := &collectorScope{ID: "s1", Kind: "network", CredentialIDs: []string{"scope-snmp"}}
	if got := credentialsForTarget(&cfg, scope, "192.0.2.10", ""); got["snmp.community"] != testValue {
		t.Fatalf("target credentials = %v", got)
	}
	if got := credentialsForTarget(&cfg, scope, "192.0.2.11", ""); got["snmp.community"] != "scope-community" {
		t.Fatalf("other device = %v", got)
	}
	collectorVault.resolveBundle(ctx, collectorBundle)
	if logins, reads := tv.counts(); logins != 1 || reads != 1 {
		t.Fatalf("logins, reads = %d, %d; want 1, 1 (cache within the TTL)", logins, reads)
	}
	// After the TTL the value is read again.
	collectorVault.cache = vault.NewCache(time.Nanosecond, nil)
	collectorVault.resolveBundle(ctx, collectorBundle)
	time.Sleep(time.Millisecond)
	collectorVault.resolveBundle(ctx, collectorBundle)
	if _, reads := tv.counts(); reads != 3 {
		t.Fatalf("reads after expiry = %d, want 3", reads)
	}
}

// TestVaultFailureDisablesOnlyAffectedCredential covers AK4: a denied
// path, a missing entry, a missing field and a refused login disable only
// their credential with their class; the other reference and the stored
// credentials stay usable.
func TestVaultFailureDisablesOnlyAffectedCredential(t *testing.T) {
	useVaultResolver(t)
	_, srv := newTestVault(t)
	conn := vaultConn(srv)
	badConn := conn
	badConn.ConnectionID, badConn.LoginID = "conn-2", "bad-login"
	badLogin := vaultLogin("wrong")
	badLogin.ID = "bad-login"
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{
		vaultLogin(testSecretID), badLogin,
		vaultRefCred("good", "snmp_v2c", conn, "network/sw1", map[string]string{"community": "snmp_ro"}),
		vaultRefCred("denied", "ssh_password", conn, "network/denied", map[string]string{"username": "user", "password": "pass"}),
		vaultRefCred("missing", "redfish", conn, "network/none", map[string]string{"username": "user", "password": "pass"}),
		vaultRefCred("field", "ipmi", conn, "network/ssh", map[string]string{"username": "user", "password": "pass"}),
		vaultRefCred("wmi-ref", "wmi", badConn, "network/sw1", map[string]string{"username": "snmp_ro", "password": "snmp_ro"}),
		{ID: "stored-ssh", Kind: "ssh_password", Source: "stored", Secret: map[string]any{"username": "u", "password": "p"}},
	}})
	out := collectorVault.resolveBundle(context.Background(), collectorBundle)
	want := map[string]string{"good": "ok", "denied": "vault_denied", "missing": "vault_not_found", "field": "field_missing",
		"wmi-ref": "vault_denied"}
	if !maps.Equal(out, want) {
		t.Fatalf("outcomes = %v, want %v", out, want)
	}
	got := collectorBundle.resolve([]string{"denied", "missing", "field", "wmi-ref", "good", "stored-ssh"})
	if !maps.Equal(got, map[string]string{"snmp.community": testValue, "ssh.username": "u", "ssh.password": "p"}) {
		t.Fatalf("resolve = %v", got)
	}
	// A reference that resolved once and then fails is disabled again.
	collectorVault.cache.Close()
	collectorBundle.mu.Lock()
	cred := collectorBundle.creds["good"]
	ref := *cred.VaultRef
	ref.FieldMap = map[string]string{"community": "absent"}
	cred.VaultRef = &ref
	collectorBundle.creds["good"] = cred
	collectorBundle.mu.Unlock()
	if out = collectorVault.resolveBundle(context.Background(), collectorBundle); out["good"] != "field_missing" {
		t.Fatalf("changed map = %v", out["good"])
	}
	if got = collectorBundle.resolve([]string{"good"}); len(got) != 0 {
		t.Fatalf("a failed reference kept its values: %v", got)
	}
}

// TestVaultLogsCarryNoSecret covers AK4: the log lines of resolutions and
// tests, successful or not, carry class, connection and path, never a
// value, the token or the AppRole ids.
func TestVaultLogsCarryNoSecret(t *testing.T) {
	useVaultResolver(t)
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, srv := newTestVault(t)
	conn := vaultConn(srv)
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{
		vaultLogin(testSecretID),
		vaultRefCred("good", "snmp_v2c", conn, "network/sw1", map[string]string{"community": "snmp_ro"}),
		vaultRefCred("denied", "snmp_v2c", conn, "network/denied", map[string]string{"community": "snmp_ro"}),
		vaultRefCred("field", "snmp_v2c", conn, "network/sw1", map[string]string{"community": "absent"}),
	}, Targets: []bundleTarget{{ScopeID: "s1", ManagementIP: "192.0.2.10", Namespace: "snmp", CredentialID: "good", Origin: "ci"}}})
	ctx := context.Background()
	collectorVault.resolveBundle(ctx, collectorBundle)
	credentialsForTarget(&collectorConfig{}, &collectorScope{ID: "s1", Kind: "network"}, "192.0.2.10", "")
	testVaultConnection(ctx, collectorBundle, vaultTestRequest{ConnectionID: "conn-1", Path: "network/denied"})
	out := logs.String()
	if !strings.Contains(out, "error_class=vault_denied") || !strings.Contains(out, "error_class=field_missing") ||
		!strings.Contains(out, "connection_id=conn-1") || !strings.Contains(out, "path=network/denied") {
		t.Fatalf("control: the log lacks the failures:\n%s", out)
	}
	for _, canary := range []string{testValue, testToken, testRoleID, testSecretID} {
		if strings.Contains(out, canary) {
			t.Errorf("the log carries %q", canary)
		}
	}
}

// TestVaultTestReply covers AK5: the collector answers
// collector.<org>.<id>.vault.test with ok or the error class only.
func TestVaultTestReply(t *testing.T) {
	useVaultResolver(t)
	_, srv := newTestVault(t)
	conn := vaultConn(srv)
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{vaultLogin(testSecretID)},
		VaultConnections: []bundleVaultConnection{conn}})
	nc, err := nats.Connect(auditNATSURL(t), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	subject := fmt.Sprintf("collector.vt-%d.c%d", time.Now().UnixNano(), rand.IntN(1_000_000))
	sub, err := serveVaultTests(context.Background(), nc, subject)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	// The reply subject lies in the collector's tree: the endpoint lets the
	// collector publish nowhere else (discovery.UserClaims).
	ask := func(req string) *nats.Msg {
		t.Helper()
		reply := subject + vaultReplyInfix + nats.NewInbox()[len("_INBOX."):]
		rsub, serr := nc.SubscribeSync(reply)
		if serr != nil {
			t.Fatal(serr)
		}
		defer func() { _ = rsub.Unsubscribe() }()
		if serr = nc.PublishRequest(subject+vaultTestSuffix, reply, []byte(req)); serr != nil {
			t.Fatal(serr)
		}
		msg, serr := rsub.NextMsg(10 * time.Second)
		if serr != nil {
			t.Fatalf("no answer to %s: %v", req, serr)
		}
		return msg
	}
	for _, c := range []struct {
		req  string
		want string
	}{
		{`{"connection_id":"conn-1"}`, "ok"},
		{`{"connection_id":"conn-1","path":"network/denied"}`, "vault_denied"},
		{`{"connection_id":"conn-1","path":"network/none"}`, "vault_not_found"},
		{`{"connection_id":"conn-unknown"}`, "vault_unreachable"},
		{`not json`, "vault_unreachable"},
	} {
		msg := ask(c.req)
		var reply map[string]any
		if err = json.Unmarshal(msg.Data, &reply); err != nil {
			t.Fatal(err)
		}
		if len(reply) != 1 || reply["result"] != c.want {
			t.Fatalf("reply to %s = %s, want only result %s", c.req, msg.Data, c.want)
		}
	}
	// A reply subject outside the tree (the server's _INBOX) is not
	// answered: the endpoint would refuse the publication.
	if _, err = nc.Request(subject+vaultTestSuffix, []byte(`{"connection_id":"conn-1"}`), time.Second); err == nil {
		t.Fatal("a request with a reply subject outside the collector's tree was answered")
	}
}

// TestVaultFailureRetriedAfterBackoff: a failed reference is not read again
// on every check but once vaultFailureBackoff passed; a changed login is
// tried at once. An unreachable vault costs one login attempt per pass, not
// one per reference.
func TestVaultFailureRetriedAfterBackoff(t *testing.T) {
	useVaultResolver(t)
	tv, srv := newTestVault(t)
	conn := vaultConn(srv)
	now := time.Now()
	collectorVault.now = func() time.Time { return now }
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{
		vaultLogin(testSecretID),
		vaultRefCred("denied", "snmp_v2c", conn, "network/denied", map[string]string{"community": "snmp_ro"}),
	}})
	ctx := context.Background()
	if out := collectorVault.resolveBundle(ctx, collectorBundle); out["denied"] != "vault_denied" {
		t.Fatalf("first pass = %v", out)
	}
	asked := tv.requestCount()
	for range 3 {
		if out := collectorVault.resolveBundle(ctx, collectorBundle); out["denied"] != "vault_denied" {
			t.Fatalf("pass within the backoff = %v", out)
		}
	}
	if n := tv.requestCount(); n != asked {
		t.Fatalf("the vault was asked %d more times within the backoff", n-asked)
	}
	now = now.Add(vaultFailureBackoff)
	collectorVault.resolveBundle(ctx, collectorBundle)
	if logins, _ := tv.counts(); tv.requestCount() == asked || logins != 1 {
		t.Fatalf("after the backoff: %d requests (before %d), %d logins; want a read with the same login", tv.requestCount(), asked, logins)
	}

	// A vault that fails (503): one login attempt for both references of
	// its connection in a pass.
	var attempts atomic.Int32
	down := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	downConn := vaultConn(down)
	downConn.ConnectionID = "conn-down"
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{
		vaultLogin(testSecretID),
		vaultRefCred("a", "snmp_v2c", downConn, "network/a", map[string]string{"community": "x"}),
		vaultRefCred("b", "snmp_v2c", downConn, "network/b", map[string]string{"community": "x"}),
	}})
	out := collectorVault.resolveBundle(ctx, collectorBundle)
	if out["a"] != "vault_unreachable" || out["b"] != "vault_unreachable" || attempts.Load() != 1 {
		t.Fatalf("failing vault = %v after %d requests, want vault_unreachable for both after one login attempt", out, attempts.Load())
	}
}

// TestVaultCacheFollowsBundle: the values of a reference that left the
// bundle are overwritten in the next pass and the client of its connection
// is closed; a changed mount or login of the connection is another cache
// entry, read anew with the new login.
func TestVaultCacheFollowsBundle(t *testing.T) {
	useVaultResolver(t)
	tv, srv := newTestVault(t)
	conn := vaultConn(srv)
	ref := vaultRefCred("ref", "snmp_v2c", conn, "network/sw1", map[string]string{"community": "snmp_ro"})
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{vaultLogin(testSecretID), ref}})
	ctx := context.Background()
	if out := collectorVault.resolveBundle(ctx, collectorBundle); out["ref"] != vaultResultOK {
		t.Fatalf("resolution = %v", out)
	}
	if collectorVault.cache.Len() != 1 || len(collectorVault.clients) != 1 {
		t.Fatalf("cache %d, clients %d", collectorVault.cache.Len(), len(collectorVault.clients))
	}
	// Another login of the connection: a new read, not the cached value.
	changed := vaultLogin(testSecretID)
	changed.Secret["role_id"] = testRoleID
	changed.Secret["secret_id"] = "other-" + testSecretID
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{changed, ref}})
	if out := collectorVault.resolveBundle(ctx, collectorBundle); out["ref"] != "vault_denied" {
		t.Fatalf("with a login the vault refuses = %v, want vault_denied (no cached value)", out)
	}
	if n := collectorVault.cache.Len(); n != 0 {
		t.Fatalf("the value read with the old login is still cached (%d entries)", n)
	}
	// Another mount: another entry.
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{vaultLogin(testSecretID), ref}})
	collectorVault.resolveBundle(ctx, collectorBundle)
	other := ref
	otherRef := *ref.VaultRef
	otherRef.Mount = "kv"
	other.VaultRef = &otherRef
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{vaultLogin(testSecretID), other}})
	if out := collectorVault.resolveBundle(ctx, collectorBundle); out["ref"] == vaultResultOK {
		t.Fatalf("the value of mount secret was served for mount kv: %v", out)
	}
	// The reference leaves the bundle: its value is overwritten, the
	// client closed.
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{vaultLogin(testSecretID), ref}})
	collectorVault.resolveBundle(ctx, collectorBundle)
	if collectorVault.cache.Len() != 1 {
		t.Fatalf("cache = %d", collectorVault.cache.Len())
	}
	collectorBundle.set(&bundleContent{Credentials: []bundleCredential{vaultLogin(testSecretID)}})
	collectorVault.resolveBundle(ctx, collectorBundle)
	if collectorVault.cache.Len() != 0 || len(collectorVault.clients) != 0 {
		t.Fatalf("after the reference left: cache %d, clients %d", collectorVault.cache.Len(), len(collectorVault.clients))
	}
	_ = tv
}
