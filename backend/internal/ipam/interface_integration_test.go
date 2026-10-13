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
