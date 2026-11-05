package order

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The 422 messages of the goods receipt (WP-760). Like msgTargetBin, an
// unknown bin, one outside the scope and one of another client than the
// order share one answer.
const (
	msgReceiveItem     = "item_id does not name a position of the order"
	msgReceiveNoBin    = "location_id is required when the order has no target_location_id"
	msgReceiveBin      = "location_id does not name a location visible for the order's client"
	msgReceiveOver     = "quantity exceeds the open quantity of the position (quantity - received_quantity)"
	msgReceiveAssets   = "an asset position names as many asset_ids as the received quantity"
	msgReceiveNoAssets = "asset_ids are only allowed for an asset position"
	msgReceiveAsset    = "asset_ids names an asset that is not visible for the order's client"
)

// Booker books a movement of the ledger stock_movement in the transaction of
// the calling operation: movement.Service (WP-274). A consumable receipt
// books the stock of its bin through consumable.ApplyBooking in the same
// transaction (WP-756); the order code writes neither stock_movement nor
// stock_level itself.
type Booker interface {
	RecordTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error
}

// WithBookings sets the booking service of the goods receipt (WP-760);
// without one POST /orders/{id}/receive answers 503.
func (h *Handler) WithBookings(b Booker) *Handler {
	h.booker = b
	return h
}

// Receive handles POST /api/v1/orders/{id}/receive (STK-05, WP-760): the
// goods receipt of an order in status ordered or partially_received, in
// one transaction (Repository.RunInTx). It answers the order with the
// received quantity of each position.
func (h *Handler) Receive(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.booker == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the goods receipt is not available")
		return
	}
	var req ReceiveOrderRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, err)
		return
	}
	rc := &Receipt{OrganizationID: t.OrganizationID, OrderID: chi.URLParam(r, "id"), ActorID: t.UserID, Items: req.Items}
	var o *Order
	err := h.repo.RunInTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		var recErr error
		o, recErr = h.repo.Receive(ctx, rc, h.booker)
		return recErr
	})
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, o)
}

// RunInTx implements Repository: fn runs in one transaction of the
// context's tenant scope, which every repository call with fn's context
// joins (database.WithRequestTenantTx, WP-332).
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

