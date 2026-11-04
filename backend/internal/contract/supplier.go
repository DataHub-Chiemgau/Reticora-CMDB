// Package contract holds the suppliers and contracts of an organization
// (MGT-12, API-06, E-41). Suppliers come first (WP-736); contracts follow
// in WP-269 and reference them.
package contract

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Supplier is the master record of a supplier (MGT-12, E-41). name is the
// company name and no personal data; the contact person is only referenced
// by contact_id, whose name, phone and e-mail the contacts resource reveals
// from the PII vault (WP-242). client_id nil is an org-wide supplier.
type Supplier struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       *string   `json:"client_id"`
	Name           string    `json:"name"`
	ExternalRef    *string   `json:"external_ref"`
	ContactID      *string   `json:"contact_id"`
	Notes          *string   `json:"notes"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateSupplierRequest is the body of POST /api/v1/suppliers.
type CreateSupplierRequest struct {
	ClientID    *string `json:"client_id"`
	Name        string  `json:"name"`
	ExternalRef *string `json:"external_ref"`
	ContactID   *string `json:"contact_id"`
	Notes       *string `json:"notes"`
}

// Optional is a nullable field of a PATCH body: Set records that it was
// sent, Value nil that it was sent as null.
type Optional struct {
	Set   bool
	Value *string
}

// UnmarshalJSON records that the field was sent; null clears the value.
func (o *Optional) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// UpdateSupplierRequest is the body of PATCH /api/v1/suppliers/{id}: sent
// fields change, null clears an optional field. client_id cannot change.
type UpdateSupplierRequest struct {
	ClientID    Optional `json:"client_id"`
	Name        *string  `json:"name"`
	ExternalRef Optional `json:"external_ref"`
	ContactID   Optional `json:"contact_id"`
	Notes       Optional `json:"notes"`
	// IfMatch is the version of the If-Match header (api.IfMatchAny for
	// "*"); nil without the header.
	IfMatch *int64 `json:"-"`
}

// apply merges the sent fields into s.
func (req *UpdateSupplierRequest) apply(s *Supplier) {
	if req.Name != nil {
		s.Name = *req.Name
	}
	if req.ExternalRef.Set {
		s.ExternalRef = req.ExternalRef.Value
	}
	if req.ContactID.Set {
		s.ContactID = req.ContactID.Value
	}
	if req.Notes.Set {
		s.Notes = req.Notes.Value
	}
}

// Errors of the supplier repositories; the handler maps them onto their
// status.
var (
	// ErrSupplierNotFound: unknown, of another organization or outside
	// the caller's client scope (404).
	ErrSupplierNotFound = errors.New("supplier not found")
	// ErrPreconditionRequired: the organization requires If-Match
	// (settings.require_if_match) and the request sent none (412).
	ErrPreconditionRequired = errors.New("If-Match is required by the organization")
	// ErrPreconditionFailed: If-Match names another version than the
	// current one (412).
	ErrPreconditionFailed = errors.New("the supplier has changed since the If-Match version")
)

// errNameTaken is the 409 of a name another supplier of the organization
// holds (UNIQUE (organization_id, name)).
var errNameTaken = &api.ConflictError{Field: "name", Message: "another supplier of the organization has this name"}

// errContact is the 422 of a contact_id that names no contact visible
// under the caller's scope, e.g. one of another organization.
var errContact = &api.FieldError{Field: "contact_id", Message: "names no contact of the organization", Status: http.StatusUnprocessableEntity}

// errClient is the 422 of a client_id that names no client of the
// organization.
var errClient = &api.FieldError{Field: "client_id", Message: "names no client of the organization", Status: http.StatusUnprocessableEntity}

// validate checks the fields of a supplier before it is stored.
func validate(s *Supplier) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return &api.FieldError{Field: "name", Message: "must not be empty"}
	}
	for _, f := range []struct {
		field string
		value *string
	}{{"client_id", s.ClientID}, {"contact_id", s.ContactID}} {
		if f.value != nil && !api.IsUUID(*f.value) {
			return &api.FieldError{Field: f.field, Message: "must be a UUID"}
		}
	}
	for _, f := range []struct {
		field string
		value *string
	}{{"name", &s.Name}, {"external_ref", s.ExternalRef}, {"notes", s.Notes}} {
		if f.value == nil {
			continue
		}
		if err := api.RejectNUL(f.field, *f.value); err != nil {
			return err
		}
	}
	return nil
}

// checkIfMatch applies If-Match (API-07): without it the request passes
// unless the organization requires it; "*" matches any version, another
// version than the current one fails.
func checkIfMatch(ifMatch *int64, required bool, current int64) error {
	switch {
	case ifMatch == nil && required:
		return ErrPreconditionRequired
	case ifMatch == nil, *ifMatch == api.IfMatchAny, *ifMatch == current:
		return nil
	}
	return ErrPreconditionFailed
}

// SupplierFields are the filters and sort fields of the supplier list
// (API-02); the first sort field is the default.
var SupplierFields = api.ListFields{
	Filterable: map[string]string{"name": api.KindText, "client_id": api.KindUUID, "external_ref": api.KindText},
	Sortable:   []string{"name", "created_at"},
}

// supplierOrder resolves the sort of a list query: name ascending by
// default.
func supplierOrder(q *api.ListQuery) (field, dir string) {
	field, dir = q.SortBy, q.SortDir
	if field == "" {
		field = SupplierFields.Sortable[0]
	}
	if dir == "" {
		dir = "asc"
	}
	return field, dir
}

// sortValue is the cursor value of s for the sort field: the name, or the
// creation time with fixed nanosecond digits, so the texts sort like the
// times.
func sortValue(s *Supplier, field string) string {
	if field == "created_at" {
		return s.CreatedAt.UTC().Format(cursorTime)
	}
	return s.Name
}

// cursorTime formats creation times in cursors.
const cursorTime = "2006-01-02T15:04:05.000000000Z07:00"

// errCursorOrder is the 400 of a cursor issued for another sort.
// validation: a fixed text wrapping api.ErrInvalidCursor (API-03).
var errCursorOrder = fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)

// cursorPosition checks that a keyset cursor belongs to the list's sort and
// carries a valid position.
func cursorPosition(c *api.Cursor, field, dir string) error {
	if !c.Matches(field, dir) || !api.IsUUID(c.ID) {
		return errCursorOrder
	}
	if field == "created_at" {
		if _, err := time.Parse(time.RFC3339Nano, c.Value); err != nil {
			return errCursorOrder
		}
	}
	return nil
}

// supplierPage is the list response of the rows a repository read (at
// most one more than the limit): when more follow, the page ends at the
// limit and next_cursor continues after its last row.
func supplierPage(rows []Supplier, total int, page api.PaginationParams, field, dir string) api.ListResponse[Supplier] {
	list := api.ListResponse[Supplier]{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := &list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: field, Dir: dir, Value: sortValue(last, field), ID: last.ID})
	}
	return list
}

// SupplierRepository stores the suppliers. List returns the rows of the
// page plus one more when more follow, and the total of the filtered list.
type SupplierRepository interface {
	ListSuppliers(ctx context.Context, orgID string, q *api.ListQuery, page api.PaginationParams) ([]Supplier, int, error)
	GetSupplier(ctx context.Context, orgID, id string) (*Supplier, error)
	CreateSupplier(ctx context.Context, s *Supplier) error
	UpdateSupplier(ctx context.Context, orgID, id string, req *UpdateSupplierRequest) (*Supplier, error)
	DeleteSupplier(ctx context.Context, orgID, id string) error
}

// MemoryRepository keeps suppliers in memory (--no-db and tests). It knows
// no client scope, contacts or clients and no If-Match requirement of the
// organization; the PostgreSQL repository enforces them.
type MemoryRepository struct {
	mu        sync.Mutex
	suppliers map[string]*Supplier
}

// NewMemoryRepository returns an empty in-memory supplier repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{suppliers: map[string]*Supplier{}}
}

// nameTaken reports whether another supplier of orgID has name.
func (m *MemoryRepository) nameTaken(orgID, id, name string) bool {
	for _, s := range m.suppliers {
		if s.OrganizationID == orgID && s.ID != id && s.Name == name {
			return true
		}
	}
	return false
}

// matches applies the list filters to s.
func matches(s *Supplier, filters []api.Filter) bool {
	for _, f := range filters {
		var value string
		switch f.Field {
		case "name":
			value = s.Name
		case "client_id":
			value = deref(s.ClientID)
		case "external_ref":
			value = deref(s.ExternalRef)
		}
		if !f.Match(value) {
			return false
		}
	}
	return true
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListSuppliers filters, sorts and pages the suppliers of orgID.
func (m *MemoryRepository) ListSuppliers(_ context.Context, orgID string, q *api.ListQuery, page api.PaginationParams) ([]Supplier, int, error) {
	field, dir := supplierOrder(q)
	m.mu.Lock()
	all := make([]Supplier, 0, len(m.suppliers))
	for _, s := range m.suppliers {
		if s.OrganizationID == orgID && matches(s, q.Filters) {
			all = append(all, *s)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(all, func(a, b Supplier) int {
		c := strings.Compare(sortValue(&a, field), sortValue(&b, field))
		if c == 0 {
			c = strings.Compare(a.ID, b.ID)
		}
		if dir == "desc" {
			return -c
		}
		return c
	})
	start := min(max(page.Offset, 0), len(all))
	if page.Cursor != nil {
		if err := cursorPosition(page.Cursor, field, dir); err != nil {
			return nil, 0, err
		}
		cursor := *page.Cursor
		if field == "created_at" {
			at, _ := time.Parse(time.RFC3339Nano, cursor.Value)
			cursor.Value = at.UTC().Format(cursorTime)
		}
		start = len(all)
		for i := range all {
			if api.KeysetCompare(sortValue(&all[i], field), all[i].ID, cursor, dir) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, len(all))
	return all[start:end], len(all), nil
}

// GetSupplier returns a supplier of orgID.
func (m *MemoryRepository) GetSupplier(_ context.Context, orgID, id string) (*Supplier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.suppliers[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrSupplierNotFound
	}
	out := *s
	return &out, nil
}

// CreateSupplier stores a new supplier with version 1.
func (m *MemoryRepository) CreateSupplier(_ context.Context, s *Supplier) error {
	if err := validate(s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(s.OrganizationID, "", s.Name) {
		return errNameTaken
	}
	now := time.Now().UTC()
	s.ID, s.Version, s.CreatedAt, s.UpdatedAt = newID(), 1, now, now
	stored := *s
	m.suppliers[s.ID] = &stored
	return nil
}

// UpdateSupplier changes the sent fields after the If-Match check.
func (m *MemoryRepository) UpdateSupplier(_ context.Context, orgID, id string, req *UpdateSupplierRequest) (*Supplier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.suppliers[id]
	if !ok || current.OrganizationID != orgID {
		return nil, ErrSupplierNotFound
	}
	if err := checkIfMatch(req.IfMatch, false, current.Version); err != nil {
		return nil, err
	}
	next := *current
	req.apply(&next)
	if err := validate(&next); err != nil {
		return nil, err
	}
	if m.nameTaken(orgID, id, next.Name) {
		return nil, errNameTaken
	}
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	*current = next
	return &next, nil
}

// newID returns a random version 4 UUID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// DeleteSupplier removes a supplier of orgID.
func (m *MemoryRepository) DeleteSupplier(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.suppliers[id]
	if !ok || s.OrganizationID != orgID {
		return ErrSupplierNotFound
	}
	delete(m.suppliers, id)
	return nil
}

// Handler serves /api/v1/suppliers (MGT-12, API-06). Reading needs
// contract:read, writing contract:write (server/authz.go); the routes
// belong to the module inventory and the feature assets_basic.
type Handler struct {
	suppliers SupplierRepository
}

// NewHandler returns the handler; without a repository every route
// answers 503.
func NewHandler(suppliers SupplierRepository) *Handler {
	return &Handler{suppliers: suppliers}
}

// RegisterRoutes registers the supplier routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/suppliers", h.ListSuppliers)
	r.Post("/api/v1/suppliers", h.CreateSupplier)
	r.Get("/api/v1/suppliers/{id}", h.GetSupplier)
	r.Patch("/api/v1/suppliers/{id}", h.UpdateSupplier)
	r.Delete("/api/v1/suppliers/{id}", h.DeleteSupplier)
}

// store returns the repository and the organization, or answers 401 or
// 503.
func (h *Handler) store(w http.ResponseWriter, r *http.Request) (SupplierRepository, string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return nil, "", false
	}
	if h.suppliers == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "suppliers are not available")
		return nil, "", false
	}
	return h.suppliers, t.OrganizationID, true
}

// writeError reports a failed supplier request (RFC 7807).
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSupplierNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrSupplierNotFound.Error())
	case errors.Is(err, ErrPreconditionRequired):
		api.WritePreconditionFailed(w, ErrPreconditionRequired.Error())
	case errors.Is(err, ErrPreconditionFailed):
		api.WritePreconditionFailed(w, ErrPreconditionFailed.Error())
	case errors.Is(err, errCursorOrder):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", errCursorOrder.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// writeSupplier answers a supplier with its version as ETag.
func writeSupplier(w http.ResponseWriter, status int, s *Supplier) {
	w.Header().Set("ETag", api.ETag(s.Version))
	api.WriteJSON(w, status, s)
}

// ListSuppliers handles GET /api/v1/suppliers: filter[name],
// filter[client_id], filter[external_ref], sort name or created_at (a
// leading - descends), limit and the signed keyset cursor (API-02).
func (h *Handler) ListSuppliers(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.store(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	q, err := api.ParseListQuery(r, SupplierFields)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	field, dir := supplierOrder(&q)
	if page.Cursor != nil {
		if posErr := cursorPosition(page.Cursor, field, dir); posErr != nil {
			writeError(w, posErr)
			return
		}
	}
	rows, total, err := repo.ListSuppliers(r.Context(), orgID, &q, page)
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, supplierPage(rows, total, page, field, dir))
}

// GetSupplier handles GET /api/v1/suppliers/{id}.
func (h *Handler) GetSupplier(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.store(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrSupplierNotFound.Error())
	if !ok {
		return
	}
	s, err := repo.GetSupplier(r.Context(), orgID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeSupplier(w, http.StatusOK, s)
}

// CreateSupplier handles POST /api/v1/suppliers.
func (h *Handler) CreateSupplier(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.store(w, r)
	if !ok {
		return
	}
	var req CreateSupplierRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	s := &Supplier{OrganizationID: orgID, ClientID: req.ClientID, Name: req.Name,
		ExternalRef: req.ExternalRef, ContactID: req.ContactID, Notes: req.Notes}
	if err := repo.CreateSupplier(r.Context(), s); err != nil {
		writeError(w, err)
		return
	}
	writeSupplier(w, http.StatusCreated, s)
}

// UpdateSupplier handles PATCH /api/v1/suppliers/{id} with If-Match
// (API-07): 412 when it is required but missing or names an older version.
func (h *Handler) UpdateSupplier(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.store(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrSupplierNotFound.Error())
	if !ok {
		return
	}
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return
	}
	var req UpdateSupplierRequest
	if err = api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.ClientID.Set {
		api.WriteFieldError(w, &api.FieldError{Field: "client_id", Message: "cannot be changed; create a supplier for the other client"})
		return
	}
	if present {
		req.IfMatch = &version
	}
	s, err := repo.UpdateSupplier(r.Context(), orgID, id, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeSupplier(w, http.StatusOK, s)
}

// DeleteSupplier handles DELETE /api/v1/suppliers/{id}; a supplier still
// referenced (contracts, assets, orders) is 422.
func (h *Handler) DeleteSupplier(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.store(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrSupplierNotFound.Error())
	if !ok {
		return
	}
	if err := repo.DeleteSupplier(r.Context(), orgID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
