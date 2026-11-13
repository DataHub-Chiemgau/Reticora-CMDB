package probe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Unit tests of the worker probe_ingest (WP-960, OPS-02, PRB-07): grouping
// of a fetch by organization, the counters of dropped results and the dead
// letter after the last delivery, on fake deliveries and a fake store.
//
//	cd backend && go test -count=1 -run 'TestBatchGroupingByOrganization|TestUnknownProbeDiscardedAndCounted|TestDeadLetterAfterMaxDeliver' ./internal/probe/

const (
	unitOrgA      = "7a000960-0000-4000-8000-0000000000a1"
	unitOrgB      = "7a000960-0000-4000-8000-0000000000b1"
	unitCollector = "7a000960-0000-4000-8000-0000000000c1"
	unitProbe     = "7a000960-0000-4000-8000-0000000000d1"
)

// fakeDelivery is a fetched message with what the worker did to it.
type fakeDelivery struct {
	subject   string
	data      []byte
	delivered uint64
	acked     bool
	naked     bool
	termed    bool
}

func (d *fakeDelivery) Subject() string                  { return d.subject }
func (d *fakeDelivery) Data() []byte                     { return d.data }
func (d *fakeDelivery) Ack() error                       { d.acked = true; return nil }
func (d *fakeDelivery) NakWithDelay(time.Duration) error { d.naked = true; return nil }
func (d *fakeDelivery) Term() error                      { d.termed = true; return nil }
func (d *fakeDelivery) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: d.delivered}, nil
}

// fakeStore records the writes per organization and answers with out or
// fails.
type fakeStore struct {
	writes map[string][]ResultsMessage
	order  []string
	out    IngestOutcome
	err    error
}

func (s *fakeStore) WriteResults(_ context.Context, orgID string, msgs []ResultsMessage) (IngestOutcome, error) {
	if s.writes == nil {
		s.writes = map[string][]ResultsMessage{}
	}
	s.order = append(s.order, orgID)
	s.writes[orgID] = append(s.writes[orgID], msgs...)
	return s.out, s.err
}

func unitIngester(store IngestStore) *Ingester {
	return NewIngester(nil, DefaultStream(0), store)
}

