package rack

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Sentinel errors returned by repositories and used by the handler to select
// the appropriate HTTP status code.
var (
	ErrNotFound     = errors.New("not found")
	ErrRackNotFound = errors.New("rack not found")
	ErrValidation   = errors.New("validation failed")
)

// Repository defines persistence operations for racks, mounts, and cables.
type Repository interface {
	ListRacks(orgID, roomID string, page api.PaginationParams) ([]Rack, int, error)
	GetRack(orgID, id string) (*Rack, error)
	CreateRack(rk *Rack) error
	UpdateRack(orgID, id string, req UpdateRackRequest) (*Rack, error)
	DeleteRack(orgID, id string) error

	ListMounts(orgID, rackID string, page api.PaginationParams) ([]RackMount, int, error)
	CreateMount(m *RackMount) error
	GetMount(orgID, id string) (*RackMount, error)
	UpdateMount(orgID, id string, req UpdateMountRequest) (*RackMount, error)
	DeleteMount(orgID, id string) error

	ListCables(orgID string, page api.PaginationParams) ([]Cable, int, error)
	GetCable(orgID, id string) (*Cable, error)
	CreateCable(c *Cable) error
	UpdateCable(orgID, id string, req UpdateCableRequest) (*Cable, error)
	DeleteCable(orgID, id string) error
}

// validateMountFit ensures the proposed mount fits within the rack height and
// does not overlap any existing mount on the same face. existing excludes the
// mount being updated (pass "" when creating).
func validateMountFit(rackHeight, positionU, heightU int, face string, existing []RackMount, selfID string) error {
	if positionU < 1 {
		return fmt.Errorf("%w: position_u must be >= 1", ErrValidation)
	}
	if heightU < 1 {
		return fmt.Errorf("%w: height_u must be >= 1", ErrValidation)
	}
	if positionU+heightU-1 > rackHeight {
		return fmt.Errorf("%w: mount exceeds rack height of %dU", ErrValidation, rackHeight)
	}
	if face == "both" {
		return nil
	}
	start, end := positionU, positionU+heightU
	for _, e := range existing {
		if e.ID == selfID || e.Face == "both" || e.Face != face {
			continue
		}
		eStart, eEnd := e.PositionU, e.PositionU+e.HeightU
		if start < eEnd && eStart < end {
			return fmt.Errorf("%w: overlaps mount at position %dU", ErrValidation, e.PositionU)
		}
	}
	return nil
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu     sync.RWMutex
	racks  map[string]*Rack
	mounts map[string]*RackMount
	cables map[string]*Cable
	seq    int
}

// NewMemoryRepository creates a new in-memory rack repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		racks:  make(map[string]*Rack),
		mounts: make(map[string]*RackMount),
		cables: make(map[string]*Cable),
	}
}

