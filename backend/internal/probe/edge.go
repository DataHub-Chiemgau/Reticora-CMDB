package probe

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The edge subjects of the monitoring probes (E-173, WP-959). A collector
// sends its state changes on collector.<org>.<id>.probe.state, its result
// batches on ….probe.results and its monitoring alive message on
// ….probe.alive. The discovery edge dispatcher (discovery.WithEdgeProbes)
// refuses an unknown or disabled collector before Edge sees the request
// (COL-04); the organization and the collector are those of the subject,
// that is of the authenticated edge identity (probewire package comment).
const (
	// MaxMessageBytes is the largest state or results message the edge
	// takes (AK2); the collector splits its batches below it (WP-955).
	MaxMessageBytes = 256 << 10
	// DefaultEdgeWorkers is the default of RETICORA_PROBE_EDGE_WORKERS: the
	// handlers per instance that answer the probe subjects in parallel.
	DefaultEdgeWorkers = 8
	// publishTimeout bounds the wait for the PubAck of the stream.
	publishTimeout = 10 * time.Second
)

// FeatureStatus reports the entitlement of a feature (entitlement.Service).
type FeatureStatus interface {
	Status(ctx context.Context, orgID, featureKey string) (entitlement.Entitlement, entitlement.Status, error)
}

// AliveHook receives every valid monitoring alive message with the time the
// server received it; the clock skew (WP-962) and the liveness of the group
// members (WP-963) attach here. A hook runs in the edge handler and must not
// block for long.
type AliveHook func(orgID, collectorID string, alive probewire.Alive, receivedAt time.Time)

// Reply answers a probe edge request. Status is the HTTP status the request
// would have on the API; Type is the problem type of a refusal (403
// feature-not-entitled pauses the collector's uplink, WP-955). Seq is the
// highest sequence number the stream holds after the request (state and
// results): the collector acknowledges its spool up to it. ServerTime
// answers .probe.alive (AliveReply).
type Reply struct {
	Status     int        `json:"status"`
	Type       string     `json:"type,omitempty"`
	Error      string     `json:"error,omitempty"`
	Seq        *uint64    `json:"seq,omitempty"`
	Duplicate  bool       `json:"duplicate,omitempty"`
	ServerTime *time.Time `json:"server_time,omitempty"`
}

// Edge takes the probe messages of the collectors: it checks the monitoring
// entitlement, validates a message and stores state changes and result
// batches in the probe stream with a deduplication ID; it answers only after
// the PubAck, so an acknowledged message is persisted (PRB-07). Nothing is
// written to the database here (WP-960, WP-961).
type Edge struct {
	js       jetstream.JetStream
	stream   StreamConfig
	features FeatureStatus
	workers  int
	now      func() time.Time

	mu      sync.RWMutex
	onAlive []AliveHook
}

// NewEdge returns the edge of the probe subjects publishing to the stream
// cfg on js.
func NewEdge(js jetstream.JetStream, cfg StreamConfig) *Edge {
	return &Edge{js: js, stream: cfg, workers: DefaultEdgeWorkers, now: time.Now}
}

// WithFeatures checks the monitoring entitlement of the organization (nil:
// every organization is entitled).
func (e *Edge) WithFeatures(features FeatureStatus) *Edge {
	e.features = features
	return e
}

// WithWorkers sets the handlers per instance (RETICORA_PROBE_EDGE_WORKERS);
// a value below DefaultEdgeWorkers takes the default.
func (e *Edge) WithWorkers(n int) *Edge {
	e.workers = max(n, DefaultEdgeWorkers)
	return e
}

// OnAlive adds a hook for the monitoring alive messages (WP-962, WP-963).
func (e *Edge) OnAlive(hook AliveHook) *Edge {
	e.mu.Lock()
	e.onAlive = append(e.onAlive, hook)
	e.mu.Unlock()
	return e
}

