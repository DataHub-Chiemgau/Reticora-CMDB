package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VLANDomain is a layer-2 domain that spans sites (IPM-06, WP-906, E-212),
// as the L2 domains of phpIPAM are. A VLAN of a domain is unique per
// (domain, vlan_id); VLANs without domain stay unique per site or, without
// site, per organization (E-24). A domain belongs to the whole
// organization: every principal reads it, only principals without client
// and site restriction write it (E-09). SiteIDs are the sites of the domain
// the caller sees (the assignments carry the client and site of their site).
type VLANDomain struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	SiteIDs        []string  `json:"site_ids"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateVLANDomainRequest is the payload of POST /api/v1/vlan-domains.
type CreateVLANDomainRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	SiteIDs     []string `json:"site_ids,omitempty"`
}

// UpdateVLANDomainRequest is the payload of PATCH /api/v1/vlan-domains/{id};
// site_ids replaces the sites of the domain.
type UpdateVLANDomainRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	SiteIDs     *[]string `json:"site_ids,omitempty"`
}

// VLANDomainRepository persists VLAN domains. Only the PostgreSQL repository
// implements it (optional interface); without a database the routes answer
// 501.
type VLANDomainRepository interface {
	ListVLANDomains(ctx context.Context, orgID string, page api.PaginationParams) ([]VLANDomain, int, error)
	GetVLANDomain(ctx context.Context, orgID, id string) (*VLANDomain, error)
	CreateVLANDomain(ctx context.Context, d *VLANDomain) error
	UpdateVLANDomain(ctx context.Context, orgID, id string, req UpdateVLANDomainRequest) (*VLANDomain, error)
	DeleteVLANDomain(ctx context.Context, orgID, id string) error
	ListDomainVLANs(ctx context.Context, orgID, domainID string, page api.PaginationParams) ([]VLAN, int, error)
}

// Fixed answers of the VLAN domain rules.
var (
	errVLANDomainNameTaken = &api.ConflictError{Field: "name", Message: "a VLAN domain with this name already exists"}
	errVLANDomainInUse     = &api.ConflictError{Field: "id", Message: "the VLAN domain still holds VLANs"}
	errVLANDomainSiteInUse = &api.ConflictError{Field: "site_ids",
		Message: "VLANs of the domain still use a site that would leave it"}
	errVLANDomainSites = &api.FieldError{Field: "site_ids", Message: "site not found",
		Status: http.StatusUnprocessableEntity}
	errVLANDomainNotFound = &api.FieldError{Field: "domain_id", Message: "VLAN domain not found",
		Status: http.StatusUnprocessableEntity}
	errVLANSiteOutsideDomain = &api.FieldError{Field: "site_id",
		Message: "the site does not belong to the VLAN domain", Status: http.StatusUnprocessableEntity}
)

// vlanDomainError maps the constraint violations of a domain write: a
// duplicate name, a domain with VLANs (409), a site that VLANs of the
// domain still use (409) and an unknown site (422).
func vlanDomainError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "vlan_domain_name_key":
		return errVLANDomainNameTaken
	case pgErr.Code == "23503" && pgErr.ConstraintName == "vlan_domain_fkey":
		return errVLANDomainInUse
	case pgErr.Code == "23503" && pgErr.ConstraintName == "vlan_domain_site_fkey":
		return errVLANDomainSiteInUse
	case pgErr.Code == "23503" && pgErr.ConstraintName == "vlan_domain_site_site_fkey":
		return errVLANDomainSites
	}
	return err
}

// vlanDomainWriteError maps the domain rules of a VLAN write: a site
// outside the domain (vlan_domain_site_fkey) is 422 site_id, a domain
// deleted meanwhile 422 domain_id.
func vlanDomainWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	switch pgErr.ConstraintName {
	case "vlan_domain_site_fkey":
		return errVLANSiteOutsideDomain
	case "vlan_domain_fkey":
		return errVLANDomainNotFound
	}
	return err
}

// checkVLANDomain accepts an empty domain id (no domain) and a domain of the
// organization; everything else is 422 domain_id. Domains are readable
// org-wide, so a domain of another organization is unknown.
func checkVLANDomain(ctx context.Context, tx pgx.Tx, orgID, domainID string) error {
	if strings.TrimSpace(domainID) == "" {
		return nil
	}
	if !api.IsUUID(domainID) {
		return errVLANDomainNotFound
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vlan_domain WHERE organization_id = $1 AND id = $2::uuid)`,
		orgID, domainID).Scan(&ok); err != nil {
		return fmt.Errorf("read vlan domain: %w", err)
	}
	if !ok {
		return errVLANDomainNotFound
	}
	return nil
}

