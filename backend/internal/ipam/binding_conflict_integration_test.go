package ipam_test

// WP-914 (IPM-14, IPM-20, REC-04, E-216): origin and pin of addresses and
// the review ip_binding_conflict, against PostgreSQL as reticora_app. The
// discovery side runs the ingest of the server (POST
// /api/v1/discovery/ingest) with the IPAM repository as interface syncer.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run
// 'TestPinned|TestManualAddress|TestBindingConflict|TestDiscoveredManagement|TestMissesRemove|TestOriginBackfill|TestIPFilterOrigin' ./internal/ipam/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

// bindEnv is the scenario of WP-914: an org-wide principal of organization
// A with the IPAM routes and the discovery ingest on the application pool.
type bindEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	ctx    context.Context
	api    ifaceAPI
	disc   *discovery.Handler
	ingest http.Handler
	seq    int
}

func newBindEnv(t *testing.T) *bindEnv {
	t.Helper()
	return newBindEnvWith(t, false)
}

// newBindEnvWith is newBindEnv; with management the CI writes of the ingest
// also synchronise the management address of the record (WP-913) as the
// server wires it.
func newBindEnvWith(t *testing.T, management bool) *bindEnv {
	t.Helper()
	f := scopetest.Seed(t, "9e")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ipam.NewPGRepository(f.App)
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	if management {
		cis.WithManagementSync(ipam.NewManagementSync(repo, nil))
	}
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithInterfaces(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return &bindEnv{t: t, f: f, ctx: ctx, api: newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA), disc: h, ingest: mux}
}

// nic is an observed interface with its addresses.
func bindNIC(name string, addresses ...string) plugins.DeviceInterface {
	out := plugins.DeviceInterface{Name: name}
	for _, a := range addresses {
		out.IPs = append(out.IPs, plugins.DeviceIP{Address: a, PrefixLength: 24})
	}
	return out
}

// observe ingests one record of the device serial in the VRF vrf (empty:
// the default VRF) with the interfaces and returns its CI.
func (e *bindEnv) observe(serial, vrf string, interfaces ...plugins.DeviceInterface) string {
	e.t.Helper()
	e.seq++
	return e.observeAt(serial, fmt.Sprintf("198.51.100.%d", e.seq), vrf, interfaces...)
}

