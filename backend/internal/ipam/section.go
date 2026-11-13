package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Section is an IPAM section (IPM-01, WP-900, E-209): it groups the subnets
// of an organization with settings of its own, as the sections of phpIPAM
// do. Every organization has exactly one org-wide default section
// "default" (is_default), which cannot be deleted; subnets written without
// a section land there. A section of a client holds only subnets of that
// client; an org-wide section (no client) holds subnets of every client.
// A principal restricted to clients reads the sections of its clients and
// the org-wide ones and writes only sections of its clients (E-09).
// strict_mode is stored here; the overlap check follows with WP-901.
type Section struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	StrictMode     bool   `json:"strict_mode"`
	ShowVLAN       bool   `json:"show_vlan"`
	ShowVRF        bool   `json:"show_vrf"`
	IsDefault      bool   `json:"is_default"`
	// ACLEnabled marks a restricted section (IPM-10, E-256): the first
	// entry of ipam_section_permission sets it, and it stays set when the
	// entries are gone (also through a deleted team), so that only the
	// principals of a team with an entry read and write the section
	// (fail-closed). Only PATCH with acl_enabled=false opens it again.
	ACLEnabled bool `json:"acl_enabled"`
	// VRFIDs are the VRFs assigned to the section (IPM-07, read-only here;
	// PATCH /vrfs/{id} with section_ids sets them).
	VRFIDs    []string  `json:"vrf_ids"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateSectionRequest is the payload of POST /api/v1/ipam-sections.
// show_vlan and show_vrf default to true, strict_mode to false.
type CreateSectionRequest struct {
	ClientID    string `json:"client_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	StrictMode  *bool  `json:"strict_mode,omitempty"`
	ShowVLAN    *bool  `json:"show_vlan,omitempty"`
	ShowVRF     *bool  `json:"show_vrf,omitempty"`
}

// UpdateSectionRequest is the payload of PATCH /api/v1/ipam-sections/{id};
// the client of a section does not change.
type UpdateSectionRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	StrictMode  *bool   `json:"strict_mode,omitempty"`
	ShowVLAN    *bool   `json:"show_vlan,omitempty"`
	ShowVRF     *bool   `json:"show_vrf,omitempty"`
	// ACLEnabled false opens a restricted section (E-256); true is 422
	// (the first entry restricts a section). Opening is refused while
	// entries exist (409 acl_enabled) and needs
	// write on the section or an org-wide administrator (team:manage
	// without client, site and team restriction); it is the explicit,
	// audited release of a section whose last team was deleted.
	ACLEnabled *bool `json:"acl_enabled,omitempty"`
	// adminRelease is set by the handler for an org-wide administrator:
	// it may open a section it cannot see or write.
	adminRelease bool
}

// SectionRepository persists IPAM sections.
type SectionRepository interface {
	ListSections(ctx context.Context, orgID string, page api.PaginationParams) ([]Section, int, error)
	GetSection(ctx context.Context, orgID, id string) (*Section, error)
	CreateSection(ctx context.Context, s *Section) error
	UpdateSection(ctx context.Context, orgID, id string, req UpdateSectionRequest) (*Section, error)
	DeleteSection(ctx context.Context, orgID, id string) error
}

// Fixed answers of the section rules.
var (
	errSectionNameTaken = &api.ConflictError{Field: "name", Message: "an IPAM section with this name already exists"}
	errSectionDefault   = &api.ConflictError{Field: "id", Message: "the default IPAM section cannot be deleted"}
	errSectionInUse     = &api.ConflictError{Field: "id", Message: "the IPAM section still holds subnets"}
	errSectionNotFound  = &api.FieldError{Field: "section_id", Message: "IPAM section not found",
		Status: http.StatusUnprocessableEntity}
	errSectionOfAnotherClient = &api.FieldError{Field: "section_id",
		Message: "the IPAM section belongs to another client", Status: http.StatusUnprocessableEntity}
	errSectionClientNotFound = &api.FieldError{Field: "client_id", Message: "client not found",
		Status: http.StatusUnprocessableEntity}
	errSectionACLEntries = &api.ConflictError{Field: "acl_enabled",
		Message: "the IPAM section still has permissions; remove them before opening the section"}
)

