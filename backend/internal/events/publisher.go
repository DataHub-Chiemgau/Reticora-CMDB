package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// Consumer receives every published event, in sequence order per
// organization (the webhook dispatcher).
type Consumer interface {
	Consume(ctx context.Context, env *Envelope) error
}

// Publisher defaults.
const (
	DefaultBatch    = 100
	DefaultInterval = time.Second
	// KeepPublished is how long published rows stay in the outbox; the
	// stream keeps the events as long (EVT-01).
	KeepPublished = StreamMaxAge
	// publisherLockKey namespaces the per-organization publisher lock.
	publisherLockKey = 1165389429
)

// Publisher delivers the outbox: per organization in sequence order, one
// replica at a time (a transaction advisory lock per organization), to the
// stream and the consumers; a delivered event is marked published in the
// same transaction. A failed delivery stops the organization's batch, so
// the order holds; the next cycle tries again (at least once).
type Publisher struct {
	pool      *pgxpool.Pool
	js        jetstream.JetStream
	consumers []Consumer
	Batch     int
	Interval  time.Duration
	now       func() time.Time
}

// WithClock replaces the clock (tests).
func (p *Publisher) WithClock(now func() time.Time) *Publisher {
	p.now = now
	return p
}

// NewPublisher creates a publisher on pool; js may be nil (no NATS: the
// consumers still receive the events).
func NewPublisher(pool *pgxpool.Pool, js jetstream.JetStream, consumers ...Consumer) *Publisher {
	return &Publisher{pool: pool, js: js, consumers: consumers, Batch: DefaultBatch, Interval: DefaultInterval,
		now: func() time.Time { return time.Now().UTC() }}
}

// Setup creates or updates the stream RETICORA_EVENTS.
func (p *Publisher) Setup(ctx context.Context) error {
	if p.js == nil {
		return nil
	}
	_, err := p.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: StreamName, Subjects: []string{SubjectPrefix + ">"}, Storage: jetstream.FileStorage,
		MaxAge: StreamMaxAge, MaxBytes: StreamMaxBytes, Duplicates: 10 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("ensure stream %s: %w", StreamName, err)
	}
	return nil
}

// Run publishes until ctx ends.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	cycle := 0
	for {
		_, err := p.PublishPending(ctx)
		failures := 0
		if err != nil {
			failures = 1
			slog.Error("event publisher", "error", err)
		}
		if cycle%3600 == 0 {
			if err := p.Cleanup(ctx); err != nil {
				slog.Warn("event outbox cleanup", "error", err)
			}
		}
		cycle++
		observability.ObserveWorkerRun("events", failures)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PublishPending delivers the pending events of every organization and
// returns how many it delivered.
func (p *Publisher) PublishPending(ctx context.Context) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT organization_id::text FROM event_outbox WHERE published_at IS NULL`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("find pending events: %w", err)
	}
	total := 0
	var firstErr error
	for _, org := range orgs {
		n, err := p.publishOrg(ctx, org)
		total += n
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("organization %s: %w", org, err)
		}
	}
	return total, firstErr
}

func (p *Publisher) publishOrg(ctx context.Context, orgID string) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	published := 0
	var deliveryErr error
	err := database.WithTenant(ctx, p.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1, audit_org_lock_key($2))`,
			publisherLockKey, orgID).Scan(&locked); err != nil || !locked {
			return err // another replica publishes this organization
		}
		rows, err := tx.Query(ctx, `SELECT id::text, type, organization_id::text, occurred_at, sequence,
			COALESCE(entity_id, ''), COALESCE(entity_seq, 0), data
			FROM event_outbox WHERE organization_id = $1 AND published_at IS NULL ORDER BY sequence LIMIT $2`, orgID, p.Batch)
		if err != nil {
			return err
		}
		pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Envelope, error) {
			var e Envelope
			scanErr := row.Scan(&e.ID, &e.Type, &e.OrgID, &e.OccurredAt, &e.Sequence, &e.EntityID, &e.EntitySeq, &e.Data)
			e.OccurredAt = e.OccurredAt.UTC()
			return e, scanErr
		})
		if err != nil {
			return err
		}
		for i := range pending {
			// The stream keeps the surrogates; the in-process consumers
			// (webhooks, notifications) need the personal values.
			revealed, rerr := revealData(ctx, tx, &pending[i])
			if rerr != nil {
				return rerr
			}
			if deliveryErr = p.deliver(ctx, &pending[i], revealed); deliveryErr != nil {
				_, err = tx.Exec(ctx, `UPDATE event_outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`,
					pending[i].ID, deliveryErr.Error())
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE event_outbox SET published_at = $2, attempts = attempts + 1, last_error = NULL WHERE id = $1`,
				pending[i].ID, p.now()); err != nil {
				return err
			}
			published++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return published, deliveryErr
}

// deliver sends one event to the stream (the event id is the message id:
// a repeated delivery is dropped as a duplicate) and to the consumers.
func (p *Publisher) deliver(ctx context.Context, env, revealed *Envelope) error {
	if p.js != nil {
		body, err := json.Marshal(env)
		if err != nil {
			return err
		}
		if _, err = p.js.Publish(ctx, env.Subject(), body, jetstream.WithMsgID(env.ID)); err != nil {
			return fmt.Errorf("publish %s: %w", env.Type, err)
		}
	}
	for _, c := range p.consumers {
		if err := c.Consume(ctx, revealed); err != nil {
			return fmt.Errorf("consume %s: %w", env.Type, err)
		}
	}
	return nil
}

// Cleanup removes the rows published longer than KeepPublished ago.
func (p *Publisher) Cleanup(ctx context.Context) error {
	var orgs []string
	err := database.WithSystem(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT organization_id::text FROM event_outbox WHERE published_at < $1`,
			p.now().Add(-KeepPublished))
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return err
	}
	for _, org := range orgs {
		scope := database.OrgWideScope(org, "")
		if err := database.WithTenant(ctx, p.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			// The newest event of the organization and of each entity stay:
			// the numbering continues from them.
			_, err := tx.Exec(ctx, `DELETE FROM event_outbox o WHERE organization_id = $1 AND published_at < $2
				AND sequence < (SELECT max(sequence) FROM event_outbox WHERE organization_id = $1)
				AND (entity_id IS NULL OR entity_seq < (SELECT max(entity_seq) FROM event_outbox e
					WHERE e.organization_id = $1 AND e.entity_id = o.entity_id))`, org, p.now().Add(-KeepPublished))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// revealData returns the envelope with the personal values resolved; env
// itself when it holds no surrogates.
func revealData(ctx context.Context, tx pgx.Tx, env *Envelope) (*Envelope, error) {
	if !bytes.Contains(env.Data, []byte(pii.RefKey)) {
		return env, nil
	}
	var data any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, fmt.Errorf("decode event %s: %w", env.ID, err)
	}
	revealed, err := pii.RevealTx(ctx, tx, env.OrgID, data)
	if err != nil {
		return nil, fmt.Errorf("resolve event %s: %w", env.ID, err)
	}
	raw, err := json.Marshal(revealed)
	if err != nil {
		return nil, err
	}
	out := *env
	out.Data = raw
	return &out, nil
}
