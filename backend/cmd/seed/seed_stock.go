package main

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
)

// The storage branch, the consumables, the orders and the stocktake of the
// Phase-2 seed (WP-819; LOC-08, STK-01, STK-05, INV-01).

// binID is the deterministic id of bin n of the storage branch.
func (s *stockSeed) binID(n int) string { return s.org.uid("location", "bin", fmt.Sprint(n)) }

// warehouseID is the id of the warehouse, the area of the stocktake.
func (s *stockSeed) warehouseID() string { return s.org.uid("location", "warehouse") }

// ensureStorage creates the storage branch below the site hq (LOC-08):
// warehouse → zone → shelf → bins. location_maintain derives client, site
// and path from the parent; an existing node keeps its row.
func (s *stockSeed) ensureStorage(ctx context.Context) error {
	zone, shelf := s.org.uid("location", "zone"), s.org.uid("location", "shelf")
	nodes := []struct{ id, parent, kind, name string }{
		{s.warehouseID(), s.org.uid("site", "hq"), "warehouse", "Warehouse hq"},
		{zone, s.warehouseID(), "zone", "Zone A"},
		{shelf, zone, "shelf", "Shelf A1"},
	}
	for n := 1; n <= s.content.bins; n++ {
		nodes = append(nodes, struct{ id, parent, kind, name string }{s.binID(n), shelf, "bin", fmt.Sprintf("Bin A1-%02d", n)})
	}
	for _, node := range nodes {
		if _, err := s.tx.Exec(ctx, `INSERT INTO location (id, organization_id, parent_id, kind, name)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`, node.id, s.org.id, node.parent, node.kind, node.name); err != nil {
			return fmt.Errorf("%s %s: %w", node.kind, node.name, err)
		}
	}
	return nil
}

// MinLevel is the minimum level of every seeded consumable.
const MinLevel = 10

// seedConsumableNames are the consumables of the seed (E-40), in turn.
var seedConsumableNames = []string{
	"Patch cable Cat6 1 m", "Patch cable Cat6 3 m", "SFP+ SR transceiver", "SFP+ LR transceiver", "Power cord C13/C14",
	"Rack screws M6", "Cable ties 200 mm", "Velcro strap 5 m", "Fibre cable LC/LC 2 m", "DAC cable 10G 1 m",
	"Hard disk 2.4 TB SAS", "SSD 960 GB SATA", "DIMM 32 GB DDR4", "Fan module", "Power supply 750 W",
	"Blanking panel 1U", "Label tape 12 mm", "Toner cartridge", "Console cable USB-C", "Patch cable Cat6 0.5 m",
}

// receipt is a receipt booking of a consumable into a bin.
type receipt struct {
	key      string
	bin      int
	quantity float64
}

// receiptsOf are the receipts of consumable n (1-based) with b bins: one
// into bin (n-1) mod b + 1, every fifth a second one into the next bin;
// the last consumable of the demo organization stays below MinLevel.
func receiptsOf(n, consumables, b int) []receipt {
	first := receipt{key: "1", bin: (n-1)%b + 1, quantity: float64(20 + n)}
	if consumables > 2 && n == consumables {
		first.quantity = MinLevel - 6
		return []receipt{first}
	}
	out := []receipt{first}
	if n%5 == 0 && b > 1 {
		out = append(out, receipt{key: "2", bin: n%b + 1, quantity: 5})
	}
	return out
}

// ensureConsumables creates the consumables (E-40) with sku, min_level
// and supplier, found again by sku, and books their stock only through
// receipt movements of the movement service (WP-283), which carries
// stock_level forward in this transaction. A movement has a deterministic
// id, so a second run books nothing twice.
func (s *stockSeed) ensureConsumables(ctx context.Context) error {
	for n := 1; n <= s.content.consumables; n++ {
		sku := fmt.Sprintf("SKU-%04d", n)
		id, err := s.lookup(ctx, `SELECT id::text FROM consumable WHERE organization_id = $1 AND sku = $2`, s.org.id, sku)
		if err != nil {
			return err
		}
		if id == "" {
			c := &consumable.Consumable{OrganizationID: s.org.id, Name: seedConsumableNames[(n-1)%len(seedConsumableNames)],
				SKU: sku, Category: "general", Unit: "pcs", MinLevel: MinLevel, Notes: "Seed (WP-819)"}
			if len(s.supplierIDs) > 0 {
				c.SupplierID = s.supplierIDs[n%len(s.supplierIDs)]
			}
			if err = s.consumables.Create(ctx, c); err != nil {
				return fmt.Errorf("consumable %s: %w", sku, err)
			}
			id = c.ID
		}
		s.consumableIDs = append(s.consumableIDs, id)
		for _, r := range receiptsOf(n, s.content.consumables, s.content.bins) {
			movementID := s.org.uid("movement", "receipt", sku, r.key)
			existing, lookupErr := s.lookup(ctx, `SELECT id::text FROM stock_movement WHERE id = $1`, movementID)
			if lookupErr != nil {
				return lookupErr
			}
			if existing != "" {
				continue
			}
			quantity := r.quantity
			m := &movement.Movement{ID: movementID, OrganizationID: s.org.id, ItemKind: "consumable", ConsumableID: id,
				MovementType: "receipt", ToLocationID: s.binID(r.bin), Quantity: &quantity, ActorID: s.admin,
				Reason: "seed: initial stock", OccurredAt: s.org.at.AddDate(0, 0, -7)}
			if err = s.movements.RecordTx(ctx, s.tx, m); err != nil {
				return fmt.Errorf("receipt %s into bin %d: %w", sku, r.bin, err)
			}
		}
	}
	return nil
}

