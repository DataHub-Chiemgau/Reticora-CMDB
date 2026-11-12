// Package maintenance implements maintenance windows with customer
// notification (spec §6.9): a window references the affected CIs; notifying
// derives the affected clients via the CIs' client assignment.
package maintenance

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Window is a scheduled maintenance time window.
type Window struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CIIDs          []string  `json:"ci_ids,omitempty"`
}

// Notification statuses. A notification is sent only once a channel has
// delivered it (MGT-04, NTF-05, NFR-04): the status of a notification of
// the channel notify follows its deliveries (notification_delivery with
// data.maintenance_notification_id, WP-1034): at least one delivered is
// sent, all finally failed is failed, otherwise pending.
const (
	NotificationPending = "pending"
	NotificationSent    = "sent"
	NotificationFailed  = "failed"
)

// Channels of a notification: notify delivers through the notification
// chain (event maintenance.scheduled, WP-1034); webhook rows were queued
// before it and stay readable.
const (
	ChannelNotify  = "notify"
	ChannelWebhook = "webhook"
)

// NotifyRequest is the optional body of POST /maintenance-windows/{id}/notify.
type NotifyRequest struct {
	IncludeHeuristic bool     `json:"include_heuristic"`
	Categories       []string `json:"categories"`
	// ContactRoles selects the contacts of each client (contact.role);
	// empty: every contact of the client.
	ContactRoles []string `json:"contact_roles"`
}

// NotifyPlan is what a notification announces: the affected clients with
// their numbers (RecipientFinder) and the contact roles. A nil plan
// notifies the clients of the window's own CIs.
type NotifyPlan struct {
	Clients      []ClientImpact
	ContactRoles []string
}

// Notification is a per-client customer notification for a window.

type Notification struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	WindowID       string     `json:"maintenance_window_id"`
	ClientID       string     `json:"client_id,omitempty"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// CreateWindowRequest is the payload for creating a maintenance window.
type CreateWindowRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	StartsAt    string   `json:"starts_at"`
	EndsAt      string   `json:"ends_at"`
	CIIDs       []string `json:"ci_ids,omitempty"`
}

// UpdateWindowRequest is the payload for updating a maintenance window.
type UpdateWindowRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

// FilterParams scopes window list queries.
type FilterParams struct {
	Status string
}

// ScheduledData is the data of maintenance.scheduled (MGT-04, WP-1034):
// the frame events.MaintenanceWindowData (id is the window) for one client
// with the title, the notification it delivers and the numbers of affected
// CIs and services; contact_roles selects the client's contacts.
type ScheduledData struct {
	events.MaintenanceWindowData
	Title                     string   `json:"title"`
	MaintenanceNotificationID string   `json:"maintenance_notification_id"`
	AffectedCIs               int      `json:"affected_cis"`
	Services                  int      `json:"services"`
	ContactRoles              []string `json:"contact_roles,omitempty"`
}
