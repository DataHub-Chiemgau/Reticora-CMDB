package ticket

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ErrNotFound: the ticket is unknown, of another organization or outside the
// caller's scope; all answer 404 alike (TEN-04).
var ErrNotFound = errors.New("ticket not found")

// ErrLinkExists: the object is already linked to the ticket (409, WP-764).
var ErrLinkExists = errors.New("the object is already linked to the ticket")

// Repository defines persistence operations for tickets.
type Repository interface {
	List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Ticket, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Ticket, error)
	Create(ctx context.Context, t *Ticket) error
	Update(ctx context.Context, orgID, id string, req *UpdateRequest) (*Ticket, error)
	Delete(ctx context.Context, orgID, id string) error
	AddComment(ctx context.Context, c *Comment) error
	ListComments(ctx context.Context, orgID, ticketID string, page api.PaginationParams) ([]Comment, int, error)
	// ListLinks returns a page of the links of a ticket in the order
	// created_at, id (keyset cursor of LinkNextCursor) and their number;
	// an unknown or invisible ticket is ErrNotFound (WP-764).
	ListLinks(ctx context.Context, orgID, ticketID string, page api.PaginationParams) ([]Link, int, error)
	// CreateLink links the object of link to its ticket: an unknown or
	// invisible ticket or target is ErrNotFound, a second link of the same
	// object ErrLinkExists.
	CreateLink(ctx context.Context, link *Link) error
	// DeleteLink removes link linkID of ticket ticketID; a link that does
	// not exist or is not visible is ErrNotFound.
	DeleteLink(ctx context.Context, orgID, ticketID, linkID string) error
}

// ValidLinkEntityType reports whether entityType is an entity type of a
// ticket link.
func ValidLinkEntityType(entityType string) bool {
	return slices.Contains(LinkEntityTypes, entityType)
}

// NormalizeTags returns the tag keys without surrounding spaces, empty
// values and duplicates, sorted; never nil.
func NormalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" && !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// linkSortValue is the keyset value of a link (created_at), formatted alike
// for the memory and the PostgreSQL repository.
func linkSortValue(l *Link) string {
	return l.CreatedAt.UTC().Format(time.RFC3339Nano)
}

// Keyset order of the links of a ticket.
const (
	linkSortColumn = "created_at"
	linkSortDir    = "asc"
)

// LinkNextCursor returns the cursor after the last link of a full page, or
// "" when the page was not full.
func LinkNextCursor(items []Link, limit int) string {
	if len(items) == 0 || len(items) < limit {
		return ""
	}
	last := &items[len(items)-1]
	return api.EncodeCursor(api.Cursor{Sort: linkSortColumn, Dir: linkSortDir, Value: linkSortValue(last), ID: last.ID})
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu        sync.RWMutex
	tickets   map[string]*Ticket
	comments  map[string]*Comment
	links     map[string]*Link
	nextID    int
	nextCmtID int
	nextLink  int
	// numbers is the last ticket number per organization (WP-290).
	numbers map[string]int
}

// NewMemoryRepository creates a new in-memory ticket repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		tickets:  make(map[string]*Ticket),
		comments: make(map[string]*Comment),
		links:    make(map[string]*Link),
		numbers:  make(map[string]int),
	}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Ticket, int, error) {
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
		if filter.ReporterID != "" && t.ReporterID != filter.ReporterID {
			continue
		}
		// The memory repository knows no team memberships: team_id=mine
		// (TeamMemberOf) lists nothing (E-143).
		if filter.TeamMemberOf != "" {
			continue
		}
		if filter.TeamID != "" && t.TeamID != filter.TeamID {
			continue
		}
		if !hasAllTags(t.Tags, filter.Tags) {
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
		remaining := make([]Ticket, 0, len(result))
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Ticket, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return t, nil
}

func (r *MemoryRepository) Create(_ context.Context, t *Ticket) error {
	if t.Status == "" {
		t.Status = StatusNew
	}
	if err := CheckInitialStatus(t.Status); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	t.Tags = NormalizeTags(t.Tags)
	r.nextID++
	r.numbers[t.OrganizationID]++
	// UUID form, as the handlers accept only UUID ids (API-03).
	t.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.nextID)
	t.TicketNumber = r.numbers[t.OrganizationID]
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	r.tickets[t.ID] = t
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req *UpdateRequest) (*Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return nil, ErrNotFound
	}

	if req.Status != nil && *req.Status != t.Status {
		if err := CheckTransition(t.Status, *req.Status); err != nil {
			return nil, err
		}
	}
	if req.Title != nil {
		t.Title = *req.Title
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	if req.Status != nil && *req.Status != t.Status {
		now := time.Now().UTC().Format(time.RFC3339)
		switch {
		case *req.Status == StatusResolved:
			t.ResolvedAt = now
		case *req.Status == StatusClosed:
			t.ClosedAt = now
		case IsReopen(t.Status, *req.Status):
			t.ResolvedAt, t.ClosedAt = "", ""
		}
		t.Status = *req.Status
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
		t.Tags = NormalizeTags(req.Tags)
	}
	t.UpdatedAt = time.Now().UTC()
	return t, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.tickets, id)
	// Remove associated comments and links
	for cid, c := range r.comments {
		if c.TicketID == id {
			delete(r.comments, cid)
		}
	}
	for lid, l := range r.links {
		if l.TicketID == id {
			delete(r.links, lid)
		}
	}
	return nil
}

