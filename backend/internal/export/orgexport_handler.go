package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The routes of the full export (TLC-05, WP-1151): an org-wide holder of
// org:export with a second factor starts the job org.export, every holder
// of org:export lists the exports and reads their status and manifest, and
// the download hands out signed URLs of the objects for 24 hours after the
// export completed. The routes belong to the module org_lifecycle.

// OrgExportTTL is how long a completed full export can be downloaded.
const OrgExportTTL = 24 * time.Hour

// OrgExportCleanupKind is the system job that removes the objects of
// expired full exports (AK4).
const OrgExportCleanupKind = "org.export_cleanup"

// ProblemTypeMFARequired is the problem type of a request that needs a
// session opened with a second factor (WP-1152).
const ProblemTypeMFARequired = "urn:reticora:problem:mfa-required"

// Status values of a full export. queued, running, failed and cancelled are
// those of its job; a succeeded job is completed until it expires.
const (
	OrgExportCompleted = "completed"
	OrgExportExpired   = "expired"
)

// OrgExport is the API representation of a full export.
type OrgExport struct {
	ID string `json:"id"`
	// JobID is the job of the export (the same id; the audit snapshot of
	// the start reads the job row by it).
	JobID       string     `json:"job_id"`
	Status      string     `json:"status"`
	Progress    int        `json:"progress"`
	InitiatedBy string     `json:"initiated_by,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// ExpiresAt is the completion plus 24 hours.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Manifest is the manifest of a completed export (GET /{id} only).
	Manifest *OrgExportManifest `json:"manifest,omitempty"`
}

// OrgExportDownload is the answer of GET /api/v1/org/exports/{id}/download.
type OrgExportDownload struct {
	ID        string              `json:"id"`
	ExpiresAt time.Time           `json:"expires_at"`
	Objects   []OrgExportDownItem `json:"objects"`
}

// OrgExportDownItem is one object of a full export with its signed URL.
type OrgExportDownItem struct {
	// Name is the path of the object below the export (tables/<t>/part-0001.jsonl,
	// documents-0001.zip, manifest.json).
	Name         string    `json:"name"`
	Bytes        int64     `json:"bytes"`
	SHA256       string    `json:"sha256"`
	URL          string    `json:"url"`
	URLExpiresAt time.Time `json:"url_expires_at"`
}

// orgExportResult is the stored result of a full export: its manifest and
// the time the cleanup removed the objects (expired_at).
type orgExportResult struct {
	OrgExportManifest
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
}

// OrgExportHandler serves /api/v1/org/exports.
type OrgExportHandler struct {
	jobs   *jobs.Service
	pool   *pgxpool.Pool
	blobs  blob.Store
	bucket string
	now    func() time.Time
}

// NewOrgExportHandler builds the handler on the job service, the
// application pool (the audit entry of a download) and the object storage.
// Without job service the start answers 503; without storage the download
// does.
func NewOrgExportHandler(svc *jobs.Service, pool *pgxpool.Pool, blobs blob.Store) *OrgExportHandler {
	return &OrgExportHandler{jobs: svc, pool: pool, blobs: blobs, bucket: ExportBucket, now: time.Now}
}

// WithClock sets the clock of the download period (tests).
func (h *OrgExportHandler) WithClock(now func() time.Time) *OrgExportHandler {
	h.now = now
	return h
}

// RegisterRoutes registers the routes of the full export.
func (h *OrgExportHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/org/exports", h.Create)
	r.Get("/api/v1/org/exports", h.List)
	r.Get("/api/v1/org/exports/{id}", h.Get)
	r.Get("/api/v1/org/exports/{id}/download", h.Download)
}

var (
	errOrgExportRunning    = errors.New("a full export of the organization is already queued or running")
	errOrgExportNotFound   = errors.New("full export not found")
	errOrgExportIncomplete = errors.New("the full export is not complete")
	errOrgExportGone       = errors.New("the download period of the full export has ended")
)

// requireMFA checks what the route permission (org:export, routeRules)
// cannot: the right for the whole organization (the export reads every
// row of it) and a user session opened with a second factor (403
// mfa-required); an API key never has one.
func requireMFA(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing principal")
		return p, false
	}
	if !identity.HoldsOrgWide(r.Context(), identity.PermOrgExport) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only a holder of org:export for the whole organization may export or download it")
		return p, false
	}
	if !p.MFA || p.Type != identity.PrincipalTypeUser {
		api.WriteProblem(w, http.StatusForbidden, ProblemTypeMFARequired, "Forbidden",
			"the full export of the organization needs a session opened with a second factor; sign in again with MFA")
		return p, false
	}
	return p, true
}

func orgOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return orgID, true
}

// Create starts a full export (202 with the export); 409 while another one
// is queued or running.
func (h *OrgExportHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	p, ok := requireMFA(w, r)
	if !ok {
		return
	}
	if h.jobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the full export needs the job service")
		return
	}
	job, err := h.enqueueSingle(r.Context(), orgID, p.Subject)
	if errors.Is(err, errOrgExportRunning) {
		api.WriteError(w, http.StatusConflict, "Conflict", errOrgExportRunning.Error())
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, h.view(job, false))
}

// enqueueSingle queues the job org.export unless one of the organization
// is queued or running (errOrgExportRunning). The check and the insert run
// in one tenant transaction behind a transaction lock of the organization,
// so two starts at the same time queue one export, never two copies of
// the organization (E-165). Without database (memory profile) there is no
// concurrent start to serialize.
func (h *OrgExportHandler) enqueueSingle(ctx context.Context, orgID, subject string) (*jobs.Job, error) {
	var job *jobs.Job
	enqueue := func(ctx context.Context) error {
		for _, status := range []jobs.Status{jobs.StatusQueued, jobs.StatusRunning} {
			_, n, err := h.jobs.List(ctx, orgID, jobs.Filter{Kind: OrgExportJobKind, Status: status}, api.PaginationParams{Limit: 1})
			if err != nil {
				return err
			}
			if n > 0 {
				return errOrgExportRunning
			}
		}
		var err error
		job, _, err = h.jobs.Enqueue(ctx, orgID, OrgExportJobKind, map[string]any{}, jobs.EnqueueOptions{CreatedBy: subject})
		return err
	}
	if h.pool == nil {
		return job, enqueue(ctx)
	}
	err := database.WithRequestTenant(ctx, h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('org.export:' || $1::text))`, orgID); err != nil {
			return err
		}
		// The job service joins this transaction (ContextWithTx).
		return enqueue(database.ContextWithTx(ctx, tx))
	})
	return job, err
}

