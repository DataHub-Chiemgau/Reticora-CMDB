package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
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
