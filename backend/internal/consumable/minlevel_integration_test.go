package consumable_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Retest (WP-285, STK-01, NTF-05):
//
//	cd backend && go test -count=1 -p 1 -run 'TestMinLevel' ./internal/consumable/

// minLevelEnv books and checks consumables of organization A for the cases
// of the minimum level.
type minLevelEnv struct {
	t    *testing.T
	f    *scopetest.Fixture
	repo *consumable.PGRepository
	org  context.Context
}

// create creates a consumable of clientID with the minimum level min
// through PGRepository.Create.
func (e *minLevelEnv) create(clientID, name string, minLevel float64) string {
	e.t.Helper()
	c := &consumable.Consumable{OrganizationID: e.f.OrgA, ClientID: clientID, Name: name, SKU: name, Unit: "pcs", MinLevel: minLevel}
	if err := e.repo.Create(e.org, c); err != nil {
		e.t.Fatalf("create consumable %s: %v", name, err)
	}
	return c.ID
}

// book applies one booking through consumable.ApplyBooking in a request
// transaction of ctx.
func (e *minLevelEnv) book(ctx context.Context, cons, kind, from, to string, qty float64) error {
	return book(ctx, e.f, &consumable.Booking{OrganizationID: e.f.OrgA, ConsumableID: cons, MovementType: kind,
		FromLocationID: from, ToLocationID: to, Quantity: qty})
}

// mustBook books as the org-wide principal and fails on an error.
func (e *minLevelEnv) mustBook(cons, kind, from, to string, qty float64) {
	e.t.Helper()
	if err := e.book(e.org, cons, kind, from, to, qty); err != nil {
		e.t.Fatalf("%s of %v: %v", kind, qty, err)
	}
}

// setMin changes min_level through PGRepository.Update.
func (e *minLevelEnv) setMin(cons string, minLevel float64) {
	e.t.Helper()
	if _, err := e.repo.Update(e.org, e.f.OrgA, cons, consumable.UpdateConsumableRequest{MinLevel: &minLevel}); err != nil {
		e.t.Fatalf("min_level %v: %v", minLevel, err)
	}
}

// events returns the data of the stock.below_minimum events of the
// consumable in event_outbox, oldest first.
func (e *minLevelEnv) events(cons string) []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = $2 AND entity_id = $3 ORDER BY sequence`, e.f.OrgA, events.StockBelowMinimum, cons)
	if err != nil {
		e.t.Fatal(err)
	}
	raw, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		if err = json.Unmarshal(r, &out[i]); err != nil {
			e.t.Fatalf("data %s: %v", r, err)
		}
	}
	return out
}

// expectEvents fails unless the consumable has n events.
func (e *minLevelEnv) expectEvents(cons, what string, n int) []map[string]any {
	e.t.Helper()
	got := e.events(cons)
	if len(got) != n {
		e.t.Fatalf("%s: %d stock.below_minimum events %v, want %d", what, len(got), got, n)
	}
	return got
}

// total returns on_hand_total of the consumable after checking, without
// RLS, that it equals the sum of on_hand over all its bins.
func (e *minLevelEnv) total(cons string) float64 {
	e.t.Helper()
	var total, sum float64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT c.on_hand_total::float8,
		COALESCE((SELECT sum(s.on_hand) FROM stock_level s WHERE s.consumable_id = c.id), 0)::float8
		FROM consumable c WHERE c.id = $1`, cons).Scan(&total, &sum); err != nil {
		e.t.Fatal(err)
	}
	if total != sum {
		e.t.Fatalf("on_hand_total %v, but the bins hold %v", total, sum)
	}
	return total
}

// lowListed reports whether the filter low_stock lists the consumable.
func (e *minLevelEnv) lowListed(cons, name string) bool {
	e.t.Helper()
	list, _, err := e.repo.List(e.org, e.f.OrgA, consumable.FilterParams{LowStock: true, Search: name}, api.PaginationParams{Limit: 10})
	if err != nil {
		e.t.Fatal(err)
	}
	for i := range list {
		if list[i].ID == cons {
			return true
		}
	}
	return false
}

