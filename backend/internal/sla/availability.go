package sla

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Availability targets (ALR-06, WP-1048, E-160): a target percent per CI,
// service (business_service, application and their subtypes), connection
// (a port or CPE CI: every CI but the service types) or client, per month,
// quarter or year, with the share of the error budget that raises the
// alarm and an optional service time (business calendar). The computation
// follows in WP-1049, the reports in WP-1050. Client and site of a target
// of a CI follow the CI, never the request.

// Scope kinds of a target.
const (
	ScopeCI         = "ci"
	ScopeService    = "service"
	ScopeConnection = "connection"
	ScopeClient     = "client"
)

// ScopeKinds lists the scope kinds (CHECK availability_target_scope_kind_check).
var ScopeKinds = []string{ScopeCI, ScopeService, ScopeConnection, ScopeClient}

// Periods of a target.
const (
	PeriodMonth   = "month"
	PeriodQuarter = "quarter"
	PeriodYear    = "year"
)

// Periods lists the periods.
var Periods = []string{PeriodMonth, PeriodQuarter, PeriodYear}

// ServiceTypeKeys are the CI types of a service target, each with its
// subtypes.
var ServiceTypeKeys = []string{"business_service", "application"}

// DefaultBudgetWarnPercent is the warning share of the error budget.
const DefaultBudgetWarnPercent = 80

// maxTargetName bounds the name of a target.
const maxTargetName = 200

var (
	// ErrTargetNotFound: the target does not exist or is outside the
	// caller's scope (404).
	ErrTargetNotFound = errors.New("availability target not found")
	// ErrTargetExists: the object has a target of the period already (409).
	ErrTargetExists = errors.New("the object has an availability target of this period already")
	// ErrTargetOutOfScope: the CI or client of the target lies outside the
	// caller's scope (403).
	ErrTargetOutOfScope = errors.New("the object of the target lies outside the caller's scope")
)

