package tenantapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for the location hierarchy.
type Repository interface {
	// Organization (the tenant itself)
	GetOrganization(ctx context.Context, orgID string) (*Organization, error)
	UpdateOrganization(ctx context.Context, orgID string, req UpdateOrganizationRequest) (*Organization, error)

	// Clients
	ListClients(ctx context.Context, orgID string, page api.PaginationParams) ([]Client, int, error)
	GetClient(ctx context.Context, orgID, id string) (*Client, error)
	CreateClient(ctx context.Context, c *Client) error
	UpdateClient(ctx context.Context, orgID, id string, req UpdateClientRequest) (*Client, error)
	DeleteClient(ctx context.Context, orgID, id string) error

	// Sites
	ListSites(ctx context.Context, orgID, clientID string, page api.PaginationParams) ([]Site, int, error)
	GetSite(ctx context.Context, orgID, id string) (*Site, error)
	CreateSite(ctx context.Context, s *Site) error
	UpdateSite(ctx context.Context, orgID, id string, req UpdateSiteRequest) (*Site, error)
	DeleteSite(ctx context.Context, orgID, id string) error

	// Buildings
	ListBuildings(ctx context.Context, orgID, siteID string, page api.PaginationParams) ([]Building, int, error)
	GetBuilding(ctx context.Context, orgID, id string) (*Building, error)
	CreateBuilding(ctx context.Context, b *Building) error
	UpdateBuilding(ctx context.Context, orgID, id string, req UpdateBuildingRequest) (*Building, error)
	DeleteBuilding(ctx context.Context, orgID, id string) error

	// Rooms
	ListRooms(ctx context.Context, orgID, buildingID string, page api.PaginationParams) ([]Room, int, error)
	GetRoom(ctx context.Context, orgID, id string) (*Room, error)
	CreateRoom(ctx context.Context, rm *Room) error
	UpdateRoom(ctx context.Context, orgID, id string, req UpdateRoomRequest) (*Room, error)
	DeleteRoom(ctx context.Context, orgID, id string) error
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu        sync.RWMutex
	orgs      map[string]*Organization
	clients   map[string]*Client
	sites     map[string]*Site
	buildings map[string]*Building
	rooms     map[string]*Room
	seq       int
}

// NewMemoryRepository creates an in-memory location repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		orgs:      make(map[string]*Organization),
		clients:   make(map[string]*Client),
		sites:     make(map[string]*Site),
		buildings: make(map[string]*Building),
		rooms:     make(map[string]*Room),
	}
}

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%d", prefix, r.seq)
}

func paginate[T any](items []T, page api.PaginationParams) ([]T, int) {
	total := len(items)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return items[start:end], total
}

// setOptional applies an optional text field of a PATCH: nil keeps the
// value, an empty string clears it.
func setOptional(dst **string, v *string) {
	if v == nil {
		return
	}
	if *v == "" {
		*dst = nil
		return
	}
	value := *v
	*dst = &value
}

// --- Organization ---

// organization returns the stored organization or, in --no-db mode, the
// defaults of a new one (LOC-01). Callers hold the lock.
func (r *MemoryRepository) organization(orgID string) *Organization {
	if o, ok := r.orgs[orgID]; ok {
		return o
	}
	now := time.Now().UTC()
	o := &Organization{ID: orgID, Name: orgID, Slug: orgID, Plan: "essential", Settings: map[string]any{},
		Timezone: "UTC", Currency: "EUR", Locale: "de-DE", CreatedAt: now, UpdatedAt: now}
	r.orgs[orgID] = o
	return o
}

func (r *MemoryRepository) GetOrganization(_ context.Context, orgID string) (*Organization, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r.organization(orgID)
	return &cp, nil
}

func (r *MemoryRepository) UpdateOrganization(_ context.Context, orgID string, req UpdateOrganizationRequest) (*Organization, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.organization(orgID)
	if req.Name != nil {
		o.Name = *req.Name
	}
	if req.Settings != nil {
		o.Settings = req.Settings
	}
	if req.Timezone != nil {
		o.Timezone = *req.Timezone
	}
	if req.Currency != nil {
		o.Currency = *req.Currency
	}
	if req.Locale != nil {
		o.Locale = *req.Locale
	}
	if req.AIOptIn != nil {
		o.AIOptIn = *req.AIOptIn
	}
	o.UpdatedAt = time.Now().UTC()
	cp := *o
	return &cp, nil
}

