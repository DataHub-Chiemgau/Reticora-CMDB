// Package ipam provides REST APIs for subnets, IP addresses, and network interfaces.
package ipam

import "time"

// Subnet represents an IP network range.
type Subnet struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	SiteID         string `json:"site_id,omitempty"`
	CIDR           string `json:"cidr"`
	// VRFID is the VRF of the subnet; the organization's default VRF when
	// none is given (CH10). The CIDR is unique per VRF.
	VRFID string `json:"vrf_id"`
	// SectionID is the IPAM section of the subnet (IPM-01); the
	// organization's default section when none is given.
	SectionID string `json:"section_id"`
	Name      string `json:"name,omitempty"`
	// VLANID references the VLAN catalog (NET-02, NET-08).
	VLANID     string   `json:"vlan_id,omitempty"`
	Gateway    string   `json:"gateway,omitempty"`
	DNSServers []string `json:"dns_servers,omitempty"`
	// NameserverSetID references a nameserver set (IPM-05); empty without
	// one.
	NameserverSetID string `json:"nameserver_set_id,omitempty"`
	// EffectiveDNSServers are the DNS servers that apply to the subnet
	// (read-only, IPM-05): its own dns_servers when set, else the servers
	// of its nameserver set, else none. Only the PostgreSQL repository
	// resolves them.
	EffectiveDNSServers []string `json:"effective_dns_servers,omitempty"`
	Description         string   `json:"description,omitempty"`
	IsManagement        bool     `json:"is_management"`
	// ParentID is the narrowest subnet of the VRF that contains this one
	// (IPM-02), computed on write and never written by the API; empty for
	// a root and for a parent the caller cannot see (Konvention 11).
	ParentID string `json:"parent_id,omitempty"`
	// IsPool marks a container without host addresses of its own.
	IsPool bool `json:"is_pool"`
	// ChildCount counts the children the caller can see.
	ChildCount int `json:"child_count"`
	// Warnings are set by create and update only: overlaps (a parent that
	// is no pool holds addresses of its own inside its child) in a section
	// without strict_mode (IPM-02).
	Warnings  []SubnetWarning `json:"warnings,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// SubnetWarning is a warning of a subnet write: code overlap names the
// subnet whose address space it shares, when the caller sees it.
type SubnetWarning struct {
	Code     string `json:"code"`
	SubnetID string `json:"subnet_id,omitempty"`
}

// CreateSubnetRequest is the payload for creating a subnet.
type CreateSubnetRequest struct {
	ClientID   string   `json:"client_id,omitempty"`
	SiteID     string   `json:"site_id,omitempty"`
	CIDR       string   `json:"cidr"`
	VRFID      string   `json:"vrf_id,omitempty"`
	SectionID  string   `json:"section_id,omitempty"`
	Name       string   `json:"name,omitempty"`
	VLANID     string   `json:"vlan_id,omitempty"`
	Gateway    string   `json:"gateway,omitempty"`
	DNSServers []string `json:"dns_servers,omitempty"`
	// NameserverSetID names a nameserver set of the organization (IPM-05);
	// an unknown one is 422 nameserver_set_id.
	NameserverSetID string `json:"nameserver_set_id,omitempty"`
	Description     string `json:"description,omitempty"`
	IsManagement    bool   `json:"is_management,omitempty"`
	IsPool          bool   `json:"is_pool,omitempty"`
}

// UpdateSubnetRequest is the payload for updating a subnet.
type UpdateSubnetRequest struct {
	ClientID *string `json:"client_id,omitempty"`
	SiteID   *string `json:"site_id,omitempty"`
	// CIDR changes the prefix of the subnet; its addresses must stay inside
	// (422 cidr), and the gateway is checked against the new prefix (422
	// gateway, IPM-04).
	CIDR *string `json:"cidr,omitempty"`
	// VRFID moves the subnet to another VRF ("" = default VRF).
	VRFID *string `json:"vrf_id,omitempty"`
	// SectionID moves the subnet to another IPAM section ("" = default
	// section, IPM-01).
	SectionID *string `json:"section_id,omitempty"`
	Name      *string `json:"name,omitempty"`
	// VLANID sets ("" clears) the VLAN of the subnet.
	VLANID     *string  `json:"vlan_id,omitempty"`
	Gateway    *string  `json:"gateway,omitempty"`
	DNSServers []string `json:"dns_servers,omitempty"`
	// NameserverSetID sets ("" clears) the nameserver set (IPM-05).
	NameserverSetID *string `json:"nameserver_set_id,omitempty"`
	Description     *string `json:"description,omitempty"`
	IsManagement    *bool   `json:"is_management,omitempty"`
	// IsPool makes the subnet a container; a subnet with addresses of its
	// own cannot become one (409 is_pool).
	IsPool *bool `json:"is_pool,omitempty"`
}

// IPAddress represents a single IP address assignment.
type IPAddress struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	SubnetID       string `json:"subnet_id,omitempty"`
	InterfaceID    string `json:"interface_id,omitempty"`
	Address        string `json:"address"`
	// VRFID is the VRF of the subnet (read-only); addresses are unique per
	// VRF and interface (NET-03).
	VRFID string `json:"vrf_id"`
	// Type is discovered, reserved, static or dhcp (NET-03).
	Type        string  `json:"type"`
	DNSName     string  `json:"dns_name,omitempty"`
	Description string  `json:"description,omitempty"`
	LastSeenAt  *string `json:"last_seen_at,omitempty"`
	// ReservedUntil is the informative end of a reservation (NET-09); no
	// automatic release follows it. ReservedBy is the surrogate id of the
	// reserving user (convention 16). Both are set only by POST
	// /subnets/{id}/reservations, never by POST or PATCH /ips.
	ReservedUntil *time.Time `json:"reserved_until,omitempty"`
	ReservedBy    string     `json:"reserved_by,omitempty"`
	// State is the reachability of the address, active, offline or unknown
	// (IPM-04, E-211), apart from Type; StateChangedAt is the time of the
	// last change. IsGateway marks the gateway of the subnet; only the
	// gateway of the subnet sets it (read-only here).
	State          string     `json:"state"`
	StateChangedAt *time.Time `json:"state_changed_at,omitempty"`
	IsGateway      bool       `json:"is_gateway"`
	// CIID is the device of the address (IPM-13, E-215): with an interface
	// always the CI of the interface, without one (VIP, anycast, a
	// management address) set directly. Role is primary, secondary, vip,
	// anycast, loopback or management.
	CIID      string    `json:"ci_id,omitempty"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CIAddress is an address of a CI in GET /api/v1/cis/{id}/addresses
