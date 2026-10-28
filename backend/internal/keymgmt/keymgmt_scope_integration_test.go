package keymgmt_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestKeyRepositoryClientScope runs the key management repository with the
// principal's tenant scope (TEN-06, WP-019): a principal restricted to client
// 1 neither sees a client-2 key nor its handovers and cannot issue it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestKeyRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "36")
	holder := f.AppUser(t, f.OrgA, "holder")
	repo := keymgmt.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "key-c1", KeyType: "physical", Status: "available"}
	foreign := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "key-c2", KeyType: "physical", Status: "available"}
	for _, k := range []*keymgmt.Item{own, foreign} {
		if err := repo.Create(orgCtx, k); err != nil {
			t.Fatalf("org-wide key %s: %v", k.Name, err)
		}
	}
	if _, err := repo.Issue(orgCtx, &keymgmt.Assignment{OrganizationID: f.OrgA, KeyItemID: foreign.ID, AssignedTo: holder}); err != nil {
		t.Fatalf("org-wide issue: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, _, err := repo.List(ctx, f.OrgA, keymgmt.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 keys: %+v err=%v, want only the own one", list, err)
	}
	if handovers, listErr := repo.ListAssignments(ctx, f.OrgA, foreign.ID); listErr != nil || len(handovers) != 0 {
		t.Fatalf("client-1 handovers of client-2 key: %+v err=%v, want none", handovers, listErr)
	}
	if _, err = repo.Return(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal took back a client-2 key")
	}
	if _, err = repo.Issue(ctx, &keymgmt.Assignment{OrganizationID: f.OrgA, KeyItemID: own.ID, AssignedTo: holder}); err != nil {
		t.Fatalf("client-1 issue of own key: %v", err)
	}
	var status string
	if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM key_item WHERE id = $1`, foreign.ID).Scan(&status); err != nil || status != "issued" {
		t.Fatalf("client-2 key: status=%q err=%v, want issued", status, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, keymgmt.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// dbLeak matches database internals in a response body: SQLSTATE codes,
// PostgreSQL error texts, constraint, policy, relation and type names.
var dbLeak = regexp.MustCompile(`(?i)sqlstate|constraint|policy|violat|row-level|relation|_fkey|_pkey|_key\b|duplicate key|foreign key|syntax for type|invalid input|pgx|pgconn|error:|key_item|key_assignment|client_id`)

// TestKeyHandlerErrorsAreSanitized carries the probe
// TestAuditProbeKeymgmtRepoError (WP-019, MGT-03) as a regression test
// through the key management handler with PostgreSQL: for a principal
// restricted to client 1 a client-2 key answers 404 (also for issue and
// return, which must not tell an issued key apart), an invalid reference 404
// or (as an org-wide principal) 422 and an invalid id 400 or 404, never with
// an SQLSTATE, constraint or policy text; a new key outside the client-1
// scope is refused with 403. The client-2 key and its handover stay
// unchanged; the own key answers 2xx. Issuing a key to a user of another
// organization is refused like issuing it to an unknown user:
// TestAuditKeyIssueStaysInOrganization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestKeyHandlerErrorsAreSanitized(t *testing.T) {
	f := scopetest.Seed(t, "36")
	holder := f.AppUser(t, f.OrgA, "key-h-holder")
	const unknown = "c0de36a0-0000-4000-8000-00000000dead"
	repo := keymgmt.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "key-h-c1", KeyType: "physical", Status: "available"}
	foreign := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "key-h-c2", KeyType: "physical", Status: "available"}
	foreignFree := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "key-h-c2-free", KeyType: "physical", Status: "available"}
	for _, k := range []*keymgmt.Item{own, foreign, foreignFree} {
		if err := repo.Create(orgCtx, k); err != nil {
			t.Fatalf("org-wide key %s: %v", k.Name, err)
		}
	}
	if _, err := repo.Issue(orgCtx, &keymgmt.Assignment{OrganizationID: f.OrgA, KeyItemID: foreign.ID, AssignedTo: holder}); err != nil {
		t.Fatalf("org-wide issue: %v", err)
	}

	mux := chi.NewRouter()
	keymgmt.NewHandler(repo).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, holder)
	scope.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, holder)
	callAs := func(scope *database.TenantScope, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		return w
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return callAs(&scope, method, path, body)
	}
	issue := `{"assigned_to":"` + holder + `"}`

	for _, c := range []struct {
		name, method, path, body string
		want                     int
		orgWide                  bool
	}{
		// Foreign row: the client-2 keys do not exist for client 1.
		{"get client-2 key", http.MethodGet, "/api/v1/keys/" + foreign.ID, "", http.StatusNotFound, false},
		{"patch client-2 key", http.MethodPatch, "/api/v1/keys/" + foreign.ID, `{"name":"hijacked"}`, http.StatusNotFound, false},
		{"delete client-2 key", http.MethodDelete, "/api/v1/keys/" + foreign.ID, "", http.StatusNotFound, false},
		{"issue issued client-2 key", http.MethodPost, "/api/v1/keys/" + foreign.ID + "/issue", issue, http.StatusNotFound, false},
		{"issue available client-2 key", http.MethodPost, "/api/v1/keys/" + foreignFree.ID + "/issue", issue, http.StatusNotFound, false},
		{"return issued client-2 key", http.MethodPost, "/api/v1/keys/" + foreign.ID + "/return", "", http.StatusNotFound, false},
		{"return available client-2 key", http.MethodPost, "/api/v1/keys/" + foreignFree.ID + "/return", "", http.StatusNotFound, false},
		{"move own key to client 2", http.MethodPatch, "/api/v1/keys/" + own.ID, `{"client_id":"` + f.Client2 + `"}`, http.StatusNotFound, false},
		// A key of client 2, or of an unknown client, is a write outside
		// the client-1 scope: refused without telling the two apart.
		{"key of client 2", http.MethodPost, "/api/v1/keys", `{"name":"ghost","client_id":"` + f.Client2 + `"}`, http.StatusForbidden, false},
		{"key of an unknown client (client 1)", http.MethodPost, "/api/v1/keys", `{"name":"ghost","client_id":"` + unknown + `"}`, http.StatusForbidden, false},
		// Invalid reference.
		{"key of an unknown client", http.MethodPost, "/api/v1/keys", `{"name":"ghost","client_id":"` + unknown + `"}`, http.StatusUnprocessableEntity, true},
		{"issue to an unknown user", http.MethodPost, "/api/v1/keys/" + own.ID + "/issue", `{"assigned_to":"` + unknown + `"}`, http.StatusNotFound, false},
		{"issue an unknown key", http.MethodPost, "/api/v1/keys/" + unknown + "/issue", issue, http.StatusNotFound, false},
		// Invalid id.
		{"get invalid id", http.MethodGet, "/api/v1/keys/not-a-uuid", "", http.StatusNotFound, false},
		{"patch invalid id", http.MethodPatch, "/api/v1/keys/not-a-uuid", `{"name":"x"}`, http.StatusNotFound, false},
		{"issue invalid id", http.MethodPost, "/api/v1/keys/not-a-uuid/issue", issue, http.StatusNotFound, false},
		{"return invalid id", http.MethodPost, "/api/v1/keys/not-a-uuid/return", "", http.StatusNotFound, false},
		{"assignments of an invalid id", http.MethodGet, "/api/v1/keys/not-a-uuid/assignments", "", http.StatusBadRequest, false},
		{"key with an invalid client id", http.MethodPost, "/api/v1/keys", `{"name":"ghost","client_id":"not-a-uuid"}`, http.StatusBadRequest, true},
	} {
		as := &scope
		if c.orgWide {
			as = &orgWide
		}
		w := callAs(as, c.method, c.path, c.body)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
		if dbLeak.MatchString(w.Body.String()) {
			t.Errorf("%s: body discloses database internals: %s", c.name, w.Body.String())
		}
	}

	// The client-2 keys and the handover are unchanged.
	bg := context.Background()
	for id, want := range map[string]string{foreign.ID: "key-h-c2/issued", foreignFree.ID: "key-h-c2-free/available"} {
		var got string
		if err := f.Admin.QueryRow(bg, `SELECT name || '/' || status FROM key_item WHERE id = $1`, id).Scan(&got); err != nil || got != want {
			t.Errorf("client-2 key %s: %q err=%v, want %q", id, got, err, want)
		}
	}
	var open, all int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE returned_at IS NULL), count(*) FROM key_assignment WHERE key_item_id IN ($1, $2)`, foreign.ID, foreignFree.ID).Scan(&open, &all); err != nil || open != 1 || all != 1 {
		t.Errorf("client-2 handovers: open=%d all=%d err=%v, want the one org-wide handover", open, all, err)
	}
	var client string
	if err := f.Admin.QueryRow(bg, `SELECT client_id::text FROM key_item WHERE id = $1`, own.ID).Scan(&client); err != nil || client != f.Client1 {
		t.Errorf("own key: client=%s err=%v, want client 1", client, err)
	}
	var ghosts int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM key_item WHERE organization_id = $1 AND name = 'ghost'`, f.OrgA).Scan(&ghosts); err != nil || ghosts != 0 {
		t.Errorf("refused keys: %d err=%v, want none", ghosts, err)
	}
	if w := call(http.MethodGet, "/api/v1/keys/"+foreign.ID+"/assignments", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), holder) {
		t.Errorf("handovers of the client-2 key: %d %s, want 200 without the handover", w.Code, w.Body.String())
	}

	// Positive control: the own key.
	if w := call(http.MethodGet, "/api/v1/keys/"+own.ID, ""); w.Code != http.StatusOK {
		t.Errorf("get own key: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, "/api/v1/keys/"+own.ID, `{"name":"key-h-c1-renamed"}`); w.Code != http.StatusOK {
		t.Errorf("patch own key: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/keys/"+own.ID+"/issue", issue); w.Code != http.StatusCreated {
		t.Errorf("issue own key: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/keys/"+own.ID+"/return", ""); w.Code != http.StatusOK {
		t.Errorf("return own key: %d %s", w.Code, w.Body.String())
	}
	w := call(http.MethodPost, "/api/v1/keys", `{"name":"key-h-c1-new","client_id":"`+f.Client1+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create own key: %d %s", w.Code, w.Body.String())
	}
	var created keymgmt.Item
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ClientID != f.Client1 {
		t.Errorf("created key: %+v err=%v, want client 1", created, err)
	}
}

