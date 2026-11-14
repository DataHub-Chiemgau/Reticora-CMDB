package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// NameserverSet is a reusable list of DNS servers of the organization
// (IPM-05, WP-905), as the nameserver sets of phpIPAM are. A subnet
// references one optionally (nameserver_set_id); its effective DNS servers
// are its own dns_servers, else the servers of its set. A set belongs to
// the whole organization: every principal reads it, only principals without
// client and site restriction write it (E-09, restrictive policies as for
// the VRFs of migration 000148).
type NameserverSet struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Servers        []string  `json:"servers"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateNameserverSetRequest is the payload of POST /api/v1/nameserver-sets.
type CreateNameserverSetRequest struct {
	Name        string   `json:"name"`
	Servers     []string `json:"servers"`
	Description string   `json:"description,omitempty"`
}

// UpdateNameserverSetRequest is the payload of PATCH
// /api/v1/nameserver-sets/{id}; servers replaces the whole list.
type UpdateNameserverSetRequest struct {
	Name        *string  `json:"name,omitempty"`
	Servers     []string `json:"servers,omitempty"`
	Description *string  `json:"description,omitempty"`
}

// NameserverRepository persists nameserver sets. Only the PostgreSQL
// repository implements it (optional interface, pattern DuplicateFinder);
// without a database the routes answer 501 and a subnet takes no set.
type NameserverRepository interface {
	ListNameserverSets(ctx context.Context, orgID string, page api.PaginationParams) ([]NameserverSet, int, error)
	GetNameserverSet(ctx context.Context, orgID, id string) (*NameserverSet, error)
	CreateNameserverSet(ctx context.Context, s *NameserverSet) error
	UpdateNameserverSet(ctx context.Context, orgID, id string, req UpdateNameserverSetRequest) (*NameserverSet, error)
	DeleteNameserverSet(ctx context.Context, orgID, id string) error
}

// maxNameserverSetServers is the upper bound of nameserver_set_servers_check.
const maxNameserverSetServers = 8

// Fixed answers of the nameserver set rules.
var (
	errNameserverSetNameTaken = &api.ConflictError{Field: "name", Message: "a nameserver set with this name already exists"}
	errNameserverSetInUse     = &api.ConflictError{Field: "id", Message: "the nameserver set is still used by subnets"}
	errNameserverSetNotFound  = &api.FieldError{Field: "nameserver_set_id", Message: "nameserver set not found",
		Status: http.StatusUnprocessableEntity}
	errNameserverSetServers = &api.FieldError{Field: "servers",
		Message: "servers must hold 1 to 8 valid IP addresses", Status: http.StatusUnprocessableEntity}
)

// nameserverSetError maps the constraint violations of nameserver_set onto
// the fixed answers: a duplicate name and a set that subnets still use
// (409), a server list outside 1 to 8 entries (422).
func nameserverSetError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "nameserver_set_name_key":
		return errNameserverSetNameTaken
	case pgErr.Code == "23503" && pgErr.ConstraintName == "subnet_nameserver_set_fkey":
		return errNameserverSetInUse
	case pgErr.Code == "23514" && pgErr.ConstraintName == "nameserver_set_servers_check":
		return errNameserverSetServers
	}
	return err
}

// normalizeNameservers checks and normalizes the server list of a set: 1 to
// 8 IP addresses.
func normalizeNameservers(servers []string) ([]string, error) {
	if len(servers) == 0 || len(servers) > maxNameserverSetServers {
		return nil, errNameserverSetServers
	}
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		// PostgreSQL inet knows no IPv6 zone ("fe80::1%eth0"); a zone would
		// fail the cast with 22P02 instead of 422 servers.
		addr, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || addr.Zone() != "" {
			return nil, errNameserverSetServers
		}
		out = append(out, addr.String())
	}
	return out, nil
}

// checkNameserverSet accepts an empty set id (no set) and a set of the
// organization; everything else is 422 nameserver_set_id. The read runs in
// the writing transaction under the principal's row level security; the
// sets are readable org-wide, so a set of another organization is unknown.
func checkNameserverSet(ctx context.Context, tx pgx.Tx, orgID, setID string) error {
	if strings.TrimSpace(setID) == "" {
		return nil
	}
	if !api.IsUUID(setID) {
		return errNameserverSetNotFound
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nameserver_set WHERE organization_id = $1 AND id = $2::uuid)`,
		orgID, setID).Scan(&ok); err != nil {
		return fmt.Errorf("read nameserver set: %w", err)
	}
	if !ok {
		return errNameserverSetNotFound
	}
	return nil
}

