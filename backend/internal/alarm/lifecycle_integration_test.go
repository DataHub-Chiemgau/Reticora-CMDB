package alarm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The lifecycle of an alarm on PostgreSQL (ALR-01, WP-982): concurrent
// raises of one key, transitions and outbox events in the transaction of
// the change, and the throughput of the outbox publisher for an alarm
// storm (E-226).

type outboxRow struct {
	Type     string
	EntityID string
	Sequence int64
	Data     map[string]any
}

// outboxOf reads the events of the organization after sequence after.
func outboxOf(t *testing.T, f *scopetest.Fixture, orgID string, after int64) []outboxRow {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT type, COALESCE(entity_id, ''), sequence, data
		FROM event_outbox WHERE organization_id = $1 AND sequence > $2 ORDER BY sequence`, orgID, after)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var raw []byte
		if err := rows.Scan(&r.Type, &r.EntityID, &r.Sequence, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.Data); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func lastSequence(t *testing.T, f *scopetest.Fixture, orgID string) int64 {
	t.Helper()
	var n int64
	if err := f.Admin.QueryRow(context.Background(), `SELECT COALESCE(max(sequence), 0) FROM event_outbox WHERE organization_id = $1`,
		orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConcurrentRaisesYieldOneAlarm(t *testing.T) {
	f := scopetest.Seed(t, alarmTag)
	s := alarm.NewPGService(f.App)
	ci := f.CI(t, f.OrgA, f.Client1, "concurrent-ci")
	before := lastSequence(t, f, f.OrgA)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := raiseIn("eth0", alarm.SeverityWarning)
			in.CIID = &ci
			if _, err := s.Raise(f.OrgCtx(f.OrgA), f.OrgA, in); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("raise: %v", err)
	}
	page, err := s.Store().List(f.OrgCtx(f.OrgA), f.OrgA, &alarm.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].OccurrenceCount != 20 || page.Items[0].State != alarm.StateOpen {
		t.Fatalf("alarms %+v, want one open alarm with occurrence_count 20", page.Items)
	}
	opened := 0
	for _, e := range outboxOf(t, f, f.OrgA, before) {
		if e.Type == events.AlarmOpened {
			opened++
		}
	}
	if opened != 1 {
		t.Errorf("%d alarm.opened events, want exactly one", opened)
	}
	trs, err := s.Store().Transitions(f.OrgCtx(f.OrgA), f.OrgA, page.Items[0].ID)
	if err != nil || len(trs) != 1 {
		t.Errorf("transitions %+v, %v; want one", trs, err)
	}
}

func TestTransitionRolledBackWithState(t *testing.T) {
	f := scopetest.Seed(t, alarmTag)
	s := alarm.NewPGService(f.App)
	ctx := f.OrgCtx(f.OrgA)
	a, err := s.Raise(ctx, f.OrgA, raiseIn("eth1", alarm.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	before := lastSequence(t, f, f.OrgA)
	boom := errors.New("caller fails after the ack")
	err = database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if _, ackErr := s.Acknowledge(ctx, f.OrgA, a.ID, f.User, "rolled back"); ackErr != nil {
			return ackErr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("transaction: %v, want the caller's error", err)
	}
	got, err := s.Store().Get(ctx, f.OrgA, a.ID)
	if err != nil || got.State != alarm.StateOpen || got.AcknowledgedAt != nil {
		t.Errorf("alarm after the rollback: %+v, %v; want open, not acknowledged", got, err)
	}
	trs, _ := s.Store().Transitions(ctx, f.OrgA, a.ID)
	if len(trs) != 1 {
		t.Errorf("%d transitions after the rollback, want 1 (the ack's went with it)", len(trs))
	}
	if evs := outboxOf(t, f, f.OrgA, before); len(evs) != 0 {
		t.Errorf("events after the rollback: %+v, want none", evs)
	}
	// An invalid transition writes nothing either.
	if _, err := s.Unacknowledge(ctx, f.OrgA, a.ID, f.User, ""); !errors.Is(err, alarm.ErrInvalidTransition) {
		t.Errorf("un-ack of an open alarm: %v, want ErrInvalidTransition", err)
	}
	if trs, _ := s.Store().Transitions(ctx, f.OrgA, a.ID); len(trs) != 1 {
		t.Errorf("%d transitions after the refused un-ack, want 1", len(trs))
	}
	// A comment longer than the database allows is refused before any
	// write, like on the MemoryStore (ErrInvalidTransitionInput, no raw
	// check violation).
	if _, err := s.Acknowledge(ctx, f.OrgA, a.ID, f.User, strings.Repeat("x", alarm.CommentMaxLen+1)); !errors.Is(err, alarm.ErrInvalidTransitionInput) {
		t.Errorf("ack with a comment of 1001 characters: %v, want ErrInvalidTransitionInput", err)
	}
	if _, err := s.Store().AppendTransition(ctx, f.OrgA, &alarm.Transition{AlarmID: a.ID, ToState: alarm.StateOpen,
		Reason: strings.Repeat("r", alarm.ReasonMaxLen+1)}); !errors.Is(err, alarm.ErrInvalidTransitionInput) {
		t.Errorf("store append with a reason of 201 characters: %v, want ErrInvalidTransitionInput", err)
	}
	if got, _ := s.Store().Get(ctx, f.OrgA, a.ID); got.State != alarm.StateOpen {
		t.Errorf("state after the refused ack: %s, want open", got.State)
	}
}

func TestLifecycleEmitsOutboxEvents(t *testing.T) {
	f := scopetest.Seed(t, alarmTag)
	s := alarm.NewPGService(f.App)
	ctx := f.OrgCtx(f.OrgA)
	ci := f.CI(t, f.OrgA, f.Client1, "events-ci")
	site := (&scopeEnv{t: t, f: f}).placeAtNewSite(ci, f.Client1, "wp982-events-site")
	before := lastSequence(t, f, f.OrgA)
	in := raiseIn("eth2", alarm.SeverityWarning)
	in.CIID = &ci
	if _, err := s.Pend(ctx, f.OrgA, in); err != nil {
		t.Fatal(err)
	}
	a, err := s.Raise(ctx, f.OrgA, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Acknowledge(ctx, f.OrgA, a.ID, f.User, "looking"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Unacknowledge(ctx, f.OrgA, a.ID, f.User, "not mine"); err != nil {
		t.Fatal(err)
	}
	in.Severity = alarm.SeverityCritical
	if _, err = s.Raise(ctx, f.OrgA, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Clear(ctx, f.OrgA, a.DedupKey, time.Time{}, "recovered"); err != nil {
		t.Fatal(err)
	}
	evs := outboxOf(t, f, f.OrgA, before)
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
		// AK4: alarm_id, ci_id, client_id, site_id, severity and dedup_key.
		if e.EntityID != a.ID || e.Data["id"] != a.ID || e.Data["alarm_id"] != a.ID || e.Data["ci_id"] != ci ||
			e.Data["client_id"] != f.Client1 || e.Data["site_id"] != site || e.Data["dedup_key"] != a.DedupKey ||
			e.Data["severity"] == nil {
			t.Errorf("%s: entity %s, data %v", e.Type, e.EntityID, e.Data)
		}
	}
	want := []string{events.AlarmOpened, events.AlarmAcknowledged, events.AlarmSeverityChanged, events.AlarmResolved}
	if !slices.Equal(types, want) {
		t.Errorf("events %v, want %v (pending and un-ack without event)", types, want)
	}
	trs, err := s.Store().Transitions(ctx, f.OrgA, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, tr := range trs {
		reasons = append(reasons, tr.Reason)
	}
	wantReasons := []string{alarm.ReasonPending, alarm.ReasonRaised, alarm.ReasonAcknowledged, alarm.ReasonUnacknowledged,
		alarm.ReasonSeverityChanged, "recovered"}
	if !slices.Equal(reasons, wantReasons) {
		t.Errorf("transition reasons %v, want %v", reasons, wantReasons)
	}
	if trs[2].ActorID == nil || *trs[2].ActorID != f.User || trs[2].Comment == nil || *trs[2].Comment != "looking" {
		t.Errorf("ack transition %+v, want actor and comment", trs[2])
	}
}

func TestRaiseAfterResolvedOpensNewAlarm(t *testing.T) {
	f := scopetest.Seed(t, alarmTag)
	s := alarm.NewPGService(f.App)
	ctx := f.OrgCtx(f.OrgA)
	first, err := s.Raise(ctx, f.OrgA, raiseIn("eth3", alarm.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Clear(ctx, f.OrgA, first.DedupKey, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	before := lastSequence(t, f, f.OrgA)
	second, err := s.Raise(ctx, f.OrgA, raiseIn("eth3", alarm.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.State != alarm.StateOpen || second.OccurrenceCount != 1 {
		t.Errorf("raise after resolve: %+v; want a new open alarm", second)
	}
	evs := outboxOf(t, f, f.OrgA, before)
	if len(evs) != 1 || evs[0].Type != events.AlarmOpened || evs[0].EntityID != second.ID {
		t.Errorf("events %+v, want one alarm.opened of the new alarm", evs)
	}
}

// burstConsumer records the delivered sequences of one organization.
type burstConsumer struct {
	org  string
	seqs []int64
}

func (c *burstConsumer) Consume(_ context.Context, env *events.Envelope) error {
	if env.OrgID == c.org {
		c.seqs = append(c.seqs, env.Sequence)
	}
	return nil
}

// TestAlarmBurstPublishedAtLeast200PerSecond: 1,000 alarm.opened of one
// organization reach the consumers at ≥ 200 events per second (E-226): the
// publisher ticks once a second (events.DefaultInterval), so at most five
// cycles may deliver them, in sequence order.
func TestAlarmBurstPublishedAtLeast200PerSecond(t *testing.T) {
	f := scopetest.Seed(t, alarmTag)
	s := alarm.NewPGService(f.App)
	ctx := f.OrgCtx(f.OrgA)
	for b := range 10 {
		err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
			for i := range 100 {
				if _, err := s.Raise(ctx, f.OrgA, raiseIn(fmt.Sprintf("burst-%d-%d", b, i), alarm.SeverityWarning)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	consumer := &burstConsumer{org: f.OrgA}
	p := events.NewPublisher(f.App, nil, consumer)
	cycles := 0
	start := time.Now()
	for len(consumer.seqs) < 1000 && cycles < 20 {
		n, err := p.PublishOrg(context.Background(), f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		cycles++
		if cycles == 1 && n < 200 {
			t.Errorf("first cycle published %d events, want ≥ 200", n)
		}
	}
	elapsed := time.Since(start)
	if len(consumer.seqs) != 1000 {
		t.Fatalf("%d events published, want 1000", len(consumer.seqs))
	}
	// One cycle per Interval: the rate is 1000 events over max(cycles ×
	// Interval, elapsed).
	window := max(time.Duration(cycles)*p.Interval, elapsed)
	if rate := 1000 / window.Seconds(); rate < 200 {
		t.Errorf("%.0f events/s (%d cycles, %v), want ≥ 200", rate, cycles, elapsed)
	}
	if !slices.IsSorted(consumer.seqs) {
		t.Error("events not delivered in sequence order")
	}
}

// TestPendingOpensWithCurrentValueOnPostgres: on PostgreSQL too an alarm
// that was pending opens with the value of the opening observation (the
// upsert keeps the details of the first one, WP-987 AK4).
func TestPendingOpensWithCurrentValueOnPostgres(t *testing.T) {
	f := scopetest.Seed(t, alarmTag)
	s := alarm.NewPGService(f.App)
	ctx := f.OrgCtx(f.OrgA)
	before := lastSequence(t, f, f.OrgA)
	in := raiseIn("value-of-open", alarm.SeverityWarning)
	in.Details = map[string]any{"value": 82.0}
	if _, err := s.Pend(ctx, f.OrgA, in); err != nil {
		t.Fatal(err)
	}
	in.Severity, in.Details = alarm.SeverityCritical, map[string]any{"value": 97.5}
	a, err := s.Raise(ctx, f.OrgA, in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := a.Details["value"].(float64); v != 97.5 || a.Severity != alarm.SeverityCritical {
		t.Errorf("opened alarm %s with details %v, want critical with value 97.5", a.Severity, a.Details)
	}
	evs := outboxOf(t, f, f.OrgA, before)
	if len(evs) != 1 || evs[0].Type != events.AlarmOpened || evs[0].Data["value"] != 97.5 {
		t.Errorf("events %+v, want one alarm.opened with value 97.5", evs)
	}
}
