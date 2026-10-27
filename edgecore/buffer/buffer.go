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
	// LossCorrupt is an undecodable spool entry; it is moved to the
	// quarantine directory so it cannot block the rest of the spool.
	LossCorrupt = "corrupt"
	// LossRejected is data the server refused for good (4xx): a poison
	// batch quarantined with Reject or Quarantine, or records and telemetry
	// the caller counts with RecordLoss.
	LossRejected = "rejected"
)

// QuarantineDir is the subdirectory of the spool that receives undecodable
// entries and batches the server refused for good, for later inspection;
// they are never delivered.
const QuarantineDir = "quarantine"

// lossFile holds the unacknowledged loss totals inside the spool directory.
// It has no ".json" suffix, so it is never listed as a message.
const lossFile = "loss.state"

// tempPrefix marks files being written; they are renamed into place only once
// their content is complete and synced, so a crash never leaves a torn entry.
const tempPrefix = ".tmp-"

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
// OnDrop so the caller can raise an event and inform the server. The loss
// totals are persisted in the spool directory, so they survive a restart, and
// are reduced only by AckLoss once the server has received them. Entries are
// written atomically (temp file, fsync, rename); an entry that cannot be
// decoded anyway is moved to QuarantineDir, counted as a loss and skipped.
// The age limit is applied on Enqueue and on PeekBatch, so it also runs while
// nothing new is spooled. Zero limits mean "unlimited".
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
	lossLoaded   bool
	droppedMsgs  int64
	droppedBytes int64
}

// lossState is the persisted form of the unacknowledged loss totals.
type lossState struct {
	Messages int64 `json:"messages"`
	Bytes    int64 `json:"bytes"`
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

	if err := b.writeFileAtomic(b.messagePath(msg.ID), data); err != nil {
		return "", fmt.Errorf("buffer: write message: %w", err)
	}

	return msg.ID, nil
}

// writeFileAtomic writes data to a temporary file in the spool directory,
// syncs it and renames it to path, then syncs the directory. Readers see
// either the old state or the complete new file, never a partial one.
func (b *DiskBuffer) writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(b.Dir, tempPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return syncDir(b.Dir)
}

// syncDir makes a rename or create in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	return err
}

// makeRoomLocked applies the age and size limits before a message of size
// bytes is written. It drops only messages older than the minimum retention
// and returns ErrSpoolFull when the new message still does not fit.
func (b *DiskBuffer) makeRoomLocked(size int64) error {
	if b.MaxAge <= 0 && b.MaxBytes <= 0 {
		return nil
	}

	kept, err := b.expireLocked()
	if err != nil {
		return err
	}
	var total int64
	for _, f := range kept {
		total += f.size
	}

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
	if err := b.recordLossLocked(trimmed); err != nil {
		return err
	}
	if total+size > b.MaxBytes {
		if err := b.recordLossLocked(Loss{Messages: 1, Bytes: size, Reason: LossSpoolFull}); err != nil {
			return fmt.Errorf("%w; %w", ErrSpoolFull, err)
		}
		return ErrSpoolFull
	}
	return nil
}

// spoolFile is a message file with its size and age.
type spoolFile struct {
	name string
	size int64
	age  time.Duration
}

// expireLocked drops messages older than max(MaxAge, MinRetention), records
// that loss and returns the remaining message files, oldest first.
func (b *DiskBuffer) expireLocked() ([]spoolFile, error) {
	files, err := b.listMessageFiles()
	if err != nil {
		return nil, err
	}

	maxAge := b.MaxAge
	if maxAge > 0 && maxAge < b.minRetention() {
		maxAge = b.minRetention()
	}
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
				return nil, fmt.Errorf("buffer: drop expired message: %w", err)
			}
			expired.Messages++
			expired.Bytes += info.Size()
			continue
		}
		kept = append(kept, spoolFile{name: entry.Name(), size: info.Size(), age: age})
	}
	if err := b.recordLossLocked(expired); err != nil {
		return nil, err
	}
	return kept, nil
}

// recordLossLocked adds a loss to the totals, persists them and reports the
// loss to OnDrop. The in-memory totals keep the loss even when persisting
// fails, so the next successful write includes it.
func (b *DiskBuffer) recordLossLocked(l Loss) error {
	if l.Messages == 0 {
		return nil
	}
	if err := b.loadLossLocked(); err != nil {
		return err
	}
	b.droppedMsgs += int64(l.Messages)
	b.droppedBytes += l.Bytes
	if b.OnDrop != nil {
		b.OnDrop(l)
	}
	return b.saveLossLocked()
}

// loadLossLocked reads the persisted loss totals once per instance. A loss
// file that cannot be decoded is quarantined; the totals then restart at
// zero rather than blocking the spool.
func (b *DiskBuffer) loadLossLocked() error {
	if b.lossLoaded {
		return nil
	}
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return fmt.Errorf("buffer: create spool: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(b.Dir, lossFile))
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return fmt.Errorf("buffer: read loss totals: %w", err)
	default:
		var st lossState
		if json.Unmarshal(data, &st) != nil || st.Messages < 0 || st.Bytes < 0 {
			if err := b.quarantineLocked(lossFile); err != nil {
				return err
			}
		} else {
			b.droppedMsgs += st.Messages
			b.droppedBytes += st.Bytes
		}
	}
	b.lossLoaded = true
	return nil
}