// ensureScaledConsumables writes the consumables beyond those of the level
// up to orgSeed.consumables (WP-821) set-based in one statement, like the
// consumables of ensureConsumables (name in turn, sku SKU-<n>, min_level,
// supplier) with the deterministic id <prefix>/consumable/<n>, and books
// the stock of each through one receipt of the movement service, the
// booking way of ensureConsumables, into bin (n-1) mod bins + 1. Every
// quantity reaches MinLevel, so the consumables below their minimum stay
// those of the level. An existing consumable or receipt is skipped.
func (s *stockSeed) ensureScaledConsumables(ctx context.Context) error {
	first := s.content.consumables + 1
	if s.org.consumables < first {
		return nil
	}
	if _, err := s.tx.Exec(ctx, `
		INSERT INTO consumable (id, organization_id, name, sku, supplier_id, category, unit, min_level, notes)
		SELECT md5($1 || '/consumable/' || n)::uuid, $2, ($3::text[])[(n - 1) % cardinality($3::text[]) + 1],
			'SKU-' || CASE WHEN n < 10000 THEN lpad(n::text, 4, '0') ELSE n::text END,
			CASE WHEN cardinality($4::uuid[]) > 0 THEN ($4::uuid[])[n % cardinality($4::uuid[]) + 1] END,
			'general', 'pcs', $5, 'Seed (WP-821)'
		FROM generate_series($6::int, $7::int) AS n
		ON CONFLICT DO NOTHING`,
		s.org.prefix, s.org.id, seedConsumableNames, s.supplierIDs, MinLevel, first, s.org.consumables); err != nil {
		return err
	}
	rows, err := s.tx.Query(ctx, `
		SELECT c.id::text, c.sku, n FROM generate_series($3::int, $4::int) AS n
		JOIN consumable c ON c.id = md5($2 || '/consumable/' || n)::uuid AND c.organization_id = $1
		WHERE NOT EXISTS (SELECT 1 FROM stock_movement m WHERE m.id = md5($2 || '/movement/receipt/' || c.sku || '/1')::uuid)
		ORDER BY n`, s.org.id, s.org.prefix, first, s.org.consumables)
	if err != nil {
		return err
	}
	type pending struct {
		id, sku string
		n       int
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.sku, &p.n); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, p := range todo {
		quantity := float64(MinLevel + 10 + p.n%20)
		m := &movement.Movement{ID: s.org.uid("movement", "receipt", p.sku, "1"), OrganizationID: s.org.id,
			ItemKind: "consumable", ConsumableID: p.id, MovementType: "receipt", ToLocationID: s.binID((p.n-1)%s.content.bins + 1),
			Quantity: &quantity, ActorID: s.admin, Reason: "seed: initial stock", OccurredAt: s.org.at.AddDate(0, 0, -7)}
		if err = s.movements.RecordTx(ctx, s.tx, m); err != nil {
			return fmt.Errorf("receipt %s: %w", p.sku, err)
		}
	}
	return nil
}

// seedOrder is an internal order of the seed: its target state and the
// quantity received of its one position of 10.
type seedOrder struct {
	status   string
	received float64
}

// seedOrders are the orders of the demo organization, one per state of
// the state machine (WP-286).
var seedOrders = []seedOrder{
	{status: order.StatusDraft}, {status: order.StatusSubmitted}, {status: order.StatusApproved},
	{status: order.StatusRejected}, {status: order.StatusOrdered}, {status: order.StatusPartiallyReceived, received: 4},
	{status: order.StatusReceived, received: 10}, {status: order.StatusClosed, received: 10}, {status: order.StatusCancelled},
}

// orderPositionQuantity is the quantity of the one position of a seeded
// order.
const orderPositionQuantity = 10

// orderPath is the way of the state machine to status: the transitions
// before the goods receipt and after it, each with its actor (the
// requester engineer or the approver, the administrator).
func (s *stockSeed) orderPath(status string) (before, after []struct{ status, actor string }) {
	type step = struct{ status, actor string }
	submit, approve, ordered := step{order.StatusSubmitted, s.engineer}, step{order.StatusApproved, s.admin},
		step{order.StatusOrdered, s.admin}
	switch status {
	case order.StatusSubmitted:
		return []step{submit}, nil
	case order.StatusApproved:
		return []step{submit, approve}, nil
	case order.StatusRejected:
		return []step{submit, {order.StatusRejected, s.admin}}, nil
	case order.StatusOrdered, order.StatusPartiallyReceived, order.StatusReceived:
		return []step{submit, approve, ordered}, nil
	case order.StatusClosed:
		return []step{submit, approve, ordered}, []step{{order.StatusClosed, s.admin}}
	case order.StatusCancelled:
		return []step{submit, {order.StatusCancelled, s.engineer}}, nil
	}
	return nil, nil
}

