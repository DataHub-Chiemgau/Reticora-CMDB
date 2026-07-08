package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// NATSPublisher publishes events to NATS JetStream.
// Stream: RETICORA_EVENTS, Subjects: events.<org_id>.<event_type>
type NATSPublisher struct {
	url    string
	stream string
}

// Envelope wraps an event for transport on NATS.
type Envelope struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

// NewNATSPublisher creates a publisher connected to the given NATS URL.
// The stream RETICORA_EVENTS is created/ensured on first publish.
func NewNATSPublisher(natsURL string) (*NATSPublisher, error) {
	if natsURL == "" {
		return nil, fmt.Errorf("events: NATS URL is required")
	}

	p := &NATSPublisher{
		url:    natsURL,
		stream: "RETICORA_EVENTS",
	}

	slog.Info("NATS publisher initialized", "url", natsURL, "stream", p.stream)
	return p, nil
}

// Publish sends an event to NATS JetStream.
// Subject format: events.<org_id>.<event_type>
func (p *NATSPublisher) Publish(ctx context.Context, event Event) error {
	subject := fmt.Sprintf("events.%s.%s", event.OrgID, event.Type)

	envelope := Envelope{
		OrgID:     event.OrgID,
		Type:      event.Type,
		Timestamp: time.Now().UTC(),
		Payload:   event.Payload,
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("events: marshal envelope: %w", err)
	}

	// TODO: Use actual NATS JetStream connection to publish
	// js.Publish(subject, data, nats.Context(ctx))
	_ = subject
	_ = data
	_ = ctx

	slog.Debug("event published", "subject", subject, "type", event.Type, "org_id", event.OrgID)
	return nil
}

// Close disconnects from NATS.
func (p *NATSPublisher) Close() error {
	// TODO: Close NATS connection
	slog.Info("NATS publisher closed")
	return nil
}
