package document

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// Repository defines persistence operations for documents.
type Repository interface {
	// List returns a page of the visible documents. With filter.EntityType
	// and filter.EntityID it returns only the documents linked to that
	// target; an entity type outside the registry fails with a 422 field
	// error, an invisible or missing target with api.ErrNotFound (WP-811).
	List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Document, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Document, error)
	// GetWritable returns the document only when the caller may change it
	// (WP-341: all links visible, or an org-wide principal); otherwise it
	// fails like GetByID for a missing document.
	GetWritable(ctx context.Context, orgID, id string) (*Document, error)
	Create(ctx context.Context, d *Document) error
	// CreateWithLinks creates the document and its links in one transaction
	// (E-61 option 1); the links get the document's id. A link fails like
	// LinkDocument, and then no document is created. With links the
	// transaction also writes document.created and one document.linked per
	// link (WP-851).
	CreateWithLinks(ctx context.Context, d *Document, links []DocumentLink) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Document, error)
	// SetStorage stores the blob location, size and MIME type after an
	// upload and returns the updated document.
	SetStorage(ctx context.Context, orgID, id, storageKey, mimeType string, size int64) (*Document, error)
	// CreateVersion stores v as the next version of its writable document
	// (WP-731) and returns the document, which mirrors it. In one
	// transaction it adds v.Size to the usage of the organization unless
	// that exceeds limit (bytes, nil unlimited; then
	// *entitlement.LimitExceededError), numbers v, inserts it and records
	// document.version_created. A document the caller may not write fails
	// with api.ErrNotFound. v.ID and v.StorageKey are set by the caller.
	CreateVersion(ctx context.Context, v *Version, limit *int64) (*Document, error)
	// ListVersions returns a page of the versions of a visible document,
	// highest first (keyset cursor sort "version", direction "desc"), and
	// their number; an invisible or missing document fails with
	// api.ErrNotFound.
	ListVersions(ctx context.Context, orgID, docID string, page api.PaginationParams) ([]Version, int, error)
	// GetVersion returns version n of a visible document, else
	// api.ErrNotFound.
	GetVersion(ctx context.Context, orgID, docID string, n int) (*Version, error)
	// StorageUsage returns the stored bytes of the whole organization,
	// whatever the scope of the caller.
	StorageUsage(ctx context.Context, orgID string) (int64, error)
	// SetScanResult stores the scan result of version n of the document
	// (WP-733, SEC-09): status (one of the four scan states, else
	// errInvalidScanStatus), scanned_at (now; NULL again for pending_scan)
	// and the signature name of a finding (empty: none). It joins a
	// transaction of the context (database.ContextWithTx), so the scan job
	// (WP-734) writes status, audit and event together. A missing version
	// fails with api.ErrNotFound.
	SetScanResult(ctx context.Context, orgID, documentID string, version int, status, signature string) error
	// PendingScans returns up to limit versions of the organization whose
	// scan is open (pending_scan or scan_failed), oldest first, through the
	// partial index idx_document_version_scan_open; versions of
	// soft-deleted documents are left out.
	PendingScans(ctx context.Context, orgID string, limit int) ([]Version, error)
	Delete(ctx context.Context, orgID, id string) error
	// LinkDocument creates a link. An entity type outside the registry
	// (links.go) fails with a 422 field error, an invisible or missing
	// target with api.ErrNotFound, a document whose current version is
	// infected with ErrQuarantined (WP-733).
	LinkDocument(ctx context.Context, link *DocumentLink) error
	// UnlinkDocument removes a link of the document that the caller sees
	// and may delete; otherwise it fails with api.ErrNotFound (WP-267).
	UnlinkDocument(ctx context.Context, orgID, docID, linkID string) error
	GetLinks(ctx context.Context, orgID, docID string) ([]DocumentLink, error)
	// GetLinksForEntity returns a page of the documents linked to an
	// entity whose link the caller sees, newest first on (created_at, id)
	// with at most one more than the limit (api.NewestFirstSQL), and their
	// number; a cursor of another order is api.ErrInvalidCursor (WP-765).
	GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Document, int, error)
}

