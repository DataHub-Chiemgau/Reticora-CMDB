package discovery_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// syncOnly hides the duplicate check of the IPAM repository: the ingest
// syncs interfaces as before WP-300.
type syncOnly struct{ next *ipam.PGRepository }

func (s syncOnly) SyncDiscoveredInterfaces(ctx context.Context, orgID, ciID string, observed []ipam.ObservedInterface) (ipam.SyncResult, error) {
	return s.next.SyncDiscoveredInterfaces(ctx, orgID, ciID, observed)
}

// TestDuplicateIPWithinVRF covers WP-300 (NET-06, E-53, E-56) against
// PostgreSQL:
//   - two records of different CIs with one interface address in one VRF
//     open exactly one duplicate_ip item with both sides in holders, on the
//     record path and in the batch alike;
//   - the address in different VRFs opens nothing and every row carries the
//     VRF of its record; an unknown vrf_id is the default VRF and the record
//     succeeds; the address at two interfaces of one CI is no duplicate;
//   - two parallel lanes, and an ingest next to POST /api/v1/ips, open
//     exactly one item;
//   - after dismiss and after accept no new item opens while side_keys stays
//     the same (no review.created, the counter unchanged); a third side opens
//     a new one;
//   - budget: a batch of 20 records with interfaces sends no statement more
//     in its record and batch transactions, and without duplicate exactly
//     one check transaction of three statements follows; the check reads
//     ip_address through idx_ip_address_org_vrf_host as reticora_app.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDuplicateIPWithinVRF(t *testing.T) {
	f := scopetest.Seed(t, "e3")
	bg := context.Background()
	notifier := &countingNotifier{}
	build := func(pool *pgxpool.Pool, batch bool, lanes int, syncer discovery.InterfaceSyncer) *discovery.Handler {
		var cis ci.Repository = ci.NewPGRepositoryWithAudit(pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
		if !batch {
			cis = noBatch{cis}
		}
		h := discovery.NewHandler(discovery.NewPGRepository(pool), cis).WithInterfaces(syncer).WithReviewNotifier(notifier)
		if lanes > 0 {
			h.WithIngestLanes(lanes)
		}
		return h
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	ctxOf := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	muxOf := func(h *discovery.Handler) http.Handler {
		mux := chi.NewRouter()
		h.RegisterRoutes(mux)
		return mux
	}
	targets := map[string]string{}
	record := func(serial, vrf string, interfaces ...plugins.DeviceInterface) plugins.DeviceRecord {
		if targets[serial] == "" {
			targets[serial] = fmt.Sprintf("198.51.%d.%d", 100+len(targets)/200, 1+len(targets)%200)
		}
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, IP: targets[serial], Serial: serial},
			time.Now().Add(-time.Second))
		rec.VRFID = vrf
		rec.Interfaces = interfaces
		return rec
	}
	nic := func(name string, addresses ...string) plugins.DeviceInterface {
		out := plugins.DeviceInterface{Name: name}
		for _, a := range addresses {
			out.IPs = append(out.IPs, plugins.DeviceIP{Address: a, PrefixLength: 24})
		}
		return out
	}
	itemsOf := func(h *discovery.Handler, org, objectKey, status string) []reviews.Item {
		t.Helper()
		out, _, err := h.ReviewService().List(ctxOf(org), org, reviews.Filter{Kind: reviews.KindDuplicateIP, ObjectKey: objectKey,
			Status: status}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	created := func(org string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox e JOIN review_item r
				ON r.organization_id = e.organization_id AND r.id::text = e.entity_id::text
			WHERE e.organization_id = $1 AND e.type = 'review.created' AND r.kind = 'duplicate_ip'`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ciOf := func(org, serial string) string {
		return str(`SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, org, serial)
	}
	interfaceOf := func(ciID, name string) string {
		return str(`SELECT id::text FROM network_interface WHERE ci_id = $1 AND name = $2`, ciID, name)
	}
	vrfOf := func(org, name string) string {
		return str(`INSERT INTO vrf (organization_id, name) VALUES ($1, $2) RETURNING id::text`, org, name)
	}
	sides := func(item *reviews.Item) []string {
		raw, _ := item.Payload["side_keys"].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			s, _ := v.(string)
			out = append(out, s)
		}
		return out
	}

	// Record path (organization A) and batch (organization B): two devices
	// report 10.88.0.5 at eth0 in one VRF.
	vrfs := map[string]string{f.OrgA: vrfOf(f.OrgA, "dup-1"), f.OrgB: vrfOf(f.OrgB, "dup-1")}
	handlers := map[string]*discovery.Handler{
		f.OrgA: build(f.App, false, 0, ipam.NewPGRepository(f.App)),
		f.OrgB: build(f.App, true, 0, ipam.NewPGRepository(f.App)),
	}
	shapes := map[string]string{}
	for _, org := range []string{f.OrgA, f.OrgB} {
		vrf, h := vrfs[org], handlers[org]
		before, counted := created(org), testutil.ToFloat64(ipam.DuplicateIPReviews)
		resp := ingestRecords(t, muxOf(h), ctxOf(org), record("DUP-A", vrf, nic("eth0", "10.88.0.5")),
			record("DUP-B", vrf, nic("eth0", "10.88.0.5")))
		if resp.Created != 2 || resp.Rejected != 0 {
			t.Fatalf("%s: ingest %+v", org, resp)
		}
		a, b := ciOf(org, "DUP-A"), ciOf(org, "DUP-B")
		open := itemsOf(h, org, "", reviews.StatusOpen)
		if len(open) != 1 {
			t.Fatalf("%s: %d open duplicate_ip items, want 1: %+v", org, len(open), open)
		}
		item := open[0]
		wantCIs := []string{a, b}
		slices.Sort(wantCIs)
		if item.ObjectKey != "ip:"+vrf+"/10.88.0.5" || !slices.Equal(item.CandidateCIIDs, wantCIs) {
			t.Errorf("%s: object_key %q, candidates %v, want both CIs", org, item.ObjectKey, item.CandidateCIIDs)
		}
		wantSides := []string{a + "/" + interfaceOf(a, "eth0"), b + "/" + interfaceOf(b, "eth0")}
		slices.Sort(wantSides)
		if got := sides(&item); !slices.Equal(got, wantSides) {
			t.Errorf("%s: side_keys %v, want %v", org, got, wantSides)
		}
		holders, _ := item.Payload["holders"].([]any)
		if len(holders) != 2 || item.Payload["address"] != "10.88.0.5" || item.Payload["vrf_id"] != vrf || len(item.Payload) != 4 {
			t.Errorf("%s: payload %v, want address, vrf_id, both holders and side_keys", org, item.Payload)
		}
		for _, raw := range holders {
			h, _ := raw.(map[string]any)
			if h["type"] != "discovered" || h["ip_address_id"] == "" || h["interface_id"] == "" || (h["ci_id"] != a && h["ci_id"] != b) {
				t.Errorf("%s: holder %v", org, h)
			}
		}
		if n := created(org) - before; n != 1 {
			t.Errorf("%s: %d review.created, want 1", org, n)
		}
		if n := testutil.ToFloat64(ipam.DuplicateIPReviews) - counted; n != 1 {
			t.Errorf("%s: counter rose by %v, want 1", org, n)
		}
		shapes[org] = fmt.Sprintf("%s %v %d %d", item.Status, item.Payload["address"], len(holders), len(sides(&item)))
	}
	if shapes[f.OrgA] != shapes[f.OrgB] {
		t.Errorf("record path %q and batch %q differ", shapes[f.OrgA], shapes[f.OrgB])
	}
	if _, inTx := notifier.state(); inTx != 0 {
		t.Errorf("%d reviews announced inside a transaction, want them after the commit", inTx)
	}

	org, vrf, h := f.OrgA, vrfs[f.OrgA], handlers[f.OrgA]
	ctx, mux := ctxOf(org), muxOf(h)
	other := vrfOf(org, "dup-2")
	count := func() int { return len(itemsOf(h, org, "", "")) }
	total := count()

	// The address in two VRFs: no item, each row in the VRF of its record.
	ingestRecords(t, mux, ctx, record("DUP-V1", vrf, nic("eth0", "10.88.0.6")), record("DUP-V2", other, nic("eth0", "10.88.0.6")))
	for serial, want := range map[string]string{"DUP-V1": vrf, "DUP-V2": other} {
		if got := str(`SELECT a.vrf_id::text FROM ip_address a JOIN network_interface n ON n.id = a.interface_id WHERE n.ci_id = $1`,
			ciOf(org, serial)); got != want {
			t.Errorf("%s: address in VRF %s, want the record's %s", serial, got, want)
		}
	}
	// An unknown vrf_id is the default VRF; the record succeeds.
	resp := ingestRecords(t, mux, ctx, record("DUP-UNKNOWN", "0b7c1a4e-1111-4222-8333-944455556666", nic("eth0", "10.88.0.7")))
	if resp.Created != 1 || resp.Rejected != 0 {
		t.Errorf("unknown vrf_id: %+v, want the record created", resp)
	}
	if got := str(`SELECT a.vrf_id::text FROM ip_address a JOIN network_interface n ON n.id = a.interface_id WHERE n.ci_id = $1`,
		ciOf(org, "DUP-UNKNOWN")); got != str(`SELECT default_vrf_of($1)::text`, org) {
		t.Errorf("unknown vrf_id: address in VRF %s, want the default VRF", got)
	}
	// One CI, two interfaces with the address (bond): no duplicate.
	ingestRecords(t, mux, ctx, record("DUP-BOND", vrf, nic("eth0", "10.88.0.8"), nic("eth1", "10.88.0.8")))
	if n := count(); n != total {
		t.Errorf("different VRFs, unknown VRF, bond: %d duplicate_ip items, want %d", n, total)
	}

	// Two parallel lanes report one address on two CIs: one open item.
	lanes := build(f.App, true, 4, ipam.NewPGRepository(f.App))
	for i, result := range discovery.ConsumeRecords(ctx, lanes, org,
		record("DUP-LANE-1", vrf, nic("eth0", "10.88.0.20")), record("DUP-LANE-2", vrf, nic("eth0", "10.88.0.20"))) {
		if result != "ack" {
			t.Errorf("parallel lanes: record %d: %s", i, result)
		}
	}
	if n := len(itemsOf(h, org, "ip:"+vrf+"/10.88.0.20", reviews.StatusOpen)); n != 1 {
		t.Errorf("parallel lanes: %d open items, want 1", n)
	}

	// An ingest next to POST /api/v1/ips (static, host notation) in the VRF
	// of a subnet: one open item.
	subnet := str(`INSERT INTO subnet (organization_id, cidr, vrf_id) VALUES ($1, '10.88.0.0/24', $2) RETURNING id::text`, org, vrf)
	ipMux := chi.NewRouter()
	ipam.NewHandler(ipam.NewPGRepository(f.App)).WithReviews(lanes.ReviewService()).RegisterRoutes(ipMux)
	var wg sync.WaitGroup
	var postCode int
	wg.Add(2)
	go func() {
		defer wg.Done()
		discovery.ConsumeRecords(ctx, lanes, org, record("DUP-REST", vrf, nic("eth0", "10.88.0.30")))
	}()
	go func() {
		defer wg.Done()
		w := httptest.NewRecorder()
		ipMux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ips",
			strings.NewReader(`{"address":"10.88.0.30","type":"static","subnet_id":"`+subnet+`"}`)).WithContext(ctx))
		postCode = w.Code
	}()
	wg.Wait()
	if postCode != http.StatusCreated {
		t.Fatalf("POST /ips: %d", postCode)
	}
	if open := itemsOf(h, org, "ip:"+vrf+"/10.88.0.30", reviews.StatusOpen); len(open) != 1 || len(sides(&open[0])) != 2 {
		t.Errorf("ingest next to POST /ips: open items %+v, want one with both sides", open)
	}

	// Dismissed, the same sides stay quiet; a third side opens a new item.
	key := "ip:" + vrf + "/10.88.0.5"
	open := itemsOf(h, org, key, reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open items of %s", len(open), key)
	}
	if _, err := h.ReviewService().Resolve(ctx, org, open[0].ID, &reviews.Resolution{Action: reviews.ActionDismiss,
		ResolvedBy: f.User}); err != nil {
		t.Fatal(err)
	}
	before, counted := created(org), testutil.ToFloat64(ipam.DuplicateIPReviews)
	for range 2 {
		ingestRecords(t, mux, ctx, record("DUP-A", vrf, nic("eth0", "10.88.0.5")), record("DUP-B", vrf, nic("eth0", "10.88.0.5")))
	}
	if n, events, c := len(itemsOf(h, org, key, reviews.StatusOpen)), created(org)-before,
		testutil.ToFloat64(ipam.DuplicateIPReviews)-counted; n != 0 || events != 0 || c != 0 {
		t.Errorf("after dismiss: %d open items, %d review.created, counter +%v, want 0", n, events, c)
	}
	ingestRecords(t, mux, ctx, record("DUP-C", vrf, nic("eth0", "10.88.0.5")))
	open = itemsOf(h, org, key, reviews.StatusOpen)
	if len(open) != 1 || len(sides(&open[0])) != 3 {
		t.Fatalf("third side: open items %+v, want one with three sides", open)
	}

	// Accepted (resolved with accept; the side action is WP-773), the same
	// sides stay quiet as well; another side opens a new item.
	if _, err := f.Admin.Exec(bg, `UPDATE review_item SET status = 'resolved', action = 'accept', resolved_by = $2, resolved_at = now()
		WHERE id = $1`, open[0].ID, f.User); err != nil {
		t.Fatal(err)
	}
	before, counted = created(org), testutil.ToFloat64(ipam.DuplicateIPReviews)
	ingestRecords(t, mux, ctx, record("DUP-C", vrf, nic("eth0", "10.88.0.5")))
	if n, events, c := len(itemsOf(h, org, key, reviews.StatusOpen)), created(org)-before,
		testutil.ToFloat64(ipam.DuplicateIPReviews)-counted; n != 0 || events != 0 || c != 0 {
		t.Errorf("after accept: %d open items, %d review.created, counter +%v, want 0", n, events, c)
	}
	ingestRecords(t, mux, ctx, record("DUP-C", vrf, nic("eth0", "10.88.0.5"), nic("eth1", "10.88.0.5")))
	if open = itemsOf(h, org, key, reviews.StatusOpen); len(open) != 1 || len(sides(&open[0])) != 4 {
		t.Errorf("another side after accept: open items %+v, want one with four sides", open)
	}
	if n := testutil.ToFloat64(ipam.DuplicateIPReviews) - counted; n != 1 {
		t.Errorf("another side after accept: counter +%v, want 1", n)
	}

	t.Run("budget", func(t *testing.T) {
		trace := &txTrace{open: map[*pgx.Conn][]string{}}
		cfg := f.App.Config()
		cfg.ConnConfig.Tracer = trace
		cfg.MinConns = 0
		pool, err := pgxpool.NewWithConfig(bg, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		seq := 0
		batch := func(prefix string) []plugins.DeviceRecord {
			records := make([]plugins.DeviceRecord, 20)
			for i := range records {
				seq++
				records[i] = record(fmt.Sprintf("%s-%02d", prefix, i), vrf, nic("eth0", fmt.Sprintf("10.89.%d.%d", seq/200, 1+seq%200)))
			}
			return records
		}
		run := func(syncer discovery.InterfaceSyncer, prefix string) (txs [][]string) {
			t.Helper()
			h := build(pool, true, 0, syncer)
			trace.take()
			if resp := ingestRecords(t, muxOf(h), ctx, batch(prefix)...); resp.Created != 20 {
				t.Fatalf("%s: %+v", prefix, resp)
			}
			txs, loose := trace.take()
			if len(loose) != 0 {
				t.Errorf("%s: statements outside transactions: %q", prefix, loose)
			}
			return txs
		}
		lengths := func(txs [][]string) []int {
			out := make([]int, 0, len(txs))
			for _, tx := range txs {
				out = append(out, len(tx))
			}
			sort.Ints(out)
			return out
		}
		checkQuery := regexp.MustCompile(`^with k as \(`)
		check := func(sql string) bool { return checkQuery.MatchString(strings.Join(strings.Fields(sql), " ")) }
		without := run(syncOnly{ipam.NewPGRepository(pool)}, "BUDGET-OFF")
		with := run(ipam.NewPGRepository(pool), "BUDGET-ON")
		var checks, rest [][]string
		for _, tx := range with {
			if slices.ContainsFunc(tx, check) {
				checks = append(checks, tx)
			} else {
				rest = append(rest, tx)
			}
		}
		if len(checks) != 1 || len(checks[0]) != 3 {
			t.Fatalf("check transactions %q, want exactly one of three statements (BEGIN, query, COMMIT)", checks)
		}
		// The one lookup of pinned addresses of other devices per sync
		// (WP-914 AK4) reads the pins only (a.pinned, idx_ip_address_pinned).
		for _, tx := range rest {
			for _, sql := range tx {
				if strings.Contains(sql, "address_host") && !strings.Contains(sql, "a.pinned") {
					t.Errorf("a record or batch transaction reads address_host: %.200q", sql)
				}
			}
		}
		if a, b := lengths(rest), lengths(without); !slices.Equal(a, b) {
			t.Errorf("record and batch transactions with the check %v, without %v; want no statement more", a, b)
		}

		// The plan of the check as reticora_app reads ip_address through
		// idx_ip_address_org_vrf_host, also beside many rows of another
		// organization.
		if _, err = f.Admin.Exec(bg, `INSERT INTO ip_address (organization_id, address, type)
			SELECT $1, '10.200.0.0'::inet + i, 'static' FROM generate_series(1, 3000) AS i`, f.OrgB); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Admin.Exec(bg, `ANALYZE ip_address`); err != nil {
			t.Fatal(err)
		}
		reads := relationReads(t, f, "ip_address", checks[0][1], org, []string{vrf, vrf}, []string{"10.89.0.2", "10.89.0.3"})
		indexed := 0
		for _, r := range reads {
			if r.node == "Seq Scan" {
				t.Errorf("the check scans ip_address: %v", r)
			}
			if r.index == "idx_ip_address_org_vrf_host" && strings.Contains(r.cond, "address_host") {
				indexed++
			}
		}
		if indexed == 0 {
			t.Errorf("the check does not read ip_address through idx_ip_address_org_vrf_host: %v", reads)
		}
	})
}
