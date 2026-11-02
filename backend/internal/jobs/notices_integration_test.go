package jobs_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// noticeEvent is a notice event of the outbox.
type noticeEvent struct {
	id, typ, entity string
	data            map[string]any
}

// noticeLog reads the notice events of the outbox; fresh returns those not
// returned before.
type noticeLog struct {
	t     *testing.T
	f     *scopetest.Fixture
	types []string
	seen  map[string]bool
}

func newNoticeLog(t *testing.T, f *scopetest.Fixture, types ...string) *noticeLog {
	return &noticeLog{t: t, f: f, types: types, seen: map[string]bool{}}
}

// fresh returns the organization's notice events appended since the last
// call, oldest first.
func (l *noticeLog) fresh(org string) []noticeEvent {
	l.t.Helper()
	rows, err := l.f.Admin.Query(context.Background(), `SELECT id::text, type, COALESCE(entity_id::text, ''), data
		FROM event_outbox WHERE organization_id = $1 AND type = ANY($2) ORDER BY sequence`, org, l.types)
	if err != nil {
		l.t.Fatal(err)
	}
	defer rows.Close()
	var out []noticeEvent
	for rows.Next() {
		var e noticeEvent
		var raw []byte
		if err = rows.Scan(&e.id, &e.typ, &e.entity, &raw); err != nil {
			l.t.Fatal(err)
		}
		if l.seen[e.id] {
			continue
		}
		l.seen[e.id] = true
		if err = json.Unmarshal(raw, &e.data); err != nil {
			l.t.Fatal(err)
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		l.t.Fatal(err)
	}
	return out
}

// noticeRows counts the organization's notice_sent rows.
func noticeRows(t *testing.T, f *scopetest.Fixture, org string) int {
	t.Helper()
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notice_sent WHERE organization_id = $1`, org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sameTransaction reports whether the notice_sent row of the key and the
// outbox event id were written by one transaction (xmin).
func sameTransaction(t *testing.T, f *scopetest.Fixture, org, key, eventID string) bool {
	t.Helper()
	var same bool
	if err := f.Admin.QueryRow(context.Background(), `SELECT (SELECT xmin::text FROM notice_sent WHERE organization_id = $1 AND dedup_key = $2)
		= (SELECT xmin::text FROM event_outbox WHERE id = $3)`, org, key, eventID).Scan(&same); err != nil {
		t.Fatal(err)
	}
	return same
}

// TestEntitlementNotices covers JOB-02, ENT-07, NTF-05 and EVT-02 (WP-425,
// F-044, F-071 in part) against PostgreSQL. The entitlement.notices run:
//   - valid_until in seven days: exactly one entitlement.warning per
//     organization (feature, valid_until, days_left), a second run sends
//     none; event and notice_sent row come from one transaction; through
//     the event consumer the warning reaches the user:manage holders of the
//     organization as in-app notification, nobody else;
//   - one warning each at 14, 7 and 1 day ahead, none on a second run the
//     same day, exactly one entitlement.expired after valid_until; an
//     extended valid_until is warned of again 14 days ahead; organization B
//     without valid_until gets nothing;
//   - bounded catch-up: a first run six days ahead sends one warning, for
//     the threshold of 7 days, not one for 14; a run after a pause of ten
//     days sends exactly one warning, the youngest due threshold;
//   - after the outbox cleanup (clock 31 days on) the expiry is not sent
//     again: the deduplication is notice_sent, not the outbox.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEntitlementNotices(t *testing.T) {
	f := scopetest.Seed(t, "46")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	entitlementOf := func(org, feature string, validUntil *time.Time) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO entitlement (organization_id, feature_key, enabled, valid_until)
			VALUES ($1, $2, true, $3) RETURNING id::text`, org, feature, validUntil).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	at := func(tm time.Time) *time.Time { return &tm }
	t0 := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	clock := t0
	notices := jobs.NewEntitlementNotices(f.App).WithClock(func() time.Time { return clock })
	run := func(now time.Time) {
		t.Helper()
		clock = now
		if _, err := notices.Run(bg); err != nil {
			t.Fatalf("run at %s: %v", now, err)
		}
	}
	log := newNoticeLog(t, f, events.EntitlementWarning, events.EntitlementExpired)
	// expect checks the notices organization A received since the last
	// check: one warning (days left, threshold in the dedup key) or one
	// expiry, or none for typ "".
	expect := func(step, entitlementID, typ string, validUntil time.Time, daysLeft, threshold int) {
		t.Helper()
		got := log.fresh(f.OrgA)
		if typ == "" {
			if len(got) != 0 {
				t.Errorf("%s: %d notices, want none: %+v", step, len(got), got)
			}
			return
		}
		if len(got) != 1 || got[0].typ != typ || got[0].entity != entitlementID {
			t.Errorf("%s: notices %+v, want one %s of entitlement %s", step, got, typ, entitlementID)
			return
		}
		until := validUntil.UTC().Format(time.RFC3339)
		key := entitlementID + "/" + until
		d := got[0].data
		if typ == events.EntitlementWarning {
			key += fmt.Sprintf("/%d", threshold)
			if d["valid_until"] != until || d["days_left"] != float64(daysLeft) || d["feature"] == "" || len(d) != 3 {
				t.Errorf("%s: warning data %v, want feature, valid_until %s, days_left %d", step, d, until, daysLeft)
			}
		} else if d["expired_at"] != until || d["feature"] == "" || len(d) != 2 {
			t.Errorf("%s: expiry data %v, want feature, expired_at %s", step, d, until)
		}
		if !sameTransaction(t, f, f.OrgA, key, got[0].id) {
			t.Errorf("%s: no notice_sent row %s from the transaction of the event", step, key)
		}
	}

	// Notifications: an in-app channel per organization, a user:manage
	// holder in A and B, a user without it in A.
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	admins := map[string]string{}
	for _, org := range []string{f.OrgA, f.OrgB} {
		if err := center.Channels.Create(bg, &notify.Channel{OrganizationID: org, Name: "center", Provider: notify.ProviderInApp, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		role := f.ID()
		exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp425-admin', 'org', '["user:manage"]'::jsonb)`, role, org)
		admins[org] = f.AppUser(t, org, "admin-"+org[len(org)-4:])
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, org, admins[org], role)
	}
	viewerRole := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp425-viewer', 'org', '["ci:read"]'::jsonb)`, viewerRole, f.OrgA)
	viewer := f.AppUser(t, f.OrgA, "viewer")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, viewer, viewerRole)
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences,
		notify.NewSender(map[string]notify.Provider{notify.ProviderInApp: notify.NewInAppProvider(center.Inbox)}))
	publisher := events.NewPublisher(f.App, nil, notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)).
		WithClock(func() time.Time { return clock })

	// valid_until = now + 7 days in both organizations.
	discoveryA := entitlementOf(f.OrgA, "discovery", at(t0.Add(7*day)))
	webhooksB := entitlementOf(f.OrgB, "webhooks", at(t0.Add(7*day)))
	run(t0)
	expect("7 days ahead", discoveryA, events.EntitlementWarning, t0.Add(7*day), 7, 7)
	if got := log.fresh(f.OrgB); len(got) != 1 || got[0].entity != webhooksB || got[0].data["feature"] != "webhooks" {
		t.Errorf("organization B, 7 days ahead: %+v, want one warning of its webhooks entitlement", got)
	}
	run(t0)
	expect("second run", discoveryA, "", t0, 0, 0)
	if got := log.fresh(f.OrgB); len(got) != 0 {
		t.Errorf("organization B, second run: %+v", got)
	}
	if a, b := noticeRows(t, f, f.OrgA), noticeRows(t, f, f.OrgB); a != 1 || b != 1 {
		t.Errorf("notice_sent rows: A %d, B %d, want 1 each", a, b)
	}
	if _, err := publisher.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := notifier.ProcessDue(bg); err != nil {
		t.Fatal(err)
	}
	inbox := func(org, user string) []notify.InAppNotification {
		t.Helper()
		items, _, err := center.Inbox.List(bg, org, user, false, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	if items := inbox(f.OrgA, admins[f.OrgA]); len(items) != 1 || items[0].Category != "entitlement" ||
		!strings.Contains(items[0].Subject, "discovery") || !strings.Contains(items[0].Body, t0.Add(7*day).Format(time.RFC3339)) {
		t.Errorf("in-app of the user:manage holder of A: %+v, want the discovery warning", items)
	}
	if items := inbox(f.OrgA, viewer); len(items) != 0 {
		t.Errorf("in-app of the user without user:manage: %+v", items)
	}
	if items := inbox(f.OrgB, admins[f.OrgB]); len(items) != 1 || !strings.Contains(items[0].Subject, "webhooks") {
		t.Errorf("in-app of the user:manage holder of B: %+v, want only B's webhooks warning", items)
	}
	exec(`DELETE FROM entitlement WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB)

	// 14, 7 and 1 day ahead, expiry, extension; B without valid_until.
	v := t0.Add(100 * day)
	topology := entitlementOf(f.OrgA, "topology", &v)
	entitlementOf(f.OrgB, "discovery", nil)
	bRows := noticeRows(t, f, f.OrgB)
	for _, step := range []struct {
		name      string
		now       time.Time
		typ       string
		daysLeft  int
		threshold int
	}{
		{"15 days ahead", v.Add(-15 * day), "", 0, 0},
		{"14 days ahead", v.Add(-14 * day), events.EntitlementWarning, 14, 14},
		{"14 days ahead, again the same day", v.Add(-14*day + 2*time.Hour), "", 0, 0},
		{"8 days ahead", v.Add(-8 * day), "", 0, 0},
		{"7 days ahead", v.Add(-7 * day), events.EntitlementWarning, 7, 7},
		{"1 day ahead", v.Add(-day), events.EntitlementWarning, 1, 1},
		{"1 day ahead, again", v.Add(-day + time.Hour), "", 0, 0},
		{"expired", v.Add(time.Hour), events.EntitlementExpired, 0, 0},
		{"a day after the expiry", v.Add(day), "", 0, 0},
	} {
		run(step.now)
		expect(step.name, topology, step.typ, v, step.daysLeft, step.threshold)
	}
	v2 := v.Add(60 * day)
	exec(`UPDATE entitlement SET valid_until = $2 WHERE id = $1`, topology, v2)
	run(v2.Add(-20 * day))
	expect("extended, 20 days ahead", topology, "", v2, 0, 0)
	run(v2.Add(-14 * day))
	expect("extended, 14 days ahead", topology, events.EntitlementWarning, v2, 14, 14)
	if got := log.fresh(f.OrgB); len(got) != 0 || noticeRows(t, f, f.OrgB) != bRows {
		t.Errorf("organization B without valid_until: notices %+v", got)
	}
	exec(`DELETE FROM entitlement WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB)

	// First run six days ahead: the threshold of 7 days only.
	w := t0.Add(200 * day)
	rackView := entitlementOf(f.OrgA, "rack_view", &w)
	run(w.Add(-6 * day))
	expect("first run 6 days ahead", rackView, events.EntitlementWarning, w, 6, 7)
	var fourteen int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM notice_sent WHERE organization_id = $1 AND dedup_key LIKE $2`,
		f.OrgA, rackView+"/%/14").Scan(&fourteen); err != nil || fourteen != 0 {
		t.Errorf("first run 6 days ahead recorded %d notices of 14 days (%v)", fourteen, err)
	}
	exec(`DELETE FROM entitlement WHERE organization_id = $1`, f.OrgA)

	// A pause of ten days after the warning of 14 days: one warning, the
	// youngest due threshold (7 days).
	x := t0.Add(300 * day)
	webhooks := entitlementOf(f.OrgA, "webhooks", &x)
	run(x.Add(-14 * day))
	expect("before the pause, 14 days ahead", webhooks, events.EntitlementWarning, x, 14, 14)
	run(x.Add(-4 * day))
	expect("after ten days, 4 days ahead", webhooks, events.EntitlementWarning, x, 4, 7)
	exec(`DELETE FROM entitlement WHERE organization_id = $1`, f.OrgA)

	// Expiry, then the outbox cleanup: published events older than 30
	// days go. The cleanup keeps the newest event of the organization and
	// of each entity for the numbering; they are removed as well, so
	// nothing of the notices is left in the outbox.
	y := t0.Add(400 * day)
	exportCSV := entitlementOf(f.OrgA, "export_csv", &y)
	run(y.Add(time.Hour))
	expect("expired before the cleanup", exportCSV, events.EntitlementExpired, y, 0, 0)
	if _, err := publisher.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	outboxNotices := func() (notices, entities int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT count(*), count(DISTINCT entity_id) FROM event_outbox WHERE organization_id = $1 AND type = ANY($2)`,
			f.OrgA, []string{events.EntitlementWarning, events.EntitlementExpired}).Scan(&notices, &entities); err != nil {
			t.Fatal(err)
		}
		return notices, entities
	}
	before, _ := outboxNotices()
	clock = y.Add(31 * day)
	if err := publisher.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	if kept, entities := outboxNotices(); kept >= before || kept != entities {
		t.Errorf("notice events after the cleanup: %d of %d, want only the newest of each of the %d entitlements", kept, before, entities)
	}
	exec(`DELETE FROM event_outbox WHERE organization_id = $1`, f.OrgA)
	aRows := noticeRows(t, f, f.OrgA)
	run(y.Add(31*day + time.Hour))
	expect("after the outbox cleanup", exportCSV, "", y, 0, 0)
	if n := noticeRows(t, f, f.OrgA); n != aRows {
		t.Errorf("notice_sent rows after the cleanup run: %d, want %d", n, aRows)
	}
	if got := log.fresh(f.OrgB); len(got) != 0 {
		t.Errorf("organization B: %+v", got)
	}
}

