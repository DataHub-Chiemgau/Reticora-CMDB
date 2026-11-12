package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// aclEnv is the scenario of the rights per IPAM section (IPM-10, WP-910,
// E-214) against PostgreSQL with the application role: organization A
// with the teams A and B, a user in each team and a user without team.
// Section X gets entries (the test decides which), section Z none. Each
// section holds a subnet of client 1 with one address; both were written
// before the first entry.
type aclEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	mux *chi.Mux

	teamA, teamB, userA, userB, userNone string
	sectionX, sectionZ                   string
	subnetX, addrX, subnetZ, addrZ       string
}

func newACLEnv(t *testing.T) *aclEnv {
	t.Helper()
	f := scopetest.Seed(t, "cc")
	e := &aclEnv{t: t, f: f, mux: chi.NewRouter()}
	ipam.NewHandler(ipam.NewPGRepository(f.App)).RegisterRoutes(e.mux)
	e.userA = f.AppUser(t, f.OrgA, "acl-a")
	e.userB = f.AppUser(t, f.OrgA, "acl-b")
	e.userNone = f.AppUser(t, f.OrgA, "acl-none")
	e.teamA = e.scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'acl team a') RETURNING id::text`, f.OrgA)
	e.teamB = e.scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'acl team b') RETURNING id::text`, f.OrgA)
	e.exec(`INSERT INTO team_member (organization_id, team_id, user_id) VALUES ($1, $2, $3), ($1, $4, $5)`,
		f.OrgA, e.teamA, e.userA, e.teamB, e.userB)

	org := e.as(database.OrgWideScope(f.OrgA, e.userNone))
	e.sectionX = e.create(org, "/api/v1/ipam-sections", `{"name":"acl-x"}`)
	e.sectionZ = e.create(org, "/api/v1/ipam-sections", `{"name":"acl-z"}`)
	e.subnetX = e.create(org, "/api/v1/subnets", `{"cidr":"10.210.1.0/24","client_id":"`+f.Client1+`","section_id":"`+e.sectionX+`","name":"acl-subnet-x"}`)
	e.addrX = e.create(org, "/api/v1/ips", `{"address":"10.210.1.10","subnet_id":"`+e.subnetX+`"}`)
	e.subnetZ = e.create(org, "/api/v1/subnets", `{"cidr":"10.210.2.0/24","client_id":"`+f.Client1+`","section_id":"`+e.sectionZ+`","name":"acl-subnet-z"}`)
	e.addrZ = e.create(org, "/api/v1/ips", `{"address":"10.210.2.10","subnet_id":"`+e.subnetZ+`"}`)
	return e
}

func (e *aclEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *aclEnv) scalar(q string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), q, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return out
}

// as is the request context of a principal with scope.
func (e *aclEnv) as(scope database.TenantScope) context.Context {
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: scope.OrgID, UserID: scope.UserID})
}

// user is the request context of an org-wide user.
func (e *aclEnv) user(id string) context.Context {
	return e.as(database.OrgWideScope(e.f.OrgA, id))
}

