package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// The data path over the edge (COL-08): once the collector holds its
// mutually authenticated NATS channel, it delivers records, heartbeats and
// metric samples there; the server takes organization and collector from
// the subject the certificate was authorized for. Without the channel (not
// enrolled, no connection, a server that does not answer the subject yet)
// records and heartbeats take the HTTP path; metric samples wait for the
// edge, the HTTP API authenticates no collector (F-046).
const (
	edgeIngestSuffix    = ".ingest"
	edgeHeartbeatSuffix = ".heartbeat"
	edgeMetricsSuffix   = ".metrics"
	edgeRequestTimeout  = 60 * time.Second
)

// errNoEdge reports that the edge channel cannot take the request; the
// caller falls back to HTTP.
var errNoEdge = errors.New("edge channel unavailable")

// errEdgeTooLarge: the payload exceeds the edge's message size. Records
// take the HTTP path as without the edge; a metric batch is dropped.
var errEdgeTooLarge = fmt.Errorf("%w: payload exceeds the maximum message size", errNoEdge)

// edgeReply is the server's answer on the edge (discovery.EdgeReply).
type edgeReply struct {
	Status          int    `json:"status"`
	Type            string `json:"type"`
	Error           string `json:"error"`
	LicenseStatus   string `json:"license_status"`
	CollectorStatus string `json:"collector_status"`
	Ingested        int    `json:"ingested"`
	Unassigned      int    `json:"unassigned"`
}

// edgeRequest sends data on the collector's subject tree; tests replace it.
var edgeRequest = func(ctx context.Context, suffix string, data []byte) ([]byte, error) {
	ch := scopeChannel.Load()
	if ch == nil || ch.nc == nil || !ch.nc.IsConnected() {
		return nil, errNoEdge
	}
	if int64(len(data)) > ch.nc.MaxPayload() {
		return nil, errEdgeTooLarge
	}
	rctx, cancel := context.WithTimeout(ctx, edgeRequestTimeout)
	defer cancel()
	msg, err := ch.nc.RequestWithContext(rctx, ch.subject+suffix, data)
	if errors.Is(err, nats.ErrNoResponders) {
		return nil, errNoEdge
	}
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// postViaEdge delivers a records payload over the edge. errNoEdge means the
// HTTP path has to take it.
func (u *uploader) postViaEdge(ctx context.Context, payload []byte) error {
	raw, err := edgeRequest(ctx, edgeIngestSuffix, payload)
	if err != nil {
		return err
	}
	var reply edgeReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("edge ingest reply: %w", err)
	}
	if reply.Status == http.StatusForbidden && reply.Type == problemLicenseExpired {
		u.setLicenseStatus("expired")
		return errLicenseExpired
	}
	if reply.Status >= 400 {
		return fmt.Errorf("upload failed with status %d: %s", reply.Status, reply.Error)
	}
	if reply.LicenseStatus != "" {
		u.setLicenseStatus(reply.LicenseStatus)
	}
	return nil
}

// heartbeatViaEdge sends a heartbeat over the edge; ok is false when the
// edge did not take it (the caller then uses HTTP).
func heartbeatViaEdge(ctx context.Context, body []byte) (licenseStatus string, ok, handled bool) {
	start := time.Now()
	raw, err := edgeRequest(ctx, edgeHeartbeatSuffix, body)
	if errors.Is(err, errNoEdge) {
		return "", false, false
	}
	if err != nil {
		slog.Error("heartbeat over the edge failed", "error", err)
		return "", false, true
	}
	var reply edgeReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		slog.Error("heartbeat over the edge: invalid reply", "error", err)
		return "", false, true
	}
	slog.Info("heartbeat completed", "status", reply.Status, "transport", "edge", "duration_ms", time.Since(start).Milliseconds())
	if reply.Status >= 300 {
		return "", false, true
	}
	return reply.LicenseStatus, true, true
}

// Metric samples (sweep reachability, UPS values, SNMP polls, traps; MON-02)
// travel on .metrics only. A batch the edge cannot take yet waits in a
// bounded buffer and goes, oldest first, at the next edge contact: the next
// upload or the flusher's next round.
const (
	// maxMetricBatches bounds the buffer; beyond it the oldest batch is
	// dropped.
	maxMetricBatches = 10
	// maxMetricBatchSamples bounds the samples of one batch, so a batch
	// stays far below the edge's message size (1 MiB by default).
	maxMetricBatchSamples = 1000
)

// metricBatch is one payload of the monitoring ingest and its sample count.
type metricBatch struct {
	payload []byte
	samples int
}

// metricBuffer holds the metric batches the edge did not take yet; sendMu
// lets one delivery run at a time.
type metricBuffer struct {
	mu      sync.Mutex
	batches []*metricBatch
	sendMu  sync.Mutex
}

