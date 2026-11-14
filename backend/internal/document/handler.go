package document

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for document endpoints.
type Handler struct {
	repo   Repository
	blobs  blob.Store
	bucket string
	// limits resolves max_storage_mb (WithLimits); nil is unlimited.
	limits StorageLimiter
	// uploads are the size, parallelism and deadline of uploads
	// (WithUploads, SEC-09).
	uploads uploadLimits
	// scans submits the malware scan of a stored version (WithScans,
	// WP-734); nil leaves every new version pending_scan.
	scans ScanSubmitter
}

// NewHandler creates a new document handler. blobs may be nil; content
// upload/download then answer 503 while metadata CRUD keeps working.
// Uploads use the defaults of config.DocumentSettings until WithUploads.
func NewHandler(repo Repository, blobs blob.Store) *Handler {
	h := &Handler{repo: repo, uploads: newUploadLimits(config.DocumentSettings{})}
	if blobs != nil {
		h.blobs, h.bucket = blobs, documentBucket
	}
	return h
}

// WithUploads sets the upload limits (SEC-09, WP-268): the largest body
// (RETICORA_DOCUMENT_MAX_UPLOAD_MB), the uploads running at the same time on
// this instance (RETICORA_DOCUMENT_UPLOAD_CONCURRENCY) and the read and
// write deadline of an upload request (RETICORA_DOCUMENT_UPLOAD_TIMEOUT). A
// zero value keeps its default.
func (h *Handler) WithUploads(settings config.DocumentSettings) *Handler {
	h.uploads = newUploadLimits(settings)
	return h
}

// uploadLimits are the limits of the upload paths PUT …/content and POST
// …/versions.
type uploadLimits struct {
	maxBytes int64
	timeout  time.Duration
	// slots holds one token per running upload.
	slots chan struct{}
}

func newUploadLimits(s config.DocumentSettings) uploadLimits {
	if s.MaxUploadMB <= 0 {
		s.MaxUploadMB = config.DefaultDocumentMaxUploadMB
	}
	if s.UploadConcurrency <= 0 {
		s.UploadConcurrency = config.DefaultDocumentUploadConcurrency
	}
	if s.UploadTimeout <= 0 {
		s.UploadTimeout = config.DefaultDocumentUploadTimeout
	}
	return uploadLimits{maxBytes: s.MaxUploadBytes(), timeout: s.UploadTimeout, slots: make(chan struct{}, s.UploadConcurrency)}
}

// uploadRetryAfter is the Retry-After (seconds) of an upload refused
// because all upload slots are taken.
const uploadRetryAfter = "5"

// beginUpload takes an upload slot and extends the read and write deadline
// of the connection to the upload timeout; the global 15 s of the server
// stay in force for every other route. Without a free slot it answers 503
// with Retry-After and reports false; otherwise the caller runs the
// returned release when the upload is done.
func (h *Handler) beginUpload(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case h.uploads.slots <- struct{}{}:
	default:
		w.Header().Set("Retry-After", uploadRetryAfter)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable",
			"too many document uploads in progress, retry later")
		return nil, false
	}
	// A writer without deadlines (tests) keeps the server's; the
	// middleware writers pass the controller through (Unwrap).
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(h.uploads.timeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
	return func() { <-h.uploads.slots }, true
}

// readUpload verifies and reads the body of an upload (sniff.go) and
// answers a refusal itself: 415 for a missing or non-whitelisted
// Content-Type, 413 above the size limit, 422 mime-mismatch for content
// that does not match its type. It returns the verified type and the body.
func (h *Handler) readUpload(w http.ResponseWriter, r *http.Request) (mimeType string, payload []byte, ok bool) {
	mimeType, payload, err := verifyUpload(w, r, r.Header.Get("Content-Type"), h.uploads.maxBytes)
	var maxErr *http.MaxBytesError
	var mm *mismatchError
	switch {
	case err == nil:
		return mimeType, payload, true
	case errors.Is(err, errUnsupportedType):
		api.WriteError(w, http.StatusUnsupportedMediaType, "Unsupported Media Type",
			fmt.Sprintf("content type %q is not allowed for document uploads", mimeType))
	case errors.As(err, &maxErr):
		api.WriteError(w, http.StatusRequestEntityTooLarge, "Payload Too Large",
			fmt.Sprintf("document content exceeds the %d MiB limit", h.uploads.maxBytes>>20))
	case errors.As(err, &mm):
		api.WriteProblem(w, http.StatusUnprocessableEntity, ProblemMimeMismatch, "Content Type Mismatch", mm.detail)
	default:
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "could not read request body")
	}
	return "", nil, false
}