func (e *aclEnv) call(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// create posts body and returns the id of the created object.
func (e *aclEnv) create(ctx context.Context, path, body string) string {
	e.t.Helper()
	w := e.call(ctx, http.MethodPost, path, body)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("POST %s %s = %d %s", path, body, w.Code, w.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.ID == "" {
		e.t.Fatalf("POST %s: no id in %s", path, w.Body.String())
	}
	return out.ID
}

// grant gives team the level on section through the API as an org-wide
// principal that may write the section.
func (e *aclEnv) grant(ctx context.Context, section, team, level string) string {
	e.t.Helper()
	return e.create(ctx, "/api/v1/ipam-sections/"+section+"/permissions", `{"team_id":"`+team+`","level":"`+level+`"}`)
}

// expect checks the status of a request and, for a list, whether id is in
// the answer.
func (e *aclEnv) expect(ctx context.Context, who, method, path, body string, want int) string {
	e.t.Helper()
	w := e.call(ctx, method, path, body)
	if w.Code != want {
		e.t.Errorf("%s: %s %s = %d %s, want %d", who, method, path, w.Code, w.Body.String(), want)
	}
	return w.Body.String()
}

// reads checks what principal ctx reads of subnet and address: list,
// detail, the addresses of the subnet and the search documents.
func (e *aclEnv) reads(ctx context.Context, scope database.TenantScope, who, subnet, address string, visible bool) {
	e.t.Helper()
	status := http.StatusNotFound
	if visible {
		status = http.StatusOK
	}
	list := e.expect(ctx, who, http.MethodGet, "/api/v1/subnets?limit=200", "", http.StatusOK)
	if strings.Contains(list, subnet) != visible {
		e.t.Errorf("%s: subnet %s in the subnet list = %v, want %v", who, subnet, !visible, visible)
	}
	ips := e.expect(ctx, who, http.MethodGet, "/api/v1/ips?limit=200", "", http.StatusOK)
	if strings.Contains(ips, address) != visible {
		e.t.Errorf("%s: address %s in the address list = %v, want %v", who, address, !visible, visible)
	}
	e.expect(ctx, who, http.MethodGet, "/api/v1/subnets/"+subnet, "", status)
	e.expect(ctx, who, http.MethodGet, "/api/v1/ips/"+address, "", status)
	addrs := e.expect(ctx, who, http.MethodGet, "/api/v1/subnets/"+subnet+"/addresses", "", status)
	if visible && !strings.Contains(addrs, address) {
		e.t.Errorf("%s: address %s missing in the addresses of its subnet: %s", who, address, addrs)
	}
	sections := e.expect(ctx, who, http.MethodGet, "/api/v1/ipam-sections?limit=200", "", http.StatusOK)
	_ = sections
	for _, id := range []string{subnet, address} {
		if got := e.searchVisible(scope, id); got != visible {
			e.t.Errorf("%s: search document of %s visible = %v, want %v", who, id, got, visible)
		}
	}
}

// searchVisible reports whether the search document of entity is readable
// under scope (the documents of the global search, WP-909).
func (e *aclEnv) searchVisible(scope database.TenantScope, entity string) bool {
	e.t.Helper()
	var ok bool
	err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM search_document WHERE organization_id = $1 AND entity_id = $2)`,
			scope.OrgID, entity).Scan(&ok)
	})
	if err != nil {
		e.t.Fatalf("search documents: %v", err)
	}
	return ok
}

// TestSectionACLReadWrite covers IPM-10 (WP-910 AK1–AK5): with an entry of
// team A (write) on section X a user of team A reads and writes its subnet
// and address (list, detail, addresses of the subnet, search), a user of
// team B sees none of them; the section itself disappears for team B. The
// rights are kept through /ipam-sections/{id}/permissions: an unknown
// team is 422 team_id, a second entry of a team 409, a principal
// restricted to clients 403, an invalid level 422 level.
func TestSectionACLReadWrite(t *testing.T) {
	e := newACLEnv(t)
	orgNone := e.user(e.userNone)
	permA := e.grant(orgNone, e.sectionX, e.teamA, "write")

	a, b := e.user(e.userA), e.user(e.userB)
	scopeA, scopeB := database.OrgWideScope(e.f.OrgA, e.userA), database.OrgWideScope(e.f.OrgA, e.userB)
	e.reads(a, scopeA, "team A", e.subnetX, e.addrX, true)
	e.reads(b, scopeB, "team B", e.subnetX, e.addrX, false)
	e.reads(b, scopeB, "team B in section Z", e.subnetZ, e.addrZ, true)
	e.expect(b, "team B", http.MethodGet, "/api/v1/ipam-sections/"+e.sectionX, "", http.StatusNotFound)
	e.expect(b, "team B", http.MethodGet, "/api/v1/ipam-sections/"+e.sectionX+"/permissions", "", http.StatusNotFound)
	e.expect(a, "team A", http.MethodGet, "/api/v1/ipam-sections/"+e.sectionX, "", http.StatusOK)

	// Writes: team A patches, team B finds nothing; team A creates an
	// address in the subnet, team B is refused by the row level security.
	e.expect(a, "team A", http.MethodPatch, "/api/v1/subnets/"+e.subnetX, `{"description":"acl a"}`, http.StatusOK)
	e.expect(b, "team B", http.MethodPatch, "/api/v1/subnets/"+e.subnetX, `{"description":"acl b"}`, http.StatusNotFound)
	e.expect(b, "team B", http.MethodDelete, "/api/v1/ips/"+e.addrX, "", http.StatusNotFound)
	e.expect(a, "team A", http.MethodPost, "/api/v1/ips", `{"address":"10.210.1.11","subnet_id":"`+e.subnetX+`"}`, http.StatusCreated)
	e.expect(b, "team B", http.MethodPost, "/api/v1/subnets", `{"cidr":"10.210.9.0/24","section_id":"`+e.sectionX+`"}`, http.StatusUnprocessableEntity)
	if got := e.scalar(`SELECT section_id::text FROM ip_address WHERE address = '10.210.1.11' AND organization_id = $1`, e.f.OrgA); got != e.sectionX {
		t.Errorf("section of the new address = %s, want %s", got, e.sectionX)
	}

	// The permissions API.
	list := e.expect(a, "team A", http.MethodGet, "/api/v1/ipam-sections/"+e.sectionX+"/permissions", "", http.StatusOK)
	if !strings.Contains(list, permA) || !strings.Contains(list, `"level":"write"`) || !strings.Contains(list, "acl team a") {
		t.Errorf("permissions of section X: %s", list)
	}
	e.expect(a, "team A", http.MethodPost, "/api/v1/ipam-sections/"+e.sectionX+"/permissions",
		`{"team_id":"`+e.teamA+`","level":"read"}`, http.StatusConflict)
	body := e.expect(a, "team A", http.MethodPost, "/api/v1/ipam-sections/"+e.sectionX+"/permissions",
		`{"team_id":"`+e.f.ID()+`","level":"read"}`, http.StatusUnprocessableEntity)
	if !strings.Contains(body, `"field":"team_id"`) {
		t.Errorf("unknown team: %s, want field team_id", body)
	}
	teamOfB := e.scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'acl team org b') RETURNING id::text`, e.f.OrgB)
	e.expect(a, "team A", http.MethodPost, "/api/v1/ipam-sections/"+e.sectionX+"/permissions",
		`{"team_id":"`+teamOfB+`","level":"read"}`, http.StatusUnprocessableEntity)
	e.expect(a, "team A", http.MethodPost, "/api/v1/ipam-sections/"+e.sectionX+"/permissions",
		`{"team_id":"`+e.teamB+`","level":"admin"}`, http.StatusUnprocessableEntity)
	clientA := database.OrgWideScope(e.f.OrgA, e.userA)
	clientA.Clients = database.ScopeIDs(e.f.Client1)
	e.expect(e.as(clientA), "team A restricted to client 1", http.MethodPost, "/api/v1/ipam-sections/"+e.sectionX+"/permissions",
		`{"team_id":"`+e.teamB+`","level":"read"}`, http.StatusForbidden)
	e.expect(e.as(clientA), "team A restricted to client 1", http.MethodDelete,
		"/api/v1/ipam-sections/"+e.sectionX+"/permissions/"+permA, "", http.StatusForbidden)

	// Team B gets read: it reads, but does not write.
	permB := e.grant(a, e.sectionX, e.teamB, "read")
	e.reads(b, scopeB, "team B with read", e.subnetX, e.addrX, true)
	e.expect(b, "team B with read", http.MethodPatch, "/api/v1/ipam-sections/"+e.sectionX+"/permissions/"+permB,
		`{"level":"write"}`, http.StatusForbidden)
	e.expect(a, "team A", http.MethodPatch, "/api/v1/ipam-sections/"+e.sectionX+"/permissions/"+permB,
		`{"level":"write"}`, http.StatusOK)
	e.expect(b, "team B with write", http.MethodPatch, "/api/v1/subnets/"+e.subnetX, `{"description":"acl b"}`, http.StatusOK)
	e.expect(a, "team A", http.MethodDelete, "/api/v1/ipam-sections/"+e.sectionX+"/permissions/"+permB, "", http.StatusNoContent)
	e.expect(a, "team A", http.MethodDelete, "/api/v1/ipam-sections/"+e.sectionX+"/permissions/"+permB, "", http.StatusNotFound)
	e.reads(b, scopeB, "team B without entry", e.subnetX, e.addrX, false)

	// The last entry removed opens the section again.
	e.expect(a, "team A", http.MethodDelete, "/api/v1/ipam-sections/"+e.sectionX+"/permissions/"+permA, "", http.StatusNoContent)
	e.reads(b, scopeB, "team B after the last entry", e.subnetX, e.addrX, true)
}