// clientConflict reports a client of the organization other than id with
// the same name or slug (LOC-02). Callers hold the lock.
func (r *MemoryRepository) clientConflict(orgID, id, name, slug string) error {
	for _, existing := range r.clients {
		if existing.OrganizationID != orgID || existing.ID == id {
			continue
		}
		if existing.Name == name {
			return &ConflictError{Field: "name", Message: "a client with this name already exists"}
		}
		if existing.Slug == slug {
			return &ConflictError{Field: "slug", Message: "a client with this slug already exists"}
		}
	}
	return nil
}

// siteConflict reports a site of the same client other than id with the
// same name (LOC-03). Callers hold the lock.
func (r *MemoryRepository) siteConflict(orgID, clientID, id, name string) error {
	for _, existing := range r.sites {
		if existing.OrganizationID == orgID && existing.ClientID == clientID && existing.ID != id && existing.Name == name {
			return &ConflictError{Field: "name", Message: "a site with this name already exists for the client"}
		}
	}
	return nil
}

// buildingConflict and roomConflict report another building of the site
// or room of the building with the name (LOC-04, LOC-05). Callers hold the
// lock.
func (r *MemoryRepository) buildingConflict(siteID, id, name string) error {
	for _, existing := range r.buildings {
		if existing.SiteID == siteID && existing.ID != id && existing.Name == name {
			return &ConflictError{Field: "name", Message: "a building with this name already exists at the site"}
		}
	}
	return nil
}

func (r *MemoryRepository) roomConflict(buildingID, id, name string) error {
	for _, existing := range r.rooms {
		if existing.BuildingID == buildingID && existing.ID != id && existing.Name == name {
			return &ConflictError{Field: "name", Message: "a room with this name already exists in the building"}
		}
	}
	return nil
}

// --- Clients ---