// (IPM-13): the address with the name of its interface and the CIDR of
// its subnet.
type CIAddress struct {
	IPAddress
	InterfaceName string `json:"interface_name,omitempty"`
	SubnetCIDR    string `json:"subnet_cidr,omitempty"`
}

// CreateIPAddressRequest is the payload for creating an IP address.
type CreateIPAddressRequest struct {
	SubnetID    string `json:"subnet_id,omitempty"`
	InterfaceID string `json:"interface_id,omitempty"`
	Address     string `json:"address"`
	Type        string `json:"type,omitempty"`
	DNSName     string `json:"dns_name,omitempty"`
	Description string `json:"description,omitempty"`
	// CIID binds the address to a device (IPM-13); with an interface it
	// must be the CI of the interface (422 ci_id), empty takes it.
	CIID string `json:"ci_id,omitempty"`
	// Role of the address, default primary (IPM-13).
	Role string `json:"role,omitempty"`
}

// UpdateIPAddressRequest is the payload for updating an IP address.
type UpdateIPAddressRequest struct {
	SubnetID    *string `json:"subnet_id,omitempty"`
	InterfaceID *string `json:"interface_id,omitempty"`
	Type        *string `json:"type,omitempty"`
	DNSName     *string `json:"dns_name,omitempty"`
	Description *string `json:"description,omitempty"`
	// State sets the state of the address manually (IPM-04).
	State *string `json:"state,omitempty"`
	// CIID sets ("" clears) the device of the address (IPM-13); a new
	// interface takes its CI along.
	CIID *string `json:"ci_id,omitempty"`
	// Role changes the role of the address (IPM-13).
	Role *string `json:"role,omitempty"`
}

// Roles of an address (IPM-13, E-215).
const (
	IPRolePrimary    = "primary"
	IPRoleSecondary  = "secondary"
	IPRoleVIP        = "vip"
	IPRoleAnycast    = "anycast"
	IPRoleLoopback   = "loopback"
	IPRoleManagement = "management"
)

// ValidIPRoles lists the roles of an address (ip_address_role_check).
var ValidIPRoles = map[string]bool{IPRolePrimary: true, IPRoleSecondary: true, IPRoleVIP: true, IPRoleAnycast: true,
	IPRoleLoopback: true, IPRoleManagement: true}

