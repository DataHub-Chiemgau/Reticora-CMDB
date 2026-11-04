package topology_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// Field permissions of the topology and impact nodes (RBA-07, E-43,
// WP-727): management_ip is left out of a node whose type and scope the
// policy of the request does not let the principal read it, in the
// topology, the neighbours, the impact (full and compact form, one and
// several CIs), the dependencies and the blast radius; it stays for a
// readable type or scope. The projection reads nothing from the database.

const (
	frOrg      = "org-1"
	frRoleA    = "00000000-0000-4000-8000-0000000007b1"
	frRoleB    = "00000000-0000-4000-8000-0000000007b2"
	frServer   = "server-type"
	frClientA  = "client-a"
	frClientB  = "client-b"
	frHiddenIP = "10.9.0.1"
)

// restrictedNodePolicy is the policy of a principal with ci:read in
// clientA, where management_ip of serverType is none, and ci:read in
// clientB without field grants. A role of the organization grants the field as
// usual elsewhere: the projection is per type and scope.
func restrictedNodePolicy(t *testing.T, serverType, clientA, clientB string) *permission.FieldPolicy {
	t.Helper()
	ctx := context.Background()
	repo := permission.NewMemoryFieldGrants()
	role, typ := frRoleA, serverType
	if _, err := repo.CreateFieldGrant(ctx, frOrg, &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindCI,
		CITypeID: &typ, FieldKey: ptr("core.management_ip"), Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	perms := []identity.Permission{identity.PermCIRead, identity.PermTopologyRead, identity.PermRelationshipRead}
	p, err := permission.NewFieldResolver(nil, nil, repo, nil).PolicyForGrants(ctx, frOrg, []identity.Grant{
		{RoleID: frRoleA, Permissions: perms, Clients: []string{clientA}},
		{RoleID: frRoleB, Permissions: perms, Clients: []string{clientB}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Restricted(permission.KindCI) {
		t.Fatal("policy not restricted")
	}
	return p
}

func ptr(s string) *string { return &s }

// fieldRightsGraph: srvA (server, client a) and srvB (server, client b)
// are powered_by pdu; sw is connected_to srvA.
func fieldRightsGraph(t *testing.T) (http.Handler, map[string]string) {
	t.Helper()
	bg := context.Background()
	cis := ci.NewMemoryRepository()
	rels := relationship.NewMemoryRepository().WithNodeVisibility(ci.VisibleIDs(cis))
	ids := map[string]string{}
	add := func(name, typ, client, ip string) {
		item := &ci.Item{OrganizationID: frOrg, Name: name, CITypeID: typ, ClientID: client, Status: "active", ManagementIP: ip}
		if err := cis.Create(bg, item); err != nil {
			t.Fatal(err)
		}
		ids[name] = item.ID
	}
	add("srvA", frServer, frClientA, frHiddenIP)
	add("srvB", frServer, frClientB, "10.9.0.2")
	add("pdu", "pdu-type", frClientA, "10.9.0.3")
	add("sw", "switch-type", frClientA, "10.9.0.4")
	for _, r := range []relationship.Relationship{
		{SourceCIID: ids["srvA"], TargetCIID: ids["pdu"], RelType: "powered_by"},
		{SourceCIID: ids["srvB"], TargetCIID: ids["pdu"], RelType: "powered_by"},
		{SourceCIID: ids["sw"], TargetCIID: ids["srvA"], RelType: "connected_to"},
	} {
		r.OrganizationID, r.Source = frOrg, "discovery"
		if err := rels.Create(bg, &r); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	topology.NewHandler(cis, rels).RegisterRoutes(mux)
	return mux, ids
}

func getNodes(t *testing.T, mux http.Handler, policy *permission.FieldPolicy, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: frOrg, UserID: "user-1"})
	if policy != nil {
		ctx = permission.WithPolicy(ctx, policy)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	return w.Body.String()
}

// TestNodesProjectManagementIP covers every route that answers nodes.
func TestNodesProjectManagementIP(t *testing.T) {
	mux, ids := fieldRightsGraph(t)
	restricted := restrictedNodePolicy(t, frServer, frClientA, frClientB)
	for _, c := range []struct {
		path    string
		visible []string // management IPs the restricted answer keeps
	}{
		{"/api/v1/topology", []string{"10.9.0.2", "10.9.0.3", "10.9.0.4"}},
		{"/api/v1/topology?root_ci_id=" + ids["pdu"] + "&depth=2", []string{"10.9.0.2", "10.9.0.3", "10.9.0.4"}},
		{"/api/v1/topology/cis/" + ids["pdu"] + "/neighbors", []string{"10.9.0.2", "10.9.0.3"}},
		{"/api/v1/topology/cis/" + ids["pdu"] + "/impact", []string{"10.9.0.2"}},
		{"/api/v1/impact/" + ids["pdu"], []string{"10.9.0.2"}},
		{"/api/v1/impact/" + ids["pdu"] + "?fields=name,management_ip", []string{"10.9.0.2"}},
		{"/api/v1/impact?ci_ids=" + ids["pdu"] + "," + ids["sw"], []string{"10.9.0.2", "10.9.0.3", "10.9.0.4"}},
		{"/api/v1/cis/" + ids["pdu"] + "/dependencies", []string{"10.9.0.2"}},
		{"/api/v1/cis/" + ids["pdu"] + "/blast-radius", []string{"10.9.0.2"}},
	} {
		open := getNodes(t, mux, nil, c.path)
		if !strings.Contains(open, frHiddenIP) {
			t.Errorf("GET %s without grants: management_ip %s missing: %s", c.path, frHiddenIP, open)
		}
		body := getNodes(t, mux, restricted, c.path)
		if strings.Contains(body, frHiddenIP) {
			t.Errorf("GET %s: unreadable management_ip of a client-a server present: %s", c.path, body)
		}
		if !strings.Contains(body, ids["srvA"]) {
			t.Errorf("GET %s: the node of the client-a server is missing: %s", c.path, body)
		}
		for _, ip := range c.visible {
			if !strings.Contains(body, ip) {
				t.Errorf("GET %s: readable management_ip %s missing: %s", c.path, ip, body)
			}
		}
	}
	// A policy that cannot be resolved (a principal without policy
	// source) leaves the field out of every node (fail-closed).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/topology", http.NoBody)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: frOrg, UserID: "user-1"})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: frOrg, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermCIRead, identity.PermTopologyRead}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "management_ip") {
		t.Errorf("topology without policy source: %d %s, want no management_ip", w.Code, w.Body)
	}
}
