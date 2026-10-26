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

// F-085: an undecodable entry, or one whose id does not match its file name
// (Ack could never remove it), is moved to quarantine/, counted as a loss and
// skipped; the batch is filled from the following entries.
func TestPeekBatchQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0-bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1-wrongid.json"), []byte(`{"id":"other","topic":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var losses []Loss
	b := &DiskBuffer{Dir: dir, OnDrop: func(l Loss) { losses = append(losses, l) }}
	ctx := testCtx(t)
	for _, id := range []string{"2-a", "3-b"} {
		if _, err := b.Enqueue(ctx, Message{ID: id, Topic: "t"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	msgs, err := b.PeekBatch(ctx, 1)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "2-a" {
		t.Fatalf("PeekBatch(1) = %+v, want the first intact message", msgs)
	}
	for _, name := range []string{"0-bad.json", "1-wrongid.json"} {
		if _, statErr := os.Stat(filepath.Join(dir, QuarantineDir, name)); statErr != nil {
			t.Errorf("%s not in quarantine: %v", name, statErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Errorf("%s still in the spool: %v", name, statErr)
		}
	}
	if len(losses) != 1 || losses[0].Reason != LossCorrupt || losses[0].Messages != 2 || losses[0].Bytes == 0 {
		t.Fatalf("losses = %+v, want one corrupt loss of 2 messages", losses)
	}
	if n, _ := b.Len(ctx); n != 2 {
		t.Fatalf("Len = %d, want the 2 intact messages", n)
	}
	stats, err := b.Stats(ctx)
	if err != nil || stats.DroppedMessages != 2 || stats.Messages != 2 {
		t.Fatalf("stats = %+v (%v), want 2 messages and 2 dropped", stats, err)
	}
}

// F-084: the loss totals live in the spool directory, survive a restart and
// are reduced only by what the server acknowledged; a loss recorded after the
// report stays pending.
func TestAckLossReducesOnlyAcknowledgedTotals(t *testing.T) {
	dir := t.TempDir()
	ctx := testCtx(t)
	b := &DiskBuffer{Dir: dir}
	if _, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("aaaa")}); err != nil {
		t.Fatal(err)
	}
	b.MaxBytes = spoolSize(t, dir)
	if _, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("bbbb")}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("expected ErrSpoolFull, got %v", err)
	}
	reported, err := b.Stats(ctx)
	if err != nil || reported.DroppedMessages != 1 || reported.DroppedBytes == 0 {
		t.Fatalf("stats = %+v (%v), want one loss", reported, err)
	}
	// A further loss after the heartbeat was sent but before it was acked.
	if _, err = b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("cccc")}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("expected ErrSpoolFull, got %v", err)
	}

	restarted := &DiskBuffer{Dir: dir, MaxBytes: b.MaxBytes}
	if err = restarted.AckLoss(ctx, reported.DroppedMessages, reported.DroppedBytes); err != nil {
		t.Fatalf("AckLoss: %v", err)
	}
	st, err := restarted.Stats(ctx)
	if err != nil || st.DroppedMessages != 1 || st.DroppedBytes == 0 {
		t.Fatalf("after ack stats = %+v (%v), want the later loss still pending", st, err)
	}
	if err = restarted.AckLoss(ctx, st.DroppedMessages, st.DroppedBytes); err != nil {
		t.Fatalf("AckLoss: %v", err)
	}
	again := &DiskBuffer{Dir: dir}
	if st, err = again.Stats(ctx); err != nil || st.DroppedMessages != 0 || st.DroppedBytes != 0 {
		t.Fatalf("after full ack and restart stats = %+v (%v), want zero", st, err)
	}
	// Acknowledging more than is pending never goes negative.
	if err = again.AckLoss(ctx, 5, 500); err != nil {
		t.Fatalf("AckLoss: %v", err)
	}
	if st, _ = again.Stats(ctx); st.DroppedMessages != 0 || st.DroppedBytes != 0 {
		t.Fatalf("over-ack stats = %+v, want zero", st)
	}
}

