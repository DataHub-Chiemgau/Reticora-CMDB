package labels

// Label batches of inventory areas (INV-03, WP-763, E-52, E-135): POST
// /api/v1/labels/batch renders the labels of a list of objects of one kind
// or of every object of a kind in a subtree of the location tree (the
// inventory area) as one PDF in the job labels.batch. The PDF lies in the
// bucket exports under a key derived from organization and job; the job
// result names the number of labels and pages and the expiry, never the
// key or a URL. GET /api/v1/labels/batch/{job_id}/download answers the
// creator of the job with a URL signed per request.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// BatchJobKind is the job kind of a label batch.
const BatchJobKind = "labels.batch"

// BatchBucket is the bucket of the batch PDFs: the bucket of the exports
// (export.ExportBucket), the only one besides the documents whose signed
// URLs frontend/objects-proxy.conf forwards to the object store (E-135).
const BatchBucket = "exports"

// Limits of a batch.
const (
	// MaxBatchIDs is the most ids a batch request names.
	MaxBatchIDs = 1000
	// MaxBatchLabels is the most labels one batch renders (100 A4 sheets
	// of 24); a larger inventory area is split into subtrees.
	MaxBatchLabels = 2400
	// BatchTTL is how long the PDF of a batch can be downloaded, like a
	// completed export (export.DefaultJobTTL).
	BatchTTL = 24 * time.Hour
)

// Errors of a batch run.
var (
	ErrBatchEmpty    = errors.New("the batch names no object the creator can see")
	ErrBatchTooLarge = fmt.Errorf("the batch has more than %d labels", MaxBatchLabels)
	errBatchStore    = errors.New("the label batch has no blob storage or object source")
)

// BatchRequest is the body of POST /api/v1/labels/batch and the parameters
// of its job: the template (default a4_24), the kind of the objects and
// either their ids or the location whose subtree is the inventory area.
type BatchRequest struct {
	Template   string   `json:"template,omitempty"`
	Kind       string   `json:"kind"`
	IDs        []string `json:"ids,omitempty"`
	LocationID string   `json:"location_id,omitempty"`
}

// BatchResult is the result of a batch job; it carries neither the object
// key nor a URL (E-135).
type BatchResult struct {
	Labels    int       `json:"labels"`
	Pages     int       `json:"pages"`
	ExpiresAt time.Time `json:"expires_at"`
}

// BatchDownload is the answer of GET /api/v1/labels/batch/{job_id}/download.
type BatchDownload struct {
	JobID       string    `json:"job_id"`
	Labels      int       `json:"labels"`
	Pages       int       `json:"pages"`
	ExpiresAt   time.Time `json:"expires_at"`
	DownloadURL string    `json:"download_url"`
}

// BatchItem is one object of a batch as the source reads it: the label
// fields and, for an asset, what its field permissions need.
type BatchItem struct {
	Kind         string
	ID           string
	Name         string
	InventoryNo  string
	Manufacturer string
	Model        string
	// Subtitle is the second line of a consumable (its SKU) or a location
	// (its kind).
	Subtitle string
	TypeID   string
	ClientID string
	SiteID   string
}

// BatchSource reads the objects of a batch under the tenant scope of the
// context; the PostgreSQL resolver implements it.
type BatchSource interface {
	Get(ctx context.Context, orgID string, ref Ref) (*Object, error)
	BatchItems(ctx context.Context, orgID string, req *BatchRequest) ([]BatchItem, error)
}

// BatchJobs runs batches in this process and reads their jobs;
// jobs.Service implements it.
type BatchJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
	Get(ctx context.Context, orgID, id string) (*jobs.Job, error)
}

// BatchQueue submits batches to the job queue on NATS, whose workers run
// them with RunJob; jobs.Queue implements it.
type BatchQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// CreatorPolicy resolves the field policy of the user who created a batch
// job: the queue runs it without principal (jobs.CreatorContext).
type CreatorPolicy func(ctx context.Context, orgID, userID string) (*permission.FieldPolicy, error)

