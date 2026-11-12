package admp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The read API of the ADMP cache (ADM-07, ADM-08; WP-930) on PostgreSQL:
// the client scope of the rows (E-09), the dedupe of refresh=true per
// minute in the job table, 404 for an unknown or invisible interface or
// CI, the org gate of the add-on admp, the statement budget of the CI list
// and no holder in plain text.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -run 'TestAdmpAPI' ./internal/admp/

// apiEnv is organization A with an account of client 1 and an
// organization-wide one, a CI of each client with an interface, and the
// read API on the PostgreSQL stores.
type apiEnv struct {
	t                      *testing.T
	f                      *scopetest.Fixture
	cache                  *admp.PGRepository
	accounts               *admp.PGAccountRepository
	vault                  *pii.Vault
	fake                   *fakeADMP
	jobs                   *jobs.Service
	now                    time.Time
	mux                    *chi.Mux
	account1, accountOrg   string
	ci1, ci2, iface1, ifc2 string
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	f := scopetest.Seed(t, "e8")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	e := &apiEnv{t: t, f: f, cache: admp.NewPGRepository(f.App), accounts: admp.NewPGAccountRepository(f.App),
		vault: pii.NewVault(f.App, enc), fake: newFakeADMP(), now: time.Now().UTC().Truncate(time.Minute).Add(5 * time.Second)}
	credentialID := f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp930-token', 'api_token', '\x00')`,
		credentialID, f.OrgA)
	e.account1 = e.account("wp930-c1", f.Client1, credentialID)
	e.accountOrg = e.account("wp930-org", "", credentialID)
	e.ci1 = f.CI(t, f.OrgA, f.Client1, "wp930-ci-1")
	e.ci2 = f.CI(t, f.OrgA, f.Client2, "wp930-ci-2")
	e.iface1 = e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'ge-0/0/1') RETURNING id::text`,
		f.OrgA, e.ci1)
	e.ifc2 = e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'ge-0/0/2') RETURNING id::text`,
		f.OrgA, e.ci2)
	e.jobs = jobs.NewService(jobs.NewPGRepository(f.App))
	e.jobs.Register(jobs.Kind{Name: admp.RefreshJobKind, MaxAttempts: 3, Timeout: 2 * time.Minute})
	e.mux = e.handler(f.App)
	return e
}

// handler builds the read API on pool (a traced pool for the budget).
func (e *apiEnv) handler(pool *pgxpool.Pool) *chi.Mux {
	cache := admp.NewPGRepository(pool)
	sync := admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: cache, Sources: e.fake, Pool: pool,
		Now: func() time.Time { return e.now }}).WithSubmitter(queueOnly{svc: e.jobs})
	mux := chi.NewRouter()
	admp.NewDataHandler(cache, cache, admp.NewAccountService(e.accounts, nil, egress.Options{}), sync).RegisterRoutes(mux)
	return mux
}

func (e *apiEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *apiEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (e *apiEnv) org() context.Context { return e.f.OrgCtx(e.f.OrgA) }

func (e *apiEnv) account(name, clientID, credentialID string) string {
	e.t.Helper()
	a := &admp.Account{OrganizationID: e.f.OrgA, Name: name, ClientID: clientID, BaseURL: "https://admp.example.com",
		CredentialID: credentialID, RateLimitRPS: 2, TimeoutMS: 10000, Enabled: true}
	if err := e.accounts.CreateAccount(e.org(), a); err != nil {
		e.t.Fatalf("create account %s: %v", name, err)
	}
	return a.ID
}

// line stores a connection of account with a service and its ports.
func (e *apiEnv) line(account, external, carrier string, ports ...admp.ServicePort) *admp.Connection {
	e.t.Helper()
	exp := e.now.Add(30 * 24 * time.Hour)
	c := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: account, ExternalID: external, Carrier: carrier,
		ContractID: "V-" + external, FetchedAt: e.now, ExpiresAt: exp}
	if err := e.cache.UpsertConnection(e.org(), c); err != nil {
		e.t.Fatalf("connection %s: %v", external, err)
	}
	s := &admp.Service{OrganizationID: e.f.OrgA, AccountID: account, ExternalID: "svc-" + external, ConnectionID: c.ID,
		Kind: "internet", FetchedAt: e.now, ExpiresAt: exp}
	if err := e.cache.UpsertService(e.org(), s); err != nil {
		e.t.Fatalf("service %s: %v", external, err)
	}
	if _, err := e.cache.ReplaceServicePorts(e.org(), e.f.OrgA, s.ID, ports); err != nil {
		e.t.Fatalf("ports %s: %v", external, err)
	}
	return c
}