// List answers the full exports of the organization, newest first.
func (h *OrgExportHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	var list []jobs.Job
	total := 0
	if h.jobs != nil {
		var err error
		if list, total, err = h.jobs.List(r.Context(), orgID, jobs.Filter{Kind: OrgExportJobKind}, page); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	data := make([]OrgExport, 0, len(list))
	for i := range list {
		data = append(data, h.view(&list[i], false))
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[OrgExport]{Data: data, Total: total, Limit: page.Limit,
		Offset: page.Offset, HasMore: page.Offset+len(data) < total})
}

// Get answers one export with its manifest once complete; an export of
// another organization, another job and an unknown id are 404.
func (h *OrgExportHandler) Get(w http.ResponseWriter, r *http.Request) {
	job, ok := h.load(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, h.view(job, true))
}

// Download answers the objects of a completed export with signed URLs
// valid at most until expires_at (410 after it); every call writes an
// audit entry (resource_type org).
func (h *OrgExportHandler) Download(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	p, ok := requireMFA(w, r)
	if !ok {
		return
	}
	job, ok := h.load(w, r)
	if !ok {
		return
	}
	view := h.view(job, true)
	switch view.Status {
	case OrgExportCompleted:
		if view.ExpiresAt == nil || view.Manifest == nil {
			api.WriteError(w, http.StatusConflict, "Conflict", errOrgExportIncomplete.Error())
			return
		}
	case OrgExportExpired:
		api.WriteError(w, http.StatusGone, "Gone", errOrgExportGone.Error())
		return
	default:
		api.WriteError(w, http.StatusConflict, "Conflict", errOrgExportIncomplete.Error())
		return
	}
	if h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the download needs the object storage")
		return
	}
	now := h.now().UTC()
	remaining := view.ExpiresAt.Sub(now)
	if remaining < time.Second {
		api.WriteError(w, http.StatusGone, "Gone", errOrgExportGone.Error())
		return
	}
	urlExpires := now.Add(min(remaining, blob.PresignTTL))
	out := OrgExportDownload{ID: job.ID, ExpiresAt: *view.ExpiresAt, Objects: []OrgExportDownItem{}}
	prefix := OrgExportPrefix(orgID, job.ID) + "/"
	add := func(key string, size int64, sum string) error {
		if err := blob.CheckOrgKey(orgID, key); err != nil || !strings.HasPrefix(key, prefix) {
			return fmt.Errorf("object outside the export: %w", blob.ErrForeignKey)
		}
		url, err := h.blobs.PresignedGetURL(r.Context(), h.bucket, key,
			blob.Download{FileName: path.Base(key), ContentType: orgExportObjectType(key), MaxAge: remaining})
		if err != nil {
			return err
		}
		out.Objects = append(out.Objects, OrgExportDownItem{Name: strings.TrimPrefix(key, prefix), Bytes: size, SHA256: sum,
			URL: url, URLExpiresAt: urlExpires})
		return nil
	}
	m := view.Manifest
	var err error
	for i := range m.Tables {
		for _, part := range m.Tables[i].Parts {
			if err = add(part.Key, part.Bytes, part.SHA256); err != nil {
				break
			}
		}
	}
	if err == nil && m.Documents != nil {
		for _, part := range m.Documents.Parts {
			if err = add(part.Key, part.Bytes, part.SHA256); err != nil {
				break
			}
		}
	}
	if err == nil {
		key := OrgExportManifestKey(orgID, job.ID)
		var size int64
		var sum string
		if size, sum, err = h.digest(r.Context(), key); err == nil {
			err = add(key, size, sum)
		}
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "org export download: sign objects", "export", job.ID, "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the objects of the export could not be signed")
		return
	}
	if err := h.auditDownload(r.Context(), orgID, p.Subject, job.ID, len(out.Objects)); err != nil {
		slog.ErrorContext(r.Context(), "org export download: audit", "export", job.ID, "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the download could not be audited")
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// digest reads an object and returns its size and SHA-256.
func (h *OrgExportHandler) digest(ctx context.Context, key string) (size int64, sum string, err error) {
	rc, err := h.blobs.Get(ctx, h.bucket, key)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = rc.Close() }()
	hash := sha256.New()
	if size, err = io.Copy(hash, rc); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

// auditDownload writes the audit entry of a download in the tenant
// transaction of the organization (AK3); the URLs are handed out only
// after its commit. Without database there is no audit log.
func (h *OrgExportHandler) auditDownload(ctx context.Context, orgID, actor, id string, objects int) error {
	if h.pool == nil {
		return nil
	}
	actorType := "user"
	if !api.IsUUID(actor) {
		actor, actorType = "", "system"
	}
	entry := audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType, Action: "org.export_downloaded",
		ResourceType: "org", ResourceID: id, Source: "api", Changes: map[string]any{"export_id": id, "objects": objects}}
	return database.WithRequestTenant(ctx, h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return database.BeforeCommit(ctx, func() error {
			_, err := audit.NewPGRecorder().Record(ctx, tx, entry)
			return err
		})
	})
}

