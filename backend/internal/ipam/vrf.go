package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VRF is a routing domain of the organization (TEN-10, CH10). Every
// organization has exactly one default VRF "global"; subnets are unique per
// VRF. A VRF is org-wide: every principal of the organization reads it, only
// principals without client or site restriction create, change or delete it
// (E-09, restrictive policies of migration 000148).
//
// RouteDistinguisher, ImportTargets and ExportTargets follow RFC 4364
// (IPM-07): ASN16:nn32, IPv4:nn16 or ASN32:nn16; the route distinguisher
// is unique per organization. The default VRF is the global routing table
// and keeps them empty. SectionIDs are the IPAM sections of the VRF the
// caller sees.
type VRF struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id"`
	Name               string    `json:"name"`
	Description        string    `json:"description,omitempty"`
	IsDefault          bool      `json:"is_default"`
	RouteDistinguisher string    `json:"route_distinguisher,omitempty"`
	ImportTargets      []string  `json:"import_targets"`
	ExportTargets      []string  `json:"export_targets"`
	SectionIDs         []string  `json:"section_ids"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// CreateVRFRequest is the payload for creating a VRF.
type CreateVRFRequest struct {
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	RouteDistinguisher string   `json:"route_distinguisher,omitempty"`
	ImportTargets      []string `json:"import_targets,omitempty"`
	ExportTargets      []string `json:"export_targets,omitempty"`
	SectionIDs         []string `json:"section_ids,omitempty"`
}

// UpdateVRFRequest is the payload for updating a VRF. route_distinguisher
// "" clears it; the lists replace the stored ones (IPM-07).
type UpdateVRFRequest struct {
	Name               *string   `json:"name,omitempty"`
	Description        *string   `json:"description,omitempty"`
	RouteDistinguisher *string   `json:"route_distinguisher,omitempty"`
	ImportTargets      *[]string `json:"import_targets,omitempty"`
	ExportTargets      *[]string `json:"export_targets,omitempty"`
	SectionIDs         *[]string `json:"section_ids,omitempty"`
}

// VRFFilter holds the optional filters of GET /vrfs: SectionID lists the
// VRFs assigned to a section (IPM-07).
type VRFFilter struct {
	SectionID string
}

// Fixed answers of the VRF attribute rules (IPM-07).
var (
	errRouteDistinguisherTaken = &api.ConflictError{Field: "route_distinguisher",
		Message: "a VRF with this route distinguisher already exists"}
	errVRFSections = &api.FieldError{Field: "section_ids", Message: "IPAM section not found",
		Status: http.StatusUnprocessableEntity}
)

// errDefaultVRFRouting refuses route distinguisher and route targets on the
// default VRF, the global routing table; field names the refused attribute.
func errDefaultVRFRouting(field string) error {
	return &api.FieldError{Field: field, Message: "the default VRF is the global routing table and takes no " + field,
		Status: http.StatusUnprocessableEntity}
}

// routingField names the attribute a write sets on the default VRF: the
// first of route_distinguisher, import_targets and export_targets that is
// not empty.
func routingField(rd string, imports, exports []string) string {
	switch {
	case rd != "":
		return "route_distinguisher"
	case len(imports) > 0:
		return "import_targets"
	case len(exports) > 0:
		return "export_targets"
	}
	return "route_distinguisher"
}

// vrfError maps the constraint violations of the VRF attributes: a route
// distinguisher taken (409) and routing attributes on the default VRF
// (422, field of the request).
func vrfError(err error, field string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "vrf_route_distinguisher_key":
		return errRouteDistinguisherTaken
	case pgErr.Code == "23514" && pgErr.ConstraintName == "vrf_default_no_routing":
		return errDefaultVRFRouting(field)
	}
	return err
}

// --- PostgreSQL ---

// vrfCols reads a VRF with the sections the caller sees (row level security
// of ipam_section).
const vrfCols = `id::text, organization_id::text, name, COALESCE(description,''), is_default,
	COALESCE(route_distinguisher, ''), import_targets, export_targets,
	ARRAY(SELECT vs.section_id::text FROM vrf_section vs JOIN ipam_section s ON s.id = vs.section_id
		WHERE vs.vrf_id = vrf.id AND vs.organization_id = vrf.organization_id ORDER BY vs.section_id),
	created_at, updated_at`

func scanVRF(s scanner) (*VRF, error) {
	v := &VRF{}
	if err := s.Scan(&v.ID, &v.OrganizationID, &v.Name, &v.Description, &v.IsDefault, &v.RouteDistinguisher,
		&v.ImportTargets, &v.ExportTargets, &v.SectionIDs, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	normalizeVRFLists(v)
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, nil
}

// normalizeVRFLists answers empty lists as [] rather than null.
func normalizeVRFLists(v *VRF) {
	if v.ImportTargets == nil {
		v.ImportTargets = []string{}
	}
	if v.ExportTargets == nil {
		v.ExportTargets = []string{}
	}
	if v.SectionIDs == nil {
		v.SectionIDs = []string{}
	}
}

// replaceVRFSections makes sectionIDs the sections of the VRF, in the
// writing transaction: every section must be a section of the organization
// the caller sees (422 section_ids).
func replaceVRFSections(ctx context.Context, tx pgx.Tx, orgID, vrfID string, sectionIDs []string) error {
	for _, id := range sectionIDs {
		// No uuid is no section; checked before the ::uuid[] cast (22P02).
		if !api.IsUUID(id) {
			return errVRFSections
		}
	}
	var visible int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ipam_section WHERE organization_id = $1 AND id = ANY ($2::uuid[])`,
		orgID, sectionIDs).Scan(&visible); err != nil {
		return fmt.Errorf("read ipam sections: %w", err)
	}
	if visible != len(sectionIDs) {
		return errVRFSections
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vrf_section WHERE organization_id = $1 AND vrf_id = $2
		AND NOT (section_id = ANY ($3::uuid[]))`, orgID, vrfID, sectionIDs); err != nil {
		return fmt.Errorf("remove vrf sections: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO vrf_section (organization_id, vrf_id, section_id)
		SELECT $1, $2, s FROM unnest($3::uuid[]) AS s ON CONFLICT (vrf_id, section_id) DO NOTHING`,
		orgID, vrfID, sectionIDs); err != nil {
		return fmt.Errorf("add vrf sections: %w", err)
	}
	return nil
}

