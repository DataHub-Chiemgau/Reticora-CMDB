package graphqlbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// The tests of this file cover RBA-07 and GQL-04 in the BFF (WP-727): the
// fields under field permissions of a CI resolve with the policy of the
// request in ci, cis and every nested CI (relationships, topology, rack
// mounts); an unreadable typed field is null, an unreadable or sensitive
// attribute key without grant is left out; the structural fields stay;
// without grants the answer is the one of phase 1; a policy that cannot be
// resolved fails the field (fail-closed).

const (
	frOrg  = "org-1"
	frRole = "00000000-0000-4000-8000-0000000007a1"
)

// frFixture is a hub CI with sensitive values, a leaf related to it and
// mounted in a rack.
type frFixture struct {
	h    *Handler
	hub  string
	leaf string
	rack string
}

func newFRFixture(t *testing.T) *frFixture {
	t.Helper()
	bg := context.Background()
	cis := ci.NewMemoryRepository()
	rels := relationship.NewMemoryRepository()
	add := func(name string) string {
		item := &ci.Item{OrganizationID: frOrg, Name: name, CITypeID: "server", Status: "active", ClientID: "client-1",
			Hostname: name + ".secret.example", FQDN: name + ".fqdn.example", SerialNumber: "SN-" + name, Manufacturer: "ACME",
			Model: "R650", ManagementIP: "10.0.0.1",
			Attributes: map[string]any{"cost_center": "CC-SECRET", "owner": "ops", "pin": "PIN-SECRET"}}
		if err := cis.Create(bg, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	f := &frFixture{hub: add("hub"), leaf: add("leaf")}
	if err := rels.Create(bg, &relationship.Relationship{OrganizationID: frOrg, SourceCIID: f.leaf, TargetCIID: f.hub,
		RelType: "depends_on"}); err != nil {
		t.Fatal(err)
	}
	racks := rack.NewMemoryRepository()
	rk := &rack.Rack{OrganizationID: frOrg, RoomID: "room-1", Name: "R1", HeightUnits: 4}
	if err := racks.CreateRack(bg, rk); err != nil {
		t.Fatal(err)
	}
	if err := racks.CreateMount(bg, &rack.RackMount{OrganizationID: frOrg, RackID: rk.ID, CIID: f.leaf, PositionU: 1,
		HeightU: 1, Face: "front"}); err != nil {
		t.Fatal(err)
	}
	f.rack = rk.ID
	f.h = NewHandler(cis, rels).
		WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureTopology: true, entitlement.FeatureRackView: true}).
		WithSources(&Sources{Topology: topology.NewHandler(cis, rels), Racks: racks})
	return f
}

// restrictedBFFPolicy reads CIs without hostname and attributes.cost_center;
// attributes.pin is sensitive without a grant; serial_number is granted read.
func restrictedBFFPolicy(t *testing.T) *permission.FieldPolicy {
	t.Helper()
	ctx := context.Background()
	repo := permission.NewMemoryFieldGrants()
	role := frRole
	for _, g := range []permission.FieldGrant{
		{RoleID: &role, ObjectKind: permission.KindCI, FieldKey: ptr("core.hostname"), Level: permission.LevelNone},
		{RoleID: &role, ObjectKind: permission.KindCI, FieldKey: ptr("attributes.cost_center"), Level: permission.LevelNone},
		{RoleID: &role, ObjectKind: permission.KindCI, FieldKey: ptr("core.serial_number"), Level: permission.LevelRead},
		{RoleID: &role, ObjectKind: permission.KindCI, FieldKey: ptr("_instance.vault"), Level: permission.LevelNone},
	} {
		if _, err := repo.CreateFieldGrant(ctx, frOrg, &g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.CreateSensitiveField(ctx, frOrg, &permission.SensitiveField{ObjectKind: permission.KindCI,
		FieldKey: "attributes.pin"}); err != nil {
		t.Fatal(err)
	}
	p, err := permission.NewFieldResolver(nil, nil, repo, nil).PolicyForGrants(ctx, frOrg,
		[]identity.Grant{{RoleID: frRole, Permissions: allRead}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Restricted(permission.KindCI) {
		t.Fatal("policy not restricted")
	}
	return p
}

func ptr(s string) *string { return &s }

// runWithPolicy sends query as a session with allRead and the policy in
// the context (nil: no policy source at all).
func runWithPolicy(t *testing.T, h *Handler, policy *permission.FieldPolicy, query string) (gqlResponse, string) {
	t.Helper()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(gql(query)))
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: frOrg, UserID: "user-1"})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: frOrg, Permissions: allRead,
		Type: identity.PrincipalTypeUser})
	if policy != nil {
		ctx = permission.WithPolicy(ctx, policy)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	var out gqlResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return out, w.Body.String()
}

