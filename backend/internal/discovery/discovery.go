// Package discovery provides collector and discovery job management for Reticora CMDB.
package discovery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
	"github.com/go-chi/chi/v5"
)

// Collector represents a customer-deployed collector instance.
type Collector struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	Name           string         `json:"name"`
	Version        string         `json:"version,omitempty"`
	Status         string         `json:"status"`
	LastHeartbeat  string         `json:"last_heartbeat,omitempty"`
	Config         map[string]any `json:"config"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// BulkIngestRequest is the payload sent by a collector for bulk CI data.
type BulkIngestRequest struct {
	CollectorID string       `json:"collector_id"`
	Items       []IngestItem `json:"items"`
}

// IngestItem is a single CI data point from discovery. Newly added identity,
// interface and relationship fields are optional so the JSON contract remains
// backwards compatible with existing collectors.
type IngestItem struct {
	Fingerprint  map[string]any `json:"fingerprint"`
	RawData      map[string]any `json:"raw_data"`
	CITypeName   string         `json:"ci_type_name"`
	Name         string         `json:"name,omitempty"`
	Manufacturer string         `json:"manufacturer,omitempty"`
	Model        string         `json:"model,omitempty"`
	SerialNumber string         `json:"serial_number,omitempty"`
	ManagementIP string         `json:"management_ip,omitempty"`

	// Source is the discovery protocol the item was collected with
	// (snmp|ssh|redfish|ipmi|wmi|api|agent|sweep). It drives the source-trust
	// ranking during conflict resolution (spec §5.3). Empty falls back to
	// "sweep", preserving the previous behavior.
	Source string `json:"source,omitempty"`

	// Extended identity fields used for neighbor resolution and topology.
	HardwareUUID string `json:"hardware_uuid,omitempty"`
	PrimaryMAC   string `json:"primary_mac,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	FQDN         string `json:"fqdn,omitempty"`

	// Interfaces discovered on the CI (MACs feed neighbor resolution).
	Interfaces []wire.IngestInterface `json:"interfaces,omitempty"`

	// Relationships discovered from the CI toward neighbors (L2/L3 topology).
	Relationships []wire.IngestRelationship `json:"relationships,omitempty"`

	// Attributes carries dynamic discovery attributes (spec §1 hybrid model).
	// They feed per-field provenance records (spec §13) without being forced
	// into typed columns.
	Attributes map[string]any `json:"attributes,omitempty"`
}

// BulkIngestResponse is the response for bulk ingest.
type BulkIngestResponse struct {
	Received      int `json:"received"`
	Created       int `json:"created"`
	Updated       int `json:"updated"`
	Conflicts     int `json:"conflicts"`
	ReviewItems   int `json:"review_items"`
	Relationships int `json:"relationships"`
	// ProtectedOverrides counts matched CIs whose protected manual overrides
	// suppressed one or more discovered writes (spec §13).
	ProtectedOverrides int `json:"protected_overrides,omitempty"`
	// ProtectedInstanceFields counts matched CIs where discovered values for
	// instance attributes were not written (MET-14).
	ProtectedInstanceFields int `json:"protected_instance_fields,omitempty"`
	// Unlicensed counts new devices held as unlicensed_ci because max_cis is
	// reached (ENT-03, CH21); already queued devices count again.
	Unlicensed int    `json:"unlicensed,omitempty"`
	JobID      string `json:"job_id,omitempty"`
}

// Repository defines persistence operations for collectors, discovery jobs and
// reconciliation review items.
type Repository interface {
	ListCollectors(ctx context.Context, orgID string, page api.PaginationParams) ([]Collector, int, error)
	RegisterCollector(ctx context.Context, c *Collector) error
	Heartbeat(ctx context.Context, orgID, collectorID string) error

	// Enrollment codes (zero-config onboarding).
	CreateEnrollmentCode(ctx context.Context, code *EnrollmentCode) error
	// RedeemEnrollmentCode validates the raw code, marks it used and returns
	// the owning organization. It must fail for unknown, expired or already
	// used codes.
	RedeemEnrollmentCode(ctx context.Context, rawCode, collectorID string) (orgID string, err error)

	// Discovery jobs.
	ListJobs(ctx context.Context, orgID string, filter JobFilter, page api.PaginationParams) ([]Job, int, error)
	CreateJob(ctx context.Context, j *Job) error
	GetJob(ctx context.Context, orgID, id string) (*Job, error)

	// Reconciliation review queue.
	ListReviewItems(ctx context.Context, orgID string, filter ReviewFilter, page api.PaginationParams) ([]ReviewItem, int, error)
	CreateReviewItem(ctx context.Context, item *ReviewItem) error
	GetReviewItem(ctx context.Context, orgID, id string) (*ReviewItem, error)
	ResolveReviewItem(ctx context.Context, orgID, id string, resolution Resolution) (*ReviewItem, error)
}