// load reads the export of the path; another kind of job is 404.
func (h *OrgExportHandler) load(w http.ResponseWriter, r *http.Request) (*jobs.Job, bool) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return nil, false
	}
	id := chi.URLParam(r, "id")
	if h.jobs == nil || !api.IsUUID(id) {
		api.WriteError(w, http.StatusNotFound, "Not Found", errOrgExportNotFound.Error())
		return nil, false
	}
	job, err := h.jobs.Get(r.Context(), orgID, id)
	if errors.Is(err, jobs.ErrNotFound) || (err == nil && job.Kind != OrgExportJobKind) {
		api.WriteError(w, http.StatusNotFound, "Not Found", errOrgExportNotFound.Error())
		return nil, false
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return nil, false
	}
	return job, true
}

// view maps the job of an export onto its representation; withManifest
// adds the manifest of a completed export.
func (h *OrgExportHandler) view(job *jobs.Job, withManifest bool) OrgExport {
	out := OrgExport{ID: job.ID, JobID: job.ID, Status: string(job.Status), Progress: job.Progress, InitiatedBy: job.CreatedBy,
		Error: job.Error, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt}
	if job.Status != jobs.StatusSucceeded {
		return out
	}
	out.Status = OrgExportCompleted
	out.CompletedAt = job.FinishedAt
	var res orgExportResult
	if len(job.Result) > 0 {
		if err := json.Unmarshal(job.Result, &res); err != nil {
			slog.Warn("org export: decode result", "export", job.ID, "error", err)
		}
	}
	if done := orgExportCompletedAt(job, &res.OrgExportManifest); done != nil {
		expires := done.Add(OrgExportTTL).UTC()
		out.CompletedAt, out.ExpiresAt = done, &expires
		if !expires.After(h.now()) {
			out.Status = OrgExportExpired
		}
	}
	if res.ExpiredAt != nil {
		out.Status = OrgExportExpired
	}
	if withManifest && out.Status == OrgExportCompleted {
		m := res.OrgExportManifest
		out.Manifest = &m
	}
	return out
}

