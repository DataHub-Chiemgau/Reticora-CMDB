package events

import "strings"

// Event types of EVT-02, and those NTF-05 notifies of besides.
const (
	CICreated           = "ci.created"
	CIChanged           = "ci.changed"
	CIDeleted           = "ci.deleted"
	CIRestored          = "ci.restored"
	CIMerged            = "ci.merged"
	CIOffline           = "ci.offline"
	CIOnline            = "ci.online"
	RelationshipCreated = "relationship.created"
	RelationshipRemoved = "relationship.removed"
	CollectorEnrolled   = "collector.enrolled"
	CollectorOffline    = "collector.offline"
	ReviewCreated       = "review.created"
	ReviewResolved      = "review.resolved"
	ExportCompleted     = "export.completed"
	JobFailed           = "job.failed"
	EntitlementWarning  = "entitlement.warning"
	// Further notification events of NTF-05 (WP-140).
	EntitlementExpired  = "entitlement.expired"
	InvitationCreated   = "invitation.created"
	WebhookDead         = "webhook.dead"
	CertificateExpiring = "certificate.expiring"
	// APIKeyCreatorDeactivated: the user who created service-account keys
	// was deactivated or anonymized; the keys stay valid and the org_admins
	// are asked to rotate them (TLC-04, decision E-64 option 2).
	APIKeyCreatorDeactivated = "apikey.creator_deactivated"
)

// Event types of phase 2 (EVT-03 [P2], the NTF-05 [P2] types and the plan
// additions; WP-246). The producing work packages write them (docs/README.md,
// "Event contract"); Payloads holds the frame of their data.
const (
	CIStatusChanged       = "ci.status_changed"
	LifecycleChanged      = "lifecycle.changed"
	RelationshipChanged   = "relationship.changed"
	AssetCreated          = "asset.created"
	AssetChanged          = "asset.changed"
	AssetDeleted          = "asset.deleted"
	AssetAssigned         = "asset.assigned"
	AssetReturned         = "asset.returned"
	AssetWarrantyExpiring = "asset.warranty_expiring"
	ContractExpiring      = "contract.expiring"
	StockMovement         = "stock.movement"
	StockBelowMinimum     = "stock.below_minimum"
	ReservationCreated    = "reservation.created"
	ReservationReleased   = "reservation.released"
	ReservationExpired    = "reservation.expired"
	ReservationConverted  = "reservation.converted"
	TicketUpdated         = "ticket.updated"
	TicketSLABreached     = "ticket.sla_breached"
	MetamodelPublished    = "metamodel.published"
	DocumentScanned       = "document.scanned"
	ImportCompleted       = "import.completed"
)

// Types lists the event types, in the order of the type enum of
// api/events.schema.json. New types are added here and in the schema only.
var Types = []string{
	CICreated, CIChanged, CIDeleted, CIRestored, CIMerged, CIOffline, CIOnline,
	RelationshipCreated, RelationshipRemoved, CollectorEnrolled, CollectorOffline,
	ReviewCreated, ReviewResolved, ExportCompleted, JobFailed, EntitlementWarning,
	EntitlementExpired, InvitationCreated, WebhookDead, CertificateExpiring,
	APIKeyCreatorDeactivated,
	CIStatusChanged, LifecycleChanged, RelationshipChanged,
	AssetCreated, AssetChanged, AssetDeleted, AssetAssigned, AssetReturned, AssetWarrantyExpiring,
	ContractExpiring, StockMovement, StockBelowMinimum,
	ReservationCreated, ReservationReleased, ReservationExpired, ReservationConverted,
	TicketUpdated, TicketSLABreached, MetamodelPublished, DocumentScanned, ImportCompleted,
}

// Reserved are the event types of phase 3 (EVT-03 [P3]); a trailing ".*"
// reserves every type of the prefix. They are no event types yet: IsType,
// and with it Outbox.Append and AppendMany, refuses them.
var Reserved = []string{"maintenance.scheduled", "workflow.*", "finding.*"}

