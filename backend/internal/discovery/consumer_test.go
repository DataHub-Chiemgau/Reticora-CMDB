package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func consumerRecord(model string, observedAt time.Time) DeviceRecord {
	return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "cons-srv", IP: "192.0.2.85",
		Serial: "CONS-SN", Model: model}, observedAt)
}

func postRecords(t *testing.T, mux http.Handler, ctx context.Context, records ...DeviceRecord) BulkIngestResponse {
	t.Helper()
	body, err := json.Marshal(DeviceRecordBatch{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Collector-ID", "col-85")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	var resp BulkIngestResponse
	if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestIngestPipelineDedupesAndOrders covers WP-085 (REC-01, NFR-03) without
// a queue: a record_id is processed once, and the records of a batch are
// processed in observed_at order whatever order they arrive in.
func TestIngestPipelineDedupesAndOrders(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})

	t0 := time.Now().UTC().Add(-time.Hour)
	first := consumerRecord("m1", t0)
	if resp := postRecords(t, mux, ctx, first); resp.Created != 1 {
		t.Fatalf("first record: %+v", resp)
	}
	if resp := postRecords(t, mux, ctx, first); resp.Duplicates != 1 || resp.Updated != 0 || resp.Created != 0 {
		t.Errorf("repeated record_id: %+v", resp)
	}

	// Newest first in the request; processed oldest first, so the newest
	// observation is the one that stays.
	newest, middle := consumerRecord("m3", t0.Add(2*time.Minute)), consumerRecord("m2", t0.Add(time.Minute))
	if resp := postRecords(t, mux, ctx, newest, middle, newest); resp.Updated != 2 || resp.Duplicates != 1 {
		t.Errorf("batch out of order: %+v", resp)
	}
	items, _, err := cis.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil || len(items) != 1 || items[0].Model != "m3" {
		t.Fatalf("CI after the batch: %+v, %v", items, err)
	}
}

type captureQueue struct {
	mu   sync.Mutex
	msgs []IngestMessage
}

func (q *captureQueue) Publish(_ context.Context, msgs []IngestMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range msgs {
		// Through JSON, as on NATS.
		raw, err := json.Marshal(&msgs[i])
		if err != nil {
			return err
		}
		var decoded IngestMessage
		if err = json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		q.msgs = append(q.msgs, decoded)
	}
	return nil
}

type fakeDelivery struct {
	data      []byte
	delivered uint64
	result    string
}

func (d *fakeDelivery) Data() []byte      { return d.data }
func (d *fakeDelivery) Ack() error        { d.result = "ack"; return nil }
func (d *fakeDelivery) Nak() error        { d.result = "nak"; return nil }
func (d *fakeDelivery) Term() error       { d.result = "term"; return nil }
func (d *fakeDelivery) Delivered() uint64 { return d.delivered }

// failingCIs fails every update, so a consumed batch cannot be processed.
type failingCIs struct{ ci.Repository }

func (failingCIs) List(context.Context, string, ci.FilterParams, api.PaginationParams) ([]ci.Item, int, error) {
	return nil, 0, errors.New("database down")
}

// TestIngestQueuePublishesAndConsumes covers WP-085 (REC-01): with a queue
// the HTTP ingest publishes on ingest.<org>.<collector> and answers 202; the
// message carries the principal's tenant scope; the consumer processes a
// batch in observed_at order and acknowledges it, redelivers a failed batch,
// gives it up after the last delivery and terminates undecodable messages.
func TestIngestQueuePublishesAndConsumes(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	queue := &captureQueue{}
	handler := NewHandler(repo, cis).WithIngestQueue(queue)
	mux := chi.NewRouter()
	handler.RegisterRoutes(mux)

	scope := database.TenantScope{OrgID: "11111111-1111-1111-1111-111111111111", UserID: "22222222-2222-2222-2222-222222222222",
		Clients: database.ScopeIDs("33333333-3333-3333-3333-333333333333"), Sites: database.AllScopes(), Teams: database.ScopeIDs()}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: scope.OrgID, UserID: scope.UserID})

	t0 := time.Now().UTC().Add(-time.Hour)
	resp := postRecords(t, mux, ctx, consumerRecord("q2", t0.Add(time.Minute)), consumerRecord("q1", t0))
	if resp.Queued != 2 || resp.Received != 2 || resp.Created != 0 {
		t.Fatalf("queued ingest: %+v", resp)
	}
	if len(queue.msgs) != 2 {
		t.Fatalf("published %d messages", len(queue.msgs))
	}
	msg := queue.msgs[0]
	if msg.Subject(1) != "ingest."+scope.OrgID+".0.col-85" || msg.Item.Model != "q1" || msg.RecordID == "" || !msg.ObservedAt.Equal(t0) {
		t.Errorf("first message: subject %s, %+v", msg.Subject(1), msg)
	}
	if want := LaneSubject(scope.OrgID, IngestLane(&msg.Item, 4), "col-85"); msg.Subject(4) != want {
		t.Errorf("subject with 4 lanes: %s, want %s", msg.Subject(4), want)
	}
	restored, ok := database.TenantScopeFromContext(msg.context(context.Background()))
	if !ok || restored.UserID != scope.UserID || restored.Clients.IsAll() || len(restored.Clients.IDs()) != 1 ||
		!restored.Sites.IsAll() || restored.Teams.IsAll() || len(restored.Teams.IDs()) != 0 {
		t.Errorf("scope after the queue: %+v", restored)
	}
	if got := tenant.FromContext(msg.context(context.Background())); got.OrganizationID != scope.OrgID {
		t.Errorf("tenant after the queue: %+v", got)
	}

	// The consumer processes the batch in observed_at order: newest wins.
	deliveries := func(msgs []IngestMessage, delivered uint64) []*fakeDelivery {
		var out []*fakeDelivery
		for i := len(msgs) - 1; i >= 0; i-- { // reversed on purpose
			raw, _ := json.Marshal(&msgs[i])
			out = append(out, &fakeDelivery{data: raw, delivered: delivered})
		}
		return out
	}
	asSource := func(ds []*fakeDelivery) []ingestDelivery {
		out := make([]ingestDelivery, len(ds))
		for i, d := range ds {
			out[i] = d
		}
		return out
	}
	batch := deliveries(queue.msgs, 1)
	consumeBatch(context.Background(), handler, asSource(batch))
	for _, d := range batch {
		if d.result != "ack" {
			t.Errorf("processed message: %s", d.result)
		}
	}
	items, _, _ := cis.List(context.Background(), scope.OrgID, ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if len(items) != 1 || items[0].Model != "q2" {
		t.Fatalf("CI after consuming: %+v", items)
	}

	// A batch that fails is redelivered, and given up at the last attempt;
	// undecodable messages are terminated.
	broken := NewHandler(repo, failingCIs{cis})
	fresh := []IngestMessage{queue.msgs[0]}
	fresh[0].RecordID = plugins.NewRecordID()
	retry := deliveries(fresh, 1)
	last := deliveries(fresh, ingestMaxDeliver)
	garbage := &fakeDelivery{data: []byte("{not json"), delivered: 1}
	before := IngestDeadLetters()
	consumeBatch(context.Background(), broken, asSource(append(retry, garbage)))
	consumeBatch(context.Background(), broken, asSource(last))
	if retry[0].result != "nak" || last[0].result != "term" || garbage.result != "term" {
		t.Errorf("failed batch: retry %s, last %s, garbage %s", retry[0].result, last[0].result, garbage.result)
	}
	if IngestDeadLetters() != before+2 {
		t.Errorf("dead letters %d, want %d", IngestDeadLetters(), before+2)
	}
}