// ListVRFs lists the VRFs of the organization, the default first; with
// f.SectionID only those assigned to that section.
func (r *PGRepository) ListVRFs(ctx context.Context, orgID string, f VRFFilter, page api.PaginationParams) ([]VRF, int, error) {
	out := []VRF{}
	var total int
	if f.SectionID != "" && !api.IsUUID(f.SectionID) {
		return out, 0, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := "organization_id = $1", []any{orgID}
		if f.SectionID != "" {
			where += ` AND EXISTS (SELECT 1 FROM vrf_section vs JOIN ipam_section s ON s.id = vs.section_id
				WHERE vs.vrf_id = vrf.id AND vs.section_id = $2)`
			args = append(args, f.SectionID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM vrf WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		pos := len(args) + 1
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM vrf WHERE %s ORDER BY is_default DESC, name LIMIT $%d OFFSET $%d",
			vrfCols, where, pos, pos+1), append(args, page.Limit, page.Offset)...)
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

// CreateVRF stores a VRF with its attributes and sections; only the
// database creates the default VRF. For a principal restricted in the
// client or site scope the insert fails with the row level security error
// 42501 (403 at the API).
func (r *PGRepository) CreateVRF(ctx context.Context, v *VRF) error {
	normalizeVRFLists(v)
	return database.WithRequestTenant(ctx, r.pool, v.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO vrf (organization_id, name, description, route_distinguisher, import_targets, export_targets)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, v.OrganizationID, v.Name, nilIfEmpty(v.Description),
			nilIfEmpty(v.RouteDistinguisher), v.ImportTargets, v.ExportTargets).Scan(&id); err != nil {
			return vrfError(err, routingField(v.RouteDistinguisher, v.ImportTargets, v.ExportTargets))
		}
		if len(v.SectionIDs) > 0 {
			if err := replaceVRFSections(ctx, tx, v.OrganizationID, id, v.SectionIDs); err != nil {
				return err
			}
		}
		created, err := scanVRF(tx.QueryRow(ctx, "SELECT "+vrfCols+" FROM vrf WHERE organization_id = $1 AND id = $2", v.OrganizationID, id))
		if err != nil {
			return err
		}
		*v = *created
		return nil
	})
}

