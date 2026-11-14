package security

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The product mapping in the API (SEC-02, WP-1122, E-236 option 2): the
// rows of cpe_mapping of the organization are kept over
// /api/v1/security/cpe-mappings (read finding:read, write finding:write and
// only org-wide, since a mapping applies to every CI of the organization),
// GET /api/v1/security/vuln-products suggests vendor and product from the
// catalog and GET /api/v1/security/unmapped lists the CIs without product
// mapping, grouped by manufacturer, model and sys_object_id. A write queues
// vulnfeed.match for the CIs its pattern (before and after) applies to.

// Limits of the product mapping API.
const (
	maxMappingProducts   = 20
	maxMappingsListed    = 1000
	maxProductSuggestion = 50
	// maxMatchIDs: a write that applies to more CIs queues a full run.
	maxMatchIDs = 10000
	// maxUnmappedGroups bounds the groups the list without mapping reads;
	// beyond it the list is marked total_capped.
	maxUnmappedGroups = 10000
	maxPriority       = 100000
)

// ErrMappingNotFound: an unknown product mapping or one of another
// organization (404).
var ErrMappingNotFound = errors.New("product mapping not found")

// errMappingsUnavailable: the product mapping needs the database (503).
var errMappingsUnavailable = errors.New("the product mapping needs the database")

// sysObjectIDPattern is a dotted numeric OID prefix.
var sysObjectIDPattern = regexp.MustCompile(`^\.?\d+(\.\d+)*$`)

// MappingRecord is a stored product mapping with its times.
type MappingRecord struct {
	ProductMapping
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// MatchJobID is the job vulnfeed.match the write queued (AK4); empty
	// in a read or without a job service.
	MatchJobID string `json:"match_job_id,omitempty"`
}

// MappingDeleted is the answer of a DELETE that queued a match (AK4).
type MappingDeleted struct {
	MatchJobID string `json:"match_job_id"`
}

// MappingRequest is the body of POST and PATCH; PATCH changes the fields it
// names.
type MappingRequest struct {
	MatchKind           *string   `json:"match_kind,omitempty"`
	ManufacturerPattern *string   `json:"manufacturer_pattern,omitempty"`
	ModelPattern        *string   `json:"model_pattern,omitempty"`
	SysObjectIDPrefix   *string   `json:"sys_object_id_prefix,omitempty"`
	Vendor              *string   `json:"vendor,omitempty"`
	Products            *[]string `json:"products,omitempty"`
	VersionField        *string   `json:"version_field,omitempty"`
	VersionScheme       *string   `json:"version_scheme,omitempty"`
	Priority            *int      `json:"priority,omitempty"`
}

// apply sets the fields req names on p.
func (req *MappingRequest) apply(p *ProductMapping) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&p.MatchKind, req.MatchKind)
	set(&p.ManufacturerPattern, req.ManufacturerPattern)
	set(&p.ModelPattern, req.ModelPattern)
	set(&p.SysObjectIDPrefix, req.SysObjectIDPrefix)
	set(&p.Vendor, req.Vendor)
	set(&p.VersionField, req.VersionField)
	set(&p.VersionScheme, req.VersionScheme)
	if req.Products != nil {
		p.Products = slices.Clone(*req.Products)
	}
	if req.Priority != nil {
		p.Priority = *req.Priority
	}
}