// MemoryRepository is an in-memory collector store.
type MemoryRepository struct {
	mu           sync.RWMutex
	collectors   map[string]*Collector
	jobs         map[string]*Job
	reviewItems  map[string]*ReviewItem
	ciTypes      map[string]string
	enrollCodes  map[string]*EnrollmentCode
	enrollHashes map[string]string
	seq          int
}

// NewMemoryRepository creates a new in-memory discovery repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		collectors:  make(map[string]*Collector),
		jobs:        make(map[string]*Job),
		reviewItems: make(map[string]*ReviewItem),
		ciTypes:     make(map[string]string),
	}
}

// SeedCIType registers a ci_type name→id mapping for tests and the --no-db
// development mode, so ingest items can reference types by name.
func (r *MemoryRepository) SeedCIType(name, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ciTypes[name] = id
}

// LookupCITypeID resolves a seeded CI type name to its id.
func (r *MemoryRepository) LookupCITypeID(_ context.Context, _ string, nameOrID string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.ciTypes[nameOrID]; ok {
		return id, nil
	}
	return "", fmt.Errorf("ci type %q not found", nameOrID)
}

func (r *MemoryRepository) ListCollectors(_ context.Context, orgID string, page api.PaginationParams) ([]Collector, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Collector, 0)
	for _, c := range r.collectors {
		if c.OrganizationID != orgID {
			continue
		}
		result = append(result, *c)
	}

	total := len(result)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) RegisterCollector(_ context.Context, c *Collector) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	c.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	c.CreatedAt = now
	c.UpdatedAt = now
	c.Status = "online"
	c.LastHeartbeat = now
	r.collectors[c.ID] = c
	return nil
}

func (r *MemoryRepository) CreateEnrollmentCode(_ context.Context, code *EnrollmentCode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enrollCodes == nil {
		r.enrollCodes = map[string]*EnrollmentCode{}
	}
	r.seq++
	code.ID = fmt.Sprintf("enc-%06d", r.seq)
	code.CreatedAt = time.Now().UTC()
	r.enrollCodes[code.ID] = code
	// rawHash is stored alongside for redemption lookup.
	if r.enrollHashes == nil {
		r.enrollHashes = map[string]string{}
	}
	r.enrollHashes[enrollmentCodeHash(code.rawCode)] = code.ID
	return nil
}

func (r *MemoryRepository) RedeemEnrollmentCode(_ context.Context, rawCode, collectorID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.enrollHashes[enrollmentCodeHash(rawCode)]
	if !ok {
		return "", fmt.Errorf("invalid enrollment code")
	}
	code := r.enrollCodes[id]
	if code == nil || code.UsedAt != nil {
		return "", fmt.Errorf("enrollment code already used")
	}
	if time.Now().UTC().After(code.ExpiresAt) {
		return "", fmt.Errorf("enrollment code expired")
	}
	now := time.Now().UTC()
	code.UsedAt = &now
	return code.OrganizationID, nil
}

func (r *MemoryRepository) Heartbeat(_ context.Context, orgID, collectorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.collectors[collectorID]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	c.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	c.Status = "online"
	return nil
}

// EnrollmentCode is a short-lived, single-use secret a collector presents to
// enroll. Only the SHA-256 hash is persisted; the raw code is carried on the
// struct only transiently between creation and hashing.
type EnrollmentCode struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Label          string     `json:"label,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	UsedAt         *time.Time `json:"used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`

	// rawCode is never serialized; it exists only so the handler can return
	// the code once at creation time.
	rawCode string
}

// SetRawCode assigns the transient plaintext code (creation only).
func (c *EnrollmentCode) SetRawCode(raw string) { c.rawCode = raw }

// RawCode exposes the transient plaintext code for hashing at persistence.
func (c *EnrollmentCode) RawCode() string { return c.rawCode }

