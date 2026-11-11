package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Target kinds of a binding (CRD-01). device_group and probe are reserved
// until WP-935 (p3-probe-schema-def) adds their tables.
const (
	TargetCI          = "ci"
	TargetDeviceGroup = "device_group"
	TargetSubnet      = "subnet"
	TargetProbe       = "probe"
)

// TargetKinds are the target kinds of CRD-01.
var TargetKinds = []string{TargetCI, TargetDeviceGroup, TargetSubnet, TargetProbe}

// supportedTargets are the target kinds usable now.
var supportedTargets = []string{TargetCI, TargetSubnet}

// DefaultBindingPriority is the priority of a binding without one.
const DefaultBindingPriority = 100

// maxBindingPriority bounds the priority (CHECK credential_binding_priority_check).
const maxBindingPriority = 10000

// maxListedBindings is how many binding IDs a refused credential deletion
// names.
const maxListedBindings = 50

// KindNamespace maps a credential kind to the protocol namespace of the
// collector (credentialNamespace in collector/collectorcmd/bundle.go); a
// binding's namespace must be that of its credential's kind.
var KindNamespace = map[string]string{
	KindSNMPv2c: "snmp", KindSNMPv3: "snmp", KindSSHPassword: "ssh", KindSSHKey: "ssh",
	KindRedfish: "redfish", KindIPMI: "ipmi", KindWMI: "wmi", KindAPIToken: "api", KindNUT: "nut",
	KindNAS: "nas",
}

