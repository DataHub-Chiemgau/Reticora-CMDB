package integrity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The concurrency matrix of phase 2 after ABN-03 (WP-328) against
// PostgreSQL (TEST_DATABASE_URL, scopetest.Seed):
//
//	cd backend && go test -count=1 -p 1 -run 'TestPhase2ConcurrencyMatrix' ./internal/tenant/integrity/
//
// The cases run real parallel transactions through the handlers and
// services of WP-277 (reservation), WP-275 (assignment and transfer),
// WP-283/WP-756 (bookings over POST /api/v1/stock-movements) and WP-284
// (quantity reservations); the test writes nothing of its own but the
// fixture. Every transaction of a round waits at a barrier after its
// booking and before its commit, until every party of the round arrived or
// ended, or gateTimeout passed: a party that waits for a lock of another
// one makes the holder wait the timeout, so the contention is real. After
// every case scopetest.IntegrityViolations is empty for the fixture
// organization, and no request ends with 5xx (40001 and 40P01 appear at
// most as 409, backend/internal/api/dberrors.go).

// gateTimeout is how long a party waits at the barrier for the others.
const gateTimeout = 150 * time.Millisecond

// gate is the barrier of one round of n parties.
type gate struct {
	mu      sync.Mutex
	n, seen int
	open    chan struct{}
	arrived atomic.Int32
}

func newGate(n int) *gate { return &gate{n: n, open: make(chan struct{})} }

// party is one transaction of a round; done is guarded by the gate's mu.
type party struct {
	g    *gate
	done bool
}

// mark counts p as arrived or ended, once.
func (p *party) mark() bool {
	p.g.mu.Lock()
	defer p.g.mu.Unlock()
	if p.done {
		return false
	}
	p.done = true
	p.g.seen++
	if p.g.seen == p.g.n {
		close(p.g.open)
	}
	return true
}

// arrive waits at the barrier: until every party arrived or ended, or
// gateTimeout passed. A second arrival of the same party passes.
func (p *party) arrive() {
	if !p.mark() {
		return
	}
	p.g.arrived.Add(1)
	select {
	case <-p.g.open:
	case <-time.After(gateTimeout):
	}
}

type partyKey struct{}

// arriveIn lets the party of ctx, if any, wait at its barrier.
func arriveIn(ctx context.Context) {
	if p, ok := ctx.Value(partyKey{}).(*party); ok {
		p.arrive()
	}
}

// gatedBooker books through the movement service and waits at the barrier
// after the booking that completes the write of the caller: the
// reservation (WP-277, WP-284) and the assignment (WP-275; a transfer
// books the return first).
type gatedBooker struct{ next *movement.Service }

func (b gatedBooker) RecordTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error {
	if err := b.next.RecordTx(ctx, tx, m); err != nil {
		return err
	}
	if m.MovementType == "reservation" || m.MovementType == "assignment" {
		arriveIn(ctx)
	}
	return nil
}

// gatedLedger is the movement repository of POST /api/v1/stock-movements
// (WP-756): it runs the booking of the repository in a request
// transaction, which the repository joins, and waits at the barrier
// before the commit.
type gatedLedger struct {
	movement.Repository
	pool *pgxpool.Pool
}

func (l gatedLedger) Record(ctx context.Context, m *movement.Movement) error {
	return database.WithRequestTenantTx(ctx, l.pool, m.OrganizationID, func(txCtx context.Context) error {
		if err := l.Repository.Record(txCtx, m); err != nil {
			return err
		}
		arriveIn(txCtx)
		return nil
	})
}

// matrix serves the routes of the cases for an org-wide principal of
// organization A and reads the stored state through the maintenance pool.
type matrix struct {
	t     *testing.T
	f     *scopetest.Fixture
	actor string
	mux   *chi.Mux
}