// observeAt is observe with the management address ip of the record.
func (e *bindEnv) observeAt(serial, ip, vrf string, interfaces ...plugins.DeviceInterface) string {
	e.t.Helper()
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial,
		IP: ip, Serial: serial}, time.Now().Add(-time.Second))
	rec.VRFID = vrf
	rec.Interfaces = interfaces
	body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
	if err != nil {
		e.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.ingest.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(e.ctx))
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("ingest %s: %d %s", serial, w.Code, w.Body.String())
	}
	var resp discovery.BulkIngestResponse
	if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Rejected != 0 {
		e.t.Fatalf("ingest %s: %+v %v", serial, resp, err)
	}
	return e.str(`SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, e.f.OrgA, serial)
}

func (e *bindEnv) str(q string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), q, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return out
}

func (e *bindEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (e *bindEnv) nicOf(ciID, name string) string {
	return e.str(`SELECT id::text FROM network_interface WHERE ci_id = $1 AND name = $2`, ciID, name)
}

// addressAt is the address row of host at the interface nic.
func (e *bindEnv) addressAt(nic, host string) string {
	return e.str(`SELECT id::text FROM ip_address WHERE interface_id = $1 AND address_host = $2`, nic, host)
}

// call runs an IPAM route and decodes the answer, failing unless want.
func (e *bindEnv) call(method, path, body string, want int) map[string]any {
	e.t.Helper()
	w := e.api.call(method, path, body)
	if w.Code != want {
		e.t.Fatalf("%s %s %s = %d %s, want %d", method, path, body, w.Code, w.Body.String(), want)
	}
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return out
}

func (e *bindEnv) pin(id string) {
	e.t.Helper()
	if got := e.call(http.MethodPatch, "/api/v1/ips/"+id, `{"pinned":true}`, http.StatusOK); got["pinned"] != true {
		e.t.Fatalf("PATCH pinned: %v", got)
	}
}

// items lists the ip_binding_conflict items of the organization.
func (e *bindEnv) items(status string) []reviews.Item {
	e.t.Helper()
	out, _, err := e.disc.ReviewService().List(e.ctx, e.f.OrgA, reviews.Filter{Kind: reviews.KindIPBindingConflict, Status: status},
		api.PaginationParams{Limit: 50})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// pinnedConflict sets up a pinned address 10.77.<n>.5 at eth0 of device
// PIN-<n> and observes it at eth0 of device OBS-<n>; it returns the address
// row and both CIs.
func (e *bindEnv) pinnedConflict(n int) (ip, pinnedCI, observedCI string) {
	e.t.Helper()
	host := fmt.Sprintf("10.77.%d.5", n)
	pinnedCI = e.observe(fmt.Sprintf("PIN-%d", n), "", bindNIC("eth0", host))
	ip = e.addressAt(e.nicOf(pinnedCI, "eth0"), host)
	e.pin(ip)
	observedCI = e.observe(fmt.Sprintf("OBS-%d", n), "", bindNIC("eth0", host))
	return ip, pinnedCI, observedCI
}

// TestPinnedAddressSurvivesDiscovery covers AK3 and AK4: another device
// reporting a pinned address writes no second row and leaves the binding
// as it is; the observation is reported as a binding conflict.
func TestPinnedAddressSurvivesDiscovery(t *testing.T) {
	e := newBindEnv(t)
	ip, pinnedCI, observedCI := e.pinnedConflict(1)
	if n := e.count(`SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.77.1.5'`, e.f.OrgA); n != 1 {
		t.Errorf("%d rows of 10.77.1.5, want the pinned one only", n)
	}
	got := e.call(http.MethodGet, "/api/v1/ips/"+ip, "", http.StatusOK)
	if got["ci_id"] != pinnedCI || got["interface_id"] != e.nicOf(pinnedCI, "eth0") || got["pinned"] != true || got["origin"] != "discovery" {
		t.Errorf("pinned address after the other observation: %v, want it at %s", got, pinnedCI)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE ci_id = $1`, observedCI); n != 0 {
		t.Errorf("observing device holds %d addresses, want none", n)
	}
	// A manual, not pinned address keeps the behavior before WP-914: the
	// other device gets a second row (and duplicate_ip after the commit).
	manualCI := e.observe("MAN-HOLD", "", bindNIC("eth0"))
	e.call(http.MethodPost, "/api/v1/ips", fmt.Sprintf(`{"address":"10.77.1.9","interface_id":%q}`, e.nicOf(manualCI, "eth0")),
		http.StatusCreated)
	e.observe("MAN-OBS", "", bindNIC("eth0", "10.77.1.9"))
	if n := e.count(`SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.77.1.9'`, e.f.OrgA); n != 2 {
		t.Errorf("%d rows of a manual unpinned address seen elsewhere, want 2", n)
	}
}