// TestMinLevelEvent covers the stored total stock and the event
// stock.below_minimum (STK-01, NTF-05, WP-285, E-128) against PostgreSQL:
// on_hand_total equals the sum of on_hand over all bins after every
// booking; the edge from "not below" to "below" (total < min_level) writes
// exactly one event with the fields of the article, a further take or a
// transfer between bins none, a refill to the minimum and a new fall
// another; raising min_level above the stock writes one, lowering it none;
// a new consumable with min_level > 0 is marked but not reported; a total
// equal to min_level is not below it (no event, no hit of low_stock);
// min_level 0 never reports; two parallel takes from different bins write
// exactly one event; a site-scoped principal books on the total stock over
// all sites (no event while the bins of other sites carry enough, one when
// the total falls below), a client-scoped one cannot book an article
// without client (ErrNotFound, nothing booked); a rolled back booking or
// change of min_level leaves neither event nor change of on_hand_total.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMinLevelEvent(t *testing.T) {
	f := scopetest.Seed(t, "30")
	e := &minLevelEnv{t: t, f: f, repo: consumable.NewPGRepository(f.App), org: f.OrgCtx(f.OrgA)}
	b1 := binTree(t, f, f.OrgA, f.Client1, "wp285-a")
	b2 := binTree(t, f, f.OrgA, f.Client1, "wp285-b")

	t.Run("edge once", func(t *testing.T) {
		e.t = t
		cons := e.create(f.Client1, "wp285-edge", 5)
		e.expectEvents(cons, "create with min_level 5", 0)
		if !e.lowListed(cons, "wp285-edge") {
			t.Error("a new consumable with min_level 5 is not marked")
		}
		e.mustBook(cons, "receipt", "", b1.bin, 8)
		e.mustBook(cons, "retrieval", b1.bin, "", 2)
		e.expectEvents(cons, "8 − 2 = 6 ≥ 5", 0)
		e.mustBook(cons, "retrieval", b1.bin, "", 2)
		data := e.expectEvents(cons, "6 − 2 = 4 < 5", 1)[0]
		for field, want := range map[string]any{"id": cons, "client_id": f.Client1, "sku": "wp285-edge", "name": "wp285-edge",
			"unit": "pcs", "on_hand_total": 4.0, "min_level": 5.0} {
			if data[field] != want {
				t.Errorf("data.%s = %v, want %v", field, data[field], want)
			}
		}
		e.mustBook(cons, "retrieval", b1.bin, "", 1)
		e.mustBook(cons, "transfer", b1.bin, b2.bin, 2)
		e.mustBook(cons, "correction", b2.bin, "", 1)
		e.expectEvents(cons, "further takes and a transfer below the threshold", 1)
		if got := e.total(cons); got != 2 {
			t.Fatalf("on_hand_total %v, want 2", got)
		}
		e.mustBook(cons, "receipt", "", b2.bin, 3)
		e.expectEvents(cons, "refilled to 5 = min_level", 1)
		if e.lowListed(cons, "wp285-edge") {
			t.Error("a total equal to min_level is listed by low_stock")
		}
		e.mustBook(cons, "retrieval", b2.bin, "", 1)
		e.expectEvents(cons, "5 − 1 = 4 < 5 after the refill", 2)
		e.total(cons)
	})

	t.Run("equal is not below", func(t *testing.T) {
		e.t = t
		cons := e.create(f.Client1, "wp285-equal", 3)
		e.mustBook(cons, "receipt", "", b1.bin, 5)
		e.mustBook(cons, "retrieval", b1.bin, "", 2)
		e.expectEvents(cons, "5 − 2 = 3 = min_level", 0)
		if e.lowListed(cons, "wp285-equal") {
			t.Error("a total equal to min_level is listed by low_stock")
		}
		c, err := e.repo.GetByID(e.org, f.OrgA, cons)
		if err != nil || c.StockLevel != 3 || c.LowStock() {
			t.Errorf("consumable %+v (%v), want stock_level 3, not low", c, err)
		}
		e.total(cons)
	})

	t.Run("min_level 0 never", func(t *testing.T) {
		e.t = t
		cons := e.create(f.Client1, "wp285-zero", 0)
		e.mustBook(cons, "receipt", "", b1.bin, 2)
		e.mustBook(cons, "retrieval", b1.bin, "", 2)
		e.expectEvents(cons, "min_level 0, emptied", 0)
		if e.lowListed(cons, "wp285-zero") {
			t.Error("an empty consumable with min_level 0 is listed by low_stock")
		}
		e.total(cons)
	})

	t.Run("min_level change", func(t *testing.T) {
		e.t = t
		cons := e.create("", "wp285-raise", 0)
		e.mustBook(cons, "receipt", "", b1.bin, 4)
		e.setMin(cons, 4)
		e.expectEvents(cons, "min_level raised to the stock", 0)
		e.setMin(cons, 6)
		data := e.expectEvents(cons, "min_level raised above the stock", 1)[0]
		if _, has := data["client_id"]; has || data["min_level"] != 6.0 || data["on_hand_total"] != 4.0 {
			t.Errorf("data %v, want min_level 6, on_hand_total 4 and no client_id", data)
		}
		e.setMin(cons, 8)
		e.expectEvents(cons, "min_level raised again while below", 1)
		e.setMin(cons, 2)
		e.expectEvents(cons, "min_level lowered", 1)
		name := "wp285-raise-renamed"
		if _, err := e.repo.Update(e.org, f.OrgA, cons, consumable.UpdateConsumableRequest{Name: &name}); err != nil {
			t.Fatal(err)
		}
		e.expectEvents(cons, "another field changed", 1)
		e.setMin(cons, 5)
		e.expectEvents(cons, "min_level raised above the stock again", 2)
		e.total(cons)
	})

	t.Run("parallel takes", func(t *testing.T) {
		e.t = t
		cons := e.create(f.Client1, "wp285-parallel", 5)
		e.mustBook(cons, "receipt", "", b1.bin, 3)
		e.mustBook(cons, "receipt", "", b2.bin, 3)
		e.expectEvents(cons, "6 ≥ 5", 0)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, bin := range []string{b1.bin, b2.bin} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = e.book(e.org, cons, "retrieval", bin, "", 2)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		e.expectEvents(cons, "two parallel takes of 2 from 6 with min_level 5", 1)
		if got := e.total(cons); got != 2 {
			t.Fatalf("on_hand_total %v, want 2", got)
		}
	})

	t.Run("site scope books on the total", func(t *testing.T) {
		e.t = t
		cons := e.create(f.Client1, "wp285-site", 5)
		e.mustBook(cons, "receipt", "", b1.bin, 3)
		e.mustBook(cons, "receipt", "", b2.bin, 10)
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(b1.site)
		siteCtx := database.ContextWithTenantScope(context.Background(), &scope)
		if err := e.book(siteCtx, cons, "retrieval", b1.bin, "", 3); err != nil {
			t.Fatalf("site-scoped take from its bin: %v", err)
		}
		e.expectEvents(cons, "site bin emptied, other site holds 10", 0)
		if got := e.total(cons); got != 10 {
			t.Fatalf("on_hand_total %v, want 10", got)
		}
		c, err := e.repo.GetByID(siteCtx, f.OrgA, cons)
		if err != nil || c.StockLevel != 10 {
			t.Errorf("stock_level for the site-scoped principal %+v (%v), want the total 10 (E-128 (a))", c, err)
		}
		e.mustBook(cons, "receipt", "", b1.bin, 3)
		e.mustBook(cons, "retrieval", b2.bin, "", 7)
		e.expectEvents(cons, "13 − 7 = 6 ≥ 5", 0)
		if err = e.book(siteCtx, cons, "retrieval", b1.bin, "", 2); err != nil {
			t.Fatalf("site-scoped take: %v", err)
		}
		data := e.expectEvents(cons, "site-scoped take to 4 < 5", 1)[0]
		if data["on_hand_total"] != 4.0 {
			t.Errorf("data.on_hand_total = %v, want the total 4", data["on_hand_total"])
		}
		e.total(cons)
	})

	t.Run("client scope cannot book an article without client", func(t *testing.T) {
		e.t = t
		cons := e.create("", "wp285-noclient", 5)
		e.mustBook(cons, "receipt", "", b1.bin, 6)
		for _, b := range []struct{ kind, from, to string }{{"retrieval", b1.bin, ""}, {"receipt", "", b1.bin}} {
			if err := e.book(f.ClientCtx(f.Client1), cons, b.kind, b.from, b.to, 2); !errors.Is(err, consumable.ErrNotFound) {
				t.Errorf("client-scoped %s of an article without client: %v, want ErrNotFound (404)", b.kind, err)
			}
		}
		if got := e.total(cons); got != 6 || onHand(t, f, cons, b1.bin) != 6 {
			t.Errorf("on_hand_total %v after the refused bookings, want 6", got)
		}
		e.expectEvents(cons, "refused client-scoped bookings", 0)
	})

	t.Run("rollback", func(t *testing.T) {
		e.t = t
		cons := e.create(f.Client1, "wp285-rollback", 5)
		e.mustBook(cons, "receipt", "", b1.bin, 6)
		abort := errors.New("abort")
		err := database.WithRequestTenant(e.org, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			if bookErr := consumable.ApplyBooking(ctx, tx, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: cons,
				MovementType: "retrieval", FromLocationID: b1.bin, Quantity: 3}); bookErr != nil {
				return bookErr
			}
			return abort
		})
		if !errors.Is(err, abort) {
			t.Fatalf("booking: %v, want the abort", err)
		}
		e.expectEvents(cons, "rolled back booking", 0)
		if got := e.total(cons); got != 6 {
			t.Fatalf("on_hand_total %v after the rollback, want 6", got)
		}
		// A refused event rolls the change of min_level back with it.
		refuse := "wp285_refuse_" + f.OrgA[:8]
		if _, err = f.Admin.Exec(context.Background(), `CREATE OR REPLACE FUNCTION `+refuse+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.type = 'stock.below_minimum' AND NEW.entity_id = '`+cons+`' THEN
		RAISE EXCEPTION 'wp285: the outbox refuses %', NEW.type;
	END IF;
	RETURN NEW;
END $$`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, dropErr := f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+refuse+`() CASCADE`); dropErr != nil {
				t.Errorf("drop trigger function: %v", dropErr)
			}
		})
		if _, err = f.Admin.Exec(context.Background(), `CREATE TRIGGER `+refuse+` BEFORE INSERT ON event_outbox
			FOR EACH ROW EXECUTE FUNCTION `+refuse+`()`); err != nil {
			t.Fatal(err)
		}
		minLevel := 7.0
		if _, err = e.repo.Update(e.org, f.OrgA, cons, consumable.UpdateConsumableRequest{MinLevel: &minLevel}); err == nil {
			t.Fatal("min_level raised although the outbox refused the event")
		}
		if err = e.book(e.org, cons, "retrieval", b1.bin, "", 3); err == nil {
			t.Fatal("booking succeeded although the outbox refused the event")
		}
		c, err := e.repo.GetByID(e.org, f.OrgA, cons)
		if err != nil || c.MinLevel != 5 || c.StockLevel != 6 {
			t.Fatalf("consumable %+v (%v) after the refused events, want min_level 5 and stock_level 6", c, err)
		}
		e.expectEvents(cons, "refused events", 0)
		e.total(cons)
	})
}