// IPFilter holds the optional filters of the address lists: the subnet,
// the state and the gateway mark (IPM-04); type, client, VRF, CI (ci_id
// of the address, IPM-13) and q (prefix of the host or part of the DNS
// name) (IPM-20, filter.go).
type IPFilter struct {
	SubnetID  string
	State     string
	IsGateway *bool
	Type      string
	ClientID  string
	VRFID     string
	CIID      string
	Q         string
}

// Address states of IPM-04 (E-211).
const (
	IPStateActive  = "active"
	IPStateOffline = "offline"
	IPStateUnknown = "unknown"
)

// ValidIPStates lists the address states (IPM-04).
var ValidIPStates = map[string]bool{IPStateActive: true, IPStateOffline: true, IPStateUnknown: true}

// NetworkInterface represents a CI's network interface (NET-01): the
// name is unique per CI.
type NetworkInterface struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	CIID           string `json:"ci_id"`
	Name           string `json:"name"`
	IfIndex        *int   `json:"if_index,omitempty"`
	MAC            string `json:"mac,omitempty"`
	IfType         string `json:"if_type"`
	SpeedMbps      *int   `json:"speed_mbps,omitempty"`
	MTU            *int   `json:"mtu,omitempty"`
	// VLANID references the VLAN catalog (NET-01).
	VLANID       string `json:"vlan_id,omitempty"`
	IsManagement bool   `json:"is_management"`
	IsUplink     bool   `json:"is_uplink"`
	AdminStatus  string `json:"admin_status"`
	OperStatus   string `json:"oper_status"`
	Description  string `json:"description,omitempty"`
	// MissCount counts the discovery runs that no longer saw the
	// interface (REC-09); read-only. Only discovered interfaces count
	// (E-90).
	MissCount int       `json:"miss_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateInterfaceRequest is the payload for creating a network interface.
type CreateInterfaceRequest struct {
	Name         string `json:"name"`
	IfIndex      *int   `json:"if_index,omitempty"`
	MAC          string `json:"mac,omitempty"`
	IfType       string `json:"if_type,omitempty"`
	SpeedMbps    *int   `json:"speed_mbps,omitempty"`
	MTU          *int   `json:"mtu,omitempty"`
	VLANID       string `json:"vlan_id,omitempty"`
	IsManagement bool   `json:"is_management,omitempty"`
	IsUplink     bool   `json:"is_uplink,omitempty"`
	AdminStatus  string `json:"admin_status,omitempty"`
	OperStatus   string `json:"oper_status,omitempty"`
	Description  string `json:"description,omitempty"`
}

// UpdateInterfaceRequest is the payload for updating a network interface.
type UpdateInterfaceRequest struct {
	Name         *string `json:"name,omitempty"`
	IfIndex      *int    `json:"if_index,omitempty"`
	MAC          *string `json:"mac,omitempty"`
	IfType       *string `json:"if_type,omitempty"`
	SpeedMbps    *int    `json:"speed_mbps,omitempty"`
	MTU          *int    `json:"mtu,omitempty"`
	VLANID       *string `json:"vlan_id,omitempty"`
	IsManagement *bool   `json:"is_management,omitempty"`
	IsUplink     *bool   `json:"is_uplink,omitempty"`
	AdminStatus  *string `json:"admin_status,omitempty"`
	OperStatus   *string `json:"oper_status,omitempty"`
	Description  *string `json:"description,omitempty"`
}

// ValidInterfaceTypes lists allowed network interface types (if_type).
var ValidInterfaceTypes = map[string]bool{
	"ethernet": true, "fiber": true, "wifi": true, "virtual": true,
	"loopback": true, "serial": true, "management": true,
}

// IP address types of NET-03.
const (
	IPTypeDiscovered = "discovered"
	IPTypeReserved   = "reserved"
	IPTypeStatic     = "static"
	IPTypeDHCP       = "dhcp"
)

// Origins of a network interface (network_interface.origin, E-90): the
// discovery sync makes every interface it reports discovered; interfaces
// created otherwise, and those that existed before WP-580, are manual. Only
// discovered interfaces and addresses of type discovered count the misses
// of REC-07 and are removed by them.
const (
	InterfaceOriginDiscovered = "discovered"
	InterfaceOriginManual     = "manual"
)

// ValidIPTypes lists the IP address types (NET-03).
var ValidIPTypes = map[string]bool{
	IPTypeDiscovered: true, IPTypeReserved: true, IPTypeStatic: true, IPTypeDHCP: true,
}