// TestManualAddressAttributesKept covers AK2 and AK3: POST writes origin
// manual whatever the body says; the sync keeps VRF, DNS name and
// description of a manual row and records only the observation.
func TestManualAddressAttributesKept(t *testing.T) {
	e := newBindEnv(t)
	device := e.observe("MAN-1", "", bindNIC("eth0"))
	nic := e.nicOf(device, "eth0")
	created := e.call(http.MethodPost, "/api/v1/ips", fmt.Sprintf(`{"address":"10.77.2.5","interface_id":%q,
		"dns_name":"core.example.net","description":"hand made"}`, nic), http.StatusCreated)
	if created["origin"] != "manual" || created["pinned"] != false {
		t.Fatalf("POST: origin %v pinned %v, want manual and false", created["origin"], created["pinned"])
	}
	id, vrf := created["id"].(string), created["vrf_id"].(string)
	if created["last_seen_at"] != nil {
		t.Fatalf("POST: last_seen_at %v before any observation", created["last_seen_at"])
	}
	other := e.str(`INSERT INTO vrf (organization_id, name) VALUES ($1, 'wp914-other') RETURNING id::text`, e.f.OrgA)
	e.observe("MAN-1", other, bindNIC("eth0", "10.77.2.5"))
	got := e.call(http.MethodGet, "/api/v1/ips/"+id, "", http.StatusOK)
	if got["vrf_id"] != vrf || got["dns_name"] != "core.example.net" || got["description"] != "hand made" ||
		got["interface_id"] != nic || got["ci_id"] != device || got["origin"] != "manual" {
		t.Errorf("manual address after the sync: %v, want VRF %s and its attributes", got, vrf)
	}
	if got["last_seen_at"] == nil {
		t.Error("manual address after the sync: last_seen_at not set")
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE interface_id = $1`, nic); n != 1 {
		t.Errorf("%d rows at the interface, want the manual one only", n)
	}
	// A manual address of the CI without interface (a maintained static
	// management address) is taken over by the interface of the same CI
	// that reports it (WP-913 AK2): no second row, the CI and the origin
	// manual stay. A row without interface holds no interface binding the
	// sync could overwrite.
	static := e.call(http.MethodPost, "/api/v1/ips", fmt.Sprintf(`{"address":"10.77.2.7","ci_id":%q}`, device),
		http.StatusCreated)["id"].(string)
	e.observe("MAN-1", "", bindNIC("eth0", "10.77.2.5", "10.77.2.7"))
	got = e.call(http.MethodGet, "/api/v1/ips/"+static, "", http.StatusOK)
	if got["interface_id"] != nic || got["ci_id"] != device || got["origin"] != "manual" || got["last_seen_at"] == nil {
		t.Errorf("manual address of the CI without interface after the sync: %v, want it at eth0 of the CI, manual, seen", got)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.77.2.7'`, e.f.OrgA); n != 1 {
		t.Errorf("%d rows of 10.77.2.7, want the manual one only", n)
	}
	// The client cannot set the origin.
	w := e.api.call(http.MethodPost, "/api/v1/ips", `{"address":"10.77.2.6","origin":"discovery"}`)
	if w.Code == http.StatusCreated {
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out["origin"] != "manual" {
			t.Errorf("POST with origin discovery: origin %v, want manual", out["origin"])
		}
	} else if w.Code != http.StatusBadRequest {
		t.Errorf("POST with origin: %d %s, want 201 manual or 400", w.Code, w.Body.String())
	}
}

// TestBindingConflictReviewOpensOnce covers AK5: two observations of the
// pinned address at the other device open exactly one item with the
// payload and both CIs as candidates.
func TestBindingConflictReviewOpensOnce(t *testing.T) {
	e := newBindEnv(t)
	ip, pinnedCI, observedCI := e.pinnedConflict(3)
	e.observe("OBS-3", "", bindNIC("eth0", "10.77.3.5"))
	open := e.items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open ip_binding_conflict items, want 1: %+v", len(open), open)
	}
	item := open[0]
	observedNIC := e.nicOf(observedCI, "eth0")
	if item.ObjectKey != "ipbind:"+ip+"/"+observedNIC {
		t.Errorf("object_key %q", item.ObjectKey)
	}
	want := []string{pinnedCI, observedCI}
	got := slices.Clone(item.CandidateCIIDs)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("candidates %v, want %v", got, want)
	}
	for k, v := range map[string]string{"address": "10.77.3.5", "vrf_id": e.str(`SELECT default_vrf_of($1)::text`, e.f.OrgA),
		"ip_address_id": ip, "pinned_ci_id": pinnedCI, "pinned_interface_id": e.nicOf(pinnedCI, "eth0"),
		"observed_ci_id": observedCI, "observed_interface_id": observedNIC} {
		if item.Payload[k] != v {
			t.Errorf("payload %s = %v, want %s", k, item.Payload[k], v)
		}
	}
	// Another interface of the same device is another pair.
	e.observe("OBS-3", "", bindNIC("eth0", "10.77.3.5"), bindNIC("eth1", "10.77.3.5"))
	if n := len(e.items(reviews.StatusOpen)); n != 2 {
		t.Errorf("%d open items after a second interface, want 2", n)
	}
}

