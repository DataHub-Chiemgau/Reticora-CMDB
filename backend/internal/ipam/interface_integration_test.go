package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestInterfaceModelFollowsNET01 covers NET-01 (WP-123) against
// PostgreSQL: network_interface has the columns, names, constraints and
// indexes of NET-01; the API stores and returns every field under its
// NET-01 name; the name is unique per CI (409 with the field), not across
// CIs.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInterfaceModelFollowsNET01(t *testing.T) {
	f := scopetest.Seed(t, "8d")
	bg := context.Background()

	// The schema contract.
	columns := map[string]string{}
	rows, err := f.Admin.Query(bg, `SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'network_interface'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, typ string
		if err = rows.Scan(&name, &typ); err != nil {
			t.Fatal(err)
		}
		columns[name] = typ
	}
	rows.Close()
	for name, typ := range map[string]string{
		"ci_id": "uuid", "name": "text", "if_index": "integer", "mac": "macaddr", "speed_mbps": "integer", "mtu": "integer",
		"admin_status": "text", "oper_status": "text", "if_type": "text", "description": "text", "vlan_id": "uuid",
		"is_management": "boolean", "miss_count": "integer",
	} {
		if columns[name] != typ {
			t.Errorf("column %s: %q, want %s", name, columns[name], typ)
		}
	}
	for _, former := range []string{"mac_address", "interface_type"} {
		if _, ok := columns[former]; ok {
			t.Errorf("former column %s still exists", former)
		}
	}
	var contract []string
	if err = f.Admin.QueryRow(bg, `SELECT array_agg(def ORDER BY def) FROM (
			SELECT pg_get_constraintdef(oid) AS def FROM pg_constraint
			WHERE conrelid = 'network_interface'::regclass AND contype IN ('u', 'f')
			UNION ALL SELECT indexdef FROM pg_indexes WHERE tablename = 'network_interface') d`).Scan(&contract); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(contract, "\n")
	for _, want := range []string{
		"UNIQUE (ci_id, name)",
		"FOREIGN KEY (vlan_id, organization_id) REFERENCES vlan(id, organization_id)",
		"USING btree (organization_id, mac)",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}

	// The API under the NET-01 names.
	var vlanID string
	if err = f.Admin.QueryRow(bg, `INSERT INTO vlan (organization_id, vlan_id, name) VALUES ($1, 120, 'net01') RETURNING id::text`,
		f.OrgA).Scan(&vlanID); err != nil {
		t.Fatal(err)
	}
	ciA := f.CI(t, f.OrgA, "", "net01-switch-a")
	ciB := f.CI(t, f.OrgA, "", "net01-switch-b")
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	declared := map[string]any{
		"name": "Gi1/0/1", "if_index": 10101.0, "mac": "00:11:22:33:44:8d", "speed_mbps": 1000.0, "mtu": 9000.0,
		"admin_status": "up", "oper_status": "down", "if_type": "fiber", "description": "uplink core",
		"vlan_id": vlanID, "is_management": true,
	}
	raw, _ := json.Marshal(declared)
	w := call(http.MethodPost, "/api/v1/cis/"+ciA+"/interfaces", string(raw))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	w = call(http.MethodGet, "/api/v1/network-interfaces/"+created["id"].(string), "")
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	for key, want := range declared {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if got["miss_count"] != 0.0 || got["ci_id"] != ciA {
		t.Errorf("read back: %v", got)
	}

	// Unique per CI, not across CIs.
	if w = call(http.MethodPost, "/api/v1/cis/"+ciA+"/interfaces", `{"name":"Gi1/0/1"}`); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"field":"name"`) {
		t.Errorf("duplicate name on the CI: %d %s, want 409 with the field", w.Code, w.Body.String())
	}
	if w = call(http.MethodPost, "/api/v1/cis/"+ciB+"/interfaces", `{"name":"Gi1/0/1"}`); w.Code != http.StatusCreated {
		t.Errorf("same name on another CI: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPost, "/api/v1/cis/"+ciA+"/interfaces", `{"name":"Gi1/0/2","mtu":1500}`)
	var second map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &second)
	if w = call(http.MethodPatch, "/api/v1/network-interfaces/"+second["id"].(string), `{"name":"Gi1/0/1"}`); w.Code != http.StatusConflict {
		t.Errorf("rename onto a taken name: %d %s, want 409", w.Code, w.Body.String())
	}
	if w = call(http.MethodPatch, "/api/v1/network-interfaces/"+second["id"].(string), `{"if_index":10102,"mtu":9216,"mac":"00:11:22:33:44:8e"}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"if_index":10102`) || !strings.Contains(w.Body.String(), `"mtu":9216`) ||
		!strings.Contains(w.Body.String(), `"mac":"00:11:22:33:44:8e"`) {
		t.Errorf("patch NET-01 fields: %d %s", w.Code, w.Body.String())
	}
}

