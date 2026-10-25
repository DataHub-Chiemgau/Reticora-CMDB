package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UpdateRequest captures mutable webhook subscription fields.
type UpdateRequest struct {
	Name     *string  `json:"name,omitempty"`
	URL      *string  `json:"url,omitempty"`
	Events   []string `json:"events,omitempty"`
	IsActive *bool    `json:"is_active,omitempty"`
}

// PGRepository implements Repository backed by PostgreSQL with RLS.
// Subscriptions carry the scope snapshot of their creator (migration
// 000155): request paths run with the principal's complete scope
// (database.WithRequestTenant), so row level security shows them only the
// subscriptions whose snapshot lies in their scope; the dispatcher and the
// system paths run with the organization-wide scope.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed webhook repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

const webhookSelectColumns = `id, organization_id, name, url, secret, events, is_active, headers,
	COALESCE(service_account_id::text, ''), created_at, updated_at, COALESCE(previous_secret, ''), previous_secret_expires_at,
	scope_recorded, scope_clients::text[], scope_sites::text[], scope_teams::text[]`

// List returns paginated subscriptions.
func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Subscription, int, error) {
	var subs []Subscription
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Subscription, error) {
	var sub *Subscription

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

// Create stores the subscription with its scope snapshot (sub.Scope, nil:
// none); WITH CHECK of webhook_sub_isolation refuses a snapshot outside the
// principal's scope. A bound service account must be visible in the
// principal's transaction, otherwise the binding is refused
// (errServiceAccountNotFound).
func (r *PGRepository) Create(ctx context.Context, sub *Subscription) error {
	return database.WithRequestTenant(ctx, r.pool, sub.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		headersJSON, err := json.Marshal(sub.Headers)
		if err != nil {
			return fmt.Errorf("marshal headers: %w", err)
		}

		if sub.ServiceAccountID != "" {
			visible := uuidPattern.MatchString(sub.ServiceAccountID)
			if visible {
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM service_account WHERE id = $1::uuid)`,
					sub.ServiceAccountID).Scan(&visible); err != nil {
					return fmt.Errorf("read service account: %w", err)
				}
			}
			if !visible {
				return errServiceAccountNotFound
			}
		}

		snapshot := Scope{}
		if sub.Scope != nil {
			snapshot = *sub.Scope
		}
		query := `
			INSERT INTO webhook_subscription (organization_id, name, url, secret, events, is_active, headers, service_account_id,
				scope_recorded, scope_clients, scope_sites, scope_teams)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid,
				$8, $9::uuid[], $10::uuid[], $11::uuid[])
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
			sub.ServiceAccountID,
			sub.Scope != nil,
			snapshot.Clients,
			snapshot.Sites,
			snapshot.Teams,
		).Scan(&sub.ID, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("create webhook: %w", err)
		}

		sub.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		sub.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		return nil
	})
}

// Update modifies an existing subscription.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Subscription, error) {
	var sub *Subscription

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
func (r *PGRepository) GetActiveForEvent(ctx context.Context, orgID, eventType string) ([]Subscription, error) {
	var subs []Subscription

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
func (r *PGRepository) ListByEvent(ctx context.Context, orgID, event string) ([]Subscription, error) {
	return r.GetActiveForEvent(ctx, orgID, event)
}

type webhookScanner interface {
	Scan(dest ...any) error
}

func scanWebhook(scanner webhookScanner) (*Subscription, error) {
	sub := &Subscription{}
	var createdAt, updatedAt time.Time
	var headersJSON []byte
	var recorded bool
	var scope Scope

	if err := scanner.Scan(
		&sub.ID,
		&sub.OrganizationID,
		&sub.Name,
		&sub.URL,
		&sub.Secret,
		&sub.Events,
		&sub.IsActive,
		&headersJSON,
		&sub.ServiceAccountID,
		&createdAt,
		&updatedAt,
		&sub.PreviousSecret,
		&sub.PreviousSecretExpiresAt,
		&recorded,
		&scope.Clients,
		&scope.Sites,
		&scope.Teams,
	); err != nil {
		return nil, err
	}
	if recorded {
		sub.Scope = &scope
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

// RotateSecret implements SecretRotator.
func (r *PGRepository) RotateSecret(ctx context.Context, orgID, id, secret string, validUntil time.Time) (*Subscription, error) {
	var sub *Subscription
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		sub, err = scanWebhook(tx.QueryRow(ctx, `UPDATE webhook_subscription
			SET previous_secret = secret, previous_secret_expires_at = $4, secret = $3, updated_at = now()
			WHERE organization_id = $1 AND id::text = $2
			RETURNING `+webhookSelectColumns, orgID, id, secret, validUntil.UTC()))
		if err != nil {
			return fmt.Errorf("rotate webhook secret: %w", err)
		}
		return nil
	})
	return sub, err
}

// ClearExpiredSecrets removes the previous secrets whose overlap has ended
// and returns how many; the system flag only lists the organizations, the
// subscriptions are changed in each organization's transaction.
func (r *PGRepository) ClearExpiredSecrets(ctx context.Context, now time.Time) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		err = database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, execErr := tx.Exec(ctx, `UPDATE webhook_subscription SET previous_secret = NULL, previous_secret_expires_at = NULL
				WHERE organization_id = $1 AND previous_secret_expires_at <= $2`, orgID, now)
			total += int(tag.RowsAffected())
			return execErr
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Deactivate switches a subscription off (WHK-01); the dispatcher calls it
// outside a request, in the organization's own tenant context.
func (r *PGRepository) Deactivate(ctx context.Context, orgID, id string) error {
	return database.WithTenant(ctx, r.pool, orgScope(orgID), func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE webhook_subscription SET is_active = false, updated_at = now()
			WHERE id = $1 AND organization_id = $2`, id, orgID)
		if err != nil {
			return fmt.Errorf("deactivate webhook: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}
