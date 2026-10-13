package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

func inOrg(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, pool, &scope, fn)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// PGDeliveryStore keeps deliveries in notification_delivery.
type PGDeliveryStore struct{ pool *pgxpool.Pool }

// NewPGDeliveryStore creates the store.
func NewPGDeliveryStore(pool *pgxpool.Pool) *PGDeliveryStore { return &PGDeliveryStore{pool: pool} }

const deliveryColumns = `id::text, organization_id::text, channel_id::text, COALESCE(recipient_user_id::text, ''),
	COALESCE(recipient_email, ''), COALESCE(recipient_name, ''), recipient_key, recipients, category, critical, subject, body,
	data, COALESCE(dedup_key, ''), status, attempts, max_attempts, next_attempt_at, COALESCE(last_error, ''),
	COALESCE(provider_id, ''), sent_at`

func scanDelivery(row pgx.Row) (QueuedDelivery, error) {
	var d QueuedDelivery
	var recipients, data []byte
	err := row.Scan(&d.ID, &d.OrganizationID, &d.ChannelID, &d.Recipient.UserID, &d.Recipient.Email, &d.Recipient.Name,
		&d.RecipientKey, &recipients, &d.Category, &d.Critical, &d.Subject, &d.Body, &data, &d.DedupKey, &d.Status,
		&d.Attempts, &d.MaxAttempts, &d.NextAttemptAt, &d.LastError, &d.ProviderID, &d.SentAt)
	if err != nil {
		return d, err
	}
	if len(recipients) > 0 {
		if jsonErr := json.Unmarshal(recipients, &d.Recipients); jsonErr != nil {
			return d, jsonErr
		}
	}
	if len(data) > 0 {
		if jsonErr := json.Unmarshal(data, &d.Data); jsonErr != nil {
			return d, jsonErr
		}
	}
	d.NextAttemptAt = d.NextAttemptAt.UTC()
	return d, nil
}

// Enqueue implements DeliveryStore.
func (s *PGDeliveryStore) Enqueue(ctx context.Context, orgID string, rows []QueuedDelivery) (int, error) {
	created := 0
	err := inOrg(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		for i := range rows {
			r := &rows[i]
			recipients, err := json.Marshal(r.Recipients)
			if err != nil {
				return err
			}
			if r.Recipients == nil {
				recipients = []byte("[]")
			}
			data, err := json.Marshal(r.Data)
			if err != nil {
				return err
			}
			if r.Data == nil {
				data = []byte("{}")
			}
			tag, err := tx.Exec(ctx, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_user_id,
					recipient_email, recipient_name, recipient_key, recipients, category, critical, subject, body, data,
					dedup_key, max_attempts, next_attempt_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
				ON CONFLICT (organization_id, channel_id, recipient_key, dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`,
				orgID, r.ChannelID, nullable(r.Recipient.UserID), nullable(r.Recipient.Email), nullable(r.Recipient.Name),
				r.RecipientKey, recipients, r.Category, r.Critical, r.Subject, r.Body, data, nullable(r.DedupKey),
				r.MaxAttempts, r.NextAttemptAt)
			if err != nil {
				return fmt.Errorf("queue notification: %w", err)
			}
			created += int(tag.RowsAffected())
		}
		return nil
	})
	return created, err
}

// ClaimDue implements DeliveryStore: the system flag only finds the
// organizations with due deliveries; each organization's are claimed in
// its own transaction (FOR UPDATE SKIP LOCKED: replicas share the work).
func (s *PGDeliveryStore) ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]QueuedDelivery, error) {
	var orgs []string
	err := database.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT organization_id::text FROM notification_delivery
			WHERE (status = 'pending' AND next_attempt_at <= $1) OR (status = 'sending' AND updated_at < $2)`, now, now.Add(-lease))
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("find due notifications: %w", err)
	}
	var out []QueuedDelivery
	for _, orgID := range orgs {
		if len(out) >= limit {
			break
		}
		err = inOrg(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			rows, qErr := tx.Query(ctx, `UPDATE notification_delivery SET status = 'sending', attempts = attempts + 1
				WHERE id IN (SELECT id FROM notification_delivery
					WHERE organization_id = $1 AND ((status = 'pending' AND next_attempt_at <= $2)
						OR (status = 'sending' AND updated_at < $3))
					ORDER BY next_attempt_at LIMIT $4 FOR UPDATE SKIP LOCKED)
				RETURNING `+deliveryColumns, orgID, now, now.Add(-lease), limit-len(out))
			if qErr != nil {
				return qErr
			}
			claimed, cErr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (QueuedDelivery, error) { return scanDelivery(row) })
			out = append(out, claimed...)
			return cErr
		})
		if err != nil {
			return out, fmt.Errorf("claim notifications: %w", err)
		}
	}
	return out, nil
}