// ifaceAPI runs the IPAM routes on PostgreSQL for one principal: ctx carries
// its tenant scope and user.
type ifaceAPI struct {
	mux *chi.Mux
	ctx context.Context
}

func newIfaceAPI(f *scopetest.Fixture, scoped context.Context, orgID string) ifaceAPI {
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewPGRepository(f.App)).RegisterRoutes(mux)
	return ifaceAPI{mux: mux, ctx: tenant.WithTenant(scoped, tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})}
}

func (a ifaceAPI) call(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(a.ctx)
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

// TestInterfaceDeleteKeepsManualEdges covers DELETE /network-interfaces/{id}
// after WP-424 (REL-05, REL-02, NET-01; F-042) against PostgreSQL: an
// interface that a manual edge (source or target end), a verified discovery
// edge or an edge of another source names is refused with 409 naming the
// edges, and nothing changes; without edge the interface is deleted (204);
// with only an unverified discovery edge the edge goes with it and is
// recorded as ci_change relationship_change of the interface's CI with the
// edge as old_value (AUD-03), 204. An edge outside the caller's scope keeps
// the interface without being named (foreign key, 000159). An unknown, a
// malformed or another organization's id is 404.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInterfaceDeleteKeepsManualEdges(t *testing.T) {
	f := scopetest.Seed(t, "8f")
	bg := context.Background()
	sw1 := f.CI(t, f.OrgA, f.Client1, "rel05-sw1")
	sw2 := f.CI(t, f.OrgA, f.Client1, "rel05-sw2")
	foreign := f.CI(t, f.OrgA, f.Client2, "rel05-c2")
	core := f.CI(t, f.OrgA, "", "rel05-core")
	port := func(ciID, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, ciID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	edge := func(src, dst, source, state, srcPort, dstPort string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, source_interface_id, target_interface_id)
			VALUES ($1, $2, $3, 'connected_to', $4, $5, NULLIF($6, '')::uuid, NULLIF($7, '')::uuid) RETURNING id::text`,
			f.OrgA, src, dst, source, state, srcPort, dstPort).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	exists := func(table, id string) bool {
		t.Helper()
		return count(`SELECT count(*) FROM `+table+` WHERE id = $1`, id) == 1
	}
	history := func(ciID string) int {
		t.Helper()
		return count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'relationship_change'`, ciID)
	}
	org := newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA)
	refused := func(a ifaceAPI, nic string, want ...string) {
		t.Helper()
		w := a.call(http.MethodDelete, "/api/v1/network-interfaces/"+nic, "")
		var problem struct {
			Status          int      `json:"status"`
			Detail          string   `json:"detail"`
			RelationshipIDs []string `json:"relationship_ids"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusConflict || problem.Status != http.StatusConflict || problem.RelationshipIDs == nil {
			t.Fatalf("DELETE interface %s: %d %s, want 409 with relationship_ids", nic, w.Code, w.Body.String())
		}
		if strings.Join(problem.RelationshipIDs, ",") != strings.Join(want, ",") {
			t.Errorf("DELETE interface %s: relationship_ids %v, want %v", nic, problem.RelationshipIDs, want)
		}
		for _, id := range want {
			if !strings.Contains(problem.Detail, id) {
				t.Errorf("DELETE interface %s: detail %q does not name edge %s", nic, problem.Detail, id)
			}
		}
		if !exists("network_interface", nic) {
			t.Errorf("refused delete removed interface %s", nic)
		}
	}

	// A manual edge at the source or the target end: 409, nothing changes.
	srcPort, dstPort := port(sw1, "Gi1/0/1"), port(sw2, "Gi1/0/1")
	manual := edge(sw1, sw2, "manual", "unverified", srcPort, dstPort)
	refused(org, srcPort, manual)
	refused(org, dstPort, manual)
	if !exists("ci_relationship", manual) {
		t.Fatal("manual edge removed by a refused interface delete")
	}

	// A discovery edge beside the manual one stays as well: the delete
	// rolls back as a whole.
	mixedPort := port(sw1, "Gi1/0/2")
	kept := edge(sw1, sw2, "manual", "unverified", mixedPort, "")
	discoveredBeside := edge(sw1, core, "discovery", "unverified", mixedPort, "")
	refused(org, mixedPort, kept)
	if !exists("ci_relationship", discoveredBeside) || history(sw1) != 0 {
		t.Errorf("refused delete changed the discovery edge %s or the history (%d rows)", discoveredBeside, history(sw1))
	}

	// A verified discovery edge and an edge of another source keep the
	// interface, too.
	verifiedPort, importPort := port(sw1, "Gi1/0/3"), port(sw1, "Gi1/0/4")
	verified := edge(sw1, core, "discovery", "verified", verifiedPort, "")
	imported := edge(sw1, core, "import", "unverified", importPort, "")
	refused(org, verifiedPort, verified)
	refused(org, importPort, imported)

	// Without an edge: 204.
	free := port(sw1, "Gi1/0/5")
	if w := org.call(http.MethodDelete, "/api/v1/network-interfaces/"+free, ""); w.Code != http.StatusNoContent || exists("network_interface", free) {
		t.Errorf("DELETE interface without edge: %d %s", w.Code, w.Body.String())
	}

	// Only an unverified discovery edge: removed with the interface and
	// recorded in the history of the interface's CI.
	discoveredPort := port(sw1, "Gi1/0/6")
	discovered := edge(core, sw1, "discovery", "unverified", "", discoveredPort)
	if w := org.call(http.MethodDelete, "/api/v1/network-interfaces/"+discoveredPort, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE interface with a discovery edge: %d %s, want 204", w.Code, w.Body.String())
	}
	if exists("network_interface", discoveredPort) || exists("ci_relationship", discovered) {
		t.Error("interface or discovery edge still present after 204")
	}
	var oldValue, actor, comment string
	var newValueNull bool
	if err := f.Admin.QueryRow(bg, `SELECT old_value::text, COALESCE(actor_id::text, ''), COALESCE(comment, ''), new_value IS NULL
		FROM ci_change WHERE ci_id = $1 AND change_type = 'relationship_change' AND field_name = 'relationships'
		  AND old_value->>'id' = $2`, sw1, discovered).Scan(&oldValue, &actor, &comment, &newValueNull); err != nil {
		t.Fatalf("history of the removed discovery edge: %v", err)
	}
	for _, want := range []string{`"source": "discovery"`, `"rel_type": "connected_to"`, `"source_ci_id": "` + core + `"`,
		`"target_ci_id": "` + sw1 + `"`, `"target_interface_id": "` + discoveredPort + `"`} {
		if !strings.Contains(oldValue, want) {
			t.Errorf("old_value %s lacks %s", oldValue, want)
		}
	}
	if !newValueNull || actor != f.User || comment != "removed with interface Gi1/0/6" || history(sw1) != 1 || history(core) != 0 {
		t.Errorf("history: new_value NULL %v, actor %q, comment %q, rows sw1 %d core %d", newValueNull, actor, comment, history(sw1), history(core))
	}

	// A client-1 principal: an edge to a client-2 CI it cannot see keeps
	// the interface unnamed; a discovery edge to an org-wide CI it sees goes.
	client1 := newIfaceAPI(f, f.ClientCtx(f.Client1), f.OrgA)
	hiddenPort := port(sw2, "Gi1/0/7")
	hidden := edge(sw2, foreign, "discovery", "unverified", hiddenPort, "")
	refused(client1, hiddenPort)
	if !exists("ci_relationship", hidden) {
		t.Error("client-1 principal removed an edge to a client-2 CI")
	}
	visiblePort := port(sw2, "Gi1/0/8")
	visible := edge(sw2, core, "discovery", "unverified", visiblePort, "")
	if w := client1.call(http.MethodDelete, "/api/v1/network-interfaces/"+visiblePort, ""); w.Code != http.StatusNoContent ||
		exists("ci_relationship", visible) || history(sw2) != 1 {
		t.Errorf("client-1 DELETE interface with a visible discovery edge: %d %s, history %d", w.Code, w.Body.String(), history(sw2))
	}

	// Not found: unknown, malformed, another organization's interface.
	var other string
	otherCI := f.CI(t, f.OrgB, "", "rel05-b")
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`,
		f.OrgB, otherCI).Scan(&other); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.ID(), "not-a-uuid", other} {
		if w := org.call(http.MethodDelete, "/api/v1/network-interfaces/"+id, ""); w.Code != http.StatusNotFound {
			t.Errorf("DELETE interface %s: %d %s, want 404", id, w.Code, w.Body.String())
		}
	}
	if !exists("network_interface", other) {
		t.Error("organization A deleted an interface of organization B")
	}
}