// AvailabilityTarget is one availability target.
type AvailabilityTarget struct {
	ID                string    `json:"id"`
	OrganizationID    string    `json:"organization_id"`
	ClientID          *string   `json:"client_id"`
	SiteID            *string   `json:"site_id"`
	Name              string    `json:"name"`
	ScopeKind         string    `json:"scope_kind"`
	CIID              *string   `json:"ci_id"`
	TargetPercent     float64   `json:"target_percent"`
	Period            string    `json:"period"`
	BudgetWarnPercent int       `json:"budget_warn_percent"`
	CalendarID        *string   `json:"calendar_id"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// CreateAvailabilityTargetRequest is the body of POST.
type CreateAvailabilityTargetRequest struct {
	Name              string   `json:"name"`
	ScopeKind         string   `json:"scope_kind"`
	CIID              *string  `json:"ci_id"`
	ClientID          *string  `json:"client_id"`
	TargetPercent     *float64 `json:"target_percent"`
	Period            string   `json:"period"`
	BudgetWarnPercent *int     `json:"budget_warn_percent"`
	CalendarID        *string  `json:"calendar_id"`
	Enabled           *bool    `json:"enabled"`
}

// UpdateAvailabilityTargetRequest is the body of PATCH: the object (scope
// kind, CI, client) of a target does not change.
type UpdateAvailabilityTargetRequest struct {
	Name              *string    `json:"name"`
	TargetPercent     *float64   `json:"target_percent"`
	Period            *string    `json:"period"`
	BudgetWarnPercent *int       `json:"budget_warn_percent"`
	CalendarID        OptionalID `json:"calendar_id"`
	Enabled           *bool      `json:"enabled"`
}

// AvailabilityFilter selects targets.
type AvailabilityFilter struct {
	ScopeKind string
	CIID      string
	ClientID  string
}

// AvailabilityRepository stores the availability targets; the memory and
// the PostgreSQL implementation behave alike.
type AvailabilityRepository interface {
	ListTargets(ctx context.Context, orgID string, f AvailabilityFilter) ([]AvailabilityTarget, error)
	GetTarget(ctx context.Context, orgID, id string) (*AvailabilityTarget, error)
	// CreateTarget checks the object of the target (ResolveObject), takes
	// client and site of a CI and stores it.
	CreateTarget(ctx context.Context, t *AvailabilityTarget) error
	UpdateTarget(ctx context.Context, orgID, id string, req *UpdateAvailabilityTargetRequest) (*AvailabilityTarget, error)
	DeleteTarget(ctx context.Context, orgID, id string) error
}

// unprocessable is the 422 of a field.
func unprocessable(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// targetOf builds a target from a create request (defaults applied); the
// object is resolved by the repository.
func targetOf(orgID string, req *CreateAvailabilityTargetRequest) *AvailabilityTarget {
	t := &AvailabilityTarget{OrganizationID: orgID, Name: strings.TrimSpace(req.Name), ScopeKind: req.ScopeKind,
		CIID: lowerPtr(req.CIID), ClientID: lowerPtr(req.ClientID), Period: req.Period,
		BudgetWarnPercent: DefaultBudgetWarnPercent, CalendarID: lowerPtr(req.CalendarID), Enabled: true}
	if req.TargetPercent != nil {
		t.TargetPercent = *req.TargetPercent
	}
	if req.BudgetWarnPercent != nil {
		t.BudgetWarnPercent = *req.BudgetWarnPercent
	}
	if req.Enabled != nil {
		t.Enabled = *req.Enabled
	}
	return t
}

func lowerPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*p))
	if v == "" {
		return nil
	}
	return &v
}

// Validate checks the fields of a target that need no lookup: 422 with the
// field.
// validation: field errors with fixed texts.
func (t *AvailabilityTarget) Validate() error {
	switch {
	case t.Name == "":
		return unprocessable("name", "name is required")
	case len([]rune(t.Name)) > maxTargetName || strings.ContainsRune(t.Name, 0):
		return unprocessable("name", "name must be at most 200 characters without NUL")
	case !slices.Contains(ScopeKinds, t.ScopeKind):
		return unprocessable("scope_kind", "scope_kind must be one of "+strings.Join(ScopeKinds, ", "))
	case !slices.Contains(Periods, t.Period):
		return unprocessable("period", "period must be one of "+strings.Join(Periods, ", "))
	case math.IsNaN(t.TargetPercent) || t.TargetPercent <= 0 || t.TargetPercent > 100:
		return unprocessable("target_percent", "target_percent must be greater than 0 and at most 100")
	case math.Abs(t.TargetPercent*1000-math.Round(t.TargetPercent*1000)) > 1e-6:
		return unprocessable("target_percent", "target_percent has at most three decimals")
	case t.BudgetWarnPercent < 1 || t.BudgetWarnPercent > 100:
		return unprocessable("budget_warn_percent", "budget_warn_percent must be between 1 and 100")
	}
	for _, f := range []struct {
		field string
		id    *string
	}{{"ci_id", t.CIID}, {"client_id", t.ClientID}, {"calendar_id", t.CalendarID}} {
		if f.id != nil && !api.IsUUID(*f.id) {
			return unprocessable(f.field, f.field+" must be a UUID")
		}
	}
	if t.ScopeKind == ScopeClient {
		if t.CIID != nil {
			return unprocessable("ci_id", "a client target names no CI")
		}
		if t.ClientID == nil {
			return unprocessable("client_id", "a client target needs client_id")
		}
	} else if t.CIID == nil {
		return unprocessable("ci_id", "a target of kind "+t.ScopeKind+" needs ci_id")
	}
	return nil
}

// ObjectCI is what a target reads of its CI: client, site and whether its
// type is a service type (or a subtype of one).
type ObjectCI struct {
	ClientID, SiteID *string
	Service          bool
}

// applyObject checks the kind of a target against its CI and takes client
// and site of the CI.
func applyObject(t *AvailabilityTarget, ci ObjectCI) error {
	switch {
	case t.ScopeKind == ScopeService && !ci.Service:
		return unprocessable("ci_id", "a service target needs a CI of type business_service or application")
	case t.ScopeKind == ScopeConnection && ci.Service:
		return unprocessable("ci_id", "a connection target needs a port or CPE CI, not a service")
	}
	t.ClientID, t.SiteID = ci.ClientID, ci.SiteID
	return nil
}

// errCalendar is the 422 of a calendar outside the target's scope.
var errCalendar = unprocessable("calendar_id", "business calendar not found or bound to another client")

// checkCalendarClient checks a calendar's client against the target's: a
// calendar of a client serves only targets of that client.
func checkCalendarClient(calendarClient string, t *AvailabilityTarget) error {
	if calendarClient != "" && (t.ClientID == nil || *t.ClientID != calendarClient) {
		return errCalendar
	}
	return nil
}

// applyUpdate applies a PATCH to t and validates it.
func applyUpdate(t *AvailabilityTarget, req *UpdateAvailabilityTargetRequest) error {
	if req.Name != nil {
		t.Name = strings.TrimSpace(*req.Name)
	}
	if req.TargetPercent != nil {
		t.TargetPercent = *req.TargetPercent
	}
	if req.Period != nil {
		t.Period = *req.Period
	}
	if req.BudgetWarnPercent != nil {
		t.BudgetWarnPercent = *req.BudgetWarnPercent
	}
	if req.CalendarID.Set {
		t.CalendarID = lowerPtr(&req.CalendarID.Value)
	}
	if req.Enabled != nil {
		t.Enabled = *req.Enabled
	}
	return t.Validate()
}

// --- Memory ---

// ObjectLookup answers the CI of a target in the memory profile; ok false
// is a CI unknown to the organization, out is a CI outside the caller's
// scope.
type ObjectLookup interface {
	ObjectCI(ctx context.Context, orgID, ciID string) (ci ObjectCI, ok bool, err error)
}

// CalendarLookup reads a business calendar (Repository).
type CalendarLookup interface {
	GetCalendar(ctx context.Context, orgID, id string) (*BusinessCalendar, error)
}

// MemoryAvailabilityRepository keeps the targets in memory (--no-db,
// tests).
type MemoryAvailabilityRepository struct {
	mu        sync.Mutex
	targets   map[string]*AvailabilityTarget
	objects   ObjectLookup
	calendars CalendarLookup
	next      int
}

// NewMemoryAvailabilityRepository returns an empty repository; objects and
// calendars answer CIs and calendars (nil: none known).
func NewMemoryAvailabilityRepository(objects ObjectLookup, calendars CalendarLookup) *MemoryAvailabilityRepository {
	return &MemoryAvailabilityRepository{targets: map[string]*AvailabilityTarget{}, objects: objects, calendars: calendars}
}

var _ AvailabilityRepository = (*MemoryAvailabilityRepository)(nil)

// inScope reports whether the target lies in the tenant scope of ctx, like
// the policies of availability_target: a restricted principal reads no
// organization-wide target.
func inScope(ctx context.Context, t *AvailabilityTarget) bool {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return true
	}
	if !scope.Clients.IsAll() && (t.ClientID == nil || !slices.Contains(scope.Clients.IDs(), *t.ClientID)) {
		return false
	}
	if !scope.Sites.IsAll() && t.SiteID != nil && !slices.Contains(scope.Sites.IDs(), *t.SiteID) {
		return false
	}
	return true
}

// writableInScope reports whether the tenant scope of ctx may write the
// target, like WITH CHECK and the restrictive UPDATE/DELETE policies of
// availability_target: a site principal reads the targets of its clients
// without site but writes only those of its sites.
func writableInScope(ctx context.Context, t *AvailabilityTarget) bool {
	if !inScope(ctx, t) {
		return false
	}
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok || scope.Sites.IsAll() {
		return true
	}
	return t.SiteID != nil && slices.Contains(scope.Sites.IDs(), *t.SiteID)
}

func (m *MemoryAvailabilityRepository) resolve(ctx context.Context, t *AvailabilityTarget) error {
	if t.ScopeKind == ScopeClient {
		t.SiteID = nil
	} else {
		if m.objects == nil {
			return unprocessable("ci_id", "CI not found")
		}
		ci, ok, err := m.objects.ObjectCI(ctx, t.OrganizationID, *t.CIID)
		if err != nil {
			return err
		}
		if !ok {
			return unprocessable("ci_id", "CI not found")
		}
		if err := applyObject(t, ci); err != nil {
			return err
		}
	}
	if !writableInScope(ctx, t) {
		return ErrTargetOutOfScope
	}
	return m.checkCalendar(ctx, t)
}

// checkCalendar checks the calendar of t against its client.
func (m *MemoryAvailabilityRepository) checkCalendar(ctx context.Context, t *AvailabilityTarget) error {
	if t.CalendarID != nil {
		if m.calendars == nil {
			return errCalendar
		}
		c, err := m.calendars.GetCalendar(ctx, t.OrganizationID, *t.CalendarID)
		if errors.Is(err, ErrCalendarNotFound) {
			return errCalendar
		}
		if err != nil {
			return err
		}
		calendarClient := ""
		if c.ClientID != nil {
			calendarClient = *c.ClientID
		}
		if err := checkCalendarClient(calendarClient, t); err != nil {
			return err
		}
	}
	return nil
}

// conflictLocked reports whether another target holds the object and
// period of t.
func (m *MemoryAvailabilityRepository) conflictLocked(t *AvailabilityTarget) bool {
	eq := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, o := range m.targets {
		if o.ID != t.ID && o.OrganizationID == t.OrganizationID && o.ScopeKind == t.ScopeKind && o.Period == t.Period &&
			eq(o.CIID, t.CIID) && eq(o.ClientID, t.ClientID) {
			return true
		}
	}
	return false
}

// ListTargets implements AvailabilityRepository: by name, then id.
func (m *MemoryAvailabilityRepository) ListTargets(ctx context.Context, orgID string, f AvailabilityFilter) ([]AvailabilityTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AvailabilityTarget{}
	for _, t := range m.targets {
		switch {
		case t.OrganizationID != orgID, !inScope(ctx, t),
			f.ScopeKind != "" && t.ScopeKind != f.ScopeKind,
			f.CIID != "" && (t.CIID == nil || *t.CIID != f.CIID),
			f.ClientID != "" && (t.ClientID == nil || *t.ClientID != f.ClientID):
			continue
		}
		out = append(out, *t)
	}
	sortTargets(out)
	return out, nil
}

func sortTargets(out []AvailabilityTarget) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
}

// GetTarget implements AvailabilityRepository.
func (m *MemoryAvailabilityRepository) GetTarget(ctx context.Context, orgID, id string) (*AvailabilityTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	if !ok || t.OrganizationID != orgID || !inScope(ctx, t) {
		return nil, ErrTargetNotFound
	}
	cp := *t
	return &cp, nil
}

// CreateTarget implements AvailabilityRepository.
func (m *MemoryAvailabilityRepository) CreateTarget(ctx context.Context, t *AvailabilityTarget) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := m.resolve(ctx, t); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conflictLocked(t) {
		return ErrTargetExists
	}
	m.next++
	t.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", m.next)
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	cp := *t
	m.targets[t.ID] = &cp
	return nil
}

// UpdateTarget implements AvailabilityRepository.
func (m *MemoryAvailabilityRepository) UpdateTarget(ctx context.Context, orgID, id string, req *UpdateAvailabilityTargetRequest) (*AvailabilityTarget, error) {
	cur, err := m.GetTarget(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !writableInScope(ctx, cur) {
		return nil, ErrTargetNotFound
	}
	if err := applyUpdate(cur, req); err != nil {
		return nil, err
	}
	// The object is immutable: the CI is not read again (a soft-deleted
	// CI keeps its targets changeable), only the calendar is checked.
	if err := m.checkCalendar(ctx, cur); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conflictLocked(cur) {
		return nil, ErrTargetExists
	}
	cur.UpdatedAt = time.Now().UTC()
	cp := *cur
	m.targets[id] = &cp
	return cur, nil
}

// DeleteTarget implements AvailabilityRepository.
func (m *MemoryAvailabilityRepository) DeleteTarget(ctx context.Context, orgID, id string) error {
	cur, err := m.GetTarget(ctx, orgID, id)
	if err != nil {
		return err
	}
	if !writableInScope(ctx, cur) {
		return ErrTargetNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.targets, id)
	return nil
}

// --- PostgreSQL ---

// PGAvailabilityRepository stores the targets in availability_target under
// the caller's tenant scope (row level security).
type PGAvailabilityRepository struct{ pool *pgxpool.Pool }

// NewPGAvailabilityRepository returns the PostgreSQL repository.
func NewPGAvailabilityRepository(pool *pgxpool.Pool) *PGAvailabilityRepository {
	return &PGAvailabilityRepository{pool: pool}
}

var _ AvailabilityRepository = (*PGAvailabilityRepository)(nil)

const targetColumns = `id::text, organization_id::text, client_id::text, site_id::text, name, scope_kind, ci_id::text,
	target_percent::float8, period, budget_warn_percent, calendar_id::text, enabled, created_at, updated_at`

func scanTarget(row pgx.Row) (*AvailabilityTarget, error) {
	var t AvailabilityTarget
	err := row.Scan(&t.ID, &t.OrganizationID, &t.ClientID, &t.SiteID, &t.Name, &t.ScopeKind, &t.CIID, &t.TargetPercent,
		&t.Period, &t.BudgetWarnPercent, &t.CalendarID, &t.Enabled, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetNotFound
	}
	return &t, err
}

// objectSQL reads client, site and the service flag of a CI.
const objectSQL = `SELECT c.client_id::text, c.site_id::text, EXISTS (
		SELECT 1 FROM ci_type t, LATERAL ci_type_subtree(t.id) st
		 WHERE t.key = ANY($3::text[]) AND (t.organization_id IS NULL OR t.organization_id = $1) AND st.id = c.ci_type_id)
	  FROM ci c WHERE c.organization_id = $1 AND c.id = $2 AND c.deleted_at IS NULL`

// widened runs fn with the tenant scope widened to the organization
// (app_scope_widen, 000141): to tell a CI or client outside the caller's
// scope (403) from an unknown one (422).
func widened(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var previous []*string
	if err := tx.QueryRow(ctx, `SELECT app_scope_widen()`).Scan(&previous); err != nil {
		return err
	}
	ferr := fn()
	if _, err := tx.Exec(ctx, `SELECT app_scope_restore($1)`, previous); err != nil {
		return err
	}
	return ferr
}

// resolve checks the object and the calendar of t in tx and takes client
// and site of its CI.
func (r *PGAvailabilityRepository) resolve(ctx context.Context, tx pgx.Tx, t *AvailabilityTarget) error {
	if t.ScopeKind == ScopeClient {
		t.SiteID = nil
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client WHERE organization_id = $1 AND id = $2)`,
			t.OrganizationID, *t.ClientID).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			var exists bool
			if err := widened(ctx, tx, func() error {
				return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client WHERE organization_id = $1 AND id = $2)`,
					t.OrganizationID, *t.ClientID).Scan(&exists)
			}); err != nil {
				return err
			}
			if exists {
				return ErrTargetOutOfScope
			}
			return unprocessable("client_id", "client not found")
		}
	} else {
		var ci ObjectCI
		err := tx.QueryRow(ctx, objectSQL, t.OrganizationID, *t.CIID, ServiceTypeKeys).Scan(&ci.ClientID, &ci.SiteID, &ci.Service)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if werr := widened(ctx, tx, func() error {
				return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL)`,
					t.OrganizationID, *t.CIID).Scan(&exists)
			}); werr != nil {
				return werr
			}
			if exists {
				return ErrTargetOutOfScope
			}
			return unprocessable("ci_id", "CI not found")
		}
		if err != nil {
			return fmt.Errorf("read the CI of the target: %w", err)
		}
		if err := applyObject(t, ci); err != nil {
			return err
		}
	}
	return checkCalendarPG(ctx, tx, t)
}