// Binding binds a credential to a target for one namespace with a priority
// (CRD-01, WP-890). client_id and site_id are those of the target.
type Binding struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	CredentialID   string    `json:"credential_id"`
	TargetKind     string    `json:"target_kind"`
	TargetID       string    `json:"target_id"`
	Namespace      string    `json:"namespace"`
	Priority       int       `json:"priority"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// BindingFilter narrows a binding listing.
type BindingFilter struct {
	CredentialID string
	TargetKind   string
	TargetID     string
}

// BindingUpdate is a PATCH of namespace and priority.
type BindingUpdate struct {
	Namespace *string
	Priority  *int
}

// Errors of the binding store; the handler maps them to 404, 409 and 422.
var (
	ErrBindingNotFound = errors.New("credential binding not found")
	// ErrBindingConflict: the target already has a binding of the namespace
	// with the priority (409).
	ErrBindingConflict = errors.New("the target already has a binding of this namespace with this priority")
	// ErrTargetUnknown: the target does not exist, belongs to another
	// organization or lies outside the caller's scope (422 target_id).
	ErrTargetUnknown = errors.New("target not found")
	// ErrClientRule: a credential of a client bound to a target of another
	// client (422 target_id, without telling foreign and unknown apart).
	ErrClientRule = errors.New("a credential of a client binds only to a target of the same client")
)

// BindingRepository stores credential bindings; PGRepository and the
// memory store of this file implement it.
type BindingRepository interface {
	CreateBinding(ctx context.Context, b *Binding) error
	ListBindings(ctx context.Context, orgID string, f BindingFilter) ([]Binding, error)
	GetBinding(ctx context.Context, orgID, id string) (*Binding, error)
	UpdateBinding(ctx context.Context, orgID, id string, u BindingUpdate) (*Binding, error)
	DeleteBinding(ctx context.Context, orgID, id string) error
	// ReplaceCIBindings replaces every binding of the CI in one transaction.
	ReplaceCIBindings(ctx context.Context, orgID, ciID string, bindings []Binding) ([]Binding, error)
	// CIVisible reports whether the CI exists in the caller's scope.
	CIVisible(ctx context.Context, orgID, ciID string) (bool, error)
}

// BindingService validates and stores bindings.
type BindingService struct {
	creds    *Service
	bindings BindingRepository
	// groups is the membership source of the device-group level of the
	// resolution (WP-891; empty until WP-935).
	groups DeviceGroupSource
}

// Bindings returns the binding service (WP-890).
// A service without a repository of bindings uses a memory store (--no-db,
// parity of the routes).
func (s *Service) Bindings() *BindingService { return s.bindings }

// BindingIDsOfCredential lists up to maxListedBindings binding IDs of the
// credential.
func (b *BindingService) BindingIDsOfCredential(ctx context.Context, orgID, credentialID string) ([]string, error) {
	list, err := b.bindings.ListBindings(ctx, orgID, BindingFilter{CredentialID: credentialID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, min(len(list), maxListedBindings))
	for i := range list {
		if len(ids) == maxListedBindings {
			break
		}
		ids = append(ids, list[i].ID)
	}
	return ids, nil
}

// validate checks a new binding: target kind, namespace of the credential's
// kind and priority. The credential is read in the caller's scope; an
// unknown or invisible one is 422 credential_id.
func (b *BindingService) validate(ctx context.Context, orgID string, in *Binding) error {
	switch {
	case !slices.Contains(TargetKinds, in.TargetKind):
		return &api.FieldError{Field: "target_kind", Status: http.StatusUnprocessableEntity,
			Message: "target_kind must be one of ci, device_group, subnet, probe"}
	case !slices.Contains(supportedTargets, in.TargetKind):
		return &api.FieldError{Field: "target_kind", Status: http.StatusUnprocessableEntity,
			Message: "target kind not supported yet"}
	case !api.IsUUID(in.TargetID):
		return &api.FieldError{Field: "target_id", Status: http.StatusUnprocessableEntity, Message: ErrTargetUnknown.Error()}
	case in.Priority < 0 || in.Priority > maxBindingPriority:
		return &api.FieldError{Field: "priority", Status: http.StatusUnprocessableEntity,
			Message: "priority must be between 0 and " + strconv.Itoa(maxBindingPriority)}
	}
	cred, err := b.creds.Get(ctx, orgID, in.CredentialID)
	if errors.Is(err, ErrNotFound) {
		return &api.FieldError{Field: "credential_id", Status: http.StatusUnprocessableEntity, Message: "credential not found"}
	}
	if err != nil {
		return err
	}
	if KindNamespace[cred.Kind] == "" {
		// The vault logins (WP-893) are used by vault connections only.
		return &api.FieldError{Field: "credential_id", Status: http.StatusUnprocessableEntity,
			Message: "a credential of kind " + cred.Kind + " is not bound to targets"}
	}
	if in.Namespace == "" {
		in.Namespace = KindNamespace[cred.Kind]
	}
	if KindNamespace[cred.Kind] != in.Namespace {
		return &api.FieldError{Field: "namespace", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("namespace must be %q for a credential of kind %s", KindNamespace[cred.Kind], cred.Kind)}
	}
	return nil
}

// Create stores a binding.
func (b *BindingService) Create(ctx context.Context, orgID string, in *Binding) (*Binding, error) {
	in.OrganizationID = orgID
	if err := b.validate(ctx, orgID, in); err != nil {
		return nil, err
	}
	if err := b.bindings.CreateBinding(ctx, in); err != nil {
		return nil, err
	}
	return in, nil
}

// Update changes namespace and priority.
func (b *BindingService) Update(ctx context.Context, orgID, id string, u BindingUpdate) (*Binding, error) {
	current, err := b.bindings.GetBinding(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	next := *current
	if u.Namespace != nil {
		next.Namespace = *u.Namespace
	}
	if u.Priority != nil {
		next.Priority = *u.Priority
	}
	if validErr := b.validate(ctx, orgID, &next); validErr != nil {
		return nil, validErr
	}
	return b.bindings.UpdateBinding(ctx, orgID, id, BindingUpdate{Namespace: &next.Namespace, Priority: &next.Priority})
}

// ReplaceCI replaces the bindings of a CI.
func (b *BindingService) ReplaceCI(ctx context.Context, orgID, ciID string, in []Binding) ([]Binding, error) {
	visible, err := b.bindings.CIVisible(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrBindingNotFound
	}
	for i := range in {
		in[i].OrganizationID = orgID
		in[i].TargetKind = TargetCI
		in[i].TargetID = ciID
		if err = b.validate(ctx, orgID, &in[i]); err != nil {
			var fe *api.FieldError
			if errors.As(err, &fe) {
				fe.Field = fmt.Sprintf("bindings[%d].%s", i, fe.Field)
			}
			return nil, err
		}
	}
	return b.bindings.ReplaceCIBindings(ctx, orgID, ciID, in)
}

// --- PostgreSQL -----------------------------------------------------------

const bindingColumns = `id::text, organization_id::text, credential_id::text, target_kind,
	COALESCE(ci_id::text, subnet_id::text, ''), namespace, priority,
	COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), created_at, updated_at`

func scanBinding(row pgx.Row) (*Binding, error) {
	var b Binding
	err := row.Scan(&b.ID, &b.OrganizationID, &b.CredentialID, &b.TargetKind, &b.TargetID, &b.Namespace, &b.Priority,
		&b.ClientID, &b.SiteID, &b.CreatedAt, &b.UpdatedAt)
	return &b, err
}

// targetColumns returns the ci_id and subnet_id of a binding.
func targetColumns(b *Binding) (ci, subnet any) {
	switch b.TargetKind {
	case TargetCI:
		return b.TargetID, nil
	case TargetSubnet:
		return nil, b.TargetID
	}
	return nil, nil
}

// bindingError maps the constraints of credential_binding.
func bindingError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "uq_credential_binding_ci", "uq_credential_binding_subnet":
		return ErrBindingConflict
	case "credential_binding_client_rule":
		return ErrClientRule
	case "credential_binding_ci_fkey", "credential_binding_subnet_fkey":
		return ErrTargetUnknown
	case "credential_binding_credential_fkey":
		return &api.FieldError{Field: "credential_id", Status: http.StatusUnprocessableEntity, Message: "credential not found"}
	}
	return err
}

// targetVisible checks the target in the caller's scope (RLS).
func targetVisible(ctx context.Context, tx pgx.Tx, b *Binding) error {
	table := "ci"
	if b.TargetKind == TargetSubnet {
		table = "subnet"
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, b.TargetID).Scan(&exists); err != nil {
		return fmt.Errorf("check target: %w", err)
	}
	if !exists {
		return ErrTargetUnknown
	}
	return nil
}

func insertBinding(ctx context.Context, tx pgx.Tx, b *Binding) error {
	if err := targetVisible(ctx, tx, b); err != nil {
		return err
	}
	ci, subnet := targetColumns(b)
	stored, err := scanBinding(tx.QueryRow(ctx, `INSERT INTO credential_binding
		(organization_id, credential_id, target_kind, ci_id, subnet_id, namespace, priority)
		VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6) RETURNING `+bindingColumns,
		b.CredentialID, b.TargetKind, ci, subnet, b.Namespace, b.Priority))
	if err != nil {
		return bindingError(err)
	}
	*b = *stored
	return nil
}

// CreateBinding implements BindingRepository.
func (r *PGRepository) CreateBinding(ctx context.Context, b *Binding) error {
	return database.WithRequestTenant(ctx, r.pool, b.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return insertBinding(ctx, tx, b)
	})
}

// ListBindings implements BindingRepository.
func (r *PGRepository) ListBindings(ctx context.Context, orgID string, f BindingFilter) ([]Binding, error) {
	out := []Binding{}
	for _, id := range []string{f.CredentialID, f.TargetID} {
		if id != "" && !api.IsUUID(id) {
			return out, nil
		}
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+bindingColumns+` FROM credential_binding
			WHERE ($1 = '' OR credential_id::text = $1) AND ($2 = '' OR target_kind = $2)
			  AND ($3 = '' OR ci_id::text = $3 OR subnet_id::text = $3)
			ORDER BY created_at DESC, id DESC`, f.CredentialID, f.TargetKind, f.TargetID)
		if err != nil {
			return fmt.Errorf("list credential bindings: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			b, scanErr := scanBinding(rows)
			if scanErr != nil {
				return fmt.Errorf("scan credential binding: %w", scanErr)
			}
			out = append(out, *b)
		}
		return rows.Err()
	})
	return out, err
}

// GetBinding implements BindingRepository.
func (r *PGRepository) GetBinding(ctx context.Context, orgID, id string) (*Binding, error) {
	if !api.IsUUID(id) {
		return nil, ErrBindingNotFound
	}
	var out *Binding
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		b, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM credential_binding WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBindingNotFound
		}
		out = b
		return err
	})
	return out, err
}

// UpdateBinding implements BindingRepository.
func (r *PGRepository) UpdateBinding(ctx context.Context, orgID, id string, u BindingUpdate) (*Binding, error) {
	if !api.IsUUID(id) {
		return nil, ErrBindingNotFound
	}
	var out *Binding
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		b, err := scanBinding(tx.QueryRow(ctx, `UPDATE credential_binding SET namespace = COALESCE($2, namespace),
			priority = COALESCE($3, priority) WHERE id = $1 RETURNING `+bindingColumns, id, u.Namespace, u.Priority))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBindingNotFound
		}
		if err != nil {
			return bindingError(err)
		}
		out = b
		return nil
	})
	return out, err
}

// DeleteBinding implements BindingRepository.
func (r *PGRepository) DeleteBinding(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrBindingNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM credential_binding WHERE id = $1`, id)
		if err != nil {
			return fmt.Errorf("delete credential binding: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrBindingNotFound
		}
		return nil
	})
}

