package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Ingest pipeline (REC-01): every record is published on
// ingest.<org>.<lane>.<collector> and processed by the consumer of its lane
// in the tenant scope of the principal that delivered it. A record_id is
// processed once within RecordDedupeWindow; a lane handles one batch at a
// time, ordered by observed_at, so the records of a device with the same
// strongest identity key are processed serially in source-time order
// (lanes.go, WP-334).

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

// Subject returns the subject the message is published on with lanes
// ingest lanes (LaneSubject).
func (m *IngestMessage) Subject(lanes int) string {
	return LaneSubject(m.OrganizationID, m.lane(lanes), m.CollectorID)
}

// lane is the ingest lane of the message's record.
func (m *IngestMessage) lane(lanes int) int { return IngestLane(&m.Item, lanes) }

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
// messages by observed_at and processes them per organization and ingest
// lane, serially per organization and lane within this process (per
// organization when its lanes do not run side by side, see serialLane),
// each in the tenant scope it carries.
func (h *Handler) ProcessMessages(ctx context.Context, msgs []IngestMessage) (BulkIngestResponse, error) {
	ordered := append([]IngestMessage(nil), msgs...)
	sortByObservedAt(ordered)
	var total BulkIngestResponse
	for len(ordered) > 0 {
		head := ordered[0]
		lane := h.serialLane(head.lane(h.ingestLanes))
		var group []IngestItem
		var rest []IngestMessage
		for i := range ordered {
			if ordered[i].OrganizationID == head.OrganizationID && sameScope(ordered[i].Scope, head.Scope) &&
				h.serialLane(ordered[i].lane(h.ingestLanes)) == lane {
				group = append(group, ordered[i].item())
			} else {
				rest = append(rest, ordered[i])
			}
		}
		ordered = rest
		resp, err := h.processSerially(head.context(ctx), head.OrganizationID, lane, group)
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

// processSerially processes the items of one lane of an organization while
// no other ingest of that lane runs in this process. With one lane, or when
// the lanes of an organization do not run side by side (serialLane), that
// is the organization.
func (h *Handler) processSerially(ctx context.Context, orgID string, lane int, items []IngestItem) (BulkIngestResponse, error) {
	mu := h.laneMutex(orgID, h.serialLane(lane))
	mu.Lock()
	defer mu.Unlock()
	return h.processItems(ctx, orgID, items)
}

// settledKey carries the records of a consumed batch that need no further
// delivery.
type settledKey struct{}

// settledRecords are the ledger keys of the records that were skipped as
// already processed, or processed and entered in the ledger.
type settledRecords struct {
	mu   sync.Mutex
	keys map[string]bool
}

// withSettled returns ctx collecting the settled records of an ingest.
func withSettled(ctx context.Context) (context.Context, *settledRecords) {
	s := &settledRecords{keys: map[string]bool{}}
	return context.WithValue(ctx, settledKey{}, s), s
}

// settle records items as settled in the collector of ctx, if any.
func settle(ctx context.Context, items []IngestItem) {
	s, ok := ctx.Value(settledKey{}).(*settledRecords)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		if items[i].RecordID != "" {
			s.keys[ledgerKey(items[i].RecordID)] = true
		}
	}
}

func (s *settledRecords) has(recordID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return recordID != "" && s.keys[ledgerKey(recordID)]
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

// ledgerKey is the record_id as the ledger stores it: a uuid in canonical
// lower-case form. The contract accepts upper-case hex digits; PostgreSQL
// reads both spellings as one uuid and returns the lower-case one, so the
// ledger compares and deduplicates the canonical form.
func ledgerKey(recordID string) string {
	if id, ok := canonicalUUID(recordID); ok {
		return id
	}
	return recordID
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
			ids = append(ids, ledgerKey(items[i].RecordID))
		}
	}
	if len(ids) == 0 {
		return items, 0, nil
	}
	seen, err := ledger.ProcessedRecords(ctx, orgID, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("read processed records: %w", err)
	}
	inLedger := maps.Clone(seen)
	// A record in the ledger is settled whatever happens to this batch; a
	// repeat within the batch is not, it shares the fate of its first
	// occurrence.
	var processed []IngestItem
	for i := range items {
		id := ledgerKey(items[i].RecordID)
		if id != "" && seen[id] {
			duplicates++
			continue
		}
		if id != "" {
			seen[id] = true
		}
		kept = append(kept, items[i])
	}
	for i := range items {
		if id := ledgerKey(items[i].RecordID); id != "" && inLedger[id] {
			processed = append(processed, items[i])
		}
	}
	settle(ctx, processed)
	return kept, duplicates, nil
}