// checkCalendarPG checks the calendar of t against its client in tx.
func checkCalendarPG(ctx context.Context, tx pgx.Tx, t *AvailabilityTarget) error {
	if t.CalendarID != nil {
		var calendarClient string
		err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM business_calendar WHERE organization_id = $1 AND id = $2`,
			t.OrganizationID, *t.CalendarID).Scan(&calendarClient)
		if errors.Is(err, pgx.ErrNoRows) {
			return errCalendar
		}
		if err != nil {
			return fmt.Errorf("read business calendar: %w", err)
		}
		return checkCalendarClient(calendarClient, t)
	}
	return nil
}

// writeErr maps the errors of a write: a unique violation is
// ErrTargetExists, a refused row of the policies ErrTargetOutOfScope.
func writeErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrTargetExists
		case "42501":
			return ErrTargetOutOfScope
		}
	}
	return err
}

// ListTargets implements AvailabilityRepository.
func (r *PGAvailabilityRepository) ListTargets(ctx context.Context, orgID string, f AvailabilityFilter) ([]AvailabilityTarget, error) {
	args := []any{orgID}
	where := []string{"organization_id = $1"}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.ScopeKind != "" {
		add("scope_kind = $%d", f.ScopeKind)
	}
	if f.CIID != "" {
		add("ci_id = $%d::uuid", f.CIID)
	}
	if f.ClientID != "" {
		add("client_id = $%d::uuid", f.ClientID)
	}
	out := []AvailabilityTarget{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+targetColumns+` FROM availability_target WHERE `+strings.Join(where, " AND ")+
			` ORDER BY name, id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTarget(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list availability targets: %w", err)
	}
	return out, nil
}

// GetTarget implements AvailabilityRepository.
func (r *PGAvailabilityRepository) GetTarget(ctx context.Context, orgID, id string) (*AvailabilityTarget, error) {
	var out *AvailabilityTarget
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanTarget(tx.QueryRow(ctx, `SELECT `+targetColumns+` FROM availability_target WHERE organization_id = $1 AND id = $2`,
			orgID, id))
		return err
	})
	return out, err
}

