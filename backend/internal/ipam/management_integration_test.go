package ipam_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// mgmtEnv is the scenario of the management IP (IPM-13, WP-913, E-215):
// the CI repository with the synchronisation wired as router.go does, the
// CI and IPAM routes on one mux, an org-wide principal of organization A.
//
// The tests run only against PostgreSQL (TEST_DATABASE_URL, see
// scopetest.Seed).
type mgmtEnv struct {
	t       *testing.T
	f       *scopetest.Fixture
	ctx     context.Context
	cis     *ci.PGRepository
	sync    *ipam.ManagementSync
	reviews *reviews.Service
	mux     *chi.Mux
	vrf     string
}

func newMgmtEnv(t *testing.T) *mgmtEnv {
	t.Helper()
	f := scopetest.Seed(t, "9b")
	svc := reviews.NewService(reviews.NewPGRepository(f.App))
	ipamRepo := ipam.NewPGRepository(f.App)
	e := &mgmtEnv{t: t, f: f, reviews: svc, mux: chi.NewRouter(),
		ctx: tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})}
	e.sync = ipam.NewManagementSync(ipamRepo, svc)
	e.cis = ci.NewPGRepository(f.App).WithManagementSync(e.sync)
	ci.NewHandler(ci.NewService(e.cis)).RegisterRoutes(e.mux)
	ipam.NewHandler(ipamRepo).WithReviews(svc).RegisterRoutes(e.mux)
	e.vrf = e.scalar(`SELECT default_vrf_of($1)::text`, f.OrgA)
	return e
}

func (e *mgmtEnv) call(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(e.ctx))
	return w
}

func (e *mgmtEnv) must(method, path, body string, status int) {
	e.t.Helper()
	if w := e.call(method, path, body); w.Code != status {
		e.t.Fatalf("%s %s %s = %d %s, want %d", method, path, body, w.Code, w.Body.String(), status)
	}
}

func (e *mgmtEnv) scalar(q string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), q, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return out
}

