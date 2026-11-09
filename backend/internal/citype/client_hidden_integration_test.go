package citype_test

// WP-858 (MET-05): CI types hidden per client. A client hides types without
// deactivating them (client_ci_type_hidden, migration 000283); the choice
// belongs to the client, not to the user.
//
//   - Row level security: a client-scoped principal reads and writes only
//     the entries of its clients; the other organization sees none; a
//     private type of another organization cannot be hidden (trigger
//     client_ci_type_hidden_type); deleting the client removes its entries.
//   - GET and PUT /api/v1/clients/{id}/hidden-ci-types: PUT replaces the
//     list and records client.hidden_ci_types_changed with the old and the
//     new list; a client outside the scope and an unknown one are 404, an
//     id that is no UUID 400, a type of another organization 422. System
//     types can be hidden.
//   - GET /api/v1/ci-types?client_id= leaves out the types hidden for the
//     client; without it a client-scoped principal misses the types every
//     client of its scope hides, an org-wide principal gets all;
//     include_inactive=true lists the whole catalog.
//   - POST /api/v1/cis of a hidden type for the client (named, or the
//     client of the location) and the manual type change to it are 422 on
//     ci_type_id; existing CIs of the type stay readable and editable;
//     discovery (CreateMany without ChosenType, reclassification) is not
//     bound by the hiding.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestClientHiddenCITypes' ./internal/citype/

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// hiddenAPI is the router of the test with a request per scope.
type hiddenAPI struct {
	t    *testing.T
	f    *scopetest.Fixture
	mux  *chi.Mux
	user string
	ci   *ci.PGRepository
}

// ctx is the request context of a principal of org; clients restricts the
// client scope (none: org-wide).
func (a *hiddenAPI) ctx(org string, clients ...string) context.Context {
	scope := database.OrgWideScope(org, a.user)
	if len(clients) > 0 {
		scope.Clients = database.ScopeIDs(clients...)
	}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: org, UserID: a.user})
	return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp858", CorrelationID: "wp858", Source: audit.SourceUI})
}

func (a *hiddenAPI) call(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

func (a *hiddenAPI) expect(w *httptest.ResponseRecorder, code int, field string) {
	a.t.Helper()
	if w.Code != code {
		a.t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), code)
	}
	if p := problemOf(w); field != "" && p.Field != field && !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
		a.t.Errorf("problem %s, want field %s", w.Body.String(), field)
	}
}

