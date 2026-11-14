package ipam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestVRFModel covers WP-082 (TEN-10, CH10, NET-02, NET-03) against
// PostgreSQL:
//   - every organization gets exactly one default VRF "global" on creation;
//     it cannot be deleted; VRF names are unique per organization;
//   - subnets are unique per (organization, VRF, CIDR) and take the default
//     VRF when none is given;
//   - IP addresses carry the VRF of their subnet and are unique per VRF;
//     moving a subnet moves its addresses; a VRF in use cannot be deleted;
//   - VRF writes require vrf:manage.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVRFModel(t *testing.T) {
	f := scopetest.Seed(t, "75")
	ctx := f.OrgCtx(f.OrgA)
	repo := ipam.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 50}

	vrfs, _, err := repo.ListVRFs(ctx, f.OrgA, ipam.VRFFilter{}, page)
	if err != nil {
		t.Fatal(err)
	}
	if len(vrfs) != 1 || vrfs[0].Name != "global" || !vrfs[0].IsDefault {
		t.Fatalf("VRFs of a new organization: %+v, want only the default global", vrfs)
	}
	global := vrfs[0]
	if bVRFs, _, _ := repo.ListVRFs(f.OrgCtx(f.OrgB), f.OrgB, ipam.VRFFilter{}, page); len(bVRFs) != 1 || bVRFs[0].ID == global.ID {
		t.Errorf("organization B VRFs: %+v", bVRFs)
	}

	customer := &ipam.VRF{OrganizationID: f.OrgA, Name: "customer-a"}
	if err = repo.CreateVRF(ctx, customer); err != nil {
		t.Fatal(err)
	}
	if customer.IsDefault {
		t.Error("a created VRF is the default")
	}
	if err = repo.CreateVRF(ctx, &ipam.VRF{OrganizationID: f.OrgA, Name: "customer-a"}); err == nil {
		t.Error("duplicate VRF name accepted")
	}
	if err = repo.DeleteVRF(ctx, f.OrgA, global.ID); err == nil {
		t.Error("the default VRF was deleted")
	}

	// Subnets: unique per VRF, default VRF when none is given.
	defaultSubnet := &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.75.0.0/24"}
	if err = repo.CreateSubnet(ctx, defaultSubnet); err != nil {
		t.Fatal(err)
	}
	if defaultSubnet.VRFID != global.ID {
		t.Errorf("subnet without VRF: %q, want the default %q", defaultSubnet.VRFID, global.ID)
	}
	customerSubnet := &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.75.0.0/24", VRFID: customer.ID}
	if err = repo.CreateSubnet(ctx, customerSubnet); err != nil {
		t.Errorf("same CIDR in another VRF: %v", err)
	}
	if err = repo.CreateSubnet(ctx, &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.75.0.0/24"}); err == nil {
		t.Error("duplicate CIDR in the default VRF accepted")
	}

	// IP addresses: VRF of the subnet, unique per VRF.
	a1 := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: defaultSubnet.ID, Address: "10.75.0.10", Type: "static"}
	if err = repo.CreateIPAddress(ctx, a1); err != nil {
		t.Fatal(err)
	}
	a2 := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: customerSubnet.ID, Address: "10.75.0.10", Type: "static"}
	if err = repo.CreateIPAddress(ctx, a2); err != nil {
		t.Errorf("same address in another VRF: %v", err)
	}
	if a1.VRFID != global.ID || a2.VRFID != customer.ID {
		t.Errorf("address VRFs: %q, %q", a1.VRFID, a2.VRFID)
	}
	if err = repo.CreateIPAddress(ctx, &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: defaultSubnet.ID,
		Address: "10.75.0.10", Type: "static"}); err == nil {
		t.Error("duplicate address in the same VRF accepted")
	}

	// Moving the default subnet into a third VRF moves its address.
	third := &ipam.VRF{OrganizationID: f.OrgA, Name: "lab"}
	if err = repo.CreateVRF(ctx, third); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateSubnet(ctx, f.OrgA, defaultSubnet.ID, &ipam.UpdateSubnetRequest{VRFID: &third.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetIPAddress(ctx, f.OrgA, a1.ID); got == nil || got.VRFID != third.ID {
		t.Errorf("address after moving its subnet: %+v", got)
	}
	if err = repo.DeleteVRF(ctx, f.OrgA, third.ID); err == nil {
		t.Error("a VRF with subnets was deleted")
	}
	renamed := "lab-2"
	if v, renameErr := repo.UpdateVRF(ctx, f.OrgA, third.ID, ipam.UpdateVRFRequest{Name: &renamed}); renameErr != nil || v.Name != renamed {
		t.Errorf("rename: %+v, %v", v, renameErr)
	}

	// The HTTP contract of /api/v1/vrfs against PostgreSQL (WP-125): a taken
	// name is 409, the default VRF and a VRF with subnets cannot be
	// deleted (422).
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	call := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	for name, tc := range map[string]struct {
		method, path, body string
		status             int
	}{
		"taken name":            {http.MethodPost, "/api/v1/vrfs", `{"name":"lab-2"}`, http.StatusConflict},
		"rename onto taken":     {http.MethodPatch, "/api/v1/vrfs/" + third.ID, `{"name":"global"}`, http.StatusConflict},
		"delete default":        {http.MethodDelete, "/api/v1/vrfs/" + global.ID, "", http.StatusUnprocessableEntity},
		"delete with subnets":   {http.MethodDelete, "/api/v1/vrfs/" + third.ID, "", http.StatusUnprocessableEntity},
		"read":                  {http.MethodGet, "/api/v1/vrfs/" + third.ID, "", http.StatusOK},
		"read another org's id": {http.MethodGet, "/api/v1/vrfs/" + f.ID(), "", http.StatusNotFound},
	} {
		if got := call(tc.method, tc.path, tc.body); got != tc.status {
			t.Errorf("%s: %d, want %d", name, got, tc.status)
		}
	}

	// Writes need vrf:manage, reads ipam:read.
	table, err := os.ReadFile("../server/testdata/route_permissions.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"POST /api/v1/vrfs vrf:manage", "PATCH /api/v1/vrfs/{id} vrf:manage",
		"DELETE /api/v1/vrfs/{id} vrf:manage", "GET /api/v1/vrfs ipam:read", "GET /api/v1/vrfs/{id} ipam:read",
		"POST /api/v1/vlans ipam:write", "PATCH /api/v1/vlans/{id} ipam:write", "DELETE /api/v1/vlans/{id} ipam:write",
		"GET /api/v1/vlans ipam:read", "GET /api/v1/vlans/{id} ipam:read"} {
		if !strings.Contains(string(table), line+"\n") {
			t.Errorf("route table lacks %q", line)
		}
	}
}

// TestAuditT3VRFWritesNeedOrgScope covers WP-082 (TEN-10, TEN-05, CH10;
// F-089, WP-372, E-09): a VRF is org-wide, so a principal restricted in the
// client or site scope, or to an empty client set (E-08), neither creates
// nor renames (the default "global" included) nor deletes one. CreateVRF
// fails with the row level security error 42501 (403 at the API),
// UpdateVRF and DeleteVRF find no row (ErrNotFound, 404); the VRF table is
// unchanged. Reading the VRFs of the organization stays allowed (control).
// The org-wide principal creates, renames and deletes a non-default VRF; the
// default VRF still cannot be deleted (422).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditT3VRFWritesNeedOrgScope(t *testing.T) {
	f := scopetest.Seed(t, "fd")
	repo := ipam.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 50}
	org := f.OrgCtx(f.OrgA)
	c1 := f.ClientCtx(f.Client1)
	orgVRF := &ipam.VRF{OrganizationID: f.OrgA, Name: "audit-t3-org-vrf"}
	if err := repo.CreateVRF(org, orgVRF); err != nil {
		t.Fatalf("control: org-wide CreateVRF: %v", err)
	}
	vrfs, _, err := repo.ListVRFs(c1, f.OrgA, ipam.VRFFilter{}, page)
	if err != nil {
		t.Fatalf("control: client-1 ListVRFs: %v", err)
	}
	var global *ipam.VRF
	for i := range vrfs {
		if vrfs[i].IsDefault {
			global = &vrfs[i]
		}
	}
	if global == nil {
		t.Fatalf("control: the client-1 principal does not see the default VRF (read must stay allowed)")
	}

	t.Run("CreateVRF", func(t *testing.T) {
		err := repo.CreateVRF(c1, &ipam.VRF{OrganizationID: f.OrgA, Name: "audit-t3-scoped-vrf"})
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf WHERE organization_id = $1 AND name = 'audit-t3-scoped-vrf'`, f.OrgA); !vrfRLSDenied(err) || n != "0" {
			t.Errorf("client-1 CreateVRF: err=%v rows=%s; want the row level security error 42501 and no org-wide row", err, n)
		}
	})
	t.Run("UpdateVRF default", func(t *testing.T) {
		name := "audit-t3-renamed-by-client1"
		_, err := repo.UpdateVRF(c1, f.OrgA, global.ID, ipam.UpdateVRFRequest{Name: &name})
		if got := auditT3Str(t, f, `SELECT name FROM vrf WHERE id = $1`, global.ID); !errors.Is(err, ipam.ErrNotFound) || got != global.Name {
			t.Errorf("client-1 UpdateVRF of the default VRF: err=%v name=%q; want ErrNotFound and %q", err, got, global.Name)
		}
	})
	t.Run("DeleteVRF", func(t *testing.T) {
		err := repo.DeleteVRF(c1, f.OrgA, orgVRF.ID)
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf WHERE id = $1`, orgVRF.ID); !errors.Is(err, ipam.ErrNotFound) || n != "1" {
			t.Errorf("client-1 DeleteVRF of an org VRF: err=%v rows left=%s; want ErrNotFound and 1", err, n)
		}
	})

	// Every scoped principal against both VRFs, through the repository and
	// the HTTP handlers; the table must stay as it is.
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	vrfTable := func() string {
		return auditT3Str(t, f, `SELECT string_agg(id::text || '|' || name || '|' || COALESCE(description, '') || '|' || is_default::text,
			',' ORDER BY id) FROM vrf WHERE organization_id = $1`, f.OrgA)
	}
	restricted := func(restrict func(*database.TenantScope)) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		restrict(&scope)
		return database.ContextWithTenantScope(context.Background(), &scope)
	}
	site1 := auditT3Site(t, f, f.Client1, "audit-t3-vrf-site")
	before := vrfTable()
	for _, p := range []struct {
		name string
		ctx  context.Context
	}{
		{"client 1", c1},
		{"site 1", restricted(func(s *database.TenantScope) { s.Sites = database.ScopeIDs(site1) })},
		{"clients 1 and 2", restricted(func(s *database.TenantScope) { s.Clients = database.ScopeIDs(f.Client1, f.Client2) })},
		{"empty client set", restricted(func(s *database.TenantScope) { s.Clients = database.ScopeIDs() })},
	} {
		t.Run("scoped "+p.name, func(t *testing.T) {
			if got, _, listErr := repo.ListVRFs(p.ctx, f.OrgA, ipam.VRFFilter{}, page); listErr != nil || len(got) != 2 {
				t.Errorf("ListVRFs: %d VRFs, %v; want the default and the org VRF", len(got), listErr)
			}
			if _, getErr := repo.GetVRF(p.ctx, f.OrgA, orgVRF.ID); getErr != nil {
				t.Errorf("GetVRF: %v", getErr)
			}
			if createErr := repo.CreateVRF(p.ctx, &ipam.VRF{OrganizationID: f.OrgA, Name: "audit-t3-scoped-" + p.name}); !vrfRLSDenied(createErr) {
				t.Errorf("CreateVRF: %v, want the row level security error 42501", createErr)
			}
			name, description := "audit-t3-renamed", "changed by a scoped principal"
			for _, v := range []*ipam.VRF{global, orgVRF} {
				if _, updErr := repo.UpdateVRF(p.ctx, f.OrgA, v.ID, ipam.UpdateVRFRequest{Name: &name}); !errors.Is(updErr, ipam.ErrNotFound) {
					t.Errorf("UpdateVRF name of %s: %v, want ErrNotFound", v.Name, updErr)
				}
				if _, updErr := repo.UpdateVRF(p.ctx, f.OrgA, v.ID, ipam.UpdateVRFRequest{Description: &description}); !errors.Is(updErr, ipam.ErrNotFound) {
					t.Errorf("UpdateVRF description of %s: %v, want ErrNotFound", v.Name, updErr)
				}
				if delErr := repo.DeleteVRF(p.ctx, f.OrgA, v.ID); !errors.Is(delErr, ipam.ErrNotFound) {
					t.Errorf("DeleteVRF of %s: %v, want ErrNotFound", v.Name, delErr)
				}
			}
			for _, tc := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, "/api/v1/vrfs", "", http.StatusOK},
				{http.MethodGet, "/api/v1/vrfs/" + global.ID, "", http.StatusOK},
				{http.MethodPost, "/api/v1/vrfs", `{"name":"audit-t3-scoped-http"}`, http.StatusForbidden},
				{http.MethodPatch, "/api/v1/vrfs/" + global.ID, `{"name":"audit-t3-renamed-http"}`, http.StatusNotFound},
				{http.MethodPatch, "/api/v1/vrfs/" + orgVRF.ID, `{"description":"x"}`, http.StatusNotFound},
				{http.MethodDelete, "/api/v1/vrfs/" + global.ID, "", http.StatusNotFound},
				{http.MethodDelete, "/api/v1/vrfs/" + orgVRF.ID, "", http.StatusNotFound},
			} {
				if got := call(p.ctx, tc.method, tc.path, tc.body); got != tc.status {
					t.Errorf("%s %s: %d, want %d", tc.method, tc.path, got, tc.status)
				}
			}
			if after := vrfTable(); after != before {
				t.Errorf("the VRF table changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}

	t.Run("org-wide principal", func(t *testing.T) {
		lab := &ipam.VRF{OrganizationID: f.OrgA, Name: "audit-t3-lab"}
		if err := repo.CreateVRF(org, lab); err != nil {
			t.Fatalf("CreateVRF: %v", err)
		}
		name, description := "audit-t3-lab-2", "renamed by the org-wide principal"
		if v, err := repo.UpdateVRF(org, f.OrgA, lab.ID, ipam.UpdateVRFRequest{Name: &name, Description: &description}); err != nil ||
			v.Name != name || v.Description != description {
			t.Errorf("UpdateVRF: %+v, %v", v, err)
		}
		if err := repo.DeleteVRF(org, f.OrgA, lab.ID); err != nil {
			t.Errorf("DeleteVRF: %v", err)
		}
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf WHERE id = $1`, lab.ID); n != "0" {
			t.Errorf("deleted VRF: %s rows left", n)
		}
		var pgErr *pgconn.PgError
		if err := repo.DeleteVRF(org, f.OrgA, global.ID); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("DeleteVRF of the default VRF: %v, want the check violation 23514 of vrf_protect_default", err)
		}

		var created struct {
			ID string `json:"id"`
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/vrfs", strings.NewReader(`{"name":"audit-t3-lab-http"}`))
		req = req.WithContext(tenant.WithTenant(org, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.ID == "" {
			t.Fatalf("POST /api/v1/vrfs: %d %s", w.Code, w.Body.String())
		}
		for _, tc := range []struct {
			method, path, body string
			status             int
		}{
			{http.MethodPatch, "/api/v1/vrfs/" + created.ID, `{"name":"audit-t3-lab-http-2"}`, http.StatusOK},
			{http.MethodDelete, "/api/v1/vrfs/" + created.ID, "", http.StatusNoContent},
			{http.MethodDelete, "/api/v1/vrfs/" + global.ID, "", http.StatusUnprocessableEntity},
		} {
			if got := call(org, tc.method, tc.path, tc.body); got != tc.status {
				t.Errorf("%s %s: %d, want %d", tc.method, tc.path, got, tc.status)
			}
		}
		if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf WHERE id = $1 AND is_default`, global.ID); n != "1" {
			t.Errorf("default VRF: %s rows, want 1", n)
		}
	})
}

// vrfRLSDenied reports whether err is the row level security error 42501.
func vrfRLSDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "row-level security")
}

