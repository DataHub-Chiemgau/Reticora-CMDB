package notify_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// The alarm and incident notifications (NTF-05 [P3], MON-04, ALR-02,
// WP-987) against PostgreSQL: alarm.Service writes alarm.opened and
// alarm.resolved to the outbox, the publisher hands them to the event
// consumer, the notifier delivers them. The audience is the contacts of
// the CI in the roles operations and escalation and the users with
// monitoring:read in the client and site of the alarm; critical alarms
// ignore quiet hours, the others wait; a suppressed alarm is not
// delivered; one delivery per transition, none after a rollback.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).

// alarmEnv is an organization with monitoring readers of the organization,
// of client 1 and of client 2, a CI per client with contacts, the e-mail
// and in-app channels and the pipeline outbox → consumer → notifier.
type alarmEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	prefix    string
	users     map[string]string
	ci1, ci2  string
	svc       *alarm.Service
	mail      *recordingProvider
	notifier  *notify.Notifier
	publisher *events.Publisher
	prefs     notify.PreferenceStore
	now       time.Time
}

func newAlarmEnv(t *testing.T, prefix string) *alarmEnv {
	t.Helper()
	f := scopetest.Seed(t, prefix)
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })

	role := func(name, scope, perms string) string {
		id := f.ID()
		exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, $4, $5::jsonb)`, id, f.OrgA, name, scope, perms)
		return id
	}
	orgRole := role("alr-org", "org", `["monitoring:read"]`)
	clientRole := role("alr-client", "client", `["monitoring:read"]`)
	ciReader := role("alr-ci", "org", `["ci:read"]`)
	e := &alarmEnv{t: t, f: f, prefix: prefix, users: map[string]string{}, now: time.Date(2020, 1, 6, 10, 5, 0, 0, time.UTC)}
	for _, u := range []struct {
		name, role string
		client     any
	}{
		{"org", orgRole, nil}, {"quiet", orgRole, nil}, {"c1", clientRole, f.Client1}, {"c2", clientRole, f.Client2},
		{"cireader", ciReader, nil},
	} {
		e.users[u.name] = f.AppUser(t, f.OrgA, u.name)
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
			f.OrgA, e.users[u.name], u.role, u.client)
	}
	e.ci1 = f.CI(t, f.OrgA, f.Client1, "alr-core-1")
	e.ci2 = f.CI(t, f.OrgA, f.Client2, "alr-core-2")
	for _, c := range []struct{ ci, client, name, rel string }{
		{e.ci1, f.Client1, "op1", "operations"}, {e.ci1, f.Client1, "esc1", "escalation"}, {e.ci1, f.Client1, "own1", "owner"},
		{e.ci2, f.Client2, "op2", "operations"},
	} {
		id := f.ID()
		exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, $4, $5)`,
			id, f.OrgA, c.client, c.name, c.name+"@contacts.invalid")
		exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, $4, $5)`,
			f.OrgA, c.ci, id, c.rel, c.client)
	}

	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err := center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	// The whole day is quiet for the user quiet.
	if err := center.Preferences.SetPreference(bg, f.OrgA, e.users["quiet"],
		&notify.Preference{QuietStart: "00:00", QuietEnd: "23:59", TimeZone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	e.prefs = center.Preferences
	e.mail = &recordingProvider{}
	e.notifier = notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: e.mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return e.now })
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), e.notifier).
		WithLinkBase("https://cmdb.example/invitations/accept")
	e.publisher = events.NewPublisher(f.App, nil, consumer)
	e.svc = alarm.NewPGService(f.App)
	return e
}

func (e *alarmEnv) mailOf(name string) string {
	if strings.HasPrefix(name, "op") || strings.HasPrefix(name, "esc") || strings.HasPrefix(name, "own") {
		return name + "@contacts.invalid"
	}
	return "scopetest-" + e.prefix + "-" + name + "@scopetest.invalid"
}

// process publishes the outbox and runs the due deliveries.
func (e *alarmEnv) process() {
	e.t.Helper()
	if _, err := e.publisher.PublishPending(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	for range 3 {
		if _, err := e.notifier.ProcessDue(context.Background()); err != nil {
			e.t.Fatal(err)
		}
	}
}

// raise opens an alarm of the CI with the severity.
func (e *alarmEnv) raise(ci, severity string) *alarm.Alarm {
	e.t.Helper()
	a, err := e.svc.Raise(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: e.f.ID(),
		SeriesKey: "s", CIID: &ci, Severity: severity, Summary: "cpu " + severity, Details: map[string]any{"value": 97.5}})
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

// sentTo lists the sorted e-mail recipients of the mails of an event type
// and entity id.
func (e *alarmEnv) sentTo(eventType, id string) []string {
	var out []string
	for _, m := range e.mail.sent {
		if m.Data["event_type"] == eventType && m.Data["id"] == id {
			for _, r := range m.To {
				out = append(out, r.Email)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (e *alarmEnv) want(names ...string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, e.mailOf(n))
	}
	sort.Strings(out)
	return out
}

func TestAlarmNotifyCriticalIgnoresQuietHours(t *testing.T) {
	e := newAlarmEnv(t, "a7")
	a := e.raise(e.ci1, "critical")
	e.process()
	if got, want := e.sentTo(events.AlarmOpened, a.ID), e.want("org", "quiet", "c1", "op1", "esc1"); !slices.Equal(got, want) {
		t.Fatalf("critical alarm.opened to %v, want %v", got, want)
	}
	for _, m := range e.mail.sent {
		if m.Data["id"] == a.ID && m.To[0].Email == e.mailOf("org") {
			if !strings.Contains(m.Subject, "alr-core-1") || !strings.Contains(m.Body, "Wert: 97.5") ||
				!strings.Contains(m.Body, "https://cmdb.example/alarms?id="+a.ID) {
				t.Errorf("mail text: %q / %q", m.Subject, m.Body)
			}
		}
	}
}

func TestAlarmNotifyWarningRespectsQuietHours(t *testing.T) {
	e := newAlarmEnv(t, "a8")
	a := e.raise(e.ci1, "warning")
	e.process()
	if got, want := e.sentTo(events.AlarmOpened, a.ID), e.want("org", "c1", "op1", "esc1"); !slices.Equal(got, want) {
		t.Fatalf("warning alarm.opened to %v, want %v (quiet waits)", got, want)
	}
	var pending int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notification_delivery
		WHERE organization_id = $1 AND recipient_user_id = $2 AND category = 'alarm' AND status = 'pending'`,
		e.f.OrgA, e.users["quiet"]).Scan(&pending); err != nil || pending == 0 {
		t.Errorf("deliveries of the quiet user waiting: %d (%v), want > 0", pending, err)
	}
}

