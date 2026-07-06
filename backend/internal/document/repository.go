package document

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for documents.
type Repository interface {
	List(orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error)
	GetByID(orgID, id string) (*Document, error)
	Create(d *Document) error
	Update(orgID, id string, req UpdateRequest) (*Document, error)
	Delete(orgID, id string) error
	LinkDocument(link *DocumentLink) error
	GetLinks(orgID, docID string) ([]DocumentLink, error)
	GetLinksForEntity(orgID, entityType, entityID string) ([]Document, error)
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

func (r *MemoryRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error) {
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.docs[id]
	if !ok || d.OrganizationID != orgID {
		return nil, fmt.Errorf("document not found")
	}
	return d, nil
}

func (r *MemoryRepository) Create(d *Document) error {
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

func (r *MemoryRepository) Update(orgID, id string, req UpdateRequest) (*Document, error) {
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

func (r *MemoryRepository) Delete(orgID, id string) error {
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

func (r *MemoryRepository) LinkDocument(link *DocumentLink) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextLink++
	link.ID = fmt.Sprintf("link-%d", r.nextLink)
	link.CreatedAt = time.Now().UTC()
	r.links[link.ID] = link
	return nil
}

func (r *MemoryRepository) GetLinks(orgID, docID string) ([]DocumentLink, error) {
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

func (r *MemoryRepository) GetLinksForEntity(orgID, entityType, entityID string) ([]Document, error) {
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