// enrollmentCodeHash derives the stored lookup key from the raw code.
func enrollmentCodeHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ProvenanceRecorder records discovered field values with their source
// metadata (spec §13). Satisfied by the override repository; the ingest
// pipeline calls it for every field it applies so the reconciliation layer
// keeps per-field discovered/discovered_source/discovered_at state and never
// overwrites protected manual overrides.
type ProvenanceRecorder interface {
	RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string) (*FieldProvenance, error)
	// Decide is the central, fail-closed write decision for automation
	// sources (override.DecideAutomatedWrite, REC-03): discovery writes a
	// field of an existing CI only when it permits.
	Decide(ctx context.Context, w *override.Write) override.Decision
}

// FieldProvenance is the subset of the override.FieldValue the ingest path
// consumes: it only needs the divergence flag to surface conflicts.
type FieldProvenance struct {
	Diverged bool
}

// Handler provides HTTP handlers for discovery endpoints.
type Handler struct {
	limits     LimitGuard
	repo       Repository
	ciRepo     ci.Repository
	relRepo    relationship.Repository
	typeLookup CITypeLookup
	provenance ProvenanceRecorder
	instance   InstanceFieldSource
}

// CITypeLookup resolves a CI type name or UUID to the canonical ci_type id.
// Implemented by the discovery PG repository; tests may stub it.
type CITypeLookup interface {
	LookupCITypeID(ctx context.Context, orgID, nameOrID string) (string, error)
}

// NewHandler creates a new discovery handler. ciRepo enables reconciliation and
// review-item resolution; the optional relRepo enables topology relationship
// derivation. Both may be nil (e.g. for --no-db smoke tests), in which case
// those features are skipped gracefully.
func NewHandler(repo Repository, ciRepo ci.Repository, relRepo ...relationship.Repository) *Handler {
	h := &Handler{repo: repo, ciRepo: ciRepo}
	if lookup, ok := repo.(CITypeLookup); ok {
		h.typeLookup = lookup
	}
	if len(relRepo) > 0 {
		h.relRepo = relRepo[0]
	}
	return h
}

// WithProvenance attaches the field-provenance recorder (spec §13). Without
// one, ingest keeps its pre-extension behavior (no per-field provenance).
func (h *Handler) WithProvenance(recorder ProvenanceRecorder) *Handler {
	h.provenance = recorder
	return h
}

// WithInstanceFields attaches the source of the CIs' instance attribute
// definitions (MET-14). With it, ingest never writes an instance attribute;
// when the definitions cannot be read, no custom attribute is written at all.
func (h *Handler) WithInstanceFields(source InstanceFieldSource) *Handler {
	h.instance = source
	return h
}

// stripInstanceFields removes the instance attributes of ciID from
// attributes and reports whether any discovered value was withheld. It fails
// closed: without the definitions every discovered custom attribute is
// withheld; the fingerprint and raw data of the ingest stay.
func (h *Handler) stripInstanceFields(ctx context.Context, orgID, ciID string, attributes, discovered map[string]any) bool {
	if h.instance == nil {
		return false
	}
	names, err := h.instance.InstanceFieldNames(ctx, orgID, ciID)
	if err != nil {
		withheld := false
		for k := range discovered {
			if k == "fingerprint" || k == "raw_data" {
				continue
			}
			if _, ok := attributes[k]; ok {
				delete(attributes, k)
				withheld = true
			}
		}
		return withheld
	}
	return len(WithoutInstanceFields(attributes, names)) > 0
}

// recordDiscovered fields the provenance of one applied field. It never
// fails the ingest: provenance is best-effort alongside the audited CI row.
func (h *Handler) recordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string) {
	if h.provenance == nil || ciID == "" {
		return
	}
	_, _ = h.provenance.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source)
}

// decide applies the central write decision; without a provenance store
// (--no-db smoke tests) the rank table alone decides.
func (h *Handler) decide(ctx context.Context, w *override.Write) override.Decision {
	if h.provenance == nil {
		return override.DecideAutomatedWrite(ctx, nil, w)
	}
	return h.provenance.Decide(ctx, w)
}