// Problem types of a refused download or link (WP-733, SEC-09, E-42): a
// version not (yet) scanned clean, i.e. pending_scan or scan_failed, and
// a quarantined (infected) one. Both answer 409.
const (
	ProblemPendingScan = "https://reticora.io/problems/document-pending-scan"
	ProblemQuarantined = "https://reticora.io/problems/document-quarantined"
)

// writeScanRefusal answers a download of a version that is not clean with
// 409 (fail-closed, E-42): document-quarantined for infected,
// document-pending-scan for every other status, an unknown or missing one
// included. It reports whether it answered.
func writeScanRefusal(w http.ResponseWriter, status string) bool {
	switch status {
	case ScanClean:
		return false
	case ScanInfected:
		api.WriteProblem(w, http.StatusConflict, ProblemQuarantined, "Document Quarantined",
			"the document content was found infected and is quarantined")
	default:
		api.WriteProblem(w, http.StatusConflict, ProblemPendingScan, "Document Pending Scan",
			"the document content has not been scanned clean yet")
	}
	return true
}

// Bucket is the blob bucket document content is stored in; the import
// (WP-301) reads document versions from it.
const Bucket = "reticora-documents"

// documentBucket is the bucket of this package's handler, Bucket.
const documentBucket = Bucket

// RegisterRoutes registers document routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/documents", h.List)
	r.Post("/api/v1/documents", h.Create)
	r.Get("/api/v1/documents/usage", h.Usage)
	r.Get("/api/v1/documents/{id}", h.Get)
	r.Patch("/api/v1/documents/{id}", h.Update)
	r.Delete("/api/v1/documents/{id}", h.Delete)
	r.Put("/api/v1/documents/{id}/content", h.UploadContent)
	r.Get("/api/v1/documents/{id}/content", h.DownloadContent)
	r.Post("/api/v1/documents/{id}/versions", h.CreateVersion)
	r.Get("/api/v1/documents/{id}/versions", h.ListVersions)
	r.Post("/api/v1/documents/{id}/links", h.Link)
	r.Get("/api/v1/documents/{id}/links", h.GetLinks)
	r.Delete("/api/v1/documents/{id}/links/{link_id}", h.Unlink)
}

// List handles GET /api/v1/documents
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	filter := FilterParams{
		Category:   r.URL.Query().Get("category"),
		Search:     r.URL.Query().Get("search"),
		SortBy:     r.URL.Query().Get("sort_by"),
		SortDir:    r.URL.Query().Get("sort_dir"),
		EntityType: r.URL.Query().Get("entity_type"),
		EntityID:   r.URL.Query().Get("entity_id"),
	}
	// entity_type and entity_id only together (400), a type of the registry
	// (422); a target outside the scope or missing answers 404 (WP-811).
	if ferr := checkEntityFilter(filter.EntityType, filter.EntityID); ferr != nil {
		api.WriteRepoError(w, ferr)
		return
	}
	policy, ok := policyOf(w, r)
	if !ok {
		return
	}
	// A filter, search or sort on a field the principal cannot read is 403
	// with the fields (WP-267, pattern of contract and asset).
	if filterErr := permission.CheckListFilter(policy, permission.KindDocument, nil, listFieldPaths(&filter)); filterErr != nil {
		api.WriteFieldError(w, filterErr)
		return
	}

	items, total, err := h.repo.List(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		if errors.Is(err, api.ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "linked object not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	writeDocumentList(w, policy, &api.ListResponse[Document]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: NextCursor(items, &filter, page.Limit),
	})
}

// Get handles GET /api/v1/documents/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	policy, ok := policyOf(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	writeDocument(w, http.StatusOK, policy, item)
}

