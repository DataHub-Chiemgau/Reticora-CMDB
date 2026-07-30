package ticket

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for tickets.
type Repository interface {
	List(orgID string, filter FilterParams, page api.PaginationParams) ([]Ticket, int, error)
	GetByID(orgID, id string) (*Ticket, error)
	Create(t *Ticket) error
	Update(orgID, id string, req UpdateRequest) (*Ticket, error)
	Delete(orgID, id string) error
	AddComment(c *Comment) error
	ListComments(orgID, ticketID string, page api.PaginationParams) ([]Comment, int, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	tickets    map[string]*Ticket
	comments   map[string]*Comment
	nextID     int
	nextCmtID  int
	nextNumber int
}

// NewMemoryRepository creates a new in-memory ticket repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		tickets:  make(map[string]*Ticket),
		comments: make(map[string]*Comment),
	}
}

func (r *MemoryRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Ticket, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Ticket
	for _, t := range r.tickets {
		if t.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && t.Status != filter.Status {
			continue
		}
		if filter.Priority != "" && t.Priority != filter.Priority {
			continue
		}
		if filter.Category != "" && t.Category != filter.Category {
			continue
		}
		if filter.AssigneeID != "" && t.AssigneeID != filter.AssigneeID {
			continue
		}
		if filter.TeamID != "" && t.TeamID != filter.TeamID {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(t.Title), strings.ToLower(filter.Search)) &&
			!strings.Contains(strings.ToLower(t.Description), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *t)
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
		remaining := make([]Ticket, 0, len(result))
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Ticket, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return nil, fmt.Errorf("ticket not found")
	}
	return t, nil
}

func (r *MemoryRepository) Create(t *Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	r.nextNumber++
	t.ID = fmt.Sprintf("ticket-%d", r.nextID)
	t.TicketNumber = r.nextNumber
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	r.tickets[t.ID] = t
	return nil
}

func (r *MemoryRepository) Update(orgID, id string, req UpdateRequest) (*Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return nil, fmt.Errorf("ticket not found")
	}

	if req.Title != nil {
		t.Title = *req.Title
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	if req.Status != nil {
		t.Status = *req.Status
		if *req.Status == "resolved" && t.ResolvedAt == "" {
			t.ResolvedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if *req.Status == "closed" && t.ClosedAt == "" {
			t.ClosedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	if req.Priority != nil {
		t.Priority = *req.Priority
	}
	if req.Category != nil {
		t.Category = *req.Category
	}
	if req.AssigneeID != nil {
		t.AssigneeID = *req.AssigneeID
	}
	if req.TeamID != nil {
		t.TeamID = *req.TeamID
	}
	if req.RelatedCIID != nil {
		t.RelatedCIID = *req.RelatedCIID
	}
	if req.RelatedAssetID != nil {
		t.RelatedAssetID = *req.RelatedAssetID
	}
	if req.DueDate != nil {
		t.DueDate = *req.DueDate
	}
	if req.Tags != nil {
		t.Tags = req.Tags
	}
	t.UpdatedAt = time.Now().UTC()
	return t, nil
}

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return fmt.Errorf("ticket not found")
	}
	delete(r.tickets, id)
	// Remove associated comments
	for cid, c := range r.comments {
		if c.TicketID == id {
			delete(r.comments, cid)
		}
	}
	return nil
}

func (r *MemoryRepository) AddComment(c *Comment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Verify ticket exists
	t, ok := r.tickets[c.TicketID]
	if !ok || t.OrganizationID != c.OrganizationID {
		return fmt.Errorf("ticket not found")
	}

	r.nextCmtID++
	c.ID = fmt.Sprintf("comment-%d", r.nextCmtID)
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	r.comments[c.ID] = c
	return nil
}

func (r *MemoryRepository) ListComments(orgID, ticketID string, page api.PaginationParams) ([]Comment, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Comment
	for _, c := range r.comments {
		if c.OrganizationID == orgID && c.TicketID == ticketID {
			result = append(result, *c)
		}
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
