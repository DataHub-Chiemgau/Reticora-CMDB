package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UpdateRequest captures mutable webhook subscription fields for future sqlc-backed updates.
type UpdateRequest struct {
	Name     *string  `json:"name,omitempty"`
	URL      *string  `json:"url,omitempty"`
	Events   []string `json:"events,omitempty"`
	IsActive *bool    `json:"is_active,omitempty"`
}

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed webhook repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

const webhookSelectColumns = `id, organization_id, name, url, secret, events, is_active, headers, created_at, updated_at`

// List returns paginated subscriptions.
func (r *PGRepository) List(orgID string, page api.PaginationParams) ([]Subscription, int, error) {
	ctx := context.Background()
	var subs []Subscription
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM webhook_subscription").Scan(&total); err != nil {
			return fmt.Errorf("count webhooks: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM webhook_subscription ORDER BY name LIMIT $1 OFFSET $2",
			webhookSelectColumns,
		)
		rows, err := tx.Query(ctx, query, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list webhooks: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			sub, err := scanWebhook(rows)
			if err != nil {
				return fmt.Errorf("scan webhook: %w", err)
			}
			subs = append(subs, *sub)
		}
		return rows.Err()
	})

	if subs == nil {
		subs = []Subscription{}
	}
	return subs, total, err
}

func (r *PGRepository) GetByID(orgID, id string) (*Subscription, error) {
	ctx := context.Background()
	var sub *Subscription

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM webhook_subscription WHERE id = $1", webhookSelectColumns)
		var err error
		sub, err = scanWebhook(tx.QueryRow(ctx, query, id))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get webhook: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return sub, nil
}

func (r *PGRepository) Create(sub *Subscription) error {
	ctx := context.Background()
	return r.withTenant(ctx, sub.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		headersJSON, err := json.Marshal(sub.Headers)
		if err != nil {
			return fmt.Errorf("marshal headers: %w", err)
		}

		query := `
			INSERT INTO webhook_subscription (organization_id, name, url, secret, events, is_active, headers)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6)
			RETURNING id::text, created_at, updated_at
		`
		var createdAt, updatedAt time.Time
		if err := tx.QueryRow(ctx, query,
			sub.Name,
			sub.URL,
			sub.Secret,
			sub.Events,
			sub.IsActive,
			headersJSON,
		).Scan(&sub.ID, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("create webhook: %w", err)
		}

		sub.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		sub.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		return nil
	})
}

// Update modifies an existing subscription.
func (r *PGRepository) Update(orgID, id string, req UpdateRequest) (*Subscription, error) {
	ctx := context.Background()
	var sub *Subscription

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 4)
		args := []any{id}
		argPos := 2

		if req.Name != nil {
			setClauses = append(setClauses, fmt.Sprintf("name = $%d", argPos))
			args = append(args, *req.Name)
			argPos++
		}
		if req.URL != nil {
			setClauses = append(setClauses, fmt.Sprintf("url = $%d", argPos))
			args = append(args, *req.URL)
			argPos++
		}
		if req.Events != nil {
			setClauses = append(setClauses, fmt.Sprintf("events = $%d", argPos))
			args = append(args, req.Events)
			argPos++
		}
		if req.IsActive != nil {
			setClauses = append(setClauses, fmt.Sprintf("is_active = $%d", argPos))
			args = append(args, *req.IsActive)
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM webhook_subscription WHERE id = $1", webhookSelectColumns)
			var err error
			sub, err = scanWebhook(tx.QueryRow(ctx, query, id))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return fmt.Errorf("get webhook for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = now()")
		query := fmt.Sprintf(
			"UPDATE webhook_subscription SET %s WHERE id = $1 RETURNING %s",
			strings.Join(setClauses, ", "),
			webhookSelectColumns,
		)

		var err error
		sub, err = scanWebhook(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update webhook: %w", err)
		}
		return nil
	})

	return sub, err
}

func (r *PGRepository) Delete(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM webhook_subscription WHERE id = $1", id)
		if err != nil {
			return fmt.Errorf("delete webhook: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// GetActiveForEvent returns active subscriptions that match the event type.
func (r *PGRepository) GetActiveForEvent(orgID, eventType string) ([]Subscription, error) {
	ctx := context.Background()
	var subs []Subscription

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf(
			"SELECT %s FROM webhook_subscription WHERE is_active = true AND $1 = ANY(events)",
			webhookSelectColumns,
		)
		rows, err := tx.Query(ctx, query, eventType)
		if err != nil {
			return fmt.Errorf("list active webhooks for event: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			sub, err := scanWebhook(rows)
			if err != nil {
				return fmt.Errorf("scan webhook: %w", err)
			}
			subs = append(subs, *sub)
		}
		return rows.Err()
	})

	if subs == nil {
		subs = []Subscription{}
	}
	return subs, err
}

// ListByEvent satisfies the current webhook.Repository interface.
func (r *PGRepository) ListByEvent(orgID, event string) ([]Subscription, error) {
	return r.GetActiveForEvent(orgID, event)
}

type webhookScanner interface {
	Scan(dest ...any) error
}

func scanWebhook(scanner webhookScanner) (*Subscription, error) {
	sub := &Subscription{}
	var createdAt, updatedAt time.Time
	var headersJSON []byte

	if err := scanner.Scan(
		&sub.ID,
		&sub.OrganizationID,
		&sub.Name,
		&sub.URL,
		&sub.Secret,
		&sub.Events,
		&sub.IsActive,
		&headersJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}

	sub.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	sub.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)

	if len(headersJSON) > 0 && string(headersJSON) != "null" {
		if err := json.Unmarshal(headersJSON, &sub.Headers); err != nil {
			sub.Headers = make(map[string]string)
		}
	}

	return sub, nil
}