// Create handles POST /api/v1/documents
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Title == "" || req.FileName == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title and file_name are required")
		return
	}
	if ferr := checkCreateLinks(req.Links); ferr != nil {
		api.WriteRepoError(w, ferr)
		return
	}
	if refuseSignatureChange(w, nil, &req.Category) {
		return
	}
	policy, ok := policyOf(w, r)
	if !ok {
		return
	}
	// Every sent field must be editable (403 with the fields).
	if err := permission.CheckWrite(policy, permission.KindDocument, "", permission.ObjectRef{}, req.sentFields()); err != nil {
		api.WriteFieldError(w, err)
		return
	}

	// A document without links is org-wide, so a principal restricted in the
	// client, site or team scope creates one only with at least one link to
	// a target in its scope (E-09, E-11, E-61 option 1).
	if scope, ok := database.TenantScopeFromContext(r.Context()); ok && !orgWideDocumentScope(&scope) && len(req.Links) == 0 {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the operation is not permitted")
		return
	}

	category := req.Category
	if category == "" {
		category = "general"
	}
	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// req.StorageKey is ignored: the key is generated by UploadContent below
	// the organization's prefix (TEN-06).
	d := &Document{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		FileName:       req.FileName,
		FileSize:       req.FileSize,
		MimeType:       mimeType,
		Category:       category,
		Tags:           req.Tags,
		UploadedBy:     t.UserID,
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}

	links := make([]DocumentLink, len(req.Links))
	for i, l := range req.Links {
		links[i] = DocumentLink{OrganizationID: t.OrganizationID, EntityType: l.EntityType, EntityID: l.EntityID}
	}
	// Document and links in one transaction: an invisible or missing target
	// answers 404 and nothing is created.
	if err := h.repo.CreateWithLinks(r.Context(), d, links); err != nil {
		if errors.Is(err, api.ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "linked object not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	writeDocument(w, http.StatusCreated, policy, d)
}

// checkEntityFilter checks the query parameters entity_type and entity_id
// of GET /api/v1/documents: both or none (400), entity_id a UUID (400) and
// entity_type in the registry of links.go (422).
//
// validation: field errors with fixed texts.
func checkEntityFilter(entityType, entityID string) error {
	switch {
	case entityType == "" && entityID == "":
		return nil
	case entityType == "":
		return &api.FieldError{Field: "entity_type", Message: "is required together with entity_id"}
	case entityID == "":
		return &api.FieldError{Field: "entity_id", Message: "is required together with entity_type"}
	case !api.IsUUID(entityID):
		return errInvalidEntityID
	}
	return checkEntityType(entityType)
}

// checkCreateLinks checks the links of POST /api/v1/documents like
// POST …/links: entity_type and entity_id required and entity_id a UUID
// (400), entity_type in the registry (422). The field names the link,
// e.g. links[1].entity_type.
//
// validation: field errors with fixed texts.
func checkCreateLinks(links []LinkRequest) error {
	for i, l := range links {
		prefix := "links[" + strconv.Itoa(i) + "]."
		switch {
		case l.EntityType == "":
			return &api.FieldError{Field: prefix + "entity_type", Message: "is required"}
		case l.EntityID == "":
			return &api.FieldError{Field: prefix + "entity_id", Message: "is required"}
		case !api.IsUUID(l.EntityID):
			return &api.FieldError{Field: prefix + "entity_id", Message: "must be a UUID"}
		}
		if _, ok := linkTargets[l.EntityType]; !ok {
			return &api.FieldError{Field: prefix + "entity_type", Status: http.StatusUnprocessableEntity,
				Message: "must be one of " + strings.Join(EntityTypes(), ", ")}
		}
	}
	return nil
}

// Update handles PATCH /api/v1/documents/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	current, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}
	if refuseSignatureChange(w, current, req.Category) {
		return
	}
	policy, ok := policyOf(w, r)
	if !ok {
		return
	}
	// A field the principal may not edit is 403 with the fields (WP-267).
	if checkErr := checkUpdate(policy, current, &req); checkErr != nil {
		api.WriteFieldError(w, checkErr)
		return
	}

	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	writeDocument(w, http.StatusOK, policy, item)
}

// Delete handles DELETE /api/v1/documents/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	current, getErr := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if getErr != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}
	if refuseSignatureChange(w, current, nil) {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DownloadContent handles GET /api/v1/documents/{id}/content and answers with
// a time-limited presigned URL for the stored object: of version n with
// ?version=n (404 when the document has no such version), else of the
// current version. Only a version scanned clean is presigned (WP-733,
// fail-closed after E-42); any other answers 409 (writeScanRefusal).
func (h *Handler) DownloadContent(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "document download requires blob storage")
		return
	}

	version, ok := requestedVersion(r)
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "version must be a positive integer")
		return
	}
	policy, ok := policyOf(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	doc, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}
	key, scanStatus, mimeType := doc.StorageKey, doc.ScanStatus, doc.MimeType
	if version > 0 {
		v, verr := h.repo.GetVersion(r.Context(), t.OrganizationID, doc.ID, version)
		if verr != nil {
			if errors.Is(verr, api.ErrNotFound) {
				api.WriteError(w, http.StatusNotFound, "Not Found", "document version not found")
				return
			}
			api.WriteRepoError(w, verr)
			return
		}
		key, scanStatus, mimeType = v.StorageKey, v.ScanStatus, v.MimeType
	}
	if key == "" {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document has no stored content")
		return
	}
	if !ownsStorageKey(t.OrganizationID, doc.ID, key) {
		api.WriteError(w, http.StatusConflict, "Conflict", "document content is stored outside the organization")
		return
	}
	if writeScanRefusal(w, scanStatus) {
		return
	}

	url, err := h.blobs.PresignedGetURL(r.Context(), h.bucket, key, downloadOf(downloadName(policy, doc), mimeType))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not create a download URL")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"download_url": url})
}