// TestVRFAttributesOnPostgreSQL covers IPM-07 (WP-907) against PostgreSQL:
//   - route distinguishers in the three forms of RFC 4364 (ASN16:nn32,
//     IPv4:nn16, ASN32:nn16) are accepted, others are 422
//     route_distinguisher; a route distinguisher taken is 409;
//   - route target lists with a duplicate or more than 64 entries are 422
//     import_targets or export_targets;
//   - the default VRF takes no route distinguisher or route targets (422)
//     and still cannot be deleted, but takes sections;
//   - section_ids replaces the sections, ?section_id= filters the VRFs,
//     the section answers vrf_ids; an unknown section is 422 section_ids;
//   - a client principal writes neither attributes nor sections (404) and
//     sees only the sections it can see.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVRFAttributesOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "d7")
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewPGRepository(f.App)).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	orgBCtx := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	decode := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	expect := func(w *httptest.ResponseRecorder, want int, field, what string) {
		t.Helper()
		if w.Code != want || (field != "" && !strings.Contains(w.Body.String(), `"`+field+`"`)) {
			t.Errorf("%s = %d %s, want %d %s", what, w.Code, w.Body.String(), want, field)
		}
	}
	createVRF := func(ctx context.Context, body string) ipam.VRF {
		t.Helper()
		w := call(ctx, http.MethodPost, "/api/v1/vrfs", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /vrfs %s = %d %s", body, w.Code, w.Body.String())
		}
		var v ipam.VRF
		decode(w, &v)
		return v
	}
	createSection := func(ctx context.Context, body string) string {
		t.Helper()
		w := call(ctx, http.MethodPost, "/api/v1/ipam-sections", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /ipam-sections %s = %d %s", body, w.Code, w.Body.String())
		}
		var s ipam.Section
		decode(w, &s)
		return s.ID
	}

	// The three forms of RFC 4364, canonical on output.
	for i, rd := range []struct{ in, want string }{
		{"65000:4294967295", "65000:4294967295"},
		{"192.0.2.1:65535", "192.0.2.1:65535"},
		{"4200000000:007", "4200000000:7"},
	} {
		v := createVRF(orgCtx, `{"name":"d7-rd-`+strconv.Itoa(i)+`","route_distinguisher":"`+rd.in+`"}`)
		if v.RouteDistinguisher != rd.want {
			t.Errorf("route distinguisher %q stored as %q, want %q", rd.in, v.RouteDistinguisher, rd.want)
		}
	}
	for _, rd := range []string{"65000", "0:1", "65000:4294967296", "192.0.2.1:65536", "4200000000:65536", "4294967296:1",
		"2001:db8::1:1", "a:1", "65000:-1", ":1"} {
		expect(call(orgCtx, http.MethodPost, "/api/v1/vrfs", `{"name":"d7-bad","route_distinguisher":"`+rd+`"}`),
			http.StatusUnprocessableEntity, "route_distinguisher", "route distinguisher "+rd)
	}
	expect(call(orgCtx, http.MethodPost, "/api/v1/vrfs", `{"name":"d7-dup","route_distinguisher":"65000:4294967295"}`),
		http.StatusConflict, "route_distinguisher", "duplicate route distinguisher")

	// Route targets.
	vrf := createVRF(orgCtx, `{"name":"d7-cust","route_distinguisher":"65001:1","import_targets":["65001:1","192.0.2.1:1"],"export_targets":["65001:1"]}`)
	if !slices.Equal(vrf.ImportTargets, []string{"65001:1", "192.0.2.1:1"}) || !slices.Equal(vrf.ExportTargets, []string{"65001:1"}) {
		t.Errorf("route targets = %v / %v", vrf.ImportTargets, vrf.ExportTargets)
	}
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"import_targets":["65001:1","65001:01"]}`),
		http.StatusUnprocessableEntity, "import_targets", "duplicate import target")
	many := make([]string, 65)
	for i := range many {
		many[i] = `"65001:` + strconv.Itoa(i) + `"`
	}
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"export_targets":[`+strings.Join(many, ",")+`]}`),
		http.StatusUnprocessableEntity, "export_targets", "65 export targets")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"export_targets":["x"]}`),
		http.StatusUnprocessableEntity, "export_targets", "invalid export target")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"route_distinguisher":"65000:4294967295"}`),
		http.StatusConflict, "route_distinguisher", "PATCH to a taken route distinguisher")
	w := call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"route_distinguisher":"","import_targets":[]}`)
	var got ipam.VRF
	decode(w, &got)
	if w.Code != http.StatusOK || got.RouteDistinguisher != "" || len(got.ImportTargets) != 0 || !slices.Equal(got.ExportTargets, []string{"65001:1"}) {
		t.Errorf("PATCH clears route distinguisher and imports = %d %s", w.Code, w.Body.String())
	}

	// The default VRF keeps no routing attributes and stays undeletable.
	global := auditT3Str(t, f, `SELECT id::text FROM vrf WHERE organization_id = $1 AND is_default`, f.OrgA)
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+global, `{"route_distinguisher":"65002:1"}`),
		http.StatusUnprocessableEntity, "route_distinguisher", "route distinguisher on the default VRF")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+global, `{"import_targets":["65002:1"]}`),
		http.StatusUnprocessableEntity, "import_targets", "import targets on the default VRF")
	expect(call(orgCtx, http.MethodDelete, "/api/v1/vrfs/"+global, ""), http.StatusUnprocessableEntity, "", "DELETE default VRF")

	// Sections: replace, filter, vrf_ids of the section.
	wide := createSection(orgCtx, `{"name":"d7-wide"}`)
	sec2 := createSection(orgCtx, `{"name":"d7-client2","client_id":"`+f.Client2+`"}`)
	w = call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"section_ids":["`+wide+`","`+sec2+`","`+wide+`"]}`)
	got = ipam.VRF{}
	decode(w, &got)
	want := []string{wide, sec2}
	sort.Strings(want)
	if w.Code != http.StatusOK || !slices.Equal(got.SectionIDs, want) {
		t.Errorf("PATCH section_ids = %d %s, want %v", w.Code, w.Body.String(), want)
	}
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+global, `{"section_ids":["`+wide+`"]}`), http.StatusOK, "", "sections of the default VRF")
	w = call(orgCtx, http.MethodGet, "/api/v1/vrfs?section_id="+sec2, "")
	var list struct {
		Data  []ipam.VRF `json:"data"`
		Total int        `json:"total"`
	}
	decode(w, &list)
	if w.Code != http.StatusOK || list.Total != 1 || len(list.Data) != 1 || list.Data[0].ID != vrf.ID {
		t.Errorf("GET /vrfs?section_id=sec2 = %d %s", w.Code, w.Body.String())
	}
	w = call(orgCtx, http.MethodGet, "/api/v1/ipam-sections/"+wide, "")
	var sec ipam.Section
	decode(w, &sec)
	wantVRFs := []string{vrf.ID, global}
	sort.Strings(wantVRFs)
	if !slices.Equal(sec.VRFIDs, wantVRFs) {
		t.Errorf("vrf_ids of the org-wide section = %v, want %v", sec.VRFIDs, wantVRFs)
	}
	secB := createSection(orgBCtx, `{"name":"d7-b"}`)
	for _, id := range []string{secB, f.ID(), "not-a-uuid"} {
		expect(call(orgCtx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"section_ids":["`+id+`"]}`),
			http.StatusUnprocessableEntity, "section_ids", "unknown section "+id)
	}

	// A client principal reads the sections it sees and writes nothing.
	w = call(client1Ctx, http.MethodGet, "/api/v1/vrfs/"+vrf.ID, "")
	got = ipam.VRF{}
	decode(w, &got)
	if w.Code != http.StatusOK || !slices.Equal(got.SectionIDs, []string{wide}) {
		t.Errorf("client GET VRF = %d %s, want only the org-wide section", w.Code, w.Body.String())
	}
	expect(call(client1Ctx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"route_distinguisher":"65003:1"}`),
		http.StatusNotFound, "", "client PATCH route distinguisher")
	expect(call(client1Ctx, http.MethodPatch, "/api/v1/vrfs/"+vrf.ID, `{"section_ids":[]}`), http.StatusNotFound, "", "client PATCH sections")
	if n := auditT3Str(t, f, `SELECT count(*)::text FROM vrf_section WHERE vrf_id = $1`, vrf.ID); n != "2" {
		t.Errorf("sections of the VRF after the client writes: %s, want 2", n)
	}

	// Deleting a section removes its assignments.
	expect(call(orgCtx, http.MethodDelete, "/api/v1/ipam-sections/"+sec2, ""), http.StatusNoContent, "", "DELETE section with a VRF")
	w = call(orgCtx, http.MethodGet, "/api/v1/vrfs/"+vrf.ID, "")
	got = ipam.VRF{}
	decode(w, &got)
	if !slices.Equal(got.SectionIDs, []string{wide}) {
		t.Errorf("sections after deleting section 2 = %v", got.SectionIDs)
	}
}

