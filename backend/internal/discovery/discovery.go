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
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
	"github.com/go-chi/chi/v5"
)

// Collector represents a customer-deployed collector instance (COL-01).
type Collector struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	// SiteID binds the collector to a site of its client; the site decides
	// the client.
	SiteID  string `json:"site_id,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Status is pending, active, offline, disabled or paused
	// (collector_status.go).
	Status          string         `json:"status"`
	StatusChangedAt string         `json:"status_changed_at,omitempty"`
	LastHeartbeatAt string         `json:"last_heartbeat_at,omitempty"`
	CertFingerprint string         `json:"cert_fingerprint,omitempty"`
	CertNotAfter    string         `json:"cert_not_after,omitempty"`
	Config          map[string]any `json:"config"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

// BulkIngestRequest is the deprecated collector_id/items ingest payload; new
// collectors send DeviceRecords (DIS-05, DeviceRecordBatch).
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
	// SysObjectID identifies the SNMP device model; with the management IP
	// and VRF it identifies a device (REC-02).
	SysObjectID string `json:"sys_object_id,omitempty"`

	// Interfaces discovered on the CI (MACs feed neighbor resolution).
	Interfaces []wire.IngestInterface `json:"interfaces,omitempty"`

	// Relationships discovered from the CI toward neighbors (L2/L3 topology).
	Relationships []wire.IngestRelationship `json:"relationships,omitempty"`

	// RecordID, ObservedAt and the scope fields come from a DeviceRecord
	// (DIS-05); the deprecated items contract cannot set them.
	RecordID    string    `json:"-"`
	CollectorID string    `json:"-"`
	ObservedAt  time.Time `json:"-"`
	VRFID       string    `json:"-"`
	ScopeID     string    `json:"-"`
	SiteID      string    `json:"-"`
	ClientID    string    `json:"-"`
	// seq is the position of the record among the messages ProcessMessages
	// processes, plus one; 0 elsewhere. A record failure names it (WP-481).
	seq int

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
	// InvalidValues counts discovered values the field metadata or the data
	// type of their typed column rejected; they are not stored, the rest of
	// their record is (CI-03, MET-11, WP-501).
	InvalidValues int `json:"invalid_values,omitempty"`
	// Duplicates counts records whose record_id was processed within the
	// last 48 hours; they change nothing (REC-01).
	Duplicates int `json:"duplicates,omitempty"`
	// Resurrected counts records that match a soft-deleted CI; they are held
	// in a resurrected_device review instead of creating a CI (CI-13).
	Resurrected int `json:"resurrected,omitempty"`
	// Queued counts records published to the ingest.> queue; they are
	// processed asynchronously (REC-01).
	Queued int `json:"queued,omitempty"`
	// RejectedFuture counts records observed more than five minutes in the
	// future; they change nothing (GLO-12).
	RejectedFuture int `json:"rejected_future,omitempty"`
	// Unlicensed counts new devices held as unlicensed_ci because max_cis is
	// reached (ENT-03, CH21); already queued devices count again.
	Unlicensed int    `json:"unlicensed,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	// Accepted counts the records processed, deduplicated or queued;
	// Rejected those refused with an error, each listed in Errors. A record
	// that fails does not take the others of its request down (REC-01,
	// WP-481).
	Accepted int                 `json:"accepted"`
	Rejected int                 `json:"rejected"`
	Errors   []IngestRecordError `json:"errors,omitempty"`
}

// Repository defines persistence operations for collectors, discovery jobs and
// reconciliation review items.
type Repository interface {
	ListCollectors(ctx context.Context, orgID string, page api.PaginationParams) ([]Collector, int, error)
	RegisterCollector(ctx context.Context, c *Collector) error
	// CountCollectors counts the collectors of the organization that are
	// not disabled, the stock max_collectors is checked against (ENT-03).
	// It counts organization-wide, whatever the caller's client or site
	// scope.
	CountCollectors(ctx context.Context, orgID string) (int64, error)
	// Heartbeat records a heartbeat and returns the collector's status: a
	// pending or offline collector becomes active, a paused one stays
	// paused, a disabled one is refused (ErrCollectorDisabled).
	Heartbeat(ctx context.Context, orgID, collectorID string) (status string, err error)
	// UpdateCollector changes name, site or status (disable, enable); an
	// unknown collector or one of another organization fails with
	// api.ErrNotFound. A disable revokes the collector's certificates and,
	// once committed, is announced to the edge workers; one of them cuts its
	// open NATS connections (PGRepository.WithDisableAnnouncer, COL-04).
	UpdateCollector(ctx context.Context, orgID, collectorID string, patch *CollectorPatch) (*Collector, error)
	// CollectorStatus returns the status of a collector of the
	// organization; an unknown collector fails with api.ErrNotFound.
	CollectorStatus(ctx context.Context, orgID, collectorID string) (string, error)

	// Enrollment codes (zero-config onboarding).
	CreateEnrollmentCode(ctx context.Context, code *EnrollmentCode) error
	// EnrollCollector consumes the raw code and registers the collector
	// (active) in the code's organization, atomically: it fails for
	// unknown, expired or already used codes, and concurrent enrollments
	// with one code register exactly one collector (COL-04).
	// With issue, the collector's first certificate is issued (CN =
	// collector id) and recorded in the same transaction (COL-04).
	EnrollCollector(ctx context.Context, rawCode string, c *Collector, issue CertIssuer) (*IssuedCert, error)
	// EnrollCollectorWithQuota is EnrollCollector that first checks
	// max_collectors of the code's organization with quota (nil: no
	// check), in the same transaction and before the code is consumed: a
	// refused enrollment returns the guard's error, registers nothing and
	// leaves the code valid (ENT-03).
	EnrollCollectorWithQuota(ctx context.Context, rawCode string, c *Collector, issue CertIssuer, quota QuotaGuard) (*IssuedCert, error)
	// Renewal is no repository call of the API: the collector renews over
	// its NATS channel with proof of its current key (CertificateRenewal,
	// PGRepository.RenewEdgeCertificate, F-012).

	// Discovery jobs.
	ListJobs(ctx context.Context, orgID string, filter JobFilter, page api.PaginationParams) ([]Job, int, error)
	CreateJob(ctx context.Context, j *Job) error
	GetJob(ctx context.Context, orgID, id string) (*Job, error)

	// Reconciliation review queue.
	ListReviewItems(ctx context.Context, orgID string, filter ReviewFilter, page api.PaginationParams) ([]ReviewItem, int, error)
	CreateReviewItem(ctx context.Context, item *ReviewItem) error
	GetReviewItem(ctx context.Context, orgID, id string) (*ReviewItem, error)
	ResolveReviewItem(ctx context.Context, orgID, id string, resolution *Resolution) (*ReviewItem, error)
}

// MemoryRepository is an in-memory collector store.
type MemoryRepository struct {
	*reviews.MemoryRepository
	mu           sync.RWMutex
	collectors   map[string]*Collector
	jobs         map[string]*Job
	ciTypes      map[string]string
	enrollCodes  map[string]*EnrollmentCode
	enrollHashes map[string]string
	records      map[string]time.Time
	scopes       map[string]*Scope
	orgSettings  map[string]map[string]any
	orgTimezone  map[string]string
	seq          int
}

// NewMemoryRepository creates a new in-memory discovery repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		MemoryRepository: reviews.NewMemoryRepository(),
		collectors:       make(map[string]*Collector),
		jobs:             make(map[string]*Job),
		ciTypes:          make(map[string]string),
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

// CountCollectors implements Repository.
func (r *MemoryRepository) CountCollectors(_ context.Context, orgID string) (int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.countCollectors(orgID), nil
}

func (r *MemoryRepository) countCollectors(orgID string) int64 {
	var n int64
	for _, c := range r.collectors {
		if c.OrganizationID == orgID && c.Status != CollectorDisabled {
			n++
		}
	}
	return n
}

func (r *MemoryRepository) RegisterCollector(_ context.Context, c *Collector) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	c.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	c.CreatedAt = now
	c.UpdatedAt = now
	c.StatusChangedAt = now
	if c.Status != CollectorActive {
		c.Status = CollectorPending
	} else {
		c.LastHeartbeatAt = now
	}
	r.collectors[c.ID] = c
	return nil
}

// UpdateCollector implements Repository.
func (r *MemoryRepository) UpdateCollector(_ context.Context, orgID, collectorID string, patch *CollectorPatch) (*Collector, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.collectors[collectorID]
	if !ok || c.OrganizationID != orgID {
		return nil, api.ErrNotFound
	}
	if patch.Name != nil {
		c.Name = *patch.Name
	}
	if patch.SiteID != nil {
		c.SiteID = *patch.SiteID
	}
	if patch.Status != nil {
		c.Status = nextAdminStatus(c.Status, *patch.Status)
	}
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	cp := *c
	return &cp, nil
}

// CollectorStatus implements Repository.
func (r *MemoryRepository) CollectorStatus(_ context.Context, orgID, collectorID string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.collectors[collectorID]
	if !ok || c.OrganizationID != orgID {
		return "", api.ErrNotFound
	}
	return c.Status, nil
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

func (r *MemoryRepository) EnrollCollector(ctx context.Context, rawCode string, c *Collector, issue CertIssuer) (*IssuedCert, error) {
	return r.EnrollCollectorWithQuota(ctx, rawCode, c, issue, nil)
}

// EnrollCollectorWithQuota implements Repository.
func (r *MemoryRepository) EnrollCollectorWithQuota(ctx context.Context, rawCode string, c *Collector, issue CertIssuer, quota QuotaGuard) (*IssuedCert, error) {
	r.mu.Lock()
	id, ok := r.enrollHashes[enrollmentCodeHash(rawCode)]
	code := r.enrollCodes[id]
	if !ok || code == nil || code.UsedAt != nil || time.Now().UTC().After(code.ExpiresAt) {
		r.mu.Unlock()
		return nil, fmt.Errorf("enrollment code invalid, expired or already used")
	}
	if quota != nil {
		if err := quota.AllowCreate(ctx, code.OrganizationID, entitlement.LimitMaxCollectors, r.countCollectors(code.OrganizationID)); err != nil {
			r.mu.Unlock()
			return nil, err
		}
	}
	now := time.Now().UTC()
	code.UsedAt = &now
	r.mu.Unlock()
	c.OrganizationID = code.OrganizationID
	c.Status = CollectorActive
	if err := r.RegisterCollector(ctx, c); err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	cert, err := issue(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if stored, ok := r.collectors[c.ID]; ok {
		stored.CertFingerprint, stored.CertNotAfter = cert.Fingerprint, cert.NotAfter.Format(time.RFC3339Nano)
	}
	return cert, nil
}

func (r *MemoryRepository) Heartbeat(_ context.Context, orgID, collectorID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.collectors[collectorID]
	if !ok || c.OrganizationID != orgID {
		return "", fmt.Errorf("not found")
	}
	if c.Status == CollectorDisabled {
		return c.Status, ErrCollectorDisabled
	}
	c.LastHeartbeatAt = time.Now().UTC().Format(time.RFC3339)
	c.Status = statusAfterHeartbeat(c.Status)
	return c.Status, nil
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
	RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*FieldProvenance, error)
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
	limits     QuotaGuard
	license    EdgeLicense
	repo       Repository
	ciRepo     ci.Repository
	relRepo    relationship.Repository
	typeLookup CITypeLookup
	provenance ProvenanceRecorder
	instance   InstanceFieldSource
	queue      IngestQueue
	interfaces InterfaceSyncer
	// observed validates discovered values against the field metadata.
	observed ObservedValidator
	// jobs registers discovery jobs in the generic job model (JOB-01).
	jobs jobs.Tracker
	// ca signs the collector certificates (COL-04); optional.
	ca *CA
	// The review queue (package reviews), built on first use.
	reviewsOnce    sync.Once
	reviews        *reviews.Service
	reviewNotifier reviews.Notifier
	// serial holds one mutex per organization and ingest lane: the ingests
	// of a lane of an organization run one after another in this process
	// (REC-01, WP-334).
	serial     sync.Map
	reviewJobs jobs.Tracker
	// merger carries out the resolution merge of probable_duplicate items
	// as the CI-12 merge (WithMerger).
	merger CIMerger
	// tx runs the frame of a batch and each record in one transaction
	// (NFR-03); without one every repository call is its own transaction.
	tx TxRunner
	// ingestLanes is the number of ingest lanes (WP-334, E-56); with more
	// than one and a repository that can lock, the lanes of an
	// organization run side by side (parallel) and every record and batch
	// transaction takes the identity locks of its records (locking).
	ingestLanes int
	// beforeCreate, when set, runs after a transaction decided to create
	// new CIs and before it inserts them (tests of concurrent lanes).
	beforeCreate func(ctx context.Context)
	// metrics and features take the collectors' metric samples on the
	// edge (WithEdgeMetrics, MON-02).
	metrics  MetricsIngester
	features FeatureStatus
	// scopeCycles counts the scope requests that failed on the server's
	// side (worker discovery_scopes, OPS-05); nil counts none.
	scopeCycles *observability.Cycles
}

// CITypeLookup resolves a CI type name or UUID to the canonical ci_type id.
// Implemented by the discovery PG repository; tests may stub it.
type CITypeLookup interface {
	LookupCITypeID(ctx context.Context, orgID, nameOrID string) (string, error)
}

// TxRunner runs fn in one transaction of the context's tenant scope: the
// repository calls fn makes with its context join it (database.ContextWithTx).
// The discovery PG repository implements it.
type TxRunner interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// CILocker locks the row of a CI until the end of the context transaction.
// The discovery PG repository implements it.
type CILocker interface {
	LockCI(ctx context.Context, orgID, ciID string) error
}

// NewHandler creates a new discovery handler. ciRepo enables reconciliation and
// review-item resolution; the optional relRepo enables topology relationship
// derivation. Both may be nil (e.g. for --no-db smoke tests), in which case
// those features are skipped gracefully. A repository that is a TxRunner
// runs the ingest in transactions per batch frame and per record (NFR-03).
func NewHandler(repo Repository, ciRepo ci.Repository, relRepo ...relationship.Repository) *Handler {
	h := &Handler{repo: repo, ciRepo: ciRepo}
	if lookup, ok := repo.(CITypeLookup); ok {
		h.typeLookup = lookup
	}
	if runner, ok := repo.(TxRunner); ok {
		h.tx = runner
	}
	if len(relRepo) > 0 {
		h.relRepo = relRepo[0]
	}
	return h
}

// inTx runs fn in one transaction, or as it is without a TxRunner.
func (h *Handler) inTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if h.tx == nil {
		return fn(ctx)
	}
	return h.tx.RunInTx(ctx, orgID, fn)
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
// withheld; the fingerprint and raw data of the ingest stay. Only a failed
// statement, which aborted the record's transaction, is returned.
func (h *Handler) stripInstanceFields(ctx context.Context, orgID, ciID string, attributes, discovered map[string]any) (bool, error) {
	if h.instance == nil {
		return false, nil
	}
	names, err := h.instance.InstanceFieldNames(ctx, orgID, ciID)
	if err != nil {
		if database.TxFailed(ctx) {
			return false, err
		}
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
		return withheld, nil
	}
	return len(WithoutInstanceFields(attributes, names)) > 0, nil
}

// recordDiscovered records the provenance of one applied field. A refused
// write does not fail the ingest: provenance is best-effort alongside the
// audited CI row. A failed statement does, because it aborted the record's
// transaction.
func (h *Handler) recordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) error {
	if h.provenance == nil || ciID == "" {
		return nil
	}
	if _, err := h.provenance.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source, observedAt); err != nil && database.TxFailed(ctx) {
		return err
	}
	return nil
}

// manyProvenance records several fields of a CI at once (the PostgreSQL
// override repository).
type manyProvenance interface {
	RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error
}

// recordDiscoveredMany records the provenance of the fields of a new CI in
// one statement where the store can (NFR-03), else field by field; like
// recordDiscovered it fails the ingest only on a failed statement.
func (h *Handler) recordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	if h.provenance == nil || ciID == "" {
		return nil
	}
	if many, ok := h.provenance.(manyProvenance); ok {
		if err := many.RecordDiscoveredMany(ctx, orgID, ciID, values, source, observedAt); err != nil && database.TxFailed(ctx) {
			return err
		}
		return nil
	}
	for field, value := range values {
		if err := h.recordDiscovered(ctx, orgID, ciID, field, value, source, observedAt); err != nil {
			return err
		}
	}
	return nil
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
// one is open already or the value was dismissed (REC-12, deduplicated per
// field). A failure does not fail the ingest: the override stays in place
// either way.
func (h *Handler) queueOverrideConflict(ctx context.Context, orgID, ciID, field string, value any, source string) bool {
	item := &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindOverrideConflict,
		Status:         ReviewStatusOpen,
		Payload:        map[string]any{"ci_id": ciID, "field": field, "observed_value": value, "source": source},
		CandidateCIIDs: []string{ciID},
		ObjectKey:      "ci:" + ciID + "/" + field,
	}
	// One open item per field; a dismissed value stays suppressed until the
	// source reports another one (REC-12). The savepoint undoes a review
	// that failed after its insert, so the record's transaction goes on.
	var opened bool
	err := database.Savepoint(ctx, func(ctx context.Context) error {
		var openErr error
		opened, openErr = h.ReviewService().OpenUnlessDismissed(ctx, item, "observed_value")
		return openErr
	})
	if err != nil {
		slog.WarnContext(ctx, "override conflict review not created", "ci_id", ciID, "field", field, "error", err)
		return false
	}
	return opened
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

// resolveCITypeIDs maps each item's ci_type_name to the canonical ci_type UUID
// with the types of the batch (lookupTypes). Entries that are already UUIDs
// pass through. Items whose type cannot be resolved get an empty string so
// the caller can queue them for review.
func resolveCITypeIDs(items []IngestItem, types map[string]string) []string {
	ids := make([]string, len(items))
	for i := range items {
		name := strings.TrimSpace(items[i].CITypeName)
		if isUUID(name) {
			ids[i] = name
		} else if name != "" {
			ids[i] = types[name]
		}
	}
	return ids
}

// frameTypeNames are the type names a batch looks up at once: the names of
// its items that are no UUID, generic_device and the virtual types of the
// identity context.
func frameTypeNames(items []IngestItem) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name != "" && !isUUID(name) && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for i := range items {
		add(strings.TrimSpace(items[i].CITypeName))
	}
	add(ci.GenericDeviceType)
	for _, key := range virtualTypeKeys {
		add(key)
	}
	sort.Strings(names)
	return names
}

// RegisterRoutes registers discovery routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/collectors", h.ListCollectors)
	r.Post("/api/v1/collectors", h.RegisterCollector)
	r.Post("/api/v1/collectors/{id}/heartbeat", h.Heartbeat)
	r.Patch("/api/v1/collectors/{id}", h.UpdateCollector)
	r.Get("/api/v1/collectors/pki", h.PKI)
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

	// Discovery scopes (DIS-09, API-05).
	h.RegisterScopeRoutes(r)

	// Reconciliation review queue.
	// The review-items resource (API-05) and its deprecated discovery path.
	reviews.NewHandler(h.ReviewService()).RegisterRoutes(r)
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
	// A created collector waits for its first heartbeat.
	c.Status = CollectorPending
	if c.Config == nil {
		c.Config = make(map[string]any)
	}

	if h.limits != nil {
		current, err := h.repo.CountCollectors(r.Context(), t.OrganizationID)
		if err == nil {
			err = h.limits.AllowCreate(r.Context(), t.OrganizationID, entitlement.LimitMaxCollectors, current)
		}
		if err != nil {
			if !api.WriteEntitlementError(w, err) {
				api.WriteRepoError(w, err)
			}
			return
		}
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
	// Enrollment codes are valid for 24 hours (COL-04); a shorter validity
	// can be requested.
	ttl := time.Duration(req.TTLMinutes) * time.Minute
	if ttl <= 0 || ttl > EnrollmentCodeTTL {
		ttl = EnrollmentCodeTTL
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
		// CSR is the PEM certificate signing request of the collector's
		// locally generated key.
		CSR string `json:"csr,omitempty"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "code and name are required")
		return
	}

	// The code is consumed and the collector registered (active) in one
	// step: the request is unauthenticated, the code authorizes registration
	// for its organization (E-08); a code works exactly once (COL-04).
	c := &Collector{
		Name:     req.Name,
		Version:  req.Version,
		ClientID: req.ClientID,
		Config:   map[string]any{},
	}
	// With a CSR the collector gets its client certificate (COL-04); the
	// key never leaves the collector.
	var issue CertIssuer
	if strings.TrimSpace(req.CSR) != "" {
		if h.ca == nil {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the collector PKI is not configured")
			return
		}
		issue = h.issuer(req.CSR)
	}
	// max_collectors of the code's organization is checked before the code
	// is consumed: at the limit the answer is 403 entitlement-limit and the
	// code stays valid (ENT-03).
	cert, err := h.repo.EnrollCollectorWithQuota(r.Context(), req.Code, c, issue, h.limits)
	if err != nil {
		var fe *api.FieldError
		switch {
		case api.WriteEntitlementError(w, err):
		case errors.As(err, &fe):
			api.WriteRepoError(w, err)
		case errors.Is(err, ErrInvalidCSR):
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		default:
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "enrollment code invalid, expired or already used")
		}
		return
	}
	if cert != nil {
		api.WriteJSON(w, http.StatusCreated, struct {
			*Collector
			Certificate *IssuedCert `json:"certificate"`
		}{c, cert})
		return
	}

	api.WriteJSON(w, http.StatusCreated, c)
}

