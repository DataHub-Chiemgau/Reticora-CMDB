package admp

import "time"

// The read-only cache of ADMP (ADM-03, ADM-04, ADM-07; WP-928, E-222):
// connections, services and their assignment to device ports, each with
// fetched_at, expires_at and stale. Fields the ADMP documentation defines
// later (A-55) live in Properties; personal values are never stored in
// plain text: the holder and the address of a private connection are
// pii_subject surrogates (E-243), a business address is AddressText.

// Port matching (admp_service_port_matched_by_check).
const (
	MatchedByRule   = "rule"
	MatchedByManual = "manual"
	MatchedByNone   = "none"
)

// Object kinds of the cache (MarkStale, the refresh of WP-929).
const (
	KindConnection = "connection"
	KindService    = "service"
)

// Connection is a connection (line) of ADMP: carrier, contract and address
// (ADM-07). HolderSubjectID and AddressSubjectID are pii_subject
// surrogates; at most one of AddressSubjectID and AddressText is set.
type Connection struct {
	ID               string         `json:"id"`
	OrganizationID   string         `json:"organization_id"`
	AccountID        string         `json:"account_id"`
	ExternalID       string         `json:"external_id"`
	Carrier          string         `json:"carrier,omitempty"`
	ContractID       string         `json:"contract_id,omitempty"`
	AddressSubjectID string         `json:"address_subject_id,omitempty"`
	AddressText      string         `json:"address_text,omitempty"`
	HolderSubjectID  string         `json:"holder_subject_id,omitempty"`
	Properties       map[string]any `json:"properties"`
	FetchedAt        time.Time      `json:"fetched_at"`
	ExpiresAt        time.Time      `json:"expires_at"`
	Stale            bool           `json:"stale"`
	LastErrorClass   string         `json:"last_error_class,omitempty"`
	ClientID         string         `json:"client_id,omitempty"`
	// PrivateAddress is the address of a private connection in plain text
	// on its way into the store (WP-1075): UpsertConnection keeps it only
	// as the surrogate AddressSubjectID (the unchanged one, else a new
	// value subject; without a vault none) and clears it. It is never
	// read back, marshalled or logged.
	PrivateAddress string `json:"-"`
}

// Service is a service of ADMP on a connection.
type Service struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	AccountID      string         `json:"account_id"`
	ExternalID     string         `json:"external_id"`
	ConnectionID   string         `json:"connection_id,omitempty"`
	Kind           string         `json:"kind"`
	Properties     map[string]any `json:"properties"`
	FetchedAt      time.Time      `json:"fetched_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	Stale          bool           `json:"stale"`
	LastErrorClass string         `json:"last_error_class,omitempty"`
	ClientID       string         `json:"client_id,omitempty"`
	// Ports are the ports of the service (ListByInterface, ListByCI).
	Ports []ServicePort `json:"ports,omitempty"`
}

// ServicePort assigns a service to a port of a device (ADM-04): port_key as
// ADMP names it, the interface and the CI it was matched to. Client and
// site are those of the CI.
type ServicePort struct {
	ID                 string `json:"id"`
	OrganizationID     string `json:"organization_id"`
	ServiceID          string `json:"service_id"`
	PortKey            string `json:"port_key"`
	NetworkInterfaceID string `json:"network_interface_id,omitempty"`
	CIID               string `json:"ci_id,omitempty"`
	MatchedBy          string `json:"matched_by"`
	ClientID           string `json:"client_id,omitempty"`
	SiteID             string `json:"site_id,omitempty"`
}

// Object kinds of the port inventory (WP-1073): the inventory of an
// account and one port with its services.
const (
	KindInventory = "inventory"
	KindPort      = "port"
)

// AccessPort is a port of the ADMP inventory of an account (E-251):
// ExternalID is AccessPort.id, the port_key of admp_service_port; node,
// unit and port index locate it; the match names the interface and CI.
// FetchedAt is the time of the inventory, ExpiresAt when the services of
// the port are due again.
type AccessPort struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id"`
	AccountID          string    `json:"account_id"`
	ExternalID         string    `json:"external_id"`
	NodeExternalID     string    `json:"node_external_id"`
	NodeName           string    `json:"node_name"`
	UnitIndex          int64     `json:"unit_index"`
	PortIndex          int64     `json:"port_index"`
	Protocol           string    `json:"protocol,omitempty"`
	NetworkInterfaceID string    `json:"network_interface_id,omitempty"`
	CIID               string    `json:"ci_id,omitempty"`
	MatchedBy          string    `json:"matched_by"`
	ClientID           string    `json:"client_id,omitempty"`
	SiteID             string    `json:"site_id,omitempty"`
	FetchedAt          time.Time `json:"fetched_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// Expired counts the rows DeleteExpired removed.
type Expired struct {
	Connections int
	Services    int
}
