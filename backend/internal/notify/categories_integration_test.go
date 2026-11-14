package notify_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// The [P3] categories of NTF-05 against PostgreSQL (WP-1147): Alerts
// (alarm with sla.violated, config), maintenance windows (maintenance) and
// workflow (workflow.run_failed). A critical alarm reaches a user who muted
// alarm and bundles it daily at once; a warning is muted or bundled. The
// events are written to the outbox as their producers write them (WP-1049,
// WP-1054, WP-1092) and go through the publisher, the consumer and the
// notifier of alarmEnv.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).

// emit writes one event of the organization to the outbox.
func (e *alarmEnv) emit(typ, id string, data map[string]any) {
	e.t.Helper()
	if err := events.NewPGEmitter(e.f.App).Emit(context.Background(),
		&events.Event{OrgID: e.f.OrgA, Type: typ, EntityID: id, Data: data}); err != nil {
		e.t.Fatal(err)
	}
}

// emitData writes one event with the marshalled contract payload of its
// producer, so the consumer sees only the fields of the contract.
func (e *alarmEnv) emitData(typ, id string, payload any) {
	e.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		e.t.Fatal(err)
	}
	e.emit(typ, id, data)
}

// exec runs a statement over the admin pool.
func (e *alarmEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// prefer stores a preference of a user.
func (e *alarmEnv) prefer(user string, p *notify.Preference) {
	e.t.Helper()
	if err := e.prefs.SetPreference(context.Background(), e.f.OrgA, e.users[user], p); err != nil {
		e.t.Fatal(err)
	}
}

// deliveries counts the deliveries of a user in a category with a status.
func (e *alarmEnv) deliveries(user, category, status string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notification_delivery
		WHERE organization_id = $1 AND recipient_user_id = $2 AND category = $3 AND status = $4`,
		e.f.OrgA, e.users[user], category, status).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestCriticalAlarmNeverDigestedOrMuted(t *testing.T) {
	e := newAlarmEnvWith(t, "f1", true)
	e.prefer("org", &notify.Preference{MutedCategories: []string{notify.CategoryAlarm},
		Digest: map[string]string{notify.CategoryAlarm: notify.DigestDaily}, TimeZone: "UTC"})
	a := e.raise(e.ci1, "critical")
	e.process()
	if got := e.sentTo(events.AlarmOpened, a.ID); !slices.Contains(got, e.mailOf("org")) {
		t.Fatalf("critical alarm to %v, want the user who muted and bundles alarm", got)
	}
	var items int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notification_digest_item
		WHERE organization_id = $1 AND recipient_user_id = $2`, e.f.OrgA, e.users["org"]).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 0 {
		t.Errorf("digest items of the critical alarm: %d, want 0", items)
	}
}