// rows lists the addresses of a host in the default VRF as
// "ci|role|type|subnet|interface" (empty parts for NULL).
func (e *mgmtEnv) rows(host string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT COALESCE(ci_id::text, '') || '|' || role || '|' || type || '|' ||
			COALESCE(subnet_id::text, '') || '|' || COALESCE(interface_id::text, '')
		FROM ip_address WHERE organization_id = $1 AND vrf_id = $2 AND address_host = $3 ORDER BY 1`, e.f.OrgA, e.vrf, host)
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *mgmtEnv) wantRows(what, host string, want ...string) {
	e.t.Helper()
	if got := e.rows(host); !slices.Equal(got, want) {
		e.t.Errorf("%s: rows of %s = %q, want %q", what, host, got, want)
	}
}

// setIP writes the management IP of a CI through the repository as the
// ingest does (UpdateRecord).
func (e *mgmtEnv) record(ciID, ip string) {
	e.t.Helper()
	if _, err := e.cis.UpdateRecord(e.ctx, e.f.OrgA, ciID, &ci.RecordUpdate{Update: ci.UpdateRequest{ManagementIP: &ip}}); err != nil {
		e.t.Fatalf("record management IP %s: %v", ip, err)
	}
}

// TestRESTManagementIPCreatesAddress covers AK1, AK2 and AK5: PATCH
// /cis/{id} with a management IP creates the address without interface
// with ci_id, role management, type static and the narrowest subnet,
// recorded as manual; a rollback of the CI write drops it; clearing the
// management IP only takes the role away.
func TestRESTManagementIPCreatesAddress(t *testing.T) {
	e := newMgmtEnv(t)
	e.must(http.MethodPost, "/api/v1/subnets", `{"cidr":"10.30.0.0/16"}`, http.StatusCreated)
	s24 := e.scalar(`INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.30.1.0/24') RETURNING id::text`, e.f.OrgA)
	host := e.f.CI(t, e.f.OrgA, "", "mgmt-rest")
	e.must(http.MethodPatch, "/api/v1/cis/"+host, `{"management_ip":"10.30.1.10"}`, http.StatusOK)
	e.wantRows("after PATCH", "10.30.1.10", host+"|management|static|"+s24+"|")
	if n := e.scalar(`SELECT count(*)::text FROM ipam_change c JOIN ip_address a ON a.id = c.object_id
		WHERE a.address_host = '10.30.1.10' AND c.change_type = 'create' AND c.source = 'manual'`); n != "1" {
		t.Errorf("history of the created address: %s manual rows, want 1", n)
	}

	// A CI write that rolls back takes the address along.
	errRollback := errors.New("rollback")
	err := database.WithRequestTenant(e.ctx, e.f.App, e.f.OrgA, func(ctx context.Context, _ pgx.Tx) error {
		ip := "10.30.1.11"
		if _, uerr := e.cis.Update(ctx, e.f.OrgA, host, ci.UpdateRequest{ManagementIP: &ip}); uerr != nil {
			return uerr
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("rolled back write: %v", err)
	}
	e.wantRows("after the rollback", "10.30.1.11")
	e.wantRows("after the rollback", "10.30.1.10", host+"|management|static|"+s24+"|")

	// An empty management IP takes only the role.
	e.must(http.MethodPatch, "/api/v1/cis/"+host, `{"management_ip":""}`, http.StatusOK)
	e.wantRows("after clearing", "10.30.1.10", host+"|primary|static|"+s24+"|")
	// A write without change of the management IP leaves the addresses.
	e.must(http.MethodPatch, "/api/v1/cis/"+host, `{"name":"mgmt-rest-renamed"}`, http.StatusOK)
	e.wantRows("after a write without the IP", "10.30.1.10", host+"|primary|static|"+s24+"|")
}

// TestIngestManagementIPMovesRole covers AK2 and AK5 for the ingest
// (UpdateRecord): the new address is discovered and recorded as discovery;
// on a change the former address becomes primary, the new one management.
func TestIngestManagementIPMovesRole(t *testing.T) {
	e := newMgmtEnv(t)
	host := e.f.CI(t, e.f.OrgA, "", "mgmt-ingest")
	e.record(host, "10.31.0.20")
	e.wantRows("first record", "10.31.0.20", host+"|management|discovered||")
	if n := e.scalar(`SELECT count(*)::text FROM ipam_change c JOIN ip_address a ON a.id = c.object_id
		WHERE a.address_host = '10.31.0.20' AND c.change_type = 'create' AND c.source = 'discovery'`); n != "1" {
		t.Errorf("history of the discovered address: %s discovery rows, want 1", n)
	}
	e.record(host, "10.31.0.21")
	e.wantRows("former address", "10.31.0.20", host+"|primary|discovered||")
	e.wantRows("new address", "10.31.0.21", host+"|management|discovered||")
	// The same record again adds nothing.
	e.record(host, "10.31.0.21")
	if n := e.scalar(`SELECT count(*)::text FROM ip_address WHERE ci_id = $1`, host); n != "2" {
		t.Errorf("addresses of the CI after a repeated record = %s, want 2", n)
	}
}

// TestManagementIPOnOwnInterface covers AK2: the address at an interface of
// the CI takes the role; no second row is created.
func TestManagementIPOnOwnInterface(t *testing.T) {
	e := newMgmtEnv(t)
	host := e.f.CI(t, e.f.OrgA, "", "mgmt-nic")
	nic := e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`,
		e.f.OrgA, host)
	e.must(http.MethodPost, "/api/v1/ips", `{"address":"10.32.0.5","interface_id":"`+nic+`"}`, http.StatusCreated)
	e.must(http.MethodPatch, "/api/v1/cis/"+host, `{"management_ip":"10.32.0.5"}`, http.StatusOK)
	e.wantRows("own interface", "10.32.0.5", host+"|management|static||"+nic)
}

// TestManagementIPBeforeOwnInterface covers AK2 in the order of the
// ingest, which writes the management IP before the interfaces: the row
// without interface the synchronisation created moves to the interface the
// discovery then reports, with its role, so there is no second row; the
// duplicate check finds nothing, also for a management IP set by REST.
func TestManagementIPBeforeOwnInterface(t *testing.T) {
	e := newMgmtEnv(t)
	repo := ipam.NewPGRepository(e.f.App)
	for i, how := range []string{"ingest", "rest"} {
		host := e.f.CI(t, e.f.OrgA, "", "mgmt-first-"+how)
		ip := fmt.Sprintf("10.37.%d.5", i)
		kind := "discovered"
		if how == "ingest" {
			e.record(host, ip)
		} else {
			e.must(http.MethodPatch, "/api/v1/cis/"+host, `{"management_ip":"`+ip+`"}`, http.StatusOK)
			kind = "static"
		}
		e.wantRows(how+": before the interfaces", ip, host+"|management|"+kind+"||")
		if _, err := repo.SyncDiscoveredInterfaces(e.ctx, e.f.OrgA, host, []ipam.ObservedInterface{{Name: "eth0", IPs: []string{ip + "/24"}}}); err != nil {
			t.Fatalf("%s: sync interfaces: %v", how, err)
		}
		nic := e.scalar(`SELECT id::text FROM network_interface WHERE ci_id = $1 AND name = 'eth0'`, host)
		e.wantRows(how+": after the interfaces", ip, host+"|management|"+kind+"||"+nic)
		if err := ipam.CheckDuplicates(e.ctx, repo, e.reviews, e.f.OrgA,
			ipam.DuplicateSource{Keys: []ipam.DuplicateKey{{VRFID: e.vrf, Host: ip}}}); err != nil {
			t.Fatal(err)
		}
		items, _, err := e.reviews.List(e.ctx, e.f.OrgA, reviews.Filter{Kind: reviews.KindDuplicateIP, ObjectKey: "ip:" + e.vrf + "/" + ip},
			api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Errorf("%s: duplicate_ip items for the CI's own address: %+v", how, items)
		}
	}
}

// TestManagementIPHeldByOtherCI covers AK4: another CI holding the address
// without interface keeps its row and no second one is created; another CI
// holding it at an interface gets a row of the CI next to it. Neither
// writer gets an error, and after the commit a duplicate_ip item names
// both CIs.
func TestManagementIPHeldByOtherCI(t *testing.T) {
	e := newMgmtEnv(t)
	wantItem := func(host string, cis ...string) {
		t.Helper()
		items, _, err := e.reviews.List(e.ctx, e.f.OrgA, reviews.Filter{Kind: reviews.KindDuplicateIP, ObjectKey: "ip:" + e.vrf + "/" + host},
			api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 {
			t.Fatalf("duplicate_ip items of %s: %+v, want one", host, items)
		}
		got := slices.Clone(items[0].CandidateCIIDs)
		slices.Sort(got)
		slices.Sort(cis)
		if !slices.Equal(got, cis) {
			t.Errorf("candidates of %s = %v, want %v", host, got, cis)
		}
	}

	// Without interface: the VIP of CI B stays the only row.
	a := e.f.CI(t, e.f.OrgA, "", "mgmt-a")
	b := e.f.CI(t, e.f.OrgA, "", "mgmt-b")
	e.must(http.MethodPost, "/api/v1/ips", `{"address":"10.33.0.5","ci_id":"`+b+`","role":"vip"}`, http.StatusCreated)
	e.must(http.MethodPatch, "/api/v1/cis/"+a, `{"management_ip":"10.33.0.5"}`, http.StatusOK)
	e.wantRows("held without interface", "10.33.0.5", b+"|vip|static||")
	wantItem("10.33.0.5", a, b)

	// At an interface of CI C: the CI gets its own row next to it.
	c := e.f.CI(t, e.f.OrgA, "", "mgmt-c")
	nic := e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`,
		e.f.OrgA, c)
	e.must(http.MethodPost, "/api/v1/ips", `{"address":"10.33.1.5","interface_id":"`+nic+`"}`, http.StatusCreated)
	d := e.f.CI(t, e.f.OrgA, "", "mgmt-d")
	e.record(d, "10.33.1.5")
	e.wantRows("held at an interface", "10.33.1.5", c+"|primary|static||"+nic, d+"|management|discovered||")
	wantItem("10.33.1.5", c, d)
}

// TestMergeKeepsManagementAddress covers AK1 for the merge: the survivor
// keeps its management IP with role management, the victim's management
// address moves to the survivor with role primary.
func TestMergeKeepsManagementAddress(t *testing.T) {
	e := newMgmtEnv(t)
	survivor := e.f.CI(t, e.f.OrgA, "", "mgmt-survivor")
	victim := e.f.CI(t, e.f.OrgA, "", "mgmt-victim")
	e.must(http.MethodPatch, "/api/v1/cis/"+survivor, `{"management_ip":"10.34.0.1"}`, http.StatusOK)
	e.must(http.MethodPatch, "/api/v1/cis/"+victim, `{"management_ip":"10.34.0.2"}`, http.StatusOK)
	if _, err := e.cis.Merge(e.ctx, e.f.OrgA, survivor, victim, ""); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if ip := e.scalar(`SELECT host(management_ip) FROM ci WHERE id = $1`, survivor); ip != "10.34.0.1" {
		t.Fatalf("management IP of the survivor = %s", ip)
	}
	e.wantRows("survivor's address", "10.34.0.1", survivor+"|management|static||")
	e.wantRows("victim's address", "10.34.0.2", survivor+"|primary|static||")
}

// TestMgmtBackfillJobIsIdempotent covers AK6: the job brings every CI with a
// management IP in line across the batch boundary of 500; a second run
// changes nothing.
func TestMgmtBackfillJobIsIdempotent(t *testing.T) {
	e := newMgmtEnv(t)
	const n = 502
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO ci (organization_id, ci_type_id, name, status, management_ip)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'mgmt-backfill-' || i, 'active',
		       ('10.35.' || (i / 250) || '.' || (i % 250 + 1))::inet
		  FROM generate_series(1, $2::int) i`, e.f.OrgA, n); err != nil {
		t.Fatal(err)
	}
	// One CI is in line already.
	first := e.scalar(`SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'mgmt-backfill-1'`, e.f.OrgA)
	e.must(http.MethodPatch, "/api/v1/cis/"+first, `{"management_ip":"10.36.0.1"}`, http.StatusOK)
	// The management IP of another CI is the VIP of a third without
	// interface (AK4): synchronised once, then in line.
	holder := e.f.CI(t, e.f.OrgA, "", "mgmt-holder")
	e.must(http.MethodPost, "/api/v1/ips", `{"address":"10.36.1.1","ci_id":"`+holder+`","role":"vip"}`, http.StatusCreated)
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO ci (organization_id, ci_type_id, name, status, management_ip)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'mgmt-held', 'active', '10.36.1.1'`,
		e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	job := &jobs.Job{OrganizationID: e.f.OrgA, Kind: ipam.MgmtBackfillJobKind}
	result, err := e.sync.RunBackfillJob(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["synchronised"]; got != n {
		t.Errorf("first run synchronised %v CIs, want %d", got, n)
	}
	e.wantRows("address held by another CI", "10.36.1.1", holder+"|vip|static||")
	if got := e.scalar(`SELECT count(*)::text FROM ip_address a JOIN ci c ON c.id = a.ci_id
		WHERE c.organization_id = $1 AND c.name LIKE 'mgmt-backfill-%' AND a.role = 'management'
		  AND a.address_host = host(c.management_ip)`, e.f.OrgA); got != "502" {
		t.Errorf("management addresses after the backfill = %s, want 502", got)
	}
	changes := e.scalar(`SELECT count(*)::text FROM ipam_change WHERE organization_id = $1`, e.f.OrgA)
	result, err = e.sync.RunBackfillJob(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["synchronised"]; got != 0 {
		t.Errorf("second run synchronised %v CIs, want 0", got)
	}
	if again := e.scalar(`SELECT count(*)::text FROM ipam_change WHERE organization_id = $1`, e.f.OrgA); again != changes {
		t.Errorf("second run wrote history: %s rows, before %s", again, changes)
	}
}

// TestCreateManyManagementAddressesSetBased covers AK1 and AK3 for the
// batch: CreateMany synchronises the management addresses of all its new
// CIs at once, each in its VRF (attributes.raw_data.vrf_id, else the
// default VRF); both VRF locks are taken in one statement in the order of
// their ids.
func TestCreateManyManagementAddressesSetBased(t *testing.T) {
	e := newMgmtEnv(t)
	other := e.scalar(`INSERT INTO vrf (organization_id, name) VALUES ($1, 'mgmt-batch') RETURNING id::text`, e.f.OrgA)
	typeID := e.scalar(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	items := []*ci.Item{
		{OrganizationID: e.f.OrgA, CITypeID: typeID, Name: "mgmt-batch-1", Status: "active", ManagementIP: "10.38.0.1"},
		{OrganizationID: e.f.OrgA, CITypeID: typeID, Name: "mgmt-batch-2", Status: "active", ManagementIP: "10.38.0.2",
			Attributes: map[string]any{"raw_data": map[string]any{"vrf_id": other}}},
		{OrganizationID: e.f.OrgA, CITypeID: typeID, Name: "mgmt-batch-3", Status: "active"},
	}
	res, err := e.cis.CreateMany(e.ctx, e.f.OrgA, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("refused: %+v", res.Refused)
	}
	got := e.scalar(`SELECT string_agg(c.name || '|' || a.role || '|' || (a.vrf_id = $2)::text, ',' ORDER BY c.name)
		FROM ip_address a JOIN ci c ON c.id = a.ci_id WHERE c.organization_id = $1 AND c.name LIKE 'mgmt-batch-%'`, e.f.OrgA, other)
	if want := "mgmt-batch-1|management|false,mgmt-batch-2|management|true"; got != want {
		t.Errorf("management addresses of the batch = %s, want %s", got, want)
	}
}
