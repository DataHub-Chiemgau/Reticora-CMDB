package document

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// ActionVersionCreated is the audit action of a new document version
// (WP-731): written by the repository in the transaction of the version, so
// the route audit adds no second entry.
const ActionVersionCreated = "document.version_created"

// Audit actions of the soft delete (WP-732): document.deleted is written by
// the repository in the transaction of the soft delete (the route audit of
// DELETE /api/v1/documents/{id} remains the fallback), document.purged by
// the retention run retention.soft_deleted with source system when it
// removes the document for good.
const (
	ActionDeleted = "document.deleted"
	ActionPurged  = "document.purged"
)

// recordDeleteAudit writes document.deleted as the last step of the
// transaction of the soft delete. Record marks the request as audited.
func recordDeleteAudit(ctx context.Context, tx pgx.Tx, orgID, docID, title string, deletedAt time.Time) error {
	actorID, actorType := auditActor(ctx)
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID,
		ActorID:        actorID,
		ActorType:      actorType,
		Action:         ActionDeleted,
		ResourceType:   "document",
		ResourceID:     docID,
		Changes:        map[string]any{"title": title, "deleted_at": deletedAt.UTC()},
	}); err != nil {
		return fmt.Errorf("audit %s: %w", ActionDeleted, err)
	}
	return nil
}

// StorageLimiter resolves the document storage limit of an organization in
// bytes (entitlement.Service.StorageLimitBytes): nil is unlimited, an
// organization without the feature documents fails with
// *entitlement.FeatureNotEntitledError.
type StorageLimiter interface {
	StorageLimitBytes(ctx context.Context, orgID string) (*int64, error)
}

// WithLimits sets the storage limit of the uploads (MGT-01, E-44). Without
// it (profile without database, tests) the storage is unlimited.
func (h *Handler) WithLimits(limits StorageLimiter) *Handler {
	if svc, ok := limits.(*entitlement.Service); ok && svc == nil {
		limits = nil
	}
	h.limits = limits
	return h
}

// storageLimit returns the storage limit of the organization in bytes, nil
// when unlimited or without a limiter.
func (h *Handler) storageLimit(ctx context.Context, orgID string) (*int64, error) {
	if h.limits == nil {
		return nil, nil
	}
	return h.limits.StorageLimitBytes(ctx, orgID)
}

// storageLimitError is the refusal of an upload that would exceed limit
// with used bytes stored (ENT-03: 403 entitlement-limit). Limit and usage
// are bytes; the usage is no secret of the organization (GET
// /api/v1/documents/usage), so it is named to every caller.
func storageLimitError(limit, used int64) error {
	return &entitlement.LimitExceededError{FeatureKey: entitlement.LimitMaxStorageMB, Limit: limit, Current: used}
}

// versionCursorSort is the keyset order of GET …/versions.
const versionCursorSort = "version"

// versionCursorAfter returns the version number a keyset cursor of GET
// …/versions points after; a cursor of another list fails with
// api.ErrInvalidCursor.
func versionCursorAfter(c *api.Cursor) (int, error) {
	if !c.Matches(versionCursorSort, "desc") {
		return 0, fmt.Errorf("%w: not issued for this list", api.ErrInvalidCursor)
	}
	n, err := strconv.Atoi(c.Value)
	if err != nil {
		return 0, fmt.Errorf("%w: not issued for this list", api.ErrInvalidCursor)
	}
	return n, nil
}

// errVersionCursor answers a cursor not issued by GET …/versions.
var errVersionCursor = errors.New("invalid cursor: not issued for this list")

// newVersionID returns a random UUID (v4) for the key of a new version.
func newVersionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// recordVersionAudit writes document.version_created with version, size,
// MIME type and checksum as the last step of the transaction (WP-731).
// Record marks the request as audited.
func recordVersionAudit(ctx context.Context, tx pgx.Tx, v *Version) error {
	actorID, actorType := auditActor(ctx)
	checksum := ""
	if v.ChecksumSHA256 != nil {
		checksum = *v.ChecksumSHA256
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: v.OrganizationID,
		ActorID:        actorID,
		ActorType:      actorType,
		Action:         ActionVersionCreated,
		ResourceType:   "document",
		ResourceID:     v.DocumentID,
		Changes: map[string]any{
			"version_id":      v.ID,
			"version":         v.Version,
			"size":            v.Size,
			"mime_type":       v.MimeType,
			"checksum_sha256": checksum,
		},
	}); err != nil {
		return fmt.Errorf("audit %s: %w", ActionVersionCreated, err)
	}
	return nil
}

