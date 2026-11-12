package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
	// ErrConflict: the value is already taken (409), as a unique
	// violation in the database.
	ErrConflict = errors.New("conflict")
	// ErrInUse: the record cannot be removed (422) – the default VRF or a
	// VRF that subnets still reference.
	ErrInUse = errors.New("in use")
)

// InterfaceInUseError refuses the delete of an interface that an edge other
// than an unverified discovery edge names as an end (409; REL-05, F-042).
// RelationshipIDs are the edges the caller can see; it is empty when only
// edges outside the caller's scope keep the interface.
type InterfaceInUseError struct {
	RelationshipIDs []string
}

// validation: a fixed rule text with the IDs of the edges the caller can see.
func (e *InterfaceInUseError) Error() string {
	if len(e.RelationshipIDs) == 0 {
		return "the interface is an end of a relationship; remove the relationship first"
	}
	return "the interface is an end of the relationships " + strings.Join(e.RelationshipIDs, ", ") +
		"; remove them first"
}

// SubnetFilter holds optional filters for listing subnets.
type SubnetFilter struct {
	ClientID string
	SiteID   string
	// ParentID lists the children of a subnet (IPM-02); Root lists the
	// subnets without a parent the caller can see.
	ParentID string
	Root     bool
}

// Repository defines persistence operations for IPAM entities.
type Repository interface {
	ListSubnets(ctx context.Context, orgID string, f SubnetFilter, page api.PaginationParams) ([]Subnet, int, error)
	GetSubnet(ctx context.Context, orgID, id string) (*Subnet, error)
	CreateSubnet(ctx context.Context, s *Subnet) error
	UpdateSubnet(ctx context.Context, orgID, id string, req *UpdateSubnetRequest) (*Subnet, error)
	DeleteSubnet(ctx context.Context, orgID, id string) error

	ListIPAddresses(ctx context.Context, orgID string, f IPFilter, page api.PaginationParams) ([]IPAddress, int, error)
	GetIPAddress(ctx context.Context, orgID, id string) (*IPAddress, error)
	CreateIPAddress(ctx context.Context, a *IPAddress) error
	UpdateIPAddress(ctx context.Context, orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error)
	DeleteIPAddress(ctx context.Context, orgID, id string) error

	// IP reservations of a subnet (NET-09, reservation.go): rows of
	// ip_address with type reserved, the subnet and no interface.
	ListReservations(ctx context.Context, orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error)
	CreateReservation(ctx context.Context, a *IPAddress) error
	DeleteReservation(ctx context.Context, orgID, subnetID, id string) error

	ListInterfacesForCI(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]NetworkInterface, int, error)
	GetInterface(ctx context.Context, orgID, id string) (*NetworkInterface, error)
	CreateInterface(ctx context.Context, ni *NetworkInterface) error
	UpdateInterface(ctx context.Context, orgID, id string, req *UpdateInterfaceRequest) (*NetworkInterface, error)
	DeleteInterface(ctx context.Context, orgID, id string) error

	ListVLANs(ctx context.Context, orgID, siteID string, page api.PaginationParams) ([]VLAN, int, error)
	GetVLAN(ctx context.Context, orgID, id string) (*VLAN, error)
	CreateVLAN(ctx context.Context, v *VLAN) error
	UpdateVLAN(ctx context.Context, orgID, id string, req UpdateVLANRequest) (*VLAN, error)
	DeleteVLAN(ctx context.Context, orgID, id string) error

	ListVRFs(ctx context.Context, orgID string, page api.PaginationParams) ([]VRF, int, error)
	GetVRF(ctx context.Context, orgID, id string) (*VRF, error)
	CreateVRF(ctx context.Context, v *VRF) error
	UpdateVRF(ctx context.Context, orgID, id string, req UpdateVRFRequest) (*VRF, error)
	DeleteVRF(ctx context.Context, orgID, id string) error

	// IPAM sections (IPM-01, section.go).
	SectionRepository

	// Occupancy of a subnet for usage and free space (IPM-03,
	// space_handler.go).
	SpaceRepository

	// Atomic allocation of addresses and subnets (IPM-03, allocate.go).
	AllocationRepository
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	subnets    map[string]*Subnet
	addresses  map[string]*IPAddress
	interfaces map[string]*NetworkInterface
	vlans      map[string]*VLAN
	vrfs       map[string]*VRF
	sections   map[string]*Section
	seq        int
}

