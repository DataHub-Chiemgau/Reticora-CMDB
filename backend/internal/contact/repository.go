package contact

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for contacts and CI-contact links.
type Repository interface {
	List(orgID, clientID string, page api.PaginationParams) ([]Contact, int, error)
	GetByID(orgID, id string) (*Contact, error)
	Create(c *Contact) error
	Update(orgID, id string, req UpdateContactRequest) (*Contact, error)
	Delete(orgID, id string) error

	ListForCI(orgID, ciID string, page api.PaginationParams) ([]CIContact, int, error)
	Link(link *CIContact) error
	Unlink(orgID, id string) error
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	contacts map[string]*Contact
	links    map[string]*CIContact
	seq      int
}

// NewMemoryRepository creates a new in-memory contact repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		contacts: make(map[string]*Contact),
		links:    make(map[string]*CIContact),
	}
}

func (r *MemoryRepository) List(orgID, clientID string, page api.PaginationParams) ([]Contact, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Contact
	for _, c := range r.contacts {
		if c.OrganizationID != orgID {
			continue
		}
		if clientID != "" && c.ClientID != clientID {
			continue
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

func (r *MemoryRepository) GetByID(orgID, id string) (*Contact, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.contacts[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Create(c *Contact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	c.ID = fmt.Sprintf("contact-%d", r.seq)
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	stored := *c
	r.contacts[c.ID] = &stored
	return nil
}

func (r *MemoryRepository) Update(orgID, id string, req UpdateContactRequest) (*Contact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.contacts[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.ClientID != nil {
		c.ClientID = *req.ClientID
	}
	if req.DisplayName != nil {
		c.DisplayName = *req.DisplayName
	}
	if req.Email != nil {
		c.Email = *req.Email
	}
	if req.Phone != nil {
		c.Phone = *req.Phone
	}
	if req.Role != nil {
		c.Role = *req.Role
	}
	if req.Department != nil {
		c.Department = *req.Department
	}
	if req.Notes != nil {
		c.Notes = *req.Notes
	}
	c.UpdatedAt = time.Now().UTC()
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.contacts[id]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.contacts, id)
	for lid, l := range r.links {
		if l.ContactID == id {
			delete(r.links, lid)
		}
	}
	return nil
}

func (r *MemoryRepository) ListForCI(orgID, ciID string, page api.PaginationParams) ([]CIContact, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CIContact
	for _, l := range r.links {
		if l.OrganizationID != orgID || l.CIID != ciID {
			continue
		}
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

func (r *MemoryRepository) Link(link *CIContact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.contacts[link.ContactID]
	if !ok || c.OrganizationID != link.OrganizationID {
		return fmt.Errorf("contact not found")
	}
	for _, l := range r.links {
		if l.OrganizationID == link.OrganizationID && l.CIID == link.CIID &&
			l.ContactID == link.ContactID && l.RelationshipType == link.RelationshipType {
			return fmt.Errorf("duplicate link")
		}
	}
	r.seq++
	link.ID = fmt.Sprintf("ci-contact-%d", r.seq)
	link.CreatedAt = time.Now().UTC()
	stored := *link
	r.links[link.ID] = &stored
	return nil
}

func (r *MemoryRepository) Unlink(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.links[id]
	if !ok || l.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.links, id)
	return nil
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