// ErrQuarantined is the refusal of a new link to a document whose current
// version is infected (WP-733, E-42: 409 document-quarantined).
var ErrQuarantined = errors.New("document is quarantined")

// errInvalidScanStatus is the refusal of a scan status outside the four
// states of document_version.scan_status.
var errInvalidScanStatus = errors.New("invalid document scan status")

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	docs     map[string]*Document
	links    map[string]*DocumentLink
	versions map[string][]Version // per document, ascending
	usage    map[string]int64     // stored bytes per organization
	// deleted holds the soft-deleted documents (WP-732): they stay with
	// versions, links and usage, but every method treats them as missing.
	deleted  map[string]time.Time
	nextID   int
	nextLink int
}

// NewMemoryRepository creates a new in-memory document repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		docs:     make(map[string]*Document),
		links:    make(map[string]*DocumentLink),
		versions: make(map[string][]Version),
		usage:    make(map[string]int64),
		deleted:  make(map[string]time.Time),
	}
}

// live returns the document id of organization orgID unless it is missing
// or soft-deleted; the caller holds the lock.
func (r *MemoryRepository) live(orgID, id string) (*Document, bool) {
	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
		return nil, false
	}
	if _, gone := r.deleted[id]; gone {
		return nil, false
	}
	return d, true
}

// view returns a copy of d with the scan status of its current version;
// the caller holds the lock.
func (r *MemoryRepository) view(d *Document) *Document {
	cp := *d
	cp.ScanStatus, cp.ScannedAt = "", nil
	versions := r.versions[d.ID]
	for i := range versions {
		if versions[i].Version == d.Version {
			cp.ScanStatus, cp.ScannedAt = versions[i].ScanStatus, versions[i].ScannedAt
		}
	}
	return &cp
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Document, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var linked map[string]bool
	if filter.EntityType != "" || filter.EntityID != "" {
		if err := checkEntityFilter(filter.EntityType, filter.EntityID); err != nil {
			return nil, 0, err
		}
		linked = make(map[string]bool)
		for _, l := range r.links {
			if l.OrganizationID == orgID && l.EntityType == filter.EntityType && l.EntityID == filter.EntityID {
				linked[l.DocumentID] = true
			}
		}
	}

	var result []Document
	for _, d := range r.docs {
		if _, ok := r.live(orgID, d.ID); !ok {
			continue
		}
		if linked != nil && !linked[d.ID] {
			continue
		}
		if filter.Category != "" && d.Category != filter.Category {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(d.Title), strings.ToLower(filter.Search)) &&
			!strings.Contains(strings.ToLower(d.FileName), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *r.view(d))
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(&result[i], column), sortValue(&result[j], column)
		if vi == vj {
			if direction == "asc" {
				return result[i].ID < result[j].ID
			}
			return result[i].ID > result[j].ID
		}
		if direction == "asc" {
			return vi < vj
		}
		return vi > vj
	})

	if page.Cursor != nil {
		if !page.Cursor.Matches(column, direction) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		remaining := make([]Document, 0, len(result))
		for i := range result {
			item := &result[i]
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, *item)
			}
		}
		if len(remaining) > page.Limit {
			remaining = remaining[:page.Limit]
		}
		return remaining, total, nil
	}

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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.live(orgID, id)
	if !ok {
		return nil, fmt.Errorf("document not found")
	}
	return r.view(d), nil
}

func (r *MemoryRepository) Create(_ context.Context, d *Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	d.ID = fmt.Sprintf("doc-%d", r.nextID)
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	if d.Version == 0 {
		d.Version = 1
	}
	r.docs[d.ID] = d
	return nil
}

