// Package buffer provides disk-backed message buffering for disconnected edge operation.
package buffer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// DefaultMinRetention is the time an unacknowledged message is kept in any
// case (NFR-04: 24 hours of offline operation without data loss).
const DefaultMinRetention = 24 * time.Hour

// ErrSpoolFull is returned by Enqueue when the size limit is reached and no
// message older than the minimum retention can make room. The message is not
// stored; callers apply backpressure (pause collection) instead of relying on
// the spool to drop data.
var ErrSpoolFull = errors.New("buffer: spool full")

// Loss reasons reported to OnDrop.
const (
	LossMaxAge    = "max_age"
	LossSizeLimit = "size_limit"
	LossSpoolFull = "spool_full"
)

// Loss describes messages the spool could not keep.
type Loss struct {
	Messages int
	Bytes    int64
	Reason   string
}

// DiskBuffer persists messages as individual JSON files inside a spool directory.
//
// The spool is bounded so a prolonged backend outage cannot fill the disk, but
// it never drops unacknowledged messages younger than MinRetention (NFR-04,
// COL-05):
//   - MaxAge drops messages older than max(MaxAge, MinRetention) on Enqueue.
//   - MaxBytes caps the total size. Over the cap, messages older than
//     MinRetention are dropped oldest first; if that is not enough, Enqueue
//     returns ErrSpoolFull without storing the new message (backpressure).
//
// Every loss is counted (Stats.DroppedMessages/DroppedBytes) and reported to
// OnDrop so the caller can raise an event and inform the server. Zero limits
// mean "unlimited".
type DiskBuffer struct {
	Dir string
	// MaxBytes caps the total size of the spool; 0 = unlimited.
	MaxBytes int64
	// MaxAge drops messages older than this duration on Enqueue; 0 = keep
	// forever. Values below MinRetention are raised to MinRetention.
	MaxAge time.Duration
	// MinRetention protects unacknowledged messages from every limit; 0 =
	// DefaultMinRetention.
	MinRetention time.Duration
	// OnDrop is called (with the buffer lock held, so it must not call back
	// into the buffer) for every loss.
	OnDrop func(Loss)

	mu           sync.Mutex
	droppedMsgs  int64
	droppedBytes int64
}

func (b *DiskBuffer) minRetention() time.Duration {
	if b.MinRetention > 0 {
		return b.MinRetention
	}
	return DefaultMinRetention
}

// Enqueue stores a message on disk and returns its durable identifier.
// Messages are delivered in the order of their CreatedAt (the source time of
// the data); when it is zero the enqueue time is used.
func (b *DiskBuffer) Enqueue(ctx context.Context, msg Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return "", fmt.Errorf("buffer: create spool: %w", err)
	}

	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.ID == "" {
		generated, err := newMessageID(msg.CreatedAt)
		if err != nil {
			return "", err
		}
		msg.ID = generated
	}

	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("buffer: encode message: %w", err)
	}

	if err := b.makeRoomLocked(int64(len(data))); err != nil {
		return "", err
	}

	if err := os.WriteFile(b.messagePath(msg.ID), data, 0o600); err != nil {
		return "", fmt.Errorf("buffer: write message: %w", err)
	}

	return msg.ID, nil
}

// makeRoomLocked applies the age and size limits before a message of size
// bytes is written. It drops only messages older than the minimum retention
// and returns ErrSpoolFull when the new message still does not fit.
func (b *DiskBuffer) makeRoomLocked(size int64) error {
	if b.MaxAge <= 0 && b.MaxBytes <= 0 {
		return nil
	}

	files, err := b.listMessageFiles()
	if err != nil {
		return err
	}

	type spoolFile struct {
		name string
		size int64
		age  time.Duration
	}
	maxAge := b.MaxAge
	if maxAge > 0 && maxAge < b.minRetention() {
		maxAge = b.minRetention()
	}
	var total int64
	kept := make([]spoolFile, 0, len(files))
	expired := Loss{Reason: LossMaxAge}
	now := time.Now()
	for _, entry := range files {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		age := now.Sub(info.ModTime())
		if maxAge > 0 && age > maxAge {
			if err := os.Remove(filepath.Join(b.Dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("buffer: drop expired message: %w", err)
			}
			expired.Messages++
			expired.Bytes += info.Size()
			continue
		}
		kept = append(kept, spoolFile{name: entry.Name(), size: info.Size(), age: age})
		total += info.Size()
	}
	b.recordLossLocked(expired)

	if b.MaxBytes <= 0 || total+size <= b.MaxBytes {
		return nil
	}
	// Over the cap: drop the oldest messages beyond the minimum retention.
	// Files are sorted oldest-first by name (source-time prefix).
	trimmed := Loss{Reason: LossSizeLimit}
	for _, f := range kept {
		if total+size <= b.MaxBytes {
			break
		}
		if f.age <= b.minRetention() {
			continue
		}
		if err := os.Remove(filepath.Join(b.Dir, f.name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("buffer: drop over-limit message: %w", err)
		}
		total -= f.size
		trimmed.Messages++
		trimmed.Bytes += f.size
	}
	b.recordLossLocked(trimmed)
	if total+size > b.MaxBytes {
		b.recordLossLocked(Loss{Messages: 1, Bytes: size, Reason: LossSpoolFull})
		return ErrSpoolFull
	}
	return nil
}

func (b *DiskBuffer) recordLossLocked(l Loss) {
	if l.Messages == 0 {
		return
	}
	b.droppedMsgs += int64(l.Messages)
	b.droppedBytes += l.Bytes
	if b.OnDrop != nil {
		b.OnDrop(l)
	}
}

// Saturated reports whether the spool has reached the high-water mark of 90 %
// of MaxBytes. Producers pause collection while it is saturated so the size
// limit is not reached (backpressure instead of loss).
func (b *DiskBuffer) Saturated(ctx context.Context) (bool, error) {
	if b.MaxBytes <= 0 {
		return false, nil
	}
	stats, err := b.Stats(ctx)
	if err != nil {
		return false, err
	}
	return stats.Bytes >= b.MaxBytes/10*9, nil
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
	// DroppedMessages and DroppedBytes count every loss of this buffer
	// instance (all reasons); they only grow.
	DroppedMessages int64
	DroppedBytes    int64
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

	stats := Stats{Messages: len(entries), DroppedMessages: b.droppedMsgs, DroppedBytes: b.droppedBytes}
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

// newMessageID derives the id from the source time, so the lexical file order
// is the delivery order (oldest data first, COL-05).
func newMessageID(sourceTime time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("buffer: generate id: %w", err)
	}

	return sourceTime.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:]), nil
}
