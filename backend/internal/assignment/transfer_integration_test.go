package assignment_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// assignFixture runs the assignment routes with the service over a
// booker against PostgreSQL and reads the stored state through the
// maintenance pool.
type assignFixture struct {
	f     *scopetest.Fixture
	t     *testing.T
	actor string
	mux   *chi.Mux
}

func newAssignFixture(t *testing.T, f *scopetest.Fixture, actor string, booker assignment.MovementBooker) *assignFixture {
	t.Helper()
	mux := chi.NewRouter()
	assignment.NewHandler(assignment.NewPGRepository(f.App)).
		WithService(assignment.NewService(f.App, booker, events.NewOutbox())).RegisterRoutes(mux)
	movement.NewHandler(movement.NewService(movement.NewPGRepository(f.App))).RegisterRoutes(mux)
	return &assignFixture{f: f, t: t, actor: actor, mux: mux}
}

// org is the request context of an org-wide principal of organization A.
func (a *assignFixture) org() context.Context {
	return tenant.WithTenant(a.f.OrgCtx(a.f.OrgA), tenant.TenantInfo{OrganizationID: a.f.OrgA, UserID: a.actor})
}

// client1 is the request context of a principal restricted to client 1.
func (a *assignFixture) client1() context.Context {
	return tenant.WithTenant(a.f.ClientCtx(a.f.Client1), tenant.TenantInfo{OrganizationID: a.f.OrgA, ClientID: a.f.Client1, UserID: a.actor})
}

func (a *assignFixture) call(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
	return w
}

// holder is the holder cache of an asset.
type holderCache struct {
	user, team string
	due        *time.Time
}

