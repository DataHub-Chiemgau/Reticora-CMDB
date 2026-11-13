package probe

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Persistent probe spool (PRB-07, NFR-04, COL-05, WP-954): two disk queues
// (edgecore/buffer.DiskBuffer, atomic writes) below the spool directory of
// the collector, next to the discovery spool:
//
//   - <SpoolDir>/probe-state: every state change as an entry of its own,
//     written before EnqueueState returns. MinRetention 24 h and no MaxAge:
//     a state change younger than 24 h is never displaced; with the state
//     budget (10 % of the total) exhausted EnqueueState fails with
//     buffer.ErrSpoolFull (backpressure).
//   - <SpoolDir>/probe-results: the results, bundled every second or at
//     1,000 results into one entry (at most about one file per second); the
//     entry is a gzip-compressed probewire.ResultBatch, its CreatedAt the
//     observed_at of the first result, its metadata seq_from and seq_to.
//     At 80 % of the result budget the oldest bundles are compacted
//     (Compact: per probe one result per 5 minutes plus every failure and
//     every result of a state change); at the limit a new bundle is
//     refused (buffer.ErrSpoolFull) and dropped.
//
// The entries of a queue are named by the sequence number of their first
// message, so a queue is delivered in sequence order whatever the
// observed_at of a result. Every loss is counted per reason (max_bytes,
// compacted, quarantined) and persisted; Stats hands the counts to the
// alive message (WP-957). Sending and acknowledging is WP-955.

// Directories of the probe spool below the collector's spool directory.
const (
	OutboxStateDir   = "probe-state"
	OutboxResultsDir = "probe-results"
)

// DefaultOutboxMaxBytes is the default of RETICORA_PROBE_SPOOL_MAX_BYTES:
// 2 GiB for both queues together, a tenth of it for the state changes.
const DefaultOutboxMaxBytes int64 = 2 << 30

// outboxStateShare is the share (1/n) of the budget for the state queue.
const outboxStateShare = 10

// Bundling of the results.
const (
	// OutboxBundleResults bundles at this many results.
	OutboxBundleResults = probewire.MaxBatchResults
	// OutboxBundleInterval bundles at least this often (Run).
	OutboxBundleInterval = time.Second
	// outboxCompactAt starts the compaction at this share (percent) of the
	// result budget.
	outboxCompactAt = 80
	// outboxCompactWindow is the most bundles compacted together.
	outboxCompactWindow = 900
	// outboxPendingCap bounds the results held in memory while the disk
	// refuses writes other than the size limit; the oldest beyond go as
	// max_bytes.
	outboxPendingCap = 10 * OutboxBundleResults
	// outboxCheckEvery is how often a flush checks the budget of the
	// result queue.
	outboxCheckEvery = 10 * time.Second
)

// resultsRetention keeps the result bundles from being dropped by the
// DiskBuffer itself: the outbox compacts them and refuses new bundles at
// the limit, so every loss of results is counted with its results.
const resultsRetention = 100 * 365 * 24 * time.Hour

// Loss reasons of the probe spool (WP-954 AK5).
const (
	// OutboxLossMaxBytes: refused at the size limit (a result bundle,
	// counted per result) or displaced beyond 24 h (a state change).
	OutboxLossMaxBytes = "max_bytes"
	// OutboxLossCompacted: results left out by the compaction; they are no
	// loss of information about the state.
	OutboxLossCompacted = "compacted"
	// OutboxLossQuarantined: an entry that could not be decoded or that
	// the server refused for good (counted per entry).
	OutboxLossQuarantined = "quarantined"
)

// Metadata keys of a result bundle.
const (
	MetaSeqFrom     = "seq_from"
	MetaSeqTo       = "seq_to"
	MetaCount       = "count"
	MetaEncoding    = "encoding"
	MetaStateSeqs   = "state_seqs"
	MetaCompacted   = "compacted"
	encodingGzip    = "gzip"
	topicProbeState = "probe.state"
	topicResults    = "probe.results"
	outboxLossFile  = "outbox-loss.state"
)

// Outbox is the persistent probe spool of a collector.
type Outbox struct {
	states  *buffer.DiskBuffer
	results *buffer.DiskBuffer
	budget  int64

	mu           sync.Mutex
	pending      []probewire.Result
	pendingState map[uint64]bool
	// checkEvery bounds how often a flush reads the size of the result
	// queue for the compaction (a directory scan); lastCheck is the last.
	checkEvery time.Duration
	lastCheck  time.Time
	// compacted is the id of the last result bundle the compaction went
	// past (compacted, or nothing to leave out); the next compaction reads
	// only the bundles after it (DiskBuffer.PeekAfter). It starts empty:
	// after a restart the first compaction walks the queue once.
	compacted string
	// compactWindow is the most bundles compacted together
	// (outboxCompactWindow; smaller in tests).
	compactWindow int

	lossMu   sync.Mutex
	loss     map[string]int64
	lossPath string
}