// ReplaceCIBindings implements BindingRepository.
func (r *PGRepository) ReplaceCIBindings(ctx context.Context, orgID, ciID string, bindings []Binding) ([]Binding, error) {
	out := make([]Binding, 0, len(bindings))
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM credential_binding WHERE ci_id = $1`, ciID); err != nil {
			return fmt.Errorf("replace credential bindings: %w", err)
		}
		for i := range bindings {
			b := bindings[i]
			if err := insertBinding(ctx, tx, &b); err != nil {
				return err
			}
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CIVisible implements BindingRepository.
func (r *PGRepository) CIVisible(ctx context.Context, orgID, ciID string) (bool, error) {
	if !api.IsUUID(ciID) {
		return false, nil
	}
	var exists bool
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1)`, ciID).Scan(&exists)
	})
	return exists, err
}

// --- memory (--no-db) -----------------------------------------------------

// memoryBindings keeps bindings in memory; it knows no targets, so every
// target counts as present and of no client.
type memoryBindings struct {
	mu   sync.Mutex
	rows map[string]*Binding
}

func newMemoryBindings() *memoryBindings { return &memoryBindings{rows: map[string]*Binding{}} }

func (m *memoryBindings) conflict(b *Binding) bool {
	for _, other := range m.rows {
		if other.ID != b.ID && other.OrganizationID == b.OrganizationID && other.TargetKind == b.TargetKind &&
			other.TargetID == b.TargetID && other.Namespace == b.Namespace && other.Priority == b.Priority {
			return true
		}
	}
	return false
}