// poisonCIs fails the creation of a CI with the model "transient" like a
// lost database connection and with the model "forbidden" like the
// row-level security of a client outside the sender's scope.
type poisonCIs struct{ ci.Repository }

func (r poisonCIs) Create(ctx context.Context, item *ci.Item) error {
	switch item.Model {
	case "transient":
		return errors.New("connection reset by peer")
	case "forbidden":
		return &pgconn.PgError{Code: "42501", Message: "new row violates row-level security policy for table \"ci\""}
	}
	return r.Repository.Create(ctx, item)
}

// poisonGroup is the group [A1 transient, A2, B1, C1 scope failure, C2]
// (WP-481): A1 and A2 share a serial number, as do C1 and C2.
func poisonGroup(t0 time.Time) []DeviceRecord {
	rec := func(serial, ip, model string, at time.Duration) DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial, IP: ip, Model: model},
			t0.Add(at))
	}
	return []DeviceRecord{rec("SN-A", "192.0.2.1", "transient", 0), rec("SN-A", "192.0.2.1", "ok", time.Second),
		rec("SN-B", "192.0.2.2", "ok", 2*time.Second), rec("SN-C", "192.0.2.3", "forbidden", 3*time.Second),
		rec("SN-C", "192.0.2.3", "ok", 4*time.Second)}
}

