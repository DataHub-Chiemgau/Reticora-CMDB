package ipam

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Sentinel errors used by the handler to select HTTP status codes.
var (
	ErrNotFound   = errors.New("not found")
	ErrValidation = errors.New("validation failed")
)

// SubnetFilter holds optional filters for listing subnets.
type SubnetFilter struct {
	ClientID string
	SiteID   string
}

// Repository defines persistence operations for IPAM entities.
type Repository interface {
	ListSubnets(orgID string, f SubnetFilter, page api.PaginationParams) ([]Subnet, int, error)
	GetSubnet(orgID, id string) (*Subnet, error)
	CreateSubnet(s *Subnet) error
	UpdateSubnet(orgID, id string, req UpdateSubnetRequest) (*Subnet, error)
	DeleteSubnet(orgID, id string) error

	ListIPAddresses(orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error)
	GetIPAddress(orgID, id string) (*IPAddress, error)
	CreateIPAddress(a *IPAddress) error
	UpdateIPAddress(orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error)
	DeleteIPAddress(orgID, id string) error

	ListInterfacesForCI(orgID, ciID string, page api.PaginationParams) ([]NetworkInterface, int, error)
	GetInterface(orgID, id string) (*NetworkInterface, error)
	CreateInterface(ni *NetworkInterface) error
	UpdateInterface(orgID, id string, req UpdateInterfaceRequest) (*NetworkInterface, error)
	DeleteInterface(orgID, id string) error
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	subnets    map[string]*Subnet
	addresses  map[string]*IPAddress
	interfaces map[string]*NetworkInterface
	seq        int
}

// NewMemoryRepository creates a new in-memory IPAM repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		subnets:    make(map[string]*Subnet),
		addresses:  make(map[string]*IPAddress),
		interfaces: make(map[string]*NetworkInterface),
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

// --- Subnets ---

func (r *MemoryRepository) ListSubnets(orgID string, f SubnetFilter, page api.PaginationParams) ([]Subnet, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Subnet
	for _, s := range r.subnets {
		if s.OrganizationID != orgID {
			continue
		}
		if f.ClientID != "" && s.ClientID != f.ClientID {
			continue
		}
		if f.SiteID != "" && s.SiteID != f.SiteID {
			continue
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetSubnet(orgID, id string) (*Subnet, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.subnets[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *MemoryRepository) CreateSubnet(s *Subnet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.subnets {
		if e.OrganizationID == s.OrganizationID && e.CIDR == s.CIDR {
			return fmt.Errorf("%w: duplicate cidr", ErrValidation)
		}
	}
	r.seq++
	s.ID = fmt.Sprintf("subnet-%d", r.seq)
	now := time.Now().UTC()
	s.CreatedAt, s.UpdatedAt = now, now
	stored := *s
	r.subnets[s.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateSubnet(orgID, id string, req UpdateSubnetRequest) (*Subnet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subnets[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.ClientID != nil {
		s.ClientID = *req.ClientID
	}
	if req.SiteID != nil {
		s.SiteID = *req.SiteID
	}
	if req.Name != nil {
		s.Name = *req.Name
	}
	if req.VLANID != nil {
		s.VLANID = req.VLANID
	}
	if req.Gateway != nil {
		s.Gateway = *req.Gateway
	}
	if req.DNSServers != nil {
		s.DNSServers = req.DNSServers
	}
	if req.Description != nil {
		s.Description = *req.Description
	}
	if req.IsManagement != nil {
		s.IsManagement = *req.IsManagement
	}
	s.UpdatedAt = time.Now().UTC()
	cp := *s
	return &cp, nil
}

func (r *MemoryRepository) DeleteSubnet(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subnets[id]
	if !ok || s.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.subnets, id)
	return nil
}

// --- IP Addresses ---

func (r *MemoryRepository) ListIPAddresses(orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []IPAddress
	for _, a := range r.addresses {
		if a.OrganizationID != orgID {
			continue
		}
		if subnetID != "" && a.SubnetID != subnetID {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetIPAddress(orgID, id string) (*IPAddress, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) CreateIPAddress(a *IPAddress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.addresses {
		if e.OrganizationID == a.OrganizationID && e.Address == a.Address {
			return fmt.Errorf("%w: duplicate address", ErrValidation)
		}
	}
	r.seq++
	a.ID = fmt.Sprintf("ip-%d", r.seq)
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	stored := *a
	r.addresses[a.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateIPAddress(orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.SubnetID != nil {
		a.SubnetID = *req.SubnetID
	}
	if req.InterfaceID != nil {
		a.InterfaceID = *req.InterfaceID
	}
	if req.Status != nil {
		a.Status = *req.Status
	}
	if req.DNSName != nil {
		a.DNSName = *req.DNSName
	}
	if req.Description != nil {
		a.Description = *req.Description
	}
	a.UpdatedAt = time.Now().UTC()
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) DeleteIPAddress(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.addresses, id)
	return nil
}

// --- Network Interfaces ---

func (r *MemoryRepository) ListInterfacesForCI(orgID, ciID string, page api.PaginationParams) ([]NetworkInterface, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []NetworkInterface
	for _, ni := range r.interfaces {
		if ni.OrganizationID != orgID || ni.CIID != ciID {
			continue
		}
		out = append(out, *ni)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetInterface(orgID, id string) (*NetworkInterface, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ni, ok := r.interfaces[id]
	if !ok || ni.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *ni
	return &cp, nil
}

func (r *MemoryRepository) CreateInterface(ni *NetworkInterface) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	ni.ID = fmt.Sprintf("nic-%d", r.seq)
	now := time.Now().UTC()
	ni.CreatedAt, ni.UpdatedAt = now, now
	stored := *ni
	r.interfaces[ni.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateInterface(orgID, id string, req UpdateInterfaceRequest) (*NetworkInterface, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ni, ok := r.interfaces[id]
	if !ok || ni.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.Name != nil {
		ni.Name = *req.Name
	}
	if req.MACAddress != nil {
		ni.MACAddress = *req.MACAddress
	}
	if req.InterfaceType != nil {
		ni.InterfaceType = *req.InterfaceType
	}
	if req.SpeedMbps != nil {
		ni.SpeedMbps = req.SpeedMbps
	}
	if req.IsManagement != nil {
		ni.IsManagement = *req.IsManagement
	}
	if req.IsUplink != nil {
		ni.IsUplink = *req.IsUplink
	}
	if req.AdminStatus != nil {
		ni.AdminStatus = *req.AdminStatus
	}
	if req.OperStatus != nil {
		ni.OperStatus = *req.OperStatus
	}
	if req.Description != nil {
		ni.Description = *req.Description
	}
	ni.UpdatedAt = time.Now().UTC()
	cp := *ni
	return &cp, nil
}

func (r *MemoryRepository) DeleteInterface(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ni, ok := r.interfaces[id]
	if !ok || ni.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.interfaces, id)
	return nil
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