// markProcessed remembers the record_ids of processed items, each once in
// canonical form (the last item of a repeated record_id wins, whatever its
// spelling: two spellings of one uuid in the one ledger statement would fail
// it); without record_ids nothing is written.
func (h *Handler) markProcessed(ctx context.Context, orgID string, items []IngestItem) error {
	ledger, ok := h.repo.(RecordLedger)
	if !ok {
		return nil
	}
	var records []ProcessedRecord
	index := map[string]int{}
	for i := range items {
		if items[i].RecordID == "" {
			continue
		}
		rec := ProcessedRecord{RecordID: ledgerKey(items[i].RecordID), CollectorID: items[i].CollectorID, ObservedAt: items[i].ObservedAt}
		if j, seen := index[rec.RecordID]; seen {
			records[j] = rec
			continue
		}
		index[rec.RecordID] = len(records)
		records = append(records, rec)
	}
	if len(records) == 0 {
		return nil
	}
	return ledger.MarkRecordsProcessed(ctx, orgID, records)
}

// Names of the JetStream stream and consumers of the ingest pipeline.
const (
	IngestStream = "RETICORA_INGEST"
	// IngestConsumer is the consumer of the releases before the lanes. It
	// keeps draining their subjects ingest.<org>.<collector> (filter
	// ingest.*.*, disjoint from the lanes) and is never deleted by the
	// server: a rolling upgrade or a rollback loses no message.
	IngestConsumer = "reticora-ingest"
	// IngestLaneConsumerPrefix plus the lane number names the consumer of a
	// lane (filter ingest.*.<lane>.*).
	IngestLaneConsumerPrefix = "reticora-ingest-lane-"
	legacyIngestFilter       = "ingest.*.*"
	// ingestBatch is the number of messages one fetch processes together;
	// MaxAckPending of every consumer equals it, so one batch per lane is in
	// flight across all server instances.
	ingestBatch      = 100
	ingestMaxDeliver = 5
	// laneRefresh is how often the queue restores the filter of the former
	// consumer and looks for lanes to drain.
	laneRefresh = 30 * time.Second
	// legacyLane is the lane of the former consumer.
	legacyLane = -1
)

func laneConsumerName(lane int) string { return IngestLaneConsumerPrefix + strconv.Itoa(lane) }

func laneFilter(lane int) string {
	if lane == legacyLane {
		return legacyIngestFilter
	}
	return "ingest.*." + strconv.Itoa(lane) + ".*"
}

// laneLabel is the lane in /readyz and the metrics: its number, or
// "legacy" for the former consumer.
func laneLabel(lane int) string {
	if lane == legacyLane {
		return "legacy"
	}
	return strconv.Itoa(lane)
}

// laneOf returns the lane a consumer name stands for; ok is false for the
// other consumers of the stream.
func laneOf(name string) (lane int, ok bool) {
	if name == IngestConsumer {
		return legacyLane, true
	}
	rest, found := strings.CutPrefix(name, IngestLaneConsumerPrefix)
	if !found {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

func ingestConsumerConfig(name string, lane int) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       name,
		FilterSubject: laneFilter(lane),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    ingestMaxDeliver,
		MaxAckPending: ingestBatch,
	}
}

var ingestDeadLetters atomic.Int64

// IngestDeadLetters is the number of ingest messages given up since start:
// undecodable, or failed ingestMaxDeliver times.
func IngestDeadLetters() int64 { return ingestDeadLetters.Load() }

