// Package events provides NATS JetStream event publishing for the Reticora platform.
package events

import "context"

// Event represents a domain event to be published.
type Event struct {
	Subject string
	OrgID   string
	Type    string
	Payload any
}

// Publisher defines the interface for publishing events to the message bus.
type Publisher interface {
	Publish(ctx context.Context, event Event) error
	Close() error
}

// NoopPublisher is a no-op implementation used when NATS is not configured.
type NoopPublisher struct{}

// Publish does nothing.
func (n *NoopPublisher) Publish(_ context.Context, _ Event) error { return nil }

// Close does nothing.
func (n *NoopPublisher) Close() error { return nil }