// OpenOutbox opens the probe spool below spoolDir with maxBytes for both
// queues (0 or less: DefaultOutboxMaxBytes); the loss counts of an earlier
// run are read back.
func OpenOutbox(spoolDir string, maxBytes int64) (*Outbox, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultOutboxMaxBytes
	}
	stateBudget := maxBytes / outboxStateShare
	o := &Outbox{budget: maxBytes - stateBudget, pendingState: map[uint64]bool{}, loss: map[string]int64{},
		checkEvery: outboxCheckEvery, compactWindow: outboxCompactWindow}
	o.states = &buffer.DiskBuffer{Dir: filepath.Join(spoolDir, OutboxStateDir), MaxBytes: stateBudget,
		MinRetention: buffer.DefaultMinRetention, OnDrop: o.onDrop(true)}
	o.results = &buffer.DiskBuffer{Dir: filepath.Join(spoolDir, OutboxResultsDir), MaxBytes: o.budget,
		MinRetention: resultsRetention, OnDrop: o.onDrop(false)}
	for _, dir := range []string{o.states.Dir, o.results.Dir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("probe: create spool: %w", err)
		}
	}
	o.lossPath = filepath.Join(o.results.Dir, outboxLossFile)
	data, err := os.ReadFile(o.lossPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("probe: read spool losses: %w", err)
	default:
		if jerr := json.Unmarshal(data, &o.loss); jerr != nil {
			slog.Warn("probe spool loss counts unreadable; they restart at zero", "error", jerr)
			o.loss = map[string]int64{}
		}
	}
	return o, nil
}

// States is the queue of the state changes (for the uplink, WP-955).
func (o *Outbox) States() *buffer.DiskBuffer { return o.states }

// Results is the queue of the result bundles (for the uplink, WP-955).
func (o *Outbox) Results() *buffer.DiskBuffer { return o.results }

// onDrop maps the losses of a DiskBuffer to the reasons of the outbox. A
// result bundle refused at the limit is counted by flushLocked with its
// results.
func (o *Outbox) onDrop(state bool) func(buffer.Loss) {
	return func(l buffer.Loss) {
		switch l.Reason {
		case buffer.LossSizeLimit, buffer.LossMaxAge:
			o.count(OutboxLossMaxBytes, int64(l.Messages))
		case buffer.LossSpoolFull:
			if state {
				o.count(OutboxLossMaxBytes, int64(l.Messages))
			}
		case buffer.LossCorrupt, buffer.LossRejected:
			o.count(OutboxLossQuarantined, int64(l.Messages))
		}
	}
}

// count adds n to a loss reason and persists the counts.
func (o *Outbox) count(reason string, n int64) {
	if n <= 0 {
		return
	}
	o.lossMu.Lock()
	defer o.lossMu.Unlock()
	o.loss[reason] += n
	data, err := json.Marshal(o.loss)
	if err == nil {
		err = writeAtomic(o.lossPath, data)
	}
	if err != nil {
		slog.Warn("probe spool loss counts not persisted", "error", err)
	}
}

// writeAtomic writes data to path through a synced temporary file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-loss-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// seqID names an entry by its first sequence number (zero-padded, so the
// file order is the sequence order).
func seqID(seq uint64) string { return fmt.Sprintf("%020d", seq) }

// EnqueueState writes a state change before it returns (WP-954 AK1). At
// the state budget it fails with buffer.ErrSpoolFull; nothing younger
// than 24 h is displaced for it.
func (o *Outbox) EnqueueState(ctx context.Context, c *probewire.StateChange) error {
	payload, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("probe: encode state change: %w", err)
	}
	_, err = o.states.Enqueue(ctx, &buffer.Message{ID: seqID(c.Seq), Topic: topicProbeState, Payload: payload,
		CreatedAt: c.ObservedAt, Metadata: map[string]string{MetaSeqFrom: strconv.FormatUint(c.Seq, 10)}})
	return err
}

// AddResult adds a result to the bundle being collected; stateRelated
// marks the last result of a state change (kept by the compaction). At
// OutboxBundleResults the bundle is written.
func (o *Outbox) AddResult(ctx context.Context, r *probewire.Result, stateRelated bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pending = append(o.pending, *r)
	if stateRelated {
		o.pendingState[r.Seq] = true
	}
	if len(o.pending) < OutboxBundleResults {
		return nil
	}
	return o.flushLocked(ctx)
}

// Flush writes the collected results as bundles.
func (o *Outbox) Flush(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.flushLocked(ctx)
}