const ciSelection = `{ id name status ciTypeId clientId hostname fqdn serialNumber manufacturer model managementIp attributes }`

// checkProjectedCI checks one CI of the answer under the restricted policy.
func checkProjectedCI(t *testing.T, where string, raw any) {
	t.Helper()
	node, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s: no CI in %v", where, raw)
	}
	if v, present := node["hostname"]; !present || v != nil {
		t.Errorf("%s: hostname %v (present %v), want null", where, v, present)
	}
	for _, field := range []string{"fqdn", "serialNumber", "manufacturer", "model", "managementIp"} {
		if v, _ := node[field].(string); v == "" {
			t.Errorf("%s: readable %s missing: %v", where, field, node[field])
		}
	}
	for _, field := range []string{"id", "name", "status", "ciTypeId", "clientId"} {
		if v, _ := node[field].(string); v == "" {
			t.Errorf("%s: structural %s missing: %v", where, field, node[field])
		}
	}
	attrs, _ := node["attributes"].(map[string]any)
	if attrs["owner"] != "ops" {
		t.Errorf("%s: readable attribute owner missing: %v", where, attrs)
	}
	if _, has := attrs["cost_center"]; has {
		t.Errorf("%s: unreadable attribute cost_center present: %v", where, attrs)
	}
	if _, has := attrs["pin"]; has {
		t.Errorf("%s: sensitive attribute pin without grant present: %v", where, attrs)
	}
}

// TestBFFProjectsCIFields covers ci, cis and the nested CIs of
// relationships, topology and rack mounts under a restricted policy.
func TestBFFProjectsCIFields(t *testing.T) {
	f := newFRFixture(t)
	policy := restrictedBFFPolicy(t)

	resp, body := runWithPolicy(t, f.h, policy, `{ ci(id: "`+f.hub+`") `+ciSelection+` }`)
	if len(resp.Errors) != 0 {
		t.Fatalf("ci: %+v", resp.Errors)
	}
	checkProjectedCI(t, "ci", resp.Data["ci"])
	for _, secret := range []string{"hub.secret.example", "CC-SECRET", "PIN-SECRET"} {
		if strings.Contains(body, secret) {
			t.Errorf("ci answer carries %s: %s", secret, body)
		}
	}

	resp, body = runWithPolicy(t, f.h, policy, `{ cis(first: 10) { nodes `+ciSelection+` edges { node `+ciSelection+` } } }`)
	conn, _ := resp.Data["cis"].(map[string]any)
	nodes, _ := conn["nodes"].([]any)
	edges, _ := conn["edges"].([]any)
	if len(resp.Errors) != 0 || len(nodes) != 2 || len(edges) != 2 {
		t.Fatalf("cis: %s", body)
	}
	for i := range nodes {
		checkProjectedCI(t, "cis.nodes", nodes[i])
		checkProjectedCI(t, "cis.edges.node", edges[i].(map[string]any)["node"])
	}

	resp, body = runWithPolicy(t, f.h, policy, `{ relationships(ciId: "`+f.hub+`") { sourceCi `+ciSelection+` targetCi `+ciSelection+` } }`)
	rels, _ := resp.Data["relationships"].([]any)
	if len(resp.Errors) != 0 || len(rels) != 1 {
		t.Fatalf("relationships: %s", body)
	}
	checkProjectedCI(t, "relationships.sourceCi", rels[0].(map[string]any)["sourceCi"])
	checkProjectedCI(t, "relationships.targetCi", rels[0].(map[string]any)["targetCi"])

	resp, body = runWithPolicy(t, f.h, policy, `{ topology(rootCiId: "`+f.hub+`", depth: 1) { nodes { id ci `+ciSelection+` } } }`)
	graph, _ := resp.Data["topology"].(map[string]any)
	tnodes, _ := graph["nodes"].([]any)
	if len(resp.Errors) != 0 || len(tnodes) != 2 {
		t.Fatalf("topology: %s", body)
	}
	for _, n := range tnodes {
		checkProjectedCI(t, "topology.nodes.ci", n.(map[string]any)["ci"])
	}

	resp, body = runWithPolicy(t, f.h, policy, `{ rackLayout(rackId: "`+f.rack+`") { mounts { ci `+ciSelection+` } } }`)
	layout, _ := resp.Data["rackLayout"].(map[string]any)
	mounts, _ := layout["mounts"].([]any)
	if len(resp.Errors) != 0 || len(mounts) != 1 {
		t.Fatalf("rackLayout: %s", body)
	}
	checkProjectedCI(t, "rackLayout.mounts.ci", mounts[0].(map[string]any)["ci"])
}

