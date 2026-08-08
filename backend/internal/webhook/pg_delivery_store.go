package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGDeliveryStore implements DeliveryStore on PostgreSQL.
//
// Tenant-scoped statements run with `app.org_id` so the regular RLS policy
// applies. The dispatcher's cross-tenant claim query is the single exception:
// it sets the `app.system` flag, which no request-scoped code path ever sets.
type PGDeliveryStore struct {
	pool *pgxpool.Pool
}

// NewPGDeliveryStore creates a PostgreSQL-backed delivery store.
func NewPGDeliveryStore(pool *pgxpool.Pool) *PGDeliveryStore {
	return &PGDeliveryStore{pool: pool}
}

func (s *PGDeliveryStore) inTx(ctx context.Context, setting, value string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", setting, value); err != nil {
		return fmt.Errorf("set session context: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Enqueue stores a new pending delivery for the tenant.
func (s *PGDeliveryStore) Enqueue(ctx context.Context, rec DeliveryRecord) (DeliveryRecord, error) {
	stored := rec

	err := s.inTx(ctx, "app.org_id", rec.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO webhook_delivery (
				organization_id, subscription_id, event, payload,
				status, attempt, max_attempts, next_retry_at
			)
			VALUES (
				current_setting('app.org_id')::uuid, $1, $2, $3,
				$4, $5, $6, $7
			)
			RETURNING id::text, created_at
		`,
			rec.SubscriptionID,
			rec.Event,
			rec.Payload,
			statusOrDefault(rec.Status),
			rec.Attempt,
			maxAttemptsOrDefault(rec.MaxAttempts),
			rec.NextRetryAt,
		)
		if err := row.Scan(&stored.ID, &stored.CreatedAt); err != nil {
			return fmt.Errorf("enqueue webhook delivery: %w", err)
		}
		stored.CreatedAt = stored.CreatedAt.UTC()
		stored.Status = statusOrDefault(rec.Status)
		stored.MaxAttempts = maxAttemptsOrDefault(rec.MaxAttempts)
		return nil
	})

	return stored, err
}

// Update persists the outcome of a delivery attempt.
func (s *PGDeliveryStore) Update(ctx context.Context, rec DeliveryRecord) error {
	return s.inTx(ctx, "app.org_id", rec.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_delivery
			SET status = $2,
			    attempt = $3,
			    response_status = $4,
			    duration_ms = $5,
			    error = $6,
			    next_retry_at = $7,
			    delivered_at = $8,
			    updated_at = now()
			WHERE id = $1
		`,
			rec.ID,
			rec.Status,
			rec.Attempt,
			nullableInt(rec.ResponseStatus),
			nullableInt(rec.DurationMS),
			nullableString(rec.Error),
			rec.NextRetryAt,
			rec.DeliveredAt,
		)
		if err != nil {
			return fmt.Errorf("update webhook delivery: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("delivery not found")
		}
		return nil
	})
}

// ClaimDue leases due deliveries across all tenants for the dispatcher worker.
func (s *PGDeliveryStore) ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]DeliveryRecord, error) {
	records := make([]DeliveryRecord, 0, limit)

	err := s.inTx(ctx, "app.system", "on", func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT id
				FROM webhook_delivery
				WHERE status IN ('pending', 'retrying')
				  AND next_retry_at IS NOT NULL
				  AND next_retry_at <= $1
				ORDER BY next_retry_at
				LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			UPDATE webhook_delivery d
			SET status = 'retrying',
			    next_retry_at = $3,
			    updated_at = now()
			FROM due
			WHERE d.id = due.id
			RETURNING d.id::text, d.organization_id::text, d.subscription_id::text, d.event,
			          d.payload, d.status, d.attempt, d.max_attempts, d.created_at
		`, now, limit, now.Add(lease))
		if err != nil {
			return fmt.Errorf("claim due webhook deliveries: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var rec DeliveryRecord
			if err := rows.Scan(
				&rec.ID,
				&rec.OrganizationID,
				&rec.SubscriptionID,
				&rec.Event,
				&rec.Payload,
				&rec.Status,
				&rec.Attempt,
				&rec.MaxAttempts,
				&rec.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan webhook delivery: %w", err)
			}
			rec.CreatedAt = rec.CreatedAt.UTC()
			records = append(records, rec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	return records, nil
}

// ListBySubscription returns the delivery history of a subscription.
func (s *PGDeliveryStore) ListBySubscription(ctx context.Context, orgID, subscriptionID string, page api.PaginationParams) ([]DeliveryRecord, int, error) {
	records := make([]DeliveryRecord, 0)
	total := 0

	err := s.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM webhook_delivery WHERE subscription_id = $1", subscriptionID,
		).Scan(&total); err != nil {
			return fmt.Errorf("count webhook deliveries: %w", err)
		}

		rows, err := tx.Query(ctx, `
			SELECT id::text, organization_id::text, subscription_id::text, event, status,
			       attempt, max_attempts, response_status, duration_ms, error,
			       next_retry_at, delivered_at, created_at
			FROM webhook_delivery
			WHERE subscription_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, subscriptionID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list webhook deliveries: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				rec            DeliveryRecord
				responseStatus *int
				durationMS     *int
				deliveryError  *string
			)
			if err := rows.Scan(
				&rec.ID,
				&rec.OrganizationID,
				&rec.SubscriptionID,
				&rec.Event,
				&rec.Status,
				&rec.Attempt,
				&rec.MaxAttempts,
				&responseStatus,
				&durationMS,
				&deliveryError,
				&rec.NextRetryAt,
				&rec.DeliveredAt,
				&rec.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan webhook delivery: %w", err)
			}
			if responseStatus != nil {
				rec.ResponseStatus = *responseStatus
			}
			if durationMS != nil {
				rec.DurationMS = *durationMS
			}
			if deliveryError != nil {
				rec.Error = *deliveryError
			}
			rec.CreatedAt = rec.CreatedAt.UTC()
			records = append(records, rec)
		}
		return rows.Err()
	})

	return records, total, err
}

