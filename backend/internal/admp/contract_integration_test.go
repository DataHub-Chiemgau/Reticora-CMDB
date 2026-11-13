package admp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The contract of the ADMP client against a mock of the documentation
// (ADM-02, E-248, E-255, WP-1077) on PostgreSQL: an account (WP-925) with
// an api_token credential of the credential service, admp.refresh of the
// inventory, a port and connections through the productive source
// (ClientSources), the cache and the read API. The mock answers the
// operations of the documentation with its fixtures; afterwards the only
// non-GET it saw is the login, twice (once more after a simulated 401),
// and no GET outside the allowlist.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -run 'TestAdmpContract' ./internal/admp/

const (
	contractUser     = "svc-reticora-contract"
	contractPassword = "contract-password-secret-7f3a"
)

// contractEnv is the sync environment with an account of client 1 on the
// mock, a CI of client 1 whose hostname is the node name of the
// inventory, and the read API.
type contractEnv struct {
	*syncEnv
	m        *contractMock
	creds    *credential.Service
	sources  *admp.ClientSources
	sync     *admp.Sync
	mux      *chi.Mux
	account  string
	ci       string
	ifaces   map[string]string
	refresh  map[string]admp.RefreshResult
	bodies   map[string]string
	sessions error
}

func newContractEnv(t *testing.T, rps float64) *contractEnv {
	t.Helper()
	e := &contractEnv{syncEnv: newSyncEnv(t), m: newContractMock(t, contractUser, contractPassword),
		refresh: map[string]admp.RefreshResult{}, bodies: map[string]string{}}
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	// Credentials and the personal data vault share the data key of the
	// organization, so both use one master key.
	e.vault = pii.NewVault(e.f.App, enc)
	e.creds = credential.NewService(credential.NewPGRepository(e.f.App), enc)
	cred, err := e.creds.Create(e.org(), credential.CreateRequest{OrganizationID: e.f.OrgA, Name: "wp1077-admp", Kind: credential.KindAPIToken,
		Secret: map[string]any{"username": contractUser, "token": contractPassword}})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	// The account of client 1 (WP-925) points to the mock with the
	// credential; one enabled account per client.
	e.account = e.account1
	e.exec(`UPDATE admp_account SET base_url = $1, credential_id = $2, rate_limit_rps = $3, timeout_ms = 5000 WHERE id = $4`,
		e.m.srv.URL, cred.ID, rps, e.account)
	prev := pii.Default()
	pii.SetDefault(e.vault)
	t.Cleanup(func() { pii.SetDefault(prev) })
	e.sources = admp.NewClientSources(e.creds, egress.Options{AllowPrivate: true})
	e.sync = admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: e.cache, Sources: e.sources, Pool: e.f.App})
	e.mux = chi.NewRouter()
	admp.NewDataHandler(e.cache, e.cache, admp.NewAccountService(e.accounts, nil, egress.Options{}), e.sync).RegisterRoutes(e.mux)
	e.ci, e.ifaces = e.device(e.f.Client1, "OLT-Traunstein-01", "ge-2/1", "ge-2/2")
	return e
}

// run refreshes kind for the account.
func (e *contractEnv) run(kind, external string) admp.RefreshResult {
	e.t.Helper()
	res, err := e.sync.Refresh(e.org(), e.f.OrgA, admp.RefreshParams{AccountID: e.account, Kind: kind, ExternalID: external})
	if err != nil {
		e.t.Fatalf("refresh %s %s: %v", kind, external, err)
	}
	e.refresh[kind+" "+external] = res
	return res
}