// --- PostgreSQL ---

const nameserverSetCols = `id::text, organization_id::text, name, ARRAY(SELECT host(u.s) FROM unnest(servers) WITH ORDINALITY AS u(s, n) ORDER BY u.n),
	COALESCE(description, ''), created_at, updated_at`

func scanNameserverSet(s scanner) (*NameserverSet, error) {
	ns := &NameserverSet{}
	if err := s.Scan(&ns.ID, &ns.OrganizationID, &ns.Name, &ns.Servers, &ns.Description, &ns.CreatedAt, &ns.UpdatedAt); err != nil {
		return nil, err
	}
	ns.CreatedAt, ns.UpdatedAt = ns.CreatedAt.UTC(), ns.UpdatedAt.UTC()
	return ns, nil
}

// ListNameserverSets lists the sets of the organization by name.
func (r *PGRepository) ListNameserverSets(ctx context.Context, orgID string, page api.PaginationParams) ([]NameserverSet, int, error) {
	out := []NameserverSet{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM nameserver_set WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+nameserverSetCols+` FROM nameserver_set WHERE organization_id = $1
			ORDER BY name, id LIMIT $2 OFFSET $3`, orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			ns, err := scanNameserverSet(rows)
			if err != nil {
				return err
			}
			out = append(out, *ns)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetNameserverSet returns a set of the organization.
func (r *PGRepository) GetNameserverSet(ctx context.Context, orgID, id string) (*NameserverSet, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var ns *NameserverSet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ns, err = scanNameserverSet(tx.QueryRow(ctx, "SELECT "+nameserverSetCols+
			" FROM nameserver_set WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return ns, err
}

// CreateNameserverSet stores a set. For a principal restricted in the
// client or site scope the insert fails with the row level security error
// 42501 (403 at the API).
func (r *PGRepository) CreateNameserverSet(ctx context.Context, s *NameserverSet) error {
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := scanNameserverSet(tx.QueryRow(ctx, `INSERT INTO nameserver_set (organization_id, name, servers, description)
			VALUES ($1, $2, $3::inet[], $4) RETURNING `+nameserverSetCols,
			s.OrganizationID, s.Name, s.Servers, nilIfEmpty(s.Description)))
		if err != nil {
			return nameserverSetError(err)
		}
		*s = *created
		return nil
	})
}

// UpdateNameserverSet changes name, servers and description of a set. A
// principal restricted in the client or site scope updates no row:
// ErrNotFound.
func (r *PGRepository) UpdateNameserverSet(ctx context.Context, orgID, id string, req UpdateNameserverSetRequest) (*NameserverSet, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var servers any
	if req.Servers != nil {
		servers = req.Servers
	}
	// New servers change the target of the PTR probes of the scanned
	// subnets that take their DNS servers from the set (IPM-12, WP-1064 AK2).
	resync := r.scanProbes != nil && req.Servers != nil
	var ns *NameserverSet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The probe budget lock first (WP-940 AK3), then the rows.
		if resync {
			if err := r.scanProbes.LockProbeBudget(ctx, tx, orgID); err != nil {
				return err
			}
		}
		var err error
		ns, err = scanNameserverSet(tx.QueryRow(ctx, `UPDATE nameserver_set SET name = COALESCE($3, name),
			servers = COALESCE($4::inet[], servers),
			description = CASE WHEN $5::text IS NULL THEN description ELSE NULLIF($5, '') END
			WHERE organization_id = $1 AND id = $2 RETURNING `+nameserverSetCols,
			orgID, id, req.Name, servers, req.Description))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || !resync {
			return nameserverSetError(err)
		}
		rows, err := tx.Query(ctx, `SELECT s.id::text FROM subnet s
			 WHERE s.organization_id = $1 AND s.nameserver_set_id = $2 AND s.scan_enabled
			   AND COALESCE(cardinality(s.dns_servers), 0) = 0`, orgID, id)
		if err != nil {
			return fmt.Errorf("subnets of the nameserver set: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(ids) == 0 {
			return err
		}
		return r.SyncScanTargets(ctx, tx, orgID, ids)
	})
	return ns, err
}

// DeleteNameserverSet removes a set; a set that subnets use stays (409,
// also for subnets outside the caller's scope). A principal restricted in
// the client or site scope deletes no row: ErrNotFound.
func (r *PGRepository) DeleteNameserverSet(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return nameserverSetError(r.deleteByID(ctx, orgID, "nameserver_set", "true", id))
}

// --- HTTP ---

// HTTP handlers of the nameserver sets (IPM-05): /api/v1/nameserver-sets;
// reads need ipam:read, writes ipam:write.

// nameserverRepo returns the repository of the sets, or answers 501 when
// the repository has none (no database).
func (h *Handler) nameserverRepo(w http.ResponseWriter) (NameserverRepository, bool) {
	repo, ok := h.repo.(NameserverRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "nameserver sets are not available")
	}
	return repo, ok
}