// processingSlots bounds the batches the lanes of a process work on at the
// same time to a quarter of the database pool, so that the ingest leaves
// the API its connections whatever the lane count (WP-334).
func processingSlots() int { return max(1, database.PoolMaxConns/4) }

// NATSIngestQueue is the ingest.> pipeline on NATS JetStream: a consumer
// per lane, the former consumer of the releases before the lanes, and the
// lanes that a configuration with more lanes left behind (drain lanes).
type NATSIngestQueue struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	stream  jetstream.Stream
	lanes   int
	handler *Handler
	// slots holds a token per batch in work (processingSlots).
	slots chan struct{}
	// sample is the backlog Backlog read last (ReadinessDetail).
	sample atomic.Pointer[IngestBacklog]

	mu        sync.Mutex
	consumers map[string]*laneConsumer
}

// laneConsumer is a consumer the queue fetches from. Own consumers (the
// lanes 0..lanes-1 and the former consumer) are created again when they
// disappear; a drain lane is consumed until an operator removes it.
type laneConsumer struct {
	consumer jetstream.Consumer
	lane     int
	own      bool
}

// NewNATSIngestQueue connects to NATS and ensures the work-queue stream on
// ingest.>, the consumers of its lanes (1..MaxIngestLanes, RETICORA_INGEST_
// LANES) and the former consumer, restricted to the subjects of the
// releases before the lanes.
func NewNATSIngestQueue(ctx context.Context, url string, lanes int, opts ...nats.Option) (*NATSIngestQueue, error) {
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
	q := &NATSIngestQueue{nc: nc, js: js, stream: stream, lanes: clampLanes(lanes),
		slots: make(chan struct{}, processingSlots()), consumers: map[string]*laneConsumer{}}
	// The former consumer first: a work-queue stream refuses a filtered
	// consumer next to an unfiltered one, so the consumer of the releases
	// before must be restricted to their subjects before any lane exists.
	// The update keeps its pending messages and their acknowledgements.
	if err = q.ensure(ctx, IngestConsumer, legacyLane); err != nil {
		nc.Close()
		return nil, err
	}
	for lane := range q.lanes {
		if err = q.ensure(ctx, laneConsumerName(lane), lane); err != nil {
			nc.Close()
			return nil, err
		}
	}
	if err = q.discover(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	ingestLanesGauge.Set(float64(q.lanes))
	return q, nil
}

// Lanes is the number of lanes the queue publishes to.
func (q *NATSIngestQueue) Lanes() int { return q.lanes }

// ensure creates or updates an own consumer.
func (q *NATSIngestQueue) ensure(ctx context.Context, name string, lane int) error {
	c, err := q.stream.CreateOrUpdateConsumer(ctx, ingestConsumerConfig(name, lane))
	if err != nil {
		return fmt.Errorf("ensure consumer %s: %w", name, err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.consumers[name] = &laneConsumer{consumer: c, lane: lane, own: true}
	return nil
}

// owns reports whether lane is one this instance keeps.
func (q *NATSIngestQueue) owns(lane int) bool { return lane == legacyLane || lane < q.lanes }

// discover rebuilds the drain lanes from the consumers of the stream: a
// lane consumer beyond this instance's lanes is drained (a configuration
// with more lanes published there), one that disappeared is forgotten.
func (q *NATSIngestQueue) discover(ctx context.Context) error {
	lister := q.stream.ConsumerNames(ctx)
	present := map[string]bool{}
	for name := range lister.Name() {
		present[name] = true
	}
	if err := lister.Err(); err != nil {
		return fmt.Errorf("list ingest consumers: %w", err)
	}
	for name := range present {
		lane, ok := laneOf(name)
		if !ok || q.owns(lane) || q.consumer(name) != nil {
			continue
		}
		c, err := q.stream.Consumer(ctx, name)
		if errors.Is(err, jetstream.ErrConsumerNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("drain lane %s: %w", name, err)
		}
		q.mu.Lock()
		q.consumers[name] = &laneConsumer{consumer: c, lane: lane}
		q.mu.Unlock()
		slog.Info("ingest lane beyond RETICORA_INGEST_LANES is drained", "event", "ingest.lane.drain", "consumer", name)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for name, lc := range q.consumers {
		if !lc.own && !present[name] {
			delete(q.consumers, name)
		}
	}
	return nil
}

// refresh restores the filter of the former consumer, which a server of an
// earlier release sets back to "all subjects" when it starts, and rebuilds
// the drain lanes.
func (q *NATSIngestQueue) refresh(ctx context.Context) {
	c, err := q.stream.Consumer(ctx, IngestConsumer)
	switch {
	case errors.Is(err, jetstream.ErrConsumerNotFound),
		err == nil && c.CachedInfo().Config.FilterSubject != legacyIngestFilter:
		if err = q.ensure(ctx, IngestConsumer, legacyLane); err != nil {
			slog.Warn("former ingest consumer not restored", "consumer", IngestConsumer, "error", err)
		} else {
			slog.Warn("filter of the former ingest consumer restored", "event", "ingest.legacy_filter_restored",
				"consumer", IngestConsumer, "filter", legacyIngestFilter)
		}
	case err != nil:
		slog.Warn("former ingest consumer not checked", "consumer", IngestConsumer, "error", err)
	}
	if err = q.discover(ctx); err != nil {
		slog.Warn("ingest drain lanes not checked", "error", err)
	}
}

// consumer returns the current consumer of name, or nil.
func (q *NATSIngestQueue) consumer(name string) jetstream.Consumer {
	q.mu.Lock()
	defer q.mu.Unlock()
	if lc, ok := q.consumers[name]; ok {
		return lc.consumer
	}
	return nil
}

// names lists the consumers the queue fetches from.
func (q *NATSIngestQueue) names() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Sorted(maps.Keys(q.consumers))
}

// Publish stores each message on the subject of its lane
// (ingest.<org>.<lane>.<collector>) and waits for the stream's
// acknowledgement.
func (q *NATSIngestQueue) Publish(ctx context.Context, msgs []IngestMessage) error {
	for i := range msgs {
		data, err := json.Marshal(&msgs[i])
		if err != nil {
			return fmt.Errorf("encode ingest message: %w", err)
		}
		if _, err = q.js.Publish(ctx, msgs[i].Subject(q.lanes), data); err != nil {
			return fmt.Errorf("publish ingest message: %w", err)
		}
	}
	return nil
}

// Close closes the NATS connection.
func (q *NATSIngestQueue) Close() { q.nc.Close() }

// Attach sets the handler that processes consumed messages and makes it
// publish to this queue with the queue's lanes.
func (q *NATSIngestQueue) Attach(h *Handler) {
	q.handler = h
	h.WithIngestQueue(q).WithIngestLanes(q.lanes)
}

// Run consumes every lane with the attached handler until ctx ends: one
// goroutine per consumer, started again for consumers found later.
func (q *NATSIngestQueue) Run(ctx context.Context) {
	if q.handler == nil {
		slog.Error("ingest consumer not started: no handler attached")
		return
	}
	// The collectors deliver over the edge to the same handler (COL-08).
	go func() {
		if err := q.handler.ServeCollectorEdge(ctx, q.nc); err != nil {
			slog.Error("collector edge data path", "error", err)
		}
	}()
	var wg sync.WaitGroup
	running := map[string]bool{}
	// A lane that ends hands over its name unless the queue stops: no lane
	// waits for a reader at shutdown, whatever the number of lanes.
	ended := make(chan string)
	start := func() {
		for _, name := range q.names() {
			if running[name] {
				continue
			}
			running[name] = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				runIngestConsumer(ctx, &laneSource{q: q, name: name}, q.handler, q.slots)
				select {
				case ended <- name:
				case <-ctx.Done():
				}
			}()
		}
	}
	start()
	ticker := time.NewTicker(laneRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case name := <-ended:
			delete(running, name)
		case <-ticker.C:
			q.refresh(ctx)
			start()
		}
	}
}

// ingestSource fetches batches of deliveries; laneSource and the tests
// implement it.
type ingestSource interface {
	Fetch(ctx context.Context, limit int) ([]ingestDelivery, error)
	// Recheck runs when a fetch found no consumer: it creates an own
	// consumer again and reports false when the lane ended (a drain lane
	// an operator removed).
	Recheck(ctx context.Context) bool
}

type ingestDelivery interface {
	Data() []byte
	Ack() error
	Nak() error
	Term() error
	Delivered() uint64
}

// laneSource fetches from the current consumer of a lane.
type laneSource struct {
	q    *NATSIngestQueue
	name string
}

func (s *laneSource) Fetch(ctx context.Context, limit int) ([]ingestDelivery, error) {
	c := s.q.consumer(s.name)
	if c == nil {
		return nil, jetstream.ErrConsumerNotFound
	}
	return fetch(ctx, c, limit)
}

func (s *laneSource) Recheck(ctx context.Context) bool {
	q := s.q
	q.mu.Lock()
	lc, known := q.consumers[s.name]
	q.mu.Unlock()
	if !known {
		return false
	}
	_, err := q.stream.Consumer(ctx, s.name)
	switch {
	case errors.Is(err, jetstream.ErrConsumerNotFound) && lc.own:
		if err = q.ensure(ctx, s.name, lc.lane); err == nil {
			slog.Warn("ingest consumer created again", "event", "ingest.lane.recreated", "consumer", s.name)
			return true
		}
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		q.mu.Lock()
		delete(q.consumers, s.name)
		q.mu.Unlock()
		slog.Info("drained ingest lane removed", "event", "ingest.lane.removed", "consumer", s.name)
		return false
	}
	if err != nil {
		slog.Warn("ingest consumer not checked", "consumer", s.name, "error", err)
	}
	// The consumer exists (the server was briefly without a responder) or
	// could not be checked: wait a moment and fetch again.
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}

func fetch(ctx context.Context, consumer jetstream.Consumer, limit int) ([]ingestDelivery, error) {
	// Wait at most a second for messages, and stop with ctx.
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	batch, err := consumer.Fetch(limit, jetstream.FetchContext(waitCtx))
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

// consumerGone reports a fetch at a consumer that no longer exists: the pull
// finds no responder, or a pull waiting at the deletion is told so. That is
// no worker failure; the source checks the consumer.
func consumerGone(err error) bool {
	return errors.Is(err, nats.ErrNoResponders) || errors.Is(err, jetstream.ErrConsumerDeleted) ||
		errors.Is(err, jetstream.ErrConsumerNotFound)
}

// runIngestConsumer is the loop of a lane: fetch a batch, process it in
// observed_at order within a processing slot (slots, nil: unbounded),
// acknowledge what was processed. It ends with ctx, or when the lane ended
// (Recheck).
func runIngestConsumer(ctx context.Context, src ingestSource, h *Handler, slots chan struct{}) {
	for ctx.Err() == nil {
		deliveries, err := src.Fetch(ctx, ingestBatch)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, nats.ErrTimeout) &&
			!errors.Is(err, context.DeadlineExceeded) && len(deliveries) == 0 {
			if consumerGone(err) {
				if !src.Recheck(ctx) {
					return
				}
				continue
			}
			observability.ObserveWorkerRun("ingest", 1)
			slog.Warn("ingest consumer fetch failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(deliveries) > 0 && slots != nil {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				// Stopping before the batch started: it goes back to the
				// queue.
				for _, d := range deliveries {
					_ = d.Nak()
				}
				return
			}
			consumeBatch(ctx, h, deliveries)
			<-slots
		} else {
			consumeBatch(ctx, h, deliveries)
		}
		observability.ObserveWorkerRun("ingest", 0)
	}
}

// IngestBacklog is the backlog of the ingest consumers (/readyz details,
// OPS-05): per lane ("legacy" for the former consumer) the messages not yet
// delivered plus those delivered but unacknowledged, summed over all
// organizations, as read at SampledAt.
type IngestBacklog struct {
	Lanes     int            `json:"lanes"`
	Backlog   map[string]int `json:"backlog"`
	Total     int            `json:"total"`
	SampledAt time.Time      `json:"sampled_at"`
}

// LaneBacklog reads the backlog of every ingest consumer of the stream with
// one listing.
func (q *NATSIngestQueue) LaneBacklog(ctx context.Context) (*IngestBacklog, error) {
	// Milliseconds: what a JSON client parses as a date.
	out := &IngestBacklog{Lanes: q.lanes, Backlog: map[string]int{}, SampledAt: time.Now().UTC().Truncate(time.Millisecond)}
	lister := q.stream.ListConsumers(ctx)
	for info := range lister.Info() {
		lane, ok := laneOf(info.Name)
		if !ok {
			continue
		}
		n := int(info.NumPending) + info.NumAckPending //nolint:gosec // a message count
		out.Backlog[laneLabel(lane)] = n
		out.Total += n
	}
	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("ingest consumer info: %w", err)
	}
	return out, nil
}