func newMatrix(t *testing.T, f *scopetest.Fixture) *matrix {
	t.Helper()
	booker := gatedBooker{next: movement.NewService(movement.NewPGRepository(f.App))}
	assigns := assignment.NewService(f.App, booker, events.NewOutbox())
	reservations := reservation.NewPGRepository(f.App).WithAudit(audit.NewPGRecorder()).WithEvents(events.NewOutbox()).
		WithMovements(booker).WithAssigner(assigns)
	mux := chi.NewRouter()
	reservation.NewHandler(reservations).RegisterRoutes(mux)
	assignment.NewHandler(assignment.NewPGRepository(f.App)).WithService(assigns).RegisterRoutes(mux)
	movement.NewHandler(gatedLedger{Repository: movement.NewPGRepository(f.App), pool: f.App}).RegisterRoutes(mux)
	return &matrix{t: t, f: f, actor: f.AppUser(t, f.OrgA, "wp328-actor"), mux: mux}
}

// ctx is the request context of an org-wide principal of organization A.
func (x *matrix) ctx() context.Context {
	return tenant.WithTenant(x.f.OrgCtx(x.f.OrgA), tenant.TenantInfo{OrganizationID: x.f.OrgA, UserID: x.actor})
}

// call is one request; the request context carries the party of ctx.
type call func(ctx context.Context) *httptest.ResponseRecorder

// post builds the call POST path with body.
func (x *matrix) post(path string, body map[string]any) call {
	raw, err := json.Marshal(body)
	if err != nil {
		x.t.Fatal(err)
	}
	return func(ctx context.Context) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw))).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		x.mux.ServeHTTP(w, req)
		return w
	}
}

// do runs c alone, outside any barrier, and requires status.
func (x *matrix) do(c call, status int) *httptest.ResponseRecorder {
	x.t.Helper()
	w := c(x.ctx())
	if w.Code != status {
		x.t.Fatalf("answer %d %s, want %d", w.Code, w.Body.String(), status)
	}
	return w
}

// parallel runs the calls as one round: started together, each a party of
// the round's barrier. No answer may be 5xx, and at least one party must
// have reached the barrier.
func (x *matrix) parallel(calls ...call) []*httptest.ResponseRecorder {
	x.t.Helper()
	g := newGate(len(calls))
	out := make([]*httptest.ResponseRecorder, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &party{g: g}
			ctx := context.WithValue(x.ctx(), partyKey{}, p)
			<-start
			out[i] = c(ctx)
			p.mark()
		}()
	}
	close(start)
	wg.Wait()
	for _, w := range out {
		if w.Code >= http.StatusInternalServerError {
			x.t.Fatalf("answer %d %s: no request may end with 5xx", w.Code, w.Body.String())
		}
	}
	if g.arrived.Load() == 0 {
		x.t.Fatal("no transaction of the round reached the barrier before its commit")
	}
	return out
}

// clean requires an empty integrity check for organization A.
func (x *matrix) clean(when string) {
	x.t.Helper()
	if v := scopetest.IntegrityViolations(x.t, x.f.Admin, x.f.OrgA); len(v) > 0 {
		x.t.Fatalf("integrity violations after %s: %v", when, v)
	}
}

