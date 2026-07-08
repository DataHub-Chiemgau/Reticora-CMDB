// Package audit provides audit log writing for the Reticora platform.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
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

// StoredEntry is an Entry with hash-chain metadata.
type StoredEntry struct {
	Entry
	Sequence     int64
	Hash         string
	PreviousHash string
}

// Writer defines the interface for writing audit log entries.
type Writer interface {
	Write(ctx context.Context, entry Entry) error
}

// Reader defines the interface for reading audit log entries.
type Reader interface {
	List(ctx context.Context, orgID string, limit, offset int) ([]StoredEntry, int, error)
	Verify(ctx context.Context, orgID string) (bool, error)
}

// MemoryWriter implements Writer with an in-memory hash-chain for development.
type MemoryWriter struct {
	mu       sync.Mutex
	entries  []StoredEntry
	lastHash string
	seq      int64
}

// NewMemoryWriter creates a new in-memory audit writer with hash-chain integrity.
func NewMemoryWriter() *MemoryWriter {
	return &MemoryWriter{
		lastHash: "genesis",
	}
}

// Write appends an entry to the in-memory audit log with hash-chain linkage.
func (w *MemoryWriter) Write(_ context.Context, entry Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now().UTC()
	}

	w.seq++
	hash := computeHash(w.lastHash, w.seq, entry)

	stored := StoredEntry{
		Entry:        entry,
		Sequence:     w.seq,
		Hash:         hash,
		PreviousHash: w.lastHash,
	}
	w.entries = append(w.entries, stored)
	w.lastHash = hash

	slog.Debug("audit entry written",
		"org_id", entry.OrgID,
		"action", entry.Action,
		"resource", entry.ResourceType+"/"+entry.ResourceID,
		"seq", w.seq,
	)
	return nil
}

// List returns paginated audit entries for an organization.
func (w *MemoryWriter) List(_ context.Context, orgID string, limit, offset int) ([]StoredEntry, int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var filtered []StoredEntry
	for _, e := range w.entries {
		if e.OrgID == orgID {
			filtered = append(filtered, e)
		}
	}

	total := len(filtered)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

// Verify checks the hash-chain integrity for an organization's audit log.
func (w *MemoryWriter) Verify(_ context.Context, orgID string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var prev string
	first := true
	for _, e := range w.entries {
		if e.OrgID != orgID {
			continue
		}
		if first {
			prev = e.PreviousHash
			first = false
		}
		expected := computeHash(prev, e.Sequence, e.Entry)
		if e.Hash != expected {
			return false, fmt.Errorf("audit: hash mismatch at sequence %d", e.Sequence)
		}
		prev = e.Hash
	}
	return true, nil
}

func computeHash(previousHash string, seq int64, entry Entry) string {
	payload, _ := json.Marshal(entry.Payload)
	data := fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s|%s|%s|%s",
		previousHash, seq,
		entry.OrgID, entry.ActorType, entry.ActorID,
		entry.Action, entry.ResourceType, entry.ResourceID,
		string(payload), entry.OccurredAt.Format(time.RFC3339Nano),
	)
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}