func mappingFieldError(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// validateMapping checks and normalizes a product mapping (WP-1121 AK1):
// the pattern of its match_kind is set and the other one empty, the globs
// are valid, vendor and products are set (normalized like the catalog),
// version field and scheme come from their lists. A violation is 422 on the
// field.
// validation: fixed texts naming the refused field of the mapping.
func validateMapping(p *ProductMapping) error {
	switch p.MatchKind {
	case MatchManufacturerModel:
		if p.ManufacturerPattern == "" {
			return mappingFieldError("manufacturer_pattern", "is required with match_kind manufacturer_model")
		}
		for field, pattern := range map[string]string{"manufacturer_pattern": p.ManufacturerPattern, "model_pattern": p.ModelPattern} {
			if _, err := path.Match(strings.ToLower(pattern), ""); err != nil {
				return mappingFieldError(field, "is not a valid pattern (use * and ?)")
			}
		}
		if p.SysObjectIDPrefix != "" {
			return mappingFieldError("sys_object_id_prefix", "is only allowed with match_kind sys_object_id")
		}
	case MatchSysObjectID:
		if p.SysObjectIDPrefix == "" {
			return mappingFieldError("sys_object_id_prefix", "is required with match_kind sys_object_id")
		}
		if !sysObjectIDPattern.MatchString(p.SysObjectIDPrefix) {
			return mappingFieldError("sys_object_id_prefix", "must be a dotted numeric OID such as 1.3.6.1.4.1.9")
		}
		p.SysObjectIDPrefix = strings.TrimPrefix(p.SysObjectIDPrefix, ".")
		if p.ManufacturerPattern != "" || p.ModelPattern != "" {
			return mappingFieldError("manufacturer_pattern", "is only allowed with match_kind manufacturer_model")
		}
	default:
		return mappingFieldError("match_kind", "must be manufacturer_model or sys_object_id")
	}
	p.Vendor = vulnfeed.Normalize(p.Vendor)
	if p.Vendor == "" {
		return mappingFieldError("vendor", "is required")
	}
	if len(p.Products) == 0 {
		return mappingFieldError("products", "must name at least one product")
	}
	if len(p.Products) > maxMappingProducts {
		return mappingFieldError("products", "must name at most "+strconv.Itoa(maxMappingProducts)+" products")
	}
	products := make([]string, 0, len(p.Products))
	for _, prod := range p.Products {
		n := vulnfeed.Normalize(prod)
		if n == "" {
			return mappingFieldError("products", "must not contain an empty product")
		}
		if !slices.Contains(products, n) {
			products = append(products, n)
		}
	}
	p.Products = products
	if p.VersionField == "" {
		p.VersionField = VersionFieldFirmware
	}
	if p.VersionField != VersionFieldFirmware && p.VersionField != VersionFieldOS {
		return mappingFieldError("version_field", "must be firmware_version or os_version")
	}
	if p.VersionScheme == "" {
		p.VersionScheme = SchemeGeneric
	}
	if !slices.Contains(VersionSchemes, p.VersionScheme) {
		return mappingFieldError("version_scheme", "must be one of "+strings.Join(VersionSchemes, ", "))
	}
	if p.Priority < 0 || p.Priority > maxPriority {
		return mappingFieldError("priority", "must be between 0 and "+strconv.Itoa(maxPriority))
	}
	return nil
}

// UnmappedGroup is a group of CIs without product mapping (AK3).
type UnmappedGroup struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	SysObjectID  string `json:"sys_object_id"`
	CICount      int    `json:"ci_count"`
	ExampleCIID  string `json:"example_ci_id"`
}

// VulnProduct is a vendor and a product of the catalog (AK2).
type VulnProduct struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
}

// ProductSearcher searches the products of the catalog
// (vulnfeed.PGRepository).
type ProductSearcher interface {
	SearchProducts(ctx context.Context, q string, limit int) ([]vulnfeed.Product, error)
}

// MatchSubmitter queues a version match (Matcher).
type MatchSubmitter interface {
	Submit(ctx context.Context, orgID string, ciIDs []string, key string) (*jobs.Job, error)
}

// MappingStore keeps the product mappings in cpe_mapping (PostgreSQL).
type MappingStore struct {
	pool *pgxpool.Pool
}

// NewMappingStore returns the store on the application pool.
func NewMappingStore(pool *pgxpool.Pool) *MappingStore { return &MappingStore{pool: pool} }

const mappingCols = `id::text, match_kind, COALESCE(manufacturer_pattern, ''), COALESCE(model_pattern, ''), COALESCE(sys_object_id_prefix, ''),
	vendor, products, version_field, version_scheme, priority, COALESCE(created_by::text, ''), created_at, updated_at`