// normalizeSiteIDs drops duplicates and refuses ids that are no UUIDs.
func normalizeSiteIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !api.IsUUID(id) {
			return nil, errVLANDomainSites
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// --- PostgreSQL ---

// vlanDomainCols reads a domain with the sites the caller sees (row level
// security of vlan_domain_site).
const vlanDomainCols = `id::text, organization_id::text, name, COALESCE(description, ''),
	ARRAY(SELECT ds.site_id::text FROM vlan_domain_site ds
		WHERE ds.domain_id = vlan_domain.id AND ds.organization_id = vlan_domain.organization_id ORDER BY ds.site_id),
	created_at, updated_at`

func scanVLANDomain(s scanner) (*VLANDomain, error) {
	d := &VLANDomain{}
	if err := s.Scan(&d.ID, &d.OrganizationID, &d.Name, &d.Description, &d.SiteIDs, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if d.SiteIDs == nil {
		d.SiteIDs = []string{}
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	return d, nil
}

// replaceDomainSites makes siteIDs the sites of the domain, in the writing
// transaction: every site must be a site of the organization the caller
// sees (422 site_ids); a site that VLANs of the domain still use cannot
// leave it (RESTRICT, 409 site_ids).
func replaceDomainSites(ctx context.Context, tx pgx.Tx, orgID, domainID string, siteIDs []string) error {
	var visible int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM site WHERE organization_id = $1 AND id = ANY ($2::uuid[])`,
		orgID, siteIDs).Scan(&visible); err != nil {
		return fmt.Errorf("read sites: %w", err)
	}
	if visible != len(siteIDs) {
		return errVLANDomainSites
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vlan_domain_site WHERE organization_id = $1 AND domain_id = $2
		AND NOT (site_id = ANY ($3::uuid[]))`, orgID, domainID, siteIDs); err != nil {
		return vlanDomainError(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO vlan_domain_site (organization_id, domain_id, site_id, client_id)
		SELECT $1, $2, s, NULL FROM unnest($3::uuid[]) AS s
		ON CONFLICT (domain_id, site_id) DO NOTHING`, orgID, domainID, siteIDs); err != nil {
		return vlanDomainError(err)
	}
	return nil
}

// ListVLANDomains lists the domains of the organization by name.
func (r *PGRepository) ListVLANDomains(ctx context.Context, orgID string, page api.PaginationParams) ([]VLANDomain, int, error) {
	out := []VLANDomain{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM vlan_domain WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+vlanDomainCols+` FROM vlan_domain WHERE organization_id = $1
			ORDER BY name, id LIMIT $2 OFFSET $3`, orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanVLANDomain(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, total, err
}

func getVLANDomain(ctx context.Context, tx pgx.Tx, orgID, id string) (*VLANDomain, error) {
	d, err := scanVLANDomain(tx.QueryRow(ctx, "SELECT "+vlanDomainCols+
		" FROM vlan_domain WHERE organization_id = $1 AND id = $2", orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// GetVLANDomain returns a domain of the organization.
func (r *PGRepository) GetVLANDomain(ctx context.Context, orgID, id string) (*VLANDomain, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var d *VLANDomain
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = getVLANDomain(ctx, tx, orgID, id)
		return err
	})
	return d, err
}

// CreateVLANDomain stores a domain with its sites. For a principal
// restricted in the client or site scope the insert fails with the row
// level security error 42501 (403 at the API).
func (r *PGRepository) CreateVLANDomain(ctx context.Context, d *VLANDomain) error {
	return database.WithRequestTenant(ctx, r.pool, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO vlan_domain (organization_id, name, description)
			VALUES ($1, $2, $3) RETURNING id::text`, d.OrganizationID, d.Name, nilIfEmpty(d.Description)).Scan(&id); err != nil {
			return vlanDomainError(err)
		}
		if err := replaceDomainSites(ctx, tx, d.OrganizationID, id, d.SiteIDs); err != nil {
			return err
		}
		created, err := getVLANDomain(ctx, tx, d.OrganizationID, id)
		if err != nil {
			return err
		}
		*d = *created
		return nil
	})
}

// UpdateVLANDomain changes name and description and replaces the sites when
// req.SiteIDs is set. The domain row is always updated first, so a
// principal restricted in the client or site scope changes nothing:
// ErrNotFound.
func (r *PGRepository) UpdateVLANDomain(ctx context.Context, orgID, id string, req UpdateVLANDomainRequest) (*VLANDomain, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var d *VLANDomain
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE vlan_domain SET name = COALESCE($3, name),
			description = CASE WHEN $4::text IS NULL THEN description ELSE NULLIF($4, '') END, updated_at = now()
			WHERE organization_id = $1 AND id = $2`, orgID, id, req.Name, req.Description)
		if err != nil {
			return vlanDomainError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if req.SiteIDs != nil {
			if siteErr := replaceDomainSites(ctx, tx, orgID, id, *req.SiteIDs); siteErr != nil {
				return siteErr
			}
		}
		d, err = getVLANDomain(ctx, tx, orgID, id)
		return err
	})
	return d, err
}

// DeleteVLANDomain removes a domain and its site assignments; a domain with
// VLANs stays (409, also for VLANs outside the caller's scope). A principal
// restricted in the client or site scope deletes no row: ErrNotFound.
func (r *PGRepository) DeleteVLANDomain(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	err := vlanDomainError(r.deleteByID(ctx, orgID, "vlan_domain", "true", id))
	if errors.Is(err, errVLANDomainSiteInUse) {
		// The cascade to the assignments hit the VLANs of the domain.
		return errVLANDomainInUse
	}
	return err
}

// ListDomainVLANs lists the VLANs of a domain the caller sees, by site and
// number; an unknown domain is ErrNotFound.
func (r *PGRepository) ListDomainVLANs(ctx context.Context, orgID, domainID string, page api.PaginationParams) ([]VLAN, int, error) {
	if !api.IsUUID(domainID) {
		return nil, 0, ErrNotFound
	}
	out := []VLAN{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vlan_domain WHERE organization_id = $1 AND id = $2)`,
			orgID, domainID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM vlan WHERE organization_id = $1 AND domain_id = $2",
			orgID, domainID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+vlanCols+` FROM vlan WHERE organization_id = $1 AND domain_id = $2
			ORDER BY site_id NULLS FIRST, vlan_id, id LIMIT $3 OFFSET $4`, orgID, domainID, page.Limit, page.Offset)
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

// --- HTTP ---

// HTTP handlers of the VLAN domains (IPM-06): /api/v1/vlan-domains; reads
// need ipam:read, writes ipam:write.

// vlanDomainRepo returns the repository of the domains, or answers 501 when
// the repository has none (no database).
func (h *Handler) vlanDomainRepo(w http.ResponseWriter) (VLANDomainRepository, bool) {
	repo, ok := h.repo.(VLANDomainRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "VLAN domains are not available")
	}
	return repo, ok
}