// Backlog reports the messages waiting for the ingest worker, summed over
// the lanes (reticora_worker_backlog), and samples the backlog per lane into
// reticora_ingest_lane_backlog (OPS-05) and for ReadinessDetail. The worker
// set calls it every 30 s while the ingest worker runs.
func (q *NATSIngestQueue) Backlog(ctx context.Context) (int, error) {
	backlog, err := q.LaneBacklog(ctx)
	if err != nil {
		return 0, err
	}
	laneBacklog.Reset()
	for lane, n := range backlog.Backlog {
		laneBacklog.WithLabelValues(lane).Set(float64(n))
	}
	q.sample.Store(backlog)
	return backlog.Total, nil
}

// errNoBacklogSample: Backlog has not read the backlog yet, or the ingest
// worker does not run in this instance.
var errNoBacklogSample = errors.New("ingest backlog not sampled yet")

// ReadinessDetail is the ingest detail of /readyz: the backlog Backlog read
// last, with the time it was read (sampled_at). It never asks NATS: the
// endpoint is unauthenticated, so a request must not make the server list
// the stream's consumers. Without a sample it fails (the detail reads
// "unavailable").
func (q *NATSIngestQueue) ReadinessDetail(context.Context) (any, error) {
	backlog := q.sample.Load()
	if backlog == nil {
		return nil, errNoBacklogSample
	}
	return backlog, nil
}

