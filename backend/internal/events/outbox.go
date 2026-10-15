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

// AppendMany writes several events of one organization in tx with one
// statement (NFR-03, WP-333), each prepared like Append. The insert trigger
// numbers them (sequence, entity_seq) in the order of es.
func (o *Outbox) AppendMany(ctx context.Context, tx pgx.Tx, es []*Event) ([]*Envelope, error) {
	envs := make([]*Envelope, len(es))
	if len(es) == 0 {
		return envs, nil
	}
	orgs, types, entities, data := make([]string, len(es)), make([]string, len(es)), make([]*string, len(es)), make([]string, len(es))
	for i, e := range es {
		if !IsType(e.Type) {
			return nil, fmt.Errorf("events: unknown event type %q", e.Type)
		}
		if e.OrgID != es[0].OrgID {
			return nil, fmt.Errorf("events: events of several organizations in one batch")
		}
		sealed, _, err := pii.SealTx(ctx, tx, e.OrgID, e.Data)
		if err != nil {
			return nil, fmt.Errorf("events: seal personal data: %w", err)
		}
		raw, err := json.Marshal(sealed)
		if err != nil {
			return nil, fmt.Errorf("events: encode data: %w", err)
		}
		if e.Data == nil {
			raw = []byte("{}")
		}
		envs[i] = &Envelope{OrgID: e.OrgID, Type: e.Type, EntityID: e.EntityID, Data: raw}
		orgs[i], types[i], data[i] = e.OrgID, e.Type, string(raw)
		if e.EntityID != "" {
			entity := e.EntityID
			entities[i] = &entity
		}
	}
	rows, err := tx.Query(ctx, `
		WITH input AS MATERIALIZED (
			SELECT gen_random_uuid() AS id, e.*
			FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) WITH ORDINALITY AS e(org, type, entity_id, data, ord)
		), ins AS (
			INSERT INTO event_outbox (id, organization_id, sequence, type, entity_id, entity_seq, data)
			SELECT id, org::uuid, 0, type, entity_id, CASE WHEN entity_id IS NULL THEN NULL ELSE 0 END, data::jsonb
			FROM input ORDER BY ord
			RETURNING id, sequence, entity_seq, occurred_at
		)
		SELECT input.ord, ins.id::text, ins.sequence, ins.entity_seq, ins.occurred_at
		FROM input JOIN ins ON ins.id = input.id ORDER BY input.ord`, orgs, types, entities, data)
	if err != nil {
		return nil, fmt.Errorf("events: append %d events: %w", len(es), err)
	}
	defer rows.Close()
	read := 0
	for rows.Next() {
		var ord int64
		var entitySeq *int64
		env := &Envelope{}
		if err := rows.Scan(&ord, &env.ID, &env.Sequence, &entitySeq, &env.OccurredAt); err != nil {
			return nil, fmt.Errorf("events: append %d events: %w", len(es), err)
		}
		i := int(ord) - 1
		if i != read {
			return nil, fmt.Errorf("events: batch numbered out of order at event %d", read)
		}
		envs[i].ID, envs[i].Sequence, envs[i].OccurredAt = env.ID, env.Sequence, env.OccurredAt.UTC()
		if entitySeq != nil {
			envs[i].EntitySeq = *entitySeq
		}
		read++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: append %d events: %w", len(es), err)
	}
	if read != len(es) {
		return nil, fmt.Errorf("events: %d of %d events written", read, len(es))
	}
	return envs, nil
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