// sectionError maps the constraint violations of ipam_section onto the
// fixed answers: a duplicate name, the protected default section, a section
// that subnets still reference (409) and an unknown client (422).
func sectionError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "ipam_section_name_key":
		return errSectionNameTaken
	case pgErr.Code == "23514" && pgErr.ConstraintName == "ipam_section_default_required":
		return errSectionDefault
	case pgErr.Code == "23503" && pgErr.ConstraintName == "subnet_section_fkey":
		return errSectionInUse
	case pgErr.Code == "23503" && pgErr.ConstraintName == "ipam_section_client_fkey":
		return errSectionClientNotFound
	}
	return err
}

// checkSection keeps the section of a subnet within its client (IPM-01,
// AK4). The composite foreign key of section_id checks only the
// organization and bypasses row level security, so the section is read
// here, in the writing transaction under the principal's row level security
// and before section_id is written (pattern of checkVLAN): a section the
// principal cannot see is "IPAM section not found"; a section of another
// client than clientID (empty: the subnet is org-wide) is refused; a
// section of that client or an org-wide section is accepted. Without a
// section (the default section) it costs no statement.
func checkSection(ctx context.Context, tx pgx.Tx, orgID, sectionID, clientID string) error {
	if strings.TrimSpace(sectionID) == "" {
		return nil
	}
	if !api.IsUUID(sectionID) {
		return errSectionNotFound
	}
	var visible, sameClient bool
	if err := tx.QueryRow(ctx, `SELECT count(*) > 0, COALESCE(bool_and(client_id IS NULL OR client_id = $3::uuid), false)
		FROM ipam_section WHERE organization_id = $1 AND id = $2::uuid`, orgID, sectionID, nilIfEmpty(clientID)).Scan(&visible, &sameClient); err != nil {
		return fmt.Errorf("read ipam section: %w", err)
	}
	switch {
	case !visible:
		return errSectionNotFound
	case !sameClient:
		return errSectionOfAnotherClient
	}
	return nil
}

// checkSubnetSection applies checkSection to an update of subnet id: to a
// new section and to the section of a subnet that moves to another client,
// against the client the subnet has after the update. The subnet row is
// locked (FOR UPDATE) until the commit, so a concurrent update of its
// client or section waits and checks the committed pair; the client of a
// section cannot change.
func checkSubnetSection(ctx context.Context, tx pgx.Tx, orgID, id string, req *UpdateSubnetRequest) error {
	if req.ClientID == nil && (req.SectionID == nil || strings.TrimSpace(*req.SectionID) == "") {
		return nil
	}
	var clientID, sectionID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), section_id::text FROM subnet
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&clientID, &sectionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read subnet: %w", err)
	}
	if req.ClientID != nil {
		clientID = *req.ClientID
	}
	if req.SectionID != nil {
		sectionID = *req.SectionID
	}
	return checkSection(ctx, tx, orgID, sectionID, clientID)
}

// --- PostgreSQL ---

const sectionCols = `id::text, organization_id::text, COALESCE(client_id::text, ''), name, COALESCE(description, ''),
	strict_mode, show_vlan, show_vrf, is_default, acl_enabled,
	ARRAY(SELECT vs.vrf_id::text FROM vrf_section vs
		WHERE vs.section_id = ipam_section.id AND vs.organization_id = ipam_section.organization_id ORDER BY vs.vrf_id),
	created_at, updated_at`

func scanSection(s scanner) (*Section, error) {
	sec := &Section{}
	if err := s.Scan(&sec.ID, &sec.OrganizationID, &sec.ClientID, &sec.Name, &sec.Description, &sec.StrictMode,
		&sec.ShowVLAN, &sec.ShowVRF, &sec.IsDefault, &sec.ACLEnabled, &sec.VRFIDs, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
		return nil, err
	}
	if sec.VRFIDs == nil {
		sec.VRFIDs = []string{}
	}
	sec.CreatedAt, sec.UpdatedAt = sec.CreatedAt.UTC(), sec.UpdatedAt.UTC()
	return sec, nil
}