// consumeBatch processes one fetched batch. Undecodable messages are
// terminated; the messages of a group (organization, scope, lane) are
// acknowledged when it was processed, and so are its records processed
// before or within it when it failed; the others are redelivered, and given
// up after ingestMaxDeliver attempts. Other consumers may process records
// of the same organization at the same time, so the transactions take the
// ingest locks whatever the lane count (Handler.locking).
func consumeBatch(ctx context.Context, h *Handler, deliveries []ingestDelivery) {
	ctx = withQueued(ctx)
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

	// One group per organization, scope and lane, processed in order. The
	// lane is the record's, whatever subject carried it: the former
	// consumer and drain lanes join the lanes of this instance.
	for len(entries) > 0 {
		head := entries[0].msg
		lane := head.lane(h.ingestLanes)
		var group, rest []entry
		for i := range entries {
			e := &entries[i]
			if e.msg.OrganizationID == head.OrganizationID && sameScope(e.msg.Scope, head.Scope) && e.msg.lane(h.ingestLanes) == lane {
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
		groupCtx, settled := withSettled(ctx)
		_, err := h.ProcessMessages(groupCtx, msgs)
		for i := range group {
			e := &group[i]
			switch {
			case err == nil, settled.has(e.msg.RecordID):
				_ = e.d.Ack()
				laneRecords.WithLabelValues(strconv.Itoa(lane)).Inc()
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