func (x *matrix) scalar(sql string, args ...any) string {
	x.t.Helper()
	var s string
	if err := x.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		x.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// number reads a numeric value of the maintenance pool.
func (x *matrix) number(sql string, args ...any) float64 {
	x.t.Helper()
	var v float64
	if err := x.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		x.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// onHand is on_hand of the stock row of cons in bin.
func (x *matrix) onHand(cons, bin string) float64 {
	return x.number(`SELECT on_hand::float8 FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, cons, bin)
}

// ledger counts the ledger rows typ of the consumable.
func (x *matrix) ledger(cons, typ string) int {
	return int(x.number(`SELECT count(*)::float8 FROM stock_movement WHERE consumable_id = $1 AND movement_type = $2`, cons, typ))
}

// shelf creates a storage branch site > warehouse > zone > shelf of client
// 1 through the location repository and returns the shelf.
func (x *matrix) shelf(name string) string {
	x.t.Helper()
	repo := locations.NewPGRepository(x.f.App)
	parent := ""
	for _, kind := range []locations.Kind{locations.KindSite, locations.KindWarehouse, locations.KindZone, locations.KindShelf} {
		req := locations.CreateRequest{Kind: kind, ParentID: parent, Name: name + "-" + string(kind)}
		if kind == locations.KindSite {
			req.ClientID = x.f.Client1
		}
		l, err := repo.Create(x.f.OrgCtx(x.f.OrgA), x.f.OrgA, req)
		if err != nil {
			x.t.Fatalf("create %s %s: %v", kind, name, err)
		}
		parent = l.ID
	}
	return parent
}

// bin creates a bin on shelf through the location repository.
func (x *matrix) bin(shelf, name string) string {
	x.t.Helper()
	l, err := locations.NewPGRepository(x.f.App).Create(x.f.OrgCtx(x.f.OrgA), x.f.OrgA,
		locations.CreateRequest{Kind: locations.KindBin, ParentID: shelf, Name: name})
	if err != nil {
		x.t.Fatalf("create bin %s: %v", name, err)
	}
	return l.ID
}

// consumable creates a consumable of client 1 through its repository and
// books a receipt of qty into each of bins over POST /api/v1/stock-movements.
func (x *matrix) consumable(name string, qty float64, bins ...string) string {
	x.t.Helper()
	c := &consumable.Consumable{OrganizationID: x.f.OrgA, ClientID: x.f.Client1, Name: name}
	if err := consumable.NewPGRepository(x.f.App).Create(x.f.OrgCtx(x.f.OrgA), c); err != nil {
		x.t.Fatalf("create consumable %s: %v", name, err)
	}
	for _, bin := range bins {
		x.do(x.booking("receipt", c.ID, "", bin, qty), http.StatusCreated)
	}
	return c.ID
}

// booking is the call POST /api/v1/stock-movements of a consumable.
func (x *matrix) booking(typ, cons, from, to string, qty float64) call {
	body := map[string]any{"movement_type": typ, "consumable_id": cons, "quantity": qty}
	if from != "" {
		body["from_location_id"] = from
	}
	if to != "" {
		body["to_location_id"] = to
	}
	return x.post("/api/v1/stock-movements", body)
}

func problemType(w *httptest.ResponseRecorder) string {
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p.Type
}

// requireConflict fails unless w is 409 of the problem type want.
func requireConflict(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if w.Code != http.StatusConflict || problemType(w) != want {
		t.Fatalf("answer %d %s, want 409 %s", w.Code, w.Body.String(), want)
	}
}

// TestPhase2ConcurrencyMatrix runs the stock cases of ABN-03 in phase 2
// (WP-328): (1) reservation against assignment of one asset to a third
// party, (2) parallel quantity reservations beyond on_hand, (3) parallel
// retrievals down to zero, (4) two transfers of one asset with the same
// expected holder to different holders, (5) opposite bin transfers of a
// consumable and a transfer parallel to a retrieval down to zero.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPhase2ConcurrencyMatrix(t *testing.T) {
	f := scopetest.Seed(t, "a8")
	x := newMatrix(t, f)
	user := f.AppUser(t, f.OrgA, "wp328-user")
	third := f.AppUser(t, f.OrgA, "wp328-third")
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	shelf := x.shelf("wp328")

	t.Run("1 reservation against assignment to a third party", func(t *testing.T) {
		x.t = t
		reserveWins, assignWins := 0, 0
		for round := range 6 {
			asset := f.Asset(t, f.OrgA, f.Client1, fmt.Sprintf("wp328-race-%d", round))
			out := x.parallel(
				x.post("/api/v1/assets/"+asset+"/reserve", map[string]any{"reserved_until": future, "assignee_id": user, "reason": "wp328"}),
				x.post("/api/v1/assets/"+asset+"/assign", map[string]any{"assigned_user_id": third}),
			)
			reserved, assigned := out[0], out[1]
			switch {
			case reserved.Code == http.StatusCreated && assigned.Code != http.StatusCreated:
				reserveWins++
				requireConflict(t, assigned, assignment.ProblemAssetReserved)
				if n := x.scalar(`SELECT count(*)::text FROM reservation WHERE asset_id = $1 AND state = 'active'`, asset); n != "1" {
					t.Fatalf("round %d: %s active reservations, want 1", round, n)
				}
				if h := x.scalar(`SELECT COALESCE(assigned_user_id::text, '') FROM asset WHERE id = $1`, asset); h != "" {
					t.Fatalf("round %d: the reserved asset has the holder %s", round, h)
				}
			case assigned.Code == http.StatusCreated && reserved.Code != http.StatusCreated:
				assignWins++
				requireConflict(t, reserved, assignment.ProblemAssetAssigned)
				if n := x.scalar(`SELECT count(*)::text FROM reservation WHERE asset_id = $1`, asset); n != "0" {
					t.Fatalf("round %d: %s reservations of the assigned asset, want 0", round, n)
				}
				if h := x.scalar(`SELECT COALESCE(assigned_user_id::text, '') FROM asset WHERE id = $1`, asset); h != third {
					t.Fatalf("round %d: holder %q, want the third party", round, h)
				}
			default:
				t.Fatalf("round %d: reserve %d %s, assign %d %s: want exactly one winner", round,
					reserved.Code, reserved.Body.String(), assigned.Code, assigned.Body.String())
			}
		}
		t.Logf("reservation won %d, assignment %d rounds", reserveWins, assignWins)
		x.clean("reservation against assignment")
	})

	t.Run("2 parallel quantity reservations beyond on_hand", func(t *testing.T) {
		x.t = t
		bin := x.bin(shelf, "wp328-reserve-bin")
		for round := range 3 {
			cons := x.consumable(fmt.Sprintf("wp328-reserve-%d", round), 10, bin)
			const n = 6
			calls := make([]call, n)
			for i := range calls {
				calls[i] = x.post("/api/v1/reservations", map[string]any{"consumable_id": cons, "location_id": bin, "quantity": 3, "project_ref": "wp328"})
			}
			created := 0
			for _, w := range x.parallel(calls...) {
				if w.Code == http.StatusCreated {
					created++
					continue
				}
				requireConflict(t, w, consumable.ProblemInsufficientStock)
			}
			active := x.number(`SELECT COALESCE(sum(quantity), 0)::float8 FROM reservation WHERE consumable_id = $1 AND state = 'active'`, cons)
			reserved := x.number(`SELECT reserved::float8 FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, cons, bin)
			if created != 3 || active != 9 || reserved != active || active > x.onHand(cons, bin) {
				t.Fatalf("round %d: %d reservations, active %v, reserved %v, on_hand %v; want 3, 9, 9 and 10",
					round, created, active, reserved, x.onHand(cons, bin))
			}
		}
		x.clean("parallel quantity reservations")
	})

	t.Run("3 parallel retrievals down to zero", func(t *testing.T) {
		x.t = t
		bin := x.bin(shelf, "wp328-retrieval-bin")
		for round := range 3 {
			cons := x.consumable(fmt.Sprintf("wp328-retrieval-%d", round), 4, bin)
			const n = 6
			calls := make([]call, n)
			for i := range calls {
				calls[i] = x.booking("retrieval", cons, bin, "", 1)
			}
			booked := 0
			for _, w := range x.parallel(calls...) {
				if w.Code == http.StatusCreated {
					booked++
					continue
				}
				requireConflict(t, w, consumable.ProblemInsufficientStock)
			}
			if got := x.onHand(cons, bin); booked != 4 || got != 0 {
				t.Fatalf("round %d: %d retrievals, on_hand %v; want 4 and 0", round, booked, got)
			}
			if got := x.ledger(cons, "retrieval"); got != booked {
				t.Fatalf("round %d: %d retrievals in the ledger, want %d: a refused booking leaves no row", round, got, booked)
			}
		}
		x.clean("parallel retrievals")
	})

	t.Run("4 parallel transfers of one asset", func(t *testing.T) {
		x.t = t
		next1 := f.AppUser(t, f.OrgA, "wp328-next-1")
		next2 := f.AppUser(t, f.OrgA, "wp328-next-2")
		for round := range 5 {
			asset := f.Asset(t, f.OrgA, f.Client1, fmt.Sprintf("wp328-transfer-%d", round))
			x.do(x.post("/api/v1/assets/"+asset+"/assign", map[string]any{"assigned_user_id": user}), http.StatusCreated)
			out := x.parallel(
				x.post("/api/v1/assets/"+asset+"/transfer", map[string]any{"expected_holder": user, "assigned_user_id": next1}),
				x.post("/api/v1/assets/"+asset+"/transfer", map[string]any{"expected_holder": user, "assigned_user_id": next2}),
			)
			want := ""
			switch {
			case out[0].Code == http.StatusCreated && out[1].Code != http.StatusCreated:
				want = next1
				requireConflict(t, out[1], assignment.ProblemHolderMismatch)
			case out[1].Code == http.StatusCreated && out[0].Code != http.StatusCreated:
				want = next2
				requireConflict(t, out[0], assignment.ProblemHolderMismatch)
			default:
				t.Fatalf("round %d: transfers %d %s and %d %s: want exactly one", round,
					out[0].Code, out[0].Body.String(), out[1].Code, out[1].Body.String())
			}
			if h := x.scalar(`SELECT COALESCE(assigned_user_id::text, '') FROM asset WHERE id = $1`, asset); h != want {
				t.Fatalf("round %d: holder %q, want the winner %s", round, h, want)
			}
			if n := x.scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1 AND movement_type = 'assignment'`, asset); n != "2" {
				t.Fatalf("round %d: %s assignment movements, want 2 (assignment and one transfer)", round, n)
			}
			// The holder cache follows the movements assignment|return: the
			// integrity check compares them.
			x.clean(fmt.Sprintf("round %d of the parallel transfers", round))
		}
	})

	t.Run("5 bin transfers of a consumable", func(t *testing.T) {
		x.t = t
		b1, b2 := x.bin(shelf, "wp328-transfer-bin-1"), x.bin(shelf, "wp328-transfer-bin-2")
		for round := range 3 {
			cons := x.consumable(fmt.Sprintf("wp328-transfer-%d", round), 10, b1, b2)
			receipts := 20.0
			// Opposite transfers between the same bins: both pass, no 40P01.
			for i, w := range x.parallel(x.booking("transfer", cons, b1, b2, 3), x.booking("transfer", cons, b2, b1, 2)) {
				if w.Code != http.StatusCreated {
					t.Fatalf("round %d: opposite transfer %d: %d %s, want 201", round, i, w.Code, w.Body.String())
				}
			}
			if got1, got2 := x.onHand(cons, b1), x.onHand(cons, b2); got1 != 9 || got2 != 11 {
				t.Fatalf("round %d: on_hand %v/%v after the opposite transfers, want 9/11", round, got1, got2)
			}
			// A transfer parallel to the retrieval of the whole source bin:
			// exactly one passes, the stock never goes below zero.
			out := x.parallel(x.booking("transfer", cons, b1, b2, 4), x.booking("retrieval", cons, b1, "", 9))
			retrieved := 0.0
			switch {
			case out[0].Code == http.StatusCreated && out[1].Code != http.StatusCreated:
				requireConflict(t, out[1], consumable.ProblemInsufficientStock)
				if got := x.onHand(cons, b1); got != 5 {
					t.Fatalf("round %d: source on_hand %v after the transfer, want 5", round, got)
				}
			case out[1].Code == http.StatusCreated && out[0].Code != http.StatusCreated:
				retrieved = 9
				requireConflict(t, out[0], consumable.ProblemInsufficientStock)
				if got := x.onHand(cons, b1); got != 0 {
					t.Fatalf("round %d: source on_hand %v after the retrieval, want 0", round, got)
				}
			default:
				t.Fatalf("round %d: transfer %d %s, retrieval %d %s: want exactly one", round,
					out[0].Code, out[0].Body.String(), out[1].Code, out[1].Body.String())
			}
			// The sum over the bins is receipts minus retrievals.
			total := x.number(`SELECT sum(on_hand)::float8 FROM stock_level WHERE consumable_id = $1`, cons)
			if total != receipts-retrieved {
				t.Fatalf("round %d: on_hand over the bins %v, want %v", round, total, receipts-retrieved)
			}
			if got := x.number(`SELECT COALESCE(sum(quantity), 0)::float8 FROM stock_movement WHERE consumable_id = $1 AND movement_type = 'retrieval'`,
				cons); got != retrieved {
				t.Fatalf("round %d: retrieved %v in the ledger, want %v", round, got, retrieved)
			}
		}
		x.clean("bin transfers")
	})
}
