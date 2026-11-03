package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

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

// TestDuplicateIPResolveSide covers WP-773 (NET-06, E-53, WP-675, WP-676)
// against PostgreSQL, with duplicate_ip items of WP-300 with two CI sides:
//   - accept with ci_id removes the ip_address rows of that side only; the
//     other side stays;
//   - accept without ci_id, or with a ci_id outside candidate_ci_ids, is 400
//     and leaves the item open;
//   - with one CI involved (a static address next to its interface) that CI
//     is the side, accept needs no ci_id;
//   - two concurrent accepts: one takes effect, the other is 409;
//   - a side that REC-07 removed meanwhile is 422 and the item stays open;
//   - dismiss changes no data, and two ingest runs afterwards open no new
//     item.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDuplicateIPResolveSide(t *testing.T) {
	f := scopetest.Seed(t, "e3")
	bg := context.Background()
	org := f.OrgA
	ctx := tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	cis := noBatch{ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())}
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithInterfaces(ipam.NewPGRepository(f.App))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	svc := h.ReviewService()

	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	vrf := str(`INSERT INTO vrf (organization_id, name) VALUES ($1, 'dup-resolve') RETURNING id::text`, org)
	subnet := str(`INSERT INTO subnet (organization_id, cidr, vrf_id) VALUES ($1, '10.89.0.0/24', $2) RETURNING id::text`, org, vrf)
	targets := map[string]string{}
	record := func(serial string, addresses ...string) plugins.DeviceRecord {
		if targets[serial] == "" {
			targets[serial] = fmt.Sprintf("198.51.120.%d", 1+len(targets))
		}
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, IP: targets[serial], Serial: serial},
			time.Now().Add(-time.Second))
		rec.VRFID = vrf
		nic := plugins.DeviceInterface{Name: "eth0"}
		for _, a := range addresses {
			nic.IPs = append(nic.IPs, plugins.DeviceIP{Address: a, PrefixLength: 24})
		}
		rec.Interfaces = []plugins.DeviceInterface{nic}
		return rec
	}
	ciOf := func(serial string) string {
		return str(`SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, org, serial)
	}
	// rows returns the ip_address rows of address in the VRF, as
	// "<ci or type>:<id>", sorted.
	rows := func(address string) []string {
		t.Helper()
		r, err := f.Admin.Query(bg, `SELECT COALESCE(n.ci_id::text, a.type) || ':' || a.id::text FROM ip_address a
			LEFT JOIN network_interface n ON n.id = a.interface_id
			WHERE a.organization_id = $1 AND a.vrf_id = $2 AND a.address_host = $3 ORDER BY 1`, org, vrf, address)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []string
		for r.Next() {
			var s string
			if err = r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	held := func(ciID, address string) int {
		n := 0
		for _, r := range rows(address) {
			if len(r) > len(ciID) && r[:len(ciID)] == ciID {
				n++
			}
		}
		return n
	}
	itemsOf := func(address, status string) []reviews.Item {
		t.Helper()
		out, _, err := svc.List(ctx, org, reviews.Filter{Kind: reviews.KindDuplicateIP, ObjectKey: "ip:" + vrf + "/" + address,
			Status: status}, api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// duplicate lets two devices report address and returns the open item
	// and both CIs.
	duplicate := func(address, serialA, serialB string) (item reviews.Item, a, b string) {
		t.Helper()
		ingestRecords(t, mux, ctx, record(serialA, address), record(serialB, address))
		open := itemsOf(address, reviews.StatusOpen)
		if len(open) != 1 || len(open[0].CandidateCIIDs) != 2 {
			t.Fatalf("%s: open items %+v, want one with two CIs", address, open)
		}
		return open[0], ciOf(serialA), ciOf(serialB)
	}
	accept := func(id, ciID string) error {
		_, err := svc.Resolve(ctx, org, id, &reviews.Resolution{Action: reviews.ActionAccept, CIID: ciID, ResolvedBy: f.User})
		return err
	}
	status := func(err error) int {
		var refused *reviews.Error
		switch {
		case errors.Is(err, reviews.ErrAlreadyResolved):
			return http.StatusConflict
		case errors.As(err, &refused):
			return refused.Status
		case err == nil:
			return http.StatusOK
		}
		return 0
	}
	stillOpen := func(id string) {
		t.Helper()
		if got, err := svc.Get(ctx, org, id); err != nil || got.Status != reviews.StatusOpen {
			t.Errorf("item %s: %+v, %v, want open", id, got, err)
		}
	}

	// Accept with ci_id removes the side of that CI only; without ci_id and
	// with a CI outside the candidates it is 400.
	item, a, b := duplicate("10.89.0.5", "RES-SIDE-A", "RES-SIDE-B")
	for _, ciID := range []string{"", "0b7c1a4e-1111-4222-8333-944455556666"} {
		if got := status(accept(item.ID, ciID)); got != http.StatusBadRequest {
			t.Errorf("accept with ci_id %q: %d, want 400", ciID, got)
		}
	}
	stillOpen(item.ID)
	if n := len(rows("10.89.0.5")); n != 2 {
		t.Fatalf("refused accepts: %d rows, want 2", n)
	}
	if err := accept(item.ID, a); err != nil {
		t.Fatalf("accept side %s: %v", a, err)
	}
	if held(a, "10.89.0.5") != 0 || held(b, "10.89.0.5") != 1 {
		t.Errorf("after accept of side %s: rows %v, want only the side of %s", a, rows("10.89.0.5"), b)
	}
	if got, err := svc.Get(ctx, org, item.ID); err != nil || got.Status != reviews.StatusResolved || got.Action != reviews.ActionAccept {
		t.Errorf("accepted item: %+v, %v", got, err)
	}

	// One CI next to a static address: that CI is the side.
	str(`INSERT INTO ip_address (organization_id, subnet_id, address, type) VALUES ($1, $2, '10.89.0.9/24', 'static')
		RETURNING id::text`, org, subnet)
	ingestRecords(t, mux, ctx, record("RES-SINGLE", "10.89.0.9"))
	single := itemsOf("10.89.0.9", reviews.StatusOpen)
	if len(single) != 1 || len(single[0].CandidateCIIDs) != 1 {
		t.Fatalf("static next to an interface: open items %+v, want one with one CI", single)
	}
	if err := accept(single[0].ID, ""); err != nil {
		t.Fatalf("accept with one CI: %v", err)
	}
	if got := rows("10.89.0.9"); len(got) != 1 || got[0][:len("static:")] != "static:" {
		t.Errorf("after accept with one CI: rows %v, want the static address", got)
	}

	// Two concurrent accepts of different sides: one takes effect, the other
	// is 409.
	item, a, b = duplicate("10.89.0.6", "RES-RACE-A", "RES-RACE-B")
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, side := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = status(accept(item.ID, side))
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
		t.Errorf("concurrent accepts: %v, want 200 and 409", codes)
	}
	if n := held(a, "10.89.0.6") + held(b, "10.89.0.6"); n != 1 {
		t.Errorf("concurrent accepts: rows %v, want one side left", rows("10.89.0.6"))
	}

	// The side of A is gone after three misses (REC-07): accept is 422 and
	// leaves the item open.
	item, a, _ = duplicate("10.89.0.7", "RES-GONE-A", "RES-GONE-B")
	for range 3 {
		ingestRecords(t, mux, ctx, record("RES-GONE-A", "10.89.0.70"))
	}
	if n := held(a, "10.89.0.7"); n != 0 {
		t.Fatalf("after three misses: %d rows of %s, want 0", n, a)
	}
	if got := status(accept(item.ID, a)); got != http.StatusUnprocessableEntity {
		t.Errorf("accept of a removed side: %d, want 422", got)
	}
	stillOpen(item.ID)
	if n := len(rows("10.89.0.7")); n != 1 {
		t.Errorf("refused accept: %d rows, want the remaining side", n)
	}

	// Dismiss changes no data; two ingest runs then open no new item.
	item, _, _ = duplicate("10.89.0.8", "RES-KEEP-A", "RES-KEEP-B")
	before := rows("10.89.0.8")
	if _, err := svc.Resolve(ctx, org, item.ID, &reviews.Resolution{Action: reviews.ActionDismiss, ResolvedBy: f.User}); err != nil {
		t.Fatal(err)
	}
	if after := rows("10.89.0.8"); !slices.Equal(before, after) {
		t.Errorf("dismiss changed the rows: %v, want %v", after, before)
	}
	for range 2 {
		ingestRecords(t, mux, ctx, record("RES-KEEP-A", "10.89.0.8"), record("RES-KEEP-B", "10.89.0.8"))
	}
	if open := itemsOf("10.89.0.8", reviews.StatusOpen); len(open) != 0 {
		t.Errorf("after dismiss and two runs: open items %+v, want none", open)
	}
	if after := rows("10.89.0.8"); !slices.Equal(before, after) {
		t.Errorf("after dismiss and two runs: rows %v, want %v", after, before)
	}
}