// WP-483: a spooled batch the server refused for good is moved to the
// quarantine and counted; a refused batch that was never spooled is written
// to the quarantine and counted; RecordLoss counts a loss the caller saw.
// All three are persisted and reported to OnDrop as LossRejected.
func TestRejectQuarantineAndRecordLossCount(t *testing.T) {
	dir := t.TempDir()
	ctx := testCtx(t)
	var reasons []string
	b := &DiskBuffer{Dir: dir, OnDrop: func(l Loss) { reasons = append(reasons, l.Reason) }}
	poison, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("poison")})
	if err != nil {
		t.Fatal(err)
	}
	next, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("next")})
	if err != nil {
		t.Fatal(err)
	}
	size := spoolSize(t, dir)
	if err = b.Reject(ctx, poison); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if err = b.Reject(ctx, poison); err != nil {
		t.Fatalf("second Reject: %v", err)
	}
	if err = b.Reject(ctx, "../loss"); err == nil {
		t.Fatal("Reject accepted an id outside the spool")
	}
	msgs, err := b.PeekBatch(ctx, 10)
	if err != nil || len(msgs) != 1 || msgs[0].ID != next {
		t.Fatalf("spool after Reject = %+v (%v), want only the next message", msgs, err)
	}
	if _, err = os.Stat(filepath.Join(dir, QuarantineDir, poison+".json")); err != nil {
		t.Fatalf("rejected message not quarantined: %v", err)
	}

	if err = b.Quarantine(ctx, &Message{Topic: "t", Payload: []byte("fresh")}); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if spoolSize(t, dir) >= size {
		t.Fatalf("spool grew by the quarantined message")
	}
	if err = b.RecordLoss(ctx, Loss{Messages: 2, Reason: LossRejected}); err != nil {
		t.Fatalf("RecordLoss: %v", err)
	}
	if err = b.RecordLoss(ctx, Loss{}); err != nil {
		t.Fatalf("empty RecordLoss: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, QuarantineDir))
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine = %d entries (%v), want 2", len(entries), err)
	}
	st, err := (&DiskBuffer{Dir: dir}).Stats(ctx)
	if err != nil || st.Messages != 1 || st.DroppedMessages != 4 || st.DroppedBytes == 0 {
		t.Fatalf("stats after restart = %+v (%v), want 1 message and 4 losses", st, err)
	}
	if len(reasons) != 3 || reasons[0] != LossRejected || reasons[1] != LossRejected || reasons[2] != LossRejected {
		t.Fatalf("OnDrop reasons = %v, want 3x %s", reasons, LossRejected)
	}
}

// Entries and loss totals are written through a temp file and renamed into
// place: no temp file remains, and every file in the spool is complete.
func TestEnqueueWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	ctx := testCtx(t)
	b := &DiskBuffer{Dir: dir}
	id, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("payload")})
	if err != nil {
		t.Fatal(err)
	}
	b.MaxBytes = 1
	if _, err = b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("x")}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("expected ErrSpoolFull, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("temp file %s left in the spool", e.Name())
		}
	}
	if len(names) != 2 || names[0] != id+".json" || names[1] != lossFile {
		t.Fatalf("spool files = %v, want the message and the loss totals", names)
	}
	data, err := os.ReadFile(filepath.Join(dir, lossFile))
	if err != nil {
		t.Fatal(err)
	}
	var st lossState
	if err = json.Unmarshal(data, &st); err != nil || st.Messages != 1 || st.Bytes == 0 {
		t.Fatalf("loss totals = %s (%v), want one message", data, err)
	}
	// A failing write leaves neither the target nor a temp file behind.
	if err = os.Mkdir(filepath.Join(dir, "blocked.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = b.writeFileAtomic(filepath.Join(dir, "blocked.json"), []byte("x")); err == nil {
		t.Fatal("rename onto a directory should fail")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, tempPrefix+"*")); len(matches) != 0 {
		t.Fatalf("temp files left after a failed write: %v", matches)
	}
}

// A torn loss file is quarantined instead of blocking the spool.
func TestCorruptLossFileIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lossFile), []byte(`{"messages":`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &DiskBuffer{Dir: dir}
	st, err := b.Stats(testCtx(t))
	if err != nil || st.DroppedMessages != 0 {
		t.Fatalf("stats = %+v (%v), want zero totals", st, err)
	}
	if _, err = os.Stat(filepath.Join(dir, QuarantineDir, lossFile)); err != nil {
		t.Fatalf("loss file not quarantined: %v", err)
	}
}

