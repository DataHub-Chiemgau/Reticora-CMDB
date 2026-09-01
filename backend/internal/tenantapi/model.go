// Package tenantapi provides REST APIs for the organization location hierarchy:
// clients, sites, buildings, and rooms.
package tenantapi

import "time"

// Client represents a tenant's customer (mandant sub-scope).
type Client struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Name           string         `json:"name"`
	Slug           string         `json:"slug"`
	Settings       map[string]any `json:"settings"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreateClientRequest is the payload for creating a client.
type CreateClientRequest struct {
	Name     string         `json:"name"`
	Slug     string         `json:"slug"`
	Settings map[string]any `json:"settings,omitempty"`
	// Code is a human-facing short identifier accepted as a slug alias so
	// integrations that send {name, code} instead of {name, slug} keep working.
	Code string `json:"code,omitempty"`
}

// UpdateClientRequest is the payload for updating a client.
type UpdateClientRequest struct {
	Name     *string        `json:"name,omitempty"`
	Slug     *string        `json:"slug,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
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
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateSiteRequest is the payload for creating a site.
type CreateSiteRequest struct {
	ClientID string   `json:"client_id"`
	Name     string   `json:"name"`
	Address  string   `json:"address,omitempty"`
	GeoLat   *float64 `json:"geo_lat,omitempty"`
	GeoLon   *float64 `json:"geo_lon,omitempty"`
	Notes    string   `json:"notes,omitempty"`
}

// UpdateSiteRequest is the payload for updating a site.
type UpdateSiteRequest struct {
	Name    *string  `json:"name,omitempty"`
	Address *string  `json:"address,omitempty"`
	GeoLat  *float64 `json:"geo_lat,omitempty"`
	GeoLon  *float64 `json:"geo_lon,omitempty"`
	Notes   *string  `json:"notes,omitempty"`
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
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	BuildingID     string    `json:"building_id"`
	Name           string    `json:"name"`
	Floor          *int      `json:"floor,omitempty"`
	RoomType       string    `json:"room_type,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
}