// ensureOrders creates the internal orders of the demo organization, found
// again by order number: requested by engineer with the first supplier and
// bin 1 as target, one position of a consumable, then moved along the
// state machine (SetStatus, approval by the administrator under the
// four-eyes rule) and, for partially_received, received and closed,
// through the goods receipt (Receive), which books receipt movements with
// order_id through the movement service.
func (s *stockSeed) ensureOrders(ctx context.Context) error {
	if !s.content.orders {
		return nil
	}
	for i, spec := range seedOrders {
		number := "SEED-ORD-" + spec.status
		id, err := s.lookup(ctx, `SELECT id::text FROM internal_order WHERE organization_id = $1 AND order_number = $2`, s.org.id, number)
		if err != nil || id != "" {
			if err != nil {
				return err
			}
			continue
		}
		o := &order.Order{OrganizationID: s.org.id, OrderNumber: number, Title: "Seed order (" + spec.status + ")",
			RequestedBy: s.engineer, SupplierID: s.supplierIDs[0], TargetLocationID: s.binID(1), Notes: "Seed (WP-819)"}
		if err = s.orders.Create(ctx, o); err != nil {
			return fmt.Errorf("order %s: %w", number, err)
		}
		consumableID := s.consumableIDs[i%len(s.consumableIDs)]
		created, err := s.orders.AddItem(ctx, &order.Item{OrganizationID: s.org.id, OrderID: o.ID,
			Description: "Seed position", Quantity: orderPositionQuantity, UnitPrice: 12.5, ConsumableID: consumableID})
		if err != nil {
			return fmt.Errorf("order %s: position: %w", number, err)
		}
		before, after := s.orderPath(spec.status)
		for _, step := range before {
			if _, err = s.orders.SetStatus(ctx, s.org.id, o.ID, step.status, step.actor); err != nil {
				return fmt.Errorf("order %s: %s: %w", number, step.status, err)
			}
		}
		if spec.received > 0 {
			if _, err = s.orders.Receive(ctx, &order.Receipt{OrganizationID: s.org.id, OrderID: o.ID, ActorID: s.admin,
				Items: []order.ReceiveItemRequest{{ItemID: created.Items[0].ID, Quantity: spec.received}}}, s.movements); err != nil {
				return fmt.Errorf("order %s: receipt: %w", number, err)
			}
		}
		for _, step := range after {
			if _, err = s.orders.SetStatus(ctx, s.org.id, o.ID, step.status, step.actor); err != nil {
				return fmt.Errorf("order %s: %s: %w", number, step.status, err)
			}
		}
	}
	return nil
}

// StocktakeTitle is the title of the running stocktake of the seed.
const StocktakeTitle = "Seed: running stocktake of the warehouse hq"

// ensureStocktake creates the stocktake of the warehouse, found again by
// its title, and starts it through the repository (WP-287): the start
// freezes the expected assets and quantities of the branch
// (stocktake_item) and sets in_progress, the running state. Three scans of
// engineer follow: two stock assets in their bins (found) and a linked
// asset that is not in the branch (unexpected).
func (s *stockSeed) ensureStocktake(ctx context.Context) error {
	if !s.content.stocktake {
		return nil
	}
	id, err := s.lookup(ctx, `SELECT id::text FROM stocktake WHERE organization_id = $1 AND title = $2`, s.org.id, StocktakeTitle)
	if err != nil || id != "" {
		return err
	}
	st := &stocktake.Stocktake{OrganizationID: s.org.id, Title: StocktakeTitle, Description: "Seed (WP-819)",
		Status: stocktake.StatusPlanned, Scope: "partial", LocationID: s.warehouseID(), DueDate: s.day(14)}
	if err = s.stocktakes.Create(ctx, st); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err = s.stocktakes.Start(ctx, s.org.id, st.ID, s.admin); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	plan := s.assetPlan()
	stock := plan[len(plan)-s.content.stock:]
	for _, scan := range []struct {
		n        int
		location string
	}{{stock[0].n, stock[0].location}, {stock[1].n, stock[1].location}, {1, s.binID(1)}} {
		if err = s.stocktakes.AddScan(ctx, &stocktake.StockScan{OrganizationID: s.org.id, StocktakeID: st.ID,
			AssetID: s.assetIDs[scan.n], LocationID: scan.location, ScannedBy: s.engineer, ScanMethod: "barcode",
			Code: fmt.Sprintf("INV-%05d", scan.n)}); err != nil {
			return fmt.Errorf("scan of asset %d: %w", scan.n, err)
		}
	}
	return nil
}
