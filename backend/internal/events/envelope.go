// Package events is the domain event contract of EVT-01 to EVT-03: every
// mutation writes its event into the transactional outbox (event_outbox)
// of its own transaction; the publisher delivers the events in order to
// the NATS stream RETICORA_EVENTS (events.<org_id>.<type>) and to the
// webhook subscriptions. Delivery is at least once; consumers recognize a
// repeated event by its id and process the events of one entity in
// entity_seq order.
package events

import (
	"context"
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
	// Origin names the workflow run whose execution wrote the event
	// (WP-1090); absent for every other writer.
	Origin *Origin `json:"origin,omitempty"`
}

// OriginWorkflowRun is the kind of an origin that is a workflow run.
const OriginWorkflowRun = "workflow_run"

// Origin is the writer of an event that is no user or system mutation of
// its own: a workflow run (WP-1090). Its events start runs only of
// definitions that allow it, up to a depth (loop protection).
type Origin struct {
	Kind  string `json:"kind"`
	RunID string `json:"run_id,omitempty"`
	// Depth is the trigger depth of the run: 0 for a run started by a
	// user, a schedule or an event without origin.
	Depth int `json:"depth"`
}

type originKey struct{}

// WithOrigin returns ctx whose events (Outbox.Append, AppendMany) carry o.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, &o)
}

// OriginFrom returns the origin WithOrigin put into ctx; nil without one.
func OriginFrom(ctx context.Context) *Origin {
	if o, ok := ctx.Value(originKey{}).(*Origin); ok {
		cp := *o
		return &cp
	}
	return nil
}

// originJSON is the value of event_outbox.origin for ctx; nil without an
// origin.
func originJSON(ctx context.Context) []byte {
	o := OriginFrom(ctx)
	if o == nil {
		return nil
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return nil
	}
	return raw
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