// ListSections lists the sections the caller sees, the default first.
func (r *PGRepository) ListSections(ctx context.Context, orgID string, page api.PaginationParams) ([]Section, int, error) {
	var out []Section
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ipam_section WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+sectionCols+` FROM ipam_section WHERE organization_id = $1
			ORDER BY is_default DESC, name, id LIMIT $2 OFFSET $3`, orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sec, err := scanSection(rows)
			if err != nil {
				return err
			}
			out = append(out, *sec)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetSection returns a section the caller sees.
func (r *PGRepository) GetSection(ctx context.Context, orgID, id string) (*Section, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var sec *Section
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		sec, err = scanSection(tx.QueryRow(ctx, "SELECT "+sectionCols+" FROM ipam_section WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return sec, err
}

// CreateSection stores a section; only the database creates the default
// section. An org-wide section created by a principal restricted to
// clients, or a section of a client outside its scope, fails with the row
// level security error 42501 (403 at the API).
func (r *PGRepository) CreateSection(ctx context.Context, s *Section) error {
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := scanSection(tx.QueryRow(ctx, `INSERT INTO ipam_section
			(organization_id, client_id, name, description, strict_mode, show_vlan, show_vrf)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+sectionCols,
			s.OrganizationID, nilIfEmpty(s.ClientID), s.Name, nilIfEmpty(s.Description), s.StrictMode, s.ShowVLAN, s.ShowVRF))
		if err != nil {
			return sectionError(err)
		}
		*s = *created
		return nil
	})
}

// UpdateSection changes name, description and settings of a section. A
// principal restricted to clients changes no org-wide section: ErrNotFound.
// acl_enabled is changed only by a principal that may write the section
// (errSectionNotWritable otherwise); opening a section (false) is refused
// while it has entries, and an org-wide administrator (adminRelease) may
// open a section it cannot see, for which the scope of the statement is
// widened (E-256).
func (r *PGRepository) UpdateSection(ctx context.Context, orgID, id string, req UpdateSectionRequest) (*Section, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var sec *Section
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var previous []*string
		if req.ACLEnabled != nil {
			var err error
			if previous, err = sectionACLWritable(ctx, tx, orgID, id, *req.ACLEnabled, req.adminRelease); err != nil {
				return err
			}
		}
		var err error
		sec, err = scanSection(tx.QueryRow(ctx, `UPDATE ipam_section SET name = COALESCE($3, name),
			description = CASE WHEN $4::text IS NULL THEN description ELSE NULLIF($4, '') END,
			strict_mode = COALESCE($5, strict_mode), show_vlan = COALESCE($6, show_vlan), show_vrf = COALESCE($7, show_vrf),
			acl_enabled = COALESCE($8, acl_enabled)
			WHERE organization_id = $1 AND id = $2 RETURNING `+sectionCols,
			orgID, id, req.Name, req.Description, req.StrictMode, req.ShowVLAN, req.ShowVRF, req.ACLEnabled))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return sectionError(err)
		}
		if previous != nil {
			if _, err = tx.Exec(ctx, `SELECT app_scope_restore($1::text[])`, previous); err != nil {
				return fmt.Errorf("restore scope: %w", err)
			}
		}
		return nil
	})
	return sec, err
}

// sectionACLWritable checks a change of acl_enabled (E-256) in the writing
// transaction and locks the section row until the commit: the caller must
// see the section and may write it; opening it (enable false) is refused
// while entries exist (409). The entries are counted in a statement of its
// own after the lock, so under READ COMMITTED its snapshot holds an entry
// whose writer held the lock first (its trigger sets acl_enabled); an
// entry written after the lock waits in that trigger for the commit and
// sets acl_enabled again. An org-wide administrator (adminRelease) that
// does not see or may not write the section opens it with the scope
// widened (app_scope_widen, which ipam_section_denied honours); the
// previous scope is returned for the restore after the update, nil when
// nothing was widened.
func sectionACLWritable(ctx context.Context, tx pgx.Tx, orgID, id string, enable, adminRelease bool) ([]*string, error) {
	read := func() (writable bool, entries int, err error) {
		if lockErr := tx.QueryRow(ctx, `SELECT NOT (s.id = ANY (ipam_section_denied('write')))
			FROM ipam_section s WHERE s.organization_id = $1 AND s.id = $2 FOR UPDATE OF s`, orgID, id).Scan(&writable); lockErr != nil {
			return false, 0, lockErr
		}
		err = tx.QueryRow(ctx, `SELECT count(*) FROM ipam_section_permission p WHERE p.organization_id = $1 AND p.section_id = $2`,
			orgID, id).Scan(&entries)
		return writable, entries, err
	}
	writable, entries, err := read()
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read ipam section: %w", err)
	}
	var previous []*string
	if (errors.Is(err, pgx.ErrNoRows) || !writable) && adminRelease && !enable {
		if err = tx.QueryRow(ctx, `SELECT app_scope_widen()`).Scan(&previous); err != nil {
			return nil, fmt.Errorf("widen scope: %w", err)
		}
		if previous == nil {
			previous = []*string{}
		}
		writable, entries, err = read()
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("read ipam section: %w", err)
		}
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case !writable:
		return nil, errSectionNotWritable
	case !enable && entries > 0:
		return nil, errSectionACLEntries
	}
	return previous, nil
}

// DeleteSection removes a section; the default section and sections with
// subnets stay (409). A principal restricted to clients deletes no
// org-wide section: ErrNotFound.
func (r *PGRepository) DeleteSection(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return sectionError(r.deleteByID(ctx, orgID, "ipam_section", "true", id))
}

// --- Memory ---

// defaultSection returns the default section of the organization and
// creates it on first use, as the database does with the organization.
func (r *MemoryRepository) defaultSection(orgID string) *Section {
	for _, s := range r.sections {
		if s.OrganizationID == orgID && s.IsDefault {
			return s
		}
	}
	r.seq++
	now := time.Now().UTC()
	s := &Section{ID: fmt.Sprintf("section-%d", r.seq), OrganizationID: orgID, Name: "default",
		Description: "Default section", ShowVLAN: true, ShowVRF: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	r.sections[s.ID] = s
	return s
}

// sectionFor resolves the section of a subnet of client clientID: the
// default section for an empty id, else the section, which must exist and
// be org-wide or of that client.
func (r *MemoryRepository) sectionFor(orgID, sectionID, clientID string) (string, error) {
	if sectionID == "" {
		return r.defaultSection(orgID).ID, nil
	}
	s, ok := r.sections[sectionID]
	switch {
	case !ok || s.OrganizationID != orgID:
		return "", errSectionNotFound
	case s.ClientID != "" && s.ClientID != clientID:
		return "", errSectionOfAnotherClient
	}
	return s.ID, nil
}

func (r *MemoryRepository) sectionNameTaken(orgID, name, exceptID string) bool {
	for _, s := range r.sections {
		if s.ID != exceptID && s.OrganizationID == orgID && s.Name == name {
			return true
		}
	}
	return false
}

// ListSections implements SectionRepository.
func (r *MemoryRepository) ListSections(_ context.Context, orgID string, page api.PaginationParams) ([]Section, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultSection(orgID)
	var out []Section
	for _, s := range r.sections {
		if s.OrganizationID == orgID {
			cp := *s
			cp.VRFIDs = r.vrfIDsOfSection(s.ID)
			out = append(out, cp)
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

// GetSection implements SectionRepository.
func (r *MemoryRepository) GetSection(_ context.Context, orgID, id string) (*Section, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultSection(orgID)
	s, ok := r.sections[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *s
	cp.VRFIDs = r.vrfIDsOfSection(id)
	return &cp, nil
}

// CreateSection implements SectionRepository.
func (r *MemoryRepository) CreateSection(_ context.Context, s *Section) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultSection(s.OrganizationID)
	if r.sectionNameTaken(s.OrganizationID, s.Name, "") {
		return errSectionNameTaken
	}
	r.seq++
	now := time.Now().UTC()
	s.ID, s.IsDefault, s.CreatedAt, s.UpdatedAt = fmt.Sprintf("section-%d", r.seq), false, now, now
	s.VRFIDs = []string{}
	stored := *s
	r.sections[s.ID] = &stored
	return nil
}

// UpdateSection implements SectionRepository.
func (r *MemoryRepository) UpdateSection(_ context.Context, orgID, id string, req UpdateSectionRequest) (*Section, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sections[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.Name != nil {
		if r.sectionNameTaken(orgID, *req.Name, id) {
			return nil, errSectionNameTaken
		}
		s.Name = *req.Name
	}
	if req.Description != nil {
		s.Description = *req.Description
	}
	if req.StrictMode != nil {
		s.StrictMode = *req.StrictMode
	}
	if req.ShowVLAN != nil {
		s.ShowVLAN = *req.ShowVLAN
	}
	if req.ShowVRF != nil {
		s.ShowVRF = *req.ShowVRF
	}
	if req.ACLEnabled != nil {
		s.ACLEnabled = *req.ACLEnabled
	}
	s.UpdatedAt = time.Now().UTC()
	cp := *s
	cp.VRFIDs = r.vrfIDsOfSection(id)
	return &cp, nil
}

// DeleteSection implements SectionRepository.
func (r *MemoryRepository) DeleteSection(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sections[id]
	if !ok || s.OrganizationID != orgID {
		return ErrNotFound
	}
	if s.IsDefault {
		return errSectionDefault
	}
	for _, sn := range r.subnets {
		if sn.SectionID == id {
			return errSectionInUse
		}
	}
	delete(r.sections, id)
	// The assignments of the VRFs go with the section (vrf_section cascade).
	for _, v := range r.vrfs {
		v.SectionIDs = slices.DeleteFunc(v.SectionIDs, func(s string) bool { return s == id })
	}
	return nil
}

// --- HTTP ---

// HTTP handlers of the IPAM sections (IPM-01): /api/v1/ipam-sections; reads
// need ipam:read, writes ipam:write.

// ListSections handles GET /api/v1/ipam-sections
func (h *Handler) ListSections(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := h.repo.ListSections(r.Context(), orgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Section]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateSection handles POST /api/v1/ipam-sections
func (h *Handler) CreateSection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateSectionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "name", Message: "name is required", Status: http.StatusUnprocessableEntity})
		return
	}
	if req.ClientID != "" && !api.IsUUID(req.ClientID) {
		api.WriteFieldError(w, errSectionClientNotFound)
		return
	}
	s := &Section{OrganizationID: orgID, ClientID: req.ClientID, Name: name, Description: req.Description,
		StrictMode: boolOr(req.StrictMode, false), ShowVLAN: boolOr(req.ShowVLAN, true), ShowVRF: boolOr(req.ShowVRF, true)}
	if err := h.repo.CreateSection(r.Context(), s); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}

// GetSection handles GET /api/v1/ipam-sections/{id}
func (h *Handler) GetSection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	s, err := h.repo.GetSection(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeSectionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// UpdateSection handles PATCH /api/v1/ipam-sections/{id}
func (h *Handler) UpdateSection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateSectionRequest
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
	if req.ACLEnabled != nil {
		// The first permission restricts a section; PATCH only opens it
		// (E-256). Opening is a change of its rights: org-wide principals
		// only, as for the entries (E-09).
		if *req.ACLEnabled {
			api.WriteFieldError(w, &api.FieldError{Field: "acl_enabled",
				Message: "the first permission restricts the section; acl_enabled may only be set to false",
				Status:  http.StatusUnprocessableEntity})
			return
		}
		if !orgWideWriter(w, r) {
			return
		}
		req.adminRelease = orgAdmin(r)
	}
	s, err := h.repo.UpdateSection(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		if errors.Is(err, errSectionNotWritable) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "the IPAM section may only be read")
			return
		}
		writeSectionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// DeleteSection handles DELETE /api/v1/ipam-sections/{id}: 409 for the
// default section and for a section with subnets.
func (h *Handler) DeleteSection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteSection(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeSectionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeSectionError answers a failed section request: 404 for an unknown
// or invisible section, else the repository error.
func writeSectionError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "IPAM section not found")
		return
	}
	api.WriteRepoError(w, err)
}

func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// --- Rights per section (IPM-10, WP-910, E-214) ---

// Levels of a section permission: write includes read.
const (
	SectionLevelRead  = "read"
	SectionLevelWrite = "write"
)

// SectionPermission grants a team read or write access to the subnets and
// addresses of a section (ipam_section_permission). A section with entries
// is read only by principals of a team with an entry and written only by
// principals of a team with level write, in addition to ipam:read and
// ipam:write; a section that never had entries is open to every principal
// as before. A section whose entries are gone stays restricted (fail-closed,
// acl_enabled, E-256). The database enforces it (row level security,
// migrations 000320 and the Nachtrag of E-256).
type SectionPermission struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	SectionID      string `json:"section_id"`
	TeamID         string `json:"team_id"`
	// TeamName is the name of the team when the caller sees it.
	TeamName  string    `json:"team_name,omitempty"`
	Level     string    `json:"level"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateSectionPermissionRequest is the payload of
// POST /api/v1/ipam-sections/{id}/permissions.
type CreateSectionPermissionRequest struct {
	TeamID string `json:"team_id"`
	Level  string `json:"level"`
}

// UpdateSectionPermissionRequest is the payload of
// PATCH /api/v1/ipam-sections/{id}/permissions/{permission_id}: only the
// level changes.
type UpdateSectionPermissionRequest struct {
	Level *string `json:"level,omitempty"`
}

// SectionPermissionRepository persists the rights per section. Only the
// PostgreSQL repository has them; without a database the routes answer
// 501.
type SectionPermissionRepository interface {
	ListSectionPermissions(ctx context.Context, orgID, sectionID string, page api.PaginationParams) ([]SectionPermission, int, error)
	CreateSectionPermission(ctx context.Context, p *SectionPermission) error
	UpdateSectionPermission(ctx context.Context, orgID, sectionID, id, level string) (*SectionPermission, error)
	DeleteSectionPermission(ctx context.Context, orgID, sectionID, id string) error
}

// Fixed answers of the section permissions.
var (
	errSectionPermissionTeam = &api.FieldError{Field: "team_id", Message: "team not found",
		Status: http.StatusUnprocessableEntity}
	errSectionPermissionLevel = &api.FieldError{Field: "level", Message: "level must be read or write",
		Status: http.StatusUnprocessableEntity}
	errSectionPermissionTaken = &api.ConflictError{Field: "team_id",
		Message: "the team already has a permission on this IPAM section"}
	// errSectionNotWritable: the caller sees the section but may not write
	// it (level read, 403).
	errSectionNotWritable = errors.New("the IPAM section may not be written")
)

const sectionPermissionCols = `p.id::text, p.organization_id::text, p.section_id::text, p.team_id::text,
	COALESCE((SELECT t.name FROM team t WHERE t.id = p.team_id AND t.organization_id = p.organization_id), ''),
	p.level, p.created_at`

func scanSectionPermission(s scanner) (*SectionPermission, error) {
	p := &SectionPermission{}
	if err := s.Scan(&p.ID, &p.OrganizationID, &p.SectionID, &p.TeamID, &p.TeamName, &p.Level, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.CreatedAt = p.CreatedAt.UTC()
	return p, nil
}

// sectionWritable checks, in the writing transaction under the caller's
// row level security, that the caller sees the section (ErrNotFound
// otherwise) and may write it (errSectionNotWritable for level read): the
// rights of a section are changed only by a principal that may write it.
func sectionWritable(ctx context.Context, tx pgx.Tx, orgID, sectionID string) error {
	if !api.IsUUID(sectionID) {
		return ErrNotFound
	}
	var writable bool
	err := tx.QueryRow(ctx, `SELECT NOT (id = ANY (ipam_section_denied('write'))) FROM ipam_section
		WHERE organization_id = $1 AND id = $2`, orgID, sectionID).Scan(&writable)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("read ipam section: %w", err)
	case !writable:
		return errSectionNotWritable
	}
	return nil
}

// sectionPermissionError maps the constraint violations of
// ipam_section_permission: a second entry of the team (409) and a team
// deleted meanwhile (422 team_id).
func sectionPermissionError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "ipam_section_permission_section_team_key":
		return errSectionPermissionTaken
	case pgErr.Code == "23503" && pgErr.ConstraintName == "ipam_section_permission_team_fkey":
		return errSectionPermissionTeam
	}
	return err
}

// ListSectionPermissions lists the entries of a section the caller sees
// (the team scope applies to team_id), oldest first.
func (r *PGRepository) ListSectionPermissions(ctx context.Context, orgID, sectionID string, page api.PaginationParams) ([]SectionPermission, int, error) {
	if !api.IsUUID(sectionID) {
		return nil, 0, ErrNotFound
	}
	out := []SectionPermission{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ipam_section WHERE organization_id = $1 AND id = $2),
			(SELECT count(*) FROM ipam_section_permission WHERE organization_id = $1 AND section_id = $2)`,
			orgID, sectionID).Scan(&visible, &total); err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, "SELECT "+sectionPermissionCols+` FROM ipam_section_permission p
			WHERE p.organization_id = $1 AND p.section_id = $2 ORDER BY p.created_at, p.id LIMIT $3 OFFSET $4`,
			orgID, sectionID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanSectionPermission(rows)
			if err != nil {
				return err
			}
			out = append(out, *p)
		}
		return rows.Err()
	})
	return out, total, err
}

// CreateSectionPermission stores an entry. The team must be one of the
// organization the caller sees (422 team_id otherwise); a principal
// restricted to clients or sites fails with the row level security error
// 42501 (403).
func (r *PGRepository) CreateSectionPermission(ctx context.Context, p *SectionPermission) error {
	return database.WithRequestTenant(ctx, r.pool, p.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := sectionWritable(ctx, tx, p.OrganizationID, p.SectionID); err != nil {
			return err
		}
		if !api.IsUUID(p.TeamID) {
			return errSectionPermissionTeam
		}
		var team bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM team WHERE organization_id = $1 AND id = $2)",
			p.OrganizationID, p.TeamID).Scan(&team); err != nil {
			return fmt.Errorf("read team: %w", err)
		}
		if !team {
			return errSectionPermissionTeam
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ipam_section_permission (organization_id, section_id, team_id, level)
			VALUES ($1, $2, $3, $4) RETURNING id::text`, p.OrganizationID, p.SectionID, p.TeamID, p.Level).Scan(&id); err != nil {
			return sectionPermissionError(err)
		}
		created, err := scanSectionPermission(tx.QueryRow(ctx, "SELECT "+sectionPermissionCols+
			" FROM ipam_section_permission p WHERE p.organization_id = $1 AND p.id = $2", p.OrganizationID, id))
		if err != nil {
			return err
		}
		*p = *created
		return nil
	})
}

// UpdateSectionPermission changes the level of an entry of the section.
func (r *PGRepository) UpdateSectionPermission(ctx context.Context, orgID, sectionID, id, level string) (*SectionPermission, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var p *SectionPermission
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := sectionWritable(ctx, tx, orgID, sectionID); err != nil {
			return err
		}
		var updated string
		err := tx.QueryRow(ctx, `UPDATE ipam_section_permission SET level = $4
			WHERE organization_id = $1 AND section_id = $2 AND id = $3 RETURNING id::text`, orgID, sectionID, id, level).Scan(&updated)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		p, err = scanSectionPermission(tx.QueryRow(ctx, "SELECT "+sectionPermissionCols+
			" FROM ipam_section_permission p WHERE p.organization_id = $1 AND p.id = $2", orgID, id))
		return err
	})
	return p, err
}