func scanMapping(row pgx.Row) (*MappingRecord, error) {
	var m MappingRecord
	err := row.Scan(&m.ID, &m.MatchKind, &m.ManufacturerPattern, &m.ModelPattern, &m.SysObjectIDPrefix, &m.Vendor, &m.Products,
		&m.VersionField, &m.VersionScheme, &m.Priority, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMappingNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// List returns the mappings of the organization by priority (at most
// maxMappingsListed; capped reports more).
func (s *MappingStore) List(ctx context.Context, orgID string) (out []MappingRecord, capped bool, err error) {
	out, err = s.list(ctx, orgID, maxMappingsListed+1)
	if len(out) > maxMappingsListed {
		return out[:maxMappingsListed], true, err
	}
	return out, false, err
}

// list reads the mappings of the organization in the order of the matcher
// (priority, created_at, id); limit 0 reads all of them.
func (s *MappingStore) list(ctx context.Context, orgID string, limit int) (out []MappingRecord, err error) {
	out = []MappingRecord{}
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, "SELECT "+mappingCols+` FROM cpe_mapping WHERE organization_id = $1
			ORDER BY priority, created_at, id LIMIT NULLIF($2, 0)`, orgID, limit)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			m, serr := scanMapping(rows)
			if serr != nil {
				return serr
			}
			out = append(out, *m)
		}
		return rows.Err()
	})
	return out, err
}

// Get returns one mapping of the organization.
func (s *MappingStore) Get(ctx context.Context, orgID, id string) (*MappingRecord, error) {
	var m *MappingRecord
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var gerr error
		m, gerr = scanMapping(tx.QueryRow(ctx, "SELECT "+mappingCols+" FROM cpe_mapping WHERE organization_id = $1 AND id = $2", orgID, id))
		return gerr
	})
	return m, err
}

// Create stores a validated mapping.
func (s *MappingStore) Create(ctx context.Context, orgID string, p *ProductMapping, createdBy string) (*MappingRecord, error) {
	var m *MappingRecord
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var cerr error
		m, cerr = scanMapping(tx.QueryRow(ctx, `INSERT INTO cpe_mapping (organization_id, match_kind, manufacturer_pattern, model_pattern,
				sys_object_id_prefix, vendor, products, version_field, version_scheme, priority, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid) RETURNING `+mappingCols,
			orgID, p.MatchKind, nullable(p.ManufacturerPattern), nullable(p.ModelPattern), nullable(p.SysObjectIDPrefix), p.Vendor, p.Products,
			p.VersionField, p.VersionScheme, p.Priority, nullable(uuidOrEmpty(createdBy))))
		return cerr
	})
	return m, err
}

// Update replaces the fields of a mapping with the validated p; old is the
// mapping before.
func (s *MappingStore) Update(ctx context.Context, orgID, id string, change func(p *ProductMapping) error) (old, updated *MappingRecord, err error) {
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var gerr error
		if old, gerr = scanMapping(tx.QueryRow(ctx, "SELECT "+mappingCols+" FROM cpe_mapping WHERE organization_id = $1 AND id = $2 FOR UPDATE",
			orgID, id)); gerr != nil {
			return gerr
		}
		p := old.ProductMapping
		p.Products = slices.Clone(old.Products)
		if cerr := change(&p); cerr != nil {
			return cerr
		}
		updated, gerr = scanMapping(tx.QueryRow(ctx, `UPDATE cpe_mapping SET match_kind = $3, manufacturer_pattern = $4, model_pattern = $5,
				sys_object_id_prefix = $6, vendor = $7, products = $8, version_field = $9, version_scheme = $10, priority = $11, updated_at = now()
			WHERE organization_id = $1 AND id = $2 RETURNING `+mappingCols,
			orgID, id, p.MatchKind, nullable(p.ManufacturerPattern), nullable(p.ModelPattern), nullable(p.SysObjectIDPrefix), p.Vendor, p.Products,
			p.VersionField, p.VersionScheme, p.Priority))
		return gerr
	})
	return old, updated, err
}

// Delete removes a mapping and returns it.
func (s *MappingStore) Delete(ctx context.Context, orgID, id string) (*MappingRecord, error) {
	var m *MappingRecord
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var derr error
		m, derr = scanMapping(tx.QueryRow(ctx, "DELETE FROM cpe_mapping WHERE organization_id = $1 AND id = $2 RETURNING "+mappingCols, orgID, id))
		return derr
	})
	return m, err
}

