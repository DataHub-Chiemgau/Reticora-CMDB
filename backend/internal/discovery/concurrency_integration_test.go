package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestDiscoveryConcurrency covers the discovery part of the concurrency
// matrix of ABN-03 (WP-207) with real parallel transactions against
// PostgreSQL:
//   - double ingest: two server instances (each with its own handler and
//     per-process serialization) ingest the same new device at once; the
//     organization ends with exactly one CI for it;
//   - discovery during edit: a manual change and an ingest of the same field
//     run at once; the manual value stays, protected by its override, in
//     every round (the CI repository drops overridden fields of an
//     automated write under the row lock);
//   - rollback of a record (WP-332): with the write path of the server the
//     record runs in one transaction; a failure after its review, CI, audit
//     entry, event, search document, provenance and interfaces were written
//     leaves none of them and notifies nothing, and the redelivered record
//     creates the CI once and announces its review after the commit;
//   - double batch (WP-333): two instances create the same five new devices
//     in a batch each at once; every device ends with exactly one CI;
//   - rollback of a batch (WP-333): a failure in the batch transaction of a
//     window, after its CIs, provenance and a review were written, leaves
//     none of them and announces nothing; the records follow one by one;
//   - double ingest across lanes (WP-334, E-56): one device whose records
//     carry different strongest keys is ingested at once in two lanes of
//     one instance (serial only and serial plus UUID; sweep with MAC and IP
//     and SNMP with serial, MAC, IP and sysObjectID; IP plus sysObjectID
//     with and without serial); the identity locks serialize the two, so
//     the device ends with exactly one CI, counted per serial, MAC and IP;
//   - two lanes update one CI at once with disjoint new attributes: both
//     succeed, without a deadlock reaching the queue, and every attribute
//     is stored;
//   - the integrity check finds no orphans, duplicates or cycles afterwards.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryConcurrency(t *testing.T) {
	f := scopetest.Seed(t, "c8")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "abn03-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})

	instance := func() *chi.Mux {
		mux := chi.NewRouter()
		discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).
			WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).RegisterRoutes(mux)
		return mux
	}
	instances := []*chi.Mux{instance(), instance()}
	ingest := func(mux *chi.Mux, recs ...plugins.DeviceRecord) int {
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: recs})
		if err != nil {
			t.Error(err)
			return 0
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		return w.Code
	}
	device := func(serial, model string) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, IP: "192.0.2.207",
			Manufacturer: "Reticora Labs", Model: model, Serial: serial}, time.Now().Add(-time.Second))
	}
	count := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`,
			f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	together := func(fns ...func()) {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, fn := range fns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				fn()
			}()
		}
		close(start)
		wg.Wait()
	}

	for round := 0; round < 5; round++ {
		serial := fmt.Sprintf("ABN03-DOUBLE-%d", round)
		codes := make([]int, len(instances))
		together(
			func() { codes[0] = ingest(instances[0], device(serial, "M1")) },
			func() { codes[1] = ingest(instances[1], device(serial, "M1")) },
		)
		// A refused write (the other instance won the unique serial) is
		// redelivered by the queue; the retry matches the new CI.
		for i, code := range codes {
			if code != http.StatusAccepted {
				if retry := ingest(instances[i], device(serial, "M1")); retry != http.StatusAccepted {
					t.Errorf("round %d: instance %d: %d, retry %d", round, i, code, retry)
				}
			}
		}
		if n := count(serial); n != 1 {
			t.Errorf("round %d: double ingest left %d CIs for one device, want 1", round, n)
		}
	}

	editor := ci.NewService(ci.NewPGRepository(f.App))
	for round := 0; round < 20; round++ {
		serial := fmt.Sprintf("ABN03-EDIT-%d", round)
		if code := ingest(instances[0], device(serial, "M0")); code != http.StatusAccepted {
			t.Fatalf("round %d: first ingest: %d", round, code)
		}
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&id); err != nil {
			t.Fatal(err)
		}
		manual := "manual-model"
		var editErr error
		together(
			func() {
				_, editErr = editor.Update(ctx, f.OrgA, id, ci.UpdateRequest{Model: &manual, Manual: &ci.ManualChange{Author: user},
					ChangeReason: "abn03"})
			},
			func() { _ = ingest(instances[1], device(serial, "auto-model")) },
		)
		if editErr != nil {
			t.Fatalf("round %d: manual change: %v", round, editErr)
		}
		// A later ingest must not overwrite the manual value either.
		_ = ingest(instances[0], device(serial, "auto-model-2"))
		var model string
		var protected bool
		if err := f.Admin.QueryRow(bg, `SELECT c.model, COALESCE(v.protected, false) FROM ci c
			LEFT JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = 'model' AND v.override_at IS NOT NULL
			WHERE c.id = $1`, id).Scan(&model, &protected); err != nil {
			t.Fatal(err)
		}
		if model != manual || !protected {
			t.Errorf("round %d: model %q (override protected %v), want the manual value", round, model, protected)
		}
	}

	// Rollback: the interfaces are synchronized last; the first sync of
	// each round fails after its writes. The devices are unclassified, so
	// each record opens a review, announced through the event outbox as on
	// the server.
	syncer := &failOnce{next: ipam.NewPGRepository(f.App)}
	notifier := &outboxNotifier{emitter: events.NewPGEmitter(f.App)}
	full := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App),
		ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()),
			search.NewCIIndexer(search.NewPGRepository(f.App)))).
		WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(f.App)}}).
		WithInterfaces(syncer).WithReviewNotifier(notifier).RegisterRoutes(full)
	rows := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		for table, query := range map[string]string{
			"review_item":       `SELECT count(*) FROM review_item WHERE organization_id = $1`,
			"ci":                `SELECT count(*) FROM ci WHERE organization_id = $1`,
			"audit_log":         `SELECT count(*) FROM audit_log WHERE organization_id = $1`,
			"event_outbox":      `SELECT count(*) FROM event_outbox WHERE organization_id = $1`,
			"search_document":   `SELECT count(*) FROM search_document WHERE organization_id = $1`,
			"ci_field_value":    `SELECT count(*) FROM ci_field_value WHERE organization_id = $1`,
			"network_interface": `SELECT count(*) FROM network_interface WHERE organization_id = $1`,
			"ingest_record":     `SELECT count(*) FROM ingest_record WHERE organization_id = $1`,
		} {
			var n int
			if err := f.Admin.QueryRow(bg, query, f.OrgA).Scan(&n); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			out[table] = n
		}
		return out
	}
	for round := 0; round < 3; round++ {
		serial := fmt.Sprintf("ABN03-ROLLBACK-%d", round)
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "generic_device", Name: serial, IP: fmt.Sprintf("192.0.2.%d", 100+round),
			Manufacturer: "Reticora Labs", Model: "R1", Serial: serial}, time.Now().Add(-time.Second))
		rec.Attributes = map[string]any{"classified_by": "fallback"}
		rec.Interfaces = []plugins.DeviceInterface{{Name: "eth0", MAC: fmt.Sprintf("00:1b:21:aa:01:%02x", round),
			IPs: []plugins.DeviceIP{{Address: fmt.Sprintf("192.0.2.%d", 100+round), PrefixLength: 24}}}}
		syncer.arm()
		before := rows()
		if code := ingest(full, rec); code == http.StatusAccepted || syncer.armed.Load() {
			t.Fatalf("round %d: ingest with a failing interface sync: %d", round, code)
		}
		if after := rows(); !maps.Equal(after, before) {
			t.Errorf("round %d: the failed record left rows behind: before %v, after %v", round, before, after)
		}
		if notified, inTx, errs := notifier.state(); notified != round || inTx != 0 || len(errs) > 0 {
			t.Errorf("round %d: %d reviews announced after the failed record (%d inside a transaction, errors %v), want %d",
				round, notified, inTx, errs, round)
		}
		// The queue delivers the record again: the CI is created once.
		if code := ingest(full, rec); code != http.StatusAccepted {
			t.Fatalf("round %d: redelivery: %d", round, code)
		}
		var id string
		var created, audited, interfaces int
		if err := f.Admin.QueryRow(bg, `SELECT c.id::text,
				(SELECT count(*) FROM event_outbox e WHERE e.organization_id = c.organization_id AND e.entity_id::text = c.id::text AND e.type = 'ci.created'),
				(SELECT count(*) FROM audit_log a WHERE a.organization_id = c.organization_id AND a.resource_id = c.id AND a.action = 'ci.created'),
				(SELECT count(*) FROM network_interface n WHERE n.ci_id = c.id)
			FROM ci c WHERE c.organization_id = $1 AND c.serial_number = $2`, f.OrgA, serial).Scan(&id, &created, &audited, &interfaces); err != nil {
			t.Fatalf("round %d: CI after the redelivery: %v", round, err)
		}
		if n := count(serial); n != 1 || created != 1 || audited != 1 || interfaces != 1 {
			t.Errorf("round %d: %d CIs, %d ci.created events, %d audit entries, %d interfaces, want 1 each", round, n, created, audited, interfaces)
		}
		var announced int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item r JOIN event_outbox e
				ON e.organization_id = r.organization_id AND e.entity_id::text = r.id::text AND e.type = 'review.created'
			WHERE r.organization_id = $1 AND r.kind = 'unclassified_device' AND r.payload->>'serial_number' = $2`, f.OrgA, serial).Scan(&announced); err != nil {
			t.Fatal(err)
		}
		if notified, inTx, errs := notifier.state(); notified != round+1 || inTx != 0 || len(errs) > 0 || announced != 1 {
			t.Errorf("round %d: %d reviews announced (%d inside a transaction, errors %v, %d review.created events), want %d after the commit",
				round, notified, inTx, errs, announced, round+1)
		}
	}

	// Double batch (WP-333): two instances ingest the same window of five
	// new devices at once, each with one batch transaction for them. The
	// unique indexes refuse the devices of the slower batch, which follow
	// one by one and match the CIs of the faster one.
	for round := 0; round < 5; round++ {
		serials := make([]string, 5)
		for i := range serials {
			serials[i] = fmt.Sprintf("ABN03-BATCH-%d-%d", round, i)
		}
		// Each instance gets records of its own (own record ids).
		window := func() []plugins.DeviceRecord {
			recs := make([]plugins.DeviceRecord, len(serials))
			for i := range recs {
				recs[i] = plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serials[i],
					IP: fmt.Sprintf("198.51.100.%d", 1+round*len(serials)+i), Manufacturer: "Reticora Labs", Model: "M1",
					Serial: serials[i]}, time.Now().Add(-time.Second))
			}
			return recs
		}
		codes := make([]int, len(instances))
		together(
			func() { codes[0] = ingest(instances[0], window()...) },
			func() { codes[1] = ingest(instances[1], window()...) },
		)
		for i, code := range codes {
			if code != http.StatusAccepted {
				if retry := ingest(instances[i], window()...); retry != http.StatusAccepted {
					t.Errorf("batch round %d: instance %d: %d, retry %d", round, i, code, retry)
				}
			}
		}
		for _, serial := range serials {
			if n := count(serial); n != 1 {
				t.Errorf("batch round %d: double ingest left %d CIs for %s, want 1", round, n, serial)
			}
		}
	}

	// Rollback of a batch (WP-333): the batch transaction of a window fails
	// after it created the CIs, wrote their provenance and opened a review
	// (probable duplicate by hostname, CH29). Nothing of it stays and the
	// review is not announced; the records follow one by one, create each
	// CI once and announce the review after their commit.
	host := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "ABN03-BROLL-HOST", IP: "198.51.100.200",
		Manufacturer: "Reticora Labs", Model: "M1", Serial: "ABN03-BROLL-HOST"}, time.Now().Add(-time.Second))
	host.Identity.Hostname = "abn03-batch-host"
	if code := ingest(full, host); code != http.StatusAccepted {
		t.Fatalf("batch rollback: first device: %d", code)
	}
	duplicate := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "ABN03-BROLL-0", IP: "198.51.100.201",
		Manufacturer: "Reticora Labs", Model: "M1", Serial: "ABN03-BROLL-0"}, time.Now().Add(-time.Second))
	duplicate.Identity.Hostname = "abn03-batch-host"
	failing := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "ABN03-BROLL-1", IP: "198.51.100.202",
		Manufacturer: "Reticora Labs", Model: "M1", Serial: "ABN03-BROLL-1"}, time.Now().Add(-time.Second))
	failing.Interfaces = []plugins.DeviceInterface{{Name: "eth0", MAC: "00:1b:21:aa:02:01",
		IPs: []plugins.DeviceIP{{Address: "198.51.100.202", PrefixLength: 24}}}}
	notifiedBefore, _, _ := notifier.state()
	syncer.arm()
	if code := ingest(full, duplicate, failing); code != http.StatusAccepted || syncer.armed.Load() {
		t.Fatalf("batch rollback: ingest %d, failure injected %v; want 202 after the records one by one", code, !syncer.armed.Load())
	}
	for _, serial := range []string{"ABN03-BROLL-0", "ABN03-BROLL-1"} {
		var created, audited, interfaces, reviews, announced int
		if err := f.Admin.QueryRow(bg, `SELECT
				(SELECT count(*) FROM event_outbox e WHERE e.organization_id = c.organization_id AND e.entity_id = c.id::text AND e.type = 'ci.created'),
				(SELECT count(*) FROM audit_log a WHERE a.organization_id = c.organization_id AND a.resource_id = c.id AND a.action = 'ci.created'),
				(SELECT count(*) FROM network_interface n WHERE n.ci_id = c.id),
				(SELECT count(*) FROM review_item r WHERE r.organization_id = c.organization_id AND r.payload->>'ci_id' = c.id::text),
				(SELECT count(*) FROM review_item r JOIN event_outbox e ON e.organization_id = r.organization_id
					AND e.entity_id = r.id::text AND e.type = 'review.created'
					WHERE r.organization_id = c.organization_id AND r.payload->>'ci_id' = c.id::text)
			FROM ci c WHERE c.organization_id = $1 AND c.serial_number = $2`, f.OrgA, serial).
			Scan(&created, &audited, &interfaces, &reviews, &announced); err != nil {
			t.Fatalf("batch rollback: CI %s: %v", serial, err)
		}
		wantInterfaces, wantReviews := 0, 1
		if serial == "ABN03-BROLL-1" {
			wantInterfaces, wantReviews = 1, 0
		}
		if n := count(serial); n != 1 || created != 1 || audited != 1 || interfaces != wantInterfaces ||
			reviews != wantReviews || announced != wantReviews {
			t.Errorf("batch rollback: %s: %d CIs, %d ci.created events, %d audit entries, %d interfaces, %d reviews announced %d times; "+
				"want 1, 1, 1, %d, %d, %d", serial, n, created, audited, interfaces, reviews, announced, wantInterfaces, wantReviews, wantReviews)
		}
	}
	if notified, inTx, errs := notifier.state(); notified != notifiedBefore+1 || inTx != 0 || len(errs) > 0 {
		t.Errorf("batch rollback: %d reviews announced (%d inside a transaction, errors %v), want 1 after the commit",
			notified-notifiedBefore, inTx, errs)
	}

	// Double ingest across lanes (WP-334): each pair of records of one new
	// device runs in two lanes at once; both decide only after the other
	// had its chance (a barrier before the creation, which the identity
	// lock keeps the second one from reaching).
	laned := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).
		WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).WithIngestLanes(4)
	pair := &pairBarrier{}
	discovery.SetBeforeCreate(laned, func(context.Context) { pair.wait(300 * time.Millisecond) })
	lanesCtx := ctx
	countBy := func(column, value string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND `+column+` = $2`,
			f.OrgA, value).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	seen := time.Now().Add(-time.Minute)
	for round := 0; round < 3; round++ {
		serial := fmt.Sprintf("ABN03-LANES-%d", round)
		ip := fmt.Sprintf("203.0.113.%d", 10+round)
		mac := fmt.Sprintf("00:1b:21:bb:00:%02x", round)
		uuid := fmt.Sprintf("4c4c4544-0000-4000-8000-0000000000%02x", round)
		snmp := func(s, ip, sysObjectID string) plugins.DeviceRecord {
			rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "lanes-" + ip, IP: ip,
				Manufacturer: "Reticora Labs", Model: "M1", Serial: s}, seen)
			rec.Classification.SysObjectID = sysObjectID
			return rec
		}
		withUUID := snmp(serial, ip, "")
		withUUID.Source = "ssh"
		withUUID.Identity.HardwareUUID = uuid
		sweep := plugins.RecordFromResult("sweep", &plugins.Result{CIType: "server", Name: "lanes-sweep", IP: ip + "1"}, seen)
		sweep.Identity.PrimaryMAC = mac
		withMAC := snmp(serial+"-M", ip+"1", "1.3.6.1.4.1.9.1.1")
		withMAC.Identity.PrimaryMAC = mac
		for _, c := range []struct {
			name       string
			a, b       plugins.DeviceRecord
			column, by string
		}{
			{"serial only vs serial and UUID", snmp(serial, ip, ""), withUUID, "serial_number", serial},
			{"sweep vs SNMP", sweep, withMAC, "primary_mac", mac},
			{"IP and sysObjectID vs serial, IP and sysObjectID", snmp("", ip+"2", "1.3.6.1.4.1.9.1.2"),
				snmp(serial+"-I", ip+"2", "1.3.6.1.4.1.9.1.2"), "management_ip", ip + "2"},
		} {
			errs := make([]error, 2)
			together(
				func() { _, errs[0] = discovery.ProcessInLane(lanesCtx, laned, f.OrgA, 0, c.a) },
				func() { _, errs[1] = discovery.ProcessInLane(lanesCtx, laned, f.OrgA, 1, c.b) },
			)
			if errs[0] != nil || errs[1] != nil {
				t.Errorf("lanes round %d, %s: %v", round, c.name, errs)
			}
			if n := countBy(c.column, c.by); n != 1 {
				t.Errorf("lanes round %d, %s: %d CIs with %s %s, want 1", round, c.name, n, c.column, c.by)
			}
		}
	}
	if met := pair.met.Load(); met != 0 {
		t.Errorf("two records of one device decided to create a CI at the same time %d times", met)
	}
	if n := countBy("hardware_uuid", "4c4c4544-0000-4000-8000-000000000000"); n != 1 {
		t.Errorf("CI with the reported UUID: %d, want 1", n)
	}

	// Two lanes update one CI at once with disjoint new attributes.
	discovery.SetBeforeCreate(laned, nil)
	for round := 0; round < 10; round++ {
		serial := fmt.Sprintf("ABN03-LANES-UPD-%d", round)
		base := device(serial, "M1")
		if _, err := discovery.ProcessInLane(lanesCtx, laned, f.OrgA, 0, base); err != nil {
			t.Fatal(err)
		}
		update := func(prefix string) plugins.DeviceRecord {
			rec := device(serial, "M1")
			rec.Attributes = map[string]any{}
			for i := 0; i < 5; i++ {
				rec.Attributes[fmt.Sprintf("%s_%d", prefix, i)] = fmt.Sprintf("%s-%d", prefix, round)
			}
			return rec
		}
		errs := make([]error, 2)
		together(
			func() { _, errs[0] = discovery.ProcessInLane(lanesCtx, laned, f.OrgA, 2, update("lane_a")) },
			func() { _, errs[1] = discovery.ProcessInLane(lanesCtx, laned, f.OrgA, 3, update("lane_b")) },
		)
		if errs[0] != nil || errs[1] != nil {
			t.Errorf("update round %d: %v (a failed record would go back to the queue)", round, errs)
		}
		var attrs int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci c, jsonb_object_keys(c.attributes) k
			WHERE c.organization_id = $1 AND c.serial_number = $2 AND (k LIKE 'lane_a_%' OR k LIKE 'lane_b_%')`, f.OrgA, serial).Scan(&attrs); err != nil {
			t.Fatal(err)
		}
		if attrs != 10 || count(serial) != 1 {
			t.Errorf("update round %d: %d of 10 attributes, %d CIs", round, attrs, count(serial))
		}
	}

	if v := integrityViolations(t, f, f.OrgA); len(v) > 0 {
		t.Errorf("integrity violations after the concurrency matrix: %v", v)
	}
}

