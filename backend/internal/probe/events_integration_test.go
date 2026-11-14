package probe_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-979, DIS-06):
//
//	cd backend && go test -count=1 -p 1 -run 'TestTrapEvent|TestListEvents' ./internal/probe/

// eventEnv is organization A with a collector group, its collector and an
// icmp probe of a CI of client 1 on 10.79.0.1.
type eventEnv struct {
	*bridgeEnv
	group string
	seq   uint64
}

func newEventEnv(t *testing.T) *eventEnv {
	t.Helper()
	f := scopetest.Seed(t, "79")
	b := &bridgeEnv{t: t, f: f, store: probe.NewPGIngestStore(f.App)}
	e := &eventEnv{bridgeEnv: b}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM monitoring_event WHERE organization_id = $1`, f.OrgA)
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM metric_sample WHERE organization_id = $1`, f.OrgA)
	})
	e.group = b.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp979') RETURNING id::text`, f.OrgA)
	b.collect = b.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp979', 'active') RETURNING id::text`, f.OrgA)
	b.exec(`UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, e.group, b.collect)
	b.ci = f.CI(t, f.OrgA, f.Client1, "wp979-ci")
	b.probeID = b.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.79.0.1')
		RETURNING id::text`, f.OrgA, b.ci, e.group)
	b.epoch = 1
	b.exec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, $4)`, b.probeID, f.OrgA, b.collect, b.epoch)
	return e
}

var eventIDs = 0

func newEventID() string {
	eventIDs++
	return fmt.Sprintf("0193f1a2-7b3c-7d4e-8f50-%012x", eventIDs)
}

func linkDown(source string, at time.Time) probewire.Event {
	return probewire.Event{ID: newEventID(), Kind: probewire.EventLinkDown, SourceIP: source, TrapOID: "1.3.6.1.6.3.1.1.5.3",
		Varbinds: map[string]string{"1.3.6.1.2.1.2.2.1.1.7": "7"}, ObservedAt: at}
}

// send writes one result of the probe with the events.
func (e *eventEnv) send(events ...probewire.Event) probe.IngestOutcome {
	e.t.Helper()
	e.seq++
	out, err := e.store.WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{{CollectorID: e.collect,
		Results: []probewire.Result{{ProbeID: e.probeID, Epoch: e.epoch, Seq: e.seq, ObservedAt: time.Now().UTC(), Status: probewire.StatusOK}},
		Events:  events}})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

type eventRowView struct{ probeID, ciID, clientID, kind string }

func (e *eventEnv) row(id string) eventRowView {
	e.t.Helper()
	var r eventRowView
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(probe_id::text, ''), COALESCE(ci_id::text, ''),
		COALESCE(client_id::text, ''), kind FROM monitoring_event WHERE id = $1`, id).Scan(&r.probeID, &r.ciID, &r.clientID, &r.kind); err != nil {
		e.t.Fatalf("event %s: %v", id, err)
	}
	return r
}

