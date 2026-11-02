// Package events is the domain event contract of EVT-01 to EVT-03: every
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
	// StreamMaxAge and StreamMaxBytes are the default retention of the
	// stream (30 days, 10 GB; NFR-06); RETICORA_EVENTS_MAX_AGE and
	// RETICORA_EVENTS_MAX_BYTES set it per installation (Publisher.MaxAge,
	// Publisher.MaxBytes).
	StreamMaxAge         = 30 * 24 * time.Hour
	StreamMaxBytes int64 = 10 << 30
)