// TestAlarmNotifyEscalationToCriticalIgnoresQuietHours: an alarm that
// opened as warning during quiet hours and escalates to critical reaches
// the quiet user at once (alarm.severity_changed, critical, AK1).
func TestAlarmNotifyEscalationToCriticalIgnoresQuietHours(t *testing.T) {
	e := newAlarmEnv(t, "ad")
	ci := e.ci1
	in := &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: e.f.ID(), SeriesKey: "s", CIID: &ci,
		Severity: "warning", Summary: "cpu warning", Details: map[string]any{"value": 85.0}}
	a, err := e.svc.Raise(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, in)
	if err != nil {
		t.Fatal(err)
	}
	e.process()
	if got := e.sentTo(events.AlarmOpened, a.ID); slices.Contains(got, e.mailOf("quiet")) {
		t.Fatalf("warning alarm.opened reached the quiet user: %v", got)
	}
	in.Severity, in.Summary, in.Details = "critical", "cpu critical", map[string]any{"value": 97.5}
	if _, err := e.svc.Raise(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, in); err != nil {
		t.Fatal(err)
	}
	e.process()
	if got, want := e.sentTo(events.AlarmSeverityChanged, a.ID), e.want("org", "quiet", "c1", "op1", "esc1"); !slices.Equal(got, want) {
		t.Fatalf("escalation to critical to %v, want %v", got, want)
	}
	for _, m := range e.mail.sent {
		if m.Data["event_type"] == events.AlarmSeverityChanged && m.Data["id"] == a.ID && !strings.Contains(m.Body, "Wert: 97.5") {
			t.Errorf("escalation text names not the current value: %q", m.Body)
		}
	}
}

func TestAlarmNotifyClientIsolation(t *testing.T) {
	e := newAlarmEnv(t, "a9")
	a := e.raise(e.ci2, "critical")
	e.process()
	got := e.sentTo(events.AlarmOpened, a.ID)
	if want := e.want("org", "quiet", "c2", "op2"); !slices.Equal(got, want) {
		t.Fatalf("alarm of client 2 to %v, want %v", got, want)
	}
	for _, other := range []string{"c1", "op1", "esc1", "own1", "cireader"} {
		if slices.Contains(got, e.mailOf(other)) {
			t.Errorf("%s of another client or without monitoring:read got the alarm", other)
		}
	}
}