// TestSectionACLWriteRequiresWrite covers AK6: with level read PATCH and
// DELETE of a subnet or an address of the section are 404 (the row is not
// visible for UPDATE and DELETE), POST of a subnet into the section and of
// an address into its subnet 403 (row level security 42501).
func TestSectionACLWriteRequiresWrite(t *testing.T) {
	e := newACLEnv(t)
	e.grant(e.user(e.userNone), e.sectionX, e.teamA, "read")
	a := e.user(e.userA)
	e.reads(a, database.OrgWideScope(e.f.OrgA, e.userA), "team A with read", e.subnetX, e.addrX, true)
	e.expect(a, "read", http.MethodPatch, "/api/v1/subnets/"+e.subnetX, `{"description":"x"}`, http.StatusNotFound)
	e.expect(a, "read", http.MethodPatch, "/api/v1/subnets/"+e.subnetX, `{"cidr":"10.210.1.0/23"}`, http.StatusNotFound)
	e.expect(a, "read", http.MethodDelete, "/api/v1/subnets/"+e.subnetX, "", http.StatusNotFound)
	e.expect(a, "read", http.MethodPatch, "/api/v1/ips/"+e.addrX, `{"description":"x"}`, http.StatusNotFound)
	e.expect(a, "read", http.MethodDelete, "/api/v1/ips/"+e.addrX, "", http.StatusNotFound)
	e.expect(a, "read", http.MethodPost, "/api/v1/subnets", `{"cidr":"10.210.3.0/24","section_id":"`+e.sectionX+`"}`, http.StatusForbidden)
	e.expect(a, "read", http.MethodPost, "/api/v1/ips", `{"address":"10.210.1.20","subnet_id":"`+e.subnetX+`"}`, http.StatusForbidden)
	// Moving a writable subnet into the read-only section is refused as
	// well (WITH CHECK; PATCH /subnets answers a refused write 404 like
	// every other write it cannot place).
	e.expect(a, "read", http.MethodPatch, "/api/v1/subnets/"+e.subnetZ, `{"section_id":"`+e.sectionX+`"}`, http.StatusNotFound)
	if n := e.scalar(`SELECT count(*)::text FROM subnet WHERE section_id = $1`, e.sectionX); n != "1" {
		t.Errorf("section X holds %s subnets, want 1", n)
	}
	// The subnet in section Z stays writable.
	e.expect(a, "read", http.MethodPatch, "/api/v1/subnets/"+e.subnetZ, `{"description":"z"}`, http.StatusOK)
}

