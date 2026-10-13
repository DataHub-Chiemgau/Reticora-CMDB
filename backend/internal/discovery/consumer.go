package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Ingest pipeline (REC-01): every record is published on ingest.<org>.<collector>
// and processed by the ingest consumer in the tenant scope of the principal
// that delivered it. A record_id is processed once within RecordDedupeWindow;
// the consumer handles one batch at a time, ordered by observed_at, so the
// records of a CI are processed serially in source-time order.

// RecordDedupeWindow is how long a processed record_id is remembered.
const RecordDedupeWindow = 48 * time.Hour

// IngestMessage is one record on the ingest.> subjects.
type IngestMessage struct {
	OrganizationID string `json:"organization_id"`
	CollectorID    string `json:"collector_id,omitempty"`
	// Scope is the tenant scope of the principal that delivered the record;
	// nil when the request carried none, so tenant repositories fail closed.
	Scope *MessageScope `json:"scope,omitempty"`
	Item  IngestItem    `json:"item"`
	// The DeviceRecord fields IngestItem does not serialize.
	RecordID   string    `json:"record_id,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	VRFID      string    `json:"vrf_id,omitempty"`
	ScopeID    string    `json:"scope_id,omitempty"`
	SiteID     string    `json:"site_id,omitempty"`
	ClientID   string    `json:"client_id,omitempty"`
}

// MessageScope is the serialized database.TenantScope: a nil dimension is
// the whole organization, a list restricts to its ids (empty: none).
type MessageScope struct {
	UserID  string    `json:"user_id,omitempty"`
	Clients *[]string `json:"clients"`
	Sites   *[]string `json:"sites"`
	Teams   *[]string `json:"teams"`
}

func encodeScopeSet(s database.ScopeSet) *[]string {
	if s.IsAll() {
		return nil
	}
	ids := s.IDs()
	if ids == nil {
		ids = []string{}
	}
	return &ids
}

func decodeScopeSet(ids *[]string) database.ScopeSet {
	if ids == nil {
		return database.AllScopes()
	}
	return database.ScopeIDs(*ids...)
}

// Subject returns the subject the message is published on.
func (m *IngestMessage) Subject() string {
	collector := m.CollectorID
	if collector == "" {
		collector = "api"
	}
	return "ingest." + m.OrganizationID + "." + subjectToken(collector)
}

// subjectToken keeps a value usable as one NATS subject token.
func subjectToken(v string) string {
	out := []rune(v)
	for i, r := range out {
		if r == '.' || r == '*' || r == '>' || r == ' ' || r == '\t' {
			out[i] = '_'
		}
	}
	return string(out)
}

// newIngestMessages wraps the admitted items of one request.
func newIngestMessages(ctx context.Context, t tenant.TenantInfo, collectorID string, items []IngestItem) []IngestMessage {
	var scope *MessageScope
	if s, ok := database.TenantScopeFromContext(ctx); ok {
		scope = &MessageScope{UserID: s.UserID, Clients: encodeScopeSet(s.Clients), Sites: encodeScopeSet(s.Sites), Teams: encodeScopeSet(s.Teams)}
	}
	msgs := make([]IngestMessage, len(items))
	for i := range items {
		item := items[i]
		msgs[i] = IngestMessage{
			OrganizationID: t.OrganizationID, CollectorID: collectorID, Scope: scope, Item: item,
			RecordID: item.RecordID, ObservedAt: item.ObservedAt,
			VRFID: item.VRFID, ScopeID: item.ScopeID, SiteID: item.SiteID, ClientID: item.ClientID,
		}
	}
	sortByObservedAt(msgs)
	return msgs
}

func sortByObservedAt(msgs []IngestMessage) {
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].ObservedAt.Before(msgs[j].ObservedAt) })
}

// item restores the ingest item with its DeviceRecord fields.
func (m *IngestMessage) item() IngestItem {
	item := m.Item
	item.RecordID, item.ObservedAt, item.CollectorID = m.RecordID, m.ObservedAt, m.CollectorID
	item.VRFID, item.ScopeID, item.SiteID, item.ClientID = m.VRFID, m.ScopeID, m.SiteID, m.ClientID
	return item
}

// context returns ctx with the message's tenant and scope.
func (m *IngestMessage) context(ctx context.Context) context.Context {
	userID := ""
	if m.Scope != nil {
		userID = m.Scope.UserID
		scope := database.TenantScope{
			OrgID: m.OrganizationID, UserID: m.Scope.UserID,
			Clients: decodeScopeSet(m.Scope.Clients), Sites: decodeScopeSet(m.Scope.Sites), Teams: decodeScopeSet(m.Scope.Teams),
		}
		ctx = database.ContextWithTenantScope(ctx, &scope)
	}
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: m.OrganizationID, UserID: userID})
}

// IngestQueue carries ingest messages to the consumer.
type IngestQueue interface {
	// Publish stores the messages durably; it returns once they are
	// accepted.
	Publish(ctx context.Context, msgs []IngestMessage) error
}

// WithIngestQueue makes BulkIngest publish to queue instead of processing
// in the request.
func (h *Handler) WithIngestQueue(queue IngestQueue) *Handler {
	h.queue = queue
	return h
}

// dispatch publishes the messages, or processes them when no queue is set.
func (h *Handler) dispatch(ctx context.Context, msgs []IngestMessage) (BulkIngestResponse, error) {
	if h.queue == nil {
		return h.ProcessMessages(ctx, msgs)
	}
	if err := h.queue.Publish(ctx, msgs); err != nil {
		return BulkIngestResponse{}, err
	}
	return BulkIngestResponse{Queued: len(msgs)}, nil
}

// ProcessMessages is the ingest consumer's processing step: it orders the
// messages by observed_at and processes them per organization, serially per
// organization within this process, each in the tenant scope it carries.
func (h *Handler) ProcessMessages(ctx context.Context, msgs []IngestMessage) (BulkIngestResponse, error) {
	ordered := append([]IngestMessage(nil), msgs...)
	sortByObservedAt(ordered)
	var total BulkIngestResponse
	for len(ordered) > 0 {
		head := ordered[0]
		var group []IngestItem
		var rest []IngestMessage
		for i := range ordered {
			if ordered[i].OrganizationID == head.OrganizationID && sameScope(ordered[i].Scope, head.Scope) {
				group = append(group, ordered[i].item())
			} else {
				rest = append(rest, ordered[i])
			}
		}
		ordered = rest
		resp, err := h.processSerially(head.context(ctx), head.OrganizationID, group)
		total.add(&resp)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func sameScope(a, b *MessageScope) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// processSerially processes the items of one organization while no other
// ingest of that organization runs in this process.
func (h *Handler) processSerially(ctx context.Context, orgID string, items []IngestItem) (BulkIngestResponse, error) {
	lock, _ := h.serial.LoadOrStore(orgID, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return h.processItems(ctx, orgID, items)
}

func (r *BulkIngestResponse) add(o *BulkIngestResponse) {
	r.Received += o.Received
	r.Created += o.Created
	r.Updated += o.Updated
	r.Conflicts += o.Conflicts
	r.ReviewItems += o.ReviewItems
	r.Relationships += o.Relationships
	r.ProtectedOverrides += o.ProtectedOverrides
	r.ProtectedInstanceFields += o.ProtectedInstanceFields
	r.InvalidValues += o.InvalidValues
	r.Unlicensed += o.Unlicensed
	r.Duplicates += o.Duplicates
	r.Resurrected += o.Resurrected
	r.Queued += o.Queued
	r.RejectedFuture += o.RejectedFuture
}

// RecordLedger remembers processed record_ids (REC-01). The PostgreSQL and
// memory repositories implement it.
type RecordLedger interface {
	// ProcessedRecords returns the ids among recordIDs processed within
	// RecordDedupeWindow.
	ProcessedRecords(ctx context.Context, orgID string, recordIDs []string) (map[string]bool, error)
	// MarkRecordsProcessed remembers the records and forgets those older
	// than RecordDedupeWindow.
	MarkRecordsProcessed(ctx context.Context, orgID string, records []ProcessedRecord) error
}

// ProcessedRecord is one entry of the ledger.
type ProcessedRecord struct {
	RecordID    string
	CollectorID string
	ObservedAt  time.Time
}

// skipProcessed removes records processed within the dedupe window and
// repeated record_ids of the same batch.
func (h *Handler) skipProcessed(ctx context.Context, orgID string, items []IngestItem) (kept []IngestItem, duplicates int, err error) {
	ledger, ok := h.repo.(RecordLedger)
	if !ok {
		return items, 0, nil
	}
	var ids []string
	for i := range items {
		if items[i].RecordID != "" {
			ids = append(ids, items[i].RecordID)
		}
	}
	if len(ids) == 0 {
		return items, 0, nil
	}
	seen, err := ledger.ProcessedRecords(ctx, orgID, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("read processed records: %w", err)
	}
	for i := range items {
		id := items[i].RecordID
		if id != "" && seen[id] {
			duplicates++
			continue
		}
		if id != "" {
			seen[id] = true
		}
		kept = append(kept, items[i])
	}
	return kept, duplicates, nil
}

// markProcessed remembers the record_ids of processed items.
func (h *Handler) markProcessed(ctx context.Context, orgID string, items []IngestItem) error {
	ledger, ok := h.repo.(RecordLedger)
	if !ok {
		return nil
	}
	var records []ProcessedRecord
	for i := range items {
		if items[i].RecordID != "" {
			records = append(records, ProcessedRecord{RecordID: items[i].RecordID, CollectorID: items[i].CollectorID, ObservedAt: items[i].ObservedAt})
		}
	}
	if len(records) == 0 {
		return nil
	}
	return ledger.MarkRecordsProcessed(ctx, orgID, records)
}

// Names of the JetStream stream and consumer of the ingest pipeline.
const (
	IngestStream   = "RETICORA_INGEST"
	IngestConsumer = "reticora-ingest"
	// ingestBatch is the number of messages one fetch processes together;
	// MaxAckPending equals it, so one batch is in flight across all server
	// instances and the records of a CI stay serial.
	ingestBatch      = 100
	ingestMaxDeliver = 5
)

var ingestDeadLetters atomic.Int64

// IngestDeadLetters is the number of ingest messages given up since start:
// undecodable, or failed ingestMaxDeliver times.
func IngestDeadLetters() int64 { return ingestDeadLetters.Load() }

// NATSIngestQueue is the ingest.> pipeline on NATS JetStream.
type NATSIngestQueue struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	consumer jetstream.Consumer
	handler  *Handler
}

// NewNATSIngestQueue connects to NATS and ensures the work-queue stream on
// ingest.> and its durable consumer.
func NewNATSIngestQueue(ctx context.Context, url string, opts ...nats.Option) (*NATSIngestQueue, error) {
	nc, err := nats.Connect(url, append([]nats.Option{nats.Name("reticora-ingest"), nats.MaxReconnects(-1)}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("connect NATS: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      IngestStream,
		Subjects:  []string{"ingest.>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		// Records not processed within the dedupe window are dropped.
		MaxAge: RecordDedupeWindow,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure stream %s: %w", IngestStream, err)
	}
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       IngestConsumer,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    ingestMaxDeliver,
		MaxAckPending: ingestBatch,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure consumer %s: %w", IngestConsumer, err)
	}
	return &NATSIngestQueue{nc: nc, js: js, consumer: consumer}, nil
}

// Publish stores each message on its ingest.<org>.<collector> subject and
// waits for the stream's acknowledgement.
func (q *NATSIngestQueue) Publish(ctx context.Context, msgs []IngestMessage) error {
	for i := range msgs {
		data, err := json.Marshal(&msgs[i])
		if err != nil {
			return fmt.Errorf("encode ingest message: %w", err)
		}
		if _, err = q.js.Publish(ctx, msgs[i].Subject(), data); err != nil {
			return fmt.Errorf("publish ingest message: %w", err)
		}
	}
	return nil
}

// Close closes the NATS connection.
func (q *NATSIngestQueue) Close() { q.nc.Close() }

// Attach sets the handler that processes consumed messages and makes it
// publish to this queue.
func (q *NATSIngestQueue) Attach(h *Handler) {
	q.handler = h
	h.WithIngestQueue(q)
}

// Run consumes ingest.> with the attached handler until ctx ends.
func (q *NATSIngestQueue) Run(ctx context.Context) {
	if q.handler == nil {
		slog.Error("ingest consumer not started: no handler attached")
		return
	}
	runIngestConsumer(ctx, jetstreamSource{q.consumer}, q.handler)
}

// ingestSource fetches batches of deliveries; jetstreamSource and the
// tests implement it.
type ingestSource interface {
	Fetch(ctx context.Context, limit int) ([]ingestDelivery, error)
}

type ingestDelivery interface {
	Data() []byte
	Ack() error
	Nak() error
	Term() error
	Delivered() uint64
}

type jetstreamSource struct{ consumer jetstream.Consumer }

func (s jetstreamSource) Fetch(ctx context.Context, limit int) ([]ingestDelivery, error) {
	// Wait at most a second for messages, and stop with ctx.
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	batch, err := s.consumer.Fetch(limit, jetstream.FetchContext(waitCtx))
	if err != nil {
		return nil, err
	}
	var out []ingestDelivery
	for msg := range batch.Messages() {
		out = append(out, jetstreamDelivery{msg})
	}
	return out, batch.Error()
}

type jetstreamDelivery struct{ msg jetstream.Msg }

func (d jetstreamDelivery) Data() []byte { return d.msg.Data() }
func (d jetstreamDelivery) Ack() error   { return d.msg.Ack() }
func (d jetstreamDelivery) Nak() error   { return d.msg.NakWithDelay(5 * time.Second) }
func (d jetstreamDelivery) Term() error  { return d.msg.Term() }
func (d jetstreamDelivery) Delivered() uint64 {
	meta, err := d.msg.Metadata()
	if err != nil {
		return 1
	}
	return meta.NumDelivered
}

// runIngestConsumer is the consumer loop: fetch a batch, process it in
// observed_at order, acknowledge what was processed.
func runIngestConsumer(ctx context.Context, src ingestSource, h *Handler) {
	for ctx.Err() == nil {
		deliveries, err := src.Fetch(ctx, ingestBatch)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, nats.ErrTimeout) &&
			!errors.Is(err, context.DeadlineExceeded) && len(deliveries) == 0 {
			observability.ObserveWorkerRun("ingest", 1)
			slog.Warn("ingest consumer fetch failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		consumeBatch(ctx, h, deliveries)
		observability.ObserveWorkerRun("ingest", 0)
	}
}

// Backlog reports the messages waiting for the ingest worker: not yet
// delivered plus delivered but unacknowledged (OPS-05).
func (q *NATSIngestQueue) Backlog(ctx context.Context) (int, error) {
	info, err := q.consumer.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("ingest consumer info: %w", err)
	}
	return int(info.NumPending) + info.NumAckPending, nil
}

// consumeBatch processes one fetched batch. Undecodable messages are
// terminated; a failed organization group is redelivered, and given up
// after ingestMaxDeliver attempts.
func consumeBatch(ctx context.Context, h *Handler, deliveries []ingestDelivery) {
	type entry struct {
		msg IngestMessage
		d   ingestDelivery
	}
	var entries []entry
	for _, d := range deliveries {
		var msg IngestMessage
		if err := json.Unmarshal(d.Data(), &msg); err != nil || msg.OrganizationID == "" {
			ingestDeadLetters.Add(1)
			slog.Error("ingest message dropped: undecodable", "event", "ingest.message.invalid", "error", err)
			_ = d.Term()
			continue
		}
		entries = append(entries, entry{msg, d})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].msg.ObservedAt.Before(entries[j].msg.ObservedAt) })

	// One group per organization and scope, processed in order.
	for len(entries) > 0 {
		head := entries[0].msg
		var group, rest []entry
		for i := range entries {
			e := &entries[i]
			if e.msg.OrganizationID == head.OrganizationID && sameScope(e.msg.Scope, head.Scope) {
				group = append(group, *e)
			} else {
				rest = append(rest, *e)
			}
		}
		entries = rest
		msgs := make([]IngestMessage, len(group))
		for i := range group {
			msgs[i] = group[i].msg
		}
		_, err := h.ProcessMessages(ctx, msgs)
		for i := range group {
			e := &group[i]
			switch {
			case err == nil:
				_ = e.d.Ack()
			case e.d.Delivered() >= ingestMaxDeliver:
				ingestDeadLetters.Add(1)
				slog.Error("ingest message given up", "event", "ingest.message.failed",
					"organization_id", e.msg.OrganizationID, "record_id", e.msg.RecordID, "error", err)
				_ = e.d.Term()
			default:
				slog.Warn("ingest message redelivered", "organization_id", e.msg.OrganizationID,
					"record_id", e.msg.RecordID, "error", err)
				_ = e.d.Nak()
			}
		}
	}
}

// ProcessedRecords implements RecordLedger in memory.
func (r *MemoryRepository) ProcessedRecords(_ context.Context, orgID string, recordIDs []string) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]bool{}
	for _, id := range recordIDs {
		if at, ok := r.records[orgID+"/"+id]; ok && time.Since(at) < RecordDedupeWindow {
			seen[id] = true
		}
	}
	return seen, nil
}

// MarkRecordsProcessed implements RecordLedger in memory.
func (r *MemoryRepository) MarkRecordsProcessed(_ context.Context, orgID string, records []ProcessedRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records == nil {
		r.records = map[string]time.Time{}
	}
	now := time.Now()
	for _, rec := range records {
		r.records[orgID+"/"+rec.RecordID] = now
	}
	for key, at := range r.records {
		if now.Sub(at) >= RecordDedupeWindow {
			delete(r.records, key)
		}
	}
	return nil
}