// TestInterfaceDeleteWithUnboundTwinAddress covers F-312 (WP-424, NET-01):
// an interface whose address is also present unbound in the same VRF is
// deleted (204, never 404); the bound address row goes with it, the unbound
// one stays. An address without such a twin is unbound as before.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInterfaceDeleteWithUnboundTwinAddress(t *testing.T) {
	f := scopetest.Seed(t, "90")
	bg := context.Background()
	host := f.CI(t, f.OrgA, "", "f312-host")
	org := newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA)
	nic := func(name string) string {
		t.Helper()
		w := org.call(http.MethodPost, "/api/v1/cis/"+host+"/interfaces", `{"name":"`+name+`"}`)
		var created map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &created)
		id, _ := created["id"].(string)
		if w.Code != http.StatusCreated || id == "" {
			t.Fatalf("create interface %s: %d %s", name, w.Code, w.Body.String())
		}
		return id
	}
	address := func(interfaceID, addr string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ip_address (organization_id, interface_id, address) VALUES ($1, NULLIF($2, '')::uuid, $3::inet)
			RETURNING id::text`, f.OrgA, interfaceID, addr).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	eth0 := nic("eth0")
	boundRow := address(eth0, "10.66.0.1")
	unboundRow := address("", "10.66.0.1")
	if w := org.call(http.MethodDelete, "/api/v1/network-interfaces/"+eth0, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE eth0 with an unbound twin of its address: %d %s, want 204", w.Code, w.Body.String())
	}
	if w := org.call(http.MethodGet, "/api/v1/network-interfaces/"+eth0, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET eth0 after the delete: %d", w.Code)
	}
	var left []string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(id::text), '{}') FROM ip_address
		WHERE organization_id = $1 AND host(address) = '10.66.0.1'`, f.OrgA).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0] != unboundRow {
		t.Errorf("rows of 10.66.0.1: %v, want only the unbound %s (bound %s deleted)", left, unboundRow, boundRow)
	}

	// Without a twin the address stays, unbound.
	eth1 := nic("eth1")
	single := address(eth1, "10.66.0.2")
	if w := org.call(http.MethodDelete, "/api/v1/network-interfaces/"+eth1, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE eth1: %d %s", w.Code, w.Body.String())
	}
	var unbound bool
	if err := f.Admin.QueryRow(bg, `SELECT interface_id IS NULL FROM ip_address WHERE id = $1`, single).Scan(&unbound); err != nil || !unbound {
		t.Errorf("address of eth1 after the delete: unbound %v, %v", unbound, err)
	}
}

