// Package tenantapi provides REST APIs for the organization location hierarchy:
// clients, sites, buildings, and rooms.
package tenantapi

import "time"

// Organization is the tenant itself (LOC-01): name, unique slug, plan,
// settings, IANA time zone, ISO 4217 currency and BCP 47 locale.
type Organization struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	Plan      string         `json:"plan"`
	Settings  map[string]any `json:"settings"`
	Timezone  string         `json:"timezone"`
	Currency  string         `json:"currency"`
	Locale    string         `json:"locale"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// UpdateOrganizationRequest is the payload of PATCH /api/v1/org. Slug and
// plan are not changed here: the slug identifies the tenant, the plan is
// set by the operator (ENT-04).
type UpdateOrganizationRequest struct {
	Name     *string        `json:"name,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
	Timezone *string        `json:"timezone,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Locale   *string        `json:"locale,omitempty"`
}

// Client represents a tenant's customer (mandant sub-scope, LOC-02): the
// name is unique per organization; external_ref links the client to an
// external system (CRM, PSA); the time zone is optional.
type Client struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Name           string         `json:"name"`
	Slug           string         `json:"slug"`
	ExternalRef    *string        `json:"external_ref"`
	Timezone       *string        `json:"timezone"`
	Settings       map[string]any `json:"settings"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreateClientRequest is the payload for creating a client.
type CreateClientRequest struct {
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	ExternalRef *string        `json:"external_ref,omitempty"`
	Timezone    *string        `json:"timezone,omitempty"`
	Settings    map[string]any `json:"settings,omitempty"`
	// Code is a human-facing short identifier accepted as a slug alias so
	// integrations that send {name, code} instead of {name, slug} keep working.
	Code string `json:"code,omitempty"`
}

// UpdateClientRequest is the payload for updating a client. An empty
// external_ref or timezone clears the value.
type UpdateClientRequest struct {
	Name        *string        `json:"name,omitempty"`
	Slug        *string        `json:"slug,omitempty"`
	ExternalRef *string        `json:"external_ref,omitempty"`
	Timezone    *string        `json:"timezone,omitempty"`
	Settings    map[string]any `json:"settings,omitempty"`
}

// Site represents a physical location belonging to a client.
type Site struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id"`
	Name           string    `json:"name"`
	Address        string    `json:"address,omitempty"`
	GeoLat         *float64  `json:"geo_lat,omitempty"`
	GeoLon         *float64  `json:"geo_lon,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	Timezone       *string   `json:"timezone"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateSiteRequest is the payload for creating a site. The name is unique
// per (organization, client) (LOC-03).
type CreateSiteRequest struct {
	ClientID string   `json:"client_id"`
	Name     string   `json:"name"`
	Address  string   `json:"address,omitempty"`
	GeoLat   *float64 `json:"geo_lat,omitempty"`
	GeoLon   *float64 `json:"geo_lon,omitempty"`
	Notes    string   `json:"notes,omitempty"`
	Timezone *string  `json:"timezone,omitempty"`
}

// UpdateSiteRequest is the payload for updating a site. An empty timezone
// clears it.
type UpdateSiteRequest struct {
	Name     *string  `json:"name,omitempty"`
	Address  *string  `json:"address,omitempty"`
	GeoLat   *float64 `json:"geo_lat,omitempty"`
	GeoLon   *float64 `json:"geo_lon,omitempty"`
	Notes    *string  `json:"notes,omitempty"`
	Timezone *string  `json:"timezone,omitempty"`
}

// Building represents a structure at a site.
type Building struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id"`
	SiteID             string    `json:"site_id"`
	Name               string    `json:"name"`
	Floors             *int      `json:"floors,omitempty"`
	FloorplanObjectKey string    `json:"floorplan_object_key,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// CreateBuildingRequest is the payload for creating a building.
type CreateBuildingRequest struct {
	SiteID             string `json:"site_id"`
	Name               string `json:"name"`
	Floors             *int   `json:"floors,omitempty"`
	FloorplanObjectKey string `json:"floorplan_object_key,omitempty"`
}

// UpdateBuildingRequest is the payload for updating a building.
type UpdateBuildingRequest struct {
	Name               *string `json:"name,omitempty"`
	Floors             *int    `json:"floors,omitempty"`
	FloorplanObjectKey *string `json:"floorplan_object_key,omitempty"`
}

// Room represents a room within a building.
type Room struct {
	ID             string                    `json:"id"`
	OrganizationID string                    `json:"organization_id"`
	BuildingID     string                    `json:"building_id"`
	Name           string                    `json:"name"`
	Floor          *int                      `json:"floor,omitempty"`
	RoomType       string                    `json:"room_type,omitempty"`
	Layout         map[string]LayoutPosition `json:"layout,omitempty"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
}

// CreateRoomRequest is the payload for creating a room.
type CreateRoomRequest struct {
	BuildingID string `json:"building_id"`
	Name       string `json:"name"`
	Floor      *int   `json:"floor,omitempty"`
	RoomType   string `json:"room_type,omitempty"`
}

// UpdateRoomRequest is the payload for updating a room.
type UpdateRoomRequest struct {
	Name     *string `json:"name,omitempty"`
	Floor    *int    `json:"floor,omitempty"`
	RoomType *string `json:"room_type,omitempty"`
	// Layout replaces the room's floor-plan object positions
	// (object_id → {x, y} normalized 0..1). Nil leaves the layout untouched;
	// an empty object clears it.
	Layout map[string]LayoutPosition `json:"layout,omitempty"`
}

// LayoutPosition is one object's normalized position on the room floor plan.
type LayoutPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