// typeIDs lists the ids of GET /api/v1/ci-types with query.
func (a *hiddenAPI) typeIDs(ctx context.Context, query string) []string {
	a.t.Helper()
	w := a.call(ctx, http.MethodGet, "/api/v1/ci-types?limit=200"+query, "")
	if w.Code != http.StatusOK {
		a.t.Fatalf("GET /ci-types%s: %d %s", query, w.Code, w.Body.String())
	}
	var page struct {
		Data []citype.Type `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		a.t.Fatal(err)
	}
	var ids []string
	for i := range page.Data {
		ids = append(ids, page.Data[i].ID)
	}
	return ids
}

// hidden reads the hidden types of client through the API.
func (a *hiddenAPI) hidden(ctx context.Context, client string) []string {
	a.t.Helper()
	w := a.call(ctx, http.MethodGet, "/api/v1/clients/"+client+"/hidden-ci-types", "")
	if w.Code != http.StatusOK {
		a.t.Fatalf("GET hidden types of %s: %d %s", client, w.Code, w.Body.String())
	}
	var out tenantapi.HiddenCITypes
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		a.t.Fatal(err)
	}
	return out.CITypeIDs
}

func (a *hiddenAPI) put(ctx context.Context, client string, ids ...string) *httptest.ResponseRecorder {
	a.t.Helper()
	body, _ := json.Marshal(tenantapi.HiddenCITypes{CITypeIDs: append([]string{}, ids...)})
	return a.call(ctx, http.MethodPut, "/api/v1/clients/"+client+"/hidden-ci-types", string(body))
}

// TestClientHiddenCITypesAgainstPostgres covers WP-858 (MET-05) against
// PostgreSQL as the application role.
func TestClientHiddenCITypesAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "d8")
	bg := context.Background()
	repo := citype.NewPGRepository(f.App)
	ciRepo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	a := &hiddenAPI{t: t, f: f, mux: chi.NewRouter(), user: f.AppUser(t, f.OrgA, "wp858-admin"), ci: ciRepo}
	citype.NewHandler(repo).RegisterRoutes(a.mux)
	tenantapi.NewHandler(tenantapi.NewPGRepository(f.App)).RegisterRoutes(a.mux)
	ci.NewHandler(ci.NewService(ciRepo)).RegisterRoutes(a.mux)

	clientB := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', 'wp858-b')`,
		clientB, f.OrgB); err != nil {
		t.Fatal(err)
	}
	orgWide, orgB := a.ctx(f.OrgA), a.ctx(f.OrgB)
	scoped1, scoped12 := a.ctx(f.OrgA, f.Client1), a.ctx(f.OrgA, f.Client1, f.Client2)
	server := systemTypeID(t, f, "server")
	generic := systemTypeID(t, f, "generic_device")
	create := func(ctx context.Context, key string) string {
		w := a.call(ctx, http.MethodPost, "/api/v1/ci-types", `{"key":"`+key+`","name":"`+key+`"}`)
		a.expect(w, http.StatusCreated, "")
		var typ citype.Type
		_ = json.Unmarshal(w.Body.Bytes(), &typ)
		return typ.ID
	}
	ownType := create(orgWide, "wp858_own")
	typeB := create(orgB, "wp858_b")
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	// A CI of the server type in client 1 before the type is hidden.
	existing := f.CI(t, f.OrgA, f.Client1, "wp858-existing")

	t.Run("PUT replaces the list and records the audit entry", func(t *testing.T) {
		a.expect(a.put(orgWide, f.Client1, server, ownType, server), http.StatusOK, "")
		if got, want := a.hidden(orgWide, f.Client1), slices.Sorted(slices.Values([]string{server, ownType})); !slices.Equal(got, want) {
			t.Errorf("hidden types of client 1: %v, want %v", got, want)
		}
		if got := a.hidden(orgWide, f.Client2); len(got) != 0 {
			t.Errorf("hidden types of client 2: %v, want none", got)
		}
		var changes []byte
		if err := f.Admin.QueryRow(bg, `SELECT changes::text FROM audit_log WHERE organization_id = $1
			AND action = 'client.hidden_ci_types_changed' AND resource_id = $2 ORDER BY created_at DESC LIMIT 1`,
			f.OrgA, f.Client1).Scan(&changes); err != nil {
			t.Fatalf("audit entry: %v", err)
		}
		var entry struct {
			IDs struct {
				Old []string `json:"old"`
				New []string `json:"new"`
			} `json:"ci_type_ids"`
		}
		if err := json.Unmarshal(changes, &entry); err != nil || len(entry.IDs.Old) != 0 || len(entry.IDs.New) != 2 {
			t.Errorf("audit changes %s (%v), want old [] and new with both types", changes, err)
		}
		a.expect(a.put(orgWide, f.Client2, ownType), http.StatusOK, "")
		a.expect(a.put(orgWide, f.Client2), http.StatusOK, "")
		if got := a.hidden(orgWide, f.Client2); len(got) != 0 {
			t.Errorf("hidden types of client 2 after the empty list: %v", got)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		a.expect(a.put(orgWide, f.Client1, typeB), http.StatusUnprocessableEntity, "ci_type_ids")
		a.expect(a.put(orgWide, f.Client1, "server"), http.StatusBadRequest, "ci_type_ids")
		a.expect(a.call(orgWide, http.MethodPut, "/api/v1/clients/"+f.Client1+"/hidden-ci-types", `{}`), http.StatusBadRequest, "ci_type_ids")
		a.expect(a.put(orgWide, unknownClient), http.StatusNotFound, "")
		a.expect(a.call(orgWide, http.MethodGet, "/api/v1/clients/"+unknownClient+"/hidden-ci-types", ""), http.StatusNotFound, "")
		// A client outside the scope and one of the other organization.
		a.expect(a.call(scoped1, http.MethodGet, "/api/v1/clients/"+f.Client2+"/hidden-ci-types", ""), http.StatusNotFound, "")
		a.expect(a.put(scoped1, f.Client2, server), http.StatusNotFound, "")
		a.expect(a.call(orgB, http.MethodGet, "/api/v1/clients/"+f.Client1+"/hidden-ci-types", ""), http.StatusNotFound, "")
		a.expect(a.put(orgB, f.Client1), http.StatusNotFound, "")
		if got := a.hidden(orgWide, f.Client1); len(got) != 2 {
			t.Errorf("the refusals changed the list of client 1: %v", got)
		}
		a.expect(a.call(orgWide, http.MethodGet, "/api/v1/ci-types?client_id=client-1", ""), http.StatusBadRequest, "client_id")
	})

	t.Run("row level security", func(t *testing.T) {
		a.expect(a.put(scoped1, f.Client1, server, ownType), http.StatusOK, "")
		insert := func(ctx context.Context, org, client, typ string) error {
			return database.WithRequestTenant(ctx, f.App, org, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO client_ci_type_hidden (organization_id, client_id, ci_type_id) VALUES ($1, $2, $3)`,
					org, client, typ)
				return err
			})
		}
		visible := func(ctx context.Context, org string) int {
			var n int
			if err := database.WithRequestTenant(ctx, f.App, org, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM client_ci_type_hidden`).Scan(&n)
			}); err != nil {
				t.Fatal(err)
			}
			return n
		}
		var pgErr *pgconn.PgError
		if err := insert(scoped1, f.OrgA, f.Client2, server); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("insert for client 2 as a principal of client 1: %v, want 42501", err)
		}
		if err := insert(orgWide, f.OrgA, f.Client2, typeB); !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Errorf("insert of a type of organization B: %v, want 23503", err)
		}
		if err := insert(orgB, f.OrgB, clientB, typeB); err != nil {
			t.Fatalf("insert of organization B: %v", err)
		}
		if err := insert(orgWide, f.OrgA, f.Client2, ownType); err != nil {
			t.Fatalf("insert of client 2: %v", err)
		}
		if n := visible(scoped1, f.OrgA); n != 2 {
			t.Errorf("principal of client 1 sees %d entries, want the 2 of its client", n)
		}
		if n := visible(orgWide, f.OrgA); n != 3 {
			t.Errorf("org-wide principal sees %d entries, want 3", n)
		}
		if n := visible(orgB, f.OrgB); n != 1 {
			t.Errorf("organization B sees %d entries, want its own", n)
		}
		a.expect(a.put(orgWide, f.Client2), http.StatusOK, "")
	})

	t.Run("type list per scope", func(t *testing.T) {
		has := func(ids []string, id string) bool { return slices.Contains(ids, id) }
		if ids := a.typeIDs(orgWide, "&client_id="+f.Client1); has(ids, server) || has(ids, ownType) || !has(ids, generic) {
			t.Errorf("types for client 1 hold server %v, own %v, generic %v; want only generic", has(ids, server), has(ids, ownType), has(ids, generic))
		}
		if ids := a.typeIDs(orgWide, "&client_id="+f.Client2); !has(ids, server) || !has(ids, ownType) {
			t.Errorf("types for client 2 miss a type that client 1 hides: %v", ids)
		}
		if ids := a.typeIDs(orgWide, ""); !has(ids, server) || !has(ids, ownType) {
			t.Error("an org-wide principal misses hidden types without client_id")
		}
		if ids := a.typeIDs(scoped1, ""); has(ids, server) || has(ids, ownType) {
			t.Error("a principal of client 1 gets the types its only client hides")
		}
		if ids := a.typeIDs(scoped12, ""); !has(ids, server) || !has(ids, ownType) {
			t.Error("a principal of clients 1 and 2 misses types client 2 does not hide")
		}
		a.expect(a.put(orgWide, f.Client2, server), http.StatusOK, "")
		if ids := a.typeIDs(scoped12, ""); has(ids, server) || !has(ids, ownType) {
			t.Error("a principal of clients 1 and 2: server is hidden for both, the own type only for client 1")
		}
		a.expect(a.put(orgWide, f.Client2), http.StatusOK, "")
		if ids := a.typeIDs(scoped1, "&include_inactive=true&client_id="+f.Client1); !has(ids, server) || !has(ids, ownType) {
			t.Error("include_inactive=true leaves out hidden types")
		}
		if ids := a.typeIDs(orgB, "&client_id="+f.Client1); !has(ids, server) {
			t.Error("organization B: client_id of another organization filters its list")
		}
	})

	t.Run("creation and type change are refused, existing CIs stay", func(t *testing.T) {
		post := func(ctx context.Context, body string) *httptest.ResponseRecorder {
			return a.call(ctx, http.MethodPost, "/api/v1/cis", body)
		}
		w := post(orgWide, `{"ci_type_id":"`+server+`","name":"wp858-new","client_id":"`+f.Client1+`"}`)
		a.expect(w, http.StatusUnprocessableEntity, "")
		if !strings.Contains(w.Body.String(), "the CI type is hidden for the client") || !strings.Contains(w.Body.String(), "ci_type_id") {
			t.Errorf("422 without the violation on ci_type_id: %s", w.Body.String())
		}
		// The client of the location.
		site, err := locations.NewPGRepository(f.App).Create(orgWide, f.OrgA,
			locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp858-site"})
		if err != nil {
			t.Fatal(err)
		}
		a.expect(post(orgWide, `{"ci_type_id":"`+ownType+`","name":"wp858-loc","location_id":"`+site.ID+`"}`),
			http.StatusUnprocessableEntity, "")
		a.expect(post(scoped1, `{"ci_type_id":"`+server+`","name":"wp858-scoped","client_id":"`+f.Client1+`"}`),
			http.StatusUnprocessableEntity, "")
		if n := count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND name IN ('wp858-new', 'wp858-loc', 'wp858-scoped')`, f.OrgA); n != 0 {
			t.Errorf("%d refused CIs written", n)
		}
		// Another client, no client and another type are accepted.
		a.expect(post(orgWide, `{"ci_type_id":"`+server+`","name":"wp858-c2","client_id":"`+f.Client2+`"}`), http.StatusCreated, "")
		a.expect(post(orgWide, `{"ci_type_id":"`+server+`","name":"wp858-org"}`), http.StatusCreated, "")
		a.expect(post(orgWide, `{"ci_type_id":"`+generic+`","name":"wp858-generic","client_id":"`+f.Client1+`"}`), http.StatusCreated, "")

		// The existing CI of the hidden type stays readable and editable.
		a.expect(a.call(orgWide, http.MethodGet, "/api/v1/cis/"+existing, ""), http.StatusOK, "")
		a.expect(a.call(orgWide, http.MethodPatch, "/api/v1/cis/"+existing, `{"name":"wp858-existing-renamed"}`), http.StatusOK, "")
		if n := count(`SELECT count(*) FROM ci WHERE id = $1 AND ci_type_id = $2 AND name = 'wp858-existing-renamed'`, existing, server); n != 1 {
			t.Error("the existing CI of the hidden type changed its type or kept its name")
		}

		// The manual type change to a hidden type is refused, to another
		// client's CI accepted; discovery is not bound.
		gen1 := f.CI(t, f.OrgA, f.Client1, "wp858-gen1")
		gen2 := f.CI(t, f.OrgA, f.Client2, "wp858-gen2")
		if _, err = f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $1 WHERE id IN ($2, $3)`, generic, gen1, gen2); err != nil {
			t.Fatal(err)
		}
		w = a.call(orgWide, http.MethodPost, "/api/v1/cis/"+gen1+"/type", `{"ci_type_id":"`+server+`"}`)
		a.expect(w, http.StatusUnprocessableEntity, "")
		if !strings.Contains(w.Body.String(), "the CI type is hidden for the client") {
			t.Errorf("type change: %s", w.Body.String())
		}
		a.expect(a.call(orgWide, http.MethodPost, "/api/v1/cis/"+gen2+"/type", `{"ci_type_id":"`+server+`"}`), http.StatusOK, "")
		if _, err = ciRepo.ChangeType(orgWide, f.OrgA, gen1, server, ci.TypeChangeDiscovery); err != nil {
			t.Errorf("discovery reclassification to a hidden type: %v", err)
		}
		// Discovery creates CIs of the type it resolved (no ChosenType).
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: server, Name: "wp858-discovered",
			Status: ci.StatusActive, DiscoverySource: ci.SourceSweep, Attributes: map[string]any{}}
		if _, err = ciRepo.CreateMany(orgWide, f.OrgA, []*ci.Item{item}); err != nil {
			t.Errorf("discovery create of a hidden type: %v", err)
		}
	})

	t.Run("deleting the client removes its entries", func(t *testing.T) {
		if _, err := f.Admin.Exec(bg, `DELETE FROM client WHERE id = $1`, clientB); err != nil {
			t.Fatal(err)
		}
		if n := count(`SELECT count(*) FROM client_ci_type_hidden WHERE client_id = $1`, clientB); n != 0 {
			t.Errorf("%d entries of the deleted client", n)
		}
	})
}

// unknownClient is a well-formed id of no client.
const unknownClient = "00000000-0000-4000-8000-000000000858"
