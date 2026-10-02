package document

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// maxDocumentUpload is the hard limit for a single document upload.
const maxDocumentUpload = 25 << 20 // 25 MiB

// allowedMimeTypes restricts uploads to common, safe office/image formats.
// Executables, scripts and HTML are excluded on purpose: a stored file is
// later downloaded by other users, and serving attacker-controlled HTML/JS
// from our own origin would be an XSS vector.
var allowedMimeTypes = map[string]bool{
	"application/pdf":    true,
	"application/json":   true,
	"application/xml":    true,
	"application/zip":    true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.ms-powerpoint":                                             true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	"application/octet-stream": true,
	"image/png":                true,
	"image/jpeg":               true,
	"image/gif":                true,
	"image/svg+xml":            false, // SVG can carry script — excluded
	"image/webp":               true,
	"text/plain":               true,
	"text/csv":                 true,
}

// Handler provides HTTP handlers for document endpoints.
type Handler struct {
	repo   Repository
	blobs  blob.Store
	bucket string
}

// NewHandler creates a new document handler. blobs may be nil; content
// upload/download then answer 503 while metadata CRUD keeps working.
func NewHandler(repo Repository, blobs blob.Store) *Handler {
	if blobs == nil {
		return &Handler{repo: repo}
	}
	return &Handler{repo: repo, blobs: blobs, bucket: documentBucket}
}

// documentBucket is the blob bucket document content is stored in.
const documentBucket = "reticora-documents"

// RegisterRoutes registers document routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/documents", h.List)
	r.Post("/api/v1/documents", h.Create)
	r.Get("/api/v1/documents/{id}", h.Get)
	r.Patch("/api/v1/documents/{id}", h.Update)
	r.Delete("/api/v1/documents/{id}", h.Delete)
	r.Put("/api/v1/documents/{id}/content", h.UploadContent)
	r.Get("/api/v1/documents/{id}/content", h.DownloadContent)
	r.Post("/api/v1/documents/{id}/links", h.Link)
	r.Get("/api/v1/documents/{id}/links", h.GetLinks)
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
		Category: r.URL.Query().Get("category"),
		Search:   r.URL.Query().Get("search"),
		SortBy:   r.URL.Query().Get("sort_by"),
		SortDir:  r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Document]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: NextCursor(items, filter, page.Limit),
	})
}

// Get handles GET /api/v1/documents/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
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

	if err := h.repo.Create(r.Context(), d); err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, d)
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

	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/documents/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UploadContent handles PUT /api/v1/documents/{id}/content. The request body
// is stored as the document's object; the storage key is server-generated so
// a client can never overwrite another tenant's blob.
func (h *Handler) UploadContent(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "document content upload requires blob storage")
		return
	}

	id := chi.URLParam(r, "id")
	doc, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	mimeType := r.Header.Get("Content-Type")
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	allowed, known := allowedMimeTypes[mimeType]
	if !known || !allowed {
		api.WriteError(w, http.StatusUnsupportedMediaType, "Unsupported Media Type",
			fmt.Sprintf("content type %q is not allowed for document uploads", mimeType))
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxDocumentUpload)
	payload, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			api.WriteError(w, http.StatusRequestEntityTooLarge, "Payload Too Large",
				fmt.Sprintf("document content exceeds the %d MiB limit", maxDocumentUpload>>20))
			return
		}
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "could not read request body")
		return
	}

	storageKey := blob.OrgKey(t.OrganizationID, "documents", doc.ID, strconv.Itoa(doc.Version))
	if err := h.blobs.Put(r.Context(), h.bucket, storageKey,
		bytes.NewReader(payload), int64(len(payload)), mimeType); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not store the document content")
		return
	}

	updated, err := h.repo.SetStorage(r.Context(), t.OrganizationID, id, storageKey, mimeType, int64(len(payload)))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, updated)
}

// DownloadContent handles GET /api/v1/documents/{id}/content and answers with
// a time-limited presigned URL for the stored object.
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

	id := chi.URLParam(r, "id")
	doc, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}
	if doc.StorageKey == "" {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document has no stored content")
		return
	}
	if !ownsStorageKey(t.OrganizationID, doc.ID, doc.StorageKey) {
		api.WriteError(w, http.StatusConflict, "Conflict", "document content is stored outside the organization")
		return
	}

	url, err := h.blobs.PresignedGetURL(r.Context(), h.bucket, doc.StorageKey)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not create a download URL")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"download_url": url})
}

// Link handles POST /api/v1/documents/{id}/links
func (h *Handler) Link(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	docID := chi.URLParam(r, "id")
	// Verify document exists
	if _, err := h.repo.GetByID(r.Context(), t.OrganizationID, docID); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
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

	if err := h.repo.LinkDocument(r.Context(), link); err != nil {
		if err.Error() == "not found" {
			api.WriteError(w, http.StatusNotFound, "Not Found", "document or linked object not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, link)
}

// GetLinks handles GET /api/v1/documents/{id}/links
func (h *Handler) GetLinks(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	docID := chi.URLParam(r, "id")
	links, err := h.repo.GetLinks(r.Context(), t.OrganizationID, docID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, links)
}

// ownsStorageKey reports whether key is an object of document docID of
// organization orgID: org/<org>/documents/<doc>/… as written by UploadContent,
// or the legacy documents/<org>/<doc>/… of uploads before migration 000066.
func ownsStorageKey(orgID, docID, key string) bool {
	if blob.CheckOrgKey(orgID, key) == nil {
		return strings.HasPrefix(key, blob.OrgKey(orgID, "documents", docID)+"/")
	}
	legacy := "documents/" + orgID + "/" + docID + "/"
	return strings.HasPrefix(key, legacy) && !strings.Contains(key, "..")
}