// NewMemoryRepository creates a new in-memory IPAM repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		subnets:    make(map[string]*Subnet),
		addresses:  make(map[string]*IPAddress),
		interfaces: make(map[string]*NetworkInterface),
		vlans:      make(map[string]*VLAN),
		vrfs:       make(map[string]*VRF),
		sections:   make(map[string]*Section),
	}
}

func pageSlice[T any](items []T, page api.PaginationParams) (window []T, total int) {
	total = len(items)
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

func (r *MemoryRepository) ListSubnets(_ context.Context, orgID string, f SubnetFilter, page api.PaginationParams) ([]Subnet, int, error) {
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
		if (f.ParentID != "" && s.ParentID != f.ParentID) || (f.Root && s.ParentID != "") {
			continue
		}
		out = append(out, r.withChildCount(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

// withChildCount is a copy of s with the number of its children.
func (r *MemoryRepository) withChildCount(s *Subnet) Subnet {
	cp := *s
	cp.ChildCount = 0
	for _, c := range r.subnets {
		if c.OrganizationID == s.OrganizationID && c.ParentID == s.ID {
			cp.ChildCount++
		}
	}
	return cp
}

// placeOrg recomputes the parents of the subnets of the organization
// (IPM-02).
func (r *MemoryRepository) placeOrg(orgID string) {
	var all []*Subnet
	for _, s := range r.subnets {
		if s.OrganizationID == orgID {
			all = append(all, s)
		}
	}
	placeSubnets(all)
}

// overlapPartners are the subnets whose address space s uses twice
// (IPM-02, subnet_overlap_partners of migration 000300): its parent, when
// that is no pool and holds addresses of its own inside s, and its
// children that hold addresses of s, when s is no pool. A true nesting is
// no overlap.
func (r *MemoryRepository) overlapPartners(s *Subnet) []string {
	var out []string
	if p, ok := r.subnets[s.ParentID]; ok && !p.IsPool && r.holdsAddressIn(p.ID, s.CIDR) {
		out = append(out, p.ID)
	}
	if !s.IsPool {
		for _, c := range r.subnets {
			if c.OrganizationID == s.OrganizationID && c.ParentID == s.ID && r.holdsAddressIn(s.ID, c.CIDR) {
				out = append(out, c.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

// holdsAddressIn reports whether subnet subnetID has an address of its own
// inside cidr.
func (r *MemoryRepository) holdsAddressIn(subnetID, cidr string) bool {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	for _, a := range r.addresses {
		if a.SubnetID != subnetID {
			continue
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(a.Address))
		if err != nil {
			if ap, perr := netip.ParsePrefix(strings.TrimSpace(a.Address)); perr == nil {
				addr, err = ap.Addr(), nil
			}
		}
		if err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}

// checkPlacement applies the overlap rule to s after placement: in a
// strict section the first partner is a conflict; otherwise each partner
// becomes a warning of s.
func (r *MemoryRepository) checkPlacement(s *Subnet) error {
	partners := r.overlapPartners(s)
	if sec, ok := r.sections[s.SectionID]; ok && sec.StrictMode && len(partners) > 0 {
		return overlapConflict(partners[0])
	}
	s.Warnings = nil
	for _, id := range partners {
		s.Warnings = append(s.Warnings, SubnetWarning{Code: "overlap", SubnetID: id})
	}
	return nil
}

func (r *MemoryRepository) GetSubnet(_ context.Context, orgID, id string) (*Subnet, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.subnets[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := r.withChildCount(s)
	return &cp, nil
}

func (r *MemoryRepository) CreateSubnet(_ context.Context, s *Subnet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createSubnetLocked(s)
}

// createSubnetLocked is CreateSubnet under a held lock.
func (r *MemoryRepository) createSubnetLocked(s *Subnet) error {
	if s.VRFID == "" {
		s.VRFID = r.defaultVRF(s.OrganizationID).ID
	}
	sectionID, err := r.sectionFor(s.OrganizationID, s.SectionID, s.ClientID)
	if err != nil {
		return err
	}
	s.SectionID = sectionID
	for _, e := range r.subnets {
		if e.OrganizationID == s.OrganizationID && e.VRFID == s.VRFID && e.CIDR == s.CIDR {
			return fmt.Errorf("%w: duplicate cidr", ErrValidation)
		}
	}
	r.seq++
	s.ID = fmt.Sprintf("subnet-%d", r.seq)
	now := time.Now().UTC()
	s.CreatedAt, s.UpdatedAt = now, now
	stored := *s
	r.subnets[s.ID] = &stored
	r.placeOrg(s.OrganizationID)
	if err := r.checkPlacement(&stored); err != nil {
		delete(r.subnets, s.ID)
		r.placeOrg(s.OrganizationID)
		return err
	}
	r.syncGateway(&stored)
	*s = r.withChildCount(&stored)
	stored.Warnings = nil
	return nil
}

// syncGateway keeps the gateway mark of the addresses of subnet s in step
// with its gateway (IPM-04), as the PostgreSQL repository does.
func (r *MemoryRepository) syncGateway(s *Subnet) {
	var marked *IPAddress
	for _, a := range r.addresses {
		if a.OrganizationID != s.OrganizationID || a.SubnetID != s.ID || !a.IsGateway {
			continue
		}
		if s.Gateway != "" && hostOf(a.Address) == s.Gateway {
			marked = a
			continue
		}
		a.IsGateway = false
	}
	if s.Gateway == "" || marked != nil {
		return
	}
	var pick *IPAddress
	for _, a := range r.addresses {
		if a.OrganizationID != s.OrganizationID || a.VRFID != s.VRFID || hostOf(a.Address) != s.Gateway ||
			(a.SubnetID != s.ID && a.SubnetID != "") {
			continue
		}
		if pick == nil || (a.SubnetID != "" && pick.SubnetID == "") ||
			(a.SubnetID == pick.SubnetID && a.InterfaceID == "" && pick.InterfaceID != "") {
			pick = a
		}
	}
	now := time.Now().UTC()
	if pick != nil {
		pick.IsGateway, pick.SubnetID, pick.UpdatedAt = true, s.ID, now
		return
	}
	r.seq++
	a := &IPAddress{ID: fmt.Sprintf("ip-%d", r.seq), OrganizationID: s.OrganizationID, SubnetID: s.ID, Address: s.Gateway,
		VRFID: s.VRFID, Type: IPTypeStatic, State: IPStateUnknown, IsGateway: true, CreatedAt: now, UpdatedAt: now}
	r.addresses[a.ID] = a
}

func (r *MemoryRepository) UpdateSubnet(_ context.Context, orgID, id string, req *UpdateSubnetRequest) (*Subnet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subnets[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	before := *s
	if req.IsPool != nil && *req.IsPool && !s.IsPool {
		for _, a := range r.addresses {
			if a.OrganizationID == orgID && a.SubnetID == id {
				return nil, errPoolWithAddresses
			}
		}
	}
	if req.ClientID != nil || req.SectionID != nil {
		clientID, sectionID := s.ClientID, s.SectionID
		if req.ClientID != nil {
			clientID = *req.ClientID
		}
		if req.SectionID != nil {
			sectionID = *req.SectionID
		}
		resolved, err := r.sectionFor(orgID, sectionID, clientID)
		if err != nil {
			return nil, err
		}
		s.SectionID = resolved
	}
	if req.CIDR != nil {
		for _, a := range r.addresses {
			if a.OrganizationID != orgID || a.SubnetID != id {
				continue
			}
			if in, err := ipInSubnet(hostOf(a.Address), *req.CIDR); err == nil && !in {
				return nil, errAddressesOutsideCIDR
			}
		}
		s.CIDR = *req.CIDR
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
	if req.VRFID != nil {
		s.VRFID = *req.VRFID
	}
	if req.VLANID != nil {
		s.VLANID = *req.VLANID
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
	if req.IsPool != nil {
		s.IsPool = *req.IsPool
	}
	s.UpdatedAt = time.Now().UTC()
	r.placeOrg(orgID)
	if err := r.checkPlacement(s); err != nil {
		*s = before
		r.placeOrg(orgID)
		return nil, err
	}
	if req.Gateway != nil {
		r.syncGateway(s)
	}
	cp := r.withChildCount(s)
	s.Warnings = nil
	return &cp, nil
}

func (r *MemoryRepository) DeleteSubnet(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subnets[id]
	if !ok || s.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.subnets, id)
	// The addresses lose their subnet and with it the gateway mark
	// (ON DELETE SET NULL, trg_ip_address_gateway_leaves_subnet).
	for _, a := range r.addresses {
		if a.OrganizationID == orgID && a.SubnetID == id {
			a.SubnetID, a.IsGateway = "", false
		}
	}
	r.placeOrg(orgID)
	return nil
}

// --- IP Addresses ---

func (r *MemoryRepository) ListIPAddresses(_ context.Context, orgID string, f IPFilter, page api.PaginationParams) ([]IPAddress, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []IPAddress
	for _, a := range r.addresses {
		if a.OrganizationID != orgID {
			continue
		}
		if (f.SubnetID != "" && a.SubnetID != f.SubnetID) || (f.State != "" && a.State != f.State) ||
			(f.IsGateway != nil && a.IsGateway != *f.IsGateway) {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	items, total := pageSlice(out, page)
	return items, total, nil
}

func (r *MemoryRepository) GetIPAddress(_ context.Context, orgID, id string) (*IPAddress, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) CreateIPAddress(_ context.Context, a *IPAddress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a.VRFID = r.defaultVRF(a.OrganizationID).ID
	if sn, ok := r.subnets[a.SubnetID]; ok && sn.VRFID != "" {
		a.VRFID = sn.VRFID
	}
	for _, e := range r.addresses {
		if e.OrganizationID == a.OrganizationID && e.VRFID == a.VRFID && e.InterfaceID == a.InterfaceID && e.Address == a.Address {
			return fmt.Errorf("%w: duplicate address", ErrValidation)
		}
	}
	r.seq++
	a.ID = fmt.Sprintf("ip-%d", r.seq)
	if a.State == "" {
		a.State = IPStateUnknown
	}
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	stored := *a
	r.addresses[a.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateIPAddress(_ context.Context, orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.SubnetID != nil && a.IsGateway && *req.SubnetID != a.SubnetID {
		return nil, errGatewayAddressSubnet
	}
	if req.SubnetID != nil {
		a.SubnetID = *req.SubnetID
	}
	if req.InterfaceID != nil {
		a.InterfaceID = *req.InterfaceID
	}
	if req.Type != nil {
		a.Type = *req.Type
	}
	if req.State != nil && *req.State != a.State {
		changed := time.Now().UTC()
		a.State, a.StateChangedAt = *req.State, &changed
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

func (r *MemoryRepository) DeleteIPAddress(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID {
		return ErrNotFound
	}
	if a.IsGateway {
		return errGatewayAddressDelete
	}
	delete(r.addresses, id)
	return nil
}

// --- Network Interfaces ---

func (r *MemoryRepository) ListInterfacesForCI(_ context.Context, orgID, ciID string, page api.PaginationParams) ([]NetworkInterface, int, error) {
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

func (r *MemoryRepository) GetInterface(_ context.Context, orgID, id string) (*NetworkInterface, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ni, ok := r.interfaces[id]
	if !ok || ni.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *ni
	return &cp, nil
}

func (r *MemoryRepository) CreateInterface(_ context.Context, ni *NetworkInterface) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, other := range r.interfaces {
		if other.CIID == ni.CIID && other.Name == ni.Name {
			return &api.ConflictError{Field: "name", Message: "the value is already taken"}
		}
	}
	r.seq++
	ni.ID = fmt.Sprintf("nic-%d", r.seq)
	now := time.Now().UTC()
	ni.CreatedAt, ni.UpdatedAt = now, now
	stored := *ni
	r.interfaces[ni.ID] = &stored
	return nil
}

func (r *MemoryRepository) UpdateInterface(_ context.Context, orgID, id string, req *UpdateInterfaceRequest) (*NetworkInterface, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ni, ok := r.interfaces[id]
	if !ok || ni.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.Name != nil {
		ni.Name = *req.Name
	}
	if req.IfIndex != nil {
		ni.IfIndex = req.IfIndex
	}
	if req.MAC != nil {
		ni.MAC = *req.MAC
	}
	if req.IfType != nil {
		ni.IfType = *req.IfType
	}
	if req.SpeedMbps != nil {
		ni.SpeedMbps = req.SpeedMbps
	}
	if req.MTU != nil {
		ni.MTU = req.MTU
	}
	if req.VLANID != nil {
		ni.VLANID = *req.VLANID
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

func (r *MemoryRepository) DeleteInterface(_ context.Context, orgID, id string) error {
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

// NetworkProjection is the network of a set of CIs as the topology and the
// impact project it (IMP-11, WP-298): the subnets and VLANs the CIs hang
// off, read from the IPAM tables and never stored. A subnet belongs to a CI
// when an address of the subnet sits on an interface of the CI, a VLAN when
// an interface of the CI is in the VLAN; the VLANs of these subnets are
// listed as well. Only subnets and VLANs the caller sees are listed, and
// only links to them; each link once.
type NetworkProjection struct {
	Subnets []ProjectedSubnet
	VLANs   []ProjectedVLAN
	// CISubnets links a CI to a subnet (in_subnet), CIVLANs a CI to a VLAN
	// (in_vlan); Target is the id of the subnet or the VLAN.
	CISubnets []NetworkLink
	CIVLANs   []NetworkLink
}

// ProjectedSubnet is a subnet of a NetworkProjection. VLANID is its VLAN
// when the caller sees it (the VLAN is then among the VLANs of the
// projection), else empty.
type ProjectedSubnet struct {
	ID       string
	Name     string
	CIDR     string
	VRFID    string
	ClientID string
	SiteID   string
	VLANID   string
}

// ProjectedVLAN is a VLAN of a NetworkProjection; Number is the VLAN
// number (vlan.vlan_id).
type ProjectedVLAN struct {
	ID       string
	Name     string
	Number   int
	ClientID string
	SiteID   string
}

// NetworkLink links a CI to a subnet or a VLAN of a NetworkProjection.
type NetworkLink struct {
	CIID   string
	Target string
}

// sortProjection orders a projection by id, so both repositories answer
// alike.
func sortProjection(p *NetworkProjection) {
	sort.Slice(p.Subnets, func(i, j int) bool { return p.Subnets[i].ID < p.Subnets[j].ID })
	sort.Slice(p.VLANs, func(i, j int) bool { return p.VLANs[i].ID < p.VLANs[j].ID })
	for _, links := range [][]NetworkLink{p.CISubnets, p.CIVLANs} {
		sort.Slice(links, func(i, j int) bool {
			if links[i].CIID != links[j].CIID {
				return links[i].CIID < links[j].CIID
			}
			return links[i].Target < links[j].Target
		})
	}
}

// NetworkProjection reads the network of the CIs ciIDs (IMP-11); the
// in-memory repository knows no client or site scope.
func (r *MemoryRepository) NetworkProjection(_ context.Context, orgID string, ciIDs []string) (NetworkProjection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wanted := make(map[string]bool, len(ciIDs))
	for _, id := range ciIDs {
		wanted[id] = true
	}
	vlanOf := func(id string) (*VLAN, bool) {
		v, ok := r.vlans[id]
		return v, ok && v.OrganizationID == orgID
	}
	subnets, vlans := map[string]bool{}, map[string]bool{}
	ciSubnets, ciVLANs := map[NetworkLink]bool{}, map[NetworkLink]bool{}
	nicCI := map[string]string{}
	for _, ni := range r.interfaces {
		if ni.OrganizationID != orgID || !wanted[ni.CIID] {
			continue
		}
		nicCI[ni.ID] = ni.CIID
		if v, ok := vlanOf(ni.VLANID); ok {
			ciVLANs[NetworkLink{CIID: ni.CIID, Target: v.ID}] = true
			vlans[v.ID] = true
		}
	}
	for _, a := range r.addresses {
		ciID, ok := nicCI[a.InterfaceID]
		if !ok || a.OrganizationID != orgID {
			continue
		}
		if s, found := r.subnets[a.SubnetID]; found && s.OrganizationID == orgID {
			ciSubnets[NetworkLink{CIID: ciID, Target: s.ID}] = true
			subnets[s.ID] = true
		}
	}
	var p NetworkProjection
	for id := range subnets {
		s := r.subnets[id]
		ps := ProjectedSubnet{ID: s.ID, Name: s.Name, CIDR: s.CIDR, VRFID: s.VRFID, ClientID: s.ClientID, SiteID: s.SiteID}
		if v, ok := vlanOf(s.VLANID); ok {
			ps.VLANID = v.ID
			vlans[v.ID] = true
		}
		p.Subnets = append(p.Subnets, ps)
	}
	for id := range vlans {
		v := r.vlans[id]
		p.VLANs = append(p.VLANs, ProjectedVLAN{ID: v.ID, Name: v.Name, Number: v.VLANID, ClientID: v.ClientID, SiteID: v.SiteID})
	}
	for l := range ciSubnets {
		p.CISubnets = append(p.CISubnets, l)
	}
	for l := range ciVLANs {
		p.CIVLANs = append(p.CIVLANs, l)
	}
	sortProjection(&p)
	return p, nil
}
