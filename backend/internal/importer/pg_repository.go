package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository stores the import profiles in import_profile (migration
// 000219). Every call runs in a transaction of the request tenant
// (database.WithRequestTenant), so RLS limits it to the organization.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository returns the PostgreSQL profile store.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// RunInTx runs fn in one transaction of the request tenant (K20, WP-775):
// the repositories fn calls with its context join it
// (database.WithRequestTenantTx), so an import row is written or rolled
// back as a whole and its database.AfterCommit callbacks (indexing,
// notifications) run only after its commit.
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

var _ RowTx = (*PGRepository)(nil)

// profileColumns are the columns of a Profile (scanProfile).
const profileColumns = `id::text, organization_id::text, name, description, entity, format, mode, sheet, mapping, identity_keys, created_by::text, created_at, updated_at`

// profileSortColumn sorts by bytes, as the in-memory store and the keyset
// comparison of the cursor do.
const profileSortColumn = `name COLLATE "C"`

// profileConstraints maps the unique name per organization onto its
// request field (409 on name).
var profileConstraints = map[string]string{"import_profile_organization_id_name_key": "name"}

// profileDBError turns a taken name into the conflict on name; other
// errors pass unchanged and reach api.WriteRepoError.
func profileDBError(err error) error {
	mapped := api.FieldDBError(err, profileConstraints)
	var conflict *api.ConflictError
	if errors.As(mapped, &conflict) {
		return errNameTaken
	}
	return mapped
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(s scanner) (*Profile, error) {
	p := &Profile{}
	var mapping []byte
	if err := s.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Description, &p.Entity, &p.Format, &p.Mode, &p.Sheet,
		&mapping, &p.IdentityKeys, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(mapping, &p.Mapping); err != nil {
		return nil, fmt.Errorf("decode import profile mapping: %w", err)
	}
	p.normalize()
	return p, nil
}

// List reads the profiles of the organization sorted by name and id, one
// row more than the page limit for the keyset cursor.
func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Profile, int, error) {
	items := make([]Profile, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		pos := 2
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM import_profile WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count import profiles: %w", err)
		}
		if page.Cursor != nil {
			name, id, posErr := profilePosition(page.Cursor)
			if posErr != nil {
				return posErr
			}
			where += " AND " + api.KeysetClause(profileSortColumn, "text", profileDir, pos)
			args = append(args, name, id)
			pos += 2
		}
		tail := fmt.Sprintf(" ORDER BY %s ASC, id ASC LIMIT $%d", profileSortColumn, pos)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", pos+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, "SELECT "+profileColumns+" FROM import_profile WHERE "+where+tail, args...)
		if err != nil {
			return fmt.Errorf("list import profiles: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, scanErr := scanProfile(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, *item)
		}
		return rows.Err()
	})
	return items, total, err
}

// Get reads a profile of the organization.
func (r *PGRepository) Get(ctx context.Context, orgID, id string) (*Profile, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var item *Profile
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scanProfile(tx.QueryRow(ctx, "SELECT "+profileColumns+" FROM import_profile WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return item, err
}

// Create validates and stores a profile; a taken name is 409 on name.
func (r *PGRepository) Create(ctx context.Context, p *Profile) error {
	p.normalize()
	if err := p.Validate(); err != nil {
		return err
	}
	mapping, err := json.Marshal(p.Mapping)
	if err != nil {
		return fmt.Errorf("encode import profile mapping: %w", err)
	}
	return database.WithRequestTenant(ctx, r.pool, p.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, insertErr := scanProfile(tx.QueryRow(ctx, `
			INSERT INTO import_profile (organization_id, name, description, entity, format, mode, sheet, mapping, identity_keys, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10::uuid)
			RETURNING `+profileColumns,
			p.OrganizationID, p.Name, p.Description, p.Entity, p.Format, p.Mode, p.Sheet, string(mapping), p.IdentityKeys, p.CreatedBy))
		if insertErr != nil {
			return profileDBError(insertErr)
		}
		*p = *created
		return nil
	})
}

// Update merges the sent fields into the locked row, validates the result
// and stores it; a taken name is 409 on name.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req *UpdateProfileRequest) (*Profile, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var item *Profile
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanProfile(tx.QueryRow(ctx, "SELECT "+profileColumns+" FROM import_profile WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		req.apply(current)
		if invalidErr := current.Validate(); invalidErr != nil {
			return invalidErr
		}
		mapping, err := json.Marshal(current.Mapping)
		if err != nil {
			return fmt.Errorf("encode import profile mapping: %w", err)
		}
		item, err = scanProfile(tx.QueryRow(ctx, `
			UPDATE import_profile SET name = $3, description = $4, entity = $5, format = $6, mode = $7, sheet = $8,
				mapping = $9::jsonb, identity_keys = $10
			WHERE organization_id = $1 AND id = $2
			RETURNING `+profileColumns,
			orgID, id, current.Name, current.Description, current.Entity, current.Format, current.Mode, current.Sheet,
			string(mapping), current.IdentityKeys))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return profileDBError(err)
		}
		return nil
	})
	return item, err
}

// Delete removes a profile of the organization.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM import_profile WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return fmt.Errorf("delete import profile: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