// IsType reports whether t is a registered event type.
func IsType(t string) bool {
	if IsReserved(t) {
		return false
	}
	for _, known := range Types {
		if known == t {
			return true
		}
	}
	return false
}

// IsReserved reports whether t is a reserved type of phase 3.
func IsReserved(t string) bool {
	for _, r := range Reserved {
		if prefix, ok := strings.CutSuffix(r, "*"); ok {
			if strings.HasPrefix(t, prefix) {
				return true
			}
		} else if r == t {
			return true
		}
	}
	return false
}

// PIIRef is a personal value in event data, a surrogate reference
// {"$pii": "<id>"} (SEC-05, WP-241; field names after pii.IsPersonalKey).
type PIIRef struct {
	Ref string `json:"$pii"`
}

// The payload frames of the phase 2 types (the data of the envelope, the
// $defs of api/events.schema.json). They name the object (id), the client
// and site of objects with a client or site reference at the top level,
// where webhook scope resolution reads them, and the core fields of the
// type. Further fields are allowed (additionalProperties); a producing
// work package adds its fields to the struct and the $def of its type.

// CIStatusChangedData is the data of ci.status_changed: the CI snapshot of
// ci.changed with changed_fields and the old and new status (WP-707).
type CIStatusChangedData struct {
	ID            string   `json:"id"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	ChangedFields []string `json:"changed_fields"`
	From          string   `json:"from"`
	To            string   `json:"to"`
}

// LifecycleChangedData is the data of lifecycle.changed, the result of a
// lifecycle transition (lifecycle.TransitionResult; WP-724): entity_type
// ci or asset names the object kind of entity_id.
type LifecycleChangedData struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	ClientID   string `json:"client_id,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to"`
	// The fields of the transition service (WP-724): the lifecycle model
	// and the reason of the transition.
	ModelID  string `json:"model_id,omitempty"`
	ModelKey string `json:"model_key,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// RelationshipChangedData is the data of relationship.changed (WP-296).
type RelationshipChangedData struct {
	ID         string `json:"id"`
	SourceCIID string `json:"source_ci_id"`
	TargetCIID string `json:"target_ci_id"`
}

// AssetData is the data of asset.created, asset.changed and asset.deleted
// (WP-740).
type AssetData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
}

// AssetAssignmentData is the data of asset.assigned and asset.returned
// (WP-275).
type AssetAssignmentData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
}

// AssetWarrantyData is the data of asset.warranty_expiring (WP-270).
type AssetWarrantyData struct {
	ID        string `json:"id"`
	ClientID  string `json:"client_id,omitempty"`
	SiteID    string `json:"site_id,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// ContractExpiringData is the data of contract.expiring (WP-270); a
// contract has a client, no site.
type ContractExpiringData struct {
	ID        string `json:"id"`
	ClientID  string `json:"client_id,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// StockMovementData is the data of stock.movement, the movement with the
// field names of movement.Movement (WP-274). id is the movement; the
// entity of the envelope is the moved asset or quantity item.
type StockMovementData struct {
	ID             string   `json:"id"`
	ItemKind       string   `json:"item_kind"`
	AssetID        string   `json:"asset_id,omitempty"`
	QuantityItemID string   `json:"quantity_item_id,omitempty"`
	MovementType   string   `json:"movement_type"`
	Quantity       *float64 `json:"quantity,omitempty"`
}

// StockBelowMinimumData is the data of stock.below_minimum, about a
// quantity item (WP-285).
type StockBelowMinimumData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
}

// ReservationData is the data of the reservation types (WP-277, WP-284):
// the reservation and its asset or quantity item.
type ReservationData struct {
	ID             string `json:"id"`
	ItemKind       string `json:"item_kind"`
	AssetID        string `json:"asset_id,omitempty"`
	QuantityItemID string `json:"quantity_item_id,omitempty"`
}

// TicketData is the data of ticket.updated (WP-290).
type TicketData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
}

