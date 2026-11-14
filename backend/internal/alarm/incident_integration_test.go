package alarm_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Incidents in PostgreSQL (ALR-03, ALR-09, WP-1027, migration 000326):
// parity with the MemoryStore, client and organization scope, the
// append-only log, the truncate guard and the retention of 400 days.

func pgIncidentEnv(t *testing.T) (*incidentEnv, *scopetest.Fixture) {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	return &incidentEnv{store: alarm.NewPGStore(f.App), ctx: f.OrgCtx(f.OrgA), orgID: f.OrgA,
		ci: func(name string) string { return f.CI(t, f.OrgA, f.Client1, name) }}, f
}

// incidentSummary is what a sequence leaves behind, without ids and
// times: per incident its state, cause, manual root, the position of its
// root among the alarms, its alarms with their suppression, and its log.
type incidentSummary struct {
	State      string
	Cause      string
	Confidence int
	Manual     bool
	Root       int
	Alarms     []string
	Reasons    []string
}

// paritySequence runs the same changes on a store and summarizes them.
func paritySequence(t *testing.T, e *incidentEnv) []incidentSummary {
	t.Helper()
	a, b := openAlarms(t, e, "par-a", 3), openAlarms(t, e, "par-b", 3)
	all := append(slices.Clone(a), b...)
	first, second := newIncident(t, e, a), newIncident(t, e, b)
	if _, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{ID: first.ID, CauseClass: alarm.CauseCollectorUnreachable,
		Confidence: 40, Reasons: []string{"collector silent"}, AffectedCounts: map[string]int{"cis": 3}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SetRoot(e.ctx, e.orgID, first.ID, a[2], true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SetRoot(e.ctx, e.orgID, first.ID, a[1], false); !errors.Is(err, alarm.ErrManualRoot) {
		t.Errorf("automatic root on a manual one: %v", err)
	}
	if _, err := e.store.MergeIncidents(e.ctx, e.orgID, first.ID, second.ID, b[0]); err != nil {
		t.Fatal(err)
	}
	actor := "00000000-0000-4000-8000-0000000000f1"
	if _, err := e.store.TransitionIncident(e.ctx, e.orgID, first.ID, alarm.IncidentAcknowledged, &actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.TransitionIncident(e.ctx, e.orgID, first.ID, alarm.IncidentOpen, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.TransitionIncident(e.ctx, e.orgID, second.ID, alarm.IncidentAcknowledged, &actor); !errors.Is(err, alarm.ErrInvalidTransition) {
		t.Errorf("ack of a merged incident: %v", err)
	}
	var out []incidentSummary
	for _, id := range []string{first.ID, second.ID} {
		inc, err := e.store.GetIncident(e.ctx, e.orgID, id)
		if err != nil {
			t.Fatal(err)
		}
		s := incidentSummary{State: inc.State, Cause: inc.CauseClass, Confidence: inc.Confidence, Manual: inc.ManualRoot, Root: -1,
			Reasons: reasonsOf(t, e, id)}
		if inc.RootAlarmID != nil {
			s.Root = slices.Index(all, *inc.RootAlarmID)
		}
		for _, al := range alarmsOf(t, e, id) {
			s.Alarms = append(s.Alarms, strings.Repeat("s", btoi(al.SuppressedReason != nil))+":"+al.SeriesKey)
		}
		slices.Sort(s.Alarms)
		out = append(out, s)
	}
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestIncidentStoreParity(t *testing.T) {
	pg, _ := pgIncidentEnv(t)
	mem := memIncidentEnv()
	got, want := paritySequence(t, pg), paritySequence(t, mem)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("postgres %+v\nmemory   %+v", got, want)
	}
	// The cases of incident_test.go hold on PostgreSQL as well.
	t.Run("merge", func(t *testing.T) {
		e, _ := pgIncidentEnv(t)
		checkMergeMovesAlarms(t, e)
	})
	t.Run("manual root", func(t *testing.T) {
		e, _ := pgIncidentEnv(t)
		checkManualRootSticky(t, e)
	})
	t.Run("alarm of another incident", func(t *testing.T) {
		e, _ := pgIncidentEnv(t)
		checkAlarmStaysInItsIncident(t, e)
	})
}

func TestIncidentScopeIsolation(t *testing.T) {
	e, f := pgIncidentEnv(t)
	ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "inc-scope-1"), f.CI(t, f.OrgA, f.Client2, "inc-scope-2")
	inc1, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{RootCIID: &ci1})
	if err != nil {
		t.Fatal(err)
	}
	inc2, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{RootCIID: &ci2})
	if err != nil {
		t.Fatal(err)
	}
	if inc1.ClientID == nil || *inc1.ClientID != f.Client1 || inc2.ClientID == nil || *inc2.ClientID != f.Client2 {
		t.Fatalf("client of the incidents from their root CI: %v, %v", inc1.ClientID, inc2.ClientID)
	}
	scoped := f.ClientCtx(f.Client1)
	page, err := e.store.ListIncidents(scoped, f.OrgA, &alarm.IncidentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != inc1.ID {
		t.Errorf("client 1 sees %+v, want only its incident", page.Items)
	}
	if _, err = e.store.GetIncident(scoped, f.OrgA, inc2.ID); !errors.Is(err, alarm.ErrIncidentNotFound) {
		t.Errorf("client 1 reads the incident of client 2: %v", err)
	}
	if trs, _ := e.store.IncidentTransitions(scoped, f.OrgA, inc2.ID); len(trs) != 0 {
		t.Errorf("client 1 reads %d transitions of client 2", len(trs))
	}
	if _, err = e.store.TransitionIncident(scoped, f.OrgA, inc2.ID, alarm.IncidentResolved, nil); !errors.Is(err, alarm.ErrIncidentNotFound) {
		t.Errorf("client 1 resolves the incident of client 2: %v", err)
	}
	other := f.OrgCtx(f.OrgB)
	if page, err = e.store.ListIncidents(other, f.OrgB, &alarm.IncidentFilter{}); err != nil || len(page.Items) != 0 {
		t.Errorf("organization B sees %d incidents, %v", len(page.Items), err)
	}
	if _, err = e.store.GetIncident(other, f.OrgB, inc1.ID); !errors.Is(err, alarm.ErrIncidentNotFound) {
		t.Errorf("organization B reads an incident of A: %v", err)
	}
	// Filters and cursor.
	page, err = e.store.ListIncidents(e.ctx, f.OrgA, &alarm.IncidentFilter{ClientID: f.Client2})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != inc2.ID {
		t.Errorf("client filter: %+v, %v", page.Items, err)
	}
	page, err = e.store.ListIncidents(e.ctx, f.OrgA, &alarm.IncidentFilter{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Next == nil {
		t.Fatalf("first page: %+v, %v", page, err)
	}
	next, err := e.store.ListIncidents(e.ctx, f.OrgA, &alarm.IncidentFilter{Limit: 1, After: page.Next})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID || next.Next != nil {
		t.Errorf("second page: %+v, %v", next, err)
	}
	// A moved root CI carries its incident and the log along.
	if _, err = f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, ci1, f.Client2); err != nil {
		t.Fatal(err)
	}
	moved, err := e.store.GetIncident(e.ctx, f.OrgA, inc1.ID)
	if err != nil || moved.ClientID == nil || *moved.ClientID != f.Client2 {
		t.Errorf("incident after its root CI moved: %+v, %v", moved, err)
	}
	if trs, _ := e.store.IncidentTransitions(e.ctx, f.OrgA, inc1.ID); len(trs) != 1 || trs[0].ClientID == nil || *trs[0].ClientID != f.Client2 {
		t.Errorf("transitions after the move: %+v", trs)
	}
	// A merge in a client scope is refused: the symptom of client 2 at
	// absorb could not be moved and would stay at a resolved incident.
	ci3 := f.CI(t, f.OrgA, f.Client1, "inc-scope-3")
	keep, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{RootCIID: &ci3})
	if err != nil {
		t.Fatal(err)
	}
	absorb, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{RootCIID: &ci3})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := e.store.UpsertOpen(e.ctx, e.orgID, &alarm.UpsertInput{SourceKind: alarm.SourceCheck, SourceID: ruleID,
		SeriesKey: "inc-scope-foreign", CIID: &ci2, Severity: alarm.SeverityCritical, State: alarm.StateOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.store.AttachAlarms(e.ctx, e.orgID, absorb.ID, []string{foreign.ID}, alarm.RoleSymptom); err != nil {
		t.Fatal(err)
	}
	if _, err = e.store.MergeIncidents(scoped, f.OrgA, keep.ID, absorb.ID, ""); !errors.Is(err, alarm.ErrMergeScopeRestricted) {
		t.Errorf("merge in the scope of client 1: %v, want ErrMergeScopeRestricted", err)
	}
	if got, _ := e.store.GetIncident(e.ctx, e.orgID, absorb.ID); got == nil || got.State != alarm.IncidentOpen {
		t.Errorf("absorb after the refused merge: %+v, want open", got)
	}
	if n := len(alarmsOf(t, e, absorb.ID)); n != 1 {
		t.Errorf("%d alarms at absorb after the refused merge, want 1", n)
	}
	if _, err = e.store.MergeIncidents(e.ctx, e.orgID, keep.ID, absorb.ID, ""); err != nil {
		t.Errorf("merge in the organization-wide scope: %v", err)
	}
	if n := len(alarmsOf(t, e, keep.ID)); n != 1 {
		t.Errorf("%d alarms at keep after the merge, want the symptom of client 2", n)
	}
}

func TestIncidentTransitionUpdateRejected(t *testing.T) {
	e, f := pgIncidentEnv(t)
	inc, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `UPDATE incident_transition SET reason = 'rewritten' WHERE incident_id = $1`, inc.ID)
		return execErr
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE of an incident transition: %v, want the append-only refusal", err)
	}
	if _, err = f.Admin.Exec(context.Background(), `UPDATE incident_transition SET reason = 'rewritten' WHERE incident_id = $1`, inc.ID); err == nil {
		t.Error("the owner rewrote an incident transition")
	}
	err = database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `DELETE FROM incident_transition WHERE incident_id = $1`, inc.ID)
		return execErr
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE of an incident transition: %v, want the append-only refusal", err)
	}
	err = database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `DELETE FROM incident WHERE id = $1`, inc.ID)
		return execErr
	})
	if err == nil {
		t.Error("an incident was deleted outside the retention")
	}
	if r := reasonsOf(t, e, inc.ID); len(r) != 1 || r[0] != alarm.IncidentReasonOpened {
		t.Errorf("log after the refused changes: %v", r)
	}
	// The cascade of a deleted client takes the incident and its log.
	client2 := f.Client2
	gone, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{ClientID: &client2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(context.Background(), `DELETE FROM client WHERE id = $1`, f.Client2); err != nil {
		t.Fatalf("delete client: %v", err)
	}
	if _, err = e.store.GetIncident(e.ctx, e.orgID, gone.ID); !errors.Is(err, alarm.ErrIncidentNotFound) {
		t.Errorf("incident after its client's delete: %v", err)
	}
}

