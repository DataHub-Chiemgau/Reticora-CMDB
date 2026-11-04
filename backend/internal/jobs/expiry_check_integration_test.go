package jobs_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// expiryWorld is the PostgreSQL fixture of the expiry.check tests (WP-270):
// the run with a settable clock and license switch, contracts and assets
// relative to the day of the clock, and the notice log of both event
// types.
type expiryWorld struct {
	f        *scopetest.Fixture
	check    *jobs.ExpiryCheck
	clock    time.Time
	day      time.Time // 00:00 UTC of the first day
	licensed map[string]bool
	log      *noticeLog
	seq      int
}

func newExpiryWorld(t *testing.T) *expiryWorld {
	t.Helper()
	f := scopetest.Seed(t, "94")
	day := time.Now().UTC().Truncate(24 * time.Hour)
	w := &expiryWorld{f: f, day: day, clock: day.Add(12 * time.Hour), licensed: map[string]bool{f.OrgA: true, f.OrgB: true},
		log: newNoticeLog(t, f, events.ContractExpiring, events.AssetWarrantyExpiring)}
	w.check = jobs.NewExpiryCheck(f.App).WithClock(func() time.Time { return w.clock })
	w.check.Licensed = func(_ context.Context, orgID string) (bool, error) { return w.licensed[orgID], nil }
	return w
}