// TestAuditKeyIssueStaysInOrganization was found by WP-538
// (t1-ev-module-isolation, MGT-03, TEN-06, TEN-09; F-366): POST
// /keys/{id}/issue accepted an assigned_to of another organization, because
// the foreign key key_assignment.assigned_to → app_user(id) did not include
// the organization and the repository did not check it; an organization-A
// principal handed an organization-A key to an organization-B user (201) and
// told an existing user of another organization apart from an unknown id
// (201 versus 404). Since migration 000177 (WP-639) the references of
// key_assignment include the organization: the handover to an
// organization-B user answers exactly like the handover to an unknown user
// (status and body, without database internals), for a client-1 and an
// org-wide principal, no row is written and the key stays available.
//
// Retest (F-366):
//
//	cd backend && go test -count=1 -v -run 'StaysInOrganization' ./internal/keymgmt/
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditKeyIssueStaysInOrganization(t *testing.T) {
	f := scopetest.Seed(t, "36")
	holder := f.AppUser(t, f.OrgA, "key-x-holder")
	holderB := f.AppUser(t, f.OrgB, "key-x-holder-b")
	const unknown = "c0de36a0-0000-4000-8000-00000000dead"
	repo := keymgmt.NewPGRepository(f.App)
	keys := map[string]*keymgmt.Item{}
	for _, name := range []string{"client 1", "org-wide"} {
		k := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "key-x " + name, KeyType: "physical", Status: "available"}
		if err := repo.Create(f.OrgCtx(f.OrgA), k); err != nil {
			t.Fatalf("key: %v", err)
		}
		keys[name] = k
	}

	mux := chi.NewRouter()
	keymgmt.NewHandler(repo).RegisterRoutes(mux)
	client1 := database.OrgWideScope(f.OrgA, holder)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, holder)
	issue := func(scope *database.TenantScope, keyID, assignedTo string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/"+keyID+"/issue", strings.NewReader(`{"assigned_to":"`+assignedTo+`"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		return w
	}
	for name, scope := range map[string]*database.TenantScope{"client 1": &client1, "org-wide": &orgWide} {
		foreign := issue(scope, keys[name].ID, holderB)
		unknownUser := issue(scope, keys[name].ID, unknown)
		if foreign.Code != http.StatusNotFound {
			t.Errorf("%s: issue to a user of organization B: status %d, want 404: %s", name, foreign.Code, foreign.Body.String())
		}
		if foreign.Code != unknownUser.Code || problemWithoutTrace(t, foreign) != problemWithoutTrace(t, unknownUser) {
			t.Errorf("%s: issue to a user of organization B answers %d %s, an unknown user %d %s; want the same", name, foreign.Code, foreign.Body.String(), unknownUser.Code, unknownUser.Body.String())
		}
		if dbLeak.MatchString(foreign.Body.String()) {
			t.Errorf("%s: body discloses database internals: %s", name, foreign.Body.String())
		}
	}
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM key_assignment WHERE assigned_to = $1`, holderB).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A handovers to the organization-B user: %d err=%v, want none", n, err)
	}
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM key_item WHERE organization_id = $1 AND status <> 'available'`, f.OrgA).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A keys no longer available: %d err=%v, want none", n, err)
	}

	// Positive control: the handover to a user of the own organization.
	if w := issue(&client1, keys["client 1"].ID, holder); w.Code != http.StatusCreated {
		t.Errorf("issue to a user of the own organization: %d %s, want 201", w.Code, w.Body.String())
	}
}

// problemWithoutTrace returns the problem body of w without its trace_id,
// which differs per request, so that two refusals can be compared.
func problemWithoutTrace(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %q: %v", w.Body.String(), err)
	}
	delete(p, "trace_id")
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("encode problem: %v", err)
	}
	return string(out)
}