// queueOverrideConflict opens an override_conflict review for a field unless
// one is open already (REC-12, deduplicated per field). A failure does not
// fail the ingest: the override stays in place either way.
func (h *Handler) queueOverrideConflict(ctx context.Context, orgID, ciID, field string, value any, source string) bool {
	open, _, err := h.repo.ListReviewItems(ctx, orgID, ReviewFilter{Status: ReviewStatusOpen, Kind: ReviewKindOverrideConflict},
		api.PaginationParams{Limit: 1000})
	if err == nil {
		for k := range open {
			if open[k].Payload["ci_id"] == ciID && open[k].Payload["field"] == field {
				return false
			}
		}
	}
	item := &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindOverrideConflict,
		Status:         ReviewStatusOpen,
		Payload:        map[string]any{"ci_id": ciID, "field": field, "observed_value": value, "source": source},
		CandidateCIIDs: []string{ciID},
	}
	if err = h.repo.CreateReviewItem(ctx, item); err != nil {
		slog.WarnContext(ctx, "override conflict review not created", "ci_id", ciID, "field", field, "error", err)
		return false
	}
	return true
}

// isUUID reports whether value looks like a canonical UUID.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

// resolveCITypeIDs maps each item's ci_type_name to the canonical ci_type UUID.
// Entries that are already UUIDs pass through. Items whose type cannot be
// resolved get an empty string so the caller can queue them for review.
func (h *Handler) resolveCITypeIDs(r *http.Request, items []IngestItem) []string {
	ids := make([]string, len(items))
	missing := map[string]struct{}{}
	for i, item := range items {
		name := strings.TrimSpace(item.CITypeName)
		if isUUID(name) {
			ids[i] = name
		} else if name != "" {
			missing[name] = struct{}{}
		}
	}
	if len(missing) == 0 {
		return ids
	}
	t := tenant.FromContext(r.Context())
	for name := range missing {
		if h.typeLookup == nil {
			continue
		}
		id, err := h.typeLookup.LookupCITypeID(r.Context(), t.OrganizationID, name)
		if err != nil {
			continue
		}
		for i, item := range items {
			if strings.TrimSpace(item.CITypeName) == name {
				ids[i] = id
			}
		}
	}
	return ids
}

// RegisterRoutes registers discovery routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/collectors", h.ListCollectors)
	r.Post("/api/v1/collectors", h.RegisterCollector)
	r.Post("/api/v1/collectors/{id}/heartbeat", h.Heartbeat)
	// Zero-config onboarding: an operator mints a short-lived enrollment code;
	// the collector redeems it (unauthenticated, code = the credential) for its
	// identity.
	r.Post("/api/v1/collectors/enrollment-codes", h.CreateEnrollmentCode)
	r.Post("/api/v1/collectors/enroll", h.EnrollCollector)
	r.Post("/api/v1/ingest/bulk", h.BulkIngest)
	// Spec-named alias for the bulk ingest endpoint.
	r.Post("/api/v1/discovery/ingest", h.BulkIngest)

	// Discovery jobs.
	r.Get("/api/v1/discovery/jobs", h.ListJobs)
	r.Post("/api/v1/discovery/jobs", h.CreateJob)
	r.Get("/api/v1/discovery/jobs/{id}", h.GetJob)

	// Reconciliation review queue.
	r.Get("/api/v1/discovery/review-items", h.ListReviewItems)
	r.Post("/api/v1/discovery/review-items/{id}/resolve", h.ResolveReviewItem)
}

// ListCollectors handles GET /api/v1/collectors
func (h *Handler) ListCollectors(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	collectors, total, err := h.repo.ListCollectors(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Collector]{
		Data:    collectors,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// RegisterCollector handles POST /api/v1/collectors
func (h *Handler) RegisterCollector(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var c Collector
	if err := api.ReadJSON(r, &c); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if c.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}

	c.OrganizationID = t.OrganizationID
	if c.Config == nil {
		c.Config = make(map[string]any)
	}

	if err := h.repo.RegisterCollector(r.Context(), &c); err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, c)
}

