package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	data, err := json.Marshal(e.Data)
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
