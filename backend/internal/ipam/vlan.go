package ipam

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// VLAN is an entry of the VLAN catalog (NET-08): the number is unique per
// site, or per organization for VLANs without site (E-24). Subnets and
// interfaces reference it by id.
type VLAN struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	// ClientID is the client of the site (read-only).
	ClientID    string    `json:"client_id,omitempty"`
	SiteID      string    `json:"site_id,omitempty"`
	VLANID      int       `json:"vlan_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateVLANRequest is the payload for creating a VLAN.
type CreateVLANRequest struct {
	SiteID      string `json:"site_id,omitempty"`
	VLANID      int    `json:"vlan_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UpdateVLANRequest is the payload for updating a VLAN.
type UpdateVLANRequest struct {
	VLANID      *int    `json:"vlan_id,omitempty"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func validateVLAN(number int, name string) error {
	if number < 1 || number > 4094 {
		return fmt.Errorf("%w: vlan_id must be between 1 and 4094", ErrValidation)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	return nil
}

// --- PostgreSQL ---

const vlanCols = `id::text, organization_id::text, COALESCE(client_id::text,''), COALESCE(site_id::text,''),
	vlan_id, name, COALESCE(description,''), created_at, updated_at`

func scanVLAN(s scanner) (*VLAN, error) {
	v := &VLAN{}
	if err := s.Scan(&v.ID, &v.OrganizationID, &v.ClientID, &v.SiteID, &v.VLANID, &v.Name, &v.Description,
		&v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, nil
}

// ListVLANs lists the VLANs of the organization (of one site when siteID is
// set), ordered by site and number.
func (r *PGRepository) ListVLANs(ctx context.Context, orgID, siteID string, page api.PaginationParams) ([]VLAN, int, error) {
	var out []VLAN
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := "organization_id = $1", []any{orgID}
		if siteID != "" {
			where += " AND site_id = $2"
			args = append(args, siteID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM vlan WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		pos := len(args) + 1
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM vlan WHERE %s ORDER BY site_id NULLS FIRST, vlan_id LIMIT $%d OFFSET $%d",
			vlanCols, where, pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVLAN(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetVLAN returns a VLAN.
func (r *PGRepository) GetVLAN(ctx context.Context, orgID, id string) (*VLAN, error) {
	var v *VLAN
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = scanVLAN(tx.QueryRow(ctx, "SELECT "+vlanCols+" FROM vlan WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return v, err
}

// CreateVLAN stores a VLAN; the client follows the site.
func (r *PGRepository) CreateVLAN(ctx context.Context, v *VLAN) error {
	return database.WithRequestTenant(ctx, r.pool, v.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := scanVLAN(tx.QueryRow(ctx, `INSERT INTO vlan (organization_id, site_id, vlan_id, name, description)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+vlanCols,
			v.OrganizationID, nilIfEmpty(v.SiteID), v.VLANID, v.Name, nilIfEmpty(v.Description)))
		if err != nil {
			return err
		}
		*v = *created
		return nil
	})
}

// UpdateVLAN changes number, name or description of a VLAN.
func (r *PGRepository) UpdateVLAN(ctx context.Context, orgID, id string, req UpdateVLANRequest) (*VLAN, error) {
	var v *VLAN
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = scanVLAN(tx.QueryRow(ctx, `UPDATE vlan SET vlan_id = COALESCE($3, vlan_id), name = COALESCE($4, name),
			description = CASE WHEN $5::text IS NULL THEN description ELSE NULLIF($5, '') END
			WHERE organization_id = $1 AND id = $2 RETURNING `+vlanCols,
			orgID, id, req.VLANID, req.Name, req.Description))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return v, err
}

// DeleteVLAN removes a VLAN; subnets and interfaces lose the reference.
func (r *PGRepository) DeleteVLAN(ctx context.Context, orgID, id string) error {
	return r.deleteByID(ctx, orgID, "vlan", "true", id)
}

