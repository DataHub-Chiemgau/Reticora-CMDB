package contact

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for contacts and CI-contact links.
type Repository interface {
	List(ctx context.Context, orgID, clientID string, page api.PaginationParams) ([]Contact, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Contact, error)
	Create(ctx context.Context, c *Contact) error
	Update(ctx context.Context, orgID, id string, req UpdateContactRequest) (*Contact, error)
	Delete(ctx context.Context, orgID, id string) error
	// ListByEmail returns every contact record carrying the given e-mail
	// address — used by the GDPR data export to gather all personal data
	// stored about a person.
	ListByEmail(ctx context.Context, orgID, email string) ([]Contact, error)

	ListForCI(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]CIContact, int, error)
	Link(ctx context.Context, link *CIContact) error
	Unlink(ctx context.Context, orgID, id string) error
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	contacts map[string]*Contact
	links    map[string]*CIContact
	seq      int
}

// AgeForTesting backdates a stored contact's updated_at timestamp. It exists
// so retention/erasure tests can create "expired" records without waiting.
func (r *MemoryRepository) AgeForTesting(id string, age time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.contacts[id]; ok {
		c.UpdatedAt = time.Now().UTC().Add(-age)
	}
}

// NewMemoryRepository creates a new in-memory contact repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		contacts: make(map[string]*Contact),
		links:    make(map[string]*CIContact),
	}
}

func (r *MemoryRepository) List(_ context.Context, orgID, clientID string, page api.PaginationParams) ([]Contact, int, error) {
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Contact, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.contacts[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, c *Contact) error {
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

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateContactRequest) (*Contact, error) {
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

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
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

func (r *MemoryRepository) ListByEmail(_ context.Context, orgID, email string) ([]Contact, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Contact
	for _, c := range r.contacts {
		if c.OrganizationID == orgID && strings.EqualFold(strings.TrimSpace(c.Email), strings.TrimSpace(email)) && c.Email != "" {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *MemoryRepository) ListForCI(_ context.Context, orgID, ciID string, page api.PaginationParams) ([]CIContact, int, error) {
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

func (r *MemoryRepository) Link(_ context.Context, link *CIContact) error {
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

func (r *MemoryRepository) Unlink(_ context.Context, orgID, id string) error {
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