// TicketSLABreachedData is the data of ticket.sla_breached, one missed due
// time of a ticket (the sla.check run, WP-769): kind response or
// resolution, its due time, the ticket's assignee, team and the team's
// lead (team.lead_id), the recipients of the escalation.
type TicketSLABreachedData struct {
	ID           string `json:"id"`
	TicketNumber int    `json:"ticket_number"`
	Priority     string `json:"priority"`
	SLAID        string `json:"sla_id"`
	Kind         string `json:"kind"`
	DueAt        string `json:"due_at"`
	AssigneeID   string `json:"assignee_id,omitempty"`
	TeamID       string `json:"team_id,omitempty"`
	TeamLeadID   string `json:"team_lead_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	SiteID       string `json:"site_id,omitempty"`
}

// MetamodelPublishedData is the data of metamodel.published, the
// publication (WP-261; its content follows E-37): the published draft
// (direct false) or a change through a resource route without a draft
// (direct true, MET-04), the number of its elements per part (the item
// kinds of the metamodel drafts) and the affected types with their
// schema_version after the publication.
type MetamodelPublishedData struct {
	ID      string                   `json:"id"`
	DraftID string                   `json:"draft_id,omitempty"`
	Direct  bool                     `json:"direct"`
	Counts  map[string]int           `json:"counts"`
	Types   []MetamodelPublishedType `json:"types"`
}

// MetamodelPublishedType is a type a publication affected, with its
// ci_type.schema_version after it.
type MetamodelPublishedType struct {
	ID            string `json:"id"`
	SchemaVersion int64  `json:"schema_version"`
}

// DocumentScannedData is the data of document.scanned (WP-734): the scan
// result of a document version, clean, infected or scan_failed, with the
// signature of a finding. ID and DocumentID name the document. The
// signature is scan_signature: the key signature would be sealed as
// personal data (pii). ClientID and SiteID are set only when every link of
// the document carries the same client or site; uploaded_by is the
// uploader's user id, absent for versions without one.
type DocumentScannedData struct {
	ID            string `json:"id"`
	DocumentID    string `json:"document_id"`
	Version       int    `json:"version"`
	Result        string `json:"result"`
	ScanSignature string `json:"scan_signature,omitempty"`
	UploadedBy    string `json:"uploaded_by,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	SiteID        string `json:"site_id,omitempty"`
}

// ImportCompletedData is the data of import.completed, the import run
// (WP-775): its profile, entity and mode, whether it was a dry run, the
// counters of its rows and the user who started it (absent for a run
// without a user, e.g. of an API key acting as a service account).
type ImportCompletedData struct {
	ID          string `json:"id"`
	ProfileID   string `json:"profile_id"`
	Entity      string `json:"entity"`
	Mode        string `json:"mode"`
	DryRun      bool   `json:"dry_run"`
	Total       int    `json:"total"`
	Created     int    `json:"created"`
	Updated     int    `json:"updated"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
	InitiatedBy string `json:"initiated_by,omitempty"`
}

// Payloads assigns every phase 2 type the frame of its data.
var Payloads = map[string]any{
	CIStatusChanged:       CIStatusChangedData{},
	LifecycleChanged:      LifecycleChangedData{},
	RelationshipChanged:   RelationshipChangedData{},
	AssetCreated:          AssetData{},
	AssetChanged:          AssetData{},
	AssetDeleted:          AssetData{},
	AssetAssigned:         AssetAssignmentData{},
	AssetReturned:         AssetAssignmentData{},
	AssetWarrantyExpiring: AssetWarrantyData{},
	ContractExpiring:      ContractExpiringData{},
	StockMovement:         StockMovementData{},
	StockBelowMinimum:     StockBelowMinimumData{},
	ReservationCreated:    ReservationData{},
	ReservationReleased:   ReservationData{},
	ReservationExpired:    ReservationData{},
	ReservationConverted:  ReservationData{},
	TicketUpdated:         TicketData{},
	TicketSLABreached:     TicketSLABreachedData{},
	MetamodelPublished:    MetamodelPublishedData{},
	DocumentScanned:       DocumentScannedData{},
	ImportCompleted:       ImportCompletedData{},
}
