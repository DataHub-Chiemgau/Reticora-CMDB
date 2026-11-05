package stocktake_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// completer returns the completion service of the fixture on the
// PostgreSQL repositories, with the movement service of WP-274 (WP-761).
func completer(f *scopetest.Fixture) *stocktake.Service {
	return stocktake.NewService(stocktake.NewPGRepository(f.App), movements(f))
}

// movements returns the movement service on the PostgreSQL repository.
func movements(f *scopetest.Fixture) *movement.Service {
	return movement.NewService(movement.NewPGRepository(f.App))
}

// failingBooker is the test double of the movement service: it books
// through the real service and fails the call failAt (1-based).
type failingBooker struct {
	next   stocktake.Booker
	failAt int
	calls  int
}

var errBookingRefused = errors.New("booking refused by the test double")

func (b *failingBooker) Record(ctx context.Context, m *movement.Movement) error {
	b.calls++
	if b.calls == b.failAt {
		return errBookingRefused
	}
	return b.next.Record(ctx, m)
}

// withBooker returns a copy of the API of w whose completion books through
// booker.
func (w *scanWorld) withBooker(booker stocktake.Booker) *stocktakeAPI {
	repo := stocktake.NewPGRepository(w.f.App)
	mux := chi.NewRouter()
	stocktake.NewHandler(repo).WithService(stocktake.NewService(repo, booker)).RegisterRoutes(mux)
	return &stocktakeAPI{t: w.a.t, mux: mux, org: w.a.org, user: w.a.user}
}

// assetPlaces returns status and location_id of the assets of w.
func (w *scanWorld) assetPlaces(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := w.f.Admin.Query(context.Background(), `SELECT id::text, status || '@' || COALESCE(location_id::text, '') || '/' || COALESCE(location, '')
		FROM asset WHERE id = ANY($1::uuid[])`, []string{w.stay, w.moved, w.gone, w.extra})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, place string
		if err = rows.Scan(&id, &place); err != nil {
			t.Fatal(err)
		}
		out[id] = place
	}
	return out
}

// stock returns on_hand per consumable@bin of w.
func (w *scanWorld) stock(t *testing.T) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	rows, err := w.f.Admin.Query(context.Background(), `SELECT consumable_id::text || '@' || location_id::text, on_hand::float8
		FROM stock_level WHERE consumable_id = ANY($1::uuid[])`, []string{w.cable, w.plug})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var qty float64
		if err = rows.Scan(&key, &qty); err != nil {
			t.Fatal(err)
		}
		out[key] = qty
	}
	return out
}