// get serves path as a principal of organization A, restricted to
// clientID when set.
func (e *apiEnv) get(h http.Handler, path, clientID string) *httptest.ResponseRecorder {
	e.t.Helper()
	ctx := e.org()
	if clientID != "" {
		ctx = e.f.ClientCtx(clientID)
	}
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
	return rec
}

func listedIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	ids := []string{}
	for _, d := range page.Data {
		ids = append(ids, d.ID)
	}
	return ids
}

// TestAdmpAPIClientScope: a principal of client 1 reads the connections of
// its client (account of client 1, or ports on its CIs) and the shared ones
// without client (E-09); those of client 2 and the CI and interface of
// client 2 are 404 for it, and the other way round.
func TestAdmpAPIClientScope(t *testing.T) {
	e := newAPIEnv(t)
	own := e.line(e.account1, "c1-line", "Netz AG",
		admp.ServicePort{PortKey: "1", NetworkInterfaceID: e.iface1, MatchedBy: admp.MatchedByRule})
	other := e.line(e.accountOrg, "c2-line", "Netz AG",
		admp.ServicePort{PortKey: "2", NetworkInterfaceID: e.ifc2, MatchedBy: admp.MatchedByRule})
	shared := e.line(e.accountOrg, "shared-line", "Netz AG", admp.ServicePort{PortKey: "3"})

	for _, c := range []struct {
		client, id string
		want       int
	}{
		{e.f.Client1, own.ID, http.StatusOK}, {e.f.Client1, other.ID, http.StatusNotFound}, {e.f.Client1, shared.ID, http.StatusOK},
		{e.f.Client2, own.ID, http.StatusNotFound}, {e.f.Client2, other.ID, http.StatusOK}, {e.f.Client2, shared.ID, http.StatusOK},
		{"", own.ID, http.StatusOK}, {"", other.ID, http.StatusOK},
	} {
		if rec := e.get(e.mux, "/api/v1/admp/connections/"+c.id, c.client); rec.Code != c.want {
			t.Errorf("client %q, connection %s: %d, want %d", c.client, c.id, rec.Code, c.want)
		}
	}
	rec := e.get(e.mux, "/api/v1/cis/"+e.ci1+"/admp-connections", e.f.Client1)
	if ids := listedIDs(t, rec); rec.Code != http.StatusOK || len(ids) != 1 || ids[0] != own.ID {
		t.Errorf("CI of client 1 for client 1: %d %v", rec.Code, ids)
	}
	if rec = e.get(e.mux, "/api/v1/cis/"+e.ci2+"/admp-connections", e.f.Client1); rec.Code != http.StatusNotFound {
		t.Errorf("CI of client 2 for client 1: %d, want 404", rec.Code)
	}
	if rec = e.get(e.mux, "/api/v1/network-interfaces/"+e.ifc2+"/admp", e.f.Client1); rec.Code != http.StatusNotFound {
		t.Errorf("interface of client 2 for client 1: %d, want 404", rec.Code)
	}
	rec = e.get(e.mux, "/api/v1/network-interfaces/"+e.iface1+"/admp", e.f.Client1)
	if ids := listedIDs(t, rec); rec.Code != http.StatusOK || len(ids) != 1 || !strings.Contains(rec.Body.String(), own.ID) {
		t.Errorf("interface of client 1 for client 1: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAdmpAPIRefreshOncePerMinute: refresh=true on the connection queues
// one admp.refresh job per minute; a second request in the minute answers
// 202 with the same job and leaves one row in the job table; the interface
// refresh of a client principal uses the account of its client; an open
// circuit breaker answers 200 with refresh_skipped circuit_open.
func TestAdmpAPIRefreshOncePerMinute(t *testing.T) {
	e := newAPIEnv(t)
	c := e.line(e.account1, "refresh-line", "Netz AG",
		admp.ServicePort{PortKey: "1", NetworkInterfaceID: e.iface1, MatchedBy: admp.MatchedByRule})
	jobOf := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var v struct {
			RefreshJobID string `json:"refresh_job_id"`
		}
		if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.RefreshJobID == "" {
			t.Fatalf("refresh: %d %s, want 202 with refresh_job_id", rec.Code, rec.Body.String())
		}
		return v.RefreshJobID
	}
	path := "/api/v1/admp/connections/" + c.ID + "?refresh=true"
	first := jobOf(e.get(e.mux, path, ""))
	e.now = e.now.Add(30 * time.Second)
	if second := jobOf(e.get(e.mux, path, e.f.Client1)); second != first {
		t.Errorf("second refresh in the minute: job %s, want %s", second, first)
	}
	count := func() string {
		return e.scalar(`SELECT count(*)::text FROM job WHERE organization_id = $1 AND kind = $2 AND params->>'external_id' = 'refresh-line'`,
			e.f.OrgA, admp.RefreshJobKind)
	}
	if n := count(); n != "1" {
		t.Errorf("%s jobs in the minute, want 1", n)
	}
	e.now = e.now.Add(time.Minute)
	if next := jobOf(e.get(e.mux, path, "")); next == first {
		t.Error("the next minute reused the job")
	}
	if n := count(); n != "2" {
		t.Errorf("%s jobs after two minutes, want 2", n)
	}
	iface := jobOf(e.get(e.mux, "/api/v1/network-interfaces/"+e.iface1+"/admp?refresh=true", e.f.Client1))
	if account := e.scalar(`SELECT params->>'account_id' FROM job WHERE id = $1`, iface); account != e.account1 {
		t.Errorf("interface refresh with account %s, want the account of client 1 %s", account, e.account1)
	}
	e.fake.set(func() { e.fake.open = true })
	e.now = e.now.Add(time.Minute)
	rec := e.get(e.mux, path, "")
	var v struct {
		RefreshSkipped string `json:"refresh_skipped"`
		Carrier        string `json:"carrier"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.RefreshSkipped != admp.RefreshSkippedCircuitOpen ||
		v.Carrier != "Netz AG" {
		t.Errorf("open circuit breaker: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAdmpAPIUnknownInterface404: an unknown interface or CI, a malformed
// id and an interface whose CI is deleted are 404.
func TestAdmpAPIUnknownInterface404(t *testing.T) {
	e := newAPIEnv(t)
	unknown := e.f.ID()
	for _, path := range []string{
		"/api/v1/network-interfaces/" + unknown + "/admp",
		"/api/v1/network-interfaces/not-a-uuid/admp",
		"/api/v1/cis/" + unknown + "/admp-connections",
		"/api/v1/admp/connections/" + unknown,
	} {
		if rec := e.get(e.mux, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	if rec := e.get(e.mux, "/api/v1/network-interfaces/"+e.iface1+"/admp", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("interface without services: %d %s, want 200 with an empty list", rec.Code, rec.Body.String())
	}
	e.exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, e.ci1)
	if rec := e.get(e.mux, "/api/v1/network-interfaces/"+e.iface1+"/admp", ""); rec.Code != http.StatusNotFound {
		t.Errorf("interface of a deleted CI: %d, want 404", rec.Code)
	}
}

// TestAdmpAPIFeatureGate: an organization without the add-on admp gets
// 403 feature-not-entitled on all three routes; with it 200.
func TestAdmpAPIFeatureGate(t *testing.T) {
	e := newAPIEnv(t)
	c := e.line(e.accountOrg, "gate-line", "Netz AG",
		admp.ServicePort{PortKey: "1", NetworkInterfaceID: e.iface1, MatchedBy: admp.MatchedByRule})
	bg := context.Background()
	e.exec(`DELETE FROM entitlement WHERE organization_id = $1`, e.f.OrgA)
	svc := entitlement.NewService(entitlement.NewPGRepository(e.f.App), entitlement.Options{Enforce: true})
	if _, err := svc.ProvisionOrganizations(bg, map[string]entitlement.Plan{e.f.OrgA: entitlement.PlanEnterprise}); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/v1/admp/connections/" + c.ID, "/api/v1/network-interfaces/" + e.iface1 + "/admp",
		"/api/v1/cis/" + e.ci1 + "/admp-connections"}
	gated := svc.Middleware(e.mux)
	for _, path := range paths {
		rec := e.get(gated, path, "")
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusForbidden || p.Type != api.ProblemFeatureNotEntitled {
			t.Errorf("without admp GET %s: %d %s, want 403 %s", path, rec.Code, rec.Body.String(), api.ProblemFeatureNotEntitled)
		}
	}
	if _, err := svc.Grant(e.org(), &entitlement.Entitlement{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureADMP,
		Plan: entitlement.PlanEnterprise, Enabled: true, Origin: entitlement.OriginAddon}); err != nil {
		t.Fatal(err)
	}
	gated = entitlement.NewService(entitlement.NewPGRepository(e.f.App), entitlement.Options{Enforce: true}).Middleware(e.mux)
	for _, path := range paths {
		if rec := e.get(gated, path, ""); rec.Code != http.StatusOK {
			t.Errorf("with admp GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// frameStatement reports a statement of the tenant frame (transaction and
// GUCs), which every repository call sends around its own statements.
func frameStatement(sql string) bool {
	s := strings.ToLower(strings.TrimSpace(sql))
	for _, prefix := range []string{"begin", "commit", "rollback", "savepoint", "release", "set "} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return strings.Contains(s, "set_config(")
}

// TestAdmpAPIListByCIStatementBudget: a page of GET /cis/{id}/admp-connections
// costs at most 3 statements besides the tenant frame, the same for 60
// connections as for 2 (ListByCI is one set-based statement).
func TestAdmpAPIListByCIStatementBudget(t *testing.T) {
	e := newAPIEnv(t)
	trace := scopetest.NewQueryTrace(t, e.f.App)
	h := e.handler(trace.Pool)
	count := func() int {
		t.Helper()
		var n int
		stmts := trace.Run(t, func() error {
			rec := e.get(h, "/api/v1/cis/"+e.ci1+"/admp-connections?limit=20", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
			}
			return nil
		})
		for _, s := range stmts {
			if !frameStatement(s.SQL) {
				n++
			}
		}
		return n
	}
	for i := range 2 {
		e.line(e.account1, "budget-a-"+string(rune('a'+i)), "Netz AG",
			admp.ServicePort{PortKey: "1", NetworkInterfaceID: e.iface1, MatchedBy: admp.MatchedByRule})
	}
	count() // warms the connections
	small := count()
	for i := range 58 {
		e.line(e.account1, "budget-b-"+time.Duration(i).String(), "Netz AG",
			admp.ServicePort{PortKey: "1", NetworkInterfaceID: e.iface1, MatchedBy: admp.MatchedByRule})
	}
	large := count()
	if small != large || large > 3 {
		t.Errorf("statements: %d for 2 connections, %d for 60; want the same and at most 3", small, large)
	}
	t.Logf("a page of the CI list: %d statements besides the tenant frame", large)
}

// TestAdmpAPIWithoutHolderInPlaintext: a connection with a holder stored
// in the vault answers holder_subject_id and holder_present, never the
// name; neither the row of admp_connection.
func TestAdmpAPIWithoutHolderInPlaintext(t *testing.T) {
	e := newAPIEnv(t)
	const name = "Erika Musterfrau-wp930"
	var c *admp.Connection
	if err := database.WithRequestTenantTx(e.org(), e.f.App, e.f.OrgA, func(ctx context.Context) error {
		holder, err := e.vault.Create(ctx, e.f.OrgA, pii.KindPerson, pii.Data{"name": name})
		if err != nil {
			return err
		}
		c = &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.accountOrg, ExternalID: "holder-line", Carrier: "Netz AG",
			HolderSubjectID: holder, FetchedAt: e.now, ExpiresAt: e.now.Add(time.Hour)}
		return e.cache.UpsertConnection(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	rec := e.get(e.mux, "/api/v1/admp/connections/"+c.ID, "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, name) || strings.Contains(body, "Musterfrau") {
		t.Errorf("answer %d carries the holder: %s", rec.Code, body)
	}
	if !strings.Contains(body, `"holder_subject_id":"`+c.HolderSubjectID+`"`) || !strings.Contains(body, `"holder_present":true`) {
		t.Errorf("holder surrogate missing: %s", body)
	}
	if row := e.scalar(`SELECT to_jsonb(c)::text FROM admp_connection c WHERE id = $1`, c.ID); strings.Contains(row, "Musterfrau") {
		t.Errorf("admp_connection holds the holder in plain text: %s", row)
	}
}
