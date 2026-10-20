package ipam

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// VRF is a routing domain of the organization (TEN-10, CH10). Every
// organization has exactly one default VRF "global"; subnets are unique per
// VRF. A VRF is org-wide: every principal of the organization reads it, only
// principals without client or site restriction create, change or delete it
// (E-09, restrictive policies of migration 000148).
type VRF struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	IsDefault      bool      `json:"is_default"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateVRFRequest is the payload for creating a VRF.
type CreateVRFRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UpdateVRFRequest is the payload for updating a VRF.
type UpdateVRFRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// --- PostgreSQL ---

const vrfCols = `id::text, organization_id::text, name, COALESCE(description,''), is_default, created_at, updated_at`

func scanVRF(s scanner) (*VRF, error) {
	v := &VRF{}
	if err := s.Scan(&v.ID, &v.OrganizationID, &v.Name, &v.Description, &v.IsDefault, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, nil
}

// ListVRFs lists the VRFs of the organization, the default first.
func (r *PGRepository) ListVRFs(ctx context.Context, orgID string, page api.PaginationParams) ([]VRF, int, error) {
	var out []VRF
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM vrf WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+vrfCols+" FROM vrf WHERE organization_id = $1 ORDER BY is_default DESC, name LIMIT $2 OFFSET $3",
			orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVRF(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetVRF returns a VRF.
func (r *PGRepository) GetVRF(ctx context.Context, orgID, id string) (*VRF, error) {
	var v *VRF
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = scanVRF(tx.QueryRow(ctx, "SELECT "+vrfCols+" FROM vrf WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return v, err
}

// CreateVRF stores a VRF; only the database creates the default VRF. For a
// principal restricted in the client or site scope the insert fails with the
// row level security error 42501 (403 at the API).
func (r *PGRepository) CreateVRF(ctx context.Context, v *VRF) error {
	return database.WithRequestTenant(ctx, r.pool, v.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := scanVRF(tx.QueryRow(ctx, `INSERT INTO vrf (organization_id, name, description)
			VALUES ($1, $2, $3) RETURNING `+vrfCols, v.OrganizationID, v.Name, nilIfEmpty(v.Description)))
		if err != nil {
			return err
		}
		*v = *created
		return nil
	})
}

// UpdateVRF renames a VRF or changes its description. A principal restricted
// in the client or site scope updates no row: ErrNotFound.
func (r *PGRepository) UpdateVRF(ctx context.Context, orgID, id string, req UpdateVRFRequest) (*VRF, error) {
	var v *VRF
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = scanVRF(tx.QueryRow(ctx, `UPDATE vrf SET name = COALESCE($3, name),
			description = CASE WHEN $4::text IS NULL THEN description ELSE NULLIF($4, '') END
			WHERE organization_id = $1 AND id = $2 RETURNING `+vrfCols, orgID, id, req.Name, req.Description))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return v, err
}

// DeleteVRF removes a VRF; the default VRF and VRFs with subnets stay. A
// principal restricted in the client or site scope deletes no row:
// ErrNotFound.
func (r *PGRepository) DeleteVRF(ctx context.Context, orgID, id string) error {
	return r.deleteByID(ctx, orgID, "vrf", "true", id)
}

// --- Memory ---

func (r *MemoryRepository) defaultVRF(orgID string) *VRF {
	for _, v := range r.vrfs {
		if v.OrganizationID == orgID && v.IsDefault {
			return v
		}
	}
	r.seq++
	now := time.Now().UTC()
	v := &VRF{ID: fmt.Sprintf("vrf-%d", r.seq), OrganizationID: orgID, Name: "global", Description: "Default VRF",
		IsDefault: true, CreatedAt: now, UpdatedAt: now}
	r.vrfs[v.ID] = v
	return v
}

func (r *MemoryRepository) vrfNameTaken(orgID, name, exceptID string) bool {
	for _, v := range r.vrfs {
		if v.ID != exceptID && v.OrganizationID == orgID && v.Name == name {
			return true
		}
	}
	return false
}

// ListVRFs implements Repository.
func (r *MemoryRepository) ListVRFs(_ context.Context, orgID string, page api.PaginationParams) ([]VRF, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultVRF(orgID)
	var out []VRF
	for _, v := range r.vrfs {
		if v.OrganizationID == orgID {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return out[i].Name < out[j].Name
	})
	items, total := pageSlice(out, page)
	return items, total, nil
}

// GetVRF implements Repository.
func (r *MemoryRepository) GetVRF(_ context.Context, orgID, id string) (*VRF, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.vrfs[id]
	if !ok || v.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *v
	return &cp, nil
}

// CreateVRF implements Repository.
func (r *MemoryRepository) CreateVRF(_ context.Context, v *VRF) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultVRF(v.OrganizationID)
	if r.vrfNameTaken(v.OrganizationID, v.Name, "") {
		return fmt.Errorf("%w: vrf %q already exists", ErrConflict, v.Name)
	}
	r.seq++
	now := time.Now().UTC()
	v.ID, v.IsDefault, v.CreatedAt, v.UpdatedAt = fmt.Sprintf("vrf-%d", r.seq), false, now, now
	stored := *v
	r.vrfs[v.ID] = &stored
	return nil
}

// UpdateVRF implements Repository.
func (r *MemoryRepository) UpdateVRF(_ context.Context, orgID, id string, req UpdateVRFRequest) (*VRF, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.vrfs[id]
	if !ok || v.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.Name != nil {
		if r.vrfNameTaken(orgID, *req.Name, id) {
			return nil, fmt.Errorf("%w: vrf %q already exists", ErrConflict, *req.Name)
		}
		v.Name = *req.Name
	}
	if req.Description != nil {
		v.Description = *req.Description
	}
	v.UpdatedAt = time.Now().UTC()
	cp := *v
	return &cp, nil
}

// DeleteVRF implements Repository.
func (r *MemoryRepository) DeleteVRF(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.vrfs[id]
	if !ok || v.OrganizationID != orgID {
		return ErrNotFound
	}
	if v.IsDefault {
		return fmt.Errorf("%w: the default VRF cannot be deleted", ErrInUse)
	}
	for _, s := range r.subnets {
		if s.VRFID == id {
			return fmt.Errorf("%w: the VRF still has subnets", ErrInUse)
		}
	}
	delete(r.vrfs, id)
	return nil
}