// CreateVersion handles POST /api/v1/documents/{id}/versions (E-38): the
// binary body becomes the next version; 201 with the version.
func (h *Handler) CreateVersion(w http.ResponseWriter, r *http.Request) {
	doc, v, policy, ok := h.storeVersion(w, r)
	if !ok {
		return
	}
	body, err := projectVersion(policy, doc, v)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, body)
}

// UploadContent handles PUT /api/v1/documents/{id}/content: like POST
// …/versions it stores the body as the next version (compatibility) and
// answers 200 with the document, which mirrors the new version.
func (h *Handler) UploadContent(w http.ResponseWriter, r *http.Request) {
	doc, _, policy, ok := h.storeVersion(w, r)
	if !ok {
		return
	}
	writeDocument(w, http.StatusOK, policy, doc)
}

// storeVersion stores the request body as the next version of the document
// without a lock during the blob transfer (K20): (1) read the body within
// the upload limits, verify its type against the whitelist and the content
// signature (SEC-09, sniff.go) and form size and sha256, (2) check usage
// and limit without binding (403 before any blob), (3) put the blob under
// org/<org>/documents/<id>/<version id>,
// a key of its own per upload, so parallel uploads never overwrite each
// other, (4) store the version in one transaction that counts the usage
// within the limit (Repository.CreateVersion). A refusal or rollback in (4)
// deletes the blob again. Only a writable document takes versions (WP-341);
// any other answers 404 before anything is stored. (5) After the commit the
// version's malware scan is submitted (WithScans, WP-734). Each upload takes one of
// the upload slots of the instance (503 with Retry-After without one) and
// runs under the upload deadline. An upload writes file_size and mime_type
// of the document: a principal that may not edit them is refused with 403
// and the fields before any blob (WP-267). It writes the error response
// itself and reports whether the version was stored; the policy is that of
// the request.
func (h *Handler) storeVersion(w http.ResponseWriter, r *http.Request) (*Document, *Version, *permission.FieldPolicy, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return nil, nil, nil, false
	}
	if h.blobs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "document content upload requires blob storage")
		return nil, nil, nil, false
	}

	release, ok := h.beginUpload(w)
	if !ok {
		return nil, nil, nil, false
	}
	defer release()
	policy, ok := policyOf(w, r)
	if !ok {
		return nil, nil, nil, false
	}

	id := chi.URLParam(r, "id")
	doc, err := h.repo.GetWritable(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return nil, nil, nil, false
	}
	if refuseSignatureChange(w, doc, nil) {
		return nil, nil, nil, false
	}

	// (1) Body, verified type (SEC-09, sniff.go), size and checksum.
	mimeType, payload, ok := h.readUpload(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	size := int64(len(payload))
	if checkErr := checkUpload(policy, doc, size, mimeType); checkErr != nil {
		api.WriteFieldError(w, checkErr)
		return nil, nil, nil, false
	}
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])

	// (2) Non-binding check against usage and limit: no blob for an upload
	// that cannot fit.
	limit, err := h.storageLimit(r.Context(), t.OrganizationID)
	if err != nil {
		if !api.WriteEntitlementError(w, err) {
			api.WriteRepoError(w, err)
		}
		return nil, nil, nil, false
	}
	if limit != nil {
		used, usageErr := h.repo.StorageUsage(r.Context(), t.OrganizationID)
		if usageErr != nil {
			api.WriteRepoError(w, usageErr)
			return nil, nil, nil, false
		}
		if used+size > *limit {
			api.WriteEntitlementError(w, storageLimitError(*limit, used))
			return nil, nil, nil, false
		}
	}

	// (3) Blob under a key of its own.
	v := &Version{
		ID:             newVersionID(),
		OrganizationID: t.OrganizationID,
		DocumentID:     doc.ID,
		Size:           size,
		MimeType:       mimeType,
		ChecksumSHA256: &checksum,
		ScanStatus:     ScanPending,
		UploadedBy:     t.UserID,
	}
	v.StorageKey = blob.OrgKey(t.OrganizationID, "documents", doc.ID, v.ID)
	if err = h.blobs.Put(r.Context(), h.bucket, v.StorageKey, bytes.NewReader(payload), size, mimeType); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not store the document content")
		return nil, nil, nil, false
	}

	// (4) The binding transaction; its refusal removes the blob again.
	updated, err := h.repo.CreateVersion(r.Context(), v, limit)
	if err != nil {
		_ = h.blobs.Delete(context.WithoutCancel(r.Context()), h.bucket, v.StorageKey)
		switch {
		case api.WriteEntitlementError(w, err):
		case errors.Is(err, api.ErrNotFound):
			api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		default:
			api.WriteRepoError(w, err)
		}
		return nil, nil, nil, false
	}
	// (5) The malware scan of the committed version (SEC-09, E-42).
	h.enqueueScan(r.Context(), v)
	return updated, v, policy, true
}