// TestSectionACLDeletedHistory covers E-214 for the history: the history
// of a deleted subnet and of a deleted address of a section with entries
// stays readable for team A and is 404 for team B and a principal without
// team, as it was before the deletion; section Z without entries stays
// readable for everyone.
func TestSectionACLDeletedHistory(t *testing.T) {
	e := newACLEnv(t)
	e.grant(e.user(e.userNone), e.sectionX, e.teamA, "write")
	a := e.user(e.userA)
	// The address first, then its subnet: the address history is reached
	// through the deletion row of its subnet.
	e.expect(a, "team A", http.MethodDelete, "/api/v1/ips/"+e.addrX, "", http.StatusNoContent)
	e.expect(a, "team A", http.MethodDelete, "/api/v1/subnets/"+e.subnetX, "", http.StatusNoContent)
	e.expect(a, "team A", http.MethodDelete, "/api/v1/ips/"+e.addrZ, "", http.StatusNoContent)
	e.expect(a, "team A", http.MethodDelete, "/api/v1/subnets/"+e.subnetZ, "", http.StatusNoContent)
	for _, c := range []struct {
		who     string
		user    string
		visible bool
	}{{"team A", e.userA, true}, {"team B", e.userB, false}, {"no team", e.userNone, false}} {
		want := http.StatusNotFound
		if c.visible {
			want = http.StatusOK
		}
		ctx := e.user(c.user)
		e.expect(ctx, c.who, http.MethodGet, "/api/v1/subnets/"+e.subnetX+"/history", "", want)
		e.expect(ctx, c.who, http.MethodGet, "/api/v1/ips/"+e.addrX+"/history", "", want)
		e.expect(ctx, c.who, http.MethodGet, "/api/v1/subnets/"+e.subnetZ+"/history", "", http.StatusOK)
		e.expect(ctx, c.who, http.MethodGet, "/api/v1/ips/"+e.addrZ+"/history", "", http.StatusOK)
	}
}