func (a *assignFixture) holder(assetID string) holderCache {
	a.t.Helper()
	var h holderCache
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(assigned_user_id::text, ''), COALESCE(assigned_team_id::text, ''),
			assignment_due_at FROM asset WHERE id = $1`, assetID).Scan(&h.user, &h.team, &h.due); err != nil {
		a.t.Fatal(err)
	}
	return h
}

// ledgerRow is an assignment or return movement of an asset.
type ledgerRow struct {
	id, kind, user, team, reason string
	due                          *time.Time
	at                           time.Time
}

func (a *assignFixture) ledger(assetID string) []ledgerRow {
	a.t.Helper()
	rows, err := a.f.Admin.Query(context.Background(), `SELECT id::text, movement_type, COALESCE(assignee_user_id::text, ''),
			COALESCE(assignee_team_id::text, ''), COALESCE(reason, ''), due_at, occurred_at
		FROM stock_movement WHERE asset_id = $1 AND movement_type IN ('assignment', 'return') ORDER BY created_at, movement_type DESC`, assetID)
	if err != nil {
		a.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ledgerRow, error) {
		var l ledgerRow
		return l, row.Scan(&l.id, &l.kind, &l.user, &l.team, &l.reason, &l.due, &l.at)
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return out
}

// projectedRow is a row of the projection assignment_projection (WP-747).
type projectedRow struct {
	id, status, kind, user, team, asset, due string
	returned                                 bool
}

func (a *assignFixture) rows(where string, args ...any) []projectedRow {
	a.t.Helper()
	rows, err := a.f.Admin.Query(context.Background(), `SELECT id::text, status, assignment_type, COALESCE(assigned_to::text, ''),
			COALESCE(assigned_team_id::text, ''), asset_id::text, COALESCE(to_char(due_date, 'YYYY-MM-DD'), ''), returned_at IS NOT NULL
		FROM assignment_projection WHERE `+where+` ORDER BY assigned_at, id`, args...)
	if err != nil {
		a.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (projectedRow, error) {
		var r projectedRow
		return r, row.Scan(&r.id, &r.status, &r.kind, &r.user, &r.team, &r.asset, &r.due, &r.returned)
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return out
}

// events are the assignment events of an asset in outbox order.
func (a *assignFixture) events(assetID string) []string {
	a.t.Helper()
	rows, err := a.f.Admin.Query(context.Background(), `SELECT type FROM event_outbox
		WHERE entity_id::text = $1 AND type IN ('asset.assigned', 'asset.returned') ORDER BY sequence`, assetID)
	if err != nil {
		a.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		a.t.Fatal(err)
	}
	return out
}

func (a *assignFixture) team(orgID, name string) string {
	a.t.Helper()
	id := a.f.ID()
	if _, err := a.f.Admin.Exec(context.Background(), `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, $3)`, id, orgID, name); err != nil {
		a.t.Fatal(err)
	}
	return id
}

// problemType reads the type of an RFC 7807 answer.
func problemType(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %s: %v", w.Body.String(), err)
	}
	return p.Type
}

func decodeAssignment(t *testing.T, w *httptest.ResponseRecorder) assignment.Assignment {
	t.Helper()
	var out assignment.Assignment
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

// TestAssignmentServiceAssignReturnTransfer covers AST-02 and AST-07
// (WP-275) against PostgreSQL: assignment to a user and to a team with a
// due date, return and transfer book the movements assignment and return
// in the ledger and keep the holder cache in one transaction, the
// projection assignment_projection shows them (WP-747), and they write
// asset.returned before asset.assigned; holder conflicts answer 409, a missing or double holder
// 422, a holder of another organization 422.
func TestAssignmentServiceAssignReturnTransfer(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp275actor")
	a := newAssignFixture(t, f, actor, movement.NewService(movement.NewPGRepository(f.App)))
	asset := f.Asset(t, f.OrgA, f.Client1, "wp275-asset")
	alice := f.AppUser(t, f.OrgA, "wp275alice")
	bob := f.AppUser(t, f.OrgA, "wp275bob")
	crew := a.team(f.OrgA, "wp275-crew")
	foreignUser := f.AppUser(t, f.OrgB, "wp275foreign")
	foreignTeam := a.team(f.OrgB, "wp275-foreign")
	ctx := a.org()
	path := "/api/v1/assets/" + asset

	// Assignment to a user with a due date.
	w := a.call(ctx, http.MethodPost, path+"/assign", `{"assigned_user_id":"`+alice+`","due_at":"2026-12-01T10:00:00Z","notes":"laptop"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}
	first := decodeAssignment(t, w)
	due := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	if first.AssignedTo != alice || first.AssignedTeamID != "" || first.AssignedBy != actor || first.DueAt == nil ||
		!first.DueAt.Equal(due) || first.DueDate != "2026-12-01" || first.Status != "active" || first.AssetID != asset {
		t.Fatalf("assign response = %+v", first)
	}
	if h := a.holder(asset); h.user != alice || h.team != "" || h.due == nil || !h.due.Equal(due) {
		t.Fatalf("holder after assign = %+v", h)
	}
	ledger := a.ledger(asset)
	if len(ledger) != 1 || ledger[0].kind != "assignment" || ledger[0].user != alice || ledger[0].id != first.ID ||
		ledger[0].due == nil || !ledger[0].due.Equal(due) {
		t.Fatalf("ledger after assign = %+v", ledger)
	}
	if rows := a.rows(`asset_id = $1`, asset); len(rows) != 1 || rows[0].id != first.ID || rows[0].status != "active" ||
		rows[0].user != alice || rows[0].due != "2026-12-01" {
		t.Fatalf("projection after assign = %+v", rows)
	}

	// An assigned asset is not assigned again.
	if w = a.call(ctx, http.MethodPost, path+"/assign", `{"assigned_user_id":"`+bob+`"}`); w.Code != http.StatusConflict ||
		problemType(t, w) != assignment.ProblemAssetAssigned {
		t.Fatalf("second assign: %d %s, want 409 asset-assigned", w.Code, w.Body.String())
	}

	// Transfer to a team: return of the user and assignment of the team
	// with the same occurred_at.
	teamDue := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)
	w = a.call(ctx, http.MethodPost, path+"/transfer", `{"expected_holder":"`+alice+`","assigned_team_id":"`+crew+`","due_at":"2027-01-15T08:00:00Z"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
	}
	moved := decodeAssignment(t, w)
	if moved.AssignedTeamID != crew || moved.AssignedTo != "" || moved.AssignmentType != "transfer" || moved.DueAt == nil || !moved.DueAt.Equal(teamDue) {
		t.Fatalf("transfer response = %+v", moved)
	}
	if h := a.holder(asset); h.user != "" || h.team != crew || h.due == nil || !h.due.Equal(teamDue) {
		t.Fatalf("holder after transfer = %+v", h)
	}
	ledger = a.ledger(asset)
	if len(ledger) != 3 || ledger[1].kind != "return" || ledger[1].user != alice || ledger[1].due != nil ||
		ledger[2].kind != "assignment" || ledger[2].team != crew || ledger[2].id != moved.ID || !ledger[1].at.Equal(ledger[2].at) {
		t.Fatalf("ledger after transfer = %+v", ledger)
	}
	// The assignment of the user is transferred, the one of the team is
	// the transfer.
	if rows := a.rows(`asset_id = $1`, asset); len(rows) != 2 || rows[0].id != first.ID || rows[0].status != "transferred" || !rows[0].returned ||
		rows[0].kind != "assignment" || rows[1].id != moved.ID || rows[1].status != "active" || rows[1].kind != "transfer" ||
		rows[1].team != crew || rows[1].user != "" || rows[1].due != "2027-01-15" {
		t.Fatalf("projection after transfer = %+v", rows)
	}

	// A stale expected_holder is a conflict.
	if w = a.call(ctx, http.MethodPost, path+"/transfer", `{"expected_holder":"`+alice+`","assigned_user_id":"`+bob+`"}`); w.Code != http.StatusConflict ||
		problemType(t, w) != assignment.ProblemHolderMismatch {
		t.Fatalf("stale transfer: %d %s, want 409 holder-mismatch", w.Code, w.Body.String())
	}

	// Return from the team.
	w = a.call(ctx, http.MethodPost, path+"/return", `{"expected_holder":"`+crew+`","return_condition":"good","notes":"back"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("return: %d %s", w.Code, w.Body.String())
	}
	var back movement.Movement
	if err := json.Unmarshal(w.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.MovementType != "return" || back.AssigneeTeamID != crew || back.Reason != "good" || back.Notes != "back" || back.AssetID != asset {
		t.Fatalf("return response = %+v", back)
	}
	if h := a.holder(asset); h != (holderCache{}) {
		t.Fatalf("holder after return = %+v", h)
	}
	if w = a.call(ctx, http.MethodPost, path+"/return", `{"expected_holder":"`+crew+`"}`); w.Code != http.StatusConflict ||
		problemType(t, w) != assignment.ProblemAssetNotAssigned {
		t.Fatalf("second return: %d %s, want 409 asset-not-assigned", w.Code, w.Body.String())
	}
	if got, want := a.events(asset), []string{"asset.assigned", "asset.returned", "asset.assigned", "asset.returned"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// No, two or a malformed holder, a missing expected_holder: 422.
	before := len(a.ledger(asset))
	for _, body := range []string{`{}`, `{"assigned_user_id":"` + alice + `","assigned_team_id":"` + crew + `"}`, `{"assigned_user_id":"alice"}`} {
		if w = a.call(ctx, http.MethodPost, path+"/assign", body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("assign %s: %d %s, want 422", body, w.Code, w.Body.String())
		}
	}
	if w = a.call(ctx, http.MethodPost, path+"/return", `{}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("return without expected_holder: %d %s, want 422", w.Code, w.Body.String())
	}
	// A user or team of another organization fails the composite foreign
	// key: 422, nothing booked.
	for _, body := range []string{`{"assigned_user_id":"` + foreignUser + `"}`, `{"assigned_team_id":"` + foreignTeam + `"}`} {
		if w = a.call(ctx, http.MethodPost, path+"/assign", body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("assign %s: %d %s, want 422", body, w.Code, w.Body.String())
		}
	}
	if after := len(a.ledger(asset)); after != before {
		t.Errorf("refused requests booked %d movements", after-before)
	}
	if h := a.holder(asset); h != (holderCache{}) {
		t.Errorf("holder after refused requests = %+v", h)
	}
	if w = a.call(ctx, http.MethodPost, "/api/v1/assets/"+f.ID()+"/assign", `{"assigned_user_id":"`+alice+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("assign of an unknown asset: %d, want 404", w.Code)
	}
}

// TestAssignmentServiceConcurrency covers the lock of the asset (WP-275):
// two parallel transfers with the same expected_holder and two parallel
// assignments of a free asset each succeed exactly once; the other answers
// 409.
func TestAssignmentServiceConcurrency(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp275actor")
	a := newAssignFixture(t, f, actor, movement.NewService(movement.NewPGRepository(f.App)))
	alice := f.AppUser(t, f.OrgA, "wp275alice")
	bob := f.AppUser(t, f.OrgA, "wp275bob")
	carol := f.AppUser(t, f.OrgA, "wp275carol")
	ctx := a.org()

	parallel := func(path string, bodies ...string) []int {
		var wg sync.WaitGroup
		start := make(chan struct{})
		codes := make([]int, len(bodies))
		for i, body := range bodies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = a.call(ctx, http.MethodPost, path, body).Code
			}()
		}
		close(start)
		wg.Wait()
		slices.Sort(codes)
		return codes
	}
	want := []int{http.StatusCreated, http.StatusConflict}

	held := f.Asset(t, f.OrgA, f.Client1, "wp275-held")
	if w := a.call(ctx, http.MethodPost, "/api/v1/assets/"+held+"/assign", `{"assigned_user_id":"`+alice+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}
	codes := parallel("/api/v1/assets/"+held+"/transfer",
		`{"expected_holder":"`+alice+`","assigned_user_id":"`+bob+`"}`, `{"expected_holder":"`+alice+`","assigned_user_id":"`+carol+`"}`)
	if !slices.Equal(codes, want) {
		t.Fatalf("parallel transfers = %v, want %v", codes, want)
	}
	if h := a.holder(held); h.user != bob && h.user != carol {
		t.Fatalf("holder after parallel transfers = %+v", h)
	}
	if n := len(a.ledger(held)); n != 3 {
		t.Fatalf("ledger after parallel transfers: %d movements, want 3", n)
	}
	if open := a.rows(`asset_id = $1 AND status = 'active'`, held); len(open) != 1 {
		t.Fatalf("open assignments = %+v, want one", open)
	}

	free := f.Asset(t, f.OrgA, f.Client1, "wp275-free")
	codes = parallel("/api/v1/assets/"+free+"/assign", `{"assigned_user_id":"`+bob+`"}`, `{"assigned_user_id":"`+carol+`"}`)
	if !slices.Equal(codes, want) {
		t.Fatalf("parallel assignments = %v, want %v", codes, want)
	}
	if n := len(a.ledger(free)); n != 1 {
		t.Fatalf("ledger after parallel assignments: %d movements, want 1", n)
	}
}

// failingBooker books through the movement service until the call fail,
// which fails.
type failingBooker struct {
	inner assignment.MovementBooker
	fail  int
	mu    sync.Mutex
	calls int
}

var errLedgerDown = errors.New("ledger unavailable")

func (b *failingBooker) RecordTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.mu.Unlock()
	if n == b.fail {
		return errLedgerDown
	}
	return b.inner.RecordTx(ctx, tx, m)
}

