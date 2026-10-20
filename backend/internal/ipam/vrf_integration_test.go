package ipam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

	vrfs, _, err := repo.ListVRFs(ctx, f.OrgA, page)
	if err != nil {
		t.Fatal(err)
	}
	if len(vrfs) != 1 || vrfs[0].Name != "global" || !vrfs[0].IsDefault {
		t.Fatalf("VRFs of a new organization: %+v, want only the default global", vrfs)
	}
	global := vrfs[0]
	if bVRFs, _, _ := repo.ListVRFs(f.OrgCtx(f.OrgB), f.OrgB, page); len(bVRFs) != 1 || bVRFs[0].ID == global.ID {
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
	if _, err = repo.UpdateSubnet(ctx, f.OrgA, defaultSubnet.ID, ipam.UpdateSubnetRequest{VRFID: &third.ID}); err != nil {
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
	vrfs, _, err := repo.ListVRFs(c1, f.OrgA, page)
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
			if got, _, listErr := repo.ListVRFs(p.ctx, f.OrgA, page); listErr != nil || len(got) != 2 {
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