// Finish implements DeliveryStore.
func (s *PGDeliveryStore) Finish(ctx context.Context, d *QueuedDelivery) error {
	return inOrg(ctx, s.pool, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE notification_delivery SET status = $2, attempts = $3, next_attempt_at = $4,
				last_error = $5, provider_id = $6, sent_at = $7
			WHERE id = $1`, d.ID, d.Status, d.Attempts, d.NextAttemptAt, nullable(d.LastError), nullable(d.ProviderID), d.SentAt)
		if err != nil {
			return fmt.Errorf("record notification delivery: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// List implements DeliveryStore.
func (s *PGDeliveryStore) List(ctx context.Context, orgID string, limit int) ([]QueuedDelivery, error) {
	var out []QueuedDelivery
	err := inOrg(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+deliveryColumns+` FROM notification_delivery WHERE organization_id = $1
			ORDER BY created_at DESC, id LIMIT $2`, orgID, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (QueuedDelivery, error) { return scanDelivery(row) })
		return err
	})
	return out, err
}

// PGInbox keeps in-app notifications in notification_inbox.
type PGInbox struct{ pool *pgxpool.Pool }

// NewPGInbox creates the inbox.
func NewPGInbox(pool *pgxpool.Pool) *PGInbox { return &PGInbox{pool: pool} }

// Add implements Inbox.
func (b *PGInbox) Add(ctx context.Context, items []InAppNotification) error {
	byOrg := map[string][]int{}
	for i := range items {
		byOrg[items[i].OrganizationID] = append(byOrg[items[i].OrganizationID], i)
	}
	for orgID, idx := range byOrg {
		err := inOrg(ctx, b.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			for _, i := range idx {
				n := &items[i]
				data, err := json.Marshal(n.Data)
				if err != nil {
					return err
				}
				if n.Data == nil {
					data = []byte("{}")
				}
				if err = tx.QueryRow(ctx, `INSERT INTO notification_inbox (organization_id, user_id, category, subject, body, data, created_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
					orgID, n.UserID, n.Category, n.Subject, n.Body, data, n.CreatedAt).Scan(&n.ID); err != nil {
					return fmt.Errorf("add in-app notification: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// List implements InboxStore.
func (b *PGInbox) List(ctx context.Context, orgID, userID string, unreadOnly bool, limit, offset int) ([]InAppNotification, int, error) {
	out := []InAppNotification{}
	total := 0
	err := inOrg(ctx, b.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := `organization_id = $1 AND user_id::text = $2`
		if unreadOnly {
			where += ` AND read_at IS NULL`
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notification_inbox WHERE `+where, orgID, userID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, user_id::text, category, subject, body, data,
				created_at, read_at
			FROM notification_inbox WHERE `+where+` ORDER BY created_at DESC, id LIMIT $3 OFFSET $4`, orgID, userID, limit, offset)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (InAppNotification, error) {
			var n InAppNotification
			var data []byte
			scanErr := row.Scan(&n.ID, &n.OrganizationID, &n.UserID, &n.Category, &n.Subject, &n.Body, &data, &n.CreatedAt, &n.ReadAt)
			if scanErr == nil && len(data) > 0 {
				scanErr = json.Unmarshal(data, &n.Data)
			}
			return n, scanErr
		})
		return err
	})
	return out, total, err
}

// MarkRead implements InboxStore.
func (b *PGInbox) MarkRead(ctx context.Context, orgID, userID, id string) error {
	return inOrg(ctx, b.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE notification_inbox SET read_at = COALESCE(read_at, now())
			WHERE organization_id = $1 AND user_id::text = $2 AND id::text = $3`, orgID, userID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// MarkAllRead implements InboxStore.
func (b *PGInbox) MarkAllRead(ctx context.Context, orgID, userID string) (int, error) {
	n := 0
	err := inOrg(ctx, b.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE notification_inbox SET read_at = now()
			WHERE organization_id = $1 AND user_id::text = $2 AND read_at IS NULL`, orgID, userID)
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}

// PGPreferences keeps preferences in notification_preference.
type PGPreferences struct{ pool *pgxpool.Pool }

// NewPGPreferences creates the store.
func NewPGPreferences(pool *pgxpool.Pool) *PGPreferences { return &PGPreferences{pool: pool} }

// Preference implements PreferenceStore.
func (s *PGPreferences) Preference(ctx context.Context, orgID, userID string) (*Preference, error) {
	var out *Preference
	err := inOrg(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var p Preference
		var start, end *string
		err := tx.QueryRow(ctx, `SELECT to_char(quiet_start, 'HH24:MI'), to_char(quiet_end, 'HH24:MI'), time_zone, muted_categories
			FROM notification_preference WHERE organization_id = $1 AND user_id::text = $2`, orgID, userID).
			Scan(&start, &end, &p.TimeZone, &p.MutedCategories)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if start != nil {
			p.QuietStart, p.QuietEnd = *start, *end
		}
		out = &p
		return nil
	})
	return out, err
}

// SetPreference implements PreferenceStore.
func (s *PGPreferences) SetPreference(ctx context.Context, orgID, userID string, p *Preference) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return inOrg(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO notification_preference (organization_id, user_id, quiet_start, quiet_end, time_zone, muted_categories)
			VALUES ($1, $2, $3::time, $4::time, $5, $6)
			ON CONFLICT (organization_id, user_id) DO UPDATE SET quiet_start = EXCLUDED.quiet_start, quiet_end = EXCLUDED.quiet_end,
				time_zone = EXCLUDED.time_zone, muted_categories = EXCLUDED.muted_categories`,
			orgID, userID, nullable(p.QuietStart), nullable(p.QuietEnd), p.TimeZone, p.MutedCategories)
		if err != nil {
			return fmt.Errorf("store notification preference: %w", err)
		}
		return nil
	})
}