func unitBatch(t *testing.T, n int, seq uint64) []byte {
	t.Helper()
	b := &probewire.ResultBatch{CollectorID: unitCollector}
	for i := range n {
		b.Results = append(b.Results, probewire.Result{ProbeID: unitProbe, Epoch: 1, Seq: seq + uint64(i), //nolint:gosec // small
			ObservedAt: time.Now().UTC(), Status: probewire.StatusOK})
	}
	data, err := probewire.Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func unitDelivery(t *testing.T, org string, n int, seq uint64) *fakeDelivery {
	return &fakeDelivery{subject: DefaultStream(0).ResultsSubject(org, unitCollector), data: unitBatch(t, n, seq), delivered: 1}
}

// TestBatchGroupingByOrganization: a fetch with messages of two
// organizations becomes one write per organization in the order of their
// first message, with every message of that organization; an undecodable
// message or one on a malformed subject is given up at once (Term, dead
// letter "undecodable") and does not block the others, which are
// acknowledged after their write.
func TestBatchGroupingByOrganization(t *testing.T) {
	store := &fakeStore{}
	ing := unitIngester(store)
	a1, b1, a2 := unitDelivery(t, unitOrgA, 2, 1), unitDelivery(t, unitOrgB, 3, 1), unitDelivery(t, unitOrgA, 1, 3)
	garbage := &fakeDelivery{subject: DefaultStream(0).ResultsSubject(unitOrgA, unitCollector), data: []byte("{"), delivered: 1}
	badSubject := &fakeDelivery{subject: "probe.results.not-an-org", data: unitBatch(t, 1, 1), delivered: 1}
	before := testutil.ToFloat64(IngestDeadLetters(DeadLetterDecode))

	ing.process(context.Background(), []ingestDelivery{a1, b1, garbage, a2, badSubject})

	if len(store.order) != 2 || store.order[0] != unitOrgA || store.order[1] != unitOrgB {
		t.Fatalf("writes in order %v, want [A B]", store.order)
	}
	if got := store.writes[unitOrgA]; len(got) != 2 || len(got[0].Results) != 2 || len(got[1].Results) != 1 ||
		got[0].CollectorID != unitCollector {
		t.Errorf("organization A got %+v, want both of its messages", got)
	}
	if got := store.writes[unitOrgB]; len(got) != 1 || len(got[0].Results) != 3 {
		t.Errorf("organization B got %+v, want its message", got)
	}
	for name, d := range map[string]*fakeDelivery{"a1": a1, "b1": b1, "a2": a2} {
		if !d.acked || d.naked || d.termed {
			t.Errorf("%s: acked %v naked %v termed %v, want acknowledged", name, d.acked, d.naked, d.termed)
		}
	}
	for name, d := range map[string]*fakeDelivery{"garbage": garbage, "bad subject": badSubject} {
		if !d.termed || d.acked {
			t.Errorf("%s: termed %v acked %v, want given up", name, d.termed, d.acked)
		}
	}
	if got := testutil.ToFloat64(IngestDeadLetters(DeadLetterDecode)) - before; got != 2 {
		t.Errorf("undecodable dead letters +%v, want +2", got)
	}
}

// TestUnknownProbeDiscardedAndCounted: the results the store dropped
// because their probe does not exist are counted under
// reticora_probe_ingest_discarded_total{reason="unknown_probe"}; the
// message is still acknowledged (redelivering it would not help), and the
// written rows are counted apart.
func TestUnknownProbeDiscardedAndCounted(t *testing.T) {
	store := &fakeStore{out: IngestOutcome{Written: 4, UnknownProbe: 3}}
	ing := unitIngester(store)
	d := unitDelivery(t, unitOrgA, 7, 1)
	discarded := testutil.ToFloat64(IngestDiscarded(DiscardUnknownProbe))
	rows := testutil.ToFloat64(ingestRows)

	ing.process(context.Background(), []ingestDelivery{d})

	if got := testutil.ToFloat64(IngestDiscarded(DiscardUnknownProbe)) - discarded; got != 3 {
		t.Errorf("unknown_probe +%v, want +3", got)
	}
	if got := testutil.ToFloat64(ingestRows) - rows; got != 4 {
		t.Errorf("rows +%v, want +4", got)
	}
	if !d.acked {
		t.Error("message with dropped results not acknowledged")
	}
}

// TestDeadLetterAfterMaxDeliver: a message whose write failed is
// redelivered with a delay (Nak) up to the fourth delivery and given up at
// the fifth (Term, reticora_probe_ingest_dead_letters_total{reason=
// "max_deliver"}), never acknowledged; the consumer carries MaxDeliver 5,
// explicit acknowledgement and the filter on the results subjects. The
// /readyz detail before the first backlog sample is the expected "not
// sampled yet" state.
func TestDeadLetterAfterMaxDeliver(t *testing.T) {
	store := &fakeStore{err: errors.New("database down")}
	ing := unitIngester(store)
	cfg := ing.ConsumerConfig()
	if cfg.Durable != IngestConsumer || cfg.MaxDeliver != 5 || cfg.AckPolicy != jetstream.AckExplicitPolicy ||
		cfg.FilterSubject != "probe.results.>" {
		t.Errorf("consumer config %+v", cfg)
	}
	before := testutil.ToFloat64(IngestDeadLetters(DeadLetterMaxDelive))
	for delivered := uint64(1); delivered <= IngestMaxDeliver; delivered++ {
		d := unitDelivery(t, unitOrgA, 1, 1)
		d.delivered = delivered
		ing.process(context.Background(), []ingestDelivery{d})
		last := delivered == IngestMaxDeliver
		if d.acked || d.termed != last || d.naked == last {
			t.Errorf("delivery %d: acked %v termed %v naked %v", delivered, d.acked, d.termed, d.naked)
		}
	}
	if got := testutil.ToFloat64(IngestDeadLetters(DeadLetterMaxDelive)) - before; got != 1 {
		t.Errorf("max_deliver dead letters +%v, want +1", got)
	}
	if _, err := ing.ReadinessDetail(context.Background()); !errors.Is(err, discovery.ErrNoBacklogSample) {
		t.Errorf("readiness before the first sample: %v, want ErrNoBacklogSample", err)
	}
}
