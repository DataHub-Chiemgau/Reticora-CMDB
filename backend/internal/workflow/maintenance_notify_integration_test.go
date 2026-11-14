package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// The maintenance notification as a workflow on PostgreSQL (WFL-04, MGT-04,
// WP-1109): the template maintenance_notify on maintenance.scheduled waits
// (wait_until) for the announcement, the reminder and the end of the window
// and notifies the contacts of every client the window affects over the
// graph (maintenance_affected, the recipients of WP-1034), also of a client
// behind the window's router. A moved window ends the old run without
// sending, a cancelled one stops the run, a second delivery of the event
// starts no second run. The clock of the executor is the test's.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestMaintenance|TestWaitUntil' ./internal/workflow/

type maintenanceEnv struct {
	*notifyEnv
	windows  *maintenance.PGRepository
	consumer *TriggerConsumer
	window   *maintenance.Window
	now      time.Time
}

// testMaintenance adapts the recipient finder like the router.
type testMaintenance struct{ finder *maintenance.RecipientFinder }

func (m testMaintenance) Window(ctx context.Context, orgID, id string) (*MaintenanceWindow, error) {
	w, err := m.finder.Window(ctx, orgID, id)
	if errors.Is(err, api.ErrNotFound) {
		return nil, ErrMaintenanceWindowNotFound
	}
	if err != nil {
		return nil, err
	}
	return &MaintenanceWindow{ID: w.ID, Status: w.Status, StartsAt: w.StartsAt, EndsAt: w.EndsAt, UpdatedAt: w.UpdatedAt}, nil
}

func (m testMaintenance) AffectedClients(ctx context.Context, orgID, id string, heuristic bool) ([]string, error) {
	return m.finder.AffectedClients(ctx, orgID, id, heuristic)
}

// newMaintenanceEnv: a window on a router of client 1 with a CPE of
// client 2 behind it (uplink_to), starting in ten days; the template
// maintenance_notify active.
func newMaintenanceEnv(t *testing.T) *maintenanceEnv {
	t.Helper()
	n := newNotifyEnv(t)
	f, bg := n.f, context.Background()
	router := f.CI(t, f.OrgA, f.Client1, "mnt-router")
	cpe := f.CI(t, f.OrgA, f.Client2, "mnt-cpe")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'uplink_to')`,
		f.OrgA, cpe, router); err != nil {
		t.Fatal(err)
	}
	e := &maintenanceEnv{notifyEnv: n, windows: maintenance.NewPGRepository(f.App), now: time.Now().UTC()}
	start := e.now.Add(10 * 24 * time.Hour).Truncate(time.Second)
	e.window = &maintenance.Window{OrganizationID: f.OrgA, Title: "Router-Upgrade", StartsAt: start, EndsAt: start.Add(2 * time.Hour),
		CIIDs: []string{router}}
	if err := e.windows.Create(f.OrgCtx(f.OrgA), e.window); err != nil {
		t.Fatal(err)
	}
	analyzer := topology.NewAnalyzer(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App))
	e.exec.WithMaintenance(testMaintenance{finder: maintenance.NewRecipientFinder(e.windows, analyzer)}).
		WithClock(func() time.Time { return e.now })
	e.consumer = NewTriggerConsumer(e.exec, e.repo, nil, func(context.Context, string) bool { return true })
	tpl := MaintenanceNotifyTemplate(MaintenanceAnnounceHours, MaintenanceRemindHours)
	def := tpl.Definition(f.OrgA, fmt.Sprintf("wf-maint-%d", time.Now().UnixNano()))
	def.Active = true
	if err := validateWrite(def.Trigger, def.Conditions, def.Actions); err != nil {
		t.Fatalf("template: %v", err)
	}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	return e
}

// announce hands maintenance.scheduled of the window's current version for
// client to the trigger consumer under eventID.
func (e *maintenanceEnv) announce(t *testing.T, eventID, client string) {
	t.Helper()
	w, err := e.windows.GetByID(e.ctx, e.f.OrgA, e.window.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"id": w.ID, "client_id": client, "title": w.Title,
		"starts_at": w.StartsAt.UTC().Format(time.RFC3339), "ends_at": w.EndsAt.UTC().Format(time.RFC3339),
		"updated_at": w.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	env := &events.Envelope{ID: eventID, Type: events.MaintenanceScheduled, OrgID: e.f.OrgA, OccurredAt: time.Now().UTC(),
		EntityID: w.ID, Data: data}
	if err := e.consumer.Handle(context.Background(), env); err != nil {
		t.Fatal(err)
	}
}

// runs lists the runs of the window's workflow, oldest first.
func (e *maintenanceEnv) runs(t *testing.T) []Run {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT id::text FROM workflow_run WHERE organization_id = $1 AND trigger = $2
		ORDER BY created_at, id`, e.f.OrgA, events.MaintenanceScheduled)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		run, err := e.repo.GetRun(e.ctx, e.f.OrgA, id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, *run)
	}
	return out
}

