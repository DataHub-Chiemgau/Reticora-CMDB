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
	Name  string `json:"name,omitempty"`
	// VLANID references the VLAN catalog (NET-02, NET-08).
	VLANID       string    `json:"vlan_id,omitempty"`
	Gateway      string    `json:"gateway,omitempty"`
	DNSServers   []string  `json:"dns_servers,omitempty"`
	Description  string    `json:"description,omitempty"`
	IsManagement bool      `json:"is_management"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreateSubnetRequest is the payload for creating a subnet.
type CreateSubnetRequest struct {
	ClientID     string   `json:"client_id,omitempty"`
	SiteID       string   `json:"site_id,omitempty"`
	CIDR         string   `json:"cidr"`
	VRFID        string   `json:"vrf_id,omitempty"`
	Name         string   `json:"name,omitempty"`
	VLANID       string   `json:"vlan_id,omitempty"`
	Gateway      string   `json:"gateway,omitempty"`
	DNSServers   []string `json:"dns_servers,omitempty"`
	Description  string   `json:"description,omitempty"`
	IsManagement bool     `json:"is_management,omitempty"`
}

// UpdateSubnetRequest is the payload for updating a subnet.
type UpdateSubnetRequest struct {
	ClientID *string `json:"client_id,omitempty"`
	SiteID   *string `json:"site_id,omitempty"`
	// VRFID moves the subnet to another VRF ("" = default VRF).
	VRFID *string `json:"vrf_id,omitempty"`
	Name  *string `json:"name,omitempty"`
	// VLANID sets ("" clears) the VLAN of the subnet.
	VLANID       *string  `json:"vlan_id,omitempty"`
	Gateway      *string  `json:"gateway,omitempty"`
	DNSServers   []string `json:"dns_servers,omitempty"`
	Description  *string  `json:"description,omitempty"`
	IsManagement *bool    `json:"is_management,omitempty"`
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
	Type        string    `json:"type"`
	DNSName     string    `json:"dns_name,omitempty"`
	Description string    `json:"description,omitempty"`
	LastSeenAt  *string   `json:"last_seen_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateIPAddressRequest is the payload for creating an IP address.
type CreateIPAddressRequest struct {
	SubnetID    string `json:"subnet_id,omitempty"`
	InterfaceID string `json:"interface_id,omitempty"`
	Address     string `json:"address"`
	Type        string `json:"type,omitempty"`
	DNSName     string `json:"dns_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// UpdateIPAddressRequest is the payload for updating an IP address.
type UpdateIPAddressRequest struct {
	SubnetID    *string `json:"subnet_id,omitempty"`
	InterfaceID *string `json:"interface_id,omitempty"`
	Type        *string `json:"type,omitempty"`
	DNSName     *string `json:"dns_name,omitempty"`
	Description *string `json:"description,omitempty"`
}

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