func (w *expiryWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// date is the date days after the first day.
func (w *expiryWorld) date(days int) string {
	return w.day.AddDate(0, 0, days).Format(time.DateOnly)
}

// contract inserts an active maintenance contract of the organization
// ending endDays after the first day; notice nil leaves the notice period
// empty, client "" makes it organization-wide.
func (w *expiryWorld) contract(t *testing.T, org, client string, endDays int, notice *int, status string) string {
	t.Helper()
	w.seq++
	id := w.f.ID()
	var clientID any
	if client != "" {
		clientID = client
	}
	w.exec(t, `INSERT INTO contract (id, organization_id, client_id, number, kind, end_date, notice_period_days, currency, status)
		VALUES ($1, $2, $3, $4, 'maintenance', $5, $6, 'EUR', $7)`, id, org, clientID, fmt.Sprintf("WP270-%d", w.seq),
		w.date(endDays), notice, status)
	return id
}

// asset inserts an asset of the organization whose warranty ends
// warrantyDays after the first day, with the lifecycle state (empty:
// none) and the legacy status.
func (w *expiryWorld) asset(t *testing.T, org, client string, warrantyDays int, state, status string) string {
	t.Helper()
	w.seq++
	id := w.f.ID()
	var clientID, lifecycle any
	if client != "" {
		clientID = client
	}
	if state != "" {
		lifecycle = state
	}
	w.exec(t, `INSERT INTO asset (id, organization_id, client_id, inventory_no, name, warranty_until, status, lifecycle_state)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7)`, id, org, clientID, fmt.Sprintf("WP270-A%d", w.seq), w.date(warrantyDays),
		status, lifecycle)
	return id
}

// run checks the organization at the clock and returns the notices sent.
func (w *expiryWorld) run(t *testing.T, org string) int {
	t.Helper()
	n, err := w.check.RunOrg(context.Background(), org)
	if err != nil {
		t.Fatalf("run %s at %s: %v", org, w.clock, err)
	}
	return n
}

// stages returns the fresh notices of the organization as sorted
// "entity:stage" entries and the notices by entity.
func (w *expiryWorld) stages(t *testing.T, org string) (string, map[string]noticeEvent) {
	t.Helper()
	var out []string
	by := map[string]noticeEvent{}
	for _, e := range w.log.fresh(org) {
		out = append(out, fmt.Sprintf("%s:%v", e.entity, e.data["stage"]))
		by[e.entity] = e
	}
	sort.Strings(out)
	return strings.Join(out, ","), by
}

func want(pairs ...any) string {
	var out []string
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, fmt.Sprintf("%s:%d", pairs[i], pairs[i+1]))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// TestExpiryCheck covers NTF-05 [P2] and MGT-12 (WP-270) against
// PostgreSQL. The expiry.check run:
//  1. reports the contracts at 30, 7 and 1 day before their due date (end
//     date, with a notice period end date minus notice period) and the
//     warranties at 30 and 7 days, each only with the smallest stage
//     reached: a contract five days ahead gets the stage of 7 days only;
//     the payload carries the frame fields of WP-246 and the fields of the
//     contract and the asset, and the notice_sent row comes from the
//     transaction of the event;
//  2. sends nothing on a second run the same day and nothing on the next
//     day within the same stages, also after the outbox cleanup removed
//     the events (notice_sent deduplicates);
//  3. reports a moved due date again and catches a missed stage up after
//     a pause;
//  4. leaves an asset in lifecycle state disposed or retired, an asset
//     without state with the legacy status lost and a terminated or ended
//     contract silent;
//  8. leaves an organization without assets_basic (Licensed false) silent;
//  9. keeps the organizations apart, and more than 500 objects are
//     reported in several batches.
//
// Case 5 is TestExpiryCheckTimeZones, cases 6 and 7
// TestExpiryCheckNotifies.
func TestExpiryCheck(t *testing.T) {
	w := newExpiryWorld(t)
	f := w.f
	notice := func(days int) *int { return &days }
	supplier := f.ID()
	w.exec(t, `INSERT INTO supplier (id, organization_id, name) VALUES ($1, $2, 'WP270 supplier')`, supplier, f.OrgA)
	owner := f.AppUser(t, f.OrgA, "expiry-owner")
	team := f.ID()
	w.exec(t, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp270 team')`, team, f.OrgA)
	ci := f.CI(t, f.OrgA, f.Client1, "wp270-ci")

	// (1) Stages.
	c30 := w.contract(t, f.OrgA, "", 30, nil, "active")
	c30n := w.contract(t, f.OrgA, f.Client1, 40, notice(10), "active")
	w.exec(t, `UPDATE contract SET supplier_id = $2 WHERE id = $1`, c30n, supplier)
	c7 := w.contract(t, f.OrgA, "", 5, nil, "active")
	c1 := w.contract(t, f.OrgA, "", 1, notice(0), "draft")
	c0 := w.contract(t, f.OrgA, "", 0, nil, "active")
	c31 := w.contract(t, f.OrgA, "", 31, nil, "active")
	w.contract(t, f.OrgA, "", -1, nil, "active")
	c60 := w.contract(t, f.OrgA, "", 60, notice(29), "active")
	a30 := w.asset(t, f.OrgA, f.Client1, 30, "", "in_stock")
	a7 := w.asset(t, f.OrgA, f.Client1, 3, "deployed", "assigned")
	w.exec(t, `UPDATE asset SET owner_user_id = $2, owner_team_id = $3, ci_id = $4 WHERE id = $1`, a7, owner, team, ci)
	a1 := w.asset(t, f.OrgA, "", 1, "in_stock", "in_stock")
	a31 := w.asset(t, f.OrgA, "", 31, "", "in_stock")
	// (4) End states stay silent.
	w.asset(t, f.OrgA, "", 5, "disposed", "disposed")
	w.asset(t, f.OrgA, "", 5, "retired", "retired")
	w.asset(t, f.OrgA, "", 5, "", "lost")
	w.contract(t, f.OrgA, "", 5, nil, "terminated")
	w.contract(t, f.OrgA, "", 5, nil, "ended")

	if n := w.run(t, f.OrgA); n != 8 {
		t.Errorf("first run sent %d notices, want 8", n)
	}
	got, by := w.stages(t, f.OrgA)
	if exp := want(c30, 30, c30n, 30, c7, 7, c1, 1, c0, 1, a30, 30, a7, 7, a1, 7); got != exp {
		t.Fatalf("first run: %s, want %s", got, exp)
	}
	k := by[c30n]
	if k.typ != events.ContractExpiring || k.data["id"] != c30n || k.data["client_id"] != f.Client1 || k.data["kind"] != "maintenance" ||
		k.data["supplier_id"] != supplier || k.data["notice_period_days"] != float64(10) || k.data["due_date"] != w.date(30) ||
		k.data["expires_at"] != w.date(40) || k.data["days_left"] != float64(30) || k.data["number"] == "" {
		t.Errorf("contract data %v", k.data)
	}
	if !sameTransaction(t, f, f.OrgA, fmt.Sprintf("%s/30/%s", c30n, w.date(30)), k.id) {
		t.Error("no notice_sent row of the contract from the transaction of its event")
	}
	if d := by[c30].data; d["notice_period_days"] != float64(0) || d["due_date"] != d["expires_at"] {
		t.Errorf("contract without notice period: %v", d)
	}
	if _, present := by[c30].data["client_id"]; present {
		t.Errorf("organization-wide contract with client_id: %v", by[c30].data)
	}
	if d := by[c7].data; d["days_left"] != float64(5) {
		t.Errorf("contract five days ahead: %v", d)
	}
	a := by[a7]
	if a.typ != events.AssetWarrantyExpiring || a.data["id"] != a7 || a.data["client_id"] != f.Client1 || a.data["name"] == "" ||
		a.data["inventory_no"] != a.data["name"] || a.data["owner_user_id"] != owner || a.data["owner_team_id"] != team ||
		a.data["primary_ci_id"] != ci || a.data["due_date"] != w.date(3) || a.data["expires_at"] != w.date(3) ||
		a.data["days_left"] != float64(3) {
		t.Errorf("asset data %v", a.data)
	}
	for _, member := range []string{"owner_user_id", "owner_team_id", "primary_ci_id", "client_id"} {
		if _, present := by[a1].data[member]; present {
			t.Errorf("asset without %s carries it: %v", member, by[a1].data)
		}
	}

	// (2) The same day again: nothing. The next day only the objects that
	// just reached their first stage (31 days ahead the day before),
	// nothing for those staying in their stage; after the outbox cleanup
	// nothing again.
	if n := w.run(t, f.OrgA); n != 0 {
		t.Errorf("second run the same day sent %d", n)
	}
	w.clock = w.clock.Add(24 * time.Hour)
	if n := w.run(t, f.OrgA); n != 3 {
		t.Errorf("next day sent %d, want 3", n)
	}
	if got, _ = w.stages(t, f.OrgA); got != want(c31, 30, c60, 30, a31, 30) {
		t.Errorf("next day: %s", got)
	}
	w.exec(t, `DELETE FROM event_outbox WHERE organization_id = $1`, f.OrgA)
	if n := w.run(t, f.OrgA); n != 0 {
		t.Errorf("run after the outbox cleanup sent %d", n)
	}
	if got, _ = w.stages(t, f.OrgA); got != "" {
		t.Errorf("events without a new stage: %s", got)
	}

	// (3) A moved due date is reported again; after a pause of 24 days
	// the missed stage of 7 days is caught up once.
	w.exec(t, `UPDATE contract SET end_date = $2 WHERE id = $1`, c7, w.date(6))
	if n := w.run(t, f.OrgA); n != 1 {
		t.Errorf("run after the moved end date sent %d", n)
	}
	got, by = w.stages(t, f.OrgA)
	if got != want(c7, 7) || by[c7].data["expires_at"] != w.date(6) {
		t.Errorf("moved end date: %s %v", got, by[c7].data)
	}
	w.clock = w.day.AddDate(0, 0, 25).Add(12 * time.Hour)
	if n := w.run(t, f.OrgA); n != 6 {
		t.Errorf("run after the pause sent %d", n)
	}
	if got, _ = w.stages(t, f.OrgA); got != want(c30, 7, c30n, 7, c31, 7, c60, 7, a30, 7, a31, 7) {
		t.Errorf("after the pause: %s", got)
	}
	if n := w.run(t, f.OrgA); n != 0 {
		t.Errorf("second run after the pause sent %d", n)
	}

	// (8) Organization B without assets_basic stays silent; (9) its run
	// does not touch A, and 501 contracts take two batches.
	w.clock = w.day.Add(12 * time.Hour)
	b := w.contract(t, f.OrgB, "", 5, nil, "active")
	w.exec(t, `INSERT INTO contract (organization_id, number, end_date, currency)
		SELECT $1, 'WP270-bulk-' || g, $2::date, 'EUR' FROM generate_series(1, 501) g`, f.OrgB, w.date(20))
	w.licensed[f.OrgB] = false
	if n := w.run(t, f.OrgB); n != 0 {
		t.Errorf("unlicensed organization sent %d", n)
	}
	if got, _ = w.stages(t, f.OrgB); got != "" {
		t.Errorf("unlicensed organization: %s", got)
	}
	w.licensed[f.OrgB] = true
	if n := w.run(t, f.OrgB); n != 502 {
		t.Errorf("organization B sent %d, want 502", n)
	}
	gotB := w.log.fresh(f.OrgB)
	entities := map[string]bool{}
	for _, e := range gotB {
		entities[e.entity] = true
	}
	if len(gotB) != 502 || len(entities) != 502 || !entities[b] {
		t.Errorf("organization B: %d events of %d contracts", len(gotB), len(entities))
	}
	if got, _ = w.stages(t, f.OrgA); got != "" {
		t.Errorf("B's run wrote events of A: %s", got)
	}
	var foreign int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM notice_sent n
		WHERE n.kind = ANY($3) AND ((n.organization_id = $1 AND EXISTS (SELECT 1 FROM contract k WHERE k.organization_id = $2 AND n.dedup_key LIKE k.id::text || '/%'))
		   OR (n.organization_id = $2 AND EXISTS (SELECT 1 FROM contract k WHERE k.organization_id = $1 AND n.dedup_key LIKE k.id::text || '/%')))`,
		f.OrgA, f.OrgB, []string{events.ContractExpiring, events.AssetWarrantyExpiring}).Scan(&foreign); err != nil || foreign != 0 {
		t.Errorf("notice_sent rows of the other organization's contracts: %d (%v)", foreign, err)
	}
}

// TestExpiryCheckTimeZones covers case 5 of WP-270 (LOC-01, LOC-02): at
// 12:00 UTC it is already the next day in Pacific/Auckland, so a contract
// and a warranty of an organization in Pacific/Auckland reach their stage
// one day earlier than those of a UTC organization; the time zone of the
// client goes before that of the organization.
func TestExpiryCheckTimeZones(t *testing.T) {
	w := newExpiryWorld(t)
	f := w.f
	w.exec(t, `UPDATE organization SET timezone = 'Pacific/Auckland' WHERE id = $1`, f.OrgB)
	w.exec(t, `UPDATE client SET timezone = 'Pacific/Auckland' WHERE id = $1`, f.Client2)
	utcClient := f.ID()
	w.exec(t, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'WP270 UTC', 'scopetest-94-utc')`, utcClient, f.OrgB)
	w.exec(t, `UPDATE client SET timezone = 'UTC' WHERE id = $1`, utcClient)

	// 31 days ahead in UTC is 30 days ahead in Auckland.
	utcContract := w.contract(t, f.OrgA, f.Client1, 31, nil, "active")
	utcAsset := w.asset(t, f.OrgA, "", 31, "", "in_stock")
	clientContract := w.contract(t, f.OrgA, f.Client2, 31, nil, "active")
	nzContract := w.contract(t, f.OrgB, "", 31, nil, "active")
	nzAsset := w.asset(t, f.OrgB, "", 31, "", "in_stock")
	backToUTC := w.contract(t, f.OrgB, utcClient, 31, nil, "active")

	w.run(t, f.OrgA)
	w.run(t, f.OrgB)
	gotA, byA := w.stages(t, f.OrgA)
	if gotA != want(clientContract, 30) || byA[clientContract].data["days_left"] != float64(30) {
		t.Errorf("organization A (UTC, client 2 in Auckland): %s", gotA)
	}
	gotB, byB := w.stages(t, f.OrgB)
	if gotB != want(nzContract, 30, nzAsset, 30) || byB[nzAsset].data["days_left"] != float64(30) {
		t.Errorf("organization B (Auckland, one client in UTC): %s", gotB)
	}
	// A day later the UTC objects follow; the others stay in their stage.
	w.clock = w.clock.Add(24 * time.Hour)
	w.run(t, f.OrgA)
	w.run(t, f.OrgB)
	if got, _ := w.stages(t, f.OrgA); got != want(utcContract, 30, utcAsset, 30) {
		t.Errorf("organization A the next day: %s", got)
	}
	if got, _ := w.stages(t, f.OrgB); got != want(backToUTC, 30) {
		t.Errorf("organization B the next day: %s", got)
	}
}

// TestExpiryCheckNotifies covers cases 6 and 7 of WP-270: through the
// outbox, events.Publisher, notify.EventConsumer and the notifier with a
// recording mail provider, contract.expiring reaches the org_admins with
// contract:read and asset.warranty_expiring the owning user, the members of
// the owning team and the org_admins with asset:read in the asset's client
// and the owner contact of the primary CI; a contract reader who is no
// org_admin, an owner without asset:read and an asset reader who is
// neither owner nor org_admin get nothing. The muted category warranty
// suppresses the delivery to the owner; the CI contact still gets the
// e-mail.
func TestExpiryCheckNotifies(t *testing.T) {
	w := newExpiryWorld(t)
	f := w.f
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err = center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	mail := &expiryMail{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox)}))
	publisher := events.NewPublisher(f.App, nil, notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier))

	var orgAdminRole string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA).Scan(&orgAdminRole); err != nil {
		t.Fatalf("org_admin role: %v", err)
	}
	assetReader, contractReader := f.ID(), f.ID()
	w.exec(t, `INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp270-assets', 'org', '["asset:read"]'::jsonb)`, assetReader, f.OrgA)
	w.exec(t, `INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp270-contracts', 'org', '["contract:read"]'::jsonb)`, contractReader, f.OrgA)
	assign := func(user, role string) {
		t.Helper()
		w.exec(t, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, user, role)
	}
	admin := f.AppUser(t, f.OrgA, "expiry-admin")
	owner := f.AppUser(t, f.OrgA, "expiry-owner")
	member := f.AppUser(t, f.OrgA, "expiry-member")
	blindOwner := f.AppUser(t, f.OrgA, "expiry-blind")
	reader := f.AppUser(t, f.OrgA, "expiry-reader")
	contracts := f.AppUser(t, f.OrgA, "expiry-contracts")
	assign(admin, orgAdminRole)
	assign(owner, assetReader)
	assign(member, assetReader)
	assign(reader, assetReader)
	assign(contracts, contractReader)
	team := f.ID()
	w.exec(t, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp270 owners')`, team, f.OrgA)
	w.exec(t, `INSERT INTO team_member (organization_id, team_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, team, member)
	ci := f.CI(t, f.OrgA, f.Client1, "wp270-notify-ci")
	contact := f.ID()
	w.exec(t, `INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, 'CI Owner', 'ci-owner@acme.example')`,
		contact, f.OrgA, f.Client1)
	w.exec(t, `INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, 'owner', $4)`,
		f.OrgA, ci, contact, f.Client1)

	k := w.contract(t, f.OrgA, f.Client1, 7, nil, "active")
	a := w.asset(t, f.OrgA, f.Client1, 30, "", "in_stock")
	w.exec(t, `UPDATE asset SET owner_user_id = $2, owner_team_id = $3, ci_id = $4 WHERE id = $1`, a, owner, team, ci)
	blind := w.asset(t, f.OrgA, f.Client1, 7, "", "in_stock")
	w.exec(t, `UPDATE asset SET owner_user_id = $2 WHERE id = $1`, blind, blindOwner)

	deliver := func() {
		t.Helper()
		if _, perr := publisher.PublishPending(bg); perr != nil {
			t.Fatal(perr)
		}
		for range 3 {
			if _, perr := notifier.ProcessDue(bg); perr != nil {
				t.Fatal(perr)
			}
		}
	}
	if n := w.run(t, f.OrgA); n != 3 {
		t.Fatalf("run sent %d notices, want 3", n)
	}
	deliver()

	mailOf := func(entity string) string { return mail.recipients(entity) }
	inbox := func(user string) []string {
		t.Helper()
		items, _, lerr := center.Inbox.List(bg, f.OrgA, user, false, 20, 0)
		if lerr != nil {
			t.Fatal(lerr)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Category)
		}
		sort.Strings(out)
		return out
	}
	email := func(name string) string { return "scopetest-94-" + name + "@scopetest.invalid" }
	if got, exp := mailOf(k), email("expiry-admin"); got != exp {
		t.Errorf("contract.expiring mail to %s, want %s", got, exp)
	}
	exp := []string{"ci-owner@acme.example", email("expiry-admin"), email("expiry-member"), email("expiry-owner")}
	sort.Strings(exp)
	if got := mailOf(a); got != strings.Join(exp, ",") {
		t.Errorf("asset.warranty_expiring mail to %s, want %s", got, strings.Join(exp, ","))
	}
	if got := mailOf(blind); got != email("expiry-admin") {
		t.Errorf("warranty of the asset whose owner lacks asset:read: mail to %s, want the org_admin only", got)
	}
	for user, categories := range map[string][]string{
		admin: {"contract", "warranty", "warranty"}, owner: {"warranty"}, member: {"warranty"},
		blindOwner: nil, reader: nil, contracts: nil,
	} {
		if got := inbox(user); !slices.Equal(got, categories) {
			t.Errorf("in-app of %s: %v, want %v", user, got, categories)
		}
	}

	// (7) The owner mutes warranty: the next stage reaches the CI contact,
	// the team member and the org_admin, not the owner.
	if err = center.Preferences.SetPreference(bg, f.OrgA, owner, &notify.Preference{MutedCategories: []string{"warranty"}}); err != nil {
		t.Fatal(err)
	}
	mail.reset()
	w.clock = w.day.AddDate(0, 0, 25).Add(12 * time.Hour)
	if n := w.run(t, f.OrgA); n != 1 {
		t.Fatalf("run at stage 7 sent %d notices, want 1", n)
	}
	deliver()
	exp = []string{"ci-owner@acme.example", email("expiry-admin"), email("expiry-member")}
	sort.Strings(exp)
	if got := mailOf(a); got != strings.Join(exp, ",") {
		t.Errorf("muted warranty: mail to %s, want %s", got, strings.Join(exp, ","))
	}
	if got := inbox(owner); !slices.Equal(got, []string{"warranty"}) {
		t.Errorf("in-app of the muting owner: %v, want only the earlier notice", got)
	}
}

// expiryMail records the e-mails of the expiry notices.
type expiryMail struct{ sent []notify.Message }

func (m *expiryMail) Send(_ context.Context, _ *notify.Channel, msg *notify.Message) (*notify.Delivery, error) {
	m.sent = append(m.sent, *msg)
	return &notify.Delivery{ProviderID: "wp270", Recipients: len(msg.To)}, nil
}

func (m *expiryMail) reset() { m.sent = nil }

// recipients returns the sorted addresses of the e-mails about the entity
// (the id of the event data).
func (m *expiryMail) recipients(entity string) string {
	var out []string
	for _, msg := range m.sent {
		if msg.Data["id"] != entity {
			continue
		}
		for _, r := range msg.To {
			out = append(out, r.Email)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