// TestTrapEventStoredWithProbeAndCI covers AK1 and AK3 (1): a linkDown of
// the probe's target is stored with probe_id, ci_id and the client of the
// CI.
func TestTrapEventStoredWithProbeAndCI(t *testing.T) {
	e := newEventEnv(t)
	// A second probe of the same target: the icmp probe wins.
	e.exec(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target, params) VALUES ($1, $2, $3, 'tcp', '10.79.0.1', '{"port":22}')`,
		e.f.OrgA, e.ci, e.group)
	ev := linkDown("10.79.0.1", time.Now().UTC())
	if out := e.send(ev); out.Events != 1 {
		t.Fatalf("events written = %d", out.Events)
	}
	if r := e.row(ev.ID); r.probeID != e.probeID || r.ciID != e.ci || r.clientID != e.f.Client1 || r.kind != probewire.EventLinkDown {
		t.Fatalf("row = %+v", r)
	}
}

// TestTrapEventFallsBackToManagementIP covers AK3 (2): without a probe the
// CI with this management address (exactly one) is assigned.
func TestTrapEventFallsBackToManagementIP(t *testing.T) {
	e := newEventEnv(t)
	other := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp979-switch")
	e.exec(`UPDATE ci SET management_ip = '10.79.0.50' WHERE id = $1`, other)
	ev := linkDown("10.79.0.50", time.Now().UTC())
	e.send(ev)
	if r := e.row(ev.ID); r.probeID != "" || r.ciID != other {
		t.Fatalf("row = %+v, want ci %s without probe", r, other)
	}
	// Two CIs with the address: none is assigned.
	twin := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp979-twin")
	e.exec(`UPDATE ci SET management_ip = '10.79.0.50' WHERE id = $1`, twin)
	ev2 := linkDown("10.79.0.50", time.Now().UTC())
	e.send(ev2)
	if r := e.row(ev2.ID); r.ciID != "" {
		t.Fatalf("ambiguous address assigned %+v", r)
	}
}

// TestTrapEventWithoutTargetKeepsNullCI covers AK3 (3): an unknown sender
// is stored without CI, with the scope of the collector.
func TestTrapEventWithoutTargetKeepsNullCI(t *testing.T) {
	e := newEventEnv(t)
	ev := linkDown("192.0.2.99", time.Now().UTC())
	e.send(ev)
	if r := e.row(ev.ID); r.probeID != "" || r.ciID != "" {
		t.Fatalf("row = %+v", r)
	}
}

// TestTrapEventIdempotentOnRedelivery covers AK3: a repeated delivery
// writes no second row; an event of an unknown collector is dropped.
func TestTrapEventIdempotentOnRedelivery(t *testing.T) {
	e := newEventEnv(t)
	ev := linkDown("10.79.0.1", time.Now().UTC())
	if out := e.send(ev); out.Events != 1 {
		t.Fatalf("first delivery wrote %d", out.Events)
	}
	if out := e.send(ev); out.Events != 0 {
		t.Fatalf("redelivery wrote %d", out.Events)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM monitoring_event WHERE id = $1`, ev.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, %v", n, err)
	}
	out, err := e.store.WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{{CollectorID: "00000000-0000-7000-8000-000000000979",
		Results: []probewire.Result{{ProbeID: e.probeID, Epoch: e.epoch, Seq: 99, ObservedAt: time.Now().UTC(), Status: probewire.StatusOK}},
		Events:  []probewire.Event{linkDown("10.79.0.1", time.Now().UTC())}}})
	if err != nil || out.Events != 0 {
		t.Fatalf("unknown collector: %+v, %v", out, err)
	}
}

