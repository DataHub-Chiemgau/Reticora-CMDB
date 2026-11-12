package alarm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Alarm instances and their state log (ALR-01, WP-981, migration
// 000313_alarm): the store cases run against PostgreSQL (the restricted
// runtime login, scopetest.AppDSN) and the MemoryStore alike; the
// append-only and retention cases need the database triggers.

// storeEnv is one store under test with the context of an org-wide
// principal of organization orgID.
type storeEnv struct {
	name  string
	store alarm.Store
	ctx   context.Context
	orgID string
	// ci returns a CI the alarms may reference (empty: no CI).
	ci func(name string) string
}

const alarmTag = "e8"

func pgEnv(t *testing.T) (*storeEnv, *scopetest.Fixture) {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	return &storeEnv{
		name: "postgres", store: alarm.NewPGStore(f.App), ctx: f.OrgCtx(f.OrgA), orgID: f.OrgA,
		ci: func(name string) string { return f.CI(t, f.OrgA, f.Client1, name) },
	}, f
}

func memoryEnv() *storeEnv {
	n := 0
	return &storeEnv{
		name: "memory", store: alarm.NewMemoryStore(), ctx: context.Background(), orgID: "00000000-0000-4000-8000-0000000000a1",
		ci: func(string) string {
			n++
			return "00000000-0000-4000-8000-00000000c0" + string(rune('0'+n%10)) + string(rune('0'+n/10%10))
		},
	}
}

// eachStore runs fn against the MemoryStore and, with TEST_DATABASE_URL,
// against PostgreSQL.
func eachStore(t *testing.T, fn func(t *testing.T, e *storeEnv)) {
	t.Run("memory", func(t *testing.T) { fn(t, memoryEnv()) })
	t.Run("postgres", func(t *testing.T) {
		e, _ := pgEnv(t)
		fn(t, e)
	})
}

const ruleID = "11111111-1111-4111-8111-111111111111"

func raise(t *testing.T, e *storeEnv, series string, ci *string, at time.Time) (*alarm.Alarm, bool) {
	t.Helper()
	a, created, err := e.store.UpsertOpen(e.ctx, e.orgID, &alarm.UpsertInput{
		SourceKind: alarm.SourceRule, SourceID: ruleID, SeriesKey: series, Labels: map[string]string{"if": series},
		CIID: ci, Severity: alarm.SeverityWarning, State: alarm.StateOpen, Summary: "cpu high", ObservedAt: at,
	})
	if err != nil {
		t.Fatalf("%s: upsert %s: %v", e.name, series, err)
	}
	return a, created
}

func resolve(t *testing.T, e *storeEnv, a *alarm.Alarm, at time.Time) *alarm.Alarm {
	t.Helper()
	out, err := e.store.UpdateState(e.ctx, e.orgID, a.ID, &alarm.StateUpdate{
		State: alarm.StateResolved, Severity: a.Severity, OpenedAt: a.OpenedAt, ResolvedAt: &at,
	})
	if err != nil {
		t.Fatalf("%s: resolve: %v", e.name, err)
	}
	return out
}

func TestAlarmUpsertSameDedupKeyIncrementsCount(t *testing.T) {
	eachStore(t, func(t *testing.T, e *storeEnv) {
		ci := e.ci("upsert-ci")
		t0 := time.Now().UTC().Truncate(time.Second)
		first, created := raise(t, e, "eth0", &ci, t0)
		if !created || first.OccurrenceCount != 1 || first.State != alarm.StateOpen || first.OpenedAt == nil {
			t.Fatalf("first raise: created %v, alarm %+v", created, first)
		}
		if want := alarm.DedupKey(alarm.SourceRule, ruleID, "eth0"); first.DedupKey != want {
			t.Errorf("dedup_key %q, want %q", first.DedupKey, want)
		}
		second, created := raise(t, e, "eth0", &ci, t0.Add(time.Minute))
		if created {
			t.Error("second raise of the same key created a row")
		}
		if second.ID != first.ID || second.OccurrenceCount != 2 || !second.LastSeenAt.Equal(t0.Add(time.Minute)) ||
			!second.FirstSeenAt.Equal(t0) {
			t.Errorf("second raise: %+v, want the first alarm with occurrence_count 2 and last_seen_at +1m", second)
		}
		page, err := e.store.List(e.ctx, e.orgID, &alarm.ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Errorf("%d alarms, want one row", len(page.Items))
		}
	})
}

func TestAlarmUpsertAfterResolvedCreatesNewRow(t *testing.T) {
	eachStore(t, func(t *testing.T, e *storeEnv) {
		ci := e.ci("resolved-ci")
		t0 := time.Now().UTC().Truncate(time.Second)
		first, _ := raise(t, e, "eth1", &ci, t0)
		resolved := resolve(t, e, first, t0.Add(time.Minute))
		if resolved.State != alarm.StateResolved || resolved.ResolvedAt == nil {
			t.Fatalf("resolve: %+v", resolved)
		}
		again, created := raise(t, e, "eth1", &ci, t0.Add(2*time.Minute))
		if !created || again.ID == first.ID || again.OccurrenceCount != 1 {
			t.Errorf("raise after resolve: created %v, %+v; want a new row", created, again)
		}
		old, err := e.store.Get(e.ctx, e.orgID, first.ID)
		if err != nil || old.State != alarm.StateResolved {
			t.Errorf("resolved alarm: %+v, %v", old, err)
		}
		page, err := e.store.List(e.ctx, e.orgID, &alarm.ListFilter{})
		if err != nil || len(page.Items) != 2 {
			t.Errorf("list: %d alarms, %v; want 2", len(page.Items), err)
		}
	})
}

func TestAlarmListFiltersAndCursor(t *testing.T) {
	eachStore(t, func(t *testing.T, e *storeEnv) {
		ci := e.ci("list-ci")
		t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		var ids []string
		for i, series := range []string{"a", "b", "c", "d", "e"} {
			a, _ := raise(t, e, series, &ci, t0.Add(time.Duration(i)*time.Minute))
			ids = append(ids, a.ID)
		}
		// One collector alarm without CI, critical, suppressed.
		other, _, err := e.store.UpsertOpen(e.ctx, e.orgID, &alarm.UpsertInput{
			SourceKind: alarm.SourceCollector, SourceID: ruleID, Severity: alarm.SeverityCritical, State: alarm.StatePending,
			ObservedAt: t0.Add(10 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		flapping := alarm.SuppressedFlapping
		if _, err := e.store.UpdateState(e.ctx, e.orgID, other.ID, &alarm.StateUpdate{
			State: alarm.StatePending, Severity: alarm.SeverityCritical, SuppressedReason: &flapping,
		}); err != nil {
			t.Fatal(err)
		}
		resolve(t, e, mustGet(t, e, ids[0]), t0.Add(30*time.Minute))

		count := func(f *alarm.ListFilter) int {
			t.Helper()
			page, err := e.store.List(e.ctx, e.orgID, f)
			if err != nil {
				t.Fatal(err)
			}
			return len(page.Items)
		}
		yes, no := true, false
		before, after := t0.Add(31*time.Minute), t0.Add(2*time.Minute)
		for name, tc := range map[string]struct {
			f    *alarm.ListFilter
			want int
		}{
			"all":           {&alarm.ListFilter{}, 6},
			"state open":    {&alarm.ListFilter{States: []string{alarm.StateOpen}}, 4},
			"severity":      {&alarm.ListFilter{Severities: []string{alarm.SeverityCritical}}, 1},
			"ci":            {&alarm.ListFilter{CIID: ci}, 5},
			"source kind":   {&alarm.ListFilter{SourceKinds: []string{alarm.SourceCollector}}, 1},
			"suppressed":    {&alarm.ListFilter{Suppressed: &yes}, 1},
			"unsuppressed":  {&alarm.ListFilter{Suppressed: &no}, 5},
			"from":          {&alarm.ListFilter{From: &before}, 5},
			"to":            {&alarm.ListFilter{To: &after}, 2},
			"incident none": {&alarm.ListFilter{IncidentID: "22222222-2222-4222-8222-222222222222"}, 0},
		} {
			if got := count(tc.f); got != tc.want {
				t.Errorf("%s: %d alarms, want %d", name, got, tc.want)
			}
		}

		// Pages of two, newest first, without gaps or repeats.
		var seen []string
		f := &alarm.ListFilter{Limit: 2}
		for range 5 {
			page, err := e.store.List(e.ctx, e.orgID, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range page.Items {
				seen = append(seen, a.ID)
			}
			if page.Next == nil {
				break
			}
			f.After = page.Next
		}
		if len(seen) != 6 || seen[0] != other.ID || seen[5] != ids[0] {
			t.Errorf("paged ids %v, want 6 newest first (collector alarm first, %s last)", seen, ids[0])
		}
		unique := map[string]bool{}
		for _, id := range seen {
			unique[id] = true
		}
		if len(unique) != 6 {
			t.Errorf("paging repeated alarms: %v", seen)
		}
	})
}

func mustGet(t *testing.T, e *storeEnv, id string) *alarm.Alarm {
	t.Helper()
	a, err := e.store.Get(e.ctx, e.orgID, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// transitionOf appends an open → acknowledged transition to a fresh alarm.
func transitionOf(t *testing.T, e *storeEnv, series string) (*alarm.Alarm, *alarm.Transition) {
	t.Helper()
	ci := e.ci(series + "-ci")
	a, _ := raise(t, e, series, &ci, time.Time{})
	from := alarm.StateOpen
	tr, err := e.store.AppendTransition(e.ctx, e.orgID, &alarm.Transition{
		AlarmID: a.ID, FromState: &from, ToState: alarm.StateAcknowledged, Reason: "ack",
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, tr
}

func TestAlarmTransitionRejectsUpdate(t *testing.T) {
	e, f := pgEnv(t)
	_, tr := transitionOf(t, e, "upd")
	err := database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE alarm_transition SET reason = 'rewritten' WHERE id = $1`, tr.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE of a transition: %v, want the append-only refusal", err)
	}
	// Not even the owner (maintenance pool, no RLS) rewrites the log.
	if _, ownerErr := f.Admin.Exec(context.Background(), `UPDATE alarm_transition SET reason = 'rewritten' WHERE id = $1`, tr.ID); ownerErr == nil {
		t.Error("the owner rewrote a transition")
	}
	log, err := e.store.Transitions(e.ctx, e.orgID, tr.AlarmID)
	if err != nil || len(log) != 1 || log[0].Reason != "ack" || log[0].ActorID != nil {
		t.Errorf("log after the refused update: %+v, %v", log, err)
	}
}

func TestAlarmTransitionRejectsDeleteWithoutRetention(t *testing.T) {
	e, f := pgEnv(t)
	a, tr := transitionOf(t, e, "del")
	del := func(sql string, retention bool, args ...any) error {
		return database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if retention {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.alarm_retention', 'on', true)`); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	if err := del(`DELETE FROM alarm_transition WHERE id = $1`, false, tr.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE without retention: %v, want the append-only refusal", err)
	}
	// Inside the retention only rows older than 400 days go.
	if err := del(`DELETE FROM alarm_transition WHERE id = $1`, true, tr.ID); err == nil {
		t.Error("the retention deleted a transition younger than 400 days")
	}
	// The alarm itself is not deleted directly, which would drop its log.
	if err := del(`DELETE FROM alarm WHERE id = $1`, false, a.ID); err == nil {
		t.Error("an alarm was deleted outside the retention")
	}
	if log, err := e.store.Transitions(e.ctx, e.orgID, a.ID); err != nil || len(log) != 1 {
		t.Errorf("log after the refused deletes: %d entries, %v", len(log), err)
	}
	// Its CI's hard delete takes the alarm and its log along (cascade).
	if _, err := f.Admin.Exec(context.Background(), `DELETE FROM ci WHERE id = $1`, *a.CIID); err != nil {
		t.Fatalf("hard delete of the CI: %v", err)
	}
	if _, err := e.store.Get(e.ctx, e.orgID, a.ID); err != alarm.ErrNotFound {
		t.Errorf("alarm after its CI's delete: %v, want ErrNotFound", err)
	}
}

func TestAlarmRetentionRemovesResolvedOlderThan400Days(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-alarm.Retention)
	t.Run("postgres", func(t *testing.T) {
		e, f := pgEnv(t)
		expired, _ := transitionOf(t, e, "expired")
		boundary, _ := transitionOf(t, e, "boundary")
		open, _ := transitionOf(t, e, "open-old")
		resolve(t, e, expired, now)
		resolve(t, e, boundary, now)
		ctx := context.Background()
		// Back-date through the owner: resolved 400 days and one second ago,
		// exactly 400 days ago, and an open alarm first seen 500 days ago.
		for id, at := range map[string]time.Time{expired.ID: cutoff.Add(-time.Second), boundary.ID: cutoff} {
			if _, err := f.Admin.Exec(ctx, `UPDATE alarm SET resolved_at = $2::timestamptz, first_seen_at = $2::timestamptz - interval '1 day' WHERE id = $1`, id, at); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.Admin.Exec(ctx, `UPDATE alarm SET first_seen_at = $2 WHERE id = $1`, open.ID, now.AddDate(0, 0, -500)); err != nil {
			t.Fatal(err)
		}
		// The log of the expired alarm is as old as the alarm (the append-only
		// guard is skipped under replica): the retention removes it by the
		// 400-day rule of reject_alarm_transition_mutation.
		backdate, berr := f.Admin.Begin(ctx)
		if berr != nil {
			t.Fatal(berr)
		}
		defer func() { _ = backdate.Rollback(ctx) }()
		if _, err := backdate.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := backdate.Exec(ctx, `UPDATE alarm_transition SET at = $2 WHERE alarm_id = $1`, expired.ID, cutoff.Add(-2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := backdate.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		run := alarm.NewRetention(f.App).WithClock(func() time.Time { return now })
		if sched := run.Schedule(); sched.Kind != alarm.RetentionKind || sched.MaxCatchUp != 1 {
			t.Errorf("schedule %+v", sched)
		}
		deleted, err := run.RunOrganization(ctx, e.orgID, cutoff)
		if err != nil || deleted != 1 {
			t.Fatalf("retention: deleted %d, %v; want 1", deleted, err)
		}
		if _, err := e.store.Get(e.ctx, e.orgID, expired.ID); err != alarm.ErrNotFound {
			t.Errorf("expired alarm: %v, want ErrNotFound", err)
		}
		if log, _ := e.store.Transitions(e.ctx, e.orgID, expired.ID); len(log) != 0 {
			t.Errorf("transitions of the expired alarm remain: %d", len(log))
		}
		for _, id := range []string{boundary.ID, open.ID} {
			if _, err := e.store.Get(e.ctx, e.orgID, id); err != nil {
				t.Errorf("alarm %s removed: %v", id, err)
			}
			if log, _ := e.store.Transitions(e.ctx, e.orgID, id); len(log) != 1 {
				t.Errorf("transitions of %s: %d, want 1", id, len(log))
			}
		}
		var audited int
		if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = $2`,
			e.orgID, alarm.RetentionKind).Scan(&audited); err != nil || audited != 1 {
			t.Errorf("audit entries of the run: %d, %v; want 1", audited, err)
		}
	})
	t.Run("memory", func(t *testing.T) {
		e := memoryEnv()
		m := e.store.(*alarm.MemoryStore)
		expired, _ := transitionOf(t, e, "expired")
		boundary, _ := transitionOf(t, e, "boundary")
		open, _ := transitionOf(t, e, "open-old")
		resolve(t, e, expired, cutoff.Add(-time.Second))
		resolve(t, e, boundary, cutoff)
		if n := m.PurgeResolved(e.orgID, cutoff, alarm.RetentionBatch); n != 1 {
			t.Fatalf("purged %d, want 1", n)
		}
		if _, err := m.Get(e.ctx, e.orgID, expired.ID); err != alarm.ErrNotFound {
			t.Errorf("expired alarm: %v", err)
		}
		for _, id := range []string{boundary.ID, open.ID} {
			if _, err := m.Get(e.ctx, e.orgID, id); err != nil {
				t.Errorf("alarm %s removed: %v", id, err)
			}
		}
	})
}