// bookedMovements returns the movements of the stocktake (reason
// stocktake:<id>) as type:object from>to=quantity, sorted, and the number
// of their stock.movement events in the outbox.
func (w *scanWorld) bookedMovements(t *testing.T) ([]string, int) {
	t.Helper()
	bg := context.Background()
	rows, err := w.f.Admin.Query(bg, `SELECT movement_type || ':' || COALESCE(asset_id::text, consumable_id::text) || ' '
		|| COALESCE(from_location_id::text, '') || '>' || COALESCE(to_location_id::text, '') || '=' || COALESCE(quantity::float8::text, '')
		|| ' by ' || COALESCE(actor_id::text, '')
		FROM stock_movement WHERE reason = $1`, "stocktake:"+w.id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err = rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	slices.Sort(out)
	var events int
	if err = w.f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox o JOIN stock_movement m ON m.id::text = o.data->>'id'
		WHERE o.type = 'stock.movement' AND m.reason = $1`, "stocktake:"+w.id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return out, events
}

// mustScan records the scans and fails the test unless each answers 201.
func (w *scanWorld) mustScan(t *testing.T, bodies ...string) {
	t.Helper()
	for _, body := range bodies {
		if code, resp := w.scan(body); code != http.StatusCreated {
			t.Fatalf("scan %s: %d %v, want 201", body, code, resp)
		}
	}
}

// TestStocktakeCompletionBooksCorrections covers the completion of WP-761
// (INV-01): an asset found in another bin and an unexpected asset get a
// transfer to the place found (asset.location_id follows, status and the
// free-text location stay); a missing asset gets no movement; per
// consumable and bin a correction books the difference between the counted
// quantity and the book stock now, after a receipt into the bin between
// start and completion, lowering (from_location_id) and raising
// (to_location_id) it. Every movement carries reason stocktake:<id> and
// the actor and has its stock.movement event in the outbox; the answer
// lists the movements. Afterwards a scan, PATCH, DELETE, cancel and a second
// completion answer 409, and nothing changes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeCompletionBooksCorrections(t *testing.T) {
	w := newScanWorld(t, "c761")
	f, tr := w.f, w.tr
	orgCtx := f.OrgCtx(f.OrgA)
	if _, err := f.Admin.Exec(context.Background(), `UPDATE asset SET status = 'in_use', location = 'Lager Nord' WHERE id = ANY($1::uuid[])`,
		[]string{w.moved, w.gone}); err != nil {
		t.Fatal(err)
	}
	four := 4.0
	if err := movements(f).Record(orgCtx, &movement.Movement{OrganizationID: f.OrgA, ConsumableID: w.cable, MovementType: "receipt",
		ToLocationID: tr.bin1, Quantity: &four}); err != nil {
		t.Fatalf("receipt into bin1 after the start: %v", err)
	}
	w.mustScan(t,
		assetScan(w.stay, tr.room),
		assetScan(w.moved, tr.bin2),
		assetScan(w.extra, tr.room),
		stockScan(w.cable, tr.bin1, 7),
		stockScan(w.cable, tr.bin2, 5),
		stockScan(w.plug, tr.bin2, 1),
	)
	before := w.assetPlaces(t)

	code, body := w.a.do(orgCtx, http.MethodPost, w.path+"/complete", "")
	st, _ := body["stocktake"].(map[string]any)
	if code != http.StatusOK || st["status"] != stocktake.StatusCompleted || body["corrections_applied"] != float64(5) {
		t.Fatalf("completion: %d %v, want 200 completed with 5 corrections", code, body)
	}
	answered := []string{}
	for _, c := range body["corrections"].([]any) {
		m := c.(map[string]any)
		object, _ := m["asset_id"].(string)
		if object == "" {
			object, _ = m["consumable_id"].(string)
		}
		if id, _ := m["movement_id"].(string); id == "" {
			t.Errorf("correction without movement_id: %v", m)
		}
		quantity := ""
		if q, ok := m["quantity"].(float64); ok {
			quantity = fmt.Sprint(q)
		}
		from, _ := m["from_location_id"].(string)
		to, _ := m["to_location_id"].(string)
		answered = append(answered, fmt.Sprintf("%v:%s %s>%s=%s by %s", m["movement_type"], object, from, to, quantity, w.a.user))
	}
	slices.Sort(answered)

	want := []string{
		fmt.Sprintf("transfer:%s %s>%s= by %s", w.moved, tr.rack, tr.bin2, w.a.user),
		fmt.Sprintf("transfer:%s %s>%s= by %s", w.extra, tr.otherRoom, tr.room, w.a.user),
		fmt.Sprintf("correction:%s %s>=2 by %s", w.cable, tr.bin1, w.a.user),
		fmt.Sprintf("correction:%s >%s=2 by %s", w.cable, tr.bin2, w.a.user),
		fmt.Sprintf("correction:%s >%s=1 by %s", w.plug, tr.bin2, w.a.user),
	}
	slices.Sort(want)
	booked, events := w.bookedMovements(t)
	if !slices.Equal(booked, want) {
		t.Errorf("booked movements:\n  %v\nwant\n  %v", booked, want)
	}
	if !slices.Equal(answered, want) {
		t.Errorf("corrections of the answer:\n  %v\nwant\n  %v", answered, want)
	}
	if events != len(want) {
		t.Errorf("stock.movement events of the completion: %d, want %d", events, len(want))
	}

	wantPlaces := maps.Clone(before)
	wantPlaces[w.moved] = "in_use@" + tr.bin2 + "/Lager Nord"
	wantPlaces[w.extra] = strings.Replace(before[w.extra], "@"+tr.otherRoom+"/", "@"+tr.room+"/", 1)
	if got := w.assetPlaces(t); !maps.Equal(got, wantPlaces) {
		t.Errorf("assets after the completion: %v, want %v (the missing asset unchanged)", got, wantPlaces)
	}
	wantStock := map[string]float64{w.cable + "@" + tr.bin1: 7, w.cable + "@" + tr.bin2: 5, w.plug + "@" + tr.bin2: 1}
	if got := w.stock(t); !maps.Equal(got, wantStock) {
		t.Errorf("stock after the completion: %v, want %v", got, wantStock)
	}

	// The result is immutable for the API.
	for _, c := range []struct{ method, suffix, body string }{
		{http.MethodPost, "/scans", assetScan(w.gone, tr.bin1)},
		{http.MethodPatch, "", `{"title":"renamed"}`},
		{http.MethodDelete, "", ""},
		{http.MethodPost, "/cancel", ""},
		{http.MethodPost, "/complete", `{"apply_corrections":true}`},
	} {
		if code, body = w.a.do(orgCtx, c.method, w.path+c.suffix, c.body); code != http.StatusConflict {
			t.Errorf("%s …%s after the completion: %d %v, want 409", c.method, c.suffix, code, body)
		}
	}
	if again, _ := w.bookedMovements(t); !slices.Equal(again, want) {
		t.Errorf("movements after the refused writes: %v, want %v", again, want)
	}
	if code, body = w.a.do(orgCtx, http.MethodGet, w.path, ""); code != http.StatusOK || body["status"] != stocktake.StatusCompleted || body["title"] != "c761" {
		t.Errorf("stocktake after the refused writes: %d %v", code, body)
	}
}

// TestStocktakeCompletionRollsBack: a completion whose second booking
// fails (test double of the movement service) rolls back as a whole: no
// movement, no event, no result in the items, the asset where it was and
// the status in_progress. A correction lowering a bin below on_hand −
// reserved is refused by consumable.ApplyBooking (409 insufficient-stock)
// and rolls back the same way; with the reservation lifted the completion
// succeeds.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeCompletionRollsBack(t *testing.T) {
	w := newScanWorld(t, "r761")
	f, tr := w.f, w.tr
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	w.mustScan(t, assetScan(w.moved, tr.bin2), stockScan(w.cable, tr.bin1, 5), stockScan(w.cable, tr.bin2, 1))
	places, stock := w.assetPlaces(t), w.stock(t)
	unchanged := func(step string) {
		t.Helper()
		var status string
		var fixed int
		if err := f.Admin.QueryRow(bg, `SELECT status, (SELECT count(*) FROM stocktake_item WHERE stocktake_id = $1
			AND (classification IS NOT NULL OR counted_quantity IS NOT NULL)) FROM stocktake WHERE id = $1`, w.id).Scan(&status, &fixed); err != nil {
			t.Fatal(err)
		}
		booked, events := w.bookedMovements(t)
		if status != stocktake.StatusInProgress || fixed != 0 || len(booked) != 0 || events != 0 {
			t.Errorf("%s: status %s, %d fixed items, movements %v, %d events; want in_progress and nothing written", step, status, fixed, booked, events)
		}
		if got := w.assetPlaces(t); !maps.Equal(got, places) {
			t.Errorf("%s: assets %v, want %v", step, got, places)
		}
		if got := w.stock(t); !maps.Equal(got, stock) {
			t.Errorf("%s: stock %v, want %v", step, got, stock)
		}
	}

	double := &failingBooker{next: movements(f), failAt: 2}
	if code, body := w.withBooker(double).do(orgCtx, http.MethodPost, w.path+"/complete", ""); code != http.StatusInternalServerError || double.calls != 2 {
		t.Errorf("completion with a failing second booking: %d %v after %d calls, want 500 after 2", code, body, double.calls)
	}
	unchanged("after the failed booking")

	if _, err := f.Admin.Exec(bg, `UPDATE stock_level SET reserved = 3 WHERE consumable_id = $1 AND location_id = $2`, w.cable, tr.bin2); err != nil {
		t.Fatal(err)
	}
	code, body := w.a.do(orgCtx, http.MethodPost, w.path+"/complete", "")
	if code != http.StatusConflict || body["type"] != consumable.ProblemInsufficientStock {
		t.Errorf("completion lowering a reserved bin: %d %v, want 409 %s", code, body, consumable.ProblemInsufficientStock)
	}
	unchanged("after the refused correction")

	if _, err := f.Admin.Exec(bg, `UPDATE stock_level SET reserved = 0 WHERE consumable_id = $1 AND location_id = $2`, w.cable, tr.bin2); err != nil {
		t.Fatal(err)
	}
	if code, body = w.a.do(orgCtx, http.MethodPost, w.path+"/complete", ""); code != http.StatusOK || body["corrections_applied"] != float64(2) {
		t.Fatalf("completion without reservation: %d %v, want 200 with 2 corrections", code, body)
	}
	booked, events := w.bookedMovements(t)
	want := []string{
		fmt.Sprintf("correction:%s %s>=2 by %s", w.cable, tr.bin2, w.a.user),
		fmt.Sprintf("transfer:%s %s>%s= by %s", w.moved, tr.rack, tr.bin2, w.a.user),
	}
	if !slices.Equal(booked, want) || events != 2 {
		t.Errorf("movements: %v with %d events, want %v with 2", booked, events, want)
	}
}

// immutable reports whether err is the refusal of the trigger
// reject_completed_stocktake_mutation (000267).
func immutable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "stocktake_immutable"
}

// TestStocktakeImmutableTrigger: direct INSERT, UPDATE and DELETE on
// stock_scan and stocktake_item of a completed stocktake and UPDATE and
// DELETE of the stocktake fail on the trigger (23514 stocktake_immutable),
// for a cancelled one as well, even through the maintenance pool. The one
// exception, releasing the CI of a scan, lets jobs.HardDeleteCI remove a
// CI with a completed scan. The fixture cleanup removes the completed
// stocktake (scopetest.appendOnlyTables).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeImmutableTrigger(t *testing.T) {
	w := newScanWorld(t, "t761")
	f, tr := w.f, w.tr
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	user := w.a.user
	ci := f.CI(t, f.OrgA, f.Client1, "t761-ci")
	var ciScan string
	if err := f.Admin.QueryRow(bg, `INSERT INTO stock_scan (organization_id, stocktake_id, ci_id, scanned_by, scan_result)
		VALUES ($1, $2, $3, $4, 'found') RETURNING id::text`, f.OrgA, w.id, ci, user).Scan(&ciScan); err != nil {
		t.Fatalf("scan of the CI: %v", err)
	}
	w.mustScan(t, assetScan(w.stay, tr.room))
	if code, body := w.a.do(orgCtx, http.MethodPost, w.path+"/complete", ""); code != http.StatusOK {
		t.Fatalf("completion: %d %v", code, body)
	}
	cancelled := w.a.create(orgCtx, "t761 cancelled", tr.room)["id"].(string)
	if code, body := w.a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+cancelled+"/cancel", ""); code != http.StatusOK {
		t.Fatalf("cancel: %d %v", code, body)
	}

	for _, c := range []struct {
		name, sql string
		args      []any
	}{
		{"insert scan", `INSERT INTO stock_scan (organization_id, stocktake_id, asset_id, scanned_by, scan_result, location_id)
			VALUES ($1, $2, $3, $4, 'found', $5)`, []any{f.OrgA, w.id, w.gone, user, tr.bin1}},
		{"update scan", `UPDATE stock_scan SET notes = 'changed' WHERE stocktake_id = $1`, []any{w.id}},
		{"update scan ci and notes", `UPDATE stock_scan SET ci_id = NULL, notes = 'changed' WHERE id = $1`, []any{ciScan}},
		{"delete scan", `DELETE FROM stock_scan WHERE stocktake_id = $1`, []any{w.id}},
		{"insert item", `INSERT INTO stocktake_item (organization_id, stocktake_id, item_kind, asset_id, location_id, expected_quantity)
			VALUES ($1, $2, 'asset', $3, $4, 0)`, []any{f.OrgA, w.id, w.extra, tr.room}},
		{"update item", `UPDATE stocktake_item SET counted_quantity = 9 WHERE stocktake_id = $1`, []any{w.id}},
		{"delete item", `DELETE FROM stocktake_item WHERE stocktake_id = $1`, []any{w.id}},
		{"update stocktake", `UPDATE stocktake SET title = 'changed' WHERE id = $1`, []any{w.id}},
		{"reopen stocktake", `UPDATE stocktake SET status = 'in_progress' WHERE id = $1`, []any{w.id}},
		{"delete stocktake", `DELETE FROM stocktake WHERE id = $1`, []any{w.id}},
		{"update cancelled stocktake", `UPDATE stocktake SET title = 'changed' WHERE id = $1`, []any{cancelled}},
		{"delete cancelled stocktake", `DELETE FROM stocktake WHERE id = $1`, []any{cancelled}},
		{"insert item of the cancelled stocktake", `INSERT INTO stocktake_item (organization_id, stocktake_id, item_kind, asset_id, location_id, expected_quantity)
			VALUES ($1, $2, 'asset', $3, $4, 1)`, []any{f.OrgA, cancelled, w.stay, tr.room}},
	} {
		if _, err := f.Admin.Exec(bg, c.sql, c.args...); !immutable(err) {
			t.Errorf("%s: %v, want 23514 stocktake_immutable", c.name, err)
		}
	}

	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() - interval '1 year' WHERE id = $1`, ci); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(bg, f.Admin, func(tx pgx.Tx) error {
		return jobs.HardDeleteCI(bg, tx, f.OrgA, ci)
	}); err != nil {
		t.Fatalf("HardDeleteCI with a completed scan: %v", err)
	}
	var released bool
	if err := f.Admin.QueryRow(bg, `SELECT ci_id IS NULL FROM stock_scan WHERE id = $1`, ciScan).Scan(&released); err != nil || !released {
		t.Errorf("scan after HardDeleteCI: released=%v %v, want ci_id NULL", released, err)
	}

	// The fixture cleanup (run again by Seed) removes the completed and
	// the cancelled stocktake.
	scopetest.Seed(t, "33")
	var left int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stocktake WHERE id = ANY($1::uuid[])`, []string{w.id, cancelled}).Scan(&left); err != nil || left != 0 {
		t.Errorf("stocktakes after the fixture cleanup: %d %v, want 0", left, err)
	}
}

// TestStocktakeCompletionConcurrency: a scan arriving with the completion
// is either counted before (its object in the result) or refused with 409
// after it (FOR SHARE in AddScan against FOR UPDATE in complete); of two
// parallel completions one books and the other answers 409, so every
// correction is booked once.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeCompletionConcurrency(t *testing.T) {
	w := newScanWorld(t, "p761")
	f, tr := w.f, w.tr
	orgCtx := f.OrgCtx(f.OrgA)
	w.mustScan(t, assetScan(w.moved, tr.bin2))

	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, 3)
	run := func(i int, method, suffix, body string) {
		defer wg.Done()
		<-start
		codes[i], _ = w.a.do(orgCtx, method, w.path+suffix, body)
	}
	wg.Add(3)
	go run(0, http.MethodPost, "/scans", assetScan(w.extra, tr.room))
	go run(1, http.MethodPost, "/complete", "")
	go run(2, http.MethodPost, "/complete", "")
	close(start)
	wg.Wait()

	completed := 0
	for _, code := range codes[1:] {
		switch code {
		case http.StatusOK:
			completed++
		case http.StatusConflict:
		default:
			t.Errorf("parallel completion: %d, want 200 or 409", code)
		}
	}
	if completed != 1 {
		t.Errorf("parallel completions: %v, want exactly one 200", codes[1:])
	}
	counted := false
	for _, it := range w.a.items(orgCtx, w.id, 200) {
		if it["asset_id"] == w.extra {
			counted = true
		}
	}
	booked, events := w.bookedMovements(t)
	want := []string{fmt.Sprintf("transfer:%s %s>%s= by %s", w.moved, tr.rack, tr.bin2, w.a.user)}
	switch codes[0] {
	case http.StatusCreated:
		want = append(want, fmt.Sprintf("transfer:%s %s>%s= by %s", w.extra, tr.otherRoom, tr.room, w.a.user))
		if !counted {
			t.Error("the scan before the completion is not part of the result")
		}
	case http.StatusConflict:
		if counted {
			t.Error("the refused scan is part of the result")
		}
	default:
		t.Errorf("parallel scan: %d, want 201 or 409", codes[0])
	}
	slices.Sort(want)
	// The planned corrections of the cable (5 and 3 expected, nothing
	// counted) lower both bins of the snapshot to 0.
	want = append(want,
		fmt.Sprintf("correction:%s %s>=5 by %s", w.cable, tr.bin1, w.a.user),
		fmt.Sprintf("correction:%s %s>=3 by %s", w.cable, tr.bin2, w.a.user))
	slices.Sort(want)
	if !slices.Equal(booked, want) || events != len(want) {
		t.Errorf("movements of the parallel completions: %v with %d events, want %v once each", booked, events, want)
	}
}

// TestStocktakeImmutableFollowsLocationScope: when the area of a completed
// stocktake moves to another site (room into a building of another site),
// the scope refresh re-derives site_id of the stocktake, its items and its
// scans (the second exception of reject_completed_stocktake_mutation,
// 000267); a change of the content stays refused with 23514
// stocktake_immutable.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeImmutableFollowsLocationScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	a := newStocktakeAPI(t, f, f.AppUser(t, f.OrgA, "m761-counter"))
	tr := seedAreaTree(t, f, f.Client1, "m761")
	asset := f.Asset(t, f.OrgA, f.Client1, "m761-asset")
	placeAsset(t, f, asset, tr.room)
	id := a.create(orgCtx, "m761", tr.room)["id"].(string)
	path := "/api/v1/stocktakes/" + id
	if code, body := a.do(orgCtx, http.MethodPost, path+"/start", ""); code != http.StatusOK {
		t.Fatalf("start: %d %v", code, body)
	}
	if code, body := a.do(orgCtx, http.MethodPost, path+"/scans", assetScan(asset, tr.rack)); code != http.StatusCreated {
		t.Fatalf("scan: %d %v", code, body)
	}
	if code, body := a.do(orgCtx, http.MethodPost, path+"/complete", ""); code != http.StatusOK {
		t.Fatalf("completion: %d %v", code, body)
	}

	var otherSite, otherBuilding string
	if err := f.Admin.QueryRow(bg, `SELECT r.building_id::text, b.site_id::text FROM room r JOIN building b ON b.id = r.building_id
		WHERE r.id = $1`, tr.otherRoom).Scan(&otherBuilding, &otherSite); err != nil {
		t.Fatal(err)
	}
	if otherSite == tr.site {
		t.Fatal("precondition: the other room lies on the site of the area")
	}
	if _, err := f.Admin.Exec(bg, `UPDATE room SET building_id = $2 WHERE id = $1`, tr.room, otherBuilding); err != nil {
		t.Fatalf("move of the area to another site: %v, want accepted", err)
	}
	var stSite, status string
	var items, itemsMoved, scans, scansMoved int
	if err := f.Admin.QueryRow(bg, `SELECT site_id::text, status,
		(SELECT count(*) FROM stocktake_item WHERE stocktake_id = $1), (SELECT count(*) FROM stocktake_item WHERE stocktake_id = $1 AND site_id = $2),
		(SELECT count(*) FROM stock_scan WHERE stocktake_id = $1), (SELECT count(*) FROM stock_scan WHERE stocktake_id = $1 AND site_id = $2)
		FROM stocktake WHERE id = $1`, id, otherSite).Scan(&stSite, &status, &items, &itemsMoved, &scans, &scansMoved); err != nil {
		t.Fatal(err)
	}
	if stSite != otherSite || status != stocktake.StatusCompleted || items == 0 || itemsMoved != items || scans == 0 || scansMoved != scans {
		t.Errorf("after the move: stocktake site %s (%s), %d/%d items and %d/%d scans on the new site; want all on %s, still completed",
			stSite, status, itemsMoved, items, scansMoved, scans, otherSite)
	}

	for _, c := range []struct{ name, sql string }{
		{"totals of the stocktake", `UPDATE stocktake SET total_scanned = total_scanned + 1 WHERE id = $1`},
		{"scope and title of the stocktake", `UPDATE stocktake SET site_id = NULL, title = 'changed' WHERE id = $1`},
		{"counted quantity of an item", `UPDATE stocktake_item SET counted_quantity = 9 WHERE stocktake_id = $1`},
		{"notes of a scan", `UPDATE stock_scan SET notes = 'moved' WHERE stocktake_id = $1`},
	} {
		if _, err := f.Admin.Exec(bg, c.sql, id); !immutable(err) {
			t.Errorf("%s: %v, want 23514 stocktake_immutable", c.name, err)
		}
	}
}