// Workers is the size of the handler pool per instance.
func (e *Edge) Workers() int { return e.workers }

// ProbeState stores a state change (probewire.StateChange) of the collector
// in the stream and answers with its seq after the PubAck.
func (e *Edge) ProbeState(ctx context.Context, orgID, collectorID string, data []byte) any {
	if r, ok := e.admit(ctx, orgID, collectorID, data); !ok {
		return r
	}
	var change probewire.StateChange
	if r, ok := decode(data, &change); !ok {
		return r
	}
	if r, ok := sameCollector(data, collectorID); !ok {
		return r
	}
	if field := validateStateChange(&change); field != "" {
		return invalid(field)
	}
	return e.publish(ctx, e.stream.StateSubject(orgID, collectorID),
		msgID(collectorID, kindState, change.Epoch, change.Seq, change.Seq), change.Seq, orgID, collectorID, data)
}

// ProbeResults stores a result batch (probewire.ResultBatch) of the
// collector in the stream and answers with its highest seq after the
// PubAck.
func (e *Edge) ProbeResults(ctx context.Context, orgID, collectorID string, data []byte) any {
	if r, ok := e.admit(ctx, orgID, collectorID, data); !ok {
		return r
	}
	var batch probewire.ResultBatch
	if r, ok := decode(data, &batch); !ok {
		return r
	}
	if batch.CollectorID != "" && !strings.EqualFold(batch.CollectorID, collectorID) {
		return invalid("collector_id")
	}
	if len(batch.Results) == 0 || len(batch.Results) > probewire.MaxBatchResults {
		return invalid("results")
	}
	var epoch int64
	from, to := batch.Results[0].Seq, batch.Results[0].Seq
	for i := range batch.Results {
		r := &batch.Results[i]
		if r.Validate() != nil {
			return invalid("results[" + strconv.Itoa(i) + "]")
		}
		epoch = max(epoch, r.Epoch)
		from, to = min(from, r.Seq), max(to, r.Seq)
	}
	return e.publish(ctx, e.stream.ResultsSubject(orgID, collectorID),
		msgID(collectorID, kindResults, epoch, from, to), to, orgID, collectorID, data)
}

// ProbeAlive validates the monitoring alive message of the collector, hands
// it to the hooks and answers with the server time (AliveReply); nothing is
// stored.
func (e *Edge) ProbeAlive(ctx context.Context, orgID, collectorID string, data []byte) any {
	received := e.now().UTC()
	if r, ok := e.admit(ctx, orgID, collectorID, data); !ok {
		return r
	}
	var alive probewire.Alive
	if r, ok := decode(data, &alive); !ok {
		return r
	}
	if r, ok := sameCollector(data, collectorID); !ok {
		return r
	}
	if alive.Validate() != nil {
		return invalid("results_per_s")
	}
	if alive.CollectorTime.IsZero() {
		return invalid("collector_time")
	}
	e.mu.RLock()
	hooks := e.onAlive
	e.mu.RUnlock()
	for _, hook := range hooks {
		hook(orgID, collectorID, alive, received)
	}
	now := e.now().UTC()
	return Reply{Status: http.StatusOK, ServerTime: &now}
}

// admit checks what every probe subject requires before the message is
// read: the monitoring entitlement (403 feature-not-entitled, as the
// entitlement middleware and the .metrics subject answer) and the size.
func (e *Edge) admit(ctx context.Context, orgID, collectorID string, data []byte) (Reply, bool) {
	if e.features != nil {
		_, status, err := e.features.Status(ctx, orgID, entitlement.FeatureMonitoring)
		if err != nil {
			slog.Error("probe edge entitlement lookup failed", "organization_id", orgID, "collector_id", collectorID, "error", err)
			return Reply{Status: http.StatusInternalServerError, Error: "entitlement lookup failed"}, false
		}
		if status == entitlement.StatusNotEntitled {
			refusal := &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureMonitoring}
			return Reply{Status: http.StatusForbidden, Type: refusal.ProblemType(), Error: refusal.Error()}, false
		}
	}
	if len(data) > MaxMessageBytes {
		return Reply{Status: http.StatusRequestEntityTooLarge, Error: "message larger than " + strconv.Itoa(MaxMessageBytes) + " bytes"}, false
	}
	return Reply{}, true
}

