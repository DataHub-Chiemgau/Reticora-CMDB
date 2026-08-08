package buffer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestEnqueueGeneratesIDAndPersists(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	id, err := b.Enqueue(ctx, Message{Topic: "metrics", Payload: []byte("hello")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("expected generated ID, got empty string")
	}

	// Message must exist on disk as JSON with the round-tripped fields.
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read spooled message: %v", err)
	}
	var stored Message
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("spooled file is not valid JSON: %v", err)
	}
	if stored.ID != id {
		t.Errorf("stored ID = %q, want %q", stored.ID, id)
	}
	if stored.Topic != "metrics" {
		t.Errorf("stored Topic = %q, want %q", stored.Topic, "metrics")
	}
	if string(stored.Payload) != "hello" {
		t.Errorf("stored Payload = %q, want %q", stored.Payload, "hello")
	}
	if stored.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set when not provided")
	}
}

func TestEnqueuePreservesProvidedIDAndTimestamp(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	ts := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	id, err := b.Enqueue(ctx, Message{
		ID:        "fixed-id",
		Topic:     "logs",
		Payload:   []byte("x"),
		Metadata:  map[string]string{"k": "v"},
		CreatedAt: ts,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id != "fixed-id" {
		t.Errorf("ID = %q, want %q", id, "fixed-id")
	}

	msgs, err := b.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("PeekBatch returned %d messages, want 1", len(msgs))
	}
	if !msgs[0].CreatedAt.Equal(ts) {
		t.Errorf("CreatedAt = %v, want %v", msgs[0].CreatedAt, ts)
	}
	if msgs[0].Metadata["k"] != "v" {
		t.Errorf("Metadata[k] = %q, want %q", msgs[0].Metadata["k"], "v")
	}
}

func TestEnqueueGeneratesUniqueIDs(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	seen := make(map[string]struct{})
	for i := 0; i < 50; i++ {
		id, err := b.Enqueue(ctx, Message{Topic: "t"})
		if err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestEnqueueCreatesSpoolDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "spool")
	b := &DiskBuffer{Dir: dir}

	if _, err := b.Enqueue(testCtx(t), Message{Topic: "t"}); err != nil {
		t.Fatalf("Enqueue into missing dir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("spool dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("spool path is not a directory")
	}
}

func TestPeekBatchOrdersOldestFirstAndLimits(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	// Files are ordered by name, and the generated ID starts with a UTC
	// timestamp, so lexicographic order equals chronological order.
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		id, err := b.Enqueue(ctx, Message{
			ID:      time.Date(2024, 1, 1, 0, 0, i, 0, time.UTC).Format("20060102T150405.000000000Z"),
			Topic:   "t",
			Payload: []byte{byte('a' + i)},
		})
		if err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	msgs, err := b.PeekBatch(ctx, 3)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("PeekBatch returned %d messages, want 3", len(msgs))
	}
	for i, msg := range msgs {
		if msg.ID != ids[i] {
			t.Errorf("msg[%d].ID = %q, want %q (oldest-first order)", i, msg.ID, ids[i])
		}
	}

	// Peek must not delete anything.
	n, err := b.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 5 {
		t.Errorf("Len after PeekBatch = %d, want 5 (peek must not delete)", n)
	}
}

func TestPeekBatchNonPositiveLimitReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	if _, err := b.Enqueue(ctx, Message{Topic: "t"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	for _, limit := range []int{0, -1} {
		msgs, err := b.PeekBatch(ctx, limit)
		if err != nil {
			t.Fatalf("PeekBatch(%d): %v", limit, err)
		}
		if len(msgs) != 0 {
			t.Errorf("PeekBatch(%d) returned %d messages, want 0", limit, len(msgs))
		}
	}
}

func TestPeekBatchIgnoresNonMessageFiles(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	if _, err := b.Enqueue(ctx, Message{ID: "real", Topic: "t"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Stray files that must be ignored by listing.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	msgs, err := b.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "real" {
		t.Errorf("PeekBatch = %+v, want only the real message", msgs)
	}
	n, err := b.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 1 {
		t.Errorf("Len = %d, want 1", n)
	}
}

func TestAckRemovesMessages(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	var ids []string
	for i := 0; i < 3; i++ {
		id, err := b.Enqueue(ctx, Message{Topic: "t"})
		if err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	if err := b.Ack(ctx, ids[:2]); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	n, err := b.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 1 {
		t.Errorf("Len after Ack = %d, want 1", n)
	}

	msgs, err := b.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != ids[2] {
		t.Errorf("remaining message = %+v, want ID %q", msgs, ids[2])
	}
}

func TestAckUnknownIDIsNoOp(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}

	if err := b.Ack(testCtx(t), []string{"does-not-exist"}); err != nil {
		t.Fatalf("Ack of unknown ID should be a no-op, got: %v", err)
	}
}

func TestReplayCycleEnqueuePeekAck(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := testCtx(t)

	// Simulate an offline period: buffer messages while "disconnected".
	want := []string{"one", "two", "three"}
	for _, p := range want {
		if _, err := b.Enqueue(ctx, Message{Topic: "telemetry", Payload: []byte(p)}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	// Reconnect: replay everything, then ack.
	msgs, err := b.PeekBatch(ctx, 100)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(msgs) != len(want) {
		t.Fatalf("replayed %d messages, want %d", len(msgs), len(want))
	}
	got := make([]string, 0, len(msgs))
	var ids []string
	for _, m := range msgs {
		got = append(got, string(m.Payload))
		ids = append(ids, m.ID)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("replayed[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if err := b.Ack(ctx, ids); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	n, err := b.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 0 {
		t.Errorf("Len after full replay+ack = %d, want 0", n)
	}
}

func TestPersistenceAcrossBufferInstances(t *testing.T) {
	dir := t.TempDir()
	ctx := testCtx(t)

	first := &DiskBuffer{Dir: dir}
	id, err := first.Enqueue(ctx, Message{Topic: "t", Payload: []byte("durable")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// A new instance over the same directory simulates a process restart.
	second := &DiskBuffer{Dir: dir}
	msgs, err := second.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch after restart: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != id {
		t.Fatalf("messages after restart = %+v, want ID %q", msgs, id)
	}
	if string(msgs[0].Payload) != "durable" {
		t.Errorf("Payload after restart = %q, want %q", msgs[0].Payload, "durable")
	}
}

func TestContextCancelled(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := b.Enqueue(ctx, Message{Topic: "t"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Enqueue with cancelled ctx = %v, want context.Canceled", err)
	}
	if _, err := b.PeekBatch(ctx, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("PeekBatch with cancelled ctx = %v, want context.Canceled", err)
	}
	if err := b.Ack(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Ack with cancelled ctx = %v, want context.Canceled", err)
	}
	if _, err := b.Len(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Len with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestEnqueueFailsWhenDirNotCreatable(t *testing.T) {
	// A file blocks creation of the spool directory beneath it.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &DiskBuffer{Dir: filepath.Join(blocker, "spool")}

	if _, err := b.Enqueue(testCtx(t), Message{Topic: "t"}); err == nil {
		t.Fatal("expected error when spool dir cannot be created, got nil")
	} else if !strings.Contains(err.Error(), "buffer:") {
		t.Errorf("error should be wrapped with buffer context, got: %v", err)
	}
}

func TestPeekBatchCorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &DiskBuffer{Dir: dir}

	if _, err := b.PeekBatch(testCtx(t), 10); err == nil {
		t.Fatal("expected decode error for corrupt message file, got nil")
	} else if !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("error should name the corrupt file, got: %v", err)
	}
}