// checkSubnetNameserverSet refuses a nameserver set on a subnet when the
// repository keeps no sets (no database): the set cannot be known.
func (h *Handler) checkSubnetNameserverSet(setID *string) error {
	if setID == nil || strings.TrimSpace(*setID) == "" {
		return nil
	}
	if _, ok := h.repo.(NameserverRepository); !ok {
		return errNameserverSetNotFound
	}
	return nil
}

// ListNameserverSets handles GET /api/v1/nameserver-sets
func (h *Handler) ListNameserverSets(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.nameserverRepo(w)
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
	items, total, err := repo.ListNameserverSets(r.Context(), orgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[NameserverSet]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateNameserverSet handles POST /api/v1/nameserver-sets
func (h *Handler) CreateNameserverSet(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.nameserverRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateNameserverSetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "name", Message: "name is required", Status: http.StatusUnprocessableEntity})
		return
	}
	servers, err := normalizeNameservers(req.Servers)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	s := &NameserverSet{OrganizationID: orgID, Name: name, Servers: servers, Description: req.Description}
	if err := repo.CreateNameserverSet(r.Context(), s); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}

// GetNameserverSet handles GET /api/v1/nameserver-sets/{id}
func (h *Handler) GetNameserverSet(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.nameserverRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	s, err := repo.GetNameserverSet(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeNameserverSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// UpdateNameserverSet handles PATCH /api/v1/nameserver-sets/{id}
func (h *Handler) UpdateNameserverSet(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.nameserverRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateNameserverSetRequest
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
	if req.Servers != nil {
		servers, err := normalizeNameservers(req.Servers)
		if err != nil {
			api.WriteFieldError(w, err)
			return
		}
		req.Servers = servers
	}
	s, err := repo.UpdateNameserverSet(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeNameserverSetError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// DeleteNameserverSet handles DELETE /api/v1/nameserver-sets/{id}: 409 for
// a set that subnets use.
func (h *Handler) DeleteNameserverSet(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.nameserverRepo(w)
	if !ok {
		return
	}
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteNameserverSet(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeNameserverSetError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeNameserverSetError answers a failed set request: 404 for an unknown
// or unwritable set, else the repository error.
func writeNameserverSetError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "nameserver set not found")
		return
	}
	api.WriteRepoError(w, err)
}
