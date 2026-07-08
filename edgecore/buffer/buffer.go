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
type DiskBuffer struct {
	Dir string
	mu  sync.Mutex
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

	return msg.ID, nil
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