// BatchHandler serves the label batches.
type BatchHandler struct {
	source        BatchSource
	blobs         blob.Store
	jobs          BatchJobs
	queue         BatchQueue
	features      entitlement.Checker
	creatorPolicy CreatorPolicy
	now           func() time.Time
}

// NewBatchHandler creates the batch handler; without source, blob storage
// or job service (WithJobs) its routes answer 503.
func NewBatchHandler(source BatchSource, blobs blob.Store) *BatchHandler {
	return &BatchHandler{source: source, blobs: blobs, now: time.Now}
}

// WithJobs sets the job service: it reads the jobs and, without queue,
// runs the batches in this process.
func (h *BatchHandler) WithJobs(j BatchJobs) *BatchHandler {
	h.jobs = j
	return h
}

// WithQueue runs the batches on the job queue instead of in this process.
func (h *BatchHandler) WithQueue(q BatchQueue) *BatchHandler {
	h.queue = q
	return h
}

// WithFeatures checks the feature of the kind (assets_basic, stock); nil
// entitles every organization. The gate barcode of the path is the
// entitlement middleware's.
func (h *BatchHandler) WithFeatures(features entitlement.Checker) *BatchHandler {
	h.features = features
	return h
}

// WithCreatorPolicy sets the resolver of the creator's field policy for
// queued batches; without it an asset label of a queued batch shows only
// the structural fields (fail-closed).
func (h *BatchHandler) WithCreatorPolicy(p CreatorPolicy) *BatchHandler {
	h.creatorPolicy = p
	return h
}

// RegisterRoutes registers the batch routes.
func (h *BatchHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/labels/batch", h.Create)
	r.Get("/api/v1/labels/batch/{job_id}/download", h.Download)
}

// BatchObjectKey is the blob key of the PDF of a batch job.
func BatchObjectKey(orgID, jobID string) string {
	return blob.OrgKey(orgID, "labels", jobID+".pdf")
}

