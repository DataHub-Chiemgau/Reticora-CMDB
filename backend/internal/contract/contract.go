package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Contract is a contract of an organization or one of its clients (MGT-12,
// WP-269). client_id nil is an org-wide contract; supplier_id names a
// supplier of the organization (E-41) that is org-wide or of the same
// client. kind and status take the English keys of E-119; dates are
// YYYY-MM-DD, value is a decimal number in currency (ISO 4217).
type Contract struct {
	ID               string       `json:"id"`
	OrganizationID   string       `json:"organization_id"`
	ClientID         *string      `json:"client_id"`
	SupplierID       *string      `json:"supplier_id"`
	Number           string       `json:"number"`
	Kind             *string      `json:"kind"`
	StartDate        *string      `json:"start_date"`
	EndDate          *string      `json:"end_date"`
	NoticePeriodDays *int         `json:"notice_period_days"`
	Value            *json.Number `json:"value"`
	Currency         string       `json:"currency"`
	Status           string       `json:"status"`
	Notes            *string      `json:"notes"`
	Version          int64        `json:"version"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// The value ranges of kind (E-119) and status; WP-270 filters on status.
var (
	ContractKinds    = []string{"maintenance", "support", "lease", "license", "purchase", "service", "other"}
	ContractStatuses = []string{"draft", "active", "terminated", "ended"}
)

// DefaultContractStatus is the status of a contract created without one.
const DefaultContractStatus = "active"

// CreateContractRequest is the body of POST /api/v1/contracts. A missing
// currency is the currency of the organization, a missing status active.
type CreateContractRequest struct {
	ClientID         *string      `json:"client_id"`
	SupplierID       *string      `json:"supplier_id"`
	Number           string       `json:"number"`
	Kind             *string      `json:"kind"`
	StartDate        *string      `json:"start_date"`
	EndDate          *string      `json:"end_date"`
	NoticePeriodDays *int         `json:"notice_period_days"`
	Value            *json.Number `json:"value"`
	Currency         *string      `json:"currency"`
	Status           *string      `json:"status"`
	Notes            *string      `json:"notes"`
}

// SentFields are the DTO paths of the fields the request sets; the field
// permissions check them (WP-265). client_id and number are structural.
func (req *CreateContractRequest) SentFields() []string {
	var out []string
	for _, f := range []struct {
		path string
		set  bool
	}{
		{"supplier_id", req.SupplierID != nil}, {"kind", req.Kind != nil}, {"start_date", req.StartDate != nil},
		{"end_date", req.EndDate != nil}, {"notice_period_days", req.NoticePeriodDays != nil}, {"value", req.Value != nil},
		{"currency", req.Currency != nil}, {"status", req.Status != nil}, {"notes", req.Notes != nil},
	} {
		if f.set {
			out = append(out, f.path)
		}
	}
	return out
}

// contract returns the contract the request creates in orgID.
func (req *CreateContractRequest) contract(orgID string) *Contract {
	c := &Contract{OrganizationID: orgID, ClientID: req.ClientID, SupplierID: req.SupplierID, Number: req.Number,
		Kind: req.Kind, StartDate: req.StartDate, EndDate: req.EndDate, NoticePeriodDays: req.NoticePeriodDays,
		Value: req.Value, Status: DefaultContractStatus, Notes: req.Notes}
	if req.Currency != nil {
		c.Currency = *req.Currency
	}
	if req.Status != nil {
		c.Status = *req.Status
	}
	return c
}

// Nullable is a nullable field of a PATCH body of any type: Set records
// that it was sent, Value nil that it was sent as null.
type Nullable[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON records that the field was sent; null clears the value.
func (o *Nullable[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// UpdateContractRequest is the body of PATCH /api/v1/contracts/{id}: sent
// fields change, null clears an optional field. client_id cannot change;
// number, currency and status cannot be null.
type UpdateContractRequest struct {
	ClientID         Optional              `json:"client_id"`
	SupplierID       Optional              `json:"supplier_id"`
	Number           Optional              `json:"number"`
	Kind             Optional              `json:"kind"`
	StartDate        Optional              `json:"start_date"`
	EndDate          Optional              `json:"end_date"`
	NoticePeriodDays Nullable[int]         `json:"notice_period_days"`
	Value            Nullable[json.Number] `json:"value"`
	Currency         Optional              `json:"currency"`
	Status           Optional              `json:"status"`
	Notes            Optional              `json:"notes"`
	// IfMatch is the version of the If-Match header (api.IfMatchAny for
	// "*"); nil without the header.
	IfMatch *int64 `json:"-"`
	// Check runs on the locked current contract before If-Match and the
	// validation (the field permissions of the handler); nil passes.
	Check func(current *Contract) error `json:"-"`
}

// SentFields are the DTO paths of the fields the request sends.
func (req *UpdateContractRequest) SentFields() []string {
	var out []string
	for _, f := range []struct {
		path string
		set  bool
	}{
		{"client_id", req.ClientID.Set}, {"supplier_id", req.SupplierID.Set}, {"number", req.Number.Set},
		{"kind", req.Kind.Set}, {"start_date", req.StartDate.Set}, {"end_date", req.EndDate.Set},
		{"notice_period_days", req.NoticePeriodDays.Set}, {"value", req.Value.Set}, {"currency", req.Currency.Set},
		{"status", req.Status.Set}, {"notes", req.Notes.Set},
	} {
		if f.set {
			out = append(out, f.path)
		}
	}
	return out
}

// nonNull refuses null for the fields that cannot be cleared.
func (req *UpdateContractRequest) nonNull() error {
	for _, f := range []struct {
		field string
		o     Optional
	}{{"number", req.Number}, {"currency", req.Currency}, {"status", req.Status}} {
		if f.o.Set && f.o.Value == nil {
			return &api.FieldError{Field: f.field, Message: "must not be null"}
		}
	}
	return nil
}

// Apply merges the sent fields into c.
func (req *UpdateContractRequest) Apply(c *Contract) {
	for _, f := range []struct {
		o   Optional
		dst **string
	}{
		{req.SupplierID, &c.SupplierID}, {req.Kind, &c.Kind}, {req.StartDate, &c.StartDate},
		{req.EndDate, &c.EndDate}, {req.Notes, &c.Notes},
	} {
		if f.o.Set {
			*f.dst = f.o.Value
		}
	}
	for _, f := range []struct {
		o   Optional
		dst *string
	}{{req.Number, &c.Number}, {req.Currency, &c.Currency}, {req.Status, &c.Status}} {
		if f.o.Set && f.o.Value != nil {
			*f.dst = *f.o.Value
		}
	}
	if req.NoticePeriodDays.Set {
		c.NoticePeriodDays = req.NoticePeriodDays.Value
	}
	if req.Value.Set {
		c.Value = req.Value.Value
	}
}

// Errors of the contract repositories.
var (
	// ErrContractNotFound: unknown, of another organization or outside
	// the caller's client scope (404).
	ErrContractNotFound = errors.New("contract not found")
	// ErrContractChanged: If-Match names another version than the current
	// one (412).
	ErrContractChanged = errors.New("the contract has changed since the If-Match version")
)

var (
	// errNumberTaken is the 409 of a number another contract of the
	// organization holds (UNIQUE (organization_id, number)).
	errNumberTaken = &api.ConflictError{Field: "number", Message: "another contract of the organization has this number"}
	// errSupplier is the 422 of a supplier_id that names no supplier
	// visible under the caller's scope.
	errSupplier = &api.FieldError{Field: "supplier_id", Message: "names no supplier of the organization", Status: http.StatusUnprocessableEntity}
	// errSupplierClient is the 422 of a supplier of another client than
	// the contract.
	errSupplierClient = &api.FieldError{Field: "supplier_id", Message: "the supplier must be org-wide or of the contract's client", Status: http.StatusUnprocessableEntity}
	errEndDate        = &api.FieldError{Field: "end_date", Message: "must not be before start_date", Status: http.StatusUnprocessableEntity}
	errNotice         = &api.FieldError{Field: "notice_period_days", Message: "must not be negative", Status: http.StatusUnprocessableEntity}
	errCurrency       = &api.FieldError{Field: "currency", Message: "must be an ISO 4217 code of three capital letters", Status: http.StatusUnprocessableEntity}
	errKind           = &api.FieldError{Field: "kind", Message: "must be one of maintenance, support, lease, license, purchase, service, other", Status: http.StatusUnprocessableEntity}
	errStatus         = &api.FieldError{Field: "status", Message: "must be one of draft, active, terminated, ended", Status: http.StatusUnprocessableEntity}
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// validateContract checks the fields of a contract before it is stored:
// syntax errors are 400, value range and CHECK rules 422.
func validateContract(c *Contract) error {
	c.Number = strings.TrimSpace(c.Number)
	if c.Number == "" {
		return &api.FieldError{Field: "number", Message: "must not be empty"}
	}
	for _, f := range []struct {
		field string
		value *string
	}{{"client_id", c.ClientID}, {"supplier_id", c.SupplierID}} {
		if f.value != nil && !api.IsUUID(*f.value) {
			return &api.FieldError{Field: f.field, Message: "must be a UUID"}
		}
	}
	for _, f := range []struct {
		field string
		value *string
	}{{"start_date", c.StartDate}, {"end_date", c.EndDate}} {
		if f.value == nil {
			continue
		}
		if _, err := time.Parse(time.DateOnly, *f.value); err != nil {
			return &api.FieldError{Field: f.field, Message: "must be a date YYYY-MM-DD"}
		}
	}
	for _, f := range []struct {
		field string
		value *string
	}{{"number", &c.Number}, {"notes", c.Notes}} {
		if f.value == nil {
			continue
		}
		if err := api.RejectNUL(f.field, *f.value); err != nil {
			return err
		}
	}
	switch {
	case c.StartDate != nil && c.EndDate != nil && *c.EndDate < *c.StartDate:
		return errEndDate
	case c.NoticePeriodDays != nil && *c.NoticePeriodDays < 0:
		return errNotice
	case !currencyCode.MatchString(c.Currency):
		return errCurrency
	case c.Kind != nil && !slices.Contains(ContractKinds, *c.Kind):
		return errKind
	case !slices.Contains(ContractStatuses, c.Status):
		return errStatus
	}
	return nil
}

// ContractFields are the filters and sort fields of the contract list
// (API-02); the first sort field is the default.
var ContractFields = api.ListFields{
	Filterable: map[string]string{
		"status": api.KindText, "kind": api.KindText, "supplier_id": api.KindUUID, "client_id": api.KindUUID,
		"end_date": api.KindTime,
	},
	Sortable: []string{"number", "start_date", "end_date", "created_at"},
}

// contractOrder resolves the sort of a list query: number ascending by
// default.
func contractOrder(q *api.ListQuery) (field, dir string) {
	field, dir = q.SortBy, q.SortDir
	if field == "" {
		field = ContractFields.Sortable[0]
	}
	if dir == "" {
		dir = "asc"
	}
	return field, dir
}

// noDate is the cursor value of a missing date: it sorts after every date
// (PostgreSQL 'infinity'), so contracts without a date come last ascending.
const noDate = "infinity"

// contractSortValue is the cursor value of c for the sort field.
func contractSortValue(c *Contract, field string) string {
	switch field {
	case "created_at":
		return c.CreatedAt.UTC().Format(cursorTime)
	case "start_date", "end_date":
		date := c.StartDate
		if field == "end_date" {
			date = c.EndDate
		}
		if date == nil {
			return noDate
		}
		return *date
	}
	return c.Number
}

// contractCursorPosition checks that a keyset cursor belongs to the list's
// sort and carries a valid position.
func contractCursorPosition(c *api.Cursor, field, dir string) error {
	if !c.Matches(field, dir) || !api.IsUUID(c.ID) {
		return errCursorOrder
	}
	switch field {
	case "created_at":
		if _, err := time.Parse(time.RFC3339Nano, c.Value); err != nil {
			return errCursorOrder
		}
	case "start_date", "end_date":
		if _, err := time.Parse(time.DateOnly, c.Value); err != nil && c.Value != noDate {
			return errCursorOrder
		}
	}
	return nil
}

// contractPage is the list of the rows a repository read (at most one more
// than the limit): when more follow, the page ends at the limit and
// next_cursor continues after its last row.
func contractPage(rows []Contract, total int, page api.PaginationParams, field, dir string) api.ListResponse[Contract] {
	list := api.ListResponse[Contract]{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := &list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: field, Dir: dir, Value: contractSortValue(last, field), ID: last.ID})
	}
	return list
}

// ContractRepository stores the contracts. List returns the rows of the
// page plus one more when more follow, and the total of the filtered list.
type ContractRepository interface {
	ListContracts(ctx context.Context, orgID string, q *api.ListQuery, page api.PaginationParams) ([]Contract, int, error)
	GetContract(ctx context.Context, orgID, id string) (*Contract, error)
	CreateContract(ctx context.Context, c *Contract) error
	UpdateContract(ctx context.Context, orgID, id string, req *UpdateContractRequest) (*Contract, error)
	DeleteContract(ctx context.Context, orgID, id string) error
}

// MemoryContracts keeps contracts in memory (--no-db and tests). It knows
// no client scope, clients, suppliers or organization settings: a missing
// currency is EUR, the default of organization.currency; the PostgreSQL
// repository enforces the rest.
type MemoryContracts struct {
	mu        sync.Mutex
	contracts map[string]*Contract
}

// NewMemoryContracts returns an empty in-memory contract repository.
func NewMemoryContracts() *MemoryContracts {
	return &MemoryContracts{contracts: map[string]*Contract{}}
}

// memoryCurrency is the currency of a contract created without one in
// memory (the default of organization.currency, migration 000101).
const memoryCurrency = "EUR"

func (m *MemoryContracts) numberTaken(orgID, id, number string) bool {
	for _, c := range m.contracts {
		if c.OrganizationID == orgID && c.ID != id && c.Number == number {
			return true
		}
	}
	return false
}

// contractMatches applies the list filters to c.
func contractMatches(c *Contract, filters []api.Filter) bool {
	for _, f := range filters {
		var value string
		switch f.Field {
		case "status":
			value = c.Status
		case "kind":
			value = deref(c.Kind)
		case "supplier_id":
			value = deref(c.SupplierID)
		case "client_id":
			value = deref(c.ClientID)
		case "end_date":
			value = deref(c.EndDate)
		}
		if !f.Match(value) {
			return false
		}
	}
	return true
}

// ListContracts filters, sorts and pages the contracts of orgID.
func (m *MemoryContracts) ListContracts(_ context.Context, orgID string, q *api.ListQuery, page api.PaginationParams) ([]Contract, int, error) {
	field, dir := contractOrder(q)
	m.mu.Lock()
	all := make([]Contract, 0, len(m.contracts))
	for _, c := range m.contracts {
		if c.OrganizationID == orgID && contractMatches(c, q.Filters) {
			all = append(all, *c)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(all, func(a, b Contract) int {
		c := strings.Compare(contractSortValue(&a, field), contractSortValue(&b, field))
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
		if err := contractCursorPosition(page.Cursor, field, dir); err != nil {
			return nil, 0, err
		}
		cursor := *page.Cursor
		if field == "created_at" {
			at, _ := time.Parse(time.RFC3339Nano, cursor.Value)
			cursor.Value = at.UTC().Format(cursorTime)
		}
		start = len(all)
		for i := range all {
			if api.KeysetCompare(contractSortValue(&all[i], field), all[i].ID, cursor, dir) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, len(all))
	return all[start:end], len(all), nil
}

// GetContract returns a contract of orgID.
func (m *MemoryContracts) GetContract(_ context.Context, orgID, id string) (*Contract, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.contracts[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrContractNotFound
	}
	out := *c
	return &out, nil
}

// CreateContract stores a new contract with version 1.
func (m *MemoryContracts) CreateContract(_ context.Context, c *Contract) error {
	if c.Currency == "" {
		c.Currency = memoryCurrency
	}
	if err := validateContract(c); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.numberTaken(c.OrganizationID, "", c.Number) {
		return errNumberTaken
	}
	now := time.Now().UTC()
	c.ID, c.Version, c.CreatedAt, c.UpdatedAt = newID(), 1, now, now
	stored := *c
	m.contracts[c.ID] = &stored
	return nil
}

// UpdateContract changes the sent fields after the check and If-Match.
func (m *MemoryContracts) UpdateContract(_ context.Context, orgID, id string, req *UpdateContractRequest) (*Contract, error) {
	if err := req.nonNull(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.contracts[id]
	if !ok || current.OrganizationID != orgID {
		return nil, ErrContractNotFound
	}
	if req.Check != nil {
		if err := req.Check(current); err != nil {
			return nil, err
		}
	}
	if err := checkContractIfMatch(req.IfMatch, false, current.Version); err != nil {
		return nil, err
	}
	next := *current
	req.Apply(&next)
	if err := validateContract(&next); err != nil {
		return nil, err
	}
	if m.numberTaken(orgID, id, next.Number) {
		return nil, errNumberTaken
	}
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	*current = next
	return &next, nil
}

// DeleteContract removes a contract of orgID.
func (m *MemoryContracts) DeleteContract(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.contracts[id]
	if !ok || c.OrganizationID != orgID {
		return ErrContractNotFound
	}
	delete(m.contracts, id)
	return nil
}

// checkContractIfMatch applies If-Match (API-07) like checkIfMatch, with
// the contract's own 412 text.
func checkContractIfMatch(ifMatch *int64, required bool, current int64) error {
	err := checkIfMatch(ifMatch, required, current)
	if errors.Is(err, ErrPreconditionFailed) {
		return ErrContractChanged
	}
	return err
}