// decode reads a probewire message: 400 for a body that is no JSON or has an
// unsupported schema version.
func decode(data []byte, m probewire.Message) (Reply, bool) {
	err := probewire.Decode(data, m)
	switch {
	case errors.Is(err, probewire.ErrUnsupportedVersion):
		return Reply{Status: http.StatusBadRequest, Error: "unsupported schema_version"}, false
	case err != nil:
		return Reply{Status: http.StatusBadRequest, Error: "invalid body"}, false
	}
	return Reply{}, true
}

// sameCollector refuses a message whose informational collector_id names
// another collector than the authenticated edge identity.
func sameCollector(data []byte, collectorID string) (Reply, bool) {
	var sender struct {
		CollectorID string `json:"collector_id"`
	}
	if json.Unmarshal(data, &sender) != nil {
		return Reply{Status: http.StatusBadRequest, Error: "invalid body"}, false
	}
	if sender.CollectorID != "" && !strings.EqualFold(sender.CollectorID, collectorID) {
		return invalid("collector_id"), false
	}
	return Reply{}, true
}

// validateStateChange returns the first invalid field of a state change.
func validateStateChange(c *probewire.StateChange) string {
	switch {
	case c.ProbeID == "":
		return "probe_id"
	case !c.From.Valid():
		return "from"
	case !c.To.Valid():
		return "to"
	case c.Reason != "" && !probewire.ValidReason(c.Reason):
		return "reason"
	case c.ObservedAt.IsZero():
		return "observed_at"
	case c.LastResult != nil && c.LastResult.Validate() != nil:
		return "last_result"
	}
	return ""
}

// invalid is the refusal of a message with an invalid field (422): the
// collector puts it into quarantine and counts it as loss (WP-955).
func invalid(field string) Reply {
	return Reply{Status: http.StatusUnprocessableEntity, Error: field + ": invalid"}
}

// The kinds of message in the deduplication ID.
const (
	kindState   = "state"
	kindResults = "results"
)

// msgID is the deduplication ID of a message in the stream (PRB-07):
// <collector>:<kind>:<epoch>:<seq_from>-<seq_to> with kind state or
// results. Seq never restarts within its stream, so a repeated
// transmission of the same message has the same ID. JetStream deduplicates
// over the whole stream, not per subject; the kind keeps a state change
// and a one-result batch with the same seq apart, as the collector counts
// state and results separately (WP-952 AK4).
func msgID(collectorID, kind string, epoch int64, from, to uint64) string {
	return strings.ToLower(collectorID) + ":" + kind + ":" + strconv.FormatInt(epoch, 10) + ":" +
		strconv.FormatUint(from, 10) + "-" + strconv.FormatUint(to, 10)
}

// publish stores data in the stream and answers after the PubAck: 202 with
// seq, also for a duplicate the stream already holds; 503 when the stream
// does not acknowledge (the collector keeps the message and repeats it).
func (e *Edge) publish(ctx context.Context, subject, id string, seq uint64, orgID, collectorID string, data []byte) Reply {
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	ack, err := e.js.Publish(pctx, subject, data, jetstream.WithMsgID(id), jetstream.WithExpectStream(e.stream.Name))
	if err != nil {
		slog.Error("probe edge publish failed", "organization_id", orgID, "collector_id", collectorID, "subject", subject, "error", err)
		return Reply{Status: http.StatusServiceUnavailable, Error: "probe stream unavailable"}
	}
	return Reply{Status: http.StatusAccepted, Seq: &seq, Duplicate: ack.Duplicate}
}
