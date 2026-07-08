// Package audit provides audit log writing for the Reticora platform.
package audit

import (
	"context"
	"time"
)

// Entry represents a single audit log entry.
type Entry struct {
	OrgID        string
	ActorType    string // user, system, api_key
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Payload      any
	OccurredAt   time.Time
}

// Writer defines the interface for writing audit log entries.
type Writer interface {
	Write(ctx context.Context, entry Entry) error
}
