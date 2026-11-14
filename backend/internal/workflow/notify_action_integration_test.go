package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The action send_notification on PostgreSQL (WFL-02, NTF-05 [P3],
// WP-1095): recipients from the contact roles of the run's CI and the
// contacts of its client, the category workflow with mute and quiet hours,
// deduplication per run and step, and the fixed texts of a failed step.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestNotify|TestCriticalNotifyIgnoresMute' ./internal/workflow/

// mailbox records the e-mails the notifier sends.
type mailbox struct {
	mu   sync.Mutex
	sent []*notify.Message
}

func (m *mailbox) Send(_ context.Context, _ *notify.Channel, msg *notify.Message) (*notify.Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return &notify.Delivery{Recipients: len(msg.To), SentAt: time.Now().UTC()}, nil
}

type notifyEnv struct {
	f          *scopetest.Fixture
	repo       *PGRepository
	exec       *Executor
	notifier   *notify.Notifier
	deliveries *notify.PGDeliveryStore
	prefs      *notify.PGPreferences
	ctx        context.Context
	ciID       string
	// reader holds ci:read and workflow:read organization-wide.
	reader string
}

func newNotifyEnv(t *testing.T) *notifyEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	e := &notifyEnv{f: f, repo: NewPGRepository(f.App), deliveries: notify.NewPGDeliveryStore(f.App), prefs: notify.NewPGPreferences(f.App)}
	user := f.AppUser(t, f.OrgA, "wf-notify-actor")
	scope := database.OrgWideScope(f.OrgA, user)
	e.ctx = tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	role := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wf-notify-readers', 'org', '["ci:read", "workflow:read"]'::jsonb)`, role, f.OrgA)
	e.reader = f.AppUser(t, f.OrgA, "wf-notify-reader")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, e.reader, role)

	e.ciID = f.CI(t, f.OrgA, f.Client1, "wf-notify-ci")
	for _, c := range []struct{ name, email, rel string }{{"Owner", "owner@wf.example", "owner"}, {"Vendor", "vendor@wf.example", "vendor"}} {
		id := f.ID()
		exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, $4, $5)`, id, f.OrgA, f.Client1, c.name, c.email)
		exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, $4, $5)`,
			f.OrgA, e.ciID, id, c.rel, f.Client1)
	}
	exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email, role) VALUES ($1, $2, $3, 'C1 Desk', 'desk@c1.example', 'IT')`, f.ID(), f.OrgA, f.Client1)
	exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email, role) VALUES ($1, $2, $3, 'C2 Desk', 'desk@c2.example', 'IT')`, f.ID(), f.OrgA, f.Client2)

	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	channels := notify.NewPGRepository(f.App, notify.NewSealer(enc))
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err := channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	e.notifier = notify.NewNotifier(channels, e.deliveries, e.prefs, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: &mailbox{}, notify.ProviderInApp: notify.NewInAppProvider(notify.NewPGInbox(f.App)),
	}))
	e.exec = NewExecutor(e.repo, nil, ci.NewPGRepository(f.App), nil, nil).
		WithNotifier(e.notifier, notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)))
	return e
}

