package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGRepository stores channels in notification_channel with the
// configuration sealed. Channels belong to the organization: every
// statement runs org-wide.
type PGRepository struct {
	pool   *pgxpool.Pool
	sealer *Sealer
}

// NewPGRepository creates the repository.
func NewPGRepository(pool *pgxpool.Pool, sealer *Sealer) *PGRepository {
	return &PGRepository{pool: pool, sealer: sealer}
}

func (r *PGRepository) inOrg(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, r.pool, &scope, fn)
}

const channelColumns = `id::text, organization_id::text, name, kind, provider, config_ciphertext, config_dek, enabled, created_at, updated_at`

func (r *PGRepository) scan(row pgx.Row) (*Channel, error) {
	var c Channel
	var ciphertext, dek []byte
	if err := row.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.Kind, &c.Provider, &ciphertext, &dek, &c.Enabled,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cfg, err := r.sealer.Open(ciphertext, dek)
	if err != nil {
		return nil, err
	}
	c.Config = cfg
	return &c, nil
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// Create implements Repository.
func (r *PGRepository) Create(ctx context.Context, ch *Channel) error {
	if err := ch.Validate(); err != nil {
		return err
	}
	ciphertext, dek, err := r.sealer.Seal(ch.Config)
	if err != nil {
		return err
	}
	return r.inOrg(ctx, ch.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO notification_channel (organization_id, name, kind, provider, config_ciphertext, config_dek, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text, created_at, updated_at`,
			ch.OrganizationID, ch.Name, ch.Kind, ch.Provider, ciphertext, dek, ch.Enabled).Scan(&ch.ID, &ch.CreatedAt, &ch.UpdatedAt)
		if err != nil {
			return fmt.Errorf("create notification channel: %w", mapWriteError(err))
		}
		return nil
	})
}

// Get implements Repository.
func (r *PGRepository) Get(ctx context.Context, orgID, id string) (*Channel, error) {
	var out *Channel
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = r.scan(tx.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channel
			WHERE organization_id = $1 AND id::text = $2`, orgID, id))
		return err
	})
	return out, err
}

// List implements Repository.
func (r *PGRepository) List(ctx context.Context, orgID string) ([]Channel, error) {
	out := []Channel{}
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+channelColumns+` FROM notification_channel
			WHERE organization_id = $1 ORDER BY name`, orgID)
		if err != nil {
			return fmt.Errorf("list notification channels: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := r.scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, err
}

// Update implements Repository: name, configuration and enabled flag.
func (r *PGRepository) Update(ctx context.Context, ch *Channel) error {
	if err := ch.Validate(); err != nil {
		return err
	}
	ciphertext, dek, err := r.sealer.Seal(ch.Config)
	if err != nil {
		return err
	}
	return r.inOrg(ctx, ch.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE notification_channel SET name = $3, kind = $4, provider = $5,
				config_ciphertext = $6, config_dek = $7, enabled = $8
			WHERE organization_id = $1 AND id::text = $2 RETURNING created_at, updated_at`,
			ch.OrganizationID, ch.ID, ch.Name, ch.Kind, ch.Provider, ciphertext, dek, ch.Enabled).Scan(&ch.CreatedAt, &ch.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("update notification channel: %w", mapWriteError(err))
		}
		return nil
	})
}

// Delete implements Repository.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM notification_channel WHERE organization_id = $1 AND id::text = $2`, orgID, id)
		if err != nil {
			return fmt.Errorf("delete notification channel: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
