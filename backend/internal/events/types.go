package events

import (
	"time"
)

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

// Event types of the monitoring of phase 3 (EVT-03 [P3], ALR-02, PRB-03,
// ALR-07; WP-867, E-161 Option 2): one event per state change, never one
// per probe result. config.changed is a change of the configuration of a
// probe, ci.config_changed one of a device. The producing work packages
// write them (plannedProducers in evt02_producers_integration_test.go).
const (
	ProbeStateChanged           = "probe.state_changed"
	AlarmOpened                 = "alarm.opened"
	AlarmAcknowledged           = "alarm.acknowledged"
	AlarmEscalated              = "alarm.escalated"
	AlarmSeverityChanged        = "alarm.severity_changed"
	AlarmResolved               = "alarm.resolved"
	IncidentOpened              = "incident.opened"
	IncidentUpdated             = "incident.updated"
	IncidentResolved            = "incident.resolved"
	CollectorGroupUnreachable   = "collector_group.unreachable"
	CollectorGroupReachable     = "collector_group.reachable"
	CollectorGroupFailover      = "collector_group.failover"
	CollectorClockSkew          = "collector.clock_skew"
	ProbeConfigChanged          = "config.changed"
	CIConfigChanged             = "ci.config_changed"
	MonitoringSuspensionChanged = "monitoring.suspension_changed"
	CapacityForecastExceeded    = "capacity.forecast_exceeded"
	SLAViolated                 = "sla.violated"
)

// Event types of the modules of phase 3 (EVT-03 [P3], NTF-05 [P3], MGT-04,
// WFL-02, SEC-02, NTF-02, PKG-02, TLC-05, TLC-06; WP-868, E-161). They
// replace the reservation of phase 2 (maintenance.scheduled, workflow.*,
// finding.*).
const (
	MaintenanceScheduled      = "maintenance.scheduled"
	MaintenanceStarted        = "maintenance.started"
	MaintenanceEnded          = "maintenance.ended"
	WorkflowRunStarted        = "workflow.run_started"
	WorkflowRunSucceeded      = "workflow.run_succeeded"
	WorkflowRunFailed         = "workflow.run_failed"
	WorkflowRunCancelled      = "workflow.run_cancelled"
	WorkflowApprovalRequested = "workflow.approval_requested"
	WorkflowApprovalDecided   = "workflow.approval_decided"
	WorkflowWebhook           = "workflow.webhook"
	FindingCreated            = "finding.created"
	FindingChanged            = "finding.changed"
	FindingResolved           = "finding.resolved"
	NotificationEscalated     = "notification.escalated"
	NotificationAcknowledged  = "notification.acknowledged"
	EntitlementChanged        = "entitlement.changed"
	OrgExportCompleted        = "org.export_completed"
	OrgDeletionRequested      = "org.deletion_requested"
	OrgDeletionCancelled      = "org.deletion_cancelled"
)

// Event types of the network domain of phase 3 (IPAM/ASN, credentials, ADMP;
// WP-870). Their data never carry a secret or the holder of a connection in
// plain text (SEC-05).
const (
	IPAMSubnetChanged          = "ipam.subnet.changed"
	IPAMAddressStateChanged    = "ipam.address.state_changed"
	ASNChanged                 = "asn.changed"
	CredentialRotated          = "credential.rotated"
	CredentialRotationDue      = "credential.rotation_due"
	CredentialResolutionFailed = "credential.resolution_failed"
	ADMPConnectionRefreshed    = "admp.connection.refreshed"
)