func TestWarningAlarmMutedAndDigested(t *testing.T) {
	e := newAlarmEnvWith(t, "f2", true)
	e.prefer("org", &notify.Preference{MutedCategories: []string{notify.CategoryAlarm}, TimeZone: "UTC"})
	e.prefer("c1", &notify.Preference{Digest: map[string]string{notify.CategoryAlarm: notify.DigestDaily}, TimeZone: "UTC"})
	a := e.raise(e.ci1, "warning")
	e.process()
	got := e.sentTo(events.AlarmOpened, a.ID)
	for _, user := range []string{"org", "c1"} {
		if slices.Contains(got, e.mailOf(user)) {
			t.Errorf("warning alarm mailed to %s at once: %v", user, got)
		}
	}
	if !slices.Contains(got, e.mailOf("op1")) {
		t.Errorf("warning alarm to %v, want the operations contact", got)
	}
	if n := e.deliveries("org", notify.CategoryAlarm, "suppressed"); n == 0 {
		t.Error("the muted user has no suppressed alarm delivery")
	}
	var items int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notification_digest_item
		WHERE organization_id = $1 AND recipient_user_id = $2 AND category = 'alarm'`, e.f.OrgA, e.users["c1"]).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 1 {
		t.Errorf("digest items of the bundled warning: %d, want 1", items)
	}
}

func TestWorkflowRunFailedReachesInitiatorAndAdmins(t *testing.T) {
	e := newAlarmEnv(t, "f3")
	role := func(name, scope, perms string) string {
		id := e.f.ID()
		e.exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, $4, $5::jsonb)`,
			id, e.f.OrgA, name, scope, perms)
		return id
	}
	reader := role("wf-reader", "org", `["workflow:read"]`)
	var admin string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`,
		e.f.OrgA).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ name, role string }{{"admin", admin}, {"reader", reader}} {
		e.users[u.name] = e.f.AppUser(e.t, e.f.OrgA, u.name)
		e.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, e.f.OrgA, e.users[u.name], u.role)
	}
	e.users["initiator"] = e.f.AppUser(e.t, e.f.OrgA, "initiator")
	def, version, run := e.f.ID(), e.f.ID(), e.f.ID()
	e.exec(`WITH d AS (INSERT INTO workflow_def (id, organization_id, name, current_version_id) VALUES ($1, $2, 'Nachtlauf', $3))
		INSERT INTO workflow_def_version (id, organization_id, workflow_id, version) VALUES ($3, $2, $1, 1)`, def, e.f.OrgA, version)
	e.exec(`INSERT INTO workflow_run (id, organization_id, workflow_id, definition_version_id, status, trigger, initiated_by, ci_id, last_error)
		VALUES ($1, $2, $3, $4, 'failed', 'manual', $5, $6, 'step 2 failed')`, run, e.f.OrgA, def, version, e.users["initiator"], e.ci1)
	e.exec(`INSERT INTO workflow_step (organization_id, run_id, step_index, action_type, status, error)
		VALUES ($1, $2, 1, 'send_webhook', 'failed', 'POST https://hooks.example/T0/secret-token: status 500')`, e.f.OrgA, run)
	e.emit(events.WorkflowRunFailed, run, map[string]any{"id": run, "run_id": run, "workflow_id": def,
		"definition_version_id": version, "trigger": "manual", "status": "failed", "ci_id": e.ci1, "client_id": e.f.Client1})
	e.process()
	got := e.sentTo(events.WorkflowRunFailed, run)
	if want := e.want("admin", "initiator"); !slices.Equal(got, want) {
		t.Fatalf("workflow.run_failed to %v, want %v (initiator and org_admin, not the reader without the role)", got, want)
	}
	for _, m := range e.mail.sent {
		if m.Data["event_type"] != events.WorkflowRunFailed {
			continue
		}
		if strings.Contains(m.Body, "secret-token") || !strings.Contains(m.Body, "https://hooks.example/********") ||
			!strings.Contains(m.Body, "Nachtlauf") || !strings.Contains(m.Body, "2 (send_webhook)") ||
			!strings.Contains(m.Body, "https://cmdb.example/workflows/runs/"+run) {
			t.Errorf("mail text: %q / %q", m.Subject, m.Body)
		}
	}
}

func TestSLAViolatedClientIsolation(t *testing.T) {
	e := newAlarmEnv(t, "f4")
	id := e.f.ID()
	e.exec(`INSERT INTO availability_target (id, organization_id, client_id, name, scope_kind, ci_id, target_percent, period)
		VALUES ($1, $2, $3, 'Core-Netz', 'ci', $4, 99.9, 'month')`, id, e.f.OrgA, e.f.Client2, e.ci2)
	// The contract payload of the producer (WP-1049): no name, no CI.
	e.emitData(events.SLAViolated, id, events.SLAViolatedData{ID: id, ClientID: e.f.Client2,
		Period: "2026-10", TargetPercent: 99.9, ActualPercent: 98.5})
	e.process()
	got := e.sentTo(events.SLAViolated, id)
	if want := e.want("org", "c2", "op2"); !slices.Equal(got, want) {
		t.Fatalf("sla.violated of client 2 to %v, want %v (quiet waits)", got, want)
	}
	for _, other := range []string{"c1", "op1", "esc1", "cireader"} {
		if slices.Contains(got, e.mailOf(other)) {
			t.Errorf("%s of another client or without monitoring:read got the violation", other)
		}
	}
	for _, m := range e.mail.sent {
		if m.Data["event_type"] == events.SLAViolated && m.To[0].Email == e.mailOf("org") &&
			(!strings.Contains(m.Body, "98,5 %") || !strings.Contains(m.Body, "2026-10") || !strings.Contains(m.Subject, "Core-Netz") ||
				!strings.Contains(m.Body, "/slas?tab=availability&target_id="+id)) {
			t.Errorf("mail text: %q / %q", m.Subject, m.Body)
		}
	}
}

// snapshot stores a configuration version of a CI, compressed as the
// configsnap store keeps it.
func (e *alarmEnv) snapshot(ci, client, content string, at time.Time) string {
	e.t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		e.t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	id := e.f.ID()
	e.exec(`INSERT INTO config_snapshot (id, organization_id, client_id, ci_id, collected_at, source, trigger, platform, sha256, size_bytes, content)
		VALUES ($1, $2, $3, $4, $5, 'ssh', 'schedule', 'ios', $6, $7, $8)`,
		id, e.f.OrgA, client, ci, at, hex.EncodeToString(sum[:]), len(content), buf.Bytes())
	return id
}

func TestConfigChangedOnlyOperationsContacts(t *testing.T) {
	e := newAlarmEnv(t, "f5")
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	e.snapshot(e.ci1, e.f.Client1, "hostname alr-core-1\nntp server 10.0.0.1\nsnmp-server community s3cret\n", at.Add(-time.Hour))
	id := e.snapshot(e.ci1, e.f.Client1, "hostname alr-core-1\nntp server 10.0.0.2\nsnmp-server community s3cret\nlogging host 10.0.0.9\n", at)
	// The contract payload of the producer (WP-1054): no line count.
	e.emitData(events.CIConfigChanged, id, events.CIConfigChangedData{ID: id, CIID: e.ci1, ClientID: e.f.Client1,
		Hash: "9f2c4e7a1b"})
	e.process()
	got := e.sentTo(events.CIConfigChanged, id)
	if want := e.want("org", "c1", "op1"); !slices.Equal(got, want) {
		t.Fatalf("ci.config_changed to %v, want %v", got, want)
	}
	n := 0
	for _, m := range e.mail.sent {
		if m.Data["event_type"] != events.CIConfigChanged {
			continue
		}
		n++
		if strings.Contains(m.Subject+m.Body, "9f2c4e7a1b") || strings.Contains(m.Subject+m.Body, "s3cret") ||
			(!strings.Contains(m.Body, "Zeilen: 3") && !strings.Contains(m.Body, "lines: 3")) || !strings.Contains(m.Body, "2026-11-09T10:00:00Z") ||
			!strings.Contains(m.Body, "/cmdb/"+e.ci1+"?config="+id+"&against=") || !strings.Contains(m.Subject, "alr-core-1") {
			t.Errorf("mail text: %q / %q", m.Subject, m.Body)
		}
		for _, k := range []string{"hash", "sha256", "previous_sha256"} {
			if _, ok := m.Data[k]; ok {
				t.Errorf("notification data names the configuration %s: %v", k, m.Data)
			}
		}
	}
	if n == 0 {
		t.Fatal("no ci.config_changed mail")
	}
}

func TestNewOrganizationSubscribedToP3Categories(t *testing.T) {
	e := newAlarmEnv(t, "f6")
	user := e.f.AppUser(e.t, e.f.OrgB, "fresh")
	p, err := e.prefs.Preference(context.Background(), e.f.OrgB, user)
	if err != nil || p != nil {
		t.Fatalf("stored preference of a new user: %+v, %v; want none", p, err)
	}
	p = &notify.Preference{}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.FillSubscriptions()
	for _, c := range []string{notify.CategoryAlarm, notify.CategoryConfig, notify.CategoryMaintenance, notify.CategoryWorkflow} {
		if p.Subscriptions[c] != notify.SubscriptionSubscribed {
			t.Errorf("default subscription of %s: %q, want subscribed", c, p.Subscriptions[c])
		}
	}
	if _, ok := p.Subscriptions["escalation"]; ok {
		t.Errorf("subscriptions %v name escalation", p.Subscriptions)
	}
}

func TestMaintenanceCategoryRenamedMutedKept(t *testing.T) {
	e := newAlarmEnv(t, "f7")
	role := e.f.ID()
	e.exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'mnt-reader', 'org', '["maintenance:read"]'::jsonb)`,
		role, e.f.OrgA)
	for _, name := range []string{"silent", "loud"} {
		e.users[name] = e.f.AppUser(e.t, e.f.OrgA, name)
		e.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, e.f.OrgA, e.users[name], role)
	}
	// A preference stored under the earlier category name.
	e.exec(`INSERT INTO notification_preference (organization_id, user_id, time_zone, muted_categories, digest)
		VALUES ($1, $2, 'UTC', ARRAY['maintenance_window'], '{"maintenance_window": "daily"}'::jsonb)`, e.f.OrgA, e.users["silent"])
	p, err := e.prefs.Preference(context.Background(), e.f.OrgA, e.users["silent"])
	if err != nil || p == nil {
		t.Fatalf("preference: %+v, %v", p, err)
	}
	if !slices.Equal(p.MutedCategories, []string{notify.CategoryMaintenance}) || p.Digest[notify.CategoryMaintenance] != notify.DigestDaily {
		t.Fatalf("read preference: muted %v, digest %v; want maintenance", p.MutedCategories, p.Digest)
	}
	p.FillSubscriptions()
	if p.Subscriptions[notify.CategoryMaintenance] != notify.SubscriptionMuted {
		t.Errorf("subscription of maintenance %q, want muted", p.Subscriptions[notify.CategoryMaintenance])
	}
	id := e.f.ID()
	e.emit(events.MaintenanceScheduled, id, map[string]any{"id": id, "client_id": e.f.Client1, "title": "Core-Upgrade",
		"starts_at": "2026-12-01T22:00:00Z", "ends_at": "2026-12-02T02:00:00Z", "affected_cis": 1, "services": 0})
	e.process()
	got := e.sentTo(events.MaintenanceScheduled, id)
	if slices.Contains(got, e.mailOf("silent")) || !slices.Contains(got, e.mailOf("loud")) {
		t.Fatalf("maintenance.scheduled to %v, want loud and not silent", got)
	}
	if n := e.deliveries("silent", notify.CategoryMaintenance, "suppressed"); n == 0 {
		t.Error("the user who muted the earlier name has no suppressed delivery")
	}
}

// TestNamedContactsStayInClient covers the contact recipient of
// request_form (WP-1102): with the client of the run, Resolve names the
// contacts of that client only, never one of another client.
func TestNamedContactsStayInClient(t *testing.T) {
	e := newAlarmEnv(t, "f9")
	ids := map[string]string{}
	for _, name := range []string{"op1", "op2"} {
		var id string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM contact WHERE organization_id = $1 AND display_name = $2`,
			e.f.OrgA, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	resolver := notify.NewPGResolver(e.f.App, permission.NewPGRepository(e.f.App))
	emails := func(a *notify.Audience) []string {
		got, err := resolver.Resolve(context.Background(), e.f.OrgA, a)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range got {
			out = append(out, r.Email)
		}
		slices.Sort(out)
		return out
	}
	both := []string{ids["op1"], ids["op2"]}
	if got := emails(&notify.Audience{ContactIDs: both, ClientID: e.f.Client1}); !slices.Equal(got, []string{"op1@contacts.invalid"}) {
		t.Errorf("contacts in the run of client 1: %v, want op1 only", got)
	}
	if got := emails(&notify.Audience{ContactIDs: both}); len(got) != 2 {
		t.Errorf("contacts of an organization-wide run: %v, want both", got)
	}
}