// UpdateVRF changes name, description and the routing attributes of a VRF
// and replaces its sections when req.SectionIDs is set. The VRF row is
// always updated first, so a principal restricted in the client or site
// scope changes nothing: ErrNotFound. Routing attributes on the default VRF
// are refused (vrf_default_no_routing, 422).
func (r *PGRepository) UpdateVRF(ctx context.Context, orgID, id string, req UpdateVRFRequest) (*VRF, error) {
	var imports, exports any
	var importList, exportList []string
	if req.ImportTargets != nil {
		imports, importList = *req.ImportTargets, *req.ImportTargets
	}
	if req.ExportTargets != nil {
		exports, exportList = *req.ExportTargets, *req.ExportTargets
	}
	rd := ""
	if req.RouteDistinguisher != nil {
		rd = *req.RouteDistinguisher
	}
	var v *VRF
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE vrf SET name = COALESCE($3, name),
			description = CASE WHEN $4::text IS NULL THEN description ELSE NULLIF($4, '') END,
			route_distinguisher = CASE WHEN $5::text IS NULL THEN route_distinguisher ELSE NULLIF($5, '') END,
			import_targets = COALESCE($6::text[], import_targets), export_targets = COALESCE($7::text[], export_targets),
			updated_at = now()
			WHERE organization_id = $1 AND id = $2`, orgID, id, req.Name, req.Description, req.RouteDistinguisher, imports, exports)
		if err != nil {
			return vrfError(err, routingField(rd, importList, exportList))
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if req.SectionIDs != nil {
			if sectionErr := replaceVRFSections(ctx, tx, orgID, id, *req.SectionIDs); sectionErr != nil {
				return sectionErr
			}
		}
		v, err = scanVRF(tx.QueryRow(ctx, "SELECT "+vrfCols+" FROM vrf WHERE organization_id = $1 AND id = $2", orgID, id))
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
	normalizeVRFLists(v)
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

// checkVRFAttributes applies the rules of IPM-07 to the VRF next: a route
// distinguisher unique per organization, no routing attributes on the
// default VRF and sections of the organization.
func (r *MemoryRepository) checkVRFAttributes(next *VRF) error {
	if next.IsDefault && (next.RouteDistinguisher != "" || len(next.ImportTargets) > 0 || len(next.ExportTargets) > 0) {
		return errDefaultVRFRouting(routingField(next.RouteDistinguisher, next.ImportTargets, next.ExportTargets))
	}
	if next.RouteDistinguisher != "" {
		for _, e := range r.vrfs {
			if e.ID != next.ID && e.OrganizationID == next.OrganizationID && e.RouteDistinguisher == next.RouteDistinguisher {
				return errRouteDistinguisherTaken
			}
		}
	}
	for _, id := range next.SectionIDs {
		if s, ok := r.sections[id]; !ok || s.OrganizationID != next.OrganizationID {
			return errVRFSections
		}
	}
	return nil
}

// vrfCopy is a copy of v with its own slices.
func vrfCopy(v *VRF) VRF {
	cp := *v
	cp.ImportTargets, cp.ExportTargets = slices.Clone(v.ImportTargets), slices.Clone(v.ExportTargets)
	cp.SectionIDs = slices.Clone(v.SectionIDs)
	normalizeVRFLists(&cp)
	return cp
}

// ListVRFs implements Repository.
func (r *MemoryRepository) ListVRFs(_ context.Context, orgID string, f VRFFilter, page api.PaginationParams) ([]VRF, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultVRF(orgID)
	out := []VRF{}
	for _, v := range r.vrfs {
		if v.OrganizationID == orgID && (f.SectionID == "" || slices.Contains(v.SectionIDs, f.SectionID)) {
			out = append(out, vrfCopy(v))
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
	cp := vrfCopy(v)
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
	v.ID, v.IsDefault = "", false
	if err := r.checkVRFAttributes(v); err != nil {
		return err
	}
	r.seq++
	now := time.Now().UTC()
	v.ID, v.CreatedAt, v.UpdatedAt = fmt.Sprintf("vrf-%d", r.seq), now, now
	stored := vrfCopy(v)
	r.vrfs[v.ID] = &stored
	*v = vrfCopy(&stored)
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
	next := vrfCopy(v)
	if req.Name != nil {
		if r.vrfNameTaken(orgID, *req.Name, id) {
			return nil, fmt.Errorf("%w: vrf %q already exists", ErrConflict, *req.Name)
		}
		next.Name = *req.Name
	}
	if req.Description != nil {
		next.Description = *req.Description
	}
	if req.RouteDistinguisher != nil {
		next.RouteDistinguisher = *req.RouteDistinguisher
	}
	if req.ImportTargets != nil {
		next.ImportTargets = slices.Clone(*req.ImportTargets)
	}
	if req.ExportTargets != nil {
		next.ExportTargets = slices.Clone(*req.ExportTargets)
	}
	if req.SectionIDs != nil {
		next.SectionIDs = slices.Clone(*req.SectionIDs)
	}
	if err := r.checkVRFAttributes(&next); err != nil {
		return nil, err
	}
	next.UpdatedAt = time.Now().UTC()
	*v = next
	cp := vrfCopy(v)
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

// vrfIDsOfSection lists the VRFs assigned to the section (memory, IPM-07).
func (r *MemoryRepository) vrfIDsOfSection(sectionID string) []string {
	out := []string{}
	for _, v := range r.vrfs {
		if slices.Contains(v.SectionIDs, sectionID) {
			out = append(out, v.ID)
		}
	}
	sort.Strings(out)
	return out
}
