package desk_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestDeskRepositoryClientScope runs the desk repository with the
// principal's tenant scope (TEN-06, WP-019): a principal restricted to client
// 1 neither sees nor books a desk on a client-2 site. A principal restricted
// to the client-1 site, alone or together with client 1, does not see or book
// the desk on the client-2 site either (WP-340).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeskRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "35")
	ownRoom := f.Room(t, f.Client1, "desk-room-c1")
	foreignRoom := f.Room(t, f.Client2, "desk-room-c2")
	user := f.AppUser(t, f.OrgA, "booker")

	repo := desk.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownDesk := &desk.Desk{OrganizationID: f.OrgA, RoomID: ownRoom, Name: "desk-c1", Status: "available", Attributes: map[string]any{}}
	foreignDesk := &desk.Desk{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "desk-c2", Status: "available", Attributes: map[string]any{}}
	for _, d := range []*desk.Desk{ownDesk, foreignDesk} {
		if err := repo.Create(orgCtx, d); err != nil {
			t.Fatalf("org-wide desk %s: %v", d.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	list, _, err := repo.List(ctx, f.OrgA, desk.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != ownDesk.ID {
		t.Fatalf("client-1 desks: %+v err=%v, want only the own one", list, err)
	}
	start := time.Now().UTC().Add(time.Hour)
	booking := func(deskID string) *desk.Booking {
		return &desk.Booking{OrganizationID: f.OrgA, DeskID: deskID, UserID: user, StartsAt: start, EndsAt: start.Add(time.Hour)}
	}
	if _, err = repo.Book(ctx, booking(foreignDesk.ID)); err == nil {
		t.Fatal("client-1 principal booked a desk on a client-2 site")
	}
	if _, err = repo.Book(ctx, booking(ownDesk.ID)); err != nil {
		t.Fatalf("client-1 booking of own desk: %v", err)
	}
	if err = repo.Create(ctx, &desk.Desk{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "intruder", Status: "available", Attributes: map[string]any{}}); err == nil {
		t.Fatal("client-1 principal placed a desk on a client-2 site")
	}
	if err = repo.Delete(ctx, f.OrgA, foreignDesk.ID); err == nil {
		t.Fatal("client-1 principal deleted a desk on a client-2 site")
	}

	// Site-scoped principals.
	var ownSite string
	if err = f.Admin.QueryRow(context.Background(), `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, ownRoom).Scan(&ownSite); err != nil {
		t.Fatalf("site of the client-1 room: %v", err)
	}
	scoped := func(clients database.ScopeSet) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Clients, scope.Sites = clients, database.ScopeIDs(ownSite)
		return database.ContextWithTenantScope(context.Background(), &scope)
	}
	for name, sctx := range map[string]context.Context{
		"site(s1)":           scoped(database.AllScopes()),
		"client+site(c1,s1)": scoped(database.ScopeIDs(f.Client1)),
	} {
		list, _, err = repo.List(sctx, f.OrgA, desk.FilterParams{}, api.PaginationParams{Limit: 100})
		if err != nil || len(list) != 1 || list[0].ID != ownDesk.ID {
			t.Fatalf("%s desks: %+v err=%v, want only the site-1 desk", name, list, err)
		}
		if _, err = repo.GetByID(sctx, f.OrgA, foreignDesk.ID); err == nil {
			t.Fatalf("%s principal read the desk on site 2", name)
		}
		if _, err = repo.Book(sctx, booking(foreignDesk.ID)); err == nil {
			t.Fatalf("%s principal booked the desk on site 2", name)
		}
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk WHERE room_id = $1`, foreignRoom).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 desks: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, desk.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// dbLeak matches database internals in a response body: SQLSTATE codes,
// PostgreSQL error texts, constraint, policy, relation and type names.
var dbLeak = regexp.MustCompile(`(?i)sqlstate|constraint|policy|violat|row-level|relation|_fkey|_pkey|_key\b|duplicate key|foreign key|syntax for type|invalid input|pgx|pgconn|error:|desk_booking|room_id`)

// TestDeskHandlerErrorsAreSanitized carries the probes TestVerifyB03desk and
// TestAuditProbeDeskRepoError (WP-019, MGT-03) as regression tests through
// the desk handler with PostgreSQL: for a principal restricted to client 1 a
// desk or booking on a client-2 site answers 404, an invalid reference 404 or
// (as an org-wide principal) 422 and an invalid id 400 or 404, never with an
// SQLSTATE, constraint or policy text; a new desk outside the client-1 scope
// is refused with 403. The client-2 rows stay unchanged; the own desk
// answers 2xx. A booking for a user of another organization is refused like
// one for an unknown user: TestAuditDeskBookingStaysInOrganization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeskHandlerErrorsAreSanitized(t *testing.T) {
	f := scopetest.Seed(t, "35")
	ownRoom := f.Room(t, f.Client1, "desk-h-room-c1")
	foreignRoom := f.Room(t, f.Client2, "desk-h-room-c2")
	user := f.AppUser(t, f.OrgA, "desk-h-booker")
	const unknown = "c0de35a0-0000-4000-8000-00000000dead"

	repo := desk.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownDesk := &desk.Desk{OrganizationID: f.OrgA, RoomID: ownRoom, Name: "desk-h-c1", Status: "available", Attributes: map[string]any{}}
	foreignDesk := &desk.Desk{OrganizationID: f.OrgA, RoomID: foreignRoom, Name: "desk-h-c2", Status: "available", Attributes: map[string]any{}}
	for _, d := range []*desk.Desk{ownDesk, foreignDesk} {
		if err := repo.Create(orgCtx, d); err != nil {
			t.Fatalf("org-wide desk %s: %v", d.Name, err)
		}
	}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	foreignBooking, err := repo.Book(orgCtx, &desk.Booking{OrganizationID: f.OrgA, DeskID: foreignDesk.ID, UserID: user, StartsAt: start, EndsAt: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("org-wide booking of the client-2 desk: %v", err)
	}

	mux := chi.NewRouter()
	desk.NewHandler(repo).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, user)
	scope.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, user)
	callAs := func(scope *database.TenantScope, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		return w
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return callAs(&scope, method, path, body)
	}
	slot := func(h int) string {
		s := start.Add(time.Duration(h) * time.Hour)
		return `{"starts_at":"` + s.Format(time.RFC3339) + `","ends_at":"` + s.Add(time.Hour).Format(time.RFC3339) + `"}`
	}

	for _, c := range []struct {
		name, method, path, body string
		want                     int
		orgWide                  bool
	}{
		// Foreign row: the client-2 desk and booking do not exist for client 1.
		{"get client-2 desk", http.MethodGet, "/api/v1/desks/" + foreignDesk.ID, "", http.StatusNotFound, false},
		{"patch client-2 desk", http.MethodPatch, "/api/v1/desks/" + foreignDesk.ID, `{"name":"hijacked"}`, http.StatusNotFound, false},
		{"delete client-2 desk", http.MethodDelete, "/api/v1/desks/" + foreignDesk.ID, "", http.StatusNotFound, false},
		{"book client-2 desk", http.MethodPost, "/api/v1/desks/" + foreignDesk.ID + "/bookings", slot(2), http.StatusNotFound, false},
		{"cancel client-2 booking", http.MethodPost, "/api/v1/desk-bookings/" + foreignBooking.ID + "/cancel", "", http.StatusNotFound, false},
		{"move own desk into a client-2 room", http.MethodPatch, "/api/v1/desks/" + ownDesk.ID, `{"room_id":"` + foreignRoom + `"}`, http.StatusNotFound, false},
		// A desk in a client-2 room, and one in an unknown room (it would be
		// org-wide), is a write outside the client-1 scope: refused without
		// telling the two apart.
		{"desk in a client-2 room", http.MethodPost, "/api/v1/desks", `{"name":"ghost","room_id":"` + foreignRoom + `"}`, http.StatusForbidden, false},
		{"desk in an unknown room (client 1)", http.MethodPost, "/api/v1/desks", `{"name":"ghost","room_id":"` + unknown + `"}`, http.StatusForbidden, false},
		// Invalid reference.
		{"desk in an unknown room", http.MethodPost, "/api/v1/desks", `{"name":"ghost","room_id":"` + unknown + `"}`, http.StatusUnprocessableEntity, true},
		{"booking for an unknown user", http.MethodPost, "/api/v1/desks/" + ownDesk.ID + "/bookings", `{"user_id":"` + unknown + `",` + slot(4)[1:], http.StatusNotFound, false},
		{"booking of an unknown desk", http.MethodPost, "/api/v1/desks/" + unknown + "/bookings", slot(6), http.StatusNotFound, false},
		// Invalid id.
		{"get invalid id", http.MethodGet, "/api/v1/desks/not-a-uuid", "", http.StatusNotFound, false},
		{"patch invalid id", http.MethodPatch, "/api/v1/desks/not-a-uuid", `{"name":"x"}`, http.StatusNotFound, false},
		{"bookings of an invalid id", http.MethodGet, "/api/v1/desks/not-a-uuid/bookings", "", http.StatusBadRequest, false},
		{"cancel invalid booking id", http.MethodPost, "/api/v1/desk-bookings/not-a-uuid/cancel", "", http.StatusNotFound, false},
		{"desk with an invalid room id", http.MethodPost, "/api/v1/desks", `{"name":"ghost","room_id":"not-a-uuid"}`, http.StatusBadRequest, false},
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

	// The client-2 rows are unchanged.
	var name, room, status string
	if err = f.Admin.QueryRow(context.Background(), `SELECT name, room_id::text FROM desk WHERE id = $1`, foreignDesk.ID).Scan(&name, &room); err != nil || name != "desk-h-c2" || room != foreignRoom {
		t.Errorf("client-2 desk: name=%q room=%s err=%v, want unchanged", name, room, err)
	}
	if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM desk_booking WHERE id = $1`, foreignBooking.ID).Scan(&status); err != nil || status != "active" {
		t.Errorf("client-2 booking: status=%q err=%v, want active", status, err)
	}
	var n int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk_booking WHERE desk_id = $1`, foreignDesk.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("bookings of the client-2 desk: %d err=%v, want only the org-wide one", n, err)
	}
	if err = f.Admin.QueryRow(context.Background(), `SELECT room_id::text FROM desk WHERE id = $1`, ownDesk.ID).Scan(&room); err != nil || room != ownRoom {
		t.Errorf("own desk: room=%s err=%v, want the client-1 room", room, err)
	}
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk WHERE organization_id = $1 AND name = 'ghost'`, f.OrgA).Scan(&n); err != nil || n != 0 {
		t.Errorf("desks with an invalid room: %d err=%v, want none", n, err)
	}

	// The client-2 bookings are not listed for client 1.
	if w := call(http.MethodGet, "/api/v1/desks/"+foreignDesk.ID+"/bookings", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), foreignBooking.ID) {
		t.Errorf("bookings of the client-2 desk: %d %s, want 200 without the booking", w.Code, w.Body.String())
	}

	// Positive control: the own desk.
	if w := call(http.MethodGet, "/api/v1/desks/"+ownDesk.ID, ""); w.Code != http.StatusOK {
		t.Errorf("get own desk: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, "/api/v1/desks/"+ownDesk.ID, `{"name":"desk-h-c1-renamed"}`); w.Code != http.StatusOK {
		t.Errorf("patch own desk: %d %s", w.Code, w.Body.String())
	}
	w := call(http.MethodPost, "/api/v1/desks/"+ownDesk.ID+"/bookings", slot(8))
	if w.Code != http.StatusCreated {
		t.Fatalf("book own desk: %d %s", w.Code, w.Body.String())
	}
	var own desk.Booking
	if err = json.Unmarshal(w.Body.Bytes(), &own); err != nil {
		t.Fatalf("decode booking: %v", err)
	}
	if w = call(http.MethodPost, "/api/v1/desk-bookings/"+own.ID+"/cancel", ""); w.Code != http.StatusOK {
		t.Errorf("cancel own booking: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPost, "/api/v1/desks", `{"name":"desk-h-c1-new","room_id":"`+ownRoom+`"}`); w.Code != http.StatusCreated {
		t.Errorf("create desk in the own room: %d %s", w.Code, w.Body.String())
	}
}

// TestAuditDeskBookingStaysInOrganization was found by WP-538
// (t1-ev-module-isolation, MGT-03, TEN-06, TEN-09; F-366): POST
// /desks/{id}/bookings accepted a user_id of another organization, because
// the foreign key desk_booking.user_id → app_user(id) did not include the
// organization and the repository did not check it; an organization-A
// principal booked an organization-A desk for an organization-B user (201)
// and told an existing user of another organization apart from an unknown id
// (201 versus 404). Since migration 000177 (WP-639) the references of the
// desk tables include the organization: the booking for an organization-B
// user answers exactly like the booking for an unknown user (status and
// body, without database internals), for a client-1 and an org-wide
// principal, and no row is written. A desk in a room of organization B, new
// or moved there, is refused like one in an unknown room.
//
// Retest (F-366):
//
//	cd backend && go test -count=1 -v -run 'StaysInOrganization' ./internal/desk/
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditDeskBookingStaysInOrganization(t *testing.T) {
	f := scopetest.Seed(t, "35")
	room := f.Room(t, f.Client1, "desk-x-room-c1")
	user := f.AppUser(t, f.OrgA, "desk-x-booker")
	userB := f.AppUser(t, f.OrgB, "desk-x-booker-b")
	const unknown = "c0de35a0-0000-4000-8000-00000000dead"
	clientB := f.ID()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', 'scopetest-35-cb')`, clientB, f.OrgB); err != nil {
		t.Fatalf("organization-B client: %v", err)
	}
	var roomB string
	if err := f.Admin.QueryRow(context.Background(), `
		WITH s AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id),
		     b AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, $3 FROM s RETURNING id)
		INSERT INTO room (organization_id, building_id, name) SELECT $1, id, $3 FROM b RETURNING id::text`,
		f.OrgB, clientB, "desk-x-room-b").Scan(&roomB); err != nil {
		t.Fatalf("organization-B room: %v", err)
	}
	repo := desk.NewPGRepository(f.App)
	d := &desk.Desk{OrganizationID: f.OrgA, RoomID: room, Name: "desk-x-c1", Status: "available", Attributes: map[string]any{}}
	if err := repo.Create(f.OrgCtx(f.OrgA), d); err != nil {
		t.Fatalf("desk: %v", err)
	}

	mux := chi.NewRouter()
	desk.NewHandler(repo).RegisterRoutes(mux)
	client1 := database.OrgWideScope(f.OrgA, user)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, user)
	callAs := func(scope *database.TenantScope, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		return w
	}
	// sameAsUnknown requires the answer to the organization-B reference to
	// equal the one to the unknown id: same status, same body, no internals.
	sameAsUnknown := func(name string, foreign, unknownID *httptest.ResponseRecorder, want int) {
		t.Helper()
		if foreign.Code != want {
			t.Errorf("%s: status %d, want %d: %s", name, foreign.Code, want, foreign.Body.String())
		}
		if foreign.Code != unknownID.Code || problemWithoutTrace(t, foreign) != problemWithoutTrace(t, unknownID) {
			t.Errorf("%s: answer %d %s differs from the answer to an unknown id %d %s", name, foreign.Code, foreign.Body.String(), unknownID.Code, unknownID.Body.String())
		}
		if dbLeak.MatchString(foreign.Body.String()) {
			t.Errorf("%s: body discloses database internals: %s", name, foreign.Body.String())
		}
	}
	start := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	booking := func(userID string, h int) string {
		s := start.Add(time.Duration(h) * time.Hour)
		return `{"user_id":"` + userID + `","starts_at":"` + s.Format(time.RFC3339) + `","ends_at":"` + s.Add(time.Hour).Format(time.RFC3339) + `"}`
	}
	for i, c := range []struct {
		name  string
		scope *database.TenantScope
	}{{"client 1", &client1}, {"org-wide", &orgWide}} {
		path := "/api/v1/desks/" + d.ID + "/bookings"
		sameAsUnknown(c.name+": booking for a user of organization B",
			callAs(c.scope, http.MethodPost, path, booking(userB, 4*i)),
			callAs(c.scope, http.MethodPost, path, booking(unknown, 4*i+2)), http.StatusNotFound)
	}
	sameAsUnknown("org-wide: desk in a room of organization B",
		callAs(&orgWide, http.MethodPost, "/api/v1/desks", `{"name":"ghost","room_id":"`+roomB+`"}`),
		callAs(&orgWide, http.MethodPost, "/api/v1/desks", `{"name":"ghost","room_id":"`+unknown+`"}`), http.StatusUnprocessableEntity)
	sameAsUnknown("org-wide: desk moved into a room of organization B",
		callAs(&orgWide, http.MethodPatch, "/api/v1/desks/"+d.ID, `{"room_id":"`+roomB+`"}`),
		callAs(&orgWide, http.MethodPatch, "/api/v1/desks/"+d.ID, `{"room_id":"`+unknown+`"}`), http.StatusNotFound)

	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk_booking WHERE user_id = $1`, userB).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A bookings for the organization-B user: %d err=%v, want none", n, err)
	}
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk WHERE room_id = $1 OR (organization_id = $2 AND name = 'ghost')`, roomB, f.OrgA).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A desks in the organization-B room: %d err=%v, want none", n, err)
	}

	// Positive control: a booking for a user of the own organization.
	if w := callAs(&client1, http.MethodPost, "/api/v1/desks/"+d.ID+"/bookings", booking(user, 10)); w.Code != http.StatusCreated {
		t.Errorf("booking for a user of the own organization: %d %s, want 201", w.Code, w.Body.String())
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
