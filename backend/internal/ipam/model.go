// Package ipam provides REST APIs for subnets, IP addresses, and network interfaces.
package ipam

import "time"

// Subnet represents an IP network range.
type Subnet struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	CIDR           string    `json:"cidr"`
	Name           string    `json:"name,omitempty"`
	VLANID         *int      `json:"vlan_id,omitempty"`
	Gateway        string    `json:"gateway,omitempty"`
	DNSServers     []string  `json:"dns_servers,omitempty"`
	Description    string    `json:"description,omitempty"`
	IsManagement   bool      `json:"is_management"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateSubnetRequest is the payload for creating a subnet.
type CreateSubnetRequest struct {
	ClientID     string   `json:"client_id,omitempty"`
	SiteID       string   `json:"site_id,omitempty"`
	CIDR         string   `json:"cidr"`
	Name         string   `json:"name,omitempty"`
	VLANID       *int     `json:"vlan_id,omitempty"`
	Gateway      string   `json:"gateway,omitempty"`
	DNSServers   []string `json:"dns_servers,omitempty"`
	Description  string   `json:"description,omitempty"`
	IsManagement bool     `json:"is_management,omitempty"`
}

// UpdateSubnetRequest is the payload for updating a subnet.
type UpdateSubnetRequest struct {
	ClientID     *string  `json:"client_id,omitempty"`
	SiteID       *string  `json:"site_id,omitempty"`
	Name         *string  `json:"name,omitempty"`
	VLANID       *int     `json:"vlan_id,omitempty"`
	Gateway      *string  `json:"gateway,omitempty"`
	DNSServers   []string `json:"dns_servers,omitempty"`
	Description  *string  `json:"description,omitempty"`
	IsManagement *bool    `json:"is_management,omitempty"`
}

// IPAddress represents a single IP address assignment.
type IPAddress struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	SubnetID       string    `json:"subnet_id,omitempty"`
	InterfaceID    string    `json:"interface_id,omitempty"`
	Address        string    `json:"address"`
	Status         string    `json:"status"`
	DNSName        string    `json:"dns_name,omitempty"`
	Description    string    `json:"description,omitempty"`
	LastSeenAt     *string   `json:"last_seen_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateIPAddressRequest is the payload for creating an IP address.
type CreateIPAddressRequest struct {
	SubnetID    string `json:"subnet_id,omitempty"`
	InterfaceID string `json:"interface_id,omitempty"`
	Address     string `json:"address"`
	Status      string `json:"status,omitempty"`
	DNSName     string `json:"dns_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// UpdateIPAddressRequest is the payload for updating an IP address.
type UpdateIPAddressRequest struct {
	SubnetID    *string `json:"subnet_id,omitempty"`
	InterfaceID *string `json:"interface_id,omitempty"`
	Status      *string `json:"status,omitempty"`
	DNSName     *string `json:"dns_name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// NetworkInterface represents a CI's network interface.
type NetworkInterface struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	CIID           string    `json:"ci_id"`
	Name           string    `json:"name"`
	MACAddress     string    `json:"mac_address,omitempty"`
	InterfaceType  string    `json:"interface_type"`
	SpeedMbps      *int      `json:"speed_mbps,omitempty"`
	IsManagement   bool      `json:"is_management"`
	IsUplink       bool      `json:"is_uplink"`
	AdminStatus    string    `json:"admin_status"`
	OperStatus     string    `json:"oper_status"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateInterfaceRequest is the payload for creating a network interface.
type CreateInterfaceRequest struct {
	Name          string `json:"name"`
	MACAddress    string `json:"mac_address,omitempty"`
	InterfaceType string `json:"interface_type,omitempty"`
	SpeedMbps     *int   `json:"speed_mbps,omitempty"`
	IsManagement  bool   `json:"is_management,omitempty"`
	IsUplink      bool   `json:"is_uplink,omitempty"`
	AdminStatus   string `json:"admin_status,omitempty"`
	OperStatus    string `json:"oper_status,omitempty"`
	Description   string `json:"description,omitempty"`
}

// UpdateInterfaceRequest is the payload for updating a network interface.
type UpdateInterfaceRequest struct {
	Name          *string `json:"name,omitempty"`
	MACAddress    *string `json:"mac_address,omitempty"`
	InterfaceType *string `json:"interface_type,omitempty"`
	SpeedMbps     *int    `json:"speed_mbps,omitempty"`
	IsManagement  *bool   `json:"is_management,omitempty"`
	IsUplink      *bool   `json:"is_uplink,omitempty"`
	AdminStatus   *string `json:"admin_status,omitempty"`
	OperStatus    *string `json:"oper_status,omitempty"`
	Description   *string `json:"description,omitempty"`
}

// ValidInterfaceTypes lists allowed network interface types.
var ValidInterfaceTypes = map[string]bool{
	"ethernet": true, "fiber": true, "wifi": true, "virtual": true,
	"loopback": true, "serial": true, "management": true,
}

// ValidIPStatuses lists allowed IP address statuses.
var ValidIPStatuses = map[string]bool{
	"active": true, "reserved": true, "deprecated": true, "dhcp": true, "available": true,
}
