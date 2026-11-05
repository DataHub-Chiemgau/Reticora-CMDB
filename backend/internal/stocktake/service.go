package stocktake

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
)

// Booker books a movement of the ledger stock_movement: movement.Service
// (WP-274), the one writer of stock_movement (convention 14). Record joins
// the transaction of the context (database.ContextWithTx), sets the
// location of a moved asset, books a consumable on its stock per bin
// (consumable.ApplyBooking) and appends stock.movement to the outbox.
type Booker interface {
	Record(ctx context.Context, m *movement.Movement) error
}

// Service completes stocktakes with their stock corrections (INV-01,
// WP-761). It needs PostgreSQL: the in-memory repository of the profile
// --no-db freezes no expected stock, and the handler answers 501 without a
// service.
type Service struct {
	repo      *PGRepository
	movements Booker
}

// NewService creates the completion service over repo and the movement
// service movements. It returns nil when repo is no *PGRepository (profile
// --no-db) or movements is nil; Handler.WithService ignores a nil service.
func NewService(repo Repository, movements Booker) *Service {
	pg, ok := repo.(*PGRepository)
	if !ok || movements == nil {
		return nil
	}
	return &Service{repo: pg, movements: movements}
}

// stocktakeReason is the reason of every movement the completion of the
// stocktake id books.
func stocktakeReason(id string) string {
	return "stocktake:" + id
}

// Complete completes the in-progress stocktake id with snapshot in one
// transaction (database.WithRequestTenantTx, WP-332), which the repository
// and the movement service join through the context. In this order: the
// stocktake is locked FOR UPDATE and must be in progress with snapshot
// (ErrAlreadyCompleted, ErrInvalidTransition, ErrNoSnapshot); the result
// per item is fixed (WP-287) and the corrections are derived from it; the
// totals and the status are set in one UPDATE; last, the corrections are
// booked through the movement service, whose outbox events stock.movement
// are the last writes of the transaction (K20). Every movement carries
// reason stocktake:<id> and the actor. An error of any step, a refused
// booking (consumable.ErrInsufficientStock) included, rolls the whole
// completion back: no movement, no result, the status stays in_progress.
func (s *Service) Complete(ctx context.Context, orgID, id, actorID string) (*Completion, error) {
	var completion *Completion
	err := s.repo.RunInTx(ctx, orgID, func(ctx context.Context) error {
		current, planned, err := s.repo.fixResult(ctx, orgID, id)
		if err != nil {
			return err
		}
		st, err := s.repo.closeCompleted(ctx, orgID, current)
		if err != nil {
			return err
		}
		corrections := make([]Correction, 0, len(planned))
		for i := range planned {
			m := planned[i].movement(orgID, actorID, stocktakeReason(id))
			if bookErr := s.movements.Record(ctx, m); bookErr != nil {
				return fmt.Errorf("book the correction of the stocktake: %w", bookErr)
			}
			corrections = append(corrections, correctionOf(m))
		}
		completion = &Completion{Stocktake: *st, CorrectionsApplied: len(corrections), Corrections: corrections}
		return nil
	})
	if err != nil {
		return nil, immutableError(err)
	}
	return completion, nil
}

// plannedCorrection is a correction derived from the fixed result before
// it is booked: a transfer of an asset or a correction of a consumable in
// a bin.
type plannedCorrection struct {
	itemKind, assetID, consumableID string
	from, to                        string
	quantity                        float64
}

// movement returns the movement that books c.
func (c *plannedCorrection) movement(orgID, actorID, reason string) *movement.Movement {
	m := &movement.Movement{OrganizationID: orgID, ItemKind: c.itemKind, FromLocationID: c.from, ToLocationID: c.to,
		ActorID: actorID, Reason: reason}
	if c.itemKind == ItemKindAsset {
		m.AssetID, m.MovementType = c.assetID, "transfer"
		return m
	}
	quantity := c.quantity
	m.ConsumableID, m.MovementType, m.Quantity = c.consumableID, "correction", &quantity
	return m
}

// correctionOf describes the booked movement m.
func correctionOf(m *movement.Movement) Correction {
	return Correction{MovementID: m.ID, MovementType: m.MovementType, ItemKind: m.ItemKind, AssetID: m.AssetID,
		ConsumableID: m.ConsumableID, FromLocationID: m.FromLocationID, ToLocationID: m.ToLocationID, Quantity: m.Quantity}
}