// CreateEnrollmentCode handles POST /api/v1/collectors/enrollment-codes. It
// mints a single-use code and returns the plaintext exactly once; only the
// hash is stored.
func (h *Handler) CreateEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req struct {
		Label      string `json:"label"`
		TTLMinutes int    `json:"ttl_minutes,omitempty"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ttl := time.Duration(req.TTLMinutes) * time.Minute
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 30 * time.Minute
	}

	raw, err := generateEnrollmentCode()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "failed to generate enrollment code")
		return
	}
	code := &EnrollmentCode{
		OrganizationID: t.OrganizationID,
		Label:          req.Label,
		ExpiresAt:      time.Now().UTC().Add(ttl),
	}
	code.SetRawCode(raw)
	if err := h.repo.CreateEnrollmentCode(r.Context(), code); err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":         code.ID,
		"code":       raw,
		"label":      code.Label,
		"expires_at": code.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// EnrollCollector handles POST /api/v1/collectors/enroll. The enrollment code
// is the credential; no bearer token is required because the collector is not
// enrolled yet. On success a collector identity is registered under the code's
// organization.
func (h *Handler) EnrollCollector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Name     string `json:"name"`
		Version  string `json:"version,omitempty"`
		ClientID string `json:"client_id,omitempty"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "code and name are required")
		return
	}

	// Redeem the code first: it yields the owning organization and fails fast
	// for invalid/expired/used codes. Only then is the collector registered
	// under that tenant.
	orgID, err := h.repo.RedeemEnrollmentCode(r.Context(), req.Code, "")
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}

	c := &Collector{
		OrganizationID: orgID,
		Name:           req.Name,
		Version:        req.Version,
		ClientID:       req.ClientID,
		Config:         map[string]any{},
	}
	// The request is unauthenticated, so there is no principal scope: the
	// redeemed code authorizes registration for its organization (E-08). The
	// client/site binding of codes follows with WP-038.
	scope := database.OrgWideScope(orgID, "")
	ctx := database.ContextWithTenantScope(r.Context(), &scope)
	if err := h.repo.RegisterCollector(ctx, c); err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, c)
}

