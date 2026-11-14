package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// The JetStream stream that takes the probe state changes and result
// batches of the collectors (E-173, PRB-05, PRB-07): the edge (WP-959)
// stores a message there before it acknowledges it to the collector, the
// workers probe-ingest (WP-960) and probe-state (WP-961) write it to the
// database. Values in docs/ops/workers.md; ADR 0002 (WP-943) may correct
// them.
const (
	// StreamName is the name of the probe stream.
	StreamName = "RETICORA_PROBE"
	// StreamSubjectPrefix is the subject root of the stream: state changes
	// go to <prefix>.state.<org>.<collector>, result batches to
	// <prefix>.results.<org>.<collector>.
	StreamSubjectPrefix = "probe"
	// StreamDuplicateWindow is the window in which the stream drops a
	// message with a Nats-Msg-Id it already holds (PRB-07). The database
	// deduplicates beyond it by (probe_id, epoch, seq) (E-171).
	StreamDuplicateWindow = 2 * time.Minute
	// StreamMaxAge bounds how long an unprocessed message stays: the
	// collector's spool holds 24 h as well (PRB-07).
	StreamMaxAge = 24 * time.Hour
	// DefaultStreamMaxBytes is the default of RETICORA_PROBE_STREAM_MAX_BYTES:
	// about two days of results at the planned load (E-173: ≈ 2.4 GB/day).
	DefaultStreamMaxBytes int64 = 5 << 30
)

// StreamConfig names the probe stream and its limits. The production values
// are DefaultStream; tests use their own name and subject root.
type StreamConfig struct {
	Name          string
	SubjectPrefix string
	MaxBytes      int64
}

// DefaultStream is the configuration of RETICORA_PROBE with maxBytes
// (RETICORA_PROBE_STREAM_MAX_BYTES; ≤ 0 takes DefaultStreamMaxBytes).
func DefaultStream(maxBytes int64) StreamConfig {
	if maxBytes <= 0 {
		maxBytes = DefaultStreamMaxBytes
	}
	return StreamConfig{Name: StreamName, SubjectPrefix: StreamSubjectPrefix, MaxBytes: maxBytes}
}

// StateSubject is the stream subject of a state change of the collector.
func (c StreamConfig) StateSubject(orgID, collectorID string) string {
	return c.SubjectPrefix + ".state." + orgID + "." + collectorID
}

// ResultsSubject is the stream subject of a result batch of the collector.
func (c StreamConfig) ResultsSubject(orgID, collectorID string) string {
	return c.SubjectPrefix + ".results." + orgID + "." + collectorID
}

// JetStream returns the stream configuration: work-queue retention (a
// message is gone once a worker acknowledged it), file storage, the
// duplicate window, 24 h maximum age and the byte limit.
func (c StreamConfig) JetStream() jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:       c.Name,
		Subjects:   []string{c.SubjectPrefix + ".state.>", c.SubjectPrefix + ".results.>"},
		Retention:  jetstream.WorkQueuePolicy,
		Storage:    jetstream.FileStorage,
		Duplicates: StreamDuplicateWindow,
		MaxAge:     StreamMaxAge,
		MaxBytes:   c.MaxBytes,
		// A full stream refuses new messages instead of dropping unprocessed
		// ones: the collector keeps them in its spool and sends them again.
		Discard: jetstream.DiscardNew,
	}
}

// EnsureStream creates the probe stream or updates its configuration; the
// server calls it at start (like the ingest stream, discovery.NewNATSIngestQueue).
func EnsureStream(ctx context.Context, js jetstream.JetStream, cfg StreamConfig) (jetstream.Stream, error) {
	stream, err := js.CreateOrUpdateStream(ctx, cfg.JetStream())
	if err != nil {
		return nil, fmt.Errorf("ensure stream %s: %w", cfg.Name, err)
	}
	return stream, nil
}