// CreateWithLinks checks every link like the PostgreSQL repository before
// anything is stored (entity type in the registry: 422, entity id a UUID:
// 400), so a failing link leaves no document; then it creates the document
// and its links. The memory profile has no target tables to check further
// and keeps no audit trail.
func (r *MemoryRepository) CreateWithLinks(ctx context.Context, d *Document, links []DocumentLink) error {
	for i := range links {
		if err := checkEntityType(links[i].EntityType); err != nil {
			return err
		}
		if !api.IsUUID(links[i].EntityID) {
			return errInvalidEntityID
		}
	}
	if err := r.Create(ctx, d); err != nil {
		return err
	}
	for i := range links {
		links[i].OrganizationID, links[i].DocumentID = d.OrganizationID, d.ID
		if err := r.LinkDocument(ctx, &links[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.live(orgID, id)
	if !ok {
		return nil, fmt.Errorf("document not found")
	}

	if req.Title != nil {
		d.Title = *req.Title
	}
	if req.Description != nil {
		d.Description = *req.Description
	}
	if req.Category != nil {
		d.Category = *req.Category
	}
	if req.Tags != nil {
		d.Tags = req.Tags
	}
	d.UpdatedAt = time.Now().UTC()
	return r.view(d), nil
}

// GetWritable equals GetByID: the memory repository has no tenant scope.
func (r *MemoryRepository) GetWritable(ctx context.Context, orgID, id string) (*Document, error) {
	return r.GetByID(ctx, orgID, id)
}

func (r *MemoryRepository) SetStorage(_ context.Context, orgID, id, storageKey, mimeType string, size int64) (*Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.live(orgID, id)
	if !ok {
		return nil, fmt.Errorf("document not found")
	}
	d.StorageKey = storageKey
	d.MimeType = mimeType
	d.FileSize = size
	d.UpdatedAt = time.Now().UTC()
	return r.view(d), nil
}

// CreateVersion numbers v after the highest version of the document, adds
// its size to the usage within limit and mirrors it in the document, like
// PGRepository without the audit trail.
func (r *MemoryRepository) CreateVersion(_ context.Context, v *Version, limit *int64) (*Document, error) {
	if err := blob.CheckOrgKey(v.OrganizationID, v.StorageKey); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.live(v.OrganizationID, v.DocumentID)
	if !ok {
		return nil, api.ErrNotFound
	}
	used := r.usage[v.OrganizationID]
	if limit != nil && used+v.Size > *limit {
		return nil, storageLimitError(*limit, used)
	}
	r.usage[v.OrganizationID] = used + v.Size

	versions := r.versions[d.ID]
	v.Version = 1
	if n := len(versions); n > 0 {
		v.Version = versions[n-1].Version + 1
	}
	if v.ScanStatus == "" {
		v.ScanStatus = ScanPending
	}
	v.CreatedAt = time.Now().UTC()
	r.versions[d.ID] = append(versions, *v)

	d.Version, d.StorageKey, d.FileSize, d.MimeType = v.Version, v.StorageKey, v.Size, v.MimeType
	d.UpdatedAt = v.CreatedAt
	return r.view(d), nil
}

// ListVersions returns the versions of the document, highest first, after
// the keyset cursor or from the offset.
func (r *MemoryRepository) ListVersions(_ context.Context, orgID, docID string, page api.PaginationParams) ([]Version, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.live(orgID, docID); !ok {
		return nil, 0, api.ErrNotFound
	}
	all := r.versions[docID]
	out := make([]Version, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		out = append(out, all[i])
	}
	total := len(out)
	if page.Cursor != nil {
		after, err := versionCursorAfter(page.Cursor)
		if err != nil {
			return nil, 0, err
		}
		i := 0
		for i < len(out) && out[i].Version >= after {
			i++
		}
		out = out[i:]
	} else {
		out = out[min(page.Offset, len(out)):]
	}
	return out[:min(page.Limit, len(out))], total, nil
}

// GetVersion returns version n of the document.
func (r *MemoryRepository) GetVersion(_ context.Context, orgID, docID string, n int) (*Version, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.live(orgID, docID); !ok {
		return nil, api.ErrNotFound
	}
	versions := r.versions[docID]
	for i := range versions {
		if versions[i].Version == n {
			cp := versions[i]
			return &cp, nil
		}
	}
	return nil, api.ErrNotFound
}

// StorageUsage returns the stored bytes of the organization.
func (r *MemoryRepository) StorageUsage(_ context.Context, orgID string) (int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.usage[orgID], nil
}

// SetScanResult stores the scan result of version n like PGRepository.
func (r *MemoryRepository) SetScanResult(_ context.Context, orgID, documentID string, n int, status, _ string) error {
	if !validScanStatus(status) {
		return errInvalidScanStatus
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.docs[documentID]
	if !ok || d.OrganizationID != orgID {
		return api.ErrNotFound
	}
	versions := r.versions[documentID]
	for i := range versions {
		if versions[i].Version != n {
			continue
		}
		versions[i].ScanStatus, versions[i].ScannedAt = status, nil
		if status != ScanPending {
			now := time.Now().UTC()
			versions[i].ScannedAt = &now
		}
		return nil
	}
	return api.ErrNotFound
}

// PendingScans returns the open versions of the organization, oldest
// first, without those of soft-deleted documents.
func (r *MemoryRepository) PendingScans(_ context.Context, orgID string, limit int) ([]Version, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Version
	for docID, versions := range r.versions {
		if _, ok := r.live(orgID, docID); !ok {
			continue
		}
		for i := range versions {
			if versions[i].ScanStatus == ScanPending || versions[i].ScanStatus == ScanFailed {
				out = append(out, versions[i])
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out[:min(max(limit, 0), len(out))], nil
}

// Delete soft-deletes the document like PGRepository: versions, links and
// usage stay; a second call fails with "not found".
func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.live(orgID, id); !ok {
		return fmt.Errorf("document not found")
	}
	r.deleted[id] = time.Now().UTC()
	return nil
}

// LinkDocument checks the entity type against the registry like
// PGRepository and refuses a soft-deleted document (api.ErrNotFound); the
// memory profile has no target tables to check further.
func (r *MemoryRepository) LinkDocument(_ context.Context, link *DocumentLink) error {
	if err := checkEntityType(link.EntityType); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, gone := r.deleted[link.DocumentID]; gone {
		return api.ErrNotFound
	}
	if d, ok := r.docs[link.DocumentID]; ok && r.view(d).ScanStatus == ScanInfected {
		return ErrQuarantined
	}

	r.nextLink++
	link.ID = fmt.Sprintf("link-%d", r.nextLink)
	link.CreatedAt = time.Now().UTC()
	r.links[link.ID] = link
	return nil
}

// UnlinkDocument removes link linkID of document docID. A link of another
// document or organization, of a soft-deleted document, or whose entity
// type is not in the registry, is not found.
func (r *MemoryRepository) UnlinkDocument(_ context.Context, orgID, docID, linkID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	link, ok := r.links[linkID]
	if !ok || link.OrganizationID != orgID || link.DocumentID != docID || checkEntityType(link.EntityType) != nil {
		return api.ErrNotFound
	}
	if _, gone := r.deleted[docID]; gone {
		return api.ErrNotFound
	}
	delete(r.links, linkID)
	return nil
}

// GetLinks returns the links of the document; a soft-deleted document
// fails with api.ErrNotFound like in PGRepository.
func (r *MemoryRepository) GetLinks(_ context.Context, orgID, docID string) ([]DocumentLink, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, gone := r.deleted[docID]; gone {
		return nil, api.ErrNotFound
	}

	var result []DocumentLink
	for _, l := range r.links {
		if l.OrganizationID == orgID && l.DocumentID == docID {
			result = append(result, *l)
		}
	}
	// Newest first, then id, like PGRepository: the offset pages of
	// GET /documents/{id}/links need a stable order (API-02).
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].ID > result[j].ID
	})
	return result, nil
}

// GetLinksForEntity pages the linked documents like PGRepository
// (api.NewestFirstSlice); a document linked twice counts once.
func (r *MemoryRepository) GetLinksForEntity(_ context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Document, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Document
	seen := map[string]bool{}
	for _, l := range r.links {
		if l.OrganizationID == orgID && l.EntityType == entityType && l.EntityID == entityID && !seen[l.DocumentID] {
			if d, ok := r.live(orgID, l.DocumentID); ok {
				seen[l.DocumentID] = true
				result = append(result, *r.view(d))
			}
		}
	}
	return api.NewestFirstSlice(result, page, documentPosition)
}

// documentPosition is the (created_at, id) position of a document in a
// newest-first list.
func documentPosition(d *Document) (createdAt time.Time, id string) { return d.CreatedAt, d.ID }