// pairBarrier lets two parties through together, or each alone after a
// timeout; met counts the times both arrived together.
type pairBarrier struct {
	mu      sync.Mutex
	waiting chan struct{}
	met     atomic.Int32
}

func (b *pairBarrier) wait(timeout time.Duration) {
	b.mu.Lock()
	if b.waiting != nil {
		close(b.waiting)
		b.waiting = nil
		b.met.Add(1)
		b.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	b.waiting = ch
	b.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(timeout):
		b.mu.Lock()
		if b.waiting == ch {
			b.waiting = nil
		}
		b.mu.Unlock()
	}
}

// outboxNotifier announces opened review items in the event outbox like the
// server's notifier (a transaction of its own, org-wide scope).
type outboxNotifier struct {
	emitter  *events.PGEmitter
	mu       sync.Mutex
	notified int
	inTx     int
	errs     []error
}

func (n *outboxNotifier) ReviewOpened(ctx context.Context, item *reviews.Item) {
	err := n.emitter.Emit(ctx, &events.Event{OrgID: item.OrganizationID, Type: events.ReviewCreated, EntityID: item.ID,
		Data: map[string]any{"id": item.ID, "kind": item.Kind}})
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified++
	if database.InTx(ctx) {
		n.inTx++
	}
	if err != nil {
		n.errs = append(n.errs, err)
	}
}

func (n *outboxNotifier) state() (notified, inTx int, errs []error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notified, n.inTx, append([]error(nil), n.errs...)
}

// failOnce fails the first interface sync after arm, after its writes, as a
// failure late in a record would.
type failOnce struct {
	next  *ipam.PGRepository
	armed atomic.Bool
}

func (s *failOnce) arm() { s.armed.Store(true) }

func (s *failOnce) SyncDiscoveredInterfaces(ctx context.Context, orgID, ciID string, observed []ipam.ObservedInterface) (ipam.SyncResult, error) {
	res, err := s.next.SyncDiscoveredInterfaces(ctx, orgID, ciID, observed)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		return ipam.SyncResult{}, errors.New("injected failure after the interface sync")
	}
	return res, err
}

// integrityViolations runs tests/integrity-check.sql for one organization
// and returns the checks with violations (ABN-03).
func integrityViolations(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	script, err := os.ReadFile("../../../tests/integrity-check.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('integrity.org', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, string(script))
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			out[name] = n
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