// DeleteSectionPermission removes an entry of the section; without entries
// the section stays restricted (acl_enabled, E-256) until PATCH opens it.
func (r *PGRepository) DeleteSectionPermission(ctx context.Context, orgID, sectionID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := sectionWritable(ctx, tx, orgID, sectionID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM ipam_section_permission WHERE organization_id = $1 AND section_id = $2 AND id = $3",
			orgID, sectionID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// sectionPermissionRepo returns the repository of the section rights, or
// answers 501 without a database.
func (h *Handler) sectionPermissionRepo(w http.ResponseWriter) (SectionPermissionRepository, bool) {
	repo, ok := h.repo.(SectionPermissionRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "IPAM section permissions are not available")
	}
	return repo, ok
}

// orgWideWriter answers 403 for a principal restricted to clients or sites:
// the rights of a section are written only by principals of the whole
// organization (E-09), as the policies of ipam_section_permission require.
func orgWideWriter(w http.ResponseWriter, r *http.Request) bool {
	if scope, ok := database.TenantScopeFromContext(r.Context()); ok && (!scope.Clients.IsAll() || !scope.Sites.IsAll()) {
		api.WriteError(w, http.StatusForbidden, "Forbidden",
			"IPAM section permissions are written only by principals without client and site restriction")
		return false
	}
	return true
}

// orgAdmin reports an org-wide administrator of the section rights
// (E-256): a principal with team:manage, the right that deletes the teams
// of the entries, without client, site and team restriction. It may open a
// section whose last team was deleted, also when it does not see it.
func orgAdmin(r *http.Request) bool {
	scope, ok := database.TenantScopeFromContext(r.Context())
	if !ok || !scope.Clients.IsAll() || !scope.Sites.IsAll() || !scope.Teams.IsAll() {
		return false
	}
	perms, ok := identity.PermissionsFromContext(r.Context())
	return ok && identity.HasPermission(perms, identity.PermTeamManage)
}

// writeSectionPermissionError answers a failed permission request: 404 for
// an unknown or invisible section or entry, 403 for a section the caller
// may only read, else the field rule or the repository error.
func writeSectionPermissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "IPAM section or permission not found")
	case errors.Is(err, errSectionNotWritable):
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the IPAM section may only be read")
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// ListSectionPermissions handles GET /api/v1/ipam-sections/{id}/permissions
func (h *Handler) ListSectionPermissions(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	repo, ok := h.sectionPermissionRepo(w)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := repo.ListSectionPermissions(r.Context(), orgID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeSectionPermissionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[SectionPermission]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateSectionPermission handles POST /api/v1/ipam-sections/{id}/permissions
func (h *Handler) CreateSectionPermission(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	repo, ok := h.sectionPermissionRepo(w)
	if !ok || !orgWideWriter(w, r) {
		return
	}
	var req CreateSectionPermissionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Level != SectionLevelRead && req.Level != SectionLevelWrite {
		api.WriteFieldError(w, errSectionPermissionLevel)
		return
	}
	p := &SectionPermission{OrganizationID: orgID, SectionID: chi.URLParam(r, "id"), TeamID: strings.TrimSpace(req.TeamID), Level: req.Level}
	if err := repo.CreateSectionPermission(r.Context(), p); err != nil {
		writeSectionPermissionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// UpdateSectionPermission handles
// PATCH /api/v1/ipam-sections/{id}/permissions/{permission_id}
func (h *Handler) UpdateSectionPermission(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	repo, ok := h.sectionPermissionRepo(w)
	if !ok || !orgWideWriter(w, r) {
		return
	}
	var req UpdateSectionPermissionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Level == nil || (*req.Level != SectionLevelRead && *req.Level != SectionLevelWrite) {
		api.WriteFieldError(w, errSectionPermissionLevel)
		return
	}
	p, err := repo.UpdateSectionPermission(r.Context(), orgID, chi.URLParam(r, "id"), chi.URLParam(r, "permission_id"), *req.Level)
	if err != nil {
		writeSectionPermissionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// DeleteSectionPermission handles
// DELETE /api/v1/ipam-sections/{id}/permissions/{permission_id}
func (h *Handler) DeleteSectionPermission(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	repo, ok := h.sectionPermissionRepo(w)
	if !ok || !orgWideWriter(w, r) {
		return
	}
	if err := repo.DeleteSectionPermission(r.Context(), orgID, chi.URLParam(r, "id"), chi.URLParam(r, "permission_id")); err != nil {
		writeSectionPermissionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
