package reservation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Tests of WP-277 (AST-06, API-06, EVT-03) against PostgreSQL
// (TEST_DATABASE_URL, scopetest.Seed):
//
//   - TestReservationConcurrency: cases 1 to 6 of the plan – two parallel
//     reservations of one asset, reservation against assignment, the
//     atomic conversion, release and expiry with movement, outbox event and
//     audit entry, expiry parallel to a conversion, and the client scope.

const problemAssetReserved = "https://reticora.io/problems/asset-reserved"

// failingBooker books every movement but the assignment through the
// movement service; the assignment fails (case 3).
type failingBooker struct{ movements *movement.Service }

var errBookingRefused = errors.New("booking refused by the test")

func (b failingBooker) RecordTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error {
	if m.MovementType == "assignment" {
		return errBookingRefused
	}
	return b.movements.RecordTx(ctx, tx, m)
}

// resFixture runs the reservation and assignment routes over the wired
// PostgreSQL repository and reads the stored state through the
// maintenance pool.
type resFixture struct {
	f      *scopetest.Fixture
	t      *testing.T
	actor  string
	repo   *reservation.PGRepository
	mux    *chi.Mux
	broken *chi.Mux
}

func newResFixture(t *testing.T, f *scopetest.Fixture) *resFixture {
	t.Helper()
	movements := movement.NewService(movement.NewPGRepository(f.App))
	wire := func(booker assignment.MovementBooker) (*reservation.PGRepository, *chi.Mux) {
		assigns := assignment.NewService(f.App, booker, events.NewOutbox())
		repo := reservation.NewPGRepository(f.App).WithAudit(audit.NewPGRecorder()).WithEvents(events.NewOutbox()).
			WithMovements(movements).WithAssigner(assigns)
		mux := chi.NewRouter()
		reservation.NewHandler(repo).RegisterRoutes(mux)
		assignment.NewHandler(assignment.NewPGRepository(f.App)).WithService(assigns).RegisterRoutes(mux)
		return repo, mux
	}
	repo, mux := wire(movements)
	_, broken := wire(failingBooker{movements: movements})
	return &resFixture{f: f, t: t, actor: f.AppUser(t, f.OrgA, "wp277-actor"), repo: repo, mux: mux, broken: broken}
}

// org is the request context of an org-wide principal of organization A.
func (x *resFixture) org() context.Context {
	return tenant.WithTenant(x.f.OrgCtx(x.f.OrgA), tenant.TenantInfo{OrganizationID: x.f.OrgA, UserID: x.actor})
}

// client1 is the request context of a principal restricted to client 1.
func (x *resFixture) client1() context.Context {
	return tenant.WithTenant(x.f.ClientCtx(x.f.Client1), tenant.TenantInfo{OrganizationID: x.f.OrgA, ClientID: x.f.Client1, UserID: x.actor})
}

func (x *resFixture) call(mux *chi.Mux, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
	return w
}

// reserve reserves assetID for user until the given time over POST
// /api/v1/assets/{id}/reserve.
func (x *resFixture) reserve(ctx context.Context, assetID, user string, until time.Time) *httptest.ResponseRecorder {
	return x.call(x.mux, ctx, http.MethodPost, "/api/v1/assets/"+assetID+"/reserve",
		`{"reserved_until":"`+until.UTC().Format(time.RFC3339)+`","assignee_id":"`+user+`","reason":"wp277"}`)
}