// read serves a GET of the read API as an org-wide user of organization A.
func (e *contractEnv) read(path string) *httptest.ResponseRecorder {
	e.t.Helper()
	ctx := tenant.WithTenant(e.org(), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	e.bodies[path] = rec.Body.String()
	return rec
}

// flow is the whole run: the login state, the inventory, port 301 with its
// services 815 and 816, connection 4711 after a simulated expiry of the
// token (401, one more login) and the private connection 4712; then the
// three reads of the API.
func (e *contractEnv) flow() {
	e.t.Helper()
	src, err := e.sources.For(e.org(), e.accountRow())
	if err != nil {
		e.t.Fatal(err)
	}
	e.sessions = src.CheckSession(e.org())
	if e.sessions != nil {
		e.t.Fatalf("login state: %v", e.sessions)
	}
	if res := e.run(admp.KindInventory, ""); res.Result != "ok" || res.MatchResult == nil || res.Matched != 2 || res.Unmatched != 1 {
		e.t.Fatalf("inventory: %+v", res)
	}
	if res := e.run(admp.KindPort, "301"); res.Result != "ok" {
		e.t.Fatalf("port 301: %+v", res)
	}
	e.m.set(func(m *contractMock) { m.expireNext = true })
	if res := e.run(admp.KindConnection, "4711"); res.Result != "ok" || res.Refreshed != 1 {
		e.t.Fatalf("connection 4711 after the expired token: %+v", res)
	}
	if res := e.run(admp.KindConnection, "4712"); res.Result != "ok" {
		e.t.Fatalf("connection 4712: %+v", res)
	}
	e.read("/api/v1/admp/connections/" + e.storedConnectionOf("4711").ID)
	e.read("/api/v1/network-interfaces/" + e.ifaces["ge-2/1"] + "/admp")
	e.read("/api/v1/cis/" + e.ci + "/admp-connections")
}

func (e *contractEnv) accountRow() *admp.Account {
	e.t.Helper()
	a, err := e.accounts.GetAccount(e.org(), e.f.OrgA, e.account)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

// storedConnectionOf is storedConnection of the contract account.
func (e *contractEnv) storedConnectionOf(external string) storedConnection {
	e.t.Helper()
	saved := e.account1
	e.account1 = e.account
	defer func() { e.account1 = saved }()
	return e.storedConnection(external)
}

// cacheText is every row of organization A in the admp tables, the event
// outbox, the audit log and the jobs as text.
func (e *contractEnv) cacheText() string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name LIKE 'admp\_%' ORDER BY table_name`)
	if err != nil {
		e.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			e.t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) < 4 {
		e.t.Fatalf("admp tables %v", tables)
	}
	var b strings.Builder
	for _, table := range append(tables, "event_outbox", "audit_log", "job") {
		b.WriteString(table + ": ")
		b.WriteString(e.scalar(`SELECT COALESCE(string_agg(to_jsonb(t)::text, ' '), '') FROM `+table+` t WHERE organization_id = $1`, e.f.OrgA))
		b.WriteString("\n")
	}
	return b.String()
}

// TestAdmpContractFullFlow: login, login state, inventory, port → services,
// service with properties, connection with carrier and holders, the cache
// and the read API carry every field of the mapping of the plan; the
// ports carry matched_by.
func TestAdmpContractFullFlow(t *testing.T) {
	e := newContractEnv(t, 50)
	e.flow()

	if got, _ := e.accessPort(e.account, "301"); got.Interface != e.ifaces["ge-2/1"] || got.CI != e.ci || got.MatchedBy != admp.MatchedByRule {
		t.Errorf("port 301: %+v", got)
	}
	if got, _ := e.accessPort(e.account, "302"); got.Interface != e.ifaces["ge-2/2"] || got.MatchedBy != admp.MatchedByRule {
		t.Errorf("port 302: %+v", got)
	}
	if got, ok := e.accessPort(e.account, "401"); !ok || got.MatchedBy != admp.MatchedByNone {
		t.Errorf("port 401: %+v %v, want stored unmatched", got, ok)
	}
	node := e.scalar(`SELECT node_external_id || '|' || node_name || '|' || unit_index || '|' || port_index || '|' || COALESCE(protocol, '')
		FROM admp_access_port WHERE account_id = $1 AND external_id = '301'`, e.account)
	if node != "3|olt-traunstein-01|2|1|KEYMILE_SSH" {
		t.Errorf("port 301 location %s", node)
	}

	c := e.storedConnectionOf("4711")
	if c.Carrier != "Netz Chiemgau GmbH" || c.ContractID != "V-2024-0815" || c.AddressText != "Bahnhofstraße 12a, 83278 Traunstein" ||
		c.AddressSubject != "" || c.HolderSubject == "" {
		t.Errorf("connection 4711 %+v", c)
	}
	if c.Properties["carrier_id"] != 9.0 || c.Properties["carrier_short_name"] != "NCG" || c.Properties["carrier_number"] != 4711.0 ||
		c.Properties["created_at"] != "2023-07-22T04:26:40Z" {
		t.Errorf("connection 4711 properties %v", c.Properties)
	}
	private := e.storedConnectionOf("4712")
	if private.AddressText != "" || private.AddressSubject == "" || private.HolderSubject == "" || private.Properties["removed_at"] != "2023-11-14T22:13:20Z" {
		t.Errorf("connection 4712 %+v", private)
	}

	var conn struct {
		ID, ExternalID, Carrier, ContractID, AddressText, AddressForm string
		HolderPresent                                                 bool `json:"holder_present"`
		HolderCount                                                   int  `json:"holder_count"`
		Services                                                      []struct {
			ExternalID string `json:"external_id"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			BookedAt   string `json:"booked_at"`
			Provision  string `json:"provisioned_at"`
			TemplateID *int64 `json:"template_id"`
			Properties []struct {
				Key   string `json:"key"`
				Label string `json:"label"`
				Value any    `json:"value"`
			} `json:"properties"`
			Ports []struct {
				PortKey            string `json:"port_key"`
				NetworkInterfaceID string `json:"network_interface_id"`
				MatchedBy          string `json:"matched_by"`
			} `json:"ports"`
		} `json:"services"`
	}
	body := e.bodies["/api/v1/admp/connections/"+c.ID]
	if err := json.Unmarshal([]byte(body), &conn); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(body), &raw)
	if raw["external_id"] != "4711" || raw["carrier"] != "Netz Chiemgau GmbH" || raw["contract_id"] != "V-2024-0815" ||
		raw["address_form"] != admp.AddressFormBusiness || !conn.HolderPresent || conn.HolderCount != 1 {
		t.Errorf("GET connection %s", body)
	}
	if len(conn.Services) != 1 {
		t.Fatalf("services of connection 4711: %s", body)
	}
	sv := conn.Services[0]
	if sv.ExternalID != "815" || sv.Kind != "FTTH 1000" || sv.BookedAt != "2023-11-14T22:13:20Z" || sv.Provision != "2023-11-17T05:46:40Z" ||
		sv.TemplateID == nil || *sv.TemplateID != 3 {
		t.Errorf("service 815 %s", body)
	}
	keys := []string{}
	for _, p := range sv.Properties {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	if !slices.Equal(keys, []string{"bandwidth_down", "note", "ports", "static_ip", "technology", "vlan"}) {
		t.Errorf("properties of service 815 %v, want the public ones", keys)
	}
	if len(sv.Ports) != 1 || sv.Ports[0].PortKey != "301" || sv.Ports[0].NetworkInterfaceID != e.ifaces["ge-2/1"] ||
		sv.Ports[0].MatchedBy != admp.MatchedByRule {
		t.Errorf("ports of service 815 %+v", sv.Ports)
	}

	var iface struct {
		Data []struct {
			ExternalID string `json:"external_id"`
			Kind       string `json:"kind"`
			Ports      []struct {
				PortKey   string `json:"port_key"`
				MatchedBy string `json:"matched_by"`
			} `json:"ports"`
			Connection *struct {
				ExternalID string `json:"external_id"`
			} `json:"connection"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(e.bodies["/api/v1/network-interfaces/"+e.ifaces["ge-2/1"]+"/admp"]), &iface); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range iface.Data {
		conn := ""
		if d.Connection != nil {
			conn = d.Connection.ExternalID
		}
		matched := ""
		for _, p := range d.Ports {
			matched += p.PortKey + ":" + p.MatchedBy
		}
		got[d.ExternalID] = d.Kind + "|" + conn + "|" + matched
	}
	if got["815"] != "FTTH 1000|4711|301:rule" || got["816"] != "template:5|4713|301:rule" || len(got) != 2 {
		t.Errorf("services at the interface %v", got)
	}

	ids := listedIDs(t, e.read("/api/v1/cis/"+e.ci+"/admp-connections"))
	want := []string{e.storedConnectionOf("4711").ID, e.storedConnectionOf("4713").ID}
	sort.Strings(ids)
	sort.Strings(want)
	if !slices.Equal(ids, want) {
		t.Errorf("connections of the CI %v, want %v (4711, 4713)", ids, want)
	}
}

// TestAdmpContractNoWriteRequests: after the whole run the set of non-GET
// pairs of method and path at the mock is exactly {POST
// /V1/user/authentication} with two requests (one, and one after the
// simulated 401); every other non-GET pair of the documentation has none,
// DELETE /V1/user/authentication (the logout) in particular; no GET left
// the allowlist.
func TestAdmpContractNoWriteRequests(t *testing.T) {
	e := newContractEnv(t, 50)
	e.flow()
	nonGet := e.m.nonGet()
	if len(nonGet) != 1 || nonGet["POST /V1/user/authentication"] != 2 {
		t.Errorf("non-GET requests at the mock %v, want exactly POST /V1/user/authentication twice", nonGet)
	}
	for _, op := range e.m.ops {
		if op.method == http.MethodGet || op.id == "postV1UserAuthentication" {
			continue
		}
		for key, n := range nonGet {
			method, path, _ := strings.Cut(key, " ")
			if o, _, _ := e.m.match(method, path); o != nil && o.id == op.id && n != 0 {
				t.Errorf("%s %s (%s) reached the mock %d times", op.method, op.path, op.id, n)
			}
		}
	}
	if n := e.m.count("DELETE /V1/user/authentication"); n != 0 {
		t.Errorf("the mock saw %d logouts", n)
	}
	if outside := e.m.getsOutsideAllowlist(); len(outside) != 0 {
		t.Errorf("GETs outside the allowlist: %v", outside)
	}
	for _, key := range []string{"GET /V1/user/authentication", "GET /V1/infrastructure/accessnodepicker",
		"GET /V1/infrastructure/port/301/dienstIds", "GET /V1/dienst/815", "GET /V1/dienst/815/properties/public", "GET /V1/anschluss/4711"} {
		if e.m.count(key) == 0 {
			t.Errorf("the run did not read %s: %v", key, e.m.snapshot())
		}
	}
}

// TestAdmpContractRenewOn401: an expired token (401) drops the token, logs
// in once more and repeats the read, which succeeds.
func TestAdmpContractRenewOn401(t *testing.T) {
	e := newContractEnv(t, 50)
	if res := e.run(admp.KindConnection, "4713"); res.Result != "ok" || e.m.count("POST /V1/user/authentication") != 1 {
		t.Fatalf("first read: %+v after %d logins", res, e.m.count("POST /V1/user/authentication"))
	}
	before := e.m.count("GET /V1/anschluss/4711")
	e.m.set(func(m *contractMock) { m.expireNext = true })
	if res := e.run(admp.KindConnection, "4711"); res.Result != "ok" {
		t.Fatalf("read after the expiry: %+v", res)
	}
	if n := e.m.count("POST /V1/user/authentication"); n != 2 {
		t.Errorf("logins %d, want 2 (one more after the 401)", n)
	}
	if n := e.m.count("GET /V1/anschluss/4711") - before; n != 2 {
		t.Errorf("reads of 4711: %d, want 2 (the 401 and the repetition)", n)
	}
	if c := e.storedConnectionOf("4711"); c.Carrier != "Netz Chiemgau GmbH" {
		t.Errorf("connection after the renewal %+v", c)
	}
	// Every token refused: one renewal, then the class auth.
	e.m.set(func(m *contractMock) { m.tokenTTL = -time.Minute })
	e.m.set(func(m *contractMock) { m.expireNext = true })
	if res := e.run(admp.KindConnection, "4713"); res.Result != readonly.ClassAuth {
		t.Errorf("read with every token refused: %+v, want %s", res, readonly.ClassAuth)
	}
}

// TestAdmpContractRateLimit429: a 429 is repeated after its Retry-After,
// at most three times; the token bucket keeps rate_limit_rps between the
// reads of the account.
func TestAdmpContractRateLimit429(t *testing.T) {
	e := newContractEnv(t, 5)
	ctx := e.org()
	src, err := e.sources.For(ctx, e.accountRow())
	if err != nil {
		t.Fatal(err)
	}
	if err = src.CheckSession(ctx); err != nil {
		t.Fatal(err)
	}
	e.m.set(func(m *contractMock) { m.fail429, m.retryAfter = 1, "1" })
	start := len(e.m.getTimes())
	if _, err = src.FetchPortServices(ctx, "301"); err != nil {
		t.Fatalf("read after one 429: %v", err)
	}
	times := e.m.getTimes()[start:]
	if len(times) != 2 || times[1].Sub(times[0]) < 950*time.Millisecond {
		t.Errorf("reads around the 429: %v, want two with the Retry-After of 1 s between", times)
	}
	e.m.set(func(m *contractMock) { m.fail429, m.retryAfter = 10, "1" })
	start = len(e.m.getTimes())
	_, err = src.FetchPortServices(ctx, "301")
	if readonly.ClassOf(err) != readonly.ClassRateLimited {
		t.Errorf("read with lasting 429: %v, want %s", err, readonly.ClassRateLimited)
	}
	times = e.m.getTimes()[start:]
	if len(times) != 4 {
		t.Errorf("%d requests with lasting 429, want 4 (one and three repetitions)", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 950*time.Millisecond {
			t.Errorf("repetition %d after %v, want at least the Retry-After of 1 s", i, gap)
		}
	}
	e.m.set(func(m *contractMock) { m.fail429 = 0 })
	// Token bucket: 5 requests per second, burst 1.
	start = len(e.m.getTimes())
	for range 4 {
		if _, err = src.FetchPortServices(ctx, "301"); err != nil {
			t.Fatal(err)
		}
	}
	times = e.m.getTimes()[start:]
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 180*time.Millisecond {
			t.Errorf("read %d after %v, want at least 200 ms at rate_limit_rps 5", i, gap)
		}
	}
}

// holderPlainValues are values of the holders of 4711 and 4712 and the
// private address of 4712 that never appear outside pii_subject.
var holderPlainValues = []string{"Muster Bau GmbH", "HRB 1234", "Amtsgericht Traunstein", "Mustermann", "Musterfrau", "Hauptstraße 3",
	"Siegsdorf"}

// TestAdmpContractNoPlaintextPII: holders and the private address are only
// in pii_subject: no admp table, event, audit entry, job or answer of the
// read API names them; the surrogates resolve to them.
func TestAdmpContractNoPlaintextPII(t *testing.T) {
	e := newContractEnv(t, 50)
	e.flow()
	text := e.cacheText()
	for _, b := range e.bodies {
		text += "\n" + b
	}
	for _, v := range holderPlainValues {
		if strings.Contains(text, v) {
			t.Errorf("%q outside pii_subject", v)
		}
	}
	c := e.storedConnectionOf("4712")
	s, err := e.vault.Resolve(e.org(), e.f.OrgA, c.HolderSubject)
	if err != nil {
		t.Fatalf("holder subject: %v", err)
	}
	raw, _ := json.Marshal(s)
	if !strings.Contains(string(raw), "Mustermann") {
		t.Errorf("the holder subject of 4712 does not hold the holder: %s", raw)
	}
}

// accessNodeSecrets are the credentials of the network elements and the
// LDAP data of the answers.
var accessNodeSecrets = []string{"node-password-canary", "node-username-canary", "node-iv-canary", "url-canary", "carrier-node-password-canary",
	"carrier-iv-canary", "byids-node-canary", "ldap-dn-canary", "ldap-username-canary", "carrier-ldap-canary", contractPassword}

// TestAdmpContractNoAccessNodeSecrets: neither the credentials of the
// access nodes nor LDAP data nor the login secret, nor any other value
// marked canary of the fixtures, reach the cache, an event, the audit
// log, a job or an answer of the read API.
func TestAdmpContractNoAccessNodeSecrets(t *testing.T) {
	e := newContractEnv(t, 50)
	e.flow()
	text := e.cacheText()
	for _, b := range e.bodies {
		text += "\n" + b
	}
	// The cache and the answers hold the run: the check looks at data.
	for _, v := range []string{"Netz Chiemgau GmbH", "olt-traunstein-01", "FTTH 1000", "bandwidth_down", "admp_access_port: {"} {
		if !strings.Contains(text, v) {
			t.Fatalf("the stored data lack %q", v)
		}
	}
	for _, v := range accessNodeSecrets {
		if strings.Contains(text, v) {
			t.Errorf("%q reached the cache, an event or an answer", v)
		}
	}
	if i := strings.Index(text, "canary"); i >= 0 {
		t.Errorf("a canary value of the fixtures is stored or answered: …%s…", text[max(0, i-80):min(len(text), i+40)])
	}
}
