package ticket

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
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

	// RunInTx runs fn in one transaction of the context's tenant scope; the
	// repository calls fn makes with its context join it, which commits when
	// fn returns nil and rolls back otherwise (WP-766).
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
	// LockOpenTicket takes the transaction lock of the target (entity type
	// and id) and returns the oldest ticket visible to the caller that is
	// linked to it and not closed, or nil; called in RunInTx, the lock is
	// held until the end of the transaction, so concurrent creations for
	// the same target see each other's ticket.
	LockOpenTicket(ctx context.Context, orgID string, target Target) (*LinkedTicket, error)
	// LinkedTickets returns per id of entityType the tickets visible to the
	// caller that are linked to it, by ticket number, with one query; ids
	// without tickets have no entry.
	LinkedTickets(ctx context.Context, orgID, entityType string, ids []string) (map[string][]LinkedTicket, error)
	// RecordOrigin writes the audit entry ticket.create of a ticket created
	// for origin as the last write of the context transaction (WP-766).
	RecordOrigin(ctx context.Context, t *Ticket, origin Target) error

	// Foreign references (TKT-03, WP-293): a reference (provider, ref) is
	// unique per organization; a duplicate is *api.ConflictError on
	// external_ref. The lookups read under the caller's scope; a reference
	// that is unknown, of another organization or outside the scope is
	// ErrNotFound.

	// GetByExternalRef returns the ticket with the reference.
	GetByExternalRef(ctx context.Context, orgID, provider, ref string) (*Ticket, error)
	// SetExternalRef sets the reference of ticket ticketID; empty provider
	// and ref remove it.
	SetExternalRef(ctx context.Context, orgID, ticketID, provider, ref string) error
	// GetCommentByExternalRef returns the comment with the reference.
	GetCommentByExternalRef(ctx context.Context, orgID, provider, ref string) (*Comment, error)
	// SetCommentExternalRef sets the reference of comment commentID; empty
	// provider and ref remove it.
	SetCommentExternalRef(ctx context.Context, orgID, commentID, provider, ref string) error
}

// Errors of a foreign reference: both parts are set together (422, as
// ticket_external_ref_pair_check), and a reference is taken once per
// organization (409, as uq_ticket_org_external_ref and
// uq_ticket_comment_org_external_ref).
var (
	errExternalRefPair = &api.FieldError{Field: "external_ref", Status: http.StatusUnprocessableEntity,
		Message: "external_provider and external_ref are set together"}
	errExternalRefTaken = &api.ConflictError{Field: "external_ref", Message: "the value is already taken"}
)

// checkExternalRefPair validates that provider and ref are both set or both
// empty.
func checkExternalRefPair(provider, ref string) error {
	if (provider == "") != (ref == "") {
		return errExternalRefPair
	}
	return nil
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
	// txMu serializes RunInTx: the memory repository knows no transaction,
	// one RunInTx at a time keeps the exclusive check and the creation of
	// a ticket together (WP-766).
	txMu      sync.Mutex
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
	if err := checkExternalRefPair(t.ExternalProvider, t.ExternalRef); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.ExternalRef != "" && r.ticketByRef(t.OrganizationID, t.ExternalProvider, t.ExternalRef) != nil {
		return errExternalRefTaken
	}

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
	if err := checkExternalRefPair(c.ExternalProvider, c.ExternalRef); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Verify ticket exists
	t, ok := r.tickets[c.TicketID]
	if !ok || t.OrganizationID != c.OrganizationID {
		return ErrNotFound
	}
	if c.ExternalRef != "" && r.commentByRef(c.OrganizationID, c.ExternalProvider, c.ExternalRef) != nil {
		return errExternalRefTaken
	}

	if c.AuthorType == "" {
		c.AuthorType = AuthorUser
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

// RunInTx implements Repository without a transaction: calls run one at a
// time, and when fn fails the tickets, comments and links it wrote are
// removed again, so a ticket and its links appear together or not at all.
func (r *MemoryRepository) RunInTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	// A nested call joins the running one, as the PostgreSQL repository
	// joins the context transaction (ticket.Provider around the service,
	// WP-293).
	if ctx.Value(memoryTxKey{}) == r {
		return fn(ctx)
	}
	ctx = context.WithValue(ctx, memoryTxKey{}, r)
	r.txMu.Lock()
	defer r.txMu.Unlock()
	r.mu.RLock()
	tickets, comments, links := maps.Clone(r.tickets), maps.Clone(r.comments), maps.Clone(r.links)
	r.mu.RUnlock()
	if err := fn(ctx); err != nil {
		r.mu.Lock()
		r.tickets, r.comments, r.links = tickets, comments, links
		r.mu.Unlock()
		return err
	}
	return nil
}

// memoryTxKey marks the context of a running MemoryRepository.RunInTx.
type memoryTxKey struct{}

// LockOpenTicket implements Repository (RunInTx serializes the callers).
func (r *MemoryRepository) LockOpenTicket(_ context.Context, orgID string, target Target) (*LinkedTicket, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var found *LinkedTicket
	for _, l := range r.links {
		if l.OrganizationID != orgID || l.EntityType != target.EntityType || !strings.EqualFold(l.EntityID, target.EntityID) {
			continue
		}
		t, ok := r.tickets[l.TicketID]
		if !ok || t.Status == StatusClosed {
			continue
		}
		if found == nil || t.TicketNumber < found.TicketNumber {
			found = &LinkedTicket{ID: t.ID, TicketNumber: t.TicketNumber, Status: t.Status}
		}
	}
	return found, nil
}

// LinkedTickets implements Repository.
func (r *MemoryRepository) LinkedTickets(_ context.Context, orgID, entityType string, ids []string) (map[string][]LinkedTicket, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string][]LinkedTicket{}
	for _, l := range r.links {
		if l.OrganizationID != orgID || l.EntityType != entityType {
			continue
		}
		idx := slices.IndexFunc(ids, func(id string) bool { return strings.EqualFold(id, l.EntityID) })
		if idx < 0 {
			continue
		}
		if t, ok := r.tickets[l.TicketID]; ok {
			out[ids[idx]] = append(out[ids[idx]], LinkedTicket{ID: t.ID, TicketNumber: t.TicketNumber, Status: t.Status})
		}
	}
	for id := range out {
		slices.SortFunc(out[id], func(a, b LinkedTicket) int { return a.TicketNumber - b.TicketNumber })
	}
	return out, nil
}