// Run writes the collected results every OutboxBundleInterval until ctx
// ends, then once more.
func (o *Outbox) Run(ctx context.Context) {
	t := time.NewTicker(OutboxBundleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := o.Flush(context.WithoutCancel(ctx)); err != nil {
				slog.Warn("probe spool: final flush failed", "error", err)
			}
			return
		case <-t.C:
			if err := o.Flush(ctx); err != nil && !errors.Is(err, buffer.ErrSpoolFull) {
				slog.Warn("probe spool: flush failed", "error", err)
			}
		}
	}
}

// flushLocked writes the pending results in bundles of at most
// OutboxBundleResults, compacting the oldest bundles first when the result
// queue is at 80 % of its budget. A bundle refused at the limit is dropped
// and counted (max_bytes); on another error the results stay pending (at
// most outboxPendingCap).
func (o *Outbox) flushLocked(ctx context.Context) error {
	if len(o.pending) == 0 {
		return nil
	}
	if now := time.Now(); now.Sub(o.lastCheck) >= o.checkEvery {
		o.lastCheck = now
		if err := o.compactLocked(ctx); err != nil {
			slog.Warn("probe spool: compaction failed", "error", err)
		}
	}
	sort.SliceStable(o.pending, func(i, j int) bool { return o.pending[i].Seq < o.pending[j].Seq })
	var refused error
	for len(o.pending) > 0 {
		n := min(len(o.pending), OutboxBundleResults)
		chunk := o.pending[:n]
		msg, err := o.bundle(chunk, o.pendingState, false)
		if err == nil {
			_, err = o.results.Enqueue(ctx, msg)
		}
		if errors.Is(err, buffer.ErrSpoolFull) {
			// At the limit: compact at once and try once more.
			o.lastCheck = time.Now()
			if cerr := o.compactLocked(ctx); cerr == nil {
				_, err = o.results.Enqueue(ctx, msg)
			}
		}
		switch {
		case errors.Is(err, buffer.ErrSpoolFull):
			o.count(OutboxLossMaxBytes, int64(n))
			refused = err
		case err != nil:
			if over := len(o.pending) - outboxPendingCap; over > 0 {
				o.count(OutboxLossMaxBytes, int64(over))
				o.dropPending(over)
			}
			return err
		}
		o.dropPending(n)
	}
	return refused
}

// dropPending removes the first n pending results.
func (o *Outbox) dropPending(n int) {
	for i := range o.pending[:n] {
		delete(o.pendingState, o.pending[i].Seq)
	}
	o.pending = append(o.pending[:0:0], o.pending[n:]...)
}

// bundle encodes results as a result bundle entry.
func (o *Outbox) bundle(results []probewire.Result, stateRelated map[uint64]bool, compacted bool) (*buffer.Message, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	batch := probewire.ResultBatch{Header: probewire.Header{SchemaVersion: probewire.SchemaVersion}, Results: results}
	if err := json.NewEncoder(zw).Encode(&batch); err != nil {
		return nil, fmt.Errorf("probe: encode result bundle: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("probe: compress result bundle: %w", err)
	}
	from, to := results[0].Seq, results[len(results)-1].Seq
	meta := map[string]string{MetaSeqFrom: strconv.FormatUint(from, 10), MetaSeqTo: strconv.FormatUint(to, 10),
		MetaCount: strconv.Itoa(len(results)), MetaEncoding: encodingGzip}
	var seqs []string
	for i := range results {
		if seq := results[i].Seq; stateRelated[seq] {
			seqs = append(seqs, strconv.FormatUint(seq, 10))
		}
	}
	if len(seqs) > 0 {
		meta[MetaStateSeqs] = strings.Join(seqs, ",")
	}
	if compacted {
		meta[MetaCompacted] = "1"
	}
	return &buffer.Message{ID: seqID(from), Topic: topicResults, Payload: buf.Bytes(), CreatedAt: results[0].ObservedAt,
		Metadata: meta}, nil
}

// DecodeBundle returns the results of a result bundle entry and the
// sequence numbers of the results of a state change.
func DecodeBundle(msg *buffer.Message) (probewire.ResultBatch, map[uint64]bool, error) {
	var batch probewire.ResultBatch
	var r io.Reader = bytes.NewReader(msg.Payload)
	if msg.Metadata[MetaEncoding] == encodingGzip {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return batch, nil, fmt.Errorf("probe: decompress result bundle: %w", err)
		}
		defer zr.Close() //nolint:errcheck // read-only
		r = zr
	}
	if err := json.NewDecoder(r).Decode(&batch); err != nil {
		return batch, nil, fmt.Errorf("probe: decode result bundle: %w", err)
	}
	related := map[uint64]bool{}
	for _, s := range strings.Split(msg.Metadata[MetaStateSeqs], ",") {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			related[n] = true
		}
	}
	return batch, related, nil
}