// TestConsumeBatchIsolatesRecordFailures covers WP-481 (REC-01, NFR-03,
// E-56, F-090): a record that fails does not take the other records of its
// group down. In the group [A1 transient, A2, B1, C1 scope failure, C2] A1 is
// redelivered, and so is A2, which shares A1's serial number and is not
// processed before it; B1 is processed and acknowledged; C1 can never
// succeed and is terminated at its first delivery and counted as a dead
// letter; C2, after a permanent failure of the same device, is processed.
func TestConsumeBatchIsolatesRecordFailures(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	h := NewHandler(repo, poisonCIs{cis})
	recs := poisonGroup(time.Now().UTC().Add(-time.Hour))
	items := make([]IngestItem, len(recs))
	for i := range recs {
		items[i] = itemFromRecord(&recs[i])
	}
	msgs := newIngestMessages(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"}, "col-1", items)
	var fakes []*fakeDelivery
	var deliveries []ingestDelivery
	for i := range msgs {
		raw, err := json.Marshal(&msgs[i])
		if err != nil {
			t.Fatal(err)
		}
		d := &fakeDelivery{data: raw, delivered: 1}
		fakes = append(fakes, d)
		deliveries = append(deliveries, d)
	}
	deadBefore, permanentBefore := IngestDeadLetters(), testutil.ToFloat64(deadLetters.WithLabelValues("permanent"))
	consumeBatch(context.Background(), h, deliveries)

	names := []string{"A1", "A2", "B1", "C1", "C2"}
	want := []string{"nak", "nak", "ack", "term", "ack"}
	for i, d := range fakes {
		if d.result != want[i] {
			t.Errorf("%s: %s, want %s", names[i], d.result, want[i])
		}
	}
	if n := IngestDeadLetters() - deadBefore; n != 1 {
		t.Errorf("dead letters grew by %d, want 1 (C1)", n)
	}
	if n := testutil.ToFloat64(deadLetters.WithLabelValues("permanent")) - permanentBefore; n != 1 {
		t.Errorf("reticora_ingest_dead_letters_total{reason=\"permanent\"} grew by %v, want 1", n)
	}
	stored, _, _ := cis.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	got := map[string]string{}
	for i := range stored {
		got[stored[i].SerialNumber] = stored[i].Model
	}
	if len(got) != 2 || got["SN-B"] != "ok" || got["SN-C"] != "ok" {
		t.Errorf("CIs %v, want SN-B and SN-C (from C2); SN-A waits for A1", got)
	}

	// Delivered again with A1 now succeeding, A1 and A2 are processed in
	// their order: the newer observation stays.
	recs[0].Classification.Model = "m1"
	again := []IngestItem{itemFromRecord(&recs[0]), itemFromRecord(&recs[1])}
	if _, err := h.ProcessMessages(context.Background(),
		newIngestMessages(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"}, "col-1", again)); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = cis.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	for i := range stored {
		if stored[i].SerialNumber == "SN-A" && stored[i].Model != "ok" {
			t.Errorf("SN-A after the redelivery: model %q, want A2's", stored[i].Model)
		}
	}
}

// TestInlineIngestReportsRecordFailures covers WP-481 (REC-01, F-090) for
// the HTTP ingest without a queue: the valid records are processed and the
// answer is 202 with the accepted and rejected records and an error per
// record_id; only when every record is rejected does the request keep the
// status of its first failure.
func TestInlineIngestReportsRecordFailures(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo, poisonCIs{cis}).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	post := func(records []DeviceRecord) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		return w
	}

	recs := poisonGroup(time.Now().UTC().Add(-time.Hour))
	w := post(recs)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest with failing records: %d %s, want 202", w.Code, w.Body.String())
	}
	var resp BulkIngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Received != 5 || resp.Accepted != 2 || resp.Rejected != 3 || resp.Created != 2 || len(resp.Errors) != 3 {
		t.Fatalf("answer %+v, want 5 received, 2 accepted (B1, C2) and created, 3 rejected", resp)
	}
	byID := map[string]IngestRecordError{}
	for _, e := range resp.Errors {
		byID[e.RecordID] = e
	}
	for _, c := range []struct {
		name      string
		rec       *DeviceRecord
		status    int
		retryable bool
	}{
		{"A1", &recs[0], http.StatusInternalServerError, true},
		{"A2", &recs[1], http.StatusServiceUnavailable, true},
		{"C1", &recs[3], http.StatusForbidden, false},
	} {
		e, ok := byID[c.rec.RecordID]
		if !ok || e.Status != c.status || e.Retryable != c.retryable || e.Detail == "" {
			t.Errorf("error of %s (record_id %s): %+v, want status %d, retryable %v", c.name, c.rec.RecordID, e, c.status, c.retryable)
		}
		if strings.Contains(e.Detail, "row-level") || strings.Contains(e.Detail, "connection reset") {
			t.Errorf("error of %s discloses internals: %q", c.name, e.Detail)
		}
	}

	// Every record rejected: the status of the request as before.
	only := poisonGroup(time.Now().UTC().Add(-time.Minute))[3:4]
	only[0].Identity.SerialNumber, only[0].TargetIP = "SN-D", "192.0.2.4"
	if w = post(only); w.Code != http.StatusForbidden {
		t.Errorf("ingest of one out-of-scope record: %d %s, want 403", w.Code, w.Body.String())
	}
}
