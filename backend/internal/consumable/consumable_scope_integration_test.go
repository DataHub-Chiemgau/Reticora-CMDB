package consumable_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestConsumableRepositoryClientScope runs the consumable repository with the
// principal's tenant scope (TEN-06, WP-018): a principal restricted to client
// 1 neither sees a client-2 consumable nor its stock movements and cannot
// book stock on it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "30")
	repo := consumable.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "toner-c1", Category: "toner", Unit: "pcs"}
	foreign := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "toner-c2", Category: "toner", Unit: "pcs"}
	for _, c := range []*consumable.Consumable{own, foreign} {
		if err := repo.Create(orgCtx, c); err != nil {
			t.Fatalf("org-wide consumable %s: %v", c.Name, err)
		}
		if _, err := repo.AddMovement(orgCtx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: c.ID, Direction: "in", Quantity: 5}); err != nil {
			t.Fatalf("org-wide stock movement on %s: %v", c.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	list, _, err := repo.List(ctx, f.OrgA, consumable.FilterParams{}, page)
	if err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 consumables: %+v err=%v, want only the own one", list, err)
	}
	moves, total, err := repo.ListMovements(ctx, f.OrgA, foreign.ID, page)
	if err != nil || total != 0 || len(moves) != 0 {
		t.Fatalf("client-1 movements of client-2 consumable: total=%d err=%v, want none", total, err)
	}
	if _, err = repo.AddMovement(ctx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: foreign.ID, Direction: "out", Quantity: 1}); err == nil {
		t.Fatal("client-1 principal booked stock on a client-2 consumable")
	}
	var level float64
	if err = f.Admin.QueryRow(context.Background(), `SELECT stock_level FROM consumable WHERE id = $1`, foreign.ID).Scan(&level); err != nil || level != 5 {
		t.Fatalf("client-2 stock level: %v err=%v, want 5", level, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, consumable.FilterParams{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestConsumableForeignClientUpdateAndConsumption covers the writes on an
// existing consumable (TEN-06, WP-537): a principal restricted to client 1
// can neither update a client-2 consumable nor consume from or restock it
// (not found); the client-2 row keeps its fields and stock level and gets no
// stock movement. Its own consumable it updates and consumes from.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableForeignClientUpdateAndConsumption(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	repo := consumable.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "toner-c1", Category: "toner", Unit: "pcs", Location: "shelf-1"}
	foreign := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "toner-c2", Category: "toner", Unit: "pcs", Location: "shelf-2"}
	for _, c := range []*consumable.Consumable{own, foreign} {
		if err := repo.Create(orgCtx, c); err != nil {
			t.Fatalf("org-wide consumable %s: %v", c.Name, err)
		}
		if _, err := repo.AddMovement(orgCtx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: c.ID, Direction: "in", Quantity: 5}); err != nil {
			t.Fatalf("org-wide stock movement on %s: %v", c.Name, err)
		}
	}
	type row struct {
		client, name, location string
		level                  float64
		movements              int
	}
	stored := func(id string) row {
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), name, COALESCE(location, ''), stock_level,
			(SELECT count(*) FROM stock_movement WHERE consumable_id = consumable.id) FROM consumable WHERE id = $1`, id).
			Scan(&r.client, &r.name, &r.location, &r.level, &r.movements); err != nil {
			t.Fatalf("stored consumable %s: %v", id, err)
		}
		return r
	}
	want := row{client: f.Client2, name: "toner-c2", location: "shelf-2", level: 5, movements: 1}
	name, location, client1 := "hijacked", "elsewhere", f.Client1
	req := consumable.UpdateConsumableRequest{Name: &name, Location: &location}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.Update(ctx, f.OrgA, foreign.ID, req); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("client-1 update of a client-2 consumable: err=%v, want not found", err)
	}
	if _, err := repo.Update(ctx, f.OrgA, foreign.ID, consumable.UpdateConsumableRequest{ClientID: &client1}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("client-1 takeover of a client-2 consumable: err=%v, want not found", err)
	}
	for _, direction := range []string{"out", "in"} {
		if _, err := repo.AddMovement(ctx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: foreign.ID, Direction: direction, Quantity: 2, Reason: "hijacked"}); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("client-1 %s movement on a client-2 consumable: err=%v, want not found", direction, err)
		}
	}
	if got := stored(foreign.ID); got != want {
		t.Errorf("client-2 consumable after foreign writes: %+v, want %+v", got, want)
	}

	// Positive control: the client-1 principal updates its own consumable
	// and consumes from it.
	if c, err := repo.Update(ctx, f.OrgA, own.ID, req); err != nil || c.Name != name {
		t.Fatalf("client-1 update of own consumable: %+v err=%v", c, err)
	}
	if c, err := repo.AddMovement(ctx, &consumable.Movement{OrganizationID: f.OrgA, ConsumableID: own.ID, Direction: "out", Quantity: 2}); err != nil || c.StockLevel != 3 {
		t.Fatalf("client-1 consumption from own consumable: %+v err=%v", c, err)
	}
	if got := stored(own.ID); got != (row{client: f.Client1, name: name, location: location, level: 3, movements: 2}) {
		t.Errorf("own consumable after update and consumption: %+v", got)
	}
}