// hasAllTags reports whether tags contains every key of want.
func hasAllTags(tags, want []string) bool {
	for _, key := range want {
		if !slices.Contains(tags, key) {
			return false
		}
	}
	return true
}

// ticketOf returns the ticket of the organization or ErrNotFound; the
// caller holds the lock.
func (r *MemoryRepository) ticketOf(orgID, id string) (*Ticket, error) {
	t, ok := r.tickets[id]
	if !ok || t.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return t, nil
}

// ListLinks implements Repository. The memory repository knows no target
// objects; it pages the links like the PostgreSQL repository.
func (r *MemoryRepository) ListLinks(_ context.Context, orgID, ticketID string, page api.PaginationParams) ([]Link, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, err := r.ticketOf(orgID, ticketID); err != nil {
		return nil, 0, err
	}
	all := make([]Link, 0)
	for _, l := range r.links {
		if l.TicketID == ticketID {
			all = append(all, *l)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		vi, vj := linkSortValue(&all[i]), linkSortValue(&all[j])
		if vi == vj {
			return all[i].ID < all[j].ID
		}
		return vi < vj
	})
	total := len(all)
	rest := all
	if page.Cursor != nil {
		if !page.Cursor.Matches(linkSortColumn, linkSortDir) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		rest = make([]Link, 0, len(all))
		for i := range all {
			if api.KeysetCompare(linkSortValue(&all[i]), all[i].ID, *page.Cursor, linkSortDir) {
				rest = append(rest, all[i])
			}
		}
	} else {
		rest = rest[min(page.Offset, len(rest)):]
	}
	return rest[:min(page.Limit, len(rest))], total, nil
}

// CreateLink implements Repository; the memory repository checks no target
// object, only the ticket and duplicates.
func (r *MemoryRepository) CreateLink(_ context.Context, link *Link) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.ticketOf(link.OrganizationID, link.TicketID); err != nil {
		return err
	}
	for _, l := range r.links {
		if l.TicketID == link.TicketID && l.EntityType == link.EntityType && l.EntityID == link.EntityID {
			return ErrLinkExists
		}
	}
	r.nextLink++
	link.ID = fmt.Sprintf("00000000-0000-4000-9000-%012d", r.nextLink)
	link.CreatedAt = time.Now().UTC()
	stored := *link
	r.links[link.ID] = &stored
	return nil
}

// DeleteLink implements Repository.
func (r *MemoryRepository) DeleteLink(_ context.Context, orgID, ticketID, linkID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.ticketOf(orgID, ticketID); err != nil {
		return err
	}
	l, ok := r.links[linkID]
	if !ok || l.TicketID != ticketID {
		return ErrNotFound
	}
	delete(r.links, linkID)
	return nil
}

func (r *MemoryRepository) AddComment(_ context.Context, c *Comment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Verify ticket exists
	t, ok := r.tickets[c.TicketID]
	if !ok || t.OrganizationID != c.OrganizationID {
		return ErrNotFound
	}

	r.nextCmtID++
	c.ID = fmt.Sprintf("comment-%d", r.nextCmtID)
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	r.comments[c.ID] = c
	return nil
}

func (r *MemoryRepository) ListComments(_ context.Context, orgID, ticketID string, page api.PaginationParams) ([]Comment, int, error) {
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
