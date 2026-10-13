package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// Event is what a mutation records: its type, the entity it concerns and
// the event data.
type Event struct {
	OrgID    string
	Type     string
	EntityID string
	Data     any
}

// Outbox writes events into event_outbox.
type Outbox struct{}

// NewOutbox creates the outbox writer.
func NewOutbox() *Outbox { return &Outbox{} }

// Append writes the event in tx, the transaction of the mutation: it is
// published only if tx commits. Sequence and entity sequence are set by the
// database in commit order.
func (o *Outbox) Append(ctx context.Context, tx pgx.Tx, e *Event) (*Envelope, error) {
	if !IsType(e.Type) {
		return nil, fmt.Errorf("events: unknown event type %q", e.Type)
	}
	// Personal values travel as vault surrogates (SEC-05); the in-process
	// consumers get them resolved (Publisher).
	sealed, _, err := pii.SealTx(ctx, tx, e.OrgID, e.Data)
	if err != nil {
		return nil, fmt.Errorf("events: seal personal data: %w", err)
	}
	data, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("events: encode data: %w", err)
	}
	if e.Data == nil {
		data = []byte("{}")
	}
	env := &Envelope{OrgID: e.OrgID, Type: e.Type, EntityID: e.EntityID, Data: data}
	var entitySeq *int64
	err = tx.QueryRow(ctx, `INSERT INTO event_outbox (organization_id, sequence, type, entity_id, entity_seq, data)
		VALUES ($1, 0, $2, $3, CASE WHEN $3::text IS NULL THEN NULL ELSE 0 END, $4)
		RETURNING id::text, sequence, entity_seq, occurred_at`,
		e.OrgID, e.Type, nilIfEmpty(e.EntityID), data).Scan(&env.ID, &env.Sequence, &entitySeq, &env.OccurredAt)
	if err != nil {
		return nil, fmt.Errorf("events: append %s: %w", e.Type, err)
	}
	if entitySeq != nil {
		env.EntitySeq = *entitySeq
	}
	env.OccurredAt = env.OccurredAt.UTC()
	return env, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Emitter records an event outside a caller's transaction: a component
// whose mutation has already committed (a worker, a dead letter) reports
// what happened.
type Emitter interface {
	Emit(ctx context.Context, e *Event) error
}

// PGEmitter writes events into the outbox in a transaction of their
// organization.
type PGEmitter struct {
	pool   *pgxpool.Pool
	outbox *Outbox
}

// NewPGEmitter creates the emitter.
func NewPGEmitter(pool *pgxpool.Pool) *PGEmitter { return &PGEmitter{pool: pool, outbox: NewOutbox()} }

// Emit implements Emitter.
func (e *PGEmitter) Emit(ctx context.Context, ev *Event) error {
	scope := database.OrgWideScope(ev.OrgID, "")
	return database.WithTenant(ctx, e.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := e.outbox.Append(ctx, tx, ev)
		return err
	})
}