// advance moves the clock to at and continues every pending run.
func (e *maintenanceEnv) advance(t *testing.T, at time.Time) {
	t.Helper()
	e.now = at
	for _, run := range e.runs(t) {
		if run.Status != StatusPending {
			continue
		}
		r := run
		if _, err := e.exec.resume(e.ctx, e.f.OrgA, &r, attempt{}); err != nil {
			t.Fatalf("resume %s: %v", r.ID, err)
		}
	}
}

// nextAttemptAt checks that every pending run waits until want.
func (e *maintenanceEnv) nextAttemptAt(t *testing.T, want time.Time) {
	t.Helper()
	for _, run := range e.runs(t) {
		var next *time.Time
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT next_attempt_at FROM workflow_run WHERE id = $1`, run.ID).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if run.Status != StatusPending || next == nil || !next.Equal(want) {
			t.Errorf("run %s %s next_attempt_at %v, want pending until %v", run.ID, run.Status, next, want)
		}
	}
}

// deliverAnnouncements hands the maintenance.scheduled events that
// NotifyClients wrote to the outbox and that are not handed yet to the
// trigger consumer, as the publisher would.
func (e *maintenanceEnv) deliverAnnouncements(t *testing.T) {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `UPDATE event_outbox SET published_at = now()
		WHERE organization_id = $1 AND type = $2 AND entity_id = $3 AND published_at IS NULL
		RETURNING id::text, occurred_at, data`, e.f.OrgA, events.MaintenanceScheduled, e.window.ID)
	if err != nil {
		t.Fatal(err)
	}
	var envs []*events.Envelope
	for rows.Next() {
		env := &events.Envelope{Type: events.MaintenanceScheduled, OrgID: e.f.OrgA, EntityID: e.window.ID}
		if err := rows.Scan(&env.ID, &env.OccurredAt, &env.Data); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	rows.Close()
	if len(envs) == 0 {
		t.Fatal("NotifyClients wrote no announcement")
	}
	for _, env := range envs {
		if err := e.consumer.Handle(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
}

// sent counts the deliveries of the window to email by the text kind
// (the dedup keys of the window) and the subjects.
func (e *maintenanceEnv) sent(t *testing.T, email string) []string {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT subject FROM notification_delivery
		WHERE organization_id = $1 AND recipient_email = $2 AND dedup_key LIKE $3 ORDER BY created_at, id`,
		e.f.OrgA, email, "maintenance:"+e.window.ID+":%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestMaintenanceWorkflowAnnouncesRemindsAndCompletes(t *testing.T) {
	e := newMaintenanceEnv(t)
	start, end := e.window.StartsAt, e.window.EndsAt
	// One announcement per affected client: two runs, one text each.
	e.announce(t, e.f.ID(), e.f.Client1)
	e.announce(t, e.f.ID(), e.f.Client2)
	if runs := e.runs(t); len(runs) != 2 || runs[0].Status != StatusPending {
		t.Fatalf("runs %+v, want two waiting", runs)
	}
	if got := e.sent(t, "desk@c2.example"); len(got) != 0 {
		t.Fatalf("sent before the announcement time: %v", got)
	}
	// Each wait_until sets the run pending with next_attempt_at at its
	// target (AK2).
	e.nextAttemptAt(t, start.Add(-MaintenanceAnnounceHours*time.Hour))
	e.advance(t, start.Add(-71*time.Hour))
	e.nextAttemptAt(t, start.Add(-MaintenanceRemindHours*time.Hour))
	e.advance(t, start.Add(-time.Hour))
	e.nextAttemptAt(t, end)
	e.advance(t, end.Add(time.Minute))
	for _, email := range []string{"desk@c1.example", "desk@c2.example", "owner@wf.example"} {
		got := e.sent(t, email)
		if len(got) != 3 || !strings.Contains(got[0], "Geplante Wartung") || !strings.Contains(got[1], "Erinnerung") ||
			!strings.Contains(got[2], "abgeschlossen") {
			t.Errorf("%s got %v, want announcement, reminder and completion once each", email, got)
		}
	}
	for _, run := range e.runs(t) {
		if run.Status != StatusSucceeded || run.Context["superseded"] == true {
			t.Errorf("run %s %s context %v", run.ID, run.Status, run.Context)
		}
	}
}

func TestMaintenanceWorkflowRescheduleSendsOneNewAnnouncement(t *testing.T) {
	e := newMaintenanceEnv(t)
	start := e.window.StartsAt
	e.announce(t, e.f.ID(), e.f.Client2)
	e.advance(t, start.Add(-71*time.Hour))
	if got := e.sent(t, "desk@c2.example"); len(got) != 1 {
		t.Fatalf("announcement: %v", got)
	}
	// The window moves by a day after the announcement.
	newStart := start.Add(24 * time.Hour)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE maintenance_window SET starts_at = $3, ends_at = $4, updated_at = now() + interval '1 second'
		WHERE organization_id = $1 AND id = $2`, e.f.OrgA, e.window.ID, newStart, newStart.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.announce(t, e.f.ID(), e.f.Client2)
	e.advance(t, newStart.Add(-71*time.Hour))
	got := e.sent(t, "desk@c2.example")
	if len(got) != 2 || !strings.Contains(got[1], "Geplante Wartung") {
		t.Fatalf("after the move: %v, want exactly one new announcement", got)
	}
	// The old reminder time passes: the old run ends superseded.
	e.advance(t, start.Add(-time.Hour))
	if got = e.sent(t, "desk@c2.example"); len(got) != 2 {
		t.Errorf("reminder at the old time sent: %v", got)
	}
	runs := e.runs(t)
	if runs[0].Status != StatusSucceeded || runs[0].Context["superseded"] != true {
		t.Errorf("old run %s %v, want succeeded and superseded", runs[0].Status, runs[0].Context)
	}
	e.advance(t, newStart.Add(-time.Hour))
	if got = e.sent(t, "desk@c2.example"); len(got) != 3 || !strings.Contains(got[2], "Erinnerung") {
		t.Errorf("reminder at the new time: %v", got)
	}
}

func TestMaintenanceWorkflowCancelledWindowStopsRun(t *testing.T) {
	e := newMaintenanceEnv(t)
	start := e.window.StartsAt
	e.announce(t, e.f.ID(), e.f.Client1)
	e.advance(t, start.Add(-71*time.Hour))
	status := "cancelled"
	if _, err := e.windows.Update(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, e.window.ID, maintenance.UpdateWindowRequest{Status: &status}); err != nil {
		t.Fatal(err)
	}
	e.advance(t, start.Add(-time.Hour))
	e.advance(t, e.window.EndsAt.Add(time.Minute))
	if got := e.sent(t, "desk@c1.example"); len(got) != 1 {
		t.Errorf("deliveries after the cancel: %v, want the announcement only", got)
	}
	if runs := e.runs(t); runs[0].Status != StatusSucceeded || runs[0].Context["superseded"] != true {
		t.Errorf("run %s %v, want succeeded and superseded", runs[0].Status, runs[0].Context)
	}
}

func TestMaintenanceWorkflowDuplicateEventNoSecondRun(t *testing.T) {
	e := newMaintenanceEnv(t)
	id := e.f.ID()
	e.announce(t, id, e.f.Client1)
	e.announce(t, id, e.f.Client1)
	if runs := e.runs(t); len(runs) != 1 {
		t.Errorf("runs of one event delivered twice: %d, want 1", len(runs))
	}
}

func TestMaintenanceAffectedUnknownWindowFails(t *testing.T) {
	e := newMaintenanceEnv(t)
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-maint-unknown-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{{"type": "send_notification", "recipients": map[string]any{"maintenance_affected": map[string]any{"window_id": "window.id"}},
			"subject": "x", "body": "y"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{"window": map[string]any{"id": e.f.ID()}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusFailed || run.Steps[0].Error != "maintenance window not found" {
		t.Errorf("run %s step %+v, want failed with the fixed text", run.Status, run.Steps)
	}
}

func TestWaitUntilPastTimeContinuesImmediately(t *testing.T) {
	e := newMaintenanceEnv(t)
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-wait-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{{"type": waitUntilActionType, "at": "when - 1h"}, {"type": "noop"}}, Active: true}
	if err := validateWaitActions(def.Actions); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{"when": e.now.Add(-time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusSucceeded || len(run.Steps) != 2 {
		t.Errorf("run %s steps %+v, want succeeded at once", run.Status, run.Steps)
	}
	if err := validateWaitActions([]JSONMap{{"type": waitUntilActionType, "at": "when ~ 1h"}}); err == nil {
		t.Error("an invalid at was accepted")
	}
}

// TestMaintenanceWorkflowFollowsTheOperatorsChoice: a run of an
// announcement of NotifyClients reaches its client only, in the contact
// roles the operator chose.
func TestMaintenanceWorkflowFollowsTheOperatorsChoice(t *testing.T) {
	e := newMaintenanceEnv(t)
	plan := &maintenance.NotifyPlan{Clients: []maintenance.ClientImpact{{ClientID: e.f.Client1}}, ContactRoles: []string{"it"}}
	if _, err := e.windows.NotifyClients(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, e.window.ID, plan); err != nil {
		t.Fatal(err)
	}
	e.deliverAnnouncements(t)
	e.advance(t, e.window.StartsAt.Add(-71*time.Hour))
	if got := e.sent(t, "desk@c1.example"); len(got) != 1 {
		t.Errorf("contact in the chosen role: %v, want the announcement", got)
	}
	for _, email := range []string{"owner@wf.example", "desk@c2.example"} {
		if got := e.sent(t, email); len(got) != 0 {
			t.Errorf("%s outside the chosen client or roles got %v", email, got)
		}
	}
}

// TestMaintenanceWorkflowTitleChangeSendsNoDuplicate: a change that does
// not move the window, announced again over NotifyClients, starts a second
// run; both send each text once per contact (dedup by schedule).
func TestMaintenanceWorkflowTitleChangeSendsNoDuplicate(t *testing.T) {
	e := newMaintenanceEnv(t)
	plan := &maintenance.NotifyPlan{Clients: []maintenance.ClientImpact{{ClientID: e.f.Client2}}}
	ctx := e.f.OrgCtx(e.f.OrgA)
	if _, err := e.windows.NotifyClients(ctx, e.f.OrgA, e.window.ID, plan); err != nil {
		t.Fatal(err)
	}
	e.deliverAnnouncements(t)
	start := e.window.StartsAt
	e.advance(t, start.Add(-71*time.Hour))
	title := "Router-Upgrade (Firmware 17.9)"
	if _, err := e.windows.Update(ctx, e.f.OrgA, e.window.ID, maintenance.UpdateWindowRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.windows.NotifyClients(ctx, e.f.OrgA, e.window.ID, plan); err != nil {
		t.Fatal(err)
	}
	e.deliverAnnouncements(t)
	if runs := e.runs(t); len(runs) != 2 {
		t.Fatalf("runs %d, want the re-announcement's second run", len(runs))
	}
	e.advance(t, start.Add(-time.Hour))
	e.advance(t, e.window.EndsAt.Add(time.Minute))
	got := e.sent(t, "desk@c2.example")
	if len(got) != 3 || !strings.Contains(got[0], "Geplante Wartung") || !strings.Contains(got[1], "Erinnerung") ||
		!strings.Contains(got[2], "abgeschlossen") {
		t.Errorf("deliveries %v, want announcement, reminder and completion once each", got)
	}
	for _, run := range e.runs(t) {
		if run.Status != StatusSucceeded || run.Context["superseded"] == true {
			t.Errorf("run %s %s %v, want succeeded, not superseded", run.ID, run.Status, run.Context)
		}
	}
}

// TestMaintenanceWorkflowSubsecondWindowStatusChange: a window with
// fractions of a second in its start keeps its run through the status
// change to in_progress (the event carries the times to the microsecond).
func TestMaintenanceWorkflowSubsecondWindowStatusChange(t *testing.T) {
	e := newMaintenanceEnv(t)
	start := e.window.StartsAt.Add(250 * time.Millisecond)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE maintenance_window SET starts_at = $3 WHERE organization_id = $1 AND id = $2`,
		e.f.OrgA, e.window.ID, start); err != nil {
		t.Fatal(err)
	}
	plan := &maintenance.NotifyPlan{Clients: []maintenance.ClientImpact{{ClientID: e.f.Client2}}}
	ctx := e.f.OrgCtx(e.f.OrgA)
	if _, err := e.windows.NotifyClients(ctx, e.f.OrgA, e.window.ID, plan); err != nil {
		t.Fatal(err)
	}
	e.deliverAnnouncements(t)
	e.advance(t, start.Add(-71*time.Hour))
	status := "in_progress"
	if _, err := e.windows.Update(ctx, e.f.OrgA, e.window.ID, maintenance.UpdateWindowRequest{Status: &status}); err != nil {
		t.Fatal(err)
	}
	e.advance(t, start.Add(-time.Hour))
	if got := e.sent(t, "desk@c2.example"); len(got) != 2 {
		t.Errorf("deliveries %v, want announcement and reminder", got)
	}
}
