package document

import (
	"context"
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
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error)
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
	Delete(ctx context.Context, orgID, id string) error
	// LinkDocument creates a link. An entity type outside the registry
	// (links.go) fails with a 422 field error, an invisible or missing
	// target with api.ErrNotFound.
	LinkDocument(ctx context.Context, link *DocumentLink) error
	// UnlinkDocument removes a link of the document that the caller sees
	// and may delete; otherwise it fails with api.ErrNotFound (WP-267).
	UnlinkDocument(ctx context.Context, orgID, docID, linkID string) error
	GetLinks(ctx context.Context, orgID, docID string) ([]DocumentLink, error)
	GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string) ([]Document, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	docs     map[string]*Document
	links    map[string]*DocumentLink
	versions map[string][]Version // per document, ascending
	usage    map[string]int64     // stored bytes per organization
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
	}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error) {
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
		if d.OrganizationID != orgID {
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
		result = append(result, *d)
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(result[i], column), sortValue(result[j], column)
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
		for _, item := range result {
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, item)
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

	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
		return nil, fmt.Errorf("document not found")
	}
	return d, nil
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

	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
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
	return d, nil
}

// GetWritable equals GetByID: the memory repository has no tenant scope.
func (r *MemoryRepository) GetWritable(ctx context.Context, orgID, id string) (*Document, error) {
	return r.GetByID(ctx, orgID, id)
}

func (r *MemoryRepository) SetStorage(_ context.Context, orgID, id, storageKey, mimeType string, size int64) (*Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
		return nil, fmt.Errorf("document not found")
	}
	d.StorageKey = storageKey
	d.MimeType = mimeType
	d.FileSize = size
	d.UpdatedAt = time.Now().UTC()
	cp := *d
	return &cp, nil
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

	d, ok := r.docs[v.DocumentID]
	if !ok || d.OrganizationID != v.OrganizationID {
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
	cp := *d
	return &cp, nil
}

// ListVersions returns the versions of the document, highest first, after
// the keyset cursor or from the offset.
func (r *MemoryRepository) ListVersions(_ context.Context, orgID, docID string, page api.PaginationParams) ([]Version, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if d, ok := r.docs[docID]; !ok || d.OrganizationID != orgID {
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

	if d, ok := r.docs[docID]; !ok || d.OrganizationID != orgID {
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

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
		return fmt.Errorf("document not found")
	}
	delete(r.docs, id)
	// The versions go with the document and stop counting (PGRepository).
	for i := range r.versions[id] {
		r.usage[orgID] = max(r.usage[orgID]-r.versions[id][i].Size, 0)
	}
	delete(r.versions, id)
	// Remove associated links
	for lid, link := range r.links {
		if link.DocumentID == id {
			delete(r.links, lid)
		}
	}
	return nil
}

// LinkDocument checks the entity type against the registry like
// PGRepository; the memory profile has no target tables to check further.
func (r *MemoryRepository) LinkDocument(_ context.Context, link *DocumentLink) error {
	if err := checkEntityType(link.EntityType); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextLink++
	link.ID = fmt.Sprintf("link-%d", r.nextLink)
	link.CreatedAt = time.Now().UTC()
	r.links[link.ID] = link
	return nil
}

// UnlinkDocument removes link linkID of document docID. A link of another
// document or organization, or whose entity type is not in the registry,
// is not found.
func (r *MemoryRepository) UnlinkDocument(_ context.Context, orgID, docID, linkID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	link, ok := r.links[linkID]
	if !ok || link.OrganizationID != orgID || link.DocumentID != docID || checkEntityType(link.EntityType) != nil {
		return api.ErrNotFound
	}
	delete(r.links, linkID)
	return nil
}

func (r *MemoryRepository) GetLinks(_ context.Context, orgID, docID string) ([]DocumentLink, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

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

func (r *MemoryRepository) GetLinksForEntity(_ context.Context, orgID, entityType, entityID string) ([]Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Document
	for _, l := range r.links {
		if l.OrganizationID == orgID && l.EntityType == entityType && l.EntityID == entityID {
			if d, ok := r.docs[l.DocumentID]; ok {
				result = append(result, *d)
			}
		}
	}
	return result, nil
}