// ListVLANDomains handles GET /api/v1/vlan-domains
func (h *Handler) ListVLANDomains(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.vlanDomainRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := repo.ListVLANDomains(r.Context(), orgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[VLANDomain]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateVLANDomain handles POST /api/v1/vlan-domains
func (h *Handler) CreateVLANDomain(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.vlanDomainRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateVLANDomainRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "name", Message: "name is required", Status: http.StatusUnprocessableEntity})
		return
	}
	sites, err := normalizeSiteIDs(req.SiteIDs)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	d := &VLANDomain{OrganizationID: orgID, Name: name, Description: req.Description, SiteIDs: sites}
	if err := repo.CreateVLANDomain(r.Context(), d); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, d)
}

// GetVLANDomain handles GET /api/v1/vlan-domains/{id}
func (h *Handler) GetVLANDomain(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.vlanDomainRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	d, err := repo.GetVLANDomain(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeVLANDomainError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

// UpdateVLANDomain handles PATCH /api/v1/vlan-domains/{id}
func (h *Handler) UpdateVLANDomain(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.vlanDomainRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateVLANDomainRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			api.WriteFieldError(w, &api.FieldError{Field: "name", Message: "name must not be empty", Status: http.StatusUnprocessableEntity})
			return
		}
		req.Name = &name
	}
	if req.SiteIDs != nil {
		sites, err := normalizeSiteIDs(*req.SiteIDs)
		if err != nil {
			api.WriteFieldError(w, err)
			return
		}
		req.SiteIDs = &sites
	}
	d, err := repo.UpdateVLANDomain(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeVLANDomainError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

// DeleteVLANDomain handles DELETE /api/v1/vlan-domains/{id}: 409 for a
// domain with VLANs.
func (h *Handler) DeleteVLANDomain(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.vlanDomainRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteVLANDomain(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeVLANDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListVLANDomainVLANs handles GET /api/v1/vlan-domains/{id}/vlans
func (h *Handler) ListVLANDomainVLANs(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.vlanDomainRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := repo.ListDomainVLANs(r.Context(), orgID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeVLANDomainError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[VLAN]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// writeVLANDomainError answers a failed domain request: 404 for an unknown
// or unwritable domain, else the repository error.
func writeVLANDomainError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "VLAN domain not found")
		return
	}
	api.WriteRepoError(w, err)
}
