package document

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for documents.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Document, error)
	Create(ctx context.Context, d *Document) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Document, error)
	// SetStorage stores the blob location, size and MIME type after an
	// upload and returns the updated document.
	SetStorage(ctx context.Context, orgID, id, storageKey, mimeType string, size int64) (*Document, error)
	Delete(ctx context.Context, orgID, id string) error
	LinkDocument(ctx context.Context, link *DocumentLink) error
	GetLinks(ctx context.Context, orgID, docID string) ([]DocumentLink, error)
	GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string) ([]Document, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	docs     map[string]*Document
	links    map[string]*DocumentLink
	nextID   int
	nextLink int
}

// NewMemoryRepository creates a new in-memory document repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		docs:  make(map[string]*Document),
		links: make(map[string]*DocumentLink),
	}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Document
	for _, d := range r.docs {
		if d.OrganizationID != orgID {
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

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
		return fmt.Errorf("document not found")
	}
	delete(r.docs, id)
	// Remove associated links
	for lid, link := range r.links {
		if link.DocumentID == id {
			delete(r.links, lid)
		}
	}
	return nil
}

func (r *MemoryRepository) LinkDocument(_ context.Context, link *DocumentLink) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextLink++
	link.ID = fmt.Sprintf("link-%d", r.nextLink)
	link.CreatedAt = time.Now().UTC()
	r.links[link.ID] = link
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