// TestBFFWithoutGrantsAnswersAsBefore: an organization without field
// permissions answers every field as in phase 1, empty typed fields as ""
// (not null).
func TestBFFWithoutGrantsAnswersAsBefore(t *testing.T) {
	f := newFRFixture(t)
	resp, body := runWithPolicy(t, f.h, permission.UnrestrictedPolicy(allRead),
		`{ ci(id: "`+f.hub+`") { hostname serialNumber attributes } }`)
	node, _ := resp.Data["ci"].(map[string]any)
	attrs, _ := node["attributes"].(map[string]any)
	if len(resp.Errors) != 0 || node["hostname"] != "hub.secret.example" || node["serialNumber"] != "SN-hub" ||
		attrs["cost_center"] != "CC-SECRET" || attrs["pin"] != "PIN-SECRET" {
		t.Errorf("unrestricted ci: %s", body)
	}
	h, id := newFixture(t)
	_, resp = run(t, h, []identity.Permission{identity.PermCIRead}, gql(`{ ci(id: "`+id+`") { fqdn } }`))
	if node, _ := resp.Data["ci"].(map[string]any); len(resp.Errors) != 0 || node["fqdn"] != "" {
		t.Errorf("empty fqdn without grants: %+v, want \"\"", resp)
	}
}

// TestBFFFieldsFailClosed: a session principal without policy source gets
// no value of a field under field permissions, only an error; structural
// fields still resolve.
func TestBFFFieldsFailClosed(t *testing.T) {
	f := newFRFixture(t)
	resp, body := runWithPolicy(t, f.h, nil, `{ ci(id: "`+f.hub+`") { name hostname attributes } }`)
	if len(resp.Errors) == 0 || resp.code() != codeInternal {
		t.Errorf("without policy source: %s, want %s", body, codeInternal)
	}
	for _, secret := range []string{"hub.secret.example", "CC-SECRET", "PIN-SECRET"} {
		if strings.Contains(body, secret) {
			t.Errorf("answer without policy source carries %s: %s", secret, body)
		}
	}
}

// TestBFFInstanceAttributes: an instance attribute is checked as
// _instance.<key> (ci.Item.InstanceKeys), not as attributes.<key>.
func TestBFFInstanceAttributes(t *testing.T) {
	policy := restrictedBFFPolicy(t)
	ctx := permission.WithPolicy(context.Background(), policy)
	r := &ciResolver{&Resolver{}}
	item := &ci.Item{ID: "ci-1", CITypeID: "server", Attributes: map[string]any{"vault": "V-SECRET", "owner": "ops"},
		InstanceKeys: []string{"vault"}}
	attrs, err := r.Attributes(ctx, item)
	if err != nil || attrs["owner"] != "ops" {
		t.Fatalf("attributes: %v, %v", attrs, err)
	}
	if _, has := attrs["vault"]; has {
		t.Errorf("unreadable instance attribute vault present: %v", attrs)
	}
	// The same key as a type attribute is readable.
	item.InstanceKeys = nil
	if attrs, _ = r.Attributes(ctx, item); attrs["vault"] != "V-SECRET" {
		t.Errorf("type attribute vault: %v", attrs)
	}
}
