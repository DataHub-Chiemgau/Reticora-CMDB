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
	clients   map[string]*Client
	sites     map[string]*Site
	buildings map[string]*Building
	rooms     map[string]*Room
	seq       int
}

// NewMemoryRepository creates an in-memory location repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
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
	for _, existing := range r.clients {
		if existing.OrganizationID == c.OrganizationID && existing.Slug == c.Slug {
			return fmt.Errorf("duplicate slug")
		}
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
	if req.Name != nil {
		c.Name = *req.Name
	}
	if req.Slug != nil {
		c.Slug = *req.Slug
	}
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
		return fmt.Errorf("client not found")
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
		s.Name = *req.Name
	}
	if req.Address != nil {
		s.Address = *req.Address
	}
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
