package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

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
	"github.com/go-chi/chi/v5"
)

// TestReservationConflictOpensDuplicateIPReview covers WP-772 (NET-09,
// REC-08, E-53) against PostgreSQL: an address discovery observes at an
// interface in the VRF of a reservation (WP-299) opens one duplicate_ip
// item, on the record path and in the batch alike; the observed address
// takes the VRF of the record; a record without VRF lands in the default
// VRF and opens nothing; repeated runs deduplicate; a dismissal holds until
// another interface reports the address; accept releases the reservation
// and is 422 once it is gone; misses never remove a reservation; two
// devices with the address in one batch of parallel lanes open one item.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReservationConflictOpensDuplicateIPReview(t *testing.T) {
	f := scopetest.Seed(t, "e3")
	bg := context.Background()
	notifier := &countingNotifier{}
	build := func(batch bool, lanes int) *discovery.Handler {
		var cis ci.Repository = ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
		if !batch {
			cis = noBatch{cis}
		}
		h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).
			WithInterfaces(ipam.NewPGRepository(f.App)).WithReviewNotifier(notifier)
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
	type setup struct {
		org, vrf, subnet, reservation string
		h                             *discovery.Handler
		mux                           http.Handler
		ctx                           context.Context
	}
	reserve := func(s *setup, address string) string {
		t.Helper()
		return str(`INSERT INTO ip_address (organization_id, subnet_id, address, type, reserved_by)
			VALUES ($1, $2, $3::inet, 'reserved', $4) RETURNING id::text`, s.org, s.subnet, address, f.User)
	}
	prepare := func(org string, batch bool) *setup {
		s := &setup{org: org, h: build(batch, 0), ctx: ctxOf(org)}
		mux := chi.NewRouter()
		s.h.RegisterRoutes(mux)
		s.mux = mux
		s.vrf = str(`INSERT INTO vrf (organization_id, name) VALUES ($1, 'res-conflict') RETURNING id::text`, org)
		s.subnet = str(`INSERT INTO subnet (organization_id, cidr, vrf_id) VALUES ($1, '10.77.0.0/24', $2) RETURNING id::text`, org, s.vrf)
		s.reservation = reserve(s, "10.77.0.5")
		return s
	}
	targets := map[string]string{}
	record := func(serial, vrf string, interfaces ...plugins.DeviceInterface) plugins.DeviceRecord {
		if targets[serial] == "" {
			targets[serial] = fmt.Sprintf("198.51.100.%d", 100+len(targets))
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
	items := func(s *setup, status string) []reviews.Item {
		t.Helper()
		out, _, err := s.h.ReviewService().List(s.ctx, s.org, reviews.Filter{Kind: reviews.KindDuplicateIP, Status: status},
			api.PaginationParams{Limit: 50})
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
	reservationExists := func(id string) bool {
		return str(`SELECT (count(*) = 1)::text FROM ip_address WHERE id = $1 AND type = 'reserved'`, id) == "true"
	}
	resolve := func(s *setup, id, action string) error {
		_, err := s.h.ReviewService().Resolve(s.ctx, s.org, id, &reviews.Resolution{Action: action, ResolvedBy: f.User})
		return err
	}

	// Record path (organization A) and batch (organization B): the device
	// reports the reserved address in CIDR notation at eth0 in the VRF of
	// the reservation, beside a second device the batch creates with it.
	single, batched := prepare(f.OrgA, false), prepare(f.OrgB, true)
	payloads := map[string]map[string]any{}
	for _, s := range []*setup{single, batched} {
		before := created(s.org)
		resp := ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0", "10.77.0.5")),
			record("RES-OTHER", s.vrf, nic("eth0", "10.77.0.6")))
		if resp.Created != 2 || resp.Rejected != 0 {
			t.Fatalf("%s: ingest %+v", s.org, resp)
		}
		holder := ciOf(s.org, "RES-HOLDER")
		eth0 := interfaceOf(holder, "eth0")
		discovered := str(`SELECT id::text FROM ip_address WHERE interface_id = $1 AND host(address) = '10.77.0.5'`, eth0)
		if vrf := str(`SELECT vrf_id::text FROM ip_address WHERE id = $1`, discovered); vrf != s.vrf {
			t.Errorf("%s: the observed address is in VRF %s, want the record's %s", s.org, vrf, s.vrf)
		}
		open := items(s, reviews.StatusOpen)
		if len(open) != 1 {
			t.Fatalf("%s: %d open duplicate_ip items, want 1: %+v", s.org, len(open), open)
		}
		item := open[0]
		want := map[string]any{"reason": "reservation", "address": "10.77.0.5", "vrf_id": s.vrf, "subnet_id": s.subnet,
			"reservation_ip_id": s.reservation, "discovered_ip_id": discovered, "interface_id": eth0, "ci_id": holder,
			"reserved_by": f.User, "conflict": s.reservation + "/" + eth0}
		if item.ObjectKey != "ip:"+s.vrf+"/10.77.0.5" || !slices.Equal(item.CandidateCIIDs, []string{holder}) {
			t.Errorf("%s: object_key %q, candidates %v", s.org, item.ObjectKey, item.CandidateCIIDs)
		}
		for k, v := range want {
			if item.Payload[k] != v {
				t.Errorf("%s: payload[%s] = %v, want %v", s.org, k, item.Payload[k], v)
			}
		}
		if len(item.Payload) != len(want) {
			t.Errorf("%s: payload %v has other keys than %v", s.org, item.Payload, want)
		}
		if n := created(s.org) - before; n != 1 {
			t.Errorf("%s: %d review.created events, want 1", s.org, n)
		}
		if !reservationExists(s.reservation) {
			t.Errorf("%s: the conflict changed the reservation", s.org)
		}
		payloads[s.org] = map[string]any{"reason": item.Payload["reason"], "address": item.Payload["address"], "status": item.Status}
	}
	if a, b := payloads[f.OrgA], payloads[f.OrgB]; a["reason"] != b["reason"] || a["address"] != b["address"] || a["status"] != b["status"] {
		t.Errorf("record path %v and batch %v differ", a, b)
	}
	if _, inTx := notifier.state(); inTx != 0 {
		t.Errorf("%d reviews announced inside a transaction, want them after the commit", inTx)
	}

	s := single
	holder := ciOf(s.org, "RES-HOLDER")

	// The same address without vrf_id is in the default VRF: no item.
	ingestRecords(t, s.mux, s.ctx, record("RES-NOVRF", "", nic("eth0", "10.77.0.5")))
	if vrf := str(`SELECT a.vrf_id::text FROM ip_address a JOIN network_interface n ON n.id = a.interface_id
			WHERE n.ci_id = $1`, ciOf(s.org, "RES-NOVRF")); vrf != str(`SELECT default_vrf_of($1)::text`, s.org) {
		t.Errorf("record without vrf_id: address in VRF %s, want the default VRF", vrf)
	}
	if n := len(items(s, "")); n != 1 {
		t.Errorf("record without vrf_id: %d duplicate_ip items, want still 1", n)
	}

	// A second run deduplicates.
	before := created(s.org)
	ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0", "10.77.0.5")))
	open := items(s, reviews.StatusOpen)
	if len(open) != 1 || created(s.org) != before {
		t.Fatalf("second run: %d open items, %d new review.created", len(open), created(s.org)-before)
	}

	// Dismissed, the same conflict stays quiet; the address at another
	// interface opens a new item.
	if err := resolve(s, open[0].ID, reviews.ActionDismiss); err != nil {
		t.Fatal(err)
	}
	if !reservationExists(s.reservation) {
		t.Error("dismiss released the reservation")
	}
	for range 2 {
		ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0", "10.77.0.5")))
	}
	if n, events := len(items(s, reviews.StatusOpen)), created(s.org)-before; n != 0 || events != 0 {
		t.Errorf("after dismiss: %d open items, %d review.created, want 0", n, events)
	}
	ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0"), nic("eth1", "10.77.0.5")))
	open = items(s, reviews.StatusOpen)
	if len(open) != 1 || open[0].Payload["interface_id"] != interfaceOf(holder, "eth1") {
		t.Fatalf("address at eth1: open items %+v, want one naming eth1", open)
	}

	// Accept releases the reservation and resolves the item.
	if err := resolve(s, open[0].ID, reviews.ActionAccept); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if reservationExists(s.reservation) {
		t.Error("accept kept the reservation")
	}
	got, err := s.h.ReviewService().Get(s.ctx, s.org, open[0].ID)
	if err != nil || got.Status != reviews.StatusResolved || got.Action != reviews.ActionAccept {
		t.Errorf("accepted item: %+v, %v", got, err)
	}

	// A new reservation of the address opens a new item; accepting it after
	// the reservation was released elsewhere is 422 and leaves it open.
	again := reserve(s, "10.77.0.5")
	ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0"), nic("eth1", "10.77.0.5")))
	open = items(s, reviews.StatusOpen)
	if len(open) != 1 || open[0].Payload["reservation_ip_id"] != again {
		t.Fatalf("new reservation: open items %+v", open)
	}
	if _, err = f.Admin.Exec(bg, `DELETE FROM ip_address WHERE id = $1`, again); err != nil {
		t.Fatal(err)
	}
	var refused *reviews.Error
	if err = resolve(s, open[0].ID, reviews.ActionAccept); !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity {
		t.Errorf("accept of a released reservation: %v, want 422", err)
	}
	if got, _ = s.h.ReviewService().Get(s.ctx, s.org, open[0].ID); got == nil || got.Status != reviews.StatusOpen {
		t.Errorf("refused accept: item %+v, want open", got)
	}
	if err = resolve(s, open[0].ID, reviews.ActionDismiss); err != nil {
		t.Fatal(err)
	}

	// Three runs without the address remove the observation, never the
	// reservation.
	kept := reserve(s, "10.77.0.7")
	ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0", "10.77.0.7"), nic("eth1")))
	for range 3 {
		ingestRecords(t, s.mux, s.ctx, record("RES-HOLDER", s.vrf, nic("eth0", "10.77.0.8"), nic("eth1")))
	}
	if !reservationExists(kept) {
		t.Error("the reservation was removed by missed observations")
	}
	if n := str(`SELECT count(*)::text FROM ip_address a JOIN network_interface n ON n.id = a.interface_id
			WHERE n.ci_id = $1 AND host(a.address) = '10.77.0.7'`, holder); n != "0" {
		t.Errorf("observed 10.77.0.7 after three misses: %s rows, want 0", n)
	}

	// Two devices report one reserved address in one batch of parallel
	// lanes: one open item, no failed record.
	lanes := build(true, 4)
	reserve(batched, "10.77.0.20")
	results := discovery.ConsumeRecords(batched.ctx, lanes, batched.org,
		record("RES-LANE-1", batched.vrf, nic("eth0", "10.77.0.20")), record("RES-LANE-2", batched.vrf, nic("eth0", "10.77.0.20")))
	for i, result := range results {
		if result != "ack" {
			t.Errorf("parallel lanes: record %d: %s", i, result)
		}
	}
	laneItems, _, err := lanes.ReviewService().List(batched.ctx, batched.org, reviews.Filter{Kind: reviews.KindDuplicateIP,
		ObjectKey: "ip:" + batched.vrf + "/10.77.0.20", Status: reviews.StatusOpen}, api.PaginationParams{Limit: 10})
	if err != nil || len(laneItems) != 1 {
		t.Errorf("parallel lanes: %d open items, want 1 (%v)", len(laneItems), err)
	}
}
