// Package buffer provides disk-backed message buffering for disconnected edge operation.
package buffer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is a buffered payload awaiting delivery to the control plane.
type Message struct {
	ID        string            `json:"id"`
	Topic     string            `json:"topic"`
	Payload   []byte            `json:"payload"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	Attempts  int               `json:"attempts"`
}

// Queue defines the offline buffering contract shared by edge runtimes.
type Queue interface {
	Enqueue(ctx context.Context, msg Message) (string, error)
	PeekBatch(ctx context.Context, limit int) ([]Message, error)
	Ack(ctx context.Context, ids []string) error
	Len(ctx context.Context) (int, error)
}

// DiskBuffer persists messages as individual JSON files inside a spool directory.
//
// Hardening (Epic D4): the spool is bounded so a prolonged backend outage
// cannot fill the disk. MaxBytes caps the total payload size; when it is
// exceeded, the oldest messages are dropped first (they are the stalest and
// the next successful sync re-discovers the current state anyway). MaxAge
// drops messages older than the given duration on Enqueue. Both limits are
// optional — zero means "unlimited", preserving the previous behaviour.
type DiskBuffer struct {
	Dir string
	// MaxBytes caps the total size of the spool; 0 = unlimited.
	MaxBytes int64
	// MaxAge drops messages older than this duration on Enqueue; 0 = keep forever.
	MaxAge time.Duration

	mu sync.Mutex
}

// Enqueue stores a message on disk and returns its durable identifier.
func (b *DiskBuffer) Enqueue(ctx context.Context, msg Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return "", fmt.Errorf("buffer: create spool: %w", err)
	}

	if msg.ID == "" {
		generated, err := newMessageID()
		if err != nil {
			return "", err
		}
		msg.ID = generated
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("buffer: encode message: %w", err)
	}

	if err := os.WriteFile(b.messagePath(msg.ID), data, 0o600); err != nil {
		return "", fmt.Errorf("buffer: write message: %w", err)
	}

	if err := b.enforceLimitsLocked(msg.ID); err != nil {
		return "", err
	}

	return msg.ID, nil
}

// enforceLimitsLocked drops expired and over-limit messages, oldest first.
// The just-enqueued message (justID) is never dropped by the size limit —
// if it alone exceeds MaxBytes the spool temporarily holds more than the cap
// rather than silently losing the newest data point.
func (b *DiskBuffer) enforceLimitsLocked(justID string) error {
	if b.MaxAge <= 0 && b.MaxBytes <= 0 {
		return nil
	}

	files, err := b.listMessageFiles()
	if err != nil {
		return err
	}

	var total int64
	type spoolFile struct {
		name string
		size int64
	}
	kept := make([]spoolFile, 0, len(files))
	now := time.Now().UTC()
	for _, entry := range files {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Age limit: drop messages that would survive a full sync cycle anyway.
		if b.MaxAge > 0 && now.Sub(info.ModTime()) > b.MaxAge {
			if err := os.Remove(filepath.Join(b.Dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("buffer: drop expired message: %w", err)
			}
			continue
		}
		kept = append(kept, spoolFile{name: entry.Name(), size: info.Size()})
		total += info.Size()
	}

	if b.MaxBytes <= 0 || total <= b.MaxBytes {
		return nil
	}
	// Files are sorted oldest-first (lexical name order carries the
	// creation timestamp prefix), so trim from the front.
	for _, f := range kept {
		if total <= b.MaxBytes {
			break
		}
		if f.name == justID+".json" {
			continue
		}
		if err := os.Remove(filepath.Join(b.Dir, f.name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("buffer: drop over-limit message: %w", err)
		}
		total -= f.size
	}
	return nil
}

// PeekBatch returns the oldest buffered messages without deleting them.
func (b *DiskBuffer) PeekBatch(ctx context.Context, limit int) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []Message{}, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	entries, err := b.listMessageFiles()
	if err != nil {
		return nil, err
	}

	if len(entries) > limit {
		entries = entries[:limit]
	}

	out := make([]Message, 0, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(b.Dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("buffer: read message: %w", err)
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("buffer: decode message %s: %w", entry.Name(), err)
		}
		out = append(out, msg)
	}

	return out, nil
}

// Ack removes acknowledged messages from the spool.
func (b *DiskBuffer) Ack(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, id := range ids {
		if err := os.Remove(b.messagePath(id)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("buffer: remove %s: %w", id, err)
		}
	}

	return nil
}

// Len reports the number of currently buffered messages.
func (b *DiskBuffer) Len(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	entries, err := b.listMessageFiles()
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// Stats summarizes the current spool state for operator visibility
// (Epic D4: Metriken).
type Stats struct {
	// Messages is the number of buffered messages.
	Messages int
	// Bytes is the total on-disk size of all buffered messages.
	Bytes int64
	// OldestAge is the age of the oldest buffered message; zero when empty.
	OldestAge time.Duration
}

// Stats returns the current spool statistics.
func (b *DiskBuffer) Stats(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	entries, err := b.listMessageFiles()
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{Messages: len(entries)}
	now := time.Now().UTC()
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stats.Bytes += info.Size()
		if age := now.Sub(info.ModTime()); age > stats.OldestAge {
			stats.OldestAge = age
		}
	}
	return stats, nil
}

func (b *DiskBuffer) listMessageFiles() ([]os.DirEntry, error) {
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("buffer: create spool: %w", err)
	}

	entries, err := os.ReadDir(b.Dir)
	if err != nil {
		return nil, fmt.Errorf("buffer: read spool: %w", err)
	}

	files := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		files = append(files, entry)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	return files, nil
}

func (b *DiskBuffer) messagePath(id string) string {
	return filepath.Join(b.Dir, id+".json")
}

func newMessageID() (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("buffer: generate id: %w", err)
	}

	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:]), nil
}