func TestIncidentTruncateGuard(t *testing.T) {
	e, f := pgIncidentEnv(t)
	inc, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, table := range []string{"incident", "incident_transition"} {
		var guarded bool
		if err := f.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = $1::regclass
			AND tgname = 'reticora_truncate_guard' AND tgenabled <> 'D')`, table).Scan(&guarded); err != nil || !guarded {
			t.Errorf("%s: truncate guard %v, %v", table, guarded, err)
		}
		err := database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `TRUNCATE `+table+` CASCADE`)
			return err
		})
		if err == nil {
			t.Errorf("the application truncated %s", table)
		}
	}
	if _, err := e.store.GetIncident(e.ctx, e.orgID, inc.ID); err != nil {
		t.Errorf("incident after the refused truncates: %v", err)
	}
}

func TestIncidentRetention400Days(t *testing.T) {
	e, f := pgIncidentEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-alarm.Retention)
	open := func() *alarm.Incident {
		inc, _, err := e.store.UpsertIncident(e.ctx, e.orgID, &alarm.IncidentInput{})
		if err != nil {
			t.Fatal(err)
		}
		return inc
	}
	expired, recent, stillOpen := open(), open(), open()
	ids := openAlarms(t, e, "ret", 1)
	if _, err := e.store.AttachAlarms(e.ctx, e.orgID, expired.ID, ids, alarm.RoleRoot); err != nil {
		t.Fatal(err)
	}
	for _, inc := range []*alarm.Incident{expired, recent} {
		if _, err := e.store.TransitionIncident(e.ctx, e.orgID, inc.ID, alarm.IncidentResolved, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Back-date through the owner with the guards lifted: resolved 401 and
	// 399 days ago, an open incident opened 401 days ago, the logs as old.
	backdate, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backdate.Rollback(ctx) }()
	if _, err = backdate.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	for id, at := range map[string]time.Time{expired.ID: now.Add(-401 * day), recent.ID: now.Add(-399 * day), stillOpen.ID: now.Add(-401 * day)} {
		if _, err = backdate.Exec(ctx, `UPDATE incident SET opened_at = $2::timestamptz - interval '1 hour',
			resolved_at = CASE WHEN state = 'resolved' THEN $2::timestamptz END WHERE id = $1`, id, at); err != nil {
			t.Fatal(err)
		}
		if _, err = backdate.Exec(ctx, `UPDATE incident_transition SET at = $2::timestamptz - interval '1 hour' WHERE incident_id = $1`, id, at); err != nil {
			t.Fatal(err)
		}
	}
	if err = backdate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Without the retention the old log stays.
	err = database.WithRequestTenant(e.ctx, f.App, e.orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `DELETE FROM incident_transition WHERE incident_id = $1`, expired.ID)
		return execErr
	})
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE of an old transition without retention: %v", err)
	}
	deleted, err := alarm.NewRetention(f.App).WithClock(func() time.Time { return now }).RunOrganization(ctx, e.orgID, cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("retention: deleted %d, %v; want 1 (the expired incident)", deleted, err)
	}
	if _, err = e.store.GetIncident(e.ctx, e.orgID, expired.ID); !errors.Is(err, alarm.ErrIncidentNotFound) {
		t.Errorf("expired incident: %v", err)
	}
	var left int
	if err = f.Admin.QueryRow(ctx, `SELECT count(*) FROM incident_transition WHERE incident_id = $1`, expired.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d transitions of the expired incident remain (%v)", left, err)
	}
	for _, id := range []string{recent.ID, stillOpen.ID} {
		if _, err = e.store.GetIncident(e.ctx, e.orgID, id); err != nil {
			t.Errorf("incident %s removed: %v", id, err)
		}
	}
	// The alarm of the deleted incident stays with its own period.
	a, err := e.store.Get(e.ctx, e.orgID, ids[0])
	if err != nil || a.IncidentID != nil {
		t.Errorf("alarm of the deleted incident: %+v, %v; want kept without incident", a, err)
	}
	// The memory store follows the same rule.
	mem := memIncidentEnv()
	m := mem.store.(*alarm.MemoryStore)
	inc, _, _ := m.UpsertIncident(mem.ctx, mem.orgID, &alarm.IncidentInput{At: now.Add(-402 * day)})
	if _, err = m.TransitionIncident(mem.ctx, mem.orgID, inc.ID, alarm.IncidentResolved, nil); err != nil {
		t.Fatal(err)
	}
	if n := m.PurgeResolvedIncidents(mem.orgID, now.Add(time.Hour), alarm.RetentionBatch); n != 1 {
		t.Errorf("memory purge: %d, want 1", n)
	}
}
