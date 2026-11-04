package movement

import (
	"context"

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
// the calling operation. With the in-memory repository (--no-db) Record
// applies the same types, rules and filters, without lock and event.
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