// CreateTarget implements AvailabilityRepository.
func (r *PGAvailabilityRepository) CreateTarget(ctx context.Context, t *AvailabilityTarget) error {
	if err := t.Validate(); err != nil {
		return err
	}
	err := database.WithRequestTenant(ctx, r.pool, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := r.resolve(ctx, tx, t); err != nil {
			return err
		}
		stored, err := scanTarget(tx.QueryRow(ctx, `INSERT INTO availability_target (organization_id, client_id, site_id, name,
				scope_kind, ci_id, target_percent, period, budget_warn_percent, calendar_id, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+targetColumns,
			t.OrganizationID, t.ClientID, t.SiteID, t.Name, t.ScopeKind, t.CIID, t.TargetPercent, t.Period, t.BudgetWarnPercent,
			t.CalendarID, t.Enabled))
		if err != nil {
			return err
		}
		*t = *stored
		return nil
	})
	return writeErr(err)
}

// UpdateTarget implements AvailabilityRepository.
func (r *PGAvailabilityRepository) UpdateTarget(ctx context.Context, orgID, id string, req *UpdateAvailabilityTargetRequest) (*AvailabilityTarget, error) {
	var out *AvailabilityTarget
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := scanTarget(tx.QueryRow(ctx, `SELECT `+targetColumns+` FROM availability_target
			WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
		if err != nil {
			return err
		}
		if uerr := applyUpdate(cur, req); uerr != nil {
			return uerr
		}
		// The object is immutable and availability_target_ci_scope keeps
		// client and site with the CI (also on a move): the CI is not read
		// again, so the target of a soft-deleted CI stays changeable; only
		// the calendar is checked.
		if cerr := checkCalendarPG(ctx, tx, cur); cerr != nil {
			return cerr
		}
		out, err = scanTarget(tx.QueryRow(ctx, `UPDATE availability_target SET name = $3, target_percent = $4, period = $5,
				budget_warn_percent = $6, calendar_id = $7, enabled = $8
			WHERE organization_id = $1 AND id = $2 RETURNING `+targetColumns,
			orgID, id, cur.Name, cur.TargetPercent, cur.Period, cur.BudgetWarnPercent, cur.CalendarID, cur.Enabled))
		return err
	})
	return out, writeErr(err)
}

// DeleteTarget implements AvailabilityRepository.
func (r *PGAvailabilityRepository) DeleteTarget(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM availability_target WHERE organization_id = $1 AND id = $2`, orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTargetNotFound
		}
		return nil
	})
}