// TestAssignmentServiceRollbackAndScope covers the transaction and the
// scope of the service (WP-275): a failing movement service rolls back the
// first movement, the holder cache, the projection and the events; a
// client-scoped principal gets 404 for an org-wide and a foreign-client
// asset; POST /api/v1/stock-movements refuses assignment and leaves the
// cache alone; without the service (profile without database) the router
// answers 501.
func TestAssignmentServiceRollbackAndScope(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp275actor")
	ledger := movement.NewService(movement.NewPGRepository(f.App))
	a := newAssignFixture(t, f, actor, ledger)
	alice := f.AppUser(t, f.OrgA, "wp275alice")
	bob := f.AppUser(t, f.OrgA, "wp275bob")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp275-rollback")
	if w := a.call(a.org(), http.MethodPost, "/api/v1/assets/"+asset+"/assign", `{"assigned_user_id":"`+alice+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}
	movements, rows, evts := len(a.ledger(asset)), a.rows(`asset_id = $1`, asset), a.events(asset)

	// The second booking of the transfer (assignment) fails.
	broken := newAssignFixture(t, f, actor, &failingBooker{inner: ledger, fail: 2})
	w := broken.call(broken.org(), http.MethodPost, "/api/v1/assets/"+asset+"/transfer", `{"expected_holder":"`+alice+`","assigned_user_id":"`+bob+`"}`)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), errLedgerDown.Error()) {
		t.Fatalf("transfer with a failing ledger: %d %s, want 500 without the error text", w.Code, w.Body.String())
	}
	if h := a.holder(asset); h.user != alice {
		t.Errorf("holder after the failed transfer = %+v, want %s", h, alice)
	}
	if n := len(a.ledger(asset)); n != movements {
		t.Errorf("failed transfer left %d movements", n-movements)
	}
	if got := a.rows(`asset_id = $1`, asset); !slices.Equal(got, rows) {
		t.Errorf("projection after the failed transfer = %+v, want %+v", got, rows)
	}
	if got := a.events(asset); !slices.Equal(got, evts) {
		t.Errorf("events after the failed transfer = %v, want %v", got, evts)
	}

	// The ledger route takes no assignment.
	w = a.call(a.org(), http.MethodPost, "/api/v1/stock-movements", `{"item_kind":"asset","asset_id":"`+asset+`","movement_type":"assignment"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("stock movement assignment: %d %s, want 422", w.Code, w.Body.String())
	}
	if h := a.holder(asset); h.user != alice {
		t.Errorf("holder after the refused stock movement = %+v", h)
	}

	// Scope: an org-wide and a client-2 asset are not lockable for a
	// client-1 principal.
	orgWide := f.Asset(t, f.OrgA, "", "wp275-orgwide")
	foreign := f.Asset(t, f.OrgA, f.Client2, "wp275-client2")
	for _, id := range []string{orgWide, foreign} {
		if w = a.call(a.client1(), http.MethodPost, "/api/v1/assets/"+id+"/assign", `{"assigned_user_id":"`+bob+`"}`); w.Code != http.StatusNotFound {
			t.Errorf("client-1 assign of %s: %d %s, want 404", id, w.Code, w.Body.String())
		}
		if h := a.holder(id); h != (holderCache{}) {
			t.Errorf("holder of %s after the refused assign = %+v", id, h)
		}
	}
	own := f.Asset(t, f.OrgA, f.Client1, "wp275-own")
	if w = a.call(a.client1(), http.MethodPost, "/api/v1/assets/"+own+"/assign", `{"assigned_user_id":"`+bob+`"}`); w.Code != http.StatusCreated {
		t.Errorf("client-1 assign of an own asset: %d %s, want 201", w.Code, w.Body.String())
	}

	// Profile without database: the writing assignment routes answer 501.
	mux := memoryRouter(t)
	p := identity.Principal{Subject: actor, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: identity.AllPermissions()}
	for _, route := range []string{"POST /api/v1/assets/" + asset + "/assign", "POST /api/v1/assets/" + asset + "/return",
		"POST /api/v1/assets/" + asset + "/transfer"} {
		method, target, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, target, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		rctx = tenant.WithTenant(rctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: actor})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(rctx))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s without database: %d %s, want 501", route, rec.Code, rec.Body.String())
		}
	}
}

// memoryRouter builds the router of the profile without database.
func memoryRouter(t *testing.T) http.Handler {
	t.Helper()
	repos := server.MemoryRepositories()
	if repos.AssignmentService != nil {
		t.Fatal("the profile without database has an assignment service")
	}
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "test", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Dispatcher:  webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}), Audit: &audit.Handler{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

// TestAssignmentCIOnlyRowAndBackfill covers the backfill 000254 over the
// rows of assignment from before WP-275 (WP-275, WP-746, WP-747): it takes
// the newest open row per asset, a row with only a ci_id through
// ci.asset_id, the due date as the end of the day in the time zone of the
// organization, and no user of another organization. The table assignment
// is gone since WP-843 (000266); the rolled-back transaction rebuilds it
// with the down part of 000266 first. That the service leaves the rows
// alone follows from the drop: no path reaches the table any more.
func TestAssignmentCIOnlyRowAndBackfill(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	bg := context.Background()
	alice := f.AppUser(t, f.OrgA, "wp275alice")
	bob := f.AppUser(t, f.OrgA, "wp275bob")
	// The fixtures come before the transaction: the down part of 000266
	// locks asset, ci and app_user (references of assignment).
	newest, viaCI, foreignHeld := f.Asset(t, f.OrgA, f.Client1, "wp275-bf1"), f.Asset(t, f.OrgA, f.Client1, "wp275-bf2"), f.Asset(t, f.OrgA, f.Client1, "wp275-bf3")
	backfillCI := f.CI(t, f.OrgA, f.Client1, "wp275-bf-ci")
	foreignUser := f.AppUser(t, f.OrgB, "wp275foreign")

	// Backfill 000254 in a transaction that is rolled back.
	raw, err := os.ReadFile("../../migrations/000254_assignment_holder_backfill.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, dropDown := readDropMigration(t)
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, query, args...); execErr != nil {
			t.Fatalf("%.70s...: %v", strings.TrimSpace(query), execErr)
		}
	}
	exec(dropDown)
	exec(`UPDATE organization SET timezone = 'Europe/Berlin' WHERE id = $1`, f.OrgA)
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, backfillCI, viaCI)
	exec(`INSERT INTO assignment (organization_id, asset_id, assigned_to, assigned_by, status, assigned_at) VALUES
		($1, $2, $3, $3, 'active', now() - interval '2 days'),
		($1, $2, $4, $3, 'overdue', now() - interval '1 day'),
		($1, $2, $3, $3, 'returned', now())`, f.OrgA, newest, alice, bob)
	exec(`UPDATE assignment SET due_date = '2026-12-24' WHERE asset_id = $1 AND assigned_to = $2`, newest, bob)
	exec(`INSERT INTO assignment (organization_id, ci_id, assigned_to, assigned_by, status) VALUES ($1, $2, $3, $3, 'active')`, f.OrgA, backfillCI, alice)
	exec(`INSERT INTO assignment (organization_id, asset_id, assigned_to, assigned_by, status) VALUES ($1, $2, $3, $3, 'active')`, f.OrgA, foreignHeld, foreignUser)
	exec(string(raw))
	read := func(id string) holderCache {
		t.Helper()
		var h holderCache
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(assigned_user_id::text, ''), COALESCE(assigned_team_id::text, ''), assignment_due_at
			FROM asset WHERE id = $1`, id).Scan(&h.user, &h.team, &h.due); qerr != nil {
			t.Fatal(qerr)
		}
		return h
	}
	endOfDay := time.Date(2026, 12, 24, 22, 59, 59, 999999000, time.UTC) // 23:59:59.999999 in Berlin
	if h := read(newest); h.user != bob || h.due == nil || !h.due.Equal(endOfDay) {
		t.Errorf("backfill of the newest open row = %+v, want %s due %s", h, bob, endOfDay)
	}
	if h := read(viaCI); h.user != alice || h.due != nil {
		t.Errorf("backfill through ci.asset_id = %+v, want %s", h, alice)
	}
	if h := read(foreignHeld); h != (holderCache{}) {
		t.Errorf("backfill of a user of another organization = %+v, want none", h)
	}
}