// push appends a batch and drops the oldest beyond maxMetricBatches.
func (b *metricBuffer) push(batch *metricBatch) {
	b.mu.Lock()
	b.batches = append(b.batches, batch)
	var dropped []*metricBatch
	if n := len(b.batches) - maxMetricBatches; n > 0 {
		dropped = slices.Clone(b.batches[:n])
		b.batches = slices.Delete(b.batches, 0, n)
	}
	b.mu.Unlock()
	for _, d := range dropped {
		slog.Error("metric samples dropped: the edge channel took none of the buffered batches",
			"event", "collector.metrics.dropped", "count", d.samples, "max_batches", maxMetricBatches)
	}
}

// oldest returns the oldest batch, nil when the buffer is empty.
func (b *metricBuffer) oldest() *metricBatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.batches) == 0 {
		return nil
	}
	return b.batches[0]
}

// remove takes a batch out of the buffer, unless push dropped it already.
func (b *metricBuffer) remove(batch *metricBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches = slices.DeleteFunc(b.batches, func(x *metricBatch) bool { return x == batch })
}

// len is the number of buffered batches.
func (b *metricBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batches)
}

// metricsRefusedError is the server's refusal of a metric batch.
type metricsRefusedError struct {
	status int
	detail string
}

func (e *metricsRefusedError) Error() string {
	return fmt.Sprintf("metric samples refused with status %d: %s", e.status, e.detail)
}

// postMetrics sends monitoring ingest samples over the edge in batches of
// at most maxMetricBatchSamples (postMetricsPayload); it returns the
// refusals and drops of these batches.
func (u *uploader) postMetrics(ctx context.Context, metrics []map[string]any) error {
	var errs []error
	for chunk := range slices.Chunk(metrics, maxMetricBatchSamples) {
		payload, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("marshal metric samples: %w", err)
		}
		if err = u.postMetricsPayload(ctx, payload, len(chunk)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// postMetricsPayload sends a monitoring ingest payload of count samples
// (polled metrics, sweep reachability, UPS values, traps) over the edge on
// .metrics, after the batches still buffered. No request without a
// credential goes to the HTTP API (F-046): without the edge the batch waits
// in the buffer. It returns the refusal of this batch (status >= 400) or
// its drop; a waiting batch is no error.
func (u *uploader) postMetricsPayload(ctx context.Context, payload []byte, count int) error {
	batch := &metricBatch{payload: payload, samples: count}
	u.metrics.push(batch)
	held, err := u.deliverMetrics(ctx, batch)
	if held {
		slog.Warn("metric samples held until the edge channel takes them", "event", "collector.metrics.held",
			"count", count, "buffered_batches", u.metrics.len())
	}
	return err
}

// flushMetrics delivers the buffered metric batches (the flusher's round
// after a heartbeat or on its interval).
func (u *uploader) flushMetrics(ctx context.Context) {
	_, _ = u.deliverMetrics(ctx, nil)
}

// deliverMetrics sends the buffered batches oldest first. It stops while
// the edge cannot take them or a request failed (held: batches wait); a
// refused or oversized batch is dropped and logged as an error with its
// status and count, except own, whose refusal or drop it returns to the
// caller.
func (u *uploader) deliverMetrics(ctx context.Context, own *metricBatch) (held bool, ownErr error) {
	u.metrics.sendMu.Lock()
	defer u.metrics.sendMu.Unlock()
	for batch := u.metrics.oldest(); batch != nil; batch = u.metrics.oldest() {
		if ctx.Err() != nil {
			return true, ownErr
		}
		err := u.sendMetricBatch(ctx, batch)
		var refused *metricsRefusedError
		switch {
		case errors.Is(err, errEdgeTooLarge):
			err = fmt.Errorf("metric samples dropped: %w", err)
		case errors.Is(err, errNoEdge):
			return true, ownErr
		case errors.As(err, &refused):
		case err != nil:
			slog.Warn("metric samples over the edge failed; kept for the next attempt", "count", batch.samples, "error", err)
			return true, ownErr
		}
		u.metrics.remove(batch)
		switch {
		case err == nil:
		case batch == own:
			ownErr = err
		case refused != nil:
			slog.Error("metric samples refused", "event", "collector.metrics.refused", "status", refused.status,
				"count", batch.samples, "error", err)
		default:
			slog.Error("metric samples dropped", "event", "collector.metrics.dropped", "count", batch.samples, "error", err)
		}
	}
	return false, ownErr
}

// sendMetricBatch sends one batch on .metrics: nil when the server took
// it, a *metricsRefusedError for a refusal.
func (u *uploader) sendMetricBatch(ctx context.Context, batch *metricBatch) error {
	raw, err := edgeRequest(ctx, edgeMetricsSuffix, batch.payload)
	if err != nil {
		return err
	}
	var reply edgeReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("edge metrics reply: %w", err)
	}
	if reply.Status >= 400 {
		return &metricsRefusedError{status: reply.Status, detail: reply.Error}
	}
	slog.Info("metric samples delivered", "count", batch.samples, "ingested", reply.Ingested,
		"unassigned", reply.Unassigned, "transport", "edge")
	return nil
}