// TestBindingConflictAccept covers AK6: accept binds the address to the
// observed interface, its CI follows, the pin is released and the IPAM
// history names the source discovery.
func TestBindingConflictAccept(t *testing.T) {
	e := newBindEnv(t)
	ip, _, observedCI := e.pinnedConflict(4)
	open := e.items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open items, want 1", len(open))
	}
	if _, err := e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionAccept, ResolvedBy: e.f.User}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	got := e.call(http.MethodGet, "/api/v1/ips/"+ip, "", http.StatusOK)
	if got["interface_id"] != e.nicOf(observedCI, "eth0") || got["ci_id"] != observedCI || got["pinned"] != false {
		t.Errorf("after accept: %v, want the address at the observed interface, not pinned", got)
	}
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE object_id = $1 AND source = 'discovery' AND change_type = 'update'
			AND field_name IN ('interface_id', 'ci_id', 'pinned')`, ip); n < 3 {
		t.Errorf("%d history rows of source discovery for interface, CI and pin, want 3", n)
	}
	// The next observation updates the row at the observed interface; no
	// further item opens.
	e.observe("OBS-4", "", bindNIC("eth0", "10.77.4.5"))
	if n := len(e.items(reviews.StatusOpen)); n != 0 {
		t.Errorf("%d open items after accept, want none", n)
	}
	// An item whose address is gone is refused with 422 and stays open.
	ip2, _, _ := e.pinnedConflict(14)
	open = e.items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open items, want 1", len(open))
	}
	e.call(http.MethodDelete, "/api/v1/ips/"+ip2, "", http.StatusNoContent)
	_, err := e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionAccept, ResolvedBy: e.f.User})
	var refused *reviews.Error
	if !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity {
		t.Errorf("accept of a removed address: %v, want 422", err)
	}
	if n := len(e.items(reviews.StatusOpen)); n != 1 {
		t.Errorf("refused accept: %d open items, want the item open", n)
	}
	if _, err = e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionDismiss, ResolvedBy: e.f.User}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	// A stale item: the pin was released since it opened. Accept is
	// refused with 422 and leaves the address where the user put it.
	ip3, pinnedCI3, _ := e.pinnedConflict(15)
	open = e.items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open items, want 1", len(open))
	}
	e.call(http.MethodPatch, "/api/v1/ips/"+ip3, `{"pinned":false}`, http.StatusOK)
	_, err = e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionAccept, ResolvedBy: e.f.User})
	if !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity {
		t.Errorf("accept after the pin was released: %v, want 422", err)
	}
	if got = e.call(http.MethodGet, "/api/v1/ips/"+ip3, "", http.StatusOK); got["ci_id"] != pinnedCI3 {
		t.Errorf("refused stale accept moved the address: %v", got)
	}
	if _, err = e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionDismiss, ResolvedBy: e.f.User}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	// The observed interface meanwhile holds the address in a manual row:
	// 422 instead of a unique violation.
	ip4, _, observedCI4 := e.pinnedConflict(16)
	open = e.items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open items, want 1", len(open))
	}
	e.str(`INSERT INTO ip_address (organization_id, interface_id, address, vrf_id) SELECT organization_id, $2, address, vrf_id
		FROM ip_address WHERE id = $1 RETURNING id::text`, ip4, e.nicOf(observedCI4, "eth0"))
	_, err = e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionAccept, ResolvedBy: e.f.User})
	if !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity {
		t.Errorf("accept onto an interface holding the address: %v, want 422", err)
	}
}

// TestDiscoveredManagementAddressFollowsDiscovery covers AK2 and AK3 for
// the management address of a record: the discovery writes it with origin
// discovery, an interface reporting it takes it over, and once neither the
// record nor the interface reports it, three misses remove it (E-90).
func TestDiscoveredManagementAddressFollowsDiscovery(t *testing.T) {
	e := newBindEnvWith(t, true)
	device := e.observeAt("MGMT-1", "10.77.9.10", "")
	id := e.str(`SELECT id::text FROM ip_address WHERE ci_id = $1 AND address_host = '10.77.9.10'`, device)
	got := e.call(http.MethodGet, "/api/v1/ips/"+id, "", http.StatusOK)
	if got["origin"] != "discovery" || got["interface_id"] != nil {
		t.Fatalf("management address of the discovery: %v, want origin discovery without interface", got)
	}
	e.observeAt("MGMT-1", "10.77.9.10", "", bindNIC("eth0", "10.77.9.10"))
	got = e.call(http.MethodGet, "/api/v1/ips/"+id, "", http.StatusOK)
	if got["interface_id"] != e.nicOf(device, "eth0") || got["origin"] != "discovery" {
		t.Fatalf("management address after the interface reported it: %v, want it at eth0", got)
	}
	filtered := e.call(http.MethodGet, "/api/v1/ips?q=10.77.9.10&origin=discovery", "", http.StatusOK)["data"].([]any)
	if len(filtered) != 1 {
		t.Errorf("GET /ips?origin=discovery: %v, want the management address", filtered)
	}
	for range ipam.MaxMissedObservations {
		e.observe("MGMT-1", "", bindNIC("eth0"))
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE id = $1`, id); n != 0 {
		t.Error("management address of the discovery kept after three misses")
	}
}

