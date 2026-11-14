package credential_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// Rotation on PostgreSQL (CRD-05, WP-897): reload notices to the
// collectors whose bundle holds a changed credential, sent after the commit
// over NATS (TEST_NATS_URL); the age check credential.age_check; the
// rotation event of a vault value read in a new version.

// notices records the bundle.changed messages of the organization by
// collector.
type notices struct {
	mu  sync.Mutex
	got map[string]int
	nc  *nats.Conn
}

func (n *notices) take(t *testing.T) map[string]int {
	t.Helper()
	if err := n.nc.Flush(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.got
	n.got = map[string]int{}
	return out
}

// withRotation lets the service of e notify the collectors over NATS and
// returns the recorder of the notices.
func (e *bindingEnv) withRotation() *notices {
	e.t.Helper()
	natsURL := os.Getenv("TEST_NATS_URL")
	if natsURL == "" {
		if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci {
			e.t.Fatal("TEST_NATS_URL not set in the CI: the reload notices of WP-897 (AK1) are tested over NATS")
		}
		e.t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(nc.Close)
	n := &notices{got: map[string]int{}, nc: nc}
	sub, err := nc.Subscribe("collector."+e.f.OrgA+".*"+credential.BundleChangedSuffix, func(m *nats.Msg) {
		parts := strings.Split(m.Subject, ".")
		n.mu.Lock()
		n.got[parts[2]]++
		n.mu.Unlock()
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err = nc.Flush(); err != nil {
		e.t.Fatal(err)
	}
	e.svc.WithRotation(credential.NewRotation(e.f.App, nc, discovery.NewBundleGate(e.f.App, nil)))
	return n
}

// scopedCollector inserts a collector of clientID (empty: org-wide) with
// an enabled CIDR scope that names only credentialIDs.
func (e *bindingEnv) scopedCollector(name, clientID, cidr string, credentialIDs ...string) string {
	e.t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	id := e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, $3, 'active') RETURNING id::text`,
		e.f.OrgA, client, name)
	e.exec(`INSERT INTO discovery_scope (organization_id, client_id, collector_id, name, kind, target, credential_ids)
		VALUES ($1, $2, $3, $4, 'cidr', $5, $6::uuid[])`, e.f.OrgA, client, id, name+"-scope", cidr, credentialIDs)
	return id
}

func wantNotices(t *testing.T, step string, got, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: notices %v, want %v", step, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: notices %v, want %v", step, got, want)
		}
	}
}

// TestSecretChangeNotifiesAffectedCollectorsOnce covers AK1 of WP-897: a
// PUT of the secret sends exactly one bundle.changed to each collector
// whose bundle holds the credential and none to the others.
func TestSecretChangeNotifiesAffectedCollectorsOnce(t *testing.T) {
	e := newBindingEnv(t)
	n := e.withRotation()
	holder := e.scopedCollector("wp897-holder", "", "10.97.0.0/24", e.credOrg)
	alsoHolder := e.collector("wp897-all", "", "10.97.1.0/24") // the empty list: every credential
	other := e.scopedCollector("wp897-other", e.f.Client1, "10.97.2.0/24", e.cred1)
	n.take(t)
	rec := e.do(http.MethodPut, "/api/v1/credentials/"+e.credOrg+"/secret", `{"secret":{"password":"wp897-new"}}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT secret: %d %s", rec.Code, rec.Body.String())
	}
	wantNotices(t, "PUT secret", n.take(t), map[string]int{holder: 1, alsoHolder: 1})
	// A PATCH of the name changes nothing a bundle holds: no notice, also
	// for the holders of the credential.
	rec = e.do(http.MethodPatch, "/api/v1/credentials/"+e.cred1, `{"name":"wp897-renamed"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
	}
	wantNotices(t, "PATCH of the name", n.take(t), map[string]int{})

	// A PATCH of a reference (WP-894: path, field map) notifies the
	// collector whose bundle holds it, once; the others get nothing.
	login := e.createCredential(`{"name":"wp897-ref-login","kind":"vault_token","secret":{"token":"wp897-ref-token"}}`)
	rec = e.do(http.MethodPost, "/api/v1/vault-connections", `{"name":"wp897-edge","kind":"openbao_kv2",
		"address":"https://vault.example.com","mount":"kv","auth_method":"token","auth_credential_id":"`+login+`",
		"resolve_at":"collector","collector_ids":["`+alsoHolder+`"]}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", rec.Code, rec.Body.String())
	}
	conn := idOf(t, rec)
	ref := e.createCredential(`{"name":"wp897-ref","kind":"snmp_v2c","vault_ref":{"connection_id":"` + conn +
		`","path":"net/sw1","field_map":{"community":"ro"}}}`)
	refHolder := e.scopedCollector("wp897-ref-holder", "", "10.97.3.0/24", ref)
	n.take(t)
	for _, patch := range []string{`{"vault_path":"net/sw2"}`, `{"field_map":{"community":"community"}}`, `{"vault_version":3}`} {
		if rec = e.do(http.MethodPatch, "/api/v1/credentials/"+ref, patch, ""); rec.Code != http.StatusOK {
			t.Fatalf("PATCH reference %s: %d %s", patch, rec.Code, rec.Body.String())
		}
		wantNotices(t, "PATCH of the reference "+patch, n.take(t), map[string]int{refHolder: 1, alsoHolder: 1})
	}
	if rec = e.do(http.MethodPatch, "/api/v1/credentials/"+ref, `{"name":"wp897-ref-renamed"}`, ""); rec.Code != http.StatusOK {
		t.Fatalf("PATCH name of the reference: %d %s", rec.Code, rec.Body.String())
	}
	wantNotices(t, "PATCH of the reference's name", n.take(t), map[string]int{})
	_ = other
}

// TestBindingChangeNotifiesOldAndNewCollectors covers AK1: creating a
// binding notifies the collector that receives the credential with it,
// deleting one the collectors that held it before (and those that still
// hold it), a replacement of the CI's bindings those of the old and of the
// new credential.
func TestBindingChangeNotifiesOldAndNewCollectors(t *testing.T) {
	e := newBindingEnv(t)
	ci3 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp897-ci-3")
	e.setIP(e.ci1, "10.98.1.10")
	e.setIP(ci3, "10.98.2.10")
	n := e.withRotation()
	near := e.scopedCollector("wp897-near", "", "10.98.1.0/24", e.credOrg)
	far := e.scopedCollector("wp897-far", e.f.Client1, "10.98.2.0/24", e.credOrg)
	n.take(t)

	first := e.mustBind(e.cred1, "ci", e.ci1, 100)
	wantNotices(t, "binding to the CI of the near collector", n.take(t), map[string]int{near: 1})
	// The near collector holds cred1 before and after: it is notified as
	// well (AK1: before or after).
	e.mustBind(e.cred1, "ci", ci3, 100)
	wantNotices(t, "binding to the CI of the far collector", n.take(t), map[string]int{near: 1, far: 1})
	if rec := e.do(http.MethodDelete, "/api/v1/credential-bindings/"+first, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE binding: %d %s", rec.Code, rec.Body.String())
	}
	wantNotices(t, "deleting the near binding", n.take(t), map[string]int{near: 1, far: 1})

	// PUT /cis/{id}/credentials: the far CI swaps cred1 for credSNMP.
	credSNMP := e.createCredential(`{"name":"wp897-snmp","kind":"snmp_v3","secret":{"username":"u"}}`)
	rec := e.do(http.MethodPut, "/api/v1/cis/"+ci3+"/credentials",
		`{"bindings":[{"credential_id":"`+credSNMP+`","priority":100}]}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /cis/{id}/credentials: %d %s", rec.Code, rec.Body.String())
	}
	wantNotices(t, "replacing the bindings of the far CI", n.take(t), map[string]int{far: 1})
	// A refused change sends nothing.
	if rec = e.do(http.MethodPost, "/api/v1/credential-bindings",
		`{"credential_id":"`+e.cred1+`","target_kind":"ci","target_id":"`+e.ci2+`"}`, ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("binding to a CI of another client: %d", rec.Code)
	}
	wantNotices(t, "refused binding", n.take(t), map[string]int{})
}

// TestRollbackSendsNothing covers AK1: a change inside a transaction that
// rolls back sends no notice; the same change committed sends one.
func TestRollbackSendsNothing(t *testing.T) {
	e := newBindingEnv(t)
	n := e.withRotation()
	holder := e.scopedCollector("wp897-rb", "", "10.99.0.0/24", e.credOrg)
	n.take(t)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	errRollback := errors.New("rollback")
	err := database.WithRequestTenantTx(ctx, e.f.App, e.f.OrgA, func(ctx context.Context) error {
		if _, rerr := e.svc.ReplaceSecret(ctx, e.f.OrgA, e.credOrg, map[string]any{"password": "wp897-rolled-back"}, nil); rerr != nil {
			return rerr
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("transaction: %v", err)
	}
	wantNotices(t, "rolled back", n.take(t), map[string]int{})
	if v := e.scalar(`SELECT secret_version::text FROM credential WHERE id = $1`, e.credOrg); v != "1" {
		t.Fatalf("secret_version after the rollback = %s", v)
	}
	err = database.WithRequestTenantTx(ctx, e.f.App, e.f.OrgA, func(ctx context.Context) error {
		_, rerr := e.svc.ReplaceSecret(ctx, e.f.OrgA, e.credOrg, map[string]any{"password": "wp897-committed"}, nil)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	wantNotices(t, "committed", n.take(t), map[string]int{holder: 1})
}

// outboxOf returns the data of the events of type for entity, oldest first.
func (e *bindingEnv) outboxOf(typ, entity string) []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = $2 AND entity_id = $3 ORDER BY sequence`, e.f.OrgA, typ, entity)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var data map[string]any
		if err = rows.Scan(&data); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

// TestAgeCheckNotifiesOncePerVersion covers AK4 and AK5: credential.age_check
// reports a stored secret beyond max_age_days once per secret_version
// (credential.rotation_due with id and credential_id, client_id, secret_version,
// max_age_days, due_at) and records it in notice_sent; a replaced secret
// starts its age again, and once it is due again it is reported for its
// new version.
func TestAgeCheckNotifiesOncePerVersion(t *testing.T) {
	e := newBindingEnv(t)
	aged := e.createCredential(`{"name":"wp897-aged","kind":"ssh_password","client_id":"` + e.f.Client1 +
		`","max_age_days":30,"secret":{"password":"wp897-aged"}}`)
	fresh := e.createCredential(`{"name":"wp897-fresh","kind":"ssh_password","max_age_days":30,"secret":{"password":"x"}}`)
	e.exec(`UPDATE credential SET created_at = now() - interval '31 days' WHERE id = $1`, aged)
	e.exec(`UPDATE credential SET created_at = now() - interval '29 days' WHERE id = $1`, fresh)
	check := credential.NewAgeCheck(e.f.App)
	ctx := context.Background()
	for range 2 {
		if _, err := check.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := e.outboxOf(events.CredentialRotationDue, aged)
	if len(got) != 1 {
		t.Fatalf("rotation_due events after two runs = %d, want 1", len(got))
	}
	d := got[0]
	if d["id"] != aged || d["credential_id"] != aged || d["client_id"] != e.f.Client1 || d["secret_version"] != float64(1) || d["max_age_days"] != float64(30) ||
		d["source"] != "stored" || d["due_at"] == nil {
		t.Fatalf("data = %v", d)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "wp897-aged\"") && strings.Contains(string(raw), "password") {
		t.Fatalf("the event carries the secret: %s", raw)
	}
	if n := e.scalar(`SELECT count(*)::text FROM notice_sent WHERE organization_id = $1 AND kind = $2 AND dedup_key = $3`,
		e.f.OrgA, events.CredentialRotationDue, "credential/"+aged+"/1"); n != "1" {
		t.Fatalf("notice_sent rows = %s", n)
	}
	if len(e.outboxOf(events.CredentialRotationDue, fresh)) != 0 {
		t.Fatal("a secret within its age limit was reported")
	}
	// The replacement starts the age again: not due.
	if rec := e.do(http.MethodPut, "/api/v1/credentials/"+aged+"/secret", `{"secret":{"password":"wp897-v2"}}`, ""); rec.Code != http.StatusOK {
		t.Fatalf("PUT secret: %d", rec.Code)
	}
	if _, err := check.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.outboxOf(events.CredentialRotationDue, aged)); n != 1 {
		t.Fatalf("events after the replacement = %d, want still 1", n)
	}
	e.exec(`UPDATE credential SET rotated_at = now() - interval '40 days' WHERE id = $1`, aged)
	for range 2 {
		if _, err := check.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got = e.outboxOf(events.CredentialRotationDue, aged)
	if len(got) != 2 || got[1]["secret_version"] != float64(2) {
		t.Fatalf("events after version 2 is due = %v", got)
	}
}

// rotatingVault is a KV v2 test vault whose entry net/api has the version
// the test sets.
type rotatingVault struct{ version atomic.Int64 }

func (v *rotatingVault) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/token/lookup-self":
		_, _ = w.Write([]byte(`{"data":{"ttl":0}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/kv/data/net/api":
		ver := v.version.Load()
		_, _ = fmt.Fprintf(w, `{"data":{"data":{"key":"wp897-value-%d"},"metadata":{"version":%d}}}`, ver, ver)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type loopbackResolver struct{}

func (loopbackResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// TestVaultVersionChangeEmitsRotated covers AK3: a value without a fixed
// version that the server reads again after its TTL in a new version emits
// credential.rotated with source vault and the new version, once per
// version; the first read and a read of the same version emit nothing.
func TestVaultVersionChangeEmitsRotated(t *testing.T) {
	e := newBindingEnv(t)
	v := &rotatingVault{}
	v.version.Store(1)
	srv := httptest.NewTLSServer(http.HandlerFunc(v.serve))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	resolver := credential.NewVaultResolver(egress.Options{AllowPrivate: true, Resolver: loopbackResolver{}}, nil).
		WithCacheTTL(time.Nanosecond)
	defer resolver.Close()
	e.svc.WithVault(resolver).WithRotation(credential.NewRotation(e.f.App, nil, nil))
	login := e.createCredential(`{"name":"wp897-token","kind":"vault_token","secret":{"token":"wp897-token-value"}}`)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	body, _ := json.Marshal(map[string]any{"name": "wp897-server", "kind": "openbao_kv2", "address": "https://example.com:" + u.Port(),
		"mount": "kv", "auth_method": "token", "auth_credential_id": login, "ca_pem": ca, "resolve_at": "server"})
	rec := e.do(http.MethodPost, "/api/v1/vault-connections", string(body), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", rec.Code, rec.Body.String())
	}
	conn := idOf(t, rec)
	ref := e.createCredential(`{"name":"wp897-ref","kind":"api_token","vault_ref":{"connection_id":"` + conn +
		`","path":"net/api","field_map":{"token":"key"}}}`)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	read := func(want string) {
		t.Helper()
		time.Sleep(time.Millisecond) // past the TTL
		secret, err := e.svc.Decrypt(ctx, e.f.OrgA, ref)
		if err != nil {
			t.Fatal(err)
		}
		if secret["token"] != want {
			t.Fatalf("token = %v, want %s", secret["token"], want)
		}
	}
	read("wp897-value-1")
	read("wp897-value-1")
	if n := len(e.outboxOf(events.CredentialRotated, ref)); n != 0 {
		t.Fatalf("events without a version change = %d", n)
	}
	v.version.Store(2)
	read("wp897-value-2")
	read("wp897-value-2")
	got := e.outboxOf(events.CredentialRotated, ref)
	if len(got) != 1 || got[0]["source"] != "vault" || got[0]["secret_version"] != float64(2) || got[0]["id"] != ref {
		t.Fatalf("events after version 2 = %v", got)
	}
	for _, d := range got {
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "wp897-value") || strings.Contains(string(raw), "wp897-token-value") {
			t.Fatalf("the event carries a value: %s", raw)
		}
	}
	v.version.Store(3)
	read("wp897-value-3")
	if n := len(e.outboxOf(events.CredentialRotated, ref)); n != 2 {
		t.Fatalf("events after version 3 = %d, want 2", n)
	}

	// A restart (or another replica): a fresh rotation knows no version in
	// memory. The version recorded is not reported again, the next one is.
	e.svc.WithRotation(credential.NewRotation(e.f.App, nil, nil))
	read("wp897-value-3")
	if n := len(e.outboxOf(events.CredentialRotated, ref)); n != 2 {
		t.Fatalf("events after a restart without a change = %d, want 2", n)
	}
	v.version.Store(4)
	read("wp897-value-4")
	if n := len(e.outboxOf(events.CredentialRotated, ref)); n != 3 {
		t.Fatalf("events after a restart and version 4 = %d, want 3", n)
	}

	// A read in the scope of one client reports the rotation for every
	// credential of the organization that references the entry, also those
	// of other clients; a credential with a fixed version gets nothing.
	other := e.createCredential(`{"name":"wp897-ref-c2","kind":"api_token","client_id":"` + e.f.Client2 +
		`","vault_ref":{"connection_id":"` + conn + `","path":"net/api","field_map":{"token":"key"}}}`)
	pinned := e.createCredential(`{"name":"wp897-ref-pinned","kind":"api_token","vault_ref":{"connection_id":"` + conn +
		`","path":"net/api","version":1,"field_map":{"token":"key"}}}`)
	read("wp897-value-4") // the baseline of the new credential
	v.version.Store(5)
	clientScope := database.OrgWideScope(e.f.OrgA, e.f.User)
	clientScope.Clients = database.ScopeIDs(e.f.Client1)
	cctx := database.ContextWithTenantScope(context.Background(), &clientScope)
	time.Sleep(time.Millisecond)
	if _, err := e.svc.Decrypt(cctx, e.f.OrgA, ref); err != nil {
		t.Fatal(err)
	}
	if n := len(e.outboxOf(events.CredentialRotated, ref)); n != 4 {
		t.Fatalf("events of the org-wide reference after version 5 = %d, want 4", n)
	}
	if got := e.outboxOf(events.CredentialRotated, other); len(got) != 1 || got[0]["secret_version"] != float64(5) ||
		got[0]["client_id"] != e.f.Client2 {
		t.Fatalf("events of the other client's reference = %v, want one for version 5", got)
	}
	if n := len(e.outboxOf(events.CredentialRotated, pinned)); n != 0 {
		t.Fatalf("a reference with a fixed version got %d rotation events", n)
	}
}