// collectorCert is a certificate of a test collector.
type collectorCert struct {
	notBefore, notAfter time.Time
	revoked             bool
}

// TestCertificateNotices covers JOB-02, COL-04, NTF-05 and EVT-02 (WP-425,
// F-044) against PostgreSQL. The certificate.notices run sends exactly one
// certificate.expiring per certificate and organization:
//   - for a collector certificate past its renewal point (60 % of the
//     lifetime, plus the grace of two days in which a working collector
//     renews), with name, expires_at, fingerprint and the collector's
//     client and site; not before the grace ended, not for a renewed
//     certificate, a disabled collector or a young certificate;
//   - for the active collector CA past its renewal point, to every
//     organization with a collector that holds a certificate; not for a
//     young CA;
//   - not again on a second run, nor after the outbox cleanup (clock 31
//     days on): the deduplication is notice_sent, not the outbox;
//   - organization B gets no notice of A's collectors.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCertificateNotices(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	bg := context.Background()
	if jobs.CertificateRenewAfter != discovery.RenewAfter {
		t.Fatalf("renewal share %v, the collectors renew from %v", jobs.CertificateRenewAfter, discovery.RenewAfter)
	}
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	if _, err := discovery.NewCA(f.App, enc).TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var siteA string
	if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp425-site') RETURNING id::text`,
		f.OrgA, f.Client1).Scan(&siteA); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	fingerprints := map[string]string{}
	// collector inserts a collector whose last certificate is current.
	collector := func(org, client, site, name, status string, certs ...collectorCert) string {
		t.Helper()
		id := f.ID()
		var clientID, siteID any
		if client != "" {
			clientID = client
		}
		if site != "" {
			siteID = site
		}
		exec(`INSERT INTO collector (id, organization_id, client_id, site_id, name, status) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, org, clientID, siteID, name, status)
		for i, c := range certs {
			fp := fmt.Sprintf("%x", sha256.Sum256([]byte(id+fmt.Sprint(i))))
			var revoked any
			if c.revoked {
				revoked = c.notBefore.Add(day)
			}
			exec(`INSERT INTO collector_certificate (organization_id, collector_id, serial, fingerprint, not_before, not_after, revoked_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, org, id, fp[:32], fp, c.notBefore, c.notAfter, revoked)
			exec(`UPDATE collector SET cert_fingerprint = $2, cert_not_after = $3 WHERE id = $1`, id, fp, c.notAfter)
			fingerprints[id] = fp
		}
		return id
	}
	due := collector(f.OrgA, f.Client1, siteA, "wp425-due", "active", collectorCert{t0.Add(-60 * day), t0.Add(30 * day), false})
	grace := collector(f.OrgA, f.Client2, "", "wp425-grace", "offline", collectorCert{t0.Add(-55 * day), t0.Add(35 * day), false})
	collector(f.OrgA, f.Client1, siteA, "wp425-renewed", "active",
		collectorCert{t0.Add(-60 * day), t0.Add(30 * day), false}, collectorCert{t0.Add(-day), t0.Add(89 * day), false})
	collector(f.OrgA, f.Client1, "", "wp425-disabled", "disabled", collectorCert{t0.Add(-80 * day), t0.Add(10 * day), true})
	collector(f.OrgA, "", "", "wp425-young", "active", collectorCert{t0.Add(-10 * day), t0.Add(80 * day), false})
	collector(f.OrgB, "", "", "wp425-b", "active", collectorCert{t0.Add(-10 * day), t0.Add(80 * day), false})

	clock := t0
	notices := jobs.NewCertificateNotices(f.App).WithClock(func() time.Time { return clock })
	run := func(now time.Time) {
		t.Helper()
		clock = now
		if _, err := notices.Run(bg); err != nil {
			t.Fatalf("run at %s: %v", now, err)
		}
	}
	log := newNoticeLog(t, f, events.CertificateExpiring)
	// expectCollector checks that A received exactly the notice of the
	// collector's current certificate.
	expectCollector := func(step, collectorID, client, site string, notAfter time.Time) {
		t.Helper()
		got := log.fresh(f.OrgA)
		if len(got) != 1 || got[0].entity != collectorID {
			t.Errorf("%s: notices %+v, want one of collector %s", step, got, collectorID)
			return
		}
		d := got[0].data
		want := map[string]any{"name": d["name"], "expires_at": notAfter.Format(time.RFC3339), "fingerprint": fingerprints[collectorID],
			"collector_id": collectorID}
		if client != "" {
			want["client_id"] = client
		}
		if site != "" {
			want["site_id"] = site
		}
		if !strings.HasPrefix(fmt.Sprint(d["name"]), "wp425-") || fmt.Sprint(d) != fmt.Sprint(want) {
			t.Errorf("%s: data %v, want %v", step, d, want)
		}
		if !sameTransaction(t, f, f.OrgA, fingerprints[collectorID], got[0].id) {
			t.Errorf("%s: no notice_sent row from the transaction of the event", step)
		}
	}
	expectNone := func(step string) {
		t.Helper()
		for _, org := range []string{f.OrgA, f.OrgB} {
			if got := log.fresh(org); len(got) != 0 {
				t.Errorf("%s: notices of %s: %+v, want none", step, org, got)
			}
		}
	}

	// A young CA; one collector past the grace.
	run(t0)
	expectCollector("renewal overdue", due, f.Client1, siteA, t0.Add(30*day))
	expectNone("renewal overdue, other notices")
	run(t0.Add(time.Hour))
	expectNone("second run")
	// The grace of the second collector ends (renewal point t0-1d, + 2 days).
	run(t0.Add(day - time.Minute))
	expectNone("within the grace")
	run(t0.Add(2 * day))
	expectCollector("after the grace", grace, f.Client2, "", t0.Add(35*day))
	expectNone("after the grace, other notices")

	// The active CA past its renewal point (issued seven years ago, ten
	// years valid): one notice per organization with a collector.
	clean()
	old := discovery.NewCA(f.App, enc).WithClock(func() time.Time { return t0.AddDate(-7, 0, 0) })
	bundle, err := old.TrustBundle(bg)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(bundle))
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(caCert.Raw)
	caFingerprint := hex.EncodeToString(sum[:])
	run(t0.Add(2*day + time.Hour))
	for _, org := range []string{f.OrgA, f.OrgB} {
		got := log.fresh(org)
		want := map[string]any{"name": caCert.Subject.CommonName, "expires_at": caCert.NotAfter.UTC().Format(time.RFC3339), "fingerprint": caFingerprint}
		if len(got) != 1 || got[0].entity != "" || fmt.Sprint(got[0].data) != fmt.Sprint(want) {
			t.Errorf("CA past its renewal point, organization %s: %+v, want one notice %v", org, got, want)
			continue
		}
		if !sameTransaction(t, f, org, caFingerprint, got[0].id) {
			t.Errorf("CA notice of %s: no notice_sent row from the transaction of the event", org)
		}
	}
	run(t0.Add(3 * day))
	expectNone("CA, second run")

	// Outbox cleanup: published events older than 30 days go; the newest
	// of the organization and of each entity, which the cleanup keeps for
	// the numbering, are removed as well.
	publisher := events.NewPublisher(f.App, nil).WithClock(func() time.Time { return clock })
	if _, err = publisher.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	clock = t0.Add(34 * day)
	if err = publisher.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM event_outbox WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB)
	a, b := noticeRows(t, f, f.OrgA), noticeRows(t, f, f.OrgB)
	if a != 3 || b != 1 {
		t.Errorf("notice_sent rows: A %d (two collectors, CA), B %d (CA)", a, b)
	}
	run(t0.Add(34 * day))
	expectNone("after the outbox cleanup")
	if na, nb := noticeRows(t, f, f.OrgA), noticeRows(t, f, f.OrgB); na != a || nb != b {
		t.Errorf("notice_sent rows after the cleanup run: A %d, B %d", na, nb)
	}
}