// saveLossLocked persists the loss totals atomically.
func (b *DiskBuffer) saveLossLocked() error {
	data, err := json.Marshal(lossState{Messages: b.droppedMsgs, Bytes: b.droppedBytes})
	if err != nil {
		return fmt.Errorf("buffer: encode loss totals: %w", err)
	}
	if err := b.writeFileAtomic(filepath.Join(b.Dir, lossFile), data); err != nil {
		return fmt.Errorf("buffer: persist loss totals: %w", err)
	}
	return nil
}

// AckLoss reduces the persisted loss totals by the amounts the server has
// acknowledged (the Stats.DroppedMessages/DroppedBytes it received). Losses
// recorded after that report stay pending for the next one.
func (b *DiskBuffer) AckLoss(ctx context.Context, messages, bytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if messages <= 0 && bytes <= 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.loadLossLocked(); err != nil {
		return err
	}
	b.droppedMsgs = max(b.droppedMsgs-max(messages, 0), 0)
	b.droppedBytes = max(b.droppedBytes-max(bytes, 0), 0)
	return b.saveLossLocked()
}

// RecordLoss counts a loss the caller observed itself (e.g. records the
// server rejected in an accepted batch). It is persisted and reported to
// OnDrop like the spool's own losses and acknowledged with AckLoss.
func (b *DiskBuffer) RecordLoss(ctx context.Context, l Loss) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.Messages <= 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.recordLossLocked(l)
}

// Reject moves a spooled message the server refused for good (a poison
// batch) to QuarantineDir and counts it as LossRejected, so the delivery
// continues with the next message instead of retrying it forever.
func (b *DiskBuffer) Reject(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := id + ".json"
	if filepath.Base(name) != name {
		return fmt.Errorf("buffer: invalid message id %q", id)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	info, err := os.Stat(filepath.Join(b.Dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("buffer: reject %s: %w", id, err)
	}
	if err := b.quarantineLocked(name); err != nil {
		return err
	}
	return b.recordLossLocked(Loss{Messages: 1, Bytes: info.Size(), Reason: LossRejected})
}

// Quarantine stores a message that was never spooled but that the server
// refused for good in QuarantineDir and counts it as LossRejected. The
// spool's size limit does not apply; nothing in the spool is dropped for it.
func (b *DiskBuffer) Quarantine(ctx context.Context, in *Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := *in
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.ID == "" {
		id, err := newMessageID(msg.CreatedAt)
		if err != nil {
			return err
		}
		msg.ID = id
	}
	name := msg.ID + ".json"
	if filepath.Base(name) != name {
		return fmt.Errorf("buffer: invalid message id %q", msg.ID)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("buffer: encode message: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	dir := filepath.Join(b.Dir, QuarantineDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("buffer: create quarantine: %w", err)
	}
	if err := b.writeFileAtomic(filepath.Join(dir, name), data); err != nil {
		return fmt.Errorf("buffer: quarantine %s: %w", name, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("buffer: quarantine %s: %w", name, err)
	}
	return b.recordLossLocked(Loss{Messages: 1, Bytes: int64(len(data)), Reason: LossRejected})
}

// quarantineLocked moves a file of the spool directory to QuarantineDir.
func (b *DiskBuffer) quarantineLocked(name string) error {
	dir := filepath.Join(b.Dir, QuarantineDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("buffer: create quarantine: %w", err)
	}
	if err := os.Rename(filepath.Join(b.Dir, name), filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("buffer: quarantine %s: %w", name, err)
	}
	return syncDir(b.Dir)
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
// It first applies the age limit, so expiry does not depend on new data
// being spooled or on a successful flush. An entry that cannot be read or
// decoded, or whose id does not match its file name (Ack could never remove
// it), is quarantined, counted as a LossCorrupt and skipped; the batch is
// filled from the following entries in source-time order.
func (b *DiskBuffer) PeekBatch(ctx context.Context, limit int) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []Message{}, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	entries, err := b.expireLocked()
	if err != nil {
		return nil, err
	}

	out := make([]Message, 0, min(len(entries), limit))
	corrupt := Loss{Reason: LossCorrupt}
	for _, entry := range entries {
		if len(out) == limit {
			break
		}
		data, err := os.ReadFile(filepath.Join(b.Dir, entry.name))
		if os.IsNotExist(err) {
			continue
		}

		var msg Message
		if err == nil {
			err = json.Unmarshal(data, &msg)
		}
		if err == nil && msg.ID+".json" != entry.name {
			err = fmt.Errorf("id %q does not match the file name", msg.ID)
		}
		if err != nil {
			if qErr := b.quarantineLocked(entry.name); qErr != nil {
				return nil, fmt.Errorf("buffer: decode message %s: %w (%w)", entry.name, err, qErr)
			}
			corrupt.Messages++
			corrupt.Bytes += entry.size
			continue
		}
		out = append(out, msg)
	}
	if err := b.recordLossLocked(corrupt); err != nil {
		return nil, err
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
	// DroppedMessages and DroppedBytes count every loss (all reasons) not yet
	// acknowledged with AckLoss. They are persisted in the spool directory
	// and survive a restart.
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
	if err := b.loadLossLocked(); err != nil {
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