// --- Memory ---

func (r *MemoryRepository) vlanTaken(v *VLAN, exceptID string) bool {
	for _, e := range r.vlans {
		if e.ID != exceptID && e.OrganizationID == v.OrganizationID && e.SiteID == v.SiteID && e.VLANID == v.VLANID {
			return true
		}
	}
	return false
}

// ListVLANs implements Repository.
func (r *MemoryRepository) ListVLANs(_ context.Context, orgID, siteID string, page api.PaginationParams) ([]VLAN, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []VLAN
	for _, v := range r.vlans {
		if v.OrganizationID == orgID && (siteID == "" || v.SiteID == siteID) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SiteID != out[j].SiteID {
			return out[i].SiteID < out[j].SiteID
		}
		return out[i].VLANID < out[j].VLANID
	})
	items, total := pageSlice(out, page)
	return items, total, nil
}

// GetVLAN implements Repository.
func (r *MemoryRepository) GetVLAN(_ context.Context, orgID, id string) (*VLAN, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.vlans[id]
	if !ok || v.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *v
	return &cp, nil
}

// CreateVLAN implements Repository.
func (r *MemoryRepository) CreateVLAN(_ context.Context, v *VLAN) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vlanTaken(v, "") {
		return fmt.Errorf("%w: vlan_id %d already exists", ErrValidation, v.VLANID)
	}
	r.seq++
	v.ID = fmt.Sprintf("vlan-%d", r.seq)
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	stored := *v
	r.vlans[v.ID] = &stored
	return nil
}

// UpdateVLAN implements Repository.
func (r *MemoryRepository) UpdateVLAN(_ context.Context, orgID, id string, req UpdateVLANRequest) (*VLAN, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.vlans[id]
	if !ok || v.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	next := *v
	if req.VLANID != nil {
		next.VLANID = *req.VLANID
	}
	if req.Name != nil {
		next.Name = *req.Name
	}
	if req.Description != nil {
		next.Description = *req.Description
	}
	if r.vlanTaken(&next, id) {
		return nil, fmt.Errorf("%w: vlan_id %d already exists", ErrValidation, next.VLANID)
	}
	next.UpdatedAt = time.Now().UTC()
	*v = next
	cp := next
	return &cp, nil
}

// DeleteVLAN implements Repository.
func (r *MemoryRepository) DeleteVLAN(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.vlans[id]
	if !ok || v.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.vlans, id)
	return nil
}

// --- Handlers ---

// ListVLANs handles GET /api/v1/vlans
func (h *Handler) ListVLANs(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListVLANs(r.Context(), orgID, r.URL.Query().Get("site_id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []VLAN{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[VLAN]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateVLAN handles POST /api/v1/vlans
func (h *Handler) CreateVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateVLANRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := validateVLAN(req.VLANID, req.Name); err != nil {
		writeRepoError(w, err, "")
		return
	}
	v := &VLAN{OrganizationID: orgID, SiteID: req.SiteID, VLANID: req.VLANID, Name: strings.TrimSpace(req.Name),
		Description: req.Description}
	if err := h.repo.CreateVLAN(r.Context(), v); err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, v)
}

// GetVLAN handles GET /api/v1/vlans/{id}
func (h *Handler) GetVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	v, err := h.repo.GetVLAN(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// UpdateVLAN handles PATCH /api/v1/vlans/{id}
func (h *Handler) UpdateVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateVLANRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	number, name := 1, "x"
	if req.VLANID != nil {
		number = *req.VLANID
	}
	if req.Name != nil {
		name = *req.Name
	}
	if err := validateVLAN(number, name); err != nil {
		writeRepoError(w, err, "")
		return
	}
	v, err := h.repo.UpdateVLAN(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// DeleteVLAN handles DELETE /api/v1/vlans/{id}
func (h *Handler) DeleteVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteVLAN(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