func (x *resFixture) scalar(sql string, args ...any) string {
	x.t.Helper()
	var s string
	if err := x.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		x.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (x *resFixture) state(id string) string {
	return x.scalar(`SELECT state FROM reservation WHERE id = $1`, id)
}

func (x *resFixture) movements(assetID, typ string) string {
	return x.scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1 AND movement_type = $2`, assetID, typ)
}

// event is the client_id of the only event typ of the reservation id in
// the outbox; it fails when there is none or several.
func (x *resFixture) eventClient(id, typ string) string {
	x.t.Helper()
	rows, err := x.f.Admin.Query(context.Background(), `SELECT COALESCE(data->>'client_id', '') FROM event_outbox
		WHERE entity_id::text = $1 AND type = $2`, id, typ)
	if err != nil {
		x.t.Fatal(err)
	}
	clients, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		x.t.Fatal(err)
	}
	if len(clients) != 1 {
		x.t.Fatalf("%d %s events of reservation %s, want one", len(clients), typ, id)
	}
	return clients[0]
}

func decodeID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.ID == "" {
		t.Fatalf("answer %d %s names no id (%v)", w.Code, w.Body.String(), err)
	}
	return body.ID
}

func problemType(w *httptest.ResponseRecorder) string {
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p.Type
}

func TestReservationConcurrency(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	x := newResFixture(t, f)
	user := f.AppUser(t, f.OrgA, "wp277-user")
	other := f.AppUser(t, f.OrgA, "wp277-other")
	future := time.Now().Add(48 * time.Hour)

	t.Run("1 parallel reservations", func(t *testing.T) {
		for i := range 5 {
			asset := f.Asset(t, f.OrgA, f.Client1, "wp277-par-"+string(rune('a'+i)))
			var wg sync.WaitGroup
			answers := make([]*httptest.ResponseRecorder, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				answers[0] = x.reserve(x.org(), asset, user, future)
			}()
			go func() {
				defer wg.Done()
				answers[1] = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations",
					`{"asset_id":"`+asset+`","assignee_id":"`+other+`"}`)
			}()
			wg.Wait()
			created, conflicts := 0, 0
			for _, w := range answers {
				switch {
				case w.Code == http.StatusCreated:
					created++
				case w.Code == http.StatusConflict && problemType(w) == problemAssetReserved:
					conflicts++
				default:
					t.Fatalf("answer %d %s, want 201 or 409 asset-reserved", w.Code, w.Body.String())
				}
			}
			if created != 1 || conflicts != 1 {
				t.Fatalf("%d created and %d refused, want one each", created, conflicts)
			}
			if n := x.scalar(`SELECT count(*)::text FROM reservation WHERE asset_id = $1 AND state = 'active'`, asset); n != "1" {
				t.Fatalf("%s active reservations, want 1", n)
			}
			if n := x.movements(asset, "reservation"); n != "1" {
				t.Fatalf("%s reservation movements, want 1", n)
			}
		}
	})

	t.Run("2 reservation against assignment", func(t *testing.T) {
		for i := range 5 {
			asset := f.Asset(t, f.OrgA, f.Client1, "wp277-race-"+string(rune('a'+i)))
			var wg sync.WaitGroup
			var reserved, assigned *httptest.ResponseRecorder
			wg.Add(2)
			go func() {
				defer wg.Done()
				reserved = x.reserve(x.org(), asset, user, future)
			}()
			go func() {
				defer wg.Done()
				assigned = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/assets/"+asset+"/assign", `{"assigned_user_id":"`+other+`"}`)
			}()
			wg.Wait()
			if (reserved.Code == http.StatusCreated) == (assigned.Code == http.StatusCreated) {
				t.Fatalf("reserve %d %s, assign %d %s: want exactly one winner", reserved.Code, reserved.Body.String(),
					assigned.Code, assigned.Body.String())
			}
			if reserved.Code != http.StatusCreated && reserved.Code != http.StatusConflict {
				t.Fatalf("reserve %d %s, want 201 or 409", reserved.Code, reserved.Body.String())
			}
			if assigned.Code != http.StatusCreated && (assigned.Code != http.StatusConflict || problemType(assigned) != problemAssetReserved) {
				t.Fatalf("assign %d %s, want 201 or 409 asset-reserved", assigned.Code, assigned.Body.String())
			}
		}

		// A third party is refused; the user of the reservation consumes it.
		asset := f.Asset(t, f.OrgA, f.Client1, "wp277-claim")
		w := x.reserve(x.org(), asset, user, future)
		if w.Code != http.StatusCreated {
			t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
		}
		id := decodeID(t, w)
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/assets/"+asset+"/assign", `{"assigned_user_id":"`+other+`"}`); w.Code != http.StatusConflict ||
			problemType(w) != problemAssetReserved {
			t.Fatalf("assign to a third party: %d %s, want 409 asset-reserved", w.Code, w.Body.String())
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/assets/"+asset+"/assign", `{"assigned_team_id":"`+f.ID()+`"}`); w.Code != http.StatusConflict ||
			problemType(w) != problemAssetReserved {
			t.Fatalf("assign to a team: %d %s, want 409 asset-reserved", w.Code, w.Body.String())
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/assets/"+asset+"/assign", `{"assigned_user_id":"`+user+`"}`); w.Code != http.StatusCreated {
			t.Fatalf("assign to the user of the reservation: %d %s", w.Code, w.Body.String())
		}
		if s := x.state(id); s != "consumed" {
			t.Fatalf("reservation %s after the assignment to its user, want consumed", s)
		}
		if c := x.eventClient(id, events.ReservationConverted); c != f.Client1 {
			t.Fatalf("reservation.converted client_id %q, want %q", c, f.Client1)
		}
	})

	t.Run("3 conversion is atomic", func(t *testing.T) {
		asset := f.Asset(t, f.OrgA, f.Client1, "wp277-convert")
		w := x.reserve(x.org(), asset, user, future)
		if w.Code != http.StatusCreated {
			t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
		}
		id := decodeID(t, w)

		// The assignment fails: the reservation stays active, no movement.
		if w = x.call(x.broken, x.org(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`); w.Code < 500 {
			t.Fatalf("convert with a failing assignment: %d %s, want a server error", w.Code, w.Body.String())
		}
		if s := x.state(id); s != "active" {
			t.Fatalf("reservation %s after the failed conversion, want active", s)
		}
		if n := x.movements(asset, "assignment"); n != "0" {
			t.Fatalf("%s assignment movements after the failed conversion, want 0", n)
		}
		if n := x.scalar(`SELECT count(*)::text FROM event_outbox WHERE entity_id::text = $1 AND type = $2`, id, events.ReservationConverted); n != "0" {
			t.Fatalf("%s reservation.converted events after the failed conversion, want 0", n)
		}

		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`); w.Code != http.StatusOK {
			t.Fatalf("convert: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Reservation reservation.Reservation `json:"reservation"`
			Assignment  assignment.Assignment   `json:"assignment"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Reservation.State != "consumed" || out.Assignment.AssignedTo != user {
			t.Fatalf("convert answered state %q and holder %q, want consumed and %s", out.Reservation.State, out.Assignment.AssignedTo, user)
		}
		if s := x.state(id); s != "consumed" {
			t.Fatalf("reservation %s, want consumed", s)
		}
		if n := x.movements(asset, "assignment"); n != "1" {
			t.Fatalf("%s assignment movements, want 1", n)
		}
		if h := x.scalar(`SELECT COALESCE(assigned_user_id::text, '') FROM asset WHERE id = $1`, asset); h != user {
			t.Fatalf("holder cache %q, want %s", h, user)
		}
		if c := x.eventClient(id, events.ReservationConverted); c != f.Client1 {
			t.Fatalf("reservation.converted client_id %q, want %q", c, f.Client1)
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`); w.Code != http.StatusConflict {
			t.Fatalf("second convert: %d %s, want 409", w.Code, w.Body.String())
		}

		// A project reservation needs a holder in the body.
		project := f.Asset(t, f.OrgA, f.Client1, "wp277-project")
		w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/assets/"+project+"/reserve",
			`{"reserved_until":"`+future.UTC().Format(time.RFC3339)+`","project_ref":"P-277"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("project reservation: %d %s", w.Code, w.Body.String())
		}
		pid := decodeID(t, w)
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+pid+"/convert", `{}`); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("convert of a project reservation without holder: %d %s, want 422", w.Code, w.Body.String())
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+pid+"/convert", `{"assignee_id":"`+other+`"}`); w.Code != http.StatusOK {
			t.Fatalf("convert of a project reservation to a user: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("4 release and expiry", func(t *testing.T) {
		asset := f.Asset(t, f.OrgA, f.Client1, "wp277-release")
		w := x.reserve(x.org(), asset, user, future)
		if w.Code != http.StatusCreated {
			t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
		}
		id := decodeID(t, w)
		if c := x.eventClient(id, events.ReservationCreated); c != f.Client1 {
			t.Fatalf("reservation.created client_id %q, want %q", c, f.Client1)
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+id+"/release", `{}`); w.Code != http.StatusOK {
			t.Fatalf("release: %d %s", w.Code, w.Body.String())
		}
		if n := x.movements(asset, "release"); n != "1" {
			t.Fatalf("%s release movements, want 1", n)
		}
		if c := x.eventClient(id, events.ReservationReleased); c != f.Client1 {
			t.Fatalf("reservation.released client_id %q, want %q", c, f.Client1)
		}

		// Created over the API with reserved_until and without expires_at:
		// it expires.
		due := f.Asset(t, f.OrgA, f.Client1, "wp277-expire")
		if w = x.reserve(x.org(), due, user, time.Now().Add(-time.Minute)); w.Code != http.StatusCreated {
			t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
		}
		did := decodeID(t, w)
		if same := x.scalar(`SELECT (expires_at = reserved_until)::text FROM reservation WHERE id = $1`, did); same != "true" {
			t.Fatal("expires_at is not reserved_until")
		}
		if err := reservation.NewSweeper(x.repo, nil).SweepOnce(context.Background()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if s := x.state(did); s != "expired" {
			t.Fatalf("reservation %s after the sweep, want expired", s)
		}
		if n := x.movements(due, "release"); n != "1" {
			t.Fatalf("%s release movements of the expiry, want 1", n)
		}
		if c := x.eventClient(did, events.ReservationExpired); c != f.Client1 {
			t.Fatalf("reservation.expired client_id %q, want %q", c, f.Client1)
		}
		if n := x.scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2
				AND action = 'reservations.expire' AND resource_type = 'reservations' AND source = 'system'`, f.OrgA, did); n != "1" {
			t.Fatalf("%s audit entries reservations.expire, want 1", n)
		}
	})

	t.Run("5 expiry parallel to conversion", func(t *testing.T) {
		for i := range 5 {
			asset := f.Asset(t, f.OrgA, f.Client1, "wp277-deadlock-"+string(rune('a'+i)))
			w := x.reserve(x.org(), asset, user, time.Now().Add(-time.Second))
			if w.Code != http.StatusCreated {
				t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
			}
			id := decodeID(t, w)
			var wg sync.WaitGroup
			var sweepErr error
			var converted *httptest.ResponseRecorder
			wg.Add(2)
			go func() {
				defer wg.Done()
				sweepErr = reservation.NewSweeper(x.repo, nil).SweepOnce(context.Background())
			}()
			go func() {
				defer wg.Done()
				converted = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`)
			}()
			wg.Wait()
			if sweepErr != nil {
				t.Fatalf("sweep: %v", sweepErr)
			}
			switch s := x.state(id); {
			case s == "consumed" && converted.Code == http.StatusOK:
				if n := x.movements(asset, "release"); n != "0" {
					t.Fatalf("converted reservation with %s release movements", n)
				}
			case s == "expired" && converted.Code == http.StatusConflict:
				if n := x.movements(asset, "assignment"); n != "0" {
					t.Fatalf("expired reservation with %s assignment movements", n)
				}
			default:
				t.Fatalf("state %s with convert %d %s: want consumed/200 or expired/409", s, converted.Code, converted.Body.String())
			}
		}
	})

	t.Run("6 client scope", func(t *testing.T) {
		own := f.Asset(t, f.OrgA, f.Client1, "wp277-own")
		orgWide := f.Asset(t, f.OrgA, "", "wp277-orgwide")
		foreign := f.Asset(t, f.OrgA, f.Client2, "wp277-foreign")
		if w := x.reserve(x.client1(), own, user, future); w.Code != http.StatusCreated {
			t.Fatalf("own asset: %d %s, want 201", w.Code, w.Body.String())
		}
		for name, asset := range map[string]string{"org-wide": orgWide, "client 2": foreign} {
			if w := x.reserve(x.client1(), asset, user, future); w.Code != http.StatusNotFound {
				t.Fatalf("%s asset: %d %s, want 404", name, w.Code, w.Body.String())
			}
			if n := x.scalar(`SELECT count(*)::text FROM reservation WHERE asset_id = $1`, asset); n != "0" {
				t.Fatalf("%s asset: %s reservations, want 0", name, n)
			}
		}
		// The organization-wide reservation of the org-wide asset can be
		// neither released nor converted by the client-1 principal.
		w := x.reserve(x.org(), orgWide, user, future)
		if w.Code != http.StatusCreated {
			t.Fatalf("org-wide reservation: %d %s", w.Code, w.Body.String())
		}
		id := decodeID(t, w)
		for _, action := range []string{"release", "convert"} {
			if w = x.call(x.mux, x.client1(), http.MethodPost, "/api/v1/reservations/"+id+"/"+action, `{}`); w.Code != http.StatusNotFound {
				t.Fatalf("client-1 %s of the org-wide asset's reservation: %d %s, want 404", action, w.Code, w.Body.String())
			}
		}
		if s := x.state(id); s != "active" {
			t.Fatalf("reservation %s, want active", s)
		}
	})
}