func (r *MemoryRepository) ListClients(_ context.Context, orgID string, page api.PaginationParams) ([]Client, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Client
	for _, c := range r.clients {
		if c.OrganizationID == orgID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := paginate(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetClient(_ context.Context, orgID, id string) (*Client, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) CreateClient(_ context.Context, c *Client) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.clientConflict(c.OrganizationID, "", c.Name, c.Slug); err != nil {
		return err
	}
	c.ID = r.nextID("client")
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Settings == nil {
		c.Settings = map[string]any{}
	}
	stored := *c
	r.clients[c.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateClient(_ context.Context, orgID, id string, req UpdateClientRequest) (*Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[id]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	name, slug := c.Name, c.Slug
	if req.Name != nil {
		name = *req.Name
	}
	if req.Slug != nil {
		slug = *req.Slug
	}
	if err := r.clientConflict(orgID, id, name, slug); err != nil {
		return nil, err
	}
	c.Name, c.Slug = name, slug
	setOptional(&c.ExternalRef, req.ExternalRef)
	setOptional(&c.Timezone, req.Timezone)
	if req.Settings != nil {
		c.Settings = req.Settings
	}
	c.UpdatedAt = time.Now().UTC()
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) DeleteClient(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[id]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.clients, id)
	return nil
}

// --- Sites ---

func (r *MemoryRepository) ListSites(_ context.Context, orgID, clientID string, page api.PaginationParams) ([]Site, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Site
	for _, s := range r.sites {
		if s.OrganizationID != orgID {
			continue
		}
		if clientID != "" && s.ClientID != clientID {
			continue
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := paginate(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetSite(_ context.Context, orgID, id string) (*Site, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sites[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *s
	return &cp, nil
}

func (r *MemoryRepository) CreateSite(_ context.Context, s *Site) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[s.ClientID]; !ok || r.clients[s.ClientID].OrganizationID != s.OrganizationID {
		return &FieldError{Field: "client_id", Message: "the referenced record does not exist"}
	}
	if err := r.siteConflict(s.OrganizationID, s.ClientID, "", s.Name); err != nil {
		return err
	}
	s.ID = r.nextID("site")
	now := time.Now().UTC()
	s.CreatedAt, s.UpdatedAt = now, now
	stored := *s
	r.sites[s.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateSite(_ context.Context, orgID, id string, req UpdateSiteRequest) (*Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sites[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Name != nil {
		if err := r.siteConflict(orgID, s.ClientID, id, *req.Name); err != nil {
			return nil, err
		}
		s.Name = *req.Name
	}
	if req.Address != nil {
		s.Address = *req.Address
	}
	setOptional(&s.Timezone, req.Timezone)
	if req.GeoLat != nil {
		s.GeoLat = req.GeoLat
	}
	if req.GeoLon != nil {
		s.GeoLon = req.GeoLon
	}
	if req.Notes != nil {
		s.Notes = *req.Notes
	}
	s.UpdatedAt = time.Now().UTC()
	cp := *s
	return &cp, nil
}

func (r *MemoryRepository) DeleteSite(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sites[id]
	if !ok || s.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.sites, id)
	return nil
}

// --- Buildings ---

func (r *MemoryRepository) ListBuildings(_ context.Context, orgID, siteID string, page api.PaginationParams) ([]Building, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Building
	for _, b := range r.buildings {
		if b.OrganizationID != orgID {
			continue
		}
		if siteID != "" && b.SiteID != siteID {
			continue
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := paginate(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetBuilding(_ context.Context, orgID, id string) (*Building, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.buildings[id]
	if !ok || b.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *b
	return &cp, nil
}

func (r *MemoryRepository) CreateBuilding(_ context.Context, b *Building) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sites[b.SiteID]; !ok || s.OrganizationID != b.OrganizationID {
		return fmt.Errorf("site not found")
	}
	if err := r.buildingConflict(b.SiteID, "", b.Name); err != nil {
		return err
	}
	b.ID = r.nextID("building")
	now := time.Now().UTC()
	b.CreatedAt, b.UpdatedAt = now, now
	stored := *b
	r.buildings[b.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateBuilding(_ context.Context, orgID, id string, req UpdateBuildingRequest) (*Building, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buildings[id]
	if !ok || b.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Name != nil {
		if err := r.buildingConflict(b.SiteID, id, *req.Name); err != nil {
			return nil, err
		}
		b.Name = *req.Name
	}
	if req.Floors != nil {
		b.Floors = req.Floors
	}
	if req.FloorplanObjectKey != nil {
		b.FloorplanObjectKey = *req.FloorplanObjectKey
	}
	b.UpdatedAt = time.Now().UTC()
	cp := *b
	return &cp, nil
}

func (r *MemoryRepository) DeleteBuilding(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buildings[id]
	if !ok || b.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.buildings, id)
	return nil
}

// --- Rooms ---

func (r *MemoryRepository) ListRooms(_ context.Context, orgID, buildingID string, page api.PaginationParams) ([]Room, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Room
	for _, rm := range r.rooms {
		if rm.OrganizationID != orgID {
			continue
		}
		if buildingID != "" && rm.BuildingID != buildingID {
			continue
		}
		out = append(out, *rm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := paginate(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetRoom(_ context.Context, orgID, id string) (*Room, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rm, ok := r.rooms[id]
	if !ok || rm.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *rm
	return &cp, nil
}

func (r *MemoryRepository) CreateRoom(_ context.Context, rm *Room) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.buildings[rm.BuildingID]; !ok || b.OrganizationID != rm.OrganizationID {
		return fmt.Errorf("building not found")
	}
	if err := r.roomConflict(rm.BuildingID, "", rm.Name); err != nil {
		return err
	}
	rm.ID = r.nextID("room")
	now := time.Now().UTC()
	rm.CreatedAt, rm.UpdatedAt = now, now
	if rm.RoomType == "" {
		rm.RoomType = "general"
	}
	stored := *rm
	r.rooms[rm.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateRoom(_ context.Context, orgID, id string, req UpdateRoomRequest) (*Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rm, ok := r.rooms[id]
	if !ok || rm.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Name != nil {
		if err := r.roomConflict(rm.BuildingID, id, *req.Name); err != nil {
			return nil, err
		}
		rm.Name = *req.Name
	}
	if req.Floor != nil {
		rm.Floor = req.Floor
	}
	if req.RoomType != nil {
		rm.RoomType = *req.RoomType
	}
	rm.UpdatedAt = time.Now().UTC()
	cp := *rm
	return &cp, nil
}

func (r *MemoryRepository) DeleteRoom(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rm, ok := r.rooms[id]
	if !ok || rm.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.rooms, id)
	return nil
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
