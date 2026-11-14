package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSearchFindsSubnetsAndAddresses covers the network hits of IPM-20
// (WP-909) against PostgreSQL: subnets and IP addresses are documents of
// search_document as soon as they are written (triggers of migration
// 000303), found by CIDR, name, address and DNS name, with the urls of the
// IPAM pages and vrf_id, section_id and subnet_id in metadata. A client
// principal finds only the subnets and addresses of its client; without
// ipam:read there are no network hits. Deleting removes the document, a
// client change of the subnet changes the scope of its document, a section
// change of the subnet the section_id of its addresses' documents, and a
// client change of the CI behind an address's interface the scope of the
// address's document; the reindex writes the same documents.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchFindsSubnetsAndAddresses(t *testing.T) {
	f := scopetest.Seed(t, "bb")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ipamRepo := ipam.NewPGRepository(f.App)
	idx := search.NewPGRepository(f.App)

	// search runs GET /api/v1/search as a principal of scope with or
	// without ipam:read and returns the hits by entity id.
	search := func(scope database.TenantScope, ipamRead bool, text string) map[string]search.Hit {
		t.Helper()
		mux := chi.NewRouter()
		granted := map[string]bool{"ci:read": true, "ipam:read": ipamRead}
		search.NewHandler(idx, permissionStub{granted: granted}).WithGates(allFeatures{}, allModules).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+url.Values{"q": {text}, "limit": {"100"}}.Encode(), nil)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		ctx = permission.WithPolicy(database.ContextWithTenantScope(ctx, &scope), nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(ctx))
		var body struct {
			Data []search.Hit `json:"data"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("GET /api/v1/search?q=%s: %d %s", text, rec.Code, rec.Body.String())
		}
		out := map[string]search.Hit{}
		for _, h := range body.Data {
			out[h.EntityID] = h
		}
		return out
	}
	org := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	client2 := database.OrgWideScope(f.OrgA, f.User)
	client2.Clients = database.ScopeIDs(f.Client2)

	subnet := &ipam.Subnet{OrganizationID: f.OrgA, ClientID: f.Client1, CIDR: "10.77.0.0/24", Name: "Lagerhalle Traunreut",
		Description: "Scanner und Drucker"}
	if err := ipamRepo.CreateSubnet(orgCtx, subnet); err != nil {
		t.Fatal(err)
	}
	address := &ipam.IPAddress{OrganizationID: f.OrgA, SubnetID: subnet.ID, Address: "10.77.0.25", Type: ipam.IPTypeStatic,
		DNSName: "etikettendrucker.example.test"}
	if err := ipamRepo.CreateIPAddress(orgCtx, address); err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{"10.77.0.0/24", "Traunreut"} {
		hit, ok := search(org, true, text)[subnet.ID]
		if !ok || hit.EntityType != "subnet" || hit.URL != "/ipam/subnets/"+subnet.ID ||
			hit.Metadata["vrf_id"] != subnet.VRFID || hit.Metadata["section_id"] != subnet.SectionID {
			t.Errorf("search %q: subnet hit %+v (found %v)", text, hit, ok)
		}
	}
	for _, text := range []string{"10.77.0.25", "etikettendrucker.example.test"} {
		hit, ok := search(org, true, text)[address.ID]
		if !ok || hit.EntityType != "ip_address" || hit.URL != "/ipam/subnets/"+subnet.ID+"?address="+address.ID ||
			hit.Metadata["subnet_id"] != subnet.ID || hit.Metadata["section_id"] != subnet.SectionID {
			t.Errorf("search %q: address hit %+v (found %v)", text, hit, ok)
		}
	}

	// Scope and permission.
	if _, ok := search(client1, true, "Traunreut")[subnet.ID]; !ok {
		t.Error("client 1 does not find the subnet of its client")
	}
	if hits := search(client2, true, "Traunreut"); len(hits) != 0 {
		t.Errorf("client 2 finds the subnet of client 1: %+v", hits)
	}
	if hits := search(client2, true, "etikettendrucker.example.test"); len(hits) != 0 {
		t.Errorf("client 2 finds the address of client 1: %+v", hits)
	}
	if hits := search(org, false, "Traunreut"); len(hits) != 0 {
		t.Errorf("without ipam:read: %+v", hits)
	}

	// A client change moves the scope of the document.
	other := f.Client2
	if _, err := ipamRepo.UpdateSubnet(orgCtx, f.OrgA, subnet.ID, &ipam.UpdateSubnetRequest{ClientID: &other}); err != nil {
		t.Fatal(err)
	}
	var docClient string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM search_document WHERE entity_type = 'subnet' AND entity_id = $1`,
		subnet.ID).Scan(&docClient); err != nil || docClient != f.Client2 {
		t.Errorf("document client after the move = %q (%v), want client 2", docClient, err)
	}
	if _, ok := search(client2, true, "Traunreut")[subnet.ID]; !ok {
		t.Error("client 2 does not find the moved subnet")
	}
	if hits := search(client1, true, "Traunreut"); len(hits) != 0 {
		t.Errorf("client 1 still finds the moved subnet: %+v", hits)
	}

	// A section change of the subnet carries the section into the metadata
	// of its addresses' documents.
	section := &ipam.Section{OrganizationID: f.OrgA, Name: "search-section"}
	if err := ipamRepo.CreateSection(orgCtx, section); err != nil {
		t.Fatal(err)
	}
	if _, err := ipamRepo.UpdateSubnet(orgCtx, f.OrgA, subnet.ID, &ipam.UpdateSubnetRequest{SectionID: &section.ID}); err != nil {
		t.Fatal(err)
	}
	if hit, ok := search(org, true, "etikettendrucker.example.test")[address.ID]; !ok || hit.Metadata["section_id"] != section.ID {
		t.Errorf("address hit after the section change of its subnet: %+v (found %v), want section %s", hit, ok, section.ID)
	}

	// The client of an address follows its interface (derive_ip_scope,
	// 000061): moving the CI to another client moves the document, though
	// no write names client_id of the address (000141 sets interface_id).
	ci := f.CI(t, f.OrgA, f.Client1, "search-nic-ci")
	var nic string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`,
		f.OrgA, ci).Scan(&nic); err != nil {
		t.Fatal(err)
	}
	bound := &ipam.IPAddress{OrganizationID: f.OrgA, InterfaceID: nic, Address: "10.78.0.9", Type: ipam.IPTypeStatic,
		DNSName: "waage-halle3.example.test"}
	if err := ipamRepo.CreateIPAddress(orgCtx, bound); err != nil {
		t.Fatal(err)
	}
	if _, ok := search(client1, true, "waage-halle3.example.test")[bound.ID]; !ok {
		t.Error("client 1 does not find the address on the interface of its CI")
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $1 WHERE id = $2`, f.Client2, ci); err != nil {
		t.Fatal(err)
	}
	if hits := search(client1, true, "waage-halle3.example.test"); len(hits) != 0 {
		t.Errorf("client 1 still finds the address after its CI moved to client 2: %+v", hits)
	}
	if _, ok := search(client2, true, "waage-halle3.example.test")[bound.ID]; !ok {
		t.Error("client 2 does not find the address after its CI moved to it")
	}

	// The reindex writes the same documents.
	if _, err := idx.ReindexTenant(orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if hit, ok := search(org, true, "etikettendrucker.example.test")[address.ID]; !ok || hit.URL != "/ipam/subnets/"+subnet.ID+"?address="+address.ID {
		t.Errorf("address hit after the reindex: %+v (found %v)", hit, ok)
	}

	// Deleting removes the documents.
	if err := ipamRepo.DeleteIPAddress(orgCtx, f.OrgA, address.ID); err != nil {
		t.Fatal(err)
	}
	if err := ipamRepo.DeleteSubnet(orgCtx, f.OrgA, subnet.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM search_document WHERE entity_id = ANY($1::uuid[])`,
		[]string{subnet.ID, address.ID}).Scan(&left); err != nil || left != 0 {
		t.Errorf("documents after the deletes: %d (%v)", left, err)
	}
}