func pageSlice[T any](items []T, page api.PaginationParams) ([]T, int) {
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

// --- Racks ---

func (r *MemoryRepository) ListRacks(orgID, roomID string, page api.PaginationParams) ([]Rack, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Rack
	for _, rk := range r.racks {
		if rk.OrganizationID != orgID {
			continue
		}
		if roomID != "" && rk.RoomID != roomID {
			continue
		}
		out = append(out, *rk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetRack(orgID, id string) (*Rack, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rk, ok := r.racks[id]
	if !ok || rk.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *rk
	return &cp, nil
}

func (r *MemoryRepository) CreateRack(rk *Rack) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rk.ID = fmt.Sprintf("rack-%d", r.seq)
	now := time.Now().UTC()
	rk.CreatedAt, rk.UpdatedAt = now, now
	stored := *rk
	r.racks[rk.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateRack(orgID, id string, req UpdateRackRequest) (*Rack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk, ok := r.racks[id]
	if !ok || rk.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.Name != nil {
		rk.Name = *req.Name
	}
	if req.HeightU != nil {
		rk.HeightU = *req.HeightU
	}
	if req.WidthMM != nil {
		rk.WidthMM = *req.WidthMM
	}
	if req.DepthMM != nil {
		rk.DepthMM = *req.DepthMM
	}
	if req.Notes != nil {
		rk.Notes = *req.Notes
	}
	rk.UpdatedAt = time.Now().UTC()
	cp := *rk
	return &cp, nil
}

func (r *MemoryRepository) DeleteRack(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk, ok := r.racks[id]
	if !ok || rk.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.racks, id)
	for mid, m := range r.mounts {
		if m.RackID == id {
			delete(r.mounts, mid)
		}
	}
	return nil
}

// --- Mounts ---

func (r *MemoryRepository) mountsForRack(rackID string) []RackMount {
	var out []RackMount
	for _, m := range r.mounts {
		if m.RackID == rackID {
			out = append(out, *m)
		}
	}
	return out
}

func (r *MemoryRepository) ListMounts(orgID, rackID string, page api.PaginationParams) ([]RackMount, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rk, ok := r.racks[rackID]
	if !ok || rk.OrganizationID != orgID {
		return nil, 0, ErrRackNotFound
	}
	out := r.mountsForRack(rackID)
	sort.Slice(out, func(i, j int) bool { return out[i].PositionU < out[j].PositionU })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) CreateMount(m *RackMount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk, ok := r.racks[m.RackID]
	if !ok || rk.OrganizationID != m.OrganizationID {
		return ErrRackNotFound
	}
	for _, e := range r.mounts {
		if e.CIID == m.CIID {
			return fmt.Errorf("%w: ci already mounted", ErrValidation)
		}
	}
	if err := validateMountFit(rk.HeightU, m.PositionU, m.HeightU, m.Face, r.mountsForRack(m.RackID), ""); err != nil {
		return err
	}
	r.seq++
	m.ID = fmt.Sprintf("rack-mount-%d", r.seq)
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	stored := *m
	r.mounts[m.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetMount(orgID, id string) (*RackMount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.mounts[id]
	if !ok || m.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (r *MemoryRepository) UpdateMount(orgID, id string, req UpdateMountRequest) (*RackMount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.mounts[id]
	if !ok || m.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	rk := r.racks[m.RackID]
	pos, height, face := m.PositionU, m.HeightU, m.Face
	if req.PositionU != nil {
		pos = *req.PositionU
	}
	if req.HeightU != nil {
		height = *req.HeightU
	}
	if req.Face != nil {
		face = *req.Face
		if !ValidFaces[face] {
			return nil, fmt.Errorf("%w: invalid face", ErrValidation)
		}
	}
	if rk != nil {
		if err := validateMountFit(rk.HeightU, pos, height, face, r.mountsForRack(m.RackID), id); err != nil {
			return nil, err
		}
	}
	m.PositionU, m.HeightU, m.Face = pos, height, face
	m.UpdatedAt = time.Now().UTC()
	cp := *m
	return &cp, nil
}

func (r *MemoryRepository) DeleteMount(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.mounts[id]
	if !ok || m.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.mounts, id)
	return nil
}

// --- Cables ---

func (r *MemoryRepository) ListCables(orgID string, page api.PaginationParams) ([]Cable, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Cable
	for _, c := range r.cables {
		if c.OrganizationID == orgID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetCable(orgID, id string) (*Cable, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cables[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) CreateCable(c *Cable) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	c.ID = fmt.Sprintf("cable-%d", r.seq)
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	stored := *c
	r.cables[c.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateCable(orgID, id string, req UpdateCableRequest) (*Cable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cables[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.Label != nil {
		c.Label = *req.Label
	}
	if req.CableType != nil {
		c.CableType = *req.CableType
	}
	if req.LengthM != nil {
		c.LengthM = req.LengthM
	}
	if req.Color != nil {
		c.Color = *req.Color
	}
	if req.SourceInterfaceID != nil {
		c.SourceInterfaceID = *req.SourceInterfaceID
	}
	if req.TargetInterfaceID != nil {
		c.TargetInterfaceID = *req.TargetInterfaceID
	}
	if req.Status != nil {
		c.Status = *req.Status
	}
	if req.InstalledAt != nil {
		c.InstalledAt = req.InstalledAt
	}
	c.UpdatedAt = time.Now().UTC()
	cp := *c
	return &cp, nil
}

func (r *MemoryRepository) DeleteCable(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cables[id]
	if !ok || c.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.cables, id)
	return nil
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