// ListVersions handles GET /api/v1/documents/{id}/versions: the versions of
// a visible document with size, MIME type, checksum, scan status and
// scanned_at (WP-733), highest first, paged with limit and the signed
// cursor (API-02).
func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
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
	policy, ok := policyOf(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")
	items, total, err := h.repo.ListVersions(r.Context(), t.OrganizationID, docID, page)
	if err != nil {
		switch {
		case errors.Is(err, api.ErrInvalidCursor):
			api.WriteError(w, http.StatusBadRequest, "Bad Request", errVersionCursor.Error())
		case errors.Is(err, api.ErrNotFound):
			api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		default:
			api.WriteRepoError(w, err)
		}
		return
	}
	if items == nil {
		items = []Version{}
	}
	next := ""
	if len(items) == page.Limit && len(items) > 0 {
		next = api.EncodeCursor(api.Cursor{Sort: versionCursorSort, Dir: "desc",
			Value: strconv.Itoa(items[len(items)-1].Version), ID: items[len(items)-1].ID})
	}
	list := api.ListResponse[Version]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    next != "",
		NextCursor: next,
	}
	if !policy.Restricted(permission.KindDocument) {
		api.WriteJSON(w, http.StatusOK, list)
		return
	}
	// Size, MIME type and uploader of a version are those of the document
	// (file_size, mime_type, uploaded_by) under its field permissions.
	doc := &Document{ID: docID}
	data := make([]any, len(items))
	for i := range items {
		if data[i], err = projectVersion(policy, doc, &items[i]); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[any]{Data: data, Total: list.Total, Limit: list.Limit,
		Offset: list.Offset, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

// Usage handles GET /api/v1/documents/usage: the stored bytes of the whole
// organization from organization_usage and the limit of max_storage_mb in
// MiB (null when unlimited).
func (h *Handler) Usage(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	limit, err := h.storageLimit(r.Context(), t.OrganizationID)
	if err != nil {
		if !api.WriteEntitlementError(w, err) {
			api.WriteRepoError(w, err)
		}
		return
	}
	used, err := h.repo.StorageUsage(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	out := Usage{StorageBytes: used}
	if limit != nil {
		mb := *limit >> 20
		out.LimitMB = &mb
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// CategorySignature is the category of a signature image (AST-02, WP-276):
// only the signature path of the assignment service sets it.
const CategorySignature = "signature"

// ErrMimeMismatch is the refusal of content that does not match its
// declared type (VerifyContent; 422 mime-mismatch).
var ErrMimeMismatch = errors.New("content does not match its type")

// VerifyContent checks content held in memory against the declared,
// whitelisted type like an upload (SEC-09, WP-268, sniff.go): executables,
// scripts and HTML are refused, the signature of the type must match
// (image/png: \x89PNG\r\n\x1a\n, http.DetectContentType image/png). A
// mismatch wraps ErrMimeMismatch, a type off the whitelist is
// errUnsupportedType.
func VerifyContent(declared string, body []byte) error {
	if _, err := declaredType(declared); err != nil {
		return err
	}
	head := body
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	err := checkHead(declared, head)
	if err == nil {
		err = checkBody(declared, body)
	}
	if err != nil {
		return fmt.Errorf("%w: %s", ErrMimeMismatch, err.Error())
	}
	return nil
}

// AddVersionTx stores v as the next version of its document in tx, the
// transaction of the caller (WP-731): it adds v.Size to the usage of the
// organization only within limit (nil: unlimited; else
// *entitlement.LimitExceededError), numbers v after the highest version and
// inserts it pending its scan. The upload route (CreateVersion) and the
// signature path (CreateWithFirstVersionTx) share it; the caller has locked
// or just inserted the document and stored the blob under v.StorageKey.
func AddVersionTx(ctx context.Context, tx pgx.Tx, v *Version, limit *int64) error {
	if err := addStorageUsage(ctx, tx, v.OrganizationID, v.Size, limit); err != nil {
		return err
	}
	if v.ScanStatus == "" {
		v.ScanStatus = ScanPending
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO document_version (id, organization_id, document_id, version, storage_key, size, mime,
		                              checksum_sha256, scan_status, uploaded_by_surrogate)
		SELECT $1, $2, $3, COALESCE(max(version), 0) + 1, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid
		  FROM document_version WHERE document_id = $3
		RETURNING version, created_at`,
		v.ID, v.OrganizationID, v.DocumentID, v.StorageKey, v.Size, v.MimeType, v.ChecksumSHA256, v.ScanStatus,
		uuidOrEmpty(v.UploadedBy)).Scan(&v.Version, &v.CreatedAt); err != nil {
		return fmt.Errorf("insert document version: %w", err)
	}
	v.CreatedAt = v.CreatedAt.UTC()
	return nil
}

// CreateWithFirstVersionTx inserts the document d with the id the caller
// chose and its content v as version 1 in tx, the transaction of the
// caller (WP-276): the blob is stored under v.StorageKey
// (blob.OrgKey(org, "documents", d.ID, v.ID)) before the transaction, the
// usage counts against limit like an upload (AddVersionTx). d mirrors the
// version (storage key, size, MIME type). The caller links the document in
// the same transaction (LinkTx); a scoped principal's document without a
// link fails at the commit (document_scoped_insert_linked).
func CreateWithFirstVersionTx(ctx context.Context, tx pgx.Tx, d *Document, v *Version, limit *int64) error {
	if !api.IsUUID(d.ID) || !api.IsUUID(v.ID) {
		return errors.New("document and version need an id")
	}
	v.OrganizationID, v.DocumentID = d.OrganizationID, d.ID
	if err := blob.CheckOrgKey(v.OrganizationID, v.StorageKey); err != nil {
		return err
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.StorageKey, d.FileSize, d.MimeType, d.Version = v.StorageKey, v.Size, v.MimeType, 1
	if err := tx.QueryRow(ctx, `
		INSERT INTO document (id, organization_id, title, description, file_name, file_size, mime_type,
		                      storage_key, version, category, tags, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $10, $11)
		RETURNING created_at, updated_at`,
		d.ID, d.OrganizationID, d.Title, nilIfEmpty(d.Description), d.FileName, d.FileSize, d.MimeType,
		d.StorageKey, d.Category, d.Tags, d.UploadedBy).Scan(&d.CreatedAt, &d.UpdatedAt); err != nil {
		return fmt.Errorf("create document: %w", err)
	}
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	if err := AddVersionTx(ctx, tx, v, limit); err != nil {
		return err
	}
	if v.Version != 1 {
		return fmt.Errorf("create document: first version is %d", v.Version)
	}
	d.ScanStatus = v.ScanStatus
	return nil
}

// EnqueueScan submits the malware scan of version v to submitter once the
// transaction of ctx has committed (WP-734, database.AfterCommit); nil
// submits nothing and the version stays pending_scan for the sweep.
func EnqueueScan(ctx context.Context, submitter ScanSubmitter, v *Version) {
	(&Handler{}).WithScans(submitter).enqueueScan(ctx, v)
}

// requestedVersion parses the query parameter version of GET …/content:
// 0 without it, else a positive version number; ok is false for any other
// value.
func requestedVersion(r *http.Request) (n int, ok bool) {
	raw := r.URL.Query().Get("version")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