// backdate rewinds the modification time of a spooled message.
func backdate(t *testing.T, dir, id string, age time.Duration) {
	t.Helper()
	stale := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(dir, id+".json"), stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func spoolSize(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var total int64
	for _, e := range entries {
		// Only message files count towards the spool size; the loss totals
		// and the quarantine directory are bookkeeping.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		total += info.Size()
	}
	return total
}

// NFR-04: a MaxAge below 24 h never drops unacknowledged messages younger than
// 24 h; older messages are dropped and reported as loss.
func TestDiskBufferMaxAgeKeepsMessagesFor24Hours(t *testing.T) {
	dir := t.TempDir()
	var losses []Loss
	b := &DiskBuffer{Dir: dir, MaxAge: time.Hour, OnDrop: func(l Loss) { losses = append(losses, l) }}
	ctx := context.Background()

	youngID, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("23h")})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	backdate(t, dir, youngID, 23*time.Hour)
	oldID, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("25h")})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	backdate(t, dir, oldID, 25*time.Hour)

	if _, err = b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("fresh")}); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}
	msgs, err := b.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected the 23 h old and the fresh message, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.ID == oldID {
			t.Fatal("message older than 24 h and MaxAge should be dropped")
		}
	}
	if len(losses) != 1 || losses[0].Reason != LossMaxAge || losses[0].Messages != 1 {
		t.Fatalf("losses = %+v, want one max_age loss", losses)
	}
	stats, _ := b.Stats(ctx)
	if stats.DroppedMessages != 1 || stats.DroppedBytes == 0 {
		t.Fatalf("stats = %+v, want the loss counted", stats)
	}
}

// At the size limit the spool applies backpressure: young messages are never
// dropped, the new message is refused with ErrSpoolFull and counted.
func TestDiskBufferSizeLimitAppliesBackpressure(t *testing.T) {
	dir := t.TempDir()
	var losses []Loss
	b := &DiskBuffer{Dir: dir, OnDrop: func(l Loss) { losses = append(losses, l) }}
	ctx := context.Background()
	var ids []string
	for _, payload := range []string{"aaaa", "bbbb", "cccc"} {
		id, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte(payload)})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		ids = append(ids, id)
	}
	b.MaxBytes = spoolSize(t, dir) + 10

	if saturated, err := b.Saturated(ctx); err != nil || !saturated {
		t.Fatalf("spool at the cap must be saturated, got %v (%v)", saturated, err)
	}
	if _, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("dddd")}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("expected ErrSpoolFull, got %v", err)
	}
	if n, _ := b.Len(ctx); n != 3 {
		t.Fatalf("young messages must be kept, got %d", n)
	}
	if len(losses) != 1 || losses[0].Reason != LossSpoolFull {
		t.Fatalf("losses = %+v, want one spool_full loss", losses)
	}

	// Messages older than 24 h may make room; that loss is reported too.
	backdate(t, dir, ids[0], 25*time.Hour)
	if _, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("eeee")}); err != nil {
		t.Fatalf("enqueue after the old message can be dropped: %v", err)
	}
	if len(losses) != 2 || losses[1].Reason != LossSizeLimit || losses[1].Messages != 1 {
		t.Fatalf("losses = %+v, want a size_limit loss for the old message", losses)
	}
	if after := spoolSize(t, dir); after > b.MaxBytes {
		t.Fatalf("spool grew past the cap: %d > %d", after, b.MaxBytes)
	}
}

// COL-05: delivery follows the source time, not the enqueue order.
func TestPeekBatchOrdersBySourceTime(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, offset := range []int{3, 1, 2} {
		if _, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte{byte(offset)}, CreatedAt: base.Add(time.Duration(offset) * time.Minute)}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	msgs, err := b.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	for i, m := range msgs {
		if int(m.Payload[0]) != i+1 {
			t.Fatalf("message %d has source offset %d, want ascending source time", i, m.Payload[0])
		}
	}
}

