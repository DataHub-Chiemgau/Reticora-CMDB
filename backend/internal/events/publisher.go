package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// DefaultBudget is how long one cycle keeps publishing the batches of
	// an organization (E-226): an alarm storm drains at several batches
	// per cycle instead of one.
	DefaultBudget = 900 * time.Millisecond
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
	// Budget is how long publishOrg keeps publishing further batches of
	// one organization in a cycle (DefaultBudget).
	Budget time.Duration
	// MaxAge and MaxBytes are the retention of the stream (EVT-01,
	// RETICORA_EVENTS_MAX_AGE, RETICORA_EVENTS_MAX_BYTES); published outbox
	// rows keep their data as long as the stream keeps the events
	// (KeepPublished).
	MaxAge   time.Duration
	MaxBytes int64
	now      func() time.Time
	// batch publishes one batch of an organization (publishBatch; tests
	// replace it).
	batch batchFunc
}

// batchFunc publishes one batch of at most limit pending events of an
// organization and returns how many it delivered; full reports that the
// batch was full and delivered completely, so more events may wait.
type batchFunc func(ctx context.Context, orgID string, limit int) (published int, full bool, err error)

// KeepPublished is how long published rows keep their data in the outbox:
// as long as the stream keeps the events (MaxAge).
func (p *Publisher) KeepPublished() time.Duration { return p.MaxAge }

// WithClock replaces the clock (tests).
func (p *Publisher) WithClock(now func() time.Time) *Publisher {
	p.now = now
	return p
}

// NewPublisher creates a publisher on pool; js may be nil (no NATS: the
// consumers still receive the events).
func NewPublisher(pool *pgxpool.Pool, js jetstream.JetStream, consumers ...Consumer) *Publisher {
	p := &Publisher{pool: pool, js: js, consumers: consumers, Batch: DefaultBatch, Interval: DefaultInterval,
		Budget: DefaultBudget, MaxAge: StreamMaxAge, MaxBytes: StreamMaxBytes, now: func() time.Time { return time.Now().UTC() }}
	p.batch = p.publishBatch
	return p
}

// Setup creates or updates the stream RETICORA_EVENTS with the retention
// MaxAge and MaxBytes.
func (p *Publisher) Setup(ctx context.Context) error {
	if p.js == nil {
		return nil
	}
	_, err := p.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: StreamName, Subjects: []string{SubjectPrefix + ">"}, Storage: jetstream.FileStorage,
		MaxAge: p.MaxAge, MaxBytes: p.MaxBytes, Duplicates: 10 * time.Minute,
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

// PublishOrg delivers the pending events of one organization and returns
// how many it delivered (publishOrg).
func (p *Publisher) PublishOrg(ctx context.Context, orgID string) (int, error) {
	return p.publishOrg(ctx, orgID)
}

// publishOrg publishes batches of the organization until its outbox is
// empty, a delivery fails or the cycle's Budget is spent (E-226): one batch
// per cycle capped an organization at Batch events per Interval. Each batch
// runs in a transaction of its own under the organization's advisory lock,
// so the order per organization holds and the next batch starts after the
// commit of the previous one.
func (p *Publisher) publishOrg(ctx context.Context, orgID string) (int, error) {
	start := p.now()
	limit := p.Batch
	if limit <= 0 {
		limit = DefaultBatch
	}
	published := 0
	for {
		n, full, err := p.batch(ctx, orgID, limit)
		published += n
		if err != nil || !full {
			return published, err
		}
		if p.Budget <= 0 || p.now().Sub(start) >= p.Budget || ctx.Err() != nil {
			return published, nil
		}
	}
}

// publishBatch publishes one batch of the organization (batchFunc).
func (p *Publisher) publishBatch(ctx context.Context, orgID string, limit int) (published int, full bool, deliveryErr error) {
	scope := database.OrgWideScope(orgID, "")
	fetched := 0
	err := database.WithTenant(ctx, p.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1, audit_org_lock_key($2))`,
			publisherLockKey, orgID).Scan(&locked); err != nil || !locked {
			return err // another replica publishes this organization
		}
		rows, err := tx.Query(ctx, `SELECT id::text, type, organization_id::text, occurred_at, sequence,
			COALESCE(entity_id, ''), COALESCE(entity_seq, 0), data, origin
			FROM event_outbox WHERE organization_id = $1 AND published_at IS NULL ORDER BY sequence LIMIT $2`, orgID, limit)
		if err != nil {
			return err
		}
		pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Envelope, error) {
			var e Envelope
			var origin []byte
			scanErr := row.Scan(&e.ID, &e.Type, &e.OrgID, &e.OccurredAt, &e.Sequence, &e.EntityID, &e.EntitySeq, &e.Data, &origin)
			e.OccurredAt = e.OccurredAt.UTC()
			// The workflow run that wrote the event (WP-1090).
			if scanErr == nil && len(origin) > 0 {
				var o Origin
				if json.Unmarshal(origin, &o) == nil {
					e.Origin = &o
				}
			}
			return e, scanErr
		})
		if err != nil {
			return err
		}
		fetched = len(pending)
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
		return 0, false, err
	}
	return published, deliveryErr == nil && fetched == limit && published == limit, deliveryErr
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

// PurgeOrganization removes every message of the organization from the
// stream RETICORA_EVENTS (TLC-06, WP-1155): Stream.Purge with the subject
// filter events.<org>.>, and returns how many messages it removed. Without
// NATS or the stream it removes nothing; repeated it removes nothing more.
func (p *Publisher) PurgeOrganization(ctx context.Context, orgID string) (uint64, error) {
	if p.js == nil {
		return 0, nil
	}
	if orgID == "" || strings.ContainsAny(orgID, ".*> ") {
		return 0, fmt.Errorf("purge events: invalid organization %q", orgID)
	}
	stream, err := p.js.Stream(ctx, StreamName)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("purge events: open stream %s: %w", StreamName, err)
	}
	subject := SubjectPrefix + orgID + ".>"
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(subject))
	if err != nil {
		return 0, fmt.Errorf("purge events: read stream %s: %w", StreamName, err)
	}
	var n uint64
	for _, count := range info.State.Subjects {
		n += count
	}
	if err := stream.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return 0, fmt.Errorf("purge events of %s: %w", orgID, err)
	}
	return n, nil
}

// Cleanup removes the rows published longer than KeepPublished ago. The
// newest row of each entity stays as the anchor entity_seq continues from,
// without its data: no event data outlives the retention (NFR-06). The
// organization's sequence continues from its counter (event_outbox_counter,
// migration 000193, WP-834), so the organization's newest row may go.
func (p *Publisher) Cleanup(ctx context.Context) error {
	cutoff := p.now().Add(-p.KeepPublished())
	var orgs []string
	err := database.WithSystem(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT organization_id::text FROM event_outbox WHERE published_at < $1`, cutoff)
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
			// The newest event of each entity stays: entity_seq continues
			// from it.
			_, err := tx.Exec(ctx, `DELETE FROM event_outbox o WHERE organization_id = $1 AND published_at < $2
				AND (entity_id IS NULL OR entity_seq < (SELECT max(entity_seq) FROM event_outbox e
					WHERE e.organization_id = $1 AND e.entity_id = o.entity_id))`, org, cutoff)
			if err != nil {
				return err
			}
			// The anchors keep only their numbers.
			_, err = tx.Exec(ctx, `UPDATE event_outbox SET data = '{}'::jsonb
				WHERE organization_id = $1 AND published_at < $2 AND data <> '{}'::jsonb`, org, cutoff)
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