// TestSectionACLWithoutEntriesUnchanged covers AK4 and AK6: without
// entries every principal reads and writes as before, a principal without
// team included, and the client scope still applies.
func TestSectionACLWithoutEntriesUnchanged(t *testing.T) {
	e := newACLEnv(t)
	for _, u := range []string{e.userA, e.userB, e.userNone} {
		e.reads(e.user(u), database.OrgWideScope(e.f.OrgA, u), "user "+u, e.subnetX, e.addrX, true)
	}
	client2 := database.OrgWideScope(e.f.OrgA, e.userA)
	client2.Clients = database.ScopeIDs(e.f.Client2)
	e.reads(e.as(client2), client2, "client 2", e.subnetZ, e.addrZ, false)
	none := e.user(e.userNone)
	e.expect(none, "no team", http.MethodPatch, "/api/v1/subnets/"+e.subnetZ, `{"description":"z"}`, http.StatusOK)
	e.expect(none, "no team", http.MethodPost, "/api/v1/ips", `{"address":"10.210.2.11","subnet_id":"`+e.subnetZ+`"}`, http.StatusCreated)
	e.expect(none, "no team", http.MethodDelete, "/api/v1/ips/"+e.addrZ, "", http.StatusNoContent)
	if got := e.scalar(`SELECT count(*)::text FROM ip_address WHERE section_id IS NULL AND subnet_id = $1`, e.subnetZ); got != "0" {
		t.Errorf("%s addresses of subnet Z without section", got)
	}
}

// TestSectionACLServiceAccountFailsClosed covers AK3: a principal without
// team and without team scope (a service account) sees no section with
// entries, and none of its subnets and addresses; the section without
// entries stays readable.
func TestSectionACLServiceAccountFailsClosed(t *testing.T) {
	e := newACLEnv(t)
	e.grant(e.user(e.userNone), e.sectionX, e.teamA, "write")
	// A service account is no app_user: its id has no membership.
	service := database.OrgWideScope(e.f.OrgA, e.f.ID())
	ctx := e.as(service)
	e.reads(ctx, service, "service account", e.subnetX, e.addrX, false)
	e.reads(ctx, service, "service account in section Z", e.subnetZ, e.addrZ, true)
	e.expect(ctx, "service account", http.MethodGet, "/api/v1/ipam-sections/"+e.sectionX, "", http.StatusNotFound)
	// A principal without user id at all is shut out as well.
	anonymous := database.OrgWideScope(e.f.OrgA, "")
	e.reads(e.as(anonymous), anonymous, "principal without user", e.subnetX, e.addrX, false)
}