// TestTrapEventClientScope covers AK1 and AK7: a principal of client 1
// lists the event of its CI, one of client 2 does not; events without CI
// and without client of the collector are read by both.
func TestTrapEventClientScope(t *testing.T) {
	e := newEventEnv(t)
	own := linkDown("10.79.0.1", time.Now().UTC())
	loose := linkDown("192.0.2.98", time.Now().UTC())
	e.send(own, loose)
	repo := probe.NewPGRepository(e.f.App)
	ids := func(clientID string) []string {
		list, _, err := repo.ListEvents(e.f.ClientCtx(clientID), &probe.EventFilter{}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := range list {
			out = append(out, list[i].ID)
		}
		return out
	}
	if got := ids(e.f.Client1); !slices.Contains(got, own.ID) || !slices.Contains(got, loose.ID) {
		t.Fatalf("client 1 sees %v", got)
	}
	if got := ids(e.f.Client2); slices.Contains(got, own.ID) || !slices.Contains(got, loose.ID) {
		t.Fatalf("client 2 sees %v", got)
	}
}

// TestTrapEventRetention30Days covers AK6: PurgeEvents deletes the events
// older than 30 days, in portions, and keeps the younger ones.
func TestTrapEventRetention30Days(t *testing.T) {
	e := newEventEnv(t)
	now := time.Now().UTC()
	old := linkDown("10.79.0.1", now.Add(-probe.EventRetention-time.Hour))
	young := linkDown("10.79.0.1", now.Add(-probe.EventRetention+time.Hour))
	e.send(old, young)
	n, err := probe.PurgeEvents(context.Background(), e.f.App, e.f.OrgA, now.Add(-probe.EventRetention))
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	var left []string
	rows, err := e.f.Admin.Query(context.Background(), `SELECT id::text FROM monitoring_event WHERE organization_id = $1`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if !slices.Equal(left, []string{young.ID}) {
		t.Fatalf("left %v, want %s", left, young.ID)
	}
}

// TestListEventsMemoryAndPGParity covers AK7: PGRepository and
// MemoryRepository list the same events in the same order, filtered and
// over the keyset cursor.
func TestListEventsMemoryAndPGParity(t *testing.T) {
	e := newEventEnv(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	var events []probewire.Event
	for i := range 5 {
		ev := linkDown("10.79.0.1", base.Add(-time.Duration(i)*time.Minute))
		if i == 3 {
			ev.Kind = probewire.EventLinkUp
		}
		events = append(events, ev)
	}
	e.send(events...)
	pg := probe.NewPGRepository(e.f.App)
	ctx := e.f.OrgCtx(e.f.OrgA)
	all, _, err := pg.ListEvents(ctx, &probe.EventFilter{}, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	mem := probe.NewMemoryRepository()
	mem.AddEvents(e.f.OrgA, all...)
	for name, f := range map[string]probe.EventFilter{
		"all": {}, "kind": {Kind: probewire.EventLinkUp}, "probe": {ProbeID: e.probeID}, "ci": {CIID: e.ci},
		"range": {From: base.Add(-150 * time.Second), To: base},
	} {
		var pgIDs, memIDs []string
		for _, repo := range []probe.EventLister{pg, mem} {
			page := api.PaginationParams{Limit: 2}
			var got []string
			for range 10 {
				list, total, err := repo.ListEvents(ctx, &f, page)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				_ = total
				n := min(len(list), page.Limit)
				for i := range list[:n] {
					got = append(got, list[i].ID)
				}
				if len(list) <= page.Limit {
					break
				}
				c, cerr := api.DecodeCursor(probe.EventCursor(&list[n-1]))
				if cerr != nil {
					t.Fatal(cerr)
				}
				page.Cursor = &c
			}
			if repo == probe.EventLister(pg) {
				pgIDs = got
			} else {
				memIDs = got
			}
		}
		if !slices.Equal(pgIDs, memIDs) || (name == "all" && len(pgIDs) != 5) || (name == "kind" && len(pgIDs) != 1) ||
			(name == "range" && len(pgIDs) != 2) {
			t.Errorf("%s: pg %v, memory %v", name, pgIDs, memIDs)
		}
	}
}

// TestTrapEventBadContentDoesNotCostBatch covers AK3: events the database
// could not store (NUL in a varbind, a source with zone, varbinds whose
// JSON text exceeds the column check) are dropped alone; the result and
// the valid event of the batch are written.
func TestTrapEventBadContentDoesNotCostBatch(t *testing.T) {
	e := newEventEnv(t)
	now := time.Now().UTC()
	nul := linkDown("10.79.0.1", now)
	nul.Varbinds = map[string]string{"1.3.6.1.2.1.2.2.1.2.7": "ge\x00-0/0/7"}
	zone := linkDown("fe80::1%eth0", now)
	quotes := linkDown("10.79.0.1", now)
	// Quotes within the 16 KiB raw: escaped above the 32 KiB of the
	// column check.
	half := strings.Repeat(`"`, probewire.MaxEventVarbindBytes/2-1)
	quotes.Varbinds = map[string]string{"1": half, "2": half}
	oid := linkDown("10.79.0.1", now)
	oid.TrapOID = "1.3.6.1.6.3.1.1.5.3\x00"
	good := linkDown("10.79.0.1", now)
	out := e.send(nul, zone, quotes, oid, good)
	if out.Written != 1 || out.Events != 1 {
		t.Fatalf("outcome %+v, want the result and the valid event", out)
	}
	if r := e.row(good.ID); r.probeID != e.probeID {
		t.Fatalf("row = %+v", r)
	}
}

// TestTrapEventThroughStream covers AK2 and AK3 over the stream
// RETICORA_PROBE and the worker probe_ingest: a result batch with an
// event, delivered twice, gives one event row with the probe of the
// sender.
func TestTrapEventThroughStream(t *testing.T) {
	e := newIngestEnv(t, 1)
	t.Cleanup(func() {
		_, _ = e.f.Admin.Exec(context.Background(), `DELETE FROM monitoring_event WHERE organization_id = $1`, e.f.OrgA)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ev := linkDown("10.96.0.1", time.Now().UTC())
	data, err := probewire.Encode(&probewire.ResultBatch{CollectorID: e.collector,
		Results: e.results(time.Now().UTC().Add(-time.Minute), 1, 1), Events: []probewire.Event{ev}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := e.js.Publish(ctx, e.cfg.ResultsSubject(e.f.OrgA, e.collector), data); err != nil {
			t.Fatal(err)
		}
	}
	e.drain(ctx, 1)
	var n int
	var probeID string
	if err := e.f.Admin.QueryRow(ctx, `SELECT count(*), COALESCE(max(probe_id::text), '') FROM monitoring_event WHERE id = $1`,
		ev.ID).Scan(&n, &probeID); err != nil {
		t.Fatal(err)
	}
	if n != 1 || probeID != e.probes[0] {
		t.Fatalf("%d rows with probe %q, want one row with %s", n, probeID, e.probes[0])
	}
}