// generateEnrollmentCode returns a URL-safe, high-entropy single-use code.
func generateEnrollmentCode() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CollectorSpoolReport is the offline spool state a collector sends with its
// heartbeat (NFR-04, COL-05, OPS-06). Dropped counters only grow while the
// collector runs.
type CollectorSpoolReport struct {
	Messages         int   `json:"messages"`
	Bytes            int64 `json:"bytes"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
	DroppedMessages  int64 `json:"dropped_messages"`
	DroppedBytes     int64 `json:"dropped_bytes"`
	Backpressure     bool  `json:"backpressure"`
}

// CollectorHeartbeatRequest is the optional body of a collector heartbeat.
type CollectorHeartbeatRequest struct {
	Spool *CollectorSpoolReport `json:"spool,omitempty"`
}

// Heartbeat handles POST /api/v1/collectors/{id}/heartbeat. The body is
// optional; a spool report with losses or backpressure is logged as an
// operator-visible warning so a silent data loss in the collector cannot go
// unnoticed.
func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CollectorHeartbeatRequest
	if err := api.ReadJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid heartbeat body")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Heartbeat(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "collector not found")
		return
	}
	if s := req.Spool; s != nil && (s.DroppedMessages > 0 || s.Backpressure) {
		slog.WarnContext(r.Context(), "collector reported spool data loss or backpressure",
			"event", "collector.spool.degraded",
			"organization_id", t.OrganizationID, "collector_id", id,
			"spool_messages", s.Messages, "spool_bytes", s.Bytes,
			"spool_oldest_age_seconds", s.OldestAgeSeconds,
			"dropped_messages", s.DroppedMessages, "dropped_bytes", s.DroppedBytes,
			"backpressure", s.Backpressure)
	}

	w.WriteHeader(http.StatusNoContent)
}

// BulkIngest handles POST /api/v1/ingest/bulk.
func (h *Handler) BulkIngest(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req BulkIngestRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if len(req.Items) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "items cannot be empty")
		return
	}

	resp := BulkIngestResponse{Received: len(req.Items)}
	if h.ciRepo == nil {
		api.WriteJSON(w, http.StatusAccepted, resp)
		return
	}

	// Resolve ci_type_name to the canonical ci_type UUID before
	// reconciliation. Collectors send the type name (e.g. "switch"); the CI
	// column stores the UUID. Names are matched against global system types
	// (organization_id IS NULL) and org-specific types. Items whose type can
	// be resolved neither as UUID nor by name are queued for review instead of
	// failing the whole batch with a 500.
	typeIDs := h.resolveCITypeIDs(r, req.Items)
	for i, item := range req.Items {
		if typeIDs[i] != "" {
			continue
		}
		reviewItem := &ReviewItem{
			OrganizationID: t.OrganizationID,
			Kind:           ReviewKindUnclassifiedDevice,
			Status:         ReviewStatusOpen,
			Payload: map[string]any{
				"reason":        "unknown_ci_type",
				"ci_type_name":  item.CITypeName,
				"name":          item.Name,
				"manufacturer":  item.Manufacturer,
				"model":         item.Model,
				"serial_number": item.SerialNumber,
				"management_ip": item.ManagementIP,
				"fingerprint":   item.Fingerprint,
				"raw_data":      item.RawData,
			},
		}
		if err := h.repo.CreateReviewItem(r.Context(), reviewItem); err != nil {
			api.WriteRepoError(w, err)
			return
		}
		resp.ReviewItems++
	}

	existing, total, err := h.ciRepo.List(r.Context(), t.OrganizationID, ci.FilterParams{}, api.PaginationParams{Limit: 10000, Offset: 0})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	// Reconciliation compares stored CIs (UUID type) against the incoming
	// item, so the item must carry the resolved UUID as well.
	for i := range req.Items {
		if typeIDs[i] != "" {
			req.Items[i].CITypeName = typeIDs[i]
		}
	}

	// resolvedCIID[i] holds the CI id that item i resolved to (matched or
	// created). Conflicts leave it empty so no topology is derived for them.
	resolvedCIID := make([]string, len(req.Items))
	var unlicensed queuedDevices

	for i, item := range req.Items {
		if typeIDs[i] == "" {
			// Unresolvable CI type: already queued for review above.
			continue
		}
		result := Reconcile(existing, item)
		now := time.Now().UTC().Format(time.RFC3339)
		source := item.Source
		if source == "" {
			source = ci.SourceSweep
		}
		// Discovered custom attributes are persisted on the CI itself so they
		// are visible, searchable and filterable like any other attribute; the
		// per-field provenance recorded below keeps the reporting source and
		// timestamp available for reconciliation.
		attributes := discoveredAttributes(&item)

		switch result.Action {
		case ReconcileCreated:
			// Over max_cis the device is held as unlicensed_ci instead of
			// becoming a CI (ENT-03, CH21); existing CIs keep updating.
			if h.limits != nil {
				queued, admitErr := h.admitNewCI(r.Context(), t.OrganizationID, int64(total), &item, typeIDs[i], source, &unlicensed)
				if admitErr != nil {
					api.WriteRepoError(w, admitErr)
					return
				}
				if queued {
					resp.Unlicensed++
					continue
				}
			}
			newItem, err := h.createDiscoveredCI(r.Context(), t.OrganizationID, &item, typeIDs[i], source)
			if err != nil {
				api.WriteRepoError(w, err)
				return
			}
			total++
			existing = append(existing, newItem)
			resolvedCIID[i] = newItem.ID
			resp.Created++
		case ReconcileMatched:
			matched := findCI(existing, result.MatchedCIID)
			if matched == nil {
				matched = &ci.Item{ID: result.MatchedCIID}
			}
			observedAt := time.Now().UTC()
			// Every field goes through the central write decision (REC-03):
			// overrides are never overwritten, ranks and observation times
			// decide the rest, unreadable state writes nothing.
			conflict := false
			decide := func(field string, value, current any) bool {
				d := h.decide(r.Context(), &override.Write{
					OrganizationID: t.OrganizationID, CIID: matched.ID, Field: field, Value: value,
					Source: source, ObservedAt: observedAt, Current: current, FallbackSource: matched.DiscoverySource,
				})
				if d.Write || d.Conflict || d.Reason == override.ReasonOverride {
					// Observed under an override or written: the provenance
					// keeps the reported value (REC-12, REC-10).
					h.recordDiscovered(r.Context(), t.OrganizationID, matched.ID, field, value, source)
				}
				if d.Conflict {
					conflict = true
					if h.queueOverrideConflict(r.Context(), t.OrganizationID, matched.ID, field, value, source) {
						resp.ReviewItems++
					}
				}
				return d.Write
			}
			update := ci.UpdateRequest{LastSeenAt: &now}
			if override.SourceRank(source) >= override.SourceRank(matched.DiscoverySource) {
				update.DiscoverySource = &source
			}
			for _, f := range []struct {
				name           string
				value, current string
				target         **string
			}{
				{"name", item.Name, matched.Name, &update.Name},
				{"manufacturer", item.Manufacturer, matched.Manufacturer, &update.Manufacturer},
				{"model", item.Model, matched.Model, &update.Model},
				{"serial_number", item.SerialNumber, matched.SerialNumber, &update.SerialNumber},
				{"management_ip", item.ManagementIP, matched.ManagementIP, &update.ManagementIP},
			} {
				if f.value == "" || f.value == f.current {
					continue
				}
				if decide(f.name, f.value, f.current) {
					*f.target = stringPtr(f.value)
				}
			}
			writes := map[string]any{
				"fingerprint": mergeFingerprint(existingFingerprintMap(*matched), item.Fingerprint,
					override.SourceRank(source) >= override.SourceRank(matched.DiscoverySource)),
				"raw_data": item.RawData,
			}
			for k, v := range attributes {
				if k == "fingerprint" || k == "raw_data" {
					continue
				}
				if current, ok := matched.Attributes[k]; ok && reflect.DeepEqual(current, v) {
					continue
				}
				if decide(k, v, matched.Attributes[k]) {
					writes[k] = v
				}
			}
			// Instance attributes of this CI are never written (MET-14).
			if h.stripInstanceFields(r.Context(), t.OrganizationID, matched.ID, writes, item.Attributes) {
				resp.ProtectedInstanceFields++
			}
			update.Attributes = writes
			updated, err := h.ciRepo.Update(r.Context(), t.OrganizationID, result.MatchedCIID, update)
			if err != nil {
				api.WriteRepoError(w, err)
				return
			}
			if conflict {
				resp.ProtectedOverrides++
			}
			for j := range existing {
				if existing[j].ID == updated.ID {
					existing[j] = *updated
					break
				}
			}
			resolvedCIID[i] = updated.ID
			resp.Updated++
			// Contradicting identity values on a matched CI (e.g. a changed
			// serial number) are queued for operator review instead of being
			// silently applied (spec §5.3: Unklarheiten in Review-Queue).
			if len(result.ValueConflicts) > 0 && matched != nil {
				if reviewItem := reviewItemFromValueConflicts(t.OrganizationID, *matched, item, result); reviewItem != nil {
					if err := h.repo.CreateReviewItem(r.Context(), reviewItem); err != nil {
						api.WriteRepoError(w, err)
						return
					}
					resp.ReviewItems++
				}
			}
		case ReconcileConflict:
			resp.Conflicts++
			if reviewItem := reviewItemFromConflict(t.OrganizationID, item, result); reviewItem != nil {
				if err := h.repo.CreateReviewItem(r.Context(), reviewItem); err != nil {
					api.WriteRepoError(w, err)
					return
				}
				resp.ReviewItems++
			}
		}
	}

	resp.Relationships = h.deriveTopology(r.Context(), t.OrganizationID, req.Items, resolvedCIID, existing)

	api.WriteJSON(w, http.StatusAccepted, resp)
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// findCI returns the CI with the given id from the cached list, or nil.
func findCI(existing []ci.Item, id string) *ci.Item {
	for j := range existing {
		if existing[j].ID == id {
			return &existing[j]
		}
	}
	return nil
}

// mergeFingerprint combines the stored and the incoming fingerprint. New
// keys are always added; for known keys the incoming value wins only when
// the source ranks at least as high as the CI's source, so a low-rank sweep
// cannot rewrite BMC-grade identity data used for matching.
func mergeFingerprint(stored, incoming map[string]any, incomingWins bool) map[string]any {
	merged := map[string]any{}
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range incoming {
		if _, known := merged[k]; !known || incomingWins {
			merged[k] = v
		}
	}
	return merged
}

// existingFingerprintMap extracts the stored fingerprint map of a CI,
// accepting both the nested attributes.fingerprint layout and top-level
// attribute keys written by older ingest versions.
func existingFingerprintMap(item ci.Item) map[string]any {
	if item.Attributes == nil {
		return nil
	}
	if nested, ok := item.Attributes["fingerprint"]; ok {
		if fingerprintMap, ok := nested.(map[string]any); ok {
			return fingerprintMap
		}
	}
	return nil
}

func firstNonEmpty(preferred, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return preferred
	}
	return fallback
}