// Receive books the goods receipt rc (STK-05, WP-760) in the transaction of
// the context (RunInTx) or one of its own, in this order: the order is
// locked (FOR UPDATE) and must be ordered or partially_received
// (ErrNotReceivable); per position, the position must belong to the order,
// the bin (location_id, else target_location_id of the order) must be a bin
// in the scope and of the order's client (checkBin), an asset position must
// name as many visible assets of a fitting client as it receives; then
// received_quantity grows by quantity, unless that exceeds quantity
// (overdelivery, 422), and book records the receipt: one consumable
// movement into the bin (whose stock_level the booking service carries
// forward), or one asset movement per asset. Last the order moves to
// received when every position is fully received, else to
// partially_received. The first error aborts the receipt; the caller's
// transaction rolls everything back.
func (r *PGRepository) Receive(ctx context.Context, rc *Receipt, book Booker) (*Order, error) {
	var o *Order
	err := database.WithRequestTenant(ctx, r.pool, rc.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockOrder(ctx, tx, rc.OrganizationID, rc.OrderID)
		if err != nil {
			return err
		}
		if current.Status != StatusOrdered && current.Status != StatusPartiallyReceived {
			return ErrNotReceivable
		}
		items, err := r.listItemsTx(ctx, tx, rc.OrganizationID, current.ID)
		if err != nil {
			return err
		}
		byID := make(map[string]*Item, len(items))
		for i := range items {
			byID[strings.ToLower(items[i].ID)] = &items[i]
		}
		for i := range rc.Items {
			if lineErr := r.receiveLine(ctx, tx, current, byID, rc, i, book); lineErr != nil {
				return lineErr
			}
		}
		var open bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM internal_order_item
				WHERE organization_id = $1 AND order_id = $2 AND received_quantity < quantity)`,
			rc.OrganizationID, current.ID).Scan(&open); err != nil {
			return fmt.Errorf("read open positions: %w", err)
		}
		next := StatusReceived
		if open {
			next = StatusPartiallyReceived
		}
		if next != current.Status && !CanReceive(current.Status, next) {
			return ErrNotReceivable
		}
		o, err = scanOrder(tx.QueryRow(ctx, `UPDATE internal_order SET status = $3, updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND status = $4 RETURNING `+orderCols,
			rc.OrganizationID, current.ID, next, current.Status))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotReceivable
		}
		if err != nil {
			return fmt.Errorf("set order status: %w", err)
		}
		o.Items, err = r.listItemsTx(ctx, tx, rc.OrganizationID, current.ID)
		return err
	})
	return o, err
}

// receiveLine checks and books position i of rc (see Receive).
func (r *PGRepository) receiveLine(ctx context.Context, tx pgx.Tx, o *Order, byID map[string]*Item, rc *Receipt, i int, book Booker) error {
	line := &rc.Items[i]
	it, ok := byID[strings.ToLower(line.ItemID)]
	if !ok {
		return fieldError(itemField(i, "item_id"), msgReceiveItem)
	}
	bin := line.LocationID
	if bin == "" {
		bin = o.TargetLocationID
	}
	if bin == "" {
		return fieldError(itemField(i, "location_id"), msgReceiveNoBin)
	}
	if err := checkBin(ctx, tx, rc.OrganizationID, o.ClientID, bin, itemField(i, "location_id"), msgReceiveBin); err != nil {
		return err
	}
	isAsset := it.ConsumableID == ""
	if isAsset {
		if line.Quantity != math.Trunc(line.Quantity) || float64(len(line.AssetIDs)) != line.Quantity {
			return fieldError(itemField(i, "asset_ids"), msgReceiveAssets)
		}
		for _, id := range line.AssetIDs {
			if err := checkReceivedAsset(ctx, tx, rc.OrganizationID, o.ClientID, id, itemField(i, "asset_ids")); err != nil {
				return err
			}
		}
	} else if len(line.AssetIDs) > 0 {
		return fieldError(itemField(i, "asset_ids"), msgReceiveNoAssets)
	}
	// The overdelivery check and the increment in one statement: the
	// order lock serializes the receipts of the order.
	var received float64
	err := tx.QueryRow(ctx, `UPDATE internal_order_item SET received_quantity = received_quantity + $3
		WHERE organization_id = $1 AND id = $2 AND received_quantity + $3 <= quantity
		RETURNING received_quantity`, rc.OrganizationID, it.ID, line.Quantity).Scan(&received)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldError(itemField(i, "quantity"), msgReceiveOver)
	}
	if err != nil {
		return fmt.Errorf("update received quantity: %w", err)
	}
	it.ReceivedQuantity = received
	base := movement.Movement{OrganizationID: rc.OrganizationID, MovementType: "receipt", ToLocationID: bin,
		ActorID: rc.ActorID, OrderID: o.ID}
	if !isAsset {
		m := base
		quantity := line.Quantity
		m.ItemKind, m.ConsumableID, m.Quantity = "consumable", it.ConsumableID, &quantity
		return bookingError(i, book.RecordTx(ctx, tx, &m))
	}
	for _, id := range line.AssetIDs {
		m := base
		m.ItemKind, m.AssetID = "asset", id
		if err = book.RecordTx(ctx, tx, &m); err != nil {
			return bookingError(i, err)
		}
	}
	return nil
}

// checkReceivedAsset verifies an asset of an asset position: visible under
// the scope and org-wide or of the order's client; for an order without a
// client every visible asset fits, like its bins (WP-847). The booking
// service checks the asset against the bin.
func checkReceivedAsset(ctx context.Context, tx pgx.Tx, orgID, orderClient, assetID, field string) error {
	if !api.IsUUID(assetID) {
		return fieldError(field, msgReceiveAsset)
	}
	var client *string
	err := tx.QueryRow(ctx, `SELECT client_id::text FROM asset WHERE organization_id = $1 AND id = $2`, orgID, assetID).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldError(field, msgReceiveAsset)
	}
	if err != nil {
		return fmt.Errorf("read asset: %w", err)
	}
	if orderClient != "" && !clientFits(client, orderClient) {
		return fieldError(field, msgReceiveAsset)
	}
	return nil
}

// bookingError places an error of the booking service on position i: a
// field error of the movement (to_location_id is the bin) moves to the
// position's field, an invisible consumable or asset is a 422 on the
// position. Any other error stays as it is.
func bookingError(i int, err error) error {
	if err == nil {
		return nil
	}
	var fe *api.FieldError
	if errors.As(err, &fe) {
		out := *fe
		switch fe.Field {
		case "to_location_id":
			out.Field = itemField(i, "location_id")
		default:
			out.Field = itemField(i, fe.Field)
		}
		return &out
	}
	switch {
	case errors.Is(err, consumable.ErrNotFound):
		return fieldError(itemField(i, "item_id"), "the consumable of the position is not visible")
	case errors.Is(err, movement.ErrAssetNotFound):
		return fieldError(itemField(i, "asset_ids"), msgReceiveAsset)
	}
	return err
}