// MoveToDeadLetter moves a failed delivery into the dead-letter queue and
// marks the delivery record dead, atomically. Repeating the move for the same
// delivery is a no-op.
func (s *PGDeliveryStore) MoveToDeadLetter(ctx context.Context, rec DeliveryRecord) error {
	return s.inTx(ctx, "app.org_id", rec.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO webhook_dead_letter (
				delivery_id, organization_id, subscription_id, event, payload,
				attempts, last_status_code, last_error, first_attempt_at
			)
			SELECT d.id, d.organization_id, d.subscription_id, d.event, d.payload,
			       $2, $3, $4, d.created_at
			FROM webhook_delivery d
			WHERE d.id = $1
			ON CONFLICT (delivery_id) DO NOTHING
		`,
			rec.ID,
			rec.Attempt,
			nullableInt(rec.ResponseStatus),
			nullableString(rec.Error),
		); err != nil {
			return fmt.Errorf("insert webhook dead letter: %w", err)
		}

		tag, err := tx.Exec(ctx, `
			UPDATE webhook_delivery
			SET status = $2,
			    attempt = $3,
			    response_status = $4,
			    duration_ms = $5,
			    error = $6,
			    next_retry_at = NULL,
			    updated_at = now()
			WHERE id = $1
		`,
			rec.ID,
			StatusDead,
			rec.Attempt,
			nullableInt(rec.ResponseStatus),
			nullableInt(rec.DurationMS),
			nullableString(rec.Error),
		)
		if err != nil {
			return fmt.Errorf("mark webhook delivery dead: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("delivery not found")
		}
		return nil
	})
}

// ListDeadLetters returns the dead-letter queue of one tenant, newest first.
func (s *PGDeliveryStore) ListDeadLetters(ctx context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error) {
	letters := make([]DeadLetter, 0)
	total := 0

	err := s.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM webhook_dead_letter WHERE organization_id = $1", orgID,
		).Scan(&total); err != nil {
			return fmt.Errorf("count webhook dead letters: %w", err)
		}

		rows, err := tx.Query(ctx, `
			SELECT id::text, delivery_id::text, organization_id::text, subscription_id::text,
			       event, attempts, last_status_code, last_error, first_attempt_at, dead_at
			FROM webhook_dead_letter
			WHERE organization_id = $1
			ORDER BY dead_at DESC
			LIMIT $2 OFFSET $3
		`, orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list webhook dead letters: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				dl             DeadLetter
				lastStatusCode *int
				lastError      *string
				firstAttemptAt *time.Time
			)
			if err := rows.Scan(
				&dl.ID,
				&dl.DeliveryID,
				&dl.OrganizationID,
				&dl.SubscriptionID,
				&dl.Event,
				&dl.Attempts,
				&lastStatusCode,
				&lastError,
				&firstAttemptAt,
				&dl.DeadAt,
			); err != nil {
				return fmt.Errorf("scan webhook dead letter: %w", err)
			}
			if lastStatusCode != nil {
				dl.LastStatusCode = *lastStatusCode
			}
			if lastError != nil {
				dl.LastError = *lastError
			}
			if firstAttemptAt != nil {
				dl.FirstAttemptAt = firstAttemptAt.UTC()
			}
			dl.DeadAt = dl.DeadAt.UTC()
			letters = append(letters, dl)
		}
		return rows.Err()
	})

	return letters, total, err
}

func statusOrDefault(status string) string {
	if status == "" {
		return StatusPending
	}
	return status
}

func maxAttemptsOrDefault(maxAttempts int) int {
	if maxAttempts <= 0 {
		return defaultMaxAttempts
	}
	return maxAttempts
}

func nullableInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