// Unmapped returns the groups of CIs visible in ctx (row level security:
// client and site scope of the caller) with a manufacturer or a
// sys_object_id that neither a mapping of the organization nor the system
// mapping applies to (AK3), ordered by manufacturer, model and
// sys_object_id. capped reports more than maxUnmappedGroups groups read.
func (s *MappingStore) Unmapped(ctx context.Context, orgID string) (groups []UnmappedGroup, capped bool, err error) {
	// Every mapping, as the matcher reads them (no cap of the list).
	org, err := s.list(ctx, orgID, 0)
	if err != nil {
		return nil, false, err
	}
	mappings := make([]ProductMapping, len(org))
	for i := range org {
		mappings[i] = org[i].ProductMapping
	}
	system := SystemMappings()
	groups = []UnmappedGroup{}
	read := 0
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `SELECT COALESCE(manufacturer, ''), COALESCE(model, ''), COALESCE(sys_object_id, ''), count(*), min(id::text)
			FROM ci WHERE organization_id = $1 AND deleted_at IS NULL
			  AND (btrim(COALESCE(manufacturer, '')) <> '' OR btrim(COALESCE(sys_object_id, '')) <> '')
			GROUP BY 1, 2, 3 ORDER BY 1, 2, 3 LIMIT $2`, orgID, maxUnmappedGroups+1)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var g UnmappedGroup
			if serr := rows.Scan(&g.Manufacturer, &g.Model, &g.SysObjectID, &g.CICount, &g.ExampleCIID); serr != nil {
				return serr
			}
			read++
			if read > maxUnmappedGroups {
				capped = true
				break
			}
			if ResolveMapping(mappings, system, &CIProduct{Manufacturer: g.Manufacturer, Model: g.Model, SysObjectID: g.SysObjectID}) != nil {
				continue
			}
			groups = append(groups, g)
		}
		return rows.Err()
	})
	return groups, capped, err
}

// matchingCIs returns the ids of the CIs of the organization that one of
// mappings applies to; full is true when they are more than maxMatchIDs.
func (s *MappingStore) matchingCIs(ctx context.Context, orgID string, mappings ...*ProductMapping) (ids []string, full bool, err error) {
	seen := map[string]bool{}
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		for _, m := range mappings {
			if m == nil {
				continue
			}
			var rows pgx.Rows
			var qerr error
			if m.MatchKind == MatchSysObjectID {
				prefix := strings.TrimPrefix(m.SysObjectIDPrefix, ".")
				rows, qerr = tx.Query(ctx, `SELECT id::text, COALESCE(manufacturer, ''), COALESCE(model, ''), COALESCE(sys_object_id, '')
					FROM ci WHERE organization_id = $1 AND deleted_at IS NULL
					  AND (ltrim(btrim(sys_object_id), '.') = $2 OR ltrim(btrim(sys_object_id), '.') LIKE $3)`,
					orgID, prefix, likeEscape(prefix)+".%")
			} else {
				rows, qerr = tx.Query(ctx, `SELECT id::text, COALESCE(manufacturer, ''), COALESCE(model, ''), COALESCE(sys_object_id, '')
					FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND btrim(COALESCE(manufacturer, '')) <> ''`, orgID)
			}
			if qerr != nil {
				return qerr
			}
			for rows.Next() {
				var c CIProduct
				if serr := rows.Scan(&c.ID, &c.Manufacturer, &c.Model, &c.SysObjectID); serr != nil {
					rows.Close()
					return serr
				}
				if seen[c.ID] || !m.Matches(&c) {
					continue
				}
				seen[c.ID] = true
				if len(seen) > maxMatchIDs {
					full = true
					rows.Close()
					return nil
				}
				ids = append(ids, c.ID)
			}
			rows.Close()
			if rerr := rows.Err(); rerr != nil {
				return rerr
			}
		}
		return nil
	})
	if full {
		return nil, true, err
	}
	slices.Sort(ids)
	return ids, false, err
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func uuidOrEmpty(id string) string {
	if api.IsUUID(id) {
		return id
	}
	return ""
}