// downloadOf is how the signed URL delivers a document: as an attachment
// with the file name of the document and its stored type, which the
// upload verified against the whitelist (sniff.go); any other type (a
// document stored before the whitelist) is delivered as
// application/octet-stream (finding of WP-829).
func downloadOf(fileName, mimeType string) blob.Download {
	dl := blob.Download{FileName: fileName, ContentType: blob.OctetStream}
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil && allowedMimeTypes[mediaType] {
		dl.ContentType = mediaType
	}
	return dl
}

// Link handles POST /api/v1/documents/{id}/links
func (h *Handler) Link(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	docID := chi.URLParam(r, "id")
	// Verify document exists; a signature document takes no further link.
	doc, getErr := h.repo.GetByID(r.Context(), t.OrganizationID, docID)
	if getErr != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}
	if refuseSignatureChange(w, doc, nil) {
		return
	}

	var req LinkRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.EntityType == "" || req.EntityID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "entity_type and entity_id are required")
		return
	}

	link := &DocumentLink{
		OrganizationID: t.OrganizationID,
		DocumentID:     docID,
		EntityType:     req.EntityType,
		EntityID:       req.EntityID,
	}

	// An unknown entity type answers 422, a target outside the scope or
	// missing 422 on entity_id like an unknown one (SR-50; 404 before, WP-267),
	// a quarantined document 409 (WP-733).
	if err := h.repo.LinkDocument(r.Context(), link); err != nil {
		if errors.Is(err, ErrTargetNotFound) {
			api.WriteFieldError(w, &api.FieldError{Field: "entity_id", Message: "not found", Status: http.StatusUnprocessableEntity})
			return
		}
		if errors.Is(err, ErrQuarantined) {
			api.WriteProblem(w, http.StatusConflict, ProblemQuarantined, "Document Quarantined",
				"the document is quarantined and cannot be linked")
			return
		}
		if errors.Is(err, api.ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "document or linked object not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, link)
}

// Unlink handles DELETE /api/v1/documents/{id}/links/{link_id} (E-38,
// WP-267): it removes a link the caller sees and may delete (204), any
// other link answers 404.
func (h *Handler) Unlink(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	docID, linkID := chi.URLParam(r, "id"), chi.URLParam(r, "link_id")
	doc, getErr := h.repo.GetByID(r.Context(), t.OrganizationID, docID)
	if getErr != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document link not found")
		return
	}
	if refuseSignatureChange(w, doc, nil) {
		return
	}
	if err := h.repo.UnlinkDocument(r.Context(), t.OrganizationID, docID, linkID); err != nil {
		if errors.Is(err, api.ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "document link not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetLinks handles GET /api/v1/documents/{id}/links
func (h *Handler) GetLinks(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	// {data, next_cursor} with limit and the signed cursor (API-02, F-075).
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")
	links, err := h.repo.GetLinks(r.Context(), t.OrganizationID, docID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.Page(links, page))
}

// OwnsStorageKey reports whether key is an object of document docID of
// organization orgID, the check of the download (ownsStorageKey) for
// readers outside the package such as the import (WP-301): the legacy keys
// documents/<org>/<doc>/… are accepted, unlike blob.CheckOrgKey.
func OwnsStorageKey(orgID, docID, key string) bool {
	return ownsStorageKey(orgID, docID, key)
}

// ownsStorageKey reports whether key is an object of document docID of
// organization orgID: org/<org>/documents/<doc>/… as written by UploadContent,
// or the legacy documents/<org>/<doc>/… of uploads before migration 000066.
func ownsStorageKey(orgID, docID, key string) bool {
	if blob.CheckOrgKey(orgID, key) == nil {
		return strings.HasPrefix(key, blob.OrgKey(orgID, "documents", docID)+"/")
	}
	legacy := LegacyPrefix(orgID) + docID + "/"
	return strings.HasPrefix(key, legacy) && !strings.Contains(key, "..")
}

// LegacyPrefix is the prefix of the document versions of an organization
// stored before migration 000066 (documents/<org>/<doc>/…); they are still
// read, and the purge of a deleted organization removes them (WP-1155).
func LegacyPrefix(orgID string) string { return "documents/" + orgID + "/" }