// normalize checks the request and fills the default template; ids are
// lower case and unique.
func (req *BatchRequest) normalize() error {
	if req.Template == "" {
		req.Template = TemplateA4x24
	}
	if _, err := Layout(req.Template); err != nil {
		return &api.FieldError{Field: "template", Message: "must be asset_50x25, bin_100x50 or a4_24", Status: http.StatusUnprocessableEntity}
	}
	if _, known := kindRules[req.Kind]; !known {
		return &api.FieldError{Field: "kind", Message: "must be asset, consumable or location", Status: http.StatusUnprocessableEntity}
	}
	switch {
	case len(req.IDs) > 0 && req.LocationID != "":
		return &api.FieldError{Field: "location_id", Message: "must not be combined with ids", Status: http.StatusUnprocessableEntity}
	case len(req.IDs) == 0 && req.LocationID == "":
		return &api.FieldError{Field: "ids", Message: "ids or location_id is required", Status: http.StatusUnprocessableEntity}
	case len(req.IDs) > MaxBatchIDs:
		return &api.FieldError{Field: "ids", Message: fmt.Sprintf("must not name more than %d ids", MaxBatchIDs), Status: http.StatusUnprocessableEntity}
	}
	if req.LocationID != "" {
		if err := api.RequireUUID("location_id", req.LocationID); err != nil {
			return err
		}
		req.LocationID = strings.ToLower(req.LocationID)
	}
	seen := make(map[string]bool, len(req.IDs))
	ids := req.IDs[:0]
	for _, id := range req.IDs {
		if err := api.RequireUUID("ids", id); err != nil {
			return err
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	req.IDs = ids
	return nil
}

// Create handles POST /api/v1/labels/batch: the batch becomes a job of
// the kind labels.batch (202 with job_id and Location). The caller needs
// the read permission of the kind (403) and the organization its feature
// (403 feature-not-entitled); the location of an inventory area must be
// visible to the caller (404).
func (h *BatchHandler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req BatchRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	if err := req.normalize(); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	rule := kindRules[req.Kind]
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok || !principal.Has(rule.permission) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission "+string(rule.permission))
		return
	}
	if rule.feature != "" && h.features != nil && !h.features.IsEnabled(r.Context(), t.OrganizationID, rule.feature) {
		api.WriteProblem(w, http.StatusForbidden, api.ProblemFeatureNotEntitled, "Forbidden",
			"the organization is not entitled to the feature "+rule.feature)
		return
	}
	if h.source == nil || h.blobs == nil || h.jobs == nil {
		api.WriteUnavailable(w, "label batches are not available")
		return
	}
	if req.LocationID != "" {
		if _, err := h.source.Get(r.Context(), t.OrganizationID, Ref{Kind: KindLocation, ID: req.LocationID}); err != nil {
			if errors.Is(err, ErrNotFound) {
				api.WriteError(w, http.StatusNotFound, "Not Found", "location not found")
				return
			}
			slog.ErrorContext(r.Context(), "label batch: read the inventory area", "error", err)
			api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the inventory area could not be read")
			return
		}
	}
	job, err := h.submit(r, t, &req)
	if err != nil {
		slog.ErrorContext(r.Context(), "label batch: submit the job", "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the label batch could not be started")
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
}

// submit queues the batch on the job queue or runs it in this process
// with the field policy of the request.
func (h *BatchHandler) submit(r *http.Request, t tenant.TenantInfo, req *BatchRequest) (*jobs.Job, error) {
	orgID := t.OrganizationID
	if h.queue != nil {
		job, _, err := h.queue.Submit(r.Context(), orgID, BatchJobKind, req, jobs.EnqueueOptions{CreatedBy: t.UserID})
		return job, err
	}
	var policy *permission.FieldPolicy
	if req.Kind == KindAsset {
		var err error
		if policy, err = permission.PolicyFrom(r.Context()); err != nil {
			return nil, err
		}
	}
	// The key of the PDF needs the id of the job, known once the job
	// exists; the run waits for it.
	jobID := make(chan string, 1)
	params := *req
	job, err := h.jobs.RunDetached(r.Context(), orgID, BatchJobKind, params, t.UserID, func(ctx context.Context) (any, error) {
		select {
		case id := <-jobID:
			return h.render(ctx, orgID, id, &params, policy)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err != nil {
		return nil, err
	}
	jobID <- job.ID
	return job, nil
}

// RunJob runs a batch job of the job queue (handler of labels.batch) with
// the tenant and scope of its creator (jobs.CreatorContext) and, for
// assets, the field policy of the creator. It only reads; the result of a
// cancelled run is dropped.
func (h *BatchHandler) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	var req BatchRequest
	if err := json.Unmarshal(job.Params, &req); err != nil {
		return nil, fmt.Errorf("decode label batch parameters: %w", err)
	}
	if err := req.normalize(); err != nil {
		return nil, fmt.Errorf("invalid label batch parameters: %w", err)
	}
	ctx = jobs.CreatorContext(ctx, job)
	var policy *permission.FieldPolicy
	if req.Kind == KindAsset && h.creatorPolicy != nil {
		var err error
		if policy, err = h.creatorPolicy(ctx, job.OrganizationID, job.CreatedBy); err != nil {
			return nil, fmt.Errorf("resolve the field permissions of the job creator: %w", err)
		}
	}
	return h.render(ctx, job.OrganizationID, job.ID, &req, policy)
}

// render reads the objects of the batch, renders their labels and stores
// the PDF under the key of the job.
func (h *BatchHandler) render(ctx context.Context, orgID, jobID string, req *BatchRequest, policy *permission.FieldPolicy) (*BatchResult, error) {
	if h.source == nil || h.blobs == nil {
		return nil, errBatchStore
	}
	items, err := h.source.BatchItems(ctx, orgID, req)
	if err != nil {
		return nil, err
	}
	switch {
	case len(items) == 0:
		return nil, ErrBatchEmpty
	case len(items) > MaxBatchLabels:
		return nil, ErrBatchTooLarge
	}
	subjects := make([]Subject, len(items))
	for i := range items {
		subjects[i] = items[i].subject(policy)
	}
	pdf, err := RenderPDF(req.Template, subjects)
	if err != nil {
		return nil, fmt.Errorf("render the label batch: %w", err)
	}
	if cancelled := ctx.Err(); cancelled != nil {
		return nil, cancelled
	}
	if putErr := h.blobs.Put(ctx, BatchBucket, BatchObjectKey(orgID, jobID), bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); putErr != nil {
		return nil, fmt.Errorf("store the label batch: %w", putErr)
	}
	page, _ := Layout(req.Template)
	perPage := len(page.Cells)
	return &BatchResult{Labels: len(subjects), Pages: (len(subjects) + perPage - 1) / perPage,
		ExpiresAt: h.now().UTC().Add(BatchTTL)}, nil
}

// subject maps the item onto its label: an asset as asset.LabelSubject
// does (WP-849) with the fields the policy lets the reader see (RBA-07),
// an unreadable inventory number makes the URN the code; consumables and
// locations carry their URN.
func (it *BatchItem) subject(policy *permission.FieldPolicy) Subject {
	s := Subject{Kind: it.Kind, ID: it.ID, Code: URN(it.Kind, it.ID), Title: it.Name, Subtitle: it.Subtitle}
	if it.Kind == KindAsset {
		ref := permission.ObjectRef{ID: it.ID, ClientID: it.ClientID, SiteID: it.SiteID}
		readable := func(path, v string) string {
			if v == "" || !permission.Readable(policy, permission.KindAsset, it.TypeID, ref, path) {
				return ""
			}
			return v
		}
		if code := strings.TrimSpace(readable("inventory_no", it.InventoryNo)); code != "" {
			s.Code = it.InventoryNo
		}
		s.Title = readable("name", it.Name)
		s.Subtitle = strings.TrimSpace(readable("manufacturer", it.Manufacturer) + " " + readable("model", it.Model))
	}
	if s.Title == "" {
		s.Title = s.Code
	}
	return s
}

// Download handles GET /api/v1/labels/batch/{job_id}/download: the creator
// of a finished batch gets a URL to the PDF, signed for this request
// (E-135); every other caller and every other job is 404, a batch without
// a valid result 409.
func (h *BatchHandler) Download(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.blobs == nil || h.jobs == nil {
		api.WriteUnavailable(w, "label batches are not available")
		return
	}
	const notFound = "label batch not found"
	id, ok := api.ParseUUIDParam(w, r, "job_id", notFound)
	if !ok {
		return
	}
	job, err := h.jobs.Get(r.Context(), t.OrganizationID, id)
	if errors.Is(err, jobs.ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", notFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "label batch: read the job", "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the label batch could not be read")
		return
	}
	// The PDF was rendered with the scope of the creator: only the
	// creator gets it.
	if job.Kind != BatchJobKind || t.UserID == "" || !strings.EqualFold(job.CreatedBy, t.UserID) {
		api.WriteError(w, http.StatusNotFound, "Not Found", notFound)
		return
	}
	var result BatchResult
	if job.Status != jobs.StatusSucceeded || json.Unmarshal(job.Result, &result) != nil || result.Labels == 0 ||
		!result.ExpiresAt.After(h.now()) {
		api.WriteError(w, http.StatusConflict, "Conflict", "the label batch has no valid result")
		return
	}
	signed, err := h.blobs.PresignedGetURL(r.Context(), BatchBucket, BatchObjectKey(job.OrganizationID, job.ID))
	if err != nil {
		slog.ErrorContext(r.Context(), "label batch: sign the download", "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the download could not be signed")
		return
	}
	api.WriteJSON(w, http.StatusOK, BatchDownload{JobID: job.ID, Labels: result.Labels, Pages: result.Pages,
		ExpiresAt: result.ExpiresAt, DownloadURL: signed})
}

// batchColumns are the label columns of each kind, in the order of
// BatchItem: id, name, inventory number, manufacturer, model, subtitle,
// type, client, site.
var batchColumns = map[string]string{
	KindAsset: `o.id::text, o.name, COALESCE(o.inventory_no, ''), COALESCE(o.manufacturer, ''), COALESCE(o.model, ''), '',
		COALESCE(o.asset_type_id::text, ''), COALESCE(o.client_id::text, ''), COALESCE(o.site_id::text, '')`,
	KindConsumable: `o.id::text, o.name, '', '', '', COALESCE(o.sku, ''), '', COALESCE(o.client_id::text, ''), ''`,
	KindLocation:   `o.id::text, o.name, '', '', '', o.kind, '', '', ''`,
}

// batchTables are the tables of the kinds.
var batchTables = map[string]string{KindAsset: "asset", KindConsumable: "consumable", KindLocation: "location"}

// batchArea is the filter of each kind on the inventory area $2: an asset
// whose location lies in the subtree, a consumable with stock in a bin of
// the subtree, a location of the subtree. The area itself is read under
// the scope as well; an invisible area has no objects.
var batchArea = map[string]string{
	KindAsset: ` JOIN location l ON l.id = o.location_id AND l.organization_id = o.organization_id
		WHERE o.organization_id = $1 AND l.path <@ (SELECT a.path FROM location a WHERE a.organization_id = $1 AND a.id = $2)
		ORDER BY l.path, o.name, o.id`,
	KindConsumable: ` WHERE o.organization_id = $1 AND EXISTS (
		SELECT 1 FROM stock_level s JOIN location l ON l.id = s.location_id AND l.organization_id = s.organization_id
		 WHERE s.organization_id = o.organization_id AND s.consumable_id = o.id
		   AND l.path <@ (SELECT a.path FROM location a WHERE a.organization_id = $1 AND a.id = $2))
		ORDER BY o.name, o.id`,
	KindLocation: ` WHERE o.organization_id = $1
		AND o.path <@ (SELECT a.path FROM location a WHERE a.organization_id = $1 AND a.id = $2)
		ORDER BY o.path, o.id`,
}

// BatchItems reads the objects of a batch under the tenant scope of the
// context (row level security on client and site): the named ids or the
// objects of the inventory area. Objects outside the scope are left out.
func (r *PGRepository) BatchItems(ctx context.Context, orgID string, req *BatchRequest) ([]BatchItem, error) {
	columns, known := batchColumns[req.Kind]
	if !known {
		return nil, ErrNotFound
	}
	query := "SELECT " + columns + " FROM " + batchTables[req.Kind] + " o"
	args := []any{orgID}
	if req.LocationID != "" {
		query += batchArea[req.Kind]
		args = append(args, req.LocationID)
	} else {
		query += " WHERE o.organization_id = $1 AND o.id = ANY($2::uuid[]) ORDER BY o.name, o.id"
		args = append(args, req.IDs)
	}
	query += fmt.Sprintf(" LIMIT %d", MaxBatchLabels+1)
	var items []BatchItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, query, args...)
		if queryErr != nil {
			return fmt.Errorf("read the label batch: %w", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			it := BatchItem{Kind: req.Kind}
			if scanErr := rows.Scan(&it.ID, &it.Name, &it.InventoryNo, &it.Manufacturer, &it.Model, &it.Subtitle,
				&it.TypeID, &it.ClientID, &it.SiteID); scanErr != nil {
				return fmt.Errorf("scan the label batch: %w", scanErr)
			}
			items = append(items, it)
		}
		return rows.Err()
	})
	return items, err
}

var _ BatchSource = (*PGRepository)(nil)