// ─── handler ──────────────────────────────────────────────────────────────────

// WithMappings wires the product mapping routes (WP-1122): the store, the
// product search of the catalog and the version match the writes queue;
// without a store (--no-db) the routes answer 503.
func (h *Handler) WithMappings(store *MappingStore, products ProductSearcher, matcher MatchSubmitter) *Handler {
	h.mappings, h.products, h.matcher = store, products, matcher
	return h
}

func (h *Handler) registerMappingRoutes(r chi.Router) {
	r.Get("/api/v1/security/cpe-mappings", h.ListMappings)
	r.Post("/api/v1/security/cpe-mappings", h.CreateMapping)
	r.Get("/api/v1/security/cpe-mappings/{id}", h.GetMapping)
	r.Patch("/api/v1/security/cpe-mappings/{id}", h.UpdateMapping)
	r.Delete("/api/v1/security/cpe-mappings/{id}", h.DeleteMapping)
	r.Get("/api/v1/security/vuln-products", h.VulnProducts)
	r.Get("/api/v1/security/unmapped", h.Unmapped)
}

// mappingContext returns the organization of the request and whether the
// store exists; it answers 401 or 503 otherwise.
func (h *Handler) mappingContext(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	if h.mappings == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", errMappingsUnavailable.Error())
		return "", false
	}
	return t.OrganizationID, true
}

// orgWideWriter answers 403 unless the caller holds finding:write for the
// whole organization: a mapping applies to every CI of it (AK1).
func orgWideWriter(w http.ResponseWriter, r *http.Request) bool {
	if identity.HoldsOrgWide(r.Context(), identity.PermFindingWrite) {
		return true
	}
	api.WriteError(w, http.StatusForbidden, "Forbidden", "product mappings are written with finding:write for the whole organization")
	return false
}

func writeMappingError(w http.ResponseWriter, err error) {
	var fe *api.FieldError
	switch {
	case errors.Is(err, ErrMappingNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrMappingNotFound.Error())
	case errors.As(err, &fe):
		api.WriteFieldError(w, fe)
	default:
		api.WriteRepoError(w, err)
	}
}

// ListMappings handles GET /api/v1/security/cpe-mappings: the mappings of
// the organization by priority, offset-paged.
func (h *Handler) ListMappings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.mappingContext(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, capped, err := h.mappings.List(r.Context(), orgID)
	if err != nil {
		writeMappingError(w, err)
		return
	}
	resp := api.Page(items, page)
	resp.TotalCapped = capped
	api.WriteJSON(w, http.StatusOK, resp)
}