// TestBindingConflictDismiss: dismiss keeps the pinned binding, and the
// same pair observed again opens no new item.
func TestBindingConflictDismiss(t *testing.T) {
	e := newBindEnv(t)
	ip, pinnedCI, observedCI := e.pinnedConflict(5)
	open := e.items(reviews.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("%d open items, want 1", len(open))
	}
	if _, err := e.disc.ReviewService().Resolve(e.ctx, e.f.OrgA, open[0].ID,
		&reviews.Resolution{Action: reviews.ActionDismiss, ResolvedBy: e.f.User}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	got := e.call(http.MethodGet, "/api/v1/ips/"+ip, "", http.StatusOK)
	if got["ci_id"] != pinnedCI || got["pinned"] != true {
		t.Errorf("after dismiss: %v, want the pinned binding", got)
	}
	e.observe("OBS-5", "", bindNIC("eth0", "10.77.5.5"))
	if n := len(e.items(reviews.StatusOpen)); n != 0 {
		t.Errorf("%d open items after dismiss and the same observation, want none", n)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE ci_id = $1`, observedCI); n != 0 {
		t.Errorf("observing device holds %d addresses after dismiss, want none", n)
	}
}

// TestMissesRemoveOnlyDiscoveryOrigin covers AK3: three observations
// without the addresses remove only the row of origin discovery that is
// not pinned; the manual row and the pinned row stay.
func TestMissesRemoveOnlyDiscoveryOrigin(t *testing.T) {
	e := newBindEnv(t)
	device := e.observe("MISS-1", "", bindNIC("eth0", "10.77.6.1", "10.77.6.2"))
	nic := e.nicOf(device, "eth0")
	discovered, pinned := e.addressAt(nic, "10.77.6.1"), e.addressAt(nic, "10.77.6.2")
	e.pin(pinned)
	manual := e.call(http.MethodPost, "/api/v1/ips", fmt.Sprintf(`{"address":"10.77.6.3","interface_id":%q,"type":"discovered"}`, nic),
		http.StatusCreated)["id"].(string)
	for range ipam.MaxMissedObservations {
		e.observe("MISS-1", "", bindNIC("eth0"))
	}
	exists := func(id string) bool {
		return e.count(`SELECT count(*) FROM ip_address WHERE id = $1`, id) == 1
	}
	if exists(discovered) {
		t.Error("address of origin discovery kept after three misses")
	}
	if !exists(pinned) {
		t.Error("pinned address removed")
	}
	if !exists(manual) {
		t.Error("manual address (type discovered) removed")
	}
	if n := e.count(`SELECT miss_count FROM ip_address WHERE id = $1`, pinned); n != 0 {
		t.Errorf("pinned address counted %d misses, want none", n)
	}
}

// TestOriginBackfill covers AK1: the up migration sets origin discovery on
// rows of type discovered and manual on every other one, without new
// history rows; run inside a rolled-back transaction from the schema of
// the down migration.
func TestOriginBackfill(t *testing.T) {
	e := newBindEnv(t)
	read := func(suffix string) string {
		t.Helper()
		files, err := filepath.Glob("../../migrations/*_ip_address_origin." + suffix + ".sql")
		if err != nil || len(files) != 1 {
			t.Fatalf("migration ip_address_origin %s: %v %v", suffix, files, err)
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	bg := context.Background()
	tx, err := e.f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	ids := map[string]string{}
	for _, typ := range []string{"discovered", "static", "reserved", "dhcp"} {
		var id string
		if err = tx.QueryRow(bg, `INSERT INTO ip_address (organization_id, address, type) VALUES ($1, $2::inet, $3) RETURNING id::text`,
			e.f.OrgA, fmt.Sprintf("10.77.7.%d", len(ids)+1), typ).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", typ, err)
		}
		ids[typ] = id
	}
	var history int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM ipam_change WHERE organization_id = $1`, e.f.OrgA).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, read("up")); err != nil {
		t.Fatalf("up: %v", err)
	}
	for typ, id := range ids {
		want := "manual"
		if typ == "discovered" {
			want = "discovery"
		}
		var origin string
		var pinned bool
		if err = tx.QueryRow(bg, `SELECT origin, pinned FROM ip_address WHERE id = $1`, id).Scan(&origin, &pinned); err != nil {
			t.Fatal(err)
		}
		if origin != want || pinned {
			t.Errorf("type %s: origin %s pinned %v, want %s and false", typ, origin, pinned, want)
		}
	}
	var after int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM ipam_change WHERE organization_id = $1`, e.f.OrgA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != history {
		t.Errorf("backfill wrote %d history rows, want none", after-history)
	}
	if _, err = tx.Exec(bg, `INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'ip_binding_conflict', 'open', '{}')`,
		e.f.OrgA); err != nil {
		t.Errorf("review kind ip_binding_conflict refused: %v", err)
	}
}

// TestIPFilterOriginPinned covers AK2 and IPM-20: GET /ips filters by
// origin and pinned; an unknown origin or a pinned value that is no
// boolean is 422 with the field.
func TestIPFilterOriginPinned(t *testing.T) {
	e := newBindEnv(t)
	device := e.observe("FILTER-1", "", bindNIC("eth0", "10.77.8.1", "10.77.8.2"))
	nic := e.nicOf(device, "eth0")
	pinned := e.addressAt(nic, "10.77.8.2")
	e.pin(pinned)
	manual := e.call(http.MethodPost, "/api/v1/ips", `{"address":"10.77.8.3"}`, http.StatusCreated)["id"].(string)
	list := func(query string) []string {
		t.Helper()
		out := e.call(http.MethodGet, "/api/v1/ips?q=10.77.8.&"+query, "", http.StatusOK)
		var ids []string
		for _, raw := range out["data"].([]any) {
			ids = append(ids, raw.(map[string]any)["id"].(string))
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	for query, want := range map[string][]string{
		"origin=discovery":              sorted(e.addressAt(nic, "10.77.8.1"), pinned),
		"origin=manual":                 {manual},
		"pinned=true":                   {pinned},
		"origin=discovery&pinned=false": {e.addressAt(nic, "10.77.8.1")},
		"origin=scan":                   nil,
	} {
		if got := list(query); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", query, got, want)
		}
	}
	for query, field := range map[string]string{"origin=robot": "origin", "pinned=maybe": "pinned"} {
		w := e.api.call(http.MethodGet, "/api/v1/ips?"+query, "")
		var problem struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || problem.Field != field {
			t.Errorf("%s: %d %s, want 422 %s", query, w.Code, w.Body.String(), field)
		}
	}
}