func (m *memoryBindings) CreateBinding(_ context.Context, b *Binding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conflict(b) {
		return ErrBindingConflict
	}
	id, err := generateID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	b.ID, b.CreatedAt, b.UpdatedAt = id, now, now
	stored := *b
	m.rows[id] = &stored
	return nil
}

func (m *memoryBindings) ListBindings(_ context.Context, orgID string, f BindingFilter) ([]Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Binding{}
	for _, b := range m.rows {
		if b.OrganizationID == orgID && (f.CredentialID == "" || b.CredentialID == f.CredentialID) &&
			(f.TargetKind == "" || b.TargetKind == f.TargetKind) && (f.TargetID == "" || b.TargetID == f.TargetID) {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memoryBindings) GetBinding(_ context.Context, orgID, id string) (*Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok || b.OrganizationID != orgID {
		return nil, ErrBindingNotFound
	}
	out := *b
	return &out, nil
}

func (m *memoryBindings) UpdateBinding(_ context.Context, orgID, id string, u BindingUpdate) (*Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok || b.OrganizationID != orgID {
		return nil, ErrBindingNotFound
	}
	next := *b
	if u.Namespace != nil {
		next.Namespace = *u.Namespace
	}
	if u.Priority != nil {
		next.Priority = *u.Priority
	}
	if m.conflict(&next) {
		return nil, ErrBindingConflict
	}
	next.UpdatedAt = time.Now().UTC()
	m.rows[id] = &next
	out := next
	return &out, nil
}

func (m *memoryBindings) DeleteBinding(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok || b.OrganizationID != orgID {
		return ErrBindingNotFound
	}
	delete(m.rows, id)
	return nil
}

func (m *memoryBindings) ReplaceCIBindings(ctx context.Context, orgID, ciID string, bindings []Binding) ([]Binding, error) {
	m.mu.Lock()
	for id, b := range m.rows {
		if b.OrganizationID == orgID && b.TargetKind == TargetCI && b.TargetID == ciID {
			delete(m.rows, id)
		}
	}
	m.mu.Unlock()
	out := make([]Binding, 0, len(bindings))
	for i := range bindings {
		b := bindings[i]
		if err := m.CreateBinding(ctx, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (m *memoryBindings) CIVisible(context.Context, string, string) (bool, error) { return true, nil }

// --- HTTP -------------------------------------------------------------------

// bindingRequest is the body of POST /credential-bindings.
type bindingRequest struct {
	CredentialID string `json:"credential_id"`
	TargetKind   string `json:"target_kind"`
	TargetID     string `json:"target_id"`
	Namespace    string `json:"namespace"`
	Priority     *int   `json:"priority"`
}

// ciBindingRequest is one entry of PUT /cis/{id}/credentials.
type ciBindingRequest struct {
	CredentialID string `json:"credential_id"`
	Namespace    string `json:"namespace"`
	Priority     *int   `json:"priority"`
}

func priorityOrDefault(p *int) int {
	if p == nil {
		return DefaultBindingPriority
	}
	return *p
}

// registerBindingRoutes mounts the binding routes (CRD-01, WP-890).
func (h *Handler) registerBindingRoutes(r chi.Router) {
	r.Route("/api/v1/credential-bindings", func(r chi.Router) {
		r.Post("/", h.createBinding)
		r.Get("/", h.listBindings)
		r.Get("/{id}", h.getBinding)
		r.Patch("/{id}", h.updateBinding)
		r.Delete("/{id}", h.deleteBinding)
	})
	r.Get("/api/v1/cis/{id}/credentials", h.listCIBindings)
	r.Put("/api/v1/cis/{id}/credentials", h.replaceCIBindings)
	r.Get("/api/v1/cis/{id}/credentials/effective", h.effectiveCredentials)
}

// writeBindingError maps the errors of the binding service.
func writeBindingError(w http.ResponseWriter, err error) {
	var fe *api.FieldError
	switch {
	case errors.Is(err, ErrBindingNotFound):
		writeError(w, http.StatusNotFound, "credential binding not found")
	case errors.Is(err, ErrBindingConflict):
		api.WriteFieldError(w, &api.ConflictError{Field: "priority", Message: ErrBindingConflict.Error()})
	case errors.Is(err, ErrTargetUnknown), errors.Is(err, ErrClientRule):
		// A foreign and an unknown target are not told apart (AK3).
		writeFieldError(w, "target_id", "target not found or not bindable for this credential")
	case errors.As(err, &fe):
		api.WriteFieldError(w, fe)
	default:
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) createBinding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var req bindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	b := Binding{CredentialID: req.CredentialID, TargetKind: req.TargetKind, TargetID: req.TargetID,
		Namespace: req.Namespace, Priority: priorityOrDefault(req.Priority)}
	created, err := h.svc.Bindings().Create(r.Context(), orgID, &b)
	if err != nil {
		writeBindingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) listBindings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	list, err := h.svc.Bindings().bindings.ListBindings(r.Context(), orgID, BindingFilter{
		CredentialID: q.Get("credential_id"), TargetKind: q.Get("target_kind"), TargetID: q.Get("target_id")})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(list, page))
}

func (h *Handler) getBinding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	b, err := h.svc.Bindings().bindings.GetBinding(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeBindingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, b)
}

func (h *Handler) updateBinding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var u BindingUpdate
	for field, raw := range body {
		switch field {
		case "namespace":
			var ns string
			if err := json.Unmarshal(raw, &ns); err != nil {
				writeFieldError(w, "namespace", "namespace must be a string")
				return
			}
			u.Namespace = &ns
		case "priority":
			var p int
			if err := json.Unmarshal(raw, &p); err != nil {
				writeFieldError(w, "priority", "priority must be an integer")
				return
			}
			u.Priority = &p
		default:
			writeFieldError(w, field, "only namespace and priority can be changed")
			return
		}
	}
	b, err := h.svc.Bindings().Update(r.Context(), orgID, chi.URLParam(r, "id"), u)
	if err != nil {
		writeBindingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, b)
}

func (h *Handler) deleteBinding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Bindings().bindings.DeleteBinding(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeBindingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listCIBindings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	ciID := chi.URLParam(r, "id")
	svc := h.svc.Bindings()
	visible, err := svc.bindings.CIVisible(r.Context(), orgID, ciID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "ci not found")
		return
	}
	list, err := svc.bindings.ListBindings(r.Context(), orgID, BindingFilter{TargetKind: TargetCI, TargetID: ciID})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (h *Handler) replaceCIBindings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var req struct {
		Bindings []ciBindingRequest `json:"bindings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in := make([]Binding, 0, len(req.Bindings))
	for _, b := range req.Bindings {
		in = append(in, Binding{CredentialID: b.CredentialID, Namespace: b.Namespace, Priority: priorityOrDefault(b.Priority)})
	}
	list, err := h.svc.Bindings().ReplaceCI(r.Context(), orgID, chi.URLParam(r, "id"), in)
	if errors.Is(err, ErrBindingNotFound) {
		writeError(w, http.StatusNotFound, "ci not found")
		return
	}
	if err != nil {
		writeBindingError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": list})
}

// effectiveCredentials answers GET /cis/{id}/credentials/effective (CRD-04,
// WP-891): per namespace the effective credential (id, name, kind), its
// origin and the overridden candidates; never a secret. An invisible or
// unknown CI is 404.
func (h *Handler) effectiveCredentials(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	ciID := chi.URLParam(r, "id")
	list, err := h.svc.Bindings().Effective(r.Context(), orgID, ciID)
	if errors.Is(err, ErrBindingNotFound) {
		writeError(w, http.StatusNotFound, "ci not found")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"ci_id": ciID, "data": list})
}

// bindingsProblem is the 409 of a credential deletion with bindings.
type bindingsProblem struct {
	api.ProblemDetail
	Bindings []string `json:"bindings"`
}

// refuseBoundDeletion answers 409 with the binding IDs (at most 50) when the
// credential still has bindings; true when it answered.
func (h *Handler) refuseBoundDeletion(w http.ResponseWriter, r *http.Request, orgID, id string) bool {
	ids, err := h.svc.Bindings().BindingIDsOfCredential(r.Context(), orgID, id)
	if err != nil || len(ids) == 0 {
		return false
	}
	api.WriteProblemBody(w, http.StatusConflict, bindingsProblem{
		ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict", Status: http.StatusConflict,
			Detail: "the credential is bound; delete its bindings first"},
		Bindings: ids,
	})
	return true
}

var _ BindingRepository = (*PGRepository)(nil)