func TestDiskBufferUnlimitedByDefault(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	for i := 0; i < 5; i++ {
		if _, err := b.Enqueue(context.Background(), Message{Topic: "t", Payload: []byte("x")}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	n, _ := b.Len(context.Background())
	if n != 5 {
		t.Fatalf("expected 5 messages, got %d", n)
	}
}

func TestStatsEmptySpool(t *testing.T) {
	b := &DiskBuffer{Dir: t.TempDir()}
	stats, err := b.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != 0 || stats.Bytes != 0 || stats.OldestAge != 0 {
		t.Fatalf("empty spool stats = %+v, want zero", stats)
	}
}

func TestStatsReportsCountsAndOldestAge(t *testing.T) {
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	payloads := []string{"aa", "bbbb", "cccccc"}
	var wantBytes int64
	for _, p := range payloads {
		if _, err := b.Enqueue(context.Background(), Message{Topic: "t", Payload: []byte(p)}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		wantBytes += info.Size()
	}

	stats, err := b.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != len(payloads) {
		t.Errorf("Messages = %d, want %d", stats.Messages, len(payloads))
	}
	if stats.Bytes != wantBytes {
		t.Errorf("Bytes = %d, want %d", stats.Bytes, wantBytes)
	}
	if stats.OldestAge <= 0 || stats.OldestAge > time.Minute {
		t.Errorf("OldestAge = %v, want small positive duration", stats.OldestAge)
	}

	if err := b.Ack(context.Background(), []string{}); err != nil {
		t.Fatalf("ack noop: %v", err)
	}
	stats, _ = b.Stats(context.Background())
	if stats.Messages != len(payloads) {
		t.Errorf("after noop ack Messages = %d, want %d", stats.Messages, len(payloads))
	}
}

func TestStatsContextCancelled(t *testing.T) {
	b := &DiskBuffer{Dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Stats(ctx); err == nil {
		t.Error("Stats with cancelled context should fail")
	}
}

// NFR-04/F-085: the age limit also runs in the flusher (PeekBatch), so expired
// data is dropped and counted even when nothing new is spooled and no flush
// succeeds; messages younger than 24 h are kept.
func TestPeekBatchExpiresWithoutEnqueue(t *testing.T) {
	dir := t.TempDir()
	ctx := testCtx(t)
	var losses []Loss
	b := &DiskBuffer{Dir: dir, MaxAge: time.Hour, OnDrop: func(l Loss) { losses = append(losses, l) }}
	youngID, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("23h")})
	if err != nil {
		t.Fatal(err)
	}
	oldID, err := b.Enqueue(ctx, Message{Topic: "t", Payload: []byte("25h")})
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, dir, youngID, 23*time.Hour)
	backdate(t, dir, oldID, 25*time.Hour)

	for attempt := 0; attempt < 2; attempt++ {
		msgs, err := b.PeekBatch(ctx, 10)
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != youngID {
			t.Fatalf("attempt %d: PeekBatch = %+v, want only the 23 h old message", attempt, msgs)
		}
	}
	if len(losses) != 1 || losses[0].Reason != LossMaxAge || losses[0].Messages != 1 {
		t.Fatalf("losses = %+v, want one max_age loss", losses)
	}
}

// F-084 (moved from audit_t3_spool_loss_auditdefect_test.go): an unavoidable
// loss (spool_full) is still reported after a restart on the same spool
// directory, until the server has acknowledged it.
func TestAuditT3SpoolLossSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	if _, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("aaaa")}); err != nil {
		t.Fatal(err)
	}
	st, err := b.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.MaxBytes = st.Bytes
	if _, err = b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("bbbb")}); err == nil {
		t.Fatalf("precondition: enqueue at the cap succeeded")
	}
	if st, err = b.Stats(ctx); err != nil || st.DroppedMessages != 1 {
		t.Fatalf("precondition: dropped before restart = %d (%v), want 1", st.DroppedMessages, err)
	}

	restarted := &DiskBuffer{Dir: dir, MaxBytes: b.MaxBytes}
	st2, err := restarted.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st2.DroppedMessages < 1 {
		t.Errorf("after a restart on the same spool directory DroppedMessages = %d, want >= 1: the loss is never reported to the server", st2.DroppedMessages)
	}
}

// F-085 (moved from audit_t3_spool_loss_auditdefect_test.go): a torn head
// file is quarantined and counted as a loss; the remaining backlog is
// delivered.
func TestAuditT3CorruptHeadDoesNotBlockSpool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := &DiskBuffer{Dir: dir}
	first, err := b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("first")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Enqueue(ctx, Message{Topic: "discovery", Payload: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	torn := 0
	for _, path := range files {
		if strings.HasPrefix(filepath.Base(path), first) {
			if err = os.WriteFile(path, []byte(`{"id":"`+first+`","topic":"disc`), 0o600); err != nil {
				t.Fatal(err)
			}
			torn++
		}
	}
	if torn != 1 {
		t.Fatalf("precondition: torn %d spool files for %s, want 1", torn, first)
	}

	// The corrupt head may cost one attempt (quarantine), not the backlog.
	var delivered bool
	var lastErr error
	for attempt := 0; attempt < 3 && !delivered; attempt++ {
		msgs, peekErr := b.PeekBatch(ctx, 64)
		lastErr = peekErr
		for _, m := range msgs {
			if string(m.Payload) == "second" {
				delivered = true
			}
		}
	}
	if !delivered {
		t.Errorf("intact spooled message behind a corrupt head file is never returned by PeekBatch (last error: %v): head-of-line blocking", lastErr)
	}
	if st, statErr := b.Stats(ctx); statErr != nil || st.DroppedMessages < 1 {
		t.Errorf("corrupt spool entry not counted as a loss: DroppedMessages=%d (%v), want >= 1", st.DroppedMessages, statErr)
	}
}
