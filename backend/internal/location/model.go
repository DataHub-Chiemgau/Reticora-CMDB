// Package location implements GPS(Advanced): asset location history (spec §6.9).
package location

import "time"

// Entry is one recorded asset position.
type Entry struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	AssetID        string    `json:"asset_id"`
	Lat            float64   `json:"lat"`
	Lon            float64   `json:"lon"`
	AccuracyM      *float64  `json:"accuracy_m,omitempty"`
	Source         string    `json:"source"` // scan | agent | manual
	RecordedAt     time.Time `json:"recorded_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateEntryRequest records an asset position.
type CreateEntryRequest struct {
	AssetID    string   `json:"asset_id"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	AccuracyM  *float64 `json:"accuracy_m,omitempty"`
	Source     string   `json:"source,omitempty"`
	RecordedAt string   `json:"recorded_at,omitempty"`
}