// orgExportCompletedAt is the completion of a succeeded export: the end of
// its job, else the completion its manifest records.
func orgExportCompletedAt(job *jobs.Job, m *OrgExportManifest) *time.Time {
	if job.FinishedAt != nil {
		t := job.FinishedAt.UTC()
		return &t
	}
	if m.CompletedAt != nil {
		t := m.CompletedAt.UTC()
		return &t
	}
	return nil
}

// orgExportObjectType is the content type of an object of the export.
func orgExportObjectType(key string) string {
	switch {
	case strings.HasSuffix(key, ".jsonl"):
		return orgExportPartType
	case strings.HasSuffix(key, ".zip"):
		return "application/zip"
	default:
		return orgExportManifestType
	}
}

// SucceededEvents implements jobs.SuccessEvents: org.export_completed in the
// transaction that marks the job succeeded (AK5), with the user who started
// it, the rows and bytes of the export and the end of its download period.
func (m *OrgExportManifest) SucceededEvents(j *jobs.Job) []*events.Event {
	done := orgExportCompletedAt(j, m)
	if done == nil {
		now := time.Now().UTC()
		done = &now
	}
	var rows, size int64
	for i := range m.Tables {
		rows += m.Tables[i].Rows
		for _, p := range m.Tables[i].Parts {
			size += p.Bytes
		}
	}
	if m.Documents != nil {
		for _, p := range m.Documents.Parts {
			size += p.Bytes
		}
	}
	data := events.OrgExportData{ID: j.ID, InitiatedBy: j.CreatedBy, Rows: rows, Bytes: size,
		ExpiresAt: done.Add(OrgExportTTL).UTC().Format(time.RFC3339)}
	return []*events.Event{{OrgID: j.OrganizationID, Type: events.OrgExportCompleted, EntityID: j.ID, Data: data}}
}