// TestInterfaceCreateOnForeignCI covers F-311 (WP-424, NET-01, TEN-05):
// POST /cis/{id}/interfaces on a CI of another organization is 404, not
// 500, like on an unknown CI, and stores no row; on an own CI it is 201.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInterfaceCreateOnForeignCI(t *testing.T) {
	f := scopetest.Seed(t, "91")
	ciA := f.CI(t, f.OrgA, "", "f311-a")
	ciB := f.CI(t, f.OrgB, "", "f311-b")
	orgB := newIfaceAPI(f, f.OrgCtx(f.OrgB), f.OrgB)
	count := func(ciID string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM network_interface WHERE ci_id = $1`, ciID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// A foreign CI answers like an unknown one (no oracle).
	for _, target := range []string{ciA, f.ID()} {
		for _, body := range []string{`{"name":"eth0"}`, `{"name":"eth0","mac":"00:11:22:33:44:91","mtu":1500}`} {
			w := orgB.call(http.MethodPost, "/api/v1/cis/"+target+"/interfaces", body)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "CI not found") {
				t.Errorf("organization B POST interface %s on CI %s: %d %s, want 404", body, target, w.Code, w.Body.String())
			}
		}
	}
	if n := count(ciA); n != 0 {
		t.Errorf("organization A's CI has %d interfaces after the refused writes", n)
	}
	if w := orgB.call(http.MethodPost, "/api/v1/cis/"+ciB+"/interfaces", `{"name":"eth0"}`); w.Code != http.StatusCreated || count(ciB) != 1 {
		t.Errorf("organization B POST interface on its own CI: %d %s, want 201", w.Code, w.Body.String())
	}
}