// RecordOrigin implements Repository; the memory profile has no audit log.
func (r *MemoryRepository) RecordOrigin(context.Context, *Ticket, Target) error {
	return nil
}

// ticketByRef returns the ticket of the organization with the reference or
// nil; the caller holds the lock.
func (r *MemoryRepository) ticketByRef(orgID, provider, ref string) *Ticket {
	for _, t := range r.tickets {
		if t.OrganizationID == orgID && t.ExternalProvider == provider && t.ExternalRef == ref {
			return t
		}
	}
	return nil
}

// commentByRef returns the comment of the organization with the reference
// or nil; the caller holds the lock.
func (r *MemoryRepository) commentByRef(orgID, provider, ref string) *Comment {
	for _, c := range r.comments {
		if c.OrganizationID == orgID && c.ExternalProvider == provider && c.ExternalRef == ref {
			return c
		}
	}
	return nil
}

// GetByExternalRef implements Repository.
func (r *MemoryRepository) GetByExternalRef(_ context.Context, orgID, provider, ref string) (*Ticket, error) {
	if provider == "" || ref == "" {
		return nil, ErrNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t := r.ticketByRef(orgID, provider, ref)
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}

// SetExternalRef implements Repository.
func (r *MemoryRepository) SetExternalRef(_ context.Context, orgID, ticketID, provider, ref string) error {
	if err := checkExternalRefPair(provider, ref); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, err := r.ticketOf(orgID, ticketID)
	if err != nil {
		return err
	}
	if other := r.ticketByRef(orgID, provider, ref); ref != "" && other != nil && other.ID != t.ID {
		return errExternalRefTaken
	}
	t.ExternalProvider, t.ExternalRef = provider, ref
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// GetCommentByExternalRef implements Repository.
func (r *MemoryRepository) GetCommentByExternalRef(_ context.Context, orgID, provider, ref string) (*Comment, error) {
	if provider == "" || ref == "" {
		return nil, ErrNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := r.commentByRef(orgID, provider, ref)
	if c == nil {
		return nil, ErrNotFound
	}
	out := *c
	return &out, nil
}

// SetCommentExternalRef implements Repository.
func (r *MemoryRepository) SetCommentExternalRef(_ context.Context, orgID, commentID, provider, ref string) error {
	if err := checkExternalRefPair(provider, ref); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.comments[commentID]
	if !ok || c.OrganizationID != orgID {
		return ErrNotFound
	}
	if other := r.commentByRef(orgID, provider, ref); ref != "" && other != nil && other.ID != c.ID {
		return errExternalRefTaken
	}
	c.ExternalProvider, c.ExternalRef = provider, ref
	c.UpdatedAt = time.Now().UTC()
	return nil
}
