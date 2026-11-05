package movement

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/jackc/pgx/v5"
)

// Service is the one writer of the ledger stock_movement (STK-04, AST-07,
// WP-274; convention 14): every stock, location, assignment, reservation
// or disposal change books its movement through it. Record runs in a
// transaction of its own or joins the transaction of the context
// (database.ContextWithTx); RecordTx books in the transaction of the
// caller. Both lock the moved object, check type, object, locations and
// references, insert the row, set the asset location or the stock level
// and append stock.movement to the outbox, in this order; an error aborts
// the calling operation. A consumable movement books its stock per bin
// through consumable.ApplyBooking in the same transaction (WP-756), which
// locks the consumable and then its stock rows in location order. With the
// in-memory repository (--no-db) Record applies the same types, rules and
// filters, without lock and event, and books a consumable on the stock of
// the consumable store (MemoryRepository.WithConsumables).
//
// The service is a Repository itself: reads and the quantity items go to
// the repository it wraps.
type Service struct {
	Repository
}

// NewService creates the movement service over repo.
func NewService(repo Repository) *Service {
	return &Service{Repository: repo}
}

// Record books m (see Service).
func (s *Service) Record(ctx context.Context, m *Movement) error {
	return s.Repository.Record(ctx, m)
}

// RecordTx books m in tx, the transaction of the calling operation; it is
// RecordTx of the package and needs PostgreSQL.
func (s *Service) RecordTx(ctx context.Context, tx pgx.Tx, m *Movement) error {
	return RecordTx(ctx, tx, m)
}

// bookingOf is the booking of the consumable movement m on the stock per
// bin.
func bookingOf(m *Movement) *consumable.Booking {
	return &consumable.Booking{OrganizationID: m.OrganizationID, ConsumableID: m.ConsumableID, MovementType: m.MovementType,
		FromLocationID: m.FromLocationID, ToLocationID: m.ToLocationID, Quantity: *m.Quantity}
}

// placeOnlyBin sets bin as the target of a receipt or the source of any
// other booking of m (POST /api/v1/consumables/{id}/movements without
// location_id).
func placeOnlyBin(m *Movement, bin string) {
	if m.MovementType == "receipt" {
		m.ToLocationID = bin
		return
	}
	m.FromLocationID = bin
}

// bookConsumable books the consumable movement m on the stock per bin in
// tx (consumable.ApplyBooking, WP-756): the rules per type, the bins
// (visible, kind bin, client of the consumable, else 422) and a shortfall
// (ErrInsufficientStock) are checked there; an invisible consumable is
// consumable.ErrNotFound. With onlyBin the bin is the only one of the
// consumable (consumable.OnlyBin).
//
// The types reservation and release (consumable.ReservationTypes, WP-284)
// change only reserved of the bin through consumable.ApplyReservation: the
// stock row is locked, a reservation beyond on_hand − reserved is
// ErrInsufficientStock, on_hand stays. Only the reservation path books
// them (POST /api/v1/stock-movements refuses them, ReservedMovementTypes).
func bookConsumable(ctx context.Context, tx pgx.Tx, m *Movement) error {
	if consumable.ReservationTypes[m.MovementType] {
		return consumable.ApplyReservation(ctx, tx, bookingOf(m))
	}
	if m.onlyBin {
		bin, err := consumable.OnlyBin(ctx, tx, m.OrganizationID, m.ConsumableID)
		if err != nil {
			return err
		}
		placeOnlyBin(m, bin)
	}
	return consumable.ApplyBooking(ctx, tx, bookingOf(m))
}