// Phase3Types are the event types of phase 3 (EVT-03 [P3]); every one has a
// producer case or a planned producer (TestEveryEVT02TypeHasAProducer).
var Phase3Types = []string{
	ProbeStateChanged, AlarmOpened, AlarmAcknowledged, AlarmEscalated, AlarmSeverityChanged, AlarmResolved, IncidentOpened, IncidentUpdated, IncidentResolved, CollectorGroupUnreachable, CollectorGroupReachable, CollectorGroupFailover, CollectorClockSkew, ProbeConfigChanged, CIConfigChanged, MonitoringSuspensionChanged, CapacityForecastExceeded, SLAViolated,
	MaintenanceScheduled, MaintenanceStarted, MaintenanceEnded, WorkflowRunStarted, WorkflowRunSucceeded, WorkflowRunFailed, WorkflowRunCancelled, WorkflowApprovalRequested, WorkflowApprovalDecided, WorkflowWebhook, FindingCreated, FindingChanged, FindingResolved, NotificationEscalated, NotificationAcknowledged, EntitlementChanged, OrgExportCompleted, OrgDeletionRequested, OrgDeletionCancelled,
	IPAMSubnetChanged, IPAMAddressStateChanged, ASNChanged, CredentialRotated, CredentialRotationDue, CredentialResolutionFailed, ADMPConnectionRefreshed,
}

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
	ProbeStateChanged, AlarmOpened, AlarmAcknowledged, AlarmEscalated, AlarmSeverityChanged, AlarmResolved, IncidentOpened, IncidentUpdated, IncidentResolved, CollectorGroupUnreachable, CollectorGroupReachable, CollectorGroupFailover, CollectorClockSkew, ProbeConfigChanged, CIConfigChanged, MonitoringSuspensionChanged, CapacityForecastExceeded, SLAViolated,
	MaintenanceScheduled, MaintenanceStarted, MaintenanceEnded, WorkflowRunStarted, WorkflowRunSucceeded, WorkflowRunFailed, WorkflowRunCancelled, WorkflowApprovalRequested, WorkflowApprovalDecided, WorkflowWebhook, FindingCreated, FindingChanged, FindingResolved, NotificationEscalated, NotificationAcknowledged, EntitlementChanged, OrgExportCompleted, OrgDeletionRequested, OrgDeletionCancelled,
	IPAMSubnetChanged, IPAMAddressStateChanged, ASNChanged, CredentialRotated, CredentialRotationDue, CredentialResolutionFailed, ADMPConnectionRefreshed,
}