// run executes a definition with action on the env's CI in the request.
func (e *notifyEnv) run(t *testing.T, action JSONMap) *Run {
	t.Helper()
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-notify-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{action}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{"ci_id": e.ciID, "data": map[string]any{"host": "srv-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// queued lists the deliveries of a run, as recipient key → delivery.
func (e *notifyEnv) queued(t *testing.T, runID string) map[string]notify.QueuedDelivery {
	t.Helper()
	all, err := e.deliveries.List(e.ctx, e.f.OrgA, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]notify.QueuedDelivery{}
	for _, d := range all {
		if !strings.HasPrefix(d.DedupKey, "workflow:"+runID+":") {
			continue
		}
		out[d.RecipientKey] = d
	}
	return out
}

func keys(m map[string]notify.QueuedDelivery) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// TestNotifyResolvesCIContactRoleOwner: contact_roles owner reaches the
// owner of the run's CI, not its vendor; the texts carry the placeholders
// of the run context and the category is workflow.
func TestNotifyResolvesCIContactRoleOwner(t *testing.T) {
	e := newNotifyEnv(t)
	run := e.run(t, JSONMap{"type": "send_notification", "recipients": map[string]any{"contact_roles": []any{"owner"}},
		"subject": "Host {{data.host}}", "body": map[string]any{"de": "Bitte {{data.host}} prüfen.", "en": "Please check {{data.host}}."}})
	if run.Status != StatusSucceeded {
		t.Fatalf("run: %s steps %+v, want succeeded", run.Status, run.Steps)
	}
	got := e.queued(t, run.ID)
	d, ok := got["mail:owner@wf.example"]
	if !ok || len(got) != 1 {
		t.Fatalf("deliveries %s, want only the owner", keys(got))
	}
	if d.Category != notify.CategoryWorkflow || !strings.Contains(d.Subject, "Host srv-1") || !strings.Contains(d.Body, "srv-1") || d.DedupKey != "workflow:"+run.ID+":0" {
		t.Errorf("delivery %+v, want category workflow with the host and the dedup key of step 0", d)
	}
	if q := fmt.Sprint(run.Steps[0].Output["queued"]); q != "1" {
		t.Errorf("step output %v, want queued 1", run.Steps[0].Output)
	}
}

// TestNotifyClientContactsOnlyOwnClient: client_contacts reaches the
// contacts of the client of the run's CI, never those of another client.
func TestNotifyClientContactsOnlyOwnClient(t *testing.T) {
	e := newNotifyEnv(t)
	run := e.run(t, JSONMap{"type": "send_notification", "recipients": map[string]any{"client_contacts": map[string]any{"roles": []any{"IT"}}},
		"subject": "Wartung", "body": "Heute Abend."})
	if run.Status != StatusSucceeded {
		t.Fatalf("run: %s steps %+v", run.Status, run.Steps)
	}
	if got := e.queued(t, run.ID); keys(got) != "mail:desk@c1.example" {
		t.Errorf("deliveries %s, want only the contact of client 1", keys(got))
	}
}

// TestNotifyRetryNoDoubleDelivery: the step executed again (a retry of an
// interrupted attempt) queues nothing twice.
func TestNotifyRetryNoDoubleDelivery(t *testing.T) {
	e := newNotifyEnv(t)
	action := JSONMap{"type": "send_notification", "recipients": map[string]any{"users": []any{e.reader}}, "subject": "Einmal"}
	run := e.run(t, action)
	first := e.queued(t, run.ID)
	if run.Status != StatusSucceeded || len(first) == 0 {
		t.Fatalf("run: %s deliveries %s", run.Status, keys(first))
	}
	if !idempotentActions["send_notification"] {
		t.Error("send_notification is not repeated after an interruption")
	}
	out, err := e.exec.sendNotification(e.ctx, e.f.OrgA, run, 0, action)
	if err != nil {
		t.Fatal(err)
	}
	if again := e.queued(t, run.ID); len(again) != len(first) {
		t.Errorf("deliveries after the repeated step: %s, want %s", keys(again), keys(first))
	}
	if q := fmt.Sprint(out["queued"]); q != "0" {
		t.Errorf("repeated step queued %s, want 0", q)
	}
}

// process sends the due deliveries.
func (e *notifyEnv) process(t *testing.T) {
	t.Helper()
	if _, err := e.notifier.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// inAppStatus returns the status of the in-app delivery of user in run.
func (e *notifyEnv) inAppStatus(t *testing.T, runID, user string) (string, time.Time) {
	t.Helper()
	var status string
	var next time.Time
	err := e.f.Admin.QueryRow(context.Background(), `SELECT d.status, d.next_attempt_at FROM notification_delivery d
		JOIN notification_channel c ON c.id = d.channel_id WHERE d.organization_id = $1 AND d.dedup_key = $2
		AND c.provider = 'inapp' AND d.recipient_key = $3`, e.f.OrgA, "workflow:"+runID+":0", "user:"+user).Scan(&status, &next)
	if err != nil {
		t.Fatalf("in-app delivery of %s: %v", user, err)
	}
	return status, next
}

// TestNotifyRespectsQuietHoursAndMute: a user who mutes the category
// workflow gets nothing (suppressed), a user in quiet hours gets the
// notification after them.
func TestNotifyRespectsQuietHoursAndMute(t *testing.T) {
	e := newNotifyEnv(t)
	quiet := e.f.AppUser(t, e.f.OrgA, "wf-notify-quiet")
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT organization_id, $2, role_id FROM role_assignment WHERE user_id = $1`, e.reader, quiet); err != nil {
		t.Fatal(err)
	}
	if err := e.prefs.SetPreference(e.ctx, e.f.OrgA, e.reader, &notify.Preference{TimeZone: "UTC", MutedCategories: []string{notify.CategoryWorkflow}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := e.prefs.SetPreference(e.ctx, e.f.OrgA, quiet, &notify.Preference{TimeZone: "UTC",
		QuietStart: now.Add(-time.Hour).Format("15:04"), QuietEnd: now.Add(time.Hour).Format("15:04")}); err != nil {
		t.Fatal(err)
	}
	run := e.run(t, JSONMap{"type": "send_notification", "recipients": map[string]any{"users": []any{e.reader, quiet}}, "subject": "Leise"})
	if run.Status != StatusSucceeded {
		t.Fatalf("run: %s steps %+v", run.Status, run.Steps)
	}
	e.process(t)
	if status, _ := e.inAppStatus(t, run.ID, e.reader); status != notify.StatusSuppressed {
		t.Errorf("muted user: %s, want suppressed", status)
	}
	if status, next := e.inAppStatus(t, run.ID, quiet); status != notify.StatusPending || !next.After(now.Add(30*time.Minute)) {
		t.Errorf("user in quiet hours: %s next %v, want pending until the end of the quiet hours", status, next)
	}
}

// TestCriticalNotifyIgnoresMute: critical = true reaches a user who mutes
// the category workflow.
func TestCriticalNotifyIgnoresMute(t *testing.T) {
	e := newNotifyEnv(t)
	if err := e.prefs.SetPreference(e.ctx, e.f.OrgA, e.reader, &notify.Preference{TimeZone: "UTC", MutedCategories: []string{notify.CategoryWorkflow}}); err != nil {
		t.Fatal(err)
	}
	run := e.run(t, JSONMap{"type": "send_notification", "recipients": map[string]any{"users": []any{e.reader}}, "subject": "Dringend", "critical": true})
	if run.Status != StatusSucceeded {
		t.Fatalf("run: %s steps %+v", run.Status, run.Steps)
	}
	e.process(t)
	if status, _ := e.inAppStatus(t, run.ID, e.reader); status != notify.StatusSent {
		t.Errorf("critical notification to a muting user: %s, want sent", status)
	}
}

// TestNotifyUnknownRecipientFailsStep: an unknown user, team or role, and
// an empty recipient set, fail the step with the fixed text; without a
// notifier the step fails with "notifications unavailable".
func TestNotifyUnknownRecipientFailsStep(t *testing.T) {
	e := newNotifyEnv(t)
	unknown := "00000000-0000-4000-8000-00000000abcd"
	for name, rec := range map[string]map[string]any{
		"unknown user":   {"users": []any{e.reader, unknown}},
		"unknown team":   {"teams": []any{unknown}},
		"unknown role":   {"roles": []any{"no-such-role"}},
		"no recipient":   {"contact_roles": []any{"escalation"}},
		"no audience at": {},
	} {
		run := e.run(t, JSONMap{"type": "send_notification", "recipients": rec, "subject": "x"})
		if run.Status != StatusFailed || len(run.Steps) != 1 || run.Steps[0].Error != errNoRecipient.Error() {
			t.Errorf("%s: run %s steps %+v, want failed with %q", name, run.Status, run.Steps, errNoRecipient)
		}
		if got := e.queued(t, run.ID); len(got) != 0 {
			t.Errorf("%s: deliveries %s, want none", name, keys(got))
		}
	}
	e.exec.WithNotifier(nil, nil)
	run := e.run(t, JSONMap{"type": "send_notification", "recipients": map[string]any{"users": []any{e.reader}}, "subject": "x"})
	if run.Status != StatusFailed || run.Steps[0].Error != errNotificationsUnavailable.Error() {
		t.Errorf("without a notifier: %s steps %+v, want failed with %q", run.Status, run.Steps, errNotificationsUnavailable)
	}
}

// TestNotifySubjectIsOneLine: a subject whose placeholder value or text
// holds line breaks is queued on one line (the SMTP provider refuses a
// line break in a header), the body keeps them.
func TestNotifySubjectIsOneLine(t *testing.T) {
	e := newNotifyEnv(t)
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-notify-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{{"type": "send_notification", "recipients": map[string]any{"contact_roles": []any{"owner"}},
			"subject": "Host {{data.host}}\r\nBcc: x@evil.example", "body": "Host {{data.host}}"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{"ci_id": e.ciID, "data": map[string]any{"host": "a\nb"}})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("run: %v %+v, want succeeded", err, run)
	}
	d, ok := e.queued(t, run.ID)["mail:owner@wf.example"]
	if !ok {
		t.Fatalf("no delivery to the owner")
	}
	if strings.ContainsAny(d.Subject, "\r\n") || !strings.Contains(d.Subject, "Host a b Bcc: x@evil.example") {
		t.Errorf("subject %q, want one line", d.Subject)
	}
	if !strings.Contains(d.Body, "a\nb") {
		t.Errorf("body %q, want the line break of the value", d.Body)
	}
}
