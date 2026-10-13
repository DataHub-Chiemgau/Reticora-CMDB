// Package events is the domain event contract of EVT-01/EVT-02: every
// mutation writes its event into the transactional outbox (event_outbox)
// of its own transaction; the publisher delivers the events in order to
// the NATS stream RETICORA_EVENTS (events.<org_id>.<type>) and to the
// webhook subscriptions. Delivery is at least once; consumers recognize a
// repeated event by its id and process the events of one entity in
// entity_seq order.
package events

import (
	"encoding/json"
	"time"
)

// Envelope is the event contract (api/events.schema.json).
type Envelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	OrgID      string          `json:"org_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Sequence   int64           `json:"sequence"`
	EntityID   string          `json:"entity_id,omitempty"`
	EntitySeq  int64           `json:"entity_seq,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// Subject is the NATS subject of the event.
func (e *Envelope) Subject() string {
	return SubjectPrefix + e.OrgID + "." + e.Type
}

// Stream settings of EVT-01.
const (
	StreamName    = "RETICORA_EVENTS"
	SubjectPrefix = "events."
	// StreamMaxAge and StreamMaxBytes are the retention of the stream
	// (30 days, 10 GB; NFR-06).
	StreamMaxAge   = 30 * 24 * time.Hour
	StreamMaxBytes = 10 << 30
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
)

// Types lists the event types.
var Types = []string{
	CICreated, CIChanged, CIDeleted, CIRestored, CIMerged, CIOffline, CIOnline,
	RelationshipCreated, RelationshipRemoved, CollectorEnrolled, CollectorOffline,
	ReviewCreated, ReviewResolved, ExportCompleted, JobFailed, EntitlementWarning,
	EntitlementExpired, InvitationCreated, WebhookDead, CertificateExpiring,
}

// IsType reports whether t is an event type of EVT-02.
func IsType(t string) bool {
	for _, known := range Types {
		if known == t {
			return true
		}
	}
	return false
}
