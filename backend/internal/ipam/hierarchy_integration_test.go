package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSubnetHierarchyOnPostgreSQL covers IPM-02 (WP-901, E-210) against
// PostgreSQL:
//   - 10.0.0.0/8, 10.1.0.0/16 and 10.1.1.0/24 give the same tree in each of
//     the six orders of creation;
//   - deleting the /16 hangs the /24 under the /8;
//   - a VRF change re-hangs in both VRFs;
//   - two parallel creations in one VRF give consistent parents, a
//     container created while the contained subnet is changed or deleted
//     neither deadlocks nor leaves a wrong parent;
//   - widening a prefix hangs its former children under a subnet that now
//     lies between them;
//   - strict_mode: a true nesting passes without warning; an overlap (a
//     parent that is no pool holds addresses of its own inside the child)
//     is 409 cidr, a child of a pool always passes; without strict_mode
//     the overlap is a warning; a subnet with addresses cannot become a
//     pool (409 is_pool);
//   - a client principal: a child under a pool of another client has an
//     empty parent_id and appears under ?parent_id=root, the tree of the
//     organization stays complete; child_count counts visible children.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubnetHierarchyOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "cb")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client2Ctx := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	newVRF := func(name string) string {
		t.Helper()
		return str(`INSERT INTO vrf (organization_id, name) VALUES ($1, $2) RETURNING id::text`, f.OrgA, name)
	}
	create := func(ctx context.Context, body string, want int) ipam.Subnet {
		t.Helper()
		w := call(ctx, http.MethodPost, "/api/v1/subnets", body)
		if w.Code != want {
			t.Fatalf("POST /subnets %s = %d %s, want %d", body, w.Code, w.Body.String(), want)
		}
		var s ipam.Subnet
		if want == http.StatusCreated {
			if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	// tree returns cidr → parent cidr of the subnets of a VRF (read as
	// admin, so it is the tree of the whole organization).
	tree := func(vrf string) map[string]string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT s.cidr::text, COALESCE(p.cidr::text, '') FROM subnet s
			LEFT JOIN subnet p ON p.id = s.parent_id WHERE s.organization_id = $1 AND s.vrf_id = $2`, f.OrgA, vrf)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var c, p string
			if err := rows.Scan(&c, &p); err != nil {
				t.Fatal(err)
			}
			out[c] = p
		}
		return out
	}
	same := func(what string, got, want map[string]string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: tree %v, want %v", what, got, want)
		}
	}

	// Six orders, one tree.
	cidrs := []string{"10.0.0.0/8", "10.1.0.0/16", "10.1.1.0/24"}
	want := map[string]string{"10.0.0.0/8": "", "10.1.0.0/16": "10.0.0.0/8", "10.1.1.0/24": "10.1.0.0/16"}
	for i, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		vrf := newVRF(fmt.Sprintf("cb-order-%d", i))
		for _, k := range order {
			create(orgCtx, `{"cidr":"`+cidrs[k]+`","vrf_id":"`+vrf+`"}`, http.StatusCreated)
		}
		same(fmt.Sprintf("order %v", order), tree(vrf), want)
	}

	// Deleting the /16 hangs the /24 under the /8.
	vrfA := newVRF("cb-delete")
	s8 := create(orgCtx, `{"cidr":"10.0.0.0/8","vrf_id":"`+vrfA+`"}`, http.StatusCreated)
	s16 := create(orgCtx, `{"cidr":"10.1.0.0/16","vrf_id":"`+vrfA+`"}`, http.StatusCreated)
	s24 := create(orgCtx, `{"cidr":"10.1.1.0/24","vrf_id":"`+vrfA+`"}`, http.StatusCreated)
	if s16.ParentID != s8.ID || s24.ParentID != s16.ID {
		t.Errorf("parents in the answers: /16 %q, /24 %q; want %s, %s", s16.ParentID, s24.ParentID, s8.ID, s16.ID)
	}
	w := call(orgCtx, http.MethodGet, "/api/v1/subnets/"+s8.ID, "")
	var got8 ipam.Subnet
	_ = json.Unmarshal(w.Body.Bytes(), &got8)
	if got8.ChildCount != 1 {
		t.Errorf("child_count of the /8 = %d, want 1", got8.ChildCount)
	}
	w = call(orgCtx, http.MethodGet, "/api/v1/subnets/"+s16.ID+"/children", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), s24.ID) {
		t.Errorf("children of the /16: %d %s", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodDelete, "/api/v1/subnets/"+s16.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /16: %d %s", w.Code, w.Body.String())
	}
	same("after deleting the /16", tree(vrfA), map[string]string{"10.0.0.0/8": "", "10.1.1.0/24": "10.0.0.0/8"})

	// A VRF change re-hangs in both VRFs.
	vrfB := newVRF("cb-move-target")
	create(orgCtx, `{"cidr":"10.0.0.0/12","vrf_id":"`+vrfB+`"}`, http.StatusCreated)
	create(orgCtx, `{"cidr":"10.1.2.0/24","vrf_id":"`+vrfB+`"}`, http.StatusCreated)
	moving := create(orgCtx, `{"cidr":"10.1.0.0/16","vrf_id":"`+vrfA+`"}`, http.StatusCreated)
	same("before the move", tree(vrfA), map[string]string{"10.0.0.0/8": "", "10.1.0.0/16": "10.0.0.0/8", "10.1.1.0/24": "10.1.0.0/16"})
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+moving.ID, `{"vrf_id":"`+vrfB+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH vrf_id: %d %s", w.Code, w.Body.String())
	}
	same("old VRF after the move", tree(vrfA), map[string]string{"10.0.0.0/8": "", "10.1.1.0/24": "10.0.0.0/8"})
	same("new VRF after the move", tree(vrfB), map[string]string{"10.0.0.0/12": "", "10.1.0.0/16": "10.0.0.0/12", "10.1.2.0/24": "10.1.0.0/16"})
	// A changed prefix re-hangs as well: 10.1.16.0/20 no longer contains
	// 10.1.2.0/24, which returns to the /12.
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+moving.ID, `{"cidr":"10.1.16.0/20"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH cidr: %d %s", w.Code, w.Body.String())
	}
	same("after the prefix change", tree(vrfB), map[string]string{"10.0.0.0/12": "", "10.1.16.0/20": "10.0.0.0/12", "10.1.2.0/24": "10.0.0.0/12"})

	// Parallel creations in one VRF.
	for round := 0; round < 5; round++ {
		vrf := newVRF(fmt.Sprintf("cb-parallel-%d", round))
		var wg sync.WaitGroup
		errs := make([]error, len(cidrs))
		for i, c := range cidrs {
			wg.Add(1)
			go func(i int, c string) {
				defer wg.Done()
				errs[i] = repo.CreateSubnet(f.OrgCtx(f.OrgA), &ipam.Subnet{OrganizationID: f.OrgA, CIDR: c, VRFID: vrf})
			}(i, c)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("parallel create: %v", err)
			}
		}
		same(fmt.Sprintf("parallel round %d", round), tree(vrf), want)
	}

	// strict_mode: a true nesting passes in every section; an overlap (a
	// parent that is no pool holds addresses of its own inside the child)
	// is 409 cidr; a child of a pool always passes.
	strict := str(`INSERT INTO ipam_section (organization_id, name, strict_mode) VALUES ($1, 'cb-strict', true) RETURNING id::text`, f.OrgA)
	vrfS := newVRF("cb-strict")
	host := create(orgCtx, `{"cidr":"10.20.0.0/16","vrf_id":"`+vrfS+`","section_id":"`+strict+`"}`, http.StatusCreated)
	nested := create(orgCtx, `{"cidr":"10.20.1.0/24","vrf_id":"`+vrfS+`","section_id":"`+strict+`"}`, http.StatusCreated)
	if nested.ParentID != host.ID || len(nested.Warnings) != 0 {
		t.Errorf("strict true nesting = parent %q warnings %+v, want under %s without warning", nested.ParentID, nested.Warnings, host.ID)
	}
	if w = call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.20.2.5","subnet_id":"`+host.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ips into the host: %d %s", w.Code, w.Body.String())
	}
	w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.20.2.0/24","vrf_id":"`+vrfS+`","section_id":"`+strict+`"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"cidr"`) || !strings.Contains(w.Body.String(), host.ID) {
		t.Errorf("strict nesting over addresses of the host = %d %s, want 409 cidr naming %s", w.Code, w.Body.String(), host.ID)
	}
	if n := str(`SELECT count(*)::text FROM subnet WHERE vrf_id = $1`, vrfS); n != "2" {
		t.Errorf("subnets after the refused overlap = %s, want 2", n)
	}
	// A changed prefix is checked as well: the nested /24 cannot move over
	// the host's address.
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+nested.ID, `{"cidr":"10.20.2.0/24"}`); w.Code != http.StatusConflict {
		t.Errorf("strict prefix change over addresses of the host = %d %s, want 409", w.Code, w.Body.String())
	}
	// A pool takes every child; a subnet with addresses cannot become one.
	pool0 := create(orgCtx, `{"cidr":"10.21.0.0/16","vrf_id":"`+vrfS+`","section_id":"`+strict+`","is_pool":true}`, http.StatusCreated)
	child := create(orgCtx, `{"cidr":"10.21.1.0/24","vrf_id":"`+vrfS+`","section_id":"`+strict+`"}`, http.StatusCreated)
	if child.ParentID != pool0.ID || len(child.Warnings) != 0 {
		t.Errorf("child in a strict pool = %+v", child)
	}
	if w = call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.21.1.5","subnet_id":"`+child.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ips: %d %s", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+child.ID, `{"is_pool":true}`); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"is_pool"`) {
		t.Errorf("subnet with addresses as pool = %d %s, want 409 is_pool", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+host.ID, `{"is_pool":true}`); w.Code != http.StatusConflict {
		t.Errorf("host with addresses as pool = %d %s, want 409", w.Code, w.Body.String())
	}
	// Without strict_mode: a true nesting without warning, an overlap
	// accepted with a warning.
	vrfW := newVRF("cb-warn")
	outer := create(orgCtx, `{"cidr":"10.30.0.0/16","vrf_id":"`+vrfW+`"}`, http.StatusCreated)
	if plain := create(orgCtx, `{"cidr":"10.30.1.0/24","vrf_id":"`+vrfW+`"}`, http.StatusCreated); len(plain.Warnings) != 0 {
		t.Errorf("warnings of a true nesting = %+v, want none", plain.Warnings)
	}
	if w = call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.30.2.7","subnet_id":"`+outer.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ips into the outer subnet: %d %s", w.Code, w.Body.String())
	}
	inner := create(orgCtx, `{"cidr":"10.30.2.0/24","vrf_id":"`+vrfW+`"}`, http.StatusCreated)
	if len(inner.Warnings) != 1 || inner.Warnings[0].Code != "overlap" || inner.Warnings[0].SubnetID != outer.ID {
		t.Errorf("warnings of an overlap = %+v, want overlap with %s", inner.Warnings, outer.ID)
	}

	// Widening a prefix re-hangs its former children under a subnet that
	// now lies between them: 10.1.1.0/24 under 10.1.0.0/16 under
	// 10.0.0.0/10; the /16 becomes 10.0.0.0/9, so the /10 and through it
	// the /24 hang below it.
	vrfG := newVRF("cb-widen")
	create(orgCtx, `{"cidr":"10.0.0.0/8","vrf_id":"`+vrfG+`"}`, http.StatusCreated)
	create(orgCtx, `{"cidr":"10.0.0.0/10","vrf_id":"`+vrfG+`"}`, http.StatusCreated)
	widening := create(orgCtx, `{"cidr":"10.1.0.0/16","vrf_id":"`+vrfG+`"}`, http.StatusCreated)
	create(orgCtx, `{"cidr":"10.1.1.0/24","vrf_id":"`+vrfG+`"}`, http.StatusCreated)
	same("before widening", tree(vrfG), map[string]string{"10.0.0.0/8": "", "10.0.0.0/10": "10.0.0.0/8",
		"10.1.0.0/16": "10.0.0.0/10", "10.1.1.0/24": "10.1.0.0/16"})
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+widening.ID, `{"cidr":"10.0.0.0/9"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH widen: %d %s", w.Code, w.Body.String())
	}
	same("after widening", tree(vrfG), map[string]string{"10.0.0.0/8": "", "10.0.0.0/9": "10.0.0.0/8",
		"10.0.0.0/10": "10.0.0.0/9", "10.1.1.0/24": "10.0.0.0/10"})

	// A container created while the contained subnet is changed or deleted
	// in parallel: no deadlock, a consistent tree.
	for round := 0; round < 5; round++ {
		vrf := newVRF(fmt.Sprintf("cb-race-%d", round))
		x := create(orgCtx, `{"cidr":"10.1.1.0/24","vrf_id":"`+vrf+`"}`, http.StatusCreated)
		y := create(orgCtx, `{"cidr":"10.1.3.0/24","vrf_id":"`+vrf+`"}`, http.StatusCreated)
		moved := "10.1.2.0/24"
		var wg sync.WaitGroup
		errs := make([]error, 3)
		wg.Add(3)
		go func() {
			defer wg.Done()
			errs[0] = repo.CreateSubnet(f.OrgCtx(f.OrgA), &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.1.0.0/16", VRFID: vrf})
		}()
		go func() {
			defer wg.Done()
			_, errs[1] = repo.UpdateSubnet(f.OrgCtx(f.OrgA), f.OrgA, x.ID, &ipam.UpdateSubnetRequest{CIDR: &moved})
		}()
		go func() {
			defer wg.Done()
			errs[2] = repo.DeleteSubnet(f.OrgCtx(f.OrgA), f.OrgA, y.ID)
		}()
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, writer %d: %v", round, i, err)
			}
		}
		same(fmt.Sprintf("race round %d", round), tree(vrf), map[string]string{"10.1.0.0/16": "", "10.1.2.0/24": "10.1.0.0/16"})
	}

	// Client principals under a pool of another client.
	vrfC := newVRF("cb-clients")
	pool := create(orgCtx, `{"cidr":"10.50.0.0/16","vrf_id":"`+vrfC+`","client_id":"`+f.Client2+`","is_pool":true}`, http.StatusCreated)
	c1 := create(orgCtx, `{"cidr":"10.50.1.0/24","vrf_id":"`+vrfC+`","client_id":"`+f.Client1+`"}`, http.StatusCreated)
	c2 := create(orgCtx, `{"cidr":"10.50.2.0/24","vrf_id":"`+vrfC+`","client_id":"`+f.Client2+`"}`, http.StatusCreated)
	same("tree of the organization", tree(vrfC), map[string]string{"10.50.0.0/16": "", "10.50.1.0/24": "10.50.0.0/16", "10.50.2.0/24": "10.50.0.0/16"})
	w = call(client1Ctx, http.MethodGet, "/api/v1/subnets/"+c1.ID, "")
	var seen ipam.Subnet
	_ = json.Unmarshal(w.Body.Bytes(), &seen)
	if w.Code != http.StatusOK || seen.ParentID != "" {
		t.Errorf("client 1 reads its child under a foreign pool: %d parent %q, want an empty parent", w.Code, seen.ParentID)
	}
	w = call(client1Ctx, http.MethodGet, "/api/v1/subnets?parent_id=root&limit=200", "")
	if !strings.Contains(w.Body.String(), c1.ID) || strings.Contains(w.Body.String(), pool.ID) {
		t.Errorf("client 1 ?parent_id=root: %s, want its child and not the foreign pool", w.Body.String())
	}
	if w = call(orgCtx, http.MethodGet, "/api/v1/subnets?parent_id=root&limit=200", ""); strings.Contains(w.Body.String(), c1.ID) {
		t.Errorf("org principal ?parent_id=root lists the child of the pool")
	}
	childCount := func(ctx context.Context) int {
		t.Helper()
		resp := call(ctx, http.MethodGet, "/api/v1/subnets/"+pool.ID, "")
		var s ipam.Subnet
		_ = json.Unmarshal(resp.Body.Bytes(), &s)
		return s.ChildCount
	}
	if n := childCount(orgCtx); n != 2 {
		t.Errorf("child_count of the pool for the organization = %d, want 2", n)
	}
	if n := childCount(client2Ctx); n != 1 {
		t.Errorf("child_count of the pool for client 2 = %d, want 1 (its own child)", n)
	}
	w = call(client2Ctx, http.MethodGet, "/api/v1/subnets?parent_id="+pool.ID, "")
	if !strings.Contains(w.Body.String(), c2.ID) || strings.Contains(w.Body.String(), c1.ID) {
		t.Errorf("client 2 ?parent_id=pool: %s", w.Body.String())
	}
	// parent_id is not writable.
	if w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+c1.ID, `{"parent_id":"`+pool.ID+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH parent_id = %d, want 400", w.Code)
	}
}