// GetMapping handles GET /api/v1/security/cpe-mappings/{id}.
func (h *Handler) GetMapping(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.mappingContext(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrMappingNotFound.Error())
	if !ok {
		return
	}
	m, err := h.mappings.Get(r.Context(), orgID, id)
	if err != nil {
		writeMappingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

// CreateMapping handles POST /api/v1/security/cpe-mappings (201): the
// mapping with the match job of the CIs it applies to.
func (h *Handler) CreateMapping(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.mappingContext(w, r)
	if !ok || !orgWideWriter(w, r) {
		return
	}
	var req MappingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	p := ProductMapping{Priority: 100}
	req.apply(&p)
	if err := validateMapping(&p); err != nil {
		writeMappingError(w, err)
		return
	}
	m, err := h.mappings.Create(r.Context(), orgID, &p, tenant.FromContext(r.Context()).UserID)
	if err != nil {
		writeMappingError(w, err)
		return
	}
	m.MatchJobID = h.queueMatch(r.Context(), orgID, m, &m.ProductMapping)
	api.WriteJSON(w, http.StatusCreated, m)
}

// UpdateMapping handles PATCH /api/v1/security/cpe-mappings/{id}: the
// fields named change, the whole mapping is validated again.
func (h *Handler) UpdateMapping(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.mappingContext(w, r)
	if !ok || !orgWideWriter(w, r) {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrMappingNotFound.Error())
	if !ok {
		return
	}
	var req MappingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	old, m, err := h.mappings.Update(r.Context(), orgID, id, func(p *ProductMapping) error {
		req.apply(p)
		// A kind change drops the pattern of the other kind unless the
		// request sets it.
		if req.MatchKind != nil {
			if p.MatchKind == MatchSysObjectID && req.ManufacturerPattern == nil && req.ModelPattern == nil {
				p.ManufacturerPattern, p.ModelPattern = "", ""
			}
			if p.MatchKind == MatchManufacturerModel && req.SysObjectIDPrefix == nil {
				p.SysObjectIDPrefix = ""
			}
		}
		return validateMapping(p)
	})
	if err != nil {
		writeMappingError(w, err)
		return
	}
	m.MatchJobID = h.queueMatch(r.Context(), orgID, m, &old.ProductMapping, &m.ProductMapping)
	api.WriteJSON(w, http.StatusOK, m)
}

// DeleteMapping handles DELETE /api/v1/security/cpe-mappings/{id}: 200
// with match_job_id (and the job in Location) when the deletion queued the
// match of the CIs the mapping applied to (AK4), 204 without a job.
func (h *Handler) DeleteMapping(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.mappingContext(w, r)
	if !ok || !orgWideWriter(w, r) {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrMappingNotFound.Error())
	if !ok {
		return
	}
	old, err := h.mappings.Delete(r.Context(), orgID, id)
	if err != nil {
		writeMappingError(w, err)
		return
	}
	job := h.queueMatch(r.Context(), orgID, old, &old.ProductMapping)
	if job == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job)
	api.WriteJSON(w, http.StatusOK, MappingDeleted{MatchJobID: job})
}

// queueMatch queues vulnfeed.match for the CIs the mappings apply to (AK4):
// their ids (at most maxMatchIDs, else a full run); the key names the
// mapping and the time of its write. No CI queues nothing. A failure is
// logged: the mapping is stored, the next scheduled run matches.
func (h *Handler) queueMatch(ctx context.Context, orgID string, m *MappingRecord, mappings ...*ProductMapping) string {
	if h.matcher == nil {
		return ""
	}
	ids, full, err := h.mappings.matchingCIs(ctx, orgID, mappings...)
	if err != nil {
		slog.WarnContext(ctx, "product mapping: CIs of the match not read", "mapping_id", m.ID, "error", err)
		return ""
	}
	if !full && len(ids) == 0 {
		return ""
	}
	key := fmt.Sprintf("vulnfeed.match:mapping:%s:%d", m.ID, time.Now().UnixNano())
	job, err := h.matcher.Submit(ctx, orgID, ids, key)
	if err != nil || job == nil {
		slog.WarnContext(ctx, "product mapping: version match not queued", "mapping_id", m.ID, "error", err)
		return ""
	}
	return job.ID
}

// VulnProducts handles GET /api/v1/security/vuln-products?q=: at most 50
// pairs of vendor and product of the catalog whose name contains q (AK2).
func (h *Handler) VulnProducts(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.mappingContext(w, r); !ok {
		return
	}
	if h.products == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", errMappingsUnavailable.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 100 {
		api.WriteFieldError(w, mappingFieldError("q", "must be at most 100 characters"))
		return
	}
	found, err := h.products.SearchProducts(r.Context(), q, maxProductSuggestion)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	out := make([]VulnProduct, 0, len(found))
	for _, p := range found {
		out = append(out, VulnProduct{Vendor: p.Vendor, Product: p.Product})
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// Unmapped handles GET /api/v1/security/unmapped: the groups of CIs
// without product mapping in the caller's scope (AK3), read in full and
// paged by next_cursor (signed offset cursor, API-02) or offset.
func (h *Handler) Unmapped(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.mappingContext(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	groups, capped, err := h.mappings.Unmapped(r.Context(), orgID)
	if err != nil {
		writeMappingError(w, err)
		return
	}
	resp := api.Page(groups, page)
	resp.TotalCapped = capped
	api.WriteJSON(w, http.StatusOK, resp)
}