// TestVRFAttributesWithoutDatabase pins AK5 of WP-907 for the memory
// repository: it keeps the attributes, the sections and the filter.
func TestVRFAttributesWithoutDatabase(t *testing.T) {
	repo := ipam.NewMemoryRepository()
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	w := serve(http.MethodPost, "/api/v1/vrfs", `{"name":"a","route_distinguisher":"65000:1","import_targets":["65000:1"]}`)
	var v ipam.VRF
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != http.StatusCreated || v.RouteDistinguisher != "65000:1" || !slices.Equal(v.ImportTargets, []string{"65000:1"}) {
		t.Fatalf("POST /vrfs = %d %s", w.Code, w.Body.String())
	}
	if w = serve(http.MethodPost, "/api/v1/vrfs", `{"name":"b","route_distinguisher":"65000:1"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate route distinguisher = %d %s, want 409", w.Code, w.Body.String())
	}
	if w = serve(http.MethodPatch, "/api/v1/vrfs/"+v.ID, `{"section_ids":["c0de0000-0000-4000-8000-000000000001"]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown section = %d %s, want 422", w.Code, w.Body.String())
	}
	// The memory section ids are no UUIDs; the API sets them all the same,
	// and filter and vrf_ids follow the assignment.
	sec := &ipam.Section{OrganizationID: "org-1", Name: "s"}
	if err := repo.CreateSection(ctx, sec); err != nil {
		t.Fatal(err)
	}
	if w = serve(http.MethodPatch, "/api/v1/vrfs/"+v.ID, `{"section_ids":["`+sec.ID+`"]}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"`+sec.ID+`"`) {
		t.Fatalf("PATCH section_ids = %d %s, want 200 with the section", w.Code, w.Body.String())
	}
	if w = serve(http.MethodGet, "/api/v1/vrfs?section_id="+sec.ID, ""); !strings.Contains(w.Body.String(), `"total":1`) {
		t.Errorf("GET /vrfs?section_id = %s, want the VRF", w.Body.String())
	}
	if got, err := repo.GetSection(ctx, "org-1", sec.ID); err != nil || !slices.Equal(got.VRFIDs, []string{v.ID}) {
		t.Errorf("vrf_ids of the section = %v, %v", got, err)
	}
	var list struct {
		Data []ipam.VRF `json:"data"`
	}
	w = serve(http.MethodGet, "/api/v1/vrfs", "")
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	for _, item := range list.Data {
		if item.IsDefault {
			if w = serve(http.MethodPatch, "/api/v1/vrfs/"+item.ID, `{"export_targets":["65000:2"]}`); w.Code != http.StatusUnprocessableEntity ||
				!strings.Contains(w.Body.String(), "export_targets") {
				t.Errorf("export targets on the default VRF = %d %s, want 422", w.Code, w.Body.String())
			}
		}
	}
}
