package consumable_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// TestConsumableRepositoryClientScope runs the consumable repository and
// the stock per bin with the principal's tenant scope (TEN-06, WP-018,
// WP-283): a principal restricted to client 1 neither sees a client-2
// consumable nor the stock_level rows of a client-2 bin (also of an
// org-wide consumable) and cannot book on them. A principal restricted to
// a site sees the row of a bin only at the site the bin currently lies at.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "30")
	repo := consumable.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	bin1 := binTree(t, f, f.OrgA, f.Client1, "scope-c1")
	bin2 := binTree(t, f, f.OrgA, f.Client2, "scope-c2")
	own := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "toner-c1", Category: "toner", Unit: "pcs"}
	foreign := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "toner-c2", Category: "toner", Unit: "pcs"}
	shared := &consumable.Consumable{OrganizationID: f.OrgA, Name: "toner-shared", Category: "toner", Unit: "pcs"}
	receipt := func(ctx context.Context, cons, bin string) error {
		return book(ctx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: cons, MovementType: "receipt", ToLocationID: bin, Quantity: 5})
	}
	for _, c := range []*consumable.Consumable{own, foreign, shared} {
		if err := repo.Create(orgCtx, c); err != nil {
			t.Fatalf("org-wide consumable %s: %v", c.Name, err)
		}
	}
	for _, r := range [][2]string{{own.ID, bin1.bin}, {foreign.ID, bin2.bin}, {shared.ID, bin1.bin}, {shared.ID, bin2.bin}} {
		if err := receipt(orgCtx, r[0], r[1]); err != nil {
			t.Fatalf("org-wide receipt of %s into %s: %v", r[0], r[1], err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	list, _, err := repo.List(ctx, f.OrgA, consumable.FilterParams{}, page)
	if err != nil || len(list) != 2 {
		t.Fatalf("client-1 consumables: %+v err=%v, want the own and the org-wide one", list, err)
	}
	if _, err = listStock(ctx, f, foreign.ID); !errors.Is(err, consumable.ErrNotFound) {
		t.Fatalf("client-1 stock of a client-2 consumable: %v, want ErrNotFound", err)
	}
	if rows, listErr := listStock(ctx, f, shared.ID); listErr != nil || len(rows) != 1 || rows[0].LocationID != bin1.bin {
		t.Fatalf("client-1 stock of the org-wide consumable: %+v err=%v, want only the client-1 bin", rows, listErr)
	}
	if err = book(ctx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: foreign.ID, MovementType: "retrieval", FromLocationID: bin2.bin, Quantity: 1}); err == nil {
		t.Fatal("client-1 principal booked stock of a client-2 consumable")
	}
	// The org-wide consumable is not locked by a client-scoped principal
	// (E-128 (b)), so it books neither into its own nor into the client-2 bin.
	for _, bin := range []string{bin1.bin, bin2.bin} {
		if err = receipt(ctx, shared.ID, bin); err == nil {
			t.Errorf("client-1 principal booked the org-wide consumable into %s", bin)
		}
	}
	for _, r := range [][2]string{{foreign.ID, bin2.bin}, {shared.ID, bin1.bin}, {shared.ID, bin2.bin}} {
		if got := onHand(t, f, r[0], r[1]); got != 5 {
			t.Errorf("on_hand of %s in %s: %v, want 5", r[0], r[1], got)
		}
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, consumable.FilterParams{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}

	// A bin moved to another site of the client: the site-scoped principal
	// sees its row only at the new site.
	target := binTree(t, f, f.OrgA, f.Client1, "scope-move")
	sees := func(site string) bool {
		t.Helper()
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(site)
		rows, listErr := listStock(database.ContextWithTenantScope(context.Background(), &scope), f, own.ID)
		if listErr != nil {
			t.Fatalf("site-scoped stock list: %v", listErr)
		}
		return len(rows) == 1 && rows[0].LocationID == bin1.bin
	}
	if !sees(bin1.site) || sees(target.site) {
		t.Fatal("before the move the row is visible at its site only")
	}
	if _, err = locations.NewPGRepository(f.App).Move(orgCtx, f.OrgA, bin1.shelf, target.zone); err != nil {
		t.Fatalf("move the shelf of the bin: %v", err)
	}
	if sees(bin1.site) || !sees(target.site) {
		t.Error("after the move the row is visible at the new site only")
	}
}

// TestConsumableForeignClientUpdateAndConsumption covers the writes on an
// existing consumable (TEN-06, WP-537, booked per bin since WP-283): a
// principal restricted to client 1 can neither update a client-2
// consumable nor consume from or restock it (not found); the client-2 row
// keeps its fields and its stock in the bin. Its own consumable it updates
// and consumes from.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableForeignClientUpdateAndConsumption(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	repo := consumable.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	bin1 := binTree(t, f, f.OrgA, f.Client1, "wp537-c1")
	bin2 := binTree(t, f, f.OrgA, f.Client2, "wp537-c2")
	own := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "toner-c1", Category: "toner", Unit: "pcs", Notes: "shelf-1"}
	foreign := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "toner-c2", Category: "toner", Unit: "pcs", Notes: "shelf-2"}
	for _, c := range []struct {
		item *consumable.Consumable
		bin  string
	}{{own, bin1.bin}, {foreign, bin2.bin}} {
		if err := repo.Create(orgCtx, c.item); err != nil {
			t.Fatalf("org-wide consumable %s: %v", c.item.Name, err)
		}
		if err := book(orgCtx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: c.item.ID, MovementType: "receipt", ToLocationID: c.bin, Quantity: 5}); err != nil {
			t.Fatalf("org-wide receipt of %s: %v", c.item.Name, err)
		}
	}
	type row struct {
		client, name, notes string
		onHand              float64
		bins                int
	}
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), name, COALESCE(notes, ''),
			(SELECT COALESCE(sum(on_hand), 0)::float8 FROM stock_level WHERE consumable_id = consumable.id),
			(SELECT count(*) FROM stock_level WHERE consumable_id = consumable.id) FROM consumable WHERE id = $1`, id).
			Scan(&r.client, &r.name, &r.notes, &r.onHand, &r.bins); err != nil {
			t.Fatalf("stored consumable %s: %v", id, err)
		}
		return r
	}
	want := row{client: f.Client2, name: "toner-c2", notes: "shelf-2", onHand: 5, bins: 1}
	name, notes, client1 := "hijacked", "elsewhere", f.Client1
	req := consumable.UpdateConsumableRequest{Name: &name, Notes: &notes}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.Update(ctx, f.OrgA, foreign.ID, req); !errors.Is(err, consumable.ErrNotFound) {
		t.Errorf("client-1 update of a client-2 consumable: err=%v, want not found", err)
	}
	if _, err := repo.Update(ctx, f.OrgA, foreign.ID, consumable.UpdateConsumableRequest{ClientID: &client1}); !errors.Is(err, consumable.ErrNotFound) {
		t.Errorf("client-1 takeover of a client-2 consumable: err=%v, want not found", err)
	}
	for _, b := range []*consumable.Booking{
		{OrganizationID: f.OrgA, ConsumableID: foreign.ID, MovementType: "retrieval", FromLocationID: bin2.bin, Quantity: 2},
		{OrganizationID: f.OrgA, ConsumableID: foreign.ID, MovementType: "receipt", ToLocationID: bin2.bin, Quantity: 2},
		{OrganizationID: f.OrgA, ConsumableID: foreign.ID, MovementType: "receipt", ToLocationID: bin1.bin, Quantity: 2},
	} {
		if err := book(ctx, f, b); !errors.Is(err, consumable.ErrNotFound) {
			t.Errorf("client-1 %s on a client-2 consumable: err=%v, want not found", b.MovementType, err)
		}
	}
	if got := stored(foreign.ID); got != want {
		t.Errorf("client-2 consumable after foreign writes: %+v, want %+v", got, want)
	}

	// Positive control: the client-1 principal updates its own consumable
	// and consumes from its bin.
	if c, err := repo.Update(ctx, f.OrgA, own.ID, req); err != nil || c.Name != name {
		t.Fatalf("client-1 update of own consumable: %+v err=%v", c, err)
	}
	if err := book(ctx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: own.ID, MovementType: "retrieval", FromLocationID: bin1.bin, Quantity: 2}); err != nil {
		t.Fatalf("client-1 consumption from own consumable: %v", err)
	}
	if rows, err := listStock(ctx, f, own.ID); err != nil || len(rows) != 1 || rows[0].OnHand != 3 {
		t.Fatalf("client-1 stock of own consumable: %+v err=%v, want 3 in its bin", rows, err)
	}
	if got := stored(own.ID); got != (row{client: f.Client1, name: name, notes: notes, onHand: 3, bins: 1}) {
		t.Errorf("own consumable after update and consumption: %+v", got)
	}
}