// TestSectionACLSystemSeesAll covers AK3: under database.WithSystem
// ipam_section_denied returns an empty list; with the system flag in the
// transaction of a principal without team the subnets and addresses of
// every section are readable.
func TestSectionACLSystemSeesAll(t *testing.T) {
	e := newACLEnv(t)
	e.grant(e.user(e.userNone), e.sectionX, e.teamA, "write")
	bg := context.Background()
	var denied []string
	if err := database.WithSystem(bg, e.f.App, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ipam_section_denied('read')::text[]`).Scan(&denied)
	}); err != nil {
		t.Fatal(err)
	}
	if len(denied) != 0 {
		t.Errorf("ipam_section_denied under WithSystem = %v, want empty", denied)
	}
	scope := database.OrgWideScope(e.f.OrgA, e.userNone)
	var plain, system []string
	var subnets, addresses int
	if err := database.WithTenant(bg, e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT ipam_section_denied('read')::text[]`).Scan(&plain); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('`+database.SystemGUC+`', 'on', true)`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT ipam_section_denied('read')::text[]`).Scan(&system); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM subnet WHERE organization_id = $1 AND section_id = $2),
			(SELECT count(*) FROM ip_address WHERE organization_id = $1 AND section_id = $2)`, e.f.OrgA, e.sectionX).Scan(&subnets, &addresses)
	}); err != nil {
		t.Fatal(err)
	}
	if len(plain) != 1 || plain[0] != e.sectionX {
		t.Errorf("ipam_section_denied of a principal without team = %v, want [%s]", plain, e.sectionX)
	}
	if len(system) != 0 || subnets != 1 || addresses != 1 {
		t.Errorf("with app.system: denied %v, %d subnets and %d addresses of section X, want none denied, 1 and 1", system, subnets, addresses)
	}
}

// TestSectionACLAPIKeyTeamScope covers AK3: an API key with the team scope
// of team A, whose owner belongs to no team, reads and writes section X
// like a member of team A; the same key without team scope fails closed,
// and a user whose role assignment is restricted to team A without being
// its member does not see section X.
func TestSectionACLAPIKeyTeamScope(t *testing.T) {
	e := newACLEnv(t)
	e.grant(e.user(e.userNone), e.sectionX, e.teamA, "write")

	key := database.OrgWideScope(e.f.OrgA, e.userNone)
	key.Teams = database.ScopeIDs(e.teamA)
	key.PrincipalType = database.PrincipalTypeAPIKey
	ctx := e.as(key)
	e.reads(ctx, key, "API key of team A", e.subnetX, e.addrX, true)
	e.expect(ctx, "API key of team A", http.MethodPatch, "/api/v1/subnets/"+e.subnetX, `{"description":"key"}`, http.StatusOK)
	e.expect(ctx, "API key of team A", http.MethodPost, "/api/v1/ips", `{"address":"10.210.1.30","subnet_id":"`+e.subnetX+`"}`, http.StatusCreated)

	keyAll := database.OrgWideScope(e.f.OrgA, e.userNone)
	keyAll.PrincipalType = database.PrincipalTypeAPIKey
	e.reads(e.as(keyAll), keyAll, "API key without team scope", e.subnetX, e.addrX, false)

	teamUser := database.OrgWideScope(e.f.OrgA, e.userNone)
	teamUser.Teams = database.ScopeIDs(e.teamA)
	e.reads(e.as(teamUser), teamUser, "user with a role restricted to team A", e.subnetX, e.addrX, false)
}

// TestSectionACLPolicyIsInitPlan covers AK4: the list of denied sections
// is evaluated once per statement (InitPlan), not per row.
func TestSectionACLPolicyIsInitPlan(t *testing.T) {
	e := newACLEnv(t)
	e.grant(e.user(e.userNone), e.sectionX, e.teamA, "write")
	scope := database.OrgWideScope(e.f.OrgA, e.userA)
	for _, q := range []string{
		`EXPLAIN SELECT id FROM subnet WHERE organization_id = $1 ORDER BY cidr LIMIT 50`,
		`EXPLAIN SELECT id FROM ip_address WHERE organization_id = $1 ORDER BY created_at DESC LIMIT 50`,
	} {
		var plan []string
		err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, q, e.f.OrgA)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan = append(plan, line)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(plan, "\n")
		if !strings.Contains(text, "InitPlan") || strings.Contains(text, "SubPlan") {
			t.Errorf("%s: the denied sections are not one InitPlan:\n%s", q, text)
		}
	}
}