// EnrollmentCodeTTL is the validity of an enrollment code (COL-04).
const EnrollmentCodeTTL = 24 * time.Hour

// generateEnrollmentCode returns a URL-safe, high-entropy single-use code.
func generateEnrollmentCode() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CollectorSpoolReport is the offline spool state a collector sends with its
// heartbeat (NFR-04, COL-05, OPS-06). The dropped counters are the losses
// since the last heartbeat the server accepted: the collector acknowledges
// them after that heartbeat's 204.
type CollectorSpoolReport struct {
	Messages         int   `json:"messages"`
	Bytes            int64 `json:"bytes"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
	DroppedMessages  int64 `json:"dropped_messages"`
	DroppedBytes     int64 `json:"dropped_bytes"`
	// DroppedSamples counts metric samples and traps the collector dropped
	// on upload failures (E-79: counted, not spooled).
	DroppedSamples int64 `json:"dropped_samples"`
	Backpressure   bool  `json:"backpressure"`
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
	status, err := h.repo.Heartbeat(r.Context(), t.OrganizationID, id)
	if errors.Is(err, ErrCollectorDisabled) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "collector not found")
		return
	}
	// A paused collector stops scanning until the license is active again
	// (ENT-07).
	w.Header().Set(CollectorStatusHeader, status)
	recordSpoolReport(r.Context(), t.OrganizationID, id, req.Spool)

	w.WriteHeader(http.StatusNoContent)
}

// BulkIngest handles POST /api/v1/ingest/bulk.
func (h *Handler) BulkIngest(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	items, ok := readIngestItems(w, r)
	if !ok {
		return
	}
	// Times compare by the source's observed_at (GLO-12): items of the
	// deprecated contract carry none and take the arrival time; records
	// from the future are rejected and counted.
	received := len(items)
	items, rejected := admitObservedAt(items, time.Now().UTC())

	// Every record goes the path of the ingest.> consumer (REC-01): with a
	// queue it is published and processed asynchronously, without one it is
	// processed in this request.
	msgs := newIngestMessages(r.Context(), t, r.Header.Get("X-Collector-ID"), items)
	resp, err := h.dispatch(r.Context(), msgs)
	// Records that fail are listed in the answer; only when no record was
	// accepted does the request fail as a whole (WP-481).
	if err = resp.finish(received, rejected, err); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, resp)
}

// processItems reconciles the items of one organization; ctx carries that
// organization's tenant scope. Records processed within 48 hours are skipped
// (REC-01).
//
// With a transaction runner (the PostgreSQL repository) the frame of the
// batch runs in one short transaction and every record in exactly one more
// (NFR-03). A failing record rolls back completely and the batch goes on
// with the next record (WP-481): the records processed get their topology
// and are entered in the ledger, and the error, an *IngestErrors, names each
// failed record. A record that failed transiently holds back the records
// after it that share one of its identity keys (the keys of the identity
// lock, E-56): they fail with it and keep their order when the batch is
// delivered again; a record that failed for good holds back nothing. A
// frame that fails for good runs each record in a frame of its own, so
// that only the records that cause it fail.
//
// With the batch queries of the repository and the batch creation of the
// CI repository (WP-333), each window of up to ingestBatch records (with
// the ingest locks also bounded by its identity keys, see windowEnd) first
// runs one more transaction: one candidate query for all its records, and
// its new devices created together. Records from the queue (with the
// ingest locks) also update the known devices there, under the window's
// identity locks (WP-608). Every record the batch leaves follows one by one
// in the order of the records; without the ingest locks the updates of
// known devices do as well. The records of one device keep their order.
// When a record fails in the batch of its window, the window rolls back and
// its records follow one by one (processWindow).
func (h *Handler) processItems(ctx context.Context, orgID string, items []IngestItem) (BulkIngestResponse, error) {
	var resp BulkIngestResponse
	if h.ciRepo == nil {
		return resp, nil
	}
	// Whether the transactions take the ingest locks (WP-334): records
	// consumed from the queue and parallel lanes do.
	locking := h.locking(ctx)
	var frame *batchFrame
	for attempt := 0; ; attempt++ {
		err := h.inTx(ctx, orgID, func(ctx context.Context) error {
			var err error
			frame, err = h.openFrame(ctx, orgID, items)
			return err
		})
		if err == nil {
			break
		}
		// With other ingests of the organization at the same time, the
		// reviews of unknown types of two frames may wait for each other
		// (WP-334): the frame starts again, as it does after PostgreSQL ran
		// out of shared memory for a moment.
		reason := retryReason(err)
		if !locking || attempt >= maxRecordRetries ||
			(reason != "deadlock" && reason != "serialization" && reason != "out_of_memory") || !retryPause(ctx, reason, attempt) {
			if len(items) > 1 && classifyRecordError(err).permanent {
				// A record makes the frame fail for good, e.g. the review of
				// an unknown type outside the sender's scope: each record
				// runs on its own, so that only it fails (WP-481).
				slog.WarnContext(ctx, "ingest frame failed for good; its records are processed one by one",
					"event", "ingest.frame.fallback", "organization_id", orgID, "records", len(items), "error", err)
				return h.processEach(ctx, orgID, items)
			}
			return resp, err
		}
		recordRetries.WithLabelValues(reason).Inc()
	}
	resp.add(&frame.resp)
	if len(frame.items) == 0 {
		return resp, nil
	}
	frame.locking = locking

	// resolvedCIID[i] holds the CI id that item i resolved to (matched or
	// created). Conflicts leave it empty so no topology is derived for them.
	// handled[i] reports that record i is committed: in the batch of its
	// window or in its own transaction.
	n := len(frame.items)
	resolvedCIID := make([]string, n)
	handled := make([]bool, n)
	// failed are the records that failed; held the identity keys of those
	// that failed transiently, which hold back the records after them.
	var failed []RecordError
	held := heldKeys{}
	frame.blocked = make([]bool, n)
	for start, end := 0, 0; start < n; start = end {
		end = h.windowEnd(orgID, frame, start)
		if len(held) > 0 {
			// The batch of the window leaves the records held back, and those
			// after them that share a key with them.
			for i := start; i < end; i++ {
				if keys := identityLockKeys(orgID, frame.identity, &frame.items[i]); frame.typeIDs[i] != "" && held.holds(keys) {
					frame.blocked[i] = true
					held.add(keys)
				}
			}
		}
		h.processWindow(ctx, orgID, frame, start, end, handled, resolvedCIID, &resp)
		for i := start; i < end; i++ {
			if handled[i] {
				continue
			}
			if frame.typeIDs[i] == "" {
				// Unresolvable CI type: queued for review in the frame.
				handled[i] = true
				continue
			}
			var keys []int32
			if len(held) > 0 {
				keys = identityLockKeys(orgID, frame.identity, &frame.items[i])
				if frame.blocked[i] || held.holds(keys) {
					failed = append(failed, newRecordError(&frame.items[i], errHeldBack))
					held.add(keys)
					continue
				}
			}
			out, err := h.runRecord(ctx, orgID, frame, i)
			if err != nil {
				f := newRecordError(&frame.items[i], err)
				failed = append(failed, f)
				slog.WarnContext(ctx, "ingest record failed; the batch goes on with the next record", "event", "ingest.record.failed",
					"organization_id", orgID, "record_id", f.RecordID, "permanent", f.Permanent, "error", err)
				if !f.Permanent {
					if keys == nil {
						keys = identityLockKeys(orgID, frame.identity, &frame.items[i])
					}
					held.add(keys)
				}
				continue
			}
			// Counted once the record's transaction has committed.
			resp.add(&out.resp)
			resolvedCIID[i] = out.ciID
			handled[i] = true
			if out.created {
				frame.total++
			}
		}
	}

	var processed []IngestItem
	var processedCIID []string
	for i := range frame.items {
		if handled[i] {
			processed = append(processed, frame.items[i])
			processedCIID = append(processedCIID, resolvedCIID[i])
		}
	}
	resp.Relationships += h.deriveTopology(ctx, orgID, processed, processedCIID, h.neighborCandidates(ctx, orgID, processed))
	// Outlet mappings of PDUs and UPS outlet groups (IMP-09).
	resp.Relationships += h.derivePowerChain(ctx, orgID, processed, processedCIID)
	if err := h.markProcessed(ctx, orgID, processed); err != nil {
		return resp, &IngestErrors{Records: failed, Rest: err}
	}
	settle(ctx, processed)
	if len(failed) > 0 {
		return resp, &IngestErrors{Records: failed}
	}
	return resp, nil
}

// batchFrame is what the frame of a batch prepares for its records: the
// records left after the ledger, their CI type ids, the identity context and
// the CI count for max_cis.
type batchFrame struct {
	items   []IngestItem
	typeIDs []string
	// noProfile marks records no collector profile classified (DIS-02);
	// known before CITypeName becomes the type id.
	noProfile  []bool
	identity   *IdentityContext
	total      int64
	unlicensed queuedDevices
	resp       BulkIngestResponse
	// candidates[i] are the identity candidates of record i the batch query
	// of its window read (WP-333); valid only with prefetched[i] and
	// without locks: with them, the batch's identity locks end with its
	// commit, so a record reads its candidates again under its own (the
	// known devices of the window are updated within it, updateInWindow).
	candidates [][]ci.Item
	prefetched []bool
	// limitExclusive makes every creation of the batch take the exclusive
	// max_cis lock (WP-334): a transaction found the headroom too small for
	// the shared one.
	limitExclusive bool
	// locking makes the transactions of the records take the identity and
	// max_cis locks and retry a lost race (Handler.locking, WP-334).
	locking bool
	// blocked marks the records an earlier transient record failure holds
	// back (WP-481); the batch of their window leaves them.
	blocked []bool
}

// recordOutcome is what processing one record changed.
type recordOutcome struct {
	resp    BulkIngestResponse
	ciID    string
	created bool
}

// openFrame runs the frame of a batch: it drops the records processed within
// the dedupe window (REC-01), resolves the CI types of the records and of the
// identity context with one lookup, queues records of an unknown type for
// review and reads the CI count (only with max_cis) and the default VRF.
func (h *Handler) openFrame(ctx context.Context, orgID string, items []IngestItem) (*batchFrame, error) {
	frame := &batchFrame{}
	items, duplicates, err := h.skipProcessed(ctx, orgID, items)
	if err != nil {
		return nil, err
	}
	frame.resp.Duplicates = duplicates
	if len(items) == 0 {
		return frame, nil
	}
	// Typed values a manual write refuses are dropped and counted before
	// anything resolves identities with them (WP-501). The items are copied:
	// a frame that starts again checks the values of the caller again.
	items = slices.Clone(items)
	for i := range items {
		frame.resp.InvalidValues += dropInvalidTyped(ctx, &items[i])
	}

	// Resolve ci_type_name to the canonical ci_type UUID before
	// reconciliation. Collectors send the type name (e.g. "switch"); the CI
	// column stores the UUID. Names are matched against global system types
	// (organization_id IS NULL) and org-specific types. Items whose type can
	// be resolved neither as UUID nor by name are queued for review instead of
	// failing the whole batch with a 500.
	types, err := h.lookupTypes(ctx, orgID, frameTypeNames(items))
	if err != nil {
		return nil, err
	}
	frame.typeIDs = resolveCITypeIDs(items, types)
	frame.noProfile = make([]bool, len(items))
	for i := range items {
		item := &items[i]
		if frame.typeIDs[i] != "" {
			frame.noProfile[i] = unclassifiedDevice(item)
			continue
		}
		clientID, siteID := h.deviceReviewScope(ctx, orgID, item)
		// The record with its client and identity, for new (CI-13).
		payload := devicePayload(item)
		payload["reason"] = "unknown_ci_type"
		opened, openErr := h.openReview(ctx, &ReviewItem{
			OrganizationID: orgID,
			Kind:           ReviewKindUnclassifiedDevice,
			Status:         ReviewStatusOpen,
			ClientID:       clientID,
			SiteID:         siteID,
			Payload:        payload,
			ObjectKey:      deviceKey(item),
		})
		if openErr != nil {
			return nil, openErr
		}
		if opened {
			frame.resp.ReviewItems++
		}
	}

	// The CI count for max_cis; identity candidates are looked up per item
	// over the whole inventory (REC-02).
	if h.limits != nil {
		_, total, countErr := h.ciRepo.List(ctx, orgID, ci.FilterParams{}, api.PaginationParams{Limit: 1})
		if countErr != nil {
			return nil, countErr
		}
		frame.total = int64(total)
	}
	if frame.identity, err = h.identityContext(ctx, orgID, types); err != nil {
		return nil, err
	}

	// Reconciliation compares stored CIs (UUID type) against the incoming
	// item, so the item must carry the resolved UUID as well.
	for i := range items {
		if frame.typeIDs[i] != "" {
			items[i].CITypeName = frame.typeIDs[i]
		}
	}
	frame.items = items
	return frame, nil
}

// deviceReviewScope returns the client and site of a review item about an
// ingested device without candidate CIs: those of its record or, when the
// record names neither, those of its collector. Without either the item has
// no client and only org-wide principals see it (migration 000140).
func (h *Handler) deviceReviewScope(ctx context.Context, orgID string, item *IngestItem) (clientID, siteID string) {
	if item.ClientID != "" || item.SiteID != "" || item.CollectorID == "" {
		return item.ClientID, item.SiteID
	}
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return "", ""
	}
	col, err := repo.ScopeCollector(ctx, orgID, item.CollectorID)
	if err != nil {
		return "", ""
	}
	return col.ClientID, col.SiteID
}

// processRecord reconciles record i of the frame; with a transaction runner
// it runs in the record's own transaction. out receives what it changed.
func (h *Handler) processRecord(ctx context.Context, orgID string, frame *batchFrame, i int, out *recordOutcome) error {
	item := frame.items[i]
	// Records that share an identity key never run at the same time: the
	// locks come first, before anything is read (WP-334).
	if err := h.lockIdentities(ctx, orgID, frame, &item); err != nil {
		return err
	}
	var candidates []ci.Item
	if i < len(frame.prefetched) && frame.prefetched[i] && !frame.locking {
		// Read by the batch query of the window (WP-333): no record before
		// this one changed them.
		candidates = frame.candidates[i]
	} else {
		var err error
		if candidates, err = h.identityCandidates(ctx, orgID, &item); err != nil {
			return err
		}
	}
	return h.reconcileRecord(ctx, orgID, frame, i, &item, candidates, out)
}

// reconcileRecord reconciles a copy of rec, record i of the frame, against
// its identity candidates in the context transaction, which holds the
// identity locks of the record when the frame's records take them: the
// transaction of the record (processRecord) or the batch transaction of its
// window (batchWindow). out receives what it changed.
func (h *Handler) reconcileRecord(ctx context.Context, orgID string, frame *batchFrame, i int, rec *IngestItem, candidates []ci.Item,
	out *recordOutcome) error {
	item := *rec
	typeID, identity := frame.typeIDs[i], frame.identity
	resp := &out.resp

	// No collector profile matched (DIS-02): the device becomes a
	// generic_device CI and a reviewer classifies it, once the identity is
	// resolved and only while the CI is still a generic_device.
	openUnclassified := func(device *ci.Item) error {
		if !frame.noProfile[i] {
			return nil
		}
		opened, err := h.reviewUnclassified(ctx, orgID, &item, device, identity)
		if opened {
			resp.ReviewItems++
		}
		return err
	}

	result := ResolveIdentity(candidates, &item, identity)
	// No live CI: a soft-deleted one may be the device (CI-13).
	if result.Action == ReconcileCreated {
		held, reviewed, resErr := h.resurrection(ctx, orgID, &item, identity)
		if resErr != nil {
			return resErr
		}
		if held {
			resp.Resurrected++
			if reviewed {
				resp.ReviewItems++
			}
			return nil
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	source := item.Source
	if source == "" {
		source = ci.SourceSweep
	}
	// Discovered custom attributes are persisted on the CI itself so they
	// are visible, searchable and filterable like any other attribute; the
	// per-field provenance recorded below keeps the reporting source and
	// timestamp available for reconciliation.
	// Discovered values pass the same field rules as manual writes.
	validateType, validateCI := typeID, ""
	if result.Action == ReconcileMatched {
		if m := findCI(candidates, result.MatchedCIID); m != nil {
			validateType, validateCI = m.CITypeID, m.ID
		}
	}
	dropped, dropErr := h.dropInvalidObserved(ctx, orgID, validateType, validateCI, &item)
	if dropErr != nil {
		return dropErr
	}
	resp.InvalidValues += dropped
	withoutBlocklistedIdentity(&item)
	attributes := discoveredAttributes(&item)

	switch result.Action {
	case ReconcileCreated:
		// Over max_cis the device is held as unlicensed_ci instead of
		// becoming a CI (ENT-03, CH21); existing CIs keep updating.
		if h.limits != nil {
			total, countErr := h.admissionCount(ctx, orgID, frame, frame.total)
			if countErr != nil {
				return countErr
			}
			queued, admitErr := h.admitNewCI(ctx, orgID, total, &item, typeID, source, &frame.unlicensed)
			if admitErr != nil {
				return admitErr
			}
			if queued {
				resp.Unlicensed++
				return nil
			}
		}
		withoutTakenIdentity(&item, candidates)
		if h.beforeCreate != nil {
			h.beforeCreate(ctx)
		}
		newItem, err := h.createDiscoveredCI(ctx, orgID, &item, typeID, source)
		if err != nil {
			return err
		}
		out.created = true
		out.ciID = newItem.ID
		resp.Created++
		if syncErr := h.syncInterfaces(ctx, orgID, newItem.ID, &item); syncErr != nil {
			return syncErr
		}
		// A hostname or FQDN hit is never merged (CH29): the new CI is
		// reviewed as a probable duplicate.
		if len(result.ProbableDuplicates) > 0 {
			review := probableDuplicateReview(orgID, newItem.ID, &item, result.ProbableDuplicates)
			if _, reviewErr := h.openReview(ctx, review); reviewErr != nil {
				return reviewErr
			}
			resp.ReviewItems++
		}
		if err := openUnclassified(&newItem); err != nil {
			return err
		}
	case ReconcileMatched:
		matched := findCI(candidates, result.MatchedCIID)
		if matched == nil {
			matched = &ci.Item{ID: result.MatchedCIID}
		}
		// The record's transaction locks the CI row before it writes field
		// values, like a manual change does: otherwise the two wait for each
		// other's field value and CI row (deadlock, ABN-03).
		if locker, ok := h.repo.(CILocker); ok {
			if err := locker.LockCI(ctx, orgID, matched.ID); err != nil {
				return err
			}
		}
		observedAt := item.ObservedAt
		// Every field goes through the central write decision (REC-03):
		// overrides are never overwritten, ranks and observation times
		// decide the rest, unreadable state writes nothing.
		conflict, refused := false, false
		// Serial number and hardware UUID refused for a lower rank are
		// reviewed as conflicting_values with the reported value (REC-06).
		var lowerRank []string
		// A failed statement aborted the record's transaction: no further
		// field is decided, the record rolls back.
		var txErr error
		decide := func(field string, value, current any) bool {
			if txErr == nil && database.TxFailed(ctx) {
				txErr = database.ErrTxAborted
			}
			if txErr != nil {
				return false
			}
			d := h.decide(ctx, &override.Write{
				OrganizationID: orgID, CIID: matched.ID, Field: field, Value: value,
				Source: source, ObservedAt: observedAt, Current: current, FallbackSource: matched.DiscoverySource,
			})
			if d.Write || d.Conflict || d.Reason == override.ReasonOverride {
				// Observed under an override or written: the provenance
				// keeps the reported value (REC-12, REC-10).
				if txErr = h.recordDiscovered(ctx, orgID, matched.ID, field, value, source, observedAt); txErr != nil {
					return false
				}
			}
			switch d.Reason {
			case override.ReasonLowerRank, override.ReasonStale, override.ReasonError:
				refused = true
			}
			if d.Reason == override.ReasonLowerRank && (field == "serial_number" || field == "hardware_uuid") {
				lowerRank = append(lowerRank, field)
			}
			if d.Conflict {
				conflict = true
				if h.queueOverrideConflict(ctx, orgID, matched.ID, field, value, source) {
					resp.ReviewItems++
				}
			}
			return d.Write
		}
		// A sighting updates last_seen_at without history and brings an
		// unknown CI back to active (REC-07, AUD-03).
		update := ci.UpdateRequest{LastSeenAt: &now}
		if matched.Status == "unknown" {
			active := "active"
			update.Status = &active
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
			{"hardware_uuid", strings.ToLower(incomingUUID(&item)), strings.ToLower(matched.HardwareUUID), &update.HardwareUUID},
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
		// In a fixed order: two transactions that write field values of
		// one CI take their row locks in the same order (WP-334).
		for _, k := range slices.Sorted(maps.Keys(attributes)) {
			v := attributes[k]
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
		if txErr != nil {
			return txErr
		}
		// A source whose values were refused does not become the CI's
		// discovery source (REC-05).
		if !refused && override.SourceRank(source) >= override.SourceRank(matched.DiscoverySource) {
			update.DiscoverySource = &source
		}
		// Instance attributes of this CI are never written (MET-14).
		stripped, stripErr := h.stripInstanceFields(ctx, orgID, matched.ID, writes, item.Attributes)
		if stripErr != nil {
			return stripErr
		}
		if stripped {
			resp.ProtectedInstanceFields++
		}
		update.Attributes = writes
		// The fields, the status and the reclassification of a
		// generic_device classified now (CI-11) are one change of the
		// record in one transaction: one change set with the record's
		// origin and one event ci.changed (AUD-03, EVT-02).
		changed, err := ci.UpdateRecord(ctx, h.ciRepo, orgID, result.MatchedCIID, &ci.RecordUpdate{
			Update: update, TypeID: raisedType(matched, typeID, identity),
			Origin: ci.RecordOrigin{CollectorID: item.CollectorID, ObservedAt: item.ObservedAt, RecordID: item.RecordID},
		})
		if err != nil {
			return err
		}
		updated := changed.CI
		if conflict {
			resp.ProtectedOverrides++
		}
		// The CI is classified now: its unclassified_device item closes
		// as reclassified (DIS-02).
		if changed.TypeChanged && updated.CITypeID != identity.GenericDeviceTypeID {
			if resolveErr := h.resolveReclassified(ctx, orgID, updated.ID); resolveErr != nil {
				return resolveErr
			}
		}
		if reviewErr := openUnclassified(updated); reviewErr != nil {
			return reviewErr
		}
		out.ciID = updated.ID
		resp.Updated++
		if syncErr := h.syncInterfaces(ctx, orgID, updated.ID, &item); syncErr != nil {
			return syncErr
		}
		// Contradicting identity values on a matched CI (e.g. a changed
		// serial number) are queued for operator review instead of being
		// silently applied (spec §5.3: Unklarheiten in Review-Queue).
		if len(lowerRank) > 0 {
			result.ValueConflicts = lowerRank
			if reviewItem := reviewItemFromValueConflicts(orgID, *matched, item, result); reviewItem != nil {
				opened, openErr := h.openReview(ctx, reviewItem)
				if openErr != nil {
					return openErr
				}
				if opened {
					resp.ReviewItems++
				}
			}
		}
	case ReconcileConflict:
		resp.Conflicts++
		if reviewItem := reviewItemFromConflict(orgID, item, result); reviewItem != nil {
			opened, openErr := h.openReview(ctx, reviewItem)
			if openErr != nil {
				return openErr
			}
			if opened {
				resp.ReviewItems++
			}
		}
	}
	return nil
}

// batchProvenance records the fields of several new CIs at once (the
// server's override adapter, WP-333).
type batchProvenance interface {
	RecordDiscoveredBatch(ctx context.Context, orgID string, batch []override.DiscoveredValues) error
}

// recordDiscoveredBatch records the provenance of the fields of the new CIs
// of a batch in one statement where the store can, else CI by CI; like
// recordDiscovered it fails the batch only on a failed statement.
func (h *Handler) recordDiscoveredBatch(ctx context.Context, orgID string, batch []override.DiscoveredValues) error {
	if h.provenance == nil || len(batch) == 0 {
		return nil
	}
	if many, ok := h.provenance.(batchProvenance); ok {
		if err := many.RecordDiscoveredBatch(ctx, orgID, batch); err != nil && database.TxFailed(ctx) {
			return err
		}
		return nil
	}
	for _, b := range batch {
		if err := h.recordDiscoveredMany(ctx, orgID, b.CIID, b.Values, b.Source, b.ObservedAt); err != nil {
			return err
		}
	}
	return nil
}

// windowOutcome is what the batch transaction of a window changed (WP-333);
// it counts only once the transaction has committed.
type windowOutcome struct {
	resp       BulkIngestResponse
	total      int64
	unlicensed queuedDevices
	// done[k] reports that record start+k was handled completely in the
	// batch, with the CI ciID[k] (held and unlicensed records have none).
	done       []bool
	ciID       []string
	candidates [][]ci.Item
	prefetched []bool
}

// pendingCI is a new device of a window that the batch creates.
type pendingCI struct {
	k      int
	item   IngestItem
	ci     ci.Item
	source string
	result ReconcileResult
}

// processWindow runs the batch of the records [start, end) of the frame in
// one transaction (NFR-03, WP-333): one query for the live candidates of
// all records, one for the soft-deleted candidates of the new devices, and
// the new devices created together (ci.BatchCreator), indexed together and
// with their provenance in one statement; with the ingest locks also the
// updates of the known devices (updateInWindow, WP-608). It marks the
// records it handled and leaves the candidates it read to the records that
// follow one by one.
// It never fails the ingest: when the batch cannot run or rolls back, every
// record of the window is processed on its own, which reports any error
// for its record.
func (h *Handler) processWindow(ctx context.Context, orgID string, frame *batchFrame, start, end int, handled []bool,
	resolvedCIID []string, resp *BulkIngestResponse) {
	finder, ok := h.repo.(BatchIdentityFinder)
	creator, creates := h.ciRepo.(ci.BatchCreator)
	if !ok || !creates || h.tx == nil {
		return
	}
	n := end - start
	qs, include := make([]IdentityQuery, n), make([]bool, n)
	records := 0
	for k := range n {
		if frame.typeIDs[start+k] == "" || (start+k < len(frame.blocked) && frame.blocked[start+k]) {
			continue
		}
		qs[k] = identityQuery(&frame.items[start+k])
		if !batchable(&qs[k]) {
			return
		}
		include[k] = true
		records++
	}
	if records == 0 {
		return
	}
	if frame.prefetched == nil {
		frame.candidates = make([][]ci.Item, len(frame.items))
		frame.prefetched = make([]bool, len(frame.items))
	}
	var w windowOutcome
	run := func() error {
		return h.inTx(ctx, orgID, func(ctx context.Context) error {
			w = windowOutcome{
				total: frame.total, unlicensed: queuedDevices{keys: maps.Clone(frame.unlicensed.keys)},
				done: make([]bool, n), ciID: make([]string, n), candidates: make([][]ci.Item, n), prefetched: make([]bool, n),
			}
			return h.batchWindow(ctx, orgID, frame, start, qs, include, finder, creator, &w)
		})
	}
	err := run()
	if errors.Is(err, errNearLimit) {
		// The lanes may pass max_cis together: once more, exclusively.
		frame.limitExclusive = true
		recordRetries.WithLabelValues("near_limit").Inc()
		err = run()
	}
	if err != nil {
		slog.WarnContext(ctx, "ingest batch rolled back; its records are processed one by one",
			"event", "ingest.batch.fallback", "organization_id", orgID, "records", records, "error", err)
		return
	}
	resp.add(&w.resp)
	frame.total, frame.unlicensed = w.total, w.unlicensed
	for k := range n {
		i := start + k
		frame.candidates[i], frame.prefetched[i] = w.candidates[k], w.prefetched[k]
		if w.done[k] {
			handled[i], resolvedCIID[i] = true, w.ciID[k]
		}
	}
}

// batchWindow is the batch transaction of processWindow; w collects what it
// changed.
func (h *Handler) batchWindow(ctx context.Context, orgID string, frame *batchFrame, start int, qs []IdentityQuery, include []bool,
	finder BatchIdentityFinder, creator ci.BatchCreator, w *windowOutcome) error {
	// The identity locks of every record of the window come first
	// (WP-334).
	if frame.locking {
		locked := make([]*IngestItem, 0, len(qs))
		for k := range qs {
			if include[k] {
				locked = append(locked, &frame.items[start+k])
			}
		}
		if err := h.lockIdentities(ctx, orgID, frame, locked...); err != nil {
			return err
		}
	}
	keys, refs := batchKeys(qs, include)
	var hits []IdentityHit
	if !keys.empty() {
		var err error
		if hits, err = finder.IdentityHits(ctx, orgID, keys); err != nil {
			return fmt.Errorf("identity candidates: %w", err)
		}
	}
	ids := assignCandidates(refs, hits)
	candidates, loadErr := h.loadCandidates(ctx, orgID, ids)
	if loadErr != nil {
		return loadErr
	}
	// Only the first record of records that can change each other's
	// resolution keeps its candidates or is created here; the others
	// follow it one by one.
	first := identityClusters(qs, include, ids)
	var fresh []int
	results := make([]ReconcileResult, len(qs))
	deletedOf := make([]bool, len(qs))
	for k := range qs {
		if !first[k] {
			continue
		}
		w.candidates[k], w.prefetched[k] = candidates[k], true
		i := start + k
		results[k] = ResolveIdentity(candidates[k], &frame.items[i], frame.identity)
		// A device no collector profile matched opens its review on its own.
		if results[k].Action == ReconcileCreated && !frame.noProfile[i] {
			fresh = append(fresh, k)
			deletedOf[k] = resurrectionGuard(&qs[k])
		}
	}
	if len(fresh) > 0 {
		if err := h.createInWindow(ctx, orgID, frame, start, qs, finder, creator, candidates, results, fresh, deletedOf, w); err != nil {
			return err
		}
	}
	return h.updateInWindow(ctx, orgID, frame, start, first, results, candidates, w)
}

// createInWindow creates the new devices fresh of the window together
// (WP-333): one query for their soft-deleted candidates, the creation with
// one statement, indexed together and with their provenance in one
// statement.
func (h *Handler) createInWindow(ctx context.Context, orgID string, frame *batchFrame, start int, qs []IdentityQuery,
	finder BatchIdentityFinder, creator ci.BatchCreator, candidates [][]ci.Item, results []ReconcileResult, fresh []int,
	deletedOf []bool, w *windowOutcome) error {
	// No live CI: a soft-deleted one may be the device (CI-13).
	var deleted [][]ci.Item
	if dkeys, drefs := batchKeys(qs, deletedOf); !dkeys.empty() {
		dhits, derr := finder.DeletedIdentityHits(ctx, orgID, dkeys)
		if derr != nil {
			return fmt.Errorf("deleted identity candidates: %w", derr)
		}
		deleted = assignDeleted(drefs, dhits)
	} else {
		deleted = make([][]ci.Item, len(qs))
	}

	// With the ingest locks, admission counts under the max_cis lock
	// (WP-334).
	if h.limits != nil {
		total, err := h.admissionCount(ctx, orgID, frame, w.total)
		if err != nil {
			return err
		}
		w.total = total
	}
	validator := h.batchValidator()
	var pending []pendingCI
	for _, k := range fresh {
		i := start + k
		// The batch works on a copy: a record it leaves is processed on its
		// own from what it reported.
		item := cloneItem(&frame.items[i])
		typeID := frame.typeIDs[i]
		held, reviewed, err := h.resurrectionWith(ctx, orgID, &item, frame.identity, deleted[k])
		if err != nil {
			return err
		}
		if held {
			w.resp.Resurrected++
			if reviewed {
				w.resp.ReviewItems++
			}
			w.done[k] = true
			continue
		}
		source := item.Source
		if source == "" {
			source = ci.SourceSweep
		}
		dropped, err := dropInvalidWith(ctx, validator, orgID, typeID, "", &item)
		if err != nil {
			return err
		}
		w.resp.InvalidValues += dropped
		withoutBlocklistedIdentity(&item)
		// Over max_cis the device is held as unlicensed_ci (ENT-03, CH21).
		if h.limits != nil {
			queued, err := h.admitNewCI(ctx, orgID, w.total, &item, typeID, source, &w.unlicensed)
			if err != nil {
				return err
			}
			if queued {
				w.resp.Unlicensed++
				w.done[k] = true
				continue
			}
		}
		withoutTakenIdentity(&item, candidates[k])
		pending = append(pending, pendingCI{k: k, item: item, ci: discoveredCI(orgID, &item, typeID, source), source: source, result: results[k]})
		w.total++
	}
	if len(pending) == 0 {
		return nil
	}

	items := make([]*ci.Item, len(pending))
	for p := range pending {
		items[p] = &pending[p].ci
	}
	if h.beforeCreate != nil {
		h.beforeCreate(ctx)
	}
	created, createErr := creator.CreateMany(ctx, orgID, items)
	if createErr != nil {
		return createErr
	}
	refused := make(map[int]bool, len(created.Refused))
	for _, p := range created.Refused {
		refused[p] = true
		// Another CI holds a unique value (WP-207): the record is processed
		// on its own with a fresh query, which matches that CI or names the
		// violated index.
		rec := &pending[p]
		slog.WarnContext(ctx, "ingest batch: CI refused by a unique index; record processed on its own",
			"event", "ingest.batch.unique", "organization_id", orgID, "record_id", rec.item.RecordID,
			"serial_number", rec.item.SerialNumber)
		w.total--
		w.prefetched[rec.k], w.candidates[rec.k] = false, nil
	}
	provenance := make([]override.DiscoveredValues, 0, len(pending))
	for p := range pending {
		if !refused[p] {
			rec := &pending[p]
			provenance = append(provenance, override.DiscoveredValues{CIID: rec.ci.ID, Values: discoveredFields(&rec.item),
				Source: rec.source, ObservedAt: rec.item.ObservedAt})
		}
	}
	if err := h.recordDiscoveredBatch(ctx, orgID, provenance); err != nil {
		return err
	}
	for p := range pending {
		if refused[p] {
			continue
		}
		rec := &pending[p]
		if err := h.syncInterfaces(ctx, orgID, rec.ci.ID, &rec.item); err != nil {
			return err
		}
		// A hostname or FQDN hit is never merged (CH29): the new CI is
		// reviewed as a probable duplicate.
		if len(rec.result.ProbableDuplicates) > 0 {
			review := probableDuplicateReview(orgID, rec.ci.ID, &rec.item, rec.result.ProbableDuplicates)
			if _, err := h.openReview(ctx, review); err != nil {
				return err
			}
			w.resp.ReviewItems++
		}
		w.resp.Created++
		w.done[rec.k], w.ciID[rec.k] = true, rec.ci.ID
	}
	return nil
}

// updateInWindow updates the known devices of the window in its batch
// transaction when the frame's records take the ingest locks (WP-608,
// NFR-03). The window holds the identity locks of its records until its
// commit, so the candidates it read stay valid; a record in a transaction
// of its own takes the locks and reads the candidates again, and takes the
// organization's audit and outbox locks and commits for itself. Only the
// first record of a device in the window is updated here, in the order of
// the records and after the new devices; the others follow one by one. A
// record that fails rolls the window back, and its records are processed
// one by one (processWindow). Without the locks the records use the
// window's candidates in transactions of their own (WP-333).
func (h *Handler) updateInWindow(ctx context.Context, orgID string, frame *batchFrame, start int, first []bool,
	results []ReconcileResult, candidates [][]ci.Item, w *windowOutcome) error {
	if !frame.locking {
		return nil
	}
	for k := range first {
		i := start + k
		// A device no collector profile matched opens its review on its own.
		if !first[k] || results[k].Action != ReconcileMatched || frame.noProfile[i] {
			continue
		}
		// The window works on a copy: a record it leaves is processed on its
		// own from what it reported.
		var out recordOutcome
		item := cloneItem(&frame.items[i])
		if err := h.reconcileRecord(ctx, orgID, frame, i, &item, candidates[k], &out); err != nil {
			return err
		}
		w.resp.add(&out.resp)
		w.done[k], w.ciID[k] = true, out.ciID
	}
	return nil
}

// cloneItem copies a record with its own attribute, fingerprint and raw
// data maps, which the batch changes or hands on.
func cloneItem(item *IngestItem) IngestItem {
	out := *item
	out.Attributes = maps.Clone(item.Attributes)
	out.Fingerprint = maps.Clone(item.Fingerprint)
	out.RawData = maps.Clone(item.RawData)
	return out
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

// raisedType returns the specific type typeID a matched generic_device CI
// is raised to (CI-11), or "": discovery never changes other types. A
// matched CI the candidates did not carry has no type here; the repository
// checks the types again in the record's transaction.
func raisedType(matched *ci.Item, typeID string, ic *IdentityContext) string {
	if ic == nil || ic.GenericDeviceTypeID == "" || typeID == "" || typeID == ic.GenericDeviceTypeID ||
		(matched.CITypeID != "" && matched.CITypeID != ic.GenericDeviceTypeID) {
		return ""
	}
	return typeID
}
