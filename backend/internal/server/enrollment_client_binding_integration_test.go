package server_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// TestEnrollmentCodeBindsCreatorClients covers WP-677 (F-406; COL-04,
// TEN-09, E-08) through the production router against PostgreSQL: an
// enrollment code carries the client and site in which its creator may
// register collectors, and the unauthenticated enrollment cannot leave
// them.
//
//   - A principal with the standard role engineer (collector:manage)
//     restricted to client 1 may register collectors in client 1 only
//     (POST /collectors control: client 2 and org-wide 403). Its code is
//     bound to client 1: an enrollment naming client 2 answers 422,
//     registers nothing and leaves the code unused; without client_id the
//     collector lands in client 1 (active, with its certificate) and its
//     creator sees it. A code requested for client 2 is refused (403).
//   - A creator restricted to two clients has to choose one (422 without);
//     the code is bound to the chosen client. A creator holding
//     collector:manage only in client 1 beside an org-wide read role is
//     bound to client 1 (scope of the permission). A creator restricted to
//     a site binds the code to that site and its client; a site of another
//     client is refused.
//   - An org-wide creator's code stays unbound: the collector names any
//     client of the organization or none (organization-wide), as before.
//   - Codes without a recorded binding (minted before migration 000187)
//     are refused (401, fail-closed). collector_enrollment_code_scope refuses
//     an unbound code to a client-scoped transaction.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEnrollmentCodeBindsCreatorClients(t *testing.T) {
	f := scopetest.Seed(t, "a1")
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = enc.UseEphemeralDerivationRoot(); err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	ent := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp677", Entitlements: ent,
		CIService:       ci.NewServiceWithLimits(repos.CI, ent).WithFieldResolver(repos.CIType),
		Credentials:     credential.NewService(repos.Credential, enc),
		OIDC:            identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:          egress.Options{AllowPrivate: true},
		CollectorCA:     discovery.NewCA(f.App, enc),
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.TenantMiddleware(mux)

	all := identity.ScopeSet{All: true}
	ids := func(v ...string) identity.ScopeSet { return identity.ScopeSet{IDs: v} }
	role := func(name string) []identity.Permission {
		keys, ok := permission.StandardRolePermissions(name)
		if !ok {
			t.Fatalf("standard role %s", name)
		}
		out := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			out = append(out, identity.Permission(k))
		}
		return out
	}
	principal := func(name string, perms []identity.Permission, clients, sites identity.ScopeSet) *identity.Principal {
		p := &identity.Principal{Subject: f.AppUser(t, f.OrgA, name), OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: &identity.Scope{Clients: clients, Sites: sites, Teams: all}, Permissions: perms, Credential: "wp677-" + name}
		p.ClientScope = p.Scope.LegacyClientScope()
		return p
	}
	call := func(p *identity.Principal, method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		if p != nil {
			req = req.WithContext(identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	short := func(s string) string {
		if len(s) > 200 {
			return s[:200] + "…"
		}
		return s
	}
	type minted struct {
		Code     string `json:"code"`
		ID       string `json:"id"`
		ClientID string `json:"client_id"`
		SiteID   string `json:"site_id"`
	}
	mint := func(p *identity.Principal, body string) (int, minted, string) {
		t.Helper()
		code, b := call(p, http.MethodPost, "/api/v1/collectors/enrollment-codes", body)
		var m minted
		_ = json.Unmarshal([]byte(b), &m)
		return code, m, b
	}
	mustMint := func(p *identity.Principal, body string) minted {
		t.Helper()
		code, m, b := mint(p, body)
		if code != http.StatusCreated || m.Code == "" {
			t.Fatalf("POST /collectors/enrollment-codes %s by %s: %d %s, want 201", body, p.Credential, code, short(b))
		}
		return m
	}
	csr := func() string {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, cerr := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{}}, k)
		if cerr != nil {
			t.Fatal(cerr)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	}
	type enrolled struct {
		ID          string          `json:"id"`
		ClientID    string          `json:"client_id"`
		SiteID      string          `json:"site_id"`
		Status      string          `json:"status"`
		Certificate json.RawMessage `json:"certificate"`
	}
	enroll := func(raw, name, clientID string, withCSR bool) (int, enrolled, string) {
		t.Helper()
		req := map[string]string{"code": raw, "name": name}
		if clientID != "" {
			req["client_id"] = clientID
		}
		if withCSR {
			req["csr"] = csr()
		}
		body, _ := json.Marshal(req)
		code, b := call(nil, http.MethodPost, "/api/v1/collectors/enroll", string(body))
		var e enrolled
		_ = json.Unmarshal([]byte(b), &e)
		return code, e, b
	}
	collectors := func(name string) int {
		t.Helper()
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM collector WHERE organization_id = $1 AND name = $2`, f.OrgA, name).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}
	unused := func(id string) bool {
		t.Helper()
		var free bool
		if qerr := f.Admin.QueryRow(bg, `SELECT used_at IS NULL FROM collector_enrollment_code WHERE id = $1`, id).Scan(&free); qerr != nil {
			t.Fatalf("code %s: %v", id, qerr)
		}
		return free
	}
	var site1, site2 string
	for _, s := range []struct {
		id     *string
		client string
		name   string
	}{{&site1, f.Client1, "wp677-site-1"}, {&site2, f.Client2, "wp677-site-2"}} {
		if serr := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, s.client, s.name).Scan(s.id); serr != nil {
			t.Fatal(serr)
		}
	}

	eng1 := principal("wp677-eng1", role("engineer"), ids(f.Client1), all)
	if !eng1.Has(identity.PermCollectorManage) {
		t.Fatal("precondition: engineer holds collector:manage")
	}

	// Control: POST /collectors keeps the client-1 engineer in client 1.
	for _, c := range []struct {
		body string
		want int
	}{
		{fmt.Sprintf(`{"name":"wp677-direct-c2","client_id":%q}`, f.Client2), http.StatusForbidden},
		{`{"name":"wp677-direct-org"}`, http.StatusForbidden},
		{fmt.Sprintf(`{"name":"wp677-direct-c1","client_id":%q}`, f.Client1), http.StatusCreated},
	} {
		if code, b := call(eng1, http.MethodPost, "/api/v1/collectors", c.body); code != c.want {
			t.Errorf("control POST /collectors %s as client-1 engineer: %d %s, want %d", c.body, code, short(b), c.want)
		}
	}

	// 1. The client-1 engineer's code is bound to client 1.
	m := mustMint(eng1, `{"label":"wp677-c1"}`)
	if !strings.EqualFold(m.ClientID, f.Client1) {
		t.Errorf("code of the client-1 engineer: client_id %q, want client 1 %s", m.ClientID, f.Client1)
	}
	code, e, b := enroll(m.Code, "wp677-rogue-c2", f.Client2, true)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("enroll with client_id client 2 on a client-1 code: %d %s, want 422", code, short(b))
	}
	if n := collectors("wp677-rogue-c2"); n != 0 {
		t.Errorf("enroll with client_id client 2 registered %d collectors (client %q), want none", n, e.ClientID)
	}
	if !unused(m.ID) {
		t.Errorf("the refused enrollment consumed the code")
	}
	code, e, b = enroll(m.Code, "wp677-c1-collector", "", true)
	if code != http.StatusCreated || !strings.EqualFold(e.ClientID, f.Client1) || e.Status != discovery.CollectorActive || len(e.Certificate) == 0 {
		t.Errorf("enroll without client_id on a client-1 code: %d %s, want 201 active collector of client 1 with certificate", code, short(b))
	}
	if e.ID != "" {
		if code, b = call(eng1, http.MethodGet, "/api/v1/collectors?limit=200", ""); code != http.StatusOK || !strings.Contains(b, e.ID) {
			t.Errorf("the creator does not see the collector its code enrolled: %d", code)
		}
	}
	m = mustMint(eng1, fmt.Sprintf(`{"label":"wp677-c1-same","client_id":%q}`, strings.ToUpper(f.Client1)))
	if code, e, b = enroll(m.Code, "wp677-c1-same", f.Client1, false); code != http.StatusCreated || !strings.EqualFold(e.ClientID, f.Client1) {
		t.Errorf("enroll naming the code's own client: %d %s, want 201 in client 1", code, short(b))
	}

	// A code for another client than the creator's is refused.
	if code, _, b = mint(eng1, fmt.Sprintf(`{"label":"wp677-ask-c2","client_id":%q}`, f.Client2)); code != http.StatusForbidden {
		t.Errorf("client-1 engineer asks for a code of client 2: %d %s, want 403", code, short(b))
	}
	// So is a site of another client.
	if code, _, b = mint(eng1, fmt.Sprintf(`{"label":"wp677-ask-s2","site_id":%q}`, site2)); code != http.StatusUnprocessableEntity {
		t.Errorf("client-1 engineer asks for a code of a client-2 site: %d %s, want 422", code, short(b))
	}

	// 2. A creator of two clients chooses one.
	eng12 := principal("wp677-eng12", role("engineer"), ids(f.Client1, f.Client2), all)
	if code, _, b = mint(eng12, `{"label":"wp677-c12"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("code of a two-client engineer without client_id: %d %s, want 422", code, short(b))
	}
	m = mustMint(eng12, fmt.Sprintf(`{"label":"wp677-c12-c2","client_id":%q}`, f.Client2))
	if code, e, b = enroll(m.Code, "wp677-c12-to-c1", f.Client1, false); code != http.StatusUnprocessableEntity || collectors("wp677-c12-to-c1") != 0 {
		t.Errorf("enroll with client 1 on a code bound to client 2: %d %s, want 422 and none", code, short(b))
	}
	if code, e, b = enroll(m.Code, "wp677-c12-c2", "", false); code != http.StatusCreated || !strings.EqualFold(e.ClientID, f.Client2) {
		t.Errorf("enroll on the code bound to client 2: %d %s, want 201 in client 2", code, short(b))
	}

	// collector:manage in client 1 beside an org-wide read role: the scope
	// of the permission decides, not the union.
	mixed := principal("wp677-mixed", append(role("viewer"), identity.PermCollectorManage), all, all)
	mixed.PermissionScopes = map[identity.Permission]identity.Scope{identity.PermCollectorManage: {Clients: ids(f.Client1), Sites: all, Teams: all}}
	if m = mustMint(mixed, `{"label":"wp677-mixed"}`); !strings.EqualFold(m.ClientID, f.Client1) {
		t.Errorf("code of a creator holding collector:manage in client 1 only: client_id %q, want client 1", m.ClientID)
	}
	if code, _, _ = enroll(m.Code, "wp677-mixed-c2", f.Client2, false); code != http.StatusUnprocessableEntity {
		t.Errorf("enroll with client 2 on that code: %d, want 422", code)
	}

	// 3. A site-restricted creator binds the code to its site and client.
	siteEng := principal("wp677-site-eng", role("engineer"), ids(f.Client1), ids(site1))
	m = mustMint(siteEng, `{"label":"wp677-site"}`)
	if !strings.EqualFold(m.ClientID, f.Client1) || !strings.EqualFold(m.SiteID, site1) {
		t.Errorf("code of a site-1 engineer: client %q site %q, want client 1 and site 1", m.ClientID, m.SiteID)
	}
	if code, e, b = enroll(m.Code, "wp677-site-collector", "", false); code != http.StatusCreated ||
		!strings.EqualFold(e.ClientID, f.Client1) || !strings.EqualFold(e.SiteID, site1) {
		t.Errorf("enroll on the site-1 code: %d %s, want 201 in client 1, site 1", code, short(b))
	}

	// 4. The org-wide creator's code stays unbound, as before.
	admin := principal("wp677-admin", role("org_admin"), all, all)
	m = mustMint(admin, `{"label":"wp677-org"}`)
	if m.ClientID != "" || m.SiteID != "" {
		t.Errorf("code of an org-wide creator bound to client %q site %q, want unbound", m.ClientID, m.SiteID)
	}
	if code, e, b = enroll(m.Code, "wp677-org-c2", f.Client2, false); code != http.StatusCreated || !strings.EqualFold(e.ClientID, f.Client2) {
		t.Errorf("enroll with client 2 on an org-wide code: %d %s, want 201 in client 2", code, short(b))
	}
	m = mustMint(admin, `{"label":"wp677-org-none"}`)
	if code, e, b = enroll(m.Code, "wp677-org-none", "", false); code != http.StatusCreated || e.ClientID != "" {
		t.Errorf("enroll without client_id on an org-wide code: %d %s, want 201 organization-wide", code, short(b))
	}
	m = mustMint(admin, fmt.Sprintf(`{"label":"wp677-org-pick","client_id":%q}`, f.Client2))
	if !strings.EqualFold(m.ClientID, f.Client2) {
		t.Errorf("org-wide creator picks client 2: client_id %q", m.ClientID)
	}

	// 5. A code without a recorded binding is refused.
	legacy := "wp677-legacy-code"
	sum := sha256.Sum256([]byte(legacy))
	if _, err = f.Admin.Exec(bg, `INSERT INTO collector_enrollment_code (organization_id, code_hash, label, expires_at)
		VALUES ($1, $2, 'wp677-legacy', now() + interval '1 hour')`, f.OrgA, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if code, _, b = enroll(legacy, "wp677-legacy", f.Client2, false); code != http.StatusUnauthorized || collectors("wp677-legacy") != 0 {
		t.Errorf("enroll with a code without binding: %d %s, want 401 and nothing registered", code, short(b))
	}

	// The policy refuses an unbound code to a client-scoped transaction.
	repo := discovery.NewPGRepository(f.App)
	unbound := &discovery.EnrollmentCode{OrganizationID: f.OrgA, Label: "wp677-rls", ExpiresAt: time.Now().Add(time.Hour)}
	unbound.SetRawCode("wp677-rls-code")
	if err = repo.CreateEnrollmentCode(f.ClientCtx(f.Client1), unbound); err == nil {
		t.Errorf("a client-1 transaction stored an unbound enrollment code")
	}
}