// compactLocked compacts the oldest result bundles while the result queue
// holds 80 % of its budget or more (WP-954 AK2): a window of bundles after
// the cursor o.compacted (the compacted parts of the run before included,
// so they thin out again together with the bundles behind them) is merged
// and thinned out with Compact, written in place of its last bundle
// (DiskBuffer.Replace) and the others are removed. The left out results
// count as compacted. A full window with nothing to leave out (only
// failures, results of state changes, rare probes) is passed over, so the
// compaction goes on with the bundles behind it instead of stalling there.
func (o *Outbox) compactLocked(ctx context.Context) error {
	for range 64 {
		stats, err := o.results.Stats(ctx)
		if err != nil {
			return err
		}
		if stats.Bytes*100 < o.budget*outboxCompactAt {
			return nil
		}
		progressed, err := o.compactOnce(ctx)
		if err != nil || !progressed {
			return err
		}
	}
	return nil
}

// compactOnce compacts the window of bundles after the cursor o.compacted.
// It returns false when nothing is left after the cursor, or when the
// window reaches the end of the queue with nothing to leave out yet (it is
// compacted again with the bundles still to come). The cursor goes past a
// full window with nothing to leave out, and past every part of a
// compaction but the last (it merges with the bundles behind it); only
// the bundles after the cursor are read (DiskBuffer.PeekAfter).
func (o *Outbox) compactOnce(ctx context.Context) (bool, error) {
	run, err := o.results.PeekAfter(ctx, o.compacted, o.compactWindow)
	if err != nil {
		return false, err
	}
	if len(run) == 0 {
		return false, nil
	}
	var all []probewire.Result
	related := map[uint64]bool{}
	for i := range run {
		batch, rel, derr := DecodeBundle(&run[i])
		if derr != nil {
			// Undecodable: quarantine it and let the next round go on.
			if rerr := o.results.Reject(ctx, run[i].ID); rerr != nil {
				return false, rerr
			}
			return true, nil
		}
		all = append(all, batch.Results...)
		for s := range rel {
			related[s] = true
		}
	}
	last := run[len(run)-1].ID
	kept, dropped := Compact(all, related, CompactBucket)
	if dropped == 0 || strings.Count(last, ".") > 1 {
		if len(run) < o.compactWindow && dropped == 0 {
			return false, nil
		}
		// Nothing to leave out in a full window (or only parts of parts):
		// the bundles stay as they are, the next round takes the ones
		// behind them.
		o.compacted = last
		return true, nil
	}
	var parts []buffer.Message
	for i := 0; i < len(kept); i += OutboxBundleResults {
		part, berr := o.bundle(kept[i:min(i+OutboxBundleResults, len(kept))], related, true)
		if berr != nil {
			return false, berr
		}
		part.ID = "" // Replace names the parts after the bundle they replace
		parts = append(parts, *part)
	}
	// The parts take the place of the last bundle of the window: after
	// every older entry and before every younger one, so the delivery
	// order stays; their names grow only when a part is replaced again.
	ids, err := o.results.Replace(ctx, last, parts)
	if err != nil {
		return false, err
	}
	rest := make([]string, 0, len(run)-1)
	for _, m := range run[:len(run)-1] {
		rest = append(rest, m.ID)
	}
	if err := o.results.Ack(ctx, rest); err != nil {
		return false, err
	}
	o.count(OutboxLossCompacted, int64(dropped))
	if len(ids) > 1 {
		o.compacted = ids[len(ids)-2]
	}
	return true, nil
}

// OutboxStats is the state of the probe spool for the alive message
// (WP-957): the entries and bytes per queue, the results not yet bundled
// and the losses per reason (max_bytes, compacted, quarantined).
type OutboxStats struct {
	StateMessages int
	StateBytes    int64
	ResultBundles int
	ResultBytes   int64
	Pending       int
	Losses        map[string]int64
}

// Lost is the number of entries or results lost for good (max_bytes and
// quarantined; compacted results are no loss).
func (s *OutboxStats) Lost() int64 {
	return s.Losses[OutboxLossMaxBytes] + s.Losses[OutboxLossQuarantined]
}

// Stats returns the state of the spool.
func (o *Outbox) Stats(ctx context.Context) (OutboxStats, error) {
	st, err := o.states.Stats(ctx)
	if err != nil {
		return OutboxStats{}, err
	}
	rs, err := o.results.Stats(ctx)
	if err != nil {
		return OutboxStats{}, err
	}
	o.mu.Lock()
	pending := len(o.pending)
	o.mu.Unlock()
	o.lossMu.Lock()
	losses := make(map[string]int64, len(o.loss))
	for k, v := range o.loss {
		losses[k] = v
	}
	o.lossMu.Unlock()
	return OutboxStats{StateMessages: st.Messages, StateBytes: st.Bytes, ResultBundles: rs.Messages, ResultBytes: rs.Bytes,
		Pending: pending, Losses: losses}, nil
}
