//go:build auditdefect

package buffer_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-062 (ING-04/GLO-12 "Spool ohne stillen Verlust": 24-h buffering,
// backpressure, loss report) P2 "Spool loss counters are not persisted;
// losses before a restart never reach the server": DiskBuffer keeps
// droppedMsgs/droppedBytes in memory only, and the collector heartbeat
// reports the current instance's Stats. Requirement: an unavoidable loss
// (spool_full) is still reported after a restart on the same spool
// directory, until the server has acknowledged it.
//
// WP-062 P2 "Head-of-line blocking: a corrupt spool file ... stops all
// delivery": PeekBatch aborts on the first undecodable file, so a torn head
// file blocks the whole backlog forever. Requirement: an unreadable entry is
// quarantined and counted as a loss, and the remaining backlog is delivered.
//
// Retest:
//   cd edgecore && go test -tags auditdefect -count=1 -v -run 'TestAuditT3SpoolLossSurvivesRestart|TestAuditT3CorruptHeadDoesNotBlockSpool' ./buffer/

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
)

func TestAuditT3SpoolLossSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := &buffer.DiskBuffer{Dir: dir}
	if _, err := b.Enqueue(ctx, buffer.Message{Topic: "discovery", Payload: []byte("aaaa")}); err != nil {
		t.Fatal(err)
	}
	st, err := b.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.MaxBytes = st.Bytes
	if _, err = b.Enqueue(ctx, buffer.Message{Topic: "discovery", Payload: []byte("bbbb")}); err == nil {
		t.Fatalf("precondition: enqueue at the cap succeeded")
	}
	if st, err = b.Stats(ctx); err != nil || st.DroppedMessages != 1 {
		t.Fatalf("precondition: dropped before restart = %d (%v), want 1", st.DroppedMessages, err)
	}

	restarted := &buffer.DiskBuffer{Dir: dir, MaxBytes: b.MaxBytes}
	st2, err := restarted.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st2.DroppedMessages < 1 {
		t.Errorf("after a restart on the same spool directory DroppedMessages = %d, want >= 1: the loss is never reported to the server", st2.DroppedMessages)
	}
}

func TestAuditT3CorruptHeadDoesNotBlockSpool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := &buffer.DiskBuffer{Dir: dir}
	first, err := b.Enqueue(ctx, buffer.Message{Topic: "discovery", Payload: []byte("first")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Enqueue(ctx, buffer.Message{Topic: "discovery", Payload: []byte("second")}); err != nil {
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