// IsType reports whether t is a registered event type. The reservation of
// phase 3 ended with WP-868: every type of phase 3 is registered.
func IsType(t string) bool {
	for _, known := range Types {
		if known == t {
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

// RelationshipChangedData is the data of relationship.changed (WP-296): the
// edge, its source (the edge source: manual, discovery, …), the change kind
// (updated, verification_changed) and the changed fields, no attribute
// value.
type RelationshipChangedData struct {
	ID            string   `json:"id"`
	SourceCIID    string   `json:"source_ci_id"`
	TargetCIID    string   `json:"target_ci_id"`
	RelType       string   `json:"rel_type"`
	Source        string   `json:"source"`
	ChangeKind    string   `json:"change_kind"`
	ChangedFields []string `json:"changed_fields"`
}

// AssetData is the data of asset.created, asset.changed and asset.deleted
// (WP-740).
type AssetData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
}

// AssetChangedData is the data of asset.changed (WP-740): the frame of
// AssetData and the fields the change wrote (changed_fields, the members
// of the asset DTO).
type AssetChangedData struct {
	ID            string   `json:"id"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	ChangedFields []string `json:"changed_fields"`
}

// AssetAssignmentData is the data of asset.assigned and asset.returned
// (WP-275, WP-748): the asset, its client and site, the booked movement,
// the holder (on asset.returned the previous one) and the due date of an
// assignment; on a signed movement the signature document, the
// signature_hash and the signer as user id or vault surrogate. It never
// holds image data or a name.
type AssetAssignmentData struct {
	ID                  string     `json:"id"`
	ClientID            string     `json:"client_id,omitempty"`
	SiteID              string     `json:"site_id,omitempty"`
	MovementID          string     `json:"movement_id"`
	AssignedUserID      string     `json:"assigned_user_id,omitempty"`
	AssignedTeamID      string     `json:"assigned_team_id,omitempty"`
	DueAt               *time.Time `json:"due_at,omitempty"`
	SignatureDocumentID string     `json:"signature_document_id,omitempty"`
	SignatureHash       string     `json:"signature_hash,omitempty"`
	SignerUserID        string     `json:"signer_user_id,omitempty"`
	SignerSurrogate     string     `json:"signer_surrogate,omitempty"`
}

// AssetWarrantyData is the data of asset.warranty_expiring, one reached
// stage of the expiry.check run (WP-270): the stage in days, the due date
// (the warranty end), expires_at (the warranty end) and the days left
// until the due date, with the asset's name, inventory number, owners and
// primary CI (asset.ci_id), the recipients of the notification.
type AssetWarrantyData struct {
	ID          string `json:"id"`
	ClientID    string `json:"client_id,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
	Name        string `json:"name"`
	InventoryNo string `json:"inventory_no"`
	OwnerUserID string `json:"owner_user_id,omitempty"`
	OwnerTeamID string `json:"owner_team_id,omitempty"`
	PrimaryCIID string `json:"primary_ci_id,omitempty"`
	Stage       int    `json:"stage"`
	DueDate     string `json:"due_date"`
	ExpiresAt   string `json:"expires_at"`
	DaysLeft    int    `json:"days_left"`
}

// ContractExpiringData is the data of contract.expiring, one reached
// stage of the expiry.check run (WP-270); a contract has a client, no
// site. The due date is end_date minus notice_period_days (0 without a
// notice period), expires_at the end_date.
type ContractExpiringData struct {
	ID               string `json:"id"`
	ClientID         string `json:"client_id,omitempty"`
	Number           string `json:"number"`
	Kind             string `json:"kind,omitempty"`
	SupplierID       string `json:"supplier_id,omitempty"`
	NoticePeriodDays int    `json:"notice_period_days"`
	Stage            int    `json:"stage"`
	DueDate          string `json:"due_date"`
	ExpiresAt        string `json:"expires_at"`
	DaysLeft         int    `json:"days_left"`
}

// StockMovementData is the data of stock.movement, the movement with the
// field names of movement.Movement (WP-274). id is the movement; the
// entity of the envelope is the moved asset or quantity item; client_id
// and site_id are the scope the ledger derived for the movement.
type StockMovementData struct {
	ID             string   `json:"id"`
	ItemKind       string   `json:"item_kind"`
	AssetID        string   `json:"asset_id,omitempty"`
	QuantityItemID string   `json:"quantity_item_id,omitempty"`
	MovementType   string   `json:"movement_type"`
	Quantity       *float64 `json:"quantity,omitempty"`
	ClientID       string   `json:"client_id,omitempty"`
	SiteID         string   `json:"site_id,omitempty"`
}

// StockBelowMinimumData is the data of stock.below_minimum, about a
// consumable whose total stock fell below its minimum level (WP-285):
// on_hand_total (sum over all bins, independent of the scope) after the
// booking or the change of min_level, with the article's SKU, name and
// unit. client_id is absent for an article without client; a consumable
// has no site (its bins have one each), so the frame carries none.
type StockBelowMinimumData struct {
	ID          string  `json:"id"`
	ClientID    string  `json:"client_id,omitempty"`
	SKU         string  `json:"sku,omitempty"`
	Name        string  `json:"name"`
	Unit        string  `json:"unit"`
	OnHandTotal float64 `json:"on_hand_total"`
	MinLevel    float64 `json:"min_level"`
}

// ReservationData is the data of the reservation types (WP-277, WP-284):
// the reservation and its asset, or its consumable, bin and quantity, with
// the client and site of the reservation. QuantityItemID appears only in
// events before WP-284.
type ReservationData struct {
	ID             string   `json:"id"`
	ItemKind       string   `json:"item_kind"`
	AssetID        string   `json:"asset_id,omitempty"`
	QuantityItemID string   `json:"quantity_item_id,omitempty"`
	ConsumableID   string   `json:"consumable_id,omitempty"`
	LocationID     string   `json:"location_id,omitempty"`
	Quantity       *float64 `json:"quantity,omitempty"`
	ClientID       string   `json:"client_id,omitempty"`
	SiteID         string   `json:"site_id,omitempty"`
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

// ProbeStateChangedData is the data of probe.state_changed (PRB-05, ALR-02):
// one state change of a probe, never one result (E-161). suppressed_by and
// root_incident_id name the root of a consequential outage.
type ProbeStateChangedData struct {
	ID             string `json:"id"`
	CIID           string `json:"ci_id,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	SiteID         string `json:"site_id,omitempty"`
	From           string `json:"from"`
	To             string `json:"to"`
	Reason         string `json:"reason,omitempty"`
	SuppressedBy   string `json:"suppressed_by,omitempty"`
	RootIncidentID string `json:"root_incident_id,omitempty"`
}

// AlarmData is the data of the alarm.* types (ALR-01, ALR-02). id and
// alarm_id both name the alarm: id is the frame of every type (WP-867),
// alarm_id the name of ALR-01 (WP-982); dedup_key is the deduplication key
// of the alarm. source_kind, summary, value, suppressed_reason, opened_at
// and resolved_at serve the notifications (WP-987): a suppressed alarm
// notifies nobody, the texts name the summary, the value and the duration.
// actions are the follow-up actions of the alarm (WP-988, AlarmActions):
// notify.enabled false notifies nobody, webhook.enabled false reaches no
// webhook subscription; absent is both on.
type AlarmData struct {
	ID               string   `json:"id"`
	AlarmID          string   `json:"alarm_id"`
	DedupKey         string   `json:"dedup_key"`
	CIID             string   `json:"ci_id,omitempty"`
	ClientID         string   `json:"client_id,omitempty"`
	SiteID           string   `json:"site_id,omitempty"`
	Severity         string   `json:"severity"`
	IncidentID       string   `json:"incident_id,omitempty"`
	SourceKind       string   `json:"source_kind,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	Value            *float64 `json:"value,omitempty"`
	SuppressedReason string   `json:"suppressed_reason,omitempty"`
	// AfterMaintenance marks the alarm.opened of an alarm still open at
	// the end of the window that suppressed it (WP-1033).
	AfterMaintenance bool          `json:"after_maintenance,omitempty"`
	OpenedAt         *time.Time    `json:"opened_at,omitempty"`
	ResolvedAt       *time.Time    `json:"resolved_at,omitempty"`
	Actions          *AlarmActions `json:"actions,omitempty"`
}

// IncidentData is the data of the incident.* types (ALR-03): the incident
// with its root CI and cause class.
//
// incident.updated of a user action (WP-1036) adds incident_id (the
// incident, as id), the kind of the change (acknowledged, root_changed),
// the root alarm and the acting user.
type IncidentData struct {
	ID          string `json:"id"`
	IncidentID  string `json:"incident_id,omitempty"`
	RootCIID    string `json:"root_ci_id,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
	Severity    string `json:"severity"`
	CauseClass  string `json:"cause_class,omitempty"`
	Change      string `json:"change,omitempty"`
	RootAlarmID string `json:"root_alarm_id,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
}

// CollectorGroupData is the data of the collector_group.* types (PRB-03,
// PRB-10): the group, its scope and the root event of a network outage.
type CollectorGroupData struct {
	ID          string `json:"id"`
	ClientID    string `json:"client_id,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
	RootEventID string `json:"root_event_id,omitempty"`
}

// CollectorGroupFailoverData is the data of collector_group.failover
// (PRB-03, PRB-10, WP-963): a member of the group stopped its monitoring
// alive messages and its probes moved to the remaining healthy members.
// ID and CollectorGroupID are the group, FailedCollectorID the member set
// down, MovedProbes the number of probes assigned anew.
type CollectorGroupFailoverData struct {
	ID                string `json:"id"`
	CollectorGroupID  string `json:"collector_group_id"`
	ClientID          string `json:"client_id,omitempty"`
	SiteID            string `json:"site_id,omitempty"`
	FailedCollectorID string `json:"failed_collector_id"`
	MovedProbes       int    `json:"moved_probes"`
}

// CollectorClockSkewData is the data of collector.clock_skew: a collector
// whose clock deviates from the server (PRB-09, WP-962). SkewMS is the
// median offset collector − server, OffsetMS the same under the name of
// WP-962 AK3, CollectorID the same as ID (id and skew_ms stay for the
// consumers of E-161); Degraded marks the crossing of 60 s
// (the member is degraded and its timestamps are corrected), Resolved the
// end of the warning (below 2 s for six alive messages).
type CollectorClockSkewData struct {
	ID               string `json:"id"`
	CollectorID      string `json:"collector_id"`
	CollectorGroupID string `json:"collector_group_id,omitempty"`
	ClientID         string `json:"client_id,omitempty"`
	SiteID           string `json:"site_id,omitempty"`
	SkewMS           int64  `json:"skew_ms"`
	OffsetMS         int64  `json:"offset_ms"`
	Degraded         bool   `json:"degraded"`
	Resolved         bool   `json:"resolved,omitempty"`
}

// ProbeConfigChangedData is the data of config.changed: a change of the
// configuration of a probe, never of a device (E-161).
type ProbeConfigChangedData struct {
	ID            string   `json:"id"`
	CIID          string   `json:"ci_id,omitempty"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	ChangedFields []string `json:"changed_fields"`
}

// CIConfigChangedData is the data of ci.config_changed (ALR-07): a new
// configuration version of a device.
type CIConfigChangedData struct {
	ID       string `json:"id"`
	CIID     string `json:"ci_id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
	Hash     string `json:"hash"`
}

// MonitoringSuspensionData is the data of monitoring.suspension_changed:
// monitoring functions of the organization were suspended or resumed because
// their feature was lost or regained.
type MonitoringSuspensionData struct {
	FeatureKey string `json:"feature_key"`
	Suspended  bool   `json:"suspended"`
}

// CapacityForecastData is the data of capacity.forecast_exceeded (ALR-08): a
// forecast reaches its threshold within the horizon.
type CapacityForecastData struct {
	ID          string `json:"id"`
	CIID        string `json:"ci_id"`
	ClientID    string `json:"client_id,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
	Metric      string `json:"metric"`
	ThresholdAt string `json:"threshold_at"`
}

// SLAViolatedData is the data of sla.violated (ALR-06): an availability
// target missed its goal in a period.
type SLAViolatedData struct {
	ID            string  `json:"id"`
	ClientID      string  `json:"client_id,omitempty"`
	SiteID        string  `json:"site_id,omitempty"`
	Period        string  `json:"period"`
	TargetPercent float64 `json:"target_percent"`
	ActualPercent float64 `json:"actual_percent"`
}

// MaintenanceWindowData is the data of the maintenance.* types (MGT-04): the
// window, its scope and its time span.
type MaintenanceWindowData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

// WorkflowRunData is the data of the workflow.run_* types (WFL-02): the run,
// its workflow and the object of its trigger.
type WorkflowRunData struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflow_id"`
	CIID       string `json:"ci_id,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
	Status     string `json:"status"`
}

// WorkflowApprovalData is the data of the workflow.approval_* types (WFL-02,
// WFL-04): one approval step of a run.
type WorkflowApprovalData struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	StepKey  string `json:"step_key"`
	Decision string `json:"decision,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
}

// WorkflowWebhookData is the data of workflow.webhook: the send_webhook
// action of a run (WFL-02); the action adds its fields.
type WorkflowWebhookData struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflow_id"`
	CIID       string `json:"ci_id,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
}

// FindingData is the data of the finding.* types (SEC-02): the finding of a
// CI with its severity.
type FindingData struct {
	ID       string `json:"id"`
	CIID     string `json:"ci_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
	Severity string `json:"severity"`
}

// NotificationEscalationData is the data of the notification.* types
// (NTF-02): one step of an escalation chain.
type NotificationEscalationData struct {
	ID         string `json:"id"`
	AlarmID    string `json:"alarm_id,omitempty"`
	IncidentID string `json:"incident_id,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
	Step       int    `json:"step"`
}

// EntitlementChangedData is the data of entitlement.changed (PKG-02): a
// change of plan, add-on or quota of the organization.
type EntitlementChangedData struct {
	ID   string `json:"id"`
	Plan string `json:"plan"`
	// PlanBefore is the plan before a plan change (WP-882); empty when the
	// plan stayed (an add-on or a quota changed).
	PlanBefore string `json:"plan_before,omitempty"`
	// Addon is the add-on booked or cancelled (WP-883); empty otherwise.
	Addon   string   `json:"addon,omitempty"`
	Changed []string `json:"changed"`
}

// OrgExportData is the data of org.export_completed (TLC-05): the full
// export of the organization is ready for download (WP-1151): the job, the
// user who started it, the exported rows and bytes and the end of the
// download period.
type OrgExportData struct {
	ID          string `json:"id"`
	InitiatedBy string `json:"initiated_by,omitempty"`
	Rows        int64  `json:"rows"`
	Bytes       int64  `json:"bytes"`
	ExpiresAt   string `json:"expires_at"`
}

// OrgDeletionData is the data of the org.deletion_* types (TLC-06,
// WP-1152): the deletion request of the organization. ID is the
// organization, RequestedBy the user who requested it, DueAt the end of the
// 30-day grace period (of the cancelled request for org.deletion_cancelled),
// CancelledBy the user who cancelled it.
type OrgDeletionData struct {
	ID          string `json:"id"`
	RequestedBy string `json:"requested_by,omitempty"`
	DueAt       string `json:"due_at"`
	CancelledBy string `json:"cancelled_by,omitempty"`
}

// SubnetChangedData is the data of ipam.subnet.changed (IPM-02): a subnet
// was created, changed, allocated or deleted.
type SubnetChangedData struct {
	ID         string `json:"id"`
	ClientID   string `json:"client_id,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
	CIDR       string `json:"cidr"`
	ChangeKind string `json:"change_kind"`
}

// AddressStateChangedData is the data of ipam.address.state_changed
// (IPM-04): the state of an IP address changed (active, offline, unknown).
// SubnetID is the subnet of the address, empty without one (WP-902).
type AddressStateChangedData struct {
	ID       string `json:"id"`
	SubnetID string `json:"subnet_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
	CIID     string `json:"ci_id,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to"`
}

// ASNChangedData is the data of asn.changed (IPM-15): an AS number was
// created, assigned or released.
type ASNChangedData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	ASN      int64  `json:"asn"`
}

// CredentialEventData is the data of the credential.* types (CRD-05,
// CRD-06): the credential and its client; never a secret.
type CredentialEventData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id,omitempty"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason,omitempty"`
	// SecretVersion is the version of the secret after a rotation and
	// Source where it lives, stored or vault (WP-889).
	SecretVersion int    `json:"secret_version,omitempty"`
	Source        string `json:"source,omitempty"`
}

// CredentialRotationDueData is the data of credential.rotation_due
// (CRD-05, WP-897): a stored secret older than its age limit; never a
// secret.
type CredentialRotationDueData struct {
	ID string `json:"id"`
	// CredentialID is the credential as AK4 of WP-897 names it; the same
	// value as ID, which every credential event carries.
	CredentialID  string `json:"credential_id"`
	ClientID      string `json:"client_id,omitempty"`
	Kind          string `json:"kind"`
	Reason        string `json:"reason,omitempty"`
	SecretVersion int    `json:"secret_version,omitempty"`
	Source        string `json:"source,omitempty"`
	// MaxAgeDays is the age limit of the credential, DueAt the time the
	// secret reached it (RFC 3339).
	MaxAgeDays int    `json:"max_age_days,omitempty"`
	DueAt      string `json:"due_at,omitempty"`
}

// CredentialResolutionFailedData is the data of
// credential.resolution_failed (CRD-06, WP-898): the frame of the
// credential.* types with the failed use, the credential (again as
// credential_id), the CI, the namespace and the error class; never a
// secret.
type CredentialResolutionFailedData struct {
	ID            string `json:"id"`
	ClientID      string `json:"client_id,omitempty"`
	Kind          string `json:"kind"`
	Reason        string `json:"reason,omitempty"`
	SecretVersion int    `json:"secret_version,omitempty"`
	Source        string `json:"source,omitempty"`
	CredentialID  string `json:"credential_id"`
	CIID          string `json:"ci_id,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	ErrorClass    string `json:"error_class"`
}

// ADMPConnectionRefreshedData is the data of admp.connection.refreshed
// (ADM-03): the read-only copy of an ADMP connection was refreshed; never
// the holder in plain text.
type ADMPConnectionRefreshedData struct {
	ID string `json:"id"`
	// AccountID is the ADMP account of the connection (WP-929).
	AccountID string `json:"account_id,omitempty"`
	CIID      string `json:"ci_id,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	SiteID    string `json:"site_id,omitempty"`
	// Stale: the refresh failed and the copy is the last known state
	// (ADM-03, WP-929).
	Stale bool `json:"stale"`
}

// Payloads assigns every phase 2 and phase 3 type the frame of its data.
var Payloads = map[string]any{
	CIStatusChanged:             CIStatusChangedData{},
	LifecycleChanged:            LifecycleChangedData{},
	RelationshipChanged:         RelationshipChangedData{},
	AssetCreated:                AssetData{},
	AssetChanged:                AssetChangedData{},
	AssetDeleted:                AssetData{},
	AssetAssigned:               AssetAssignmentData{},
	AssetReturned:               AssetAssignmentData{},
	AssetWarrantyExpiring:       AssetWarrantyData{},
	ContractExpiring:            ContractExpiringData{},
	StockMovement:               StockMovementData{},
	StockBelowMinimum:           StockBelowMinimumData{},
	ReservationCreated:          ReservationData{},
	ReservationReleased:         ReservationData{},
	ReservationExpired:          ReservationData{},
	ReservationConverted:        ReservationData{},
	TicketUpdated:               TicketData{},
	TicketSLABreached:           TicketSLABreachedData{},
	MetamodelPublished:          MetamodelPublishedData{},
	DocumentScanned:             DocumentScannedData{},
	ImportCompleted:             ImportCompletedData{},
	ProbeStateChanged:           ProbeStateChangedData{},
	AlarmOpened:                 AlarmData{},
	AlarmAcknowledged:           AlarmData{},
	AlarmEscalated:              AlarmData{},
	AlarmSeverityChanged:        AlarmData{},
	AlarmResolved:               AlarmData{},
	IncidentOpened:              IncidentData{},
	IncidentUpdated:             IncidentData{},
	IncidentResolved:            IncidentData{},
	CollectorGroupUnreachable:   CollectorGroupData{},
	CollectorGroupReachable:     CollectorGroupData{},
	CollectorGroupFailover:      CollectorGroupFailoverData{},
	CollectorClockSkew:          CollectorClockSkewData{},
	ProbeConfigChanged:          ProbeConfigChangedData{},
	CIConfigChanged:             CIConfigChangedData{},
	MonitoringSuspensionChanged: MonitoringSuspensionData{},
	CapacityForecastExceeded:    CapacityForecastData{},
	SLAViolated:                 SLAViolatedData{},
	MaintenanceScheduled:        MaintenanceWindowData{},
	MaintenanceStarted:          MaintenanceWindowData{},
	MaintenanceEnded:            MaintenanceWindowData{},
	WorkflowRunStarted:          WorkflowRunData{},
	WorkflowRunSucceeded:        WorkflowRunData{},
	WorkflowRunFailed:           WorkflowRunData{},
	WorkflowRunCancelled:        WorkflowRunData{},
	WorkflowApprovalRequested:   WorkflowApprovalData{},
	WorkflowApprovalDecided:     WorkflowApprovalData{},
	WorkflowWebhook:             WorkflowWebhookData{},
	FindingCreated:              FindingData{},
	FindingChanged:              FindingData{},
	FindingResolved:             FindingData{},
	NotificationEscalated:       NotificationEscalationData{},
	NotificationAcknowledged:    NotificationEscalationData{},
	EntitlementChanged:          EntitlementChangedData{},
	OrgExportCompleted:          OrgExportData{},
	OrgDeletionRequested:        OrgDeletionData{},
	OrgDeletionCancelled:        OrgDeletionData{},
	IPAMSubnetChanged:           SubnetChangedData{},
	IPAMAddressStateChanged:     AddressStateChangedData{},
	ASNChanged:                  ASNChangedData{},
	CredentialRotated:           CredentialEventData{},
	CredentialRotationDue:       CredentialRotationDueData{},
	CredentialResolutionFailed:  CredentialResolutionFailedData{},
	ADMPConnectionRefreshed:     ADMPConnectionRefreshedData{},
}