func TestAlarmNotifySuppressedNotDelivered(t *testing.T) {
	e := newAlarmEnv(t, "aa")
	emitter := events.NewPGEmitter(e.f.App)
	ids := map[string]string{}
	for _, reason := range []string{"symptom", "maintenance", ""} {
		id := e.f.ID()
		ids[reason] = id
		d := events.AlarmData{ID: id, AlarmID: id, DedupKey: "rule:" + id + ":s", CIID: e.ci1, ClientID: e.f.Client1,
			Severity: "critical", SourceKind: "rule", Summary: "x", SuppressedReason: reason}
		if err := emitter.Emit(context.Background(), &events.Event{OrgID: e.f.OrgA, Type: events.AlarmOpened, EntityID: id, Data: d}); err != nil {
			t.Fatal(err)
		}
	}
	e.process()
	for _, reason := range []string{"symptom", "maintenance"} {
		if got := e.sentTo(events.AlarmOpened, ids[reason]); len(got) != 0 {
			t.Errorf("suppressed %s: delivered to %v", reason, got)
		}
	}
	if got := e.sentTo(events.AlarmOpened, ids[""]); len(got) == 0 {
		t.Error("the alarm without suppression reached nobody")
	}
}

// TestAlarmNotifyDisabledByRule covers MON-04 (WP-988): the alarms of a
// rule with actions.notify.enabled false notify nobody on opening and
// resolving, while alarm.opened and alarm.resolved stay in the outbox (and
// carry the actions); an alarm of a rule with notify on is delivered.
func TestAlarmNotifyDisabledByRule(t *testing.T) {
	e := newAlarmEnv(t, "ae")
	ctx := e.f.OrgCtx(e.f.OrgA)
	ci := e.ci1
	off := alarm.Actions{Notify: events.AlarmAction{Enabled: false}, Webhook: events.AlarmAction{Enabled: true}}
	muted, err := e.svc.Raise(ctx, e.f.OrgA, &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: e.f.ID(),
		SeriesKey: "s", CIID: &ci, Severity: "critical", Summary: "dashboard only", Details: map[string]any{"value": 1.0},
		Actions: &off})
	if err != nil {
		t.Fatal(err)
	}
	e.process()
	e.now = e.now.Add(10 * time.Minute)
	if _, err := e.svc.Clear(ctx, e.f.OrgA, muted.DedupKey, e.now, ""); err != nil {
		t.Fatal(err)
	}
	loud := e.raise(e.ci1, "critical")
	e.process()
	for _, typ := range []string{events.AlarmOpened, events.AlarmResolved} {
		if got := e.sentTo(typ, muted.ID); len(got) != 0 {
			t.Errorf("%s of a rule without notification delivered to %v", typ, got)
		}
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox
			WHERE organization_id = $1 AND type = $2 AND entity_id = $3
			  AND data #>> '{actions,notify,enabled}' = 'false'`, e.f.OrgA, typ, muted.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s in the outbox with the actions: %d, want 1", typ, n)
		}
	}
	var inapp int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notification_delivery
		WHERE organization_id = $1 AND category = 'alarm' AND data ->> 'id' = $2`, e.f.OrgA, muted.ID).Scan(&inapp); err != nil {
		t.Fatal(err)
	}
	if inapp != 0 {
		t.Errorf("deliveries of the muted alarm: %d, want 0", inapp)
	}
	if got := e.sentTo(events.AlarmOpened, loud.ID); len(got) == 0 {
		t.Error("the alarm of a rule with notification reached nobody")
	}
}

func TestAlarmNotifyOncePerTransition(t *testing.T) {
	e := newAlarmEnv(t, "ab")
	ctx := e.f.OrgCtx(e.f.OrgA)
	a := e.raise(e.ci1, "critical")
	// One more occurrence of the open alarm is no transition.
	e.raise(e.ci1, "critical")
	e.process()
	e.now = e.now.Add(25 * time.Minute)
	if _, err := e.svc.Clear(ctx, e.f.OrgA, a.DedupKey, e.now, ""); err != nil {
		t.Fatal(err)
	}
	e.process()
	// A second run of the publisher and the notifier sends nothing again.
	e.process()
	count := func(eventType string) map[string]int {
		n := map[string]int{}
		for _, m := range e.mail.sent {
			if m.Data["event_type"] == eventType && m.Data["dedup_key"] == a.DedupKey {
				for _, r := range m.To {
					n[r.Email]++
				}
			}
		}
		return n
	}
	for _, typ := range []string{events.AlarmOpened, events.AlarmResolved} {
		got := count(typ)
		if len(got) != 5 {
			t.Errorf("%s: recipients %v, want 5", typ, got)
		}
		for to, n := range got {
			if n != 1 {
				t.Errorf("%s: %d mails to %s, want 1", typ, n, to)
			}
		}
	}
	// The in-app channel too: exactly one inbox entry per user and
	// transition, and one delivery per recipient, channel and transition.
	for _, typ := range []string{events.AlarmOpened, events.AlarmResolved} {
		rows, err := e.f.Admin.Query(ctx, `SELECT user_id::text, count(*) FROM notification_inbox
			WHERE organization_id = $1 AND data->>'event_type' = $2 AND data->>'dedup_key' = $3 GROUP BY user_id`,
			e.f.OrgA, typ, a.DedupKey)
		if err != nil {
			t.Fatal(err)
		}
		inbox := map[string]int{}
		for rows.Next() {
			var (
				user string
				n    int
			)
			if err := rows.Scan(&user, &n); err != nil {
				t.Fatal(err)
			}
			inbox[user] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"org", "quiet", "c1"} {
			if inbox[e.users[name]] != 1 {
				t.Errorf("%s: %d inbox entries of %s, want 1", typ, inbox[e.users[name]], name)
			}
		}
		if len(inbox) != 3 {
			t.Errorf("%s: inbox entries of %d users, want 3 (org, quiet, c1)", typ, len(inbox))
		}
		var doubled, channels int
		if err := e.f.Admin.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM (SELECT 1 FROM notification_delivery
				  WHERE organization_id = $1 AND data->>'event_type' = $2 AND data->>'dedup_key' = $3
				  GROUP BY channel_id, recipient_key HAVING count(*) > 1) d),
				(SELECT count(DISTINCT channel_id) FROM notification_delivery
				  WHERE organization_id = $1 AND data->>'event_type' = $2 AND data->>'dedup_key' = $3)`,
			e.f.OrgA, typ, a.DedupKey).Scan(&doubled, &channels); err != nil {
			t.Fatal(err)
		}
		if doubled != 0 || channels != 2 {
			t.Errorf("%s: %d recipients with several deliveries on a channel, deliveries on %d channels; want 0 and 2", typ, doubled, channels)
		}
	}
	var resolved bool
	for _, m := range e.mail.sent {
		if m.Data["event_type"] == events.AlarmResolved && m.Data["dedup_key"] == a.DedupKey && strings.Contains(m.Body, "Dauer: ") {
			resolved = true
		}
	}
	if !resolved {
		t.Error("alarm.resolved names no duration")
	}

	// A rolled back raise writes no event and notifies nobody.
	errRollback := errors.New("rollback")
	var rolled *alarm.Alarm
	err := e.svc.InTx(ctx, e.f.OrgA, func(ctx context.Context) error {
		ci := e.ci1
		var rerr error
		rolled, rerr = e.svc.Raise(ctx, e.f.OrgA, &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: e.f.ID(),
			SeriesKey: "s", CIID: &ci, Severity: "critical", Summary: "rolled back"})
		if rerr != nil {
			return rerr
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) || rolled == nil {
		t.Fatalf("rollback: %v", err)
	}
	e.process()
	if got := e.sentTo(events.AlarmOpened, rolled.ID); len(got) != 0 {
		t.Errorf("rolled back alarm delivered to %v", got)
	}
	var rolledInbox int
	if err := e.f.Admin.QueryRow(ctx, `SELECT count(*) FROM notification_inbox WHERE organization_id = $1 AND data->>'id' = $2`,
		e.f.OrgA, rolled.ID).Scan(&rolledInbox); err != nil || rolledInbox != 0 {
		t.Errorf("rolled back alarm in %d inboxes (%v), want none", rolledInbox, err)
	}
}

func TestIncidentNotifySynthetic(t *testing.T) {
	e := newAlarmEnv(t, "ac")
	emitter := events.NewPGEmitter(e.f.App)
	id := e.f.ID()
	for _, typ := range []string{events.IncidentOpened, events.IncidentResolved} {
		d := events.IncidentData{ID: id, RootCIID: e.ci1, ClientID: e.f.Client1, Severity: "warning", CauseClass: "network"}
		if err := emitter.Emit(context.Background(), &events.Event{OrgID: e.f.OrgA, Type: typ, EntityID: id, Data: d}); err != nil {
			t.Fatal(err)
		}
	}
	e.process()
	want := e.want("org", "c1", "op1", "esc1")
	for _, typ := range []string{events.IncidentOpened, events.IncidentResolved} {
		if got := e.sentTo(typ, id); !slices.Equal(got, want) {
			t.Errorf("%s to %v, want %v", typ, got, want)
		}
	}
	for _, m := range e.mail.sent {
		if m.Data["id"] == id && !strings.Contains(m.Body, "https://cmdb.example/alarms?incident_id="+id) {
			t.Errorf("incident mail without link: %q", m.Body)
		}
	}
}
