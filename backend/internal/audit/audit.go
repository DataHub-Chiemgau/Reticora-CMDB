// Package audit provides the audit log with hash-chain integrity.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdvisoryLockID is the lock namespace for audit log serialisation.
const AdvisoryLockID int32 = 0x52657469 // "Reti" in hex

// Entry represents a single audit log entry.
type Entry struct {
	ID             string                 `json:"id"`
	OrganizationID string                 `json:"organization_id"`
	Timestamp      time.Time              `json:"timestamp"`
	ActorID        string                 `json:"actor_id"`
	Action         string                 `json:"action"`
	ResourceType   string                 `json:"resource_type"`
	ResourceID     string                 `json:"resource_id"`
	Changes        map[string]interface{} `json:"changes,omitempty"`
	PreviousHash   string                 `json:"previous_hash"`
	Hash           string                 `json:"hash"`
}

// ComputeHash computes the SHA-256 hash for an audit entry, chaining from the previous hash.
func ComputeHash(entry *Entry) string {
	data := struct {
		Timestamp    time.Time              `json:"timestamp"`
		ActorID      string                 `json:"actor_id"`
		Action       string                 `json:"action"`
		ResourceType string                 `json:"resource_type"`
		ResourceID   string                 `json:"resource_id"`
		Changes      map[string]interface{} `json:"changes,omitempty"`
		PreviousHash string                 `json:"previous_hash"`
	}{
		Timestamp:    entry.Timestamp,
		ActorID:      entry.ActorID,
		Action:       entry.Action,
		ResourceType: entry.ResourceType,
		ResourceID:   entry.ResourceID,
		Changes:      entry.Changes,
		PreviousHash: entry.PreviousHash,
	}

	b, _ := json.Marshal(data)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// AcquireAdvisoryLock acquires a transaction-scoped advisory lock for audit log serialisation.
// The lock is automatically released when the transaction commits or rolls back.
func AcquireAdvisoryLock(ctx context.Context, tx pgx.Tx, orgID string) error {
	// Use a hash of the org ID as the second lock key for per-tenant serialisation.
	orgHash := sha256.Sum256([]byte(orgID))
	orgKey := int32(orgHash[0])<<24 | int32(orgHash[1])<<16 | int32(orgHash[2])<<8 | int32(orgHash[3])

	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", int32(AdvisoryLockID), orgKey)
	if err != nil {
		return fmt.Errorf("audit: acquire advisory lock: %w", err)
	}
	return nil
}

// Log is an in-memory audit log (will be backed by PostgreSQL in production).
type Log struct {
	entries  []Entry
	lastHash string
}

// NewLog creates a new audit log.
func NewLog() *Log {
	return &Log{}
}

// Append adds a new entry to the audit log with hash-chain integrity.
func (l *Log) Append(entry *Entry) {
	entry.PreviousHash = l.lastHash
	entry.Hash = ComputeHash(entry)
	l.lastHash = entry.Hash
	l.entries = append(l.entries, *entry)
}

// Entries returns all log entries.
func (l *Log) Entries() []Entry {
	return l.entries
}

// Verify checks the integrity of the hash chain.
func (l *Log) Verify() bool {
	prevHash := ""
	for i := range l.entries {
		if l.entries[i].PreviousHash != prevHash {
			return false
		}
		computed := ComputeHash(&l.entries[i])
		if computed != l.entries[i].Hash {
			return false
		}
		prevHash = l.entries[i].Hash
	}
	return true
}
